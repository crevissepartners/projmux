package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

type processCodexCreateCLI struct{ processCreateCLI }

func newProcessCodexCreateCLI(t *testing.T) processCodexCreateCLI {
	t.Helper()
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := newProcessCreateCLI(t)
	// Mixed attention retains tmux read errors. This empty read fixture keeps
	// the process projection observable without a real or live tmux server.
	if err := os.WriteFile(filepath.Join(f.root, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(f.root, ".codex"))
	script := "import signal\nsignal.signal(signal.SIGTERM,lambda *_: sys.exit(0))\n"
	// Install the handler after importing sys. No provider/API credentials or
	// ambient daemon are used by this exact executable protocol fixture.
	script = strings.Replace(processCodexProviderFixture, "import os,sys,json", "import os,sys,json\n"+script, 1)
	script = strings.Replace(script, "def complete():notify", "def complete():open(os.path.join(os.environ['HOME'],'completed'),'w').write(current);notify", 1)
	if err := os.WriteFile(filepath.Join(f.root, "codex-provider.py"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "codex"), []byte("#!/bin/sh\nexec python3 -u "+fmt.Sprintf("%q", filepath.Join(f.root, "codex-provider.py"))+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return processCodexCreateCLI{f}
}

func (f processCodexCreateCLI) args(extra ...string) []string {
	args := f.processCreateCLI.args()
	for i := range args {
		if args[i] == "--provider" {
			args[i+1] = "codex"
		}
	}
	args = append(args, "--model", "stub-model", "--effort", "low")
	return append(args, extra...)
}

func waitCodexCreate(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("Codex create condition timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestProcessCodexCreateTurnsAndBlockingControlsActualCLI(t *testing.T) {
	for _, prompt := range []bool{false, true} {
		t.Run(fmt.Sprint(prompt), func(t *testing.T) {
			f := newProcessCodexCreateCLI(t)
			paths, _ := config.DefaultPathsFromEnv()
			if err := os.MkdirAll(paths.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			policy := "claude\n"
			if prompt {
				policy = "projmux\n"
			}
			if err := os.WriteFile(paths.AgentQuestionAnsweringFile(), []byte(policy), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			args := f.args()
			if prompt {
				args = append(args, "--", "plain initial")
			}
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
			fields := strings.Fields(line)
			if len(fields) != 6 || fields[0] != "agent" || fields[2] != "pane" || fields[4] != "runtime=process" || fields[5] != "foreground=owned" {
				t.Fatalf("ownership bytes: %q", line)
			}
			if line != fmt.Sprintf("agent %s pane %s runtime=process foreground=owned\n", fields[1], fields[3]) {
				t.Fatalf("default ownership bytes: %q", line)
			}
			ref := fields[1]
			uid := strings.TrimPrefix(ref, "uid:")
			cli := func(args ...string) []byte {
				t.Helper()
				out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("CLI %q: %v %s", args, err, out)
				}
				return out
			}
			if !prompt {
				cli("agent", "turn", "start", ref, "--", "plain remote")
			}
			waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(filepath.Join(f.root, "completed")); return err == nil })
			raw, _ := os.ReadFile(f.trace)
			if !bytes.Contains(raw, []byte("plain ")) || bytes.Contains(raw, []byte("projmux-coordination")) {
				t.Fatalf("operator wire: %s", raw)
			}
			cli("agent", "turn", "start", ref, "--", "controls")
			qs := agentquestion.NewStore(paths.StateDir)
			as := agentapproval.NewStore(paths.StateDir)
			var questions []agentquestion.Record
			var approvals []agentapproval.Record
			waitCodexCreate(t, ctx, func() bool {
				questions, _ = qs.List(uid)
				approvals, _ = as.List(uid)
				return len(questions) == 1 && len(approvals) == 1
			})
			if out := cli("agent", "question", "list", ref, "-o", "json"); !bytes.Contains(out, []byte(questions[0].ID)) {
				t.Fatalf("question list: %s", out)
			}
			if out := cli("agent", "approval", "list", ref, "-o", "json"); !bytes.Contains(out, []byte(approvals[0].ID)) {
				t.Fatalf("approval list: %s", out)
			}
			if out := cli("attention", "list", "--json"); !bytes.Contains(out, []byte(strings.TrimPrefix(fields[3], "uid:"))) {
				t.Fatalf("attention: %s", out)
			}
			if out := cli("get", "notifications", "--live", "--json"); !bytes.Contains(out, []byte("Input required")) || !bytes.Contains(out, []byte("Approval required")) {
				t.Fatalf("notifications: %s", out)
			}
			cli("agent", "question", "answer", ref, questions[0].ID, "--option", "1=blue")
			cli("agent", "approval", "answer", ref, approvals[0].ID, "--deny", "--via", "cli")
			waitCodexCreate(t, ctx, func() bool { raw, _ := os.ReadFile(f.trace); return bytes.Count(raw, []byte(`"result"`)) == 2 })
			_ = input.Close()
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("normal owned exit: %v %s", err, stderr.String())
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(uid)
			pane, _ := reg.Pane(agent.Status.PaneRef)
			if !pane.Status.Activation.IsZero() || pane.Status.LastTermination == nil || pane.Status.ProcessSession.ThreadID != "process-thread" {
				t.Fatal("Codex Wait/thread record lost")
			}
		})
	}
}

func TestProcessCodexCreateInitializationRollbackActualCLI(t *testing.T) {
	f := newProcessCodexCreateCLI(t)
	before, _ := f.store.LoadReadOnly()
	path := filepath.Join(f.root, "codex-provider.py")
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte("if method=='initialize':reply({'userAgent':'fixture/0.160.0'})"), []byte("if method=='initialize':emit({'id':n['id'],'error':{'code':-32603,'message':'fixture init refusal'}})"), 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.binary, f.args()...).CombinedOutput()
	reg, readErr := f.store.LoadReadOnly()
	if err == nil || readErr != nil || len(reg.Agents) != 0 || len(reg.Panes) != len(before.Panes) || !bytes.Contains(out, []byte("remaining: none")) {
		t.Fatalf("initialization rollback: %v %v %s", err, readErr, out)
	}
}

func TestProcessCodexCreatePeerMessageActualCLI(t *testing.T) {
	f := newProcessCodexCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	start := func(name string) (string, io.WriteCloser) {
		t.Helper()
		cmd := exec.CommandContext(ctx, f.binary, f.args("--name", name)...)
		input, _ := cmd.StdinPipe()
		output, _ := cmd.StdoutPipe()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = input.Close()
			if err := cmd.Wait(); err != nil {
				t.Errorf("owned exit: %v %s", err, stderr.String())
			}
		})
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil {
			t.Fatalf("ownership: %v %s", err, stderr.String())
		}
		return strings.Fields(line)[1], input
	}
	source, sourceInput := start("source")
	target, _ := start("target")
	out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target, "--source", source, "--", "bounded peer payload").CombinedOutput()
	if err != nil {
		t.Fatalf("peer message: %v %s", err, out)
	}
	messageRef := strings.Split(string(out), "\t")[0]
	retry, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target, "--source", source, "--message-ref", messageRef, "--", "bounded peer payload").CombinedOutput()
	if err != nil || !bytes.Equal(retry, out) {
		t.Fatalf("peer receipt retry: %v %s", err, retry)
	}
	waitCodexCreate(t, ctx, func() bool {
		raw, _ := os.ReadFile(f.trace)
		return bytes.Contains(raw, []byte("bounded peer payload"))
	})
	raw, _ := os.ReadFile(f.trace)
	if !bytes.Contains(raw, []byte("untrusted-coordination-only")) || !bytes.Contains(raw, []byte("projmux-coordination")) || !bytes.Contains(raw, []byte("sourceNotice")) {
		t.Fatalf("peer envelope: %s", raw)
	}
	// A replay of the accepted reference is a receipt read, never a second turn.
	if bytes.Count(raw, []byte(`"method":"turn/start"`))+bytes.Count(raw, []byte(`"method": "turn/start"`)) != 1 {
		t.Fatalf("peer replay: %s", raw)
	}
	_ = sourceInput.Close()
	waitCodexCreate(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		agent, _ := reg.Agent(strings.TrimPrefix(source, "uid:"))
		return agent.Status.Phase == "Offline"
	})
	refused, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target, "--source", source, "--", "offline source must refuse").CombinedOutput()
	if err == nil {
		t.Fatalf("offline source admitted: %s", refused)
	}
}

