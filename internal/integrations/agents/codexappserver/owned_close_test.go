package codexappserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// This child owns only its stdio pipes. It never opens an endpoint, reads
// credentials, or starts another process, and deliberately never reads stdin.
func TestOwnedProxyCloseHelperProcess(t *testing.T) {
	if os.Getenv("PROJMUX_TEST_OWNED_PROXY_CLOSE") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte{1})
	time.Sleep(time.Hour)
	os.Exit(0)
}

func fullOwnedProxyPipe(t *testing.T) *commandStream {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedProxyCloseHelperProcess$")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PROJMUX_TEST_OWNED_PROXY_CLOSE=1"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stream := &commandStream{stdin: stdin, stdout: stdout, cmd: cmd}
	t.Cleanup(func() { _ = stream.Close() })
	var ready [1]byte
	if _, err := io.ReadFull(stdout, ready[:]); err != nil {
		t.Fatal(err)
	}
	fd := int(stdin.(*os.File).Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	fill := make([]byte, 4096)
	for {
		_, err := unix.Write(fd, fill)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// The close frame is smaller than PIPE_BUF: finish any remaining pipe
	// capacity, then restore the descriptor's blocking mode before the test.
	for {
		_, err := unix.Write(fd, []byte{0})
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	return stream
}

func requireOwnedProxyClosed(t *testing.T, client *Client, stream *commandStream) {
	t.Helper()
	started := time.Now()
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- client.Close() }()
	}
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			// Rescue only this exact fixture; do not leave the failed test's
			// blocked Close or writer alive after reporting the regression.
			_ = stream.Close()
			<-done
			t.Fatal("owned proxy Close exceeded one second")
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close elapsed=%v", elapsed)
	}
	if stream.cmd.ProcessState == nil {
		t.Fatal("Close returned before owned child was reaped")
	}
	if err := stream.cmd.Process.Signal(unix.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("owned child is not reaped: %v", err)
	}
	select {
	case <-client.readerDone:
	default:
		t.Fatal("Close left its reader running")
	}
	if _, ok := <-client.Notifications(); ok {
		t.Fatal("notification channel remained open")
	}
	if err := client.Request(context.Background(), "after-close", nil, nil); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("closed client request=%v", err)
	}
	_ = client.Close()
}

func TestOwnedProxyCloseIsBoundedWhenPeerDoesNotRead(t *testing.T) {
	stream := fullOwnedProxyPipe(t)
	websocket := &websocketStream{raw: stream, reader: bufio.NewReader(stream)}
	client := NewClient(websocket)
	requireOwnedProxyClosed(t, client, stream)
}

type witnessedProxyWrite struct {
	*commandStream
	once     sync.Once
	started  chan struct{}
	finished chan struct{}
}

func (s *witnessedProxyWrite) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	n, err := s.commandStream.Write(p)
	select {
	case s.finished <- struct{}{}:
	default:
	}
	return n, err
}

func TestOwnedProxyCloseUnblocksConcurrentWriterAndReapsChild(t *testing.T) {
	stream := fullOwnedProxyPipe(t)
	raw := &witnessedProxyWrite{commandStream: stream, started: make(chan struct{}), finished: make(chan struct{}, 3)}
	websocket := &websocketStream{raw: raw, reader: bufio.NewReader(raw)}
	client := NewClient(websocket)
	requests := make(chan error, 2)
	go func() { requests <- client.Request(context.Background(), "blocked-first", nil, nil) }()
	<-raw.started
	go func() { requests <- client.Request(context.Background(), "queued-second", nil, nil) }()
	control := make(chan error, 1)
	go func() { control <- websocket.writeFrame(0xA, nil) }()
	requireOwnedProxyClosed(t, client, stream)
	for range 2 {
		select {
		case err := <-requests:
			if !errors.Is(err, ErrDisconnected) {
				t.Fatalf("writer result=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("request writer did not finish")
		}
	}
	select {
	case <-raw.finished:
	default:
		t.Fatal("Close returned before pipe writer finished")
	}
	select {
	case err := <-control:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("control writer result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("websocket lock waiter did not finish")
	}
}
