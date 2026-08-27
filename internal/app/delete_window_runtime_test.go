package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func deleteWindowRuntimePlan(uid string) deletePlan {
	return deletePlan{
		Kind: coremetadata.KindWindow,
		Targets: []deleteTarget{{Match: selector.Match{
			Kind: coremetadata.KindWindow, UID: uid,
		}}},
	}
}

func liveInventoryRow(fields ...string) string {
	return strings.Join(fields, tmuxRowSepFormat) + "\n"
}

func newWindowRuntimeFixture(t *testing.T, inventory string) (*tmuxWindowDeleteRuntime, *recordingTmuxRunner, coremetadata.Registry) {
	t.Helper()
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	// The exact target matches what the route resolves from the hermetic
	// $TMUX in newTestDeleteCommand, so a runtime fixture and a full route
	// invocation address the same isolated server.
	target, err := tmuxSocketPathTarget(testDeleteTarget.Value)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &tmuxWindowDeleteRuntime{
		runner: runner, target: target, getenv: func(string) string { return "" },
		expectedSocketPath: testDeleteTarget.Value, expectedLogicalSocket: defaultAppSocket,
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{socket_path}")] = testDeleteTarget.Value + "\n"
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{@projmux_project_uid}", "#{@projmux_window_uid}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "list-windows", "-a", "-F", format)] = inventory
	return runtime, runner, resourceFixtureRegistry(t)
}

func TestWindowDeleteRuntimePreflightExactBindingAndSessionImpact(t *testing.T) {
	inventory := liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main") +
		liveInventoryRow("$1", "alpha", "@11", "prj-alpha", "win-alpha-review") +
		liveInventoryRow("$2", "beta", "@12", "prj-beta", "win-beta-main")
	runtime, _, registry := newWindowRuntimeFixture(t, inventory)

	mainPlan, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-alpha-main"))
	if err != nil {
		t.Fatalf("two-Window preflight error = %v", err)
	}
	if len(mainPlan.Targets) != 1 || mainPlan.Targets[0].WindowID != "@10" || mainPlan.Targets[0].EndsSession {
		t.Fatalf("two-Window live plan = %#v", mainPlan)
	}
	lastPlan, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-beta-main"))
	if err != nil {
		t.Fatalf("last-Window preflight error = %v", err)
	}
	if len(lastPlan.Targets) != 1 || !lastPlan.Targets[0].EndsSession || lastPlan.endsSessions() != 1 {
		t.Fatalf("last-Window live plan = %#v", lastPlan)
	}
}

func TestWindowDeletePreExecuteNoServerRefusesInsteadOfCompletingAbsence(t *testing.T) {
	runtime, runner, _ := newWindowRuntimeFixture(t, "")
	runtime.expectedSocketPath = testDeleteTarget.Value
	runtime.expectedLogicalSocket = defaultAppSocket
	runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
	key := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{socket_path}")
	runner.errors = map[string]error{key: appTypedCommandFailure{inttmux.CommandFailure{Kind: inttmux.CommandFailureExit, Stderr: "no server running"}}}
	target := windowLiveDeleteTarget{UID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"}
	applied, err := runtime.killAll(context.Background(), []windowLiveDeleteTarget{target})
	if err == nil || applied != 0 {
		t.Fatalf("pre-execute no-server result = applied %d, err %v; want zero/refusal", applied, err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-window") {
			t.Fatalf("pre-execute no-server reached kill: %#v", runner.calls)
		}
	}
}

func TestWindowDeleteSuccessfulLastKillAcceptsPostApplyNoServerReceipt(t *testing.T) {
	runner := &lastTargetDeleteRunner{kind: "window"}
	target, err := tmuxSocketPathTarget(testDeleteTarget.Value)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &tmuxWindowDeleteRuntime{
		runner: runner, target: target, getenv: func(string) string { return "" },
		expectedSocketPath: testDeleteTarget.Value, expectedLogicalSocket: defaultAppSocket,
		routeAuthority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
	}
	live := windowLiveDeleteTarget{
		UID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1",
		RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
	}
	applied, err := runtime.killAll(context.Background(), []windowLiveDeleteTarget{live})
	if err != nil || applied != 1 || !runner.killed {
		t.Fatalf("last-Window kill result = applied %d, killed %t, err %v; want one successful kill", applied, runner.killed, err)
	}
	kills := 0
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-window") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("last-Window kill calls = %d, want 1: %#v", kills, runner.calls)
	}
}

func TestWindowDeleteRuntimePreservesControlSessionRootAuthority(t *testing.T) {
	runtime, _, _ := newWindowRuntimeFixture(t,
		liveInventoryRow("$3", "home", "@20", "", "win-home"))
	store := newFakeResourceStore(t)
	addControlReadRoot(t, store)
	registry := store.registry

	live, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-home"))
	if err != nil {
		t.Fatalf("control-owned Window preflight: %v", err)
	}
	if len(live.Targets) != 1 {
		t.Fatalf("control-owned live plan = %#v", live)
	}
	target := live.Targets[0]
	if target.RootKind != coremetadata.KindControlSession || target.RootUID != "ctl-home" ||
		target.SessionName != "home" || !target.EndsSession {
		t.Fatalf("control-owned target lost root authority: %#v", target)
	}

	runtime, _, _ = newWindowRuntimeFixture(t,
		liveInventoryRow("$3", "home", "@20", "prj-alpha", "win-home"))
	_, err = runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-home"))
	if err == nil || !strings.Contains(err.Error(), "ControlSession owner scope") ||
		!strings.Contains(err.Error(), "conflicting Project uid") || strings.Contains(err.Error(), "foreign Project") {
		t.Fatalf("control mirror conflict diagnostic = %v", err)
	}
}

func TestWindowDeleteRuntimePreflightAllowsExplicitOfflineWindow(t *testing.T) {
	runtime, runner, registry := newWindowRuntimeFixture(t,
		liveInventoryRow("$1", "alpha", "@11", "prj-alpha", "win-alpha-review")+
			liveInventoryRow("$2", "beta", "@12", "prj-beta", "win-beta-main"))

	live, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-alpha-main"))
	if err != nil {
		t.Fatalf("offline preflight error = %v", err)
	}
	if len(live.Targets) != 0 || live.signature() != "" {
		t.Fatalf("offline live plan = %#v, want no tmux targets", live)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "kill-window") {
			t.Fatalf("offline preflight mutated tmux: %#v", runner.calls)
		}
	}
}

