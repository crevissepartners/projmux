package selector

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// TestNoImplicitCommaDisplayNamePathOrTmuxIDEverResolvesASelector is acceptance
// criterion 1. Every input below names something that exists in the fixture
// under some *other* accessor -- a comma-joined pair of real names, a duplicate
// displayName, a real spec.root path, a tmux transport id -- and every one of
// them must resolve to zero matches.
//
// The assertion is an aggregate: the total number of resolved resources across
// the whole table must be exactly 0 occurrences.
func TestNoImplicitCommaDisplayNamePathOrTmuxIDEverResolvesASelector(t *testing.T) {
	t.Parallel()

	registry := standardRegistry(t)
	resolver := New(registry)

	alpha := func() *Ref { return refPtr(mustRef(t, metadata.KindProject, "alpha")) }
	windowQuery := func(project *Ref, value string) Query {
		return Query{Project: project, Windows: []Ref{mustRef(t, metadata.KindWindow, value)}}
	}
	paneQuery := func(project *Ref, value string) Query {
		return Query{Project: project, Panes: []Ref{mustRef(t, metadata.KindPane, value)}}
	}

	tests := []struct {
		name    string
		query   Query
		resolve func(Query) (Resolution, error)
	}{
		// Implicit comma split. "main" and "review" are both real Window names
		// in project alpha; the joined value must stay one literal token.
		{name: "comma joined window names", query: windowQuery(alpha(), "main,review"), resolve: resolver.ResolveWindows},
		{name: "comma joined pane names", query: paneQuery(alpha(), "zsh,log"), resolve: resolver.ResolvePanes},
		// displayName. "projmux" is the duplicate-allowed displayName of two
		// Projects; "zsh" is the displayName of the Pane named "log".
		{name: "project displayName", query: paneQuery(refPtr(mustRef(t, metadata.KindProject, "projmux")), "zsh"), resolve: resolver.ResolvePanes},
		// Path. /srv/alpha is a real Project spec.root; /srv/alpha/logs is a
		// real Pane spec.cwd.
		{name: "project spec.root path", query: windowQuery(refPtr(mustRef(t, metadata.KindProject, "/srv/alpha")), "main"), resolve: resolver.ResolveWindows},
		{name: "pane cwd path", query: paneQuery(alpha(), "/srv/alpha/logs"), resolve: resolver.ResolvePanes},
		// tmux transport ids.
		{name: "tmux pane id", query: paneQuery(alpha(), "%3"), resolve: resolver.ResolvePanes},
		{name: "tmux window id", query: windowQuery(alpha(), "@1"), resolve: resolver.ResolveWindows},
		{name: "tmux session id", query: paneQuery(alpha(), "$0"), resolve: resolver.ResolvePanes},
		// A bare uid without the uid: prefix is not a selector form either.
		{name: "bare uid", query: paneQuery(alpha(), "pan-alpha-zsh"), resolve: resolver.ResolvePanes},
	}

	occurrences := 0
	for _, test := range tests {
		resolution, err := test.resolve(test.query)
		// A --project value that resolves to nothing is itself an exact-one
		// failure, which is also zero resolved resources.
		if err != nil {
			if !metadata.IsUsageError(err) {
				t.Fatalf("%s: unexpected error %v", test.name, err)
			}
			continue
		}
		if got := len(resolution.Matches); got != 0 {
			t.Errorf("%s resolved %d resources (%v), want 0", test.name, got, names(resolution.Matches))
		}
		occurrences += len(resolution.Matches)
	}
	if occurrences != 0 {
		t.Fatalf("excluded selector forms resolved %d occurrences, want 0", occurrences)
	}
}

