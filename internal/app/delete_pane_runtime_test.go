package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
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
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
		"display-message", "-p", "-F", "#{socket_path}")] = testDeleteTarget.value + "\n"
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

func exactPanePlanFor(t *testing.T, registry coremetadata.Registry, kind coremetadata.Kind, targets ...string) deletePlan {
	t.Helper()
	plan := panePlanFor(t, registry, kind, targets...)
	plan.ExactUID = true
	return plan
}

func markPaneMissingRuntime(t *testing.T, registry *coremetadata.Registry, uid string) {
	t.Helper()
	pane, ok := registry.Pane(uid)
	if !ok {
		t.Fatalf("missing Pane fixture %s", uid)
	}
	pane.Status.Conditions = []coremetadata.Condition{{
		Type: coremetadata.ConditionMissingRuntime, Status: coremetadata.ConditionTrue,
		Reason: coremetadata.ReasonRuntimeUnbound, FirstObservedAt: resourceFixtureClock,
		LastTransitionAt: resourceFixtureClock,
	}}
}

type paneDeleteExitCommandFailure struct {
	failure inttmux.CommandFailure
}

func (e paneDeleteExitCommandFailure) Error() string {
	return "typed tmux command failure: " + e.failure.Stderr
}

func (e paneDeleteExitCommandFailure) CommandFailure() inttmux.CommandFailure {
	return e.failure
}

func (e paneDeleteExitCommandFailure) ExitCode() int { return 1 }

func TestPaneDeletePreflightFlattensExitCodeAfterTypedSocketClassification(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	plan := exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log")
	exitFailure := paneDeleteExitCommandFailure{failure: inttmux.CommandFailure{
		Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + testDeleteTarget.value,
	}}
	newRuntime := func() *tmuxPaneDeleteRuntime {
		runtime, runner, _ := newPaneRuntimeFixture(t, "")
		key := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
			"display-message", "-p", "-F", "#{socket_path}")
		runner.errors = map[string]error{key: exitFailure}
		return runtime
	}

	observationErr := newRuntime().observeSocketIdentity(context.Background())
	if !inttmux.IsNoServerFailure(observationErr) {
		t.Fatalf("direct observation lost typed no-server evidence: %v", observationErr)
	}
	var observedExit interface{ ExitCode() int }
	if !errors.As(observationErr, &observedExit) {
		t.Fatalf("direct observation lost subprocess exit identity: %v", observationErr)
	}

	_, preflightErr := newRuntime().preflight(context.Background(), registry, plan)
	if preflightErr == nil || !strings.Contains(preflightErr.Error(), "unavailable (no-server)") ||
		!strings.Contains(preflightErr.Error(), "absence is not Registry deletion authority") {
		t.Fatalf("preflight diagnostic = %v", preflightErr)
	}
	var escapedExit interface{ ExitCode() int }
	if errors.As(preflightErr, &escapedExit) {
		t.Fatalf("CLI-facing preflight leaked subprocess ExitCode identity: %T %v", escapedExit, preflightErr)
	}
	if inttmux.IsNoServerFailure(preflightErr) {
		t.Fatalf("CLI-facing preflight leaked internal typed no-server carrier: %v", preflightErr)
	}
}

func TestPaneDeletePreflightFlattensOtherSocketExitFailures(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	plan := exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log")
	runtime, runner, _ := newPaneRuntimeFixture(t, "")
	key := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
		"display-message", "-p", "-F", "#{socket_path}")
	runner.errors = map[string]error{key: paneDeleteExitCommandFailure{failure: inttmux.CommandFailure{
		Kind: inttmux.CommandFailureExit, Stderr: "failed to connect to server: permission denied",
	}}}

	_, err := runtime.preflight(context.Background(), registry, plan)
	if err == nil || !strings.Contains(err.Error(), "exact tmux socket observation failed") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("generic socket observation diagnostic = %v", err)
	}
	var escapedExit interface{ ExitCode() int }
	if errors.As(err, &escapedExit) {
		t.Fatalf("generic CLI-facing preflight leaked subprocess ExitCode identity: %T %v", escapedExit, err)
	}
}

