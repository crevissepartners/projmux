package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/controller"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// resolveControllerRuntimeMutationRoute binds controller writes to one
// physical server generation. Explicit standalone reconcile is an internal
// operator grant over --socket-path only: action-specific graph guards provide
// the real handle/owner containment, so this route never invents a Pane receipt.
func resolveControllerRuntimeMutationRoute(ctx context.Context, runner tmuxCommandRunner, target explicitTmuxTarget, lookupEnv func(string) string) (runtimeMutationRoute, error) {
	route, err := resolveExistingRuntimeMutationRoute(ctx, runner, target, lookupEnv)
	if err == nil {
		return route, nil
	}
	if !strings.Contains(err.Error(), "standalone authority requires exact inherited TMUX receipt") {
		return runtimeMutationRoute{}, err
	}
	if target.flag != "-S" {
		return runtimeMutationRoute{}, errors.New("controller standalone mutation requires explicit --socket-path authority")
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	pathOut, pathErr := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	pidOut, pidErr := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
	appOut, appErr := routed.Run(ctx, "tmux", "show-options", "-gqv", "@projmux_app")
	logicalOut, logicalErr := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if pathErr != nil || pidErr != nil || appErr != nil || logicalErr != nil {
		return runtimeMutationRoute{}, errors.New("controller standalone authority could not observe the exact server generation")
	}
	path, pid := strings.TrimSpace(string(pathOut)), strings.TrimSpace(string(pidOut))
	parsedPID, parseErr := strconv.Atoi(pid)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path != target.value || parseErr != nil || parsedPID <= 0 {
		return runtimeMutationRoute{}, errors.New("controller standalone authority has no exact physical path/PID receipt")
	}
	if strings.TrimSpace(string(appOut)) != "" || strings.TrimSpace(string(logicalOut)) != "" {
		return runtimeMutationRoute{}, errors.New("controller standalone authority class drifted")
	}
	return runtimeMutationRoute{
		target: target, expectedSocketPath: path, socketName: defaultAppSocket,
		authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteStandaloneExplicit, ServerPID: pid},
	}, nil
}

func controllerRuntimeMutationKind(write controller.Action) string {
	switch write.Scope {
	case resourcegraph.ObjectSession:
		return "session"
	case resourcegraph.ObjectWindow:
		return "window"
	case resourcegraph.ObjectPane:
		return "pane"
	}
	switch {
	case strings.HasPrefix(write.Target, "$"):
		return "session"
	case strings.HasPrefix(write.Target, "@"):
		return "window"
	default:
		return "pane"
	}
}

func validateControllerRuntimeMutationGuards(write controller.Action, requirePaneSession bool) error {
	kind := controllerRuntimeMutationKind(write)
	wantUID := map[string]string{
		"session": "@projmux_project_uid", "window": "@projmux_window_uid", "pane": "@projmux_pane_uid",
	}[kind]
	seen := map[string]string{}
	for _, guard := range write.Guards {
		if previous, duplicate := seen[guard.Field]; duplicate {
			if previous != strings.TrimSpace(guard.Expect) {
				return fmt.Errorf("controller write %q has conflicting guard %s", write.Key, guard.Field)
			}
			continue
		}
		seen[guard.Field] = strings.TrimSpace(guard.Expect)
	}
	if _, ok := seen[wantUID]; !ok {
		return fmt.Errorf("controller write %q has no exact %s UID guard", write.Key, kind)
	}
	switch kind {
	case "session":
		if exactTmuxHandle(write.Target, "$") == "" {
			return fmt.Errorf("controller write %q session target is not exact", write.Key)
		}
	case "window":
		if exactTmuxHandle(write.Target, "@") == "" || exactTmuxHandle(seen["session_id"], "$") == "" {
			return fmt.Errorf("controller write %q has no exact Window/session containment guard", write.Key)
		}
	case "pane":
		if exactTmuxHandle(write.Target, "%") == "" || exactTmuxHandle(seen["window_id"], "@") == "" {
			return fmt.Errorf("controller write %q has no exact Pane/window containment guard", write.Key)
		}
		if requirePaneSession && exactTmuxHandle(seen["session_id"], "$") == "" {
			return fmt.Errorf("controller write %q has no exact Pane/session containment guard", write.Key)
		}
		if session := seen["session_id"]; session != "" && exactTmuxHandle(session, "$") == "" {
			return fmt.Errorf("controller write %q has malformed Pane/session containment", write.Key)
		}
	default:
		return fmt.Errorf("controller write %q has unknown runtime scope", write.Key)
	}
	return nil
}

