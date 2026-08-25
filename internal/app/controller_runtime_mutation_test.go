package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/controller"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type controllerRollbackMoveRunner struct {
	base       *routedTmuxRunner
	server     *fakeTmux
	firstField string
	moved      bool
}

type controllerBooleanOptionRunner struct{ base tmuxCommandRunner }

func (r controllerBooleanOptionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.base.Run(ctx, name, args...)
	if err != nil || !slices.Contains(args, "display-message") || !slices.Contains(args, "#{automatic-rename}") {
		return out, err
	}
	switch strings.TrimSpace(string(out)) {
	case "on":
		return []byte("1\n"), nil
	case "off":
		return []byte("0\n"), nil
	default:
		return out, nil
	}
}

func (r *controllerRollbackMoveRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.base.Run(ctx, name, args...)
	if err != nil || r.moved || !slices.Contains(args, r.firstField) || !slices.Contains(args, "set-option") {
		return out, err
	}
	r.moved = true
	first, second := r.server.sessions[0].windows[0], r.server.sessions[0].windows[1]
	pane := first.panes[0]
	first.panes = first.panes[1:]
	second.panes = append(second.panes, pane)
	return out, nil
}

func controllerMutationFixture(t *testing.T) (*fakeTmux, *routedTmuxRunner, runtimeMutationRoute, controller.Action) {
	t.Helper()
	server := newFakeTmux()
	server.socketPath = "/tmp/fake-tmux/controller-authority"
	session := server.addSession("alpha")
	session.opts[tmuxopts.ProjectUIDSession] = "project-controller"
	session.opts[tmuxopts.ProjectNameSession] = "before"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	route := runtimeMutationRoute{
		target:             explicitTmuxTarget{flag: "-L", value: "primary"},
		expectedSocketPath: server.socketPath, socketName: "primary",
		authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID},
	}
	write := controller.Action{
		Key:     "tmux:set-option:project:$1:@projmux_project_name",
		Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
		Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectSession,
		Target: session.id, Field: tmuxopts.ProjectNameSession, Before: "before", After: "after",
		Guards: []controller.Guard{{Field: tmuxopts.ProjectUIDSession, Expect: "project-controller"}},
		Args:   []string{"set-option", "-t", session.id, "-q", tmuxopts.ProjectNameSession, "after"},
	}
	return server, runner, route, write
}

func TestControllerRuntimeMutationPlanPrintsAndPinsPhysicalGeneration(t *testing.T) {
	server, runner, route, write := controllerMutationFixture(t)
	action, err := controllerRuntimeMutationAction(1, route, write, nil)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := (runtimeMutationPlan{Version: 1, Actions: []plannedRuntimeMutation{action}}).printableBytes()
	if err != nil {
		t.Fatal(err)
	}
	if text := string(bytes); !strings.Contains(text, `"physicalSocket": "/tmp/fake-tmux/controller-authority"`) ||
		!strings.Contains(text, `"routeAuthority": "app:pid=4242/session=/window=/pane="`) {
		t.Fatalf("printable plan omitted physical generation authority:\n%s", text)
	}

	server.serverPID = "5252"
	before := tmuxMutationCallCount(server)
	err = executeControllerRuntimeMutations(context.Background(), runner, route, []controller.Action{write})
	if err == nil || !strings.Contains(err.Error(), "generation drifted") {
		t.Fatalf("same-path replacement was not refused: %v", err)
	}
	if got := tmuxMutationCallCount(server); got != before {
		t.Fatalf("same-path replacement received %d write(s)", got-before)
	}
}

func TestControllerRuntimeMutationExecuteThenRepeatIsEmpty(t *testing.T) {
	server, runner, route, write := controllerMutationFixture(t)
	if err := executeControllerRuntimeMutations(context.Background(), runner, route, []controller.Action{write}); err != nil {
		t.Fatal(err)
	}
	if got := server.sessions[0].opts[tmuxopts.ProjectNameSession]; got != "after" {
		t.Fatalf("project name = %q", got)
	}
	firstWrites := tmuxMutationCallCount(server)
	if err := executeControllerRuntimeMutations(context.Background(), runner, route, []controller.Action{write}); err != nil {
		t.Fatal(err)
	}
	if got := tmuxMutationCallCount(server); got != firstWrites {
		t.Fatalf("repeat executed %d additional write(s)", got-firstWrites)
	}
}

