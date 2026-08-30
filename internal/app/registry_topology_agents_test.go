package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// fakeTopologyAgentLauncher is the provider-launch seam under test. It records
// nothing tmux does and builds a deterministic argv per provider, so a test can
// assert *which* launch a replayed Agent got without depending on an installed
// provider binary.
type fakeTopologyAgentLauncher struct {
	disabled  map[string]bool
	resumeErr map[string]error
	launchErr map[string]error
	binds     []string
}

func newFakeTopologyAgentLauncher() *fakeTopologyAgentLauncher {
	return &fakeTopologyAgentLauncher{
		disabled:  map[string]bool{},
		resumeErr: map[string]error{},
		launchErr: map[string]error{},
	}
}

func (f *fakeTopologyAgentLauncher) RequireAgentEnabled(provider string) error {
	if f.disabled[provider] {
		return fmt.Errorf("the %s agent is disabled in Settings", provider)
	}
	return nil
}

func (f *fakeTopologyAgentLauncher) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (string, []string, error) {
	if err := f.launchErr[provider]; err != nil {
		return "", nil, err
	}
	if len(payload) != 0 {
		return "", nil, fmt.Errorf("topology replay must never carry a payload, got %v", payload)
	}
	return provider, []string{"/opt/" + provider, "--cwd", workspace.CWD}, nil
}

func (f *fakeTopologyAgentLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	if err := f.resumeErr[provider]; err != nil {
		return "", nil, err
	}
	return provider, []string{"/opt/" + provider, "--cwd", workspace.CWD, "--resume", conversationID}, nil
}

func (f *fakeTopologyAgentLauncher) BindManagedAgentPane(paneID, provider, contextDir, title string) {
	f.binds = append(f.binds, fmt.Sprintf("managed %s %s %s", paneID, provider, contextDir))
}

func (f *fakeTopologyAgentLauncher) BindResumedAgentPane(paneID, provider, contextDir, title, conversationID string) {
	f.binds = append(f.binds, fmt.Sprintf("resumed %s %s %s %s", paneID, provider, contextDir, conversationID))
}

func (f *fakeTopologyAgentLauncher) BindAgentPaneOnRoute(_ context.Context, _ tmuxCommandRunner, binding agentPaneBinding) error {
	if binding.ConversationID != "" {
		f.BindResumedAgentPane(binding.PaneID, binding.Provider, binding.ContextDir, binding.Title, binding.ConversationID)
	} else {
		f.BindManagedAgentPane(binding.PaneID, binding.Provider, binding.ContextDir, binding.Title)
	}
	return nil
}

var _ topologyAgentLauncher = (*fakeTopologyAgentLauncher)(nil)

// topologyFixtureAgent declares one stored Agent for the shared beta Project.
type topologyFixtureAgent struct {
	name     string
	provider string
	cwd      string
	phase    coremetadata.AgentPhase
	ref      *coremetadata.AgentSessionRef
	topic    string
}

func addTopologyFixtureAgent(t *testing.T, store *fakeResourceStore, declared topologyFixtureAgent) coremetadata.Agent {
	t.Helper()
	created, err := store.mutator().CreateAgent(&store.registry, "win-beta-main", coremetadata.CreateAgentOptions{
		Name:        declared.name,
		Provider:    declared.provider,
		Workspace:   coremetadata.AgentWorkspace{CWD: declared.cwd},
		OperationID: "op-topology-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := store.registry.Agent(created.Metadata.UID)
	if !ok {
		t.Fatalf("stored Agent %q disappeared", created.Metadata.UID)
	}
	phase := declared.phase
	if phase == "" {
		phase = coremetadata.PhaseOffline
	}
	stored.Status.Phase = phase
	stored.Status.SessionRef = declared.ref
	if strings.TrimSpace(declared.topic) != "" {
		if stored.Metadata.Annotations == nil {
			stored.Metadata.Annotations = map[string]string{}
		}
		stored.Metadata.Annotations[coremetadata.AnnotationAgentTopic] = declared.topic
	}
	return *stored
}

func claudeConversationRef(id string) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{
		Provider:   "claude",
		ObservedAt: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		Claude:     &coremetadata.ClaudeSessionRef{SessionID: id},
	}
}

func codexConversationRef(id string) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		Codex:      &coremetadata.CodexSessionRef{ThreadID: id},
	}
}

