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
	"math"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	lifecycleJSONBytes       = 16 << 20
	lifecycleWireBytes       = 17 << 20
	lifecycleChunkBytes      = 4 << 10
	lifecycleDepth           = 32
	lifecycleValues          = 100_000
	lifecycleObjectFields    = 64
	lifecycleTurns           = 4_096
	lifecycleScalarBytes     = 1_024
	lifecycleIdentityBytes   = 256
	lifecycleSummaryBytes    = 2 << 10
	lifecycleFrames          = 4_096
	lifecycleControlBytes    = 64 << 10
	lifecycleRetainedBytes   = 2 << 20
	lifecycleOperationLimit  = 750 * time.Millisecond
	lifecycleCleanupLimit    = 250 * time.Millisecond
	lifecycleInitializeLimit = 64 << 10

	lifecycleProjectionSlots          = 4
	lifecycleStringHeaderBytes        = int(unsafe.Sizeof(""))
	lifecycleStringSliceHeaderBytes   = int(unsafe.Sizeof([]string(nil)))
	lifecycleProjectionSlotBytes      = int(unsafe.Sizeof(any(nil)))
	lifecycleObjectKeyCapacityBytes   = lifecycleObjectFields * lifecycleStringHeaderBytes
	lifecycleObjectProjectionBytes    = lifecycleProjectionSlots * lifecycleProjectionSlotBytes
	lifecycleObjectRetainedStateBytes = lifecycleStringSliceHeaderBytes +
		lifecycleObjectKeyCapacityBytes + lifecycleObjectProjectionBytes

	// Fixed-reserve upper bound on all non-object state that can coexist:
	//
	//   input buffers (bufio + websocket chunk)       8,192 B
	//   encoded + decoded scalar under construction   7,170 B
	//   64 flag bodies + slice header/capacity        66,584 B
	//   six captured metadata scalar bodies            6,144 B
	//   final body-free summary                         2,048 B
	//   remaining projector/header/control state        8,166 B
	//                                                  --------
	//                                                   98,304 B
	lifecycleRetainedInputReserveBytes    = 2 * lifecycleChunkBytes
	lifecycleRetainedScalarReserveBytes   = lifecycleScalarBytes*7 + 2
	lifecycleRetainedFlagsReserveBytes    = lifecycleObjectFields*(lifecycleScalarBytes+lifecycleStringHeaderBytes) + lifecycleStringSliceHeaderBytes
	lifecycleRetainedMetadataReserveBytes = 6 * lifecycleScalarBytes
	lifecycleRetainedSummaryReserveBytes  = lifecycleSummaryBytes
	lifecycleRetainedResidualReserveBytes = lifecycleRetainedReserve - lifecycleRetainedInputReserveBytes -
		lifecycleRetainedScalarReserveBytes - lifecycleRetainedFlagsReserveBytes -
		lifecycleRetainedMetadataReserveBytes - lifecycleRetainedSummaryReserveBytes
	// lifecycleRetainedReserve conservatively accounts for the bounded input,
	// one encoded/decoded scalar under construction, all captured flag payloads
	// and their slice, the final summary, and residual projector state. Each
	// live object's duplicate-key slice (header and full backing capacity),
	// fixed projection slots, and decoded key bodies are admitted separately.
	// unsafe.Sizeof follows the 64-bit ABI of every supported linux/darwin
	// amd64/arm64 target; allocator span slack, total allocations over the scan,
	// goroutine stack capacity, heap peaks, and RSS are distinct from retained
	// projector state and are not claims made by this cap.
	lifecycleRetainedReserve = 96 << 10
)

// LifecycleEndpoint is one owned, initialized transport whose only provider
// operation is a complete thread/read projected before generic frame
// materialization. It has no notification or approval-response surface.
type LifecycleEndpoint interface {
	ReadLifecycleSnapshot(context.Context, string) (LifecycleSnapshot, error)
	PeerIdentity() PeerIdentity
	Close() error
}

// LifecycleClient is a one-operation app-server connection. Unlike Client it
// is never shared: cancellation and every limit failure abort only this
// transport, while the broker's event/control connection remains untouched.
type LifecycleClient struct {
	stream readWriteCloser
	peer   PeerIdentity
	reader *bufio.Reader
	budget lifecycleBudget
	mu     sync.Mutex
	closed bool
	nextID int64
}

type lifecycleBudget struct {
	jsonBytes    int
	wireBytes    int
	frames       int
	controlBytes int
}

func (b *lifecycleBudget) addWire(count int) error {
	if count < 0 || b.wireBytes > lifecycleWireBytes-count {
		return fmt.Errorf("%w: lifecycle wire limit", ErrPayloadTooLarge)
	}
	b.wireBytes += count
	return nil
}

