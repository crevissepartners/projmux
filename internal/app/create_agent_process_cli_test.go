package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestProcessCreateActualCLIIsolated(t *testing.T) {
	for _, prompt := range []bool{false, true} {
		t.Run(fmt.Sprint(prompt), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			root, binary, trace, store, project, window := f.root, f.binary, f.trace, f.store, f.project, f.window
			_ = root
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			args := []string{"create", "agent", "--host", "process", "--project", "uid:" + project, "--window", "uid:" + window, "--provider", "claude", "--name", "owned"}
			if prompt {
				args = append(args, "--", "initial task")
			}
			cmd := exec.CommandContext(ctx, binary, args...)
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
			if !strings.Contains(line, "runtime=process foreground=owned") {
				t.Fatalf("ownership %q", line)
			}
			fields := strings.Fields(line)
			agentRef := fields[1]
			if !prompt {
				reg, err := store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				observed := observeRegistryProcesses(ctx, reg)
				if len(observed.Observed) != 1 || observed.Observed[0].Status != resourcegraph.StatusLive {
					t.Fatalf("owned starting was unknown: %+v", observed)
				}
				out, err := exec.CommandContext(ctx, binary, "agent", "turn", "start", agentRef, "--", "plain user task").CombinedOutput()
				if err != nil {
					t.Fatalf("remote turn: %v %s", err, out)
				}
			}
			for {
				raw, _ := os.ReadFile(trace)
				if bytes.Contains(raw, []byte("initial task")) || bytes.Contains(raw, []byte("plain user task")) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("user frame absent: %s", stderr.String())
				case <-time.After(10 * time.Millisecond):
				}
			}
			_ = input.Close()
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("Wait: %v %s", err, stderr.String())
			}
			reg, err := store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(strings.TrimPrefix(agentRef, "uid:"))
			pane, _ := reg.Pane(agent.Status.PaneRef)
			if agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || pane.Status.LastTermination == nil || agent.Status.LastTermination == nil {
				t.Fatal("EOF lost actual Wait retirement")
			}
			assertProcessOwnerStop(t, store.Path(), stderr.String(), diagnostics.OwnerStopStdinEOF, cmd.Process.Pid)
			// The recorded Wait projects the retired Agent offline, not unknown.
			if described, err := exec.CommandContext(ctx, binary, "describe", "agent", agentRef).CombinedOutput(); err != nil || !regexp.MustCompile(`(?m)^Status: +offline$`).Match(described) {
				t.Fatalf("retired Agent status: %v\n%s", err, described)
			}
			raw, _ := os.ReadFile(trace)
			var frame map[string]any
			if err = json.Unmarshal(bytes.Split(raw, []byte("\n"))[0], &frame); err != nil || frame["type"] != "user" || bytes.Contains(raw, []byte("projmux-coordination")) {
				t.Fatalf("turn was enveloped: %s %v", raw, err)
			}
		})
	}
}

type processCreateCLI struct {
	root, binary, trace, project, window string
	store                                *intmetadata.Store
}

