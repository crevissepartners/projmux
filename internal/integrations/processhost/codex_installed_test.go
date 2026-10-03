package processhost

import (
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

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// This qualification uses no credentials or real model endpoint. CI runs the
// deterministic provider; an explicit opt-in qualifies the installed binary.
func TestInstalledCodexStdio(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CODEX") != "1" {
		t.Skip("opt-in installed Codex qualification")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var hold atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "stub-model" {
			t.Errorf("model request = %+v, %v", request, err)
		}
		if hold.Swap(false) {
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
				return
			case <-release:
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = fmt.Fprint(w, `{"error":{"code":"server_is_overloaded","message":"isolated stub failure","type":"server_error"}}`)
	}))
	defer provider.Close()
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("model = \"stub-model\"\nmodel_provider = \"stub\"\nmodel_reasoning_effort = \"low\"\n[model_providers.stub]\nname = \"stub\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\n", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "OPENAI_API_KEY=") || strings.HasPrefix(value, "CODEX_API_KEY=") || strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "CODEX_HOME=") || strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") || strings.HasPrefix(value, "__PROJMUX_RUNTIME_ANCHOR_PANE=") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "HOME="+home, "CODEX_HOME="+codexHome)
	cmd, err := CodexCommand(path, home, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := testHost(t, func(_ *Transactions, l *Limits) {
		l.Startup = 15 * time.Second
		l.Grace = 2 * time.Second
		l.Write = time.Second
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	handle, err := host.StartCodex(ctx, Launch{Binding: binding(), Command: cmd}, CodexConfig{Version: "0.1.0", Settings: codexappserver.ThreadSettings{Model: "stub-model", Effort: "low", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "never"}}})
	if handle != nil {
		t.Cleanup(func() {
			_ = handle.Stop(binding())
			wait, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			s, e := handle.Wait(wait, binding())
			t.Logf("installed Wait=%+v err=%v", s, e)
			if e != nil {
				t.Error(e)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	snap, err := handle.Observe(binding())
	if err != nil || snap.State != "ready" || snap.Session == "" {
		t.Fatalf("ready=%+v err=%v", snap, err)
	}
	a := Authority{Binding: binding(), Connection: snap.Connection, Session: snap.Session}
	for i := range 2 {
		if err := handle.Turn(ctx, a, fmt.Sprintf("operation-%d", i), "offline probe"); err != nil {
			t.Fatal(err)
		}
		s := observeUntil(t, handle.handle, func(s Snapshot) bool { return s.Turn == "" || s.Exit != nil })
		if s.Exit != nil || s.State != "ready" {
			t.Fatalf("turn result was process exit: %+v", s)
		}
	}
	hold.Store(true)
	if err := handle.Turn(ctx, a, "operation-interrupt", "hold offline probe"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	active, _ := handle.Observe(binding())
	if err := handle.Interrupt(ctx, a, active.Turn); err != nil {
		t.Fatal(err)
	}
	interrupted := observeUntil(t, handle.handle, func(s Snapshot) bool { return s.Turn == "" || s.Exit != nil })
	if interrupted.Exit != nil || !hasEvent(handle.handle, "interrupt-ack") {
		t.Fatalf("interrupt=%+v", interrupted)
	}
	if err := handle.Turn(ctx, a, "operation-after-interrupt", "offline follow-up"); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, handle.handle, func(s Snapshot) bool { return s.Turn == "" })
	if calls.Load() != 4 {
		t.Fatalf("stub calls=%d", calls.Load())
	}
	if err := handle.Stop(binding()); err != nil {
		t.Fatal(err)
	}
	s, err := handle.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.Exit.Code != 0 || s.Exit.Signal != "" {
		t.Fatalf("Wait=%+v err=%v", s, err)
	}
	t.Logf("isolated dedicated app-server PID=%d session=%s model=stub-model effort=low policy=read-only/never calls=%d exit=%+v", s.PID, s.Session, calls.Load(), s.Exit)
}