// TestStoredDisplayNameIsNeverMatchedAndContextIsProjected pins both halves of
// the cutover: stored presentation never resolves a selector, while the
// invocation projector still reports non-authoritative context.
func TestStoredDisplayNameIsNeverMatchedAndContextIsProjected(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	resolution, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main")},
		Panes:   []Ref{mustRef(t, metadata.KindPane, "zsh")},
	})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if got := names(resolution.Matches); !reflect.DeepEqual(got, []string{"zsh"}) {
		t.Fatalf("matches = %v, want exactly the Pane named zsh", got)
	}
	if resolution.Matches[0].UID != "pan-alpha-zsh" {
		t.Fatalf("matched uid = %q, want pan-alpha-zsh", resolution.Matches[0].UID)
	}
	if resolution.Matches[0].Context.Value != "zsh" || resolution.Matches[0].Context.Source != registryview.ContextSourceCommand {
		t.Fatalf("context = %#v, want command-derived zsh", resolution.Matches[0].Context)
	}
}

func TestContextSourcesCannotChangeAddressSelectionReservationOrOwnerRef(t *testing.T) {
	t.Parallel()

	registry := standardRegistry(t)
	before, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal Registry before projection: %v", err)
	}
	query := Query{Panes: []Ref{mustRef(t, metadata.KindPane, "zsh")}}

	noTransport, err := New(registry).ResolvePanes(query)
	if err != nil {
		t.Fatalf("resolve no-transport Panes: %v", err)
	}
	graph := resourcegraph.Graph{}
	for _, project := range registry.Projects {
		graph.Projects = append(graph.Projects, resourcegraph.ProjectNode{Project: project})
	}
	for _, control := range registry.ControlSessions {
		graph.ControlSessions = append(graph.ControlSessions, resourcegraph.ControlSessionNode{ControlSession: control})
	}
	for _, window := range registry.Windows {
		graph.Windows = append(graph.Windows, resourcegraph.WindowNode{Window: window})
	}
	for _, pane := range registry.Panes {
		node := resourcegraph.PaneNode{Pane: pane}
		if pane.Metadata.UID == "pan-alpha-zsh" {
			node.Status = resourcegraph.StatusLive
			node.Runtime = &resourcegraph.RuntimeRef{Kind: resourcegraph.ObjectPane, ID: "%1", Name: "log"}
		}
		graph.Panes = append(graph.Panes, node)
	}
	for _, agent := range registry.Agents {
		graph.Agents = append(graph.Agents, resourcegraph.AgentNode{Agent: agent})
	}
	live, err := NewObservedWithContext(registry, liveAlphaObservation(), registryview.NewObservedContextProjector(graph)).ResolvePanes(query)
	if err != nil {
		t.Fatalf("resolve live-context Panes: %v", err)
	}
	if got, want := live.UIDs(), noTransport.UIDs(); !slices.Equal(got, want) {
		t.Fatalf("Context changed selected UIDs: got %v, want %v", got, want)
	}
	var noTransportContext, liveContext registryview.Context
	for _, match := range noTransport.Matches {
		if match.UID == "pan-alpha-zsh" {
			noTransportContext = match.Context
		}
	}
	for _, match := range live.Matches {
		if match.UID == "pan-alpha-zsh" {
			liveContext = match.Context
		}
	}
	if noTransportContext.Source != registryview.ContextSourceCommand || liveContext.Source != registryview.ContextSourceLivePaneTitle || liveContext.Value != "log" {
		t.Fatalf("test did not vary semantic/live Context sources: no-transport=%+v live=%+v", noTransportContext, liveContext)
	}
	after, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal Registry after projection: %v", err)
	}
	if !slices.Equal(before, after) {
		t.Fatalf("Context projection mutated Registry address/reservation/ownerRef bytes:\nbefore %s\nafter  %s", before, after)
	}
}