func TestDeleteWindowTreatsOnlyTypedNoServerAsOffline(t *testing.T) {
	inventoryFormat := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{@projmux_project_uid}", "#{@projmux_window_uid}")
	inventoryKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "list-windows", "-a", "-F", inventoryFormat)

	t.Run("typed no-server permits Registry-only cascade", func(t *testing.T) {
		store := newFakeResourceStore(t)
		runtime, runner, _ := newWindowRuntimeFixture(t, "")
		runtime.getenv = func(name string) string {
			switch name {
			case "TMUX":
				return "/tmp/foreign.sock,123,0"
			case "TMUX_PANE":
				return "%99"
			}
			return ""
		}
		runner.errors = map[string]error{inventoryKey: appTypedCommandFailure{failure: inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on /tmp/isolated-delete",
		}}}
		cmd := newTestDeleteCommand(store, false, false, nil)
		cmd.windows = runtime

		stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
		if err != nil {
			t.Fatalf("typed no-server delete error = %v", err)
		}
		if _, ok := store.registry.Window("win-alpha-main"); ok {
			t.Fatal("typed no-server delete left offline Window")
		}
		if !strings.Contains(stdout, "registry-only deleted this Window; no tmux Window was killed") {
			t.Fatalf("typed no-server result = %q", stdout)
		}
		for _, call := range runner.calls {
			if strings.Contains(strings.Join(call.args, " "), "kill-window") {
				t.Fatalf("typed no-server delete mutated tmux: %#v", runner.calls)
			}
			if strings.Contains(strings.Join(call.args, " "), "display-message") && !slicesHas(call.args, "#{socket_path}") {
				t.Fatalf("typed no-server delete probed caller state after authoritative absent-server inventory: %#v", runner.calls)
			}
		}
	})

	t.Run("plain lookalike remains fail-closed", func(t *testing.T) {
		store := newFakeResourceStore(t)
		runtime, runner, _ := newWindowRuntimeFixture(t, "")
		runner.errors = map[string]error{inventoryKey: errors.New("no server running on /tmp/isolated-delete")}
		cmd := newTestDeleteCommand(store, false, false, nil)
		cmd.windows = runtime
		before := store.snapshot()

		stdout, _, err := runRoute(t, cmd, "window", "uid:win-alpha-main", "--yes")
		if err == nil || !strings.Contains(err.Error(), "inventory exact tmux socket") {
			t.Fatalf("plain lookalike error = %v", err)
		}
		if stdout != "" || store.writes != 0 || store.snapshot() != before {
			t.Fatalf("plain lookalike changed state: stdout=%q writes=%d", stdout, store.writes)
		}
	})
}