func (b *lifecycleBudget) addJSON(count int) error {
	if count < 0 || b.jsonBytes > lifecycleJSONBytes-count {
		return fmt.Errorf("%w: lifecycle JSON limit", ErrPayloadTooLarge)
	}
	b.jsonBytes += count
	return nil
}

func (b *lifecycleBudget) addFrame() error {
	if b.frames >= lifecycleFrames {
		return fmt.Errorf("%w: lifecycle frame limit", ErrProtocol)
	}
	b.frames++
	return nil
}

func (b *lifecycleBudget) addControl(count int) error {
	if count < 0 || b.controlBytes > lifecycleControlBytes-count {
		return fmt.Errorf("%w: lifecycle control limit", ErrProtocol)
	}
	b.controlBytes += count
	return nil
}

// PeerIdentity returns the direct Unix server process witness captured before
// WebSocket negotiation.
func (c *LifecycleClient) PeerIdentity() PeerIdentity { return c.peer }

// OpenPrivateUnixLifecycle opens one owned private-route transport and proves
// it reached the same kernel process birth identity as the broker's shared
// connection before trusting any provider bytes.
func OpenPrivateUnixLifecycle(
	ctx context.Context,
	socketPath string,
	projmuxVersion string,
	experimental bool,
	expected PeerIdentity,
) (*LifecycleClient, error) {
	if !expected.Valid() {
		return nil, ErrEndpointChanged
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, classifyProxyOpenError(ctx, err)
	}
	peer, err := peerIdentity(connection)
	if err != nil || !SamePeerIdentity(expected, peer) {
		_ = connection.Close()
		return nil, ErrEndpointChanged
	}
	websocket, err := upgradeProxyWebSocket(ctx, connection)
	if err != nil {
		_ = connection.Close()
		return nil, classifyProxyOpenError(ctx, err)
	}
	client := &LifecycleClient{
		stream: websocket, peer: peer, nextID: 1,
	}
	if err := client.initialize(ctx, projmuxVersion, experimental); err != nil {
		_ = client.Close()
		return nil, classifyProxyOpenError(ctx, err)
	}
	return client, nil
}

func (c *LifecycleClient) initialize(ctx context.Context, version string, experimental bool) error {
	params := initializeParams{ClientInfo: clientInfo{Name: "projmux", Title: "Projmux", Version: safeVersion(version)}}
	if experimental {
		params.Capabilities = &initializeCapabilities{ExperimentalAPI: true}
	}
	id := c.nextID
	c.nextID++
	if err := c.write(ctx, wireRequest{Method: methodInitialize, ID: id, Params: params}); err != nil {
		return err
	}
	frame, err := c.readSmall(ctx, lifecycleInitializeLimit)
	if err != nil {
		return err
	}
	var envelope wireEnvelope
	if json.Unmarshal(frame, &envelope) != nil {
		return fmt.Errorf("%w: invalid initialize response", ErrProtocol)
	}
	var responseID int64
	if json.Unmarshal(envelope.ID, &responseID) != nil || responseID != id || envelope.Method != "" ||
		(len(envelope.Result) == 0) == (envelope.Error == nil) {
		return fmt.Errorf("%w: invalid initialize response", ErrProtocol)
	}
	if envelope.Error != nil {
		return classifyResponseError(envelope.Error)
	}
	var result initializeResult
	if json.Unmarshal(envelope.Result, &result) != nil {
		return fmt.Errorf("%w: invalid initialize result", ErrProtocol)
	}
	if err := c.write(ctx, wireNotification{Method: methodInitialized, Params: struct{}{}}); err != nil {
		return err
	}
	// The 16 MiB JSON budget belongs to the complete thread/read response.
	// Initialize bytes remain in the cumulative wire/frame/control totals, but
	// do not reduce that independent response allowance.
	c.budget.jsonBytes = 0
	return nil
}

// ReadLifecycleSnapshot writes exactly one complete read and incrementally
// projects its response. The operation is not reusable after return: Close is
// part of success, so no notification or late response can escape the owned
// authority boundary.
func (c *LifecycleClient) ReadLifecycleSnapshot(ctx context.Context, threadID string) (LifecycleSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle snapshot thread is empty", ErrProtocol)
	}
	id := c.nextID
	c.nextID++
	if err := c.write(ctx, wireRequest{
		Method: methodThreadRead, ID: id,
		Params: lifecycleThreadReadParams{ThreadID: threadID, IncludeTurns: true},
	}); err != nil {
		return LifecycleSnapshot{}, err
	}

	type result struct {
		snapshot LifecycleSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		input := c.nextInput()
		projector := lifecycleProjector{input: input, requestID: id, threadID: threadID}
		snapshot, err := projector.run(ctx)
		done <- result{snapshot: snapshot, err: err}
	}()
	select {
	case got := <-done:
		return got.snapshot, got.err
	case <-ctx.Done():
		_ = c.abort()
		timer := time.NewTimer(lifecycleCleanupLimit)
		defer timer.Stop()
		select {
		case <-done:
			return LifecycleSnapshot{}, ctx.Err()
		case <-timer.C:
			return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle cleanup timeout", ErrDisconnected)
		}
	}
}

