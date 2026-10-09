package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

type processResumeCLIInvocation struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	stderr *bytes.Buffer
	ref    string
	done   bool
}

func startResumeCLIInvocation(t *testing.T, ctx context.Context, f processCreateCLI, args []string) *processResumeCLIInvocation {
	t.Helper()
	run := &processResumeCLIInvocation{cmd: exec.CommandContext(ctx, f.binary, args...), stderr: &bytes.Buffer{}}
	run.cmd.Stderr = run.stderr
	var err error
	run.input, err = run.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := run.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = run.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run.input.Close()
		if !run.done {
			_ = run.cmd.Process.Kill()
			_ = run.cmd.Wait()
		}
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatalf("ownership: %v %s", err, run.stderr.String())
	}
	fields := strings.Fields(line)
	if len(fields) != 6 || !strings.Contains(line, "runtime=process foreground=owned") {
		t.Fatalf("ownership %q", line)
	}
	run.ref = fields[1]
	return run
}
func (r *processResumeCLIInvocation) shutdown(t *testing.T) {
	t.Helper()
	_ = r.input.Close()
	err := r.cmd.Wait()
	r.done = true
	if err != nil {
		t.Fatalf("owner Wait: %v %s", err, r.stderr.String())
	}
}
func awaitProcessResumeRecord(t *testing.T, ctx context.Context, f processCreateCLI, ref string, accept func(*coremetadata.ProcessSessionRecord) bool) coremetadata.ProcessSessionRecord {
	t.Helper()
	for {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		pane, _ := processResumePane(reg, strings.TrimPrefix(ref, "uid:"))
		if pane != nil && pane.Status.ProcessSession != nil && accept(pane.Status.ProcessSession) {
			return *pane.Status.ProcessSession.Clone()
		}
		select {
		case <-ctx.Done():
			t.Fatal("process session predicate timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func assertProcessCLIForeground(t *testing.T, f processCreateCLI, record coremetadata.ProcessSessionRecord) {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, ok := reg.Pane(record.Binding.PaneUID)
	if !ok || pane.Status.Activation.Process == nil {
		t.Fatal("no live CLI owner")
	}
	view, ok := observeProcessHostOwner(reg, f.store.Path(), *pane)
	if !ok || view.OwnerMode != processHostOwnerForeground {
		t.Fatalf("public CLI owner observation: ok=%v, view=%+v", ok, view)
	}
}

func processResumeCLIFixture(t *testing.T, provider string) processCreateCLI {
	t.Helper()
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	var f processCreateCLI
	if provider == aiModeCodex {
		f = newProcessCodexCreateCLI(t).processCreateCLI
	} else {
		f = newProcessCreateCLI(t)
	}
	if provider == aiModeClaude {
		path := filepath.Join(f.root, "provider.py")
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := strings.Replace(string(script), "s=socket.socket(socket.AF_UNIX);s.bind(path)", "if os.path.exists(path):os.unlink(path)\ns=socket.socket(socket.AF_UNIX);s.bind(path)", 1)
		source = strings.Replace(source, "path=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'provider.sock')", "open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'argv.jsonl'),'a').write(json.dumps(sys.argv)+'\\n')\npath=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'provider.sock')", 1)
		if err = os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(f.root, "claude"), []byte("#!/bin/sh\n"+processFixtureExports(f.root)+"exec python3 -u "+fmt.Sprintf("%q", path)+" \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if provider == aiModeCodex {
		raw, err := os.ReadFile(filepath.Join(f.root, "codex-provider.py"))
		if err != nil {
			t.Fatal(err)
		}
		script := strings.Replace(string(raw), "elif method=='thread/start':", "elif method in ('thread/start','thread/resume'):", 1)
		script = strings.Replace(script, "p['model']", "p.get('model') or 'stub-model'", 1)
		script = strings.Replace(script, "p['config']['model_reasoning_effort']", "p.get('config',{}).get('model_reasoning_effort','low')", 1)
		script = "import sys\nif '--version' in sys.argv: print('codex-cli 0.160.0');sys.exit(0)\n" + script
		path := filepath.Join(f.root, "codex-provider.py")
		if err := os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "codex"), []byte("#!/bin/sh\nexec python3 -u "+fmt.Sprintf("%q", path)+" \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestCodexResumeAttentionConflictRefusesBeforeProviderActualCLI(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%t", conflict), func(t *testing.T) {
			f := processResumeCLIFixture(t, aiModeCodex)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(f.store.Path())))
			if !conflict {
				second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref})
				current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.Binding.Generation != old.Binding.Generation })
				if current.ThreadID != old.ThreadID || second.ref != first.ref {
					t.Fatal("matching attention lost the recorded conversation")
				}
				second.shutdown(t)
				return
			}
			if err := attention.update(func(records map[string]processAttentionRecord) error {
				r := records[old.Binding.PaneUID]
				r.Binding.Generation = "gen-conflicting-attention"
				r.Binding.Operation = "op-conflicting-attention"
				records[old.Binding.PaneUID] = r
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Even a provider version probe would create this marker. The
			// conflict must refuse before launch preparation, not just RPC.
			marker := filepath.Join(f.root, "unexpected-provider")
			if err := os.WriteFile(filepath.Join(f.root, "codex"), []byte("#!/bin/sh\n: > "+fmt.Sprintf("%q", marker)+"\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			preserved := map[string][]byte{}
			for _, path := range []string{f.store.Path(), attention.path, filepath.Join(f.root, "wire.jsonl")} {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				preserved[path] = raw
			}
			for range 2 {
				out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--model", "different-model", "--", "must not send this turn").CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("conflict exit: %v %s", err, out)
				}
				for _, want := range []string{processResumeAttentionMismatch, "recordedGeneration=" + old.Binding.Generation, "attentionGeneration=gen-conflicting-attention", "recorded conversation preserved", "projmux describe agent " + first.ref} {
					if !bytes.Contains(out, []byte(want)) {
						t.Fatalf("missing %q: %s", want, out)
					}
				}
				if bytes.Contains(out, []byte("after this owned generation is retired")) || bytes.Contains(out, []byte("foreground=owned")) {
					t.Fatalf("incorrect retry or startup guidance: %s", out)
				}
				for path, before := range preserved {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("conflict changed %s: %v", filepath.Base(path), err)
					}
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("conflict invoked provider", err)
				}
			}
		})
	}
}