func TestWindowDeleteRuntimeStillRefusesImplicitOfflineWindow(t *testing.T) {
	runtime, _, registry := newWindowRuntimeFixture(t, "")
	runtime.getenv = func(name string) string {
		switch name {
		case "TMUX":
			return testDeleteTarget.Value + ",123,0"
		case "TMUX_PANE":
			return "%7"
		}
		return ""
	}
	format := tmuxRowFormat("#{socket_path}", "#{window_id}")
	runtime.runner.(*recordingTmuxRunner).outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{socket_path}")] =
		testDeleteTarget.Value + "\n"
	runtime.runner.(*recordingTmuxRunner).outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-t", "%7", "-F", format)] =
		liveInventoryRow(testDeleteTarget.Value, "@10")
	plan := deleteWindowRuntimePlan("win-alpha-main")
	plan.Implicit = true

	_, err := runtime.preflight(context.Background(), registry, plan)
	if err == nil || !strings.Contains(err.Error(), "no exact live tmux Window mirror") {
		t.Fatalf("implicit offline preflight error = %v", err)
	}
}

func TestWindowDeleteRuntimeFailsClosedOnMissingDuplicateForeignAndStaleMirrors(t *testing.T) {
	base := liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main")
	for _, test := range []struct {
		name      string
		inventory string
		want      string
	}{
		{name: "duplicate exact Window mirror", inventory: base + liveInventoryRow("$2", "alpha", "@12", "prj-alpha", "win-alpha-main"), want: "2 live tmux Window mirrors"},
		{name: "foreign Project mirror", inventory: liveInventoryRow("$2", "beta", "@10", "prj-beta", "win-alpha-main"), want: "foreign Project uid"},
		{name: "stale session projection", inventory: liveInventoryRow("$1", "renamed", "@10", "prj-alpha", "win-alpha-main"), want: "stale session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, runner, registry := newWindowRuntimeFixture(t, test.inventory)
			_, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-alpha-main"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want %q", err, test.want)
			}
			for _, call := range runner.calls {
				if strings.Contains(strings.Join(call.args, " "), "kill-window") {
					t.Fatalf("failed preflight mutated tmux: %#v", runner.calls)
				}
			}
		})
	}
}

func TestWindowDeleteRuntimeAllowsAbsentOptionalProjectMirror(t *testing.T) {
	runtime, _, registry := newWindowRuntimeFixture(t,
		liveInventoryRow("$1", "alpha", "@10", "", "win-alpha-main"))
	live, err := runtime.preflight(context.Background(), registry, deleteWindowRuntimePlan("win-alpha-main"))
	if err != nil {
		t.Fatalf("preflight with absent optional Project mirror error = %v", err)
	}
	if len(live.Targets) != 1 || live.Targets[0].RootKind != coremetadata.KindProject || live.Targets[0].RootUID != "prj-alpha" {
		t.Fatalf("live plan = %#v", live)
	}
}

