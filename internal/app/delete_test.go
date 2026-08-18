package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// newTestDeleteCommand builds a delete route whose confirmation answer is
// scripted rather than read from a terminal.
func newTestDeleteCommand(store *fakeResourceStore, interactive bool, answer bool, prompts *[]string) *deleteCommand {
	return &deleteCommand{
		store: store.store(),
		confirm: &confirmer{
			interactive: func() bool { return interactive },
			ask: func(prompt string, _ io.Writer) (bool, error) {
				if prompts != nil {
					*prompts = append(*prompts, prompt)
				}
				return answer, nil
			},
		},
		resolveKinds: deleteRegistryKinds,
		windows:      newFixtureWindowDeleteRuntime(),
		panes:        newFixturePaneDeleteRuntime(),
	}
}

type fakePaneDeleteRuntime struct {
	preflights   int
	killed       []paneLiveDeleteTarget
	tombstoned   []paneLiveDeleteTarget
	restored     []paneLiveDeleteTarget
	queued       []paneLiveDeleteTarget
	selfUID      string
	preflightErr error
	killErr      error
	killErrs     map[string]error
	tombstoneErr error
	restoreErr   error
	queueErr     error
	killHook     func(paneLiveDeleteTarget)
	queueHook    func([]paneLiveDeleteTarget)
}

func newFixturePaneDeleteRuntime() *fakePaneDeleteRuntime { return &fakePaneDeleteRuntime{} }

func (r *fakePaneDeleteRuntime) preflight(_ context.Context, registry coremetadata.Registry, plan deletePlan) (paneLiveDeletePlan, error) {
	r.preflights++
	if r.preflightErr != nil {
		return paneLiveDeletePlan{}, r.preflightErr
	}
	planned := plannedPaneDeletes(plan)
	selectedByWindow := map[string]int{}
	lastByWindow := map[string]string{}
	windowByPane := map[string]string{}
	projectByWindow := map[string]string{}
	for _, item := range planned {
		_, window, project, err := paneRegistryAncestry(registry, item.paneUID)
		if err != nil {
			return paneLiveDeletePlan{}, err
		}
		windowByPane[item.paneUID] = window.Metadata.UID
		projectByWindow[window.Metadata.UID] = project.Metadata.UID
		selectedByWindow[window.Metadata.UID]++
		lastByWindow[window.Metadata.UID] = item.paneUID
	}
	paneCount := map[string]int{}
	for _, pane := range registry.Panes {
		_, window, _, err := paneRegistryAncestry(registry, pane.Metadata.UID)
		if err == nil {
			paneCount[window.Metadata.UID]++
		}
	}
	windowCount := map[string]int{}
	for _, window := range registry.Windows {
		windowCount[window.Metadata.OwnerUID()]++
	}
	endingWindows := map[string]int{}
	for windowUID, selected := range selectedByWindow {
		if selected == paneCount[windowUID] {
			endingWindows[projectByWindow[windowUID]]++
		}
	}
	lastEndingWindow := map[string]string{}
	for _, item := range planned {
		windowUID := windowByPane[item.paneUID]
		if selectedByWindow[windowUID] == paneCount[windowUID] && lastByWindow[windowUID] == item.paneUID {
			lastEndingWindow[projectByWindow[windowUID]] = windowUID
		}
	}
	out := paneLiveDeletePlan{}
	for _, item := range planned {
		_, window, project, _ := paneRegistryAncestry(registry, item.paneUID)
		paneIndex := 0
		for i := range registry.Panes {
			if registry.Panes[i].Metadata.UID == item.paneUID {
				paneIndex = i
				break
			}
		}
		windowIndex := 0
		for i := range registry.Windows {
			if registry.Windows[i].Metadata.UID == window.Metadata.UID {
				windowIndex = i
				break
			}
		}
		projectIndex := 0
		for i := range registry.Projects {
			if registry.Projects[i].Metadata.UID == project.Metadata.UID {
				projectIndex = i
				break
			}
		}
		sessionName := project.Metadata.Name
		if project.Status.Session != nil {
			sessionName = project.Status.Session.Name
		}
		endsWindow := selectedByWindow[window.Metadata.UID] == paneCount[window.Metadata.UID] && lastByWindow[window.Metadata.UID] == item.paneUID
		out.Targets = append(out.Targets, paneLiveDeleteTarget{
			ResourceUID: item.resourceUID, PaneUID: item.paneUID, PaneID: fmt.Sprintf("%%%d", paneIndex+30),
			WindowUID: window.Metadata.UID, WindowID: fmt.Sprintf("@%d", windowIndex+10),
			SessionID: fmt.Sprintf("$%d", projectIndex+20), SessionName: sessionName, ProjectUID: project.Metadata.UID,
			EndsWindow: endsWindow,
			EndsSession: endsWindow && endingWindows[project.Metadata.UID] == windowCount[project.Metadata.UID] &&
				lastEndingWindow[project.Metadata.UID] == window.Metadata.UID,
			Self: item.paneUID == r.selfUID,
		})
	}
	return out, nil
}

func (r *fakePaneDeleteRuntime) kill(_ context.Context, target paneLiveDeleteTarget) error {
	if r.killHook != nil {
		r.killHook(target)
	}
	if err := r.killErrs[target.PaneID]; err != nil {
		return err
	}
	if r.killErr != nil {
		return r.killErr
	}
	r.killed = append(r.killed, target)
	return nil
}

func (r *fakePaneDeleteRuntime) tombstoneSelfKill(_ context.Context, targets []paneLiveDeleteTarget) error {
	if r.tombstoneErr != nil {
		return r.tombstoneErr
	}
	r.tombstoned = append(r.tombstoned, targets...)
	return nil
}

func (r *fakePaneDeleteRuntime) restoreSelfKill(_ context.Context, targets []paneLiveDeleteTarget) error {
	if r.restoreErr != nil {
		return r.restoreErr
	}
	r.restored = append(r.restored, targets...)
	return nil
}

func (r *fakePaneDeleteRuntime) queueSelfKill(_ context.Context, targets []paneLiveDeleteTarget) error {
	if r.queueHook != nil {
		r.queueHook(targets)
	}
	if r.queueErr != nil {
		return r.queueErr
	}
	r.queued = append(r.queued, targets...)
	return nil
}

type fakeWindowDeleteRuntime struct {
	preflights    int
	killed        []windowLiveDeleteTarget
	queued        []windowLiveDeleteTarget
	selfUID       string
	preflightErr  error
	killErr       error
	killErrs      map[string]error
	queueErr      error
	killHook      func(windowLiveDeleteTarget)
	queueHook     func([]windowLiveDeleteTarget)
	offlineUIDs   map[string]bool
	preflightHook func(int)
}

