package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// CombinedOutput must finish without waiting for detached owner stdio. Each
// provider is a protocol fixture in the existing private, liveguard domain.
func TestDetachedProcessCreateCLIAndExactStop(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var f processCreateCLI
			var args []string
			if provider == "codex" {
				codex := newProcessCodexCreateCLI(t)
				f = codex.processCreateCLI
				args = codex.args()
			} else {
				f = newProcessCreateCLI(t)
				args = f.args()
			}
			args = append(args, "--detach", "--", "initial task")
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("detached create: %v %s", err, output)
			}
			if !bytes.Contains(output, []byte("foreground=detached")) {
				t.Fatalf("ownership: %s", output)
			}
			var ref string
			for word := range strings.FieldsSeq(string(output)) {
				if strings.HasPrefix(word, "uid:agent-") {
					ref = word
					break
				}
			}
			if ref == "" {
				t.Fatalf("no Agent ref: %s", output)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
				defer stop()
				_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", ref).CombinedOutput()
			})
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
			if agent == nil {
				t.Fatal("Agent missing")
			}
			pane, _ := reg.Pane(agent.Status.PaneRef)
			binding := pane.Status.ProcessSession.Binding
			// Two independent CLI reads after the creator has exited retain one binding.
			for range 2 {
				output, err = exec.CommandContext(ctx, f.binary, "describe", "agent", ref).CombinedOutput()
				if err != nil {
					t.Fatalf("describe: %v %s", err, output)
				}
				reg, err = f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				current, _ := reg.Pane(pane.Metadata.UID)
				if current.Status.ProcessSession.Binding != binding || current.Status.Activation.Process == nil {
					t.Fatal("caller exit retired or replaced owner")
				}
			}
			output, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("exact stop: %v %s", err, output)
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			stopped, _ := reg.Agent(agent.Metadata.UID)
			retired, _ := reg.Pane(pane.Metadata.UID)
			if stopped.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(binding, retired.Status.LastTermination) {
				t.Fatal("stop returned without exact Wait and Offline")
			}
			evidence := *retired.Status.LastTermination
			output, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("idempotent stop: %v %s", err, output)
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			retired, _ = reg.Pane(pane.Metadata.UID)
			if !reflect.DeepEqual(*retired.Status.LastTermination, evidence) {
				t.Fatal("already stopped target wrote another Wait")
			}
		})
	}
}

