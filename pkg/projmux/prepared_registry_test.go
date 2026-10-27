package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// mutatedGraphFixture is graphFixtureRegistry after change.
func mutatedGraphFixture(t *testing.T, change func(*metadata.Registry)) []byte {
	t.Helper()
	var reg metadata.Registry
	if err := json.Unmarshal(graphFixtureRegistry(t), &reg); err != nil {
		t.Fatal(err)
	}
	change(&reg)
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustPrepare(t *testing.T, c *Client, previous *PreparedRegistrySnapshot) (*PreparedRegistrySnapshot, SnapshotReuse) {
	t.Helper()
	prepared, reuse, err := c.PrepareRegistrySnapshot(context.Background(), previous)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil {
		t.Fatal("successful prepare returned no handle")
	}
	return prepared, reuse
}

// The same root and file identity return the original handle with no Registry
// read or decode; every doubtful case reads once and returns a new handle.
func TestPreparedSnapshotReuseDecodeCounts(t *testing.T) {
	raw := graphFixtureRegistry(t)
	c, paths := graphFixtureClient(t, raw)
	path := store.PathFor(paths.StateDir)
	counts := countSnapshotReads(t, nil)

	first, reuse := mustPrepare(t, c, nil)
	identity, err := store.NewDefaultStore(paths).RegistryFileIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 1 || counts.stats != 2 || reuse.Reused || reuse.Identity == nil || *reuse.Identity != publicRegistryFileIdentity(identity) {
		t.Fatalf("first prepare: %+v %+v", counts, reuse)
	}
	plain, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Snapshot(), plain) {
		t.Fatal("prepared snapshot differs from ReadRegistrySnapshot")
	}

	second, again := mustPrepare(t, c, first)
	if second != first || counts.loads != 1 || counts.stats != 3 || !again.Reused || again.Identity == nil || *again.Identity != *reuse.Identity {
		t.Fatalf("reuse: same=%v %+v %+v", second == first, counts, again)
	}
	if again.Identity == reuse.Identity {
		t.Fatal("reuse identity shares a previous result's pointer")
	}

	// An atomic rewrite with the same bytes changes the identity: read again.
	replaceRegistryFile(t, path, raw)
	third, refreshed := mustPrepare(t, c, first)
	if third == first || counts.loads != 2 || refreshed.Reused || refreshed.Identity == nil || *refreshed.Identity == *reuse.Identity {
		t.Fatalf("rewritten file reused: %+v %+v", counts, refreshed)
	}
	if !reflect.DeepEqual(third.Snapshot(), first.Snapshot()) {
		t.Fatal("same bytes prepared differently")
	}

	// A stat failure is never reused and leaves no identity.
	stat := statSnapshotRegistry
	statSnapshotRegistry = func(config.Paths) (store.RegistryFileIdentity, error) {
		counts.stats++
		return store.RegistryFileIdentity{}, errors.New("stat failed")
	}
	unstated, failedStat := mustPrepare(t, c, third)
	statSnapshotRegistry = stat
	if unstated == third || counts.loads != 3 || failedStat.Reused || failedStat.Identity != nil {
		t.Fatalf("stat failure reused: %+v %+v", counts, failedStat)
	}
	// A handle without identity is never reused even once the stat succeeds.
	if next, nextReuse := mustPrepare(t, c, unstated); next == unstated || nextReuse.Reused || counts.loads != 4 {
		t.Fatalf("nil identity reused: %+v %+v", counts, nextReuse)
	}
}