// TestResolutionRunsNameUIDUnionThenLabelFilterThenUIDDedupe is acceptance
// criterion 2. The query is built so each stage reports a different surviving
// count, which makes the order observable rather than merely documented:
//
//	union  = 3  (zsh, log, and zsh again through its uid: form)
//	filter = 2  (log carries role=sidecar and drops out)
//	dedupe = 1  (the two zsh occurrences collapse to one uid)
//
// A dedupe that ran before the filter, or a filter that ran before the union,
// could not produce this 3/2/1 shape.
func TestResolutionRunsNameUIDUnionThenLabelFilterThenUIDDedupe(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	resolution, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main"), mustRef(t, metadata.KindWindow, "main")},
		Panes: []Ref{
			mustRef(t, metadata.KindPane, "zsh"),
			mustRef(t, metadata.KindPane, "log"),
			mustRef(t, metadata.KindPane, UIDPrefix+"pan-alpha-zsh"),
		},
		Labels: []Label{mustLabel(t, "role=shell")},
	})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}

	want := []TraceStep{
		{Stage: StageNameUIDUnion, Count: 3},
		{Stage: StageLabelFilter, Count: 2},
		{Stage: StageUIDDedupe, Count: 1},
	}
	if !reflect.DeepEqual(resolution.Trace, want) {
		t.Fatalf("trace = %#v, want %#v", resolution.Trace, want)
	}

	var stages []Stage
	for _, step := range resolution.Trace {
		stages = append(stages, step.Stage)
	}
	if !reflect.DeepEqual(stages, stageOrder) {
		t.Fatalf("trace stages = %v, want the fixed order %v", stages, stageOrder)
	}
	if got := resolution.UIDs(); !reflect.DeepEqual(got, []string{"pan-alpha-zsh"}) {
		t.Fatalf("resolved uids = %v, want [pan-alpha-zsh]", got)
	}
}

// TestRepeatedOccurrencesUnionAndUIDPrefixResolvesTheSameResource covers the
// repeatable singular flags and the uid: form together.
func TestRepeatedOccurrencesUnionAndUIDPrefixResolvesTheSameResource(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))

	resolution, err := resolver.ResolveWindows(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{
			mustRef(t, metadata.KindWindow, "review"),
			mustRef(t, metadata.KindWindow, UIDPrefix+"win-alpha-main"),
		},
	})
	if err != nil {
		t.Fatalf("ResolveWindows error = %v", err)
	}
	// The union preserves occurrence order, so `review` comes first.
	if got := resolution.UIDs(); !reflect.DeepEqual(got, []string{"win-alpha-review", "win-alpha-main"}) {
		t.Fatalf("union = %v, want [win-alpha-review win-alpha-main]", got)
	}

	// The same resource reached twice, once by name and once by uid, collapses.
	deduped, err := resolver.ResolveWindows(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{
			mustRef(t, metadata.KindWindow, "main"),
			mustRef(t, metadata.KindWindow, UIDPrefix+"win-alpha-main"),
		},
	})
	if err != nil {
		t.Fatalf("ResolveWindows error = %v", err)
	}
	if got := deduped.UIDs(); !reflect.DeepEqual(got, []string{"win-alpha-main"}) {
		t.Fatalf("dedupe = %v, want [win-alpha-main]", got)
	}
	if deduped.Trace[0].Count != 2 || deduped.Trace[2].Count != 1 {
		t.Fatalf("trace = %#v, want the union to hold 2 and the dedupe to hold 1", deduped.Trace)
	}
}

// TestLabelSelectorsAreANDedOverTheTargetKind covers the repeatable label
// filter, including the AND of two conditions.
func TestLabelSelectorsAreANDedOverTheTargetKind(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	scope := Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main")},
	}

	single := scope
	single.Labels = []Label{mustLabel(t, "tier=primary")}
	got, err := resolver.ResolvePanes(single)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if want := []string{"zsh", "codex-pane"}; !reflect.DeepEqual(names(got.Matches), want) {
		t.Fatalf("tier=primary matched %v, want %v", names(got.Matches), want)
	}

	both := scope
	both.Labels = []Label{mustLabel(t, "tier=primary"), mustLabel(t, "role=agent")}
	got, err = resolver.ResolvePanes(both)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if want := []string{"codex-pane"}; !reflect.DeepEqual(names(got.Matches), want) {
		t.Fatalf("AND of two labels matched %v, want %v", names(got.Matches), want)
	}

	// A condition no resource satisfies empties the set rather than being
	// ignored.
	none := scope
	none.Labels = []Label{mustLabel(t, "tier=primary"), mustLabel(t, "role=nosuch")}
	got, err = resolver.ResolvePanes(none)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if len(got.Matches) != 0 {
		t.Fatalf("unsatisfiable AND matched %v, want none", names(got.Matches))
	}
}

