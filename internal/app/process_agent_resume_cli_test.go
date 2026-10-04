package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
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
		if err = os.WriteFile(filepath.Join(f.root, "claude"), []byte("#!/bin/sh\nexec python3 -u "+fmt.Sprintf("%q", path)+" \"$@\"\n"), 0700); err != nil {
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
			out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", run.ref, "--", "must refuse").CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-resume-not-resumable")) {
				t.Fatalf("KILL resume: %v %s", err, out)
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