func markTopologyAgentInterrupted(t *testing.T, store *fakeResourceStore, agentUID, paneUID string) coremetadata.Pane {
	t.Helper()
	mutator := store.mutator()
	if paneUID == "" {
		pane, err := mutator.AttachAgentPane(&store.registry, agentUID, coremetadata.BootstrapPane{
			Name: "retained", CWD: store.registry.Projects[1].Spec.Root,
		}, "op-topology-interrupted-pane")
		if err != nil {
			t.Fatal(err)
		}
		paneUID = pane.Metadata.UID
	}
	if _, err := mutator.RecordPaneActivation(&store.registry, paneUID, coremetadata.PaneActivationOptions{
		Generation: "generation-" + agentUID, AgentUID: agentUID, OperationID: "op-topology-launch",
	}); err != nil {
		t.Fatal(err)
	}
	receipt := coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceControlAction, Classification: coremetadata.TerminationInterrupted,
		ObservedAt: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), PaneUID: paneUID, AgentUID: agentUID,
		Generation: "generation-" + agentUID, OperationID: "op-topology-stop",
	}
	if outcome, err := mutator.RecordTermination(&store.registry, receipt); err != nil || !outcome.Applied {
		t.Fatalf("record interrupted evidence: %+v, %v", outcome, err)
	}
	if projection, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID: paneUID, Generation: receipt.Generation, ObservedAt: receipt.ObservedAt,
	}); err != nil || projection.AgentUID != agentUID || projection.Phase != coremetadata.PhaseOffline {
		t.Fatalf("project interrupted evidence: %+v, %v", projection, err)
	}
	pane, ok := store.registry.Pane(paneUID)
	if !ok {
		t.Fatalf("interrupted projection removed retained Pane %s", paneUID)
	}
	return pane.Clone()
}

