package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func livePaneInventoryRow(fields ...string) string {
	return strings.Join(fields, tmuxRowSepFormat) + "\n"
}

func paneRuntimeInventory() string {
	return livePaneInventoryRow("$1", "alpha", "@10", "%30", "prj-alpha", "win-alpha-main", "pan-alpha-zsh") +
		livePaneInventoryRow("$1", "alpha", "@10", "%31", "prj-alpha", "win-alpha-main", "pan-alpha-log") +
		livePaneInventoryRow("$1", "alpha", "@10", "%32", "prj-alpha", "win-alpha-main", "pan-alpha-codex") +
		livePaneInventoryRow("$1", "alpha", "@11", "%33", "prj-alpha", "win-alpha-review", "pan-alpha-review") +
		livePaneInventoryRow("$2", "beta", "@12", "%34", "prj-beta", "win-beta-main", "pan-beta-zsh")
}

func newPaneRuntimeFixture(t *testing.T, inventory string) (*tmuxPaneDeleteRuntime, *recordingTmuxRunner, coremetadata.Registry) {
	t.Helper()
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	// The exact target matches what the route resolves from the hermetic
	// $TMUX in newTestDeleteCommand, so a runtime fixture and a full route
	// invocation address the same isolated server.
	target, err := tmuxSocketPathTarget(testDeleteTarget.value)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &tmuxPaneDeleteRuntime{runner: runner, target: target, getenv: func(string) string { return "" }}
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{@projmux_project_uid}", "#{@projmux_window_uid}", "#{@projmux_pane_uid}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "list-panes", "-a", "-F", format)] = inventory
	return runtime, runner, resourceFixtureRegistry(t)
}

func panePlanFor(t *testing.T, registry coremetadata.Registry, kind coremetadata.Kind, targets ...string) deletePlan {
	t.Helper()
	plan := deletePlan{Kind: kind}
	for _, uid := range targets {
		plan.Targets = append(plan.Targets, deleteTarget{
			Match:       selector.Match{Kind: kind, UID: uid},
			Descendants: cascadeOf(registry, kind, uid),
		})
	}
	return plan
}

func TestPaneDeleteRuntimePreflightPinsSiblingAgentAndImplicitCascades(t *testing.T) {
	runtime, _, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())

	siblingPlan, err := runtime.preflight(context.Background(), registry,
		panePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log"))
	if err != nil {
		t.Fatalf("sibling preflight: %v", err)
	}
	if len(siblingPlan.Targets) != 1 || siblingPlan.Targets[0].PaneID != "%31" ||
		siblingPlan.Targets[0].EndsWindow || siblingPlan.Targets[0].EndsSession {
		t.Fatalf("sibling plan = %#v", siblingPlan)
	}

	agentPlan, err := runtime.preflight(context.Background(), registry,
		panePlanFor(t, registry, coremetadata.KindAgent, "agt-alpha-codex"))
	if err != nil {
		t.Fatalf("Agent preflight: %v", err)
	}
	if len(agentPlan.Targets) != 1 || agentPlan.Targets[0].ResourceUID != "agt-alpha-codex" ||
		agentPlan.Targets[0].PaneUID != "pan-alpha-codex" {
		t.Fatalf("Agent plan = %#v", agentPlan)
	}

	lastPlan, err := runtime.preflight(context.Background(), registry,
		panePlanFor(t, registry, coremetadata.KindPane, "pan-beta-zsh"))
	if err != nil {
		t.Fatalf("last Pane preflight: %v", err)
	}
	if len(lastPlan.Targets) != 1 || !lastPlan.Targets[0].EndsWindow || !lastPlan.Targets[0].EndsSession ||
		lastPlan.endsWindows() != 1 || lastPlan.endsSessions() != 1 {
		t.Fatalf("last Pane plan = %#v", lastPlan)
	}
}

