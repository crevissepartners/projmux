package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// recordingProjectStartupReporter captures exactly what the operator is told,
// without a tmux server.
type recordingProjectStartupReporter struct {
	messages []string
}

func (r *recordingProjectStartupReporter) Report(message string) {
	r.messages = append(r.messages, message)
}

func (r *recordingProjectStartupReporter) contains(fragment string) bool {
	return slices.ContainsFunc(r.messages, func(m string) bool { return strings.Contains(m, fragment) })
}

// freshStartFixtureStore builds the shared resource fixture with one conversation
// pointer recorded, so the confirmation's status.sessionRef count is a real
// number rather than a constant zero.
func freshStartFixtureStore(t *testing.T) *fakeResourceStore {
	t.Helper()
	store := newFakeResourceStore(t)
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("resource fixture lost agt-alpha-codex")
	}
	agent.Status.SessionRef = codexConversationRef("thread-alpha")
	return store
}

// TestProjectStartupRowTable is the row-addition regression guard. It pins every
// row of the `ask`-mode startup screen -- name, description, and transport value
// -- so the `new` row cannot be added, renamed, or reordered without this table
// saying so, and so the four rows that already shipped cannot drift while it is.
func TestProjectStartupRowTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		candidate       projectStartupCandidate
		wantName        string
		wantDescription string
		wantValue       string
	}{
		{
			name:            "latest snapshot",
			candidate:       projectStartupCandidate{Kind: projectStartupKindLatest, Description: "saved 2026-05-13 12:00:00 UTC, auto-saved, 1 window, 1 pane"},
			wantName:        "Latest snapshot",
			wantDescription: "saved 2026-05-13 12:00:00 UTC, auto-saved, 1 window, 1 pane",
			wantValue:       "latest",
		},
		{
			name:            "named snapshot",
			candidate:       projectStartupCandidate{Kind: projectStartupKindNamed, Name: "team", Description: "team, team workspace"},
			wantName:        "Named snapshot",
			wantDescription: "team, team workspace",
			wantValue:       "named:team",
		},
		{
			name:            "project topology",
			candidate:       topologyProjectStartupCandidate(),
			wantName:        "Project topology",
			wantDescription: "restore every saved Window, shell Pane, and Agent",
			wantValue:       "topology",
		},
		{
			name:            "new",
			candidate:       newProjectStartupCandidate(),
			wantName:        "New",
			wantDescription: "discard the latest snapshot and every saved Window, Pane, and Agent, then start one fresh shell Window",
			wantValue:       "new",
		},
		{
			name:            "back",
			candidate:       backProjectStartupCandidate(),
			wantName:        "Back",
			wantDescription: "return to projects",
			wantValue:       settingsBackValue,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label := projectStartupPickerLabel(tc.candidate)
			if !strings.Contains(label, tc.wantName) {
				t.Fatalf("label = %q, want the row name %q", label, tc.wantName)
			}
			if !strings.Contains(label, tc.wantDescription) {
				t.Fatalf("label = %q, want the row description %q", label, tc.wantDescription)
			}
			if got := projectStartupPickerValue(tc.candidate); got != tc.wantValue {
				t.Fatalf("value = %q, want %q", got, tc.wantValue)
			}
		})
	}
}

// TestProjectStartupNewRowValuePaths asserts all four value paths of the `new`
// row: the picker value it renders, the candidate that value resolves back to,
// the row's place in the rendered candidate list, and the
// `switch sidebar-open --mode` token the re-exec transport carries.
func TestProjectStartupNewRowValuePaths(t *testing.T) {
	t.Parallel()

	if got, want := projectStartupPickerValue(newProjectStartupCandidate()), projectStartupValueNew; got != want {
		t.Fatalf("picker value = %q, want %q", got, want)
	}
	candidate, ok := projectStartupCandidateFromValue(projectStartupValueNew)
	if !ok || candidate.Kind != projectStartupKindNew {
		t.Fatalf("projectStartupCandidateFromValue(%q) = %+v, %t; want the new kind", projectStartupValueNew, candidate, ok)
	}
	if projectStartupKindNew != projectStartupValueNew {
		t.Fatalf("the picker value %q and the --mode token %q must be one spelling", projectStartupValueNew, projectStartupKindNew)
	}

	cmd := &switchCommand{
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
	}
	values := make([]string, 0, 3)
	for _, row := range cmd.projectStartupCandidates("workspace", t.TempDir()) {
		values = append(values, projectStartupPickerValue(row))
	}
	want := []string{projectStartupValueTopology, projectStartupValueNew, settingsBackValue}
	if !slices.Equal(values, want) {
		t.Fatalf("snapshotless startup rows = %q, want %q", values, want)
	}
}

