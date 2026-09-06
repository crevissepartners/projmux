package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

type lifecycleTestDuplex struct {
	reader *bytes.Reader
	writes bytes.Buffer
}

func (d *lifecycleTestDuplex) Read(p []byte) (int, error)  { return d.reader.Read(p) }
func (d *lifecycleTestDuplex) Write(p []byte) (int, error) { return d.writes.Write(p) }
func (*lifecycleTestDuplex) Close() error                  { return nil }

type lifecycleEOFInput struct {
	chunks [][]byte
	index  int
}

// newJSONLLifecycleClient is the isolated JSONL transport seam. Production
// direct routes use WebSocket; both exercise the same projector and budgets.
func newJSONLLifecycleClient(stream readWriteCloser, peer PeerIdentity) *LifecycleClient {
	return &LifecycleClient{
		stream: stream, peer: peer,
		reader: bufio.NewReaderSize(stream, lifecycleChunkBytes), nextID: 1,
	}
}

func (in *lifecycleEOFInput) next() ([]byte, error) {
	if in.index >= len(in.chunks) {
		return nil, io.EOF
	}
	chunk := in.chunks[in.index]
	in.index++
	return chunk, nil
}

func projectLifecycleJSON(ctx context.Context, raw []byte, requestID int64, threadID string, widths ...int) (LifecycleSnapshot, error) {
	if len(widths) == 0 {
		widths = []int{len(raw)}
	}
	var chunks [][]byte
	for offset, index := 0, 0; offset < len(raw); index++ {
		width := widths[index%len(widths)]
		if width <= 0 {
			width = 1
		}
		end := min(len(raw), offset+width)
		chunks = append(chunks, raw[offset:end])
		offset = end
	}
	return (&lifecycleProjector{
		input: &lifecycleEOFInput{chunks: chunks}, requestID: requestID, threadID: threadID,
	}).run(ctx)
}

func projectLifecycleJSONL(ctx context.Context, raw []byte, requestID int64, threadID string) (LifecycleSnapshot, lifecycleBudget, error) {
	budget := lifecycleBudget{}
	input := &jsonLineLifecycleInput{
		reader: bufio.NewReaderSize(io.MultiReader(bytes.NewReader(raw), strings.NewReader("\n")), lifecycleChunkBytes),
		budget: &budget,
	}
	snapshot, err := (&lifecycleProjector{input: input, requestID: requestID, threadID: threadID}).run(ctx)
	return snapshot, budget, err
}

func projectLifecycleWebSocket(ctx context.Context, wire []byte, requestID int64, threadID string) (LifecycleSnapshot, lifecycleBudget, error) {
	raw := &lifecycleTestDuplex{reader: bytes.NewReader(wire)}
	stream := &websocketStream{raw: raw, reader: bufio.NewReaderSize(raw, lifecycleChunkBytes)}
	budget := lifecycleBudget{}
	input := &websocketLifecycleInput{stream: stream, budget: &budget}
	snapshot, err := (&lifecycleProjector{input: input, requestID: requestID, threadID: threadID}).run(ctx)
	return snapshot, budget, err
}

func TestLifecycleProjectorLiteralMetadataAndStrictShapes(t *testing.T) {
	t.Parallel()
	valid := `{"jsonrpc":"2.0","result":{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]},"turns":[{"id":null,"status":"future"},{"id":"turn-2","status":"inProgress","startedAt":null}]}},"id":7}`
	snapshot, err := projectLifecycleJSON(t.Context(), []byte(valid), 7, "thread-1", 1, 3, 17, 4_096)
	if err != nil {
		t.Fatalf("project valid response: %v", err)
	}
	want := LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: ThreadStateWaitingOnApproval,
		TurnCount: 2, TurnID: "turn-2", TurnState: TurnStateInProgress,
	}
	if snapshot != want {
		t.Fatalf("snapshot = %+v, want %+v", snapshot, want)
	}
	duplicateID := `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"turn-same","status":"future"},{"id":"turn-same","status":"completed"}]}}}`
	duplicateSnapshot, err := projectLifecycleJSON(t.Context(), []byte(duplicateID), 7, "thread-1", 3)
	if err != nil || duplicateSnapshot.TurnCount != 2 || duplicateSnapshot.TurnID != "turn-same" || duplicateSnapshot.TurnState != TurnStateCompleted {
		t.Fatalf("duplicate turn id snapshot=%+v err=%v", duplicateSnapshot, err)
	}

	for _, test := range []struct {
		name string
		raw  string
		kind error
	}{
		{name: "zero turns", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}}`},
		{name: "turns omitted", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"}}}}`, kind: ErrProtocol},
		{name: "turns null", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":null}}}`, kind: ErrProtocol},
		{name: "wrong response id", raw: `{"id":8,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}}`, kind: ErrProtocol},
		{name: "wrong thread", raw: `{"id":7,"result":{"thread":{"id":"thread-2","status":{"type":"idle"},"turns":[]}}}`, kind: ErrProtocol},
		{name: "unknown thread status", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"future"},"turns":[]}}}`, kind: ErrProtocol},
		{name: "unknown latest status", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[{"id":"turn-1","status":"future"}]}}}`, kind: ErrProtocol},
		{name: "duplicate key", raw: `{"id":7,"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}}`, kind: ErrProtocol},
		{name: "result and error", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}},"error":{"code":-1,"message":"x"}}`, kind: ErrProtocol},
		{name: "unsupported rpc", raw: `{"id":7,"error":{"code":-32601,"message":"missing"}}`, kind: ErrUnsupported},
		{name: "absent rpc", raw: `{"id":7,"error":{"code":-32000,"message":"thread not found"}}`, kind: ErrThreadAbsent},
		{name: "not durable rpc", raw: `{"id":7,"error":{"code":-32000,"message":"no rollout found"}}`, kind: ErrThreadNotDurable},
		{name: "notification", raw: `{"method":"turn/started","params":{},"id":7}`, kind: ErrProtocol},
		{name: "trailing json", raw: `{"id":7,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}}}{}`, kind: ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := projectLifecycleJSON(t.Context(), []byte(test.raw), 7, "thread-1", 2, 7)
			if test.kind == nil {
				if err != nil || got.ThreadID != "thread-1" || got.TurnCount != 0 {
					t.Fatalf("zero-turn snapshot=%+v err=%v", got, err)
				}
				return
			}
			if !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
		})
	}
}

func TestLifecycleProjectorRejectsMalformedUTF8AndDeclaredBounds(t *testing.T) {
	t.Parallel()
	validPrefix := []byte(`{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]},"body":"`)
	malformed := append(validPrefix, 0xff)
	malformed = append(malformed, []byte(`"}}`)...)
	if _, err := projectLifecycleJSON(t.Context(), malformed, 1, "thread-1", 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed UTF-8 error = %v", err)
	}

	cases := []struct {
		name string
		raw  string
	}{
		{name: "identity", raw: `{"id":1,"result":{"thread":{"id":"` + strings.Repeat("x", lifecycleIdentityBytes+1) + `","status":{"type":"idle"},"turns":[]}}}`},
		{name: "key scalar", raw: `{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}},"` + strings.Repeat("k", lifecycleScalarBytes+1) + `":0}`},
		{name: "object fields", raw: `{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}},` + repeatedJSONFields(lifecycleObjectFields) + `}`},
		{name: "turn count", raw: `{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[` + strings.TrimSuffix(strings.Repeat(`{"id":"old","status":"future"},`, lifecycleTurns+1), ",") + `]}}}`},
		{name: "value count", raw: `{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}},"body":[` + strings.TrimSuffix(strings.Repeat("0,", lifecycleValues), ",") + `]}`},
		{name: "depth", raw: `{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]}},"body":` + strings.Repeat("[", lifecycleDepth+1) + "0" + strings.Repeat("]", lifecycleDepth+1) + `}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := projectLifecycleJSON(t.Context(), []byte(test.raw), 1, "thread-1", lifecycleChunkBytes); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want protocol refusal", err)
			}
		})
	}
}

func repeatedJSONFields(count int) string {
	var fields strings.Builder
	for index := range count + 1 {
		if index > 0 {
			fields.WriteByte(',')
		}
		fields.WriteString(`"field`)
		fields.WriteString(strconv.Itoa(index))
		fields.WriteString(`":0`)
	}
	return fields.String()
}