func (c *LifecycleClient) nextInput() lifecycleChunkInput {
	if websocket, ok := c.stream.(*websocketStream); ok {
		return &websocketLifecycleInput{stream: websocket, budget: &c.budget}
	}
	return &jsonLineLifecycleInput{reader: c.reader, budget: &c.budget}
}

func (c *LifecycleClient) readSmall(ctx context.Context, limit int) ([]byte, error) {
	input := c.nextInput()
	type result struct {
		frame []byte
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var frame []byte
		for {
			chunk, err := input.next()
			if errors.Is(err, io.EOF) {
				done <- result{frame: frame}
				return
			}
			if err != nil {
				done <- result{err: err}
				return
			}
			if len(frame) > limit-len(chunk) {
				done <- result{err: fmt.Errorf("%w: lifecycle control message too large", ErrProtocol)}
				return
			}
			frame = append(frame, chunk...)
		}
	}()
	select {
	case got := <-done:
		return got.frame, got.err
	case <-ctx.Done():
		_ = c.abort()
		timer := time.NewTimer(lifecycleCleanupLimit)
		defer timer.Stop()
		select {
		case <-done:
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("%w: lifecycle cleanup timeout", ErrDisconnected)
		}
	}
}

func (c *LifecycleClient) write(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil || len(payload)+1 > maxFrameBytes {
		return fmt.Errorf("%w: lifecycle request frame", ErrProtocol)
	}
	payload = append(payload, '\n')
	done := make(chan error, 1)
	go func() {
		_, writeErr := c.stream.Write(payload)
		done <- writeErr
	}()
	select {
	case err := <-done:
		if err != nil {
			return ErrDisconnected
		}
		return nil
	case <-ctx.Done():
		_ = c.abort()
		timer := time.NewTimer(lifecycleCleanupLimit)
		defer timer.Stop()
		select {
		case <-done:
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("%w: lifecycle cleanup timeout", ErrDisconnected)
		}
	}
}

func (c *LifecycleClient) abort() error {
	if aborter, ok := c.stream.(streamAborter); ok {
		return aborter.abort()
	}
	return c.stream.Close()
}

func (c *LifecycleClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.abort()
}

type lifecycleChunkInput interface {
	next() ([]byte, error)
}

type jsonLineLifecycleInput struct {
	reader *bufio.Reader
	budget *lifecycleBudget
	done   bool
	first  bool
}

func (in *jsonLineLifecycleInput) next() ([]byte, error) {
	if in.done {
		return nil, io.EOF
	}
	if !in.first {
		in.first = true
		if err := in.budget.addFrame(); err != nil {
			return nil, err
		}
	}
	chunk, err := in.reader.ReadSlice('\n')
	if wireErr := in.budget.addWire(len(chunk)); wireErr != nil {
		return nil, wireErr
	}
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		in.done = true
		chunk = chunk[:len(chunk)-1]
		err = nil
	}
	if jsonErr := in.budget.addJSON(len(chunk)); jsonErr != nil {
		return nil, jsonErr
	}
	if errors.Is(err, bufio.ErrBufferFull) {
		return chunk, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: unterminated lifecycle JSONL frame", ErrProtocol)
	}
	return chunk, nil
}

type websocketLifecycleInput struct {
	stream        *websocketStream
	budget        *lifecycleBudget
	started       bool
	done          bool
	frameRemain   uint64
	frameFinal    bool
	frameData     bool
	messageOpcode byte
	buffer        [lifecycleChunkBytes]byte
}

