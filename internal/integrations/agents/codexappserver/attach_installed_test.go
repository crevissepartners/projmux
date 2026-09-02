package codexappserver_test

import (
	"context"
	"os"
	"slices"
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
// its first turn, that thread is loaded, and its includeTurns=false snapshot is
// readable from a second connection with no materialized turn. It then closes
// the creating connection and asks the installed CLI to attach in one exact
// isolated tmux Pane. The semantic capability is supported only when that Pane
// survives two independent loaded/runtime observation barriers. The CLI/RPC
// spellings are evidence metadata; the reducer never branches on them.
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
	method := codexinstalled.CapabilityMethod{
		Attach: "cli-remote-resume", Loaded: "rpc-thread-loaded-list", Runtime: "rpc-thread-read",
	}
	evidence := codexinstalled.CapabilityEvidence{}
	observation := codexinstalled.CapabilityObservation{
		Probe: "TestInstalledIsolatedPreTurnBootstrapSmoke",
		Run:   os.Getenv("PROJMUX_CODEX_EVIDENCE_RUN"),
	}
	defer func() {
		result := codexinstalled.EvaluateTurnFreeThreadLiveAttach(fixture.Versions(), method, evidence, observation)
		logInstalledCapability(t, result)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	direct, ready := fixture.StartDirect(ctx, "installed-pre-turn-smoke")
	if ready.Class != codexinstalled.ResultPass {
		logInstalledResult(t, ready)
		t.Fatalf("pre-turn direct endpoint = %+v", ready)
	}
	evidence.EndpointReady = true
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
	evidence.ThreadCreatedWithoutTurn = true

	reader, _, err := codexappserver.AttachDefaultEndpoint(ctx, "installed-pre-turn-smoke",
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
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
	loaded, err := reader.ListLoadedThreadIDs(ctx)
	if err != nil {
		_ = reader.Close()
		t.Fatalf("pre-turn loaded identity: %v", err)
	}
	evidence.LoadedBeforeAttach = slices.Contains(loaded, binding.ThreadID)
	if !evidence.LoadedBeforeAttach {
		_ = reader.Close()
		t.Fatalf("turn-free thread is absent from the loaded runtime")
	}
	_ = reader.Close()

	pane, err := fixture.StartTurnFreeAttachPane(ctx, binding.ThreadID)
	if err != nil {
		_ = creator.Close()
		t.Fatalf("pre-turn Pane attach: %v", err)
	}
	evidence.AttachAttempted = true
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := pane.Close(closeCtx); err != nil {
			t.Errorf("pre-turn Pane cleanup: %v", err)
		}
	})
	// The creator is the only known subscription before the CLI Pane starts.
	// Closing it makes post-attach loaded/runtime evidence attributable to the
	// candidate carrier instead of to the Phase 1 setup connection.
	_ = creator.Close()

	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()
	for {
		alive, paneErr := pane.Alive(probeCtx)
		if paneErr != nil {
			t.Fatalf("observe exact pre-turn Pane %s: %v", pane.ID(), paneErr)
		}
		evidence.PaneObserved = true
		evidence.PaneAlive = alive
		if !alive {
			t.Log("turn-free attach reached a terminal unsupported Pane exit")
			break
		}

		loadedAfter, runtimeStatus, observationErr := observeTurnFreeRuntime(probeCtx, binding.ThreadID)
		if observationErr == nil && loadedAfter && (runtimeStatus == "idle" || runtimeStatus == "active") {
			// A fresh connection and a second exact Pane query form the independent
			// barrier. One transient sample is not living-Pane evidence.
			loadedAgain, statusAgain, secondErr := observeTurnFreeRuntime(probeCtx, binding.ThreadID)
			aliveAgain, paneAgainErr := pane.Alive(probeCtx)
			if secondErr == nil && paneAgainErr == nil && aliveAgain && loadedAgain && statusAgain == runtimeStatus {
				evidence.LoadedAfterAttach = true
				evidence.RuntimeStatusAfterAttach = statusAgain
				evidence.PaneObserved = true
				evidence.PaneAlive = true
				t.Logf("turn-free attach supported: pane=%s loaded=true runtime=%s", pane.ID(), statusAgain)
				break
			}
		}
		select {
		case <-probeCtx.Done():
			t.Fatalf("turn-free attach evidence deadline: %v", probeCtx.Err())
		case <-time.After(25 * time.Millisecond):
		}
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
	if recorded, err := fixture.Ledger().HasOperation("pre-turn-cli-remote-resume"); err != nil || !recorded {
		t.Fatalf("turn-free CLI attach command ledger: recorded=%t err=%v", recorded, err)
	}
	if err := fixture.Ledger().AssertNoAmbientMutation(); err != nil {
		t.Fatal(err)
	}
	result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "pre-turn-second-attach-thread-read-compatible")
	logInstalledResult(t, result)
}

func observeTurnFreeRuntime(ctx context.Context, threadID string) (bool, string, error) {
	reader, _, err := codexappserver.AttachDefaultEndpoint(ctx, "installed-pre-turn-capability-observer",
		codexappserver.AttachOptions{Timeout: 5 * time.Second, ExperimentalAPI: true})
	if err != nil {
		return false, "", err
	}
	defer reader.Close()
	loaded, err := reader.ListLoadedThreadIDs(ctx)
	if err != nil {
		return false, "", err
	}
	thread, err := reader.ReadCatalogThread(ctx, threadID)
	if err != nil {
		return false, "", err
	}
	return slices.Contains(loaded, threadID), thread.RuntimeStatus, nil
}

func logInstalledCapability(t *testing.T, result codexinstalled.CapabilityResult) {
	t.Helper()
	encoded, err := result.JSON()
	if err != nil {
		t.Errorf("invalid installed capability result: %v", err)
		return
	}
	t.Logf("installed-capability: %s", encoded)
}