// TestProjectStartupCandidateFromValueParity keeps the retired-value path and
// every shipped value spelling intact next to the new one.
func TestProjectStartupCandidateFromValueParity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value    string
		wantKind string
		wantName string
		wantOK   bool
	}{
		{value: "latest", wantKind: projectStartupKindLatest, wantOK: true},
		{value: "named:team", wantKind: projectStartupKindNamed, wantName: "team", wantOK: true},
		{value: "topology", wantKind: projectStartupKindTopology, wantOK: true},
		{value: "empty", wantKind: projectStartupKindTopology, wantOK: true},
		{value: "new", wantKind: projectStartupKindNew, wantOK: true},
		{value: settingsBackValue, wantKind: projectStartupKindBack, wantOK: true},
		{value: "named:", wantOK: false},
		{value: "nonsense", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			candidate, ok := projectStartupCandidateFromValue(tc.value)
			if ok != tc.wantOK {
				t.Fatalf("projectStartupCandidateFromValue(%q) ok = %t, want %t", tc.value, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if candidate.Kind != tc.wantKind || candidate.Name != tc.wantName {
				t.Fatalf("projectStartupCandidateFromValue(%q) = %+v, want kind %q name %q",
					tc.value, candidate, tc.wantKind, tc.wantName)
			}
		})
	}
}

// TestProjectFreshStartPruneScope pins the confirmed prune target: that Project's
// every stored Window, Pane, and Agent, and nothing else. The Registry state
// before and after each prune is asserted as a whole, so a cascade that reached
// one record too far -- another Project, a Project record, a name reservation --
// fails here rather than in production.
func TestProjectFreshStartPruneScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		root        string
		wantWindows int
		wantPanes   int
		wantAgents  int
		wantRefs    int
		wantCounts  string
		// wantRemoved is every uid the prune must delete.
		wantRemoved []string
		// wantKept is every uid that must survive it.
		wantKept []string
	}{
		{
			name:        "project with windows panes and an agent",
			root:        "/srv/alpha",
			wantWindows: 2, wantPanes: 4, wantAgents: 1, wantRefs: 1,
			wantCounts:  "Window 2 / Pane 4 / Agent 1",
			wantRemoved: []string{"win-alpha-main", "win-alpha-review", "pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review", "agt-alpha-codex"},
			wantKept:    []string{"prj-alpha", "prj-beta", "prj-gone", "win-beta-main", "pan-beta-zsh", "agt-beta-codex"},
		},
		{
			name:        "project with one window and an offline agent",
			root:        "/srv/beta",
			wantWindows: 1, wantPanes: 1, wantAgents: 1, wantRefs: 0,
			wantCounts:  "Window 1 / Pane 1 / Agent 1",
			wantRemoved: []string{"win-beta-main", "pan-beta-zsh", "agt-beta-codex"},
			wantKept:    []string{"prj-alpha", "prj-beta", "prj-gone", "win-alpha-main", "win-alpha-review", "pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review", "agt-alpha-codex"},
		},
		{
			name:        "project that declares no topology",
			root:        "/srv/gone",
			wantWindows: 0, wantPanes: 0, wantAgents: 0, wantRefs: 0,
			wantCounts:  "Window 0 / Pane 0 / Agent 0",
			wantRemoved: nil,
			wantKept:    []string{"prj-alpha", "prj-beta", "prj-gone", "win-alpha-main", "win-alpha-review", "win-beta-main", "agt-alpha-codex", "agt-beta-codex"},
		},
		{
			name:        "root no Project claims",
			root:        "/srv/unregistered",
			wantWindows: 0, wantPanes: 0, wantAgents: 0, wantRefs: 0,
			wantCounts:  "Window 0 / Pane 0 / Agent 0",
			wantRemoved: nil,
			wantKept:    []string{"prj-alpha", "prj-beta", "prj-gone", "win-alpha-main", "win-alpha-review", "win-beta-main", "agt-alpha-codex", "agt-beta-codex"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := freshStartFixtureStore(t)
			starter := &registryProjectFreshStarter{resources: store.store()}

			plan, err := starter.PlanProjectFreshStart(tc.root)
			if err != nil {
				t.Fatalf("PlanProjectFreshStart() error = %v", err)
			}
			if plan.Windows != tc.wantWindows || plan.Panes != tc.wantPanes || plan.Agents != tc.wantAgents {
				t.Fatalf("plan = %+v, want Window %d / Pane %d / Agent %d",
					plan, tc.wantWindows, tc.wantPanes, tc.wantAgents)
			}
			if plan.AgentSessionRefs != tc.wantRefs {
				t.Fatalf("plan.AgentSessionRefs = %d, want %d", plan.AgentSessionRefs, tc.wantRefs)
			}
			if got := plan.Counts(); got != tc.wantCounts {
				t.Fatalf("plan.Counts() = %q, want %q", got, tc.wantCounts)
			}
			if store.writes != 0 {
				t.Fatalf("planning wrote the Registry %d time(s)", store.writes)
			}

			before := store.snapshot()
			if err := starter.PruneProjectFreshStart(context.Background(), tc.root, plan); err != nil {
				t.Fatalf("PruneProjectFreshStart() error = %v", err)
			}
			if len(tc.wantRemoved) == 0 {
				if store.writes != 0 || store.transactions != 0 {
					t.Fatalf("an empty prune opened a transaction: transactions=%d writes=%d", store.transactions, store.writes)
				}
				if after := store.snapshot(); after != before {
					t.Fatalf("an empty prune changed the Registry:\nbefore %s\nafter  %s", before, after)
				}
				return
			}
			for _, uid := range tc.wantRemoved {
				if freshStartRegistryHasUID(store.registry, uid) {
					t.Fatalf("prune left %s behind:\n%s", uid, store.snapshot())
				}
			}
			for _, uid := range tc.wantKept {
				if !freshStartRegistryHasUID(store.registry, uid) {
					t.Fatalf("prune removed %s, which is out of scope:\n%s", uid, store.snapshot())
				}
			}
			// The prune must converge: replanning the same root now finds nothing.
			replan, err := starter.PlanProjectFreshStart(tc.root)
			if err != nil {
				t.Fatalf("replan error = %v", err)
			}
			if !replan.Empty() {
				t.Fatalf("replan after prune = %+v, want an empty plan", replan)
			}
		})
	}
}