// TestPaneScopeIsOwnerScopedAcrossProjectsWindowsAndAgents is the owner-scope
// fixture assertion. The Pane name "zsh" is legal four times over because Pane
// names are unique only inside their owner scope.
func TestPaneScopeIsOwnerScopedAcrossProjectsWindowsAndAgents(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	zsh := []Ref{mustRef(t, metadata.KindPane, "zsh")}

	// No --project: every Window in the registry is in scope.
	all, err := resolver.ResolvePanes(Query{Panes: zsh})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if len(all.Matches) != 4 {
		t.Fatalf("registry-wide `zsh` matched %d panes, want 4", len(all.Matches))
	}

	// --project narrows to one Project's Windows.
	scoped, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Panes:   zsh,
	})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if got := scoped.UIDs(); !reflect.DeepEqual(got, []string{"pan-alpha-zsh", "pan-alpha-review-zsh"}) {
		t.Fatalf("project-scoped `zsh` = %v", got)
	}

	// --project plus --window narrows to exactly one owner scope.
	exact, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main")},
		Panes:   zsh,
	})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if got := exact.UIDs(); !reflect.DeepEqual(got, []string{"pan-alpha-zsh"}) {
		t.Fatalf("window-scoped `zsh` = %v", got)
	}

	// A managed Pane is owned by its Agent but still lives in its Window's
	// scope, and its owner context names the whole chain.
	managed, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main")},
		Panes:   []Ref{mustRef(t, metadata.KindPane, "codex-pane")},
	})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if len(managed.Matches) != 1 {
		t.Fatalf("codex-pane matched %d panes, want 1", len(managed.Matches))
	}
	if got := managed.Matches[0].Owner.String(); got != "project/alpha window/main agent/codex" {
		t.Fatalf("managed pane owner = %q", got)
	}
}

// TestObservedStatusIsTheOneDerivationRule pins the single rule every kind's
// status goes through, as a truth table over its two inputs.
//
// There is deliberately nothing else in the codebase that decides live vs
// offline vs missing-root. A second rule is how the two halves of a
// three-valued status drift apart, and MissingRoot's precedence is exactly the
// kind of thing that gets re-implemented slightly differently the second time.
func TestObservedStatusIsTheOneDerivationRule(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		missingRoot bool
		bound       bool
		want        Status
	}{
		{name: "observed live", bound: true, want: StatusLive},
		{name: "observed on nothing", want: StatusOffline},
		{name: "missing root outranks an unobserved runtime", missingRoot: true, want: StatusMissingRoot},
		{name: "missing root outranks a live runtime", missingRoot: true, bound: true, want: StatusMissingRoot},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ObservedStatus(test.missingRoot, test.bound); got != test.want {
				t.Fatalf("ObservedStatus(%t, %t) = %q, want %q", test.missingRoot, test.bound, got, test.want)
			}
		})
	}
}

