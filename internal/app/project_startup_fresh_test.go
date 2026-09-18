package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// recordingProjectStartupReporter captures exactly what the operator is told,
// without a tmux server.
type recordingProjectStartupReporter struct {
	messages []string
}

func (r *recordingProjectStartupReporter) Report(text projectStartupText) {
	r.messages = append(r.messages, text(i18n.FallbackLocale))
}

// projectStartupLiteral is a startup message with no catalog text: every locale
// renders the same bytes.
func projectStartupLiteral(message string) projectStartupText {
	return func(i18n.Locale) string { return message }
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

func (r orderedFreshReporter) Report(projectStartupText) { *r.calls = append(*r.calls, "notice") }

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
			wantDescription: "keep this Project identity; open the Windows, shell Panes, and Agents the Registry declares, or create a new Window and shell when none remain",
			wantValue:       "continue",
		},
		{
			name:            "open fresh",
			candidate:       newProjectStartupCandidate(),
			wantName:        "Clear layout and open",
			wantDescription: "clear the saved Window and Agent layout and open again; the folder, its files, .projmux/config.toml, and trust stay",
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
	if got, want := []string{candidates[0].Label, candidates[1].Label}, []string{"이어서 열기", "구성 비우고 새로 열기"}; !slices.Equal(got, want) {
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
	for index, want := range []string{"이어서 열기", "구성 비우고 새로 열기"} {
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

// TestContinueUnregisteredRootIsZeroWriteRecreateRefusalWithoutHandoff pins the
// unregistered-root Continue cell at the seam: a typed state-table refusal that
// names Clear layout and open, no Registry transaction, and no topology
// materialization or runtime open afterwards.
func TestContinueUnregisteredRootIsZeroWriteRecreateRefusalWithoutHandoff(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	root := "/srv/continued"
	store.dirs[root] = true
	before := store.snapshot()
	starter := &registryProjectFreshStarter{resources: store.store(), shell: "/bin/zsh"}

	_, err := starter.ContinueProject(context.Background(), root, "continued")
	want := "continue project unavailable: " + root + " is not a registered Project; choose Clear layout and open"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Continue error = %v, want %q", err, want)
	}
	var staged projectLifecycleStageError
	if !errors.As(err, &staged) || staged.action != coremetadata.ProjectLifecycleContinue || staged.stage != "state-table" {
		t.Fatalf("Continue error = %#v, want typed state-table Continue refusal", err)
	}
	for _, want := range []string{"action=continue", "stage=state-table", "old_uid=-", "new_uid=-"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Continue error = %q, want %q", err, want)
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "snapshot") {
		t.Fatalf("Continue error = %q, want no snapshot wording", err)
	}
	if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
		t.Fatalf("unregistered Continue changed Registry: transactions=%d writes=%d", store.transactions, store.writes)
	}

	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	cmd := &switchCommand{
		sessions: executor, projectFreshStart: starter, projectTopology: topology,
		homeDir: func() (string, error) { return "/home/test", nil }, lookupEnv: func(string) string { return "" },
	}
	if err := cmd.authorizeAndContinueProjectOpen(context.Background(), root, "continued", projectStartupCandidate{Kind: projectStartupKindTopology}); err == nil {
		t.Fatal("authorizeAndContinueProjectOpen() succeeded for an unregistered root")
	}
	if len(topology.calls) != 0 || slices.Contains(executor.calls, "open:continued") {
		t.Fatalf("refused Continue reached handoff: topology=%v runtime=%v", topology.calls, executor.calls)
	}
	if store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
		t.Fatalf("refused Continue handoff changed Registry: transactions=%d writes=%d", store.transactions, store.writes)
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
		Scope: "prj-beta", Kind: coremetadata.KindPane, Name: agentPane.Metadata.Name, UID: agentPane.Metadata.UID,
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
		coremetadata.NameReservation{Scope: "ctl-home", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-ctl-home"},
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
// flow: a Registry with the shared resource fixture, a legacy snapshot file an
// older release saved (which Recreate must leave byte-identical), and recorders
// for tmux calls and operator reports.
func freshStartSwitchFixture(t *testing.T, steps []pickerStep) (
	*switchCommand, *fakeResourceStore, *capturingSwitchSessionExecutor,
	*recordingTmuxRunner, *recordingProjectStartupReporter, string,
) {
	t.Helper()
	home := t.TempDir()
	snapshotPath := writeLegacyProjectSnapshotFile(t, filepath.Join(home, "state", "projmux", "sessions"), "alpha", "/tmp/workspace")

	store := freshStartFixtureStore(t)
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	tmux := &recordingTmuxRunner{}
	reporter := &recordingProjectStartupReporter{}
	_, native := scriptedPicker(t, steps)
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
		nativePicker:      native,
		projectTopology:   &fakeProjectTopologyMaterializer{},
		projectFreshStart: &registryProjectFreshStarter{resources: store.store(), runner: &projectionMissingSessionRunner{}},
		projectRegistrar: &defaultSwitchProjectRegistrar{
			store: store.store(), shell: "/bin/zsh", sessionNameFor: func(string) string { return "alpha" },
		},
		startupNotices: reporter,
	}
	return cmd, store, executor, tmux, reporter, snapshotPath
}

// TestSwitchProjectStartupOpenFreshPreservesSnapshotBytesAndCanonicalIdentity
// covers the full successful fresh action.
func TestSwitchProjectStartupOpenFreshPreservesSnapshotBytesAndCanonicalIdentity(t *testing.T) {
	cmd, store, executor, tmux, reporter, snapshotPath := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupRecreateConfirmValue}},
	})
	topology := cmd.projectTopology.(*fakeProjectTopologyMaterializer)
	topology.materialized = true
	oldProject, _ := store.registry.ProjectByRoot("/srv/alpha")
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
	if _, err := os.Stat(snapshotPath); err != nil {
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
	cmd, store, executor, _, reporter, snapshotPath := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupRecreateConfirmValue}},
	})
	server := newFakeTmux()
	live := server.addSession("alpha-under-another-name")
	live.opts["@projmux_project_uid"] = "prj-alpha"
	live.opts["@projmux_project_path"] = "/srv/alpha"
	routed := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00projmux": server}}
	cmd.projectFreshStart.(*registryProjectFreshStarter).runner = routed

	registryBefore := store.snapshot()
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

