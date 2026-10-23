package app

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// peerTransitionSocket returns both ends of one real Unix connection on a
// private short socket path, and this process's kernel identity.
func peerTransitionSocket(t *testing.T) (*localipc.Listener, *net.UnixConn, *net.UnixConn, coremetadata.ProcessIdentity) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pc-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := localipc.Listen(filepath.Join(dir, "host.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, err := listener.Unix.AcceptUnix()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: listener.Path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { _ = server.Close() })
	self, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return listener, client, server, self
}

// scriptedClaudeProcessPeer replaces the peer read: the listed errors come
// first, then the kernel's real snapshot. It counts every read.
func scriptedClaudeProcessPeer(t *testing.T, first ...error) *int {
	t.Helper()
	reads := 0
	original := claudeProcessPeer
	claudeProcessPeer = func(conn *net.UnixConn) (coremetadata.ProcessIdentity, int, error) {
		reads++
		if reads <= len(first) {
			return coremetadata.ProcessIdentity{}, 0, first[reads-1]
		}
		return original(conn)
	}
	t.Cleanup(func() { claudeProcessPeer = original })
	return &reads
}

func peerTransition(process coremetadata.ProcessIdentity) error {
	return localipc.ParentTransitionWitness{Process: process, OldParent: 987654, NewParent: 987653}
}

// The helper's first idle check races its hook's exit: the host's peer read
// sees the helper reparent from the hook to the supervisor while the hook is
// still readable. One independent snapshot after that transition is ordinary
// peer evidence; the exchange must not close without a response.
func TestClaudeHostExchangePeerAfterHelperReparent(t *testing.T) {
	_, _, server, self := peerTransitionSocket(t)
	// No admitted registration backs the registered-helper recheck, so it
	// refuses here as it did for the measured still-readable hook.
	s := claudeProcessService{}
	reads := scriptedClaudeProcessPeer(t, peerTransition(self))
	peer, _, err := s.exchangePeer(claudeProcessCheck{Lookup: true}, server)
	if err != nil || peer != self {
		t.Fatalf("transition snapshot refused the exchange: peer=%+v err=%v", peer, err)
	}
	if *reads != 2 {
		t.Fatalf("peer reads = %d, want exactly one independent snapshot", *reads)
	}
}

func TestClaudeHostExchangePeerRefusalsSurviveOneSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(coremetadata.ProcessIdentity) []error
		reads int
	}{
		{"second-transition", func(p coremetadata.ProcessIdentity) []error { return []error{peerTransition(p), peerTransition(p)} }, 3},
		{"second-unavailable", func(p coremetadata.ProcessIdentity) []error {
			return []error{peerTransition(p), errors.New("peer unavailable")}
		}, 2},
		{"ordinary-error", func(coremetadata.ProcessIdentity) []error { return []error{errors.New("peer unavailable")} }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, server, self := peerTransitionSocket(t)
			s := claudeProcessService{}
			reads := scriptedClaudeProcessPeer(t, tc.first(self)...)
			peer, parent, err := s.exchangePeer(claudeProcessCheck{Lookup: true}, server)
			if err == nil || peer.PID != 0 || parent != 0 {
				t.Fatalf("refused peer accepted: %+v %d %v", peer, parent, err)
			}
			if *reads > tc.reads {
				t.Fatalf("peer reads = %d, want at most %d", *reads, tc.reads)
			}
		})
	}
}

// A client's dial reads the host as its peer. A detached host reparents when
// its creating CLI exits; one independent snapshot after that transition must
// still prove the same host instead of reporting it stale.
func TestDialProcessClaudeHostAfterHostReparent(t *testing.T) {
	listener, _, _, self := peerTransitionSocket(t)
	proof := claudeProcessProof{Socket: listener.Path, HostSocket: listener.Identity(), HostProcess: self, Process: self}
	reads := scriptedClaudeProcessPeer(t, peerTransition(self))
	conn, err := dialProcessClaudeHost(proof)
	if err != nil {
		t.Fatalf("host transition snapshot reported stale: %v", err)
	}
	_ = conn.Close()
	if *reads != 2 {
		t.Fatalf("peer reads = %d, want exactly one independent snapshot", *reads)
	}
}

func TestDialProcessClaudeHostRefusalsSurviveOneSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(coremetadata.ProcessIdentity) []error
		other bool
	}{
		{"second-transition", func(p coremetadata.ProcessIdentity) []error { return []error{peerTransition(p), peerTransition(p)} }, false},
		{"ordinary-error", func(coremetadata.ProcessIdentity) []error { return []error{errors.New("peer unavailable")} }, false},
		{"other-host", func(coremetadata.ProcessIdentity) []error { return []error{} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, _, _, self := peerTransitionSocket(t)
			host := self
			if tc.other {
				host.Start = "replacement"
			}
			proof := claudeProcessProof{Socket: listener.Path, HostSocket: listener.Identity(), HostProcess: host, Process: self}
			reads := scriptedClaudeProcessPeer(t, tc.first(self)...)
			conn, err := dialProcessClaudeHost(proof)
			if err == nil {
				_ = conn.Close()
				t.Fatal("refused host accepted")
			}
			if !errors.Is(err, processhost.ErrStale) {
				t.Fatalf("err = %v, want stale", err)
			}
			if *reads > 2 {
				t.Fatalf("peer reads = %d, want at most 2", *reads)
			}
		})
	}
}
