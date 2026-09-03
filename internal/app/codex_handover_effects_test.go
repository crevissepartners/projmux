package app

import (
	"context"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type handoverFakeRegistry struct {
	registry metadata.Registry
	updates  int
}

func (store *handoverFakeRegistry) LoadSnapshot() (metadata.Registry, error) {
	return store.registry.Clone(), nil
}

func (store *handoverFakeRegistry) UpdateConvergent(fn func(*metadata.Registry) error) (metadata.Registry, bool, error) {
	working := store.registry.Clone()
	if err := fn(&working); err != nil {
		return metadata.Registry{}, false, err
	}
	store.registry = working
	store.updates++
	return working.Clone(), true, nil
}

type handoverFakeClient struct {
	snapshot    codexappserver.LifecycleSnapshot
	resumeCalls int
}

func (client *handoverFakeClient) ResumeThread(_ context.Context, threadID, _ string, _ []string) (codexappserver.ThreadBinding, error) {
	client.resumeCalls++
	return codexappserver.ThreadBinding{ThreadID: threadID}, nil
}
func (client *handoverFakeClient) ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error) {
	return client.snapshot, nil
}
func (*handoverFakeClient) Close() error { return nil }

type handoverRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (run handoverRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, name, args...)
}

func handoverEffectFixture(endpoint metadata.CodexEndpointRef) (*handoverFakeRegistry, codexgeneration.HandoverTarget) {
	target := codexgeneration.HandoverTarget{AgentUID: "agent-one", PaneUID: "pane-one", PaneRuntimeID: "%21",
		PaneGeneration: "pane-generation-one", RelaunchGeneration: "handover-generation-one", ThreadID: "thread-one",
		SuccessorAbsentObserved: true, ResumeIntended: true}
	registry := metadata.NewRegistry()
	registry.Agents = []metadata.Agent{{APIVersion: metadata.APIVersion, Kind: metadata.KindAgent,
		Metadata: metadata.ObjectMeta{UID: target.AgentUID, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-one"}},
		Spec:     metadata.AgentSpec{Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: "/work"}},
		Status: metadata.AgentStatus{Phase: metadata.PhaseRunning, PaneRef: target.PaneUID,
			SessionRef: &metadata.AgentSessionRef{Provider: "codex", Codex: &metadata.CodexSessionRef{
				ThreadID: target.ThreadID, HasStartedTurn: true, Endpoint: &endpoint}}}}}
	registry.Panes = []metadata.Pane{{APIVersion: metadata.APIVersion, Kind: metadata.KindPane,
		Metadata: metadata.ObjectMeta{UID: target.PaneUID, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindAgent, UID: target.AgentUID}},
		Status: metadata.PaneStatus{Activation: metadata.PaneActivation{RuntimeID: target.PaneRuntimeID,
			Generation: target.PaneGeneration, AgentUID: target.AgentUID, Codex: &metadata.CodexActivationBinding{ThreadID: target.ThreadID}}}}}
	return &handoverFakeRegistry{registry: registry}, target
}

func exactHandoverPaneRunner(target codexgeneration.HandoverTarget) tmuxCommandRunner {
	return handoverRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || len(args) == 0 || args[0] != "display-message" {
			return nil, errors.New("unexpected tmux call")
		}
		return []byte(target.PaneUID + "\x1f" + target.AgentUID + "\x1fAgent\x1f" + target.AgentUID + "\n"), nil
	})
}

func TestHandoverResumeRefusesSameDomainWrongOldGenerationBeforeProviderWire(t *testing.T) {
	old := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	drifted := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "third"}
	registry, target := handoverEffectFixture(drifted)
	opened := 0
	effects := &codexHandoverEffects{registry: registry, runner: exactHandoverPaneRunner(target), open: func(context.Context, codexNativeEndpointRoute, bool) (codexHandoverClient, error) {
		opened++
		return &handoverFakeClient{}, nil
	}}
	err := effects.EnsureTargetResumed(context.Background(), "handover-one", old, codexupgrade.GenerationRoute{}, target)
	if err == nil || opened != 0 {
		t.Fatalf("err=%v provider opens=%d", err, opened)
	}
}

