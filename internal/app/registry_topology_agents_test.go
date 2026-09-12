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
	resumes   []string
	launches  []string
}

type productionBindingTopologyAgentLauncher struct {
	*fakeTopologyAgentLauncher
	binder *aiCommand
}

func (l *productionBindingTopologyAgentLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	return l.binder.BindAgentPaneOnRoute(ctx, runner, binding)
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
	f.launches = append(f.launches, provider)
	if err := f.launchErr[provider]; err != nil {
		return "", nil, err
	}
	if len(payload) != 0 {
		return "", nil, fmt.Errorf("topology replay must never carry a payload, got %v", payload)
	}
	return provider, []string{"/opt/" + provider, "--cwd", workspace.CWD}, nil
}

func (f *fakeTopologyAgentLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	f.resumes = append(f.resumes, provider+":"+conversationID)
	if err := f.resumeErr[provider]; err != nil {
		return "", nil, err
	}
	return provider, []string{"/opt/" + provider, "--cwd", workspace.CWD, "--resume", conversationID}, nil
}

func (f *fakeTopologyAgentLauncher) BindAgentPaneOnRoute(_ context.Context, _ tmuxCommandRunner, binding agentPaneBinding) error {
	if binding.ConversationID != "" {
		f.binds = append(f.binds, fmt.Sprintf("resumed %s %s %s %s", binding.PaneID, binding.Provider, binding.ContextDir, binding.ConversationID))
	} else {
		f.binds = append(f.binds, fmt.Sprintf("managed %s %s %s", binding.PaneID, binding.Provider, binding.ContextDir))
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
	return markTopologyAgentTermination(t, store, agentUID, paneUID, coremetadata.TerminationInterrupted)
}

// markTopologyAgentTermination uses the real receipt and projection producers,
// including abnormal -> Failed. An empty classification removes both receipts
// after projection to model an older retained activation without evidence.
func markTopologyAgentTermination(t *testing.T, store *fakeResourceStore, agentUID, paneUID string, classification coremetadata.TerminationClassification) coremetadata.Pane {
	t.Helper()
	mutator := store.mutator()
	if paneUID == "" {
		pane, err := mutator.AttachAgentPane(&store.registry, agentUID, coremetadata.BootstrapPane{
			Name: "retained-" + agentUID, CWD: store.registry.Projects[1].Spec.Root,
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
	receipt.Classification = classification
	switch classification {
	case coremetadata.TerminationNormal, coremetadata.TerminationAbnormal:
		receipt.Source = coremetadata.TerminationSourceSupervisor
		code := 0
		if classification == coremetadata.TerminationAbnormal {
			code = 42
		}
		receipt.ExitCode = &code
	case coremetadata.TerminationKilled:
		receipt.Source = coremetadata.TerminationSourceSupervisor
		receipt.Signal = "HUP"
	case coremetadata.TerminationUnknown, "":
		receipt.Source = coremetadata.TerminationSourceReconcile
		receipt.Classification = coremetadata.TerminationUnknown
		receipt.OperationID = ""
	}
	if outcome, err := mutator.RecordTermination(&store.registry, receipt); err != nil || !outcome.Applied {
		t.Fatalf("record interrupted evidence: %+v, %v", outcome, err)
	}
	wantPhase := coremetadata.PhaseOffline
	if classification == coremetadata.TerminationAbnormal {
		wantPhase = coremetadata.PhaseFailed
	}
	if projection, err := mutator.ProjectTermination(&store.registry, coremetadata.TerminationProjectionInput{
		PaneUID: paneUID, Generation: receipt.Generation, ObservedAt: receipt.ObservedAt,
	}); err != nil || projection.AgentUID != agentUID || projection.Phase != wantPhase {
		t.Fatalf("project interrupted evidence: %+v, %v", projection, err)
	}
	pane, ok := store.registry.Pane(paneUID)
	if !ok {
		t.Fatalf("interrupted projection removed retained Pane %s", paneUID)
	}
	if classification == "" {
		agent, _ := store.registry.Agent(agentUID)
		agent.Status.LastTermination = nil
		pane.Status.LastTermination = nil
	}
	return pane.Clone()
}

// The complete source/classification x phase matrix keeps intent and normal
// exit excluded even when only the phase changes. Ref requirements follow in
// the replay planner and never substitute a fresh conversation.
func TestTopologyAgentContinueEligibilityMatrix(t *testing.T) {
	for _, classification := range []coremetadata.TerminationClassification{
		coremetadata.TerminationInterrupted, coremetadata.TerminationKilled, coremetadata.TerminationAbnormal,
		coremetadata.TerminationUnknown, "", coremetadata.TerminationIntentional, coremetadata.TerminationNormal,
	} {
		for _, phase := range []coremetadata.AgentPhase{coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed, coremetadata.PhasePending} {
			t.Run(string(classification)+"/"+string(phase), func(t *testing.T) {
				command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
				seed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
					name: "matrix", provider: "codex", cwd: root, ref: codexConversationRef("thread-matrix"),
				})
				pane := markTopologyAgentTermination(t, store, seed.Metadata.UID, "", classification)
				agent, _ := store.registry.Agent(seed.Metadata.UID)
				agent.Status.Phase = phase
				if phase == coremetadata.PhaseRunning {
					agent.Status.PaneRef = pane.Metadata.UID
				}
				want := phase != coremetadata.PhasePending && classification != coremetadata.TerminationIntentional && classification != coremetadata.TerminationNormal
				got, _, reason := decideTopologyAgentContinueEligibility(store.registry, agent.Clone())
				if got != want || (got && reason != "") || (!got && reason == "") {
					t.Fatalf("eligible=%t reason=%q, want %t", got, reason, want)
				}
				// Run the same row through materialization, not only the pure gate.
				launcher := command.agents.(*fakeTopologyAgentLauncher)
				before := agent.Clone()
				_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
				if err != nil || len(launcher.launches) != 0 {
					t.Fatalf("materialize: %v stderr=%q fresh=%v", err, stderr, launcher.launches)
				}
				if want {
					if !slices.Equal(launcher.resumes, []string{"codex:thread-matrix"}) || len(launcher.binds) != 1 {
						t.Fatalf("exact resumes=%v binds=%v", launcher.resumes, launcher.binds)
					}
				} else if len(launcher.resumes) != 0 || len(launcher.binds) != 0 || !strings.Contains(stderr, reason) {
					t.Fatalf("refusal: resumes=%v binds=%v stderr=%q", launcher.resumes, launcher.binds, stderr)
				}
				after, _ := store.registry.Agent(seed.Metadata.UID)
				if !after.Status.SessionRef.SameConversation(before.Status.SessionRef) || (!want && !sameTopologyTerminationEvidence(after.Status.LastTermination, before.Status.LastTermination)) {
					t.Fatalf("Continue changed retained ref or excluded evidence: %+v", after.Status)
				}
			})
		}
	}
}

func TestTopologyAgentContinueRejectsInvalidActivationEvidence(t *testing.T) {
	_, store, _, _, root, _ := newTopologyMaterializeFixture(t)
	seed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "evidence", provider: "codex", cwd: root, ref: codexConversationRef("thread-evidence")})
	retained := markTopologyAgentInterrupted(t, store, seed.Metadata.UID, "")
	type mutation func(*coremetadata.Registry, *coremetadata.Agent, *coremetadata.Pane)
	withoutReceipt := func(next mutation) mutation {
		return func(reg *coremetadata.Registry, agent *coremetadata.Agent, pane *coremetadata.Pane) {
			agent.Status.LastTermination, pane.Status.LastTermination = nil, nil
			next(reg, agent, pane)
		}
	}
	for _, test := range []struct {
		name   string
		mutate mutation
	}{
		{"empty receipt", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.LastTermination = &coremetadata.TerminationEvidence{}
			p.Status.LastTermination = &coremetadata.TerminationEvidence{}
		}},
		{"stale generation", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.LastTermination.Generation = "old"
			p.Status.LastTermination.Generation = "old"
		}},
		{"foreign receipt agent", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.LastTermination.AgentUID = "foreign"
			p.Status.LastTermination.AgentUID = "foreign"
		}},
		{"missing observation", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.LastTermination.ObservedAt = time.Time{}
			p.Status.LastTermination.ObservedAt = time.Time{}
		}},
		{"missing control operation", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.LastTermination.OperationID = ""
			p.Status.LastTermination.OperationID = ""
		}},
		{"intent with exit", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			code := 1
			a.Status.LastTermination.ExitCode = &code
			p.Status.LastTermination.ExitCode = &code
		}},
		{"pane receipt absent", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.LastTermination = nil
		}},
		{"agent receipt absent", func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.LastTermination = nil
		}},
		{"receipt pane mismatch", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.LastTermination.PaneUID = "another-pane"
		}},
		{"receipt timestamp mismatch", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.LastTermination.ObservedAt = p.Status.LastTermination.ObservedAt.Add(time.Second)
		}},
		{"receipt operation mismatch", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.LastTermination.OperationID = "another-operation"
		}},
		{"missing retained pane", func(r *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			r.Panes = slices.DeleteFunc(r.Panes, func(candidate coremetadata.Pane) bool { return candidate.Metadata.UID == p.Metadata.UID })
		}},
		{"foreign owner", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Metadata.OwnerRef.UID = "foreign-agent"
		}},
		{"shell role", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Spec.Role = coremetadata.PaneRoleShell
		}},
		{"foreign activation", func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.AgentUID = "foreign-agent"
		}},
		{"Running another binding", func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.Phase = coremetadata.PhaseRunning
			a.Status.PaneRef = "another-pane"
		}},
		{"Offline binding", func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.PaneRef = p.Metadata.UID
		}},
		{"no receipt empty activation", withoutReceipt(func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation = coremetadata.PaneActivation{}
		})},
		{"no receipt foreign activation", withoutReceipt(func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.AgentUID = "foreign-agent"
		})},
		{"no receipt missing operation", withoutReceipt(func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.OperationID = ""
		})},
		{"no receipt missing start", withoutReceipt(func(_ *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			p.Status.Activation.StartedAt = time.Time{}
		})},
		{"no receipt ambiguous panes", withoutReceipt(func(r *coremetadata.Registry, _ *coremetadata.Agent, p *coremetadata.Pane) {
			other := p.Clone()
			other.Metadata.UID = "other-pane"
			other.Status.Activation.Generation = "other-generation"
			r.Panes = append(r.Panes, other)
		})},
		{"no receipt Failed current binding", withoutReceipt(func(_ *coremetadata.Registry, a *coremetadata.Agent, p *coremetadata.Pane) {
			a.Status.Phase = coremetadata.PhaseFailed
			a.Status.PaneRef = p.Metadata.UID
		})},
		{"no receipt Running missing binding", withoutReceipt(func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.Phase = coremetadata.PhaseRunning
		})},
		{"no receipt Running foreign binding", withoutReceipt(func(_ *coremetadata.Registry, a *coremetadata.Agent, _ *coremetadata.Pane) {
			a.Status.Phase = coremetadata.PhaseRunning
			a.Status.PaneRef = "pane-alpha"
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := store.registry.Clone()
			agent, _ := reg.Agent(seed.Metadata.UID)
			pane, _ := reg.Pane(retained.Metadata.UID)
			test.mutate(&reg, agent, pane)
			if ok, _, reason := decideTopologyAgentContinueEligibility(reg, agent.Clone()); ok || reason == "" {
				t.Fatalf("invalid activation admitted: eligible=%t reason=%q", ok, reason)
			}
		})
	}
}

