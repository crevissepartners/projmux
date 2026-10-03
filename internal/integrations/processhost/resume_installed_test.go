package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func installedResumeStub(t *testing.T, provider string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Error(err)
			}
			return // Installed Claude also sends empty transport warmups.
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event string, frame any) {
			raw, _ := json.Marshal(frame)
			if event != "" {
				_, _ = fmt.Fprintf(w, "event: %s\n", event)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		if provider == "codex" {
			emit("", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_stub", "status": "in_progress"}})
			emit("", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "REMEMBERED_STUB_REPLY"}}}})
			emit("", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_stub", "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
		} else {
			emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "content": []any{}, "model": "claude-haiku-4-5", "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "REMEMBERED_STUB_REPLY"}})
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
			emit("message_stop", map[string]any{"type": "message_stop"})
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, bodies...) }
}

func resumeInstalledEnv(root string) []string {
	// A fixed allowlist excludes live credentials, provider settings and hooks.
	env := []string{"HOME=" + root, "TMPDIR=" + os.TempDir(), "PATH=" + os.Getenv("PATH"), "GOMODCACHE=" + os.Getenv("GOMODCACHE"), "GOCACHE=" + os.Getenv("GOCACHE"), "GOTOOLCHAIN=local"}
	return env
}

func installedResumeHost(t *testing.T) *Host {
	return testHost(t, func(_ *Transactions, l *Limits) {
		l.Startup = 15 * time.Second
		l.Grace = time.Second
		l.Write = time.Second
	})
}

func assertNativeResumeContext(t *testing.T, bodies []string) {
	t.Helper()
	if len(bodies) != 2 {
		t.Fatalf("model calls=%d, wanted two", len(bodies))
	}
	for _, text := range []string{"T4B_CONTEXT_MARKER", "REMEMBERED_STUB_REPLY", "NEW_GENERATION_PROMPT"} {
		if !strings.Contains(bodies[1], text) {
			t.Fatalf("resumed model request lacks %s", text)
		}
	}
}

func TestInstalledResumeClaude(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CLAUDE") != "1" {
		t.Skip("opt-in localhost resume qualification")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	server, bodies := installedResumeStub(t, "claude")
	root := t.TempDir()
	env := append(resumeInstalledEnv(root), "CLAUDE_CONFIG_DIR="+filepath.Join(root, ".claude"), "ANTHROPIC_BASE_URL="+server.URL, "ANTHROPIC_API_KEY=isolated-dummy", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	cmd, err := ClaudeCommand(path, root, env, []string{"--model", "haiku", "--permission-mode", "manual", "--tools", "", "--setting-sources", "", "--settings", `{"hooks":{}}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`})
	if err != nil {
		t.Fatal(err)
	}
	h := installedResumeHost(t)
	old, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopResume(t, old) })
	turn(t, old, "first-turn", "T4B_CONTEXT_MARKER")
	s := waitResume(t, old, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	record := savedRecord(t, "claude", s)
	stopResume(t, old)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := h.ResumeClaude(ctx, Launch{Binding: resumedBinding(), Command: cmd}, record, "new-turn", "NEW_GENERATION_PROMPT")
	if p != nil {
		t.Cleanup(func() { stopResume(t, p) })
	}
	if err != nil {
		t.Fatal(err)
	}
	new := waitResume(t, p, func(s Snapshot) bool { return s.Turn == "" || s.Exit != nil })
	if new.Session != record.Session || new.Exit != nil || new.PID == s.PID {
		t.Fatalf("native resume=%+v", new)
	}
	assertNativeResumeContext(t, bodies())
	stopResume(t, p)
	bad := record
	bad.Session = "00000000-0000-0000-0000-000000000000"
	b := resumedBinding()
	b.Generation = "refused-generation"
	b.Operation = "refused-operation"
	failed, err := h.ResumeClaude(ctx, Launch{Binding: b, Command: cmd}, bad, "refused-turn", "missing-session")
	if !errors.Is(err, ErrResumeRefused) || failed == nil {
		t.Fatalf("missing-session=%v", err)
	}
	if s, _ := failed.Observe(b); s.Exit == nil || s.Session != "" {
		t.Fatalf("missing-session did not retire: %+v", s)
	}
	assertNativeResumeContext(t, bodies())
	t.Logf("HOME=%s sameSession=%s oldPID=%d newPID=%d stubCalls=%d missingSession=explicit-refusal", root, record.Session, s.PID, new.PID, len(bodies()))
}

