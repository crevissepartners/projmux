package codexhandover

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type staticRegistry struct{ registry metadata.Registry }

func (store staticRegistry) LoadSnapshot() (metadata.Registry, error) {
	return store.registry.Clone(), nil
}

type recordingEffects struct {
	seen                map[string]bool
	calls               []string
	recoveryInvocations int
	recover             func(codexupgrade.GenerationRoute) (codexgenerationhost.LaunchProof, error)
	fail                map[string]error
}

func (effects *recordingEffects) EnsureSameGenerationRecovered(_ context.Context, op string, route codexupgrade.GenerationRoute) (codexgenerationhost.LaunchProof, error) {
	effects.recoveryInvocations++
	if err := effects.once(op + ":recover:" + route.Generation.Endpoint.EndpointGenerationID); err != nil {
		return codexgenerationhost.LaunchProof{}, err
	}
	if effects.recover != nil {
		return effects.recover(route)
	}
	if route.Proof == nil {
		return codexgenerationhost.LaunchProof{}, errors.New("missing recovery proof")
	}
	return *route.Proof, nil
}

func (effects *recordingEffects) once(key string) error {
	if effects.seen == nil {
		effects.seen = map[string]bool{}
	}
	if !effects.seen[key] {
		effects.seen[key] = true
		effects.calls = append(effects.calls, key)
	}
	return effects.fail[key]
}
func (effects *recordingEffects) EnsureNoTurnChoice(_ context.Context, op string, c codexgeneration.NoTurnChoice, _, _ metadata.CodexEndpointRef) error {
	return effects.once(op + ":choice:" + c.AgentUID + ":" + string(c.Decision))
}
func (effects *recordingEffects) EnsureAdmissionFence(_ context.Context, op string, _ metadata.CodexEndpointRef) error {
	return effects.once(op + ":admission-fence")
}
func (effects *recordingEffects) EnsureBindingFence(_ context.Context, op string, _ metadata.CodexEndpointRef, _ []codexgeneration.HandoverTarget) error {
	return effects.once(op + ":binding-fence")
}
func (effects *recordingEffects) EnsureTargetAbsent(_ context.Context, op string, _ metadata.CodexEndpointRef, _ codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	return effects.once(op + ":absent:" + target.AgentUID)
}
func (effects *recordingEffects) EnsureOldStopped(_ context.Context, op string, _, _ codexupgrade.GenerationRoute, _ []codexgeneration.HandoverTarget) error {
	return effects.once(op + ":stop")
}
func (effects *recordingEffects) EnsureTargetResumed(_ context.Context, op string, _ metadata.CodexEndpointRef, _ codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	return effects.once(op + ":resume:" + target.AgentUID)
}
func (effects *recordingEffects) EnsureTargetSnapshot(_ context.Context, op string, _ metadata.CodexEndpointRef, _ codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	return effects.once(op + ":snapshot:" + target.AgentUID)
}
func (effects *recordingEffects) EnsureTargetCAS(_ context.Context, op string, _, _ metadata.CodexEndpointRef, target codexgeneration.HandoverTarget) error {
	return effects.once(op + ":cas:" + target.AgentUID)
}
func (effects *recordingEffects) EnsurePaneRelaunched(_ context.Context, op string, _ codexupgrade.GenerationRoute, target codexgeneration.HandoverTarget) error {
	return effects.once(op + ":relaunch:" + target.AgentUID)
}
func (effects *recordingEffects) EnsureRetired(_ context.Context, op string, _ metadata.CodexEndpointRef) error {
	return effects.once(op + ":retire")
}
func (effects *recordingEffects) EnsureLeaseReleased(_ context.Context, op string, _ codexupgrade.GenerationRoute) error {
	return effects.once(op + ":lease-release")
}
func (effects *recordingEffects) EnsureOldAuthorityRestored(_ context.Context, op string, _ metadata.CodexEndpointRef, _ []codexgeneration.HandoverTarget) error {
	return effects.once(op + ":restore")
}