// The parent is a real process-hosted provider fixture, not just a shell whose
// PID changed. Its whole provider process group exits before we use the child.
func TestDetachedProcessChildSurvivesParentProviderGroupCLI(t *testing.T) {
	for _, scenario := range []struct {
		provider string
		signal   syscall.Signal
	}{{"claude", syscall.SIGTERM}, {"codex", syscall.SIGTERM}, {"claude", syscall.SIGHUP}, {"codex", syscall.SIGHUP}} {
		provider := scenario.provider
		t.Run(provider+"/"+scenario.signal.String(), func(t *testing.T) {
			var f processCreateCLI
			if provider == "codex" {
				f = newProcessCodexCreateCLI(t).processCreateCLI
			} else {
				f = newProcessCreateCLI(t)
			}
			script := filepath.Join(f.root, "provider.py")
			raw, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Replace(string(raw), "'provider.sock'", "'provider-'+json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])['Agent'][-8:]+'.sock'", 1)
			branch := "elif prompt=='nested-detached':\n   result=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'create','agent','--host','process','--detach','--project','uid:" + f.project + "','--window','uid:" + f.window + "','--provider','" + provider + "','--profile','none','--name','detached-child','--model','stub-model','--effort','low','--','child initial task'],input='',text=True,capture_output=True)\n   result_path=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'detached-result')\n   with open(result_path+'.tmp','w') as result_file:result_file.write(str(result.returncode)+'\\n'+result.stdout+result.stderr)\n   os.replace(result_path+'.tmp',result_path)\n  elif prompt=='register-again':"
			if err = os.WriteFile(script, []byte(strings.Replace(text, "elif prompt=='register-again':", branch, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			parent := exec.CommandContext(ctx, f.binary, f.args("--", "nested-detached")...)
			input, err := parent.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := parent.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var parentErrors bytes.Buffer
			parent.Stderr = &parentErrors
			if err = parent.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				input.Close()
				if !waited {
					parent.Process.Kill()
					parent.Wait()
				}
			}()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				t.Fatalf("parent startup: %v %s", err, parentErrors.String())
			}
			parentRef := strings.Fields(line)[1]
			processCLIUntil(t, ctx, func() bool {
				raw, err := os.ReadFile(filepath.Join(f.root, "detached-result"))
				if err != nil {
					return false
				}
				if !bytes.HasPrefix(raw, []byte("0\n")) {
					t.Fatalf("nested detached create: %s", raw)
				}
				return true
			})
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			var child *coremetadata.Agent
			for i := range reg.Agents {
				if reg.Agents[i].Metadata.Name == "detached-child" {
					copy := reg.Agents[i].Clone()
					child = &copy
					break
				}
			}
			if child == nil {
				t.Fatal("detached child missing")
			}
			ref := "uid:" + child.Metadata.UID
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
				defer stop()
				_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", ref).CombinedOutput()
			})
			parentAgent, _ := reg.Agent(strings.TrimPrefix(parentRef, "uid:"))
			parentPane, _ := reg.Pane(parentAgent.Status.PaneRef)
			parentActivation := *parentPane.Status.Activation.Process
			childPane, _ := reg.Pane(child.Status.PaneRef)
			childBinding := childPane.Status.ProcessSession.Binding
			if child.Metadata.Annotations[coremetadata.AnnotationCreatorAgent] != parentAgent.Metadata.UID || child.Metadata.Annotations[coremetadata.AnnotationCreatorBasis] != coremetadata.CreatorBasisProcessChain {
				t.Fatalf("caller creator lineage lost: %v", child.Metadata.Annotations)
			}
			// Only this fixture-created provider's own group is targeted.
			if err = syscall.Kill(-parentActivation.Child.PID, scenario.signal); err != nil {
				t.Fatal(err)
			}
			_ = parent.Wait()
			waited = true
			_ = input.Close()
			processCLIUntil(t, ctx, func() bool { return processActivationAbsent(parentActivation, localipc.Process) })
			if err = syscall.Kill(-parentActivation.Child.PID, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("parent provider group still present: %v", err)
			}
			out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "independent child response").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("delivered")) {
				t.Fatalf("coordination after parent group exit: %v %s", err, out)
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			current, _ := reg.Pane(childPane.Metadata.UID)
			if current.Status.ProcessSession.Binding != childBinding || current.Status.Activation.Process == nil {
				t.Fatal("parent group exit replaced or retired detached child")
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("child stop: %v %s", err, out)
			}
		})
	}
}

func TestDetachedProcessCreateWindowCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var f processCreateCLI
			var args []string
			if provider == "codex" {
				codex := newProcessCodexCreateCLI(t)
				f = codex.processCreateCLI
				args = codex.args()
			} else {
				f = newProcessCreateCLI(t)
				args = f.args()
			}
			for i := range args {
				if args[i] == "--window" {
					args[i+1] = "detached-window"
				}
			}
			args = append(args, "--create-window", "--detach", "--", "window initial task")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("create-window: %v %s", err, out)
			}
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			var ref string
			for _, agent := range reg.Agents {
				window, ok := reg.Window(agent.Metadata.OwnerUID())
				if ok && window.Metadata.Name == "detached-window" {
					ref = "uid:" + agent.Metadata.UID
					break
				}
			}
			if ref == "" {
				t.Fatal("Agent not in exact newly created Window")
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
				defer stop()
				_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", ref).CombinedOutput()
			})
			out, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("stop created-window target: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--dry-run").CombinedOutput()
			if err != nil {
				t.Fatalf("dry-run delete: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("delete: %v %s", err, out)
			}
		})
	}
}

func TestDetachedProcessHookFailureRollsBackOnlyNewAgentCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	// Give simultaneous Claude providers independent fixture endpoints.
	script := filepath.Join(f.root, "provider.py")
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "'provider.sock'", "'provider-'+json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])['Agent'][-8:]+'.sock'", 1)
	if err = os.WriteFile(script, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, f.args("--detach")...).CombinedOutput()
	if err != nil {
		t.Fatalf("sibling: %v %s", err, out)
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 1 {
		t.Fatal("sibling missing")
	}
	sibling := reg.Agents[0].Clone()
	pane, _ := reg.Pane(sibling.Status.PaneRef)
	binding := pane.Status.ProcessSession.Binding
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
		defer stop()
		_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", "uid:"+sibling.Metadata.UID).CombinedOutput()
	})
	f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = \"exit 7\"\n")
	args := f.args("--detach")
	for i := range args {
		if args[i] == "--name" {
			args[i+1] = "failed-startup"
		}
	}
	out, err = exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("process-post-create-hook-failed")) || !bytes.Contains(out, []byte("remaining: none")) {
		t.Fatalf("failed hook: %v %s", err, out)
	}
	reg, err = f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	current, _ := reg.Pane(pane.Metadata.UID)
	if len(reg.Agents) != 1 || current.Status.ProcessSession.Binding != binding || current.Status.Activation.Process == nil {
		t.Fatal("failed startup changed sibling")
	}
}