func TestProcessResumeActualCLIRoundTrip(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			prompt := "question"
			if provider == aiModeCodex {
				prompt = "controls"
			}
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", prompt))
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.TurnID != "" && len(r.Pending) > 0 })
			assertProcessCLIForeground(t, f, old)
			out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--", "refused live task").CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) {
				t.Fatalf("live owner: %v %s", err, out)
			}
			first.shutdown(t)
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			for _, query := range [][]string{{"describe", "pane", "uid:" + old.Binding.PaneUID}, {"get", "panes", "--pane", "uid:" + old.Binding.PaneUID, "-o", "json"}} {
				output, err := exec.CommandContext(ctx, f.binary, query...).CombinedOutput()
				if err != nil || (!bytes.Contains(output, []byte("ResumeState")) && !bytes.Contains(output, []byte("resumeState"))) || !bytes.Contains(output, []byte("resumable")) {
					t.Fatalf("resume availability projection: %v %s", err, output)
				}
			}
			second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "new explicit task"})
			if second.ref != first.ref {
				t.Fatal("resume minted another Agent UID")
			}
			current := awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != "" && r.TurnID == ""
			})
			assertProcessCLIForeground(t, f, current)
			if current.Binding.PaneUID != old.Binding.PaneUID || current.Binding.OperationID == old.Binding.OperationID || current.History == nil || current.History.Binding != old.Binding || current.History.InterruptedTurnID != old.TurnID || len(current.History.Expired) != len(old.Pending) {
				t.Fatalf("resume history/current identities: %+v", current)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, ok := reg.CurrentProcessActivation(old.Binding); ok {
				t.Fatal("old generation retained authority")
			}
			pane, _ := reg.Pane(current.Binding.PaneUID)
			socket := processClaudeHostSocket(f.store.Path(), current.Binding.PaneUID, current.Binding.Generation)
			var request any
			foreground := &processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(old.Binding), Connection: old.ConnectionID, Session: old.SessionID}, Action: "turn", Operation: "old-generation-must-not-write", Prompt: "must not write"}
			if provider == aiModeCodex {
				foreground.Authority.Session = old.ThreadID
				socket = claudeActivationLeaseDir(f.store.Path(), current.Binding.PaneUID, current.Binding.Generation) + "/codex-host.sock"
				request = codexProcessExchange{Foreground: foreground}
			} else {
				request = claudeProcessCheck{Foreground: foreground}
			}
			identity, err := localipc.InspectOwnedSocket(socket)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := callProcessForeground(ctx, socket, identity, pane.Status.Activation.Process.HostProcess, request)
			if err == nil && stale.Accepted {
				t.Fatal("old generation wrote to new host")
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "turn", "start", second.ref, "--", "next user task").CombinedOutput()
			if err != nil {
				t.Fatalf("next turn: %v %s", err, out)
			}
			second.shutdown(t)
			wire, err := os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(wire, []byte("new explicit task")) || !bytes.Contains(wire, []byte("next user task")) || bytes.Contains(wire, []byte("must not write")) {
				t.Fatalf("turn wire: %s", wire)
			}
			if provider == aiModeClaude {
				argv, err := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				var calls [][]string
				for line := range bytes.SplitSeq(bytes.TrimSpace(argv), []byte("\n")) {
					var call []string
					if err = json.Unmarshal(line, &call); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, call)
				}
				if len(calls) != 2 {
					t.Fatalf("Claude startup count: %s", argv)
				}
				// Creation and resume share the headless auto policy.
				if permissionModes(calls[0]) != "auto" || permissionModes(calls[1]) != "auto" {
					t.Fatalf("permission mode: create %q resume %q", calls[0], calls[1])
				}
				found := false
				for index, arg := range calls[1] {
					if arg == "--resume" && index+1 < len(calls[1]) && calls[1][index+1] == old.SessionID {
						found = true
					}
				}
				if !found {
					t.Fatalf("Claude did not resume recorded session: %s", argv)
				}
			}
			if provider == aiModeCodex && strings.Count(string(wire), "thread/start") != 1 {
				t.Fatal("resume created a replacement thread")
			}
		})
	}
}