func newProcessCreateCLI(t *testing.T) processCreateCLI {
	t.Helper()
	product := os.Getenv("PMX_TEST_CLI")
	if product == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	root, err := os.MkdirTemp(os.TempDir(), "pc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		t.Setenv(key, "")
	}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
			t.Setenv(key, "")
		}
	}
	for key, dir := range map[string]string{"HOME": root, "XDG_STATE_HOME": root + "/state", "XDG_CONFIG_HOME": root + "/config", "XDG_CACHE_HOME": root + "/cache"} {
		t.Setenv(key, dir)
	}
	binary := filepath.Join(root, "projmux")
	raw, err := os.ReadFile(product)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "provider.py")
	trace := filepath.Join(root, "wire.jsonl")
	provider := strings.Replace(processClaudeProviderFixture, " frame=json.loads(line)", " open("+fmt.Sprintf("%q", trace)+",'a').write(line)\n frame=json.loads(line)", 1)
	provider = strings.Replace(provider, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'messages.jsonl'),'a').write(json.dumps(m)+'\\n');emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", 1)
	provider = strings.Replace(provider, "elif prompt=='register-again':", "elif prompt=='send-message':\n   emit({'type':'result','subtype':'success','session_id':'process-session'})\n   b=json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])\n   subprocess.Popen([os.environ['PMX_TEST_PROCESS_BINARY'],'agent','message','send','uid:'+b['Agent'],'--source','uid:'+b['Agent'],'--','peer payload'],stdout=open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'message-receipt'),'w'),stderr=subprocess.STDOUT)\n  elif prompt=='register-again':", 1)
	if err = os.WriteFile(script, []byte(provider), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\n"+processFixtureExports(root)+"exec python3 -u "+fmt.Sprintf("%q", script)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PMX_TEST_PROCESS_ROOT", root)
	t.Setenv("PMX_TEST_PROCESS_BINARY", binary)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store := intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
	var project, window string
	_, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, err := intmetadata.DefaultMutator().RegisterProject(reg, coremetadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/sh", OperationID: "fixture-create"})
		if err == nil {
			project, window = p.Project.Metadata.UID, p.Windows[0].Metadata.UID
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return processCreateCLI{root, binary, trace, project, window, store}
}
func (f processCreateCLI) args(extra ...string) []string {
	return append([]string{"create", "agent", "--host", "process", "--project", "uid:" + f.project, "--window", "uid:" + f.window, "--provider", "claude", "--name", "owned"}, extra...)
}
func (f processCreateCLI) config(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(f.root, "config", "projmux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestProcessCreateHookRollbackActualCLI(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			run := "exit 7"
			if broken {
				run = "mkdir -p " + filepath.Join(f.root, "state", "projmux", terminationJournalFile) + "; exit 7"
			}
			f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = "+fmt.Sprintf("%q", run)+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, f.args()...).CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-post-create-hook-failed")) {
				t.Fatalf("hook failure %v %s", err, out)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if !broken {
				if len(reg.Agents) != 0 || bytes.Contains(out, []byte("runtime=process foreground=owned")) || !bytes.Contains(out, []byte("remaining: none")) {
					t.Fatalf("rollback remnants %s %+v", out, reg.Agents)
				}
			} else {
				if len(reg.Agents) != 1 || !bytes.Contains(out, []byte("runtime=unknown")) || !bytes.Contains(out, []byte("cleanup: projmux delete agent uid:"+reg.Agents[0].Metadata.UID)) {
					t.Fatalf("failed rollback lost exact ref: %s", out)
				}
			}
		})
	}
}
func TestProcessCreateOwnerSignalsActualCLI(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
				t.Fatal(err)
			}
			ref := strings.TrimPrefix(strings.Fields(line)[1], "uid:")
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(ref)
			pane, _ := reg.Pane(agent.Status.PaneRef)
			birth := pane.Status.Activation.Process.Child
			if err = cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if signal != syscall.SIGKILL && err != nil {
				t.Fatalf("owned shutdown: %v %s", err, stderr.String())
			}
			if signal != syscall.SIGKILL {
				t.Logf("owner %s actual CLI exit=%d", signal, cmd.ProcessState.ExitCode())
				reason := diagnostics.OwnerStopSIGINT
				if signal == syscall.SIGTERM {
					reason = diagnostics.OwnerStopSIGTERM
				}
				assertProcessOwnerStop(t, f.store.Path(), stderr.String(), reason, cmd.Process.Pid)
			}
			for {
				current, _, probeErr := localipc.Process(birth.PID)
				if probeErr != nil || current != birth {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owned child survived owner death")
				case <-time.After(10 * time.Millisecond):
				}
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			pane, _ = reg.Pane(agent.Status.PaneRef)
			if signal != syscall.SIGKILL && (!pane.Status.Activation.IsZero() || pane.Status.LastTermination == nil) {
				t.Fatal("signal lost actual Wait")
			}
			if signal == syscall.SIGKILL {
				got := observeRegistryProcesses(ctx, reg)
				if len(got.Observed) != 1 || got.Observed[0].Status != resourcegraph.StatusUnknown {
					t.Fatalf("missing Wait invented exit: %+v", got)
				}
			}
		})
	}
}
func TestProcessCreateProjectionAndRefusalsActualCLI(t *testing.T) {
	for _, mode := range []string{"uid", "name", "ref", "metadata", "json", "receipt", "none", "pane-id"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcessCreateCLI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
				if err == nil || len(reg.Agents) != 0 {
					t.Fatal("pane-id mutated")
				}
				return
			}
			if err != nil {
				t.Fatalf("projection %v %s", err, stderr.String())
			}
			if bytes.Contains(stdout.Bytes(), []byte("runtime=process foreground=owned")) || bytes.Contains(stdout.Bytes(), []byte("session_id")) {
				t.Fatal("projection leaked ownership/provider")
			}
			if mode == "none" {
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("none output: %s %s", stdout.String(), stderr.String())
				}
				return
			}
			if !bytes.Contains(stderr.Bytes(), []byte("runtime=process foreground=owned")) {
				t.Fatal("ownership stderr absent")
			}
			if mode == "uid" && strings.TrimSpace(stdout.String()) != reg.Agents[0].Metadata.UID {
				t.Fatalf("UID projection %q", stdout.String())
			}
			if mode == "name" && strings.TrimSpace(stdout.String()) != "owned" {
				t.Fatalf("name projection %q", stdout.String())
			}
		})
	}
}

func TestProcessCreateAnswerAndMessageActualCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args()...)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	defer func() {
		if t.Failed() {
			raw, _ := os.ReadFile(f.trace)
			receipt, _ := os.ReadFile(filepath.Join(f.root, "message-receipt"))
			t.Logf("wire=%s message=%s owner=%s", raw, receipt, stderr.String())
		}
	}()
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
		t.Fatal(err)
	}
	ref := strings.Fields(line)[1]
	uid := strings.TrimPrefix(ref, "uid:")
	paths, _ := config.DefaultPathsFromEnv()
	questions := agentquestion.NewStore(paths.StateDir)
	approvals := agentapproval.NewStore(paths.StateDir)
	run := func(args ...string) {
		t.Helper()
		t.Logf("CLI %v", args)
		out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
	}
	// Global projmux holds questions even with no per-Agent annotation.
	if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), config.AgentQuestionAnsweringProjmux); err != nil {
		t.Fatal(err)
	}
	run("agent", "turn", "start", ref, "--", "question")
	processCLIUntil(t, ctx, func() bool {
		records, _ := questions.List(uid)
		return len(records) == 1 && records[0].State == agentquestion.StateWaiting
	})
	records, _ := questions.List(uid)
	run("agent", "question", "answer", ref, records[0].ID, "--option", "1=blue")
	processCLIUntil(t, ctx, func() bool { raw, _ := os.ReadFile(f.trace); return bytes.Contains(raw, []byte("control_response")) })
	processCLIUntil(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		p, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
		return p.Status.ProcessSession != nil && len(p.Status.ProcessSession.Pending) == 0
	})
	run("agent", "turn", "start", ref, "--", "permission")
	processCLIUntil(t, ctx, func() bool {
		records, _ := approvals.List(uid)
		return len(records) == 1 && records[0].State == agentapproval.StateWaiting
	})
	requests, _ := approvals.List(uid)
	run("agent", "approval", "answer", ref, requests[0].ID, "--allow")
	processCLIUntil(t, ctx, func() bool { raw, _ := os.ReadFile(f.trace); return bytes.Count(raw, []byte("control_response")) >= 2 })
	processCLIUntil(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		p, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
		return p.Status.ProcessSession != nil && len(p.Status.ProcessSession.Pending) == 0
	})
	run("agent", "turn", "start", ref, "--", "interrupt")
	processCLIUntil(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		p, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
		return p.Status.ProcessSession != nil && p.Status.ProcessSession.TurnID != ""
	})
	run("agent", "turn", "interrupt", ref, "--via", "cli")
	processCLIUntil(t, ctx, func() bool {
		raw, _ := os.ReadFile(f.trace)
		return bytes.Contains(raw, []byte("interrupt")) && bytes.Contains(raw, []byte("control_request"))
	})
	run("agent", "turn", "start", ref, "--", "send-message")
	processCLIUntil(t, ctx, func() bool {
		raw, _ := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
		return bytes.Contains(raw, []byte("projmux-coordination")) && bytes.Contains(raw, []byte("peer payload"))
	})
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("answer owner Wait: %v %s", err, stderr.String())
	}
}
func processCLIUntil(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("isolated CLI condition exceeded bound")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestProcessCreateHookEligibilityActualCLI(t *testing.T) {
	for _, mode := range []string{"undeclared", "declared", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcessCreateCLI(t)
			text := "[hooks.post-create]\nrun = \"exit 7\"\n"
			if mode == "declared" {
				text = "[hooks.post-create]\nruntime = \"process\"\nrun = \"printf '%s:%s:%s' \\\"$PROJMUX_RUNTIME\\\" \\\"${PROJMUX_PANE-absent}\\\" \\\"${TMUX-absent}\\\"\"\n"
			}
			if mode == "invalid" {
				text = "[hooks.post-create]\nruntime = \"typo\"\nrun = \"exit 7\"\n"
			}
			f.config(t, text)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, f.args()...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("hook eligibility %v %s", err, stderr.String())
			}
			if mode == "declared" && !strings.Contains(stderr.String(), "process:absent:absent") {
				t.Fatalf("hook env %s", stderr.String())
			}
			if mode == "invalid" && !strings.Contains(stderr.String(), "runtime") {
				t.Fatal("parse warning hidden")
			}
		})
	}
}
func TestProcessCreateReturnsActualWaitExitCLI(t *testing.T) {
	for _, exit := range []struct {
		body string
		code int
	}{{"sys.exit(9)", 9}, {"import signal;os.kill(os.getpid(),signal.SIGTERM)", 143}} {
		t.Run(fmt.Sprint(exit.code), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			path := filepath.Join(f.root, "provider.py")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, append(raw, []byte("\n"+exit.body+"\n")...), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, f.args()...).CombinedOutput()
			status, ok := err.(*exec.ExitError)
			if !ok || status.ExitCode() != exit.code {
				t.Fatalf("actual exit %v %s", err, out)
			}
		})
	}
}