func newFixtureWindowDeleteRuntime() *fakeWindowDeleteRuntime {
	return &fakeWindowDeleteRuntime{}
}

func (r *fakeWindowDeleteRuntime) preflight(_ context.Context, registry coremetadata.Registry, plan deletePlan) (windowLiveDeletePlan, error) {
	r.preflights++
	if r.preflightHook != nil {
		r.preflightHook(r.preflights)
	}
	if r.preflightErr != nil {
		return windowLiveDeletePlan{}, r.preflightErr
	}
	sessionCounts := map[string]int{}
	for _, window := range registry.Windows {
		if r.offlineUIDs[window.Metadata.UID] {
			continue
		}
		sessionCounts[window.Metadata.OwnerUID()]++
	}
	selectedCounts := map[string]int{}
	lastSelected := map[string]string{}
	for _, target := range plan.Targets {
		if r.offlineUIDs[target.Match.UID] {
			continue
		}
		window, _ := registry.Window(target.Match.UID)
		selectedCounts[window.Metadata.OwnerUID()]++
		lastSelected[window.Metadata.OwnerUID()] = target.Match.UID
	}
	out := windowLiveDeletePlan{}
	for _, target := range plan.Targets {
		if r.offlineUIDs[target.Match.UID] {
			continue
		}
		window, _ := registry.Window(target.Match.UID)
		project, _ := registry.Project(window.Metadata.OwnerUID())
		sessionName := project.Metadata.Name
		if project.Status.Session != nil {
			sessionName = project.Status.Session.Name
		}
		windowIndex := 0
		for i := range registry.Windows {
			if registry.Windows[i].Metadata.UID == target.Match.UID {
				windowIndex = i
				break
			}
		}
		projectIndex := 0
		for i := range registry.Projects {
			if registry.Projects[i].Metadata.UID == window.Metadata.OwnerUID() {
				projectIndex = i
				break
			}
		}
		out.Targets = append(out.Targets, windowLiveDeleteTarget{
			UID: target.Match.UID, WindowID: fmt.Sprintf("@%d", windowIndex+10),
			SessionID: fmt.Sprintf("$%d", projectIndex+20), SessionName: sessionName,
			ProjectUID: window.Metadata.OwnerUID(),
			EndsSession: selectedCounts[window.Metadata.OwnerUID()] == sessionCounts[window.Metadata.OwnerUID()] &&
				target.Match.UID == lastSelected[window.Metadata.OwnerUID()],
			Self: target.Match.UID == r.selfUID,
		})
	}
	return out, nil
}

func TestDeleteOfflineWindowDryRunAndExecutionAreRegistryOnly(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	runtime.offlineUIDs = map[string]bool{"win-alpha-main": true}
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	before := store.snapshot()

	stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--project", "uid:prj-alpha", "--dry-run")
	if err != nil {
		t.Fatalf("offline dry-run error = %v", err)
	}
	for _, want := range []string{
		"delete window: would delete 1 window and 4 descendant resources",
		"cascade agent/codex uid=agt-alpha-codex",
		"cascade pane/codex-pane uid=pan-alpha-codex",
		"cascade pane/zsh uid=pan-alpha-zsh",
		"cascade pane/log uid=pan-alpha-log",
		"registry-only would delete this Window; no tmux Window would be killed on socket=-L/projmux",
		"dry-run: nothing was deleted",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("offline dry-run output missing %q:\n%s", want, stdout)
		}
	}
	if store.transactions != 0 || store.snapshot() != before || len(runtime.killed) != 0 {
		t.Fatalf("offline dry-run changed state: tx=%d killed=%v", store.transactions, runtime.killed)
	}

	stdout, _, err = runRoute(t, cmd, "window", "uid:win-alpha-main", "--project", "uid:prj-alpha", "--yes")
	if err != nil {
		t.Fatalf("offline delete error = %v", err)
	}
	for _, uid := range []string{"win-alpha-main", "agt-alpha-codex", "pan-alpha-codex", "pan-alpha-zsh", "pan-alpha-log"} {
		if registryUIDs(store.registry)[uid] {
			t.Fatalf("offline delete left %q behind", uid)
		}
	}
	for _, uid := range []string{"prj-alpha", "win-alpha-review", "pan-alpha-review", "prj-beta", "win-beta-main"} {
		if !registryUIDs(store.registry)[uid] {
			t.Fatalf("offline delete removed sibling/owner %q", uid)
		}
	}
	if len(runtime.killed) != 0 || !strings.Contains(stdout, "registry-only deleted this Window; no tmux Window was killed") {
		t.Fatalf("offline delete used tmux or omitted result: killed=%v stdout=%q", runtime.killed, stdout)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("offline delete left invalid registry: %v", err)
	}

	beforeRepeat := store.snapshot()
	stdout, _, err = runRoute(t, cmd, "window", "uid:win-alpha-main", "--project", "uid:prj-alpha", "--yes")
	if err == nil || !strings.Contains(err.Error(), "matched no windows") {
		t.Fatalf("repeat offline delete error = %v", err)
	}
	if stdout != "" || store.snapshot() != beforeRepeat || len(runtime.killed) != 0 {
		t.Fatalf("repeat offline delete changed state: stdout=%q killed=%v", stdout, runtime.killed)
	}
}

func TestDeleteOfflineWindowBindingRaceFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name           string
		initialOffline bool
		change         func(map[string]bool)
	}{
		{
			name:           "offline mirror appears before locked execution",
			initialOffline: true,
			change:         func(offline map[string]bool) { delete(offline, "win-alpha-main") },
		},
		{
			name:           "live mirror disappears before locked execution",
			initialOffline: false,
			change:         func(offline map[string]bool) { offline["win-alpha-main"] = true },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			runtime := newFixtureWindowDeleteRuntime()
			runtime.offlineUIDs = map[string]bool{"win-alpha-main": test.initialOffline}
			runtime.preflightHook = func(call int) {
				if call == 2 {
					test.change(runtime.offlineUIDs)
				}
			}
			cmd := newTestDeleteCommand(store, false, false, nil)
			cmd.windows = runtime
			before := store.snapshot()

			stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
			if err == nil || !strings.Contains(err.Error(), "exact live cascade changed") {
				t.Fatalf("binding race error = %v", err)
			}
			if stdout != "" || store.writes != 0 || store.snapshot() != before || len(runtime.killed) != 0 {
				t.Fatalf("binding race changed state: stdout=%q writes=%d killed=%v", stdout, store.writes, runtime.killed)
			}
		})
	}
}