func TestHandoverPostFenceTupleDriftBlocksAbsenceSnapshotAndNoTurnBeforeAnyWire(t *testing.T) {
	old := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	registry, target := handoverEffectFixture(old)
	registry.registry.Panes[0].Status.Activation.Generation = "drifted-generation"
	opened := 0
	effects := &codexHandoverEffects{registry: registry, runner: exactHandoverPaneRunner(target), open: func(context.Context, codexNativeEndpointRoute, bool) (codexHandoverClient, error) {
		opened++
		return &handoverFakeClient{}, nil
	}}
	if err := effects.EnsureTargetAbsent(context.Background(), "handover-one", old, codexupgrade.GenerationRoute{}, target); err == nil {
		t.Fatal("absence accepted drifted tuple")
	}
	if err := effects.EnsureTargetSnapshot(context.Background(), "handover-one", old, codexupgrade.GenerationRoute{}, target); err == nil {
		t.Fatal("snapshot accepted drifted tuple")
	}
	oldRoute := codexupgrade.GenerationRoute{Generation: codexgeneration.Generation{Endpoint: old, Owner: codexgeneration.OwnerProjmuxPrivate},
		LaunchOperationRef: "launch-old", Ready: true, Proof: &codexgenerationhost.LaunchProof{}}
	if err := effects.EnsureOldStopped(context.Background(), "handover-one", oldRoute, codexupgrade.GenerationRoute{}, []codexgeneration.HandoverTarget{target}); err == nil {
		t.Fatal("old stop accepted post-fence tuple drift")
	}
	choice := codexgeneration.NoTurnChoice{AgentUID: target.AgentUID, Decision: codexgeneration.NoTurnClose,
		PaneUID: target.PaneUID, PaneRuntimeID: target.PaneRuntimeID, PaneGeneration: target.PaneGeneration}
	registry.registry.Agents[0].Status.SessionRef.Codex.HasStartedTurn = false
	if err := effects.EnsureNoTurnChoice(context.Background(), "handover-one", choice, old, metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "new"}); err == nil {
		t.Fatal("no-turn close accepted rebound Pane")
	}
	if opened != 0 || registry.updates != 0 {
		t.Fatalf("provider opens=%d Registry updates=%d", opened, registry.updates)
	}
}

func TestHandoverSuccessorAbsenceRejectsPreloadedThreadBeforeStop(t *testing.T) {
	old := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	registry, target := handoverEffectFixture(old)
	client := &handoverFakeClient{snapshot: codexappserver.LifecycleSnapshot{ThreadID: target.ThreadID, ThreadState: codexappserver.ThreadStateIdle}}
	effects := &codexHandoverEffects{registry: registry, runner: exactHandoverPaneRunner(target), open: func(context.Context, codexNativeEndpointRoute, bool) (codexHandoverClient, error) {
		return client, nil
	}}
	if err := effects.EnsureTargetAbsent(context.Background(), "handover-one", old, codexupgrade.GenerationRoute{}, target); err == nil {
		t.Fatal("preloaded successor thread passed absence barrier")
	}
	if client.resumeCalls != 0 {
		t.Fatalf("resume calls=%d", client.resumeCalls)
	}
}

func TestHandoverResumeReceiptSurvivesSuccessorRestartWithoutSecondResumeWire(t *testing.T) {
	old := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "old"}
	successor := metadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "new"}
	registry, target := handoverEffectFixture(old)
	client := &handoverFakeClient{snapshot: codexappserver.LifecycleSnapshot{ThreadID: target.ThreadID, ThreadState: codexappserver.ThreadStateNotLoaded}}
	opened := 0
	effects := &codexHandoverEffects{registry: registry, mutator: metadata.Mutator{}, runner: exactHandoverPaneRunner(target),
		open: func(context.Context, codexNativeEndpointRoute, bool) (codexHandoverClient, error) {
			opened++
			return client, nil
		}}
	route := codexupgrade.GenerationRoute{Generation: codexgeneration.Generation{Endpoint: successor}}
	if err := effects.EnsureTargetResumed(context.Background(), "handover-one", old, route, target); err != nil {
		t.Fatal(err)
	}
	// A restarted successor projects the persisted thread as not-loaded. The
	// exact Registry effect receipt must suppress a second thread/resume wire.
	client.snapshot = codexappserver.LifecycleSnapshot{ThreadID: target.ThreadID, ThreadState: codexappserver.ThreadStateNotLoaded,
		TurnID: "turn-one", TurnState: codexappserver.TurnStateCompleted}
	if err := effects.EnsureTargetResumed(context.Background(), "handover-one", old, route, target); err != nil {
		t.Fatal(err)
	}
	if client.resumeCalls != 1 || opened != 1 {
		t.Fatalf("resume calls=%d endpoint opens=%d", client.resumeCalls, opened)
	}
	if err := effects.EnsureTargetSnapshot(context.Background(), "handover-one", old, route, target); err != nil {
		t.Fatalf("persisted post-restart snapshot: %v", err)
	}
}
