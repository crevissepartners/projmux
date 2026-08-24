package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
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

type orderedFreshStarter struct{ calls *[]string }

func (s orderedFreshStarter) PlanProjectFreshStart(string) (projectFreshStartPlan, error) {
	*s.calls = append(*s.calls, "plan")
	return projectFreshStartPlan{}, nil
}

func (s orderedFreshStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) error {
	*s.calls = append(*s.calls, "prune")
	return nil
}

type orderedFreshTopology struct{ calls *[]string }

func (m orderedFreshTopology) MaterializeProjectTopology(context.Context, string, string) (bool, error) {
	*m.calls = append(*m.calls, "materialize")
	return true, nil
}

type orderedFreshSessions struct{ calls *[]string }

func (s orderedFreshSessions) EnsureSession(context.Context, string, string) error {
	*s.calls = append(*s.calls, "ensure")
	return nil
}

func (s orderedFreshSessions) OpenSession(context.Context, string) error {
	*s.calls = append(*s.calls, "open")
	return nil
}

type orderedFreshReporter struct{ calls *[]string }

func (r orderedFreshReporter) Report(string) { *r.calls = append(*r.calls, "notice") }

func TestOpenFreshFinalClientHandoffIsLast(t *testing.T) {
	t.Parallel()
	var calls []string
	cmd := &switchCommand{
		sessions:          orderedFreshSessions{calls: &calls},
		projectFreshStart: orderedFreshStarter{calls: &calls},
		projectTopology:   orderedFreshTopology{calls: &calls},
		startupNotices:    orderedFreshReporter{calls: &calls},
	}
	if err := cmd.startProjectFresh(context.Background(), "workspace", "/tmp/workspace", openedProjectBootstrap{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"plan", "prune", "plan", "materialize", "notice", "open"}
	if !slices.Equal(calls, want) {
		t.Fatalf("Open fresh order = %q, want %q", calls, want)
	}
}

func TestProjectStartupBackgroundActionsUseExactClientHandoff(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{projectStartupKindTopology, projectStartupKindNew} {
		t.Run(kind, func(t *testing.T) {
			var starterCalls []string
			runner := &recordingTmuxRunner{}
			cmd := &switchCommand{
				sessions:   &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true},
				tmuxRunner: runner,
				lookupEnv: func(name string) string {
					if name == inttmux.SwitchTargetClientEnv {
						return "/dev/pts/12"
					}
					return ""
				},
				projectTopology:   &fakeProjectTopologyMaterializer{materialized: true},
				projectFreshStart: orderedFreshStarter{calls: &starterCalls},
			}
			err := cmd.authorizeAndContinueProjectOpen(context.Background(), "/tmp/workspace", "workspace", projectStartupCandidate{Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("tmux calls = %#v, want one final handoff", runner.calls)
			}
			want := recordedTmuxCall{name: "tmux", args: []string{"-L", "projmux", "switch-client", "-c", "/dev/pts/12", "-t", "=workspace"}}
			if got := runner.calls[0]; !reflect.DeepEqual(got, want) {
				t.Fatalf("final handoff = %#v, want %#v", got, want)
			}
		})
	}
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

// TestProjectStartupRowTable pins both rows of the startup screen -- name,
// description, and transport value -- so a third action cannot be added and
// either of the closed two actions cannot be renamed or reordered silently.
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
			name:            "continue project",
			candidate:       topologyProjectStartupCandidate(),
			wantName:        "Continue project",
			wantDescription: "open every saved Window, shell Pane, and Agent",
			wantValue:       "continue",
		},
		{
			name:            "open fresh",
			candidate:       newProjectStartupCandidate(),
			wantName:        "Open fresh",
			wantDescription: "open a new Project identity with one canonical shell",
			wantValue:       "fresh",
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

func TestProjectStartupKoreanLocaleRendersExactTwoRowsAndFreshConfirmation(t *testing.T) {
	t.Parallel()

	locale := i18n.Locale("ko-KR")
	candidates := []projectStartupCandidate{
		topologyProjectStartupCandidate(locale),
		newProjectStartupCandidate(locale),
	}
	if got, want := []string{candidates[0].Label, candidates[1].Label}, []string{"이어서 열기", "새로 열기"}; !slices.Equal(got, want) {
		t.Fatalf("Korean startup row labels = %q, want %q", got, want)
	}
	options := projectStartupPickerOptions(candidates)
	options.Locale = locale
	options = localizePickerOptions(nil, nil, options)
	if got, want := options.Header, "프로젝트 시작"; got != want {
		t.Fatalf("Korean startup header = %q, want %q", got, want)
	}
	if got, want := options.Footer, "Enter: 열기  |  Esc: 프로젝트"; got != want {
		t.Fatalf("Korean startup footer = %q, want %q", got, want)
	}
	if len(options.Entries) != 2 {
		t.Fatalf("Korean startup rows = %d, want exactly 2", len(options.Entries))
	}
	for index, want := range []string{"이어서 열기", "새로 열기"} {
		if !strings.Contains(options.Entries[index].Label, want) {
			t.Fatalf("Korean startup row %d = %q, want %q", index, options.Entries[index].Label, want)
		}
	}

	plan := projectFreshStartPlan{}
	if got := plan.ResultMessageLocale(locale, "alpha"); !strings.Contains(got, "alpha") || !strings.Contains(got, "새 Project identity") {
		t.Fatalf("Korean fresh result = %q", got)
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
	want := []string{projectStartupValueTopology, projectStartupValueNew}
	if !slices.Equal(values, want) {
		t.Fatalf("snapshotless startup rows = %q, want %q", values, want)
	}
}

func TestContinueDeletedProjectRestoresSnapshotUnderNewIdentityWithoutWritingSnapshot(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	root := "/srv/continued"
	store.dirs[root] = true
	snapshot := sessionstate.Snapshot{
		Version: sessionstate.Version, Session: "continued", Source: sessionstate.SourceAutosave,
		DefaultCWD: root, SavedAt: time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC),
		Metadata: &sessionstate.ResourceMetadata{UID: "proj-deleted"},
		Windows: []sessionstate.Window{{Index: 0, Name: "main", ActivePaneIndex: 0,
			Metadata: &sessionstate.ResourceMetadata{UID: "win-deleted", OwnerKind: string(coremetadata.KindProject), OwnerUID: "proj-deleted"},
			Panes: []sessionstate.Pane{{Index: 0, CWD: root,
				Metadata: &sessionstate.ResourceMetadata{UID: "pane-deleted", OwnerKind: string(coremetadata.KindWindow), OwnerUID: "win-deleted"},
				Recipe:   sessionstate.ShellRecipe()}},
		}},
	}
	beforeSnapshot := snapshot
	starter := &registryProjectFreshStarter{
		resources: store.store(), shell: "/bin/zsh",
		loadSnapshot: func(session string) (sessionstate.Snapshot, error) {
			if session != "continued" {
				t.Fatalf("snapshot session = %q", session)
			}
			return snapshot, nil
		},
	}
	opened, err := starter.ContinueProject(context.Background(), root, "continued")
	if err != nil {
		t.Fatal(err)
	}
	if !opened.bootstrapped || opened.project.Metadata.UID == "" || opened.project.Metadata.UID == "proj-deleted" || store.writes != 1 {
		t.Fatalf("Continue opened=%+v writes=%d", opened, store.writes)
	}
	windows := store.registry.WindowsOf(opened.project.Metadata.UID)
	if len(windows) != 1 || windows[0].Metadata.UID == "win-deleted" {
		t.Fatalf("restored Windows = %+v", windows)
	}
	panes := store.registry.PanesOf(windows[0].Metadata.UID)
	if len(panes) != 1 || panes[0].Metadata.UID == "pane-deleted" || panes[0].Spec.CWD != root {
		t.Fatalf("restored Panes = %+v", panes)
	}
	if !reflect.DeepEqual(snapshot, beforeSnapshot) {
		t.Fatalf("Continue mutated snapshot input: before=%+v after=%+v", beforeSnapshot, snapshot)
	}
	writes := store.writes
	if repeat, err := starter.ContinueProject(context.Background(), root, "continued"); err != nil || repeat.bootstrapped || store.writes != writes {
		t.Fatalf("repeat Continue=%+v err=%v writes=%d", repeat, err, store.writes)
	}
}

func TestContinueDeletedProjectUnavailableSnapshotIsZeroWriteAndNeverFreshFallback(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		load func(string) (sessionstate.Snapshot, error)
	}{
		{name: "missing", load: func(string) (sessionstate.Snapshot, error) {
			return sessionstate.Snapshot{}, fmt.Errorf("%w", sessionstate.ErrNotFound)
		}},
		{name: "different root", load: func(string) (sessionstate.Snapshot, error) {
			return sessionstate.Snapshot{Version: sessionstate.Version, Session: "continued", DefaultCWD: "/srv/foreign",
				SavedAt: time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC),
				Windows: []sessionstate.Window{{Index: 0, Name: "main", Panes: []sessionstate.Pane{{Index: 0, CWD: "/srv/foreign", Recipe: sessionstate.ShellRecipe()}}}}}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			store.dirs["/srv/continued"] = true
			starter := &registryProjectFreshStarter{resources: store.store(), loadSnapshot: test.load}
			before := store.snapshot()
			_, err := starter.ContinueProject(context.Background(), "/srv/continued", "continued")
			if err == nil || !strings.Contains(err.Error(), "choose Open fresh") {
				t.Fatalf("Continue error = %v", err)
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
				t.Fatalf("unavailable Continue changed Registry: transactions=%d writes=%d", store.transactions, store.writes)
			}
		})
	}
}