func TestPaneDeleteDefaultRouteForgedServerRefusesBeforeFirstWrite(t *testing.T) {
	path := "/tmp/projmux-route/cloned-delete.sock"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):      "0\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):                  "0\n",
	}}
	runtime := &tmuxPaneDeleteRuntime{
		runner: runner, target: explicitTmuxTarget{flag: "-L", value: defaultAppSocket}, getenv: func(string) string { return "" },
	}
	registry := resourceFixtureRegistry(t)
	_, err := runtime.preflight(context.Background(), registry, panePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log"))
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("forged default delete error = %v", err)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, "kill-") || strings.Contains(joined, "set-option") {
			t.Fatalf("forged default delete reached a write: %#v", runner.calls)
		}
	}
}

func TestPaneDeletePreExecuteNoServerRefusesInsteadOfCompletingAbsence(t *testing.T) {
	runtime, runner, _ := newPaneRuntimeFixture(t, "")
	runtime.expectedSocketPath = testDeleteTarget.value
	runtime.expectedLogicalSocket = defaultAppSocket
	runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
	key := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{socket_path}")
	runner.errors = map[string]error{key: appTypedCommandFailure{inttmux.CommandFailure{Kind: inttmux.CommandFailureExit, Stderr: "no server running"}}}
	target := paneLiveDeleteTarget{PaneUID: "pan-alpha-log", PaneID: "%31", WindowUID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"}
	applied, err := runtime.killAll(context.Background(), []paneLiveDeleteTarget{target})
	if err == nil || applied != 0 {
		t.Fatalf("pre-execute no-server result = applied %d, err %v; want zero/refusal", applied, err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-pane") {
			t.Fatalf("pre-execute no-server reached kill: %#v", runner.calls)
		}
	}
}

type lastTargetDeleteRunner struct {
	kind   string
	killed bool
	calls  []recordedTmuxCall
}

func (r *lastTargetDeleteRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != testDeleteTarget.value {
		return nil, fmt.Errorf("last-target delete runner requires exact -S routing: %s %v", name, args)
	}
	command := args[2]
	if r.killed && command == "display-message" && args[len(args)-1] == "#{socket_path}" {
		return nil, appTypedCommandFailure{inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + testDeleteTarget.value,
		}}
	}
	switch command {
	case "display-message":
		if args[len(args)-1] == "#{socket_path}" {
			return []byte(testDeleteTarget.value + "\n"), nil
		}
		if args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		if r.kind == "pane" {
			return []byte(livePaneInventoryRow("$1", "alpha", "@10", "%31", "prj-alpha", "win-alpha-main", "pan-alpha-log")), nil
		}
		return []byte(liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main")), nil
	case "show-options":
		switch args[len(args)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte(defaultAppSocket + "\n"), nil
		}
	case "list-panes":
		return []byte(livePaneInventoryRow("$1", "@10", "%31", "pan-alpha-log")), nil
	case "list-windows":
		return []byte(liveInventoryRow("$1", "@10", "win-alpha-main")), nil
	case "kill-pane":
		if r.kind != "pane" || flagValue(args[2:], "-t") != "%31" {
			return nil, fmt.Errorf("unexpected last Pane kill: %v", args)
		}
		r.killed = true
		return nil, nil
	case "kill-window":
		if r.kind != "window" || flagValue(args[2:], "-t") != "@10" {
			return nil, fmt.Errorf("unexpected last Window kill: %v", args)
		}
		r.killed = true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected last-target delete command: %v", args)
}

func TestPaneDeleteSuccessfulLastKillAcceptsPostApplyNoServerReceipt(t *testing.T) {
	runner := &lastTargetDeleteRunner{kind: "pane"}
	runtime := statefulPaneRuntime(t, runner)
	runtime.expectedSocketPath = testDeleteTarget.value
	runtime.expectedLogicalSocket = defaultAppSocket
	target := paneLiveDeleteTarget{
		PaneUID: "pan-alpha-log", PaneID: "%31", WindowUID: "win-alpha-main", WindowID: "@10",
		SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
	}
	applied, err := runtime.killAll(context.Background(), []paneLiveDeleteTarget{target})
	if err != nil || applied != 1 || !runner.killed {
		t.Fatalf("last-Pane kill result = applied %d, killed %t, err %v; want one successful kill", applied, runner.killed, err)
	}
	kills := 0
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-pane") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("last-Pane kill calls = %d, want 1: %#v", kills, runner.calls)
	}
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
		lastPlan.endsWindows() != 0 || lastPlan.endsSessions() != 0 {
		t.Fatalf("last Pane plan = %#v", lastPlan)
	}
}

func TestPaneDeleteRuntimePreservesControlSessionOwnerChain(t *testing.T) {
	store := newFakeResourceStore(t)
	addControlReadRoot(t, store)
	inventory := livePaneInventoryRow("$3", "home", "@20", "%40", "", "win-home", "pan-home-shell") +
		livePaneInventoryRow("$3", "home", "@20", "%41", "", "win-home", "pan-home-agent")
	runtime, _, _ := newPaneRuntimeFixture(t, inventory)

	for _, row := range []struct {
		name string
		kind coremetadata.Kind
		uid  string
		pane string
	}{
		{name: "shell Pane", kind: coremetadata.KindPane, uid: "pan-home-shell", pane: "pan-home-shell"},
		{name: "Agent", kind: coremetadata.KindAgent, uid: "agt-home", pane: "pan-home-agent"},
	} {
		t.Run(row.name, func(t *testing.T) {
			live, err := runtime.preflight(context.Background(), store.registry,
				panePlanFor(t, store.registry, row.kind, row.uid))
			if err != nil {
				t.Fatalf("control-owned %s preflight: %v", row.name, err)
			}
			if len(live.Targets) != 1 {
				t.Fatalf("control-owned %s plan = %#v", row.name, live)
			}
			target := live.Targets[0]
			if target.PaneUID != row.pane || target.RootKind != coremetadata.KindControlSession ||
				target.RootUID != "ctl-home" || target.SessionName != "home" {
				t.Fatalf("control-owned %s lost owner chain: %#v", row.name, target)
			}
		})
	}
}

func TestPaneDeleteRuntimeFailsClosedOnMissingDuplicateForeignAndStaleMirrors(t *testing.T) {
	base := livePaneInventoryRow("$1", "alpha", "@10", "%30", "prj-alpha", "win-alpha-main", "pan-alpha-zsh")
	for _, test := range []struct {
		name      string
		inventory string
		want      string
	}{
		{name: "missing", inventory: livePaneInventoryRow("$1", "alpha", "@11", "%33", "prj-alpha", "win-alpha-review", "pan-alpha-review"), want: "no exact live tmux Pane mirror"},
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

func TestPaneDeleteRuntimeRegistryOnlyEvidenceTable(t *testing.T) {
	sibling := livePaneInventoryRow("$1", "alpha", "@11", "%33", "prj-alpha", "win-alpha-review", "pan-alpha-review")

	t.Run("MissingRuntime Pane exact uid", func(t *testing.T) {
		runtime, _, registry := newPaneRuntimeFixture(t, sibling)
		markPaneMissingRuntime(t, &registry, "pan-alpha-log")
		plan, err := runtime.preflight(context.Background(), registry,
			exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log"))
		if err != nil {
			t.Fatalf("registry-only Pane preflight: %v", err)
		}
		if len(plan.Targets) != 0 || len(plan.RegistryOnly) != 1 ||
			plan.RegistryOnly[0].Evidence != coremetadata.ConditionMissingRuntime || plan.SocketPath != testDeleteTarget.value {
			t.Fatalf("registry-only Pane plan = %#v", plan)
		}
	})

	t.Run("Offline Agent with retained MissingRuntime Pane", func(t *testing.T) {
		runtime, _, registry := newPaneRuntimeFixture(t, sibling)
		agent, _ := registry.Agent("agt-alpha-codex")
		agent.Status.Phase = coremetadata.PhaseOffline
		agent.Status.PaneRef = ""
		markPaneMissingRuntime(t, &registry, "pan-alpha-codex")
		plan, err := runtime.preflight(context.Background(), registry,
			exactPanePlanFor(t, registry, coremetadata.KindAgent, "agt-alpha-codex"))
		if err != nil {
			t.Fatalf("registry-only Agent preflight: %v", err)
		}
		if len(plan.Targets) != 0 || len(plan.RegistryOnly) != 1 ||
			plan.RegistryOnly[0].Evidence != "Offline+MissingRuntime" {
			t.Fatalf("registry-only Agent plan = %#v", plan)
		}
	})

	t.Run("Offline Agent without retained Pane", func(t *testing.T) {
		runtime, _, registry := newPaneRuntimeFixture(t, sibling)
		plan, err := runtime.preflight(context.Background(), registry,
			exactPanePlanFor(t, registry, coremetadata.KindAgent, "agt-beta-codex"))
		if err != nil {
			t.Fatalf("registry-only empty Agent preflight: %v", err)
		}
		if len(plan.Targets) != 0 || len(plan.RegistryOnly) != 1 || plan.RegistryOnly[0].Evidence != "Offline" {
			t.Fatalf("registry-only empty Agent plan = %#v", plan)
		}
	})

	for _, test := range []struct {
		name      string
		plan      func(coremetadata.Registry) deletePlan
		prepare   func(*coremetadata.Registry)
		want      string
		inventory string
	}{
		{
			name: "name selector cannot authorize zero mirror",
			plan: func(reg coremetadata.Registry) deletePlan {
				return panePlanFor(t, reg, coremetadata.KindPane, "pan-alpha-log")
			},
			prepare: func(reg *coremetadata.Registry) { markPaneMissingRuntime(t, reg, "pan-alpha-log") },
			want:    "no exact live tmux Pane mirror", inventory: sibling,
		},
		{
			name: "absence without MissingRuntime",
			plan: func(reg coremetadata.Registry) deletePlan {
				return exactPanePlanFor(t, reg, coremetadata.KindPane, "pan-alpha-log")
			},
			want: "no exact live tmux Pane mirror", inventory: sibling,
		},
		{
			name: "empty inventory is not authority",
			plan: func(reg coremetadata.Registry) deletePlan {
				return exactPanePlanFor(t, reg, coremetadata.KindPane, "pan-alpha-log")
			},
			prepare: func(reg *coremetadata.Registry) { markPaneMissingRuntime(t, reg, "pan-alpha-log") },
			want:    "absence is not Registry deletion authority", inventory: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, registry := newPaneRuntimeFixture(t, test.inventory)
			if test.prepare != nil {
				test.prepare(&registry)
			}
			_, err := runtime.preflight(context.Background(), registry, test.plan(registry))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("MissingRuntime evidence with a unique live mirror uses exact live delete", func(t *testing.T) {
		runtime, _, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())
		markPaneMissingRuntime(t, &registry, "pan-alpha-log")
		plan, err := runtime.preflight(context.Background(), registry,
			exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log"))
		if err != nil {
			t.Fatalf("live rebound preflight: %v", err)
		}
		if len(plan.Targets) != 1 || len(plan.RegistryOnly) != 0 || plan.Targets[0].PaneID != "%31" {
			t.Fatalf("live rebound plan = %#v", plan)
		}
	})
}

func TestPaneDeleteStandaloneSocketAuthorizesOnlyRegistryOnlyEvidence(t *testing.T) {
	sibling := livePaneInventoryRow("$1", "alpha", "@11", "%33", "prj-alpha", "win-alpha-review", "pan-alpha-review")
	for _, test := range []struct {
		name    string
		kind    coremetadata.Kind
		uid     string
		prepare func(*coremetadata.Registry)
		want    string
	}{
		{
			name: "MissingRuntime Pane", kind: coremetadata.KindPane, uid: "pan-alpha-log", want: coremetadata.ConditionMissingRuntime,
			prepare: func(registry *coremetadata.Registry) { markPaneMissingRuntime(t, registry, "pan-alpha-log") },
		},
		{
			name: "Offline Agent", kind: coremetadata.KindAgent, uid: "agt-alpha-codex", want: "Offline+MissingRuntime",
			prepare: func(registry *coremetadata.Registry) {
				agent, _ := registry.Agent("agt-alpha-codex")
				agent.Status.Phase = coremetadata.PhaseOffline
				agent.Status.PaneRef = ""
				markPaneMissingRuntime(t, registry, "pan-alpha-codex")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, runner, registry := newPaneRuntimeFixture(t, sibling)
			test.prepare(&registry)
			// If the preflight accidentally upgrades this read-only route to
			// mutation authority, the standalone marker answer must refuse it.
			runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
				"show-options", "-gqv", tmuxopts.AppGlobal)] = "\n"

			first, err := runtime.preflight(context.Background(), registry,
				exactPanePlanFor(t, registry, test.kind, test.uid))
			if err != nil {
				t.Fatalf("standalone Registry-only preflight: %v", err)
			}
			second, err := runtime.preflight(context.Background(), registry,
				exactPanePlanFor(t, registry, test.kind, test.uid))
			if err != nil || second.signature() != first.signature() {
				t.Fatalf("standalone locked reobservation = %#v, err %v; first %#v", second, err, first)
			}
			if len(first.Targets) != 0 || len(first.RegistryOnly) != 1 || first.RegistryOnly[0].Evidence != test.want {
				t.Fatalf("standalone Registry-only plan = %#v", first)
			}
			for _, call := range runner.calls {
				argv := tmuxCommandArgv(call.args)
				if len(argv) > 0 && (argv[0] == "kill-pane" || argv[0] == "set-option" || argv[0] == "set-environment" || argv[0] == "run-shell") {
					t.Fatalf("standalone Registry-only plan reached a tmux write: %#v", runner.calls)
				}
			}
		})
	}
}

func TestPaneDeleteStandaloneSocketRefusesLiveMirrorBeforeWrite(t *testing.T) {
	runtime, runner, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())
	markPaneMissingRuntime(t, &registry, "pan-alpha-log")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
		"show-options", "-gqv", tmuxopts.AppGlobal)] = "\n"

	_, err := runtime.preflight(context.Background(), registry,
		exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log"))
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("standalone live-mirror preflight error = %v, want mutation-authority refusal", err)
	}
	for _, call := range runner.calls {
		argv := tmuxCommandArgv(call.args)
		if len(argv) > 0 && (argv[0] == "kill-pane" || argv[0] == "set-option" || argv[0] == "set-environment" || argv[0] == "run-shell") {
			t.Fatalf("standalone live-mirror refusal reached a tmux write: %#v", runner.calls)
		}
	}
}

func TestPaneDeleteRuntimeRefusesNoServerAndSignsOwnerGeneration(t *testing.T) {
	sibling := livePaneInventoryRow("$1", "alpha", "@11", "%33", "prj-alpha", "win-alpha-review", "pan-alpha-review")
	runtime, runner, registry := newPaneRuntimeFixture(t, sibling)
	markPaneMissingRuntime(t, &registry, "pan-alpha-log")
	plan := exactPanePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log")
	first, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("initial preflight: %v", err)
	}

	pane, _ := registry.Pane("pan-alpha-log")
	pane.Status.Activation.Generation = "gen-replaced"
	second, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("generation revalidation: %v", err)
	}
	if first.signature() == second.signature() {
		t.Fatal("activation generation change did not change the signed plan")
	}

	pane.Status.Activation.Generation = ""
	pane.Metadata.OwnerRef.UID = "win-alpha-review"
	third, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("owner revalidation: %v", err)
	}
	if first.signature() == third.signature() {
		t.Fatal("owner change did not change the signed plan")
	}

	socketKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value,
		"display-message", "-p", "-F", "#{socket_path}")
	runner.errors = map[string]error{socketKey: appTypedCommandFailure{failure: inttmux.CommandFailure{
		Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + testDeleteTarget.value,
	}}}
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil ||
		!strings.Contains(err.Error(), "unavailable (no-server)") {
		t.Fatalf("typed no-server error = %v", err)
	}

	runner.errors = map[string]error{socketKey: errors.New("permission denied")}
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil ||
		!strings.Contains(err.Error(), "reobserve exact socket identity") {
		t.Fatalf("permission error = %v", err)
	}

	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{@projmux_project_uid}", "#{@projmux_window_uid}", "#{@projmux_pane_uid}")
	inventoryKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "list-panes", "-a", "-F", format)
	runner.errors = map[string]error{inventoryKey: errors.New("inventory unavailable")}
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil ||
		!strings.Contains(err.Error(), "inventory exact tmux socket") {
		t.Fatalf("inventory error = %v", err)
	}
}