func normalizeControllerRuntimeMutationGuards(write controller.Action) (controller.Action, error) {
	seen := map[string]string{}
	guards := make([]controller.Guard, 0, len(write.Guards))
	for _, guard := range write.Guards {
		expect := strings.TrimSpace(guard.Expect)
		if previous, ok := seen[guard.Field]; ok {
			if previous != expect {
				return controller.Action{}, fmt.Errorf("controller write %q has conflicting guard %s", write.Key, guard.Field)
			}
			continue
		}
		seen[guard.Field] = expect
		guards = append(guards, guard)
	}
	write.Guards = guards
	return write, nil
}

func controllerRuntimeMutationUIDField(kind string) string {
	switch kind {
	case "session":
		return tmuxopts.ProjectUIDSession
	case "window":
		return tmuxopts.WindowUID
	case "pane":
		return tmuxopts.PaneUID
	default:
		return ""
	}
}

type controllerRuntimeMutationFieldClass string

const (
	controllerRuntimeMutationManaged      controllerRuntimeMutationFieldClass = "managed"
	controllerRuntimeMutationPresentation controllerRuntimeMutationFieldClass = "presentation-exempt"
	controllerRuntimeMutationForward      string                              = "forward"
	controllerRuntimeMutationOwnedReverse string                              = "owned-reverse"
)

var controllerRuntimeMutationManagedFields = map[string][]string{
	"session": {tmuxopts.ProjectUIDSession, tmuxopts.ProjectNameSession, tmuxopts.ProjectPathSession},
	"window":  {tmuxopts.WindowUID, tmuxopts.AutomaticRenameWindow, tmuxopts.WindowName, "window_name"},
	"pane":    {tmuxopts.PaneUID, tmuxopts.PaneName, tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane},
}

var controllerRuntimeMutationPresentationFields = []string{
	aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption,
}

func controllerRuntimeMutationFieldClassFor(kind, field string) (controllerRuntimeMutationFieldClass, bool) {
	if slices.Contains(controllerRuntimeMutationManagedFields[kind], field) {
		return controllerRuntimeMutationManaged, true
	}
	if kind == "pane" && slices.Contains(controllerRuntimeMutationPresentationFields, field) {
		return controllerRuntimeMutationPresentation, true
	}
	return "", false
}