func testCoordinator(t *testing.T, owner codexgeneration.OwnerClass, states ...codexgeneration.ObligationState) (*Coordinator, Request, *recordingEffects) {
	t.Helper()
	domain, oldID, newID := "domain-one", "generation-old", "generation-new"
	old := metadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: oldID}
	successor := metadata.CodexEndpointRef{StateDomainID: domain, EndpointGenerationID: newID}
	rolling, err := codexgeneration.NewRollingUpgradeOperation("upgrade-one", domain, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	rolling, _, _ = rolling.RecordCandidateLaunchIntent()
	rolling, _, _ = rolling.RecordCandidateStart()
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionPrepareCandidate, nil)
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionCommitAdmission, nil)
	var registry metadata.Registry
	var obligations []codexgeneration.AgentObligation
	for index, state := range states {
		agentUID, paneUID, threadID := fmt.Sprintf("agent-%d", index), fmt.Sprintf("pane-%d", index), fmt.Sprintf("thread-%d", index)
		interaction, phase, started := metadata.InteractionUnknown, metadata.PhaseRunning, true
		switch state {
		case codexgeneration.ObligationCompletedPersisted:
			interaction = metadata.InteractionResponseComplete
		case codexgeneration.ObligationActive:
			interaction = metadata.InteractionInProgress
		case codexgeneration.ObligationApprovalPending:
			interaction = metadata.InteractionApprovalRequired
		case codexgeneration.ObligationNoTurn:
			started = false
		case codexgeneration.ObligationUnknown:
			phase = metadata.PhaseFailed
		}
		registry.Agents = append(registry.Agents, metadata.Agent{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent, Metadata: metadata.ObjectMeta{UID: agentUID, Name: agentUID, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-one"}}, Spec: metadata.AgentSpec{Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: "/work"}}, Status: metadata.AgentStatus{Phase: phase, PaneRef: paneUID, Interaction: metadata.AgentInteraction{Kind: interaction}, SessionRef: &metadata.AgentSessionRef{Provider: "codex", ObservedAt: time.Unix(1, 0), Codex: &metadata.CodexSessionRef{ThreadID: threadID, HasStartedTurn: started, Endpoint: &old, Lifecycle: &metadata.CodexGenerationLifecycleRef{State: metadata.CodexGenerationHandoverPending, Operation: &metadata.CodexGenerationOperationRef{ID: "upgrade-one", Endpoint: old}}}}}})
		registry.Panes = append(registry.Panes, metadata.Pane{APIVersion: metadata.APIVersion, Kind: metadata.KindPane, Metadata: metadata.ObjectMeta{UID: paneUID, Name: paneUID, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindAgent, UID: agentUID}}, Spec: metadata.PaneSpec{Role: metadata.PaneRoleAgent, CWD: "/work"}, Status: metadata.PaneStatus{Activation: metadata.PaneActivation{Generation: "pane-generation-" + fmt.Sprint(index), RuntimeID: "%" + fmt.Sprint(index+10), AgentUID: agentUID, Codex: &metadata.CodexActivationBinding{ThreadID: threadID}}}})
		obligations = append(obligations, codexgeneration.AgentObligation{AgentUID: agentUID, EndpointGenerationID: oldID, State: state})
	}
	ledger, _ := codexgeneration.ProjectDrainLedger(oldID, obligations)
	rolling, _, _ = rolling.RecordAction(codexgeneration.RollingActionPublishDrain, ledger)
	rolling, _, _ = rolling.RequestGenerationHandover()
	qualification := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true, PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true, BundleDriftRefused: true, ProtocolMismatchRefused: true})
	route := func(endpoint metadata.CodexEndpointRef, state codexgeneration.GenerationState, owner codexgeneration.OwnerClass, suffix string) codexupgrade.GenerationRoute {
		if owner != codexgeneration.OwnerProjmuxPrivate {
			return codexupgrade.GenerationRoute{Generation: codexgeneration.Generation{Endpoint: endpoint, State: state, Owner: owner, BundleID: "bundle-" + suffix}}
		}
		return codexupgrade.GenerationRoute{Generation: codexgeneration.Generation{Endpoint: endpoint, State: state, Owner: owner, BundleID: "bundle-" + suffix}, Config: codexupgrade.GenerationConfig{Endpoint: endpoint, StateDomainPath: "/state/domain", PrivateRoot: "/run/" + suffix, SocketPath: "/run/" + suffix + "/codex-" + endpoint.EndpointGenerationID + ".sock", LeaseRoot: "/lease/" + suffix, RequiredProtocol: codexbundle.ProtocolRange{Min: 1, Max: 1}}, TUIPath: "/lease/" + suffix + "/bin/codex", LaunchOperationRef: "launch-" + suffix, Ready: true, Proof: &codexgenerationhost.LaunchProof{Endpoint: codexgenerationhost.EndpointIdentity{StateDomainID: domain, EndpointGenerationID: endpoint.EndpointGenerationID}, EndpointRuntimeID: "runtime-" + suffix, SocketPath: "/run/" + suffix + "/codex-" + endpoint.EndpointGenerationID + ".sock", BundleID: "bundle-" + suffix}}
	}
	journal := codexupgrade.Journal{Version: codexupgrade.JournalVersion, StateDomainID: domain, CurrentGenerationID: newID, Routes: []codexupgrade.GenerationRoute{route(old, codexgeneration.StateHandoverPending, owner, "old"), route(successor, codexgeneration.StateCurrent, codexgeneration.OwnerProjmuxPrivate, "new")}, Obligations: obligations, Qualification: &qualification, Operation: &rolling}
	store := codexupgrade.NewStateStore(t.TempDir())
	if _, err := store.Update(context.Background(), func(current *codexupgrade.Journal, _ bool) error { *current = journal; return nil }); err != nil {
		t.Fatal(err)
	}
	effects := &recordingEffects{}
	coordinator := &Coordinator{Journal: store, Registry: staticRegistry{registry}, Effects: effects,
		Observe:    func(context.Context, codexupgrade.GenerationRoute) error { return nil },
		CanRecover: func(codexupgrade.GenerationRoute) error { return nil }}
	return coordinator, Request{OperationRef: "handover-one", RollingOperationRef: "upgrade-one"}, effects
}