// TestSwitchProjectStartupRecreateConfirmsBeforeReplacingIdentity is the
// authorization contract of the one startup row that replaces a Project.
//
// Both halves are asserted from the same fixture on purpose. Confirming has to
// produce exactly the write, notice, and runtime handoff the row always
// produced -- a confirmation that changed the outcome would be a second
// behavior, not a gate. Declining has to leave the Registry write count at zero,
// which is what "confirmation before mutation" means operationally.
func TestSwitchProjectStartupRecreateConfirmsBeforeReplacingIdentity(t *testing.T) {
	var confirmation intpickercompat.Options
	cmd, store, executor, tmux, reporter, snapshotPath := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{observe: func(o intpickercompat.Options) { confirmation = o },
			reply: intpickercompat.Result{Key: "enter", Value: projectStartupRecreateConfirmValue}},
	})
	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	if err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if confirmation.UI != "project-recreate-confirm" || len(confirmation.Entries) != 2 {
		t.Fatalf("Recreate confirmation surface = %+v", confirmation)
	}
	if confirmation.Entries[0].Value != "" || confirmation.Entries[1].Value != projectStartupRecreateConfirmValue {
		t.Fatalf("Recreate confirmation rows = %+v", confirmation.Entries)
	}
	if store.writes != 1 {
		t.Fatalf("confirmed Recreate writes=%d, want 1", store.writes)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("Recreate changed the latest snapshot: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("Recreate issued unexpected tmux commands: %#v", tmux.calls)
	}
	if !equalStrings(executor.calls, []string{"authorize:/srv/alpha", "open:alpha"}) {
		t.Fatalf("Recreate runtime calls: %q", executor.calls)
	}
	if len(reporter.messages) != 1 {
		t.Fatalf("Recreate notice = %q", reporter.messages)
	}
}

// TestSwitchProjectStartupRecreateDeclineWritesNothing is the other half: a
// declined confirmation returns to the startup rows with the Registry, the
// snapshot, and the runtime untouched.
func TestSwitchProjectStartupRecreateDeclineWritesNothing(t *testing.T) {
	cmd, store, executor, tmux, reporter, snapshotPath := freshStartSwitchFixture(t, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueNew}},
		{reply: intpickercompat.Result{Key: "enter", Value: ""}},
	})
	cmd.projectTopology.(*fakeProjectTopologyMaterializer).materialized = true
	err := cmd.openProjectTarget(context.Background(), "/srv/alpha", "alpha")
	if !errors.Is(err, errProjectStartupBack) {
		t.Fatalf("declined Recreate error = %v, want the startup back sentinel", err)
	}
	if store.writes != 0 {
		t.Fatalf("declined Recreate writes=%d, want 0", store.writes)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("declined Recreate changed the latest snapshot: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("declined Recreate issued tmux commands: %#v", tmux.calls)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("declined Recreate runtime calls: %q", executor.calls)
	}
	if len(reporter.messages) != 0 {
		t.Fatalf("declined Recreate emitted notices: %q", reporter.messages)
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
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupRecreateConfirmValue}},
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
	snapshotPath := writeLegacyProjectSnapshotFile(t, filepath.Join(home, "state", "projmux", "sessions"), "workspace", "/tmp/workspace")
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
				if len(got) > projectStartupNoticeMax {
					t.Fatalf("truncated message is %d bytes, want at most %d", len(got), projectStartupNoticeMax)
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

	sink.Report(projectStartupLiteral("projmux: started alpha fresh"))
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
	off.Report(projectStartupLiteral("projmux: started alpha fresh"))
	if len(offRunner.calls) != 0 {
		t.Fatalf("a clientless process emitted display-message: %#v", offRunner.calls)
	}
	if !strings.Contains(offMirror.String(), "projmux: started alpha fresh") {
		t.Fatalf("a clientless process lost the durable record: %q", offMirror.String())
	}
}

