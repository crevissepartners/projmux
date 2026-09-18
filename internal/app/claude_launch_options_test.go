package app

import (
	"slices"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestCreateClaudeAgentPassesModelAndEffort(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--model", "opus[1m]", "--effort", "xhigh",
		"--project", "alpha", "--window", "review", "--", "review this"); err != nil {
		t.Fatal(err)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].model != "opus[1m]" || launcher.plans[0].effort != "xhigh" {
		t.Fatalf("plans = %+v", launcher.plans)
	}
}

func TestCreateAgentRefusesModelAndEffortItCannotHonor(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"another provider": {"agent", "--provider", "codex", "--interactive-only", "--model", "gpt-6"},
		"unknown effort":   {"agent", "--provider", "claude", "--effort", "extreme"},
		"option as model":  {"agent", "--provider", "claude", "--model", "--dangerously-skip-permissions"},
		"model with space": {"agent", "--provider", "claude", "--model", "opus sonnet"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
			argv := append(args, "--project", "alpha", "--window", "review")
			_, _, err := runRoute(t, create, argv...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if len(launcher.plans) != 0 {
				t.Fatalf("a refused create planned a launch: %+v", launcher.plans)
			}
		})
	}
}

func TestClaudeLaunchOptionsPrecedeTheWorkspaceArguments(t *testing.T) {
	t.Parallel()
	cmd := agentLaunchArgvTestCommand(t)
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}
	_, argv, err := cmd.PlanAgentLaunchWithOptions(aiModeClaude, workspace, []string{"do the thing"}, "sonnet", "low", "")
	if err != nil {
		t.Fatal(err)
	}
	got := execArgvTail(t, argv, aiModeClaude)
	want := []string{"--model", "sonnet", "--effort", "low", "--add-dir", "/work/extra", "--", "do the thing"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv tail = %q, want %q", got, want)
	}
	if _, _, err := cmd.PlanAgentLaunchWithOptions(aiModeCodex, workspace, nil, "x", "", ""); err == nil {
		t.Fatal("codex accepted Claude launch options")
	}
}