func TestCoordinatorGenerationWideOrderingAndEveryFailpointResumeConvergesWithoutDuplicateEffects(t *testing.T) {
	want := []string{"handover-one:admission-fence", "handover-one:binding-fence", "handover-one:absent:agent-0", "handover-one:absent:agent-1", "handover-one:stop", "handover-one:resume:agent-0", "handover-one:resume:agent-1", "handover-one:snapshot:agent-0", "handover-one:snapshot:agent-1", "handover-one:cas:agent-0", "handover-one:relaunch:agent-0", "handover-one:cas:agent-1", "handover-one:relaunch:agent-1", "handover-one:retire", "handover-one:lease-release"}
	for _, failpoint := range []string{FailAfterPrewrite, FailAfterIntent, FailAfterEffect, FailAfterReceipt} {
		t.Run(failpoint, func(t *testing.T) {
			coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted, codexgeneration.ObligationCompletedPersisted)
			fired := false
			coordinator.Failpoint = func(point string) error {
				if point == failpoint && !fired {
					fired = true
					return errors.New("crash")
				}
				return nil
			}
			_, _ = coordinator.Apply(context.Background(), request)
			coordinator.Failpoint = nil
			journal, err := coordinator.Resume(context.Background(), request.OperationRef)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
				t.Fatalf("handover=%+v", journal.Handover)
			}
			if !reflect.DeepEqual(effects.calls, want) {
				t.Fatalf("calls=%q want=%q", effects.calls, want)
			}
		})
	}
}