// TestNewSwitchCommandWiresFreshStartAndReportSurface keeps the production wiring
// honest. A nil prune seam would turn `new` into a silent alias of the topology
// start, a nil report surface would put the result back where nobody reads it,
// and a nil origin lookup would leave the saved launch default with no Pane to
// open on. The launch default route itself is wired by the application graph,
// not here: it belongs to the AI command that owns the saved mode file.
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
	if cmd.freshOriginShellPane == nil {
		t.Fatal("switcher.freshOriginShellPane = nil, want the Registry origin Pane lookup")
	}
	if cmd.launchChoose != nil || cmd.launchApply != nil {
		t.Fatal("switcher.launchChoose/launchApply are wired by the application graph, not the constructor")
	}
	if app := New(); app.switcher.launchChoose == nil || app.switcher.launchApply == nil {
		t.Fatal("the application graph left the fresh open with no saved launch default route")
	}
}

// projectionMissingSessionRunner answers every has-session probe with "no such
// session" and records every call, so a Fresh replacement sees a closed
// Project without touching a real tmux server.
type projectionMissingSessionRunner struct{ calls [][]string }

func (r *projectionMissingSessionRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	command := args
	if len(command) >= 2 && (command[0] == "-L" || command[0] == "-S") {
		command = command[2:]
	}
	if len(command) > 0 && command[0] == "has-session" {
		return nil, exec.Command("sh", "-c", "exit 1").Run()
	}
	return nil, nil
}

// The fresh open's launch default: a Project opened through the UI follows the
// saved mode on the first Pane of the Window it commits, exactly as a UI Window
// create does -- asked before anything is pruned, filled in before the client
// handoff. Everything below is about that one caller: when it asks at all,
// what the Registry looks like while it asks, which Pane and which client the
// answer is filled into, and what a failure costs.

const (
	// freshLaunchDefaultClient is the exact client that pressed the row. The
	// sidebar continuation is re-executed with it in the environment, which is
	// the only evidence this flow accepts that a human asked for the open.
	freshLaunchDefaultClient = "/dev/pts/9"
	freshLaunchDefaultOrigin = "%31"
	freshLaunchDefaultRoot   = "/srv/fresh"
	// freshLaunchPressedPane is the Pane the row was pressed in -- the sidebar
	// continuation's exact anchor, and the Pane the question is asked on.
	freshLaunchPressedPane = "%7"
)

// freshAskStarter is the production Registry fresh starter with its prune
// recorded on the open's timeline.
type freshAskStarter struct {
	*registryProjectFreshStarter
	events *[]string
}

func (s freshAskStarter) PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) (projectFreshStartCommit, error) {
	*s.events = append(*s.events, "prune")
	return s.registryProjectFreshStarter.PruneProjectFreshStart(ctx, root, plan)
}

// freshAskRunner is the app-socket tmux of a fresh open: the exact handoff and
// the one bounded line are recorded on the open's timeline, and displayErr
// stands in for a client that could not be shown the line.
type freshAskRunner struct {
	events     *[]string
	lines      []string
	displayErr error
}

func (r *freshAskRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	command := args
	if len(command) >= 2 && (command[0] == "-L" || command[0] == "-S") {
		command = command[2:]
	}
	switch {
	case len(command) > 0 && command[0] == "switch-client":
		*r.events = append(*r.events, "open")
	case len(command) > 0 && command[0] == "display-message":
		*r.events = append(*r.events, "line")
		r.lines = append(r.lines, command[len(command)-1])
		if len(command) < 3 || command[1] != "-c" || command[2] != freshLaunchDefaultClient {
			return nil, fmt.Errorf("display-message %v is not addressed at the pressing client", command)
		}
		return nil, r.displayErr
	}
	return nil, nil
}

// freshAskObservation is what the Registry and the client looked like when the
// question was asked.
type freshAskObservation struct {
	anchor, client string
	// projectUID is the Project the opened root declared at that moment, or
	// empty for a root the Registry had never heard of.
	projectUID string
	writes     int
	handedOff  bool
}

// freshAskOpen is one fresh open over the shared resource fixture, with the
// question, the fill, the prune, the materialization, the report and the
// handoff all recorded on one timeline.
type freshAskOpen struct {
	cmd      *switchCommand
	store    *fakeResourceStore
	runner   *freshAskRunner
	root     string
	oldUID   string
	events   []string
	asks     []freshAskObservation
	fills    [][2]string
	filled   []launchChoice
	choice   launchChoice
	applied  launchDefaultResult
	origin   error
	env      map[string]string
	sessions *capturingSwitchSessionExecutor
}