func TestProcessCreateClosedStdinExitActualCLI(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			want := 0
			if forced {
				path := filepath.Join(f.root, "provider.py")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, append(raw, []byte("\nthreading.Event().wait()\n")...), 0600); err != nil {
					t.Fatal(err)
				}
				want = 143
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, f.args("-o", "none")...)
			// os/exec's absent Stdin is /dev/null, as in a detached CI owner.
			err := cmd.Run()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != want {
				t.Fatalf("closed stdin: %v state=%v want=%d", err, cmd.ProcessState, want)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if len(reg.Agents) != 1 {
				t.Fatal("creation did not run before EOF")
			}
			pane, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
			receipt := pane.Status.LastTermination
			if receipt == nil || receipt.Classification != coremetadata.TerminationNormal || (want == 0 && (receipt.ExitCode == nil || *receipt.ExitCode != 0)) || (want == 143 && receipt.Signal == "") {
				t.Fatalf("actual Wait missing: %+v", receipt)
			}
			t.Logf("stdin=/dev/null forced=%t actual CLI exit=%d classification=%s", forced, want, receipt.Classification)
		})
	}
}

// processFixtureExports lets a provider wrapper hand the fixture root and the
// copied product binary to its script. A process-hosted provider receives only
// allowlisted inherited variables, so the test's own environment never
// reaches it.
func processFixtureExports(root string) string {
	return "export PMX_TEST_PROCESS_ROOT=" + fmt.Sprintf("%q", root) + " PMX_TEST_PROCESS_BINARY=" + fmt.Sprintf("%q", filepath.Join(root, "projmux")) + "\n"
}

// processGuidanceLocation is the default guidance sentence that tells a
// process-hosted model where it runs and how it is controlled.
const processGuidanceLocation = "This agent runs in a foreground process host without a tmux pane."