func (r *fakeWindowDeleteRuntime) kill(_ context.Context, target windowLiveDeleteTarget) error {
	if r.killHook != nil {
		r.killHook(target)
	}
	if err := r.killErrs[target.WindowID]; err != nil {
		return err
	}
	if r.killErr != nil {
		return r.killErr
	}
	r.killed = append(r.killed, target)
	return nil
}

func (r *fakeWindowDeleteRuntime) queueSelfKill(_ context.Context, targets []windowLiveDeleteTarget) error {
	if r.queueHook != nil {
		r.queueHook(targets)
	}
	if r.queueErr != nil {
		return r.queueErr
	}
	r.queued = append(r.queued, targets...)
	return nil
}

// newTestDeleteCommandWithActiveTarget is the same route wired to a scripted
// active-target observation instead of a real tmux client.
//
// newTestDeleteCommand deliberately leaves activeTarget nil, which is the
// outside-tmux observation, so every pre-existing delete test keeps measuring
// the explicit-selector contract untouched by the fallback.
func newTestDeleteCommandWithActiveTarget(store *fakeResourceStore, active *recordedActiveTarget,
	interactive bool, answer bool, prompts *[]string) *deleteCommand {
	cmd := newTestDeleteCommand(store, interactive, answer, prompts)
	cmd.activeTarget = active.lookup
	return cmd
}

func registryUIDs(registry coremetadata.Registry) map[string]bool {
	out := map[string]bool{}
	for _, project := range registry.Projects {
		out[project.Metadata.UID] = true
	}
	for _, window := range registry.Windows {
		out[window.Metadata.UID] = true
	}
	for _, pane := range registry.Panes {
		out[pane.Metadata.UID] = true
	}
	for _, agent := range registry.Agents {
		out[agent.Metadata.UID] = true
	}
	return out
}

// TestDeleteCascadeRemovesExactlyTheDescendantPlan is the cascade table.
//
// Each case names both what must disappear and what must survive, because the
// contract is as much about the preserved parent and siblings as it is about the
// removed descendants.
func TestDeleteCascadeRemovesExactlyTheDescendantPlan(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		gone    []string
		kept    []string
		cascade int
	}{
		{
			name:    "a window cascades to its agents and every descendant pane, and preserves the project",
			args:    []string{"window", "main", "--project", "alpha", "--yes"},
			gone:    []string{"win-alpha-main", "agt-alpha-codex", "pan-alpha-codex", "pan-alpha-zsh", "pan-alpha-log"},
			kept:    []string{"prj-alpha", "win-alpha-review", "pan-alpha-review", "win-beta-main"},
			cascade: 4,
		},
		{
			name:    "an agent cascades to its managed panes and preserves the window and sibling panes",
			args:    []string{"agent", "codex", "--project", "alpha", "--yes"},
			gone:    []string{"agt-alpha-codex", "pan-alpha-codex"},
			kept:    []string{"win-alpha-main", "pan-alpha-zsh", "pan-alpha-log", "agt-beta-codex"},
			cascade: 1,
		},
		{
			name:    "a leaf pane removes only itself",
			args:    []string{"pane", "log", "--project", "alpha", "--window", "main"},
			gone:    []string{"pan-alpha-log"},
			kept:    []string{"pan-alpha-zsh", "pan-alpha-codex", "agt-alpha-codex", "win-alpha-main"},
			cascade: 0,
		},
		{
			name:    "multiple targets fan out over the whole resolved set",
			args:    []string{"pane", "zsh", "log", "--project", "alpha", "--window", "main", "--yes"},
			gone:    []string{"pan-alpha-zsh", "pan-alpha-log"},
			kept:    []string{"pan-alpha-codex", "pan-alpha-review", "win-alpha-main"},
			cascade: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			stdout, stderr, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err != nil {
				t.Fatalf("delete %v error = %v (stderr=%s)", test.args, err, stderr)
			}
			if store.writes != 1 {
				t.Fatalf("delete %v committed %d writes, want 1", test.args, store.writes)
			}
			uids := registryUIDs(store.registry)
			for _, uid := range test.gone {
				if uids[uid] {
					t.Fatalf("delete %v left %q behind", test.args, uid)
				}
			}
			for _, uid := range test.kept {
				if !uids[uid] {
					t.Fatalf("delete %v removed %q, which must be preserved", test.args, uid)
				}
			}
			if !strings.Contains(stdout, "descendant resource") {
				t.Fatalf("delete %v stdout does not report the cascade:\n%s", test.args, stdout)
			}
			// The result the registry is left in must still validate, which is
			// what proves the cascade did not leave a dangling ownerRef,
			// primaryPaneRef, paneRef, or name reservation.
			if err := store.registry.Validate(); err != nil {
				t.Fatalf("delete %v left an invalid registry: %v", test.args, err)
			}
		})
	}
}

// TestDeletePaneLeavesAnAgentOwnedCurrentPaneAgentAliveAsOffline pins the one
// lifecycle transition the delete verb owns.
func TestDeletePaneLeavesAnAgentOwnedCurrentPaneAgentAliveAsOffline(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"pane", "codex-pane", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("delete pane error = %v", err)
	}
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok {
		t.Fatal("deleting the managed pane removed the agent")
	}
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("agent phase = %q, want Offline", agent.Status.Phase)
	}
	if agent.Status.PaneRef != "" {
		t.Fatalf("agent still points at pane %q", agent.Status.PaneRef)
	}
	if _, ok := store.registry.Pane("pan-alpha-codex"); ok {
		t.Fatal("the managed pane survived the delete")
	}
}

func TestDeleteOfflineAgentAlreadyUsesRegistryOnlyPath(t *testing.T) {
	store := newFakeResourceStore(t)
	cmd := newTestDeleteCommand(store, false, false, nil)
	if _, _, err := runRoute(t, cmd, "pane", "uid:pan-alpha-codex"); err != nil {
		t.Fatalf("prepare Offline Agent by deleting managed Pane: %v", err)
	}
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("prepared Agent = %#v, present=%t; want unbound Offline", agent, ok)
	}
	paneKills := len(cmd.panes.(*fakePaneDeleteRuntime).killed)

	stdout, _, err := runRoute(t, cmd, "agent", "uid:agt-alpha-codex", "--yes")
	if err != nil {
		t.Fatalf("delete Offline Agent error = %v", err)
	}
	if _, ok := store.registry.Agent("agt-alpha-codex"); ok {
		t.Fatal("Offline Agent survived canonical delete")
	}
	if len(cmd.panes.(*fakePaneDeleteRuntime).killed) != paneKills {
		t.Fatalf("delete Offline Agent attempted a tmux Pane kill: %#v", cmd.panes.(*fakePaneDeleteRuntime).killed)
	}
	if !strings.Contains(stdout, "delete agent: deleting 1 agent and 0 descendant resources") {
		t.Fatalf("delete Offline Agent result = %q", stdout)
	}
}