func TestInstalledResumeCodex(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CODEX") != "1" {
		t.Skip("opt-in localhost resume qualification")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	server, bodies := installedResumeStub(t, "codex")
	root := t.TempDir()
	ch := filepath.Join(root, ".codex")
	if err := os.Mkdir(ch, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("model=\"stub-model\"\nmodel_provider=\"stub\"\nmodel_reasoning_effort=\"low\"\n[model_providers.stub]\nname=\"stub\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n", server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(ch, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	env := append(resumeInstalledEnv(root), "CODEX_HOME="+ch)
	cmd, err := CodexCommand(path, root, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := CodexConfig{Version: "0.1.0", Settings: codexappserver.ThreadSettings{Model: "stub-model", Effort: "low", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "never"}}}
	h := installedResumeHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	old, err := h.StartCodex(ctx, Launch{Binding: binding(), Command: cmd}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopResume(t, old.handle) })
	s, _ := old.Observe(binding())
	pre := savedRecord(t, "codex", s)
	// A separate owned host cannot load an unpersisted thread. No model call.
	probe := installedResumeHost(t)
	notDurable, err := probe.ResumeCodex(ctx, Launch{Binding: resumedBinding(), Command: cmd}, cfg, pre)
	if !errors.Is(err, ErrResumeRefused) || notDurable == nil || len(bodies()) != 0 {
		t.Fatalf("not-durable resume=%v calls=%d", err, len(bodies()))
	}
	if s, _ := notDurable.Observe(resumedBinding()); s.Exit == nil {
		t.Fatal("not-durable child retained")
	}
	if err := old.Turn(ctx, codexAuthority(old), "first-turn", "T4B_CONTEXT_MARKER"); err != nil {
		t.Fatal(err)
	}
	s = waitResume(t, old.handle, func(s Snapshot) bool { return s.Turn == "" || s.Exit != nil })
	if s.Exit != nil {
		t.Fatalf("first turn failed: %+v", s)
	}
	record := savedRecord(t, "codex", s)
	stopResume(t, old.handle)
	p, err := h.ResumeCodex(ctx, Launch{Binding: resumedBinding(), Command: cmd}, cfg, record)
	if p != nil {
		t.Cleanup(func() { stopResume(t, p.handle) })
	}
	if err != nil {
		t.Fatal(err)
	}
	new, _ := p.Observe(resumedBinding())
	a := Authority{Binding: resumedBinding(), Connection: new.Connection, Session: new.Session}
	if new.Session != record.Session || new.PID == s.PID {
		t.Fatalf("native resume=%+v", new)
	}
	if err := p.Turn(ctx, a, "new-turn", "NEW_GENERATION_PROMPT"); err != nil {
		t.Fatal(err)
	}
	waitResume(t, p.handle, func(s Snapshot) bool { return s.Turn == "" || s.Exit != nil })
	assertNativeResumeContext(t, bodies())
	stopResume(t, p.handle)
	bad := record
	bad.Session = "00000000-0000-0000-0000-000000000000"
	b := resumedBinding()
	b.Generation = "refused-generation"
	b.Operation = "refused-operation"
	failed, err := h.ResumeCodex(ctx, Launch{Binding: b, Command: cmd}, cfg, bad)
	if !errors.Is(err, ErrResumeRefused) || failed == nil {
		t.Fatalf("missing-session=%v", err)
	}
	if s, _ := failed.Observe(b); s.Exit == nil || s.Session != "" {
		t.Fatalf("missing-session did not retire: %+v", s)
	}
	assertNativeResumeContext(t, bodies())
	t.Logf("HOME=%s sameThread=%s oldPID=%d newPID=%d stubCalls=%d notDurable=refused missingSession=explicit-refusal", root, record.Session, s.PID, new.PID, len(bodies()))
}