// TestWindowAndPaneStatusIsObservedNotInherited is the status-derivation table
// of the resolver: the same registry, read against three different machines.
//
// The registry never changes across the rows. Only the observation does, and
// the reported status follows the observation every time. That is the contract
// in one sentence: status is an observation, not a stored field.
func TestWindowAndPaneStatusIsObservedNotInherited(t *testing.T) {
	t.Parallel()

	registry := standardRegistry(t)

	for _, test := range []struct {
		name       string
		observed   metadata.RuntimeObservation
		wantWindow map[string]Status
		wantPane   map[string]Status
	}{
		{
			name:     "the machine runs the alpha project",
			observed: liveAlphaObservation(),
			wantWindow: map[string]Status{
				"win-alpha-main":   StatusLive,
				"win-alpha-review": StatusLive,
				"win-beta-main":    StatusOffline,
				"win-gone-main":    StatusMissingRoot,
			},
			wantPane: map[string]Status{
				"pan-alpha-zsh":        StatusLive,
				"pan-alpha-log":        StatusLive,
				"pan-alpha-codex":      StatusLive,
				"pan-alpha-review-zsh": StatusLive,
				"pan-beta-zsh":         StatusOffline,
				"pan-gone-zsh":         StatusMissingRoot,
			},
		},
		{
			name: "one pane of a live window was closed",
			observed: observing(
				[]string{"win-alpha-main", "win-alpha-review"},
				[]string{"pan-alpha-zsh", "pan-alpha-codex", "pan-alpha-review-zsh"},
			),
			wantWindow: map[string]Status{
				"win-alpha-main":   StatusLive,
				"win-alpha-review": StatusLive,
			},
			wantPane: map[string]Status{
				// Judged on its own binding, not on its Window's: closing one
				// pane offlines exactly that Pane.
				"pan-alpha-log":   StatusOffline,
				"pan-alpha-zsh":   StatusLive,
				"pan-alpha-codex": StatusLive,
			},
		},
		{
			name: "nothing is running at all",
			// The registry still says project alpha has a live session. It is
			// not consulted: no live tmux object mirrors any uid, so every
			// Window and Pane is offline. This row is the defect.
			observed: metadata.RuntimeObservation{},
			wantWindow: map[string]Status{
				"win-alpha-main":   StatusOffline,
				"win-alpha-review": StatusOffline,
				"win-beta-main":    StatusOffline,
				"win-gone-main":    StatusMissingRoot,
			},
			wantPane: map[string]Status{
				"pan-alpha-zsh":        StatusOffline,
				"pan-alpha-log":        StatusOffline,
				"pan-alpha-codex":      StatusOffline,
				"pan-alpha-review-zsh": StatusOffline,
				"pan-beta-zsh":         StatusOffline,
				"pan-gone-zsh":         StatusMissingRoot,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolver := NewObserved(registry, test.observed)

			windows, err := resolver.ResolveWindows(Query{})
			if err != nil {
				t.Fatalf("ResolveWindows error = %v", err)
			}
			assertStatusByUID(t, metadata.KindWindow, windows.Matches, test.wantWindow)

			panes, err := resolver.ResolvePanes(Query{})
			if err != nil {
				t.Fatalf("ResolvePanes error = %v", err)
			}
			assertStatusByUID(t, metadata.KindPane, panes.Matches, test.wantPane)
		})
	}
}

// assertStatusByUID checks the wanted subset of a resolution's statuses.
func assertStatusByUID(t *testing.T, kind metadata.Kind, matches []Match, want map[string]Status) {
	t.Helper()
	seen := map[string]Status{}
	for _, match := range matches {
		seen[match.UID] = match.Status
	}
	for uid, wanted := range want {
		got, ok := seen[uid]
		if !ok {
			t.Fatalf("%s %q did not resolve at all", kind, uid)
		}
		if got != wanted {
			t.Fatalf("%s %q status = %q, want %q", kind, uid, got, wanted)
		}
	}
}