func (in *websocketLifecycleInput) next() ([]byte, error) {
	for {
		if in.done {
			return nil, io.EOF
		}
		if in.frameRemain > 0 {
			count := min(uint64(len(in.buffer)), in.frameRemain)
			if _, err := io.ReadFull(in.stream.reader, in.buffer[:count]); err != nil {
				return nil, err
			}
			in.frameRemain -= count
			if err := in.budget.addWire(int(count)); err != nil {
				return nil, err
			}
			if in.frameData {
				if err := in.budget.addJSON(int(count)); err != nil {
					return nil, err
				}
				if in.frameRemain == 0 && in.frameFinal {
					in.done = true
				}
				return in.buffer[:count], nil
			}
			if err := in.budget.addControl(int(count)); err != nil {
				return nil, err
			}
			continue
		}

		fin, opcode, length, headerBytes, err := in.readHeader()
		if err != nil {
			return nil, err
		}
		if err := in.budget.addFrame(); err != nil {
			return nil, err
		}
		if err := in.budget.addWire(headerBytes); err != nil {
			return nil, err
		}
		if opcode >= 0x8 {
			if !fin || length > 125 {
				return nil, fmt.Errorf("%w: invalid websocket control frame", ErrProtocol)
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(in.stream.reader, payload); err != nil {
				return nil, err
			}
			if err := in.budget.addWire(int(length)); err != nil {
				return nil, err
			}
			if err := in.budget.addControl(int(length)); err != nil {
				return nil, err
			}
			switch opcode {
			case 0x8:
				return nil, io.EOF
			case 0x9:
				if err := in.stream.writeFrame(0xA, payload); err != nil {
					return nil, err
				}
			case 0xA:
			default:
				return nil, fmt.Errorf("%w: invalid websocket control opcode", ErrProtocol)
			}
			continue
		}
		if opcode == 0x1 {
			if in.started {
				return nil, fmt.Errorf("%w: nested websocket message", ErrProtocol)
			}
			in.started = true
			in.messageOpcode = opcode
		} else if opcode != 0x0 || !in.started || in.messageOpcode != 0x1 {
			return nil, fmt.Errorf("%w: invalid websocket continuation", ErrProtocol)
		}
		in.frameRemain = length
		in.frameFinal = fin
		in.frameData = true
		if length == 0 {
			if fin {
				in.done = true
				return nil, io.EOF
			}
			continue
		}
	}
}

func (in *websocketLifecycleInput) readHeader() (bool, byte, uint64, int, error) {
	var header [2]byte
	if _, err := io.ReadFull(in.stream.reader, header[:]); err != nil {
		return false, 0, 0, 0, err
	}
	fin, opcode := header[0]&0x80 != 0, header[0]&0x0f
	if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
		return false, 0, 0, 0, fmt.Errorf("%w: invalid websocket frame flags", ErrProtocol)
	}
	length := uint64(header[1] & 0x7f)
	headerBytes := 2
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(in.stream.reader, extended[:]); err != nil {
			return false, 0, 0, 0, err
		}
		length, headerBytes = uint64(binary.BigEndian.Uint16(extended[:])), 4
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(in.stream.reader, extended[:]); err != nil {
			return false, 0, 0, 0, err
		}
		length, headerBytes = binary.BigEndian.Uint64(extended[:]), 10
	}
	return fin, opcode, length, headerBytes, nil
}

type lifecycleMode uint8

const (
	modeIgnored lifecycleMode = iota
	modeRoot
	modeResult
	modeThread
	modeStatus
	modeTurns
	modeTurn
	modeFlags
	modeScalar
	modeError
)

type lifecycleProjector struct {
	input     lifecycleChunkInput
	requestID int64
	threadID  string
	ctx       context.Context
	buffer    []byte
	scalarBuf [lifecycleScalarBytes*6 + 2]byte
	pos       int
	values    int
	retained  int
}

type projectedTurn struct {
	id        any
	status    any
	startedAt any
	present   bool
}

type projectedRPCError struct {
	code    any
	message any
}

type projectedResult struct{ thread any }
type lifecycleProjectionFields [lifecycleProjectionSlots]any

var lifecycleNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func (p *lifecycleProjector) run(ctx context.Context) (LifecycleSnapshot, error) {
	p.ctx = ctx
	value, err := p.value(modeRoot, 0)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	if err := p.space(); err != nil && !errors.Is(err, io.EOF) {
		return LifecycleSnapshot{}, err
	}
	if next, err := p.peek(); err == nil || !errors.Is(err, io.EOF) || next != 0 {
		return LifecycleSnapshot{}, fmt.Errorf("%w: trailing lifecycle JSON", ErrProtocol)
	}
	snapshot, ok := value.(LifecycleSnapshot)
	if !ok {
		return LifecycleSnapshot{}, fmt.Errorf("%w: missing lifecycle snapshot", ErrProtocol)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > lifecycleSummaryBytes {
		return LifecycleSnapshot{}, fmt.Errorf("%w: lifecycle summary limit", ErrProtocol)
	}
	if err := ctx.Err(); err != nil {
		return LifecycleSnapshot{}, err
	}
	return snapshot, nil
}

func (p *lifecycleProjector) check() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if p.retained > lifecycleRetainedBytes-lifecycleRetainedReserve {
		return fmt.Errorf("%w: lifecycle retained-state limit", ErrProtocol)
	}
	return nil
}