// TestDeleteDryRunPrintsTheFullPlanAndMutatesNothing is the dry-run half of the
// destructive UX contract.
func TestDeleteDryRunPrintsTheFullPlanAndMutatesNothing(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"window", "main", "--project", "alpha", "--dry-run"},
		{"agent", "codex", "--project", "alpha", "--dry-run"},
		{"pane", "zsh", "log", "--project", "alpha", "--window", "main", "--dry-run"},
	} {
		store := newFakeResourceStore(t)
		before := store.snapshot()
		stdout, stderr, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), args...)
		if err != nil {
			t.Fatalf("delete %v --dry-run error = %v", args, err)
		}
		if store.transactions != 0 {
			t.Fatalf("delete %v --dry-run opened %d write transactions, want 0", args, store.transactions)
		}
		if store.snapshot() != before {
			t.Fatalf("delete %v --dry-run mutated the registry", args)
		}
		if !strings.Contains(stdout, "would delete") || !strings.Contains(stdout, "dry-run: nothing was deleted") {
			t.Fatalf("delete %v --dry-run stdout = %q", args, stdout)
		}
		if stderr != "" {
			t.Fatalf("delete %v --dry-run stderr = %q, want none", args, stderr)
		}
	}

	// The dry run lists the whole descendant plan, not just the targets.
	store := newFakeResourceStore(t)
	stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"window", "main", "--project", "alpha", "--dry-run")
	if err != nil {
		t.Fatalf("dry run error = %v", err)
	}
	for _, want := range []string{
		"window/main uid=win-alpha-main owner=project/alpha",
		"cascade agent/codex uid=agt-alpha-codex",
		"cascade pane/codex-pane uid=pan-alpha-codex",
		"cascade pane/zsh uid=pan-alpha-zsh",
		"cascade pane/log uid=pan-alpha-log",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run plan is missing %q:\n%s", want, stdout)
		}
	}
}

// TestDeleteRequiresConfirmationAndRefusesNonInteractivelyWithoutYes is
// acceptance criterion 5's confirmation half.
func TestDeleteRequiresConfirmationAndRefusesNonInteractivelyWithoutYes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "cascade", args: []string{"window", "main", "--project", "alpha"}},
		{name: "agent cascade", args: []string{"agent", "codex", "--project", "alpha"}},
		{name: "multi target", args: []string{"pane", "zsh", "log", "--project", "alpha", "--window", "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err == nil {
				t.Fatalf("delete %v ran without confirmation", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("delete %v refusal is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("delete %v refusal does not name --yes: %v", test.args, err)
			}
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry before confirmation", test.args)
			}
			if stdout != "" {
				t.Fatalf("delete %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}

			// On a TTY the same invocation asks exactly once and honors "no".
			store = newFakeResourceStore(t)
			before = store.snapshot()
			var prompts []string
			_, _, err = runRoute(t, newTestDeleteCommand(store, true, false, &prompts), test.args...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("delete %v declined confirmation error = %v", test.args, err)
			}
			if len(prompts) != 1 {
				t.Fatalf("delete %v asked %d times, want 1", test.args, len(prompts))
			}
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry after a declined confirmation", test.args)
			}

			// A "yes" answer on a TTY runs without --yes.
			store = newFakeResourceStore(t)
			prompts = nil
			if _, _, err := runRoute(t, newTestDeleteCommand(store, true, true, &prompts), test.args...); err != nil {
				t.Fatalf("delete %v after an accepted confirmation error = %v", test.args, err)
			}
			if store.writes != 1 {
				t.Fatalf("delete %v after confirmation committed %d writes, want 1", test.args, store.writes)
			}
		})
	}
}

// TestDeleteExactOneLeafPaneNeedsNoConfirmation is the single carve-out.
func TestDeleteExactOneLeafPaneNeedsNoConfirmation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	var prompts []string
	// Non-interactive, no --yes: the leaf delete still runs.
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, &prompts),
		"pane", "log", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("leaf pane delete error = %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("leaf pane delete prompted: %v", prompts)
	}
	if store.writes != 1 {
		t.Fatalf("leaf pane delete committed %d writes, want 1", store.writes)
	}

	// The carve-out is exactly "one target, Pane kind, no descendants": a single
	// Window target with no descendants is still confirmed, and so is a single
	// Agent target.
	store = newFakeResourceStore(t)
	if _, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil),
		"window", "review", "--project", "alpha"); err == nil {
		t.Fatal("a single Window delete ran without confirmation")
	}
	if store.transactions != 0 {
		t.Fatalf("a refused Window delete opened %d transactions", store.transactions)
	}
}

// TestDeleteValidationFailuresLeaveZeroMutations is acceptance criterion 5's
// atomicity half: any preflight failure exits 2 with the registry untouched.
func TestDeleteValidationFailuresLeaveZeroMutations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no match", args: []string{"pane", "nosuch", "--project", "alpha", "--yes"}, want: "matched no panes"},
		{name: "one missing target in a multi-target set", args: []string{"pane", "zsh", "nosuch", "--project", "alpha", "--window", "main", "--yes"}, want: `nosuch matched no panes`},
		{name: "label filter empties the set", args: []string{"pane", "zsh", "--project", "alpha", "--window", "main", "--selector", "role=nosuch", "--yes"}, want: "matched no panes"},
		{name: "unknown project scope", args: []string{"pane", "zsh", "--project", "nosuch", "--yes"}, want: "--project nosuch"},
		{name: "comma is never split", args: []string{"pane", "zsh,log", "--project", "alpha", "--window", "main", "--yes"}, want: "matched no panes"},
		{name: "unknown kind", args: []string{"project", "alpha", "--yes"}, want: "not available"},
		{name: "no kind", args: nil, want: "delete requires a resource kind"},
		{name: "unknown flag", args: []string{"pane", "--bogus"}, want: "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), test.args...)
			if err == nil {
				t.Fatalf("delete %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("delete %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("delete %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("delete %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if store.transactions != 0 {
				t.Fatalf("delete %v opened %d write transactions, want 0", test.args, store.transactions)
			}
			if store.snapshot() != before {
				t.Fatalf("delete %v mutated the registry", test.args)
			}
		})
	}
}