func TestProcessResumeOwnerKillIsNotResumableActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			run := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			record := awaitProcessResumeRecord(t, ctx, f, run.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(record.Binding.PaneUID)
			child := pane.Status.Activation.Process.Child
			// Freeze the fixture supervisor so this specifically observes the
			// owner-only death window, even under a slow test runner. Both-gone
			// recovery has its own test below.
			_, supervisorPID, err := localipc.Process(child.PID)
			if err != nil {
				t.Fatal(err)
			}
			supervisor, _, err := localipc.Process(supervisorPID)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(child.PID, syscall.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(supervisorPID, syscall.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if current, _, err := localipc.Process(child.PID); err == nil && current == child {
					_ = syscall.Kill(child.PID, syscall.SIGCONT)
				}
				if current, _, err := localipc.Process(supervisorPID); err == nil && current == supervisor {
					_ = syscall.Kill(supervisorPID, syscall.SIGCONT)
				}
			})
			t.Cleanup(func() {
				if identity, _, err := localipc.Process(child.PID); err == nil && identity == child {
					_ = syscall.Kill(child.PID, syscall.SIGKILL)
				}
			})
			if err = run.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = run.cmd.Wait()
			run.done = true
			if current, _, err := localipc.Process(child.PID); err != nil || current != child {
				t.Fatal("provider must remain alive in owner-only fixture", err)
			}
			out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", run.ref, "--", "must refuse").CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-resume-not-resumable")) {
				t.Fatalf("KILL resume: %v %s", err, out)
			}
			if err := syscall.Kill(child.PID, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(supervisorPID, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			for {
				identity, _, err := localipc.Process(child.PID)
				if err != nil || identity != child {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owned child survived owner death")
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}

func TestProcessResumeMissingFrameAndAmbiguousPaneActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := startResumeCLIInvocation(t, ctx, f, f.args("--", "first task"))
	awaitProcessResumeRecord(t, ctx, f, run.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" })
	run.shutdown(t)
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", run.ref).CombinedOutput()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 2 || !bytes.Contains(out, []byte("requires -- <prompt>")) {
		t.Fatalf("Claude absent first frame: %v %s", err, out)
	}
	after, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if before.UpdatedAt != after.UpdatedAt {
		t.Fatal("missing first frame mutated registry")
	}
	_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		pane, _ := processResumePane(*reg, strings.TrimPrefix(run.ref, "uid:"))
		duplicate := pane.Clone()
		duplicate.Metadata.UID += "-other"
		duplicate.Metadata.Name += "-other"
		duplicate.Status.ProcessSession.Binding.PaneUID = duplicate.Metadata.UID
		reg.Panes = append(reg.Panes, duplicate)
		for _, reservation := range reg.NameReservations {
			if reservation.Kind == coremetadata.KindPane && reservation.UID == pane.Metadata.UID {
				reservation.UID, reservation.Name = duplicate.Metadata.UID, duplicate.Metadata.Name
				reg.NameReservations = append(reg.NameReservations, reservation)
				break
			}
		}
		return reg.Validate()
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = exec.CommandContext(ctx, f.binary, "agent", "resume", run.ref, "--", "must refuse").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("process-resume-refused")) {
		t.Fatalf("ambiguous Pane: %v %s", err, out)
	}
}

func TestProcessResumeOutputModesAndCodexEmptyReattachActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeCodex)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ThreadID != "" })
	first.shutdown(t)
	for _, mode := range []string{"uid", "json", "receipt", "none"} {
		args := []string{"agent", "resume", first.ref, "-o", mode}
		// Empty input is an explicit Codex reattachment; later user turns use the
		// public turn command rather than a synthetic resume prompt.
		cmd := exec.CommandContext(ctx, f.binary, args...)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := false
		t.Cleanup(func() {
			_ = input.Close()
			if !done {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		defer func() {
			if t.Failed() {
				t.Logf("resume mode %s stderr: %s", mode, stderr.String())
			}
		}()
		current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.Binding.Generation != old.Binding.Generation })
		switch mode {
		case "uid":
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != strings.TrimPrefix(first.ref, "uid:") {
				t.Fatalf("UID projection %q %v", line, err)
			}
		case "json", "receipt":
			var result map[string]any
			if err = json.NewDecoder(output).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if mode == "receipt" {
				if result["operation"] != "agent.resume" {
					t.Fatalf("resume receipt %v", result)
				}
			} else if result["kind"] != resourceListKind(coremetadata.KindAgent, false) {
				t.Fatalf("JSON projection %v", result)
			}
		}
		_ = input.Close()
		rest, readErr := io.ReadAll(output)
		err = cmd.Wait()
		done = true
		if err != nil || readErr != nil {
			t.Fatalf("mode %s Wait: %v %v %s", mode, err, readErr, stderr.String())
		}
		if mode == "none" && (len(rest) > 0 || stderr.Len() > 0) {
			t.Fatalf("none displayed stdout=%s stderr=%s", rest, stderr.String())
		}
		if mode != "none" && !strings.Contains(stderr.String(), "runtime=process foreground=owned") {
			t.Fatalf("mode %s missing ownership stderr: %s", mode, stderr.String())
		}
		old = current
	}
	wire, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(wire), `"method":"thread/start"`) > 1 || strings.Count(string(wire), "thread/start") != 1 || strings.Count(string(wire), "turn/start") != 1 {
		t.Fatalf("empty reattach created a thread or synthetic task: %s", wire)
	}
}

func TestProcessResumePreSpawnFailureCanRetryActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			prompt := "question"
			if provider == aiModeCodex {
				prompt = "controls"
			}
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", prompt))
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.TurnID != "" && len(r.Pending) > 0 })
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			command := New().agent
			const generation = "test-unspawned-resume"
			command.rebind.create.newGeneration = func() (string, error) { return generation, nil }
			lease := claudeActivationLeaseDir(f.store.Path(), old.Binding.PaneUID, generation)
			file, err := os.OpenFile(lease, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
			t.Cleanup(func() { _ = os.Remove(lease) })
			request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: old.Binding.AgentUID}, Prompt: processResumeFirstFrame{Kind: "user", Text: "retry task"}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := command.resumeProcessAgent(ctx, request)
			if err == nil || result.Handle != nil {
				t.Fatal("occupied lease did not refuse before spawn", err)
			}
			failure := result.fail(err)
			if strings.Contains(failure.Error(), "delete") || !strings.Contains(failure.Error(), "conversation preserved") || !strings.Contains(failure.Error(), "retry: projmux agent resume") {
				t.Fatal("resume failure lost conversation guidance", failure)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(old.Binding.PaneUID)
			agent, _ := reg.Agent(old.Binding.AgentUID)
			if !reflect.DeepEqual(pane.Status.ProcessSession, &old) || agent.Status.Phase != coremetadata.PhaseOffline {
				t.Fatal("failed startup did not restore resumable record")
			}
			if err := os.Remove(lease); err != nil {
				t.Fatal(err)
			}
			retry := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "retry task"})
			if retry.ref != first.ref {
				t.Fatal("retry replaced Agent identity")
			}
			retry.shutdown(t)
		})
	}
}

func TestProcessResumeSupervisorSpawnFailureCanRetryActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "task"))
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(executable)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(executable, stat.Mode().Perm()) })
			// The isolated go-test executable is the supervisor chosen by the API.
			// Keep the separately copied public CLI executable available for retry.
			if err := os.Chmod(executable, stat.Mode().Perm()&^0111); err != nil {
				t.Fatal(err)
			}
			request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: old.Binding.AgentUID}, Prompt: processResumeFirstFrame{Kind: "user", Text: "retry task"}})
			if err != nil {
				t.Fatal(err)
			}
			result, startErr := New().agent.resumeProcessAgent(ctx, request)
			if err := os.Chmod(executable, stat.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if startErr == nil || result.Handle == nil || !result.hasNoChild() {
				t.Fatal("supervisor spawn failure did not return an unspawned handle", startErr)
			}
			failure := result.fail(startErr)
			if strings.Contains(failure.Error(), "delete") || strings.Contains(failure.Error(), "Wait evidence is unavailable") {
				t.Fatal("unspawned failure invented cleanup or Wait guidance", failure)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(old.Binding.PaneUID)
			agent, _ := reg.Agent(old.Binding.AgentUID)
			if !reflect.DeepEqual(pane.Status.ProcessSession, &old) || agent.Status.Phase != coremetadata.PhaseOffline {
				t.Fatal("unspawned handle did not restore resumable conversation")
			}
			retry := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "retry task"})
			if retry.ref != first.ref {
				t.Fatal("retry replaced Agent identity")
			}
			retry.shutdown(t)
		})
	}
}

