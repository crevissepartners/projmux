package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/cli"
	"os"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestCodexResumeAttentionChecksTheWholeBindingWithoutRepair(t *testing.T) {
	store := newProcessAttentionStore(t.TempDir())
	recorded := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "recorded-generation", Operation: "operation"}
	if _, err := checkCodexResumeAttention(store, recorded, nil); err != nil {
		t.Fatal("absent attention should retain existing resume behavior", err)
	}
	for _, change := range []struct {
		name string
		edit func(*processhost.Binding)
	}{
		{"matching", func(*processhost.Binding) {}},
		{"generation", func(b *processhost.Binding) { b.Generation = "attention-generation" }},
		{"same generation different operation", func(b *processhost.Binding) { b.Operation = "other" }},
		{"host", func(b *processhost.Binding) { b.Host = "other" }},
		{"agent", func(b *processhost.Binding) { b.Agent = "other" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			attention := recorded
			change.edit(&attention)
			if err := store.write(map[string]processAttentionRecord{recorded.Pane: {Binding: attention, Provider: aiModeCodex, Terminal: true}}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(store.path)
			_, err := checkCodexResumeAttention(store, recorded, nil)
			if attention == recorded {
				if err != nil {
					t.Fatal("matching attention refused", err)
				}
			} else {
				if !errors.Is(err, processhost.ErrResumeRefused) || !strings.Contains(err.Error(), processResumeAttentionMismatch) || !strings.Contains(err.Error(), "recordedGeneration="+recorded.Generation) || !strings.Contains(err.Error(), "attentionGeneration="+attention.Generation) {
					t.Fatal("missing exact conflict reason", err)
				}
				if strings.Contains(processResumeFailure(recorded, err).Error(), "after this owned generation is retired") {
					t.Fatal("conflict claimed retirement/retry would repair attention")
				}
			}
			after, _ := os.ReadFile(store.path)
			if !bytes.Equal(before, after) {
				t.Fatal("preflight repaired attention")
			}
		})
	}
}

func TestProcessSupportedResumeFirstFrameDistinguishesUserAndPeer(t *testing.T) {
	agent := selector.Ref{Kind: coremetadata.KindAgent, UID: "agent-resume"}
	peer, err := providerCoordinationContent(dialogueEnvelope("resume-peer", time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []processResumeFirstFrame{{}, {Kind: "user", Text: "literal user task"}, {Kind: "peer", Text: peer}} {
		req, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: agent, Prompt: frame})
		if err != nil || req.options.Prompt != frame {
			t.Fatal("first frame was rewritten", err)
		}
	}
	for _, frame := range []processResumeFirstFrame{{Text: "without kind"}, {Kind: "user", Text: " "}, {Kind: "peer", Text: "plain peer task"}, {Kind: "unknown", Text: "task"}} {
		if _, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: agent, Prompt: frame}); err == nil {
			t.Fatal("untyped or unwrapped first frame accepted", frame)
		}
	}
	var content map[string]any
	if json.Unmarshal([]byte(peer), &content) != nil || content["authority"] != "untrusted-coordination-only" {
		t.Fatal("peer content lost the live delivery envelope")
	}
}

func TestProcessSupportedResumeRefusesBeforePreparingProvider(t *testing.T) {
	reg := processResumeQueryFixture(t)
	candidate := listResumableProcessAgents(reg, processResumeFilter{})[0]
	command := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: candidate.Agent.Metadata.UID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = command.resumeProcessAgent(context.Background(), request)
	if !errors.Is(err, processhost.ErrResumeRefused) || !strings.Contains(err.Error(), "explicit first frame") {
		t.Fatal("missing first frame reached provider", err)
	}
}