func TestPaneDeleteAuthorityUsesActivationGenerationNotTerminationReceipt(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	plan := panePlanFor(t, registry, coremetadata.KindAgent, "agt-alpha-codex")
	before, err := paneDeleteAuthoritySignature(registry, plan)
	if err != nil {
		t.Fatalf("initial authority: %v", err)
	}

	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Status.LastTermination = &coremetadata.TerminationEvidence{Generation: "receipt-written-by-delete"}
	afterReceipt, err := paneDeleteAuthoritySignature(registry, plan)
	if err != nil {
		t.Fatalf("receipt authority: %v", err)
	}
	if afterReceipt != before {
		t.Fatal("delete's own termination receipt changed live authority")
	}

	pane, _ := registry.Pane("pan-alpha-codex")
	pane.Status.Activation.Generation = "replacement-materialization"
	afterGeneration, err := paneDeleteAuthoritySignature(registry, plan)
	if err != nil {
		t.Fatalf("generation authority: %v", err)
	}
	if afterGeneration == before {
		t.Fatal("current Pane activation generation did not change live authority")
	}
}

func TestNamedLastPaneCreatesReplacementWithoutRootCascadeConfirmation(t *testing.T) {
	store := newFakeResourceStore(t)
	runtime := newFixturePaneDeleteRuntime()
	var prompts []string
	cmd := newTestDeleteCommand(store, false, false, &prompts)
	cmd.panes = runtime
	out, _, err := runRoute(t, cmd, "pane", "uid:pan-beta-zsh")
	if err != nil {
		t.Fatalf("named last-Pane replacement = %v", err)
	}
	if len(runtime.killed) != 1 || len(runtime.replacements) != 1 || len(prompts) != 0 ||
		!strings.Contains(out, "replacement shell") {
		t.Fatalf("named last-Pane outcome killed=%+v replacements=%+v prompts=%v out=%q", runtime.killed, runtime.replacements, prompts, out)
	}
}