// TestStoredSessionLivenessNeverDecidesWindowOrPaneStatus is the negative
// guard: the stored bool must be unreachable from a Window or Pane status read.
//
// It flips `status.session.live` on the owning Project and nothing else, then
// asserts every Window and Pane status is byte-identical. The old
// implementation fails both halves of this -- with the observation empty it
// reported live, and with the observation full it would still have tracked the
// bool.
func TestStoredSessionLivenessNeverDecidesWindowOrPaneStatus(t *testing.T) {
	t.Parallel()

	build := func(live bool) metadata.Registry {
		b := newBuilder(t)
		b.project("prj-alpha", "alpha", "", "/srv/alpha", &metadata.SessionProjection{Name: "alpha", Live: live}, false)
		b.window("win-alpha-main", "main", "prj-alpha", nil)
		b.shellPane("pan-alpha-zsh", "zsh", "", "win-alpha-main", "/srv/alpha", nil)
		b.shellPane("pan-alpha-log", "log", "", "win-alpha-main", "/srv/alpha", nil)
		b.agentWithPane("agt-alpha-codex", "codex", "win-alpha-main", "pan-alpha-codex", "codex-pane", nil)
		return b.build()
	}

	// The machine runs the Window and one of its Panes, whatever the stored
	// bool says.
	observed := observing([]string{"win-alpha-main"}, []string{"pan-alpha-zsh"})
	want := map[string]Status{
		"win-alpha-main":  StatusLive,
		"pan-alpha-zsh":   StatusLive,
		"pan-alpha-log":   StatusOffline,
		"pan-alpha-codex": StatusOffline,
	}

	for _, live := range []bool{true, false} {
		resolver := NewObserved(build(live), observed)

		windows, err := resolver.ResolveWindows(Query{})
		if err != nil {
			t.Fatalf("ResolveWindows(session live=%t) error = %v", live, err)
		}
		panes, err := resolver.ResolvePanes(Query{})
		if err != nil {
			t.Fatalf("ResolvePanes(session live=%t) error = %v", live, err)
		}
		for _, match := range slices.Concat(windows.Matches, panes.Matches) {
			if got := want[match.UID]; got != match.Status {
				t.Fatalf("with status.session.live=%t, %s %q status = %q, want %q (the stored bool leaked into the answer)",
					live, match.Kind, match.UID, match.Status, got)
			}
		}
	}
}

// TestOfflineResourcesStayQueryable pins the preservation half of the contract:
// runtime disappearing never deletes, re-identifies, or hides a resource.
func TestOfflineResourcesStayQueryable(t *testing.T) {
	t.Parallel()

	// Nothing at all is running.
	resolver := NewObserved(standardRegistry(t), metadata.RuntimeObservation{})

	offline, err := resolver.ResolvePanes(Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "beta")),
		Panes:   []Ref{mustRef(t, metadata.KindPane, "zsh")},
	})
	if err != nil {
		t.Fatalf("ResolvePanes(beta) error = %v", err)
	}
	if got := offline.UIDs(); !reflect.DeepEqual(got, []string{"pan-beta-zsh"}) {
		t.Fatalf("offline resolution = %v, want [pan-beta-zsh]", got)
	}

	// A MissingRoot Project is preserved and selectable rather than deleted.
	missing, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, "gone"))
	if err != nil {
		t.Fatalf("ResolveProject(gone) error = %v", err)
	}
	if missing.CWD != "/srv/gone" {
		t.Fatalf("MissingRoot project lost spec.root: %q", missing.CWD)
	}
}

// TestProjectStatusStillReadsItsSessionProjection pins the one kind whose
// runtime object is a tmux session rather than a mirrored uid. Project status
// is unchanged by this Phase.
func TestProjectStatusStillReadsItsSessionProjection(t *testing.T) {
	t.Parallel()

	resolver := NewObserved(standardRegistry(t), liveAlphaObservation())
	for _, test := range []struct {
		project string
		want    Status
	}{
		{project: "alpha", want: StatusLive},
		{project: "beta", want: StatusOffline},
		{project: "gone", want: StatusMissingRoot},
	} {
		match, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, test.project))
		if err != nil {
			t.Fatalf("ResolveProject(%q) error = %v", test.project, err)
		}
		if match.Status != test.want {
			t.Fatalf("project %q status = %q, want %q", test.project, match.Status, test.want)
		}
	}
}