func TestPaneDeleteRuntimeFailsClosedOnMissingDuplicateForeignAndStaleMirrors(t *testing.T) {
	base := livePaneInventoryRow("$1", "alpha", "@10", "%30", "prj-alpha", "win-alpha-main", "pan-alpha-zsh")
	for _, test := range []struct {
		name      string
		inventory string
		want      string
	}{
		{name: "missing", inventory: "", want: "no exact live tmux Pane mirror"},
		{name: "duplicate", inventory: base + livePaneInventoryRow("$2", "alpha", "@14", "%40", "prj-alpha", "win-alpha-main", "pan-alpha-zsh"), want: "2 live tmux Pane mirrors"},
		{name: "duplicate owning Window mirror", inventory: base + livePaneInventoryRow("$1", "alpha", "@14", "%40", "prj-alpha", "win-alpha-main", "pan-foreign"), want: "2 live tmux Window mirrors"},
		{name: "foreign Window", inventory: livePaneInventoryRow("$1", "alpha", "@11", "%30", "prj-alpha", "win-alpha-review", "pan-alpha-zsh"), want: "foreign Window uid"},
		{name: "missing Window mirror", inventory: livePaneInventoryRow("$1", "alpha", "@10", "%30", "prj-alpha", "", "pan-alpha-zsh"), want: "<missing>"},
		{name: "foreign Project", inventory: livePaneInventoryRow("$1", "alpha", "@10", "%30", "prj-beta", "win-alpha-main", "pan-alpha-zsh"), want: "foreign Project uid"},
		{name: "stale session", inventory: livePaneInventoryRow("$1", "renamed", "@10", "%30", "prj-alpha", "win-alpha-main", "pan-alpha-zsh"), want: "stale session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, runner, registry := newPaneRuntimeFixture(t, test.inventory)
			_, err := runtime.preflight(context.Background(), registry,
				panePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-zsh"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want %q", err, test.want)
			}
			for _, call := range runner.calls {
				if strings.Contains(strings.Join(call.args, " "), "kill-pane") {
					t.Fatalf("failed preflight mutated tmux: %#v", runner.calls)
				}
			}
		})
	}
}

func TestNamedLastPaneRuntimeCascadeForcesConfirmation(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	var prompts []string
	cmd := newTestDeleteCommand(store, false, false, &prompts)
	cmd.panes = runtime
	before := store.snapshot()
	out, _, err := runRoute(t, cmd, "pane", "uid:pan-beta-zsh")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("named last-Pane refusal = %v", err)
	}
	if out != "" || store.snapshot() != before || store.transactions != 0 || len(runtime.killed) != 0 {
		t.Fatal("named last-Pane refusal mutated Registry/tmux or wrote stdout")
	}

	store = newFakeResourceStore(t)
	runtime = newFixturePaneDeleteRuntime()
	prompts = nil
	cmd = newTestDeleteCommand(store, true, false, &prompts)
	cmd.panes = runtime
	_, _, err = runRoute(t, cmd, "pane", "uid:pan-beta-zsh")
	if err == nil || len(prompts) != 1 {
		t.Fatalf("interactive named last-Pane refusal err=%v prompts=%v", err, prompts)
	}
	for _, want := range []string{"kill 1 exact live tmux Pane", "end 1 Window", "end 1 Project session"} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("last-Pane prompt = %q, want %q", prompts[0], want)
		}
	}
}

func TestPaneDeleteRuntimeImplicitTargetRequiresExactCallerSocketAndPane(t *testing.T) {
	runtime, runner, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())
	runtime.getenv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/exact.sock,123,0"
		case "TMUX_PANE":
			return "%31"
		}
		return ""
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{socket_path}")] = "/tmp/exact.sock\n"
	format := tmuxRowFormat("#{socket_path}", "#{pane_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-t", "%31", "-F", format)] =
		livePaneInventoryRow("/tmp/exact.sock", "%31")
	plan := panePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log")
	plan.Implicit = true

	live, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("implicit preflight: %v", err)
	}
	if len(live.Targets) != 1 || !live.Targets[0].Self {
		t.Fatalf("implicit plan = %#v", live)
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{socket_path}")] = "/tmp/foreign.sock\n"
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil || !strings.Contains(err.Error(), "not attached to the exact") {
		t.Fatalf("foreign socket error = %v", err)
	}
}

