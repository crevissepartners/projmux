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

// countSnapshotReads counts the loader (every Registry decode) and the stat of
// ReadRegistrySnapshotCached for one test. after, when set, runs after each
// load, between the two stats.
type snapshotReadCounts struct{ loads, stats int }

func countSnapshotReads(t *testing.T, after func()) *snapshotReadCounts {
	t.Helper()
	counts := &snapshotReadCounts{}
	load, stat := loadSnapshotRegistry, statSnapshotRegistry
	loadSnapshotRegistry = func(paths config.Paths) (metadata.Registry, error) {
		counts.loads++
		registry, err := load(paths)
		if after != nil {
			after()
		}
		return registry, err
	}
	statSnapshotRegistry = func(paths config.Paths) (store.RegistryFileIdentity, error) {
		counts.stats++
		return stat(paths)
	}
	t.Cleanup(func() { loadSnapshotRegistry, statSnapshotRegistry = load, stat })
	return counts
}

// replaceRegistryFile rewrites the Registry atomically, as writers do, so the
// stat identity changes even when the bytes do not.
func replaceRegistryFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	next := path + ".next"
	if err := os.WriteFile(next, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

// The snapshot carries the whole stored envelope and every collection in
// stored order, with values equal to the loaded Registry's.
func TestReadRegistrySnapshotEnvelopeAndAllRows(t *testing.T) {
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	loaded, err := resources.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != loaded.APIVersion || got.SchemaVersion != loaded.SchemaVersion || !got.UpdatedAt.Equal(loaded.UpdatedAt) || got.UpdatedAt.IsZero() {
		t.Fatalf("envelope %q %d %s vs %q %d %s", got.APIVersion, got.SchemaVersion, got.UpdatedAt, loaded.APIVersion, loaded.SchemaVersion, loaded.UpdatedAt)
	}
	if len(got.ControlSessions) != 1 || len(got.NameReservations) == 0 {
		t.Fatalf("fixture must carry ControlSessions and NameReservations: %+v", got)
	}
	// The snapshot JSON is the stored Registry JSON: same keys, order and values.
	want, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(gotJSON) {
		t.Fatalf("snapshot JSON differs from stored Registry JSON:\n got %s\nwant %s", gotJSON, want)
	}
	got.ControlSessions[0].Metadata.Name = "changed"
	got.NameReservations[0].Name = "changed"
	fresh, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ControlSessions[0].Metadata.Name != "home" || fresh.NameReservations[0].Name != loaded.NameReservations[0].Name {
		t.Fatal("returned rows alias a later read")
	}
}

// An unchanged file is reused without a decode and the same memory comes back;
// any doubt about the file reads it again, once.
func TestReadRegistrySnapshotCachedReusesUnchangedFile(t *testing.T) {
	ctx := context.Background()
	raw := graphFixtureRegistry(t)
	c, paths := graphFixtureClient(t, raw)
	path := store.PathFor(paths.StateDir)
	counts := countSnapshotReads(t, nil)

	first, err := c.ReadRegistrySnapshotCached(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.NewDefaultStore(paths).RegistryFileIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 1 || counts.stats != 2 || first.Reused || first.RegistryIdentity == nil || *first.RegistryIdentity != publicRegistryFileIdentity(identity) {
		t.Fatalf("first read: %+v identity %+v", counts, first.RegistryIdentity)
	}
	plain, err := c.ReadRegistrySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, first.Snapshot) {
		t.Fatal("cached read differs from ReadRegistrySnapshot")
	}

	second, err := c.ReadRegistrySnapshotCached(ctx, &first)
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 1 || counts.stats != 3 || !second.Reused || second.RegistryIdentity == nil || *second.RegistryIdentity != *first.RegistryIdentity {
		t.Fatalf("reuse: %+v %+v", counts, second)
	}
	if second.RegistryIdentity == first.RegistryIdentity {
		t.Fatal("reused identity shares the caller's pointer")
	}
	if &second.Snapshot.Projects[0] != &first.Snapshot.Projects[0] || &second.Snapshot.Panes[0] != &first.Snapshot.Panes[0] || &second.Snapshot.NameReservations[0] != &first.Snapshot.NameReservations[0] {
		t.Fatal("reused snapshot is not the previous memory")
	}

	// Doubtful previous values always read: nil identity, a different identity.
	for _, previous := range []core.RegistrySnapshotRead{
		{Snapshot: first.Snapshot},
		{Snapshot: first.Snapshot, RegistryIdentity: &core.RegistryFileIdentity{Inode: first.RegistryIdentity.Inode + 1}},
	} {
		loads := counts.loads
		got, err := c.ReadRegistrySnapshotCached(ctx, &previous)
		if err != nil {
			t.Fatal(err)
		}
		if counts.loads != loads+1 || got.Reused || got.RegistryIdentity == nil || &got.Snapshot.Projects[0] == &first.Snapshot.Projects[0] {
			t.Fatalf("doubtful previous reused: %+v %+v", counts, got)
		}
	}

	// A rewrite with the same bytes still changes the identity and is read.
	replaceRegistryFile(t, path, raw)
	loads := counts.loads
	third, err := c.ReadRegistrySnapshotCached(ctx, &first)
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != loads+1 || third.Reused || third.RegistryIdentity == nil || *third.RegistryIdentity == *first.RegistryIdentity {
		t.Fatalf("rewritten file reused: %+v %+v", counts, third)
	}
	if !reflect.DeepEqual(third.Snapshot, first.Snapshot) {
		t.Fatal("same bytes decoded differently")
	}
}

// A rewrite between the two stats leaves the identity nil, and a nil identity
// is never reused.
func TestReadRegistrySnapshotCachedOmitsIdentityWhenFileChanges(t *testing.T) {
	ctx := context.Background()
	raw := graphFixtureRegistry(t)
	c, paths := graphFixtureClient(t, raw)
	path := store.PathFor(paths.StateDir)
	rewrite := true
	counts := countSnapshotReads(t, func() {
		if rewrite {
			replaceRegistryFile(t, path, raw)
		}
	})
	changed, err := c.ReadRegistrySnapshotCached(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed.RegistryIdentity != nil || changed.Reused || len(changed.Snapshot.Projects) == 0 {
		t.Fatalf("changed read: %+v", changed)
	}
	rewrite = false
	again, err := c.ReadRegistrySnapshotCached(ctx, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 2 || again.Reused || again.RegistryIdentity == nil {
		t.Fatalf("nil identity reused: %+v %+v", counts, again)
	}
}

// First use reads empty without creating state and with no identity, so it is
// read again each time.
func TestReadRegistrySnapshotCachedFirstUse(t *testing.T) {
	ctx := context.Background()
	c, root := snapshotClient(t, nil)
	if err := os.RemoveAll(filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	counts := countSnapshotReads(t, nil)
	first, err := c.ReadRegistrySnapshotCached(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.ReadRegistrySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.RegistryIdentity != nil || first.Reused || !reflect.DeepEqual(first.Snapshot, plain) {
		t.Fatalf("first use: %+v", first)
	}
	again, err := c.ReadRegistrySnapshotCached(ctx, &first)
	if err != nil || again.Reused || counts.loads != 2 {
		t.Fatalf("first use reused: %+v %+v %v", counts, again, err)
	}
	if !reflect.DeepEqual(before, snapshotTree(t, root)) {
		t.Fatal("first use changed the filesystem")
	}
}

// Loader failures keep ReadRegistrySnapshot's refusal and detail, and the
// loader's cause stays reachable through errors.Is and errors.As.
func TestReadRegistrySnapshotCachedRefusalKeepsCause(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       []byte
		marker    bool
		directory bool
		sentinel  error
	}{
		{name: "initialized-missing", marker: true, sentinel: store.ErrRegistryStateLost},
		{name: "initialized-empty", raw: []byte{}, marker: true},
		{name: "malformed", raw: []byte("{"), sentinel: store.ErrMalformedRegistry},
		{name: "unsupported", raw: []byte(`{"schemaVersion":999}`), sentinel: metadata.ErrSchemaTooNew},
		{name: "read-directory", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, root := snapshotClient(t, tc.raw)
			path := store.PathFor(c.paths.StateDir)
			if tc.marker {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "registry.initialized"), []byte("marker"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.directory {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTree(t, root)
			_, loadErr := resources.Load(c.paths)
			var failure *resources.Failure
			if !errors.As(loadErr, &failure) || failure.Cause == nil {
				t.Fatalf("fixture must fail in the loader: %v", loadErr)
			}
			_, plainErr := c.ReadRegistrySnapshot(context.Background())
			_, err := c.ReadRegistrySnapshotCached(context.Background(), nil)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable || plainErr == nil || err.Error() != plainErr.Error() {
				t.Fatalf("refusal parity: %v / %v", err, plainErr)
			}
			// Each load builds its own error values, so compare the unwrapped
			// chain by type and text; the sentinels below compare identity.
			var cause *resources.Failure
			if !errors.As(err, &cause) || cause.Code != failure.Code || errors.Unwrap(err) != error(cause) ||
				reflect.TypeOf(cause.Cause) != reflect.TypeOf(failure.Cause) || cause.Cause.Error() != failure.Cause.Error() {
				t.Fatalf("cause lost: %v", err)
			}
			if tc.sentinel != nil && (!errors.Is(loadErr, tc.sentinel) || !errors.Is(err, tc.sentinel)) {
				t.Fatalf("sentinel %v: loader %v, SDK %v", tc.sentinel, loadErr, err)
			}
			if !reflect.DeepEqual(before, snapshotTree(t, root)) {
				t.Fatal("error path changed filesystem")
			}
		})
	}
	c, _ := snapshotClient(t, nil)
	sentinel := errors.New("captured paths")
	c.pathsErr = sentinel
	if _, err := c.ReadRegistrySnapshotCached(context.Background(), nil); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// Cancellation is checked on entry, before any stat or load, and before
// returning success, on both the read and the reuse path.
func TestReadRegistrySnapshotCachedCancellation(t *testing.T) {
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	counts := countSnapshotReads(t, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadRegistrySnapshotCached(canceled, nil); !errors.Is(err, context.Canceled) || counts.loads != 0 || counts.stats != 0 {
		t.Fatalf("entry cancellation: %v %+v", err, counts)
	}
	ctx := &snapshotCancelContext{Context: context.Background()}
	if _, err := c.ReadRegistrySnapshotCached(ctx, nil); !errors.Is(err, context.Canceled) || ctx.calls != 2 || counts.loads != 1 {
		t.Fatalf("read cancellation: %v calls %d %+v", err, ctx.calls, counts)
	}
	first, err := c.ReadRegistrySnapshotCached(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx = &snapshotCancelContext{Context: context.Background()}
	if _, err := c.ReadRegistrySnapshotCached(ctx, &first); !errors.Is(err, context.Canceled) || ctx.calls != 2 || counts.loads != 2 {
		t.Fatalf("reuse cancellation: %v calls %d %+v", err, ctx.calls, counts)
	}
}
