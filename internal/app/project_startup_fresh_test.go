package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func (s orderedFreshStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) (projectFreshStartCommit, error) {
	*s.calls = append(*s.calls, "prune")
	return projectFreshStartCommit{}, nil
}

type failingContinueStarter struct{ err error }

func (s failingContinueStarter) PlanProjectFreshStart(string) (projectFreshStartPlan, error) {
	return projectFreshStartPlan{}, nil
}

func (s failingContinueStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) (projectFreshStartCommit, error) {
	return projectFreshStartCommit{}, nil
}

func (s failingContinueStarter) ContinueProject(context.Context, string, string) (openedProjectBootstrap, error) {
	return openedProjectBootstrap{}, s.err
}

type orderedFreshTopology struct{ calls *[]string }

func (m orderedFreshTopology) MaterializeProjectTopology(context.Context, projectTopologyMaterializeRequest) (bool, error) {
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
	if err := cmd.startProjectFresh(context.Background(), "workspace", "/tmp/workspace", openedProjectBootstrap{}, ""); err != nil {
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

func TestAuthorizeAndContinueWrapsRawPreparationFailure(t *testing.T) {
	t.Parallel()
	cmd := &switchCommand{
		sessions:          &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true},
		projectFreshStart: failingContinueStarter{err: errors.New("injected preparation failure")},
	}
	err := cmd.authorizeAndContinueProjectOpen(context.Background(), "/srv/missing", "missing", projectStartupCandidate{Kind: projectStartupKindTopology})
	if err == nil {
		t.Fatal("raw Continue preparation failure = nil")
	}
	for _, want := range []string{"action=continue", "stage=preparation", "old_uid=-", "new_uid=-", "injected preparation failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("preparation failure=%q, want %q", err, want)
		}
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
			wantDescription: "keep this Project identity; restore saved Windows, shell Panes, and Agents, or create a new Window and shell when none remain",
			wantValue:       "continue",
		},
		{
			name:            "open fresh",
			candidate:       newProjectStartupCandidate(),
			wantName:        "Open fresh",
			wantDescription: "replace this Project identity with a new Project, Window, and shell",
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

	plan := projectFreshStartPlan{ProjectUID: "proj-old", NewProjectUID: "proj-new"}
	if got := plan.ResultMessageLocale(locale, "alpha"); !strings.Contains(got, "alpha") || !strings.Contains(got, "proj-old") || !strings.Contains(got, "proj-new") || !strings.Contains(got, "stage=materialized") {
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
			for _, want := range []string{"action=continue", "stage=snapshot-preflight", "old_uid=-", "new_uid=-"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Continue unavailable error=%q, want %q", err, want)
				}
			}
			if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
				t.Fatalf("unavailable Continue changed Registry: transactions=%d writes=%d", store.transactions, store.writes)
			}
		})
	}
}

func TestContinueDeletedProjectCommitFailureReportsAllocatedUIDAndRetainsPreimage(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	root := "/srv/continued"
	store.dirs[root] = true
	before := store.snapshot()
	resources := store.store()
	resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return coremetadata.Registry{}, false, errors.New("injected Continue commit failure")
	}
	starter := &registryProjectFreshStarter{
		resources: resources, shell: "/bin/zsh",
		loadSnapshot: func(string) (sessionstate.Snapshot, error) {
			return sessionstate.Snapshot{
				Version: sessionstate.Version, Session: "continued", DefaultCWD: root,
				SavedAt: time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC),
				Windows: []sessionstate.Window{{Index: 0, Name: "main", Panes: []sessionstate.Pane{{Index: 0, CWD: root, Recipe: sessionstate.ShellRecipe()}}}},
			}, nil
		},
	}
	_, err := starter.ContinueProject(context.Background(), root, "continued")
	if err == nil || !strings.Contains(err.Error(), "injected Continue commit failure") {
		t.Fatalf("Continue commit failure=%v", err)
	}
	if len(store.newUIDs) == 0 {
		t.Fatal("Continue commit failure did not expose an allocated Project UID")
	}
	newUID := store.newUIDs[0]
	for _, want := range []string{"action=continue", "stage=registry-commit", "old_uid=-", "new_uid=" + newUID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Continue commit failure=%q, want %q", err, want)
		}
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("Continue commit failure changed Registry preimage: writes=%d", store.writes)
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