// TestMissingRootOutranksAStaleLiveSession pins the status precedence rule on
// every kind at once: the Project's own session projection, and a Window and a
// Pane the machine is demonstrably still running.
//
// This is the preservation contract. A Project whose root disappeared needs an
// explicit rebind or prune, and a tmux window that happens to still be up must
// not hide that from any of the three reads.
func TestMissingRootOutranksAStaleLiveSession(t *testing.T) {
	t.Parallel()

	b := newBuilder(t)
	b.project("prj-stale", "stale", "", "/srv/stale", &metadata.SessionProjection{Name: "stale", Live: true}, true)
	b.window("win-stale", "main", "prj-stale", nil)
	b.shellPane("pan-stale", "zsh", "", "win-stale", "/srv/stale", nil)
	registry := b.build()

	resolver := NewObserved(registry, observing([]string{"win-stale"}, []string{"pan-stale"}))

	match, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, "stale"))
	if err != nil {
		t.Fatalf("ResolveProject error = %v", err)
	}
	if match.Status != StatusMissingRoot {
		t.Fatalf("project status = %q, want %q", match.Status, StatusMissingRoot)
	}

	windows, err := resolver.ResolveWindows(Query{})
	if err != nil {
		t.Fatalf("ResolveWindows error = %v", err)
	}
	assertStatusByUID(t, metadata.KindWindow, windows.Matches, map[string]Status{"win-stale": StatusMissingRoot})

	panes, err := resolver.ResolvePanes(Query{})
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	assertStatusByUID(t, metadata.KindPane, panes.Matches, map[string]Status{"pan-stale": StatusMissingRoot})
}

// TestProjectSelectorIsExactOne covers the at-most-once exact-one --project
// rule, including the duplicate-displayName no-match.
func TestProjectSelectorIsExactOne(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))

	match, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, "alpha"))
	if err != nil {
		t.Fatalf("ResolveProject(alpha) error = %v", err)
	}
	if match.UID != "prj-alpha" {
		t.Fatalf("uid = %q, want prj-alpha", match.UID)
	}

	match, err = resolver.ResolveProject(mustRef(t, metadata.KindProject, UIDPrefix+"prj-beta"))
	if err != nil {
		t.Fatalf("ResolveProject(uid:prj-beta) error = %v", err)
	}
	if match.Name != "beta" {
		t.Fatalf("name = %q, want beta", match.Name)
	}

	// "projmux" is a duplicate displayName, not a name.
	if _, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, "projmux")); err == nil {
		t.Fatal("a displayName resolved a project selector")
	} else if !metadata.IsUsageError(err) {
		t.Fatalf("no-match error is not a usage error: %v", err)
	}
}

// TestParseRefAndParseLabelRejectMalformedInput covers the selector parser.
func TestParseRefAndParseLabelRejectMalformedInput(t *testing.T) {
	t.Parallel()

	if _, err := ParseRef(metadata.KindWindow, ""); err == nil {
		t.Fatal("empty --window value parsed")
	}
	if _, err := ParseRef(metadata.KindWindow, UIDPrefix); err == nil {
		t.Fatal("bare uid: prefix parsed")
	}

	ref := mustRef(t, metadata.KindPane, UIDPrefix+"pan-alpha-zsh")
	if !ref.IsUID() || ref.UID != "pan-alpha-zsh" || ref.Name != "" {
		t.Fatalf("uid ref = %#v", ref)
	}
	name := mustRef(t, metadata.KindPane, "zsh")
	if name.IsUID() || name.Name != "zsh" || name.Raw != "zsh" {
		t.Fatalf("name ref = %#v", name)
	}

	if _, err := ParseLabel("role"); err == nil {
		t.Fatal("--selector without = parsed")
	}
	if _, err := ParseLabel("=shell"); err == nil {
		t.Fatal("--selector with an empty key parsed")
	}
	label := mustLabel(t, " role = shell ")
	if label.Key != "role" || label.Value != "shell" || label.String() != "role=shell" {
		t.Fatalf("label = %#v", label)
	}
}