func TestAuthorshipPromotionRuntimeRollbackIsIndependentOfActionOrder(t *testing.T) {
	fields := []string{tmuxopts.AgentUIDPane, tmuxopts.PaneOwnerKind, tmuxopts.PaneOwnerUID, tmuxopts.PaneRole}
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 1, 0, 2}, {2, 0, 3, 1}} {
		name := strings.Trim(strings.ReplaceAll(strings.Trim(fmt.Sprint(order), "[]"), " ", "-"), "-")
		t.Run(name, func(t *testing.T) {
			server := newFakeTmux()
			server.socketPath = "/tmp/fake-tmux/promotion-shuffle"
			session := server.addSession("alpha")
			window, pane := session.windows[0], session.windows[0].panes[0]
			pane.opts[tmuxopts.PaneUID] = "pane-promotion"
			pane.opts[tmuxopts.AgentProviderPane] = "codex"
			pane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "1"
			runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
			route := runtimeMutationRoute{target: explicitTmuxTarget{flag: "-L", value: "primary"}, expectedSocketPath: server.socketPath,
				socketName: "primary", authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID}}
			values := map[string]string{
				tmuxopts.AgentUIDPane: "agent-promotion", tmuxopts.PaneOwnerKind: "Agent",
				tmuxopts.PaneOwnerUID: "agent-promotion", tmuxopts.PaneRole: "agent",
			}
			var writes []controller.Action
			for index, fieldIndex := range order {
				field := fields[fieldIndex]
				writes = append(writes, controller.Action{
					Key: fmt.Sprintf("promotion-%d-%s", index, field), Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
					Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectPane, Target: pane.id, Field: field, Before: "", After: values[field],
					Guards: []controller.Guard{{Field: tmuxopts.PaneUID, Expect: "pane-promotion"}, {Field: "session_id", Expect: session.id},
						{Field: "window_id", Expect: window.id}, {Field: tmuxopts.AgentLaunchAuthorshipPane, Expect: "1"},
						{Field: tmuxopts.AgentProviderPane, Expect: "codex"}},
					Args: []string{"set-option", "-p", "-t", pane.id, "-q", field, values[field]},
				})
			}
			server.fail = []string{"set-option", fields[order[len(order)-1]]}
			server.failAfterMutation = true
			if err := executeControllerRuntimeMutations(context.Background(), runner, route, writes); err == nil || strings.Contains(err.Error(), "owned reverse rollback incomplete") {
				t.Fatalf("shuffled injected failure = %v", err)
			}
			for _, field := range fields {
				if pane.opts[field] != "" {
					t.Fatalf("shuffled rollback left %s=%q; order=%v", field, pane.opts[field], order)
				}
			}
		})
	}
}

