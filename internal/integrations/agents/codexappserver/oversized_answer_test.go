package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// oversizedFrame renders one JSONL frame whose byte count is past the client's
// retained bound. The payload is filler rather than a real answer because the
// point of the frame is its size: nothing downstream ever parses it.
func oversizedFrame() []byte {
	return append([]byte(`{"id":1,"result":{"filler":"`+strings.Repeat("x", maxFrameBytes+4096)+`"}}`), '\n')
}

// TestClientDropsAnOversizedAnswerAndKeepsTheConnection pins the blast radius
// of one unreadable answer. The upstream connection is multiplexed across
// every bound thread, so failing it here suspends every binding and forces a
// reconnect that re-issues the same read; that is the cycle this test exists
// to keep out.
func TestClientDropsAnOversizedAnswerAndKeepsTheConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		reader := bufio.NewReader(serverConn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		_, _ = serverConn.Write(oversizedFrame())
		_, _ = serverConn.Write([]byte(`{"method":"thread/status/changed","params":{"threadId":"thr-one"}}` + "\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result map[string]any
	err := client.Request(ctx, "thread/read", map[string]any{"threadId": "thr-one"}, &result)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("request answered by an oversized frame = %v, want ErrPayloadTooLarge", err)
	}
	if errors.Is(err, ErrDisconnected) || errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized answer reported as a connection fault: %v", err)
	}

	select {
	case notification, open := <-client.Notifications():
		if !open {
			t.Fatal("notification stream closed; the oversized answer ended the connection")
		}
		if notification.Method != "thread/status/changed" {
			t.Fatalf("notification after the dropped frame = %q", notification.Method)
		}
	case <-ctx.Done():
		t.Fatal("no notification arrived after the oversized frame was dropped")
	}
	if got := client.OversizedAnswers(); got != 1 {
		t.Fatalf("OversizedAnswers() = %d, want 1", got)
	}
}

// TestClientKeepsServingRequestsAfterAnOversizedAnswer proves the drop is
// request-scoped: the connection stays usable, so a later request on the same
// connection still gets its answer.
func TestClientKeepsServingRequestsAfterAnOversizedAnswer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		reader := bufio.NewReader(serverConn)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		_, _ = serverConn.Write(oversizedFrame())
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		_, _ = serverConn.Write([]byte(`{"id":2,"result":{"ok":true}}` + "\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dropped map[string]any
	if err := client.Request(ctx, "thread/read", nil, &dropped); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("first request = %v, want ErrPayloadTooLarge", err)
	}
	var answered struct {
		OK bool `json:"ok"`
	}
	if err := client.Request(ctx, "thread/read", nil, &answered); err != nil {
		t.Fatalf("request after the dropped answer = %v, want a served answer", err)
	}
	if !answered.OK {
		t.Fatal("request after the dropped answer returned an empty result")
	}
}

// TestWebsocketStreamDropsAnOversizedMessageAndStaysFramed keeps the drop
// honest at the framing layer: the discarded message is consumed to its exact
// byte count, so the message behind it is read whole rather than as garbage.
func TestWebsocketStreamDropsAnOversizedMessageAndStaysFramed(t *testing.T) {
	var wire bytes.Buffer
	writeServerTextFrame(&wire, bytes.Repeat([]byte("x"), maxFrameBytes+1))
	writeServerTextFrame(&wire, []byte(`{"method":"thread/status/changed"}`))
	stream := &websocketStream{raw: nopReadWriteCloser{}, reader: bufio.NewReader(&wire)}

	payload, oversize, err := stream.readBoundedText(maxFrameBytes)
	if err != nil || !oversize || payload != nil {
		t.Fatalf("oversized message = (%q, %v, %v), want (nil, true, nil)", payload, oversize, err)
	}
	payload, oversize, err = stream.readBoundedText(maxFrameBytes)
	if err != nil || oversize {
		t.Fatalf("message behind the dropped one = (%v, %v)", oversize, err)
	}
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.Method != "thread/status/changed" {
		t.Fatalf("message behind the dropped one = %q, want the next whole frame", payload)
	}
}

// writeServerTextFrame appends one unmasked server-to-client text frame.
func writeServerTextFrame(buffer *bytes.Buffer, payload []byte) {
	switch {
	case len(payload) <= 125:
		buffer.Write([]byte{0x81, byte(len(payload))})
	case len(payload) <= 65535:
		var extended [2]byte
		binary.BigEndian.PutUint16(extended[:], uint16(len(payload)))
		buffer.Write(append([]byte{0x81, 126}, extended[:]...))
	default:
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(len(payload)))
		buffer.Write(append([]byte{0x81, 127}, extended[:]...))
	}
	buffer.Write(payload)
}

type nopReadWriteCloser struct{}

func (nopReadWriteCloser) Read([]byte) (int, error)    { return 0, nil }
func (nopReadWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopReadWriteCloser) Close() error                { return nil }