func (p *lifecycleProjector) retain(count int) error {
	if count < 0 || p.retained > lifecycleRetainedBytes-lifecycleRetainedReserve-count {
		return fmt.Errorf("%w: lifecycle retained-state limit", ErrProtocol)
	}
	p.retained += count
	return nil
}

func (p *lifecycleProjector) available() error {
	if p.pos < len(p.buffer) {
		return nil
	}
	if err := p.check(); err != nil {
		return err
	}
	chunk, err := p.input.next()
	if err != nil {
		return err
	}
	p.buffer, p.pos = chunk, 0
	if len(chunk) == 0 {
		return p.available()
	}
	return p.check()
}

func (p *lifecycleProjector) peek() (byte, error) {
	if err := p.available(); err != nil {
		return 0, err
	}
	return p.buffer[p.pos], nil
}

func (p *lifecycleProjector) take() (byte, error) {
	value, err := p.peek()
	if err != nil {
		return 0, err
	}
	p.pos++
	return value, nil
}

func (p *lifecycleProjector) expect(want byte) error {
	got, err := p.take()
	if err != nil || got != want {
		return fmt.Errorf("%w: malformed lifecycle JSON", ErrProtocol)
	}
	return nil
}

func (p *lifecycleProjector) space() error {
	for {
		value, err := p.peek()
		if err != nil {
			return err
		}
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return nil
		}
		p.pos++
	}
}