// newFreshAskOpen builds the open. registered picks between the fixture's
// closed registered Project at /srv/alpha and a root that no Project declares;
// both are fresh opens, and both must ask the same way.
func newFreshAskOpen(t *testing.T, registered bool, env map[string]string) *freshAskOpen {
	t.Helper()
	open := &freshAskOpen{store: freshStartFixtureStore(t), root: "/srv/unregistered-fresh", env: env}
	// Registering a root requires an existing directory.
	open.store.dirs[open.root] = true
	if registered {
		open.root, open.oldUID = "/srv/alpha", "prj-alpha"
	}
	if _, ok := open.store.registry.ProjectByRoot(open.root); ok != registered {
		t.Fatalf("fixture root %s registered = %v, want %v", open.root, ok, registered)
	}
	open.runner = &freshAskRunner{events: &open.events}
	open.sessions = &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	open.cmd = &switchCommand{
		sessions:   open.sessions,
		tmuxRunner: open.runner,
		lookupEnv:  func(name string) string { return open.env[name] },
		projectFreshStart: freshAskStarter{
			registryProjectFreshStarter: &registryProjectFreshStarter{
				resources: open.store.store(), runner: &projectionMissingSessionRunner{}, shell: "/bin/zsh",
			},
			events: &open.events,
		},
		projectTopology: orderedFreshTopology{calls: &open.events},
		startupNotices:  orderedFreshReporter{calls: &open.events},
		launchChoose: func(anchor, client string) launchChoice {
			open.events = append(open.events, "ask")
			observed := freshAskObservation{anchor: anchor, client: client, writes: open.store.writes,
				handedOff: slices.Contains(open.events, "open")}
			if project, ok := open.store.registry.ProjectByRoot(open.root); ok {
				observed.projectUID = project.Metadata.UID
			}
			open.asks = append(open.asks, observed)
			return open.choice
		},
		launchApply: func(originPaneID, client string, choice launchChoice) launchDefaultResult {
			open.events = append(open.events, "fill")
			open.fills = append(open.fills, [2]string{originPaneID, client})
			open.filled = append(open.filled, choice)
			return open.applied
		},
		freshOriginShellPane: func(_ context.Context, root string) (string, error) {
			if root != open.root {
				return "", fmt.Errorf("the fresh open resolved its origin Pane for %q", root)
			}
			if open.origin != nil {
				return "", open.origin
			}
			return freshLaunchDefaultOrigin, nil
		},
	}
	return open
}

func (o *freshAskOpen) start(t *testing.T, anchor string) {
	t.Helper()
	if err := o.cmd.startProjectFresh(context.Background(), "fresh-session", o.root, openedProjectBootstrap{}, anchor); err != nil {
		t.Fatalf("startProjectFresh() error = %v, want the open to succeed", err)
	}
}

// requireFreshCommitted checks the open itself happened: exactly one Registry
// commit, a Project at the root whose identity is not the old one.
func (o *freshAskOpen) requireFreshCommitted(t *testing.T) {
	t.Helper()
	if o.store.writes != 1 {
		t.Fatalf("Registry writes = %d, want the one fresh commit", o.store.writes)
	}
	project, ok := o.store.registry.ProjectByRoot(o.root)
	if !ok || project.Metadata.UID == "" || project.Metadata.UID == o.oldUID {
		t.Fatalf("Project at %s after the open = %+v (ok=%v), want a new identity replacing %q", o.root, project.Metadata, ok, o.oldUID)
	}
}

func freshSidebarEnv() map[string]string {
	return map[string]string{inttmux.SwitchTargetClientEnv: freshLaunchDefaultClient}
}