// TestProjectFreshStartNeverReachesAControlSession is the #702 boundary.
//
// A Window may now be owned by a Project *or* by the app-owned ControlSession
// (the Home session `projmux shell` opens), so "delete every Window of this
// Project" has to be provably unable to reach the other owner kind. It is:
// WindowsOf matches on the exact owner uid, uids are globally unique across
// kinds and carry distinct prefixes, and ProjectByRoot searches only Projects --
// a ControlSession has no root field at all to match on.
func TestProjectFreshStartNeverReachesAControlSession(t *testing.T) {
	t.Parallel()

	store := freshStartFixtureStore(t)
	addFreshStartControlSession(t, store)
	starter := &registryProjectFreshStarter{resources: store.store()}

	// A control session owns no root, so no root resolves to it.
	for _, root := range []string{"/srv/alpha", "$HOME", "", "home"} {
		plan, err := starter.PlanProjectFreshStart(root)
		if err != nil {
			t.Fatalf("PlanProjectFreshStart(%q) error = %v", root, err)
		}
		if slices.Contains(plan.WindowUIDs, "win-ctl-home") {
			t.Fatalf("fresh start for %q planned to delete the control session's Window: %+v", root, plan)
		}
	}

	plan, err := starter.PlanProjectFreshStart("/srv/alpha")
	if err != nil {
		t.Fatalf("PlanProjectFreshStart() error = %v", err)
	}
	if plan.Windows != 2 || plan.Panes != 4 || plan.Agents != 1 {
		t.Fatalf("plan = %+v, want the Project's own Window 2 / Pane 4 / Agent 1 only", plan)
	}
	if err := starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan); err != nil {
		t.Fatalf("PruneProjectFreshStart() error = %v", err)
	}
	for _, uid := range []string{"ctl-home", "win-ctl-home", "pan-ctl-home"} {
		if !freshStartRegistryHasUID(store.registry, uid) && !freshStartRegistryHasControlSession(store.registry, uid) {
			t.Fatalf("fresh start removed control session resource %s:\n%s", uid, store.snapshot())
		}
	}
}

// addFreshStartControlSession adds the app-owned Home control session with a
// Window and a Pane of its own, which is exactly the shape #702 made legal.
func addFreshStartControlSession(t *testing.T, store *fakeResourceStore) {
	t.Helper()
	registry := &store.registry
	registry.ControlSessions = append(registry.ControlSessions, coremetadata.ControlSession{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindControlSession,
		Metadata:   coremetadata.ObjectMeta{UID: "ctl-home", Name: "home", CreatedAt: resourceFixtureClock},
		Spec:       coremetadata.ControlSessionSpec{Session: "home"},
	})
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{
			UID: "win-ctl-home", Name: "home", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindControlSession, UID: "ctl-home"},
		},
		Spec: coremetadata.WindowSpec{PrimaryPaneRef: "pan-ctl-home"},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{
			UID: "pan-ctl-home", Name: "zsh", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-ctl-home"},
		},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Scope: "", Kind: coremetadata.KindControlSession, Name: "home", UID: "ctl-home"},
		coremetadata.NameReservation{Scope: "ctl-home", Kind: coremetadata.KindWindow, Name: "home", UID: "win-ctl-home"},
		coremetadata.NameReservation{Scope: "win-ctl-home", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-ctl-home"},
	)
	if err := registry.Validate(); err != nil {
		t.Fatalf("control session fixture is not a valid registry: %v", err)
	}
}

func freshStartRegistryHasControlSession(registry coremetadata.Registry, uid string) bool {
	_, ok := registry.ControlSession(uid)
	return ok
}