func TestContinueDeletedProjectMaterializesRestoredTopologyBeforeHandoff(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	root := "/srv/restored"
	store.dirs[root] = true
	starter := &registryProjectFreshStarter{resources: store.store(), shell: "/bin/zsh", loadSnapshot: func(string) (sessionstate.Snapshot, error) {
		return sessionstate.Snapshot{Version: sessionstate.Version, Session: "restored", DefaultCWD: root,
			SavedAt: time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC),
			Windows: []sessionstate.Window{{Index: 0, Name: "main", Panes: []sessionstate.Pane{{Index: 0, CWD: root, Recipe: sessionstate.ShellRecipe()}}}}}, nil
	}}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	cmd := &switchCommand{
		sessions: executor, projectFreshStart: starter, projectTopology: topology,
		homeDir: func() (string, error) { return "/home/test", nil }, lookupEnv: func(string) string { return "" },
	}
	if err := cmd.authorizeAndContinueProjectOpen(context.Background(), root, "restored", projectStartupCandidate{Kind: projectStartupKindTopology}); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(topology.calls, []string{"topology:" + root + ":restored"}) ||
		!equalStrings(executor.calls, []string{"authorize:" + root, "open:restored"}) {
		t.Fatalf("Continue order topology=%v runtime=%v", topology.calls, executor.calls)
	}
}