// TestFreshOpenAsksBeforeItPrunesAndFillsBeforeItHandsOff is the condition
// table of the fresh open's order, run for both kinds of fresh open -- a
// registered closed Project after its confirmation, and a root no Project
// declares -- because both reach the same route and must behave the same.
//
// Every row asks first, on the pressed Pane with the pressing client, while the
// old Project is still exactly as it was and nobody has been moved. An Agent
// answer is filled into the committed shell Pane before the handoff; anything
// that did not happen is one line after it, and the open itself never fails.
func TestFreshOpenAsksBeforeItPrunesAndFillsBeforeItHandsOff(t *testing.T) {
	t.Parallel()

	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}}
	shell := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"}}
	var (
		opened   = []string{"ask", "prune", "materialize", "notice", "open"}
		filled   = []string{"ask", "prune", "materialize", "fill", "notice", "open"}
		saidOnce = func(events []string) []string { return append(slices.Clone(events), "line") }
	)
	for _, registered := range []bool{true, false} {
		kind := "unregistered root"
		if registered {
			kind = "registered closed Project"
		}
		for _, test := range []struct {
			name       string
			choice     launchChoice
			applied    launchDefaultResult
			origin     error
			displayErr error
			wantEvents []string
			wantFill   bool
			wantLine   string
		}{
			{name: "an Agent answer fills the shell Pane before the handoff", choice: claude,
				wantEvents: filled, wantFill: true},
			{name: "a committed Agent's start notice is not repeated after the handoff", choice: claude,
				applied: launchDefaultResult{notice: "started in /srv/fresh"}, wantEvents: filled, wantFill: true},
			{name: "a shell answer keeps the committed Pane and says nothing", choice: shell, wantEvents: opened},
			// Owner ruling 15 (user decision 4, being re-confirmed): a cancelled
			// picker does not stop the open. This is that ruling's one row.
			{name: "a cancelled picker opens with the shell and says nothing", choice: launchChoice{cancelled: true},
				wantEvents: opened},
			{name: "a question that could not be asked opens with the shell and says so once",
				choice:     launchChoice{problem: "could not open the launch picker: injected"},
				wantEvents: saidOnce(opened), wantLine: "could not open the launch picker: injected; the Window keeps its shell Pane"},
			{name: "a refused fill keeps the Session and says so once", choice: claude,
				applied:    launchDefaultResult{problem: keptOriginShellLine("projmux could not open the Agent: injected")},
				wantEvents: saidOnce(filled), wantFill: true,
				wantLine: "projmux could not open the Agent: injected; the Window keeps its shell Pane"},
			{name: "an unresolvable shell Pane keeps the Session and says so once", choice: claude,
				origin:     errors.New("the fresh Window declares 2 shell Panes and 0 Agents"),
				wantEvents: saidOnce(opened),
				wantLine:   "the fresh Window declares 2 shell Panes and 0 Agents; the Window keeps its shell Pane"},
			{name: "a line that cannot be shown does not fail the open", choice: claude,
				applied:    launchDefaultResult{problem: "injected fill refusal"},
				displayErr: errors.New("injected display failure"),
				wantEvents: saidOnce(filled), wantFill: true, wantLine: "injected fill refusal"},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				open := newFreshAskOpen(t, registered, freshSidebarEnv())
				open.choice, open.applied, open.origin = test.choice, test.applied, test.origin
				open.runner.displayErr = test.displayErr

				open.start(t, freshLaunchPressedPane)

				want := []freshAskObservation{{
					anchor: freshLaunchPressedPane, client: freshLaunchDefaultClient, projectUID: open.oldUID,
				}}
				if !reflect.DeepEqual(open.asks, want) {
					t.Fatalf("asked %+v, want once on the pressed Pane and pressing client before any write or handoff %+v",
						open.asks, want)
				}
				if !slices.Equal(open.events, test.wantEvents) {
					t.Fatalf("open order = %v, want %v", open.events, test.wantEvents)
				}
				open.requireFreshCommitted(t)
				var wantFills [][2]string
				var wantFilled []launchChoice
				if test.wantFill {
					wantFills = [][2]string{{freshLaunchDefaultOrigin, freshLaunchDefaultClient}}
					wantFilled = []launchChoice{test.choice}
				}
				if !reflect.DeepEqual(open.fills, wantFills) || !reflect.DeepEqual(open.filled, wantFilled) {
					t.Fatalf("filled %v with %+v, want %v with %+v", open.fills, open.filled, wantFills, wantFilled)
				}
				var wantLines []string
				if test.wantLine != "" {
					wantLines = []string{test.wantLine}
				}
				if !slices.Equal(open.runner.lines, wantLines) {
					t.Fatalf("client lines = %q, want %q", open.runner.lines, wantLines)
				}
				for _, line := range open.runner.lines {
					if strings.Contains(line, "no Window was created") {
						t.Fatalf("line %q claims no Window was created; the fresh open proceeded", line)
					}
				}
				if open.sessions.killSessionName != "" {
					t.Fatalf("the launch default killed session %q", open.sessions.killSessionName)
				}
			})
		}
	}
}

