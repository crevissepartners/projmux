package processhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in qualification runs the installed CLI against localhost SSE
// in a disposable HOME. It uses no real credentials, model API or user hooks.
func TestInstalledClaudeStream(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CLAUDE") != "1" {
		t.Skip("opt-in installed provider qualification")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Logf("isolated provider HOME=%s", home)
	server := installedClaudeStub(t, os.Getenv("PROCESSHOST_TEST_PERMISSION") != "")
	defer server.Close()
	settings := filepath.Join(home, ".claude", "settings.json")
	before, readErr := os.ReadFile(settings)
	defer func() {
		after, err := os.ReadFile(settings)
		if (readErr == nil) != (err == nil) || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Error("isolated settings changed")
		}
	}()
	env := installedClaudeEnv(home, server.URL)
	tools := ""
	settingsArg := `{"hooks":{}}`
	probe := os.Getenv("PROCESSHOST_TEST_PERMISSION")
	if probe == "deny" || probe == "cancel" {
		tools = "Bash"
		// Restrict this isolated process to asking for every Bash call. The
		// probe never grants permission and does not edit user settings.
		settingsArg = `{"hooks":{},"permissions":{"ask":["Bash"]}}`
	}
	cmd, err := ClaudeCommand(path, t.TempDir(), env, []string{"--model", "haiku", "--permission-mode", "manual", "--tools", tools, "--setting-sources", "", "--settings", settingsArg, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence"})
	if err != nil {
		t.Fatal(err)
	}
	h := testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 30 * time.Second; l.Grace = 3 * time.Second })
	h.supervisor.Path = installedClaudeSupervisor(t, home)
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = p.Stop(binding())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s, err := p.Wait(ctx, binding())
		if err != nil || s.Exit == nil {
			t.Errorf("provider Wait: %+v %v", s, err)
		}
	}()
	if probe == "deny" || probe == "cancel" {
		turn(t, p, "permission", "Use the Bash tool exactly once to run: printf RUNTIME_PERMISSION_PROBE. Do not use other tools or read or write any files. If permission is denied, do not retry; reply DENIED.")
		deadline := time.Now().Add(45 * time.Second)
		var req Request
		for time.Now().Before(deadline) {
			s, _ := p.Observe(binding())
			if len(s.Pending) != 0 {
				req = s.Pending[0]
				break
			}
			if s.State != "starting" && s.State != "ready" {
				t.Fatalf("permission probe failed: %+v", s)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if req.Tool != "Bash" || req.Kind != "permission" {
			t.Fatalf("isolated permission request not observed: %+v", req)
		}
		t.Logf("observed exact Bash permission request %s on session %s", req.ID, req.Session)
		if probe == "deny" {
			if err := p.Respond(context.Background(), authority(p), req, Response{Deny: "Denied by isolated qualification; do not retry."}); err != nil {
				t.Fatal(err)
			}
		} else if err := p.Interrupt(context.Background(), authority(p), "permission"); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(45 * time.Second)
		completed := false
		for time.Now().Before(deadline) {
			s, _ := p.Observe(binding())
			if s.State != "starting" && s.State != "ready" {
				t.Fatalf("permission probe failed after %s: %+v", probe, s)
			}
			if s.Turn == "" {
				completed = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !completed {
			t.Fatal("permission probe did not complete")
		}
		if err := p.Respond(context.Background(), authority(p), req, Response{Allow: true}); err != ErrStale {
			t.Fatalf("old permission request wrote: %v", err)
		}
		if probe == "cancel" {
			events, _, _ := p.Events(binding(), 0)
			observed := false
			for _, event := range events {
				if event.Kind == "control-expired" && event.Request != nil && event.Request.ID == req.ID && bytes.Contains(event.Raw, []byte("control_cancel_request")) {
					observed = true
				}
			}
			if !observed {
				t.Fatal("actual provider cancellation frame not observed")
			}
		}
		t.Logf("permission %s completed; stale response refused", probe)
	}
	for _, id := range []string{"first", "second"} {
		turn(t, p, id, "Reply exactly STREAM_OK. Do not use any tools.")
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			s, _ := p.Observe(binding())
			if s.State == "ready" && s.Turn == "" {
				break
			}
			if s.State == "exited" || s.State == "unknown" || s.State == "stopping" {
				t.Fatalf("provider failed: %+v", s)
			}
			time.Sleep(10 * time.Millisecond)
		}
		s, _ := p.Observe(binding())
		if s.State != "ready" || s.Turn != "" {
			t.Fatalf("provider timeout: %+v", s)
		}
		t.Logf("%s turn completed; repeated init retained exact session; pid=%d", id, s.PID)
	}
}

// The fixture returns one tool request when asked, then bounded plain text.
func installedClaudeStub(t *testing.T, permission bool) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			return
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind string, data any) {
			raw, _ := json.Marshal(data)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "model": "claude-haiku-4-5", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
		stop := "end_turn"
		if permission && call == 1 {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "tool_stub", "name": "Bash", "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"printf RUNTIME_PERMISSION_PROBE"}`}})
			stop = "tool_use"
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "STREAM_OK"}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
}

func installedClaudeEnv(home, url string) []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "CLAUDE") || key == "TMUX" || key == "TMUX_PANE" || key == "CLAUDECODE" || strings.HasPrefix(key, "CLAUDE_CODE_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "PMX_") || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "HOME="+home, "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"), "ANTHROPIC_BASE_URL="+url, "ANTHROPIC_API_KEY=isolated-dummy", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_STATE_HOME="+filepath.Join(home, "state"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
	return env
}

func installedClaudeSupervisor(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "processhost.test")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