// The launched Claude receives the process execution-location guidance
// through its instructions file, not only in unit-rendered text.
func TestProcessCreateGuidanceActualCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	argv := filepath.Join(f.root, "argv")
	wrapper := "#!/bin/sh\n" + processFixtureExports(f.root) + "printf '%s\\n' \"$@\" >" + fmt.Sprintf("%q", argv) + "\nexec python3 -u " + fmt.Sprintf("%q", filepath.Join(f.root, "provider.py")) + "\n"
	if err := os.WriteFile(filepath.Join(f.root, "claude"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	if _, err := bufio.NewReader(output).ReadString('\n'); err != nil {
		t.Fatalf("ownership: %v %s", err, stderr.String())
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for arg := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if text, readErr := os.ReadFile(arg); readErr == nil && bytes.Contains(text, []byte(processGuidanceLocation)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("process guidance absent from the launched instructions; argv:\n%s", raw)
	}
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("Wait: %v %s", err, stderr.String())
	}
}

func TestProcessCreatorActualCLI(t *testing.T) {
	for _, mode := range []string{"process-chain", "explicit", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcessCreateCLI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			// The fixture provider executes create through its own child shell/process.
			script := filepath.Join(f.root, "provider.py")
			raw, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			// Parent and child share a Registry but each provider owns its endpoint.
			raw = []byte(strings.Replace(string(raw), "'provider.sock'", "'provider-'+json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])['Agent'][-8:]+'.sock'", 1))
			branch := "elif prompt=='nested-create':\n   result=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'create','agent','--host','process','--project','uid:" + f.project + "','--window','uid:" + f.window + "','--provider','claude','--name','nested'],input='',text=True,capture_output=True)\n   result_path=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'nested-result')\n   with open(result_path+'.tmp','w') as result_file:result_file.write(str(result.returncode)+'\\n'+result.stdout+result.stderr)\n   os.replace(result_path+'.tmp',result_path)\n  elif prompt=='register-again':"
			if err = os.WriteFile(script, []byte(strings.Replace(string(raw), "elif prompt=='register-again':", branch, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			args := f.args()
			if mode == "process-chain" {
				args = append(args, "--", "nested-create")
			}
			parent := exec.CommandContext(ctx, f.binary, args...)
			input, err := parent.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := parent.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			parent.Stderr = &stderr
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = input.Close()
				if !waited {
					_ = parent.Process.Kill()
					_ = parent.Wait()
				}
			}()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				t.Fatalf("ownership %v %s", err, stderr.String())
			}
			parentUID := strings.TrimPrefix(strings.Fields(line)[1], "uid:")
			if mode != "process-chain" {
				childArgs := []string{"create", "agent", "--host", "process", "--project", "uid:" + f.project, "--window", "uid:" + f.window, "--provider", "claude", "--name", "nested"}
				if mode == "explicit" {
					childArgs = append(childArgs, "--creator", "uid:"+parentUID)
				}
				out, err := exec.CommandContext(ctx, f.binary, childArgs...).CombinedOutput()
				if err != nil {
					t.Fatalf("create %v %s", err, out)
				}
			} else {
				for {
					raw, err := os.ReadFile(filepath.Join(f.root, "nested-result"))
					if err == nil {
						if !bytes.HasPrefix(raw, []byte("0\n")) {
							t.Fatalf("nested create %s", raw)
						}
						break
					}
					select {
					case <-ctx.Done():
						t.Fatalf("nested create timeout %s", stderr.String())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			var nested *coremetadata.Agent
			for i := range reg.Agents {
				if reg.Agents[i].Metadata.Name == "nested" {
					nested = &reg.Agents[i]
				}
			}
			if nested == nil {
				t.Fatal("nested Agent absent")
			}
			annotations := nested.Metadata.Annotations
			if mode == "unrelated" {
				if annotations[coremetadata.AnnotationCreatorBasis] != "" {
					t.Fatalf("unrelated creator %v", annotations)
				}
			} else if annotations[coremetadata.AnnotationCreatorBasis] != mode || annotations[coremetadata.AnnotationCreatorAgent] != parentUID {
				t.Fatalf("creator %v", annotations)
			}
			pane, _ := reg.Pane(nested.Status.PaneRef)
			for _, key := range []string{coremetadata.AnnotationCreatorAgent, coremetadata.AnnotationCreatorPane, coremetadata.AnnotationCreatorBasis} {
				if pane.Metadata.Annotations[key] != annotations[key] {
					t.Fatalf("Pane creator differs: %s", key)
				}
			}
			_ = input.Close()
			err = parent.Wait()
			waited = true
			if err != nil {
				t.Fatalf("parent Wait %v %s", err, stderr.String())
			}
		})
	}
}

func TestProcessCreatorMalformedFlagActualCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	for _, value := range []string{"", "agent-bare", "a-name"} {
		cmd := exec.Command(f.binary, f.args("--creator", value)...)
		out, err := cmd.CombinedOutput()
		want := fmt.Sprintf("create agent --creator must be an exact Agent reference uid:<agent>; got %q; nothing was created", value)
		if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("value %q: %v %s", value, err, out)
		}
	}
}

// A turn Claude opens itself no longer kills the owned session: its
// permission request stays answerable and operator input over it is busy with
// a named reason. Operator input into a running host turn joins that turn.
func TestProcessClaudeProviderTurnAndJoinedInputActualCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args()...)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	defer func() {
		if t.Failed() {
			raw, _ := os.ReadFile(f.trace)
			t.Logf("wire=%s owner=%s", raw, stderr.String())
		}
	}()
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
		t.Fatal(err)
	}
	ref := strings.Fields(line)[1]
	uid := strings.TrimPrefix(ref, "uid:")
	paths, _ := config.DefaultPathsFromEnv()
	approvals := agentapproval.NewStore(paths.StateDir)
	run := func(args ...string) (string, error) {
		t.Helper()
		out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
		t.Logf("CLI %v: %v %s", args, err, out)
		return string(out), err
	}
	session := func() *coremetadata.ProcessSessionRecord {
		reg, _ := f.store.LoadReadOnly()
		agent, _ := reg.Agent(uid)
		p, _ := reg.Pane(agent.Status.PaneRef)
		return p.Status.ProcessSession
	}
	if _, err = run("agent", "turn", "start", ref, "--", "background"); err != nil {
		t.Fatal(err)
	}
	processCLIUntil(t, ctx, func() bool {
		records, _ := approvals.List(uid)
		return len(records) == 1 && records[0].State == agentapproval.StateWaiting
	})
	processCLIUntil(t, ctx, func() bool {
		s := session()
		return s != nil && strings.HasPrefix(s.TurnID, "provider-") && len(s.Pending) == 1 && s.Pending[0].TurnID == s.TurnID
	})
	if out, err := run("agent", "turn", "start", ref, "--", "over-dialog"); err == nil || !strings.Contains(out, "control-pending") {
		t.Fatalf("input over a provider permission: %v %s", err, out)
	}
	requests, _ := approvals.List(uid)
	if _, err = run("agent", "approval", "answer", ref, requests[0].ID, "--allow"); err != nil {
		t.Fatal(err)
	}
	processCLIUntil(t, ctx, func() bool { s := session(); return s != nil && s.TurnID == "" && len(s.Pending) == 0 })
	out, err := run("agent", "turn", "start", ref, "--", "hold")
	if err != nil || strings.Contains(out, "delivery=joined") {
		t.Fatalf("idle input: %v %s", err, out)
	}
	held := strings.TrimPrefix(strings.Fields(out)[4], "turn=")
	// Until Claude visibly opens the turn, input is a zero-write busy; then it joins.
	joinedOut := ""
	processCLIUntil(t, ctx, func() bool {
		out, err := run("agent", "turn", "start", ref, "--", "joined-input")
		if err != nil && !strings.Contains(out, "process admission capacity exhausted") {
			t.Fatalf("join refused: %v %s", err, out)
		}
		joinedOut = out
		return err == nil
	})
	if want := " runtime=process delivery=joined running-turn=" + held + " origin=host\n"; !strings.HasSuffix(joinedOut, want) {
		t.Fatalf("joined output %q, want suffix %q", joinedOut, want)
	}
	processCLIUntil(t, ctx, func() bool { s := session(); return s != nil && s.TurnID == "" })
	raw, _ := os.ReadFile(f.trace)
	if got := bytes.Count(raw, []byte(`"joined-input"`)); got != 1 {
		t.Fatalf("joined input written %d times", got)
	}
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("owner did not survive the provider turn: %v %s", err, stderr.String())
	}
}