// TestProjectFreshStartPruneScope pins atomic same-root identity replacement:
// the old graph disappears, exactly one new claimant with a canonical shell is
// committed, and unrelated graphs survive. Repeating Fresh replaces identity
// again; a canonical graph is not permission to reuse its UIDs.
func TestProjectFreshStartPruneScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                               string
		root                               string
		wantWindows, wantPanes, wantAgents int
		wantRefs                           int
	}{
		{name: "retained graph", root: "/srv/alpha", wantWindows: 2, wantPanes: 4, wantAgents: 1, wantRefs: 1},
		{name: "retained graph with offline agent", root: "/srv/beta", wantWindows: 1, wantPanes: 1, wantAgents: 1},
		{name: "zero-window Project", root: "/srv/gone"},
		{name: "deleted Project", root: "/srv/unregistered"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := freshStartFixtureStore(t)
			store.dirs[tc.root] = true
			if tc.root == "/srv/gone" {
				mutator := store.mutator()
				for _, window := range store.registry.WindowsOf("prj-gone") {
					if err := mutator.DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
						t.Fatal(err)
					}
				}
			}
			starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}}
			before := store.registry.Clone()
			oldProject, existed := before.ProjectByRoot(tc.root)

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
			if got, want := plan.Counts(), fmt.Sprintf("Window %d / Pane %d / Agent %d", tc.wantWindows, tc.wantPanes, tc.wantAgents); got != want {
				t.Fatalf("plan.Counts() = %q, want %q", got, want)
			}
			if store.writes != 0 {
				t.Fatalf("planning wrote the Registry %d time(s)", store.writes)
			}

			plan.SessionName = "fresh"
			if _, err := starter.PruneProjectFreshStart(context.Background(), tc.root, plan); err != nil {
				t.Fatal(err)
			}
			fresh, ok := store.registry.ProjectByRoot(tc.root)
			if !ok || (existed && fresh.Metadata.UID == oldProject.Metadata.UID) {
				t.Fatalf("Open fresh Project = %+v, old uid=%q", fresh, oldProject.Metadata.UID)
			}
			claimants := 0
			for _, project := range store.registry.Projects {
				if project.Spec.Root == tc.root {
					claimants++
				}
			}
			if claimants != 1 {
				t.Fatalf("same-root claimants = %d, want 1", claimants)
			}
			windows := store.registry.WindowsOf(fresh.Metadata.UID)
			if len(windows) != 1 || len(store.registry.PanesOf(windows[0].Metadata.UID)) != 1 || len(store.registry.AgentsOf(windows[0].Metadata.UID)) != 0 {
				t.Fatalf("Open fresh topology = windows=%+v panes=%+v agents=%+v", windows,
					store.registry.PanesOf(windows[0].Metadata.UID), store.registry.AgentsOf(windows[0].Metadata.UID))
			}
			if existed {
				for _, uid := range []string{oldProject.Metadata.UID, oldProject.Spec.PrimaryWindowRef} {
					if uid != "" && freshStartRegistryHasUID(store.registry, uid) {
						t.Fatalf("old graph uid %s survived Fresh", uid)
					}
				}
			}
			for _, siblingRoot := range []string{"/srv/alpha", "/srv/beta", "/srv/gone"} {
				if siblingRoot == tc.root {
					continue
				}
				beforeSibling, beforeOK := before.ProjectByRoot(siblingRoot)
				afterSibling, afterOK := store.registry.ProjectByRoot(siblingRoot)
				if beforeOK != afterOK || (beforeOK && beforeSibling.Metadata.UID != afterSibling.Metadata.UID) {
					t.Fatalf("unrelated Project %q changed", siblingRoot)
				}
			}
			repeat, err := starter.PlanProjectFreshStart(tc.root)
			if err != nil {
				t.Fatal(err)
			}
			firstUID := fresh.Metadata.UID
			writesAfterFirst := store.writes
			repeat.SessionName = "fresh"
			if _, err := starter.PruneProjectFreshStart(context.Background(), tc.root, repeat); err != nil {
				t.Fatal(err)
			}
			repeated, ok := store.registry.ProjectByRoot(tc.root)
			if !ok || repeated.Metadata.UID == firstUID || store.writes != writesAfterFirst+1 {
				t.Fatalf("repeat Fresh Project=%+v writes=%d->%d", repeated, writesAfterFirst, store.writes)
			}
		})
	}
}

