package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestRenderAgentProgressWidthAndLocaleMatrix(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	progress := coremetadata.AgentProgress{
		TurnRef: "turn-1", Activity: coremetadata.ProgressCommand,
		PlanCompleted: 2, PlanInProgress: 1, PlanTotal: 4,
		ChangedFiles: 3, ActiveItemCount: 2, StartedAt: started,
		ObservedAt: started, Source: coremetadata.AgentProgressSource,
	}
	for _, test := range []struct {
		locale i18n.Locale
		prefix string
	}{
		{locale: i18n.FallbackLocale, prefix: "Working · plan 2/4 · files 3 · items 2 · command · 01:23"},
		{locale: i18n.Locale("ko-KR"), prefix: "작업 중 · 계획 2/4 · 파일 3 · 항목 2 · command · 01:23"},
	} {
		for _, width := range []int{80, 100, 120} {
			got := renderAgentProgress(progress, started.Add(83*time.Second), test.locale, width)
			if !strings.HasPrefix(test.prefix, got) {
				t.Fatalf("%s/%d = %q, want deterministic prefix of %q", test.locale, width, got, test.prefix)
			}
			if cells := i18n.TerminalCellWidth(got); cells > width {
				t.Fatalf("%s/%d uses %d cells: %q", test.locale, width, cells, got)
			}
		}
	}
	if got := renderAgentProgress(coremetadata.AgentProgress{}, started, i18n.FallbackLocale, 80); got != "" {
		t.Fatalf("unavailable progress = %q, want no UI", got)
	}
}

func TestCodexProgressSinkProjectsBoundedRegistryAndClearsOnTerminal(t *testing.T) {
	store := newFakeResourceStore(t)
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, "pan-alpha-codex", coremetadata.PaneActivationOptions{
		Generation: "generation-1", RuntimeID: "%7", AgentUID: "agt-alpha-codex", OperationID: "phase7-test",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Generation: "generation-1", ThreadID: "thread-1", TurnID: "turn-1",
	})
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = store.store().load
	cmd.updateRegistry = store.store().update
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) == 5 && args[0] == "show-options" && args[4] == tmuxopts.PaneUID {
			return []byte("pan-alpha-codex\n"), nil
		}
		if name == "tmux" && len(args) == 5 && args[4] == "#{@projmux_pane_uid}" {
			return []byte("pan-alpha-codex\n"), nil
		}
		return nil, os.ErrNotExist
	}
	identity := codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
	progress := coremetadata.AgentProgress{
		TurnRef: "turn-2", Activity: coremetadata.ProgressFileChange, PlanCompleted: 1, PlanTotal: 2,
		ChangedFiles: 3, ActiveItemCount: 1, StartedAt: time.Unix(1, 0).UTC(), ObservedAt: time.Unix(2, 0).UTC(),
		Source: coremetadata.AgentProgressSource,
	}
	if err := testCodexLifecycleSink(cmd).ApplyProgress(identity, progress, agentprogress.Diagnostics{Dropped: 1, Unknown: 2, Overflow: 3}); err != nil {
		t.Fatal(err)
	}
	agent, _ := store.registry.Agent(identity.AgentUID)
	if agent.Status.Progress != progress {
		t.Fatalf("durable progress = %+v, want %+v", agent.Status.Progress, progress)
	}
	pane, _ := store.registry.Pane(identity.PaneUID)
	if pane.Status.Activation.Codex.TurnID != "turn-2" {
		t.Fatalf("activation turn = %q", pane.Status.Activation.Codex.TurnID)
	}
	if err := testCodexLifecycleSink(cmd).Apply(identity, codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionIdle, ClearProgress: true,
	}); err != nil {
		t.Fatal(err)
	}
	agent, _ = store.registry.Agent(identity.AgentUID)
	if !agent.Status.Progress.IsZero() {
		t.Fatalf("terminal retained progress: %+v", agent.Status.Progress)
	}
}

func TestDescribeAndResourceProjectionContainOnlyBoundedProgress(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 1, 23, 0, time.UTC)
	progress := coremetadata.AgentProgress{
		TurnRef: "turn-1", Activity: coremetadata.ProgressTool, PlanCompleted: 2, PlanTotal: 4,
		ChangedFiles: 3, StartedAt: now.Add(-83 * time.Second), ObservedAt: now,
		Source: coremetadata.AgentProgressSource,
	}
	rows := describeSpecRows(coremetadata.Agent{Spec: coremetadata.AgentSpec{Provider: "codex"}, Status: coremetadata.AgentStatus{
		Phase: coremetadata.PhaseRunning, PaneRef: "pane-1", Progress: progress,
	}})
	var joined strings.Builder
	for _, row := range rows {
		joined.WriteString(row[0] + "=" + row[1] + "\n")
	}
	if !strings.Contains(joined.String(), "Progress=Working · plan 2/4 · files 3 · tool") {
		t.Fatalf("describe rows = %q", joined.String())
	}
	for _, forbidden := range []string{"prompt", "reasoning", "/repo/private", "command output", "diff --git"} {
		if strings.Contains(strings.ToLower(joined.String()), forbidden) {
			t.Fatalf("describe leaked %q: %s", forbidden, joined.String())
		}
	}
	row := registryview.Row{Kind: registryview.RowKindAgent, Progress: progress}
	if got := registryNavigationProgress(row, i18n.FallbackLocale, now); got != "Working · plan 2/4 · files 3 · tool · 01:23" {
		t.Fatalf("resource projection = %q", got)
	}
}
