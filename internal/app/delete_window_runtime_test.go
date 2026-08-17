package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
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
	target, err := tmuxSocketNameTarget("isolated-delete")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &tmuxWindowDeleteRuntime{runner: runner, target: target, getenv: func(string) string { return "" }}
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{@projmux_project_uid}", "#{@projmux_window_uid}")
	runner.outputs[recordedTmuxCallKey("tmux", "-L", "isolated-delete", "list-windows", "-a", "-F", format)] = inventory
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

func TestWindowDeleteRuntimeFailsClosedOnMissingDuplicateForeignAndStaleMirrors(t *testing.T) {
	base := liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main")
	for _, test := range []struct {
		name      string
		inventory string
		want      string
	}{
		{name: "missing exact Window mirror", inventory: liveInventoryRow("$1", "alpha", "@11", "prj-alpha", "win-alpha-review"), want: "no exact live tmux Window mirror"},
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
	if len(live.Targets) != 1 || live.Targets[0].ProjectUID != "prj-alpha" {
		t.Fatalf("live plan = %#v", live)
	}
}

func TestWindowDeleteRuntimeImplicitTargetRequiresExactCallerSocketAndWindow(t *testing.T) {
	inventory := liveInventoryRow("$1", "alpha", "@10", "prj-alpha", "win-alpha-main")
	runtime, runner, registry := newWindowRuntimeFixture(t, inventory)
	runtime.getenv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/exact.sock,123,0"
		case "TMUX_PANE":
			return "%7"
		}
		return ""
	}
	format := tmuxRowFormat("#{socket_path}", "#{window_id}")
	runner.outputs[recordedTmuxCallKey("tmux", "-L", "isolated-delete", "display-message", "-p", "-F", "#{socket_path}")] =
		"/tmp/exact.sock\n"
	runner.outputs[recordedTmuxCallKey("tmux", "-L", "isolated-delete", "display-message", "-p", "-t", "%7", "-F", format)] =
		liveInventoryRow("/tmp/exact.sock", "@10")
	plan := deleteWindowRuntimePlan("win-alpha-main")
	plan.Implicit = true

	live, err := runtime.preflight(context.Background(), registry, plan)
	if err != nil {
		t.Fatalf("implicit preflight error = %v", err)
	}
	if len(live.Targets) != 1 || !live.Targets[0].Self {
		t.Fatalf("implicit live plan = %#v", live)
	}

	runner.outputs[recordedTmuxCallKey("tmux", "-L", "isolated-delete", "display-message", "-p", "-F", "#{socket_path}")] =
		"/tmp/foreign.sock\n"
	if _, err := runtime.preflight(context.Background(), registry, plan); err == nil || !strings.Contains(err.Error(), "not attached to the exact") {
		t.Fatalf("foreign caller socket error = %v", err)
	}
}

func TestWindowDeleteRuntimeQueueUsesQuotedExactSocketAndWindow(t *testing.T) {
	runtime, runner, _ := newWindowRuntimeFixture(t, "")
	target := windowLiveDeleteTarget{UID: "win-alpha-main", WindowID: "@12", SessionName: "alpha", SessionID: "$1"}
	mirrorKey := recordedTmuxCallKey("tmux", "-L", "isolated-delete", "show-options", "-wqv", "-t", "@12", "@projmux_window_uid")
	runner.outputs[mirrorKey] = "win-alpha-main\n"
	if err := runtime.queueSelfKill(context.Background(), []windowLiveDeleteTarget{target}); err != nil {
		t.Fatalf("queue error = %v", err)
	}
	wantQueue := recordedTmuxCall{name: "tmux", args: []string{
		"-L", "isolated-delete", "run-shell", "-b",
		"exec 'tmux' '-L' 'isolated-delete' kill-window -t '@12'",
	}}
	if len(runner.calls) != 2 || !reflect.DeepEqual(runner.calls[1], wantQueue) {
		t.Fatalf("queue calls = %#v, want mirror check then %#v", runner.calls, wantQueue)
	}

	runner.errors = map[string]error{
		recordedTmuxCallKey("tmux", wantQueue.args...): errors.New("injected queue failure"),
	}
	if err := runtime.queueSelfKill(context.Background(), []windowLiveDeleteTarget{target}); err == nil || !strings.Contains(err.Error(), "@12") {
		t.Fatalf("queue failure = %v", err)
	}
}

func TestWindowDeleteRuntimeQueueRevalidatesEveryMirrorBeforeQueueing(t *testing.T) {
	runtime, runner, _ := newWindowRuntimeFixture(t, "")
	targets := []windowLiveDeleteTarget{
		{UID: "win-alpha-main", WindowID: "@10", SessionName: "alpha", SessionID: "$1"},
		{UID: "win-alpha-review", WindowID: "@11", SessionName: "alpha", SessionID: "$1"},
	}
	runner.outputs[recordedTmuxCallKey("tmux", "-L", "isolated-delete", "show-options", "-wqv", "-t", "@10", "@projmux_window_uid")] = "win-alpha-main\n"
	secondKey := recordedTmuxCallKey("tmux", "-L", "isolated-delete", "show-options", "-wqv", "-t", "@11", "@projmux_window_uid")

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