func TestPaneDeleteRuntimeQueueRevalidatesTombstonesAndUsesExactSocket(t *testing.T) {
	runtime, runner, _ := newPaneRuntimeFixture(t, "")
	target := paneLiveDeleteTarget{PaneUID: "pan-alpha-log", PaneID: "%31", WindowID: "@10", SessionName: "alpha", SessionID: "$1"}
	showKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "show-options", "-pqv", "-t", "%31", tmuxopts.PaneUID)
	runner.outputs[showKey] = "pan-alpha-log\n"
	if err := runtime.tombstoneSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	runner.outputs[showKey] = "deleted:pan-alpha-log\n"
	if err := runtime.queueSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"-S", testDeleteTarget.value, "show-options", "-pqv", "-t", "%31", tmuxopts.PaneUID}},
		{name: "tmux", args: []string{"-S", testDeleteTarget.value, "set-option", "-pq", "-t", "%31", tmuxopts.PaneUID, "deleted:pan-alpha-log"}},
		{name: "tmux", args: []string{"-S", testDeleteTarget.value, "show-options", "-pqv", "-t", "%31", tmuxopts.PaneUID}},
		{name: "tmux", args: []string{"-S", testDeleteTarget.value, "run-shell", "-b", "exec 'tmux' '-S' '" + testDeleteTarget.value + "' kill-pane -t '%31'"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("queue calls = %#v, want %#v", runner.calls, want)
	}

	runner.calls = nil
	runner.outputs[showKey] = "pan-foreign\n"
	err := runtime.queueSelfKill(context.Background(), []paneLiveDeleteTarget{target})
	if err == nil || !strings.Contains(err.Error(), "pan-foreign") || !strings.Contains(err.Error(), `want="deleted:pan-alpha-log"`) {
		t.Fatalf("foreign revalidation = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("foreign revalidation made mutation calls: %#v", runner.calls)
	}
}

type statefulPaneDeleteRunner struct {
	options   map[string]string
	failSet   map[string]error
	failQueue map[string]error
	calls     []recordedTmuxCall
}

func paneSetFailureKey(paneID, value string) string { return paneID + "\x00" + value }

func (r *statefulPaneDeleteRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name != "tmux" || len(args) < 3 || (args[0] != "-L" && args[0] != "-S") {
		return nil, errors.New("stateful Pane delete runner requires exact -L/-S routing")
	}
	command := args[2]
	paneID := flagValue(args[2:], "-t")
	switch command {
	case "show-options":
		return []byte(r.options[paneID] + "\n"), nil
	case "set-option":
		value := args[len(args)-1]
		if err := r.failSet[paneSetFailureKey(paneID, value)]; err != nil {
			return nil, err
		}
		r.options[paneID] = value
		return nil, nil
	case "run-shell":
		for id, err := range r.failQueue {
			if strings.Contains(args[len(args)-1], shellQuote(id)) {
				return nil, err
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("stateful Pane delete runner: unexpected command %q", command)
	}
}

func statefulPaneRuntime(t *testing.T, runner tmuxCommandRunner) *tmuxPaneDeleteRuntime {
	t.Helper()
	target, err := tmuxSocketPathTarget(testDeleteTarget.value)
	if err != nil {
		t.Fatal(err)
	}
	return &tmuxPaneDeleteRuntime{runner: runner, target: target, getenv: func(string) string { return "" }}
}

func multiSelfDeleteTargets() []paneLiveDeleteTarget {
	return []paneLiveDeleteTarget{
		{PaneUID: "pan-alpha-zsh", PaneID: "%30", WindowID: "@10", SessionName: "alpha", SessionID: "$1", Self: true},
		{PaneUID: "pan-alpha-log", PaneID: "%31", WindowID: "@10", SessionName: "alpha", SessionID: "$1"},
		{PaneUID: "pan-alpha-codex", PaneID: "%32", WindowID: "@10", SessionName: "alpha", SessionID: "$1"},
	}
}

func TestPaneDeleteRuntimeTombstoneFailureRollsBackBeforeRegistryCommit(t *testing.T) {
	targets := multiSelfDeleteTargets()
	runner := &statefulPaneDeleteRunner{
		options: map[string]string{"%30": "pan-alpha-zsh", "%31": "pan-alpha-log", "%32": "pan-alpha-codex"},
		failSet: map[string]error{
			paneSetFailureKey("%31", deletedPaneMirrorPrefix+"pan-alpha-log"): errors.New("injected second tombstone failure"),
		},
	}
	runtime := statefulPaneRuntime(t, runner)
	err := runtime.tombstoneSelfKill(context.Background(), targets)
	if err == nil {
		t.Fatal("second tombstone failure succeeded")
	}
	for _, want := range []string{"injected second tombstone failure", "%30/pane-uid=pan-alpha-zsh", "were restored", "Registry resources remain unchanged"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("tombstone failure = %q, want %q", err, want)
		}
	}
	registry := resourceFixtureRegistry(t)
	for paneID, want := range map[string]string{"%30": "pan-alpha-zsh", "%31": "pan-alpha-log", "%32": "pan-alpha-codex"} {
		if got := runner.options[paneID]; got != want {
			t.Fatalf("mirror %s = %q, want restored/original %q", paneID, got, want)
		}
		match := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}).MatchPane(
			&registry, "win-alpha-main", want)
		if match.Kind != coremetadata.AdoptionRebind || match.UID != want {
			t.Fatalf("restored Pane %s match = %#v, want existing Registry binding", paneID, match)
		}
	}
	for _, call := range runner.calls {
		if len(call.args) > 2 && call.args[2] == "run-shell" {
			t.Fatalf("pre-commit tombstone failure queued a kill: %#v", runner.calls)
		}
	}
}