func TestPaneDeleteRuntimeImplicitTargetRequiresExactCallerSocketAndPane(t *testing.T) {
	runtime, runner, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())
	runtime.getenv = func(name string) string {
		switch name {
		case "TMUX":
			return testDeleteTarget.value + ",123,0"
		case "TMUX_PANE":
			return "%31"
		}
		return ""
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{socket_path}")] = testDeleteTarget.value + "\n"
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{pid}")] = "123\n"
	format := tmuxRowFormat("#{socket_path}", "#{pane_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-t", "%31", "-F", format)] =
		livePaneInventoryRow(testDeleteTarget.value, "%31")
	authorityFormat := tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-t", "%31", "-F", authorityFormat)] =
		livePaneInventoryRow(testDeleteTarget.value, "123", "$1", "@10", "%31")
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
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil || !strings.Contains(err.Error(), "exact socket drifted") {
		t.Fatalf("foreign socket error = %v", err)
	}
}

func TestPaneDeleteRuntimeProducerAnchorBindsRouteWithoutInheritedPaneEnv(t *testing.T) {
	runtime, runner, registry := newPaneRuntimeFixture(t, paneRuntimeInventory())
	runtime.getenv = func(name string) string {
		if name == "TMUX" {
			return testDeleteTarget.value + ",123,0"
		}
		return ""
	}
	runtime.useRouteAnchor("%31")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-F", "#{pid}")] = "123\n"
	authorityFormat := tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "display-message", "-p", "-t", "%31", "-F", authorityFormat)] =
		livePaneInventoryRow(testDeleteTarget.value, "123", "$1", "@10", "%31")

	plan := panePlanFor(t, registry, coremetadata.KindPane, "pan-alpha-log")
	if _, err := runtime.preflight(context.Background(), registry, plan); err != nil {
		t.Fatalf("producer-anchored Pane preflight: %v", err)
	}
	if err := runtime.guardSocketIdentity(context.Background()); err != nil {
		t.Fatalf("producer-anchored mutation authority: %v", err)
	}
	if runtime.routeAuthority == nil || runtime.routeAuthority.PaneID != "%31" || runtime.routeAuthority.ServerPID != "123" {
		t.Fatalf("producer-anchored delete authority = %#v", runtime.routeAuthority)
	}
}

