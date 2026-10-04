package app

import (
	"bufio"
	"bytes"
	"context"
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

	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestCodexProcessInstalledPublicCreate(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CODEX") != "1" {
		t.Skip("opt-in installed Codex qualification")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	f := newProcessCreateCLI(t)
	if err := os.WriteFile(filepath.Join(f.root, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(frame any) { raw, _ := json.Marshal(frame); _, _ = fmt.Fprintf(w, "data: %s\n\n", raw) }
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_stub", "status": "in_progress"}})
		emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "msg_stub", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "LOCAL_STUB_REPLY"}}}})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_stub", "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
	}))
	defer provider.Close()
	codexHome := filepath.Join(f.root, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	configuration := fmt.Sprintf("model = \"stub-model\"\nmodel_provider = \"stub\"\nmodel_reasoning_effort = \"low\"\n[model_providers.stub]\nname = \"stub\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\n", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "codex"), []byte("#!/bin/sh\nexec "+fmt.Sprintf("%q", path)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := f.args("--model", "stub-model", "--effort", "low")
	for i := range args {
		if args[i] == "--provider" {
			args[i+1] = aiModeCodex
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, args...)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatalf("ownership: %v %s", err, stderr.String())
	}
	ref := strings.Fields(line)[1]
	out, err := exec.CommandContext(ctx, f.binary, "agent", "turn", "start", ref, "--", "local model task").CombinedOutput()
	if err != nil {
		t.Fatalf("turn: %v %s", err, out)
	}
	waitCodexCreate(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		agent, ok := reg.Agent(strings.TrimPrefix(ref, "uid:"))
		if !ok {
			return false
		}
		pane, _ := reg.Pane(agent.Status.PaneRef)
		return calls.Load() == 1 && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.TurnID == ""
	})
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("actual Wait: %v %s", err, stderr.String())
	}
	reg, _ := f.store.LoadReadOnly()
	agent, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
	pane, _ := reg.Pane(agent.Status.PaneRef)
	if pane.Status.LastTermination == nil || !pane.Status.Activation.IsZero() || calls.Load() != 1 {
		t.Fatal("public installed Wait/model stub evidence missing")
	}
	t.Logf("copied CLI public create/turn/Wait0; localhost requests=%d isolated HOME=%s", calls.Load(), f.root)
}

// Qualification uses only an isolated HOME and a localhost model stub. Native
// question/approval contracts are separately covered by typed fixtures.
func TestCodexProcessInstalledBidirectionalReceipts(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CODEX") != "1" {
		t.Skip("opt-in installed Codex qualification")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "stub-model" {
			t.Errorf("stub input: %+v %v", input, err)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = fmt.Fprint(w, `{"error":{"code":"server_is_overloaded","message":"isolated stub failure","type":"server_error"}}`)
	}))
	defer provider.Close()
	command := func(root, binary string, env []string) processhost.Command {
		codexHome := filepath.Join(root, ".codex")
		if err := os.Mkdir(codexHome, 0700); err != nil {
			t.Fatal(err)
		}
		config := fmt.Sprintf("model = \"stub-model\"\nmodel_provider = \"stub\"\nmodel_reasoning_effort = \"low\"\n[model_providers.stub]\nname = \"stub\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\n", provider.URL+"/v1")
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		cmd, err := processhost.CodexCommand(path, root, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	left := newProcessCodexFixture(t, command)
	right := newProcessCodexFixture(t, command)
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	left.endpoint.messages.Store(m)
	right.endpoint.messages.Store(m)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	receipt, err := m.send(ctx, left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-native-first", "conversation-native", "offline coordination", now, deadline)
	if err != nil || receipt.Delivery.State != "delivered" {
		t.Fatalf("native send %+v %v", receipt, err)
	}
	right.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	receipt, err = m.reply(ctx, right.endpoint.binding.Agent, left.endpoint.binding.Agent, "message-native-first", "message-native-reply", "offline reply", time.Now().UTC(), deadline)
	if err != nil || receipt.Delivery.State != "delivered" {
		t.Fatalf("native reply %+v %v", receipt, err)
	}
	left.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if calls.Load() != 2 {
		t.Fatalf("stub calls %d", calls.Load())
	}
	for _, f := range []*processCodexFixture{left, right} {
		e := f.endpoint
		if err = e.handle.Stop(e.binding); err != nil {
			t.Fatal(err)
		}
		snap, err := e.handle.Wait(ctx, e.binding)
		if err != nil || snap.Exit == nil || snap.Exit.Code != 0 || snap.Exit.Signal != "" {
			t.Fatalf("native actual Wait %+v %v", snap, err)
		}
		t.Logf("isolated HOME=%s PID=%d host=%s generation=%s thread=%s connection=%s actual exit=%+v", f.root, snap.PID, e.binding.Host, e.binding.Generation, snap.Session, snap.Connection, snap.Exit)
	}
}
