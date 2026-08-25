package app

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The four routes C-5 Guarantee has to survive with a control-owned Window in
// the Registry: reconcile, prune, pins, materialize.
//
// Each of them reads the whole Registry for its own reason and each of them
// used to be written when Project was the only root. `#705` proved the failure
// mode is not hypothetical -- the reconcile commit projection dropped a control
// graph and the Registry refused to validate -- so the other three get the same
// fixture rather than an argument about why they are probably fine.

// controlOwnedSeed is what one seeded control session left behind, so an
// assertion can compare the graph after a route against the graph before it.
type controlOwnedSeed struct {
	binding coremetadata.ControlSessionBinding
	window  coremetadata.Window
	pane    coremetadata.Pane
}

// seedControlOwnedGraph binds one app-owned control session into a fixture
// store, exactly the way `projmux shell` does.
//
// It goes through Mutator.BindControlSession rather than writing literal rows
// because the routes under test are supposed to meet the real thing: real uids,
// the real name reservations, the real owner refs. The hand-written rows in
// controlOwnerFixtureRegistry answer a different question -- what a projection
// does with stated input -- and both are worth having.
//
// server may be nil for a route that never reads tmux. When it is not nil the
// live session is marked and its window/pane carry the minted uids, so the
// session is control-owned on both sides at once.
func seedControlOwnedGraph(t *testing.T, store *fakeResourceStore, server *fakeTmux, session string) controlOwnedSeed {
	t.Helper()

	observed := coremetadata.ControlSessionObservation{
		Session: session,
		Windows: []coremetadata.ControlSessionWindow{{
			DisplayName: session,
			Panes:       []coremetadata.ControlSessionPane{{Command: "zsh", CWD: t.TempDir()}},
		}},
	}
	var live *fakeTmuxSession
	if server != nil {
		live = server.addSession(session)
		live.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
		observed.Windows[0].DisplayName = live.windows[0].name
	}
	binding, err := store.mutator().BindControlSession(&store.registry, observed, "/bin/zsh", "op-control-fixture", nil)
	if err != nil {
		t.Fatalf("bind control session %q: %v", session, err)
	}
	store.registry = store.registry.Normalize()
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("registry seeded with a control session %q is invalid: %v", session, err)
	}
	if len(binding.Windows) != 1 || len(binding.Panes) != 1 {
		t.Fatalf("control session %q bound %d window(s) and %d pane(s), want 1 and 1",
			session, len(binding.Windows), len(binding.Panes))
	}
	if server != nil {
		liveWindow, livePane := live.windows[0], live.windows[0].panes[0]
		liveWindow.opts[tmuxopts.WindowUID] = binding.Windows[0].UID
		liveWindow.opts[tmuxopts.WindowName] = binding.Windows[0].Name
		livePane.opts[tmuxopts.PaneUID] = binding.Panes[0].UID
		livePane.opts[tmuxopts.PaneName] = binding.Panes[0].Name
	}
	window, ok := store.registry.Window(binding.Windows[0].UID)
	if !ok {
		t.Fatalf("control session %q minted no Window resource", session)
	}
	pane, ok := store.registry.Pane(binding.Panes[0].UID)
	if !ok {
		t.Fatalf("control session %q minted no Pane resource", session)
	}
	return controlOwnedSeed{binding: binding, window: window.Clone(), pane: pane.Clone()}
}

// assertControlGraphIntact is the shared post-condition of all four routes: a
// route whose scope is Projects may not touch the control graph, and it may not
// leave a Registry that will not validate.
func assertControlGraphIntact(t *testing.T, route string, registry coremetadata.Registry, seed controlOwnedSeed) {
	t.Helper()

	if err := registry.Validate(); err != nil {
		t.Fatalf("%s left an invalid Registry: %v", route, err)
	}
	control, ok := registry.ControlSession(seed.binding.ControlSession.Metadata.UID)
	if !ok {
		t.Fatalf("%s dropped the ControlSession root: %+v", route, registry.ControlSessions)
	}
	if control.Spec.Session != seed.binding.ControlSession.Spec.Session {
		t.Errorf("%s rewrote the ControlSession identity: %q, want %q",
			route, control.Spec.Session, seed.binding.ControlSession.Spec.Session)
	}
	window, ok := registry.Window(seed.window.Metadata.UID)
	if !ok || !reflect.DeepEqual(*window, seed.window) {
		t.Errorf("%s changed the control-owned Window: %+v", route, window)
	}
	pane, ok := registry.Pane(seed.pane.Metadata.UID)
	if !ok || !reflect.DeepEqual(*pane, seed.pane) {
		t.Errorf("%s changed the control-owned Pane: %+v", route, pane)
	}
	for _, uid := range []string{
		seed.binding.ControlSession.Metadata.UID, seed.window.Metadata.UID, seed.pane.Metadata.UID,
	} {
		if !slices.ContainsFunc(registry.NameReservations, func(r coremetadata.NameReservation) bool { return r.UID == uid }) {
			t.Errorf("%s dropped the name reservation of control-graph uid %q", route, uid)
		}
	}
}