// TestDeleteAbortsWhenTheCascadePlanChangesBeforeExecution proves the preflight
// is enforced rather than advisory: a plan that no longer matches inside the
// transaction aborts the whole operation with zero mutations.
func TestDeleteAbortsWhenTheCascadePlanChangesBeforeExecution(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	cmd := newTestDeleteCommand(store, false, false, nil)
	commit := cmd.store.update
	var beforeExecution string
	// Simulate a concurrent writer: a new Pane appears under the target Window
	// between the preflight read and the locked transaction.
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{
				UID: "pan-alpha-late", Name: "late",
				OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-main"},
				CreatedAt: resourceFixtureClock,
			},
			Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/alpha"},
		})
		store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
			Scope: "win-alpha-main", Kind: coremetadata.KindPane, Name: "late", UID: "pan-alpha-late",
		})
		beforeExecution = store.snapshot()
		return commit(fn)
	}

	stdout, _, err := runRoute(t, cmd, "window", "main", "--project", "alpha", "--yes")
	if err == nil {
		t.Fatal("delete ran against a stale cascade plan")
	}
	if !strings.Contains(err.Error(), "cascade plan changed") {
		t.Fatalf("error = %v, want the stale-plan abort", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.writes != 0 {
		t.Fatalf("committed %d writes, want 0", store.writes)
	}
	if beforeExecution == "" {
		t.Fatal("the transaction never opened, so the abort proves nothing")
	}
	if store.snapshot() != beforeExecution {
		t.Fatalf("the aborted delete still mutated the registry")
	}
}

// deleteFixturePopulation is the whole-registry blast radius of the shared
// fixture, per kind. It is what an omitted selector used to address.
var deleteFixturePopulation = map[string]int{"window": 3, "pane": 5, "agent": 2}

// TestDeleteEmptySelectorInsideTmuxTargetsOnlyTheActiveResource is acceptance
// criterion 1.
//
// The measurement is the plan, not the exit code: a route that resolved the
// whole registry and a route that resolved one resource both exit 0 under
// --dry-run, and before this change both printed a plan. So each row asserts the
// exact header count, the exact target line, and -- the part that actually
// proves containment -- that not one uid outside the plan appears anywhere in
// the output.
func TestDeleteEmptySelectorInsideTmuxTargetsOnlyTheActiveResource(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		kind      string
		paneUID   string
		windowUID string
		header    string
		target    string
		absent    []string
	}{
		{
			kind: "pane", paneUID: "pan-alpha-log", windowUID: "win-alpha-main",
			header: "delete pane: would delete 1 pane and 0 descendant resources",
			target: "pane/log uid=pan-alpha-log owner=project/alpha window/main",
			absent: []string{"pan-alpha-zsh", "pan-alpha-codex", "pan-alpha-review", "pan-beta-zsh"},
		},
		{
			kind: "window", paneUID: "pan-alpha-log", windowUID: "win-alpha-main",
			header: "delete window: would delete 1 window and 4 descendant resources",
			target: "window/main uid=win-alpha-main owner=project/alpha",
			absent: []string{"win-alpha-review", "win-beta-main", "pan-alpha-review", "pan-beta-zsh"},
		},
		{
			kind: "agent", paneUID: "pan-alpha-codex", windowUID: "win-alpha-main",
			header: "delete agent: would delete 1 agent and 1 descendant resource",
			target: "agent/codex uid=agt-alpha-codex owner=project/alpha window/main",
			absent: []string{"agt-beta-codex", "pan-alpha-zsh", "pan-alpha-log"},
		},
	} {
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			active := insideTmux(test.paneUID, test.windowUID)
			stdout, stderr, err := runRoute(t,
				newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), test.kind, "--dry-run")
			if err != nil {
				t.Fatalf("delete %s --dry-run error = %v (stderr=%s)", test.kind, err, stderr)
			}
			if active.calls != 1 {
				t.Fatalf("delete %s consulted the active target %d times, want 1", test.kind, active.calls)
			}
			header, _, _ := strings.Cut(stdout, "\n")
			if header != test.header {
				t.Fatalf("delete %s header = %q, want %q", test.kind, header, test.header)
			}
			if !strings.Contains(stdout, test.target) {
				t.Fatalf("delete %s plan is missing %q:\n%s", test.kind, test.target, stdout)
			}
			for _, uid := range test.absent {
				if strings.Contains(stdout, uid) {
					t.Fatalf("delete %s planned %q, which is outside the active target:\n%s", test.kind, uid, stdout)
				}
			}
			// A dry run is still a dry run.
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("delete %s --dry-run opened %d transactions or changed the registry", test.kind, store.transactions)
			}
		})
	}
}

// TestDeleteEmptySelectorWithYesCannotTouchTheWholeRegistry is acceptance
// criteria 2 and 4.
//
// `--yes` answers the confirmation; it has never been allowed to answer "which
// resources". Outside tmux there is no active target to contain the empty
// selector onto, so the invocation that used to delete the entire registry
// non-interactively is now a refusal with zero writes -- and the two halves are
// asserted together, because "it errored" would be worth nothing if it errored
// after the transaction.
func TestDeleteEmptySelectorWithYesCannotTouchTheWholeRegistry(t *testing.T) {
	t.Parallel()

	for kind, population := range deleteFixturePopulation {
		t.Run(kind+" outside tmux", func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			active := outsideTmux()
			stdout, _, err := runRoute(t,
				newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), kind, "--yes")
			if err == nil {
				t.Fatalf("delete %s --yes deleted the whole registry outside tmux", kind)
			}
			if !IsUsageError(err) {
				t.Fatalf("delete %s refusal is not a usage error: %v", kind, err)
			}
			want := "resolve " + kind + ": no selector was given and no active tmux target resolved, " +
				"so nothing was selected; pass an explicit resource reference, a "
			if !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("delete %s error = %q, want the containment refusal", kind, err)
			}
			if !strings.HasSuffix(err.Error(), "--all to address every "+kind+" in the registry") {
				t.Fatalf("delete %s refusal does not name the registry-wide escape hatch: %v", kind, err)
			}
			// The refusal is its own failure, not the 1..N cardinality error: a
			// 1..N cell is satisfied by the whole registry, so reporting it that
			// way would be reporting a violation that did not happen.
			if strings.Contains(err.Error(), "want at least one") {
				t.Fatalf("delete %s refusal collapsed onto the cardinality error: %v", kind, err)
			}
			if stdout != "" {
				t.Fatalf("delete %s wrote %q to stdout, want 0 bytes", kind, stdout)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("delete %s opened %d transactions and committed %d writes", kind, store.transactions, store.writes)
			}
			if store.snapshot() != before {
				t.Fatalf("delete %s changed the registry", kind)
			}
			// Non-vacuity: the whole registry really was reachable through this
			// argv, so the refusal is protecting something.
			if population < 2 {
				t.Fatalf("the fixture holds %d %ss; the containment assertion is weak", population, kind)
			}
		})
	}

	// Inside tmux the same argv commits exactly one delete, and every other
	// resource of that kind survives.
	t.Run("pane inside tmux", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		active := insideTmux("pan-alpha-log", "win-alpha-main")
		if _, _, err := runRoute(t,
			newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), "pane", "--yes"); err != nil {
			t.Fatalf("delete pane --yes inside tmux error = %v", err)
		}
		if store.writes != 1 {
			t.Fatalf("delete pane --yes committed %d writes, want 1", store.writes)
		}
		uids := registryUIDs(store.registry)
		if uids["pan-alpha-log"] {
			t.Fatal("delete pane --yes left the active pane behind")
		}
		for _, uid := range []string{"pan-alpha-zsh", "pan-alpha-codex", "pan-alpha-review", "pan-beta-zsh"} {
			if !uids[uid] {
				t.Fatalf("delete pane --yes removed %q, which is outside the active target", uid)
			}
		}
		if err := store.registry.Validate(); err != nil {
			t.Fatalf("the contained delete left an invalid registry: %v", err)
		}
	})
}