// TestTopologyAgentContinueEligibilityMatrix is the exact
// source x classification x phase x sessionRef contract. SessionRef chooses
// fresh versus exact resume only after the termination gate; it never grants
// launch authority itself.
func TestTopologyAgentContinueEligibilityMatrix(t *testing.T) {
	_, store, _, _, root, _ := newTopologyMaterializeFixture(t)
	seed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "matrix", provider: "codex", cwd: root, ref: codexConversationRef("thread-matrix"),
	})
	markTopologyAgentInterrupted(t, store, seed.Metadata.UID, "")

	type mutation func(*coremetadata.Registry, *coremetadata.Agent, *coremetadata.Pane)
	setEvidence := func(source coremetadata.TerminationSource, classification coremetadata.TerminationClassification) mutation {
		return func(_ *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			agent.Status.LastTermination.Source = source
			agent.Status.LastTermination.Classification = classification
			pane.Status.LastTermination.Source = source
			pane.Status.LastTermination.Classification = classification
		}
	}
	for _, test := range []struct {
		name   string
		mutate mutation
		want   bool
		reason string
	}{
		{name: "control interrupted Offline with sessionRef", want: true},
		{name: "control interrupted Offline without sessionRef", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, _ *coremetadata.Pane) {
			agent.Status.SessionRef = nil
		}, want: true},
		{name: "supervisor normal Offline", mutate: setEvidence(coremetadata.TerminationSourceSupervisor, coremetadata.TerminationNormal), reason: "supervisor/normal"},
		{name: "supervisor abnormal Failed", mutate: func(reg *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			setEvidence(coremetadata.TerminationSourceSupervisor, coremetadata.TerminationAbnormal)(reg, agent, pane)
			agent.Status.Phase = coremetadata.PhaseFailed
		}, reason: "supervisor/abnormal"},
		{name: "supervisor killed Offline", mutate: setEvidence(coremetadata.TerminationSourceSupervisor, coremetadata.TerminationKilled), reason: "supervisor/killed"},
		{name: "delete intentional Offline", mutate: setEvidence(coremetadata.TerminationSourceControlAction, coremetadata.TerminationIntentional), reason: "control-action/intentional"},
		{name: "reconcile unknown Offline", mutate: setEvidence(coremetadata.TerminationSourceReconcile, coremetadata.TerminationUnknown), reason: "reconcile/unknown"},
		{name: "nil evidence", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			agent.Status.LastTermination, pane.Status.LastTermination = nil, nil
		}, reason: "no termination evidence"},
		{name: "stale generation", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			agent.Status.LastTermination.Generation = "generation-stale"
			pane.Status.LastTermination.Generation = "generation-stale"
		}, reason: "current Agent activation generation"},
		{name: "illegal source classification pairing", mutate: setEvidence(coremetadata.TerminationSourceControlAction, coremetadata.TerminationNormal), reason: "control-action/normal"},
		{name: "interrupted Pending", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, _ *coremetadata.Pane) {
			agent.Status.Phase = coremetadata.PhasePending
		}, reason: "phase Pending"},
		{name: "interrupted Running before absence projection", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			agent.Status.Phase = coremetadata.PhaseRunning
			agent.Status.PaneRef = pane.Metadata.UID
		}, want: true},
		{name: "interrupted Failed", mutate: func(_ *coremetadata.Registry, agent *coremetadata.Agent, _ *coremetadata.Pane) {
			agent.Status.Phase = coremetadata.PhaseFailed
		}, reason: "phase Failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := store.registry.Clone()
			agent, _ := registry.Agent(seed.Metadata.UID)
			pane, _ := registry.Pane(agent.Status.LastTermination.PaneUID)
			if test.mutate != nil {
				test.mutate(&registry, agent, pane)
			}
			got, reason := decideTopologyAgentContinueEligibility(registry, agent.Clone())
			if got != test.want {
				t.Fatalf("eligible=%t reason=%q, want %t", got, reason, test.want)
			}
			if test.reason != "" && !strings.Contains(reason, test.reason) {
				t.Fatalf("reason=%q, want substring %q", reason, test.reason)
			}
		})
	}
}

// TestTopologyAgentResumeDecisionTable pins the three resume-decision branches
// the Phase owes: a stored ref resumes, a missing or unusable ref starts a new
// conversation with a stated reason, and the provider discriminator is the
// stored ref's own -- never a conversation store, never a snapshot recipe.
func TestTopologyAgentResumeDecisionTable(t *testing.T) {
	for _, test := range []struct {
		name             string
		agent            coremetadata.Agent
		wantProvider     string
		wantConversation string
		wantReason       string
	}{
		{
			name: "claude ref resumes its own conversation",
			agent: coremetadata.Agent{
				Spec:   coremetadata.AgentSpec{Provider: "claude"},
				Status: coremetadata.AgentStatus{SessionRef: claudeConversationRef("conv-claude")},
			},
			wantProvider:     "claude",
			wantConversation: "conv-claude",
		},
		{
			name: "codex ref resumes its own thread",
			agent: coremetadata.Agent{
				Spec:   coremetadata.AgentSpec{Provider: "codex"},
				Status: coremetadata.AgentStatus{SessionRef: codexConversationRef("thread-codex")},
			},
			wantProvider:     "codex",
			wantConversation: "thread-codex",
		},
		{
			name: "a ref with no declared provider still resumes",
			agent: coremetadata.Agent{
				Status: coremetadata.AgentStatus{SessionRef: claudeConversationRef("conv-loose")},
			},
			wantProvider:     "claude",
			wantConversation: "conv-loose",
		},
		{
			name: "no session ref starts a new conversation",
			agent: coremetadata.Agent{
				Spec:   coremetadata.AgentSpec{Provider: "codex"},
				Status: coremetadata.AgentStatus{},
			},
			wantProvider: "codex",
			wantReason:   "no provider session ref is recorded",
		},
		{
			name: "a ref with no conversation id starts a new conversation",
			agent: coremetadata.Agent{
				Spec: coremetadata.AgentSpec{Provider: "claude"},
				Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
					Provider: "claude", Claude: &coremetadata.ClaudeSessionRef{},
				}},
			},
			wantProvider: "claude",
			wantReason:   "carries no conversation id",
		},
		{
			name: "a ref with no provider discriminator starts a new conversation",
			agent: coremetadata.Agent{
				Spec: coremetadata.AgentSpec{Provider: "claude"},
				Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
					Claude: &coremetadata.ClaudeSessionRef{SessionID: "conv-orphan"},
				}},
			},
			wantProvider: "claude",
			wantReason:   "no provider discriminator",
		},
		{
			name: "a cross-provider ref is never resumed onto the declared provider",
			agent: coremetadata.Agent{
				Spec:   coremetadata.AgentSpec{Provider: "codex"},
				Status: coremetadata.AgentStatus{SessionRef: claudeConversationRef("conv-claude")},
			},
			wantProvider: "codex",
			wantReason:   "is a codex Agent but its session ref is a claude conversation",
		},
		{
			name:         "an Agent with no provider anywhere has no launch discriminator",
			agent:        coremetadata.Agent{},
			wantProvider: "",
			wantReason:   "no provider session ref is recorded",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := decideTopologyAgentResume(test.agent)
			if got.provider != test.wantProvider {
				t.Fatalf("provider = %q, want %q", got.provider, test.wantProvider)
			}
			if got.conversationID != test.wantConversation {
				t.Fatalf("conversation = %q, want %q", got.conversationID, test.wantConversation)
			}
			if test.wantReason == "" {
				if got.reason != "" {
					t.Fatalf("resumed decision carried a reason: %q", got.reason)
				}
				return
			}
			if !strings.Contains(got.reason, test.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", got.reason, test.wantReason)
			}
			if got.conversationID != "" {
				t.Fatalf("a non-resuming decision named a conversation: %q", got.conversationID)
			}
		})
	}
}