func TestTopologyAgentContinueTerminationPairingAndShape(t *testing.T) {
	for _, source := range []coremetadata.TerminationSource{coremetadata.TerminationSourceControlAction, coremetadata.TerminationSourceSupervisor, coremetadata.TerminationSourceReconcile, "bogus"} {
		for _, classification := range []coremetadata.TerminationClassification{coremetadata.TerminationInterrupted, coremetadata.TerminationKilled, coremetadata.TerminationAbnormal, coremetadata.TerminationUnknown, coremetadata.TerminationIntentional, coremetadata.TerminationNormal, "bogus"} {
			t.Run(string(source)+"/"+string(classification), func(t *testing.T) {
				r := coremetadata.TerminationEvidence{Source: source, Classification: classification, OperationID: "op"}
				if classification == coremetadata.TerminationAbnormal {
					code := 42
					r.ExitCode = &code
				}
				if classification == coremetadata.TerminationKilled {
					r.Signal = "HUP"
				}
				want := source == coremetadata.TerminationSourceControlAction && classification == coremetadata.TerminationInterrupted || source == coremetadata.TerminationSourceSupervisor && (classification == coremetadata.TerminationKilled || classification == coremetadata.TerminationAbnormal) || source == coremetadata.TerminationSourceReconcile && classification == coremetadata.TerminationUnknown
				if _, reason := topologyContinueTerminationReason(r); (reason == "") != want {
					t.Fatalf("reason=%q want admitted=%t", reason, want)
				}
			})
		}
	}
	for _, test := range []struct {
		name           string
		classification coremetadata.TerminationClassification
		code           *int
		signal         string
		want           bool
	}{
		{"abnormal signal", coremetadata.TerminationAbnormal, nil, "TERM", true},
		{"abnormal without wait status", coremetadata.TerminationAbnormal, nil, "", false},
		{"abnormal zero exit", coremetadata.TerminationAbnormal, exitCodePtr(0), "", false},
		{"abnormal negative exit", coremetadata.TerminationAbnormal, exitCodePtr(-1), "", false},
		{"abnormal HUP", coremetadata.TerminationAbnormal, nil, "HUP", false},
		{"killed non HUP", coremetadata.TerminationKilled, nil, "TERM", false},
		{"killed dual status", coremetadata.TerminationKilled, exitCodePtr(129), "HUP", false},
		{"abnormal dual status", coremetadata.TerminationAbnormal, exitCodePtr(143), "TERM", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, reason := topologyContinueTerminationReason(coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: test.classification, ExitCode: test.code, Signal: test.signal})
			if (reason == "") != test.want {
				t.Fatalf("reason=%q want admitted=%t", reason, test.want)
			}
		})
	}
}

