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
	"testing"
	"time"
)

// The installed provider talks only to localhost with dummy credentials.
func TestInstalledProcessCreateClaudeActualCLI(t *testing.T) {
	provider := os.Getenv("PMX_TEST_REAL_CLAUDE_BIN")
	if provider == "" {
		t.Skip("opt-in installed Claude creation qualification")
	}
	if !filepath.IsAbs(provider) {
		t.Fatal("provider path must be absolute")
	}
	f := newProcessCreateCLI(t)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE") || strings.HasPrefix(key, "AWS_") {
			t.Setenv(key, "")
		}
	}
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.Error(w, "isolated", 404)
			return
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []struct {
			kind string
			data string
		}{
			{"message_start", `{"type":"message_start","message":{"id":"msg_local","type":"message","role":"assistant","content":[],"model":"claude-haiku-4-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"LOCAL_CREATE_STUB"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		for _, frame := range frames {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.kind, frame.data)
		}
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("ANTHROPIC_API_KEY", "isolated-dummy")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	if err := os.Remove(filepath.Join(f.root, "claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(provider, filepath.Join(f.root, "claude")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args("--model", "haiku", "--", "local task")...)
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
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "foreground=owned") {
		t.Fatalf("ownership %s", line)
	}
	select {
	case <-called:
	case <-ctx.Done():
		t.Fatal("localhost model was not reached")
	}
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("installed Wait %v %s", err, stderr.String())
	}
	remaining, _ := reader.ReadString('\x00')
	if strings.Contains(line+remaining, "LOCAL_CREATE_STUB") {
		t.Fatal("provider content reached stdout")
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
	if pane.Status.LastTermination == nil || !pane.Status.Activation.IsZero() {
		t.Fatal("installed Wait receipt absent")
	}
}
