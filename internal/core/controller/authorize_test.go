package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

var testGuardFields = GuardFields{
	SessionUID: "@projmux_project_uid",
	WindowUID:  "@projmux_window_uid",
	PaneUID:    "@projmux_pane_uid",
	SessionID:  "session_id",
	WindowID:   "window_id",
}

// runtimeGraph builds a graph from runtime nodes alone. IndexHandles reads
// nothing else, and stating the machine directly keeps each case about the one
// containment or attribution fact it is checking.
func runtimeGraph(nodes ...resourcegraph.RuntimeNode) resourcegraph.Graph {
	return resourcegraph.Graph{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "primary"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Runtime:   nodes,
	}
}

func sessionNode(id, name, uid string, class resourcegraph.Class) resourcegraph.RuntimeNode {
	return resourcegraph.RuntimeNode{
		Ref:   resourcegraph.RuntimeRef{Kind: resourcegraph.ObjectSession, ID: id, Target: name, Name: name},
		Class: class, UID: uid,
	}
}

func windowNode(id, target, uid, sessionID string, class resourcegraph.Class) resourcegraph.RuntimeNode {
	return resourcegraph.RuntimeNode{
		Ref:   resourcegraph.RuntimeRef{Kind: resourcegraph.ObjectWindow, ID: id, Target: target},
		Class: class, UID: uid, ContainerID: sessionID,
	}
}

func paneNode(id, uid, windowID string, class resourcegraph.Class) resourcegraph.RuntimeNode {
	return resourcegraph.RuntimeNode{
		Ref:   resourcegraph.RuntimeRef{Kind: resourcegraph.ObjectPane, ID: id, Target: id},
		Class: class, UID: uid, ContainerID: windowID,
	}
}

func setOption(scope, target, field, value string) []string {
	return []string{"set-option", scope, "-t", target, field, value}
}

func TestAuthorizeAppliesThePolicyToEveryClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		class resourcegraph.Class
		want  Authority
	}{
		{resourcegraph.ClassManaged, AuthorityAllow},
		{resourcegraph.ClassUnattributed, AuthorityAllow},
		{resourcegraph.ClassRecoverable, AuthorityRefuse},
		{resourcegraph.ClassControl, AuthorityObserve},
		{resourcegraph.ClassEphemeral, AuthorityObserve},
		{resourcegraph.ClassForeign, AuthorityRefuse},
		{resourcegraph.ClassConflict, AuthorityRefuse},
	}
	for _, tc := range cases {
		graph := runtimeGraph(sessionNode("$1", "alpha", "proj-1", tc.class))
		actions, _ := Authorize(IndexHandles(graph), testGuardFields, Grant{}, []Candidate{{
			Key: "tmux:set-option:project:$1:@projmux_project_name", Intent: IntentRepairMirror,
			Kind: "Project", Target: "$1", Field: "@projmux_project_name", After: "alpha",
			Args: setOption("", "$1", "@projmux_project_name", "alpha"),
		}})
		if len(actions) != 1 {
			t.Fatalf("%s: actions = %d, want 1", tc.class, len(actions))
		}
		if actions[0].Authority != tc.want {
			t.Fatalf("%s: authority = %s, want %s", tc.class, actions[0].Authority, tc.want)
		}
		if actions[0].Allowed() != (tc.want == AuthorityAllow) {
			t.Fatalf("%s: Allowed() disagrees with authority %s", tc.class, actions[0].Authority)
		}
		if !actions[0].Allowed() && len(actions[0].Guards) != 0 {
			t.Fatalf("%s: a write that will not run was given guards: %+v", tc.class, actions[0].Guards)
		}
	}
}

