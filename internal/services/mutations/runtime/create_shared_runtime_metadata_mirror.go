package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// RuntimeMutationMetadataMirror is the app-layer replacement for direct
// metadata Mirror writes in live create/controller reconciliation. Reads stay
// on the generic adapter; every managed write is expanded into printable,
// totally ordered steps here.
type RuntimeMutationMetadataMirror struct {
	Runner TmuxCommandRunner
	Route  RuntimeMutationRoute
	// scope, when set, is the Materializer whose runtime-mutation transaction
	// this mirror writes inside. Its route guards then reuse that transaction's
	// identity cache under the same #1012 contract as the Materializer's own
	// guards, and every mirror Apply/Undo passes through the Materializer's
	// guarded-write seam, which drops every reusable proof after the write. So
	// no mirror write relies on a proof older than the previous guarded write of
	// the transaction, and no later guard relies on a proof older than a mirror
	// write. Outside a transaction the cache is nil and every guard reads tmux.
	Scope *Materializer
}

// identity is the open transaction cache the route guards may reuse, or nil.
func (m RuntimeMutationMetadataMirror) Identity() *RuntimeRouteIdentityCache {
	if m.Scope == nil {
		return nil
	}
	return m.Scope.RouteIdentity
}

// guardedSteps routes one mirror plan's steps through the Materializer's
// guarded-write seam when the mirror writes inside a transaction scope; see
// scope. Outside a scope the steps are returned unchanged.
func (m RuntimeMutationMetadataMirror) GuardedSteps(steps []RuntimeMutationStep) []RuntimeMutationStep {
	if m.Scope == nil {
		return steps
	}
	return m.Scope.GuardedWriteSteps(steps)
}

func (m RuntimeMutationMetadataMirror) ExactRoute(ctx context.Context) (TmuxCommandRunner, RuntimeMutationRoute, error) {
	if m.Runner == nil {
		return nil, RuntimeMutationRoute{}, errors.New("typed metadata mirror requires a tmux runner")
	}
	var base TmuxCommandRunner
	var target TmuxTransport
	switch runner := m.Runner.(type) {
	case ExplicitTmuxRunner:
		base, target = runner.Runner, runner.Target
	case *ExplicitTmuxRunner:
		base, target = runner.Runner, runner.Target
	default:
		return nil, RuntimeMutationRoute{}, errors.New("typed metadata mirror requires an explicit tmux route")
	}
	if base == nil || target.Flag() == "" || target.Value == "" {
		return nil, RuntimeMutationRoute{}, errors.New("typed metadata mirror route is incomplete")
	}
	if m.Route.Target.Flag() != "" {
		if !m.Route.Target.SameRoute(target) || m.Route.ExpectedSocketPath == "" {
			return nil, RuntimeMutationRoute{}, errors.New("typed metadata mirror injected route disagrees with its transport")
		}
		if err := GuardResolvedRuntimeMutationRouteWithIdentity(ctx, base, m.Route, m.Identity()); err != nil {
			return nil, RuntimeMutationRoute{}, err
		}
		return base, m.Route, nil
	}
	route, err := ResolveExistingRuntimeMutationRoute(ctx, base, target, nil)
	if err != nil {
		return nil, RuntimeMutationRoute{}, fmt.Errorf("typed metadata mirror: bind exact route: %w", err)
	}
	return base, route, nil
}