func TestProcessCodexCreateHooksAndOutputActualCLI(t *testing.T) {
	for _, mode := range []string{"uid", "name", "ref", "metadata", "json", "receipt", "none", "pane-id"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcessCodexCreateCLI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, f.args("-o", mode)...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			reg, readErr := f.store.LoadReadOnly()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "pane-id" {
				status, ok := err.(*exec.ExitError)
				if !ok || status.ExitCode() != 2 || len(reg.Agents) != 0 {
					t.Fatalf("usage mutation: %v %s", err, stderr.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("projection: %v %s", err, stderr.String())
			}
			if mode == "none" {
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatal("none emitted output")
				}
				return
			}
			if strings.Contains(stdout.String(), "foreground=owned") || !strings.Contains(stderr.String(), "foreground=owned") {
				t.Fatal("ownership stream changed")
			}
			if mode == "uid" && stdout.String() != reg.Agents[0].Metadata.UID+"\n" {
				t.Fatalf("uid bytes %q", stdout.String())
			}
			if mode == "name" && stdout.String() != "owned\n" {
				t.Fatalf("name bytes %q", stdout.String())
			}
			agent := reg.Agents[0]
			normalized := strings.NewReplacer(agent.Metadata.UID, "AGENT", agent.Status.PaneRef, "PANE", f.project, "PROJECT", f.window, "WINDOW", f.root, "ROOT").Replace(stdout.String())
			normalized = regexp.MustCompile(`\d{4}-\d\d-\d\dT[^" ]+Z`).ReplaceAllString(normalized, "TIME")
			want := map[string]string{"uid": "AGENT\n", "name": "owned\n", "ref": "agent/owned\n"}[mode]
			if want == "" {
				raw, err := os.ReadFile(filepath.Join("testdata", "process-codex-create."+mode+".golden"))
				if err != nil {
					t.Fatal(err)
				}
				want = string(raw)
			}
			if normalized != want {
				t.Fatalf("projection %s bytes changed:\n%s", mode, normalized)
			}
			ownership := fmt.Sprintf("agent uid:%s pane uid:%s runtime=process foreground=owned\n", agent.Metadata.UID, agent.Status.PaneRef)
			if stderr.String() != ownership {
				t.Fatalf("ownership bytes: %q", stderr.String())
			}
		})
	}
	for _, hook := range []string{"undeclared", "declared", "failed"} {
		t.Run(hook, func(t *testing.T) {
			f := newProcessCodexCreateCLI(t)
			text := "[hooks.post-create]\nrun = \"exit 7\"\n"
			if hook == "declared" {
				text = "[hooks.post-create]\nruntime = \"process\"\nrun = \"echo $PROJMUX_RUNTIME:${PROJMUX_PANE-absent}:${TMUX-absent}\"\n"
			}
			if hook == "failed" {
				text = "[hooks.post-create]\nruntime = \"process\"\nrun = \"exit 7\"\n"
			}
			f.config(t, text)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, f.args()...).CombinedOutput()
			if hook == "failed" {
				reg, _ := f.store.LoadReadOnly()
				if err == nil || len(reg.Agents) != 0 || !bytes.Contains(out, []byte("process-post-create-hook-failed")) || !bytes.Contains(out, []byte("remaining: none")) {
					t.Fatalf("hook rollback %v %s", err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("hook: %v %s", err, out)
			}
			if hook == "declared" && !bytes.Contains(out, []byte("process:absent:absent")) {
				t.Fatalf("hook env %s", out)
			}
		})
	}
}

