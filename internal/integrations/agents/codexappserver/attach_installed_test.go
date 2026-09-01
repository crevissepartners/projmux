package codexappserver_test

import (
	"context"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
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
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_BROKER_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_BROKER_SMOKE_ROOT for the installed broker-facing smoke")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("pre-turn fixture cleanup: %v", err)
		}
	})
	_ = fixture.ProvisionManagedPayload()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	direct, ready := fixture.StartDirect(ctx, "installed-pre-turn-smoke")
	if ready.Class != codexinstalled.ResultPass {
		logInstalledResult(t, ready)
		t.Fatalf("pre-turn direct endpoint = %+v", ready)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if result := direct.Close(closeCtx); result.Class != codexinstalled.ResultPass {
			t.Errorf("pre-turn direct close = %+v", result)
		}
	})

	creator, health, err := codexappserver.AttachDefaultEndpoint(ctx, "installed-pre-turn-smoke",
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
			codexinstalled.StageReady, codexinstalled.ResultFail, "direct-attach-refused")
		logInstalledResult(t, result)
		t.Fatalf("isolated direct endpoint is not attachable: %v", err)
	}
	authority := codexappserver.AuthorityFor(health)
	t.Logf("attach authority=%s lifecycle=%s ownership=%s version=%s",
		authority.Attach, authority.Lifecycle, health.ManagerOwnership, health.VersionRelation)

	binding, err := creator.StartThread(ctx, fixture.Workspace, nil)
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

	reader, _, err := codexappserver.AttachDefaultEndpoint(ctx, "installed-pre-turn-smoke",
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	defer reader.Close()
	thread, err := reader.ReadCatalogThread(ctx, binding.ThreadID)
	if err != nil {
		t.Fatalf("pre-turn includeTurns=false snapshot: %v", err)
	}
	if thread.ID != binding.ThreadID {
		t.Fatalf("snapshot = %+v, want thread %q", thread, binding.ThreadID)
	}
	if thread.CWD == "" || thread.RuntimeStatus == "" {
		t.Fatalf("snapshot is missing content-free identity: %+v", thread)
	}
	t.Logf("pre-turn snapshot status=%s flags=%v", thread.RuntimeStatus, thread.ActiveFlags)

	// Typed evidence for the subscription leg. Installed 0.150.1 has no rollout
	// for a thread whose first turn never ran, so thread/resume is refused here
	// while the same call succeeds once the thread has materialized. Recording
	// it keeps the unsupported item visible without asserting an outcome the
	// upstream does not offer before the first turn.
	if _, err := reader.BootstrapThread(ctx, binding.ThreadID, fixture.Workspace, nil); err != nil {
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
	result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "pre-turn-second-attach-thread-read-compatible")
	logInstalledResult(t, result)
}
