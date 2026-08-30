package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type managedRuntimeStopTarget struct {
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	Route       runtimeMutationRoute
}

type projectStopInterruption struct {
	PaneUID    string
	AgentUID   string
	Generation string
}

// managedRuntimeStopAuthority re-reads only Registry authority. Runtime
// identity is observed separately through the exact printed physical socket;
// accepting an ambient runtime view here would split one guard across two
// servers if a logical alias were retargeted between reads.
type managedRuntimeStopAuthority func(context.Context, coremetadata.Kind, string, string) (bool, error)

func executeManagedRuntimeStop(ctx context.Context, runner tmuxCommandRunner, target managedRuntimeStopTarget, authoritative managedRuntimeStopAuthority, stopStore *resourceStore) (err error) {
	defer func() {
		if err != nil {
			err = wrapProjectLifecycleError(coremetadata.ProjectLifecycleStop, "runtime-stop", target.RootUID, target.RootUID, err)
		}
	}()
	if runner == nil || authoritative == nil || exactTmuxHandle(target.SessionID, "$") == "" || strings.TrimSpace(target.RootUID) == "" {
		return errors.New("managed runtime stop requires exact session, root UID, route, and Registry authority")
	}
	if target.RootKind == coremetadata.KindProject &&
		(stopStore == nil || stopStore.load == nil || stopStore.update == nil || stopStore.mutator == nil) {
		return errors.New("managed Project runtime stop requires a writable Registry store for interruption evidence")
	}
	routed := explicitTmuxRunner{runner: runner, target: target.Route.target}
	mutationTarget := runtimeMutationTarget{
		Kind: string(target.RootKind), ID: target.SessionID, UID: target.RootUID,
		Parent: target.SessionName,
	}
	bindRuntimeMutationRouteTarget(&mutationTarget, target.Route)
	action := newRuntimeMutation(1, mutationStopManagedSession, mutationTarget)
	bindRuntimeMutationGuard(&action, "exact managed session="+target.SessionID+"/"+target.SessionName+";root="+string(target.RootKind)+"/"+target.RootUID)
	action.Operands = []string{"-t", target.SessionID}

	// Refuse stale route/runtime/Registry authority before opening the evidence
	// transaction. The mutation plan repeats these guards after the durable
	// commit to close the write-to-kill race.
	if err := guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
		return err
	}
	live, err := observeExactManagedRuntimeStopTarget(ctx, runner, target)
	if err != nil {
		return err
	}
	if !live {
		return errors.New("managed session disappeared before interruption prewrite")
	}
	managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
	if err != nil {
		return err
	}
	if !managed {
		return errors.New("managed session Registry authority drifted before interruption prewrite")
	}

	operationID := ""
	var interruptions []projectStopInterruption
	if target.RootKind == coremetadata.KindProject {
		operationID, err = newCreateOperationID()
		if err != nil {
			return fmt.Errorf("mint Project stop interruption operation: %w", err)
		}
		interruptions, err = recordProjectStopInterruptions(stopStore, target.RootUID, operationID)
		if err != nil {
			return err
		}
	}
	compensate := func(cause error) error {
		if len(interruptions) == 0 {
			return cause
		}
		if compensationErr := clearProjectStopInterruptions(stopStore, interruptions, operationID); compensationErr != nil {
			return errors.Join(cause, fmt.Errorf("same-operation interruption compensation failed for source=%s classification=%s operation=%s activations=%s: %w",
				coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
				projectStopInterruptionSummary(interruptions), compensationErr))
		}
		return cause
	}

	applyAttempted := false
	planErr := executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			live, err := observeExactManagedRuntimeStopTarget(ctx, runner, target)
			if err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			if !live {
				return true, nil
			}
			managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
			if err != nil {
				return false, err
			}
			if !managed {
				return false, errors.New("managed runtime Registry authority disappeared before stop")
			}
			return false, nil
		},
		Guard: func(ctx context.Context) error {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
				return err
			}
			live, err := observeExactManagedRuntimeStopTarget(ctx, runner, target)
			if err != nil {
				return err
			}
			if !live {
				return errors.New("managed session disappeared before stop")
			}
			managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
			if err != nil {
				return err
			}
			if !managed {
				return errors.New("managed session Registry authority drifted before stop")
			}
			if target.RootKind == coremetadata.KindProject {
				if err := verifyProjectStopInterruptions(stopStore, target.RootUID, interruptions, operationID); err != nil {
					return err
				}
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			applyAttempted = true
			_, err := runRuntimeMutationCommand(ctx, routed, action)
			return err
		},
	}})
	if !applyAttempted {
		return compensate(planErr)
	}
	if planErr == nil {
		return nil
	}

	// tmux may return an error after applying kill-session. Retain the receipt
	// only when exact reobservation proves the target Session absent. A proved-
	// live target means this stop did not interrupt it and must withdraw only
	// the receipts carrying this operation id.
	live, observeErr := observeExactManagedRuntimeStopTarget(ctx, runner, target)
	if observeErr == nil && live {
		return compensate(planErr)
	}
	if observeErr != nil && !inttmux.IsNoServerFailure(observeErr) {
		return errors.Join(planErr, fmt.Errorf("project stop effect could not be proved live or absent; retaining source=%s classification=%s operation=%s activations=%s: %w",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
			projectStopInterruptionSummary(interruptions), observeErr))
	}
	return planErr
}