func (p *lifecycleProjector) value(mode lifecycleMode, depth int) (any, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	p.values++
	if p.values > lifecycleValues {
		return nil, fmt.Errorf("%w: lifecycle value limit", ErrProtocol)
	}
	if depth > lifecycleDepth {
		return nil, fmt.Errorf("%w: lifecycle depth limit", ErrProtocol)
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	first, err := p.peek()
	if err != nil {
		return nil, fmt.Errorf("%w: malformed lifecycle JSON", ErrProtocol)
	}
	if mode == modeRoot || mode == modeResult || mode == modeThread || mode == modeStatus || mode == modeTurn || mode == modeError {
		if first != '{' {
			return nil, fmt.Errorf("%w: lifecycle metadata shape", ErrProtocol)
		}
	}
	if mode == modeTurns || mode == modeFlags {
		if first != '[' {
			return nil, fmt.Errorf("%w: lifecycle metadata shape", ErrProtocol)
		}
	}
	if mode == modeScalar && (first == '{' || first == '[') {
		return nil, fmt.Errorf("%w: lifecycle metadata shape", ErrProtocol)
	}
	switch first {
	case '{':
		return p.object(mode, depth)
	case '[':
		return p.array(mode, depth)
	default:
		return p.scalar(mode == modeScalar)
	}
}

func (p *lifecycleProjector) object(mode lifecycleMode, depth int) (any, error) {
	if err := p.expect('{'); err != nil {
		return nil, err
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	if err := p.retain(lifecycleObjectRetainedStateBytes); err != nil {
		return nil, err
	}
	retained := lifecycleObjectRetainedStateBytes
	defer func() { p.retained -= retained }()
	keys := make([]string, 0, lifecycleObjectFields)
	var fields lifecycleProjectionFields
	first, err := p.peek()
	if err != nil {
		return nil, err
	}
	if first != '}' {
		for {
			if err := p.space(); err != nil {
				return nil, err
			}
			key, err := p.string(true)
			if err != nil {
				return nil, err
			}
			if hasLifecycleKey(keys, key) {
				return nil, fmt.Errorf("%w: duplicate lifecycle JSON key", ErrProtocol)
			}
			if len(keys) >= lifecycleObjectFields {
				return nil, fmt.Errorf("%w: lifecycle object limit", ErrProtocol)
			}
			if err := p.retain(len(key)); err != nil {
				return nil, err
			}
			keys = append(keys, key)
			retained += len(key)
			if err := p.space(); err != nil {
				return nil, err
			}
			if err := p.expect(':'); err != nil {
				return nil, err
			}
			wanted := wantedLifecycleMode(mode, key)
			value, err := p.value(wanted, depth+1)
			if err != nil {
				return nil, err
			}
			if wanted != modeIgnored {
				index := lifecycleProjectionFieldIndex(mode, key)
				if index < 0 {
					return nil, fmt.Errorf("%w: lifecycle projection field", ErrProtocol)
				}
				fields[index] = value
			}
			if err := p.space(); err != nil {
				return nil, err
			}
			next, err := p.peek()
			if err != nil {
				return nil, err
			}
			if next != ',' {
				break
			}
			p.pos++
		}
	}
	if err := p.expect('}'); err != nil {
		return nil, err
	}
	return p.projectObject(mode, keys, fields)
}

func hasLifecycleKey(keys []string, want string) bool {
	return slices.Contains(keys, want)
}

func lifecycleProjectionFieldIndex(mode lifecycleMode, key string) int {
	switch mode {
	case modeRoot:
		switch key {
		case "id":
			return 0
		case "jsonrpc":
			return 1
		case "result":
			return 2
		case "error":
			return 3
		}
	case modeResult:
		if key == "thread" {
			return 0
		}
	case modeThread:
		switch key {
		case "id":
			return 0
		case "status":
			return 1
		case "turns":
			return 2
		}
	case modeStatus:
		if key == "type" {
			return 0
		}
		if key == "activeFlags" {
			return 1
		}
	case modeTurn:
		switch key {
		case "id":
			return 0
		case "status":
			return 1
		case "startedAt":
			return 2
		}
	case modeError:
		if key == "code" {
			return 0
		}
		if key == "message" {
			return 1
		}
	}
	return -1
}

func wantedLifecycleMode(mode lifecycleMode, key string) lifecycleMode {
	switch mode {
	case modeRoot:
		switch key {
		case "id", "jsonrpc":
			return modeScalar
		case "result":
			return modeResult
		case "error":
			return modeError
		}
	case modeResult:
		if key == "thread" {
			return modeThread
		}
	case modeThread:
		switch key {
		case "id":
			return modeScalar
		case "status":
			return modeStatus
		case "turns":
			return modeTurns
		}
	case modeStatus:
		if key == "type" {
			return modeScalar
		}
		if key == "activeFlags" {
			return modeFlags
		}
	case modeTurn:
		if key == "id" || key == "status" || key == "startedAt" {
			return modeScalar
		}
	case modeError:
		if key == "code" || key == "message" {
			return modeScalar
		}
	}
	return modeIgnored
}

func (p *lifecycleProjector) projectObject(mode lifecycleMode, keys []string, fields lifecycleProjectionFields) (any, error) {
	switch mode {
	case modeRoot:
		if hasLifecycleKey(keys, "method") {
			return nil, fmt.Errorf("%w: owned lifecycle notification refused", ErrProtocol)
		}
		if hasLifecycleKey(keys, "params") {
			return nil, fmt.Errorf("%w: owned lifecycle server request refused", ErrProtocol)
		}
		id, ok := fields[0].(float64)
		if !ok || math.Trunc(id) != id || id != float64(p.requestID) {
			return nil, fmt.Errorf("%w: lifecycle response identity", ErrProtocol)
		}
		if jsonrpc := fields[1]; hasLifecycleKey(keys, "jsonrpc") && jsonrpc != "2.0" {
			return nil, fmt.Errorf("%w: lifecycle response version", ErrProtocol)
		}
		result, hasResult := fields[2], hasLifecycleKey(keys, "result")
		rpcErr, hasError := fields[3], hasLifecycleKey(keys, "error")
		if hasResult == hasError {
			return nil, fmt.Errorf("%w: lifecycle response shape", ErrProtocol)
		}
		if hasError {
			failure := rpcErr.(projectedRPCError)
			return nil, projectedResponseError(failure)
		}
		projected := result.(projectedResult)
		thread, ok := projected.thread.(LifecycleSnapshot)
		if !ok {
			return nil, fmt.Errorf("%w: lifecycle response thread", ErrProtocol)
		}
		return thread, nil
	case modeResult:
		thread := fields[0]
		if !hasLifecycleKey(keys, "thread") {
			return nil, fmt.Errorf("%w: lifecycle result thread", ErrProtocol)
		}
		return projectedResult{thread: thread}, nil
	case modeThread:
		id, ok := fields[0].(string)
		if !ok || !validLifecycleIdentity(id) || id != p.threadID {
			return nil, fmt.Errorf("%w: lifecycle thread identity", ErrProtocol)
		}
		state, ok := fields[1].(ThreadState)
		if !ok || state == ThreadStateUnknown {
			return nil, fmt.Errorf("%w: lifecycle thread status", ErrProtocol)
		}
		turns, ok := fields[2].(struct {
			count  int
			latest *projectedTurn
		})
		if !ok {
			return nil, fmt.Errorf("%w: lifecycle turns unobserved", ErrProtocol)
		}
		snapshot := LifecycleSnapshot{ThreadID: id, ThreadState: state, TurnCount: turns.count}
		if turns.latest != nil {
			turnID, ok := turns.latest.id.(string)
			if !ok || !validLifecycleIdentity(turnID) {
				return nil, fmt.Errorf("%w: lifecycle latest turn identity", ErrProtocol)
			}
			turnState, ok := turns.latest.status.(string)
			if !ok {
				return nil, fmt.Errorf("%w: lifecycle latest turn status", ErrProtocol)
			}
			snapshot.TurnID = turnID
			snapshot.TurnState = normalizeTurnState(turnState)
			if snapshot.TurnState == TurnStateUnknown {
				return nil, fmt.Errorf("%w: lifecycle latest turn status", ErrProtocol)
			}
			if turns.latest.present && turns.latest.startedAt != nil {
				started, ok := turns.latest.startedAt.(float64)
				if !ok || math.IsNaN(started) || math.IsInf(started, 0) {
					return nil, fmt.Errorf("%w: lifecycle latest startedAt", ErrProtocol)
				}
				snapshot.StartedAt = unixSeconds(started)
			}
		}
		return snapshot, nil
	case modeStatus:
		kind, ok := fields[0].(string)
		if !ok {
			return ThreadStateUnknown, nil
		}
		status := lifecycleThreadStatus{Type: kind}
		if flags, ok := fields[1].([]string); ok {
			status.ActiveFlags = flags
		}
		return normalizeThreadState(status), nil
	case modeTurn:
		return projectedTurn{id: fields[0], status: fields[1], startedAt: fields[2], present: hasLifecycleKey(keys, "startedAt")}, nil
	case modeError:
		return projectedRPCError{code: fields[0], message: fields[1]}, nil
	}
	return nil, nil
}

func projectedResponseError(failure projectedRPCError) error {
	codeValue, codeOK := failure.code.(float64)
	message, _ := failure.message.(string)
	if !codeOK || math.Trunc(codeValue) != codeValue {
		return fmt.Errorf("%w: invalid lifecycle RPC error", ErrProtocol)
	}
	code := int(codeValue)
	if code == -32601 {
		return ErrUnsupported
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(message), " "))
	switch {
	case strings.Contains(normalized, "no rollout found"):
		return &responseError{code: code, kind: ErrThreadNotDurable}
	case strings.Contains(normalized, "thread not found"), strings.Contains(normalized, "thread id not found"),
		strings.Contains(normalized, "unknown thread"), strings.Contains(normalized, "could not find thread"):
		return &responseError{code: code, kind: ErrThreadAbsent}
	default:
		return &responseError{code: code}
	}
}

func (p *lifecycleProjector) array(mode lifecycleMode, depth int) (any, error) {
	if err := p.expect('['); err != nil {
		return nil, err
	}
	if err := p.space(); err != nil {
		return nil, err
	}
	count := 0
	var latest *projectedTurn
	var flags []string
	if mode == modeFlags {
		// The full backing capacity is covered by lifecycleRetainedReserve.
		flags = make([]string, 0, lifecycleObjectFields)
	}
	first, err := p.peek()
	if err != nil {
		return nil, err
	}
	if first != ']' {
		for {
			count++
			if mode == modeTurns && count > lifecycleTurns {
				return nil, fmt.Errorf("%w: lifecycle turn limit", ErrProtocol)
			}
			if mode == modeFlags && count > lifecycleObjectFields {
				return nil, fmt.Errorf("%w: lifecycle flag limit", ErrProtocol)
			}
			itemMode := modeIgnored
			if mode == modeTurns {
				itemMode = modeTurn
			} else if mode == modeFlags {
				itemMode = modeScalar
			}
			value, err := p.value(itemMode, depth+1)
			if err != nil {
				return nil, err
			}
			if mode == modeTurns {
				turn := value.(projectedTurn)
				latest = &turn
			} else if mode == modeFlags {
				flag, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("%w: lifecycle active flag", ErrProtocol)
				}
				flags = append(flags, flag)
			}
			if err := p.space(); err != nil {
				return nil, err
			}
			next, err := p.peek()
			if err != nil {
				return nil, err
			}
			if next != ',' {
				break
			}
			p.pos++
		}
	}
	if err := p.expect(']'); err != nil {
		return nil, err
	}
	if mode == modeTurns {
		return struct {
			count  int
			latest *projectedTurn
		}{count: count, latest: latest}, nil
	}
	if mode == modeFlags {
		return flags, nil
	}
	return nil, nil
}

