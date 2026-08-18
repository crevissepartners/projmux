package runtimediag

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// multiClassRegistry is one Project with a Window, a shell Pane, and an
// Agent-owned Pane. It is deliberately small: the attribution matrix is the
// graph package's contract and is tested there, so what matters here is that
// every class the graph can produce survives the projection with its handle,
// its reason, and its binding intact.
func multiClassRegistry() coremetadata.Registry {
	created := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	meta := func(uid, name string, owner *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: owner, CreatedAt: created}
	}
	own := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: meta("project-alpha", "alpha", nil),
		Spec:     coremetadata.ProjectSpec{Root: "/src/alpha"},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: meta("win-alpha-1", "editor", own(coremetadata.KindProject, "project-alpha")),
		Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pane-alpha-1"},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: meta("pane-alpha-1", "shell", own(coremetadata.KindWindow, "win-alpha-1")),
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}
	return registry
}

// multiClassInventory is one app-owned server carrying every class at once:
// the managed Project session, the Home control session, a scratch session, an
// unmarked window and pane inside the managed session, and a live window whose
// mirrored uid this Registry does not contain.
func multiClassInventory() resourcegraph.Inventory {
	return resourcegraph.Inventory{
		Transport: resourcegraph.Transport{
			Kind: resourcegraph.TransportSocketName, Value: "projmux",
			Source: resourcegraph.TransportSourceSocketName,
		},
		HostMode: resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$1", Name: "alpha", ProjectUID: "project-alpha", ProjectName: "alpha", Root: "/src/alpha"},
			{ID: "$2", Name: "Home", Role: resourcegraph.ControlSessionRole},
			{ID: "$3", Name: "scratch", Ephemeral: true},
		},
		Windows: []resourcegraph.Window{
			{ID: "@1", SessionID: "$1", Index: "0", DisplayName: "editor", UID: "win-alpha-1", MirroredName: "editor"},
			{ID: "@2", SessionID: "$1", Index: "1", DisplayName: "notes"},
			{ID: "@3", SessionID: "$2", Index: "0", DisplayName: "home"},
			{ID: "@4", SessionID: "$1", Index: "2", DisplayName: "ghost", UID: "win-not-in-registry"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%1", WindowID: "@1", UID: "pane-alpha-1", MirroredName: "shell", Title: "zsh"},
			{ID: "%2", WindowID: "@2", Title: "vim"},
			{ID: "%3", WindowID: "@3", Title: "control"},
		},
	}
}

func multiClassGraph() resourcegraph.Graph {
	return resourcegraph.Resolve(multiClassRegistry(), multiClassInventory())
}

func rowByID(t *testing.T, rows []Row, id string) Row {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no runtime row %q in %d rows", id, len(rows))
	return Row{}
}