func TestControllerNonUIDOptionsHaveTypedEffectsRepeatEmptyAndOwnedRollback(t *testing.T) {
	newFixture := func() (*fakeTmux, *routedTmuxRunner, tmuxCommandRunner, runtimeMutationRoute, []controller.Action) {
		server := newFakeTmux()
		server.socketPath = "/tmp/fake-tmux/controller-options"
		session := server.addSession("alpha")
		window := session.windows[0]
		window.opts[tmuxopts.WindowUID] = ""
		window.opts[tmuxopts.AutomaticRenameWindow] = "on"
		window.opts[tmuxopts.WindowName] = "before"
		base := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
		runner := controllerBooleanOptionRunner{base: base}
		route := runtimeMutationRoute{
			target: explicitTmuxTarget{flag: "-L", value: "primary"}, expectedSocketPath: server.socketPath, socketName: "primary",
			authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID},
		}
		guards := []controller.Guard{{Field: tmuxopts.WindowUID, Expect: ""}, {Field: "session_id", Expect: session.id}}
		writes := []controller.Action{
			{
				Key: "uid", Surface: controller.SurfaceTmux, Intent: controller.IntentRepairBinding,
				Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectWindow,
				Target: window.id, Field: tmuxopts.WindowUID, Before: "", After: "window-controller", Guards: slices.Clone(guards),
				Args: []string{"set-option", "-w", "-t", window.id, "-q", tmuxopts.WindowUID, "window-controller"},
			},
			{
				Key: "a-automatic-rename", Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
				Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectWindow,
				Target: window.id, Field: tmuxopts.AutomaticRenameWindow, Before: "1", After: "off", Guards: slices.Clone(guards),
				Args: []string{"set-option", "-w", "-t", window.id, tmuxopts.AutomaticRenameWindow, "off"},
			},
			{
				Key: "b-stable-name", Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
				Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectWindow,
				Target: window.id, Field: tmuxopts.WindowName, Before: "before", After: "after", Guards: slices.Clone(guards),
				Args: []string{"set-option", "-w", "-t", window.id, "-q", tmuxopts.WindowName, "after"},
			},
		}
		return server, base, runner, route, writes
	}

	server, base, runner, route, writes := newFixture()
	final := map[string]string{}
	for _, write := range writes {
		final[write.Target+"\x00"+write.Field] = write.After
	}
	for _, write := range writes[1:] {
		action, err := controllerRuntimeMutationAction(1, route, write, final)
		if err != nil {
			t.Fatal(err)
		}
		if action.Verb != mutationWriteOption || action.Target.UID != "window-controller" || action.Effect != runtimeMutationInventory[mutationWriteOption].Effect {
			t.Fatalf("non-UID option action = %#v", action)
		}
		if !strings.Contains(action.Guard.Expect, "exact option before="+write.Field+"="+write.Before) {
			t.Fatalf("non-UID option guard omitted exact before-value: %#v", action.Guard)
		}
		if _, err := (runtimeMutationPlan{Version: 1, Actions: []plannedRuntimeMutation{action}}).printableBytes(); err != nil {
			t.Fatalf("print non-UID option action: %v", err)
		}
	}
	if err := executeControllerRuntimeMutations(context.Background(), runner, route, writes); err != nil {
		t.Fatal(err)
	}
	if got := server.sessions[0].windows[0].opts[tmuxopts.AutomaticRenameWindow]; got != "off" {
		t.Fatalf("automatic-rename = %q, want off", got)
	}
	if got := server.sessions[0].windows[0].opts[tmuxopts.WindowUID]; got != "window-controller" {
		t.Fatalf("Window UID = %q, want exact controller identity", got)
	}
	firstWrites := tmuxMutationCallCount(server)
	if err := executeControllerRuntimeMutations(context.Background(), runner, route, writes); err != nil {
		t.Fatal(err)
	}
	if got := tmuxMutationCallCount(server); got != firstWrites {
		t.Fatalf("repeat executed %d option write(s)", got-firstWrites)
	}
	for _, call := range base.calls {
		if slices.Contains(call.args, "set-option") && (call.flag != "-S" || call.value != server.socketPath) {
			t.Fatalf("controller option escaped exact typed executor route: %#v", call)
		}
	}

	// A generated hook/controller pass may converge a presentation option
	// after planning while the sibling UID claim remains blank. The blank UID
	// is still the exact planned Before identity; observe the option itself and
	// replan only that already-satisfied row away.
	server, _, runner, route, writes = newFixture()
	server.sessions[0].windows[0].opts[tmuxopts.AutomaticRenameWindow] = "off"
	beforeCalls := len(server.calls)
	if err := executeControllerRuntimeMutations(context.Background(), runner, route, writes); err != nil {
		t.Fatalf("already-converged automatic-rename with pending UID: %v", err)
	}
	automaticWrites := 0
	for _, call := range server.calls[beforeCalls:] {
		if slices.Contains(call, "set-option") && slices.Contains(call, tmuxopts.AutomaticRenameWindow) {
			automaticWrites++
		}
	}
	if automaticWrites != 0 {
		t.Fatalf("already-converged automatic-rename executed %d write(s)", automaticWrites)
	}
	convergedWindow := server.sessions[0].windows[0]
	if convergedWindow.opts[tmuxopts.WindowUID] != "window-controller" || convergedWindow.opts[tmuxopts.WindowName] != "after" || convergedWindow.opts[tmuxopts.AutomaticRenameWindow] != "off" {
		t.Fatalf("sibling convergence after option replan = %#v", convergedWindow.opts)
	}

	server, _, runner, route, writes = newFixture()
	server.sessions[0].windows[0].opts[tmuxopts.AutomaticRenameWindow] = "unexpected"
	beforeWrites := tmuxMutationCallCount(server)
	err := executeControllerRuntimeMutations(context.Background(), runner, route, writes)
	if err == nil || !strings.Contains(err.Error(), "automatic-rename drifted before write") {
		t.Fatalf("pre-write option drift error = %v", err)
	}
	if got := tmuxMutationCallCount(server); got != beforeWrites {
		t.Fatalf("pre-write option drift executed %d write(s)", got-beforeWrites)
	}

	server, _, runner, route, writes = newFixture()
	server.fail = []string{"set-option", tmuxopts.WindowName}
	err = executeControllerRuntimeMutations(context.Background(), runner, route, writes)
	if err == nil || strings.Contains(err.Error(), "owned reverse rollback incomplete") {
		t.Fatalf("partial failure/owned rollback error = %v; opts=%#v calls=%#v", err, server.sessions[0].windows[0].opts, server.calls)
	}
	window := server.sessions[0].windows[0]
	if got := controllerRuntimeMutationEffectValue(tmuxopts.AutomaticRenameWindow, window.opts[tmuxopts.AutomaticRenameWindow]); got != "1" {
		t.Fatalf("automatic-rename rollback = %q, want original enabled state", window.opts[tmuxopts.AutomaticRenameWindow])
	}
	if got := window.opts[tmuxopts.WindowName]; got != "before" {
		t.Fatalf("failed sibling option changed = %q", got)
	}
	if got := window.opts[tmuxopts.WindowUID]; got != "" {
		t.Fatalf("partial-failure rollback left Window UID = %q", got)
	}
}

func controllerClosedWrite(scope resourcegraph.ObjectKind, target, uid, field, before, after string) controller.Action {
	kind := map[resourcegraph.ObjectKind]string{
		resourcegraph.ObjectSession: "session", resourcegraph.ObjectWindow: "window", resourcegraph.ObjectPane: "pane",
	}[scope]
	uidField := controllerRuntimeMutationUIDField(kind)
	guards := []controller.Guard{{Field: uidField, Expect: uid}}
	if scope == resourcegraph.ObjectWindow {
		guards = append(guards, controller.Guard{Field: "session_id", Expect: "$1"})
	}
	if scope == resourcegraph.ObjectPane {
		guards = append(guards, controller.Guard{Field: "session_id", Expect: "$1"}, controller.Guard{Field: "window_id", Expect: "@1"})
	}
	write := controller.Action{
		Key: "closed/" + field, Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
		Authority: controller.AuthorityAllow, Scope: scope, Target: target, Field: field, Before: before, After: after, Guards: guards,
	}
	if field == "window_name" {
		write.Args = []string{"rename-window", "-t", target, after}
		return write
	}
	write.Args = []string{"set-option"}
	if scope == resourcegraph.ObjectWindow {
		write.Args = append(write.Args, "-w")
	} else if scope == resourcegraph.ObjectPane {
		write.Args = append(write.Args, "-p")
	}
	if after == "" {
		write.Args = append(write.Args, "-u", "-t", target, field)
	} else {
		write.Args = append(write.Args, "-t", target)
		class, _ := controllerRuntimeMutationFieldClassFor(kind, field)
		if field != tmuxopts.AutomaticRenameWindow && class != controllerRuntimeMutationPresentation {
			write.Args = append(write.Args, "-q")
		}
		write.Args = append(write.Args, field, after)
	}
	return write
}

