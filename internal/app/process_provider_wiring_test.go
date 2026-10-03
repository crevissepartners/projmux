package app

import (
	"context"
	"encoding/json"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"slices"
	"testing"
)

func TestClaudeProcessControlIDRequestReuse(t *testing.T) {
	b := processhost.Binding{Host: "h", Agent: "a", Pane: "p", Generation: "g", Operation: "o"}
	r := processhost.Request{ID: "reused", Connection: "connection", Turn: "first", Kind: "question"}
	first := processClaudeControlID(b, r)
	if first != processCodexControlID(b, r) {
		t.Fatal("providers disagree on control identity")
	}
	r.Turn = "second"
	if first == processClaudeControlID(b, r) || first == processCodexControlID(b, r) {
		t.Fatal("new turn reused an earlier store answer")
	}
}

func TestClaudeProcessResolvedSettingsParity(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	_, argv, err := planner.PlanAgentLaunchWithSettings(aiModeClaude, coremetadata.AgentWorkspace{CWD: t.TempDir()}, nil, "haiku", "low", "", "/profile/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	resolved := execArgvTail(t, argv, aiModeClaude)
	command, err := processhost.ClaudeCommand("/bin/claude", "/workspace", []string{}, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(command.Args[:len(resolved)], resolved) {
		t.Fatal("process transport changed resolved profile settings")
	}
	for _, arg := range command.Args {
		if arg == "--setting-sources" {
			t.Fatal("process transport narrowed inherited setting sources")
		}
	}
}

// The protocol fixture deliberately reuses numeric and string request IDs in
// every turn. A later question/approval must await its own store answer.
func TestCodexProcessReusedRequestsAwaitNewAnswers(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	f.turn(t, "first", "controls")
	f.answerControls(t)
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.turn(t, "second", "controls")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, request := range snap.Pending {
		id := processCodexControlID(f.endpoint.binding, request)
		if request.Kind == "question" {
			record, found, err := f.control.questions.Get(id)
			if err != nil || !found || record.State != agentquestion.StateWaiting {
				t.Fatalf("new question reused an answer: %+v %v", record, err)
			}
		} else {
			record, found, err := f.control.approvals.Get(id)
			if err != nil || !found || record.State != agentapproval.StateWaiting {
				t.Fatalf("new approval reused an answer: %+v %v", record, err)
			}
		}
	}
	writes := 0
	for _, n := range f.wire(t) {
		if len(n["result"]) > 0 {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("old answers reached new turn: responses=%d", writes)
	}
}

func TestClaudeProcessProviderLaunchEnvDropsInheritedAuthority(t *testing.T) {
	original := []string{"HOME=/isolated", "PATH=/bin", "CUSTOM=value", "PMX_INTERNAL_OLD=stale", "TMUX=stale", "TMUX_PANE=%1", "__PROJMUX_RUNTIME_ANCHOR_PANE=%2"}
	launch := processhost.Launch{Binding: processhost.Binding{Host: "host", Pane: "pane", Generation: "gen", Operation: "op"}, Command: processhost.Command{Env: slices.Clone(original)}}
	for provider, env := range map[string][]string{"claude": processClaudeLaunchEnv(launch, "/registry", "/socket"), "codex": processCodexLaunchEnv(launch, "/socket")} {
		if !slices.Equal(env[:3], original[:3]) {
			t.Fatalf("%s changed unrelated env: %v", provider, env)
		}
		for _, value := range env {
			if slices.Contains(original[3:], value) {
				t.Fatalf("%s retained inherited authority: %s", provider, value)
			}
		}
		key := internalClaudeProcessBindingEnv
		hostKey := internalClaudeProcessHostEnv
		if provider == "codex" {
			key = internalCodexProcessBindingEnv
			hostKey = internalCodexProcessHostEnv
		}
		raw, _ := json.Marshal(launch.Binding)
		if !slices.Contains(env, key+"="+string(raw)) || !slices.Contains(env, hostKey+"=/socket") {
			t.Fatalf("%s missing exact launch: %v", provider, env)
		}
	}
	if !slices.Equal(original, launch.Command.Env) {
		t.Fatal("launch environment mutated")
	}
}
