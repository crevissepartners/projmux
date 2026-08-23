package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAgentProgressClosedInventoryAndLifecycleClear(t *testing.T) {
	t.Parallel()
	want := []AgentProgressActivity{
		ProgressPlanning, ProgressCommand, ProgressFileChange, ProgressTool,
		ProgressWebSearch, ProgressDelegation, ProgressImage, ProgressReview,
		ProgressCompaction, ProgressOther,
	}
	if got := AgentProgressActivities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("activities = %v", got)
	}
	if ValidAgentProgressActivity("shell-command") || ValidAgentProgressActivity("tool-name") {
		t.Fatal("content-derived activity entered the closed vocabulary")
	}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	reg.Panes = []Pane{{Metadata: ObjectMeta{UID: "pane-1", OwnerRef: &OwnerRef{Kind: KindAgent, UID: "agent-1"}}, Status: PaneStatus{Activation: PaneActivation{Codex: &CodexActivationBinding{ThreadID: "thread-1", TurnID: "turn-1"}}}}}
	reg.Agents = []Agent{{Metadata: ObjectMeta{UID: "agent-1", Name: "codex"}, Status: AgentStatus{Phase: PhaseRunning, PaneRef: "pane-1"}}}
	mutator := Mutator{Now: func() time.Time { return now }}
	progress := AgentProgress{
		TurnRef: "turn-1", Activity: ProgressCommand, PlanCompleted: 2, PlanTotal: 4,
		ChangedFiles: 3, ActiveItemCount: 1, StartedAt: now.Add(-time.Minute), ObservedAt: now,
		Source: AgentProgressSource,
	}
	if _, changed, err := mutator.SetAgentProgress(&reg, "agent-1", "turn-1", progress); err != nil || !changed {
		t.Fatalf("set progress = %t/%v", changed, err)
	}
	if _, changed, err := mutator.SetAgentProgress(&reg, "agent-1", "turn-1", progress); err != nil || changed {
		t.Fatalf("identical progress = %t/%v, want write 0", changed, err)
	}
	if _, _, err := mutator.SetAgentProgress(&reg, "agent-1", "turn-other", progress); err == nil {
		t.Fatal("independent turn authority was accepted")
	}
	if _, err := mutator.TransitionAgent(&reg, "agent-1", PhaseOffline, "disconnected"); err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent("agent-1")
	if !agent.Status.Progress.IsZero() {
		t.Fatalf("Offline retained progress: %+v", agent.Status.Progress)
	}
}

func TestAgentProgressClearsOnFailedRebindAndPaneRelease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	progress := AgentProgress{TurnRef: "turn-1", Activity: ProgressTool, ObservedAt: now, Source: AgentProgressSource}
	fixture := func() Registry {
		reg := NewRegistry()
		reg.Panes = []Pane{{Metadata: ObjectMeta{UID: "pane-1", OwnerRef: &OwnerRef{Kind: KindAgent, UID: "agent-1"}}, Status: PaneStatus{Activation: PaneActivation{
			AgentUID: "agent-1", Generation: "generation-1", Codex: &CodexActivationBinding{ThreadID: "thread-1", TurnID: "turn-1"},
		}}}}
		reg.Agents = []Agent{{Metadata: ObjectMeta{UID: "agent-1", Name: "codex"}, Spec: AgentSpec{Provider: "codex"}, Status: AgentStatus{
			Phase: PhaseRunning, PaneRef: "pane-1", Progress: progress,
		}}}
		return reg
	}
	mutator := Mutator{Now: func() time.Time { return now }}
	failed := fixture()
	if _, err := mutator.TransitionAgent(&failed, "agent-1", PhaseFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	if agent, _ := failed.Agent("agent-1"); !agent.Status.Progress.IsZero() {
		t.Fatalf("Failed retained progress: %+v", agent.Status.Progress)
	}

	rebound := fixture()
	changed, err := mutator.RefineCodexActivation(&rebound, CodexActivationObservation{
		AgentUID: "agent-1", PaneUID: "pane-1", Generation: "generation-1", ThreadID: "thread-1", TurnID: "turn-2",
	})
	if err != nil || !changed {
		t.Fatalf("refine = %t/%v", changed, err)
	}
	if agent, _ := rebound.Agent("agent-1"); !agent.Status.Progress.IsZero() {
		t.Fatalf("turn rebind retained progress: %+v", agent.Status.Progress)
	}

	released := fixture()
	if _, err := mutator.ReleaseAgentPane(&released, "agent-1", AgentExitUnknown, "disconnected"); err != nil {
		t.Fatal(err)
	}
	if agent, _ := released.Agent("agent-1"); !agent.Status.Progress.IsZero() {
		t.Fatalf("release retained progress: %+v", agent.Status.Progress)
	}
}

func TestAgentProgressPersistedFieldInventoryIsContentFree(t *testing.T) {
	t.Parallel()
	typeOf := reflect.TypeFor[AgentProgress]()
	for i := 0; i < typeOf.NumField(); i++ {
		name := strings.ToLower(typeOf.Field(i).Name)
		for _, forbidden := range []string{"prompt", "reason", "step", "path", "command", "tool", "output", "diff", "hash", "model", "effort", "name", "content", "history"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("durable progress has forbidden field %q", typeOf.Field(i).Name)
			}
		}
	}
	raw, err := json.Marshal(AgentStatus{Progress: AgentProgress{
		TurnRef: "turn-1", Activity: ProgressOther, PlanTotal: 99, PlanTruncated: true,
		ChangedFiles: 999, FilesTruncated: true, ActiveItemCount: 32,
		ObservedAt: time.Unix(1, 0), Source: AgentProgressSource,
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"prompt", "reasoning", "command", "path", "output", "diff", "hash", "model", "effort", "history"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("persisted progress contains %q: %s", forbidden, text)
		}
	}
}