func TestControllerRuntimeMutationClosedProductionFieldAndArgvGrammar(t *testing.T) {
	_, _, route, _ := controllerMutationFixture(t)
	tests := []struct {
		name      string
		write     controller.Action
		wantVerb  runtimeMutationVerb
		wantClass controllerRuntimeMutationFieldClass
	}{
		{"session uid", controllerClosedWrite(resourcegraph.ObjectSession, "$1", "", tmuxopts.ProjectUIDSession, "", "project-1"), mutationWriteIdentity, controllerRuntimeMutationManaged},
		{"session name", controllerClosedWrite(resourcegraph.ObjectSession, "$1", "project-1", tmuxopts.ProjectNameSession, "old", "new"), mutationWriteOption, controllerRuntimeMutationManaged},
		{"session path", controllerClosedWrite(resourcegraph.ObjectSession, "$1", "project-1", tmuxopts.ProjectPathSession, "/old", "/new"), mutationWriteOption, controllerRuntimeMutationManaged},
		{"window uid", controllerClosedWrite(resourcegraph.ObjectWindow, "@1", "", tmuxopts.WindowUID, "", "window-1"), mutationWriteIdentity, controllerRuntimeMutationManaged},
		{"automatic rename", controllerClosedWrite(resourcegraph.ObjectWindow, "@1", "window-1", tmuxopts.AutomaticRenameWindow, "1", "off"), mutationWriteOption, controllerRuntimeMutationManaged},
		{"window name mirror", controllerClosedWrite(resourcegraph.ObjectWindow, "@1", "window-1", tmuxopts.WindowName, "old", "new"), mutationWriteOption, controllerRuntimeMutationManaged},
		{"window display rename", controllerClosedWrite(resourcegraph.ObjectWindow, "@1", "window-1", "window_name", "old", "new"), mutationRenameWindow, controllerRuntimeMutationManaged},
		{"pane uid", controllerClosedWrite(resourcegraph.ObjectPane, "%1", "", tmuxopts.PaneUID, "", "pane-1"), mutationWriteIdentity, controllerRuntimeMutationManaged},
		{"pane name", controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-1", tmuxopts.PaneName, "old", "new"), mutationWriteOption, controllerRuntimeMutationManaged},
		{"L8 session index unset", controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-orphan", tmuxopts.AgentSessionIDPane, "session-1", ""), mutationWriteOption, controllerRuntimeMutationManaged},
		{"L8 thread index unset", controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-orphan", tmuxopts.AgentThreadIDPane, "thread-1", ""), mutationWriteOption, controllerRuntimeMutationManaged},
	}
	for _, field := range []string{aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption} {
		tests = append(tests,
			struct {
				name      string
				write     controller.Action
				wantVerb  runtimeMutationVerb
				wantClass controllerRuntimeMutationFieldClass
			}{"presentation set " + field, controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-1", field, "old", "new"), mutationWritePresentationOption, controllerRuntimeMutationPresentation},
			struct {
				name      string
				write     controller.Action
				wantVerb  runtimeMutationVerb
				wantClass controllerRuntimeMutationFieldClass
			}{"presentation unset " + field, controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-1", field, "old", ""), mutationWritePresentationOption, controllerRuntimeMutationPresentation},
		)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			final := map[string]string{tc.write.Target + "\x00" + controllerRuntimeMutationUIDField(controllerRuntimeMutationKind(tc.write)): tc.write.Guards[0].Expect}
			if tc.write.Field == controllerRuntimeMutationUIDField(controllerRuntimeMutationKind(tc.write)) {
				final[tc.write.Target+"\x00"+tc.write.Field] = tc.write.After
			}
			action, err := controllerRuntimeMutationAction(1, route, tc.write, final)
			if err != nil {
				t.Fatal(err)
			}
			if action.Verb != tc.wantVerb || action.Controller == nil || action.Controller.Class != string(tc.wantClass) {
				t.Fatalf("typed action = %#v", action)
			}
			if _, err := newRuntimeMutationPlan(action).printableBytes(); err != nil {
				t.Fatalf("print typed action: %v", err)
			}
		})
	}

	base := controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-1", tmuxopts.PaneName, "old", "new")
	for _, tc := range []struct {
		name string
		edit func(*controller.Action)
	}{
		{"global scope", func(w *controller.Action) { w.Args = []string{"set-option", "-g", tmuxopts.PaneName, "new"} }},
		{"session scope", func(w *controller.Action) {
			w.Args = []string{"set-option", "-s", "-t", "%1", tmuxopts.PaneName, "new"}
		}},
		{"wrong target", func(w *controller.Action) { w.Args[3] = "%9" }},
		{"wrong field", func(w *controller.Action) { w.Args[5] = tmuxopts.AppGlobal }},
		{"wrong value", func(w *controller.Action) { w.Args[6] = "hidden" }},
		{"extra operand", func(w *controller.Action) { w.Args = append(w.Args, "extra") }},
		{"separator", func(w *controller.Action) { w.Args = append(w.Args, ";", "kill-pane", "-t", "%1") }},
	} {
		t.Run("reject "+tc.name, func(t *testing.T) {
			write := base
			write.Args = slices.Clone(base.Args)
			tc.edit(&write)
			if _, err := controllerRuntimeMutationAction(1, route, write, nil); err == nil {
				t.Fatalf("forged argv accepted: %v", write.Args)
			}
		})
	}
	for _, field := range []string{tmuxopts.AppGlobal, tmuxopts.EphemeralSession, tmuxopts.AgentLaunchAuthorshipPane, runtimeMutationSocketNameOption, "@global"} {
		write := controllerClosedWrite(resourcegraph.ObjectPane, "%1", "pane-1", field, "", "x")
		if _, err := controllerRuntimeMutationAction(1, route, write, nil); err == nil {
			t.Fatalf("unclassified field %q acquired controller execution", field)
		}
	}
}

func TestControllerUIDMintAndUnsetRollbackDistinguishNoEffectFromOwnedEffect(t *testing.T) {
	for _, operation := range []struct {
		name, before, after string
	}{
		{"mint", "", "project-minted"},
		{"unset", "project-orphan", ""},
	} {
		for _, failure := range []struct {
			name        string
			afterEffect bool
			wantWrites  int
		}{
			{"fail-before", false, 1},
			{"error-after-effect", true, 2},
		} {
			t.Run(operation.name+"/"+failure.name, func(t *testing.T) {
				server := newFakeTmux()
				server.socketPath = "/tmp/fake-tmux/controller-uid-rollback"
				session := server.addSession("alpha")
				session.opts[tmuxopts.ProjectUIDSession] = operation.before
				runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
				route := runtimeMutationRoute{target: explicitTmuxTarget{flag: "-L", value: "primary"}, expectedSocketPath: server.socketPath, socketName: "primary", authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID}}
				write := controllerClosedWrite(resourcegraph.ObjectSession, session.id, operation.before, tmuxopts.ProjectUIDSession, operation.before, operation.after)
				server.fail, server.failAfterMutation, server.failMessage = []string{"set-option", tmuxopts.ProjectUIDSession}, failure.afterEffect, "injected UID apply failure"
				beforeWrites := tmuxMutationCallCount(server)
				err := executeControllerRuntimeMutations(context.Background(), runner, route, []controller.Action{write})
				if err == nil || !strings.Contains(err.Error(), "injected UID apply failure") || strings.Contains(err.Error(), "owned reverse rollback incomplete") {
					t.Fatalf("UID %s %s error = %v", operation.name, failure.name, err)
				}
				if got := session.opts[tmuxopts.ProjectUIDSession]; got != operation.before {
					t.Fatalf("UID after owned rollback = %q, want %q", got, operation.before)
				}
				if got := tmuxMutationCallCount(server) - beforeWrites; got != failure.wantWrites {
					t.Fatalf("mutation writes = %d, want exact %d", got, failure.wantWrites)
				}
			})
		}
	}
}

func TestRealL8OrphanRecoveryPrintsOwnedUIDForEveryUnset(t *testing.T) {
	graph := resourcegraph.Graph{Runtime: []resourcegraph.RuntimeNode{{
		Ref:   resourcegraph.RuntimeRef{Kind: resourcegraph.ObjectPane, ID: "%7"},
		Class: resourcegraph.ClassRecoverable, UID: "pane-orphan", ContainerID: "@3",
		AgentSessionID: "session-orphan", AgentThreadID: "thread-orphan",
	}}}
	candidates := controllerRecoveryCandidates(graph, controller.RecoveryHookConverge)
	if len(candidates) != 3 {
		t.Fatalf("L8 candidates = %#v, want UID+session-index+thread-index", candidates)
	}
	actions, _ := controller.Authorize(controller.IndexHandles(graph), controller.GuardFields{
		SessionUID: tmuxopts.ProjectUIDSession, WindowUID: tmuxopts.WindowUID, PaneUID: tmuxopts.PaneUID,
		SessionID: "session_id", WindowID: "window_id",
	}, controller.Grant{}, candidates)
	route := runtimeMutationRoute{
		target: explicitTmuxTarget{flag: "-L", value: "primary"}, expectedSocketPath: "/tmp/controller-l8", socketName: "primary",
		authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
	}
	final := map[string]string{}
	for _, action := range actions {
		if !action.Allowed() {
			t.Fatalf("automatic L8 action refused: %+v", action)
		}
		final[action.Target+"\x00"+action.Field] = action.After
	}
	seen := map[string]bool{}
	planned := make([]plannedRuntimeMutation, 0, len(actions))
	for index, write := range actions {
		action, err := controllerRuntimeMutationAction(index+1, route, write, final)
		if err != nil {
			t.Fatal(err)
		}
		seen[write.Field] = true
		if action.Target.UID != "pane-orphan" || action.Target.ID != "%7" || action.Controller == nil || action.Controller.After != "" {
			t.Fatalf("L8 printable target/effect = %#v", action)
		}
		planned = append(planned, action)
	}
	if _, err := newRuntimeMutationPlan(planned...).printableBytes(); err != nil {
		t.Fatalf("print L8 plan: %v", err)
	}
	for _, field := range []string{tmuxopts.PaneUID, tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane} {
		if !seen[field] {
			t.Errorf("L8 printable inventory omitted %s", field)
		}
	}
}

func TestForgedControllerDeclaredEffectIsUnprintableBeforeAnyTmuxCall(t *testing.T) {
	_, _, route, write := controllerMutationFixture(t)
	valid, err := controllerRuntimeMutationAction(1, route, write, nil)
	if err != nil {
		t.Fatal(err)
	}
	forged := valid
	forged.Order = 2
	forged.Controller = &runtimeMutationControllerEffect{
		Class: string(controllerRuntimeMutationManaged), Mode: controllerRuntimeMutationForward, Scope: string(resourcegraph.ObjectSession),
		Field: tmuxopts.ProjectNameSession, Before: "before", After: "hidden",
	}
	guards, applies, observes := 0, 0, 0
	steps := []runtimeMutationStep{
		{Action: valid, TargetRouteGuard: func(context.Context) error { guards++; return nil }, Reobserve: func(context.Context) (bool, error) { observes++; return false, nil }, Guard: func(context.Context) error { guards++; return nil }, Apply: func(context.Context) error { applies++; return nil }},
		{Action: forged, TargetRouteGuard: func(context.Context) error { guards++; return nil }, Reobserve: func(context.Context) (bool, error) { observes++; return false, nil }, Guard: func(context.Context) error { guards++; return nil }, Apply: func(context.Context) error { applies++; return nil }},
	}
	err = executeRuntimeMutationPlan(context.Background(), steps)
	if err == nil || !strings.Contains(err.Error(), "disagrees with executable argv") {
		t.Fatalf("forged declared effect error = %v", err)
	}
	if guards != 0 || applies != 0 || observes != 0 {
		t.Fatalf("malformed later action reached tmux seam: guards=%d observes=%d applies=%d", guards, observes, applies)
	}

	standalone := forged
	standalone.Order = 1
	standalone.Target.Socket = "-S=/tmp/controller-forged"
	standalone.Target.PhysicalSocket = "/tmp/controller-forged"
	standalone.Target.RouteAuthority = (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteStandaloneExplicit, ServerPID: "4242"}).printable()
	standalone.Operands = []string{"-g", tmuxopts.ProjectNameSession, "hidden"}
	bindRuntimeMutationGuard(&standalone, "controller action=forged-global")
	if _, err := newRuntimeMutationPlan(standalone).printableBytes(); err == nil || !strings.Contains(err.Error(), "disagrees with executable argv") {
		t.Fatalf("standalone-explicit escaped object scope: %v", err)
	}

	for _, tc := range []struct {
		name string
		edit func(*plannedRuntimeMutation)
		want string
	}{
		{"nil declaration and remapped parent", func(action *plannedRuntimeMutation) {
			action.Controller = nil
			action.Target.Parent = "project/root"
			bindRuntimeMutationGuard(action, "forged canonical guard")
		}, "requires a typed controller declaration"},
		{"declared effect and remapped parent", func(action *plannedRuntimeMutation) {
			action.Target.Parent = "project/root"
			bindRuntimeMutationGuard(action, "forged canonical guard")
		}, "no controller target namespace"},
		{"controller declaration on non-controller verb", func(action *plannedRuntimeMutation) {
			action.Verb = mutationKillPane
			action.Guard.Kind = runtimeMutationInventory[mutationKillPane].GuardKind
			action.Effect = runtimeMutationInventory[mutationKillPane].Effect
			action.Target.Kind, action.Target.ID, action.Target.UID = "pane", "%1", "pane-1"
			action.Target.Parent = "controller.identity/session_id=$1/window_id=@1"
			action.Operands = []string{"-t", "%1"}
			bindRuntimeMutationGuard(action, "controller action=forged-kill")
		}, "cannot carry a controller declaration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := valid
			action.Controller = &runtimeMutationControllerEffect{
				Class: valid.Controller.Class, Mode: valid.Controller.Mode, Scope: valid.Controller.Scope,
				Field: valid.Controller.Field, Before: valid.Controller.Before, After: valid.Controller.After,
			}
			tc.edit(&action)
			calls := 0
			err := executeRuntimeMutationPlan(context.Background(), []runtimeMutationStep{{
				Action:           action,
				TargetRouteGuard: func(context.Context) error { calls++; return nil },
				Reobserve:        func(context.Context) (bool, error) { calls++; return false, nil },
				Guard:            func(context.Context) error { calls++; return nil },
				Apply:            func(context.Context) error { calls++; return nil },
			}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forged action error = %v, want %q", err, tc.want)
			}
			if calls != 0 {
				t.Fatalf("forged action reached tmux seam %d time(s)", calls)
			}
		})
	}
}

func TestControllerExplicitStandaloneAuthorityNeverInfersAPane(t *testing.T) {
	server := newFakeTmux()
	server.socketPath, server.appMarker, server.socketName = "/tmp/fake-tmux/operator", "", ""
	server.addSession("operator")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-S\x00" + server.socketPath: server}}
	route, err := resolveControllerRuntimeMutationRoute(context.Background(), runner,
		explicitTmuxTarget{flag: "-S", value: server.socketPath}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if route.authority == nil || route.authority.Class != runtimeMutationRouteStandaloneExplicit ||
		route.authority.SessionID != "" || route.authority.WindowID != "" || route.authority.PaneID != "" {
		t.Fatalf("explicit standalone authority invented containment: %+v", route.authority)
	}
	for _, call := range runner.calls {
		if len(call.args) != 0 && call.args[0] == "list-panes" {
			t.Fatalf("explicit authority inferred an arbitrary Pane: %+v", call)
		}
	}
	if _, err := resolveControllerRuntimeMutationRoute(context.Background(), runner,
		explicitTmuxTarget{flag: "-L", value: "operator"}, func(string) string { return "" }); err == nil {
		t.Fatal("logical standalone route acquired internal explicit-path authority")
	}
}

func TestControllerRollbackRefusesMovedTargetAndPreservesSibling(t *testing.T) {
	server, base, route, _ := controllerMutationFixture(t)
	session := server.sessions[0]
	firstWindow, firstPane := session.windows[0], session.windows[0].panes[0]
	firstPane.opts[tmuxopts.PaneUID], firstPane.opts[tmuxopts.PaneName] = "pane-first", "old-first"
	secondPane := newFakeTmuxPane("%21")
	secondPane.opts[tmuxopts.PaneUID], secondPane.opts[tmuxopts.PaneName] = "pane-second", "old-second"
	secondWindow := &fakeTmuxWindow{id: "@20", name: "second", opts: map[string]string{tmuxopts.WindowUID: "window-second"}, panes: []*fakeTmuxPane{secondPane}}
	session.windows = append(session.windows, secondWindow)
	server.fail = []string{"set-option", secondPane.id, tmuxopts.PaneName}
	runner := &controllerRollbackMoveRunner{base: base, server: server, firstField: tmuxopts.PaneName}
	writes := []controller.Action{
		{
			Key: "a-first", Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
			Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectPane,
			Target: firstPane.id, Field: tmuxopts.PaneName, Before: "old-first", After: "applied",
			Guards: []controller.Guard{
				{Field: tmuxopts.PaneUID, Expect: "pane-first"}, {Field: "session_id", Expect: session.id}, {Field: "window_id", Expect: firstWindow.id},
			},
			Args: []string{"set-option", "-p", "-t", firstPane.id, "-q", tmuxopts.PaneName, "applied"},
		},
		{
			Key: "b-second", Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
			Authority: controller.AuthorityAllow, Scope: resourcegraph.ObjectPane,
			Target: secondPane.id, Field: tmuxopts.PaneName, Before: "old-second", After: "never",
			Guards: []controller.Guard{
				{Field: tmuxopts.PaneUID, Expect: "pane-second"}, {Field: "session_id", Expect: session.id}, {Field: "window_id", Expect: secondWindow.id},
			},
			Args: []string{"set-option", "-p", "-t", secondPane.id, "-q", tmuxopts.PaneName, "never"},
		},
	}
	firstAction, err := controllerRuntimeMutationAction(1, route, writes[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstAction.Target.Parent, "session_id="+session.id) || !strings.Contains(firstAction.Target.Parent, "window_id="+firstWindow.id) {
		t.Fatalf("printable target omitted containment: %+v", firstAction.Target)
	}
	err = executeControllerRuntimeMutations(context.Background(), runner, route, writes)
	if err == nil || !strings.Contains(err.Error(), "owned reverse rollback incomplete") {
		t.Fatalf("moved target did not leave an explicit rollback residual: %v", err)
	}
	if got := firstPane.opts[tmuxopts.PaneName]; got != "applied" {
		t.Fatalf("moved target was unsafely rolled back: %q", got)
	}
	if got := secondPane.opts[tmuxopts.PaneName]; got != "old-second" {
		t.Fatalf("sibling received failed write: %q", got)
	}
}

func TestTopologyPlannedWriteCarriesUIDAndContainmentGuards(t *testing.T) {
	actions, err := controllerRuntimeActionsFromPlannedWrites([]plannedTmuxWrite{{
		args:   []string{"set-option", "-p", "-t", "%8", "-q", "@projmux_pane_label", "shell"},
		target: "%8", field: "@projmux_pane_label", after: "shell",
		guardField: tmuxopts.PaneUID, guardBefore: "pane-8",
		guardSessionID: "$3", guardWindowID: "@5",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %#v", actions)
	}
	want := []controller.Guard{
		{Field: tmuxopts.PaneUID, Expect: "pane-8"},
		{Field: "session_id", Expect: "$3"},
		{Field: "window_id", Expect: "@5"},
	}
	if !slices.Equal(actions[0].Guards, want) {
		t.Fatalf("guards = %#v, want %#v", actions[0].Guards, want)
	}
}

func TestControllerRuntimeMutationRejectsIncompleteSemanticGuards(t *testing.T) {
	_, _, route, _ := controllerMutationFixture(t)
	tests := []struct {
		name  string
		write controller.Action
	}{
		{"session uid", controller.Action{Key: "session", Scope: resourcegraph.ObjectSession, Target: "$1", Field: "@x", Args: []string{"set-option", "-t", "$1", "@x", "x"}}},
		{"window uid", controller.Action{Key: "window-uid", Scope: resourcegraph.ObjectWindow, Target: "@2", Field: "@x", Guards: []controller.Guard{{Field: "session_id", Expect: "$1"}}, Args: []string{"set-option", "-w", "-t", "@2", "@x", "x"}}},
		{"window parent", controller.Action{Key: "window-parent", Scope: resourcegraph.ObjectWindow, Target: "@2", Field: "@x", Guards: []controller.Guard{{Field: tmuxopts.WindowUID, Expect: "window-2"}}, Args: []string{"set-option", "-w", "-t", "@2", "@x", "x"}}},
		{"pane uid", controller.Action{Key: "pane-uid", Scope: resourcegraph.ObjectPane, Target: "%3", Field: "@x", Guards: []controller.Guard{{Field: "window_id", Expect: "@2"}}, Args: []string{"set-option", "-p", "-t", "%3", "@x", "x"}}},
		{"pane parent", controller.Action{Key: "pane-parent", Scope: resourcegraph.ObjectPane, Target: "%3", Field: "@x", Guards: []controller.Guard{{Field: tmuxopts.PaneUID, Expect: "pane-3"}}, Args: []string{"set-option", "-p", "-t", "%3", "@x", "x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := controllerRuntimeMutationAction(1, route, tc.write, nil); err == nil {
				t.Fatalf("incomplete guards accepted: %#v", tc.write.Guards)
			}
		})
	}
	if _, err := controllerRuntimeActionsFromPlannedWrites([]plannedTmuxWrite{{
		args: []string{"set-option", "-p", "-t", "%3", "@x", "x"}, target: "%3", field: "@x",
		guardField: tmuxopts.PaneUID, guardBefore: "pane-3", guardWindowID: "@2",
	}}); err == nil || !strings.Contains(err.Error(), "Pane/session") {
		t.Fatalf("topology recorder omitted Pane session containment without refusal: %v", err)
	}
}

func TestStandaloneExplicitAuthorityRejectsNonControllerVerbs(t *testing.T) {
	authority := (&runtimeMutationRouteAuthority{Class: runtimeMutationRouteStandaloneExplicit, ServerPID: "4242"}).printable()
	tests := []plannedRuntimeMutation{
		func() plannedRuntimeMutation {
			action := newRuntimeMutation(1, mutationCreateSession, runtimeMutationTarget{
				Socket: "-S=/tmp/controller", PhysicalSocket: "/tmp/controller", RouteAuthority: authority,
				Kind: "session", ID: "alpha", UID: "project-alpha", Parent: "controller.identity/root",
			})
			bindRuntimeMutationGuard(&action, "controller action=forged-create")
			action.Operands = []string{"-s", "alpha"}
			return action
		}(),
		func() plannedRuntimeMutation {
			action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
				Socket: "-S=/tmp/controller", PhysicalSocket: "/tmp/controller", RouteAuthority: authority,
				Kind: "pane", ID: "%3", UID: "pane-3", Parent: "controller.identity/window_id=@2",
			})
			bindRuntimeMutationGuard(&action, "controller action=forged-kill")
			action.Operands = []string{"-t", "%3"}
			return action
		}(),
	}
	for _, action := range tests {
		if _, err := (runtimeMutationPlan{Version: 1, Actions: []plannedRuntimeMutation{action}}).printableBytes(); err == nil || !strings.Contains(err.Error(), "controller-only explicit standalone") {
			t.Fatalf("non-controller verb %q acquired receiptless authority: %v", action.Verb, err)
		}
	}
}

func TestStandaloneExplicitAuthorityHasOneProductionConstructor(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	root := filepath.Dir(thisFile)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		bytes, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(bytes), "Class: runtimeMutationRouteStandaloneExplicit") {
			sites = append(sites, entry.Name())
		}
	}
	if !slices.Equal(sites, []string{"controller_runtime_mutation.go"}) {
		t.Fatalf("receiptless authority constructors = %v", sites)
	}
}