// TestControlOwnedRegistryKeepsReconcileGreen is acceptance criterion 2, route
// 1 of 4.
func TestControlOwnedRegistryKeepsReconcileGreen(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("bootstrap reconciliation: %v", err)
	}
	seed := seedControlOwnedGraph(t, store, server, "home")

	stdout, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("reconcile over a control-owned Registry: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, `"outcome": "failed"`) {
		t.Fatalf("reconcile reported a failed item:\n%s", stdout)
	}
	assertControlGraphIntact(t, "reconcile resources", store.registry, seed)
}

// TestControlOwnedRegistryKeepsPruneGreen is acceptance criterion 2, route 2 of
// 4.
//
// `prune project` is a Project-only traversal on purpose -- it selects on the
// MissingRoot condition and a ControlSession has no root to lose -- so what has
// to hold here is that the destructive route completes and leaves the control
// graph exactly where it was, reservations included.
func TestControlOwnedRegistryKeepsPruneGreen(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	seed := seedControlOwnedGraph(t, store, nil, "home")
	if _, ok := store.registry.Project("prj-gone"); !ok {
		t.Fatalf("fixture has no missing-root Project to prune")
	}

	stdout, stderr, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h", "--yes")
	if err != nil {
		t.Fatalf("prune project over a control-owned Registry: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "prune project: deleted 1 project") {
		t.Fatalf("prune did not delete the missing-root Project:\n%s", stdout)
	}
	if _, ok := store.registry.Project("prj-gone"); ok {
		t.Fatalf("prune reported a deletion it did not make")
	}
	assertControlGraphIntact(t, "prune project", store.registry, seed)
}

// TestControlOwnedRegistryKeepsPinsGreen is acceptance criterion 2, route 3 of
// 4, and it is the enforcement half of C-5's Non-Guarantee row: a ControlSession
// is not a managed root, so it must never reach a pin.
func TestControlOwnedRegistryKeepsPinsGreen(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	seed := seedControlOwnedGraph(t, store, nil, "home")
	controlUID := seed.binding.ControlSession.Metadata.UID

	refs := projectRefsOf(store.registry)
	if got, want := len(refs), len(store.registry.Projects); got != want {
		t.Fatalf("projectRefsOf returned %d refs over %d Projects", got, want)
	}
	for _, ref := range refs {
		if ref.UID == controlUID {
			t.Fatalf("the ControlSession uid reached the pin resolver as a ProjectRef: %+v", ref)
		}
		if strings.TrimSpace(ref.Root) == "" {
			t.Fatalf("a pin ProjectRef carries no root: %+v", ref)
		}
	}

	// Both pin spellings, over the same control-owned Registry: a managed pin on
	// a real Project, and a legacy path pin naming the control session's own
	// pane cwd.
	authority := authorityOver(newStubPinStore("prj-alpha"), refs...)
	legacy := authorityOver(&stubSwitchPinStore{set: pins.Set{Format: pins.FormatLegacy}.With(pins.Pin{
		Kind: pins.KindCandidate, Value: seed.pane.Spec.CWD,
	})}, refs...)

	selection, err := authority.selection()
	if err != nil {
		t.Fatalf("pin selection over a control-owned Registry: %v", err)
	}
	if !selection.pinnedProject("prj-alpha") {
		t.Errorf("the managed pin stopped resolving")
	}
	if selection.pinnedProject(controlUID) {
		t.Errorf("the ControlSession uid reads as a pinned Project")
	}
	rows, resolution, err := authority.pinnedRows()
	if err != nil {
		t.Fatalf("pinned rows over a control-owned Registry: %v", err)
	}
	if len(rows) != 1 || rows[0].Reference != "uid:prj-alpha" || rows[0].Root != "/srv/alpha" {
		t.Errorf("pinned rows = %+v", rows)
	}
	if len(resolution.Ambiguous) != 0 {
		t.Errorf("a control-owned Registry made a pin ambiguous: %+v", resolution.Ambiguous)
	}
	paths, err := authority.discoveryPaths()
	if err != nil {
		t.Fatalf("discovery paths over a control-owned Registry: %v", err)
	}
	if !slices.Equal(paths, []string{"/srv/alpha"}) {
		t.Errorf("discovery paths = %v, want [/srv/alpha]", paths)
	}

	// C-5's Non-Guarantee, stated as behaviour: a path a ControlSession's own
	// Pane sits in is not a managed root, so migration leaves it a candidate
	// rather than typing it onto anything. Nothing here may mint a Project for
	// it either.
	migrated, err := legacy.resolved()
	if err != nil {
		t.Fatalf("legacy pin resolution over a control-owned Registry: %v", err)
	}
	if len(migrated.Moved) != 0 || len(migrated.Ambiguous) != 0 {
		t.Errorf("a control-session pane cwd was migrated onto a managed pin: %+v", migrated)
	}
	if !slices.Equal(migrated.Kept, []string{seed.pane.Spec.CWD}) {
		t.Errorf("legacy resolution Kept = %v, want [%s]", migrated.Kept, seed.pane.Spec.CWD)
	}

	assertControlGraphIntact(t, "pins", store.registry, seed)
	if store.transactions != 0 || store.writes != 0 {
		t.Errorf("the pin routes opened %d transaction(s) and %d write(s) on the Registry", store.transactions, store.writes)
	}
}

// TestControlOwnedRegistryKeepsMaterializeGreen is acceptance criterion 2, route
// 4 of 4.
func TestControlOwnedRegistryKeepsMaterializeGreen(t *testing.T) {
	t.Parallel()

	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	seed := seedControlOwnedGraph(t, store, nil, "home")

	preview, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("materialize dry-run over a control-owned Registry: err=%v stderr=%q\n%s", err, stderr, preview)
	}
	if strings.Contains(preview, `"kind": "ControlSession"`) {
		t.Fatalf("a Project-scoped materialization planned work on a ControlSession:\n%s", preview)
	}
	stdout, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("materialize execute over a control-owned Registry: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, `"outcome": "failed"`) || strings.Contains(stdout, `"outcome": "refused"`) {
		t.Fatalf("materialize did not converge:\n%s", stdout)
	}
	if server.session("beta") == nil {
		t.Fatalf("materialize created no session; server holds %v", server.sessionNames())
	}
	assertControlGraphIntact(t, "materialize", store.registry, seed)
}