func TestWindowDeleteRuntimeImplicitTargetRequiresExactCallerSocketAndWindow(t *testing.T) {
	inventory := liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main")
	runtime, runner, registry := newWindowRuntimeFixture(t, inventory)
	runtime.getenv = func(name string) string {
		switch name {
		case "TMUX":
			return testDeleteTarget.Value + ",123,0"
		case "TMUX_PANE":
			return "%7"
		}
		return ""
	}
	format := tmuxRowFormat("#{socket_path}", "#{window_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{socket_path}")] =
		testDeleteTarget.Value + "\n"
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{pid}")] = "123\n"
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-t", "%7", "-F", format)] =
		liveInventoryRow(testDeleteTarget.Value, "@10")
	authorityFormat := tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-t", "%7", "-F", authorityFormat)] =
		liveInventoryRow(testDeleteTarget.Value, "123", "$1", "@10", "%7")
	plan := deleteWindowRuntimePlan("win-alpha-main")
	plan.Implicit = true

	live, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("implicit preflight error = %v", err)
	}
	if len(live.Targets) != 1 || !live.Targets[0].Self {
		t.Fatalf("implicit live plan = %#v", live)
	}

	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "display-message", "-p", "-F", "#{socket_path}")] =
		"/tmp/foreign.sock\n"
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil || !strings.Contains(err.Error(), "exact socket drifted") {
		t.Fatalf("foreign caller socket error = %v", err)
	}
}

func TestWindowDeleteRuntimeQueueUsesQuotedExactSocketAndWindow(t *testing.T) {
	runtime, _, _ := newWindowRuntimeFixture(t, "")
	runtime.expectedSocketPath = testDeleteTarget.Value
	runtime.expectedLogicalSocket = defaultAppSocket
	runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
	target := windowLiveDeleteTarget{UID: "win-alpha-main", WindowID: "@12", SessionName: "alpha", SessionID: "$1", RootKind: coremetadata.KindProject, RootUID: "prj-alpha"}
	action := runtime.mutationAction(mutationQueueWindowKill, target, target.UID)
	action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: testDeleteTarget.Value, LogicalSocket: defaultAppSocket,
		RouteAuthority: action.Target.RouteAuthority,
		ExpectedUID:    target.UID, SessionID: target.SessionID, WindowID: target.WindowID}
	action.Queue.Marker = runtimeMutationQueueMarker(action)
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"set-environment -g", "run-shell -b", "-S '" + testDeleteTarget.Value + "'", "kill-window -t @12", tmuxopts.WindowUID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queued Window argv = %q, want %q", joined, want)
		}
	}
}

func TestWindowDeleteRuntimeQueueRevalidatesEveryMirrorBeforeQueueing(t *testing.T) {
	runtime, runner, _ := newWindowRuntimeFixture(t, "")
	runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
	targets := []windowLiveDeleteTarget{
		{UID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1"},
		{UID: "win-alpha-review", WindowID: "@11", SessionName: "alpha", SessionID: "$1"},
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "show-options", "-wqv", "-t", "@10", "@projmux_window_uid")] = "win-alpha-main\n"
	effectFormat := tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "list-windows", "-a", "-F", effectFormat)] =
		liveInventoryRow("$1", "@10", "win-alpha-main") + liveInventoryRow("$1", "@11", "win-alpha-review")
	secondKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "show-options", "-wqv", "-t", "@11", "@projmux_window_uid")

	for _, test := range []struct {
		name     string
		observed string
		want     string
	}{
		{name: "missing", observed: "", want: "<missing>"},
		{name: "foreign", observed: "win-foreign\n", want: "win-foreign"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner.calls = nil
			runner.outputs[secondKey] = test.observed
			err := runtime.queueSelfKill(context.Background(), targets)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "no self-target kill was queued") {
				t.Fatalf("mirror mismatch error = %v, want %q and zero-queue diagnostic", err, test.want)
			}
			for _, call := range runner.calls {
				if strings.Contains(strings.Join(call.args, " "), "run-shell") {
					t.Fatalf("mirror mismatch queued a kill: %#v", runner.calls)
				}
			}
		})
	}
}