// TestEveryObservedObjectIsProjectedWithItsAttribution is acceptance (1) and
// (3) at the projection layer: nothing observed is dropped, managed no-ops
// included, and Home, an unknown uid, and a uid-less ordinary object stay three
// different answers.
func TestEveryObservedObjectIsProjectedWithItsAttribution(t *testing.T) {
	t.Parallel()

	graph := multiClassGraph()
	rows := Rows(graph)
	if got, want := len(rows), 10; got != want {
		t.Fatalf("projected %d rows, want %d (3 sessions + 4 windows + 3 panes)", got, want)
	}

	for _, test := range []struct {
		id       string
		kind     string
		class    resourcegraph.Class
		target   string
		resource string
	}{
		// The managed session is a no-op for any reconciler and is still here.
		{id: "$1", kind: "session", class: resourcegraph.ClassManaged, target: "alpha", resource: "project-alpha"},
		{id: "$2", kind: "session", class: resourcegraph.ClassControl, target: "Home"},
		{id: "$3", kind: "session", class: resourcegraph.ClassEphemeral, target: "scratch"},
		{id: "@1", kind: "window", class: resourcegraph.ClassManaged, target: "alpha:@1", resource: "win-alpha-1"},
		{id: "@2", kind: "window", class: resourcegraph.ClassUnattributed, target: "alpha:@2"},
		{id: "@3", kind: "window", class: resourcegraph.ClassUnattributed, target: "Home:@3"},
		{id: "@4", kind: "window", class: resourcegraph.ClassRecoverable, target: "alpha:@4"},
		{id: "%1", kind: "pane", class: resourcegraph.ClassManaged, target: "alpha:@1.%1", resource: "pane-alpha-1"},
		{id: "%2", kind: "pane", class: resourcegraph.ClassUnattributed, target: "alpha:@2.%2"},
		{id: "%3", kind: "pane", class: resourcegraph.ClassUnattributed, target: "Home:@3.%3"},
	} {
		row := rowByID(t, rows, test.id)
		if row.Kind != test.kind {
			t.Fatalf("%s kind = %q, want %q", test.id, row.Kind, test.kind)
		}
		if row.Class != string(test.class) {
			t.Fatalf("%s class = %q, want %q", test.id, row.Class, test.class)
		}
		if row.Target != test.target {
			t.Fatalf("%s target = %q, want %q", test.id, row.Target, test.target)
		}
		if row.Reason == "" {
			t.Fatalf("%s carries no reason for class %q", test.id, row.Class)
		}
		switch {
		case test.resource == "":
			if row.Resource != nil {
				t.Fatalf("%s bound to resource %#v, want none", test.id, row.Resource)
			}
		default:
			if row.Resource == nil || row.Resource.UID != test.resource {
				t.Fatalf("%s resource = %#v, want uid %q", test.id, row.Resource, test.resource)
			}
		}
	}
}

// TestARefusedObjectNeverCarriesAResourceIdentity is the authority boundary:
// a live object mirroring a uid this Registry does not contain is legible and
// still not attached to anything.
func TestARefusedObjectNeverCarriesAResourceIdentity(t *testing.T) {
	t.Parallel()

	row := rowByID(t, Rows(multiClassGraph()), "@4")
	if row.Class != string(resourcegraph.ClassRecoverable) {
		t.Fatalf("@4 class = %q, want recoverable", row.Class)
	}
	if row.UID != "win-not-in-registry" {
		t.Fatalf("@4 uid = %q, want the mirrored uid reported verbatim", row.UID)
	}
	if row.Resource != nil {
		t.Fatalf("@4 bound to %#v; a refused object must not be handed a resource identity", row.Resource)
	}
	if row.Managed() {
		t.Fatal("@4 reports Managed(); a recoverable object is not managed")
	}
}

// TestContainmentComesFromTmuxIdsRatherThanNames pins the two halves of the
// coordinate rule: a session whose name could not be read still yields an exact
// `$N`-qualified coordinate, and an object whose enclosing session cannot be
// resolved at all yields none rather than an unqualified handle the focus
// grammar would read as a session name.
func TestContainmentComesFromTmuxIdsRatherThanNames(t *testing.T) {
	t.Parallel()

	// A failed sessions query still leaves the windows carrying their session
	// id, so the coordinate degrades to the id rather than disappearing.
	byID := multiClassInventory()
	byID.Sessions = nil
	byID = byID.MarkUnavailable(resourcegraph.ScopeSessions, "tmux sessions could not be listed")
	rows := Rows(resourcegraph.Resolve(multiClassRegistry(), byID))
	if got := rowByID(t, rows, "@1").Target; got != "$1:@1" {
		t.Fatalf("@1 target = %q, want $1:@1 when the session name was not observed", got)
	}
	if got := rowByID(t, rows, "%1").Target; got != "$1:@1.%1" {
		t.Fatalf("%%1 target = %q, want $1:@1.%%1", got)
	}

	// A failed windows query leaves the panes with a container id that resolves
	// to no session at all. That is the case with no safe coordinate.
	noWindows := multiClassInventory()
	noWindows.Windows = nil
	noWindows = noWindows.MarkUnavailable(resourcegraph.ScopeWindows, "tmux windows could not be listed")
	rows = Rows(resourcegraph.Resolve(multiClassRegistry(), noWindows))
	for _, id := range []string{"%1", "%2", "%3"} {
		row := rowByID(t, rows, id)
		if row.Target != "" {
			t.Fatalf("%s target = %q, want empty when its session cannot be resolved", id, row.Target)
		}
		if row.ContainerID == "" {
			t.Fatalf("%s lost its container id; the panes scope was readable", id)
		}
	}
	// The sessions themselves are unaffected: a failed windows query is one
	// scope, not the whole observation.
	if got := rowByID(t, rows, "$1").Target; got != "alpha" {
		t.Fatalf("$1 target = %q, want alpha", got)
	}
}