func TestAuthorizeRefusesATargetTheObservationNeverSaw(t *testing.T) {
	t.Parallel()

	// A handle that vanished between the observation and the plan is the exact
	// case a later write would land on a recycled id. There is nothing to guard
	// against, because there is nothing known about it.
	graph := runtimeGraph(sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged))
	actions, _ := Authorize(IndexHandles(graph), testGuardFields, Grant{}, []Candidate{{
		Key: "tmux:set-option:pane:%99:@projmux_pane_uid", Intent: IntentRepairBinding, Target: "%99",
		Field: "@projmux_pane_uid", Args: setOption("-p", "%99", "@projmux_pane_uid", "pan-1"),
	}})
	if !actions[0].Refused() || !strings.Contains(actions[0].Reason, "observation does not contain") {
		t.Fatalf("unobserved target = %+v, want a refusal naming the observation", actions[0])
	}
	if actions[0].Target != "%99" {
		t.Fatalf("refused action lost its target: %+v", actions[0])
	}
}

func TestAuthorizeRefusesEveryVerbOutsideTheConvergenceSet(t *testing.T) {
	t.Parallel()

	// The class is managed and the policy would allow the write. The verb gate
	// is what makes "the controller never created or killed anything" a
	// structural fact rather than a property of which candidates happen to be
	// produced today.
	graph := runtimeGraph(sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged))
	for _, args := range [][]string{
		{"new-session", "-s", "alpha"},
		{"new-window", "-t", "$1"},
		{"split-window", "-t", "$1"},
		{"kill-session", "-t", "$1"},
		{"kill-pane", "-t", "$1"},
		{},
	} {
		actions, _ := Authorize(IndexHandles(graph), testGuardFields, Grant{}, []Candidate{{
			Key: "tmux:forbidden:$1", Intent: IntentRepairMirror, Target: "$1", Args: args,
		}})
		if !actions[0].Refused() || !strings.Contains(actions[0].Reason, "outside the convergence set") {
			t.Fatalf("verb %v = %+v, want a forbidden-verb refusal", args, actions[0])
		}
	}
	// The two verbs convergence actually needs still pass, so the gate is a
	// boundary rather than a blanket refusal.
	for _, args := range [][]string{
		{"set-option", "-t", "$1", "@projmux_project_name", "alpha"},
		{"rename-window", "-t", "$1", "alpha"},
	} {
		actions, _ := Authorize(IndexHandles(graph), testGuardFields, Grant{}, []Candidate{{
			Key: "tmux:allowed:$1", Intent: IntentRepairMirror, Target: "$1", Args: args,
		}})
		if !actions[0].Allowed() {
			t.Fatalf("verb %v = %+v, want allowed", args, actions[0])
		}
	}
}

func TestAuthorizeGuardsBothIdentityAndContainment(t *testing.T) {
	t.Parallel()

	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		windowNode("@2", "alpha:0", "win-1", "$1", resourcegraph.ClassManaged),
		paneNode("%3", "pan-1", "@2", resourcegraph.ClassManaged),
	)
	handles := IndexHandles(graph)
	want := map[string][]Guard{
		"$1": {{Field: "@projmux_project_uid", Expect: "proj-1"}},
		"@2": {{Field: "@projmux_window_uid", Expect: "win-1"}, {Field: "session_id", Expect: "$1"}},
		"%3": {{Field: "@projmux_pane_uid", Expect: "pan-1"}, {Field: "window_id", Expect: "@2"}},
	}
	for target, guards := range want {
		handle, ok := handles.Lookup(target)
		if !ok {
			t.Fatalf("handle %s was not indexed", target)
		}
		if got := handle.Guards(testGuardFields); !slices.Equal(got, guards) {
			t.Fatalf("guards for %s = %+v, want %+v", target, got, guards)
		}
	}
}