// TestProjectStartupCandidateFromValueParity accepts only the two current
// startup actions. Esc is picker cancellation, not a synthetic row value.
func TestProjectStartupCandidateFromValueParity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value    string
		wantKind string
		wantName string
		wantOK   bool
	}{
		{value: "continue", wantKind: projectStartupKindTopology, wantOK: true},
		{value: "fresh", wantKind: projectStartupKindNew, wantOK: true},
		{value: "retired-snapshot-mode", wantOK: false},
		{value: "retired-topology-mode", wantOK: false},
		{value: settingsBackValue, wantOK: false},
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
			wantWindows: 1, wantPanes: 3, wantAgents: 1, wantRefs: 1,
			wantCounts:  "Window 1 / Pane 3 / Agent 1",
			wantRemoved: []string{"win-alpha-review", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review", "agt-alpha-codex"},
			wantKept:    []string{"prj-alpha", "win-alpha-main", "pan-alpha-zsh", "prj-beta", "prj-gone", "win-beta-main", "pan-beta-zsh", "agt-beta-codex"},
		},
		{
			name:        "project with one window and an offline agent",
			root:        "/srv/beta",
			wantWindows: 0, wantPanes: 0, wantAgents: 1, wantRefs: 0,
			wantCounts:  "Window 0 / Pane 0 / Agent 1",
			wantRemoved: []string{"agt-beta-codex"},
			wantKept:    []string{"prj-alpha", "prj-beta", "prj-gone", "win-beta-main", "pan-beta-zsh", "win-alpha-main", "win-alpha-review", "pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex", "pan-alpha-review", "agt-alpha-codex"},
		},
		{
			name:        "project with the minimum canonical topology",
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
			store.dirs[tc.root] = true
			starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}}

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

			plan.SessionName = "fresh"
			if err := starter.PruneProjectFreshStart(context.Background(), tc.root, plan); err != nil {
				t.Fatal(err)
			}
			fresh, ok := store.registry.ProjectByRoot(tc.root)
			if !ok || fresh.Metadata.UID == plan.ProjectUID {
				t.Fatalf("Open fresh Project = %+v, previous uid=%q", fresh, plan.ProjectUID)
			}
			windows := store.registry.WindowsOf(fresh.Metadata.UID)
			if len(windows) != 1 || len(store.registry.PanesOf(windows[0].Metadata.UID)) != 1 || len(store.registry.AgentsOf(windows[0].Metadata.UID)) != 0 {
				t.Fatalf("Open fresh topology = windows=%+v panes=%+v agents=%+v", windows,
					store.registry.PanesOf(windows[0].Metadata.UID), store.registry.AgentsOf(windows[0].Metadata.UID))
			}
			repeat, err := starter.PlanProjectFreshStart(tc.root)
			if err != nil {
				t.Fatal(err)
			}
			if !repeat.Empty() {
				t.Fatalf("repeat plan=%+v", repeat)
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
	starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}}

	// A control session owns no root, so no root resolves to it.
	for _, root := range []string{"/srv/alpha", "$HOME", "", "home"} {
		_, err := starter.PlanProjectFreshStart(root)
		if err != nil {
			t.Fatalf("PlanProjectFreshStart(%q) error = %v", root, err)
		}
	}

	plan, err := starter.PlanProjectFreshStart("/srv/alpha")
	if err != nil {
		t.Fatalf("PlanProjectFreshStart() error = %v", err)
	}
	if plan.Windows != 1 || plan.Panes != 3 || plan.Agents != 1 {
		t.Fatalf("plan = %+v, want only non-canonical Project descendants", plan)
	}
	if err := starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan); err != nil {
		t.Fatal(err)
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
		Spec: coremetadata.WindowSpec{AnchorPaneRef: "pan-ctl-home"},
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

// TestProjectFreshStartHasNoConfirmationSurface pins the neutral one-step UI.
func TestProjectFreshStartHasNoConfirmationSurface(t *testing.T) {
	t.Parallel()
	candidate := newProjectStartupCandidate()
	label := projectStartupPickerLabel(candidate)
	if strings.Contains(label, settingsColorRemove) || strings.Contains(label, "confirm") || strings.Contains(label, "delete") {
		t.Fatalf("Fresh row is destructive or confirmatory: %q", label)
	}
	if got := (projectFreshStartPlan{}).ResultMessage("alpha"); !strings.Contains(got, "new Project identity") {
		t.Fatalf("Fresh result = %q", got)
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
		projectFreshStart: &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}},
		startupNotices:    reporter,
	}
	return cmd, store, executor, tmux, reporter, stateStore
}