type lifecycleSiblingCleanupRunner struct {
	killed bool
	calls  []recordedTmuxCall
}

func (r *lifecycleSiblingCleanupRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != testDeleteTarget.value {
		return nil, fmt.Errorf("lifecycle cleanup requires exact -S routing: %s %v", name, args)
	}
	switch args[2] {
	case "display-message":
		switch args[len(args)-1] {
		case "#{socket_path}":
			return []byte(testDeleteTarget.value + "\n"), nil
		case "#{pid}":
			return []byte("4242\n"), nil
		case tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
			"#{@projmux_project_uid}", "#{@projmux_window_uid}", "#{@projmux_pane_uid}"):
			return []byte(livePaneInventoryRow("$1", "alpha", "@10", "%31", "prj-alpha", "win-alpha-main", "pan-alpha-log")), nil
		}
	case "show-options":
		switch args[len(args)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte(defaultAppSocket + "\n"), nil
		}
	case "list-panes":
		if r.killed {
			return []byte(livePaneInventoryRow("$1", "@10", "%30", "pan-alpha-zsh")), nil
		}
		return []byte(
			livePaneInventoryRow("$1", "@10", "%30", "pan-alpha-zsh") +
				livePaneInventoryRow("$1", "@10", "%31", "pan-alpha-log"),
		), nil
	case "kill-pane":
		if flagValue(args[3:], "-t") != "%31" {
			return nil, fmt.Errorf("unexpected lifecycle cleanup target: %v", args)
		}
		r.killed = true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected lifecycle cleanup command: %v", args)
}