// recordProjectStopInterruptions atomically records one receipt for every and
// only currently Running Agent whose exact Pane activation belongs to project.
// Any malformed/stale ownership aborts the whole Store.Update, so no subset can
// become durable and the caller has not touched tmux yet.
func recordProjectStopInterruptions(store *resourceStore, projectUID, operationID string) ([]projectStopInterruption, error) {
	if store == nil || store.update == nil || store.mutator == nil {
		return nil, errors.New("project stop interruption Registry store is not configured")
	}
	projectUID = strings.TrimSpace(projectUID)
	operationID = strings.TrimSpace(operationID)
	if projectUID == "" || operationID == "" {
		return nil, errors.New("project stop interruption requires Project UID and operation id")
	}
	var interruptions []projectStopInterruption
	_, err := store.update(func(working *coremetadata.Registry) error {
		if _, ok := working.Project(projectUID); !ok {
			return fmt.Errorf("project %q disappeared before interruption prewrite", projectUID)
		}
		windowUIDs := make(map[string]bool)
		for _, window := range working.WindowsOf(projectUID) {
			windowUIDs[window.Metadata.UID] = true
		}
		mutator := store.mutator()
		for i := range working.Agents {
			agent := &working.Agents[i]
			if agent.Status.Phase != coremetadata.PhaseRunning || agent.Metadata.OwnerRef == nil ||
				agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow || !windowUIDs[agent.Metadata.OwnerRef.UID] {
				continue
			}
			pane, ok := working.Pane(agent.Status.PaneRef)
			if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
				pane.Metadata.OwnerRef.UID != agent.Metadata.UID || pane.Status.Activation.AgentUID != agent.Metadata.UID ||
				strings.TrimSpace(pane.Status.Activation.Generation) == "" {
				return fmt.Errorf("running Agent %s has no exact current Pane activation; interruption prewrite refused",
					agent.Metadata.UID)
			}
			receipt := coremetadata.TerminationEvidence{
				Source:         coremetadata.TerminationSourceControlAction,
				Classification: coremetadata.TerminationInterrupted,
				PaneUID:        pane.Metadata.UID,
				AgentUID:       agent.Metadata.UID,
				Generation:     pane.Status.Activation.Generation,
				OperationID:    operationID,
			}
			outcome, err := mutator.RecordTermination(working, receipt)
			if err != nil {
				return err
			}
			if !outcome.Applied && !outcome.Duplicate {
				return fmt.Errorf("running Agent %s interruption receipt refused: %s", agent.Metadata.UID, outcome.Reason)
			}
			storedPane, _ := working.Pane(pane.Metadata.UID)
			storedAgent, _ := working.Agent(agent.Metadata.UID)
			if !matchingProjectStopInterruption(storedPane.Status.LastTermination, receipt) ||
				!matchingProjectStopInterruption(storedAgent.Status.LastTermination, receipt) {
				return fmt.Errorf("running Agent %s interruption receipt did not reach Pane and Agent", agent.Metadata.UID)
			}
			interruptions = append(interruptions, projectStopInterruption{
				PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID, Generation: pane.Status.Activation.Generation,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("project stop interruption prewrite failed for source=%s classification=%s; zero tmux mutations were attempted: %w",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, MapMetadataError(err))
	}
	return interruptions, nil
}

func matchingProjectStopInterruption(stored *coremetadata.TerminationEvidence, want coremetadata.TerminationEvidence) bool {
	return stored != nil && stored.Source == coremetadata.TerminationSourceControlAction &&
		stored.Classification == coremetadata.TerminationInterrupted && stored.PaneUID == want.PaneUID &&
		stored.AgentUID == want.AgentUID && stored.Generation == want.Generation && stored.OperationID == want.OperationID
}

func verifyProjectStopInterruptions(store *resourceStore, projectUID string, interruptions []projectStopInterruption, operationID string) error {
	registry, err := store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	windowUIDs := make(map[string]bool)
	for _, window := range registry.WindowsOf(projectUID) {
		windowUIDs[window.Metadata.UID] = true
	}
	expected := make(map[string]projectStopInterruption, len(interruptions))
	for _, interruption := range interruptions {
		expected[interruption.PaneUID] = interruption
	}
	seen := make(map[string]bool, len(interruptions))
	for i := range registry.Agents {
		agent := &registry.Agents[i]
		if agent.Status.Phase != coremetadata.PhaseRunning || agent.Metadata.OwnerRef == nil ||
			agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow || !windowUIDs[agent.Metadata.OwnerRef.UID] {
			continue
		}
		pane, ok := registry.Pane(agent.Status.PaneRef)
		want, selected := expected[agent.Status.PaneRef]
		if !ok || !selected || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
			pane.Metadata.OwnerRef.UID != agent.Metadata.UID || want.AgentUID != agent.Metadata.UID ||
			want.Generation != pane.Status.Activation.Generation || pane.Status.Activation.AgentUID != agent.Metadata.UID {
			return fmt.Errorf("project stop interruption authority drifted before kill for source=%s classification=%s operation=%s activations=%s",
				coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
				projectStopInterruptionSummary(interruptions))
		}
		receipt := coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceControlAction, Classification: coremetadata.TerminationInterrupted,
			PaneUID: want.PaneUID, AgentUID: want.AgentUID, Generation: want.Generation, OperationID: operationID,
		}
		if !matchingProjectStopInterruption(pane.Status.LastTermination, receipt) ||
			!matchingProjectStopInterruption(agent.Status.LastTermination, receipt) {
			return fmt.Errorf("project stop interruption receipt drifted before kill for source=%s classification=%s operation=%s activation=%s",
				coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
				projectStopInterruptionSummary([]projectStopInterruption{want}))
		}
		seen[want.PaneUID] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("project stop Running Agent set drifted before kill for source=%s classification=%s operation=%s activations=%s",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
			projectStopInterruptionSummary(interruptions))
	}
	return nil
}

func clearProjectStopInterruptions(store *resourceStore, interruptions []projectStopInterruption, operationID string) error {
	if len(interruptions) == 0 {
		return nil
	}
	_, err := store.update(func(working *coremetadata.Registry) error {
		mutator := store.mutator()
		for _, interruption := range interruptions {
			if _, err := mutator.ClearTermination(working, interruption.PaneUID, operationID); err != nil {
				return err
			}
		}
		return nil
	})
	return MapMetadataError(err)
}

func projectStopInterruptionSummary(interruptions []projectStopInterruption) string {
	parts := make([]string, 0, len(interruptions))
	for _, interruption := range interruptions {
		parts = append(parts, fmt.Sprintf("pane=%s/agent=%s/generation=%s",
			interruption.PaneUID, interruption.AgentUID, interruption.Generation))
	}
	return strings.Join(parts, ",")
}

func managedRuntimeStopRegistryAuthority(load func() (coremetadata.Registry, error)) managedRuntimeStopAuthority {
	return func(_ context.Context, kind coremetadata.Kind, uid, sessionName string) (bool, error) {
		if load == nil {
			return false, errors.New("managed runtime Registry reader is not configured")
		}
		registry, err := load()
		if err != nil {
			return false, fmt.Errorf("re-read managed runtime Registry authority: %w", err)
		}
		switch kind {
		case coremetadata.KindProject:
			project, ok := registry.Project(uid)
			if !ok {
				return false, nil
			}
			state := coremetadata.ProjectLifecycleRetainedWindows
			if len(registry.WindowsOf(project.Metadata.UID)) == 0 {
				state = coremetadata.ProjectLifecycleZeroWindows
			}
			decision := coremetadata.DecideProjectLifecycle(state, coremetadata.ProjectLifecycleStop, coremetadata.ProjectLifecyclePreconditions{})
			if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationStop,
				coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsPreserved,
				coremetadata.ProjectStartupWriteStopRuntime); err != nil {
				return false, nil
			}
			return true, nil
		case coremetadata.KindControlSession:
			control, ok := registry.ControlSession(uid)
			return ok && control.Spec.Session == sessionName, nil
		default:
			return false, fmt.Errorf("managed runtime stop has unknown Registry root kind %q", kind)
		}
	}
}

func observeExactManagedRuntimeStopTarget(ctx context.Context, runner tmuxCommandRunner, target managedRuntimeStopTarget) (bool, error) {
	if target.Route.expectedSocketPath == "" {
		return false, errors.New("managed runtime stop has no physical socket authority")
	}
	exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: filepath.Clean(target.Route.expectedSocketPath), Source: tmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "list-sessions", "-F", tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range splitTmuxRows(string(out), 4) {
		if row[0] != target.SessionID {
			continue
		}
		if row[1] != target.SessionName {
			return false, errors.New("managed runtime session name drifted on exact physical route")
		}
		switch target.RootKind {
		case coremetadata.KindProject:
			if row[2] != target.RootUID || row[3] != "" {
				return false, errors.New("managed Project attribution drifted on exact physical route")
			}
		case coremetadata.KindControlSession:
			if row[2] != "" || row[3] != resourcegraph.ControlSessionRole {
				return false, errors.New("managed ControlSession attribution drifted on exact physical route")
			}
		default:
			return false, errors.New("managed runtime stop has unknown root kind")
		}
		return true, nil
	}
	return false, nil
}

func guardResolvedRuntimeMutationRoute(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute) error {
	return guardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, false)
}

// guardResolvedRuntimeMutationRouteBeforeMarkerWrite preserves the physical
// socket, server-generation, and app-ownership authority of the normal route
// guard while allowing the logical marker to be absent for the one action that
// creates it. An already-present foreign marker is still drift. Callers must
// use guardResolvedRuntimeMutationRoute after the write to prove the effect.
func guardResolvedRuntimeMutationRouteBeforeMarkerWrite(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute) error {
	return guardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, true)
}

func guardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, allowMissingLogicalMarker bool) error {
	if runner == nil || route.target.Flag() == "" || route.target.Value == "" {
		return errors.New("runtime mutation route is not exact")
	}
	// Once a physical socket has been observed, it is the execution authority.
	// Re-resolving the logical alias here would let an alias replacement make a
	// pre-observation report the effect from the wrong server.
	probeTarget := route.target
	if route.expectedSocketPath != "" {
		probeTarget = tmuxTransport{Kind: tmuxSocketPath, Value: filepath.Clean(route.expectedSocketPath), Source: tmuxSocketPathSource}
	}
	routed := explicitTmuxRunner{runner: runner, target: probeTarget}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return fmt.Errorf("reobserve planned runtime socket: %w", err)
	}
	observed := filepath.Clean(strings.TrimSpace(string(out)))
	if route.expectedSocketPath != "" && observed != filepath.Clean(route.expectedSocketPath) {
		return fmt.Errorf("runtime socket drifted: observed %q, planned %q", observed, filepath.Clean(route.expectedSocketPath))
	}
	if route.authority != nil {
		if (route.authority.Class == runtimeMutationRouteStandalone || route.authority.Class == runtimeMutationRouteStandaloneExplicit) &&
			(route.target.Flag() != "-S" || route.target.Value != route.expectedSocketPath) {
			return errors.New("planned standalone runtime route is not exact physical authority")
		}
		pidOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		if err != nil || strings.TrimSpace(string(pidOut)) != route.authority.ServerPID {
			return errors.New("planned runtime server generation drifted")
		}
		if route.authority.Class == runtimeMutationRouteStandalone || route.authority.Class == runtimeMutationRouteStandaloneExplicit {
			appOwned, appErr := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
			logical, logicalErr := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if appErr != nil || logicalErr != nil || strings.TrimSpace(string(appOwned)) != "" || strings.TrimSpace(string(logical)) != "" {
				return errors.New("planned standalone runtime route class drifted")
			}
			return nil
		}
		if route.authority.Class != runtimeMutationRouteApp {
			return errors.New("planned runtime route has an unknown authority class")
		}
	}
	appOwned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil || strings.TrimSpace(string(appOwned)) != "1" {
		return errors.New("planned runtime socket is not app-owned")
	}
	logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	logicalName := strings.TrimSpace(string(logical))
	if err != nil || (logicalName != route.socketName && !(allowMissingLogicalMarker && logicalName == "")) {
		return errors.New("planned runtime socket logical route marker drifted")
	}
	return nil
}

