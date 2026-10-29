package localipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/testutil/socketreuse"
)

func TestOwnedSocketLifecycleAndSingleJSONBoundaries(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-localipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "private", "test.sock")
	listener, err := Listen(path)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir info=%v err=%v", info, err)
	}
	if identity, err := InspectOwnedSocket(path); err != nil || identity.Inode == 0 || identity.Mode.Perm() != 0o600 {
		t.Fatalf("socket identity=%+v err=%v", identity, err)
	}
	if err := PrepareSocket(path); err == nil {
		t.Fatal("active collision accepted")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed owned socket remains: %v", err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = stale.Close()
	if err := PrepareSocket(path); err != nil {
		t.Fatalf("owned stale socket not replaced: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket remains: %v", err)
	}

	if err := os.Symlink(filepath.Join(root, "elsewhere"), path); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocket(path); err == nil {
		t.Fatal("symlink collision accepted")
	}
}

func TestOwnedSocketCleanupPreservesReplacementAndPathModeBounds(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-localipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "private", "cleanup.sock")
	listener, err := Listen(path)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	original := listener.Identity()
	if err := listener.Unix.Close(); err != nil {
		t.Fatal(err)
	}
	// Measured once sockets are known to be available, in the same /tmp.
	socketreuse.SkipIfReplacementCollapses(t, "/tmp", InspectOwnedSocket)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer func() {
		_ = replacement.Close()
		_ = os.Remove(path)
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedSocket(path, original); !errors.Is(err, ErrSocketReplaced) {
		t.Fatalf("replacement cleanup err = %v, want identity change", err)
	}
	if _, err := InspectOwnedSocket(path); err != nil {
		t.Fatalf("replacement was removed: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectOwnedSocket(path); err == nil {
		t.Fatal("open socket mode accepted")
	}
	if err := PrepareSocket(strings.Repeat("/x", MaxSocketPath)); err == nil {
		t.Fatal("overlong socket path accepted")
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSocket(filepath.Join(filepath.Dir(path), "mode-check.sock")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("owned directory mode was not converged: info=%v err=%v", info, err)
	}
}

func TestSocketIncarnationIdentityRejectsLegacyAxisReuse(t *testing.T) {
	current := SocketIdentity{
		Device: 41, Inode: 73, Owner: 1000, Mode: os.ModeSocket | 0o600, Size: 0,
		ChangeTimeSeconds: 900, ChangeTimeNanoseconds: 12,
	}
	stale := current
	// Simulate immediate filesystem reuse: every legacy device/inode/owner/
	// mode/size axis is identical and only the socket incarnation time differs.
	stale.ChangeTimeNanoseconds ^= 1
	if sameSocketIdentity(current, stale) {
		t.Fatal("socket identity accepted a replacement with reused legacy axes")
	}
}

func TestBoundedJSONRoundTripTrailingAndOversize(t *testing.T) {
	type frame struct {
		Value string `json:"value"`
	}
	var got frame
	if err := ReadJSON(strings.NewReader("{\"value\":\"ok\"}\n\t"), &got); err != nil || got.Value != "ok" {
		t.Fatalf("valid frame got=%+v err=%v", got, err)
	}
	for _, input := range []string{"{} {}", strings.Repeat("x", MaxFrameBytes+1)} {
		if err := ReadJSON(strings.NewReader(input), &got); err == nil {
			t.Fatalf("invalid frame accepted len=%d", len(input))
		}
	}
	payload, err := MarshalJSON(frame{Value: "ok"})
	if err != nil || len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	var wire bytes.Buffer
	if err := WriteJSON(&wire, frame{Value: "ok"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.Bytes(), payload) {
		t.Fatalf("wire=%q payload=%q", wire.Bytes(), payload)
	}
}

func TestPrimitivesUseOneHalfClosedRequestAndOneResponse(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-localipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := Listen(filepath.Join(root, "private", "call.sock"))
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Unix.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		var request map[string]string
		if err := ReadJSON(conn, &request); err != nil {
			done <- err
			return
		}
		done <- WriteJSON(conn, map[string]string{"reply": request["request"]})
	}()
	connection, err := net.Dial("unix", listener.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := WriteJSON(connection, map[string]string{"request": "exact"}); err != nil {
		t.Fatal(err)
	}
	if err := connection.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var response map[string]string
	if err := ReadJSON(connection, &response); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || response["reply"] != "exact" {
		t.Fatalf("response=%v serverErr=%v", response, err)
	}
}

func TestPeerProcessReturnsExactKernelBirth(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-localipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := Listen(filepath.Join(root, "private", "peer.sock"))
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	want, _, err := Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Unix.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		got, _, err := PeerProcess(conn)
		if err == nil && got != want {
			err = errors.New("peer birth mismatch")
		}
		done <- err
	}()
	conn, err := net.Dial("unix", listener.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(conn).Encode(map[string]bool{"ready": true})
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestKernelProcessAbsenceIsDistinctFromUnreadableIdentity(t *testing.T) {
	if _, _, err := Process(-1); err == nil || errors.Is(err, ErrProcessAbsent) {
		t.Fatal("invalid PID claimed kernel absence", err)
	}
	identity, _, err := Process(os.Getpid())
	if err != nil || !identity.Valid() || identity.PID != os.Getpid() {
		t.Fatal("live identity unavailable", identity, err)
	}
	// Linux and Darwin kernel PID limits are below this positive PID.
	if _, _, err := Process(1 << 30); !errors.Is(err, ErrProcessAbsent) {
		t.Fatal("missing PID did not prove absence", err)
	}
}

// A Reader can return bytes and an error together, including at the frame bound.
type terminalFrameReader struct {
	*bytes.Reader
	err error
}

func (r terminalFrameReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.Reader.Len() == 0 {
		return n, r.err
	}
	return n, err
}

type frameSyntheticTimeout struct{}

func (frameSyntheticTimeout) Error() string   { return "synthetic timeout" }
func (frameSyntheticTimeout) Timeout() bool   { return true }
func (frameSyntheticTimeout) Temporary() bool { return true }

func TestBoundedJSONReadDeadlineClassificationAndPriority(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		err, want     error
	}{
		{"deadline", "", os.ErrDeadlineExceeded, ErrFrameReadDeadline},
		{"partial deadline", `{"value":`, os.ErrDeadlineExceeded, ErrFrameReadDeadline},
		{"complete deadline", `{"value":"must not decode"}`, os.ErrDeadlineExceeded, ErrFrameReadDeadline},
		{"oversize before deadline", strings.Repeat("x", MaxFrameBytes+1), os.ErrDeadlineExceeded, ErrFrameTooLarge},
		{"malformed", `{"value":`, io.EOF, ErrFrameMalformed},
		{"other error", "", io.ErrUnexpectedEOF, nil},
		{"timeout interface alone", "", frameSyntheticTimeout{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := struct{ Value string }{Value: "unchanged"}
			err := ReadJSON(terminalFrameReader{bytes.NewReader([]byte(tc.payload)), tc.err}, &got)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("read error=%v want=%v", err, tc.want)
			}
			if tc.want == ErrFrameReadDeadline && err.Error() != "bounded frame read failed" {
				t.Fatalf("native deadline changed the existing read-error text: %v", err)
			}
			if tc.want == nil && (errors.Is(err, ErrFrameReadDeadline) || errors.Is(err, os.ErrDeadlineExceeded)) {
				t.Fatalf("unrelated error classified as native deadline: %v", err)
			}
			if got.Value != "unchanged" {
				t.Fatal("failed read decoded a value")
			}
		})
	}
}

func TestBoundedJSONNativeReadDeadlinePreservesPartialFrame(t *testing.T) {
	for _, payload := range []string{"", `{"value":`} {
		t.Run(fmt.Sprintf("bytes-%d", len(payload)), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done, writerDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(writerDone)
				defer server.Close()
				if payload != "" {
					_, _ = server.Write([]byte(payload))
				}
				<-done
			}()
			if err := client.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			var got any
			err := ReadJSON(client, &got)
			close(done)
			server.Close()
			<-writerDone
			if !errors.Is(err, ErrFrameReadDeadline) || got != nil {
				t.Fatalf("native deadline: value=%v error=%v", got, err)
			}
		})
	}
}

