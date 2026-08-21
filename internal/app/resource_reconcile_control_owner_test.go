package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// controlOwnerFixtureRegistry builds the exact shape the live regression was
// reported on: one Project whose session is on the target socket, plus one
// app-owned ControlSession that OWNS a Window and a Pane.
//
// It is written by hand rather than through BindControlSession so the uids and
// the reservation rows are literal: the defect under test is a projection that
// keeps a reservation whose resource it dropped, and that is only legible when
// the input rows are stated rather than derived.
func controlOwnerFixtureRegistry(control bool) coremetadata.Registry {
	created := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	registry := coremetadata.NewRegistry()
	reserve := func(scope string, kind coremetadata.Kind, name, uid string) {
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
			Scope: scope, Kind: kind, Name: name, UID: uid,
		})
	}
	meta := func(uid, name string, owner *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: owner, CreatedAt: created}
	}
	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}

	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: meta("prj-alpha", "alpha", nil),
		Spec:     coremetadata.ProjectSpec{Root: "/srv/alpha", PrimaryWindowRef: "win-alpha-main"},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "alpha", Live: true}},
	}}
	reserve("", coremetadata.KindProject, "alpha", "prj-alpha")

	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: meta("win-alpha-main", "main", ownedBy(coremetadata.KindProject, "prj-alpha")),
		Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-alpha-zsh"},
	}}
	reserve("prj-alpha", coremetadata.KindWindow, "main", "win-alpha-main")

	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: meta("pan-alpha-zsh", "zsh", ownedBy(coremetadata.KindWindow, "win-alpha-main")),
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
	}}
	reserve("win-alpha-main", coremetadata.KindPane, "zsh", "pan-alpha-zsh")

	if control {
		registry.ControlSessions = []coremetadata.ControlSession{{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession,
			Metadata: meta("ctl-home", "home", nil),
			Spec:     coremetadata.ControlSessionSpec{Session: "home"},
		}}
		reserve("", coremetadata.KindControlSession, "home", "ctl-home")

		registry.Windows = append(registry.Windows, coremetadata.Window{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-home", "window", ownedBy(coremetadata.KindControlSession, "ctl-home")),
			Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-home"},
		})
		reserve("ctl-home", coremetadata.KindWindow, "window", "win-home")

		registry.Panes = append(registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-home", "zsh", ownedBy(coremetadata.KindWindow, "win-home")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/home/operator"},
		})
		reserve("win-home", coremetadata.KindPane, "zsh", "pan-home")
	}
	return registry.Normalize()
}

func controlOwnerFixtureSessions() []observedResourceProjectSession {
	return []observedResourceProjectSession{{id: "$0", name: "alpha", uid: "prj-alpha", root: "/srv/alpha"}}
}

func controlOwnerFixtureReconciler() *registryReconciler {
	return &registryReconciler{
		refusedSessions: map[string]bool{},
		sessionNameFor:  func(string) string { return "" },
	}
}

// TestResourceReconcileProjectionsAreReservationConsistent is the invariant the
// live regression violated: every projection the reconcile plan builds must
// name a resource for every reservation it keeps, and keep a reservation for
// every resource it names.
func TestResourceReconcileProjectionsAreReservationConsistent(t *testing.T) {
	t.Parallel()

	for _, control := range []bool{false, true} {
		before := controlOwnerFixtureRegistry(control)
		if err := before.Validate(); err != nil {
			t.Fatalf("fixture(control=%v) is not a valid registry: %v", control, err)
		}
		sessions, reconciler := controlOwnerFixtureSessions(), controlOwnerFixtureReconciler()
		projects := resourceProjectUIDsForSessions(before, sessions, reconciler)
		scopedBefore := scopeResourceRegistry(before, sessions, reconciler)
		scopedAfter := scopedBefore.Clone()

		// Scope projections are allocator working sets: they legitimately carry
		// reservations whose bodies are out of the mutation scope, so full
		// Validate is the wrong question for them. What they may never do is the
		// converse -- carry a resource the reservation table does not name.
		for name, projection := range map[string]coremetadata.Registry{
			"resourceRegistryProjectGraph": resourceRegistryProjectGraph(before, projects),
			"scopeResourceRegistry":        scopedBefore,
		} {
			for _, unreserved := range unreservedProjectionRows(projection) {
				t.Errorf("%s(control=%v) carries %s with no name reservation", name, control, unreserved)
			}
		}

		// The committed projection has no such licence. It is the exact value the
		// controller kernel assigns to the working registry, so it must validate
		// with no pruning at all.
		merged := mergeScopedResourceRegistry(before, scopedBefore, scopedAfter, sessions, reconciler)
		if err := merged.Validate(); err != nil {
			t.Errorf("mergeScopedResourceRegistry(control=%v) commit projection is invalid: %v", control, err)
		}

		// "For any input" includes an input the ordinary pass is not supposed to
		// produce: a scoped half whose Project stopped resolving between the two
		// halves, so the merge has a removal set with nothing to put back. The
		// resource is gone either way; what must not survive it is the
		// reservation, because that is the row that makes the whole registry
		// unwritable rather than just incomplete.
		vanished := scopedBefore.Clone()
		vanished.Projects, vanished.Windows, vanished.Panes, vanished.Agents = nil, nil, nil, nil
		orphaned := mergeScopedResourceRegistry(before, scopedBefore, vanished, sessions, reconciler)
		if err := orphaned.Validate(); err != nil {
			t.Errorf("mergeScopedResourceRegistry(control=%v) over a vanished scope is invalid: %v", control, err)
		}
	}
}