func (p *lifecycleProjector) scalar(capture bool) (any, error) {
	first, err := p.peek()
	if err != nil {
		return nil, err
	}
	if first == '"' {
		if !capture {
			_, err := p.string(false)
			return nil, err
		}
		value, err := p.string(true)
		return value, err
	}
	var token []byte
	for {
		value, err := p.peek()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if bytes.ContainsRune([]byte(" \t\r\n,]}"), rune(value)) {
			break
		}
		if len(token) >= lifecycleScalarBytes {
			return nil, fmt.Errorf("%w: lifecycle scalar limit", ErrProtocol)
		}
		token = append(token, value)
		p.pos++
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("%w: malformed lifecycle scalar", ErrProtocol)
	}
	text := string(token)
	if text == "null" {
		if capture {
			return nil, nil
		}
		return nil, nil
	}
	if text == "true" || text == "false" {
		if !capture {
			return nil, nil
		}
		return text == "true", nil
	}
	if !lifecycleNumber.MatchString(text) {
		return nil, fmt.Errorf("%w: malformed lifecycle scalar", ErrProtocol)
	}
	number, err := json.Number(text).Float64()
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return nil, fmt.Errorf("%w: invalid lifecycle number", ErrProtocol)
	}
	if capture {
		return number, nil
	}
	return nil, nil
}