// TestRegistryResourceRecordsCarryBothRootKinds pins the projection this slice
// repaired, behaviourally rather than by inspection.
//
// registryUIDSet is what decides whether a live tmux object's uid is one projmux
// owns, and registryReconcileItems is what decides whether a Registry change is
// reported at all. A root kind missing from the record projection is invisible
// to both.
func TestRegistryResourceRecordsCarryBothRootKinds(t *testing.T) {
	t.Parallel()

	without := controlOwnerFixtureRegistry(false)
	with := controlOwnerFixtureRegistry(true)

	known := registryUIDSet(with)
	for _, uid := range []string{"prj-alpha", "ctl-home", "win-home", "pan-home"} {
		if !known[uid] {
			t.Errorf("registryUIDSet does not carry %q", uid)
		}
	}

	items := registryReconcileItems(without, with, newPlanUIDNormalizerWithAllocations(without, with, nil))
	var control []resourceReconcileItem
	for _, item := range items {
		if item.Kind == string(coremetadata.KindControlSession) {
			control = append(control, item)
		}
	}
	if len(control) != 1 {
		t.Fatalf("an appearing ControlSession produced %d plan item(s), want 1: %+v", len(control), items)
	}
	if control[0].Action != "create" || control[0].Drift != resourceDriftMissing {
		t.Errorf("ControlSession item = %+v, want a missing/create row", control[0])
	}
	if control[0].Target != "controlsession/home" {
		t.Errorf("ControlSession item target = %q, want %q", control[0].Target, "controlsession/home")
	}

	// The converse: an unchanged control root is not churn. A projection that
	// reported one every pass would be as wrong as one that reported none.
	if got := registryReconcileItems(with, with, newPlanUIDNormalizerWithAllocations(with, with, nil)); len(got) != 0 {
		t.Errorf("an unchanged Registry produced %d plan item(s): %+v", len(got), got)
	}
}