// TestRegistryTopologyMaterializationReplaysStoredAgents is the end-to-end
// slice: a closed Project with stored Agents comes back with an Agent-owned
// Pane per Agent, the one with a session ref rejoins that exact conversation,
// the one without starts a new one and says so, and the whole materialization
// still succeeds.
func TestRegistryTopologyMaterializationReplaysStoredAgents(t *testing.T) {
	command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
	launcher := command.agents.(*fakeTopologyAgentLauncher)
	resumed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-claude-1"), topic: "roadmap",
	})
	fresh := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "codex", provider: "codex", cwd: root,
	})
	markTopologyAgentInterrupted(t, store, resumed.Metadata.UID, "")
	markTopologyAgentInterrupted(t, store, fresh.Metadata.UID, "")

	out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("materialize: err=%v\n%s", err, out)
	}
	for _, want := range []string{`"kind": "Agent"`, "uid:" + resumed.Metadata.UID, "uid:" + fresh.Metadata.UID} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}

	// 1. Both Agents are managed again: Running, with an Agent-owned Pane that
	//    carries the exact uid mirror `get agents` reads.
	for _, agentUID := range []string{resumed.Metadata.UID, fresh.Metadata.UID} {
		agent, ok := store.registry.Agent(agentUID)
		if !ok {
			t.Fatalf("agent %s disappeared", agentUID)
		}
		if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
			t.Fatalf("agent %s = %s paneRef=%q, want Running with a managed Pane", agentUID, agent.Status.Phase, agent.Status.PaneRef)
		}
		pane, ok := store.registry.Pane(agent.Status.PaneRef)
		if !ok || pane.Spec.Role != coremetadata.PaneRoleAgent || pane.Metadata.OwnerUID() != agentUID {
			t.Fatalf("agent %s managed Pane is not an Agent-owned Pane: %+v", agentUID, pane)
		}
		if !slices.ContainsFunc(server.session("beta").windows[0].panes, func(p *fakeTmuxPane) bool {
			return p.opts[tmuxopts.PaneUID] == agent.Status.PaneRef
		}) {
			t.Fatalf("agent %s managed Pane uid never reached tmux:\n%s", agentUID, server.state())
		}
	}

	// 2. The Agent that had a session ref resumes that exact conversation, and
	//    the ref itself is untouched by the replay.
	if !server.argvContains("--resume") || !server.argvContains("conv-claude-1") {
		t.Fatalf("the stored claude conversation never reached the launch argv:\n%#v", server.calls)
	}
	stillStored, _ := store.registry.Agent(resumed.Metadata.UID)
	if stillStored.Status.SessionRef.ConversationID() != "conv-claude-1" {
		t.Fatalf("replay rewrote the durable session ref: %+v", stillStored.Status.SessionRef)
	}
	if !slices.ContainsFunc(launcher.binds, func(bind string) bool {
		return strings.HasPrefix(bind, "resumed ") && strings.HasSuffix(bind, "conv-claude-1")
	}) {
		t.Fatalf("the resumed Pane was not bound with its conversation: %v", launcher.binds)
	}

	// 3. The Agent with no session ref comes up on a NEW conversation, it is
	//    reported, and the overall materialization still succeeded.
	if !slices.ContainsFunc(launcher.binds, func(bind string) bool { return strings.HasPrefix(bind, "managed ") }) {
		t.Fatalf("the ref-less Agent was not bound as a fresh managed pane: %v", launcher.binds)
	}
	if !strings.Contains(stderr, "agent/main/codex starts a new conversation instead of resuming") ||
		!strings.Contains(stderr, "no provider session ref is recorded") {
		t.Fatalf("the unresumed Agent was not reported: %q", stderr)
	}
	if strings.Contains(stderr, "agent/main/claude") {
		t.Fatalf("a resumed Agent was reported as unresumed: %q", stderr)
	}

	// 4. A repeat is a Registry-write-free no-op: a live Agent Pane is not this
	//    pass's work.
	server.calls = nil
	writesBefore := store.writes
	repeat, repeatErr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writesBefore || repeatErr != "" {
		t.Fatalf("repeat replayed Agents: err=%v stderr=%q writes=%d->%d\n%s", err, repeatErr, writesBefore, store.writes, repeat)
	}
	for _, call := range server.calls {
		if len(call) > 0 && call[0] == "split-window" {
			t.Fatalf("repeat split a second Agent pane: %v", call)
		}
	}
}