func TestProcessOwnerStopExit143RemainsResumableActualCLI(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			path := filepath.Join(f.root, "provider.py")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Install the handler before the first user frame, then remain alive after
			// stdin EOF so the real supervisor must send TERM. Wait must retain 143.
			provider := strings.Replace(string(raw), "for line in sys.stdin:", "import signal\nsignal.signal(signal.SIGTERM,lambda *_: sys.exit(143))\nfor line in sys.stdin:", 1)
			provider += "\nthreading.Event().wait()\n"
			if err = os.WriteFile(path, []byte(provider), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.binary, f.args("-o", "none", "--", "hello")...)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err = cmd.Start(); err != nil {
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
			var agentUID string
			for {
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				if len(reg.Agents) == 1 {
					pane, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
					if reg.Agents[0].Status.SessionRef != nil && pane.Status.ProcessSession != nil && pane.Status.ProcessSession.SessionID != "" {
						agentUID = reg.Agents[0].Metadata.UID
						break
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal("session was not recorded")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if explicit {
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := reg.Agent(agentUID)
				pane, _ := reg.Pane(agent.Status.PaneRef)
				session := pane.Status.ProcessSession
				socket := processClaudeHostSocket(f.store.Path(), pane.Metadata.UID, session.Binding.Generation)
				identity, err := localipc.InspectOwnedSocket(socket)
				if err != nil {
					t.Fatal(err)
				}
				request := claudeProcessCheck{Foreground: &processForegroundRequest{Authority: processhost.Authority{Binding: processSchemaBinding(session.Binding), Connection: session.ConnectionID, Session: session.SessionID}, Action: "stop"}}
				reply, err := callProcessForeground(ctx, socket, identity, pane.Status.Activation.Process.HostProcess, request)
				if err != nil || !reply.Accepted {
					t.Fatalf("explicit stop: %+v %v", reply, err)
				}
			} else {
				_ = input.Close()
			}
			err = cmd.Wait()
			waited = true
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 143 {
				t.Fatalf("actual Wait: %v %s", err, output.String())
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(agentUID)
			pane, _ := reg.Pane(agent.Status.PaneRef)
			receipt := pane.Status.LastTermination
			if receipt == nil || receipt.Classification != coremetadata.TerminationNormal || receipt.ExitCode == nil || *receipt.ExitCode != 143 || receipt.Signal != "" {
				t.Fatalf("owner TERM lost actual Wait or normal classification: %+v", receipt)
			}
			reason := diagnostics.OwnerStopStdinEOF
			if explicit {
				reason = diagnostics.OwnerStopControl
			}
			assertProcessOwnerStop(t, f.store.Path(), output.String(), reason, cmd.Process.Pid)
			candidates := listResumableProcessAgents(reg, processResumeFilter{})
			if agent.Status.Phase != coremetadata.PhaseOffline || len(candidates) != 1 || candidates[0].Agent.Metadata.UID != agentUID {
				t.Fatalf("owner stop lost resumability: %+v agent=%+v", candidates, agent)
			}
			t.Logf("copied CLI explicit=%t: normal exitCode=%d resumable=%s", explicit, *receipt.ExitCode, candidates[0].Agent.Metadata.UID)
		})
	}
}

func TestProcessVirtualWindowActualCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, route := range []string{"agent", "window"} {
			t.Run(provider+"/"+route, func(t *testing.T) {
				var f processCreateCLI
				if provider == "codex" {
					f = newProcessCodexCreateCLI(t).processCreateCLI
					path := filepath.Join(f.root, "codex-provider.py")
					raw, _ := os.ReadFile(path)
					raw = bytes.ReplaceAll(raw, []byte("p['model']"), []byte("p.get('model','stub-model')"))
					raw = bytes.ReplaceAll(raw, []byte("p['config']['model_reasoning_effort']"), []byte("p.get('config',{}).get('model_reasoning_effort','low')"))
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}

				} else {
					f = newProcessCreateCLI(t)
				}
				// A tmux sentinel records any accidental call; no tmux server is started.
				trace := filepath.Join(f.root, "tmux-calls")
				if err := os.WriteFile(filepath.Join(f.root, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+trace+"'\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error { return coremetadata.Mutator{}.DeleteWindow(reg, f.window) }); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				args := []string{"create", route, "--host", "process", "--provider", provider, "-p", "uid:" + f.project}
				if route == "agent" {
					args = append(args, "--create-window", "--window", "virtual", "--name", "owned")
				} else {
					args = append(args, "--name", "virtual")
				}
				args = append(args, "--", "initial task")
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
				waited := false
				defer func() {
					_ = input.Close()
					if !waited {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				line, err := bufio.NewReader(output).ReadString('\n')
				if err != nil || !strings.Contains(line, "foreground=owned") {
					t.Fatalf("ownership: %q %v %s", line, err, stderr.String())
				}
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				windows := reg.WindowsOf(f.project)
				if len(windows) != 1 || len(reg.Agents) != 1 {
					t.Fatalf("unexpected resources: %+v", reg)
				}
				w := windows[0]
				a := reg.Agents[0]
				if !reg.IsVirtualWindow(w.Metadata.UID) || w.Spec.AnchorPaneRef != a.Status.PaneRef || w.Spec.DefaultShellPaneRef != "" || w.Status.RuntimeID != "" || w.Status.RuntimeSessionID != "" || len(reg.Panes) != 1 {
					t.Fatalf("unexpected topology: %+v", reg)
				}
				if raw, _ := os.ReadFile(trace); len(raw) != 0 {
					t.Fatalf("creation called tmux: %s", raw)
				}
				described, err := exec.CommandContext(ctx, f.binary, "describe", "window", "uid:"+w.Metadata.UID, "-o", "json").CombinedOutput()
				if err != nil {
					t.Fatalf("describe: %v %s", err, described)
				}
				var document map[string]any
				if err = json.Unmarshal(described, &document); err != nil {
					t.Fatal(err)
				}
				spec := document["spec"].(map[string]any)
				if spec["anchorPaneRef"] != a.Status.PaneRef || spec["defaultShellPaneRef"] != nil {
					t.Fatalf("describe spec: %s", described)
				}
				if status, ok := document["status"].(map[string]any); ok && (status["runtimeID"] != nil || status["runtimeSessionID"] != nil) {
					t.Fatalf("describe status: %s", described)
				}
				// describe performs its own read-only tmux inventory. Creation was
				// checked above; reset the sentinel to check foreground retirement.
				_ = os.Remove(trace)
				_ = input.Close()
				err = cmd.Wait()
				waited = true
				if err != nil {
					t.Fatalf("EOF: %v %s", err, stderr.String())
				}
				if raw, _ := os.ReadFile(trace); len(raw) != 0 {
					t.Fatalf("tmux invoked: %s", raw)
				}
			})
		}
	}
}

func TestProcessVirtualWindowRollbackActualCLI(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "incomplete"}[broken], func(t *testing.T) {
			f := newProcessCreateCLI(t)
			run := "exit 7"
			if broken {
				run = "mkdir -p " + filepath.Join(f.root, "state", "projmux", terminationJournalFile) + "; exit 7"
			}
			f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = "+fmt.Sprintf("%q", run)+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			args := []string{"create", "window", "--host", "process", "--provider", "claude", "-p", "uid:" + f.project, "--name", "virtual"}
			out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-post-create-hook-failed")) {
				t.Fatalf("failure: %v %s", err, out)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if broken {
				if len(reg.Windows) != 2 || len(reg.Agents) != 1 {
					t.Fatalf("missing cleanup evidence: %s", out)
				}
				windowUID := reg.Agents[0].Metadata.OwnerUID()
				if !bytes.Contains(out, []byte("remaining window uid:"+windowUID)) || !bytes.Contains(out, []byte("cleanup: projmux delete window uid:"+windowUID)) {
					t.Fatalf("missing exact cleanup: %s", out)
				}
			} else if len(reg.Windows) != 1 || len(reg.Agents) != 0 || len(reg.Panes) != 1 || !bytes.Contains(out, []byte("remaining: none")) {
				t.Fatalf("rollback left resources: %s %+v", out, reg)
			}
		})
	}
}