func freshStartRegistryHasUID(registry coremetadata.Registry, uid string) bool {
	if _, ok := registry.Project(uid); ok {
		return true
	}
	if _, ok := registry.Window(uid); ok {
		return true
	}
	if _, ok := registry.Pane(uid); ok {
		return true
	}
	_, ok := registry.Agent(uid)
	return ok
}

// TestProjectFreshStartConfirmationText pins the exact confirmation step the
// operator reads. The destructive-action contract is specific about what has to
// be on screen -- per-kind counts, unrounded and unlumped, and the fact that the
// Agents' conversation pointer goes with them -- so it is asserted as text.
func TestProjectFreshStartConfirmationText(t *testing.T) {
	t.Parallel()

	store := freshStartFixtureStore(t)
	starter := &registryProjectFreshStarter{resources: store.store()}
	plan, err := starter.PlanProjectFreshStart("/srv/alpha")
	if err != nil {
		t.Fatalf("PlanProjectFreshStart() error = %v", err)
	}
	plan.LatestSnapshot = true

	var options intpickercompat.Options
	_, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { options = o },
			reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewConfirmValue}},
	})
	cmd := &switchCommand{
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		lookupEnv:    func(string) string { return "" },
		nativePicker: native,
	}
	approved, err := cmd.confirmProjectFreshStart(plan)
	if err != nil || !approved {
		t.Fatalf("confirmProjectFreshStart() = %t, %v; want true, nil", approved, err)
	}

	if got, want := options.UI, "project-startup-new-confirm"; got != want {
		t.Fatalf("confirm UI = %q, want %q", got, want)
	}
	wantHeader := "deletes Window 2 / Pane 4 / Agent 1 and discards the latest snapshot; " +
		"Named snapshots, the Project registration, its managed root, and its trust decision are kept"
	if options.Header != wantHeader {
		t.Fatalf("confirm header = %q, want %q", options.Header, wantHeader)
	}
	if len(options.Entries) != 2 {
		t.Fatalf("confirm rows = %#v, want exactly Cancel and the confirm row", options.Entries)
	}
	if got, want := options.Entries[0].Value, projectStartupNewCancelValue; got != want {
		t.Fatalf("first confirm row value = %q, want the cancel row %q", got, want)
	}
	if !strings.Contains(options.Entries[0].Label, "keep the saved state; nothing is deleted") {
		t.Fatalf("cancel row = %q, want the no-op description", options.Entries[0].Label)
	}
	wantConfirmHelp := "deletes Window 2 / Pane 4 / Agent 1; " +
		"the Agents' conversation pointer status.sessionRef (1 recorded) is deleted with them and cannot be recovered"
	if got, want := options.Entries[1].Value, projectStartupNewConfirmValue; got != want {
		t.Fatalf("second confirm row value = %q, want %q", got, want)
	}
	if !strings.Contains(options.Entries[1].Label, wantConfirmHelp) {
		t.Fatalf("confirm row = %q, want %q", options.Entries[1].Label, wantConfirmHelp)
	}
	if !strings.Contains(options.Footer, "Enter: discard and start") || !strings.Contains(options.Footer, "Esc: cancel") {
		t.Fatalf("confirm footer = %q, want the enter/esc contract", options.Footer)
	}

	// The no-snapshot and no-Agent variants of the same two lines.
	empty := projectFreshStartPlan{}
	if got, want := empty.ConfirmHeader(), "deletes Window 0 / Pane 0 / Agent 0 and there is no latest snapshot to discard; "+
		"Named snapshots, the Project registration, its managed root, and its trust decision are kept"; got != want {
		t.Fatalf("empty plan header = %q, want %q", got, want)
	}
	if got, want := empty.ConfirmRowHelp(), "deletes Window 0 / Pane 0 / Agent 0; "+
		"no Agent record remains, so no Agent conversation pointer status.sessionRef is lost"; got != want {
		t.Fatalf("empty plan confirm help = %q, want %q", got, want)
	}
	if got, want := empty.ResultMessage("alpha"), "projmux: started alpha fresh: deleted Window 0 / Pane 0 / Agent 0 "+
		"and there was no latest snapshot to discard; no Agent record remained, so nothing was resumed"; got != want {
		t.Fatalf("empty plan result = %q, want %q", got, want)
	}
	full := projectFreshStartPlan{Windows: 2, Panes: 4, Agents: 1, AgentSessionRefs: 1, LatestSnapshot: true}
	if got, want := full.ResultMessage("alpha"), "projmux: started alpha fresh: deleted Window 2 / Pane 4 / Agent 1 "+
		"and discarded the latest snapshot; no Agent record remained, so nothing was resumed"; got != want {
		t.Fatalf("result message = %q, want %q", got, want)
	}
}