func TestLifecycleSiblingCleanupBindsExistingExactSocketBeforeKillPlan(t *testing.T) {
	target, err := tmuxSocketPathTarget(testDeleteTarget.value)
	if err != nil {
		t.Fatal(err)
	}
	runner := &lifecycleSiblingCleanupRunner{}
	runtime := &tmuxPaneDeleteRuntime{runner: runner, target: target, getenv: func(string) string { return "" }}
	inventory := &exactLifecycleInventory{replacements: runtime}
	cleanup := paneLiveDeleteTarget{
		PaneUID: "pan-alpha-log", PaneID: "%31", WindowUID: "win-alpha-main", WindowID: "@10",
		SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
	}

	if err := inventory.CleanupLifecycleDeadPane(context.Background(), cleanup); err != nil {
		t.Fatalf("lifecycle sibling cleanup: %v", err)
	}
	if !runner.killed || runtime.routeAnchor != "%31" || runtime.expectedSocketPath != testDeleteTarget.value || runtime.routeAuthority == nil ||
		runtime.routeAuthority.ServerPID != "4242" {
		t.Fatalf("cleanup route killed=%t anchor=%q path=%q authority=%+v",
			runner.killed, runtime.routeAnchor, runtime.expectedSocketPath, runtime.routeAuthority)
	}
	kills := 0
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-pane") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("exact lifecycle cleanup kill calls = %d, want 1: %#v", kills, runner.calls)
	}
}

