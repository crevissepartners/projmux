package codexappserver_test

import (
	"context"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

func TestInstalledIsolatedConversationCatalogSmoke(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_CATALOG_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_CATALOG_SMOKE_ROOT for the installed Codex catalog smoke")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("catalog fixture cleanup: %v", err)
		}
	})
	_ = fixture.ProvisionManagedPayload()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	direct, ready := fixture.StartDirect(ctx, "installed-catalog-smoke")
	if ready.Class != codexinstalled.ResultPass {
		logInstalledResult(t, ready)
		t.Fatalf("catalog direct endpoint = %+v", ready)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if result := direct.Close(closeCtx); result.Class != codexinstalled.ResultPass {
			t.Errorf("catalog direct close = %+v", result)
		}
	})

	client, err := codexappserver.OpenDefaultProxy(ctx, codexappserver.DefaultProbeTimeout, "installed-catalog-smoke")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	page, err := client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog thread/list returned content-free rows=%d", len(page.Threads))
	// thread/list is this canary's unique real-binary primitive. Exact
	// thread/read remains owned by the pre-turn second-attach canary.
	result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "thread-list-compatible")
	logInstalledResult(t, result)
}
