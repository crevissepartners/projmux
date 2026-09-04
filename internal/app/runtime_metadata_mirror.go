package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationMetadataMirror is the app-layer replacement for direct
// metadata Mirror writes in live create/controller reconciliation. Reads stay
// on the generic adapter; every managed write is expanded into printable,
// totally ordered steps here.
type runtimeMutationMetadataMirror struct {
	runner tmuxCommandRunner
	route  runtimeMutationRoute
}

func (m runtimeMutationMetadataMirror) exactRoute(ctx context.Context) (tmuxCommandRunner, runtimeMutationRoute, error) {
	if m.runner == nil {
		return nil, runtimeMutationRoute{}, errors.New("typed metadata mirror requires a tmux runner")
	}
	var base tmuxCommandRunner
	var target tmuxTransport
	switch runner := m.runner.(type) {
	case explicitTmuxRunner:
		base, target = runner.runner, runner.target
	case *explicitTmuxRunner:
		base, target = runner.runner, runner.target
	default:
		return nil, runtimeMutationRoute{}, errors.New("typed metadata mirror requires an explicit tmux route")
	}
	if base == nil || target.Flag() == "" || target.Value == "" {
		return nil, runtimeMutationRoute{}, errors.New("typed metadata mirror route is incomplete")
	}
	if m.route.target.Flag() != "" {
		if !m.route.target.SameRoute(target) || m.route.expectedSocketPath == "" {
			return nil, runtimeMutationRoute{}, errors.New("typed metadata mirror injected route disagrees with its transport")
		}
		if err := guardResolvedRuntimeMutationRoute(ctx, base, m.route); err != nil {
			return nil, runtimeMutationRoute{}, err
		}
		return base, m.route, nil
	}
	route, err := resolveExistingRuntimeMutationRoute(ctx, base, target, nil)
	if err != nil {
		return nil, runtimeMutationRoute{}, fmt.Errorf("typed metadata mirror: bind exact route: %w", err)
	}
	return base, route, nil
}