func TestPaneDeleteRuntimeQueueRevalidatesTombstonesAndUsesExactSocket(t *testing.T) {
	runtime, _, _ := newPaneRuntimeFixture(t, "")
	runtime.expectedSocketPath = testDeleteTarget.value
	runtime.expectedLogicalSocket = defaultAppSocket
	runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
	target := paneLiveDeleteTarget{PaneUID: "pan-alpha-log", PaneID: "%31", WindowUID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"}
	action := runtime.mutationAction(mutationQueuePaneKill, target, "", "")
	action.Target.UID = "deleted:" + target.PaneUID
	action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: testDeleteTarget.value, LogicalSocket: defaultAppSocket,
		RouteAuthority: action.Target.RouteAuthority,
		ExpectedUID:    action.Target.UID, SessionID: target.SessionID, WindowID: target.WindowID}
	action.Queue.Marker = runtimeMutationQueueMarker(action)
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"set-environment -g", "run-shell -b", "-S '" + testDeleteTarget.value + "'", "kill-pane -t %31", tmuxopts.PaneUID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queued Pane argv = %q, want %q", joined, want)
		}
	}
}

type statefulPaneDeleteRunner struct {
	options     map[string]string
	environment map[string]string
	failSet     map[string]error
	failQueue   map[string]error
	calls       []recordedTmuxCall
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
	case "display-message":
		if len(args) > 0 && args[len(args)-1] == "#{socket_path}" {
			return []byte(testDeleteTarget.value + "\n"), nil
		}
		if len(args) > 0 && args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		return []byte(livePaneInventoryRow("$1", "alpha", "@10", paneID, "prj-alpha", "win-alpha-main", r.options[paneID])), nil
	case "show-options":
		if args[len(args)-1] == tmuxopts.AppGlobal {
			return []byte("1\n"), nil
		}
		if args[len(args)-1] == runtimeMutationSocketNameOption {
			return []byte(defaultAppSocket + "\n"), nil
		}
		return []byte(r.options[paneID] + "\n"), nil
	case "list-panes":
		var out strings.Builder
		for id, uid := range r.options {
			out.WriteString(livePaneInventoryRow("$1", "@10", id, uid))
		}
		return []byte(out.String()), nil
	case "show-environment":
		var out strings.Builder
		for key, value := range r.environment {
			fmt.Fprintf(&out, "%s=%s\n", key, value)
		}
		return []byte(out.String()), nil
	case "set-environment":
		if r.environment == nil {
			r.environment = map[string]string{}
		}
		if slices.Contains(args, "-gu") {
			delete(r.environment, args[len(args)-1])
			return nil, nil
		}
		if len(args) < 6 {
			return nil, fmt.Errorf("stateful Pane delete runner: malformed set-environment %v", args)
		}
		r.environment[args[4]] = args[5]
		joined := strings.Join(args, " ")
		for id, err := range r.failQueue {
			if strings.Contains(joined, shellQuote(id)) {
				return nil, err
			}
		}
		return nil, nil
	case "set-option":
		value := args[len(args)-1]
		if err := r.failSet[paneSetFailureKey(paneID, value)]; err != nil {
			return nil, err
		}
		r.options[paneID] = value
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
	return &tmuxPaneDeleteRuntime{
		runner: runner, target: target, getenv: func(string) string { return "" },
		expectedSocketPath: testDeleteTarget.value, expectedLogicalSocket: defaultAppSocket,
		routeAuthority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
	}
}