// validateControllerRuntimeMutationArgv is the closed executable grammar for
// the controller adapter. A declared field/effect cannot select a different
// scope, target, option, value, or tmux verb at execution time.
func validateControllerRuntimeMutationArgv(write controller.Action, mode string) error {
	if mode != controllerRuntimeMutationForward && mode != controllerRuntimeMutationOwnedReverse {
		return fmt.Errorf("controller write %q has unknown execution mode %q", write.Key, mode)
	}
	kind := controllerRuntimeMutationKind(write)
	class, ok := controllerRuntimeMutationFieldClassFor(kind, write.Field)
	if !ok {
		return fmt.Errorf("controller write %q field %q is outside the closed %s option set", write.Key, write.Field, kind)
	}
	if write.Field == "window_name" {
		want := []string{"rename-window", "-t", write.Target, write.After}
		if kind != "window" || strings.TrimSpace(write.After) == "" || !slices.Equal(write.Args, want) {
			return fmt.Errorf("controller write %q rename declaration disagrees with executable argv", write.Key)
		}
		return nil
	}
	if write.Field == tmuxopts.AutomaticRenameWindow {
		switch strings.ToLower(strings.TrimSpace(write.After)) {
		case "", "0", "1", "off", "on":
		default:
			return fmt.Errorf("controller write %q automatic-rename value %q is not closed", write.Key, write.After)
		}
	}
	if mode == controllerRuntimeMutationForward {
		switch write.Field {
		case tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane:
			if write.After != "" {
				return fmt.Errorf("controller write %q may only clear the L8 conversation index %q", write.Key, write.Field)
			}
		default:
			if class == controllerRuntimeMutationManaged && write.Field != controllerRuntimeMutationUIDField(kind) && write.After == "" {
				return fmt.Errorf("controller write %q may not unset managed field %q outside owned rollback", write.Key, write.Field)
			}
		}
	}
	want := []string{"set-option"}
	switch kind {
	case "window":
		want = append(want, "-w")
	case "pane":
		want = append(want, "-p")
	}
	if write.After == "" {
		want = append(want, "-u", "-t", write.Target, write.Field)
	} else {
		want = append(want, "-t", write.Target)
		if write.Field != tmuxopts.AutomaticRenameWindow && class != controllerRuntimeMutationPresentation {
			want = append(want, "-q")
		}
		want = append(want, write.Field, write.After)
	}
	if !slices.Equal(write.Args, want) {
		return fmt.Errorf("controller write %q declared %s=%q disagrees with executable argv %v (want %v)", write.Key, write.Field, write.After, write.Args, want)
	}
	return nil
}

func validateControllerRuntimeMutationPlanAction(action plannedRuntimeMutation) error {
	if action.Controller == nil {
		return fmt.Errorf("runtime mutation plan: controller target %q has no typed declared effect", action.Target.ID)
	}
	declared := action.Controller
	write := controller.Action{
		Key: action.Target.UID, Scope: resourcegraph.ObjectKind(declared.Scope), Target: action.Target.ID,
		Field: declared.Field, Before: declared.Before, After: declared.After,
	}
	switch action.Verb {
	case mutationWriteIdentity, mutationWriteOption, mutationWritePresentationOption:
		write.Args = append([]string{"set-option"}, action.Operands...)
	case mutationRenameWindow:
		write.Args = append([]string{"rename-window"}, action.Operands...)
	default:
		return fmt.Errorf("runtime mutation plan: controller effect uses unsupported action %q", action.Verb)
	}
	if controllerRuntimeMutationKind(write) != action.Target.Kind {
		return fmt.Errorf("runtime mutation plan: controller scope %q disagrees with target kind %q", declared.Scope, action.Target.Kind)
	}
	if err := validateControllerRuntimeMutationArgv(write, declared.Mode); err != nil {
		return fmt.Errorf("runtime mutation plan: %w", err)
	}
	class, ok := controllerRuntimeMutationFieldClassFor(action.Target.Kind, declared.Field)
	if !ok || declared.Class != string(class) {
		return fmt.Errorf("runtime mutation plan: controller field %q has mismatched semantic class %q", declared.Field, declared.Class)
	}
	wantVerb := mutationWriteOption
	switch {
	case declared.Field == controllerRuntimeMutationUIDField(action.Target.Kind):
		wantVerb = mutationWriteIdentity
	case declared.Field == "window_name":
		wantVerb = mutationRenameWindow
	case class == controllerRuntimeMutationPresentation:
		wantVerb = mutationWritePresentationOption
	}
	if action.Verb != wantVerb {
		return fmt.Errorf("runtime mutation plan: controller field %q requires action %q, got %q", declared.Field, wantVerb, action.Verb)
	}
	uidField := controllerRuntimeMutationUIDField(action.Target.Kind)
	if uidField == "" || strings.TrimSpace(action.Target.UID) == "" {
		return errors.New("runtime mutation plan: controller action has no exact kind UID")
	}
	return nil
}