func TestProcessCodexCreateActualWaitSignalsCLI(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			f := newProcessCodexCreateCLI(t)
			path := filepath.Join(f.root, "codex-provider.py")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, []byte("signal.signal(signal.SIGTERM,lambda *_: sys.exit(0))"), []byte("signal.signal(signal.SIGTERM,signal.SIG_DFL)"), 1)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				t.Fatalf("ownership: %v %s", err, stderr.String())
			}
			reg, _ := f.store.LoadReadOnly()
			agent, _ := reg.Agent(strings.TrimPrefix(strings.Fields(line)[1], "uid:"))
			pane, _ := reg.Pane(agent.Status.PaneRef)
			// Signal the owned provider, so the CLI must propagate its actual Wait
			// rather than treating a signal on the owner as provider exit evidence.
			if err = syscall.Kill(pane.Status.Activation.Process.Child.PID, signal); err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, output)
			err = cmd.Wait()
			waited = true
			want := 128 + int(signal)
			if cmd.ProcessState.ExitCode() != want {
				t.Fatalf("Wait code=%d want=%d: %v %s", cmd.ProcessState.ExitCode(), want, err, stderr.String())
			}
		})
	}
}

func TestProcessCodexCreateUnsupportedProviderActualCLI(t *testing.T) {
	f := newProcessCodexCreateCLI(t)
	args := f.args()
	for i := range args {
		if args[i] == "--provider" {
			args[i+1] = "antigravity"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("unsupported provider exit: %v, stderr=%q", err, stderr.String())
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "process-codex-create.unsupported.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !bytes.Equal(stderr.Bytes(), golden) {
		t.Fatalf("unsupported output: stdout=%q stderr=%q want=%q", stdout.String(), stderr.String(), golden)
	}
}