// TestProjectFreshStartConfirmationBranches is the approve/cancel table. Approval
// requires the Enter key AND the exact confirm value; every other answer is a
// cancel, so a picker that closes, a stray binding, or a highlighted-but-not-
// accepted confirm row can never delete anything.
func TestProjectFreshStartConfirmationBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		reply intpickercompat.Result
		want  bool
	}{
		{name: "enter on the confirm row approves", reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewConfirmValue}, want: true},
		{name: "enter on the cancel row cancels", reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewCancelValue}},
		{name: "a non-enter key on the confirm row cancels", reply: intpickercompat.Result{Key: "esc", Value: projectStartupNewConfirmValue}},
		{name: "an empty result cancels", reply: intpickercompat.Result{}},
		{name: "an unknown value cancels", reply: intpickercompat.Result{Key: "enter", Value: "yes"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, native := scriptedPicker(t, []pickerStep{{reply: tc.reply}})
			cmd := &switchCommand{
				homeDir:      func() (string, error) { return t.TempDir(), nil },
				lookupEnv:    func(string) string { return "" },
				nativePicker: native,
			}
			got, err := cmd.confirmProjectFreshStart(projectFreshStartPlan{})
			if err != nil {
				t.Fatalf("confirmProjectFreshStart() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("confirmProjectFreshStart() = %t, want %t", got, tc.want)
			}
		})
	}
}

// freshStartSwitchFixture wires a switchCommand for the closed-Project `new`
// flow: a Registry with the shared resource fixture, a real session-state store
// with an auto-saved snapshot, and recorders for tmux calls and operator reports.
func freshStartSwitchFixture(t *testing.T, steps []pickerStep) (
	*switchCommand, *fakeResourceStore, *capturingSwitchSessionExecutor,
	*recordingTmuxRunner, *recordingProjectStartupReporter, sessionstate.Store,
) {
	t.Helper()
	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	stateStore := sessionstate.NewStore(filepath.Join(home, "state", "projmux", "sessions"))
	saveSwitchProjectStartupSnapshot(t, stateStore, "alpha")

	store := freshStartFixtureStore(t)
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	tmux := &recordingTmuxRunner{}
	reporter := &recordingProjectStartupReporter{}
	runner, native := scriptedPicker(t, steps)
	cmd := &switchCommand{
		sessions:   executor,
		identity:   stubSwitchIdentityResolver{name: "alpha"},
		tmuxRunner: tmux,
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return filepath.Join(home, "state")
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, "config")
			default:
				return ""
			}
		},
		runner:            runner,
		nativePicker:      native,
		projectTopology:   &fakeProjectTopologyMaterializer{},
		projectFreshStart: &registryProjectFreshStarter{resources: store.store()},
		startupNotices:    reporter,
	}
	return cmd, store, executor, tmux, reporter, stateStore
}

// TestSwitchProjectStartupNewApprovedPrunesDiscardsThenStartsFresh is the
// approve path end to end: the latest snapshot is discarded, the Registry
// topology is pruned, the Project comes up through the shipped single-Window
// bootstrap because no desired topology is left to materialize, and the operator
// is told that nothing was resumed.
func TestSwitchProjectStartupNewApprovedPrunesDiscardsThenStartsFresh(t *testing.T) {
	cmd, store, executor, tmux, reporter, stateStore := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewConfirmValue}},
	})
	topology := cmd.projectTopology.(*fakeProjectTopologyMaterializer)

	if err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}

	// The Registry half: every Window, Pane, and Agent of prj-alpha is gone and
	// nothing else moved.
	for _, uid := range []string{"win-alpha-main", "win-alpha-review", "pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review", "agt-alpha-codex"} {
		if freshStartRegistryHasUID(store.registry, uid) {
			t.Fatalf("approved fresh start left %s in the Registry:\n%s", uid, store.snapshot())
		}
	}
	for _, uid := range []string{"prj-alpha", "prj-beta", "win-beta-main", "pan-beta-zsh", "agt-beta-codex"} {
		if !freshStartRegistryHasUID(store.registry, uid) {
			t.Fatalf("approved fresh start removed out-of-scope %s:\n%s", uid, store.snapshot())
		}
	}
	if store.writes != 1 {
		t.Fatalf("approved fresh start wrote the Registry %d time(s), want exactly one prune transaction", store.writes)
	}

	// The snapshot half: the latest snapshot is gone.
	if _, err := stateStore.Summary("alpha"); err == nil {
		t.Fatal("approved fresh start kept the latest snapshot")
	}

	// The start half: the topology engine was consulted and found nothing, so the
	// shipped ensure bootstrap built the single fresh Window and shell Pane.
	if got, want := topology.calls, []string{"topology:/srv/alpha:alpha"}; !equalStrings(got, want) {
		t.Fatalf("topology calls = %q, want %q", got, want)
	}
	if got, want := executor.calls, []string{"authorize:/srv/alpha", "ensure:alpha", "open:alpha"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}

	// The report half: "nothing was resumed" is stated as a result.
	if !reporter.contains("deleted Window 2 / Pane 4 / Agent 1") {
		t.Fatalf("operator was not told what was deleted: %q", reporter.messages)
	}
	if !reporter.contains("discarded the latest snapshot") {
		t.Fatalf("operator was not told the snapshot was discarded: %q", reporter.messages)
	}
	if !reporter.contains("no Agent record remained, so nothing was resumed") {
		t.Fatalf("the empty replay was not reported as a normal result: %q", reporter.messages)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("fresh start issued tmux commands of its own: %#v", tmux.calls)
	}
}