func TestAuthorizeResolvesOperatorSpelledTargetsToTheStableID(t *testing.T) {
	t.Parallel()

	// A planner that walked a session reports `alpha:0`; the observation
	// reports `@2`. Executing against the coordinate would re-resolve the index
	// at write time, which is the recycled-handle hazard the guards exist to
	// close, so the id replaces the spelling before anything runs.
	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		windowNode("@2", "alpha:0", "win-1", "$1", resourcegraph.ClassManaged),
	)
	actions, _ := Authorize(IndexHandles(graph), testGuardFields, Grant{}, []Candidate{{
		Key: "tmux:rename-window:window:alpha:0", Intent: IntentRepairMirror, Target: "alpha:0",
		Args: []string{"rename-window", "-t", "alpha:0", "shell"},
	}})
	if actions[0].Target != "@2" || !actions[0].Allowed() {
		t.Fatalf("coordinate target = %+v, want the stable @2 handle allowed", actions[0])
	}
}

func TestIndexHandlesWalksContainmentForTheManagedEnclosure(t *testing.T) {
	t.Parallel()

	// The pane is unmarked and its window is unmarked; the managed enclosure is
	// two levels up. Walking only one level would report the pane as enclosed
	// by nothing, and the report would then explain a legitimate repair as if
	// it had happened outside projmux's world.
	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		windowNode("@2", "alpha:0", "", "$1", resourcegraph.ClassUnattributed),
		paneNode("%3", "", "@2", resourcegraph.ClassUnattributed),
		sessionNode("$4", "other", "", resourcegraph.ClassForeign),
		paneNode("%5", "", "@9", resourcegraph.ClassForeign),
	)
	handles := IndexHandles(graph)
	for target, want := range map[string]bool{"@2": true, "%3": true, "$4": false, "%5": false} {
		handle, ok := handles.Lookup(target)
		if !ok {
			t.Fatalf("handle %s was not indexed", target)
		}
		if handle.ManagedEnclosure != want {
			t.Fatalf("managed enclosure of %s = %t, want %t", target, handle.ManagedEnclosure, want)
		}
	}
}

func TestExercisedReportsTheRefusalsTheGraphPutInPlay(t *testing.T) {
	t.Parallel()

	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		sessionNode("$2", "home", "", resourcegraph.ClassControl),
		paneNode("%3", "", "@9", resourcegraph.ClassUnattributed),
	)
	handles := IndexHandles(graph)
	withOffline := Exercised(handles, Grant{}, true)
	if !slices.ContainsFunc(withOffline, func(v Verdict) bool { return v.Intent == IntentStart }) {
		t.Fatalf("offline rows did not put the start refusal in play: %+v", withOffline)
	}
	for _, verdict := range withOffline {
		if verdict.Authority != AuthorityRefuse {
			t.Fatalf("exercised verdict %+v is not a refusal", verdict)
		}
	}
	for _, class := range []resourcegraph.Class{resourcegraph.ClassControl, resourcegraph.ClassUnattributed} {
		if !slices.ContainsFunc(withOffline, func(v Verdict) bool {
			return v.Intent == IntentImport && v.Class == class
		}) {
			t.Fatalf("class %s did not put its import refusal in play: %+v", class, withOffline)
		}
	}
	if slices.ContainsFunc(Exercised(handles, Grant{}, false), func(v Verdict) bool { return v.Intent == IntentStart }) {
		t.Fatalf("start refusal was reported with no offline row to start")
	}
}