func (p *lifecycleProjector) string(capture bool) (string, error) {
	if err := p.expect('"'); err != nil {
		return "", err
	}
	var raw []byte
	if capture {
		// Reuse one projector-owned buffer so short keys do not allocate a 6 KiB
		// temporary each. json.Unmarshal copies the decoded string before this
		// scratch space is reused. Its exact capacity and that decoded copy are
		// both covered by the fixed scalar reserve.
		raw = p.scalarBuf[:1]
		raw[0] = '"'
	}
	appendRaw := func(value byte) error {
		if len(raw) >= cap(raw) {
			return fmt.Errorf("%w: lifecycle scalar limit", ErrProtocol)
		}
		raw = append(raw, value)
		return nil
	}
	for {
		value, err := p.take()
		if err != nil {
			return "", fmt.Errorf("%w: malformed lifecycle string", ErrProtocol)
		}
		switch {
		case value == '"':
			if !capture {
				return "", nil
			}
			if err := appendRaw('"'); err != nil {
				return "", err
			}
			var decoded string
			if json.Unmarshal(raw, &decoded) != nil || len(decoded) > lifecycleScalarBytes {
				return "", fmt.Errorf("%w: malformed lifecycle string", ErrProtocol)
			}
			return decoded, nil
		case value == '\\':
			escape, err := p.take()
			if err != nil || !bytes.ContainsRune([]byte(`"\\/bfnrtu`), rune(escape)) {
				return "", fmt.Errorf("%w: malformed lifecycle escape", ErrProtocol)
			}
			if capture {
				if err := appendRaw(value); err != nil {
					return "", err
				}
				if err := appendRaw(escape); err != nil {
					return "", err
				}
			}
			if escape == 'u' {
				for range 4 {
					digit, err := p.take()
					if err != nil || !isHex(digit) {
						return "", fmt.Errorf("%w: malformed lifecycle unicode escape", ErrProtocol)
					}
					if capture {
						if err := appendRaw(digit); err != nil {
							return "", err
						}
					}
				}
			}
		case value < 0x20:
			return "", fmt.Errorf("%w: malformed lifecycle string", ErrProtocol)
		case value < utf8.RuneSelf:
			if capture {
				if err := appendRaw(value); err != nil {
					return "", err
				}
			}
		default:
			width := utf8SequenceWidth(value)
			if width == 0 {
				return "", fmt.Errorf("%w: malformed lifecycle UTF-8", ErrProtocol)
			}
			sequence := [utf8.UTFMax]byte{value}
			for index := 1; index < width; index++ {
				next, err := p.take()
				if err != nil {
					return "", fmt.Errorf("%w: malformed lifecycle UTF-8", ErrProtocol)
				}
				sequence[index] = next
			}
			if !utf8.Valid(sequence[:width]) {
				return "", fmt.Errorf("%w: malformed lifecycle UTF-8", ErrProtocol)
			}
			if capture {
				for _, part := range sequence[:width] {
					if err := appendRaw(part); err != nil {
						return "", err
					}
				}
			}
		}
		// A JSON escape can use six wire bytes for one retained byte. Bound the
		// temporary encoded scalar as well as the decoded scalar checked above.
		if capture && len(raw) > lifecycleScalarBytes*6+1 {
			return "", fmt.Errorf("%w: lifecycle scalar limit", ErrProtocol)
		}
		if err := p.check(); err != nil {
			return "", err
		}
	}
}

func utf8SequenceWidth(first byte) int {
	switch {
	case first >= 0xC2 && first <= 0xDF:
		return 2
	case first >= 0xE0 && first <= 0xEF:
		return 3
	case first >= 0xF0 && first <= 0xF4:
		return 4
	default:
		return 0
	}
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func validLifecycleIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]byte(value)) <= lifecycleIdentityBytes
}