// A rewrite between the two stats leaves no identity, so the handle is read
// again next time; a first-use missing file is never reused either.
func TestPreparedSnapshotDoubleStatChangeAndFirstUse(t *testing.T) {
	raw := graphFixtureRegistry(t)
	c, paths := graphFixtureClient(t, raw)
	rewrite := true
	counts := countSnapshotReads(t, func() {
		if rewrite {
			replaceRegistryFile(t, store.PathFor(paths.StateDir), raw)
		}
	})
	changed, reuse := mustPrepare(t, c, nil)
	if reuse.Identity != nil || reuse.Reused {
		t.Fatalf("changed file: %+v", reuse)
	}
	rewrite = false
	if next, again := mustPrepare(t, c, changed); next == changed || again.Reused || again.Identity == nil || counts.loads != 2 {
		t.Fatalf("changed handle reused: %+v %+v", counts, again)
	}

	empty, root := snapshotClient(t, nil)
	if err := os.RemoveAll(filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	missing, missingReuse := mustPrepare(t, empty, nil)
	plain, err := empty.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if missingReuse.Identity != nil || missingReuse.Reused || !reflect.DeepEqual(missing.Snapshot(), plain) {
		t.Fatalf("first use: %+v", missingReuse)
	}
	if next, again := mustPrepare(t, empty, missing); next == missing || again.Reused {
		t.Fatalf("first-use handle reused: %+v", again)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) {
		t.Fatal("first-use prepare changed the filesystem")
	}
}

// A failed refresh returns no handle and leaves previous unchanged: its rows,
// virtual Windows and graph stay usable.
func TestPreparedSnapshotFailedRefreshKeepsPrevious(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	previous, _ := mustPrepare(t, c, nil)
	rows := previous.Snapshot()
	overlay := PreparedGraphOverlay{Runtime: core.GraphRuntimeInventory{Transport: core.RuntimeTransport{Kind: "none", Source: "none"}}}
	graph, err := c.ResolveResourceGraphFromPrepared(ctx, previous, overlay)
	if err != nil {
		t.Fatal(err)
	}

	replaceRegistryFile(t, store.PathFor(paths.StateDir), []byte("{"))
	_, plainErr := c.ReadRegistrySnapshotCached(ctx, nil)
	prepared, reuse, err := c.PrepareRegistrySnapshot(ctx, previous)
	var refusal *Refusal
	if prepared != nil || reuse != (SnapshotReuse{}) || !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable ||
		plainErr == nil || err.Error() != plainErr.Error() || !errors.Is(err, store.ErrMalformedRegistry) {
		t.Fatalf("failed refresh: %v %+v %v (cached %v)", prepared, reuse, err, plainErr)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if prepared, _, err := c.PrepareRegistrySnapshot(canceled, previous); prepared != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh: %v %v", prepared, err)
	}

	if !reflect.DeepEqual(previous.Snapshot(), rows) {
		t.Fatal("failed refresh changed the previous handle")
	}
	after, err := c.ResolveResourceGraphFromPrepared(ctx, previous, overlay)
	if err != nil || !reflect.DeepEqual(after, graph) {
		t.Fatalf("previous handle no longer resolves the same graph: %v", err)
	}
}

// Snapshot is an independent clone every time, and a handle is reused only by
// a Client of the same state root: a symlinked root that reaches the same file
// is still another root, and its Client cannot resolve the handle.
func TestPreparedSnapshotRootAndCloneIsolation(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	prepared, _ := mustPrepare(t, c, nil)
	want := prepared.Snapshot()
	a, b := prepared.Snapshot(), prepared.Snapshot()
	if &a.Projects[0] == &b.Projects[0] || &a.Panes[0] == &b.Panes[0] || &a.NameReservations[0] == &b.NameReservations[0] {
		t.Fatal("two Snapshot calls share rows")
	}
	a.Projects[0].Metadata.Name = "changed"
	a.Panes[0].Metadata.Labels = map[string]string{"changed": "yes"}
	a.Agents = a.Agents[:0]
	if !reflect.DeepEqual(prepared.Snapshot(), want) {
		t.Fatal("changing a returned snapshot changed the handle")
	}

	link := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink(paths.StateDir, link); err != nil {
		t.Fatal(err)
	}
	other, err := NewClient(Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: link})
	if err != nil {
		t.Fatal(err)
	}
	counts := countSnapshotReads(t, nil)
	foreign, reuse := mustPrepare(t, other, prepared)
	if foreign == prepared || reuse.Reused || counts.loads != 1 || reuse.Identity == nil {
		t.Fatalf("foreign root reused: %+v %+v", counts, reuse)
	}
	if !reflect.DeepEqual(foreign.Snapshot(), want) {
		t.Fatal("the same file prepared differently through another root")
	}
	overlay := PreparedGraphOverlay{}
	for _, tc := range []struct {
		client   *Client
		prepared *PreparedRegistrySnapshot
	}{
		{c, nil}, {c, &PreparedRegistrySnapshot{}}, {c, foreign}, {other, prepared},
	} {
		_, err := tc.client.ResolveResourceGraphFromPrepared(ctx, tc.prepared, overlay)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodePreparedSnapshotInvalid {
			t.Fatalf("foreign or unprepared handle: %v", err)
		}
	}
	if _, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, overlay); err != nil {
		t.Fatal(err)
	}
	var zero *PreparedRegistrySnapshot
	if !reflect.DeepEqual(zero.Snapshot(), core.RegistrySnapshot{}) || zero.IsVirtualWindow("window-01") || (&PreparedRegistrySnapshot{}).IsVirtualWindow("window-01") {
		t.Fatal("unprepared handle reports rows")
	}
}