// TestFreshOpenWithoutAnExactPressingClientAsksNothing is the product decision,
// not an oversight: the launch default attaches only to a gesture a human made,
// and without the exact client that made it there is nowhere to put a
// question, an Agent, or the line either would report. Every saved mode is
// driven through the real aiCommand, so the mode really is saved and really
// does nothing: no question, no popup, no canonical create, no line.
func TestFreshOpenWithoutAnExactPressingClientAsksNothing(t *testing.T) {
	for _, mode := range []string{aiModeClaude, aiModeCodex, aiModeAntigravity, aiModeShell, aiModeSelective, aiModeResume, ""} {
		name := mode
		if name == "" {
			name = "unset"
		}
		for _, registered := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/registered=%v", name, registered), func(t *testing.T) {
				ai, recorder := launchDefaultAICommand(t, t.TempDir())
				if mode != "" {
					if err := ai.setMode(mode); err != nil {
						t.Fatalf("setMode(%s) error = %v", mode, err)
					}
				}
				cmdRecorder(ai).commands = nil
				// The anchor is there; only the client is missing.
				open := newFreshAskOpen(t, registered, map[string]string{"TMUX_PANE": freshLaunchPressedPane})
				open.cmd.launchChoose = func(anchor, client string) launchChoice {
					open.events = append(open.events, "ask")
					return ai.chooseLaunchDefault(anchor, client)
				}
				open.cmd.launchApply = func(originPaneID, client string, choice launchChoice) launchDefaultResult {
					open.events = append(open.events, "fill")
					return ai.applyLaunchChoice(originPaneID, client, choice)
				}

				open.start(t, freshLaunchPressedPane)

				if want := []string{"prune", "materialize", "notice"}; !slices.Equal(open.events, want) {
					t.Fatalf("open order = %v, want %v with no question and no fill", open.events, want)
				}
				if !slices.Equal(open.sessions.calls, []string{"open:fresh-session"}) {
					t.Fatalf("session executor = %v, want the ordinary handoff", open.sessions.calls)
				}
				open.requireFreshCommitted(t)
				if len(recorder.intents) != 0 || len(recorder.deleted) != 0 {
					t.Fatalf("a clientless open reached the canonical routes: intents=%+v deleted=%v",
						recorder.intents, recorder.deleted)
				}
				if len(cmdRecorder(ai).commands) != 0 {
					t.Fatalf("a clientless open ran %#v, want no popup", cmdRecorder(ai).commands)
				}
				if len(open.runner.lines) != 0 {
					t.Fatalf("client lines = %q, want none", open.runner.lines)
				}
			})
		}
	}
}

// TestFreshOpenFromTheInProcessPickerNeverWaitsOnAQuestionItCannotShow covers
// the Project Picker popup route (`switch --ui=popup`), which opens in process:
// no explicit anchor, the pressed Pane in the private producer anchor, the
// pressing client in the environment. It is outside the ask-first guarantee --
// its own popup is still up, so the question may come back empty or fail --
// but the open must still proceed with a shell first Pane: no failure, no fill.
func TestFreshOpenFromTheInProcessPickerNeverWaitsOnAQuestionItCannotShow(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		choice   launchChoice
		wantLine string
	}{
		{name: "the popup could not show, so the answer is empty", choice: launchChoice{cancelled: true}},
		{name: "the question failed", choice: launchChoice{problem: "could not open the launch picker: a popup is already open"},
			wantLine: "could not open the launch picker: a popup is already open; the Window keeps its shell Pane"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			env := freshSidebarEnv()
			env[runtimeMutationAnchorPaneEnv] = freshLaunchPressedPane
			env["TMUX_PANE"] = "%99"
			open := newFreshAskOpen(t, false, env)
			open.choice = test.choice

			open.start(t, "")

			if len(open.asks) != 1 || open.asks[0].anchor != freshLaunchPressedPane || open.asks[0].client != freshLaunchDefaultClient {
				t.Fatalf("asked %+v, want once on the private producer anchor %s", open.asks, freshLaunchPressedPane)
			}
			if !slices.Contains(open.events, "open") {
				t.Fatalf("open order = %v, want the Session handed to the client", open.events)
			}
			open.requireFreshCommitted(t)
			if len(open.fills) != 0 {
				t.Fatalf("filled %v, want the shell the open committed", open.fills)
			}
			var wantLines []string
			if test.wantLine != "" {
				wantLines = []string{test.wantLine}
			}
			if !slices.Equal(open.runner.lines, wantLines) {
				t.Fatalf("client lines = %q, want %q", open.runner.lines, wantLines)
			}
		})
	}
}

// TestContinueAndDetachedProjectOpensNeverReachTheSavedLaunchDefault is the
// boundary this caller sits behind. Reopening an existing Project restores the
// Windows and Panes it already declares -- replacing one of them with an Agent
// would be destroying stored topology -- and the `start project` verb is
// detached by construction: it never hands a client anywhere. Both are driven
// with `claude` saved as the launch default and an exact client present, so the
// only thing keeping them out is the caller's own gate.
func TestContinueAndDetachedProjectOpensNeverReachTheSavedLaunchDefault(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     string
		detached bool
	}{
		{name: "continue reopens a registered Project", mode: projectStartupKindTopology},
		{name: "detached start project", mode: projectStartupKindTopology, detached: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ai, recorder := launchDefaultAICommand(t, t.TempDir())
			if err := ai.setMode(aiModeClaude); err != nil {
				t.Fatalf("setMode(claude) error = %v", err)
			}
			open := newFreshAskOpen(t, true, freshSidebarEnv())
			cmd := open.cmd
			cmd.projectFreshStart = &startupModeFreshStarter{registered: true}
			cmd.projectRegistrar = &fakeProjectRegistrar{uid: "proj-existing", name: "workspace", reused: true}
			topology := &fakeProjectTopologyMaterializer{materialized: true}
			cmd.projectTopology = topology
			wireFakeProjectSessionPlan(cmd)
			failOnLaunchDefault(t, cmd, ai, "a Continue open")

			if err := cmd.authorizeAndContinueProjectOpenRequest(context.Background(), projectOpenRequest{
				Target: freshLaunchDefaultRoot, SessionName: "workspace",
				Mode: projectStartupCandidate{Kind: test.mode}, Detached: test.detached,
			}); err != nil {
				t.Fatalf("authorizeAndContinueProjectOpenRequest() error = %v", err)
			}
			if len(recorder.intents) != 0 {
				t.Fatalf("a Continue open opened an Agent: %+v", recorder.intents)
			}
			if want := []string{"topology:" + freshLaunchDefaultRoot + ":workspace"}; !slices.Equal(topology.calls, want) {
				t.Fatalf("topology calls = %q, want %q: the open under test must have happened", topology.calls, want)
			}
		})
	}
}

