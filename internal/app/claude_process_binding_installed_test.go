package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Opt-in installation qualification uses an isolated HOME, a copied test
// executable and localhost SSE. No real credentials or model API are used.
func TestInstalledProcessClaudeBinding(t *testing.T) {
	path := os.Getenv("PMX_TEST_REAL_CLAUDE_BIN")
	if path == "" {
		t.Skip("opt-in installed Claude binding qualification")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("installed Claude path must be absolute")
	}
	var calls atomic.Int32
	var hold atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return
		}
		index := calls.Add(1)
		if hold.Swap(false) {
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
				return
			case <-release:
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, data any) {
			raw, _ := json.Marshal(data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg_stub_%d", index), "type": "message", "role": "assistant", "content": []any{}, "model": "claude-haiku-4-5", "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})
		stop := "end_turn"
		if index == 1 || index == 2 || index == 3 {
			tool := "Bash"
			input := map[string]any{"command": "printf PROCESS_BINDING_STUB"}
			if index == 1 {
				tool = "AskUserQuestion"
				input = map[string]any{"questions": []any{map[string]any{"question": "Color?", "header": "Color", "options": []any{map[string]any{"label": "blue", "description": "Blue"}, map[string]any{"label": "red", "description": "Red"}}, "multiSelect": false}}}
			}
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("tool_stub_%d", index), "name": tool, "input": map[string]any{}}})
			raw, _ := json.Marshal(input)
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}})
			stop = "tool_use"
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "STUB_OK"}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	defer close(release)
	f := newProcessClaudeFixture(t, func(root, binary string) processhost.Command {
		var env []string
		for _, value := range os.Environ() {
			key, _, _ := strings.Cut(value, "=")
			if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE") || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "PMX_INTERNAL_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "XDG_") || key == "HOME" || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
				continue
			}
			env = append(env, value)
		}
		env = append(env, "HOME="+root, "CLAUDE_CONFIG_DIR="+filepath.Join(root, ".claude"), "ANTHROPIC_BASE_URL="+server.URL, "ANTHROPIC_API_KEY=isolated-dummy", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "XDG_RUNTIME_DIR="+root)
		hooks := map[string]any{}
		for event, route := range map[string]string{"SessionStart": "claude-endpoint-register", "PreToolUse": claudeQuestionHookRoute, "PermissionRequest": claudePermissionHookRoute} {
			entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `exec "$PMX_TEST_PROCESS_BINARY" internal ` + route, "timeout": 5}}}
			if event == "PreToolUse" {
				entry["matcher"] = "AskUserQuestion"
			}
			hooks[event] = []any{entry}
		}
		settings, _ := json.Marshal(map[string]any{"hooks": hooks, "permissions": map[string]any{"ask": []string{"AskUserQuestion", "Bash"}}})
		cmd, err := processhost.ClaudeCommand(path, root, env, []string{"--model", "haiku", "--permission-mode", "default", "--tools", "AskUserQuestion,Bash", "--setting-sources", "", "--settings", string(settings), "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence"})
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	})
	f.turn(t, "first", "offline scripted tools")
	first := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	if first.Pending[0].Kind != "question" {
		t.Fatalf("native question: %+v", first)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	questions, _ := f.control.questions.List(f.binding.Agent)
	if len(questions) != 1 {
		t.Fatalf("hook/stream question count=%d", len(questions))
	}
	if _, err := f.control.questions.Answer(questions[0].ID, f.binding.Agent, map[string]string{"Color?": "blue"}); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 && s.Pending[0].Kind == "permission" })
		if err := f.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		approvals, _ := f.control.approvals.List(f.binding.Agent)
		waiting := []agentapproval.Record{}
		for _, r := range approvals {
			if r.State == agentapproval.StateWaiting {
				waiting = append(waiting, r)
			}
		}
		if len(waiting) != 1 {
			t.Fatalf("hook/stream permission count=%d", len(waiting))
		}
		if _, err := f.control.approvals.Answer(waiting[0].ID, f.binding.Agent, allow, agentapproval.ViaCLI); err != nil {
			t.Fatal(err)
		}
		if err := f.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	proof := f.proof(t)
	if !checkClaudeProcessHost(proof, false) {
		t.Fatal("native process registration not exact")
	}
	f.turn(t, "second", "offline second turn")
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	hold.Store(true)
	f.turn(t, "interrupt", "offline held turn")
	select {
	case <-entered:
	case <-time.After(12 * time.Second):
		t.Fatal("stub did not hold")
	}
	s, _ := f.handle.Observe(f.binding)
	if err := f.handle.Interrupt(context.Background(), processhost.Authority{Binding: f.binding, Connection: s.Connection, Session: s.Session}, s.Turn); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	f.turn(t, "after-interrupt", "offline follow-up")
	s = f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if s.Exit != nil {
		t.Fatal("interrupt became process exit")
	}
	t.Logf("installed Claude session=%s PID=%d localhost requests=%d; question1/deny1/allow1, repeated init, interrupt and follow-up", proof.Session, s.PID, calls.Load())
}