// TestConflictsTravelWithEveryClaimant is the contradiction half: both tmux
// handles that claim one uid carry the recorded conflict, so an operator
// reading either row is pointed at the other.
func TestConflictsTravelWithEveryClaimant(t *testing.T) {
	t.Parallel()

	inventory := multiClassInventory()
	inventory.Windows = append(inventory.Windows, resourcegraph.Window{
		ID: "@9", SessionID: "$1", Index: "9", DisplayName: "double", UID: "win-alpha-1",
	})
	rows := Rows(resourcegraph.Resolve(multiClassRegistry(), inventory))

	for _, id := range []string{"@1", "@9"} {
		row := rowByID(t, rows, id)
		if row.Class != string(resourcegraph.ClassConflict) {
			t.Fatalf("%s class = %q, want conflict", id, row.Class)
		}
		if len(row.Conflicts) != 1 {
			t.Fatalf("%s conflicts = %#v, want exactly one", id, row.Conflicts)
		}
		if row.Conflicts[0].Reason != string(resourcegraph.ConflictDuplicateClaim) {
			t.Fatalf("%s conflict reason = %q", id, row.Conflicts[0].Reason)
		}
		if !slices.Contains(row.Conflicts[0].Targets, "@1") || !slices.Contains(row.Conflicts[0].Targets, "@9") {
			t.Fatalf("%s conflict targets = %v, want both claimants", id, row.Conflicts[0].Targets)
		}
		if row.Resource != nil {
			t.Fatalf("%s bound to %#v; a contradicted uid must bind to nothing", id, row.Resource)
		}
	}
}

// TestNoTransportProjectsUnavailableAndNothingElse is acceptance (4)'s
// outside-tmux half: the read succeeds, reports why, and invents no rows.
func TestNoTransportProjectsUnavailableAndNothingElse(t *testing.T) {
	t.Parallel()

	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportNone, Source: resourcegraph.TransportSourceNone},
		HostMode:  resourcegraph.HostModeUnknown,
	}
	for _, scope := range resourcegraph.Scopes() {
		inventory = inventory.MarkUnavailable(scope, "no exact tmux transport")
	}
	graph := resourcegraph.Resolve(multiClassRegistry(), inventory)

	for _, kind := range resourcegraph.ObjectKinds() {
		report := Project(graph, kind)
		if report.Observed() {
			t.Fatalf("%s report claims an observation with no transport", kind)
		}
		if len(report.Items) != 0 {
			t.Fatalf("%s report invented %d items with no transport", kind, len(report.Items))
		}
		if report.Items == nil {
			t.Fatalf("%s report items are nil; the envelope must always carry a list", kind)
		}
		if len(report.Unavailable) != len(resourcegraph.Scopes()) {
			t.Fatalf("%s report unavailable = %#v, want every scope", kind, report.Unavailable)
		}
	}
}

// TestAnEmptyServerIsNotAnUnreadableOne keeps the two empty answers apart: a
// running server with nothing on it reports no items and no unavailability.
func TestAnEmptyServerIsNotAnUnreadableOne(t *testing.T) {
	t.Parallel()

	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{
			Kind: resourcegraph.TransportSocketName, Value: "projmux",
			Source: resourcegraph.TransportSourceSocketName,
		},
		HostMode: resourcegraph.HostModeAppOwned,
	}
	report := Project(resourcegraph.Resolve(multiClassRegistry(), inventory), resourcegraph.ObjectSession)
	if len(report.Items) != 0 || len(report.Unavailable) != 0 {
		t.Fatalf("empty-server report = %#v, want no items and no unavailability", report)
	}
	if !report.Observed() {
		t.Fatal("empty-server report claims no observation; the transport was present")
	}
}