func TestJSONLLifecycleTransportProjectsFivePointTwoMegabytesWithoutBodyRetention(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	peer := PeerIdentity{PID: 7, OwnerUID: 1000, Start: "test:jsonl-peer"}
	client := newJSONLLifecycleClient(clientConn, peer)
	defer client.Close()

	body := strings.Repeat("x", 5_200_000)
	response := `{"jsonrpc":"2.0","result":{"thread":{"id":"thread-large","status":{"type":"active"},"turns":[{"id":"turn-old","status":"future","items":[{"type":"agentMessage","text":"` + body + `"}]},{"id":"turn-last","status":"completed","startedAt":1700000000}]}},"id":1}`
	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConn)
		request, err := reader.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		var decoded struct {
			Method string                    `json:"method"`
			ID     int64                     `json:"id"`
			Params lifecycleThreadReadParams `json:"params"`
		}
		if json.Unmarshal(request, &decoded) != nil || decoded.Method != methodThreadRead || decoded.ID != 1 ||
			decoded.Params.ThreadID != "thread-large" || !decoded.Params.IncludeTurns {
			serverDone <- errors.New("lifecycle request did not ask for the complete exact thread")
			return
		}
		for offset := 0; offset < len(response); {
			end := min(len(response), offset+997)
			if _, err := serverConn.Write([]byte(response[offset:end])); err != nil {
				serverDone <- err
				return
			}
			offset = end
		}
		_, err = serverConn.Write([]byte("\r\n"))
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := client.ReadLifecycleSnapshot(ctx, "thread-large")
	if err != nil {
		t.Fatalf("read 5.2MB lifecycle response: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("synthetic provider: %v", err)
	}
	want := LifecycleSnapshot{
		ThreadID: "thread-large", ThreadState: ThreadStateActive, TurnCount: 2,
		TurnID: "turn-last", TurnState: TurnStateCompleted, StartedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if snapshot != want {
		t.Fatalf("5.2MB snapshot = %+v, want literal %+v", snapshot, want)
	}
	if len(response) <= maxFrameBytes || client.budget.jsonBytes != len(response)+1 {
		t.Fatalf("response bytes=%d json budget=%d, want >1MiB and CR-counted JSON", len(response), client.budget.jsonBytes)
	}
	if client.budget.wireBytes != len(response)+2 {
		t.Fatalf("wire bytes=%d, want %d", client.budget.wireBytes, len(response)+2)
	}
}

func TestWebSocketLifecycleTransportProjectsFragmentedUTF8AcrossControlFrames(t *testing.T) {
	response := []byte(`{"result":{"thread":{"id":"thread-ws","status":{"type":"active"},"turns":[{"id":"과거","status":"future"},{"id":"turn-last","status":"inProgress","startedAt":0}]}},"id":1}`)
	var wire bytes.Buffer
	for index, value := range response {
		opcode := byte(0)
		if index == 0 {
			opcode = 1
		}
		wire.Write(serverWebSocketFrame(index == len(response)-1, opcode, []byte{value}))
		if index == len(response)/2 {
			wire.Write(serverWebSocketFrame(true, 9, []byte("still-alive")))
		}
	}
	raw := &lifecycleTestDuplex{reader: bytes.NewReader(wire.Bytes())}
	stream := &websocketStream{raw: raw, reader: bufio.NewReaderSize(raw, lifecycleChunkBytes)}
	client := &LifecycleClient{
		stream: stream, peer: PeerIdentity{PID: 8, OwnerUID: 1000, Start: "test:ws-peer"},
		reader: bufio.NewReaderSize(stream, lifecycleChunkBytes), nextID: 1,
	}
	snapshot, err := client.ReadLifecycleSnapshot(t.Context(), "thread-ws")
	if err != nil {
		t.Fatalf("fragmented websocket lifecycle response: %v", err)
	}
	want := LifecycleSnapshot{
		ThreadID: "thread-ws", ThreadState: ThreadStateActive, TurnCount: 2,
		TurnID: "turn-last", TurnState: TurnStateInProgress, StartedAt: time.Unix(0, 0).UTC(),
	}
	if snapshot != want {
		t.Fatalf("websocket snapshot = %+v, want %+v", snapshot, want)
	}
	if client.budget.frames != len(response)+1 || client.budget.controlBytes != len("still-alive") {
		t.Fatalf("websocket budget frames=%d control=%d", client.budget.frames, client.budget.controlBytes)
	}
	// The owned reader answered ping on its own transport; no notification or
	// approval surface was involved.
	if raw.writes.Len() == 0 {
		t.Fatal("websocket transport wrote neither the lifecycle request nor pong")
	}
}

func TestLifecycleBudgetsAcceptBoundaryAndRejectOnePast(t *testing.T) {
	t.Parallel()
	budget := lifecycleBudget{}
	if err := budget.addJSON(lifecycleJSONBytes); err != nil {
		t.Fatalf("JSON boundary: %v", err)
	}
	if err := budget.addJSON(1); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("JSON overflow = %v", err)
	}
	budget = lifecycleBudget{}
	if err := budget.addWire(lifecycleWireBytes); err != nil {
		t.Fatalf("wire boundary: %v", err)
	}
	if err := budget.addWire(1); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("wire overflow = %v", err)
	}
	budget = lifecycleBudget{}
	for range lifecycleFrames {
		if err := budget.addFrame(); err != nil {
			t.Fatalf("frame boundary: %v", err)
		}
	}
	if err := budget.addFrame(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("frame overflow = %v", err)
	}
	budget = lifecycleBudget{}
	if err := budget.addControl(lifecycleControlBytes); err != nil {
		t.Fatalf("control boundary: %v", err)
	}
	if err := budget.addControl(1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("control overflow = %v", err)
	}
	projector := lifecycleProjector{ctx: t.Context(), retained: lifecycleRetainedBytes - lifecycleRetainedReserve}
	if err := projector.check(); err != nil {
		t.Fatalf("retained boundary: %v", err)
	}
	projector.retained++
	if err := projector.check(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("retained overflow = %v", err)
	}
	if lifecycleChunkBytes != 4<<10 || lifecycleDepth != 32 || lifecycleValues != 100_000 ||
		lifecycleObjectFields != 64 || lifecycleTurns != 4_096 || lifecycleScalarBytes != 1_024 ||
		lifecycleIdentityBytes != 256 || lifecycleSummaryBytes != 2<<10 {
		t.Fatal("declared lifecycle budget constants drifted")
	}
}