func TestProcessSupportedResumeReceiptPreservesExistingIdentity(t *testing.T) {
	receipt := processResumeReceipt(processhost.Binding{Agent: "existing-agent", Window: "existing-window"}, "existing")
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := receipt.WriteJSON(&output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"operation":"agent.resume"`, `"identity":"reused"`, `"address":"unchanged"`, `"topology":"unchanged"`, `"desiredState":"unchanged"`, `"runtime":"materialized"`, `"focus":"unchanged"`} {
		if !strings.Contains(strings.ReplaceAll(output.String(), " ", ""), want) {
			t.Fatalf("dishonest resume receipt missing %s: %s", want, output.String())
		}
	}
	if receipt.Operation == cli.OperationCreateAgent {
		t.Fatal("resume claimed creation")
	}
}

func TestProcessSupportedResumeKeepsTmuxGrammarAndDetachedResult(t *testing.T) {
	for _, args := range [][]string{{"resume", "codex", "--project", "beta"}, {"resume", "--project", "beta", "--", "codex"}} {
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
		command, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
		enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
		stdout, stderr, err := runRoute(t, command, args...)
		if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
			t.Fatalf("tmux bytes changed for %q: %q %q %v", args, stdout, stderr, err)
		}
	}
}

func TestCodexResumeAttentionRequiresExactRetiredOwner(t *testing.T) {
	store := newProcessAttentionStore(t.TempDir())
	recorded := processhost.Binding{Host: "new-host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "new-generation", Operation: "new-op"}
	old := processAttentionRecord{Binding: recorded, Provider: aiModeCodex}
	old.Binding.Host, old.Binding.Generation, old.Binding.Operation = "old-host", "old-generation", "old-op"
	exit := 0
	receipt := coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal, ObservedAt: time.Now().UTC(), AgentUID: old.Binding.Agent, PaneUID: old.Binding.Pane, Generation: old.Binding.Generation, OperationID: old.Binding.Operation, ExitCode: &exit}
	for _, tc := range []struct {
		name  string
		edit  func(*processAttentionRecord, *coremetadata.TerminationEvidence)
		allow bool
	}{
		{"exact Wait", func(*processAttentionRecord, *coremetadata.TerminationEvidence) {}, true},
		{"terminal badge without Wait", func(o *processAttentionRecord, r *coremetadata.TerminationEvidence) {
			o.Terminal = true
			r.Source = coremetadata.TerminationSourceReconcile
		}, false},
		{"wrong generation Wait", func(_ *processAttentionRecord, r *coremetadata.TerminationEvidence) { r.Generation = "other" }, false},
		{"wrong operation Wait", func(_ *processAttentionRecord, r *coremetadata.TerminationEvidence) { r.OperationID = "other" }, false},
		{"wrong Agent", func(o *processAttentionRecord, r *coremetadata.TerminationEvidence) {
			o.Binding.Agent = "other"
			r.AgentUID = "other"
		}, false},
		{"wrong Pane", func(o *processAttentionRecord, r *coremetadata.TerminationEvidence) {
			o.Binding.Pane = "other"
			r.PaneUID = "other"
		}, false},
		{"wrong Window", func(o *processAttentionRecord, _ *coremetadata.TerminationEvidence) { o.Binding.Window = "other" }, false},
		{"wrong Project", func(o *processAttentionRecord, _ *coremetadata.TerminationEvidence) { o.Binding.Project = "other" }, false},
		{"wrong provider", func(o *processAttentionRecord, _ *coremetadata.TerminationEvidence) { o.Provider = aiModeClaude }, false},
		{"same generation", func(o *processAttentionRecord, r *coremetadata.TerminationEvidence) {
			o.Binding.Generation = recorded.Generation
			r.Generation = recorded.Generation
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, evidence := old, receipt
			tc.edit(&o, &evidence)
			if err := store.write(map[string]processAttentionRecord{recorded.Pane: o}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(store.path)
			selected, err := checkCodexResumeAttention(store, recorded, []coremetadata.TerminationEvidence{evidence})
			if tc.allow {
				if err != nil || selected == nil || selected.Binding != o.Binding {
					t.Fatal("retired owner refused", err)
				}
			} else if !errors.Is(err, processhost.ErrResumeRefused) {
				t.Fatal("unproven owner accepted", err)
			}
			after, _ := os.ReadFile(store.path)
			if !bytes.Equal(before, after) {
				t.Fatal("preflight mutated attention")
			}
		})
	}
}

func TestCodexResumeAttentionTakeoverFencesWholeBinding(t *testing.T) {
	state := t.TempDir()
	store := newProcessAttentionStore(state)
	old := processAttentionRecord{Provider: aiModeCodex, Binding: processhost.Binding{Host: "old-host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "old-gen", Operation: "old-op"}}
	binding := old.Binding
	binding.Host, binding.Generation, binding.Operation = "new-host", "new-gen", "new-op"
	result := processAgentResumeResult{Binding: binding, previousBinding: old.Binding, previousAttention: &old, owner: processAgentCreateResult{registryPath: state + "/metadata/registry.json"}}
	if err := store.write(map[string]processAttentionRecord{binding.Pane: old}); err != nil {
		t.Fatal(err)
	}
	changed := old
	changed.Binding.Host = "concurrent-host"
	if err := store.write(map[string]processAttentionRecord{binding.Pane: changed}); err != nil {
		t.Fatal(err)
	}
	if err := result.activateCodexResumeAttention(); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("same-generation race accepted", err)
	}
	if err := store.write(map[string]processAttentionRecord{binding.Pane: old}); err != nil {
		t.Fatal(err)
	}
	if err := result.activateCodexResumeAttention(); err != nil {
		t.Fatal(err)
	}
	if err := result.activateCodexResumeAttention(); err != nil {
		t.Fatal("idempotence", err)
	}
	if err := store.clear(old.Binding, 0); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("old writer retained authority", err)
	}
}