func TestPaneDeleteRuntimeQueueFailureLeavesEveryUnfinishedTargetTombstoned(t *testing.T) {
	targets := multiSelfDeleteTargets()
	runner := &statefulPaneDeleteRunner{
		options:   map[string]string{"%30": "pan-alpha-zsh", "%31": "pan-alpha-log", "%32": "pan-alpha-codex"},
		failSet:   map[string]error{},
		failQueue: map[string]error{"%32": errors.New("injected second queue failure")},
	}
	runtime := statefulPaneRuntime(t, runner)
	if err := runtime.tombstoneSelfKill(context.Background(), targets); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	err := runtime.queueSelfKill(context.Background(), targets)
	if err == nil {
		t.Fatal("second queue failure succeeded")
	}
	for _, want := range []string{
		"injected second queue failure", "queued exact Pane(s) %31/pane-uid=pan-alpha-log",
		"tombstoned unqueued Pane(s) %32/pane-uid=pan-alpha-codex,%30/pane-uid=pan-alpha-zsh",
		"cannot be orphan-imported",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("queue failure = %q, want %q", err, want)
		}
	}
	registry := resourceFixtureRegistry(t)
	mutator := fixtureMutator()
	for _, uid := range []string{"pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex"} {
		if err := mutator.DeletePane(&registry, uid); err != nil {
			t.Fatalf("delete fixture Pane %s: %v", uid, err)
		}
	}
	for _, target := range targets {
		want := deletedPaneMirrorPrefix + target.PaneUID
		got := runner.options[target.PaneID]
		if got != want {
			t.Fatalf("post-commit mirror %s = %q, want tombstone %q", target.PaneID, got, want)
		}
		match := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}).MatchPane(
			&registry, "win-alpha-main", got)
		if match.Kind != coremetadata.AdoptionRefused {
			t.Fatalf("tombstoned deleted Pane %s match = %#v, want refused", target.PaneID, match)
		}
	}
}

func TestPaneDeleteRouteDryRunExecutionPartialFailureAndAgentOffline(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	before := store.snapshot()
	out, _, err := runRoute(t, cmd, "pane", "uid:pan-beta-zsh", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	for _, want := range []string{"live would kill tmux pane %34", "would end Window @12", "would end Project session beta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run missing %q:\n%s", want, out)
		}
	}
	if store.snapshot() != before || store.transactions != 0 || len(runtime.killed) != 0 {
		t.Fatal("dry-run mutated Registry or tmux")
	}

	store = newFakeResourceStore(t)
	runtime = newFixturePaneDeleteRuntime()
	cmd = newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	if _, _, err := runRoute(t, cmd, "pane", "uid:pan-alpha-codex"); err != nil {
		t.Fatalf("managed Pane delete: %v", err)
	}
	agent, ok := store.registry.Agent("agt-alpha-codex")
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("Agent after managed Pane delete = %#v, ok=%t", agent, ok)
	}
	if len(runtime.killed) != 1 || runtime.killed[0].PaneUID != "pan-alpha-codex" {
		t.Fatalf("exact kills = %#v", runtime.killed)
	}

	store = newFakeResourceStore(t)
	runtime = newFixturePaneDeleteRuntime()
	runtime.killErrs = map[string]error{"%31": errors.New("injected second Pane failure")}
	cmd = newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	_, _, err = runRoute(t, cmd, "pane", "uid:pan-alpha-zsh", "uid:pan-alpha-log", "--yes")
	if err == nil {
		t.Fatal("partial failure succeeded")
	}
	for _, want := range []string{"injected second Pane failure", "%30/window=@10/session=alpha($20)/pane-uid=pan-alpha-zsh", "retryable drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("partial failure = %q, want %q", err, want)
		}
	}
	// The Registry resources are untouched, but the intentional receipt this
	// operation recorded before killing %30 stays: that Pane really was ended
	// on purpose, and withdrawing the evidence would leave a real termination
	// with nothing to explain it.
	if !registryUIDs(store.registry)["pan-alpha-zsh"] || !registryUIDs(store.registry)["pan-alpha-log"] {
		t.Fatalf("partial failure removed a Registry Pane:\n%s", store.snapshot())
	}
	if len(runtime.killed) != 1 || runtime.killed[0].PaneID != "%30" {
		t.Fatalf("partial state killed=%#v", runtime.killed)
	}
	killedPane, _ := store.registry.Pane("pan-alpha-zsh")
	if killedPane.Status.LastTermination == nil ||
		killedPane.Status.LastTermination.Classification != coremetadata.TerminationIntentional {
		t.Fatalf("killed Pane lost its intentional receipt: %#v", killedPane.Status.LastTermination)
	}
}

