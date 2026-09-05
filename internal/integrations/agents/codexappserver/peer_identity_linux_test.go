package codexappserver

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestPeerIdentityHelperProcess is an isolated Unix peer used by the endpoint
// witness preflight. It deliberately speaks no provider protocol: this test is
// about the kernel identity captured before any WebSocket or JSON byte can be
// trusted.
func TestPeerIdentityHelperProcess(t *testing.T) {
	if os.Getenv("PROJMUX_PEER_IDENTITY_HELPER") != "1" {
		return
	}
	socketPath := os.Getenv("PROJMUX_PEER_IDENTITY_SOCKET")
	readyPath := os.Getenv("PROJMUX_PEER_IDENTITY_READY")
	releasePath := os.Getenv("PROJMUX_PEER_IDENTITY_RELEASE")
	releasedPath := os.Getenv("PROJMUX_PEER_IDENTITY_RELEASED")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		os.Exit(2)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		os.Exit(3)
	}
	go func() {
		for {
			if _, err := os.Stat(releasePath); err == nil {
				_ = listener.Close()
				_ = os.Remove(socketPath)
				_ = os.WriteFile(releasedPath, []byte("released"), 0o600)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {}
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}()
	}
}

func TestEndpointPeerWitnessRejectsSamePathReplacementAndSyntheticPIDReuse(t *testing.T) {
	root := t.TempDir()
	socketPath := filepath.Join(root, "endpoint.sock")
	type peerProcess struct {
		cmd      *exec.Cmd
		release  string
		released string
	}
	start := func(label string) peerProcess {
		t.Helper()
		ready := filepath.Join(root, label+".ready")
		release := filepath.Join(root, label+".release")
		released := filepath.Join(root, label+".released")
		cmd := exec.Command(os.Args[0], "-test.run=^TestPeerIdentityHelperProcess$")
		cmd.Env = append(os.Environ(),
			"PROJMUX_PEER_IDENTITY_HELPER=1",
			"PROJMUX_PEER_IDENTITY_SOCKET="+socketPath,
			"PROJMUX_PEER_IDENTITY_READY="+ready,
			"PROJMUX_PEER_IDENTITY_RELEASE="+release,
			"PROJMUX_PEER_IDENTITY_RELEASED="+released,
		)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s peer: %v", label, err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s peer did not publish", label)
			}
			time.Sleep(5 * time.Millisecond)
		}
		return peerProcess{cmd: cmd, release: release, released: released}
	}
	dial := func() (*net.UnixConn, PeerIdentity) {
		t.Helper()
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
		if err != nil {
			t.Fatalf("dial peer: %v", err)
		}
		unixConn := conn.(*net.UnixConn)
		identity, err := peerIdentity(unixConn)
		if err != nil {
			_ = conn.Close()
			t.Fatalf("capture peer: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return unixConn, identity
	}

	peerP := start("p")
	firstConn, first := dial()
	_, second := dial()
	if !SamePeerIdentity(first, second) {
		t.Fatalf("two connections to peer P differ: first=%+v second=%+v", first, second)
	}

	// Keep both P connections open while replacing only the pathname listener.
	if err := os.WriteFile(peerP.release, []byte("release"), 0o600); err != nil {
		t.Fatalf("request peer P listener release: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(peerP.released); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("peer P socket pathname was not released")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The route pathname is gone, but the already-established shared P
	// connection and its peer process remain live.
	if err := firstConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := firstConn.Write([]byte("p")); err != nil {
		t.Fatalf("shared P connection closed with listener: %v", err)
	}
	var echoed [1]byte
	if _, err := io.ReadFull(firstConn, echoed[:]); err != nil || echoed[0] != 'p' {
		t.Fatalf("shared P connection is not live: echo=%q err=%v", echoed, err)
	}
	start("q")
	_, replacement := dial()
	if SamePeerIdentity(first, replacement) {
		t.Fatalf("same-path peer Q inherited P witness: P=%+v Q=%+v", first, replacement)
	}

	reusedPID := first
	reusedPID.Start += "-replacement"
	if SamePeerIdentity(first, reusedPID) {
		t.Fatalf("same PID with a different birth identity was accepted: %+v", reusedPID)
	}
}