// A read ends at the caller's cancel even while the owner holds its answer,
// and an answer sent after the cancel is not a success. An exchange with an
// effect keeps reading its receipt after the cancel.
func TestReadOwnedJSONCancelEndsConnectedRead(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pmx-localipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := Listen(filepath.Join(root, "private", "read.sock"))
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, _, err := Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range []bool{true, false} {
		accepted, release := make(chan struct{}), make(chan struct{})
		served := make(chan error, 1)
		go func() {
			conn, err := listener.Unix.AcceptUnix()
			if err != nil {
				served <- err
				return
			}
			defer conn.Close()
			var request map[string]string
			if err := ReadJSON(conn, &request); err != nil {
				served <- err
				return
			}
			close(accepted)
			<-release
			served <- WriteJSON(conn, map[string]string{"reply": request["request"]})
		}()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-accepted
			cancel()
			close(release)
		}()
		exchange := ExchangeOwnedJSON
		if read {
			exchange = ReadOwnedJSON
		}
		var response map[string]string
		started := time.Now()
		err := exchange(ctx, listener.Path, listener.Identity(), host, map[string]string{"request": "exact"}, &response)
		elapsed := time.Since(started)
		cancel()
		<-served
		if read && (!errors.Is(err, context.Canceled) || elapsed > time.Second) {
			t.Fatalf("read after cancel: %v after %s, response %v", err, elapsed, response)
		}
		if !read && (err != nil || response["reply"] != "exact") {
			t.Fatalf("exchange after cancel lost its receipt: %v %v", err, response)
		}
	}
}
