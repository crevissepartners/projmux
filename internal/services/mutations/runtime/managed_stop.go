package runtime

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
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

type StopStore struct {
	Interruptions func(string, []ProjectStopInterruption)
	Load          func() (coremetadata.Registry, error)
	Update        func(func(*coremetadata.Registry) error) (coremetadata.Registry, error)
	Mutator       func() coremetadata.Mutator
	MapError      func(error) error
	WrapError     func(coremetadata.ProjectLifecycleAction, string, string, string, error) error
}

func (s *StopStore) mapError(err error) error {
	if s.MapError != nil {
		return s.MapError(err)
	}
	return mutations.Classify(err)
}
func (s *StopStore) wrapError(action coremetadata.ProjectLifecycleAction, stage, oldUID, newUID string, err error) error {
	if s != nil && s.WrapError != nil {
		return s.WrapError(action, stage, oldUID, newUID, err)
	}
	return fmt.Errorf("project lifecycle action=%s stage=%s old_uid=%s new_uid=%s: %w", action, stage, oldUID, newUID, err)
}
func BindRuntimeMutationRouteTarget(target *RuntimeMutationTarget, route RuntimeMutationRoute) {
	if target == nil {
		return
	}
	target.Socket = route.Target.Flag() + "=" + route.Target.Value
	target.PhysicalSocket = PrintableRuntimeMutationSocket(route.ExpectedSocketPath)
	target.RouteAuthority = route.Authority.Printable()
}

type ManagedRuntimeStopTarget struct {
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	Route       RuntimeMutationRoute
}

type ProjectStopInterruption struct {
	PaneUID    string
	AgentUID   string
	Generation string
}

// ManagedRuntimeStopAuthority re-reads only Registry authority. Runtime
// identity is observed separately through the exact printed physical socket;
// accepting an ambient runtime view here would split one guard across two
// servers if a logical alias were retargeted between reads.
type ManagedRuntimeStopAuthority func(context.Context, coremetadata.Kind, string, string) (bool, error)