func TestProjectFreshStartAgentAnchorCreatesNewMinimumIdentityOnEveryFresh(t *testing.T) {
	t.Parallel()

	store := freshStartFixtureStore(t)
	window, ok := store.registry.Window("win-beta-main")
	if !ok {
		t.Fatal("fixture lost beta Window")
	}
	agent, ok := store.registry.Agent("agt-beta-codex")
	if !ok {
		t.Fatal("fixture lost beta Agent")
	}
	store.registry.Panes = slices.DeleteFunc(store.registry.Panes, func(pane coremetadata.Pane) bool {
		return pane.Metadata.UID == "pan-beta-zsh"
	})
	store.registry.NameReservations = slices.DeleteFunc(store.registry.NameReservations, func(reservation coremetadata.NameReservation) bool {
		return reservation.UID == "pan-beta-zsh"
	})
	agentPane := coremetadata.Pane{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{
			UID:       "pan-beta-agent",
			Name:      "codex-pane",
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID},
			CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent, CWD: "/srv/beta"},
	}
	store.registry.Panes = append(store.registry.Panes, agentPane)
	store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
		Scope: agent.Metadata.UID, Kind: coremetadata.KindPane, Name: agentPane.Metadata.Name, UID: agentPane.Metadata.UID,
	})
	agent.Status.PaneRef = agentPane.Metadata.UID
	window.Spec.AnchorPaneRef = agentPane.Metadata.UID
	window.Spec.DefaultShellPaneRef = ""
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("Agent-anchor fixture: %v", err)
	}

	starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}}
	plan, err := starter.PlanProjectFreshStart("/srv/beta")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProjectUID != "prj-beta" || plan.Windows != 1 || plan.Panes != 1 || plan.Agents != 1 {
		t.Fatalf("Agent-anchor Fresh plan = %+v", plan)
	}
	if _, err := starter.PruneProjectFreshStart(context.Background(), "/srv/beta", plan); err != nil {
		t.Fatal(err)
	}
	project, ok := store.registry.ProjectByRoot("/srv/beta")
	if !ok || project.Metadata.UID == "prj-beta" {
		t.Fatalf("Fresh Project = %+v, want a new identity", project)
	}
	windows := store.registry.WindowsOf(project.Metadata.UID)
	if len(windows) != 1 || windows[0].Metadata.UID == "win-beta-main" {
		t.Fatalf("Fresh Window = %+v, want a new canonical identity", windows)
	}
	panes := store.registry.PanesOf(windows[0].Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleShell ||
		windows[0].Spec.AnchorPaneRef != panes[0].Metadata.UID || windows[0].Spec.DefaultShellPaneRef != panes[0].Metadata.UID {
		t.Fatalf("minimum shell projection = Window %+v Panes %+v", windows[0], panes)
	}
	if len(store.registry.AgentsOf(windows[0].Metadata.UID)) != 0 {
		t.Fatal("Open fresh retained Agent descendants")
	}
	firstUID := project.Metadata.UID
	writesAfterFirst := store.writes
	repeat, err := starter.PlanProjectFreshStart("/srv/beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := starter.PruneProjectFreshStart(context.Background(), "/srv/beta", repeat); err != nil {
		t.Fatal(err)
	}
	repeated, ok := store.registry.ProjectByRoot("/srv/beta")
	if !ok || repeated.Metadata.UID == firstUID || store.writes != writesAfterFirst+1 {
		t.Fatalf("repeat Fresh Project=%+v writes %d -> %d", repeated, writesAfterFirst, store.writes)
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
	if plan.Windows != 2 || plan.Panes != 4 || plan.Agents != 1 {
		t.Fatalf("plan = %+v, want the complete old Project graph", plan)
	}
	if _, err := starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan); err != nil {
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
	if got := (projectFreshStartPlan{ProjectUID: "proj-old", NewProjectUID: "proj-new"}).ResultMessage("alpha"); !strings.Contains(got, "old Project UID proj-old") || !strings.Contains(got, "new Project UID proj-new") || !strings.Contains(got, "stage=materialized") {
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
		projectRegistrar: &defaultSwitchProjectRegistrar{
			store: store.store(), shell: "/bin/zsh", sessionNameFor: func(string) string { return "alpha" },
		},
		startupNotices: reporter,
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
	oldProject, _ := store.registry.ProjectByRoot("/srv/alpha")
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
	newProject, ok := store.registry.ProjectByRoot("/srv/alpha")
	if !ok || newProject.Metadata.UID == oldProject.Metadata.UID {
		t.Fatalf("Open fresh Project = %+v, old uid=%s", newProject, oldProject.Metadata.UID)
	}
	claimants := 0
	for _, project := range store.registry.Projects {
		if project.Spec.Root == "/srv/alpha" {
			claimants++
		}
	}
	if claimants != 1 {
		t.Fatalf("Open fresh same-root claimants=%d", claimants)
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
	if !strings.Contains(reporter.messages[0], "old Project UID "+oldProject.Metadata.UID) ||
		!strings.Contains(reporter.messages[0], "new Project UID "+newProject.Metadata.UID) ||
		!strings.Contains(reporter.messages[0], "stage=materialized") {
		t.Fatalf("Open fresh notice=%q", reporter.messages)
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
	cmd.projectRegistrar = nil

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

func (s *stubProjectFreshStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) (projectFreshStartCommit, error) {
	return projectFreshStartCommit{}, nil
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
	_, err = starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan)
	if err == nil || !strings.Contains(err.Error(), "graph drifted after preflight") {
		t.Fatalf("drifted Fresh error=%v", err)
	}
	if store.writes != writesBefore || store.snapshot() != before {
		t.Fatalf("drift refusal changed Registry: writes=%d before=%d", store.writes, writesBefore)
	}
}

func TestProjectFreshStartCommitFailureRetainsExactOldGraphPreimage(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	before := store.snapshot()
	resources := store.store()
	resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return coremetadata.Registry{}, false, errors.New("injected atomic commit failure")
	}
	starter := &registryProjectFreshStarter{resources: resources, runner: &projectionMissingSessionRunner{}, shell: "/bin/zsh"}
	plan, err := starter.PlanProjectFreshStart("/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	plan.SessionName = "alpha"
	commit, err := starter.PruneProjectFreshStart(context.Background(), "/srv/alpha", plan)
	if err == nil || !strings.Contains(err.Error(), "injected atomic commit failure") {
		t.Fatalf("Fresh commit failure=%v", err)
	}
	if commit.NewProjectUID == "" || commit.NewProjectUID == plan.ProjectUID {
		t.Fatalf("Fresh commit failure allocated identity = %+v, old_uid=%s", commit, plan.ProjectUID)
	}
	for _, want := range []string{"action=fresh", "stage=registry-commit", "old_uid=" + plan.ProjectUID, "new_uid=" + commit.NewProjectUID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Fresh commit failure=%q, want %q", err, want)
		}
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("Fresh commit failure changed old graph: writes=%d", store.writes)
	}
}

func TestStartProjectFreshCommitFailureReportsAllocatedNewUID(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	before := store.snapshot()
	resources := store.store()
	resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return coremetadata.Registry{}, false, errors.New("injected production Fresh commit failure")
	}
	cmd := &switchCommand{projectFreshStart: &registryProjectFreshStarter{
		resources: resources, runner: &projectionMissingSessionRunner{}, shell: "/bin/zsh",
	}}
	err := cmd.startProjectFresh(context.Background(), "alpha", "/srv/alpha", openedProjectBootstrap{}, "")
	if err == nil || !strings.Contains(err.Error(), "injected production Fresh commit failure") {
		t.Fatalf("production Fresh commit failure=%v", err)
	}
	if len(store.newUIDs) == 0 {
		t.Fatal("production Fresh failure did not allocate a replacement UID")
	}
	newUID := store.newUIDs[0]
	for _, want := range []string{"action=fresh", "stage=registry-commit", "old_uid=prj-alpha", "new_uid=" + newUID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("production Fresh commit failure=%q, want %q", err, want)
		}
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("production Fresh commit failure changed old preimage: writes=%d", store.writes)
	}
}

func TestStartProjectFreshPostCommitReadbackFailureReportsAllocatedNewUID(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	cmd := &switchCommand{
		projectFreshStart: &registryProjectFreshStarter{
			resources: store.store(), runner: &projectionMissingSessionRunner{}, shell: "/bin/zsh",
		},
		projectRegistrar: &fakeProjectRegistrar{err: errors.New("injected replacement readback failure")},
	}
	err := cmd.startProjectFresh(context.Background(), "alpha", "/srv/alpha", openedProjectBootstrap{}, "")
	if err == nil || !strings.Contains(err.Error(), "injected replacement readback failure") {
		t.Fatalf("production Fresh replacement readback failure=%v", err)
	}
	replacement, ok := store.registry.ProjectByRoot("/srv/alpha")
	if !ok {
		t.Fatal("successful Fresh commit has no replacement Project claimant")
	}
	if replacement.Metadata.UID == "" || replacement.Metadata.UID == "prj-alpha" {
		t.Fatalf("successful Fresh commit replacement UID=%q, want allocated new UID", replacement.Metadata.UID)
	}
	for _, want := range []string{
		"action=fresh",
		"stage=replacement-readback",
		"old_uid=prj-alpha",
		"new_uid=" + replacement.Metadata.UID,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("production Fresh replacement readback failure=%q, want %q", err, want)
		}
	}
	if store.writes != 1 {
		t.Fatalf("successful Fresh commit writes=%d, want 1 before post-commit readback failure", store.writes)
	}
	if _, ok := store.registry.Project("prj-alpha"); ok {
		t.Fatal("successful Fresh commit retained the old Project after post-commit readback failure")
	}
}