func controllerRuntimeMutationAction(order int, route runtimeMutationRoute, write controller.Action, final map[string]string) (plannedRuntimeMutation, error) {
	return controllerRuntimeMutationActionMode(order, route, write, final, controllerRuntimeMutationForward)
}

func controllerRuntimeMutationActionMode(order int, route runtimeMutationRoute, write controller.Action, final map[string]string, mode string) (plannedRuntimeMutation, error) {
	var err error
	write, err = normalizeControllerRuntimeMutationGuards(write)
	if err != nil {
		return plannedRuntimeMutation{}, err
	}
	if len(write.Args) == 0 || (write.Args[0] != "set-option" && write.Args[0] != "rename-window") {
		return plannedRuntimeMutation{}, fmt.Errorf("controller write %q has an unsupported managed argv", write.Key)
	}
	if err := validateControllerRuntimeMutationGuards(write, false); err != nil {
		return plannedRuntimeMutation{}, err
	}
	if err := validateControllerRuntimeMutationArgv(write, mode); err != nil {
		return plannedRuntimeMutation{}, err
	}
	verb := mutationWriteIdentity
	if write.Args[0] == "rename-window" {
		verb = mutationRenameWindow
	} else if write.Field != "@projmux_project_uid" && write.Field != "@projmux_window_uid" && write.Field != "@projmux_pane_uid" {
		verb = mutationWriteOption
		if class, _ := controllerRuntimeMutationFieldClassFor(controllerRuntimeMutationKind(write), write.Field); class == controllerRuntimeMutationPresentation {
			verb = mutationWritePresentationOption
		}
	}
	kind := controllerRuntimeMutationKind(write)
	uidField := controllerRuntimeMutationUIDField(kind)
	uid := ""
	for _, guard := range write.Guards {
		if guard.Field == uidField {
			if finalUID, ok := final[write.Target+"\x00"+uidField]; ok && strings.TrimSpace(finalUID) != "" {
				uid = strings.TrimSpace(finalUID)
			} else if write.Field == uidField && strings.TrimSpace(write.After) != "" {
				uid = strings.TrimSpace(write.After)
			} else if strings.TrimSpace(guard.Expect) != "" {
				uid = strings.TrimSpace(guard.Expect)
			}
			break
		}
	}
	if uid == "" {
		return plannedRuntimeMutation{}, fmt.Errorf("controller write %q has no stable %s target UID", write.Key, kind)
	}
	parents := make([]string, 0, 2)
	for _, guard := range write.Guards {
		if guard.Field == "session_id" || guard.Field == "window_id" {
			parents = append(parents, guard.Field+"="+guard.Expect)
		}
	}
	parent := "controller.identity/root"
	if len(parents) != 0 {
		parent = "controller.identity/" + strings.Join(parents, "/")
	}
	target := runtimeMutationTarget{
		Kind: kind, ID: write.Target,
		UID: uid, Parent: parent,
	}
	bindRuntimeMutationRouteTarget(&target, route)
	action := newRuntimeMutation(order, verb, target)
	bindRuntimeMutationGuard(&action, "controller action="+write.Key+";exact target guards="+fmt.Sprint(write.Guards)+";exact option before="+write.Field+"="+write.Before)
	action.Operands = slices.Clone(write.Args[1:])
	action.Controller = &runtimeMutationControllerEffect{
		Class: string(func() controllerRuntimeMutationFieldClass {
			class, _ := controllerRuntimeMutationFieldClassFor(kind, write.Field)
			return class
		}()),
		Mode: mode, Scope: string(write.Scope), Field: write.Field, Before: write.Before, After: write.After,
	}
	return action, nil
}

func controllerFinalGuardValue(final map[string]string, write controller.Action, guard controller.Guard) string {
	if value, ok := final[write.Target+"\x00"+guard.Field]; ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(guard.Expect)
}