func TestDetachedProcessCallerCancellationRollsBackCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	marker := filepath.Join(f.root, "hook-started")
	f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = "+fmt.Sprintf("%q", "touch "+marker+"; sleep 20")+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, f.binary, f.args("--detach")...)
	var out bytes.Buffer
	command.Stdout = &out
	command.Stderr = &out
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			command.Process.Kill()
			command.Wait()
		}
	}()
	processCLIUntil(t, ctx, func() bool { _, err := os.Stat(marker); return err == nil })
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 1 {
		t.Fatal("startup Agent not reserved")
	}
	pane, _ := reg.Pane(reg.Agents[0].Status.PaneRef)
	activation := *pane.Status.Activation.Process
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	if err == nil || bytes.Contains(out.Bytes(), []byte("foreground=detached")) {
		t.Fatalf("cancelled startup reported success: %v %s", err, out.String())
	}
	processCLIUntil(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		return err == nil && len(reg.Agents) == 0 && processActivationAbsent(activation, localipc.Process)
	})
}

var errDetachedOutputFixture = errors.New("fixture output unavailable")

type detachedOutputFailureWriter struct{}

func (detachedOutputFailureWriter) Write([]byte) (int, error) { return 0, errDetachedOutputFixture }

func TestDetachedProcessOutputFailureWaitsForRollbackCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request := processDetachedRequest{Options: processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, UID: f.project}, Window: selector.Ref{Kind: coremetadata.KindWindow, UID: f.window}, Provider: "claude", Name: "failed-output"}, Caller: identity, Output: cli.OutputModeDefault, RegistryPath: f.store.Path()}
	command := &createCommand{store: newResourceStore()}
	err = launchProcessDetached(ctx, f.binary, request, command, detachedOutputFailureWriter{}, &bytes.Buffer{})
	if !errors.Is(err, errDetachedOutputFixture) || strings.Contains(err.Error(), "cleanup-unconfirmed") {
		t.Fatalf("output failure cleanup: %v", err)
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 0 {
		t.Fatal("output failure returned before its exact rollback")
	}
}

func TestDetachedProcessOwnerEarlyDeathReportsExactUnknownCLI(t *testing.T) {
	f := newProcessCreateCLI(t)
	f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = \"kill -KILL $PPID\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.binary, f.args("--detach")...).CombinedOutput()
	if err == nil || bytes.Contains(out, []byte("foreground=detached")) {
		t.Fatalf("dead owner startup success: %v %s", err, out)
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 1 {
		t.Fatalf("unknown owner lost exact resource: %+v %s", reg.Agents, out)
	}
	agent := reg.Agents[0]
	pane, _ := reg.Pane(agent.Status.PaneRef)
	if !bytes.Contains(out, []byte("remaining agent uid:"+agent.Metadata.UID)) || !bytes.Contains(out, []byte("pane uid:"+pane.Metadata.UID)) {
		t.Fatalf("unknown startup omitted exact recovery refs: %s", out)
	}
	if coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
		t.Fatal("dead owner fabricated Wait")
	}
	stoppedOut, stopErr := exec.CommandContext(ctx, f.binary, "agent", "stop", "uid:"+agent.Metadata.UID).CombinedOutput()
	if stopErr == nil {
		t.Fatalf("unknown owner stop manufactured success: %s", stoppedOut)
	}
	latest, readErr := f.store.LoadReadOnly()
	if readErr != nil {
		t.Fatal(readErr)
	}
	still, _ := latest.Pane(pane.Metadata.UID)
	if still.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding || coremetadata.MatchesProcessWait(still.Status.ProcessSession.Binding, still.Status.LastTermination) {
		t.Fatal("unknown stop altered generation or manufactured Wait")
	}

	if pane.Status.Activation.Process != nil {
		activation := *pane.Status.Activation.Process
		processCLIUntil(t, ctx, func() bool { return processActivationAbsent(activation, localipc.Process) })
	}
	out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", "uid:"+agent.Metadata.UID, "--dry-run").CombinedOutput()
	if err != nil {
		t.Fatalf("unknown cleanup preview: %v %s", err, out)
	}
	out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", "uid:"+agent.Metadata.UID, "--yes").CombinedOutput()
	if err != nil {
		t.Fatalf("unknown cleanup: %v %s", err, out)
	}
}