// TestSwitchProjectStartupOpenFreshPreservesSnapshotBytesAndCanonicalIdentity
// covers the full successful fresh action.
func TestSwitchProjectStartupOpenFreshPreservesSnapshotBytesAndCanonicalIdentity(t *testing.T) {
	cmd, store, executor, tmux, reporter, stateStore := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
	})
	topology := cmd.projectTopology.(*fakeProjectTopologyMaterializer)
	topology.materialized = true
	snapshotPath, err := stateStore.Path("alpha")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot before Open fresh: %v", err)
	}

	if err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("Open fresh Registry writes=%d, want one scoped commit", store.writes)
	}
	if _, err := stateStore.Summary("alpha"); err != nil {
		t.Fatalf("Open fresh removed the source snapshot: %v", err)
	}
	snapshotAfter, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot after Open fresh: %v", err)
	}
	if !bytes.Equal(snapshotAfter, snapshotBefore) {
		t.Fatalf("Open fresh changed snapshot bytes:\nbefore=%s\nafter=%s", snapshotBefore, snapshotAfter)
	}
	if len(topology.calls) != 1 || !equalStrings(executor.calls, []string{"authorize:/srv/alpha", "open:alpha"}) || len(tmux.calls) != 0 || len(reporter.messages) != 1 {
		t.Fatalf("Open fresh flow: topology=%v executor=%v tmux=%v notices=%v", topology.calls, executor.calls, tmux.calls, reporter.messages)
	}
}