func controllerRuntimeMutationEffectValue(field, value string) string {
	value = strings.TrimSpace(value)
	if field != "automatic-rename" {
		return value
	}
	switch strings.ToLower(value) {
	case "on", "1":
		return "1"
	case "off", "0":
		return "0"
	default:
		return value
	}
}

func guardControllerRuntimeMutationFinal(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation, write controller.Action, final map[string]string) error {
	if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
		return err
	}
	exact := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
	for _, guard := range write.Guards {
		out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+guard.Field+"}")
		if err != nil || strings.TrimSpace(string(out)) != controllerFinalGuardValue(final, write, guard) {
			return fmt.Errorf("controller rollback target %s final guard %s drifted", write.Target, guard.Field)
		}
	}
	return nil
}

func guardControllerRuntimeMutation(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation, write controller.Action) error {
	if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
		return err
	}
	exact := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
	for _, guard := range write.Guards {
		out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+guard.Field+"}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != strings.TrimSpace(guard.Expect) {
			return fmt.Errorf("controller target %s guard %s drifted", write.Target, guard.Field)
		}
	}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+write.Field+"}")
	if err != nil {
		return err
	}
	if controllerRuntimeMutationEffectValue(write.Field, string(out)) != controllerRuntimeMutationEffectValue(write.Field, write.Before) {
		return fmt.Errorf("controller target %s option %s drifted before write", write.Target, write.Field)
	}
	return nil
}

func observeControllerRuntimeMutation(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation, write controller.Action, final map[string]string) (bool, error) {
	if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
		return false, err
	}
	exact := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
	for _, guard := range write.Guards {
		out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+guard.Field+"}")
		if err != nil {
			return false, err
		}
		observed := strings.TrimSpace(string(out))
		desired := controllerFinalGuardValue(final, write, guard)
		if observed == desired {
			continue
		}
		if observed == strings.TrimSpace(guard.Expect) {
			// Another action in this same all-guards-before-write plan may own
			// this guard's transition (most commonly a blank Window UID claim).
			// The exact Before tuple still identifies the planned object, so do
			// not stop before observing this action's own effect. An asynchronous
			// controller pass may already have converged the option while leaving
			// the sibling UID claim for this plan.
			continue
		}
		return false, fmt.Errorf("controller target %s guard %s drifted", write.Target, guard.Field)
	}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+write.Field+"}")
	return err == nil && controllerRuntimeMutationEffectValue(write.Field, string(out)) == controllerRuntimeMutationEffectValue(write.Field, write.After), err
}

func controllerRuntimeMutationUndo(write controller.Action, route runtimeMutationRoute, final map[string]string) (plannedRuntimeMutation, error) {
	reverse := write
	reverse.After = write.Before
	if len(write.Args) == 0 {
		return plannedRuntimeMutation{}, errors.New("controller rollback has no managed argv")
	}
	if write.Args[0] == "rename-window" {
		reverse.Args = []string{"rename-window", "-t", write.Target, write.Before}
	} else {
		reverse.Args = []string{"set-option"}
		if slices.Contains(write.Args, "-w") {
			reverse.Args = append(reverse.Args, "-w")
		} else if slices.Contains(write.Args, "-p") {
			reverse.Args = append(reverse.Args, "-p")
		}
		if write.Before == "" {
			reverse.Args = append(reverse.Args, "-u", "-t", write.Target, write.Field)
		} else {
			reverse.Args = append(reverse.Args, "-t", write.Target)
			class, _ := controllerRuntimeMutationFieldClassFor(controllerRuntimeMutationKind(write), write.Field)
			if write.Field != tmuxopts.AutomaticRenameWindow && class != controllerRuntimeMutationPresentation {
				reverse.Args = append(reverse.Args, "-q")
			}
			reverse.Args = append(reverse.Args, write.Field, write.Before)
		}
	}
	return controllerRuntimeMutationActionMode(1, route, reverse, final, controllerRuntimeMutationOwnedReverse)
}

