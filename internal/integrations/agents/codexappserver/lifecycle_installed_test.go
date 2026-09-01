package codexappserver_test

import (
	"context"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// TestInstalledHermeticTopologyQualification is the single lifecycle owner
// for the installed direct and managed topologies. The old daemon-only smoke's
// root, socket, readiness, and classification assertions are intentionally
// gone; the shared fixture now owns those boundaries and always emits one
// validated terminal result per topology when the opt-in root is supplied.
func TestInstalledHermeticTopologyQualification(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot(codexinstalled.DefaultSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_DAEMON_SMOKE_ROOT for the installed topology qualification")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("installed topology cleanup: %v", err)
		}
	})

	provision := fixture.ProvisionManagedPayload()
	logInstalledResult(t, provision)
	if provision.Class != codexinstalled.ResultPass && provision.Class != codexinstalled.ResultUnsupported {
		t.Fatalf("managed payload provision = %+v", provision)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	direct, ready := fixture.StartDirect(ctx, "installed-topology-qualification")
	logInstalledResult(t, ready)
	if ready.Class != codexinstalled.ResultPass {
		t.Fatalf("direct ready = %+v", ready)
	}
	if health := direct.Health(); health.EndpointReadiness != codexappserver.EndpointReady ||
		health.ManagerOwnership != codexappserver.ManagerUnmanaged ||
		health.VersionRelation != codexappserver.VersionCurrent ||
		health.NativeAction != codexappserver.NativeActionRefused ||
		health.NativeRefusal != codexappserver.NativeActionRefusalUnmanaged ||
		health.Lifecycle != codexappserver.LifecycleNotAttempted ||
		health.LifecycleReason != codexappserver.LifecycleReasonReadOnly {
		t.Fatalf("direct topology diagnostics = endpoint=%s ownership=%s version=%s native=%s/%s lifecycle=%s/%s",
			health.EndpointReadiness, health.ManagerOwnership, health.VersionRelation,
			health.NativeAction, health.NativeRefusal, health.Lifecycle, health.LifecycleReason)
	}
	closeCtx, closeCancel := context.WithTimeout(ctx, 20*time.Second)
	closed := direct.Close(closeCtx)
	closeCancel()
	logInstalledResult(t, closed)
	if closed.Class != codexinstalled.ResultPass {
		t.Fatalf("direct close = %+v", closed)
	}

	managed := fixture.RunManagedLifecycle(ctx, "installed-topology-qualification")
	logInstalledResult(t, managed)
	if managed.Class != codexinstalled.ResultPass && managed.Class != codexinstalled.ResultUnsupported {
		t.Fatalf("managed topology = %+v", managed)
	}
	if err := fixture.Ledger().AssertNoAmbientMutation(); err != nil {
		t.Fatal(err)
	}
}

func logInstalledResult(t *testing.T, result codexinstalled.Result) {
	t.Helper()
	encoded, err := result.JSON()
	if err != nil {
		t.Fatalf("invalid installed qualification result: %v", err)
	}
	t.Logf("installed-result: %s", encoded)
}