// TestResourceReconcileForeignReasonsNameTheOwnerScopeTheyMean pins the refuse
// vocabulary across the three cases a live Window can be in, because the
// wording is user-facing parity for two of them and was simply untrue for the
// third: a Window a ControlSession legitimately owns was reported as being
// "outside the exact Project owner scope" it had never been inside.
func TestResourceReconcileForeignReasonsNameTheOwnerScopeTheyMean(t *testing.T) {
	t.Parallel()

	registry := controlOwnerFixtureRegistry(true)
	for _, row := range []struct {
		name    string
		object  observedPlanObject
		want    string
		refused bool
	}{
		{
			name:   "control-owned Window in its own control session is not drift",
			object: observedPlanObject{kind: coremetadata.KindWindow, target: "@0", uid: "win-home", session: "home"},
		},
		{
			name:    "control-owned Window somewhere else names its ControlSession scope",
			object:  observedPlanObject{kind: coremetadata.KindWindow, target: "@9", uid: "win-home", session: "alpha"},
			want:    "live Window uid is outside the exact ControlSession owner scope",
			refused: true,
		},
		{
			name:    "Project-owned Window outside its Project keeps the exact pre-existing wording",
			object:  observedPlanObject{kind: coremetadata.KindWindow, target: "@7", uid: "win-alpha-main", session: "elsewhere"},
			want:    "live Window uid is outside the exact Project owner scope",
			refused: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			recorder := newResourcePlanTmuxRunner(nil)
			recorder.objects = []observedPlanObject{row.object}
			items := recorder.foreignItems(registry, controlOwnerFixtureReconciler())
			if !row.refused {
				if len(items) != 0 {
					t.Fatalf("expected no refused item, got %+v", items)
				}
				return
			}
			if len(items) != 1 {
				t.Fatalf("expected exactly one refused item, got %+v", items)
			}
			if items[0].Reason != row.want {
				t.Fatalf("reason = %q, want %q", items[0].Reason, row.want)
			}
			if !items[0].refused || items[0].Drift != resourceDriftForeign {
				t.Fatalf("item is not refused foreign drift: %+v", items[0])
			}
		})
	}
}