func TestSwitchProjectStartupOpenFreshRefusesExactLiveProjectBeforeCommit(t *testing.T) {
	cmd, store, executor, _, reporter, stateStore := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
	})
	server := newFakeTmux()
	live := server.addSession("alpha-under-another-name")
	live.opts["@projmux_project_uid"] = "prj-alpha"
	live.opts["@projmux_project_path"] = "/srv/alpha"
	routed := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00projmux": server}}
	cmd.projectFreshStart.(*registryProjectFreshStarter).runner = routed

	registryBefore := store.snapshot()
	snapshotPath, err := stateStore.Path("alpha")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}

	err = cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")
	if err == nil || !strings.Contains(err.Error(), "must be exactly closed before Open fresh") {
		t.Fatalf("Open fresh error=%v, want exact-live Project refusal", err)
	}
	if store.transactions != 0 || store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("exact-live refusal changed Registry: transactions=%d writes=%d", store.transactions, store.writes)
	}
	snapshotAfter, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshotBefore, snapshotAfter) {
		t.Fatal("exact-live refusal changed the source snapshot")
	}
	if len(executor.calls) != 1 || executor.calls[0] != "authorize:/srv/alpha" {
		t.Fatalf("exact-live refusal runtime calls=%q, want trust read only", executor.calls)
	}
	if len(reporter.messages) != 0 {
		t.Fatalf("exact-live precommit refusal emitted postcommit notices: %q", reporter.messages)
	}
	for _, call := range routed.calls {
		if len(call.args) == 0 || call.args[0] != "list-sessions" {
			t.Fatalf("exact-live precommit refusal issued tmux write: %#v", routed.calls)
		}
	}
}

// TestSwitchProjectStartupNewCancelWritesNothing is the cancel contract: zero
// Registry writes and zero tmux writes. Cancelling returns the operator to the
// startup rows, and the Registry file, the snapshot, and the runtime are all
// exactly as they were.
func TestSwitchProjectStartupFreshIsOneStepWithoutConfirmation(t *testing.T) {
	confirmationShown := false
	cmd, store, executor, tmux, reporter, stateStore := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{observe: func(intpickercompat.Options) { confirmationShown = true }},
	})
	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	if err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if confirmationShown || store.writes != 1 {
		t.Fatalf("Fresh confirmationShown=%t writes=%d", confirmationShown, store.writes)
	}
	if _, err := stateStore.Summary("alpha"); err != nil {
		t.Fatalf("Fresh changed the latest snapshot: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("Fresh issued unexpected tmux commands: %#v", tmux.calls)
	}
	if !equalStrings(executor.calls, []string{"authorize:/srv/alpha", "open:alpha"}) {
		t.Fatalf("Fresh runtime calls: %q", executor.calls)
	}
	if len(reporter.messages) != 1 {
		t.Fatalf("Fresh notice = %q", reporter.messages)
	}
}