func TestCallerContainingMultiPaneTombstoneFailureKeepsRegistryAndQueueUntouched(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	runtime.selfUID = "pan-alpha-zsh"
	runtime.tombstoneErr = errors.New("injected multi-target tombstone failure after exact rollback")
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	before := store.snapshot()

	out, _, err := runRoute(t, cmd,
		"pane", "uid:pan-alpha-zsh", "uid:pan-alpha-log", "--yes")
	if err == nil || !strings.Contains(err.Error(), "injected multi-target tombstone failure") {
		t.Fatalf("tombstone failure = %v", err)
	}
	// The snapshot covers termination evidence, so a recorded intent that was
	// not withdrawn would fail here. The commit count deliberately is not
	// asserted: the withdrawal is itself a commit.
	if out != "" || store.snapshot() != before ||
		len(runtime.killed) != 0 || len(runtime.queued) != 0 {
		t.Fatalf("tombstone failure mutated state: stdout=%q snapshot-changed=%t killed=%#v queued=%#v",
			out, store.snapshot() != before, runtime.killed, runtime.queued)
	}
}

func TestCallerContainingPaneStoreFailureRestoresEveryPrecommitTombstone(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	runtime.selfUID = "pan-alpha-zsh"
	cmd := newTestDeleteCommand(store, false, false, nil)
	cmd.panes = runtime
	// Commit 1 is the pre-mutation intentional receipt and commit 3 is its
	// withdrawal; both must run. The failure under test is commit 2, the
	// resource cascade between them.
	commit := cmd.store.update
	commits := 0
	cmd.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		commits++
		if commits != 2 {
			return commit(fn)
		}
		working := store.registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, err
		}
		return coremetadata.Registry{}, errors.New("injected post-tombstone store failure")
	}

	out, _, err := runRoute(t, cmd,
		"pane", "uid:pan-alpha-zsh", "uid:pan-alpha-log", "--yes")
	if err == nil {
		t.Fatal("post-tombstone store failure succeeded")
	}
	for _, want := range []string{"injected post-tombstone store failure", "were restored", "remain unchanged for retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("store failure = %q, want %q", err, want)
		}
	}
	if out != "" || len(runtime.tombstoned) != 2 ||
		len(runtime.restored) != 2 || len(runtime.queued) != 0 {
		t.Fatalf("store failure state: stdout=%q tombstoned=%#v restored=%#v queued=%#v",
			out, runtime.tombstoned, runtime.restored, runtime.queued)
	}
	// Every tombstone was rolled back, so no live Pane was ended and the
	// recorded intent has to be withdrawn with them.
	for _, uid := range []string{"pan-alpha-zsh", "pan-alpha-log"} {
		pane, ok := store.registry.Pane(uid)
		if !ok {
			t.Fatalf("Pane %s was removed by a failed delete", uid)
		}
		if pane.Status.LastTermination != nil {
			t.Fatalf("Pane %s kept a stale intentional receipt: %#v", uid, pane.Status.LastTermination)
		}
	}
}

func TestDeletedPaneTransportTombstoneCannotBeReimported(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	before := registry.Clone()
	binder := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{})
	reconciler := &registryReconciler{}
	uid, mirror, ok := reconciler.paneBindingFor(&registry, fixtureMutator(), "op-delete", "win-alpha-main",
		coremetadata.LegacyPane{UID: deletedPaneMirrorPrefix + "pan-deleted", CWD: "/srv/alpha"}, binder)
	if uid != "" || mirror || ok || !reflect.DeepEqual(registry, before) {
		t.Fatalf("tombstone reimport result uid=%q mirror=%t ok=%t changed=%t", uid, mirror, ok, !reflect.DeepEqual(registry, before))
	}
}