func TestCoordinatorRecoversExactSameGenerationBeforeHandoverAndCrashRetryIsIdempotent(t *testing.T) {
	for _, failAfterEffect := range []bool{false, true} {
		t.Run(fmt.Sprintf("crash-after-effect=%t", failAfterEffect), func(t *testing.T) {
			coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
			recovered := false
			effects.recover = func(route codexupgrade.GenerationRoute) (codexgenerationhost.LaunchProof, error) {
				recovered = true
				return *route.Proof, nil
			}
			coordinator.Observe = func(_ context.Context, route codexupgrade.GenerationRoute) error {
				if route.Generation.Endpoint.EndpointGenerationID == "generation-old" && !recovered {
					return errors.New("old process disappeared")
				}
				return nil
			}
			if plan := coordinator.Plan(context.Background(), request); plan.Decision != DecisionRecoverSameGeneration {
				t.Fatalf("plan=%+v", plan)
			}
			fired := false
			if failAfterEffect {
				coordinator.Failpoint = func(point string) error {
					if point == FailAfterEffect && !fired {
						fired = true
						return errors.New("crash after recovery effect")
					}
					return nil
				}
			}
			_, firstErr := coordinator.Apply(context.Background(), request)
			if failAfterEffect && firstErr == nil {
				t.Fatal("recovery failpoint did not fire")
			}
			coordinator.Failpoint = nil
			journal, err := coordinator.Apply(context.Background(), request)
			if err != nil || journal.ColdRecovery == nil || !journal.ColdRecovery.Recovered || journal.ColdRecovery.Mutations != 1 ||
				journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
				t.Fatalf("journal=%+v err=%v", journal, err)
			}
			if len(effects.calls) == 0 || effects.calls[0] != "handover-one:recover:generation-old" {
				t.Fatalf("recovery was not first: %q", effects.calls)
			}
			wantInvocations := 1
			if failAfterEffect {
				wantInvocations = 2
			}
			if effects.recoveryInvocations != wantInvocations || countCall(effects.calls, "handover-one:recover:generation-old") != 1 {
				t.Fatalf("recovery invocations=%d semantic calls=%q", effects.recoveryInvocations, effects.calls)
			}
		})
	}
}

func TestCoordinatorUsesQualifiedFallbackWhenExactSameGenerationRestartIsUnavailable(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	coordinator.Observe = func(_ context.Context, route codexupgrade.GenerationRoute) error {
		if route.Generation.Endpoint.EndpointGenerationID == "generation-old" {
			return errors.New("old process absent")
		}
		return nil
	}
	coordinator.CanRecover = func(codexupgrade.GenerationRoute) error { return errors.New("exact bundle unavailable") }
	plan := coordinator.Plan(context.Background(), request)
	if plan.Decision != DecisionReady || len(plan.Blockers) != 0 {
		t.Fatalf("qualified fallback plan=%+v", plan)
	}
	journal, err := coordinator.Apply(context.Background(), request)
	if err != nil || journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
		t.Fatalf("journal=%+v err=%v", journal.Handover, err)
	}
	if countCall(effects.calls, "handover-one:recover:generation-old") != 0 || countCall(effects.calls, "handover-one:stop") != 1 {
		t.Fatalf("fallback effects=%q", effects.calls)
	}
}

func TestSuccessorPreloadedTargetStopsBeforeOldStop(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	effects.fail = map[string]error{"handover-one:absent:agent-0": errors.New("successor already loaded")}
	if _, err := coordinator.Apply(context.Background(), request); err == nil {
		t.Fatal("preloaded successor target was accepted")
	}
	if countCall(effects.calls, "handover-one:stop") != 0 || countCall(effects.calls, "handover-one:resume:agent-0") != 0 {
		t.Fatalf("destructive effects crossed absence barrier: %q", effects.calls)
	}
}

func TestPlanBlockersKeepEveryDestructiveEffectAtZero(t *testing.T) {
	for _, state := range []codexgeneration.ObligationState{codexgeneration.ObligationActive, codexgeneration.ObligationApprovalPending, codexgeneration.ObligationUnknown, codexgeneration.ObligationNoTurn} {
		t.Run(string(state), func(t *testing.T) {
			coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, state)
			plan := coordinator.Plan(context.Background(), request)
			if plan.Decision != DecisionBlocked {
				t.Fatalf("plan=%+v", plan)
			}
			if len(effects.calls) != 0 {
				t.Fatalf("effects=%q", effects.calls)
			}
			if _, err := coordinator.Apply(context.Background(), request); err == nil {
				t.Fatal("blocked apply succeeded")
			}
			if len(effects.calls) != 0 {
				t.Fatalf("blocked effects=%q", effects.calls)
			}
		})
	}
}