// TestProjectFreshStartKeepsEverySnapshot is the snapshot-storage boundary.
func TestProjectFreshStartKeepsEverySnapshot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspace")
	assets := map[string][]byte{
		filepath.Join(root, ".git", "HEAD"):                         []byte("ref: refs/heads/main\n"),
		filepath.Join(root, ".git", "worktrees", "topic", "gitdir"): []byte("/tmp/topic/.git\n"),
		filepath.Join(root, ".projmux", "layouts", "team.toml"):     []byte("schemaVersion = 1\n"),
	}
	for path, contents := range assets {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stateStore := sessionstate.NewStore(filepath.Join(home, "state", "projmux", "sessions"))
	saveSwitchProjectStartupSnapshot(t, stateStore, "workspace")
	snapshotPath, err := stateStore.Path("workspace")
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}

	store := freshStartFixtureStore(t)
	project, _ := store.registry.Project("prj-alpha")
	project.Spec.Root = root
	store.dirs[root] = true
	starter := &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}, shell: "/bin/zsh"}
	plan, err := starter.PlanProjectFreshStart(root)
	if err != nil {
		t.Fatal(err)
	}
	plan.SessionName = "workspace"
	if _, err := starter.PruneProjectFreshStart(context.Background(), root, plan); err != nil {
		t.Fatalf("Fresh replacement: %v", err)
	}
	fresh, ok := store.registry.ProjectByRoot(root)
	if !ok || fresh.Metadata.UID == "prj-alpha" {
		t.Fatalf("Fresh Project identity = %+v", fresh)
	}
	for path, want := range assets {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("Fresh changed external asset %s: bytes=%q err=%v", path, got, readErr)
		}
	}
	snapshotAfter, err := os.ReadFile(snapshotPath)
	if err != nil || !bytes.Equal(snapshotAfter, snapshotBefore) {
		t.Fatalf("Fresh changed latest snapshot: bytes=%q err=%v", snapshotAfter, err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Fresh changed root: info=%v err=%v", info, err)
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
		validateProjectOpenRoute: func(context.Context, string) error {
			return nil
		},
	}

	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	if err := cmd.runSidebarOpen([]string{"--path", "/srv/alpha", "--session", "alpha", "--mode", projectStartupValueNew, "--anchor", "%12"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("--mode fresh Registry writes=%d", store.writes)
	}
	if !equalStrings(executor.calls, []string{"authorize:/srv/alpha", "open:alpha"}) {
		t.Fatalf("--mode fresh calls: %v", executor.calls)
	}
}

// TestSidebarAnchorDriftAfterPreflightRefusesBeforeFreshRegistryWrite pins the
// two-observation boundary. The parser preflight may succeed, but authority can
// still drift while the trust prompt is open; the post-trust guard must fail
// before Fresh opens even one Registry transaction.
func TestSidebarAnchorDriftAfterPreflightRefusesBeforeFreshRegistryWrite(t *testing.T) {
	store := freshStartFixtureStore(t)
	registryBefore, err := json.Marshal(store.registry)
	if err != nil {
		t.Fatal(err)
	}
	registryProjectionBefore := store.snapshot()
	routeChecks := 0
	runner := &recordingTmuxRunner{}
	cmd := &switchCommand{
		sessions:   &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true},
		tmuxRunner: runner,
		executable: func() (string, error) { return "/usr/bin/projmux", nil },
		projectFreshStart: &registryProjectFreshStarter{
			resources: store.store(), runner: &projectionMissingSessionRunner{},
		},
		validateProjectOpenRoute: func(context.Context, string) error {
			routeChecks++
			if routeChecks == 1 {
				return nil
			}
			return errors.New("injected Pane/Project ownership drift")
		},
	}

	err = cmd.runSidebarOpen([]string{
		"--path", "/srv/alpha", "--session", "alpha", "--mode", projectStartupValueNew, "--anchor", "%12",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "injected Pane/Project ownership drift") {
		t.Fatalf("post-trust anchor drift error = %v", err)
	}
	if routeChecks != 2 {
		t.Fatalf("route checks = %d, want parser preflight and post-trust guard", routeChecks)
	}
	registryAfter, marshalErr := json.Marshal(store.registry)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if store.transactions != 0 || store.writes != 0 || !bytes.Equal(registryAfter, registryBefore) || store.snapshot() != registryProjectionBefore {
		t.Fatalf("post-trust drift reached Registry state: transactions=%d writes=%d bytesChanged=%t projectionChanged=%t",
			store.transactions, store.writes, !bytes.Equal(registryAfter, registryBefore), store.snapshot() != registryProjectionBefore)
	}
	for _, call := range runner.calls {
		argv := tmuxCommandArgv(call.args)
		for _, write := range []string{"set-option", "set-environment", "new-session", "new-window", "split-window", "kill-pane", "kill-window", "kill-session", "switch-client"} {
			if slices.Contains(argv, write) {
				t.Fatalf("post-trust drift reached tmux mutation %q: %#v", write, runner.calls)
			}
		}
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