// TestDeleteWholeRegistryFanOutNeedsTheExplicitAllFlag pins the escape hatch.
//
// `--all` restores the historical plan byte for byte, in and out of tmux alike,
// and it is measured to consult the active-target seam zero times: an explicit
// registry-wide request must not depend on where it was typed. This is also the
// negative parity guard for the later Project-scoped read default: read-side
// `--all-projects` cannot reinterpret or route destructive `delete --all`.
func TestDeleteWholeRegistryFanOutNeedsTheExplicitAllFlag(t *testing.T) {
	t.Parallel()

	for kind, population := range deleteFixturePopulation {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var plans []string
			for _, active := range []*recordedActiveTarget{
				outsideTmux(),
				insideTmux("pan-alpha-log", "win-alpha-main"),
			} {
				store := newFakeResourceStore(t)
				before := store.snapshot()
				stdout, _, err := runRoute(t,
					newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), kind, "--all", "--dry-run")
				if err != nil {
					t.Fatalf("delete %s --all --dry-run error = %v", kind, err)
				}
				if active.calls != 0 {
					t.Fatalf("delete %s --all consulted the active target %d times, want 0", kind, active.calls)
				}
				header, _, _ := strings.Cut(stdout, "\n")
				want := fmt.Sprintf("delete %s: would delete %d %ss and", kind, population, kind)
				if !strings.HasPrefix(header, want) {
					t.Fatalf("delete %s --all header = %q, want it to start with %q", kind, header, want)
				}
				if store.transactions != 0 || store.snapshot() != before {
					t.Fatalf("delete %s --all --dry-run mutated the registry", kind)
				}
				plans = append(plans, stdout)
			}
			if plans[0] != plans[1] {
				t.Fatalf("delete %s --all differs in and out of tmux:\n--- outside ---\n%s\n--- inside ---\n%s",
					kind, plans[0], plans[1])
			}
		})
	}
}

// TestDeleteAllIsRefusedNextToASelector keeps the two spellings from blending.
//
// `--all` answers the same question a selector answers, so accepting both would
// leave the route deciding which one wins. Every shape below is rejected before
// the registry is even read.
func TestDeleteAllIsRefusedNextToASelector(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"pane", "zsh", "--all", "--yes"},
		{"pane", "--project", "alpha", "--all", "--yes"},
		{"pane", "--window", "main", "--all", "--yes"},
		{"pane", "--selector", "role=shell", "--all", "--yes"},
		{"window", "main", "--all", "--yes"},
		{"agent", "codex", "--all", "--yes"},
	} {
		store := newFakeResourceStore(t)
		before := store.snapshot()
		active := insideTmux("pan-alpha-log", "win-alpha-main")
		stdout, _, err := runRoute(t,
			newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), args...)
		if err == nil {
			t.Fatalf("delete %v accepted --all next to a selector", args)
		}
		if !IsUsageError(err) {
			t.Fatalf("delete %v error is not a usage error: %v", args, err)
		}
		if !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), "cannot be combined with a selector") {
			t.Fatalf("delete %v error = %q, want the --all conflict", args, err)
		}
		if stdout != "" {
			t.Fatalf("delete %v wrote %q to stdout, want 0 bytes", args, stdout)
		}
		if store.transactions != 0 || store.snapshot() != before {
			t.Fatalf("delete %v mutated the registry", args)
		}
	}
}

// TestDeleteWithNoSelectorAlwaysConfirms is the confirmation half of the
// containment.
//
// The leaf-Pane carve-out was written for an argv that names its one cheap
// target. An empty selector names nothing, so the prompt is the only place the
// plan is stated before it runs. The contrast is asserted in one test on
// purpose: the same resource, deleted two ways, prompts exactly when the
// operator did not say which resource it was.
func TestDeleteWithNoSelectorAlwaysConfirms(t *testing.T) {
	t.Parallel()

	// Non-interactive and without --yes: the implicit leaf-Pane delete refuses
	// where the explicit one runs.
	store := newFakeResourceStore(t)
	before := store.snapshot()
	active := insideTmux("pan-alpha-log", "win-alpha-main")
	stdout, _, err := runRoute(t,
		newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), "pane")
	if err == nil {
		t.Fatal("an implicit leaf-pane delete ran without confirmation")
	}
	if !IsUsageError(err) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("implicit leaf-pane refusal = %v, want a usage error naming --yes", err)
	}
	if stdout != "" || store.transactions != 0 || store.snapshot() != before {
		t.Fatalf("the refused implicit delete wrote %q or mutated the registry", stdout)
	}

	// The same Pane, named explicitly, keeps the carve-out untouched.
	store = newFakeResourceStore(t)
	var prompts []string
	if _, _, err := runRoute(t, newTestDeleteCommandWithActiveTarget(store, insideTmux("pan-alpha-log", "win-alpha-main"),
		false, false, &prompts), "pane", "log", "--project", "alpha", "--window", "main"); err != nil {
		t.Fatalf("explicit leaf-pane delete error = %v", err)
	}
	if len(prompts) != 0 || store.writes != 1 {
		t.Fatalf("the explicit leaf-pane carve-out changed: prompts=%v writes=%d", prompts, store.writes)
	}

	// On a TTY the implicit delete asks exactly once, and "no" writes nothing.
	store = newFakeResourceStore(t)
	before = store.snapshot()
	prompts = nil
	if _, _, err := runRoute(t, newTestDeleteCommandWithActiveTarget(store,
		insideTmux("pan-alpha-log", "win-alpha-main"), true, false, &prompts), "pane"); err == nil {
		t.Fatal("a declined implicit delete succeeded")
	}
	if len(prompts) != 1 {
		t.Fatalf("the implicit delete asked %d times, want 1", len(prompts))
	}
	if store.transactions != 0 || store.snapshot() != before {
		t.Fatal("a declined implicit delete mutated the registry")
	}

	// And "yes" on the same prompt runs it.
	store = newFakeResourceStore(t)
	prompts = nil
	if _, _, err := runRoute(t, newTestDeleteCommandWithActiveTarget(store,
		insideTmux("pan-alpha-log", "win-alpha-main"), true, true, &prompts), "pane"); err != nil {
		t.Fatalf("an accepted implicit delete error = %v", err)
	}
	if len(prompts) != 1 || store.writes != 1 {
		t.Fatalf("accepted implicit delete: prompts=%d writes=%d", len(prompts), store.writes)
	}

	// --all never inherits the carve-out either, even though it could resolve a
	// single leaf Pane in a smaller registry.
	store = newFakeResourceStore(t)
	prompts = nil
	if _, _, err := runRoute(t, newTestDeleteCommandWithActiveTarget(store, outsideTmux(), true, false, &prompts),
		"pane", "--all"); err == nil {
		t.Fatal("delete pane --all ran without confirmation")
	}
	if len(prompts) != 1 {
		t.Fatalf("delete pane --all asked %d times, want 1", len(prompts))
	}
}