// failOnLaunchDefault wires a switcher whose saved launch default fails the
// test if any half of it is reached.
func failOnLaunchDefault(t *testing.T, cmd *switchCommand, ai *aiCommand, route string) {
	t.Helper()
	cmd.launchChoose = func(anchor, client string) launchChoice {
		t.Errorf("%s asked for the launch default on %q for client %q", route, anchor, client)
		return ai.chooseLaunchDefault(anchor, client)
	}
	cmd.launchApply = func(originPaneID, client string, choice launchChoice) launchDefaultResult {
		t.Errorf("%s filled the launch default into %q on client %q", route, originPaneID, client)
		return ai.applyLaunchChoice(originPaneID, client, choice)
	}
	cmd.freshOriginShellPane = func(_ context.Context, root string) (string, error) {
		t.Errorf("%s resolved a launch-default origin Pane for %q", route, root)
		return "", nil
	}
}

// TestFreshOpenOriginShellPaneIsReadFromTheRegistry pins the origin the launch
// default replaces. tmux Pane order is not consulted: the Registry names the
// Window's own shell Pane and the exact `%N` it was materialized as, and every
// shape a fresh open does not produce is a refusal rather than a guess -- the
// guess would be a Pane somebody is working in.
func TestFreshOpenOriginShellPaneIsReadFromTheRegistry(t *testing.T) {
	t.Parallel()

	freshRegistry := func(mutate func(*coremetadata.Registry)) coremetadata.Registry {
		registry := coremetadata.Registry{
			Projects: []coremetadata.Project{{
				Metadata: coremetadata.ObjectMeta{UID: "prj-fresh", Name: "fresh"},
				Spec:     coremetadata.ProjectSpec{Root: freshLaunchDefaultRoot, PrimaryWindowRef: "win-fresh"},
			}},
			Windows: []coremetadata.Window{{
				Metadata: coremetadata.ObjectMeta{UID: "win-fresh", Name: "fresh",
					OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-fresh"}},
				Spec: coremetadata.WindowSpec{AnchorPaneRef: "pane-fresh", DefaultShellPaneRef: "pane-fresh"},
			}},
			Panes: []coremetadata.Pane{{
				Metadata: coremetadata.ObjectMeta{UID: "pane-fresh", Name: "shell",
					OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-fresh"}},
				Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: freshLaunchDefaultRoot},
			}},
		}
		if mutate != nil {
			mutate(&registry)
		}
		return registry
	}

	for _, test := range []struct {
		name   string
		mutate func(*coremetadata.Registry)
		root   string
		// mirrored is what the live Pane mirror answers for the Registry Pane
		// uid. A fresh Project's first Pane stores no runtime handle of its own,
		// so this lookup is the only thing that can name it.
		mirrored string
		want     string
		wantFail string
	}{
		{name: "the one committed shell Pane", want: freshLaunchDefaultOrigin},
		{
			name: "the anchor when no default shell is declared",
			mutate: func(registry *coremetadata.Registry) {
				registry.Windows[0].Spec.DefaultShellPaneRef = ""
			},
			want: freshLaunchDefaultOrigin,
		},
		{name: "an unregistered root", root: "/srv/unknown", wantFail: "found no Project"},
		{
			name: "a second Window",
			mutate: func(registry *coremetadata.Registry) {
				registry.Windows = append(registry.Windows, registry.Windows[0])
			},
			wantFail: "declares 2 Windows",
		},
		{
			name:     "no Window at all",
			mutate:   func(registry *coremetadata.Registry) { registry.Windows = nil },
			wantFail: "declares 0 Windows",
		},
		{
			name: "a second shell Pane",
			mutate: func(registry *coremetadata.Registry) {
				second := registry.Panes[0]
				second.Metadata.UID = "pane-second"
				registry.Panes = append(registry.Panes, second)
			},
			wantFail: "declares 2 shell Panes",
		},
		{
			name: "an Agent already in the Window",
			mutate: func(registry *coremetadata.Registry) {
				registry.Agents = append(registry.Agents, coremetadata.Agent{
					Metadata: coremetadata.ObjectMeta{UID: "agt-fresh", Name: "agent",
						OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-fresh"}},
					Spec: coremetadata.AgentSpec{Provider: aiModeClaude},
				})
			},
			wantFail: "1 Agents",
		},
		{
			name:     "a shell Pane no live tmux Pane mirrors",
			mirrored: "-",
			wantFail: "no live Pane carries an exact %N",
		},
		{
			name:     "a mirror answer that is not an exact %N",
			mirrored: "shell",
			wantFail: "no live Pane carries an exact %N",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			registry := freshRegistry(test.mutate)
			root := freshLaunchDefaultRoot
			if test.root != "" {
				root = test.root
			}
			// "-" is the row that mirrors nothing at all, spelled so the zero
			// value can stay the ordinary live Pane.
			mirrored := freshLaunchDefaultOrigin
			if test.mirrored != "" {
				mirrored = test.mirrored
			}
			if mirrored == "-" {
				mirrored = ""
			}
			origin, err := freshProjectOriginShellPane(context.Background(),
				func() (coremetadata.Registry, error) { return registry, nil },
				func(_ context.Context, paneUID string) (string, bool, error) {
					if paneUID != "pane-fresh" {
						return "", false, fmt.Errorf("the mirror was asked for Pane %q", paneUID)
					}
					return mirrored, mirrored != "", nil
				}, root)
			if test.wantFail != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantFail) {
					t.Fatalf("origin Pane = %q, error = %v, want a refusal containing %q", origin, err, test.wantFail)
				}
				if origin != "" {
					t.Fatalf("a refused shape still named Pane %q", origin)
				}
				return
			}
			if err != nil {
				t.Fatalf("freshProjectOriginShellPane() error = %v", err)
			}
			if origin != test.want {
				t.Fatalf("origin Pane = %q, want %q", origin, test.want)
			}
		})
	}
}

