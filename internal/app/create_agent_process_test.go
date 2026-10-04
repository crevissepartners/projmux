package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessClaudeLaunchKeepsResolvedPolicyAndWorkspace(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := &aiCommand{homeDir: func() (string, error) { return home, nil }, readCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte(binary), nil }}
	t.Setenv("PATH", "")
	workspace := coremetadata.AgentWorkspace{CWD: home, AdditionalWritableRoots: []string{filepath.Join(home, "extra")}}
	opts := processClaudeLaunchOptions{Model: "model-one", Effort: "high", InstructionsFile: filepath.Join(home, "instructions"), SettingsFile: filepath.Join(home, "settings.json")}
	launch, err := command.PlanProcessClaudeCommand(workspace, opts)
	if err != nil {
		t.Fatal(err)
	}
	policy := append(claudeLaunchOptionArgs(opts.Model, opts.Effort, opts.InstructionsFile), claudeSettingsArgs(opts.SettingsFile)...)
	roots, err := providerLaunchArgs(aiModeClaude, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy = append(policy, roots...)
	if launch.Path != binary || launch.Dir != home || !reflect.DeepEqual(launch.Args[:len(policy)], policy) {
		t.Fatalf("resolved launch changed: %+v, policy=%q", launch, policy)
	}
	if !reflect.DeepEqual(launch.Args[len(policy):], []string{"--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--include-partial-messages", "--permission-prompt-tool", "stdio"}) {
		t.Fatalf("stream transport changed: %q", launch.Args)
	}
	for _, value := range launch.Env {
		if strings.HasPrefix(value, "PATH=") && strings.TrimPrefix(value, "PATH=") != filepath.Dir(binary)+string(os.PathListSeparator) {
			t.Fatalf("provider directory missing from PATH: %q", value)
		}
	}
}

func TestProcessCreateRequestRejectsAmbiguousTypedScope(t *testing.T) {
	for _, ref := range []selector.Ref{
		{Kind: coremetadata.KindPane, UID: "pane-one"},
		{Kind: coremetadata.KindProject, UID: "proj-one", Name: "one"},
		{Kind: coremetadata.KindProject, Name: "uid:proj-one"},
		{Kind: coremetadata.KindProject, Name: " "},
	} {
		if _, err := newProcessAgentCreateRequest(processAgentCreateOptions{Project: ref, Provider: aiModeClaude}); err == nil {
			t.Fatalf("accepted invalid typed scope: %+v", ref)
		}
	}
}

func TestProcessCreateRequestCopiesCallerOwnedLaunchValues(t *testing.T) {
	opts := processAgentCreateOptions{
		Project:  selector.Ref{Kind: coremetadata.KindProject, UID: "proj-one"},
		Provider: aiModeClaude, Creator: "uid:agent-creator",
		Payload: []string{"task"}, AddDirs: []string{"/extra"}, Labels: map[string]string{"role": "worker"},
	}
	request, err := newProcessAgentCreateRequest(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Payload[0], opts.AddDirs[0], opts.Labels["role"] = "changed", "/changed", "changed"
	got := request.options
	if got.Payload[0] != "task" || got.AddDirs[0] != "/extra" || got.Labels["role"] != "worker" || got.Creator != "agent-creator" {
		t.Fatalf("request retained mutable caller state: %+v", got)
	}
}

func TestProcessCreateRequestRequiresScopeAndUnambiguousInstructions(t *testing.T) {
	for _, opts := range []processAgentCreateOptions{
		{Provider: aiModeClaude},
		{Window: selector.Ref{Kind: coremetadata.KindWindow, UID: "win-one"}, Instructions: "one", Persona: "two"},
		{Window: selector.Ref{Kind: coremetadata.KindWindow, UID: "win-one"}, Creator: "a-name"},
	} {
		if _, err := newProcessAgentCreateRequest(opts); err == nil {
			t.Fatalf("accepted invalid request: %+v", opts)
		}
	}
}

func TestProcessCreateReservationHasNoTmuxOrLiveChildClaim(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	command, _ := newTestAgentCreateCommand(t, store, tmux)
	opts := processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, Name: "alpha"}, Window: selector.Ref{Kind: coremetadata.KindWindow, Name: "review"}, Provider: aiModeClaude, Name: "process-reserved"}
	project, window, err := resolveProcessCreateScope(store.registry, opts)
	if err != nil {
		t.Fatal(err)
	}
	plan := processAgentCreatePlan{project: project, window: window, workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}}
	result, err := command.reserveProcessAgent(context.Background(), plan, opts, "op-process-owned", "gen-process-owned")
	if err != nil {
		t.Fatal(err)
	}
	pane, ok := store.registry.Pane(result.Binding.Pane)
	agent, _ := store.registry.Agent(result.Binding.Agent)
	if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(result.Binding) || agent.Status.Phase != coremetadata.PhasePending {
		t.Fatal("reservation invented child/session authority")
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("process reservation called tmux: %v", tmux.calls)
	}
	changed := plan
	changed.project.Spec.Root = "/different"
	before := store.registry.Clone()
	if _, err := command.reserveProcessAgent(context.Background(), changed, opts, "op-foreign", "gen-foreign"); err == nil || !reflect.DeepEqual(store.registry, before) {
		t.Fatal("changed preparation scope committed")
	}
}

func TestProcessSnapshotUnchangedTicksDoNotOpenTransactions(t *testing.T) {
	transactions, answers := 0, 0
	syncSnapshot := processSnapshotSynchronizer(func(processhost.Snapshot) error { transactions++; return nil }, func(processhost.Snapshot) error { answers++; return nil })
	snapshot := processhost.Snapshot{State: "ready", Sequence: 1}
	if err := syncSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	transactions = 0
	for range 20 {
		if err := syncSnapshot(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if transactions != 0 || answers != 20 {
		t.Fatalf("unchanged ticks: transactions=%d answers=%d", transactions, answers)
	}
	snapshot.Sequence++
	if err := syncSnapshot(snapshot); err != nil || transactions != 1 {
		t.Fatalf("changed snapshot: %v transactions=%d", err, transactions)
	}
}