// TestDeleteExplicitSelectorsAreUnchangedByTheContainment is acceptance
// criterion 3.
//
// Every explicit shape runs twice against the same fixture -- once with the
// active target pointing at a *different* resource, once outside tmux -- and the
// two runs must be byte-identical, with the seam consulted zero times. That is
// stronger than asserting the output looks right: it proves the fallback is not
// merely producing the same answer by luck, it is not reached at all.
func TestDeleteExplicitSelectorsAreUnchangedByTheContainment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "positional ref", args: []string{"pane", "log", "--project", "alpha", "--window", "main", "--dry-run"}},
		{name: "uid ref", args: []string{"pane", "uid:pan-alpha-log", "--dry-run"}},
		{name: "multi ref fan-out", args: []string{"pane", "zsh", "log", "--project", "alpha", "--window", "main", "--dry-run"}},
		{name: "project scope", args: []string{"pane", "--project", "beta", "--dry-run"}},
		{name: "window scope", args: []string{"window", "--project", "beta", "--dry-run"}},
		{name: "label filter", args: []string{"pane", "--project", "alpha", "--selector", "role=sidecar", "--dry-run"}},
		{name: "agent scope", args: []string{"agent", "--project", "beta", "--dry-run"}},
		{name: "unmatched ref", args: []string{"pane", "zzz-nonexistent", "--dry-run"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var outputs []string
			var errs []string
			for _, active := range []*recordedActiveTarget{
				insideTmux("pan-alpha-codex", "win-alpha-review"),
				outsideTmux(),
			} {
				store := newFakeResourceStore(t)
				stdout, _, err := runRoute(t,
					newTestDeleteCommandWithActiveTarget(store, active, false, false, nil), test.args...)
				if active.calls != 0 {
					t.Fatalf("delete %v consulted the active target %d times, want 0", test.args, active.calls)
				}
				outputs = append(outputs, stdout)
				errs = append(errs, fmt.Sprint(err))
			}
			if outputs[0] != outputs[1] || errs[0] != errs[1] {
				t.Fatalf("delete %v differs in and out of tmux:\nstdout %q vs %q\nerr %q vs %q",
					test.args, outputs[0], outputs[1], errs[0], errs[1])
			}
		})
	}

	// The historical no-match summary is pinned verbatim: an explicit reference
	// that resolves nothing must keep reporting the 1..N cardinality failure and
	// must not be re-routed onto the new containment refusal.
	store := newFakeResourceStore(t)
	_, _, err := runRoute(t, newTestDeleteCommandWithActiveTarget(store,
		insideTmux("pan-alpha-log", "win-alpha-main"), false, false, nil), "pane", "zzz-nonexistent", "--dry-run")
	if err == nil {
		t.Fatal("an unmatched explicit ref succeeded")
	}
	summary, _, _ := strings.Cut(err.Error(), "\n")
	const want = "resolve pane: --pane zzz-nonexistent matched no panes, want at least one"
	if summary != want {
		t.Fatalf("unmatched ref summary = %q, want %q", summary, want)
	}
}

// TestDeleteRefusesAnUnmappedActiveTargetWithTheSeamsOwnMessage keeps the third
// outcome distinguishable.
//
// Inside tmux on a pane that carries no Projmux identity, the failure is the
// active-target seam's refusal, not the containment refusal and not a
// cardinality error. Collapsing them would tell an operator sitting in a
// non-Projmux pane to pass --all, which is the single worst advice available at
// that moment.
func TestDeleteRefusesAnUnmappedActiveTargetWithTheSeamsOwnMessage(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"window", "pane", "agent"} {
		store := newFakeResourceStore(t)
		before := store.snapshot()
		stdout, _, err := runRoute(t,
			newTestDeleteCommandWithActiveTarget(store, insideTmux("", ""), false, false, nil), kind, "--yes")
		if err == nil {
			t.Fatalf("delete %s selected something for an unmapped active target", kind)
		}
		if !IsUsageError(err) {
			t.Fatalf("delete %s unmapped refusal is not a usage error: %v", kind, err)
		}
		if !strings.Contains(err.Error(), "nothing was selected, so pass an explicit resource reference") {
			t.Fatalf("delete %s unmapped refusal is not the seam's own: %v", kind, err)
		}
		if strings.Contains(err.Error(), "--all") || strings.Contains(err.Error(), "want at least one") {
			t.Fatalf("delete %s unmapped refusal collapsed onto another failure: %v", kind, err)
		}
		if stdout != "" || store.transactions != 0 || store.snapshot() != before {
			t.Fatalf("delete %s unmapped refusal wrote %q or mutated the registry", kind, stdout)
		}
	}
}

func TestDeleteWindowDryRunShowsLiveAndLastSessionCascadeWithoutWrites(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	before := store.snapshot()

	stdout, _, err := runRoute(t, cmd, "window", "uid:win-beta-main", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	for _, want := range []string{
		"live would kill tmux window @12 session=beta session-id=$21 socket=-L/projmux",
		"live cascade would end Project session beta because its last live Window is deleted",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, stdout)
		}
	}
	if store.transactions != 0 || store.snapshot() != before || len(runtime.killed) != 0 || len(runtime.queued) != 0 {
		t.Fatalf("dry-run mutated state: tx=%d killed=%v queued=%v", store.transactions, runtime.killed, runtime.queued)
	}
}