func TestAuthorizeIsDeterministicAcrossCandidateOrder(t *testing.T) {
	t.Parallel()

	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		windowNode("@2", "alpha:0", "win-1", "$1", resourcegraph.ClassManaged),
		paneNode("%3", "pan-1", "@2", resourcegraph.ClassManaged),
	)
	candidates := []Candidate{
		{Key: "tmux:set-option:pane:%3:@projmux_pane_label", Intent: IntentRepairMirror, Target: "%3", Field: "@projmux_pane_label", Args: setOption("-p", "%3", "@projmux_pane_label", "shell")},
		{Key: "tmux:set-option:window:@2:@projmux_window_uid", Intent: IntentRepairBinding, Target: "@2", Field: "@projmux_window_uid", Args: setOption("-w", "@2", "@projmux_window_uid", "win-1")},
		{Key: "tmux:set-option:project:$1:@projmux_project_name", Intent: IntentRepairMirror, Target: "$1", Field: "@projmux_project_name", Args: setOption("", "$1", "@projmux_project_name", "alpha")},
	}
	reference := planFor(graph, candidates)
	for _, order := range [][]int{{2, 1, 0}, {1, 0, 2}, {0, 2, 1}} {
		shuffled := make([]Candidate, 0, len(candidates))
		for _, index := range order {
			shuffled = append(shuffled, candidates[index])
		}
		got := planFor(graph, shuffled)
		if !slices.Equal(keysOf(got), keysOf(reference)) {
			t.Fatalf("candidate order %v changed the plan order: %v vs %v", order, keysOf(got), keysOf(reference))
		}
		left, _ := json.Marshal(got)
		right, _ := json.Marshal(reference)
		if string(left) != string(right) {
			t.Fatalf("candidate order %v changed the projected bytes:\n%s\n%s", order, left, right)
		}
	}
	// Containment order, not alphabetical order: a Pane uid written into a
	// Window that does not yet carry its own uid is attributable to nothing.
	want := []resourcegraph.ObjectKind{resourcegraph.ObjectSession, resourcegraph.ObjectWindow, resourcegraph.ObjectPane}
	if scopes := scopesOf(reference); !slices.Equal(scopes, want) {
		t.Fatalf("plan scope order = %v, want %v", scopes, want)
	}
}

func planFor(graph resourcegraph.Graph, candidates []Candidate) Plan {
	handles := IndexHandles(graph)
	actions, policy := Authorize(handles, testGuardFields, Grant{}, candidates)
	return NewPlan(graph.Transport, graph.HostMode, actions, policy)
}

func keysOf(plan Plan) []string {
	out := make([]string, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		out = append(out, action.Key)
	}
	return out
}

func scopesOf(plan Plan) []resourcegraph.ObjectKind {
	out := make([]resourcegraph.ObjectKind, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		out = append(out, action.Scope)
	}
	return out
}

func TestPlanPartitionsWritesRefusalsAndConvergence(t *testing.T) {
	t.Parallel()

	graph := runtimeGraph(
		sessionNode("$1", "alpha", "proj-1", resourcegraph.ClassManaged),
		sessionNode("$2", "guest", "", resourcegraph.ClassForeign),
		sessionNode("$3", "home", "", resourcegraph.ClassControl),
	)
	plan := planFor(graph, []Candidate{
		{Key: "a", Intent: IntentRepairMirror, Target: "$1", Args: setOption("", "$1", "@projmux_project_name", "alpha")},
		{Key: "b", Intent: IntentRepairMirror, Target: "$2", Args: setOption("", "$2", "@projmux_project_name", "guest")},
		{Key: "c", Intent: IntentRepairMirror, Target: "$3", Args: setOption("", "$3", "@projmux_project_name", "home")},
	})
	if len(plan.Writes()) != 1 || plan.Writes()[0].Target != "$1" {
		t.Fatalf("writes = %+v, want only the managed session", plan.Writes())
	}
	if len(plan.Refusals()) != 1 || plan.Refusals()[0].Target != "$2" {
		t.Fatalf("refusals = %+v, want only the foreign session", plan.Refusals())
	}
	if plan.Converged() {
		t.Fatal("a plan with a write and a refusal reported convergence")
	}
	if empty := NewPlan(plan.Transport, plan.HostMode, nil, nil); !empty.Converged() {
		t.Fatal("an empty plan did not report convergence")
	}
	observed := planFor(graph, []Candidate{
		{Key: "c", Intent: IntentRepairMirror, Target: "$3", Args: setOption("", "$3", "@projmux_project_name", "home")},
	})
	if !observed.Converged() {
		t.Fatalf("an observe-only plan is drift: %+v", observed.Actions)
	}
}