func TestProcessVirtualWindowProviderRollbackActualCLI(t *testing.T) {
	f := newProcessCodexCreateCLI(t)
	before, _ := f.store.LoadReadOnly()
	path := filepath.Join(f.root, "codex-provider.py")
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte("if method=='initialize':reply({'userAgent':'projmux/0.160.0'})"), []byte("if method=='initialize':emit({'id':n['id'],'error':{'code':-32603,'message':'fixture init refusal'}})"), 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.binary, "create", "agent", "--host", "process", "--provider", "codex", "-p", "uid:"+f.project, "--create-window", "--window", "virtual").CombinedOutput()
	reg, readErr := f.store.LoadReadOnly()
	if err == nil || readErr != nil || len(reg.Windows) != len(before.Windows) || len(reg.Agents) != 0 || len(reg.Panes) != len(before.Panes) || !bytes.Contains(out, []byte("remaining: none")) {
		t.Fatalf("provider rollback: %v %v %s", err, readErr, out)
	}
}

func TestProcessVirtualWindowRefusalsActualCLI(t *testing.T) {
	for _, provider := range []string{"", "shell"} {
		t.Run(provider, func(t *testing.T) {
			f := newProcessCreateCLI(t)
			before, err := os.ReadFile(f.store.Path())
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"create", "window", "--host", "process", "-p", "uid:" + f.project}
			token := "process-window-provider-required"
			if provider != "" {
				args = append(args, "--provider", provider)
				token = "process-window-provider-unsupported"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			after, readErr := os.ReadFile(f.store.Path())
			if !ok || exit.ExitCode() != 2 || !bytes.Contains(out, []byte(token)) || readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal: %v %s", err, out)
			}
		})
	}
}

