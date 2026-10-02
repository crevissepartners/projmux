package processhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in qualification uses real provider authentication, no user hooks,
// no MCP servers and no tools. CI's deterministic tests require no credentials.
func TestInstalledClaudeStream(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CLAUDE") != "1" {
		t.Skip("opt-in installed provider qualification")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	before, readErr := os.ReadFile(settings)
	defer func() {
		after, err := os.ReadFile(settings)
		if (readErr == nil) != (err == nil) || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Error("user settings changed")
		}
	}()
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "CLAUDECODE" || strings.HasPrefix(key, "CLAUDE_CODE_") || strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "PMX_") || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" {
			continue
		}
		env = append(env, value)
	}
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