func (m RuntimeMutationMetadataMirror) MirrorProject(ctx context.Context, sessionName string, project coremetadata.Project) error {
	runner, route, err := m.ExactRoute(ctx)
	if err != nil {
		return err
	}
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || strings.TrimSpace(project.Metadata.UID) == "" {
		return errors.New("typed metadata mirror requires a Project UID and session name")
	}
	identity := m.Identity()
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	format := TmuxRowFormat("#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectNameSession+"}", "#{"+tmuxopts.SessionRole+"}")
	observeTuple := func(ctx context.Context) ([]string, error) {
		if err := GuardResolvedRuntimeMutationRouteWithIdentity(ctx, runner, route, identity); err != nil {
			return nil, err
		}
		out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "-F", format)
		rows := SplitTmuxRows(string(out), 5)
		if err != nil || len(rows) != 1 || ExactTmuxHandle(rows[0][0], "$") == "" || rows[0][1] != sessionName {
			return nil, errors.New("typed metadata mirror: Project session containment is unavailable")
		}
		return rows[0], nil
	}
	initial, err := observeTuple(ctx)
	if err != nil {
		return err
	}
	// unwritten is the latest tuple observation with no write of this mirror
	// after it. An effect reobservation may answer from it instead of reading
	// the same tuple again; the first Apply clears it, so every observation
	// after a write -- the post-effect proof -- reads tmux. The pre-write Guard
	// always reads tmux: it is the proof that brackets the write.
	unwritten := initial
	reobserveTuple := func(ctx context.Context) ([]string, error) {
		if unwritten != nil {
			return unwritten, nil
		}
		current, err := observeTuple(ctx)
		if err != nil {
			return nil, err
		}
		unwritten = current
		return current, nil
	}
	if initial[2] != "" && initial[2] != project.Metadata.UID {
		return errors.New("typed metadata mirror: Project UID is foreign")
	}
	if initial[4] != "" {
		return errors.New("typed metadata mirror: Project session carries a non-Project role")
	}
	target := RuntimeMutationTarget{Kind: "session", ID: initial[0], UID: project.Metadata.UID, Parent: "project/" + project.Metadata.UID}
	BindRuntimeMutationRouteTarget(&target, route)
	declarations := []struct{ option, value string }{
		{tmuxopts.ProjectUIDSession, project.Metadata.UID},
		{tmuxopts.ProjectNameSession, project.Metadata.Name},
	}
	steps := make([]RuntimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := NewRuntimeMutation(index+1, MutationWriteIdentity, target)
		BindRuntimeMutationGuard(&action, "exact Project session tuple and prior mirrors="+strings.Join(initial, "/"))
		action.Operands = []string{"-t", target.ID, "-q", item.option, item.value}
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return GuardPrintedRuntimeMutationRouteWithIdentity(ctx, runner, route, action, identity)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				current, err := reobserveTuple(ctx)
				if err != nil {
					return false, err
				}
				if current[0] != target.ID || current[1] != initial[1] || current[2] != project.Metadata.UID || current[4] != "" {
					return false, nil
				}
				return current[MirrorProjectTupleField(item.option)] == item.value, nil
			},
			Guard: func(ctx context.Context) error {
				current, err := observeTuple(ctx)
				if err != nil {
					return err
				}
				// The session handle, name, and role are pinned to the planning
				// observation. Each field this plan writes may sit at its initial
				// value or at its planned value: a sibling step's field may be
				// converged by a concurrent writer without changing the target.
				drifted := current[0] != initial[0] || current[1] != initial[1] || current[4] != initial[4]
				for _, written := range declarations {
					field := MirrorProjectTupleField(written.option)
					drifted = drifted || (current[field] != initial[field] && current[field] != written.value)
				}
				if drifted {
					return errors.New("typed metadata mirror: Project session tuple drifted before write")
				}
				field := MirrorProjectTupleField(item.option)
				if current[field] != initial[field] && current[field] == item.value {
					return fmt.Errorf("typed metadata mirror: Project session %s: %w", item.option, ErrRuntimeMutationEffectConverged)
				}
				return nil
			},
			Apply: func(ctx context.Context) error {
				unwritten = nil
				_, err := RunRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedSteps(steps))
}

// MirrorProjectTupleField is the index of a MirrorProject-written option in
// the observed Project session tuple.
func MirrorProjectTupleField(option string) int {
	if option == tmuxopts.ProjectNameSession {
		return 3
	}
	return 2
}