// The actual copied CLI sends a peer from the exact owned provider child while
// the target's turn runs. Only the native socket sees the coordination frame.
func TestProcessClaudeRunningPeerNativeDeliveryActualCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	path := filepath.Join(f.root, "provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), "elif prompt=='send-message':\n   emit({'type':'result','subtype':'success','session_id':'process-session'})", "elif prompt=='send-message':\n   emit({'type':'assistant','session_id':'process-session','content':'running'})", 1)
	// Keep the native arrival in the existing turn, without a control prompt.
	script = strings.Replace(script, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();continue", 1)
	if err = os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, f.args("--", "send-message")...)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
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
		t.Fatal(err, stderr.String())
	}
	ref := strings.Fields(line)[1]
	uid := strings.TrimPrefix(ref, "uid:")
	var receipt []byte
	processCLIUntil(t, ctx, func() bool {
		receipt, _ = os.ReadFile(filepath.Join(f.root, "message-receipt"))
		return bytes.Contains(receipt, []byte("delivered")) || bytes.Contains(receipt, []byte("held"))
	})
	if !bytes.Contains(receipt, []byte("\tdelivered")) {
		t.Fatalf("running peer %s", receipt)
	}
	messageRef := strings.Fields(string(receipt))[0]
	status, statusErr := exec.CommandContext(ctx, f.binary, "agent", "message", "status", messageRef, "-o", "json").CombinedOutput()
	if statusErr != nil || !bytes.Contains(status, []byte(claudeNativePeerJoinedReason)) {
		t.Fatalf("joined receipt reason %v %s", statusErr, status)
	}
	processCLIUntil(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		agent, _ := reg.Agent(uid)
		pane, _ := reg.Pane(agent.Status.PaneRef)
		session := pane.Status.ProcessSession
		return session != nil && session.SessionID == "process-session" && session.TurnID != ""
	})
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(uid)
	pane, _ := reg.Pane(agent.Status.PaneRef)
	session := pane.Status.ProcessSession
	if session == nil || session.SessionID != "process-session" || session.TurnID == "" || agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatal("native admission replaced active ownership", pane)
	}
	running := session.TurnID
	native, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(native, []byte("untrusted-coordination-only")) != 1 {
		t.Fatalf("native write count %s", native)
	}
	stdin, _ := os.ReadFile(f.trace)
	if bytes.Contains(stdin, []byte("projmux-coordination")) || bytes.Contains(stdin, []byte("peer payload")) {
		t.Fatalf("peer leaked into user stdin %s", stdin)
	}
	// The same registration remains current after delivery; no replacement.
	out, err := exec.CommandContext(ctx, f.binary, "agent", "capabilities", ref, "-o", "json").CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("ended-not-current")) {
		t.Fatalf("registration ended %v %s", err, out)
	}
	out, err = exec.CommandContext(ctx, f.binary, "agent", "turn", "start", ref, "--", "joined-finish").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("running-turn="+running)) {
		t.Fatalf("turn changed %v %s", err, out)
	}
	processCLIUntil(t, ctx, func() bool {
		reg, _ := f.store.LoadReadOnly()
		a, _ := reg.Agent(uid)
		p, _ := reg.Pane(a.Status.PaneRef)
		return p.Status.ProcessSession != nil && p.Status.ProcessSession.TurnID == ""
	})
	_ = input.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("owner died %v %s", err, stderr.String())
	}
}

// assertProcessOwnerStop reads the same journal as diagnostics log. It rejects
// both a missing cause and competing causes for this generation.
func assertProcessOwnerStop(t *testing.T, registryPath, stderr string, reason diagnostics.OwnerStopReason, pid int) {
	t.Helper()
	path := filepath.Join(filepath.Dir(filepath.Dir(registryPath)), diagnostics.LogDirName, diagnostics.LogFileName)
	events, err := diagnostics.NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	var stops []diagnostics.Event
	for _, event := range events {
		if event.Event == "agent.owner.stop" {
			stops = append(stops, event)
		}
	}
	if len(stops) != 1 {
		t.Fatalf("owner causes=%+v, want one", stops)
	}
	got := stops[0]
	if got.Code != "owner.stop."+string(reason) || got.AgentUID == "" || got.PaneUID == "" || got.Generation == "" || got.OwnerPID != pid || got.OwnerPPID <= 0 {
		t.Fatalf("owner cause=%+v want %s pid=%d", got, reason, pid)
	}
	if strings.Count(stderr, "agent owner stop:") != 1 || !strings.Contains(stderr, "reason="+string(reason)) {
		t.Fatalf("owner stderr missing cause: %s", stderr)
	}
}