func guardPrintedRuntimeMutationRoute(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation) error {
	return guardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, action, false)
}

// guardPrintedRuntimeMutationRouteBeforeMarkerWrite is deliberately limited to
// the route-marker verb. It keeps the printable/execution tuple checks exact,
// then applies the only valid phase exception: the desired logical marker may
// not exist until this action executes.
func guardPrintedRuntimeMutationRouteBeforeMarkerWrite(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation) error {
	if action.Verb != mutationWriteRouteMarker {
		return errors.New("pre-marker route guard requires a write-route-marker action")
	}
	return guardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, action, true)
}

func guardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation, allowMissingLogicalMarker bool) error {
	printed := strings.TrimSpace(action.Target.PhysicalSocket)
	if printed == runtimeMutationSocketAbsentBeforeCreate {
		if route.expectedSocketPath != "" || (action.Verb != mutationCreateSession && action.Verb != mutationBootstrapControlSession) {
			return errors.New("printed absent-before-create socket disagrees with execution route")
		}
		if action.Target.RouteAuthority != "" || route.authority != nil {
			return errors.New("printed absent-before-create action unexpectedly claims a server generation")
		}
	} else if filepath.Clean(printed) != filepath.Clean(route.expectedSocketPath) {
		return fmt.Errorf("printed physical socket %q disagrees with execution route %q", printed, route.expectedSocketPath)
	} else {
		if route.authority == nil || action.Target.RouteAuthority == "" || action.Target.RouteAuthority != route.authority.printable() {
			return errors.New("printed runtime route authority disagrees with captured server generation")
		}
		printedRoute := route.target.Flag() + "=" + route.target.Value
		if action.Verb == mutationWriteRouteMarker {
			printedRoute = "-S=" + route.expectedSocketPath
		}
		if action.Target.Socket != printedRoute {
			return errors.New("printed runtime execution route disagrees with captured route")
		}
	}
	if allowMissingLogicalMarker {
		return guardResolvedRuntimeMutationRouteBeforeMarkerWrite(ctx, runner, route)
	}
	return guardResolvedRuntimeMutationRoute(ctx, runner, route)
}