// TestFreshOpenOriginShellPaneSurfacesAnUnreadableRegistry keeps a Registry
// that cannot be read from being answered with a Pane. There is no fallback to
// guess from here: a read failure means the launch default is not applied, and
// the Window keeps the shell Pane the open committed.
func TestFreshOpenOriginShellPaneSurfacesAnUnreadableRegistry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		snapshot func() (coremetadata.Registry, error)
	}{
		{name: "no snapshot route"},
		{
			name: "an unreadable Registry",
			snapshot: func() (coremetadata.Registry, error) {
				return coremetadata.Registry{}, errors.New("injected registry read failure")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			origin, err := freshProjectOriginShellPane(context.Background(), test.snapshot,
				func(context.Context, string) (string, bool, error) {
					t.Fatal("an unreadable Registry still reached the live Pane mirror")
					return "", false, nil
				}, freshLaunchDefaultRoot)
			if err == nil || !strings.Contains(err.Error(), "could not read the Registry") {
				t.Fatalf("origin Pane = %q, error = %v, want an unreadable-Registry refusal", origin, err)
			}
		})
	}
}

// freshPaneMirrorRunner answers one `list-panes -a` read and records exactly
// what was routed, so the production locator's socket and command are visible
// without a tmux server.
type freshPaneMirrorRunner struct {
	calls [][]string
	rows  string
}

func (r *freshPaneMirrorRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.rows), nil
}

// TestFreshOpenResolvesItsOriginPaneThroughTheAppSocketPaneMirror pins the
// production half of the origin lookup. The fresh Project's first Pane is
// mirrored with its uid but records no runtime handle of its own, so the uid is
// resolved by reading every live Pane's mirrored uid -- on the app's own
// socket, because the sidebar continuation inherits no useful $TMUX.
func TestFreshOpenResolvesItsOriginPaneThroughTheAppSocketPaneMirror(t *testing.T) {
	t.Parallel()

	runner := &freshPaneMirrorRunner{rows: "pane-fresh\\037" + freshLaunchDefaultOrigin + "\n"}
	cmd := &switchCommand{tmuxRunner: runner}

	target, found, err := cmd.liveShellPaneTarget(context.Background(), "pane-fresh")
	if err != nil {
		t.Fatalf("liveShellPaneTarget() error = %v", err)
	}
	if !found || target != freshLaunchDefaultOrigin {
		t.Fatalf("live Pane = %q found = %v, want %q", target, found, freshLaunchDefaultOrigin)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("tmux calls = %v, want the one mirror read", runner.calls)
	}
	call := runner.calls[0]
	if want := []string{"tmux", "-L", "projmux", "list-panes", "-a", "-F"}; !slices.Equal(call[:len(want)], want) {
		t.Fatalf("mirror read = %v, want %v on the app socket", call, want)
	}
}
