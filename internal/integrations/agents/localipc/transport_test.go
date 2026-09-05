package localipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