func TestDetachedProcessConversationResumeAndTranscriptCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
			scriptPath := filepath.Join(f.root, "provider.py")
			conversation := "process-session"
			transcript := filepath.Join(f.root, ".claude", "projects", aisessions.EncodeClaudeProjectPath(f.root), conversation+".jsonl")
			meta := ""
			if provider == "codex" {
				scriptPath = filepath.Join(f.root, "codex-provider.py")
				conversation = "process-thread"
				transcript = filepath.Join(f.root, ".codex", "sessions", "rollout-"+conversation+".jsonl")
				raw, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": conversation, "cwd": f.root, "timestamp": "2026-10-05T00:00:00Z"}})
				meta = string(raw) + "\n"
			}
			script, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			pathJSON, _ := json.Marshal(transcript)
			metaJSON, _ := json.Marshal(meta)
			text := "import os,json\nos.makedirs(os.path.dirname(" + string(pathJSON) + "),exist_ok=True)\nopen(" + string(pathJSON) + ",'w').write(" + string(metaJSON) + ")\n" + string(script)
			response := `{"type":"assistant","message":{"content":"detached fixture reply"}}` + "\n"
			if provider == "claude" {
				responseJSON, _ := json.Marshal(response)
				text = strings.Replace(text, "with write_lock: print(json.dumps(frame),flush=True)", "with write_lock:\n  if frame.get('type')=='assistant':open("+string(pathJSON)+",'a').write("+string(responseJSON)+")\n  print(json.dumps(frame),flush=True)", 1)
				text = strings.Replace(text, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();emit({'type':'result','subtype':'success','session_id':'process-session'});continue", 1)
			} else {
				response = `{"type":"event_msg","payload":{"type":"agent_message","message":"detached fixture reply"}}` + "\n"
				responseJSON, _ := json.Marshal(response)
				text = strings.Replace(text, "if prompt=='controls' or prompt.startswith('{'):", "if prompt=='controls':", 1)
				text = strings.Replace(text, "def complete():open", "def complete():open("+string(pathJSON)+",'a').write("+string(responseJSON)+");notify('item/agentMessage/delta',{'threadId':'process-thread','turnId':current,'itemId':'reply-'+current,'delta':'detached fixture reply'});open", 1)
			}
			if err = os.WriteFile(scriptPath, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--detach", "--", "initial dialogue")...).CombinedOutput()
			if err != nil {
				t.Fatalf("detached dialogue create: %v %s", err, out)
			}
			fields := strings.Fields(string(out))
			if len(fields) < 2 {
				t.Fatalf("no ownership: %s", out)
			}
			ref := fields[1]
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
				defer stop()
				_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", ref).CombinedOutput()
			})
			old := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "")
			})
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
			pane, _ := reg.Pane(agent.Status.PaneRef)
			view, ok := observeProcessHostOwner(reg, f.store.Path(), pane.Clone())
			if !ok || view.OwnerMode != processHostOwnerDetached || view.Binding != processSchemaBinding(old.Binding) {
				t.Fatalf("detached owner binding: %+v", view)
			}
			if provider == "claude" {
				processCLIUntil(t, ctx, func() bool {
					latest, err := f.store.LoadReadOnly()
					if err != nil {
						return false
					}
					_, ready := discoverProcessClaudeProof(f.store.Path(), latest, strings.TrimPrefix(ref, "uid:"))
					return ready
				})
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "continue independent dialogue").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("delivered")) {
				t.Fatalf("peer dialogue: %v %s", err, out)
			}
			processCLIUntil(t, ctx, func() bool {
				raw, err := os.ReadFile(transcript)
				return err == nil && bytes.Contains(raw, []byte("detached fixture reply"))
			})
			out, err = exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", ref, "-o", "json").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte(conversation)) {
				t.Fatalf("detached sessions: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue stop: %v %s", err, out)
			}
			resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", ref, "--", "continue same dialogue"})
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && (r.SessionID != "" || r.ThreadID != "")
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("foreground resume replaced detached dialogue")
			}
			resumed.shutdown(t)
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--dry-run").CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue delete preview: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue delete: %v %s", err, out)
			}
		})
	}
}
