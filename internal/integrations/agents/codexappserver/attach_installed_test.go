package codexappserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInstalledIsolatedPreTurnBootstrapSmoke exercises the broker-facing
// contract against a real installed Codex app-server. It is opt-in through
// PROJMUX_CODEX_BROKER_SMOKE_ROOT and requires a matching contained
// CODEX_HOME, inherited tmux identity stripped, and an isolated temporary
// root, so it can never touch an ambient shared endpoint's state.
//
// It proves the upstream facts this Phase depends on: a thread exists before
// its first turn, and that pre-turn thread's includeTurns=false snapshot is
// readable from a second connection with no materialized turn. The explicit
// thread/resume subscription leg is recorded as typed evidence rather than
// asserted, because installed Codex 0.150.1 answers thread/resume only for a
// thread whose rollout already exists. Everything it records is content-free.
func TestInstalledIsolatedPreTurnBootstrapSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_BROKER_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_BROKER_SMOKE_ROOT for the installed broker-facing smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("smoke root must be an isolated child of %s", tmpRoot)
	}
	if _, present := os.LookupEnv("TMUX"); present {
		t.Fatal("TMUX must be removed for the installed broker-facing smoke")
	}
	if _, present := os.LookupEnv("TMUX_PANE"); present {
		t.Fatal("TMUX_PANE must be removed for the installed broker-facing smoke")
	}
	wantCodexHome := filepath.Join(root, "codex-home")
	if got := filepath.Clean(os.Getenv("CODEX_HOME")); got != wantCodexHome {
		t.Fatalf("CODEX_HOME = %q, want %q", got, wantCodexHome)
	}
	socketPath, ok := defaultControlSocketPath()
	wantSocket := filepath.Join(wantCodexHome, "app-server-control", "app-server-control.sock")
	if !ok || socketPath != wantSocket || !strings.HasPrefix(socketPath, root+string(filepath.Separator)) {
		t.Fatalf("control socket = %q, want contained %q", socketPath, wantSocket)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	creator, health, err := AttachDefaultEndpoint(ctx, "0.13.0", AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		var attachErr *AttachError
		if errors.As(err, &attachErr) {
			t.Skipf("isolated endpoint is not attachable: %s", attachErr.Refusal)
		}
		t.Fatal(err)
	}
	authority := AuthorityFor(health)
	t.Logf("attach authority=%s lifecycle=%s ownership=%s version=%s",
		authority.Attach, authority.Lifecycle, health.ManagerOwnership, health.VersionRelation)

	binding, err := creator.StartThread(ctx, workspace, nil)
	if err != nil {
		_ = creator.Close()
		t.Fatalf("pre-turn thread/start: %v", err)
	}
	if binding.ThreadID == "" || binding.TurnID != "" {
		_ = creator.Close()
		t.Fatalf("pre-turn binding = %+v, want a thread with no turn", binding)
	}
	// Close the creating connection so the bootstrap runs against a genuinely
	// second connection, which is the case a broker has to survive. Closing the
	// owned stdio proxy kills that process, so its exit status is the expected
	// outcome of the close rather than a failure.
	_ = creator.Close()

	reader, _, err := AttachDefaultEndpoint(ctx, "0.13.0", AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	defer reader.Close()
	thread, err := reader.ReadCatalogThread(ctx, binding.ThreadID)
	if err != nil {
		t.Fatalf("pre-turn includeTurns=false snapshot: %v", err)
	}
	snapshot := newThreadSnapshot(thread)
	if snapshot.ThreadID != binding.ThreadID {
		t.Fatalf("snapshot = %+v, want thread %q", snapshot, binding.ThreadID)
	}
	if snapshot.CWD == "" || snapshot.RuntimeStatus == "" {
		t.Fatalf("snapshot is missing content-free identity: %+v", snapshot)
	}
	t.Logf("pre-turn snapshot status=%s flags=%v", snapshot.RuntimeStatus, snapshot.ActiveFlags)

	// Typed evidence for the subscription leg. Installed 0.150.1 has no rollout
	// for a thread whose first turn never ran, so thread/resume is refused here
	// while the same call succeeds once the thread has materialized. Recording
	// it keeps the unsupported item visible without asserting an outcome the
	// upstream does not offer before the first turn.
	if _, err := reader.BootstrapThread(ctx, binding.ThreadID, workspace, nil); err != nil {
		t.Logf("pre-turn explicit resume subscription unsupported: %v", err)
	} else {
		t.Log("pre-turn explicit resume subscription established")
	}

	// A no-turn thread produces no inbound server request, so the response-once
	// ledger has nothing to consume here. That is recorded as typed evidence
	// rather than asserted as an approval outcome.
	select {
	case event, open := <-reader.Notifications():
		if open && len(event.RawRequestID) > 0 {
			t.Logf("observed an inbound server request during bootstrap: method=%s", event.Method)
		}
	default:
		t.Log("no inbound server request during a no-turn bootstrap, as expected")
	}
}