// TestMergedResourceProjectionCarriesControlGraphsUnchanged states the other
// half of the invariant: a ControlSession graph is outside `reconcile
// resources`' mutation scope, so it survives the round trip byte-identically
// rather than being dropped and reattached.
func TestMergedResourceProjectionCarriesControlGraphsUnchanged(t *testing.T) {
	t.Parallel()

	before := controlOwnerFixtureRegistry(true)
	sessions, reconciler := controlOwnerFixtureSessions(), controlOwnerFixtureReconciler()
	scopedBefore := scopeResourceRegistry(before, sessions, reconciler)
	merged := mergeScopedResourceRegistry(before, scopedBefore, scopedBefore.Clone(), sessions, reconciler)

	if _, ok := merged.Window("win-home"); !ok {
		t.Fatalf("control-owned Window dropped from the commit projection: %+v", merged.Windows)
	}
	if _, ok := merged.Pane("pan-home"); !ok {
		t.Fatalf("control-owned Pane dropped from the commit projection: %+v", merged.Panes)
	}
	if _, ok := merged.ControlSession("ctl-home"); !ok {
		t.Fatalf("ControlSession dropped from the commit projection: %+v", merged.ControlSessions)
	}
	// Sorted, because the merge has always emitted the retained half before the
	// reconciled half and that ordering is not what this pins. What it pins is
	// that a round trip whose reconcile changed nothing carries every control
	// row through with the same uid, name, owner, and reservation.
	merged.UpdatedAt = before.UpdatedAt
	if got, want := registryDigest(merged), registryDigest(before); got != want {
		t.Fatalf("commit projection changed a no-op round trip:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// A second pass over the first pass's output must be a fixed point, or every
	// reconcile would rewrite the registry forever.
	repeatScoped := scopeResourceRegistry(merged, sessions, reconciler)
	repeat := mergeScopedResourceRegistry(merged, repeatScoped, repeatScoped.Clone(), sessions, reconciler)
	repeat.UpdatedAt = merged.UpdatedAt
	if !reflect.DeepEqual(repeat.Normalize(), merged.Normalize()) {
		t.Fatalf("commit projection is not a fixed point:\n--- second ---\n%s\n--- first ---\n%s",
			registryDigest(repeat), registryDigest(merged))
	}
}

// unreservedProjectionRows names every resource a projection carries that its
// reservation table does not, which is the half of Registry.Validate a scope
// projection is still answerable for.
func unreservedProjectionRows(registry coremetadata.Registry) []string {
	reserved := map[string]bool{}
	for _, reservation := range registry.NameReservations {
		reserved[reservation.UID] = true
	}
	var missing []string
	record := func(kind coremetadata.Kind, uid, name string) {
		if !reserved[uid] {
			missing = append(missing, string(kind)+" "+name+" (uid "+uid+")")
		}
	}
	for _, project := range registry.Projects {
		record(coremetadata.KindProject, project.Metadata.UID, project.Metadata.Name)
	}
	for _, control := range registry.ControlSessions {
		record(coremetadata.KindControlSession, control.Metadata.UID, control.Metadata.Name)
	}
	for _, window := range registry.Windows {
		record(coremetadata.KindWindow, window.Metadata.UID, window.Metadata.Name)
	}
	for _, pane := range registry.Panes {
		record(coremetadata.KindPane, pane.Metadata.UID, pane.Metadata.Name)
	}
	for _, agent := range registry.Agents {
		record(coremetadata.KindAgent, agent.Metadata.UID, agent.Metadata.Name)
	}
	return missing
}

// registryDigest renders the identity-bearing rows of a registry, sorted, so a
// round trip can be compared without depending on struct printing or on the
// order the merge happens to emit its two halves in.
func registryDigest(registry coremetadata.Registry) string {
	var rows []string
	row := func(line string) { rows = append(rows, line) }
	for _, control := range registry.ControlSessions {
		row("controlsession " + control.Metadata.UID + " " + control.Metadata.Name + " " + control.Spec.Session + "\n")
	}
	for _, project := range registry.Projects {
		row("project " + project.Metadata.UID + " " + project.Metadata.Name + " " + project.Spec.Root + "\n")
	}
	for _, window := range registry.Windows {
		row("window " + window.Metadata.UID + " " + window.Metadata.Name + " " + window.Metadata.OwnerUID() + "\n")
	}
	for _, pane := range registry.Panes {
		row("pane " + pane.Metadata.UID + " " + pane.Metadata.Name + " " + pane.Metadata.OwnerUID() + "\n")
	}
	for _, agent := range registry.Agents {
		row("agent " + agent.Metadata.UID + " " + agent.Metadata.Name + " " + agent.Metadata.OwnerUID() + "\n")
	}
	for _, reservation := range registry.NameReservations {
		row("reservation " + reservation.Scope + "/" + string(reservation.Kind) + "/" + reservation.Name + " " + reservation.UID + "\n")
	}
	slices.Sort(rows)
	return strings.Join(rows, "")
}

// TestPublicResourceReconcileCommitsWithControlOwnedResources is the live
// regression, end to end: with a Home ControlSession owning a Window and a
// Pane, `projmux reconcile resources` execute reached the Registry commit and
// aborted with `Window name reservation "window" refers to unknown uid`, so
// every planned repair -- including the two session option mirrors another
// recovery path reads -- was rolled back and nothing converged.
func TestPublicResourceReconcileCommitsWithControlOwnedResources(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("bootstrap reconciliation: %v", err)
	}
	project := store.registry.Projects[0]

	// A Home control session, exactly as `projmux shell` provisions it: an
	// app-owned session carrying the control role, owning one Window and one
	// Pane that are Registry resources and belong to no Project.
	home := server.addSession("home")
	home.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
	homeWindow, homePane := home.windows[0], home.windows[0].panes[0]
	binding, err := store.mutator().BindControlSession(&store.registry, coremetadata.ControlSessionObservation{
		Session: "home",
		Windows: []coremetadata.ControlSessionWindow{{
			DisplayName: homeWindow.name,
			Panes:       []coremetadata.ControlSessionPane{{Command: "zsh", CWD: t.TempDir()}},
		}},
	}, "/bin/zsh", "op-control-session", nil)
	if err != nil {
		t.Fatalf("bind control session: %v", err)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("seeded registry is invalid: %v", err)
	}
	controlWindowUID, controlPaneUID := binding.Windows[0].UID, binding.Panes[0].UID
	homeWindow.opts[tmuxopts.WindowUID] = controlWindowUID
	homeWindow.opts[tmuxopts.WindowName] = binding.Windows[0].Name
	homePane.opts[tmuxopts.PaneUID] = controlPaneUID
	homePane.opts[tmuxopts.PaneName] = binding.Panes[0].Name
	seededWindow, _ := store.registry.Window(controlWindowUID)
	seededPane, _ := store.registry.Pane(controlPaneUID)
	controlWindow, controlPane := seededWindow.Clone(), seededPane.Clone()

	// The exact drift the user reported: the Project session lost both option
	// mirrors, which is the repair the aborted commit never got to apply.
	alpha := server.session("alpha")
	delete(alpha.opts, tmuxopts.ProjectUIDSession)
	delete(alpha.opts, tmuxopts.ProjectNameSession)

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("control-owned dry-run: %v\n%s", err, preview)
	}
	stdout, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("control-owned execute: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, `"outcome": "failed"`) || strings.Contains(stdout, "name reservation") {
		t.Fatalf("execute reported a failed commit:\n%s", stdout)
	}
	// Dry-run and execute have to agree. The regression was exactly the
	// asymmetry: dry-run planned cleanly because it never opened the store,
	// while execute died the moment the projection met Validate.
	if got, want := reconcileRefusedTargets(t, stdout), reconcileRefusedTargets(t, preview); !reflect.DeepEqual(got, want) {
		t.Fatalf("dry-run and execute disagree on refused drift: execute=%v dry-run=%v", got, want)
	}

	if err := store.registry.Validate(); err != nil {
		t.Fatalf("committed registry is invalid: %v", err)
	}
	if got := alpha.opts[tmuxopts.ProjectUIDSession]; got != project.Metadata.UID {
		t.Fatalf("%s = %q, want %q", tmuxopts.ProjectUIDSession, got, project.Metadata.UID)
	}
	if got := alpha.opts[tmuxopts.ProjectNameSession]; got != project.Metadata.Name {
		t.Fatalf("%s = %q, want %q", tmuxopts.ProjectNameSession, got, project.Metadata.Name)
	}

	// The control graph is outside this command's mutation scope, so it comes
	// through the commit byte-identically rather than dropped and reattached.
	committedWindow, ok := store.registry.Window(controlWindowUID)
	if !ok || !reflect.DeepEqual(*committedWindow, controlWindow) {
		t.Fatalf("control-owned Window changed across the commit: %+v", committedWindow)
	}
	committedPane, ok := store.registry.Pane(controlPaneUID)
	if !ok || !reflect.DeepEqual(*committedPane, controlPane) {
		t.Fatalf("control-owned Pane changed across the commit: %+v", committedPane)
	}
	if _, ok := store.registry.ControlSession(binding.ControlSession.Metadata.UID); !ok {
		t.Fatalf("ControlSession dropped by the commit: %+v", store.registry.ControlSessions)
	}

	// A repeat pass writes nothing, which is what "converged" means here.
	writes := store.writes
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("repeat execute: %v\n%s", err, repeat)
	}
	if store.writes != writes {
		t.Fatalf("repeat pass wrote %d additional registry transaction(s)", store.writes-writes)
	}
}

// reconcileRefusedTargets extracts the refused drift rows of one report so a
// dry-run and an execute can be compared on the only thing that has to agree.
func reconcileRefusedTargets(t *testing.T, report string) []string {
	t.Helper()
	var decoded struct {
		RemainingDrift []struct {
			Target string `json:"target"`
			Reason string `json:"reason"`
		} `json:"remainingDrift"`
	}
	if err := json.Unmarshal([]byte(report), &decoded); err != nil {
		t.Fatalf("decode reconcile report: %v\n%s", err, report)
	}
	out := []string{}
	for _, item := range decoded.RemainingDrift {
		out = append(out, item.Target+" "+item.Reason)
	}
	slices.Sort(out)
	return out
}