// TestResolverNeverMutatesTheSuppliedRegistry proves the read-only guarantee at
// the model level: the resolver works on its own copy.
func TestResolverNeverMutatesTheSuppliedRegistry(t *testing.T) {
	t.Parallel()

	registry := standardRegistry(t)
	before := registry.Clone()
	resolver := New(registry)

	if _, err := resolver.ResolvePanes(Query{Panes: []Ref{mustRef(t, metadata.KindPane, "zsh")}}); err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("resolving mutated the caller's registry")
	}
}

// TestSelectorErrorsAreUsageErrors proves the exit-code classification seam:
// a selector failure is operator input, so it is a metadata usage error and
// reaches CLI exit code 2 rather than the runtime exit code 1.
func TestSelectorErrorsAreUsageErrors(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))
	_, err := resolver.ResolveProject(mustRef(t, metadata.KindProject, "nosuch"))
	if err == nil {
		t.Fatal("missing project resolved")
	}
	if !metadata.IsUsageError(err) {
		t.Fatalf("selector error is not a usage error: %v", err)
	}
	// "nosuch" resolves zero Projects, so this is the one failure class that
	// does wrap ErrNotFound. The too-many and malformed cases must not;
	// TestOnlyANoMatchFailureWrapsErrNotFound pins all three together.
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("no-match selector error does not wrap ErrNotFound: %v", err)
	}
	var selectorErr *SelectorError
	if !errors.As(err, &selectorErr) {
		t.Fatalf("selector error is not a *SelectorError: %v", err)
	}
	if !strings.Contains(err.Error(), "--project nosuch") {
		t.Fatalf("error text does not name the failing selector: %q", err)
	}
}

// TestNoValueTokenCanBecomeAnActiveTargetSentinel is the falsifiable record
// behind the decision that the implicit active target has no spelling of its
// own: an empty selector is the only way to ask for it.
//
// The tempting alternative -- `--pane current`, `--pane active` -- cannot work.
// ParseRef takes any non-`uid:` token as a bare metadata.name, and ValidateName
// accepts both words, so a resource really can be named `current`. A sentinel
// would therefore shadow that resource silently: the same argv would mean "the
// pane named current" on one machine and "the focused pane" on another, and the
// operator would have no way to address the former.
//
// The test also records the escape hatch for a future Phase that wants an
// explicit spelling: `.` is already reserved by ValidateName and `@` is already
// a forbidden name rune, so either could become a sentinel without shadowing
// anything. Neither is claimed here.
func TestNoValueTokenCanBecomeAnActiveTargetSentinel(t *testing.T) {
	t.Parallel()

	for _, candidate := range []string{"current", "active", "focused", "this", "here", "self"} {
		if err := metadata.ValidateName(candidate); err != nil {
			t.Fatalf("%q is not a legal resource name (%v); it could be a sentinel after all", candidate, err)
		}
		ref, err := ParseRef(metadata.KindPane, candidate)
		if err != nil {
			t.Fatalf("ParseRef(%q) error = %v", candidate, err)
		}
		if ref.IsUID() || ref.Name != candidate {
			t.Fatalf("ParseRef(%q) = %+v, want a bare name occurrence", candidate, ref)
		}
	}

	// The two collision-free candidates, recorded but deliberately unused.
	for _, reserved := range []string{".", "@"} {
		if err := metadata.ValidateName(reserved); err == nil {
			t.Fatalf("%q became a legal name; the documented sentinel escape hatch is gone", reserved)
		}
	}
}

func refPtr(ref Ref) *Ref { return &ref }