func multiSelfDeleteTargets() []paneLiveDeleteTarget {
	return []paneLiveDeleteTarget{
		{PaneUID: "pan-alpha-zsh", PaneID: "%30", WindowUID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha", Self: true},
		{PaneUID: "pan-alpha-log", PaneID: "%31", WindowUID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"},
		{PaneUID: "pan-alpha-codex", PaneID: "%32", WindowUID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"},
	}
}

func TestPaneDeleteTombstoneAndRestoreExecuteOnceThenReplanEmpty(t *testing.T) {
	target := multiSelfDeleteTargets()[0]
	runner := &statefulPaneDeleteRunner{options: map[string]string{target.PaneID: target.PaneUID}, failSet: map[string]error{}, failQueue: map[string]error{}}
	runtime := statefulPaneRuntime(t, runner)
	if err := runtime.tombstoneSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("first tombstone: %v", err)
	}
	if got := runner.options[target.PaneID]; got != deletedPaneMirrorPrefix+target.PaneUID {
		t.Fatalf("tombstone mirror = %q", got)
	}
	firstWrites := len(slicesDeletePaneSetWrites(runner.calls))
	if err := runtime.tombstoneSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("repeat tombstone: %v", err)
	}
	if got := len(slicesDeletePaneSetWrites(runner.calls)); got != firstWrites {
		t.Fatalf("repeat tombstone writes = %d, want %d", got, firstWrites)
	}
	if err := runtime.restoreSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("first restore: %v", err)
	}
	restoredWrites := len(slicesDeletePaneSetWrites(runner.calls))
	if err := runtime.restoreSelfKill(context.Background(), []paneLiveDeleteTarget{target}); err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if got := len(slicesDeletePaneSetWrites(runner.calls)); got != restoredWrites {
		t.Fatalf("repeat restore writes = %d, want %d", got, restoredWrites)
	}
}

func slicesDeletePaneSetWrites(calls []recordedTmuxCall) []recordedTmuxCall {
	var writes []recordedTmuxCall
	for _, call := range calls {
		if slicesHas(call.args, "set-option") {
			writes = append(writes, call)
		}
	}
	return writes
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
	for _, want := range []string{"live would kill tmux pane %34", "would create a replacement shell in Window @12", "Window uid=win-beta-main and name are preserved"} {
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
