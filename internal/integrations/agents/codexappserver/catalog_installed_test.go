package codexappserver_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// stateDbOnlyListParams is the A/B request shape. The pointer field is the
// only difference between the three arms: explicit true is what the product
// sends, and explicit false plus omitted are the scan-and-repair baselines the
// product must never fall back to.
type stateDbOnlyListParams struct {
	Limit          uint32   `json:"limit"`
	SortKey        string   `json:"sortKey"`
	SortDirection  string   `json:"sortDirection"`
	SourceKinds    []string `json:"sourceKinds"`
	Archived       bool     `json:"archived"`
	UseStateDbOnly *bool    `json:"useStateDbOnly,omitempty"`
}

// stateDbOnlyListResult keeps the response shape and drops every row byte.
// Decoding rows into empty structs preserves the observable count and the
// nextCursor envelope while retaining no provider content at all.
type stateDbOnlyListResult struct {
	Data       []struct{} `json:"data"`
	NextCursor *string    `json:"nextCursor"`
}

// TestInstalledStateDbOnlyCatalogListABSmoke is the installed half of the
// state-db-only wire contract. It runs the real app-server on a fixture-owned
// private listener and compares three exact thread/list arms: the product's
// explicit true, explicit false, and omitted. The pass condition is
// compatibility, not latency -- the true arm must be accepted and must return
// a well-formed result plus nextCursor envelope rather than a field or params
// refusal. Elapsed times for all three arms are recorded as observation only:
// this fixture owns an isolated empty CODEX_HOME, so it cannot and does not
// claim the scan-and-repair timing separation of a large real store.
func TestInstalledStateDbOnlyCatalogListABSmoke(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_STATE_DB_ONLY_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_STATE_DB_ONLY_SMOKE_ROOT for the installed state-db-only A/B smoke")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("state-db-only fixture cleanup: %v", err)
		}
	})
	_ = fixture.ProvisionManagedPayload()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The exact generation under test is an input, not an inference: an
	// absolute PROJMUX_CODEX_STATE_DB_ONLY_BINARY pins the private endpoint to
	// one exact release, and without it the fixture uses the PATH binary. The
	// recorded result carries whichever version actually answered.
	var override []string
	if exact := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_STATE_DB_ONLY_BINARY")); exact != "" {
		if !filepath.IsAbs(exact) {
			t.Fatalf("PROJMUX_CODEX_STATE_DB_ONLY_BINARY must be absolute")
		}
		override = append(override, exact)
	}
	direct, ready := fixture.StartDirect(ctx, "installed-state-db-only-smoke", override...)
	if ready.Class != codexinstalled.ResultPass {
		logInstalledResult(t, ready)
		t.Fatalf("state-db-only direct endpoint = %+v", ready)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if result := direct.Close(closeCtx); result.Class != codexinstalled.ResultPass {
			t.Errorf("state-db-only direct close = %+v", result)
		}
	})

	client, err := codexappserver.OpenPrivateUnix(ctx, fixture.SocketPath, 10*time.Second, "installed-state-db-only-smoke", true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	started := time.Now()
	page, err := client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{})
	trueElapsed := time.Since(started)
	if err != nil {
		if errors.Is(err, codexappserver.ErrStateDbOnlyRejected) {
			t.Fatalf("installed endpoint refused the explicit useStateDbOnly field: %v", err)
		}
		t.Fatalf("explicit useStateDbOnly=true thread/list: %v", err)
	}
	t.Logf("state-db-only A/B arm=true rows=%d next_cursor=%t elapsed=%s",
		len(page.Threads), page.NextCursor != nil, trueElapsed)

	explicitFalse := false
	for _, arm := range []struct {
		name  string
		field *bool
	}{
		{name: "false", field: &explicitFalse},
		{name: "omitted", field: nil},
	} {
		params := stateDbOnlyListParams{
			Limit: codexappserver.DefaultCatalogPageSize, SortKey: "recency_at", SortDirection: "desc",
			SourceKinds: []string{"cli", "vscode", "appServer"}, Archived: false, UseStateDbOnly: arm.field,
		}
		var baseline stateDbOnlyListResult
		started := time.Now()
		err := client.Request(ctx, "thread/list", params, &baseline)
		elapsed := time.Since(started)
		if err != nil {
			t.Logf("state-db-only A/B arm=%s refused elapsed=%s: %v", arm.name, elapsed, err)
			continue
		}
		t.Logf("state-db-only A/B arm=%s rows=%d next_cursor=%t elapsed=%s",
			arm.name, len(baseline.Data), baseline.NextCursor != nil, elapsed)
	}

	result := codexinstalled.NewResult(fixture.Versions(), codexinstalled.TopologyDirect,
		codexinstalled.StageReady, codexinstalled.ResultPass, "state-db-only-list-compatible")
	logInstalledResult(t, result)
}