// executeControllerRuntimeMutations is the app-internal adapter between the
// public controller plan and Phase 10's printable mutation plan. The public
// controller JSON stays unchanged; only its execution context gains the exact
// physical generation and typed argv seam.
func executeControllerRuntimeMutations(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, writes []controller.Action) error {
	writes = slices.Clone(writes)
	for index := range writes {
		normalized, err := normalizeControllerRuntimeMutationGuards(writes[index])
		if err != nil {
			return err
		}
		writes[index] = normalized
	}
	slices.SortStableFunc(writes, func(a, b controller.Action) int {
		rank := func(action controller.Action) int {
			scope := map[resourcegraph.ObjectKind]int{
				resourcegraph.ObjectSession: 0, resourcegraph.ObjectWindow: 10, resourcegraph.ObjectPane: 20,
			}[action.Scope]
			if action.Field != "@projmux_project_uid" && action.Field != "@projmux_window_uid" && action.Field != "@projmux_pane_uid" {
				scope++
			}
			return scope
		}
		if delta := rank(a) - rank(b); delta != 0 {
			return delta
		}
		return strings.Compare(a.Key, b.Key)
	})
	final := make(map[string]string, len(writes))
	for _, write := range writes {
		final[write.Target+"\x00"+write.Field] = write.After
	}
	steps := make([]runtimeMutationStep, 0, len(writes))
	for index, write := range writes {
		action, err := controllerRuntimeMutationAction(index+1, route, write, final)
		if err != nil {
			return err
		}
		write, action := write, action
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return guardPrintedRuntimeMutationRoute(ctx, runner, route, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				return observeControllerRuntimeMutation(ctx, runner, route, action, write, final)
			},
			Guard: func(ctx context.Context) error {
				return guardControllerRuntimeMutation(ctx, runner, route, action, write)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, runner, action)
				return err
			},
			Undo: func(ctx context.Context) error {
				if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
					return err
				}
				exact := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
				out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+write.Field+"}")
				if err != nil {
					return errors.New("controller rollback target effect is unreadable")
				}
				observed := controllerRuntimeMutationEffectValue(write.Field, string(out))
				if observed == controllerRuntimeMutationEffectValue(write.Field, write.Before) {
					return nil
				}
				if observed != controllerRuntimeMutationEffectValue(write.Field, write.After) {
					return errors.New("controller rollback target no longer carries the exact applied effect")
				}
				if err := guardControllerRuntimeMutationFinal(ctx, runner, route, action, write, final); err != nil {
					return err
				}
				reverse, err := controllerRuntimeMutationUndo(write, route, final)
				if err != nil {
					return err
				}
				_, err = runRuntimeMutationCommand(ctx, runner, reverse)
				return err
			},
		})
	}
	return executeRuntimeMutationPlan(ctx, steps)
}

func controllerRuntimeActionsFromPlannedWrites(writes []plannedTmuxWrite) ([]controller.Action, error) {
	actions := make([]controller.Action, 0, len(writes))
	for _, write := range writes {
		guards := []controller.Guard{}
		if write.guardField != "" {
			guards = append(guards, controller.Guard{Field: write.guardField, Expect: write.guardBefore})
		}
		if write.guardSessionID != "" {
			guards = append(guards, controller.Guard{Field: "session_id", Expect: write.guardSessionID})
		}
		if write.guardWindowID != "" {
			guards = append(guards, controller.Guard{Field: "window_id", Expect: write.guardWindowID})
		}
		action := controller.Action{
			Key: write.itemKey(), Surface: controller.SurfaceTmux, Intent: controller.IntentRepairMirror,
			Authority: controller.AuthorityAllow, Kind: plannedWriteKind(write),
			Scope:  resourcegraph.ObjectKind(strings.ToLower(plannedWriteKind(write))),
			Target: write.target, Field: write.field, Before: write.before, After: write.after,
			Guards: guards, Args: slices.Clone(write.args),
		}
		if err := validateControllerRuntimeMutationGuards(action, action.Scope == resourcegraph.ObjectPane); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, nil
}