func TestLifecycleJSONLTransportEnforcesJSONAndDepthBoundaries(t *testing.T) {
	base := []byte(`{"id":1,"result":{"thread":{"id":"thread-budget","status":{"type":"idle"},"turns":[]}}}`)
	raw := make([]byte, lifecycleJSONBytes+1)
	copy(raw, base)
	for index := len(base); index < len(raw); index++ {
		raw[index] = ' '
	}
	snapshot, budget, err := projectLifecycleJSONL(t.Context(), raw[:lifecycleJSONBytes], 1, "thread-budget")
	if err != nil || snapshot.ThreadID != "thread-budget" || budget.jsonBytes != lifecycleJSONBytes {
		t.Fatalf("JSON boundary snapshot=%+v budget=%+v err=%v", snapshot, budget, err)
	}
	if _, _, err := projectLifecycleJSONL(t.Context(), raw, 1, "thread-budget"); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("JSON one-past error=%v", err)
	}

	for _, test := range []struct {
		name   string
		arrays int
		ok     bool
	}{
		{name: "depth boundary", arrays: lifecycleDepth - 1, ok: true},
		{name: "depth one-past", arrays: lifecycleDepth},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"id":1,"result":{"thread":{"id":"thread-budget","status":{"type":"idle"},"turns":[]}},"body":` +
				strings.Repeat("[", test.arrays) + "0" + strings.Repeat("]", test.arrays) + `}`)
			got, _, err := projectLifecycleJSONL(t.Context(), raw, 1, "thread-budget")
			if test.ok {
				if err != nil || got.ThreadID != "thread-budget" {
					t.Fatalf("depth boundary snapshot=%+v err=%v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("depth one-past error=%v", err)
			}
		})
	}
}

func TestInitializedLifecycleClientKeepsFullJSONResponseBudgetAndCumulativeWire(t *testing.T) {
	for _, test := range []struct {
		name         string
		responseSize int
		wantOverflow bool
	}{
		{name: "boundary", responseSize: lifecycleJSONBytes},
		{name: "one past", responseSize: lifecycleJSONBytes + 1, wantOverflow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := newJSONLLifecycleClient(clientConn, PeerIdentity{PID: 10, OwnerUID: 1000, Start: "test:initialized-budget"})
			serverDone := make(chan error, 1)
			requestsReady := make(chan struct{})
			go func() {
				reader := bufio.NewReader(serverConn)
				initRequest, err := reader.ReadBytes('\n')
				if err != nil || !bytes.Contains(initRequest, []byte(`"method":"initialize"`)) {
					serverDone <- errors.New("missing initialize request")
					return
				}
				initBase := []byte(`{"id":1,"result":{}}`)
				initResponse := make([]byte, lifecycleInitializeLimit+1)
				copy(initResponse, initBase)
				for index := len(initBase); index < lifecycleInitializeLimit; index++ {
					initResponse[index] = ' '
				}
				initResponse[lifecycleInitializeLimit] = '\n'
				if _, err := serverConn.Write(initResponse); err != nil {
					serverDone <- err
					return
				}
				initialized, err := reader.ReadBytes('\n')
				if err != nil || !bytes.Contains(initialized, []byte(`"method":"initialized"`)) {
					serverDone <- errors.New("missing initialized notification")
					return
				}
				readRequest, err := reader.ReadBytes('\n')
				if err != nil || !bytes.Contains(readRequest, []byte(`"method":"thread/read"`)) {
					serverDone <- errors.New("missing thread/read request")
					return
				}
				close(requestsReady)
				responseBase := []byte(`{"id":2,"result":{"thread":{"id":"thread-initialized","status":{"type":"idle"},"turns":[]}}}`)
				response := make([]byte, test.responseSize+1)
				copy(response, responseBase)
				for index := len(responseBase); index < test.responseSize; index++ {
					response[index] = ' '
				}
				response[test.responseSize] = '\n'
				_, err = serverConn.Write(response)
				serverDone <- err
			}()

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := client.initialize(ctx, "test", true); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			if client.budget.jsonBytes != 0 || client.budget.wireBytes != lifecycleInitializeLimit+1 || client.budget.frames != 1 {
				t.Fatalf("post-initialize budget=%+v", client.budget)
			}
			snapshot, err := client.ReadLifecycleSnapshot(ctx, "thread-initialized")
			<-requestsReady
			if test.wantOverflow {
				if !errors.Is(err, ErrPayloadTooLarge) {
					t.Fatalf("one-past snapshot=%+v err=%v", snapshot, err)
				}
			} else if err != nil || snapshot.ThreadID != "thread-initialized" || client.budget.jsonBytes != lifecycleJSONBytes {
				t.Fatalf("boundary snapshot=%+v budget=%+v err=%v", snapshot, client.budget, err)
			}
			_ = client.Close()
			_ = serverConn.Close()
			serverErr := <-serverDone
			if !test.wantOverflow && serverErr != nil {
				t.Fatalf("synthetic initialized server: %v", serverErr)
			}
			wantWireFloor := lifecycleInitializeLimit + 1 + test.responseSize
			if client.budget.wireBytes < wantWireFloor || client.budget.wireBytes > lifecycleWireBytes {
				t.Fatalf("cumulative wire=%d, want >=%d and <=%d", client.budget.wireBytes, wantWireFloor, lifecycleWireBytes)
			}
		})
	}
}

func TestLifecycleJSONLTransportAcceptsMetadataCardinalityBoundaries(t *testing.T) {
	identity := strings.Repeat("i", lifecycleIdentityBytes)
	latest := strings.Repeat("u", lifecycleIdentityBytes)

	t.Run("scalar identity and object fields", func(t *testing.T) {
		bodyFields := repeatedJSONFields(lifecycleObjectFields - 1)
		raw := []byte(`{"id":1,"result":{"thread":{"id":` + strconv.Quote(identity) +
			`,"status":{"type":"idle"},"turns":[{"id":` + strconv.Quote(latest) +
			`,"status":"completed"}]}},"body":{` + bodyFields + `},` +
			strconv.Quote(strings.Repeat("k", lifecycleScalarBytes)) + `:0}`)
		snapshot, _, err := projectLifecycleJSONL(t.Context(), raw, 1, identity)
		if err != nil || snapshot.ThreadID != identity || snapshot.TurnID != latest {
			t.Fatalf("cardinality boundary snapshot=%+v err=%v", snapshot, err)
		}
	})

	t.Run("turns", func(t *testing.T) {
		turns := strings.Repeat(`{"id":"old","status":"future"},`, lifecycleTurns-1) +
			`{"id":"last","status":"completed"}`
		raw := []byte(`{"id":1,"result":{"thread":{"id":"thread-turns","status":{"type":"idle"},"turns":[` + turns + `]}}}`)
		snapshot, _, err := projectLifecycleJSONL(t.Context(), raw, 1, "thread-turns")
		if err != nil || snapshot.TurnCount != lifecycleTurns || snapshot.TurnID != "last" {
			t.Fatalf("turn boundary snapshot=%+v err=%v", snapshot, err)
		}
	})

	t.Run("values", func(t *testing.T) {
		// The empty lifecycle response consumes eight values (root, id,
		// result, thread, thread id, status, status type, and turns). Body
		// consumes its array value plus the remaining scalar values.
		const responseValues = 8
		bodyValues := lifecycleValues - responseValues - 1
		makeResponse := func(count int) []byte {
			return []byte(`{"id":1,"result":{"thread":{"id":"thread-values","status":{"type":"idle"},"turns":[]}},"body":[` +
				strings.TrimSuffix(strings.Repeat("0,", count), ",") + `]}`)
		}
		snapshot, _, err := projectLifecycleJSONL(t.Context(), makeResponse(bodyValues), 1, "thread-values")
		if err != nil || snapshot.ThreadID != "thread-values" {
			t.Fatalf("value boundary snapshot=%+v err=%v", snapshot, err)
		}
		if _, _, err := projectLifecycleJSONL(t.Context(), makeResponse(bodyValues+1), 1, "thread-values"); !errors.Is(err, ErrProtocol) {
			t.Fatalf("value one-past error=%v", err)
		}
	})
}

func TestLifecycleWebSocketTransportEnforcesFrameAndControlBoundaries(t *testing.T) {
	response := []byte(`{"id":1,"result":{"thread":{"id":"thread-ws-budget","status":{"type":"idle"},"turns":[]}}}`)

	t.Run("frame", func(t *testing.T) {
		var exact bytes.Buffer
		for range lifecycleFrames - 1 {
			exact.Write(serverWebSocketFrame(true, 0xA, nil))
		}
		exact.Write(serverWebSocketFrame(true, 0x1, response))
		snapshot, budget, err := projectLifecycleWebSocket(t.Context(), exact.Bytes(), 1, "thread-ws-budget")
		if err != nil || snapshot.ThreadID != "thread-ws-budget" || budget.frames != lifecycleFrames {
			t.Fatalf("frame boundary snapshot=%+v budget=%+v err=%v", snapshot, budget, err)
		}

		var overflow bytes.Buffer
		for range lifecycleFrames {
			overflow.Write(serverWebSocketFrame(true, 0xA, nil))
		}
		overflow.Write(serverWebSocketFrame(true, 0x1, response))
		if _, _, err := projectLifecycleWebSocket(t.Context(), overflow.Bytes(), 1, "thread-ws-budget"); !errors.Is(err, ErrProtocol) {
			t.Fatalf("frame one-past error=%v", err)
		}
	})

	t.Run("control", func(t *testing.T) {
		var exact bytes.Buffer
		writeLifecycleControlBytes(&exact, lifecycleControlBytes)
		exact.Write(serverWebSocketFrame(true, 0x1, response))
		snapshot, budget, err := projectLifecycleWebSocket(t.Context(), exact.Bytes(), 1, "thread-ws-budget")
		if err != nil || snapshot.ThreadID != "thread-ws-budget" || budget.controlBytes != lifecycleControlBytes {
			t.Fatalf("control boundary snapshot=%+v budget=%+v err=%v", snapshot, budget, err)
		}

		var overflow bytes.Buffer
		writeLifecycleControlBytes(&overflow, lifecycleControlBytes+1)
		overflow.Write(serverWebSocketFrame(true, 0x1, response))
		if _, _, err := projectLifecycleWebSocket(t.Context(), overflow.Bytes(), 1, "thread-ws-budget"); !errors.Is(err, ErrProtocol) {
			t.Fatalf("control one-past error=%v", err)
		}
	})
}

func writeLifecycleControlBytes(wire *bytes.Buffer, count int) {
	payload := bytes.Repeat([]byte{'p'}, 125)
	for count > 0 {
		frameBytes := min(count, len(payload))
		wire.Write(serverWebSocketFrame(true, 0xA, payload[:frameBytes]))
		count -= frameBytes
	}
}

func TestLifecycleTransportEnforcesRetainedAndProvesWireSummaryDominance(t *testing.T) {
	// The enforced retained metric includes a conservative fixed reserve for
	// input/scalar/flags/summary state, plus every active decoded key byte and
	// the bounded duplicate/projection storage for all 32 live objects.
	keyBudget := lifecycleRetainedBytes - lifecycleRetainedReserve - lifecycleDepth*lifecycleObjectRetainedStateBytes
	boundary := lifecycleRetainedResponse("thread-retained", keyBudget)
	snapshot, budget, err := projectLifecycleJSONL(t.Context(), []byte(boundary), 1, "thread-retained")
	if err != nil || snapshot.ThreadID != "thread-retained" {
		t.Fatalf("retained boundary snapshot=%+v budget=%+v err=%v", snapshot, budget, err)
	}
	overRetained := lifecycleRetainedResponse("thread-retained", keyBudget+1)
	if _, _, err := projectLifecycleJSONL(t.Context(), []byte(overRetained), 1, "thread-retained"); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "retained-state limit") {
		t.Fatalf("retained one-past refusal=%v", err)
	}

	// Even the maximum JSON, control payload, and ten-byte header for every
	// allowed frame stays below 17 MiB, so wire is dominated before it can
	// fail independently. The transport-driven JSON/control/frame tests above
	// exercise each dominating path at boundary and one-past.
	maxWire := lifecycleInitializeLimit + lifecycleJSONBytes + lifecycleControlBytes + lifecycleFrames*10
	if maxWire >= lifecycleWireBytes {
		t.Fatalf("wire dominance proof drifted: max=%d cap=%d", maxWire, lifecycleWireBytes)
	}
	maxSummary, err := json.Marshal(LifecycleSnapshot{
		ThreadID: strings.Repeat("t", lifecycleIdentityBytes), ThreadState: ThreadStateWaitingOnApproval,
		TurnCount: lifecycleTurns, TurnID: strings.Repeat("u", lifecycleIdentityBytes),
		TurnState: TurnStateInProgress, StartedAt: time.Date(9999, time.December, 31, 23, 59, 59, 999_999_999, time.UTC),
	})
	if err != nil || len(maxSummary) >= lifecycleSummaryBytes {
		t.Fatalf("summary dominance proof bytes=%d err=%v", len(maxSummary), err)
	}
}

func TestLifecycleTransportAccountsForSimultaneouslyRetainedProjectorState(t *testing.T) {
	// All supported targets are 64-bit. This oracle deliberately uses literal
	// ABI sizes rather than the production accounting constants: each live
	// object owns one 24-byte slice header, 64 16-byte string slots, and four
	// 16-byte interface slots. The fixed 108 KiB reserve independently covers
	// input/escaped-scalar/metadata/flags/management/summary state beside these
	// object tables.
	const (
		liveObjects            = 32
		objectKeySlots         = 64
		stringHeaderBytes      = 16
		sliceHeaderBytes       = 24
		interfaceHeaderBytes   = 16
		objectFieldSlots       = 4
		objectManagedBytes     = sliceHeaderBytes + objectKeySlots*stringHeaderBytes + objectFieldSlots*interfaceHeaderBytes
		allObjectManagedBytes  = liveObjects * objectManagedBytes
		oldKeyOnlyBoundary     = (2 << 20) - (96 << 10)
		currentAdmission       = (2 << 20) - (108 << 10)
		fullStateKeyBoundary   = currentAdmission - allObjectManagedBytes
		ownerLiteralLowerBound = oldKeyOnlyBoundary +
			liveObjects*objectKeySlots*stringHeaderBytes + objectKeySlots*1_024 + objectKeySlots*stringHeaderBytes
	)
	if oldKeyOnlyBoundary != 1_998_848 || objectManagedBytes != 1_112 ||
		allObjectManagedBytes != 35_584 || currentAdmission != 1_986_560 || fullStateKeyBoundary != 1_950_976 ||
		ownerLiteralLowerBound != 2_098_176 || ownerLiteralLowerBound-(2<<20) != 1_024 {
		t.Fatal("independent retained-state oracle constants drifted")
	}
	if lifecycleRetainedInputReserveBytes != 8_192 || lifecycleRetainedEncodedStringBytes != 6_146 ||
		lifecycleRetainedUnquoteBytes != 6_152 || lifecycleRetainedDecodedStringBytes != 6_143 ||
		lifecycleRetainedScalarReserveBytes != 18_441 ||
		lifecycleRetainedFlagsReserveBytes != 66_584 || lifecycleRetainedMetadataReserveBytes != 9_216 ||
		lifecycleRetainedManagementBytes != 4_096 || lifecycleRetainedSummaryReserveBytes != 2_048 ||
		lifecycleRetainedResidualReserveBytes != 2_015 || lifecycleRetainedReserve != 108<<10 {
		t.Fatal("fixed retained-state reserve breakdown drifted")
	}

	want := LifecycleSnapshot{
		ThreadID: "thread-retained", ThreadState: ThreadStateActive, TurnCount: 1,
		TurnID: "turn-last", TurnState: TurnStateCompleted,
		StartedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	for _, transport := range []struct {
		name    string
		project func([]byte) (LifecycleSnapshot, error)
	}{
		{
			name: "jsonl",
			project: func(raw []byte) (LifecycleSnapshot, error) {
				snapshot, _, err := projectLifecycleJSONL(t.Context(), raw, 1, want.ThreadID)
				return snapshot, err
			},
		},
		{
			name: "websocket",
			project: func(raw []byte) (LifecycleSnapshot, error) {
				snapshot, _, err := projectLifecycleWebSocket(t.Context(), serverWebSocketFrame(true, 0x1, raw), 1, want.ThreadID)
				return snapshot, err
			},
		},
	} {
		t.Run(transport.name, func(t *testing.T) {
			boundary := []byte(lifecycleSimultaneousRetainedResponse(want.ThreadID, fullStateKeyBoundary))
			snapshot, err := transport.project(boundary)
			if err != nil || snapshot != want {
				t.Fatalf("full-state boundary snapshot=%+v want=%+v err=%v", snapshot, want, err)
			}

			onePast := []byte(lifecycleSimultaneousRetainedResponse(want.ThreadID, fullStateKeyBoundary+1))
			if _, err := transport.project(onePast); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "retained-state limit") {
				t.Fatalf("full-state one-past refusal=%v", err)
			}

			// This strengthens the archived owner's key/flag layout with the
			// scalar fields needed for a complete summary. Its key bodies are
			// exactly the former admission boundary, so key-byte-only accounting
			// admits it. The whole-state oracle exceeds the corrected admission
			// boundary without relying on allocator, stack, whole-heap, or RSS costs.
			ownerBoundaryVariant := []byte(lifecycleSimultaneousRetainedResponse(want.ThreadID, oldKeyOnlyBoundary))
			if _, err := transport.project(ownerBoundaryVariant); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "retained-state limit") {
				t.Fatalf("old key-only boundary was not refused: %v", err)
			}
		})
	}

	// Removing any managed-storage term from the one-past oracle makes the
	// invalid fixture appear admissible. This mutation table ensures the
	// boundary above depends on every term, rather than copying the product
	// accounting helper into the expectation.
	onePastOracle := fullStateKeyBoundary + 1 + allObjectManagedBytes + (108 << 10)
	if onePastOracle != 2<<20+1 {
		t.Fatalf("one-past oracle=%d", onePastOracle)
	}
	for name, omitted := range map[string]int{
		"decoded key body":       1,
		"key string slots":       liveObjects * objectKeySlots * stringHeaderBytes,
		"object slice headers":   liveObjects * sliceHeaderBytes,
		"projection field slots": liveObjects * objectFieldSlots * interfaceHeaderBytes,
		"input buffers":          8_192,
		"scalar work buffer":     18_441,
		"flag bodies and slice":  66_584,
		"metadata scalar bodies": 9_216,
		"management state":       4_096,
		"summary":                2_048,
		"residual state":         2_015,
	} {
		if mutated := onePastOracle - omitted; mutated > 2<<20 {
			t.Fatalf("%s omission mutation stayed over cap: %d", name, mutated)
		}
	}
}

func TestLifecycleTransportReservesEscapedScalarBeforeValidation(t *testing.T) {
	// This oracle is intentionally independent of the production reserve terms.
	// At the boundary, 33 object containers (depths 0 through 32) and all prior
	// decoded key bodies exactly fill admission. The next key has a 6,144-byte
	// encoded interior with one escape: encoding/json therefore holds its 6,152-
	// byte unquote buffer and 6,143-byte decoded body beside the 6,146-byte
	// projector scratch before the 1 KiB decoded limit can refuse it.
	const (
		retainedCap             = 2 << 20
		fixedReserve            = 108 << 10
		liveObjects             = 33
		objectManagedBytes      = 24 + 64*16 + 4*16
		admittedKeyBodyBoundary = retainedCap - fixedReserve - liveObjects*objectManagedBytes
		inputBuffers            = 8_192
		projectorEncodedScratch = 6_146
		jsonUnquoteTemporary    = 6_152
		decodedBeforeValidation = 6_143
		unescapedDecodedString  = 6_144
		// Go 1.26.6 grows the unquoted token backing to 1,408 bytes before
		// converting its maximum 1,024-byte token to a string; the anchored
		// number regexp's one-pass machine contributes another 96 bytes.
		unquotedTokenBacking = 1_408
		unquotedTokenString  = 1_024
		unquotedRegexpState  = 96
		flagBodiesAndSlice   = 66_584
		priorMetadataBodies  = 9_216
		managementState      = 4_096
		laterSummary         = 2_048
		reserveResidual      = 2_015

		// Exact or rounded-up 64-bit product descriptors. Object key-slice
		// headers, their 64 string slots, and their four projection slots are
		// charged separately through objectManagedBytes above.
		projectorHeaderAndCounters = 112 // 6,256-byte projector minus its 6,146-byte scalar buffer, rounded to alignment
		ownedClientDescriptor      = 112
		websocketStreamDescriptor  = 56
		bufioReaderDescriptor      = 88
		websocketInputDescriptor   = 40  // 4,136-byte input minus its separately reserved 4,096-byte chunk
		projectedValueWrappers     = 104 // RPC error 32 + latest turn 56 + turn-list result 16
		metadataStringHeaders      = 9 * 16
		currentDecodedStringHeader = 16
		expectedThreadIdentity     = 256
		peerStartIdentity          = 160
		bufferedReadResultSlot     = 112

		// These are deliberately rounded envelopes for bounded implementation
		// machinery rather than allocator, stack, whole-heap, or RSS claims.
		resultChannelAndClosureAllowance = 256
		jsonDecoderAllowance             = 512
		controlAndPongAllowance          = 512
		cancellationTimerAllowance       = 512
	)
	if admittedKeyBodyBoundary != 1_949_864 {
		t.Fatalf("escaped-scalar admitted key boundary=%d", admittedKeyBodyBoundary)
	}
	unquotedScalarUpper := projectorEncodedScratch + unquotedTokenBacking + unquotedTokenString + unquotedRegexpState
	unescapedStringUpper := projectorEncodedScratch + unescapedDecodedString
	escapedScalarUpper := projectorEncodedScratch + jsonUnquoteTemporary + decodedBeforeValidation
	// One valid two-byte escape maximizes decoded length while forcing unquote.
	// Malformed-UTF-8 decoder growth is unreachable because the projector rejects
	// invalid UTF-8 before calling encoding/json.
	if unquotedScalarUpper != 8_674 || unescapedStringUpper != 12_290 || escapedScalarUpper != 18_441 ||
		unquotedScalarUpper >= escapedScalarUpper || unescapedStringUpper >= escapedScalarUpper {
		t.Fatalf("scalar branch oracle unquoted=%d unescaped=%d escaped=%d",
			unquotedScalarUpper, unescapedStringUpper, escapedScalarUpper)
	}
	managementProved := projectorHeaderAndCounters + ownedClientDescriptor + websocketStreamDescriptor +
		bufioReaderDescriptor + websocketInputDescriptor + projectedValueWrappers +
		metadataStringHeaders + currentDecodedStringHeader +
		expectedThreadIdentity + peerStartIdentity + bufferedReadResultSlot +
		resultChannelAndClosureAllowance + jsonDecoderAllowance + controlAndPongAllowance +
		cancellationTimerAllowance
	if managementProved != 2_992 || managementProved > managementState || managementState-managementProved != 1_104 {
		t.Fatalf("management oracle proved=%d envelope=%d", managementProved, managementState)
	}
	workingUpper := inputBuffers + projectorEncodedScratch + jsonUnquoteTemporary +
		decodedBeforeValidation + flagBodiesAndSlice + priorMetadataBodies + managementState
	if workingUpper != 106_529 || workingUpper+laterSummary != 108_577 ||
		fixedReserve-(workingUpper+laterSummary) != reserveResidual {
		t.Fatalf("escaped-scalar reserve oracle working=%d conservative=%d", workingUpper, workingUpper+laterSummary)
	}
	onePastOracle := admittedKeyBodyBoundary + 1 + liveObjects*objectManagedBytes +
		workingUpper + laterSummary + reserveResidual
	if onePastOracle != retainedCap+1 {
		t.Fatalf("escaped-scalar one-past oracle=%d", onePastOracle)
	}
	for name, omitted := range map[string]int{
		"unquote temporary":                      jsonUnquoteTemporary,
		"extra three metadata bodies":            3 * 1_024,
		"active-object headers/projection slots": liveObjects * (24 + 4*16),
		"management envelope":                    managementState,
	} {
		if mutated := onePastOracle - omitted; mutated > retainedCap {
			t.Fatalf("%s omission mutation stayed over cap: %d", name, mutated)
		}
	}

	for _, transport := range []struct {
		name    string
		project func([]byte) error
	}{
		{
			name: "jsonl",
			project: func(raw []byte) error {
				_, _, err := projectLifecycleJSONL(t.Context(), raw, 1, "thread-escaped-reserve")
				return err
			},
		},
		{
			name: "websocket",
			project: func(raw []byte) error {
				_, _, err := projectLifecycleWebSocket(t.Context(), serverWebSocketFrame(true, 0x1, raw), 1, "thread-escaped-reserve")
				return err
			},
		},
	} {
		t.Run(transport.name, func(t *testing.T) {
			boundary := []byte(lifecycleEscapedScalarReserveResponse(admittedKeyBodyBoundary))
			if err := transport.project(boundary); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "malformed lifecycle string") {
				t.Fatalf("escaped-scalar boundary error=%v", err)
			}

			onePast := []byte(lifecycleEscapedScalarReserveResponse(admittedKeyBodyBoundary + 1))
			if err := transport.project(onePast); !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "retained-state limit") {
				t.Fatalf("escaped-scalar one-past error=%v", err)
			}
		})
	}
}

func lifecycleEscapedScalarReserveResponse(targetKeyBytes int) string {
	const (
		ignoredObjects = lifecycleDepth - 3
		fillerKeys     = (lifecycleObjectFields - 4) +
			(lifecycleObjectFields - 1) +
			(lifecycleObjectFields - 3) +
			(lifecycleObjectFields - 3) +
			(ignoredObjects-1)*(lifecycleObjectFields-1)
		fixedKeyBytes = len("id") + len("jsonrpc") + len("error") + len("result") +
			len("thread") + len("id") + len("turns") + len("status") +
			len("type") + len("activeFlags") + len("n") + (ignoredObjects-1)*len("n")
	)
	fillerBytes := targetKeyBytes - fixedKeyBytes
	baseLength, extra := fillerBytes/fillerKeys, fillerBytes%fillerKeys
	if baseLength < len("k0000-") || baseLength+1 > lifecycleScalarBytes {
		panic("escaped scalar reserve fixture cannot represent target")
	}
	nextKey := 0
	longKey := func() string {
		prefix := "k" + strconv.Itoa(nextKey) + "-"
		length := baseLength
		if nextKey < extra {
			length++
		}
		nextKey++
		return prefix + strings.Repeat("x", length-len(prefix))
	}
	fillerFields := func(count int) string {
		var value strings.Builder
		for index := range count {
			if index > 0 {
				value.WriteByte(',')
			}
			value.WriteString(strconv.Quote(longKey()))
			value.WriteString(":0")
		}
		return value.String()
	}
	appendFields := func(prefix string, fillerCount int, suffix string) string {
		var value strings.Builder
		value.WriteByte('{')
		if prefix != "" {
			value.WriteString(prefix)
		}
		if fillerCount > 0 {
			if prefix != "" {
				value.WriteByte(',')
			}
			value.WriteString(fillerFields(fillerCount))
		}
		if suffix != "" {
			if prefix != "" || fillerCount > 0 {
				value.WriteByte(',')
			}
			value.WriteString(suffix)
		}
		value.WriteByte('}')
		return value.String()
	}

	// One two-byte escape forces encoding/json's unquote allocation while the
	// remaining literal bytes maximize the decoded body before validation.
	escapedKey := `"\/` + strings.Repeat("x", lifecycleScalarBytes*6-2) + `"`
	child := "{" + escapedKey + ":0}"
	for range ignoredObjects - 1 {
		child = appendFields("", lifecycleObjectFields-1, `"n":`+child)
	}
	flags := make([]string, lifecycleObjectFields)
	for index := range flags {
		prefix := fmt.Sprintf("%02d", index)
		flags[index] = prefix + strings.Repeat("f", lifecycleScalarBytes-len(prefix))
	}
	flagJSON, err := json.Marshal(flags)
	if err != nil {
		panic(err)
	}
	metadata := func(prefix byte) string { return strings.Repeat(string(prefix), lifecycleScalarBytes) }
	errorObject := `{"code":` + strconv.Quote(metadata('c')) + `,"message":` + strconv.Quote(metadata('m')) + `}`
	turns := `[{"id":` + strconv.Quote(metadata('u')) + `,"status":` + strconv.Quote(metadata('v')) +
		`,"startedAt":` + strconv.Quote(metadata('a')) + `}]`
	status := appendFields(`"type":`+strconv.Quote(metadata('s'))+`,"activeFlags":`+string(flagJSON),
		lifecycleObjectFields-3, `"n":`+child)
	thread := appendFields(`"id":`+strconv.Quote(metadata('t'))+`,"turns":`+turns,
		lifecycleObjectFields-3, `"status":`+status)
	result := appendFields("", lifecycleObjectFields-1, `"thread":`+thread)
	response := appendFields(`"id":`+strconv.Quote(metadata('i'))+`,"jsonrpc":`+strconv.Quote(metadata('j'))+
		`,"error":`+errorObject, lifecycleObjectFields-4, `"result":`+result)
	if nextKey != fillerKeys {
		panic("escaped scalar reserve fixture key count drifted")
	}
	return response
}

func lifecycleSimultaneousRetainedResponse(threadID string, targetKeyBytes int) string {
	const (
		ignoredObjects = lifecycleDepth - 4
		fillerKeys     = (lifecycleObjectFields - 2) +
			(lifecycleObjectFields - 1) +
			(lifecycleObjectFields - 3) +
			(lifecycleObjectFields - 3) +
			(ignoredObjects-1)*(lifecycleObjectFields-1) + lifecycleObjectFields
		fixedKeyBytes = len("id") + len("result") + len("thread") +
			len("id") + len("turns") + len("status") +
			len("type") + len("activeFlags") + len("n") +
			(ignoredObjects-1)*len("n")
	)
	fillerBytes := targetKeyBytes - fixedKeyBytes
	baseLength, extra := fillerBytes/fillerKeys, fillerBytes%fillerKeys
	if baseLength < len("k0000-") || baseLength+1 > lifecycleScalarBytes {
		panic("simultaneous retained fixture cannot represent target")
	}
	nextKey := 0
	longKey := func() string {
		prefix := "k" + strconv.Itoa(nextKey) + "-"
		length := baseLength
		if nextKey < extra {
			length++
		}
		nextKey++
		return prefix + strings.Repeat("x", length-len(prefix))
	}
	fillerFields := func(count int) string {
		var value strings.Builder
		for index := range count {
			if index > 0 {
				value.WriteByte(',')
			}
			value.WriteString(strconv.Quote(longKey()))
			value.WriteString(":0")
		}
		return value.String()
	}
	appendFields := func(prefix string, fillerCount int, suffix string) string {
		var value strings.Builder
		value.WriteByte('{')
		if prefix != "" {
			value.WriteString(prefix)
		}
		if fillerCount > 0 {
			if prefix != "" {
				value.WriteByte(',')
			}
			value.WriteString(fillerFields(fillerCount))
		}
		if suffix != "" {
			if prefix != "" || fillerCount > 0 {
				value.WriteByte(',')
			}
			value.WriteString(suffix)
		}
		value.WriteByte('}')
		return value.String()
	}

	child := appendFields("", lifecycleObjectFields, "")
	for range ignoredObjects - 1 {
		child = appendFields("", lifecycleObjectFields-1, `"n":`+child)
	}
	flags := make([]string, lifecycleObjectFields)
	for index := range flags {
		prefix := fmt.Sprintf("%02d", index)
		flags[index] = prefix + strings.Repeat("f", lifecycleScalarBytes-len(prefix))
	}
	flagJSON, err := json.Marshal(flags)
	if err != nil {
		panic(err)
	}
	status := appendFields(`"type":"active","activeFlags":`+string(flagJSON), lifecycleObjectFields-3, `"n":`+child)
	turns := `[{"id":"turn-last","status":"completed","startedAt":1700000000}]`
	thread := appendFields(`"id":`+strconv.Quote(threadID)+`,"turns":`+turns, lifecycleObjectFields-3, `"status":`+status)
	result := appendFields("", lifecycleObjectFields-1, `"thread":`+thread)
	response := appendFields(`"id":1`, lifecycleObjectFields-2, `"result":`+result)
	if nextKey != fillerKeys {
		panic("simultaneous retained fixture key count drifted")
	}
	return response
}

func lifecycleRetainedResponse(threadID string, targetKeyBytes int) string {
	const nestedObjects = lifecycleDepth - 1
	const intermediateObjects = nestedObjects - 1
	fillerCount := lifecycleObjectFields - 3
	fillerCount += intermediateObjects * (lifecycleObjectFields - 1)
	fillerCount += lifecycleObjectFields
	fixedKeyBytes := len("id") + len("result") + len("n") + intermediateObjects*len("n")
	fillerBytes := targetKeyBytes - fixedKeyBytes
	baseLength, extra := fillerBytes/fillerCount, fillerBytes%fillerCount
	if baseLength < len("k63-") || baseLength+1 > lifecycleScalarBytes {
		panic("lifecycle retained fixture cannot represent target")
	}
	nextKey := 0
	longKey := func(index int) string {
		prefix := "k" + strconv.Itoa(index) + "-"
		length := baseLength
		if nextKey < extra {
			length++
		}
		nextKey++
		return prefix + strings.Repeat("x", length-len(prefix))
	}
	object := func(fields int, child string) string {
		var value strings.Builder
		value.WriteByte('{')
		for index := range fields {
			if index > 0 {
				value.WriteByte(',')
			}
			value.WriteString(strconv.Quote(longKey(index)))
			value.WriteString(":0")
		}
		if child != "" {
			if fields > 0 {
				value.WriteByte(',')
			}
			value.WriteString(`"n":`)
			value.WriteString(child)
		}
		value.WriteByte('}')
		return value.String()
	}
	child := object(lifecycleObjectFields, "")
	for range intermediateObjects {
		child = object(lifecycleObjectFields-1, child)
	}
	var root strings.Builder
	root.WriteString(`{"id":1,"result":{"thread":{"id":`)
	root.WriteString(strconv.Quote(threadID))
	root.WriteString(`,"status":{"type":"idle"},"turns":[]}},`)
	for index := range lifecycleObjectFields - 3 {
		root.WriteString(strconv.Quote(longKey(index)))
		root.WriteString(":0,")
	}
	root.WriteString(`"n":`)
	root.WriteString(child)
	root.WriteByte('}')
	return root.String()
}

func TestJSONLLifecycleTransportAcceptsLFAndCRLF(t *testing.T) {
	response := `{"id":1,"result":{"thread":{"id":"thread-lines","status":{"type":"idle"},"turns":[]}}}`
	for _, delimiter := range []string{"\n", "\r\n"} {
		t.Run(strconv.Quote(delimiter), func(t *testing.T) {
			budget := lifecycleBudget{}
			input := &jsonLineLifecycleInput{
				reader: bufio.NewReaderSize(strings.NewReader(response+delimiter), 7), budget: &budget,
			}
			snapshot, err := (&lifecycleProjector{input: input, requestID: 1, threadID: "thread-lines"}).run(t.Context())
			if err != nil || snapshot.ThreadID != "thread-lines" || snapshot.TurnCount != 0 {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
			if budget.frames != 1 || budget.wireBytes != len(response)+len(delimiter) {
				t.Fatalf("budget=%+v", budget)
			}
		})
	}
}

func TestLifecycleTransportBlockedReadAndWriteRespectCallerPlusCleanup(t *testing.T) {
	for _, test := range []struct {
		name       string
		readServer bool
	}{
		{name: "blocked write"},
		{name: "blocked read", readServer: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer serverConn.Close()
			if test.readServer {
				go func() {
					_, _ = bufio.NewReader(serverConn).ReadBytes('\n')
					_, _ = io.Copy(io.Discard, serverConn)
				}()
			}
			client := newJSONLLifecycleClient(clientConn, PeerIdentity{PID: 9, OwnerUID: 1000, Start: "test:blocked"})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err := client.ReadLifecycleSnapshot(ctx, "thread-blocked")
			if err == nil {
				t.Fatal("blocked transport returned success")
			}
			if elapsed := time.Since(started); elapsed > 20*time.Millisecond+lifecycleCleanupLimit {
				t.Fatalf("blocked transport elapsed=%s, want caller+cleanup", elapsed)
			}
			if closeErr := client.Close(); closeErr != nil {
				t.Fatalf("reap blocked transport: %v", closeErr)
			}
		})
	}
}

func serverWebSocketFrame(fin bool, opcode byte, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	header := []byte{first}
	switch {
	case len(payload) <= 125:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65_535:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	return append(header, payload...)
}