func TestExternalOwnersRemainAwaitingOwnerStopWithLifecycleArgvZero(t *testing.T) {
	for _, owner := range []codexgeneration.OwnerClass{codexgeneration.OwnerOfficialManaged, codexgeneration.OwnerUnmanaged, codexgeneration.OwnerUnknown} {
		t.Run(string(owner), func(t *testing.T) {
			coordinator, request, effects := testCoordinator(t, owner, codexgeneration.ObligationCompletedPersisted)
			plan := coordinator.Plan(context.Background(), request)
			if plan.Decision != DecisionAwaitingOwnerStop {
				t.Fatalf("plan=%+v", plan)
			}
			journal, err := coordinator.Apply(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Handover == nil {
				t.Fatalf("journal=%+v", journal.Handover)
			}
			if action, _ := journal.Handover.NextAction(); action != codexgeneration.HandoverActionAwaitOwnerStop {
				t.Fatalf("action=%s journal=%+v", action, journal.Handover)
			}
			wantPrefix := []string{"handover-one:admission-fence", "handover-one:binding-fence", "handover-one:absent:agent-0"}
			if !reflect.DeepEqual(effects.calls, wantPrefix) {
				t.Fatalf("pre-user-stop effects=%q want=%q", effects.calls, wantPrefix)
			}
			for _, call := range effects.calls {
				if strings.HasSuffix(call, ":stop") {
					t.Fatalf("foreign stop call=%q", effects.calls)
				}
			}
			request.OwnerStopReceipt = &codexgeneration.OwnerStopReceipt{ReceiptID: "user-stop-one",
				Endpoint: metadata.CodexEndpointRef{StateDomainID: "domain-one", EndpointGenerationID: "generation-old"}}
			journal, err = coordinator.Apply(context.Background(), request)
			if err != nil || journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
				t.Fatalf("continue after owner receipt journal=%+v err=%v", journal.Handover, err)
			}
			if countCall(effects.calls, "handover-one:stop") != 0 || countCall(effects.calls, "handover-one:resume:agent-0") != 1 {
				t.Fatalf("foreign lifecycle/resume calls=%q", effects.calls)
			}
		})
	}
}

func TestPlanIgnoresSameGenerationIDFromAnotherStateDomain(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate)
	fixture := coordinator.Registry.(staticRegistry).registry
	other := metadata.CodexEndpointRef{StateDomainID: "other-domain", EndpointGenerationID: "generation-old"}
	fixture.Agents = append(fixture.Agents, metadata.Agent{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent,
		Metadata: metadata.ObjectMeta{UID: "foreign-domain-agent", Name: "foreign-domain-agent"}, Spec: metadata.AgentSpec{Provider: "codex"},
		Status: metadata.AgentStatus{Phase: metadata.PhaseRunning, Interaction: metadata.AgentInteraction{Kind: metadata.InteractionInProgress},
			SessionRef: &metadata.AgentSessionRef{Provider: "codex", Codex: &metadata.CodexSessionRef{ThreadID: "thread-foreign", HasStartedTurn: true, Endpoint: &other}}}})
	coordinator.Registry = staticRegistry{fixture}
	plan := coordinator.Plan(context.Background(), request)
	if plan.Decision != DecisionReady || len(plan.Targets) != 0 || len(plan.Blockers) != 0 || len(effects.calls) != 0 {
		t.Fatalf("cross-domain collision affected plan: %+v effects=%q", plan, effects.calls)
	}
}