func TestRegistryTopologyContinueLaunchesInterruptedBOnceAndRetainsCleanA(t *testing.T) {
	command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
	launcher := command.agents.(*fakeTopologyAgentLauncher)
	cleanRef := codexConversationRef("thread-clean-a")
	interruptedRef := codexConversationRef("thread-interrupted-b")
	clean := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "clean-a", provider: "codex", cwd: root, ref: cleanRef,
	})
	interrupted := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "interrupted-b", provider: "codex", cwd: root, ref: interruptedRef,
	})
	cleanPane := markTopologyAgentInterrupted(t, store, clean.Metadata.UID, "")
	markTopologyAgentInterrupted(t, store, interrupted.Metadata.UID, "")
	zero := 0
	cleanStored, _ := store.registry.Agent(clean.Metadata.UID)
	cleanEvidence := cleanStored.Status.LastTermination.Clone()
	cleanEvidence.Source = coremetadata.TerminationSourceSupervisor
	cleanEvidence.Classification = coremetadata.TerminationNormal
	cleanEvidence.ExitCode = &zero
	cleanEvidence.OperationID = "op-supervisor-clean"
	cleanStored.Status.LastTermination = cleanEvidence.Clone()
	cleanStored.Status.Reason = coremetadata.TerminationReasonNormal
	cleanPaneStored, _ := store.registry.Pane(cleanPane.Metadata.UID)
	cleanPaneStored.Status.LastTermination = cleanEvidence.Clone()
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("clean/interrupted fixture: %v", err)
	}

	out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || !strings.Contains(out, "uid:"+interrupted.Metadata.UID) || strings.Contains(out, "uid:"+clean.Metadata.UID) {
		t.Fatalf("Continue plan did not select only interrupted B: err=%v stderr=%q\n%s", err, stderr, out)
	}
	if !strings.Contains(stderr, "agent/main/clean-a was not restored") || !strings.Contains(stderr, "supervisor/normal") {
		t.Fatalf("clean A refusal was not useful: %q", stderr)
	}
	afterClean, cleanOK := store.registry.Agent(clean.Metadata.UID)
	afterInterrupted, interruptedOK := store.registry.Agent(interrupted.Metadata.UID)
	if !cleanOK || afterClean.Status.Phase != coremetadata.PhaseOffline || afterClean.Status.PaneRef != "" ||
		!afterClean.Status.SessionRef.SameConversation(cleanRef) {
		t.Fatalf("clean A UID/sessionRef/state changed: ok=%t status=%+v", cleanOK, afterClean.Status)
	}
	if !interruptedOK || afterInterrupted.Status.Phase != coremetadata.PhaseRunning || afterInterrupted.Status.PaneRef == "" ||
		!afterInterrupted.Status.SessionRef.SameConversation(interruptedRef) {
		t.Fatalf("interrupted B did not resume exactly: ok=%t status=%+v", interruptedOK, afterInterrupted.Status)
	}
	if len(launcher.binds) != 1 || !strings.HasSuffix(launcher.binds[0], "thread-interrupted-b") {
		t.Fatalf("Continue Agent launches = %v, want interrupted B exactly once", launcher.binds)
	}

	server.calls = nil
	writes := store.writes
	repeat, repeatStderr, repeatErr := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if repeatErr != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writes || len(launcher.binds) != 1 {
		t.Fatalf("repeated Continue duplicated B: err=%v stderr=%q writes=%d->%d binds=%v\n%s",
			repeatErr, repeatStderr, writes, store.writes, launcher.binds, repeat)
	}
	if slices.ContainsFunc(server.calls, func(call []string) bool { return len(call) != 0 && call[0] == "split-window" }) {
		t.Fatalf("repeated Continue split a duplicate Agent Pane: %#v", server.calls)
	}
}