func TestDeleteWindowKillsExactLiveTargetBeforeStoreCommitAndPreservesSibling(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	var events []string
	runtime.killHook = func(target windowLiveDeleteTarget) {
		events = append(events, "kill "+target.WindowID)
		if _, ok := store.registry.Window(target.UID); !ok {
			t.Fatal("registry target disappeared before exact live kill")
		}
	}
	originalUpdate := cmd.store.update
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		result, err := originalUpdate(fn)
		if err == nil {
			events = append(events, "store committed")
		}
		return result, err
	}

	stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
	if err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if got := strings.Join(events, ","); got != "kill @10,store committed" {
		t.Fatalf("ordering = %q", got)
	}
	if _, ok := store.registry.Window("win-alpha-main"); ok {
		t.Fatal("target Window survived")
	}
	if _, ok := store.registry.Window("win-alpha-review"); !ok {
		t.Fatal("sibling Window was removed")
	}
	if _, ok := store.registry.Project("prj-alpha"); !ok {
		t.Fatal("owning Project was removed")
	}
	if !strings.Contains(stdout, "live killed tmux window @10 session=alpha") || strings.Contains(stdout, "last live Window") {
		t.Fatalf("two-Window result = %q", stdout)
	}
}

func TestDeleteWindowTmuxFailureCommitsZeroRegistryWrites(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	runtime.killErr = errors.New("injected tmux failure")
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	before := store.snapshot()

	stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
	if err == nil || !strings.Contains(err.Error(), "injected tmux failure") {
		t.Fatalf("tmux failure = %v", err)
	}
	if stdout != "" || store.writes != 0 || store.snapshot() != before || len(runtime.queued) != 0 {
		t.Fatalf("tmux failure changed state: stdout=%q writes=%d queued=%v", stdout, store.writes, runtime.queued)
	}
}

func TestDeleteWindowSecondTmuxFailureReportsFirstExactDriftWithoutUnplannedKill(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	runtime.killErrs = map[string]error{"@12": errors.New("injected second tmux failure")}
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	before := store.snapshot()
	var attempts []string
	runtime.killHook = func(target windowLiveDeleteTarget) { attempts = append(attempts, target.WindowID) }

	stdout, _, err := runRoute(t, cmd,
		"window", "uid:win-alpha-main", "uid:win-beta-main", "--yes")
	if err == nil {
		t.Fatal("second tmux failure succeeded")
	}
	for _, want := range []string{
		"injected second tmux failure",
		"@10/session=alpha($20)",
		"win-alpha-main,win-beta-main",
		"retryable drift",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("partial tmux failure error = %q, want %q", err, want)
		}
	}
	if got := strings.Join(attempts, ","); got != "@10,@12" {
		t.Fatalf("exact kill attempts = %q, want first target then failing second", got)
	}
	if len(runtime.killed) != 1 || runtime.killed[0].WindowID != "@10" {
		t.Fatalf("successful exact kills = %#v", runtime.killed)
	}
	if strings.Contains(err.Error(), "@11") {
		t.Fatalf("partial failure named unplanned sibling @11: %v", err)
	}
	if stdout != "" || store.writes != 0 || store.snapshot() != before {
		t.Fatalf("partial tmux failure committed Registry/output: stdout=%q writes=%d", stdout, store.writes)
	}
}

func TestDeleteWindowStoreFailureReportsExactRetainedDrift(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	before := store.snapshot()
	var events []string
	runtime.killHook = func(target windowLiveDeleteTarget) { events = append(events, "kill "+target.WindowID) }
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, err
		}
		events = append(events, "store failed")
		return coremetadata.Registry{}, errors.New("injected store commit failure")
	}

	stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
	if err == nil {
		t.Fatal("store failure succeeded")
	}
	for _, want := range []string{"injected store commit failure", "@10/session=alpha($20)", "win-alpha-main", "retryable drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("store failure error = %q, want %q", err, want)
		}
	}
	if got := strings.Join(events, ","); got != "kill @10,store failed" {
		t.Fatalf("failure ordering = %q", got)
	}
	if stdout != "" || store.snapshot() != before {
		t.Fatalf("store failure committed registry/output: stdout=%q", stdout)
	}
}

type deleteFlushBuffer struct {
	bytes.Buffer
	flushed bool
}

func (b *deleteFlushBuffer) Flush() error {
	b.flushed = true
	return nil
}

func TestDeleteWindowSelfTargetCommitsAndFlushesBeforeQueue(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	runtime.selfUID = "win-alpha-main"
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	stdout := &deleteFlushBuffer{}
	runtime.queueHook = func(targets []windowLiveDeleteTarget) {
		if !stdout.flushed {
			t.Fatal("self kill queued before result flush")
		}
		if _, ok := store.registry.Window("win-alpha-main"); ok {
			t.Fatal("self kill queued before registry commit")
		}
		if len(targets) != 1 || targets[0].WindowID != "@10" {
			t.Fatalf("queued targets = %#v", targets)
		}
	}
	if err := cmd.Run([]string{"window", "uid:win-alpha-main", "--yes"}, stdout, io.Discard); err != nil {
		t.Fatalf("self delete error = %v", err)
	}
	if len(runtime.killed) != 0 || len(runtime.queued) != 1 {
		t.Fatalf("self mutation used sync kill=%v queued=%v", runtime.killed, runtime.queued)
	}
	if !strings.Contains(stdout.String(), "will queue after this result is flushed to kill tmux window @10") {
		t.Fatalf("self result = %q", stdout.String())
	}
}

func TestDeleteWindowSelfQueueFailureLeavesDurableRegistryResultAndExactDrift(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixtureWindowDeleteRuntime()
	runtime.selfUID = "win-alpha-main"
	runtime.queueErr = errors.New("injected queue failure")
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.windows = runtime
	stdout := &deleteFlushBuffer{}

	err := cmd.Run([]string{"window", "uid:win-alpha-main", "--yes"}, stdout, io.Discard)
	if err == nil {
		t.Fatal("queue failure succeeded")
	}
	for _, want := range []string{"registry cascade committed", "complete result was written", "injected queue failure", "retryable orphan drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("queue failure error = %q, want %q", err, want)
		}
	}
	if _, ok := store.registry.Window("win-alpha-main"); ok || !stdout.flushed || stdout.Len() == 0 {
		t.Fatalf("queue failure lost durable result: flushed=%t stdout=%q", stdout.flushed, stdout.String())
	}
}