func TestIncompleteManagedOldRouteBlocksBeforeEveryEffect(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	_, err := coordinator.Journal.Update(context.Background(), func(journal *codexupgrade.Journal, _ bool) error {
		for i := range journal.Routes {
			if journal.Routes[i].Generation.Endpoint.EndpointGenerationID == "generation-old" {
				journal.Routes[i].Ready = false
				journal.Routes[i].Proof = nil
				journal.Routes[i].LaunchOperationRef = ""
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plan(context.Background(), request)
	if plan.Decision != DecisionBlocked || !slices.Contains(plan.Blockers, "exact-old-managed-lifecycle-authority-incomplete") {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := coordinator.Apply(context.Background(), request); err == nil || len(effects.calls) != 0 {
		t.Fatalf("apply err=%v effects=%q", err, effects.calls)
	}
}

func TestRepeatedApplyUsesPinnedJournalAfterChoiceAndCASProgress(t *testing.T) {
	for _, state := range []codexgeneration.ObligationState{codexgeneration.ObligationNoTurn, codexgeneration.ObligationCompletedPersisted} {
		t.Run(string(state), func(t *testing.T) {
			coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, state)
			if state == codexgeneration.ObligationNoTurn {
				request.Choices = []codexgeneration.NoTurnChoice{{AgentUID: "agent-0", Decision: codexgeneration.NoTurnClose}}
			}
			paused := false
			coordinator.Failpoint = func(point string) error {
				if point != FailAfterReceipt || paused {
					return nil
				}
				journal, _, _ := coordinator.Journal.Load()
				if journal.Handover != nil && (journal.Handover.Mutations.NoTurnChoice == 1 || journal.Handover.Mutations.EndpointRefCAS == 1) {
					paused = true
					return errors.New("pause after irreversible progress")
				}
				return nil
			}
			_, _ = coordinator.Apply(context.Background(), request)
			coordinator.Failpoint = nil
			journal, err := coordinator.Apply(context.Background(), request)
			if err != nil || journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
				t.Fatalf("repeat Apply journal=%+v err=%v calls=%q", journal.Handover, err, effects.calls)
			}
		})
	}
}

func TestAbortFenceSerializesAgainstStopIntent(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	stopIntent := false
	coordinator.Failpoint = func(point string) error {
		if point != FailAfterIntent || stopIntent {
			return nil
		}
		journal, _, _ := coordinator.Journal.Load()
		if journal.Handover != nil && journal.Handover.StopIntended {
			stopIntent = true
			return errors.New("pause after stop intent")
		}
		return nil
	}
	_, _ = coordinator.Apply(context.Background(), request)
	if !stopIntent {
		t.Fatal("stop intent was not reached")
	}
	if _, err := coordinator.Abort(context.Background(), request.OperationRef); err == nil {
		t.Fatal("abort crossed durable stop intent")
	}
	coordinator.Failpoint = nil
	journal, err := coordinator.Resume(context.Background(), request.OperationRef)
	if err != nil || journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
		t.Fatalf("forward resume=%+v err=%v", journal.Handover, err)
	}
	if count := countCall(effects.calls, "handover-one:stop"); count != 1 {
		t.Fatalf("stop effects=%q count=%d", effects.calls, count)
	}
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func TestPreStopAbortRestoresAuthorityAndPostStopAbortRefuses(t *testing.T) {
	coordinator, request, effects := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterReceipt {
			return errors.New("pause")
		}
		return nil
	}
	_, _ = coordinator.Apply(context.Background(), request)
	coordinator.Failpoint = nil
	journal, err := coordinator.Abort(context.Background(), request.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Handover == nil || !journal.Handover.Aborted || effects.calls[len(effects.calls)-1] != "handover-one:restore" {
		t.Fatalf("abort=%+v calls=%q", journal.Handover, effects.calls)
	}
	coordinator, request, _ = testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationCompletedPersisted)
	stopSeen := false
	coordinator.Failpoint = func(point string) error {
		if point == FailAfterReceipt {
			loaded, _, _ := coordinator.Journal.Load()
			if loaded.Handover != nil && loaded.Handover.OldStopped && !stopSeen {
				stopSeen = true
				return errors.New("pause")
			}
		}
		return nil
	}
	_, _ = coordinator.Apply(context.Background(), request)
	if _, err := coordinator.Abort(context.Background(), request.OperationRef); err == nil {
		t.Fatal("post-stop abort succeeded")
	}
}

func TestAppliedNoTurnChoiceIsForwardOnlyAndCannotClaimAuthorityRestored(t *testing.T) {
	coordinator, request, _ := testCoordinator(t, codexgeneration.OwnerProjmuxPrivate, codexgeneration.ObligationNoTurn)
	request.Choices = []codexgeneration.NoTurnChoice{{AgentUID: "agent-0", Decision: codexgeneration.NoTurnClose}}
	paused := false
	coordinator.Failpoint = func(point string) error {
		if point != FailAfterReceipt || paused {
			return nil
		}
		journal, _, _ := coordinator.Journal.Load()
		if journal.Handover != nil && journal.Handover.Mutations.NoTurnChoice == 1 {
			paused = true
			return errors.New("pause after irreversible no-turn choice")
		}
		return nil
	}
	_, _ = coordinator.Apply(context.Background(), request)
	if !paused {
		t.Fatal("no-turn choice was not applied")
	}
	if _, err := coordinator.Abort(context.Background(), request.OperationRef); err == nil {
		t.Fatal("abort accepted an irreversibly applied no-turn choice")
	}
}