// TestTopologyAgentResumeDecisionTable pins the three resume-decision branches
// the Phase owes: a stored ref resumes, a missing or unusable ref has a stated
// refusal reason, and the provider discriminator is the
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
			name: "no session ref has a refusal reason",
			agent: coremetadata.Agent{
				Spec:   coremetadata.AgentSpec{Provider: "codex"},
				Status: coremetadata.AgentStatus{},
			},
			wantProvider: "codex",
			wantReason:   "no provider session ref is recorded",
		},
		{
			name: "a ref with no conversation id has a refusal reason",
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
			name: "a ref with no provider discriminator has a refusal reason",
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
// the one without stays Offline with a reason, and the whole materialization
// still succeeds.
func TestRegistryTopologyMaterializationReplaysStoredAgents(t *testing.T) {
	command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
	launcher := command.agents.(*fakeTopologyAgentLauncher)
	resumed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-claude-1"), topic: "roadmap",
	})
	refLessAgent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "codex", provider: "codex", cwd: root,
	})
	markTopologyAgentInterrupted(t, store, resumed.Metadata.UID, "")
	markTopologyAgentInterrupted(t, store, refLessAgent.Metadata.UID, "")

	out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("materialize: err=%v\n%s", err, out)
	}
	for _, want := range []string{`"kind": "Agent"`, "uid:" + resumed.Metadata.UID} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}

	// 1. The resumable Agent is managed again: Running, with an Agent-owned Pane that
	//    carries the exact uid mirror `get agents` reads.
	for _, agentUID := range []string{resumed.Metadata.UID} {
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

	// 3. Managed stop grants no exception for a missing sessionRef. Its UID,
	// evidence and Offline state remain, while all other topology converges.
	refLess, _ := store.registry.Agent(refLessAgent.Metadata.UID)
	if refLess.Status.Phase != coremetadata.PhaseOffline || refLess.Status.PaneRef != "" || len(launcher.launches) != 0 || len(launcher.binds) != 1 {
		t.Fatalf("ref-less Continue launched fresh: status=%+v fresh=%v binds=%v", refLess.Status, launcher.launches, launcher.binds)
	}
	if !strings.Contains(stderr, "agent/main/codex was not restored") || !strings.Contains(stderr, "no provider session ref is recorded") {
		t.Fatalf("the ref-less Agent was not reported: %q", stderr)
	}
	if strings.Contains(stderr, "agent/main/claude") {
		t.Fatalf("a resumed Agent was reported as unresumed: %q", stderr)
	}

	// 4. A repeat is a Registry-write-free no-op: a live Agent Pane is not this
	//    pass's work.
	server.calls = nil
	writesBefore := store.writes
	repeat, repeatErr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writesBefore || !strings.Contains(repeatErr, "no provider session ref is recorded") {
		t.Fatalf("repeat replayed Agents: err=%v stderr=%q writes=%d->%d\n%s", err, repeatErr, writesBefore, store.writes, repeat)
	}
	for _, call := range server.calls {
		if len(call) > 0 && call[0] == "split-window" {
			t.Fatalf("repeat split a second Agent pane: %v", call)
		}
	}
}