// deferredResumeCLIFixture completes peer turns in the protocol fixture so
// the ordinary provider adapter can admit the next held envelope in order.
func deferredResumeCLIFixture(t *testing.T, provider string) processCreateCLI {
	t.Helper()
	f := processResumeCLIFixture(t, provider)
	if err := os.WriteFile(filepath.Join(f.root, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if provider == aiModeCodex {
		path := filepath.Join(f.root, "codex-provider.py")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(strings.Replace(string(raw), "prompt=='controls' or prompt.startswith('{')", "prompt=='controls'", 1)), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(f.root, "provider.py")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		script := strings.Replace(string(raw), "  inp={'questions'", "  emit({'type':'result','subtype':'success','session_id':'process-session'})\n  continue\n  inp={'questions'", 1)
		script = strings.Replace(script, "'provider.sock'", "'provider-%s.sock'%os.getpid()", 1)
		if err = os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

type deferredCLIClaim struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
	done   bool
}

func startDeferredCLIClaim(t *testing.T, ctx context.Context, f processCreateCLI, ref string) *deferredCLIClaim {
	t.Helper()
	run := &deferredCLIClaim{cmd: exec.CommandContext(ctx, f.binary, "agent", "resume", ref, "--wait-for-peer")}
	run.cmd.Stderr = &run.stderr
	var err error
	run.input, err = run.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := run.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	run.output = bufio.NewReader(out)
	if err = run.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run.input.Close()
		if !run.done {
			_ = run.cmd.Process.Kill()
			_ = run.cmd.Wait()
		}
	})
	line, err := run.output.ReadString('\n')
	if err != nil || !strings.Contains(line, "runtime=process foreground=claimed") || len(strings.Fields(line)) != 6 {
		t.Fatalf("claim output %q %v %s", line, err, run.stderr.String())
	}
	return run
}

func (run *deferredCLIClaim) finish(t *testing.T) {
	t.Helper()
	_ = run.input.Close()
	if err := run.cmd.Wait(); err != nil {
		t.Fatalf("claim Wait: %v %s", err, run.stderr.String())
	}
	run.done = true
}

func deferredCLIStatus(t *testing.T, ctx context.Context, f processCreateCLI, ref, state string) map[string]any {
	t.Helper()
	var receipt map[string]any
	processCLIUntil(t, ctx, func() bool {
		raw, err := exec.CommandContext(ctx, f.binary, "agent", "message", "status", ref, "-o", "json").Output()
		if err != nil {
			return false
		}
		if json.Unmarshal(raw, &receipt) != nil {
			return false
		}
		delivery, _ := receipt["delivery"].(map[string]any)
		if terminal := delivery["state"]; terminal != "held" && terminal != "accepted" && terminal != state {
			t.Fatalf("unexpected terminal state want %s: %s", state, raw)
		}
		return delivery["state"] == state
	})
	return receipt
}

func TestDeferredPeerWakeAndKilledClaimantActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, killed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/killed=%t", provider, killed), func(t *testing.T) {
				f := deferredResumeCLIFixture(t, provider)
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
				first.shutdown(t)
				old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
				args := f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source task")
				source := startResumeCLIInvocation(t, ctx, f, args)
				awaitProcessResumeRecord(t, ctx, f, source.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
				send := func(ref, text string) ([]byte, error) {
					return exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--", text).CombinedOutput()
				}
				before, beforeErr := send("no-claim", "without claimant")
				for bytes.Contains(before, []byte("source Agent is not eligible")) {
					select {
					case <-ctx.Done():
						t.Fatal("source route never ready")
					case <-time.After(10 * time.Millisecond):
					}
					before, beforeErr = send("no-claim", "without claimant")
				}
				if beforeErr == nil {
					t.Fatalf("unclaimed accepted: %s", before)
				}
				claim := startDeferredCLIClaim(t, ctx, f, first.ref)
				for _, args := range [][]string{{"agent", "resume", first.ref, "--wait-for-peer"}, {"agent", "resume", first.ref, "--", "other user"}} {
					out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
					if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) {
						t.Fatalf("claim exclusion: %v %s", err, out)
					}
				}
				// SIGSTOP keeps the exact claimant alive while messages accumulate; SIGKILL
				// then proves takeover without Close, before any frame has been dispatched.
				deferredPauseClaimant(t, ctx, f, first.ref, claim)
				refs := []string{"deferred-first", "deferred-second", "deferred-third"}
				state := filepath.Dir(filepath.Dir(f.store.Path()))
				store := messagestore.NewStore(state)
				var firstRecord messagestore.Record
				for i, ref := range refs {
					out, err := send(ref, fmt.Sprintf("peer-%d", i))
					if err != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
						t.Fatalf("held send %v %s", err, out)
					}
					receipt := deferredCLIStatus(t, ctx, f, ref, "held")
					if receipt["target"].(map[string]any)["activationGeneration"] != old.Binding.Generation {
						t.Fatal("held route not retired generation")
					}
					if i == 0 {
						var found bool
						firstRecord, found, err = store.Get(ref)
						if err != nil || !found {
							t.Fatal("first record", err)
						}
					}
				}
				expected, err := deferredPeerText(firstRecord)
				if err != nil {
					t.Fatal(err)
				}
				if killed {
					err = claim.cmd.Process.Kill()
				} else {
					err = claim.cmd.Process.Signal(syscall.SIGTERM)
					if err == nil {
						err = claim.cmd.Process.Signal(syscall.SIGCONT)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				stopErr := claim.cmd.Wait()
				if !killed && stopErr != nil {
					t.Fatalf("claimant signal exit: %v %s", stopErr, claim.stderr.String())
				}
				claim.done = true
				after, afterErr := send("no-claim", "without claimant")
				if afterErr == nil || !bytes.Equal(before, after) {
					t.Fatalf("unclaimed refusal parity before=%q after=%q", before, after)
				}
				started := time.Now()
				resumed := startDeferredCLIClaim(t, ctx, f, first.ref)
				line, err := resumed.output.ReadString('\n')
				if err != nil || !strings.Contains(line, "foreground=owned") {
					t.Fatalf("wake output %q %v %s", line, err, resumed.stderr.String())
				}
				for _, ref := range refs {
					deferredCLIStatus(t, ctx, f, ref, "delivered")
				}
				current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
				})
				assertProcessCLIForeground(t, f, current)
				latency := time.Since(started)
				if latency > 5*time.Second {
					t.Fatalf("wake latency=%s", latency)
				}
				t.Logf("provider=%s wake latency=%s", provider, latency)
				resumed.finish(t)
				source.shutdown(t)
				wire, err := os.ReadFile(f.trace)
				if err != nil {
					t.Fatal(err)
				}
				var peerFrames []string
				for line := range bytes.SplitSeq(bytes.TrimSpace(wire), []byte("\n")) {
					var frame map[string]any
					if json.Unmarshal(line, &frame) != nil {
						t.Fatal("wire JSON")
					}
					text := ""
					if frame["type"] == "user" {
						text, _ = frame["message"].(map[string]any)["content"].(string)
					}
					if frame["method"] == "turn/start" {
						inputs := frame["params"].(map[string]any)["input"].([]any)
						text, _ = inputs[0].(map[string]any)["text"].(string)
					}
					if strings.Contains(text, "projmux-coordination") {
						peerFrames = append(peerFrames, text)
					}
				}
				if provider == aiModeClaude {
					raw, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
						var frame map[string]any
						if json.Unmarshal(line, &frame) != nil {
							t.Fatal("push JSON")
						}
						peerFrames = append(peerFrames, frame["message"].(map[string]any)["content"].(string))
					}
				}
				if len(peerFrames) != 3 || peerFrames[0] != expected {
					t.Fatalf("first frame bytes/order/count: %q want %q", peerFrames, expected)
				}
				for i, text := range peerFrames {
					var frame map[string]any
					_ = json.Unmarshal([]byte(text), &frame)
					if frame["messageRef"] != refs[i] || frame["authority"] != "untrusted-coordination-only" || frame["payload"] != fmt.Sprintf("peer-%d", i) {
						t.Fatal("peer promoted or reordered", frame)
					}
				}
			})
		}
	}
}

func TestDeferredExpiryAndProviderRefusalActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--name", "source", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "source task"))
			awaitProcessResumeRecord(t, ctx, f, source.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
			claim := startDeferredCLIClaim(t, ctx, f, first.ref)
			deferredPauseClaimant(t, ctx, f, first.ref, claim)
			send := func(ref, ttl string) {
				out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--ttl", ttl, "--", "deferred task").CombinedOutput()
				if err != nil || !bytes.Contains(out, []byte("held")) {
					t.Fatalf("send: %v %s", err, out)
				}
			}
			send("deferred-expiry", "50ms")
			deferredCLIStatus(t, ctx, f, "deferred-expiry", "expired")
			path := filepath.Join(f.root, "provider.py")
			if provider == aiModeCodex {
				path = filepath.Join(f.root, "codex-provider.py")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			delayed := "import sys,time\nif '--resume' in sys.argv: time.sleep(2)\n" + string(raw)
			if provider == aiModeCodex {
				delayed = strings.Replace(string(raw), "elif method in ('thread/start','thread/resume'):\n", "elif method in ('thread/start','thread/resume'):\n  if method=='thread/resume': __import__('time').sleep(2)\n", 1)
			}
			if err = os.WriteFile(path, []byte(delayed), 0600); err != nil {
				t.Fatal(err)
			}
			send("deferred-bootstrap-expiry", "1s")
			if err = claim.cmd.Process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			waitErr := claim.cmd.Wait()
			claim.done = true
			if waitErr == nil {
				t.Fatal("bootstrap expiry exited zero")
			}
			receipt := deferredCLIStatus(t, ctx, f, "deferred-bootstrap-expiry", "expired")
			if receipt["delivery"].(map[string]any)["reason"] != "deadline-expired" {
				t.Fatal("deadline reason", receipt)
			}
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			claim = startDeferredCLIClaim(t, ctx, f, first.ref)
			deferredPauseClaimant(t, ctx, f, first.ref, claim)
			replacement := "import sys\nif '--resume' in sys.argv: sys.exit(7)\n" + string(raw)
			if provider == aiModeCodex {
				replacement = strings.Replace(string(raw), "elif method in ('thread/start','thread/resume'):", "elif method=='thread/resume':emit({'id':n['id'],'error':{'code':-32603,'message':'fixture resume refusal'}})\n elif method=='thread/start':", 1)
			}
			if err = os.WriteFile(path, []byte(replacement), 0600); err != nil {
				t.Fatal(err)
			}
			send("deferred-refusal", "30s")
			if err = claim.cmd.Process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			err = claim.cmd.Wait()
			claim.done = true
			if err == nil {
				t.Fatal("provider refusal exited zero")
			}
			receipt = deferredCLIStatus(t, ctx, f, "deferred-refusal", "failed")
			if receipt["delivery"].(map[string]any)["reason"] != processResumeRefused {
				t.Fatal("resume failure reason", receipt)
			}
			record := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			if record.SessionID != old.SessionID || record.ThreadID != old.ThreadID {
				t.Fatal("failed resume replaced conversation")
			}
			source.shutdown(t)
		})
	}
}

func TestDeferredClaimUserFirstFrameGoPathActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			first.shutdown(t)
			t.Setenv("PMX_TEST_DEFERRED_INTERNAL", "1")
			c := New().agent
			opts := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: strings.TrimPrefix(first.ref, "uid:")}}
			claim, err := c.claimDeferredProcessAgent(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer claim.Close()
			text := "literal user first frame\nwith exact newline"
			// Both constructors and the claimant consume the same 5d2 typed request.
			result, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "user", Text: text})
			if err != nil {
				t.Fatal(err)
			}
			end, stop := context.WithCancel(ctx)
			stop()
			if _, err = result.owner.waitProcessAgent(end, nil); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte("literal user first frame")) || bytes.Contains(raw, []byte("projmux-coordination")) {
				t.Fatal("user frame promoted/wrapped")
			}
		})
	}
}

func TestDeferredClaimIdleCPUActualCLI(t *testing.T) {
	f := deferredResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--", "first task"))
	first.shutdown(t)
	run := startDeferredCLIClaim(t, ctx, f, first.ref)
	start := time.Now()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	run.finish(t)
	seconds := run.cmd.ProcessState.UserTime().Seconds() + run.cmd.ProcessState.SystemTime().Seconds()
	percent := 100 * seconds / time.Since(start).Seconds()
	t.Logf("claimant idle CPU 60s average=%.4f%% (cpu seconds=%.6f)", percent, seconds)
	if percent >= 1 {
		t.Fatalf("claimant idle CPU >=1%%: %.4f%%", percent)
	}
}