// TestSwitchProjectStartupNewCancelWritesNothing is the cancel contract: zero
// Registry writes and zero tmux writes. Cancelling returns the operator to the
// startup rows, and the Registry file, the snapshot, and the runtime are all
// exactly as they were.
func TestSwitchProjectStartupNewCancelWritesNothing(t *testing.T) {
	cmd, store, executor, tmux, reporter, stateStore := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewCancelValue}},
		{reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})
	before := store.snapshot()

	err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")
	if err == nil || !strings.Contains(err.Error(), "project startup back") {
		t.Fatalf("openProjectTarget() error = %v, want the Back result after a cancel", err)
	}

	if store.writes != 0 || store.transactions != 0 {
		t.Fatalf("cancel wrote the Registry: transactions=%d writes=%d", store.transactions, store.writes)
	}
	if after := store.snapshot(); after != before {
		t.Fatalf("cancel changed the Registry:\nbefore %s\nafter  %s", before, after)
	}
	if _, err := stateStore.Summary("alpha"); err != nil {
		t.Fatalf("cancel discarded the latest snapshot: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("cancel issued tmux commands: %#v", tmux.calls)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("cancel touched the runtime: %q", executor.calls)
	}
	if !reporter.contains("fresh start canceled; nothing was deleted") {
		t.Fatalf("cancel was not reported: %q", reporter.messages)
	}
}

// TestSwitchProjectStartupNewCancelKeepsTheStartupRows proves the cancel lands
// back on the startup screen rather than on the Projects list, and that the rows
// it lands on are the same rows.
func TestSwitchProjectStartupNewCancelKeepsTheStartupRows(t *testing.T) {
	var first, second intpickercompat.Options
	cmd, _, _, _, _, _ := freshStartSwitchFixture(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { first = o },
			reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewCancelValue}},
		{observe: func(o intpickercompat.Options) { second = o },
			reply: intpickercompat.Result{Key: "enter", Value: settingsBackValue}},
	})

	_ = cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")

	if first.UI != "project-startup" || second.UI != "project-startup" {
		t.Fatalf("startup UIs = %q then %q, want the startup picker twice", first.UI, second.UI)
	}
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("cancel changed the startup rows: %d then %d", len(first.Entries), len(second.Entries))
	}
	for i := range first.Entries {
		if first.Entries[i] != second.Entries[i] {
			t.Fatalf("cancel changed row %d:\nbefore %#v\nafter  %#v", i, first.Entries[i], second.Entries[i])
		}
	}
}

// TestSwitchProjectStartupNewRefusesToStartWhileTopologyRemains covers the
// verification acceptance 3 depends on. A prune that silently left Windows behind
// would otherwise be indistinguishable from a fresh start until the restored
// topology appeared on screen.
func TestSwitchProjectStartupNewRefusesToStartWhileTopologyRemains(t *testing.T) {
	cmd, _, executor, _, _, _ := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupNewConfirmValue}},
	})
	cmd.projectFreshStart = &stubProjectFreshStarter{
		plan: projectFreshStartPlan{ProjectUID: "prj-alpha", WindowUIDs: []string{"win-alpha-main"}, Windows: 1, Panes: 2, Agents: 1},
	}

	err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")
	if err == nil || !strings.Contains(err.Error(), "still declares Window 1 / Pane 2 / Agent 1 after the prune") {
		t.Fatalf("openProjectTarget() error = %v, want the unpruned refusal", err)
	}
	if executor.openSessionName != "" || executor.ensureSessionName != "" {
		t.Fatalf("an unpruned fresh start still started the Project: %#v", executor)
	}
}

// stubProjectFreshStarter answers with a fixed plan and never prunes, so the
// post-prune verification can be exercised on its own.
type stubProjectFreshStarter struct {
	plan projectFreshStartPlan
}

func (s *stubProjectFreshStarter) PlanProjectFreshStart(string) (projectFreshStartPlan, error) {
	return s.plan, nil
}

func (s *stubProjectFreshStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) error {
	return nil
}

