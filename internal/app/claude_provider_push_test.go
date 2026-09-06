package app

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

type singleWriteRecorder struct {
	writes int
	n      int
	err    error
}

func TestClaudeProviderPushFinalRouteCheckPrecedesSoleWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := inspectClaudeSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	process, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	readBytes := make(chan int, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			readBytes <- -1
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, 1)
		n, _ := connection.Read(buffer)
		readBytes <- n
	}()
	checks := 0
	poster := &liveClaudeProviderPoster{socket: path, token: "nonsecret-test-token", socketIdentity: identity,
		process: process, current: func() bool { return true }}
	outcome, err := poster.Post("marker", func() bool { checks++; return checks < 3 })
	if err == nil || outcome.WroteAny || outcome.FullFrameWritten || checks != 3 {
		t.Fatalf("outcome=%+v err=%v route checks=%d", outcome, err, checks)
	}
	if n := <-readBytes; n != 0 {
		t.Fatalf("provider received %d bytes after final route became stale", n)
	}
}

func (w *singleWriteRecorder) Write(payload []byte) (int, error) {
	w.writes++
	n := w.n
	if n == -1 {
		n = len(payload)
	}
	return n, w.err
}

func TestClaudeFrozenProviderFrameIsExactAndEscapesContent(t *testing.T) {
	frame, err := buildClaudeProviderPushFrame("opaque-token", "marker:\"one\"")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"type\":\"auth\",\"token\":\"opaque-token\"}\n" +
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"marker:\\\"one\\\"\"}}\n"
	if string(frame) != want {
		t.Fatalf("frame = %q, want %q", frame, want)
	}
	if strings.Count(string(frame), "\n") != 2 {
		t.Fatalf("frame is not exactly two newline-delimited records: %q", frame)
	}
}

func TestClaudeProviderPushUsesOneWriteAndClassifiesUnknownOutcome(t *testing.T) {
	for _, test := range []struct {
		name      string
		n         int
		err       error
		full      bool
		wroteAny  bool
		ambiguous bool
	}{
		{name: "zero byte known failure", n: 0, err: errors.New("closed")},
		{name: "invalid negative count is known failure", n: -2, err: errors.New("invalid")},
		{name: "invalid oversized count is ambiguous", n: 6, err: errors.New("invalid"), wroteAny: true, ambiguous: true},
		{name: "partial is ambiguous", n: 2, err: errors.New("short"), wroteAny: true, ambiguous: true},
		{name: "full with error is ambiguous", n: -1, err: errors.New("post-write close"), wroteAny: true, ambiguous: true},
		{name: "full write is helper handoff", n: -1, full: true, wroteAny: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &singleWriteRecorder{n: test.n, err: test.err}
			outcome := writeClaudeProviderPushFrame(writer, []byte("frame"))
			if writer.writes != 1 || outcome.FullFrameWritten != test.full || outcome.WroteAny != test.wroteAny || outcome.Ambiguous() != test.ambiguous {
				t.Fatalf("writes=%d outcome=%+v", writer.writes, outcome)
			}
		})
	}
}

func TestClaudeFrozenProviderFrameRejectsCredentialAndPayloadBoundary(t *testing.T) {
	for _, input := range []struct{ token, content string }{
		{token: "", content: "x"},
		{token: "bad\ntoken", content: "x"},
		{token: "token", content: ""},
		{token: strings.Repeat("t", 4096), content: strings.Repeat("x", claudeProviderFrameMaxBytes-4096)},
		{token: "token", content: strings.Repeat("x", claudeProviderFrameMaxBytes)},
	} {
		if frame, err := buildClaudeProviderPushFrame(input.token, input.content); err == nil || frame != nil {
			t.Fatalf("buildClaudeProviderPushFrame(%q, %d bytes) = %q, %v", input.token, len(input.content), frame, err)
		}
	}
}