func TestDeferredSupervisorSpawnFailureActualCLI(t *testing.T) {
	f := deferredResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--", "first task"))
	first.shutdown(t)
	source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--", "source task"))
	awaitProcessResumeRecord(t, ctx, f, source.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
	run := startDeferredCLIClaim(t, ctx, f, first.ref)
	deferredPauseClaimant(t, ctx, f, first.ref, run)
	out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", "deferred-spawn-failure", "--", "peer task").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("held")) {
		t.Fatalf("send: %v %s", err, out)
	}
	// The running claimant remains alive, but its owned supervisor executable
	// cannot be spawned. No ambient provider binary can be selected as fallback.
	moved := f.binary + ".saved"
	if err = os.Rename(f.binary, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(moved, f.binary) })
	if err = run.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	err = run.cmd.Wait()
	run.done = true
	if err == nil {
		t.Fatal("spawn failure exited zero")
	}
	if err = os.Rename(moved, f.binary); err != nil {
		t.Fatal(err)
	}
	deferredCLIStatus(t, ctx, f, "deferred-spawn-failure", "failed")
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
	source.shutdown(t)
}

func TestDeferredPeerWakeLatencyActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			target := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "target task"))
			target.shutdown(t)
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source task"))
			// Probe the unclaimed target until the source's live route is ready.
			for {
				out, _ := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target.ref, "--source", source.ref, "--", "route probe").CombinedOutput()
				if bytes.Contains(out, []byte("target Agent is not eligible")) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("source route never ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			claim := startDeferredCLIClaim(t, ctx, f, target.ref)
			started := time.Now()
			out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target.ref, "--source", source.ref, "--message-ref", "natural-wake", "--", "natural peer wake").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
				t.Fatalf("send %v %s", err, out)
			}
			line, err := claim.output.ReadString('\n')
			if err != nil || !strings.Contains(line, "foreground=owned") {
				t.Fatalf("wake %v %s %s", err, line, claim.stderr.String())
			}
			deferredCLIStatus(t, ctx, f, "natural-wake", "delivered")
			latency := time.Since(started)
			if latency > 5*time.Second {
				t.Fatalf("send-to-delivered latency %s", latency)
			}
			t.Logf("provider=%s natural send-to-delivered wake latency=%s", provider, latency)
			claim.finish(t)
			source.shutdown(t)
		})
	}
}

func TestDeferredExplicitReplyActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			if provider == aiModeClaude {
				path := filepath.Join(f.root, "provider.py")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				reply := `  original=json.loads(m['message']['content'])
  if original.get('messageRef')=='deferred-reply-original':
   def deferred_reply(original=original):
    root=os.environ['PMX_TEST_PROCESS_ROOT']
    while not os.path.exists(os.path.join(root,'reply-trigger')):time.sleep(.01)
    binding=json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])
    with open(os.path.join(root,'reply-receipt'),'w') as output:
     p=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'agent','message','send','uid:'+original['source']['agentUID'],'--source','uid:'+binding['Agent'],'--reply-to',original['messageRef'],'--message-ref','deferred-reply','--','reply peer'],stdout=output,stderr=subprocess.STDOUT)
    open(os.path.join(root,'reply-exit'),'w').write(str(p.returncode))
   threading.Thread(target=deferred_reply,daemon=True).start()
`
				script := "import time\n" + strings.Replace(string(raw), "  continue\n  inp=", reply+"  continue\n  inp=", 1)
				if err = os.WriteFile(path, []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			target := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "target task"))
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source task"))
			for {
				out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", source.ref, "--source", target.ref, "--message-ref", "deferred-reply-original", "--", "original peer").CombinedOutput()
				if err == nil {
					break
				}
				if !bytes.Contains(out, []byte("Agent is not eligible")) {
					t.Fatalf("original %v %s", err, out)
				}
				select {
				case <-ctx.Done():
					t.Fatal("original route never ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			deferredCLIStatus(t, ctx, f, "deferred-reply-original", "delivered")
			target.shutdown(t)
			claim := startDeferredCLIClaim(t, ctx, f, target.ref)
			var out []byte
			var err error
			if provider == aiModeClaude {
				if err = os.WriteFile(filepath.Join(f.root, "reply-trigger"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				for {
					exit, readErr := os.ReadFile(filepath.Join(f.root, "reply-exit"))
					if readErr == nil {
						out, err = os.ReadFile(filepath.Join(f.root, "reply-receipt"))
						if string(exit) != "0" {
							t.Fatalf("provider reply exit %s: %s", exit, out)
						}
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("provider reply timed out")
					case <-time.After(10 * time.Millisecond):
					}
				}
			} else {
				out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", target.ref, "--source", source.ref, "--reply-to", "deferred-reply-original", "--message-ref", "deferred-reply", "--", "reply peer").CombinedOutput()
			}
			if err != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
				t.Fatalf("reply acceptance %v %s", err, out)
			}
			line, err := claim.output.ReadString('\n')
			if err != nil || !strings.Contains(line, "foreground=owned") {
				t.Fatalf("reply wake %v %s %s", err, line, claim.stderr.String())
			}
			deferredCLIStatus(t, ctx, f, "deferred-reply", "delivered")
			store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.store.Path())))
			original, found, err := store.Get("deferred-reply-original")
			if err != nil || !found {
				t.Fatal(err)
			}
			reply, found, err := store.Get("deferred-reply")
			if err != nil || !found || reply.Envelope.ReplyTo != original.Envelope.MessageRef || reply.Envelope.ConversationRef != original.Envelope.ConversationRef || reply.Envelope.Deadline.After(original.Envelope.Deadline) {
				t.Fatal("reply correlation changed", err)
			}
			claim.finish(t)
			source.shutdown(t)
		})
	}
}