// TestRegistryTopologyMaterializationAgentReplayRefusalsAreNeverFatal fixes the
// contract's central asymmetry: an Agent projmux cannot launch is disclosed and
// left behind, and the Windows and shell Panes around it still converge.
func TestRegistryTopologyMaterializationAgentReplayRefusalsAreNeverFatal(t *testing.T) {
	for _, test := range []struct {
		name    string
		agent   topologyFixtureAgent
		arrange func(*fakeTopologyAgentLauncher)
		want    string
	}{
		{
			name:  "a provider that refuses exact resume is not launched",
			agent: topologyFixtureAgent{name: "claude", provider: "claude", ref: claudeConversationRef("conv-dead")},
			arrange: func(f *fakeTopologyAgentLauncher) {
				f.resumeErr["claude"] = fmt.Errorf("conversation conv-dead is unknown to this provider")
			},
			want: "could not build the required exact resume launch for conversation conv-dead",
		},
		{
			name:  "a Settings-disabled provider is not launched at all",
			agent: topologyFixtureAgent{name: "codex", provider: "codex", ref: codexConversationRef("thread-1")},
			arrange: func(f *fakeTopologyAgentLauncher) {
				f.disabled["codex"] = true
			},
			want: "the codex agent is disabled in Settings",
		},
		{
			name:  "a provider with no launch at all is not launched",
			agent: topologyFixtureAgent{name: "codex", provider: "codex"},
			arrange: func(f *fakeTopologyAgentLauncher) {
				f.launchErr["codex"] = fmt.Errorf("codex is not installed")
			},
			want: "no fresh codex launch could be built either",
		},
		{
			name:  "an Agent with no provider anywhere is not launched",
			agent: topologyFixtureAgent{name: "nameless"},
			want:  "neither the Agent nor its session ref names a provider",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, store, server, _, root, logs := newTopologyMaterializeFixture(t)
			launcher := command.agents.(*fakeTopologyAgentLauncher)
			if test.arrange != nil {
				test.arrange(launcher)
			}
			declared := test.agent
			declared.cwd = root
			agent := addTopologyFixtureAgent(t, store, declared)
			markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")

			out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err != nil {
				t.Fatalf("an Agent replay problem aborted the whole materialization: %v\n%s", err, out)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, test.want)
			}
			// The shell topology converged regardless.
			session := server.session("beta")
			if session == nil || len(session.windows) != 2 || len(session.windows[0].panes) < 2 {
				t.Fatalf("the shell topology did not converge around the Agent:\n%s", server.state())
			}
			for _, cwd := range []string{root, logs} {
				if !slices.ContainsFunc(server.calls, func(call []string) bool { return flagValue(call, "-c") == cwd }) {
					t.Fatalf("no exact -c %s call: %#v", cwd, server.calls)
				}
			}
			stored, _ := store.registry.Agent(agent.Metadata.UID)
			if stored.Status.Phase != coremetadata.PhaseOffline || stored.Status.PaneRef != "" {
				t.Fatalf("an unlaunchable Agent was still materialized: %s paneRef=%q", stored.Status.Phase, stored.Status.PaneRef)
			}
			if strings.Contains(out, "uid:"+agent.Metadata.UID) {
				t.Fatalf("an unlaunchable Agent entered the plan:\n%s", out)
			}
		})
	}
}