func ExecuteManagedRuntimeStop(ctx context.Context, runner TmuxCommandRunner, target ManagedRuntimeStopTarget, authoritative ManagedRuntimeStopAuthority, stopStore *StopStore) (err error) {
	defer func() {
		if err != nil {
			err = stopStore.wrapError(coremetadata.ProjectLifecycleStop, "runtime-stop", target.RootUID, target.RootUID, err)
		}
	}()
	if runner == nil || authoritative == nil || ExactTmuxHandle(target.SessionID, "$") == "" || strings.TrimSpace(target.RootUID) == "" {
		return errors.New("managed runtime stop requires exact session, root UID, route, and Registry authority")
	}
	if target.RootKind == coremetadata.KindProject &&
		(stopStore == nil || stopStore.Load == nil || stopStore.Update == nil || stopStore.Mutator == nil) {
		return errors.New("managed Project runtime stop requires a writable Registry store for interruption evidence")
	}
	routed := ExplicitTmuxRunner{Runner: runner, Target: target.Route.Target}
	mutationTarget := RuntimeMutationTarget{
		Kind: string(target.RootKind), ID: target.SessionID, UID: target.RootUID,
		Parent: target.SessionName,
	}
	BindRuntimeMutationRouteTarget(&mutationTarget, target.Route)
	action := NewRuntimeMutation(1, MutationStopManagedSession, mutationTarget)
	BindRuntimeMutationGuard(&action, "exact managed session="+target.SessionID+"/"+target.SessionName+";root="+string(target.RootKind)+"/"+target.RootUID)
	action.Operands = []string{"-t", target.SessionID}

	// Refuse stale route/runtime/Registry authority before opening the evidence
	// transaction. The mutation plan repeats these guards after the durable
	// commit to close the write-to-kill race.
	if err := GuardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
		return err
	}
	live, err := ObserveExactManagedRuntimeStopTarget(ctx, runner, target)
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
	var interruptions []ProjectStopInterruption
	if target.RootKind == coremetadata.KindProject {
		operationID, err = mutations.NewOperationID()
		if err != nil {
			return fmt.Errorf("mint Project stop interruption operation: %w", err)
		}
		interruptions, err = RecordProjectStopInterruptions(stopStore, target.RootUID, operationID)
		if err != nil {
			return err
		}
	}
	if stopStore != nil && stopStore.Interruptions != nil {
		stopStore.Interruptions(operationID, interruptions)
	}
	compensate := func(cause error) error {
		if len(interruptions) == 0 {
			return cause
		}
		if compensationErr := ClearProjectStopInterruptions(stopStore, interruptions, operationID); compensationErr != nil {
			return errors.Join(cause, fmt.Errorf("same-operation interruption compensation failed for source=%s classification=%s operation=%s activations=%s: %w",
				coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
				ProjectStopInterruptionSummary(interruptions), compensationErr))
		}
		return cause
	}

	// observeAbsent is the one exact-absence predicate Reobserve and Guard
	// share: no server behind the printed route, or the exact $N unlisted.
	// Name or attribution drift on a listed $N stays an observation error.
	observeAbsent := func(ctx context.Context) (bool, error) {
		if err := GuardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
			if inttmux.IsNoServerFailure(err) {
				return true, nil
			}
			return false, err
		}
		live, err := ObserveExactManagedRuntimeStopTarget(ctx, runner, target)
		if err != nil {
			if inttmux.IsNoServerFailure(err) {
				return true, nil
			}
			return false, err
		}
		return !live, nil
	}
	applyAttempted := false
	planErr := ExecuteRuntimeMutationPlan(ctx, []RuntimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return GuardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			absent, err := observeAbsent(ctx)
			if err != nil || absent {
				return absent, err
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
			// A concurrent writer may remove the exact Session after
			// Reobserve. Its absence is this step's goal, so the Guard's own
			// read reports it converged rather than drifted.
			absent, err := observeAbsent(ctx)
			if err != nil {
				return err
			}
			if absent {
				return RuntimeMutationAbsentTargetConverged("managed session " + target.SessionID)
			}
			managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
			if err != nil {
				return err
			}
			if !managed {
				return errors.New("managed session Registry authority drifted before stop")
			}
			if target.RootKind == coremetadata.KindProject {
				if err := VerifyProjectStopInterruptions(stopStore, target.RootUID, interruptions, operationID); err != nil {
					return err
				}
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			applyAttempted = true
			_, err := RunRuntimeMutationCommand(ctx, routed, action)
			return err
		},
	}})
	if !applyAttempted {
		if planErr != nil {
			return compensate(planErr)
		}
		// Reobserve proved the exact Session already absent, so this stop
		// interrupted nothing yet still succeeds on a proved-absent target.
		return errors.Join(compensate(nil), RecordManagedRuntimeStopSessionNotLive(stopStore, target))
	}
	if planErr == nil {
		return errors.Join(RecordManagedRuntimeStopSessionNotLive(stopStore, target),
			ProjectProjectStopInterruptions(stopStore, interruptions, operationID))
	}

	// tmux may return an error after applying kill-session. Retain the receipt
	// only when exact reobservation proves the target Session absent. A proved-
	// live target means this stop did not interrupt it and must withdraw only
	// the receipts carrying this operation id.
	live, observeErr := ObserveExactManagedRuntimeStopTarget(ctx, runner, target)
	if observeErr == nil && live {
		return compensate(planErr)
	}
	if observeErr != nil && !inttmux.IsNoServerFailure(observeErr) {
		return errors.Join(planErr, fmt.Errorf("project stop effect could not be proved live or absent; retaining source=%s classification=%s operation=%s activations=%s: %w",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
			ProjectStopInterruptionSummary(interruptions), observeErr))
	}
	return errors.Join(planErr, RecordManagedRuntimeStopSessionNotLive(stopStore, target),
		ProjectProjectStopInterruptions(stopStore, interruptions, operationID))
}