func TestDeferredTailDrainKeepsControlsResponsiveActualCLI(t *testing.T) {
	f := deferredResumeCLIFixture(t, aiModeCodex)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err = config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), config.AgentQuestionAnsweringProjmux); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), "if prompt=='controls':", "if prompt=='controls' or (prompt.startswith('{') and turn==1):", 1)
	if err = os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
	first.shutdown(t)
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
	source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source task"))
	send := func(ref string) ([]byte, error) {
		return exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--", ref).CombinedOutput()
	}
	processCLIUntil(t, ctx, func() bool {
		out, _ := send("ready-probe")
		return !bytes.Contains(out, []byte("source Agent is not eligible"))
	})
	claim := startDeferredCLIClaim(t, ctx, f, first.ref)
	deferredPauseClaimant(t, ctx, f, first.ref, claim)
	for _, ref := range []string{"controls-first", "controls-tail"} {
		out, e := send(ref)
		if e != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
			t.Fatalf("held: %v %s", e, out)
		}
	}
	if err = claim.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(filepath.Dir(f.store.Path()))
	qs, as := agentquestion.NewStore(state), agentapproval.NewStore(state)
	uid := strings.TrimPrefix(first.ref, "uid:")
	var questions []agentquestion.Record
	var approvals []agentapproval.Record
	controlCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	processCLIUntil(t, controlCtx, func() bool {
		questions, _ = qs.List(uid)
		approvals, _ = as.List(uid)
		return len(questions) == 1 && len(approvals) == 1
	})
	cli := func(args ...string) {
		t.Helper()
		out, e := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("control: %v %s", e, out)
		}
	}
	cli("agent", "question", "answer", first.ref, questions[0].ID, "--option", "1=blue")
	cli("agent", "approval", "answer", first.ref, approvals[0].ID, "--deny", "--via", "cli")
	for _, ref := range []string{"controls-first", "controls-tail"} {
		deferredCLIStatus(t, ctx, f, ref, "delivered")
	}
	line, err := claim.output.ReadString('\n')
	if err != nil || !strings.Contains(line, "foreground=owned") {
		t.Fatalf("owned: %v %s", err, line)
	}
	claim.finish(t)
	source.shutdown(t)
}

func TestDeferredDrainEOFPreservesPeerForNextClaimActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			target := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "target task"))
			target.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, target.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source task"))
			send := func(ref string) ([]byte, error) {
				return exec.CommandContext(ctx, f.binary, "agent", "message", "send", target.ref, "--source", source.ref, "--message-ref", ref, "--", ref).CombinedOutput()
			}
			processCLIUntil(t, ctx, func() bool {
				out, _ := send("ready-probe")
				return bytes.Contains(out, []byte("target Agent is not eligible"))
			})
			path := filepath.Join(f.root, "provider.py")
			if provider == aiModeCodex {
				path = filepath.Join(f.root, "codex-provider.py")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(raw), "elif prompt=='interrupt':", "elif prompt=='interrupt' or 'eof-first' in prompt:", 1)
			if provider == aiModeCodex {
				script = strings.Replace(string(raw), "elif prompt!='hold':complete()", "elif prompt!='hold' and 'eof-first' not in prompt:complete()", 1)
			}
			if err = os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			claim := startDeferredCLIClaim(t, ctx, f, target.ref)
			deferredPauseClaimant(t, ctx, f, target.ref, claim)
			for _, ref := range []string{"eof-first", "eof-tail"} {
				out, e := send(ref)
				if e != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
					t.Fatalf("held: %v %s", e, out)
				}
			}
			if err = claim.cmd.Process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			deferredCLIStatus(t, ctx, f, "eof-first", "delivered")
			running := awaitProcessResumeRecord(t, ctx, f, target.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.TurnID != ""
			})
			deferredCLIStatus(t, ctx, f, "eof-tail", "held")
			claim.finish(t)
			retired := awaitProcessResumeRecord(t, ctx, f, target.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			if retired.SessionID != old.SessionID || retired.ThreadID != old.ThreadID {
				t.Fatal("EOF changed conversation")
			}
			held := deferredCLIStatus(t, ctx, f, "eof-tail", "held")
			if retired.Binding.Generation != running.Binding.Generation || held["target"].(map[string]any)["activationGeneration"] != retired.Binding.Generation {
				t.Fatal("queued peer did not follow the recorded Wait generation")
			}
			next := startDeferredCLIClaim(t, ctx, f, target.ref)
			line, err := next.output.ReadString('\n')
			if err != nil || !strings.Contains(line, "foreground=owned") {
				t.Fatalf("next wake: %v %s %s", err, line, next.stderr.String())
			}
			deferredCLIStatus(t, ctx, f, "eof-tail", "delivered")
			next.finish(t)
			source.shutdown(t)
		})
	}
}

// permissionModes joins every --permission-mode value of a recorded argv.
func permissionModes(argv []string) string {
	var modes []string
	for index, arg := range argv {
		if arg == "--permission-mode" && index+1 < len(argv) {
			modes = append(modes, argv[index+1])
		}
	}
	return strings.Join(modes, ",")
}

func TestProcessClaudeOfflineResumeSelectsAutoActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	old := deferredCandidate(t, f, first.ref)
	second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "offline resume"})
	deferredReady(t, ctx, f, first.ref)
	second.shutdown(t)
	current := deferredCandidate(t, f, first.ref)
	argv := deferredArgv(t, f)
	if len(argv) != 2 || permissionModes(argv[0]) != "auto" || permissionModes(argv[1]) != "auto" || current.Record.SessionID != old.Record.SessionID || current.Record.Binding.Generation == old.Record.Binding.Generation {
		t.Fatal("offline auto argv/conversation", argv)
	}
}

func TestCodexResumeRetiredAttentionAndInitFailureActualCLI(t *testing.T) {
	f := processResumeCLIFixture(t, aiModeCodex)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--", "first task"))
	first.shutdown(t)
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
	attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(f.store.Path())))
	records, err := attention.read()
	if err != nil {
		t.Fatal(err)
	}
	retired := records[old.Binding.PaneUID]
	path := filepath.Join(f.root, "codex-provider.py")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	failedScript := bytes.Replace(script, []byte("if method=='initialize':reply({'userAgent':'projmux/0.160.0'})"), []byte("if method=='initialize':emit({'id':n['id'],'error':{'code':-32603,'message':'fixture init refusal'}})"), 1)
	if bytes.Equal(script, failedScript) {
		t.Fatal("fixture failure injection did not match")
	}
	if err := os.WriteFile(path, failedScript, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte(`"method":"initialize"`)) || !bytes.Contains(out, []byte(`"rpc_code":-32603`)) {
		t.Fatalf("init refusal: %v %s", err, out)
	}
	failed := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.ResumeState == coremetadata.ProcessResumable && r.Binding.Generation != old.Binding.Generation
	})
	records, err = attention.read()
	if err != nil || records[failed.Binding.PaneUID].Binding != processSchemaBinding(failed.Binding) || failed.ThreadID != old.ThreadID {
		t.Fatal("init failure left attention skew or lost thread", err)
	}
	if err := os.WriteFile(path, script, 0600); err != nil {
		t.Fatal(err)
	}
	// Recreate a persisted pre-fix attention record. Its original actual Wait
	// remains in the append-only journal; no terminal-badge inference is used.
	if err := attention.update(func(records map[string]processAttentionRecord) error {
		records[old.Binding.PaneUID] = retired
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "same conversation"})
	current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != failed.Binding.Generation && r.ConnectionID != ""
	})
	if current.ThreadID != old.ThreadID || second.ref != first.ref {
		t.Fatal("retired attention recovery replaced conversation")
	}
	second.shutdown(t)
	records, err = attention.read()
	if err != nil || records[current.Binding.PaneUID].Binding != processSchemaBinding(current.Binding) {
		t.Fatal("resume did not transfer exact attention binding", err)
	}
	wire, err := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(wire, []byte(`"method": "thread/start"`))+bytes.Count(wire, []byte(`"method":"thread/start"`)) != 1 {
		t.Fatal("resume created another thread", string(wire))
	}
}

func TestProcessHostLostResumeActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			prompt := "question"
			if provider == aiModeCodex {
				prompt = "controls"
			}
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", prompt))
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.TurnID != "" && len(r.Pending) > 0 })
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ := reg.Pane(old.Binding.PaneUID)
			a := *pane.Status.Activation.Process
			// Kill only fixture-owned processes, retaining the exact kernel identities.
			// Stop at a point outside the unrelated notification lock: SIGKILL
			// while holding that file lock would exercise stale-notify recovery,
			// rather than the process conversation ownership boundary.
			notifyLock := filepath.Join(filepath.Dir(filepath.Dir(f.store.Path())), "notify.json.lock")
			for {
				if err := syscall.Kill(a.HostProcess.PID, syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(notifyLock); os.IsNotExist(err) {
					break
				}
				if err := syscall.Kill(a.HostProcess.PID, syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
				select {
				case <-ctx.Done():
					t.Fatal("owner never released notification lock")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := syscall.Kill(a.HostProcess.PID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(a.Child.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Fatal(err)
			}
			_ = first.input.Close()
			_ = first.cmd.Wait()
			first.done = true
			for !processActivationAbsent(a, localipc.Process) {
				select {
				case <-ctx.Done():
					t.Fatal("fixture processes did not disappear")
				case <-time.After(10 * time.Millisecond):
				}
			}
			before, err := os.ReadFile(f.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"get", "agents", "-o", "json"}, {"describe", "agent", first.ref}} {
				if out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput(); err != nil {
					t.Fatalf("read: %v %s", err, out)
				}
			}
			after, _ := os.ReadFile(f.store.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("read-only query converged")
			}
			second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "new explicit task"})
			current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if current.Binding.PaneUID != old.Binding.PaneUID || second.ref != first.ref || current.SessionID != old.SessionID || current.ThreadID != old.ThreadID || current.History == nil || current.History.Binding != old.Binding || current.History.InterruptedTurnID != old.TurnID || len(current.History.Expired) != len(old.Pending) {
				t.Fatalf("conversation/history changed: %+v", current)
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(current.Binding.PaneUID)
			receipt := pane.Status.LastTermination
			if receipt == nil || receipt.Source != coremetadata.TerminationSourceReconcile || receipt.Classification != coremetadata.TerminationUnknown || receipt.ExitCode != nil || receipt.Signal != "" {
				t.Fatalf("invented Wait: %+v", receipt)
			}
			second.shutdown(t)
			if !strings.Contains(second.stderr.String(), "last turn may have been truncated") {
				t.Fatal("missing warning", second.stderr.String())
			}
		})
	}
}

func TestProcessCodexResumeGoalFirstFramesActualCLI(t *testing.T) {
	for _, kind := range []string{"user", "peer"} {
		t.Run(kind, func(t *testing.T) {
			f := processResumeCLIFixture(t, aiModeCodex)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", aiModeCodex, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			path := filepath.Join(f.root, "codex-provider.py")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(raw), " elif method in ('thread/start','thread/resume'):", ` elif method=='thread/resume':
  current='goal-resumed'
  notify('turn/started',{'threadId':'process-thread','turn':{'id':current}})
  reply({'thread':{'id':'process-thread'},'model':'stub-model','reasoningEffort':'low','sandbox':{'type':'readOnly'},'approvalPolicy':'on-request'})
 elif method in ('thread/start','thread/resume'):`, 1)
			script = strings.Replace(script, " elif method=='turn/interrupt':", " elif method=='turn/steer':reply({'turnId':current})\n elif method=='turn/interrupt':", 1)
			if err = os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			text := "explicit resume task"
			if kind == "user" {
				second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", text})
				current := awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.TurnID == "goal-resumed"
				})
				if current.ThreadID != old.ThreadID {
					t.Fatal("conversation changed")
				}
				assertProcessCLIForeground(t, f, current)
				second.shutdown(t)
			} else {
				text, err = providerCoordinationContent(dialogueEnvelope("goal-first-peer", time.Now().Add(time.Minute)))
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("PMX_TEST_DEFERRED_INTERNAL", "1")
				c := New().agent
				request, err := newProcessAgentResumeRequest(processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: strings.TrimPrefix(first.ref, "uid:")}, Prompt: processResumeFirstFrame{Kind: kind, Text: text}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := c.resumeProcessAgent(ctx, request)
				defer func() { _ = result.fail(nil) }()
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := result.Handle.Observe(result.Binding)
				if err != nil || snapshot.Turn != "goal-resumed" || snapshot.Session != old.ThreadID || snapshot.State != "ready" || snapshot.Exit != nil {
					t.Fatalf("peer goal owner: %+v %v", snapshot, err)
				}
			}
			raw, err = os.ReadFile(f.trace)
			if err != nil {
				t.Fatal(err)
			}
			starts, steers := 0, 0
			for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
				var frame struct {
					Method string `json:"method"`
					Params struct {
						ExpectedTurnID string `json:"expectedTurnId"`
						Input          []struct {
							Text string `json:"text"`
						} `json:"input"`
					} `json:"params"`
				}
				if err = json.Unmarshal(line, &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Method == "turn/start" {
					starts++
				}
				if frame.Method == "turn/steer" {
					steers++
					if frame.Params.ExpectedTurnID != "goal-resumed" || len(frame.Params.Input) != 1 || frame.Params.Input[0].Text != text {
						t.Fatalf("first frame rewritten or misrouted: %s", line)
					}
				}
			}
			if starts != 1 || steers != 1 {
				t.Fatalf("turn/start=%d turn/steer=%d", starts, steers)
			}
		})
	}
}