func (m runtimeMutationMetadataMirror) MirrorProject(ctx context.Context, sessionName string, project coremetadata.Project) error {
	runner, route, err := m.exactRoute(ctx)
	if err != nil {
		return err
	}
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || strings.TrimSpace(project.Metadata.UID) == "" {
		return errors.New("typed metadata mirror requires a Project UID and session name")
	}
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectNameSession+"}", "#{"+tmuxopts.SessionRole+"}")
	observeTuple := func(ctx context.Context) ([]string, error) {
		if err := guardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
			return nil, err
		}
		out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "-F", format)
		rows := splitTmuxRows(string(out), 5)
		if err != nil || len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || rows[0][1] != sessionName {
			return nil, errors.New("typed metadata mirror: Project session containment is unavailable")
		}
		return rows[0], nil
	}
	initial, err := observeTuple(ctx)
	if err != nil {
		return err
	}
	if initial[2] != "" && initial[2] != project.Metadata.UID {
		return errors.New("typed metadata mirror: Project UID is foreign")
	}
	if initial[4] != "" {
		return errors.New("typed metadata mirror: Project session carries a non-Project role")
	}
	target := runtimeMutationTarget{Kind: "session", ID: initial[0], UID: project.Metadata.UID, Parent: "project/" + project.Metadata.UID}
	bindRuntimeMutationRouteTarget(&target, route)
	declarations := []struct{ option, value string }{
		{tmuxopts.ProjectUIDSession, project.Metadata.UID},
		{tmuxopts.ProjectNameSession, project.Metadata.Name},
	}
	steps := make([]runtimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := newRuntimeMutation(index+1, mutationWriteIdentity, target)
		bindRuntimeMutationGuard(&action, "exact Project session tuple and prior mirrors="+strings.Join(initial, "/"))
		action.Operands = []string{"-t", target.ID, "-q", item.option, item.value}
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return guardPrintedRuntimeMutationRoute(ctx, runner, route, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				current, err := observeTuple(ctx)
				if err != nil {
					return false, err
				}
				if current[0] != target.ID || current[1] != initial[1] || current[2] != project.Metadata.UID || current[4] != "" {
					return false, nil
				}
				field := 2
				if item.option == tmuxopts.ProjectNameSession {
					field = 3
				}
				return current[field] == item.value, nil
			},
			Guard: func(ctx context.Context) error {
				current, err := observeTuple(ctx)
				if err != nil {
					return err
				}
				if current[0] != initial[0] || current[1] != initial[1] || current[2] != initial[2] || current[3] != initial[3] || current[4] != initial[4] {
					return errors.New("typed metadata mirror: Project session tuple drifted before write")
				}
				return nil
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return executeRuntimeMutationPlan(ctx, steps)
}

func (m runtimeMutationMetadataMirror) MirrorWindow(ctx context.Context, windowID string, window coremetadata.Window) error {
	windowID = exactTmuxHandle(windowID, "@")
	if windowID == "" || strings.TrimSpace(window.Metadata.UID) == "" || window.Metadata.OwnerRef == nil {
		return errors.New("typed metadata mirror requires exact Window handle, UID, and owner")
	}
	if window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
		return errors.New("typed Project reconciler mirror refuses non-Project Window ownership")
	}
	runner, route, err := m.exactRoute(ctx)
	if err != nil {
		return err
	}
	target := runtimeMutationTarget{
		Kind: "window", ID: windowID, UID: window.Metadata.UID,
		Parent: string(window.Metadata.OwnerRef.Kind) + "/" + window.Metadata.OwnerRef.UID,
	}
	bindRuntimeMutationRouteTarget(&target, route)
	type declaration struct {
		verb     runtimeMutationVerb
		operands []string
		observe  func(context.Context) (bool, error)
	}
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	observeOwnedTarget := func(ctx context.Context) (bool, error) {
		if err := guardTypedMetadataWindow(ctx, runner, route, windowID, window); err != nil {
			return false, err
		}
		out, err := exact.Run(ctx, "tmux", "show-options", "-wqv", "-t", windowID, tmuxopts.WindowUID)
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(string(out)) == window.Metadata.UID, nil
	}
	observeOption := func(option, want string) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			owned, err := observeOwnedTarget(ctx)
			if err != nil || !owned {
				return false, err
			}
			out, err := exact.Run(ctx, "tmux", "show-options", "-wqv", "-t", windowID, option)
			return err == nil && strings.TrimSpace(string(out)) == want, err
		}
	}
	declarations := []declaration{
		{verb: mutationWriteIdentity, operands: []string{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"}, observe: observeOption(tmuxopts.AutomaticRenameWindow, "off")},
		{verb: mutationWriteIdentity, operands: []string{"-w", "-t", windowID, "-q", tmuxopts.WindowUID, window.Metadata.UID}, observe: observeOption(tmuxopts.WindowUID, window.Metadata.UID)},
		{verb: mutationWriteIdentity, operands: []string{"-w", "-t", windowID, "-q", tmuxopts.WindowName, window.Metadata.Name}, observe: observeOption(tmuxopts.WindowName, window.Metadata.Name)},
	}
	steps := make([]runtimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := newRuntimeMutation(index+1, item.verb, target)
		bindRuntimeMutationGuard(&action, "exact app route, Window UID/owner containment, and handle="+windowID)
		action.Operands = item.operands
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return guardPrintedRuntimeMutationRoute(ctx, runner, route, action)
			},
			Reobserve: item.observe,
			Guard: func(ctx context.Context) error {
				return guardTypedMetadataWindow(ctx, runner, route, windowID, window)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return executeRuntimeMutationPlan(ctx, steps)
}

func guardTypedMetadataWindow(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, windowID string, window coremetadata.Window) error {
	if err := guardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
		return err
	}
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", windowID, "-F", tmuxRowFormat(
		"#{window_id}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return err
	}
	rows := splitTmuxRows(string(out), 4)
	if len(rows) != 1 || rows[0][0] != windowID || (rows[0][1] != "" && rows[0][1] != window.Metadata.UID) {
		return errors.New("typed metadata mirror: Window handle or UID drifted")
	}
	owner := window.Metadata.OwnerRef
	if owner == nil {
		return errors.New("typed metadata mirror: Window owner is unknown")
	}
	if owner.Kind != coremetadata.KindProject {
		return errors.New("typed metadata mirror: Window root kind is unsupported")
	}
	if rows[0][2] != owner.UID || rows[0][3] != "" {
		return errors.New("typed metadata mirror: Window Project containment drifted")
	}
	return nil
}