func (m RuntimeMutationMetadataMirror) MirrorWindow(ctx context.Context, windowID string, window coremetadata.Window) error {
	windowID = ExactTmuxHandle(windowID, "@")
	if windowID == "" || strings.TrimSpace(window.Metadata.UID) == "" || window.Metadata.OwnerRef == nil {
		return errors.New("typed metadata mirror requires exact Window handle, UID, and owner")
	}
	if window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
		return errors.New("typed Project reconciler mirror refuses non-Project Window ownership")
	}
	runner, route, err := m.ExactRoute(ctx)
	if err != nil {
		return err
	}
	target := RuntimeMutationTarget{
		Kind: "window", ID: windowID, UID: window.Metadata.UID,
		Parent: string(window.Metadata.OwnerRef.Kind) + "/" + window.Metadata.OwnerRef.UID,
	}
	BindRuntimeMutationRouteTarget(&target, route)
	type declaration struct {
		verb     RuntimeMutationVerb
		operands []string
		observe  func(context.Context) (bool, error)
	}
	identity := m.Identity()
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	observeOwnedTarget := func(ctx context.Context) (bool, error) {
		if err := GuardTypedMetadataWindow(ctx, runner, route, identity, windowID, window); err != nil {
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
		{verb: MutationWriteIdentity, operands: []string{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"}, observe: observeOption(tmuxopts.AutomaticRenameWindow, "off")},
		{verb: MutationWriteIdentity, operands: []string{"-w", "-t", windowID, "-q", tmuxopts.WindowUID, window.Metadata.UID}, observe: observeOption(tmuxopts.WindowUID, window.Metadata.UID)},
		{verb: MutationWriteIdentity, operands: []string{"-w", "-t", windowID, "-q", tmuxopts.WindowName, window.Metadata.Name}, observe: observeOption(tmuxopts.WindowName, window.Metadata.Name)},
	}
	steps := make([]RuntimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := NewRuntimeMutation(index+1, item.verb, target)
		BindRuntimeMutationGuard(&action, "exact app route, Window UID/owner containment, and handle="+windowID)
		action.Operands = item.operands
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return GuardPrintedRuntimeMutationRouteWithIdentity(ctx, runner, route, action, identity)
			},
			Reobserve: item.observe,
			Guard: func(ctx context.Context) error {
				return GuardTypedMetadataWindow(ctx, runner, route, identity, windowID, window)
			},
			Apply: func(ctx context.Context) error {
				_, err := RunRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedSteps(steps))
}

func GuardTypedMetadataWindow(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, identity *RuntimeRouteIdentityCache, windowID string, window coremetadata.Window) error {
	if err := GuardResolvedRuntimeMutationRouteWithIdentity(ctx, runner, route, identity); err != nil {
		return err
	}
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", windowID, "-F", TmuxRowFormat(
		"#{window_id}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return err
	}
	rows := SplitTmuxRows(string(out), 4)
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

func (m RuntimeMutationMetadataMirror) MirrorPane(ctx context.Context, paneID, windowUID string, pane coremetadata.Pane) error {
	paneID = ExactTmuxHandle(paneID, "%")
	if paneID == "" || strings.TrimSpace(windowUID) == "" || strings.TrimSpace(pane.Metadata.UID) == "" {
		return errors.New("typed metadata mirror requires exact Pane handle, Window UID, and Pane UID")
	}
	runner, route, err := m.ExactRoute(ctx)
	if err != nil {
		return err
	}
	target := RuntimeMutationTarget{Kind: "pane", ID: paneID, UID: pane.Metadata.UID, Parent: "window/" + windowUID}
	BindRuntimeMutationRouteTarget(&target, route)
	identity := m.Identity()
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	observeOwnedTarget := func(ctx context.Context) (bool, error) {
		if err := GuardTypedMetadataPane(ctx, runner, route, identity, paneID, windowUID, pane.Metadata.UID); err != nil {
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
	steps := make([]RuntimeMutationStep, 0, len(declarations))
	for index, item := range declarations {
		action := NewRuntimeMutation(index+1, MutationWriteIdentity, target)
		BindRuntimeMutationGuard(&action, "exact app route, Pane UID and Window containment="+windowUID+"/"+paneID)
		action.Operands = []string{"-p", "-t", paneID, "-q", item.option, item.value}
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return GuardPrintedRuntimeMutationRouteWithIdentity(ctx, runner, route, action, identity)
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
				return GuardTypedMetadataPane(ctx, runner, route, identity, paneID, windowUID, pane.Metadata.UID)
			},
			Apply: func(ctx context.Context) error {
				_, err := RunRuntimeMutationCommand(ctx, runner, action)
				return err
			},
		})
	}
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedSteps(steps))
}

func GuardTypedMetadataPane(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, identity *RuntimeRouteIdentityCache, paneID, windowUID, paneUID string) error {
	if err := GuardResolvedRuntimeMutationRouteWithIdentity(ctx, runner, route, identity); err != nil {
		return err
	}
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", TmuxRowFormat(
		"#{pane_id}", "#{"+tmuxopts.PaneUID+"}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		return err
	}
	rows := SplitTmuxRows(string(out), 3)
	if len(rows) != 1 || rows[0][0] != paneID || rows[0][2] != windowUID || (rows[0][1] != "" && rows[0][1] != paneUID) {
		return errors.New("typed metadata mirror: Pane UID or Window containment drifted")
	}
	return nil
}