// ProjectProjectStopInterruptions lowers the Agents this stop interrupted once
// exact reobservation proved their Session absent. Nothing else would: the
// window-unlinked hook deliberately projects nothing, and no pane-exit hook
// fires for a killed Session, so without this pass an Agent stays Running on a
// vanished Pane until an unrelated route happens to reconcile.
//
// The absence proof covers only the Panes that carried this operation's
// receipt, so only those are projected, and only while that exact receipt and
// its activation generation are still current. A Pane relaunched or re-stamped
// in between is someone else's state and is left for the reconciler. The
// projection itself is the reconciler's own transition, so the phase and reason
// are the ones that pass would have written later.
func ProjectProjectStopInterruptions(store *StopStore, interruptions []ProjectStopInterruption, operationID string) error {
	if len(interruptions) == 0 {
		return nil
	}
	_, err := store.Update(func(working *coremetadata.Registry) error {
		mutator := store.Mutator()
		for _, interruption := range interruptions {
			pane, ok := working.Pane(interruption.PaneUID)
			if !ok || pane.Status.Activation.Generation != interruption.Generation ||
				!MatchingProjectStopInterruption(pane.Status.LastTermination, coremetadata.TerminationEvidence{
					PaneUID: interruption.PaneUID, AgentUID: interruption.AgentUID,
					Generation: interruption.Generation, OperationID: operationID,
				}) {
				continue
			}
			if _, err := mutator.ProjectTermination(working, coremetadata.TerminationProjectionInput{
				PaneUID: interruption.PaneUID, Generation: interruption.Generation,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("project stop removed its Session but could not lower the interrupted Agents (operation=%s activations=%s) to their terminal phase: %w",
			operationID, ProjectStopInterruptionSummary(interruptions), store.mapError(err))
	}
	return nil
}

// RecordManagedRuntimeStopSessionNotLive lowers the stored Project session
// projection once exact reobservation proved the target Session absent. It is
// written after the kill, not prewritten with compensation like interruption
// receipts: the create routes read status.session as their materialization
// preflight, so a prewrite would publish live=false while the Session still
// runs, and a failed compensation would leave a live Session reported offline.
// Only the projection naming this exact Session is lowered; a missing Project,
// an absent projection, or one naming another Session stays untouched.
func RecordManagedRuntimeStopSessionNotLive(store *StopStore, target ManagedRuntimeStopTarget) error {
	if target.RootKind != coremetadata.KindProject {
		return nil
	}
	_, err := store.Update(func(working *coremetadata.Registry) error {
		project, ok := working.Project(target.RootUID)
		if !ok || project.Status.Session == nil || project.Status.Session.Name != target.SessionName ||
			!project.Status.Session.Live {
			return nil
		}
		_, err := store.Mutator().BindOfflineProjectSession(working, target.RootUID, project.Status.Session.Name)
		return err
	})
	if err != nil {
		return fmt.Errorf("managed session %s was stopped but Project %s session projection could not be recorded as not live: %w",
			target.SessionName, target.RootUID, store.mapError(err))
	}
	return nil
}

// RecordProjectStopInterruptions atomically records one receipt for every and
// only currently Running Agent whose exact Pane activation belongs to project.
// Any malformed/stale ownership aborts the whole Store.Update, so no subset can
// become durable and the caller has not touched tmux yet.
func RecordProjectStopInterruptions(store *StopStore, projectUID, operationID string) ([]ProjectStopInterruption, error) {
	if store == nil || store.Update == nil || store.Mutator == nil {
		return nil, errors.New("project stop interruption Registry store is not configured")
	}
	projectUID = strings.TrimSpace(projectUID)
	operationID = strings.TrimSpace(operationID)
	if projectUID == "" || operationID == "" {
		return nil, errors.New("project stop interruption requires Project UID and operation id")
	}
	var interruptions []ProjectStopInterruption
	_, err := store.Update(func(working *coremetadata.Registry) error {
		if _, ok := working.Project(projectUID); !ok {
			return fmt.Errorf("project %q disappeared before interruption prewrite", projectUID)
		}
		windowUIDs := make(map[string]bool)
		for _, window := range working.WindowsOf(projectUID) {
			windowUIDs[window.Metadata.UID] = true
		}
		mutator := store.Mutator()
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
			if !MatchingProjectStopInterruption(storedPane.Status.LastTermination, receipt) ||
				!MatchingProjectStopInterruption(storedAgent.Status.LastTermination, receipt) {
				return fmt.Errorf("running Agent %s interruption receipt did not reach Pane and Agent", agent.Metadata.UID)
			}
			interruptions = append(interruptions, ProjectStopInterruption{
				PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID, Generation: pane.Status.Activation.Generation,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("project stop interruption prewrite failed for source=%s classification=%s; zero tmux mutations were attempted: %w",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, store.mapError(err))
	}
	return interruptions, nil
}

func MatchingProjectStopInterruption(stored *coremetadata.TerminationEvidence, want coremetadata.TerminationEvidence) bool {
	return stored != nil && stored.Source == coremetadata.TerminationSourceControlAction &&
		stored.Classification == coremetadata.TerminationInterrupted && stored.PaneUID == want.PaneUID &&
		stored.AgentUID == want.AgentUID && stored.Generation == want.Generation && stored.OperationID == want.OperationID
}

func VerifyProjectStopInterruptions(store *StopStore, projectUID string, interruptions []ProjectStopInterruption, operationID string) error {
	registry, err := store.Load()
	if err != nil {
		return store.mapError(err)
	}
	windowUIDs := make(map[string]bool)
	for _, window := range registry.WindowsOf(projectUID) {
		windowUIDs[window.Metadata.UID] = true
	}
	expected := make(map[string]ProjectStopInterruption, len(interruptions))
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
				ProjectStopInterruptionSummary(interruptions))
		}
		receipt := coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceControlAction, Classification: coremetadata.TerminationInterrupted,
			PaneUID: want.PaneUID, AgentUID: want.AgentUID, Generation: want.Generation, OperationID: operationID,
		}
		if !MatchingProjectStopInterruption(pane.Status.LastTermination, receipt) ||
			!MatchingProjectStopInterruption(agent.Status.LastTermination, receipt) {
			return fmt.Errorf("project stop interruption receipt drifted before kill for source=%s classification=%s operation=%s activation=%s",
				coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
				ProjectStopInterruptionSummary([]ProjectStopInterruption{want}))
		}
		seen[want.PaneUID] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("project stop Running Agent set drifted before kill for source=%s classification=%s operation=%s activations=%s",
			coremetadata.TerminationSourceControlAction, coremetadata.TerminationInterrupted, operationID,
			ProjectStopInterruptionSummary(interruptions))
	}
	return nil
}

func ClearProjectStopInterruptions(store *StopStore, interruptions []ProjectStopInterruption, operationID string) error {
	if len(interruptions) == 0 {
		return nil
	}
	_, err := store.Update(func(working *coremetadata.Registry) error {
		mutator := store.Mutator()
		for _, interruption := range interruptions {
			if _, err := mutator.ClearTermination(working, interruption.PaneUID, operationID); err != nil {
				return err
			}
		}
		return nil
	})
	return store.mapError(err)
}

func ProjectStopInterruptionSummary(interruptions []ProjectStopInterruption) string {
	parts := make([]string, 0, len(interruptions))
	for _, interruption := range interruptions {
		parts = append(parts, fmt.Sprintf("pane=%s/agent=%s/generation=%s",
			interruption.PaneUID, interruption.AgentUID, interruption.Generation))
	}
	return strings.Join(parts, ",")
}

func ManagedRuntimeStopRegistryAuthority(load func() (coremetadata.Registry, error)) ManagedRuntimeStopAuthority {
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
			if err := mutations.RequireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationStop,
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

func ObserveExactManagedRuntimeStopTarget(ctx context.Context, runner TmuxCommandRunner, target ManagedRuntimeStopTarget) (bool, error) {
	if target.Route.ExpectedSocketPath == "" {
		return false, errors.New("managed runtime stop has no physical socket authority")
	}
	exact := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: filepath.Clean(target.Route.ExpectedSocketPath), Source: TmuxSocketPathSource}}
	out, err := exact.Run(ctx, "tmux", "list-sessions", "-F", TmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range SplitTmuxRows(string(out), 4) {
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