func (m runtimeMutationMetadataMirror) MirrorPane(ctx context.Context, paneID, windowUID string, pane coremetadata.Pane) error {
	paneID = exactTmuxHandle(paneID, "%")
	if paneID == "" || strings.TrimSpace(windowUID) == "" || strings.TrimSpace(pane.Metadata.UID) == "" {
		return errors.New("typed metadata mirror requires exact Pane handle, Window UID, and Pane UID")
	}
	runner, route, err := m.exactRoute(ctx)
	if err != nil {
		return err
	}
	target := runtimeMutationTarget{Kind: "pane", ID: paneID, UID: pane.Metadata.UID, Parent: "window/" + windowUID}
	bindRuntimeMutationRouteTarget(&target, route)
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	observeOwnedTarget := func(ctx context.Context) (bool, error) {
		if err := guardTypedMetadataPane(ctx, runner, route, paneID, windowUID, pane.Metadata.UID); err != nil {
			return false, err
		}
		out, err := exact.Run(ctx, "tmux", "show-options", "-pqv", "-t", paneID, tmuxopts.PaneUID)
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(string(out)) == pane.Metadata.UID, nil
	}
	if pane.Metadata.OwnerRef == nil {
		return errors.New("typed metadata mirror requires Pane stable ownerRef")
	}
	declarations := []struct{ option, value string }{
		{tmuxopts.PaneUID, pane.Metadata.UID},
		{tmuxopts.PaneName, pane.Metadata.Name},
		{tmuxopts.PaneOwnerKind, string(pane.Metadata.OwnerRef.Kind)},
		{tmuxopts.PaneOwnerUID, pane.Metadata.OwnerRef.UID},
		{tmuxopts.PaneRole, string(pane.Spec.Role)},
	}
	if pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
		declarations = append(declarations, struct{ option, value string }{tmuxopts.AgentUIDPane, pane.Metadata.OwnerRef.UID})
	}
	steps := make([]runtimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := newRuntimeMutation(index+1, mutationWriteIdentity, target)
		bindRuntimeMutationGuard(&action, "exact app route, Pane UID and Window containment="+windowUID+"/"+paneID)
		action.Operands = []string{"-p", "-t", paneID, "-q", item.option, item.value}
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return guardPrintedRuntimeMutationRoute(ctx, runner, route, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				owned, err := observeOwnedTarget(ctx)
				if err != nil || !owned {
					return false, err
				}
				out, err := exact.Run(ctx, "tmux", "show-options", "-pqv", "-t", paneID, item.option)
				return err == nil && strings.TrimSpace(string(out)) == item.value, err
			},
			Guard: func(ctx context.Context) error {
				return guardTypedMetadataPane(ctx, runner, route, paneID, windowUID, pane.Metadata.UID)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return executeRuntimeMutationPlan(ctx, steps)
}

func guardTypedMetadataPane(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, paneID, windowUID, paneUID string) error {
	if err := guardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
		return err
	}
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", tmuxRowFormat(
		"#{pane_id}", "#{"+tmuxopts.PaneUID+"}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		return err
	}
	rows := splitTmuxRows(string(out), 3)
	if len(rows) != 1 || rows[0][0] != paneID || rows[0][2] != windowUID || (rows[0][1] != "" && rows[0][1] != paneUID) {
		return errors.New("typed metadata mirror: Pane UID or Window containment drifted")
	}
	return nil
}