// TestRegistryTopologyMaterializationAgentReplayHonorsTheOwnerGuard proves the
// Phase did not widen the owner guard. A stale managed Pane row is released only
// when its uid is live nowhere on the socket; a uid a foreign session still
// claims refuses the pass before the first mutation, in the shipped wording.
func TestRegistryTopologyMaterializationAgentReplayHonorsTheOwnerGuard(t *testing.T) {
	command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-guard"),
	})
	stale, err := store.mutator().AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		Name: "managed", CWD: root,
	}, "op-agent-stale")
	if err != nil {
		t.Fatal(err)
	}
	markTopologyAgentInterrupted(t, store, agent.Metadata.UID, stale.Metadata.UID)

	// A foreign live session claims the stale managed Pane uid. The pass must
	// refuse rather than delete a Registry row whose runtime object is alive.
	foreign := server.addSession("foreign")
	foreign.windows[0].panes[0].opts[tmuxopts.PaneUID] = stale.Metadata.UID

	before := store.snapshot()
	out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err == nil {
		t.Fatalf("a foreign claim on the stale managed Pane uid was not refused:\n%s", out)
	}
	if !strings.Contains(err.Error(), "is already live on") {
		t.Fatalf("owner-guard wording changed: %v", err)
	}
	if store.snapshot() != before {
		t.Fatalf("a refused pass mutated the Registry")
	}
	// With the foreign claim gone the stale row is provably dead, so the Agent
	// is released and re-attached under its own uid.
	server.sessions = slices.DeleteFunc(server.sessions, func(s *fakeTmuxSession) bool { return s.name == "foreign" })
	if _, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json"); err != nil {
		t.Fatalf("stale managed Pane row was not converged: %v", err)
	}
	stored, _ := store.registry.Agent(agent.Metadata.UID)
	if stored.Status.Phase != coremetadata.PhaseRunning || stored.Status.PaneRef == stale.Metadata.UID {
		t.Fatalf("stale managed Pane row survived: %s paneRef=%q", stored.Status.Phase, stored.Status.PaneRef)
	}
	if _, ok := store.registry.Pane(stale.Metadata.UID); ok {
		t.Fatalf("the stale managed Pane row was never released")
	}
}

// TestProjectTopologyStartupDescriptionNamesAgents pins the startup row copy to
// the restore scope it actually performs.
func TestProjectTopologyStartupDescriptionNamesAgents(t *testing.T) {
	if !strings.Contains(projectTopologyStartupDescription, "Agent") {
		t.Fatalf("the Project topology row still hides Agent replay: %q", projectTopologyStartupDescription)
	}
	if strings.Contains(projectTopologyStartupDescription, "Window and shell Pane") {
		t.Fatalf("the Project topology row still claims a shell-only restore: %q", projectTopologyStartupDescription)
	}
	if got := topologyProjectStartupCandidate().Description; got != projectTopologyStartupDescription {
		t.Fatalf("the startup row and the shared description drifted: %q != %q", got, projectTopologyStartupDescription)
	}
}