func TestRegistryTopologyBinderWriteFailureIsVisibleAndRollsBack(t *testing.T) {
	t.Parallel()
	command, store, server, routed, root, _ := newTopologyMaterializeFixture(t)
	planner := command.agents.(*fakeTopologyAgentLauncher)
	binder := testAICommand(t.TempDir())
	command.agents = &productionBindingTopologyAgentLauncher{fakeTopologyAgentLauncher: planner, binder: binder}
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-bind-failure"), topic: "roadmap",
	})
	markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")
	registryBefore, runtimeBefore, writesBefore := store.snapshot(), server.state(), store.writes
	server.fail = []string{"set-option", aiPaneAgentOption}
	server.failMessage = "permission denied writing replayed Pane metadata"

	out, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err == nil || !strings.Contains(stderr, "Continue failed: resumed 0, skipped 0;") || !strings.Contains(err.Error(), "permission denied writing replayed Pane metadata") {
		t.Fatalf("binder failure = stderr=%q err=%v\n%s", stderr, err, out)
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore || server.state() != runtimeBefore {
		t.Fatalf("binder failure did not roll back: writes=%d->%d registry before=%s after=%s runtime before=%s after=%s",
			writesBefore, store.writes, registryBefore, store.snapshot(), runtimeBefore, server.state())
	}
	if !server.argvContains("split-window") || (!server.argvContains("kill-pane") && !server.argvContains("kill-session")) {
		t.Fatalf("binder failure did not materialize and roll back the replayed Pane: %#v", server.calls)
	}
	if commands := cmdRecorder(binder).commands; len(commands) != 0 {
		t.Fatalf("failed topology binder used ambient subprocess runner: %#v", commands)
	}
	foundBindingWrite := false
	for _, call := range routed.calls {
		if !slices.Contains(call.args, aiPaneAgentOption) {
			continue
		}
		foundBindingWrite = true
		if call.flag != "-L" || call.value != "topology" {
			t.Fatalf("topology binder write escaped exact route: %#v", call)
		}
	}
	if !foundBindingWrite {
		t.Fatalf("topology binder failure was not injected through the exact route: %#v", routed.calls)
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
	if !slices.Equal(launcher.resumes, []string{"codex:thread-interrupted-b"}) || len(launcher.launches) != 0 {
		t.Fatalf("Continue plans resume=%v fresh=%v, want exact resume once and fresh zero", launcher.resumes, launcher.launches)
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
			want: "no provider session ref is recorded",
		},
		{
			name:  "an Agent with no provider anywhere is not launched",
			agent: topologyFixtureAgent{name: "nameless"},
			want:  "no provider session ref is recorded",
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
			if len(launcher.launches) != 0 || len(launcher.binds) != 0 {
				t.Fatalf("exact resume preparation failure planned a fresh Agent: resume=%v fresh=%v", launcher.resumes, launcher.launches)
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

	before, runtimeBefore, writesBefore := store.snapshot(), server.state(), store.writes
	server.calls = nil
	out, _, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err == nil {
		t.Fatalf("a foreign claim on the stale managed Pane uid was not refused:\n%s", out)
	}
	if !strings.Contains(err.Error(), "is already live on") {
		t.Fatalf("owner-guard wording changed: %v", err)
	}
	if store.snapshot() != before || server.state() != runtimeBefore || store.writes != writesBefore {
		t.Fatalf("a refused pass mutated the Registry")
	}
	for _, call := range server.calls {
		if len(call) != 0 && slices.Contains([]string{"new-session", "new-window", "split-window", "kill-pane", "set-option"}, call[0]) {
			t.Fatalf("foreign UID claim reached a first runtime write: %v", call)
		}
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

func TestTopologyAgentContinueResumePreparationMatrix(t *testing.T) {
	for _, test := range []struct {
		name         string
		ref          *coremetadata.AgentSessionRef
		provider     string
		configure    func(*fakeTopologyAgentLauncher, *coremetadata.Agent)
		want         string
		conversation string
	}{
		{name: "nil ref", provider: "codex", want: "no provider session ref"},
		{name: "empty ref", provider: "codex", ref: &coremetadata.AgentSessionRef{}, want: "no provider session ref"},
		{name: "blank ref", provider: "codex", ref: codexConversationRef(" \t\n"), want: "no conversation id"},
		{name: "missing id", provider: "codex", ref: codexConversationRef(""), want: "no conversation id"},
		{name: "missing discriminator", provider: "codex", ref: &coremetadata.AgentSessionRef{Codex: &coremetadata.CodexSessionRef{ThreadID: "thread"}}, want: "no provider discriminator"},
		{name: "blank discriminator", provider: "codex", ref: &coremetadata.AgentSessionRef{Provider: " \t", Codex: &coremetadata.CodexSessionRef{ThreadID: "thread"}}, want: "no provider discriminator"},
		{name: "cross provider", provider: "codex", ref: claudeConversationRef("conversation"), want: "is a codex Agent but its session ref is a claude"},
		{name: "mismatched member", provider: "codex", ref: &coremetadata.AgentSessionRef{Provider: "codex", Claude: &coremetadata.ClaudeSessionRef{SessionID: "conversation"}}, want: "mismatched provider member"},
		{name: "multiple members", provider: "codex", ref: &coremetadata.AgentSessionRef{Provider: "codex", Claude: &coremetadata.ClaudeSessionRef{SessionID: "conversation"}, Codex: &coremetadata.CodexSessionRef{ThreadID: "thread"}}, want: "mismatched provider member"},
		{name: "unsupported provider", provider: "unsupported", ref: &coremetadata.AgentSessionRef{Provider: "unsupported", Codex: &coremetadata.CodexSessionRef{ThreadID: "thread"}}, want: "unsupported provider"},
		{name: "disabled provider", provider: "codex", ref: codexConversationRef("thread"), configure: func(l *fakeTopologyAgentLauncher, _ *coremetadata.Agent) { l.disabled["codex"] = true }, want: "disabled in Settings"},
		{name: "missing cwd", provider: "codex", ref: codexConversationRef("thread"), configure: func(_ *fakeTopologyAgentLauncher, a *coremetadata.Agent) { a.Spec.Workspace.CWD += "/missing" }, want: "Agent cwd"},
		{name: "resume prepare failure", provider: "codex", ref: codexConversationRef("thread"), configure: func(l *fakeTopologyAgentLauncher, _ *coremetadata.Agent) {
			l.resumeErr["codex"] = fmt.Errorf("prepare failed")
		}, want: "prepare failed"},
		{name: "normalized valid id", provider: " codex ", ref: codexConversationRef(" \tthread\n"), conversation: "thread"},
		{name: "legacy codex session id", provider: "codex", ref: &coremetadata.AgentSessionRef{Provider: "codex", Codex: &coremetadata.CodexSessionRef{SessionID: " session "}}, conversation: "session"},
		{name: "normalized discriminator", provider: "claude", ref: &coremetadata.AgentSessionRef{Provider: " claude ", Claude: &coremetadata.ClaudeSessionRef{SessionID: " conversation "}}, conversation: "conversation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			launcher := newFakeTopologyAgentLauncher()
			agent := coremetadata.Agent{Spec: coremetadata.AgentSpec{Provider: test.provider, Workspace: coremetadata.AgentWorkspace{CWD: root}}, Status: coremetadata.AgentStatus{SessionRef: test.ref}}
			if test.configure != nil {
				test.configure(launcher, &agent)
			}
			plan := &registryTopologyPlan{}
			work, ok := planTopologyAgentReplay(plan, coremetadata.Project{Spec: coremetadata.ProjectSpec{Root: root}}, agent, "main/matrix", launcher, topologyAgentReplayInterrupted)
			if ok != (test.conversation != "") || len(launcher.launches) != 0 {
				t.Fatalf("planned=%t fresh=%v notices=%v", ok, launcher.launches, plan.notices)
			}
			if ok {
				if work.conversationID != test.conversation || len(launcher.resumes) != 1 || len(plan.notices) != 0 {
					t.Fatalf("exact resume=%+v calls=%v notices=%v", work, launcher.resumes, plan.notices)
				}
			} else if !strings.Contains(strings.Join(plan.notices, "\n"), test.want) {
				t.Fatalf("notices=%v want %q", plan.notices, test.want)
			}
		})
	}
}

func TestTopologyAgentSnapshotKeepsFreshFallback(t *testing.T) {
	for _, hasRef := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume-fails-%t", hasRef), func(t *testing.T) {
			root := t.TempDir()
			launcher := newFakeTopologyAgentLauncher()
			agent := coremetadata.Agent{Spec: coremetadata.AgentSpec{Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: root}}}
			if hasRef {
				agent.Status.SessionRef = claudeConversationRef("saved-conversation")
				launcher.resumeErr["claude"] = fmt.Errorf("cannot prepare")
			}
			plan := &registryTopologyPlan{}
			work, ok := planTopologyAgentReplay(plan, coremetadata.Project{}, agent, "main/snapshot", launcher, topologyAgentReplaySnapshot)
			if !ok || work.conversationID != "" || !slices.Equal(launcher.launches, []string{"claude"}) || !strings.Contains(strings.Join(plan.notices, "\n"), "starts a new conversation") {
				t.Fatalf("snapshot fallback=%t work=%+v fresh=%v notices=%v", ok, work, launcher.launches, plan.notices)
			}
		})
	}
}

func TestRegistryTopologyContinueRechecksEvidenceUnderLock(t *testing.T) {
	command, store, server, _, root, _ := newTopologyMaterializeFixture(t)
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "raced", provider: "codex", cwd: root, ref: codexConversationRef("thread-raced")})
	pane := markTopologyAgentTermination(t, store, agent.Metadata.UID, "", "")
	originalUpdate := command.resources.updateConvergent
	command.resources.updateConvergent = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		// A newer activation arrives after the unlocked plan. Its mismatching
		// owner makes the earlier no-receipt plan unusable under the lock.
		current, _ := store.registry.Pane(pane.Metadata.UID)
		current.Status.Activation.AgentUID = "different-agent"
		return originalUpdate(fn)
	}
	_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	launcher := command.agents.(*fakeTopologyAgentLauncher)
	if err != nil || len(launcher.binds) != 0 || len(launcher.launches) != 0 || server.argvContains("thread-raced") || !strings.Contains(stderr, "current Agent activation generation") {
		t.Fatalf("locked evidence escaped recheck: err=%v stderr=%q binds=%v", err, stderr, launcher.binds)
	}
}