// TestProjectFreshStartRefusesADriftedPlan pins the reuse of the canonical
// delete discipline: the approved cascade is re-derived inside the store lock and
// a mismatch aborts with zero mutations.
func TestProjectFreshStartRefusesADriftedPlan(t *testing.T) {
	t.Parallel()

	store := freshStartFixtureStore(t)
	starter := &registryProjectFreshStarter{resources: store.store()}
	plan, err := starter.PlanProjectFreshStart("/srv/alpha")
	if err != nil {
		t.Fatalf("PlanProjectFreshStart() error = %v", err)
	}

	// A Window appears between the confirmation and the execution.
	mutator := store.mutator()
	if _, _, err := mutator.AddWindow(&store.registry, "prj-alpha", coremetadata.BootstrapWindow{
		Name: "late", Panes: []coremetadata.BootstrapPane{{CWD: "/srv/alpha"}},
	}, "/bin/zsh", "op-drift"); err != nil {
		t.Fatal(err)
	}
	before := store.snapshot()
	writesBefore := store.writes

	err = starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan)
	if err == nil || !strings.Contains(err.Error(), "changed between the confirmation and execution") {
		t.Fatalf("PruneProjectFreshStart() error = %v, want the plan-drift refusal", err)
	}
	if store.writes != writesBefore {
		t.Fatalf("a refused prune wrote the Registry: %d -> %d", writesBefore, store.writes)
	}
	if after := store.snapshot(); after != before {
		t.Fatalf("a refused prune changed the Registry:\nbefore %s\nafter  %s", before, after)
	}
}

// TestProjectFreshStartKeepsNamedSnapshots is acceptance 5's snapshot half: the
// `new` row discards the auto-saved snapshot and never touches the manual,
// user-named project files.
func TestProjectFreshStartKeepsNamedSnapshots(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(project, ".projmux", "layouts"), 0o755); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(project, ".projmux", "layouts", "team.toml")
	if err := os.WriteFile(named, []byte("schemaVersion = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateStore := sessionstate.NewStore(filepath.Join(home, "state", "projmux", "sessions"))
	saveSwitchProjectStartupSnapshot(t, stateStore, "workspace")

	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return filepath.Join(home, "state")
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, "config")
			default:
				return ""
			}
		},
		projectTopology: &fakeProjectTopologyMaterializer{},
	}

	if err := cmd.startProjectFresh(context.Background(), "workspace", project, false); err != nil {
		t.Fatalf("startProjectFresh() error = %v", err)
	}
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("fresh start deleted the Named snapshot: %v", err)
	}
	if _, err := stateStore.Summary("workspace"); err == nil {
		t.Fatal("fresh start kept the latest snapshot")
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("fresh start removed the managed root: %v", err)
	}
}

// TestSwitchSidebarOpenAcceptsTheNewMode covers the re-exec transport: the
// approved `new` decision survives the `switch sidebar-open --mode new` hop the
// sidebar route launches.
func TestSwitchSidebarOpenAcceptsTheNewMode(t *testing.T) {
	home := t.TempDir()
	store := freshStartFixtureStore(t)
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	reporter := &recordingProjectStartupReporter{}
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "alpha"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return filepath.Join(home, "state")
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, "config")
			default:
				return ""
			}
		},
		projectTopology:   &fakeProjectTopologyMaterializer{},
		projectFreshStart: &registryProjectFreshStarter{resources: store.store()},
		startupNotices:    reporter,
	}

	if err := cmd.runSidebarOpen([]string{"--path", "/srv/alpha", "--session", "alpha", "--mode", projectStartupValueNew}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if freshStartRegistryHasUID(store.registry, "win-alpha-main") {
		t.Fatalf("--mode new did not prune:\n%s", store.snapshot())
	}
	if got, want := executor.calls, []string{"authorize:/srv/alpha", "ensure:alpha", "open:alpha"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestProjectStartupNoticeMessageTruncatesOnRuneBoundaries covers the transport
// budget. The ko-KR catalog and an Agent name are both multi-byte, so a
// byte-exact cut would land mid-rune and put a replacement character in the one
// disclosure the operator actually sees.
func TestProjectStartupNoticeMessageTruncatesOnRuneBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		text          string
		wantTruncated bool
	}{
		{name: "short ascii is untouched", text: "projmux: started alpha fresh"},
		{
			name:          "long ascii is truncated",
			text:          strings.Repeat("a", projectStartupNoticeMax+40),
			wantTruncated: true,
		},
		{
			name:          "long ko-KR is truncated on a rune boundary",
			text:          strings.Repeat("저장된 상태를 버리고 새로 시작합니다. ", 20),
			wantTruncated: true,
		},
		{
			name:          "long mixed-width agent disclosure is truncated on a rune boundary",
			text:          "projmux: agent/main/에이전트-클로드 starts a new conversation instead of resuming: " + strings.Repeat("사유 ", 60),
			wantTruncated: true,
		},
		{
			name:          "a multi-byte line right at the cap is truncated on a rune boundary",
			text:          strings.Repeat("한", projectStartupNoticeMax/3+1),
			wantTruncated: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := projectStartupNoticeMessage(tc.text)
			if !utf8.ValidString(got) {
				t.Fatalf("message is not valid UTF-8: %q", got)
			}
			if strings.ContainsRune(got, utf8.RuneError) {
				t.Fatalf("message carries a replacement character: %q", got)
			}
			if tc.wantTruncated {
				if !strings.HasSuffix(got, "...") {
					t.Fatalf("truncated message = %q, want the explicit ellipsis", got)
				}
				if len(got) > projectStartupNoticeMax+len("...") {
					t.Fatalf("truncated message is %d bytes, want at most %d", len(got), projectStartupNoticeMax+len("..."))
				}
				return
			}
			if got != tc.text {
				t.Fatalf("message = %q, want the input unchanged", got)
			}
		})
	}
}