// Clients of separate state roots prepare exactly what their own
// ReadRegistrySnapshot reads; neither sees the other's Registry.
func TestPreparedSnapshotSeparateStateRootReadParity(t *testing.T) {
	ctx := context.Background()
	first, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	second, _ := snapshotClient(t, snapshotRaw(t))
	for _, c := range []*Client{first, second} {
		prepared, _ := mustPrepare(t, c, nil)
		plain, err := c.ReadRegistrySnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(prepared.Snapshot(), plain) {
			t.Fatalf("root %s: prepared rows differ from its own read", c.paths.StateDir)
		}
		loaded, err := resources.Load(c.paths)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(prepared.registry, loaded) {
			t.Fatalf("root %s: prepared Registry differs from the loader", c.paths.StateDir)
		}
	}
	a, _ := mustPrepare(t, first, nil)
	b, _ := mustPrepare(t, second, nil)
	if reflect.DeepEqual(a.Snapshot(), b.Snapshot()) {
		t.Fatal("fixtures must differ")
	}
	if reused, reuse := mustPrepare(t, second, a); reused == a || reuse.Reused || !reflect.DeepEqual(reused.Snapshot(), b.Snapshot()) {
		t.Fatal("another root's handle was reused")
	}
}

// Prepare keeps ReadRegistrySnapshotCached's refusals and cancellation
// checkpoints, and its loader cause stays reachable.
func TestPreparedSnapshotRefusalAndCancellation(t *testing.T) {
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	counts := countSnapshotReads(t, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.PrepareRegistrySnapshot(canceled, nil); !errors.Is(err, context.Canceled) || counts.loads != 0 || counts.stats != 0 {
		t.Fatalf("entry cancellation: %v %+v", err, counts)
	}
	ctx := &snapshotCancelContext{Context: context.Background()}
	if prepared, _, err := c.PrepareRegistrySnapshot(ctx, nil); prepared != nil || !errors.Is(err, context.Canceled) || ctx.calls != 2 || counts.loads != 1 {
		t.Fatalf("read cancellation: %v calls %d %+v", err, ctx.calls, counts)
	}
	first, _ := mustPrepare(t, c, nil)
	ctx = &snapshotCancelContext{Context: context.Background()}
	if prepared, _, err := c.PrepareRegistrySnapshot(ctx, first); prepared != nil || !errors.Is(err, context.Canceled) || ctx.calls != 2 || counts.loads != 2 {
		t.Fatalf("reuse cancellation: %v calls %d %+v", err, ctx.calls, counts)
	}

	unsupported, _ := snapshotClient(t, []byte(`{"schemaVersion":999}`))
	_, cachedErr := unsupported.ReadRegistrySnapshotCached(context.Background(), nil)
	_, _, err := unsupported.PrepareRegistrySnapshot(context.Background(), nil)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable || cachedErr == nil || err.Error() != cachedErr.Error() || !errors.Is(err, metadata.ErrSchemaTooNew) {
		t.Fatalf("refusal parity: %v / %v", err, cachedErr)
	}
	sentinel := errors.New("captured paths")
	c.pathsErr = sentinel
	if _, _, err := c.PrepareRegistrySnapshot(context.Background(), first); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := c.ResolveResourceGraphFromPrepared(context.Background(), first, PreparedGraphOverlay{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// IsVirtualWindow is the Registry's own exact-anchor rule for every anchor
// shape, independent of Agent phase.
func TestPreparedVirtualWindowEligibilityParity(t *testing.T) {
	variants := map[string]func(*metadata.Registry){
		"shell-anchor":   func(*metadata.Registry) {},
		"process-anchor": func(r *metadata.Registry) { r.Windows[0].Spec.AnchorPaneRef = "pane-02" },
		"process-anchor-offline": func(r *metadata.Registry) {
			r.Windows[0].Spec.AnchorPaneRef = "pane-02"
			r.Agents[0].Status.Phase = metadata.PhaseOffline
		},
		"missing-anchor": func(r *metadata.Registry) { r.Windows[0].Spec.AnchorPaneRef = "pane-absent" },
		"foreign-anchor": func(r *metadata.Registry) {
			r.Windows[0].Spec.AnchorPaneRef = "pane-02"
			r.Agents[0].Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-elsewhere"}
		},
	}
	virtual := map[bool]int{}
	for name, change := range variants {
		t.Run(name, func(t *testing.T) {
			c, paths := graphFixtureClient(t, mutatedGraphFixture(t, change))
			prepared, _ := mustPrepare(t, c, nil)
			loaded, err := resources.Load(paths)
			if err != nil {
				t.Fatal(err)
			}
			uids := []string{"", "window-absent"}
			for _, window := range loaded.Windows {
				uids = append(uids, window.Metadata.UID, " "+window.Metadata.UID+" ")
			}
			for _, uid := range uids {
				got, want := prepared.IsVirtualWindow(uid), loaded.IsVirtualWindow(uid)
				if got != want {
					t.Fatalf("%q: prepared %v, Registry %v", uid, got, want)
				}
				virtual[got]++
			}
		})
	}
	if virtual[true] == 0 || virtual[false] == 0 {
		t.Fatalf("variants must include virtual and non-virtual Windows: %v", virtual)
	}
}