// TestProjectionIsDeterministicAndScopedToOneKind proves the JSON is a pure
// function of the graph and that each report holds exactly its own kind.
func TestProjectionIsDeterministicAndScopedToOneKind(t *testing.T) {
	t.Parallel()

	graph := multiClassGraph()
	for _, kind := range resourcegraph.ObjectKinds() {
		first, err := json.Marshal(Project(graph, kind))
		if err != nil {
			t.Fatalf("marshal %s report: %v", kind, err)
		}
		for range 8 {
			again, err := json.Marshal(Project(multiClassGraph(), kind))
			if err != nil {
				t.Fatalf("marshal %s report: %v", kind, err)
			}
			if string(again) != string(first) {
				t.Fatalf("%s report is not byte-identical across builds:\n%s\n%s", kind, first, again)
			}
		}
		for _, item := range Project(graph, kind).Items {
			if item.Kind != string(kind) {
				t.Fatalf("%s report carries a %q row", kind, item.Kind)
			}
		}
	}
}

// TestRowsAreOrderedOutermostContainmentFirst pins the picker's list order.
func TestRowsAreOrderedOutermostContainmentFirst(t *testing.T) {
	t.Parallel()

	var kinds []string
	for _, row := range Rows(multiClassGraph()) {
		if len(kinds) == 0 || kinds[len(kinds)-1] != row.Kind {
			kinds = append(kinds, row.Kind)
		}
	}
	want := []string{"session", "window", "pane"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("row kind order = %v, want %v", kinds, want)
	}
}

// TestListKindsAreStableSchemaNames guards the published envelope names, which
// a consumer branches on.
func TestListKindsAreStableSchemaNames(t *testing.T) {
	t.Parallel()

	want := map[resourcegraph.ObjectKind]string{
		resourcegraph.ObjectSession: "RuntimeSessionList",
		resourcegraph.ObjectWindow:  "RuntimeWindowList",
		resourcegraph.ObjectPane:    "RuntimePaneList",
	}
	for kind, expected := range want {
		if got := ListKind(kind); got != expected {
			t.Fatalf("ListKind(%s) = %q, want %q", kind, got, expected)
		}
		if got := Project(multiClassGraph(), kind); got.Kind != expected || got.APIVersion != coremetadata.APIVersion {
			t.Fatalf("%s report envelope = (%q, %q)", kind, got.APIVersion, got.Kind)
		}
	}
	if got := ListKind(resourcegraph.ObjectKind("agent")); got != "" {
		t.Fatalf("ListKind of an unknown kind = %q, want empty", got)
	}
}

// TestCountsSummarizeOnlyTheClassesPresent keeps the summary honest: a class
// with nothing in it is not reported as zero.
func TestCountsSummarizeOnlyTheClassesPresent(t *testing.T) {
	t.Parallel()

	counts := Counts(Rows(multiClassGraph()))
	got := map[string]int{}
	for _, entry := range counts {
		if entry.Count == 0 {
			t.Fatalf("counts include an empty class: %#v", entry)
		}
		got[entry.Class] = entry.Count
	}
	want := map[string]int{"managed": 3, "recoverable": 1, "control": 1, "ephemeral": 1, "unattributed": 4}
	for class, total := range want {
		if got[class] != total {
			t.Fatalf("class %q count = %d, want %d (all: %#v)", class, got[class], total, counts)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("counts = %#v, want exactly %v", counts, want)
	}
	// Declaration order, not map order.
	var order []string
	for _, entry := range counts {
		order = append(order, entry.Class)
	}
	if !slices.Equal(order, []string{"managed", "recoverable", "control", "ephemeral", "unattributed"}) {
		t.Fatalf("counts order = %v, want the closed class declaration order", order)
	}
}