// recordingNoticeRunner captures the tmux argv the report surface emits.
type recordingNoticeRunner struct {
	calls [][]string
}

func (r *recordingNoticeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, nil
}

// TestProjectStartupNoticeSinkTeesStderrAndDisplayMessage pins the report surface
// this Phase settled on for BOTH the `new` result and Phase 0's resume-failure
// notice: every line is mirrored to the durable stderr record, and the batch is
// flushed as one `tmux display-message` the operator can actually read.
func TestProjectStartupNoticeSinkTeesStderrAndDisplayMessage(t *testing.T) {
	t.Parallel()

	var mirror bytes.Buffer
	runner := &recordingNoticeRunner{}
	sink := newProjectStartupNoticeSink(runner)
	sink.mirror = &mirror
	sink.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux-1000/smoke,42,0"
		}
		return ""
	}

	fmt.Fprintln(sink, "projmux: agent/main/codex starts a new conversation instead of resuming: no recorded conversation")
	fmt.Fprintln(sink, "projmux: agent/main/claude was not restored: pane cwd is gone")
	if len(runner.calls) != 0 {
		t.Fatalf("the sink emitted before the flush: %#v", runner.calls)
	}
	sink.Flush()

	if got := strings.Count(mirror.String(), "projmux: agent/"); got != 2 {
		t.Fatalf("stderr mirror = %q, want both lines", mirror.String())
	}
	if len(runner.calls) != 1 {
		t.Fatalf("flush emitted %d tmux calls, want exactly one display-message", len(runner.calls))
	}
	call := runner.calls[0]
	if len(call) != 3 || call[0] != "tmux" || call[1] != "display-message" {
		t.Fatalf("flush argv = %#v, want tmux display-message <message>", call)
	}
	if !strings.Contains(call[2], "starts a new conversation instead of resuming") ||
		!strings.Contains(call[2], "was not restored") {
		t.Fatalf("display-message = %q, want both disclosures in one line", call[2])
	}

	// A second flush with nothing buffered stays silent.
	sink.Flush()
	if len(runner.calls) != 1 {
		t.Fatalf("an empty flush emitted a message: %#v", runner.calls)
	}

	sink.Report("projmux: started alpha fresh")
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[1][2], "started alpha fresh") {
		t.Fatalf("Report() argv = %#v", runner.calls)
	}
	if !strings.Contains(mirror.String(), "projmux: started alpha fresh") {
		t.Fatalf("Report() skipped the stderr mirror: %q", mirror.String())
	}

	// Outside a tmux client there is no current client to display on, so the
	// display half is skipped and only the durable record is written.
	var offMirror bytes.Buffer
	offRunner := &recordingNoticeRunner{}
	off := newProjectStartupNoticeSink(offRunner)
	off.mirror = &offMirror
	off.lookupEnv = func(string) string { return "" }
	off.Report("projmux: started alpha fresh")
	if len(offRunner.calls) != 0 {
		t.Fatalf("a clientless process emitted display-message: %#v", offRunner.calls)
	}
	if !strings.Contains(offMirror.String(), "projmux: started alpha fresh") {
		t.Fatalf("a clientless process lost the durable record: %q", offMirror.String())
	}
}

// TestNewSwitchCommandWiresFreshStartAndReportSurface keeps the production wiring
// honest. A nil prune seam would turn `new` into a silent alias of the topology
// start, and a nil report surface would put the result back where nobody reads it.
func TestNewSwitchCommandWiresFreshStartAndReportSurface(t *testing.T) {
	t.Parallel()

	cmd := newSwitchCommand()
	starter, ok := cmd.projectFreshStart.(*registryProjectFreshStarter)
	if !ok || starter.resources == nil {
		t.Fatalf("switcher.projectFreshStart = %#v, want the Registry fresh-start prune", cmd.projectFreshStart)
	}
	if _, ok := cmd.startupNotices.(*projectStartupNoticeSink); !ok {
		t.Fatalf("switcher.startupNotices = %T, want the display-message report surface", cmd.startupNotices)
	}
	activation, ok := newRegistryProjectTopologyMaterializer().notices.(*projectStartupNoticeSink)
	if !ok || activation == nil {
		t.Fatalf("topology activation notices = %T, want the same report surface", newRegistryProjectTopologyMaterializer().notices)
	}
}