// TestSwitchProjectStartupNewCancelKeepsTheStartupRows proves the cancel lands
// back on the startup screen rather than on the Projects list, and that the rows
// it lands on are the same rows.
func TestSwitchProjectStartupFreshRowIsNeutralOpenAction(t *testing.T) {
	var first intpickercompat.Options
	cmd, _, _, _, _, _ := freshStartSwitchFixture(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { first = o },
			reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
	})
	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	_ = cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")
	if first.UI != "project-startup" || len(first.Entries) != 2 ||
		!strings.Contains(first.Entries[1].Label, settingsGlyphOpen) || strings.Contains(first.Entries[1].Label, settingsColorRemove) {
		t.Fatalf("Fresh row is not neutral: %+v", first)
	}
}

// TestSwitchProjectStartupNewRefusesToStartWhileTopologyRemains covers the
// verification acceptance 3 depends on. A prune that silently left Windows behind
// would otherwise be indistinguishable from a fresh start until the restored
// topology appeared on screen.
func TestSwitchProjectStartupNewRefusesToStartWhileTopologyRemains(t *testing.T) {
	cmd, _, executor, _, _, _ := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
	})
	cmd.projectFreshStart = &stubProjectFreshStarter{
		plan: projectFreshStartPlan{ProjectUID: "prj-alpha", Windows: 1, Panes: 2, Agents: 1},
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
	starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}}
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

	plan.SessionName = "alpha"
	err = starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan)
	if err != nil {
		t.Fatalf("one-step Fresh rejected current root state: %v", err)
	}
	if store.writes != writesBefore+1 || store.snapshot() == before {
		t.Fatalf("Fresh replacement writes=%d before=%d", store.writes, writesBefore)
	}
	if fresh, ok := store.registry.ProjectByRoot("/srv/alpha"); !ok || fresh.Metadata.UID == plan.ProjectUID {
		t.Fatalf("Fresh did not mint a new Project: %+v", fresh)
	}
}

// TestProjectFreshStartKeepsEverySnapshot is the snapshot-storage boundary.
func TestProjectFreshStartKeepsEverySnapshot(t *testing.T) {
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
	wireFakeProjectSessionPlan(cmd)

	opened := openedProjectBootstrap{project: coremetadata.Project{Metadata: coremetadata.ObjectMeta{UID: "proj-workspace", Name: "workspace"}, Spec: coremetadata.ProjectSpec{Root: project}}}
	if err := cmd.startProjectFresh(context.Background(), "workspace", project, opened); err != nil {
		t.Fatalf("startProjectFresh() error = %v", err)
	}
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("fresh start deleted the manual snapshot: %v", err)
	}
	if _, err := stateStore.Summary("workspace"); err != nil {
		t.Fatalf("fresh start changed the latest snapshot: %v", err)
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("fresh start removed the managed root: %v", err)
	}
}

// TestSwitchSidebarOpenAcceptsFreshMode covers the detached re-exec transport.
func TestSwitchSidebarOpenAcceptsFreshMode(t *testing.T) {
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
		projectFreshStart: &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}},
		startupNotices:    reporter,
	}

	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	if err := cmd.runSidebarOpen([]string{"--path", "/srv/alpha", "--session", "alpha", "--mode", projectStartupValueNew}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("--mode fresh Registry writes=%d", store.writes)
	}
	if !equalStrings(executor.calls, []string{"authorize:/srv/alpha", "open:alpha"}) {
		t.Fatalf("--mode fresh calls: %v", executor.calls)
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
