package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestProcessCodexSteerRunningAndIdleActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied product binary required")
	}
	f := newProcessCodexCreateCLI(t)
	scriptPath := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), " elif method=='turn/interrupt':", ` elif method=='turn/steer':
  reply({'turnId':current})
  open(os.path.join(os.environ['HOME'],'steered-response.json'),'w').write(json.dumps({'turn':current,'answer':n['params']['input'][0]['text']}))
  notify('item/agentMessage/delta',{'threadId':'process-thread','turnId':current,'itemId':'answer','delta':n['params']['input'][0]['text']})
  complete()
 elif method=='turn/interrupt':`, 1)
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args()...)
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
		t.Fatalf("ownership: %v %s", err, stderr.String())
	}
	ref := strings.Fields(line)[1]
	cli := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
	}
	registry := func() []byte {
		t.Helper()
		reg, err := f.store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	refuseIdle := func() {
		t.Helper()
		before, wire := registry(), mustReadSteerWire(t, f.trace)
		out, err := cli("agent", "turn", "steer", ref, "--", "idle input")
		if err == nil || !bytes.Contains(out, []byte("agent turn start")) {
			t.Fatalf("idle refusal: %v %s", err, out)
		}
		if !bytes.Equal(before, registry()) || !bytes.Equal(wire, mustReadSteerWire(t, f.trace)) {
			t.Fatal("idle steer changed Registry or provider wire")
		}
	}
	refuseIdle()
	if out, err := cli("agent", "turn", "start", ref, "--", "hold"); err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	waitCodexCreate(t, ctx, func() bool {
		reg, _ := f.store.LoadDegradedReadOnly()
		a, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
		p, _ := reg.Pane(a.Status.PaneRef)
		return p.Status.ProcessSession.TurnID == "turn-1"
	})
	// Targeting another exact Agent cannot deliver to this owned host.
	if out, err := cli("agent", "turn", "steer", "uid:agent-foreign", "--", "foreign"); err == nil {
		t.Fatalf("foreign admitted: %s", out)
	}
	out, err := cli("agent", "turn", "steer", ref, "--", "same-turn-answer")
	want := "Steer current turn thread=\"process-thread\" turn=\"turn-1\" acceptance=provider delivery=unconfirmed\n"
	if err != nil || string(out) != want {
		t.Fatalf("steer: %v %q want %q", err, out, want)
	}
	waitCodexCreate(t, ctx, func() bool {
		response, _ := os.ReadFile(filepath.Join(f.root, "steered-response.json"))
		return bytes.Contains(response, []byte(`"turn": "turn-1"`)) && bytes.Contains(response, []byte("same-turn-answer"))
	})
	waitCodexCreate(t, ctx, func() bool {
		reg, _ := f.store.LoadDegradedReadOnly()
		a, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
		p, _ := reg.Pane(a.Status.PaneRef)
		return p.Status.ProcessSession.TurnID == ""
	})
	refuseIdle()
	// Preserve the endpoint but replace the Registry generation: old host refuses.
	_, err = f.store.Update(func(reg *coremetadata.Registry) error {
		a, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
		p, _ := reg.Pane(a.Status.PaneRef)
		p.Status.Activation.Generation = "replaced"
		p.Status.Activation.Process.Binding.Generation = "replaced"
		p.Status.ProcessSession.Binding.Generation = "replaced"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := mustReadSteerWire(t, f.trace)
	if out, err := cli("agent", "turn", "steer", ref, "--", "old-generation"); err == nil {
		t.Fatalf("stale admitted: %s", out)
	}
	if !bytes.Equal(before, mustReadSteerWire(t, f.trace)) {
		t.Fatal("stale wrote provider bytes")
	}
	starts, steers := 0, 0
	for line := range bytes.SplitSeq(before, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var frame struct {
			Method string
			Params struct {
				ExpectedTurnID string `json:"expectedTurnId"`
			}
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Method == "turn/start" {
			starts++
		}
		if frame.Method == "turn/steer" {
			steers++
			if frame.Params.ExpectedTurnID != "turn-1" {
				t.Fatalf("wrong turn: %s", line)
			}
		}
	}
	if starts != 1 || steers != 1 {
		t.Fatalf("start=%d steer=%d", starts, steers)
	}
	_ = input.Close()
	_ = cmd.Wait()
	waited = true
}

func mustReadSteerWire(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
