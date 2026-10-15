package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func detachedResumeCLI(t *testing.T, ctx context.Context, f processCreateCLI, args ...string) []byte {
	t.Helper()
	out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
	return out
}

func cleanupDetachedResume(t *testing.T, f processCreateCLI, ref string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, _ = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
	})
}

// Ownership output precedes asynchronous snapshot persistence. Resume tests
// retire a confirmed conversation, rather than race EOF against initial init.
func awaitDetachedInitialConversation(t *testing.T, ctx context.Context, f processCreateCLI, ref string) {
	t.Helper()
	awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "") && r.TurnID == ""
	})
}

func TestDetachedProcessResumeSameConversationCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first dialogue"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			cleanupDetachedResume(t, f, first.ref)
			for _, mode := range []string{"default", "uid", "receipt", "none"} {
				args := []string{"agent", "resume", first.ref, "--detach"}
				if mode != "default" {
					args = append(args, "-o", mode)
				}
				if provider == "claude" {
					args = append(args, "--", "continue dialogue")
				}
				out := detachedResumeCLI(t, ctx, f, args...)
				if mode == "none" && len(out) != 0 {
					t.Fatalf("none output: %s", out)
				}
				if mode != "none" && !bytes.Contains(out, []byte("foreground=detached")) {
					t.Fatalf("ownership: %s", out)
				}
				next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
				})
				if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
					t.Fatal("conversation replaced")
				}
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				pane, _ := reg.Pane(next.Binding.PaneUID)
				view, ok := observeProcessHostOwner(reg, f.store.Path(), pane.Clone())
				if !ok || view.OwnerMode != processHostOwnerDetached {
					t.Fatalf("owner: %+v", view)
				}
				duplicate, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach", "--", "competing").CombinedOutput()
				if err == nil || !bytes.Contains(duplicate, []byte("process-resume-owned")) {
					t.Fatalf("competing: %v %s", err, duplicate)
				}
				detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
				retired := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
				reg, err = f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := reg.Agent(next.Binding.AgentUID)
				p, _ := reg.Pane(next.Binding.PaneUID)
				if a.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(next.Binding, p.Status.LastTermination) || !coremetadata.SameProcessWait(p.Status.LastTermination, a.Status.LastTermination) {
					t.Fatal("stop lacks actual Wait")
				}
				old = retired
			}
			// Detached generations still return to legacy EOF-owned resume.
			legacy := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "legacy foreground"})
			legacy.shutdown(t)
		})
	}
}

func TestDetachedProcessResumeClaimStopWithoutChildCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, _ := f.store.LoadReadOnly()
			a, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			p, _ := old.Pane(a.Status.PaneRef)
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			path := c.deferredClaimPath(a.Metadata.UID)
			cleanupDetachedResume(t, f, first.ref)
			for _, mode := range []string{"default", "uid", "json", "receipt", "none"} {
				args := []string{"agent", "resume", first.ref, "--wait-for-peer", "--detach"}
				if mode != "default" {
					args = append(args, "-o", mode)
				}
				out := detachedResumeCLI(t, ctx, f, args...)
				if (mode != "none" && !bytes.Contains(out, []byte("claim=waiting"))) || (mode == "none" && len(out) != 0) {
					t.Fatalf("no claim output: %s", out)
				}
				if mode == "default" && !bytes.Contains(out, []byte("agent/"+a.Metadata.Name+" reused")) {
					t.Fatalf("claim kind projection: %s", out)
				}
				claim, err := readDeferredClaim(path)
				if err != nil || !claim.Detached || !deferredClaimLive(claim) {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				after, _ := f.store.LoadReadOnly()
				current, _ := after.Pane(p.Metadata.UID)
				if !reflect.DeepEqual(current.Status, p.Status) {
					t.Fatal("claim invented child/generation/Wait")
				}
				for _, args := range [][]string{{"agent", "resume", first.ref, "--wait-for-peer", "--detach"}, {"agent", "resume", first.ref, "--detach", "--", "other"}} {
					out, err = exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
					if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) {
						t.Fatalf("claim competition: %v %s", err, out)
					}
				}
				detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
				if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("claim remains: %v", err)
				}
				if !processIdentityAbsent(claim.Process, localipc.Process) {
					t.Fatal("claimant still alive")
				}
				after, _ = f.store.LoadReadOnly()
				current, _ = after.Pane(p.Metadata.UID)
				target, _ := after.Agent(a.Metadata.UID)
				if !reflect.DeepEqual(current.Status, p.Status) || !reflect.DeepEqual(target.Status, a.Status) {
					t.Fatal("claim stop invented Wait or changed conversation")
				}
			}
		})
	}
}

func TestDetachedProcessResumeClaimPeerOrderCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source"))
			defer source.shutdown(t)
			cleanupDetachedResume(t, f, first.ref)
			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--wait-for-peer", "--detach")
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			claim, err := readDeferredClaim(c.deferredClaimPath(old.Binding.AgentUID))
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(claim.Process.PID)
			if err != nil {
				t.Fatal(err)
			}
			if err = process.Signal(syscall.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Signal(syscall.SIGCONT) })
			for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
				processCLIUntil(t, ctx, func() bool {
					out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--", "peer-"+string(rune('0'+i))).CombinedOutput()
					if err == nil {
						return bytes.Contains(out, []byte("held"))
					}
					if bytes.Contains(out, []byte("source Agent is not eligible")) {
						return false
					}
					t.Fatalf("peer: %v %s", err, out)
					return false
				})
			}
			if err = process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("peer replaced dialogue")
			}
			for _, ref := range []string{"detached-first", "detached-second", "detached-third"} {
				deferredCLIStatus(t, ctx, f, ref, "delivered")
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
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
			if provider == "claude" {
				raw, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
					var frame map[string]any
					if json.Unmarshal(line, &frame) != nil {
						t.Fatal(err)
					}
					peerFrames = append(peerFrames, frame["message"].(map[string]any)["content"].(string))
				}
			}
			if len(peerFrames) != 3 {
				t.Fatalf("peer count: %q", peerFrames)
			}
			for i, text := range peerFrames {
				var frame map[string]any
				_ = json.Unmarshal([]byte(text), &frame)
				if frame["messageRef"] != []string{"detached-first", "detached-second", "detached-third"}[i] || frame["authority"] != "untrusted-coordination-only" || frame["payload"] != fmt.Sprintf("peer-%d", i) {
					t.Fatal("peer promoted or reordered", frame)
				}
			}

		})
	}
}

func TestDetachedProcessResumeFailurePreservesConversationCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, _ := f.store.LoadReadOnly()
			for _, args := range [][]string{{"agent", "resume", first.ref, "--detach", "-o", "pane-id"}, {"agent", "resume", first.ref, "--detach", "-o", "invalid"}, {"agent", "resume", first.ref, "--detach", "--dialogue-reply-only"}, {"agent", "resume", first.ref, "--detach", "--wait-for-peer", "--", "invalid"}} {
				out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
				if err == nil {
					t.Fatalf("invalid accepted: %s", out)
				}
				after, _ := f.store.LoadReadOnly()
				if !reflect.DeepEqual(after, old) {
					t.Fatal("invalid arguments mutated Registry")
				}
			}
			cleanupDetachedResume(t, f, first.ref)
			a, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			originalPane, _ := old.Pane(a.Status.PaneRef)
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			wire := processDetachedResumeRequest{Agent: a.Metadata.UID, Prompt: processResumeFirstFrame{Kind: "user", Text: "rollback"}, Previous: originalPane.Status.ProcessSession.Binding}
			command := &createCommand{store: newResourceStore()}
			err = launchProcessDetached(ctx, f.binary, processDetachedRequest{Resume: &wire, Caller: caller, Output: cli.OutputModeDefault, RegistryPath: f.store.Path()}, command, detachedOutputFailureWriter{}, &bytes.Buffer{})
			if !errors.Is(err, errDetachedOutputFixture) || strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("output rollback: %v", err)
			}
			after, _ := f.store.LoadReadOnly()
			target, _ := after.Agent(a.Metadata.UID)
			if target == nil || target.Status.Phase != coremetadata.PhaseOffline {
				t.Fatal("Agent not preserved offline")
			}
			pane, _ := after.Pane(target.Status.PaneRef)
			if pane == nil {
				t.Fatal("Pane not preserved")
			}
			original, _ := old.Pane(a.Status.PaneRef)
			if pane.Status.ProcessSession.SessionID != original.Status.ProcessSession.SessionID || pane.Status.ProcessSession.ThreadID != original.Status.ProcessSession.ThreadID || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
				t.Fatal("rollback lost dialogue or Wait")
			}
			// A rollback is retryable by the same public path, with no new conversation.
			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--", "retry")
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
		})
	}
}

func TestDetachedProcessResumeTranscriptCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
			scriptPath := filepath.Join(f.root, "provider.py")
			conversation := "process-session"
			transcript := filepath.Join(f.root, ".claude", "projects", aisessions.EncodeClaudeProjectPath(f.root), conversation+".jsonl")
			meta := ""
			if provider == "codex" {
				prepareProcessCodexPeerCLI(t, f)
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
			text := "import os,json\nos.makedirs(os.path.dirname(" + string(pathJSON) + "),exist_ok=True)\nopen(" + string(pathJSON) + ",'a').write(" + string(metaJSON) + ")\n" + string(script)
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
			detachedResumeCLI(t, ctx, f, "agent", "resume", ref, "--detach", "--", "continue same dialogue")
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && (r.SessionID != "" || r.ThreadID != "")
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("detached resume replaced dialogue")
			}
			before, _ := os.ReadFile(transcript)
			processCLIUntil(t, ctx, func() bool {
				out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "reply after detached resume").CombinedOutput()
				if err == nil {
					return bytes.Contains(out, []byte("delivered"))
				}
				if bytes.Contains(out, []byte("not eligible")) || bytes.Contains(out, []byte("registration lease")) {
					return false
				}
				t.Fatalf("resumed peer: %v %s", err, out)
				return false
			})
			processCLIUntil(t, ctx, func() bool {
				raw, err := os.ReadFile(transcript)
				return err == nil && len(raw) > len(before) && bytes.Contains(raw, []byte("detached fixture reply"))
			})
			out, err = exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", ref, "-o", "json").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte(conversation)) {
				t.Fatalf("resumed sessions: %v %s", err, out)
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
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

func TestDetachedProcessResumeClaimOutputRollbackCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			pane, _ := old.Pane(agent.Status.PaneRef)
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			wire := processDetachedResumeRequest{Agent: agent.Metadata.UID, WaitForPeer: true, Previous: pane.Status.ProcessSession.Binding}
			command := &createCommand{store: newResourceStore()}
			err = launchProcessDetached(ctx, f.binary, processDetachedRequest{Resume: &wire, Caller: caller, Output: cli.OutputModeDefault, RegistryPath: f.store.Path()}, command, detachedOutputFailureWriter{}, &bytes.Buffer{})
			if !errors.Is(err, errDetachedOutputFixture) || strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("claim rollback: %v", err)
			}
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			if _, err = os.Stat(c.deferredClaimPath(agent.Metadata.UID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim leaked: %v", err)
			}
			current, err := f.store.LoadReadOnly()
			if err != nil || !reflect.DeepEqual(current, old) {
				t.Fatal("child-free rollback changed Registry", err)
			}
		})
	}
}

func TestDetachedProcessResumeStaleClaimStopCLI(t *testing.T) {
	f := deferredResumeCLIFixture(t, "codex")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "codex", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	awaitDetachedInitialConversation(t, ctx, f, first.ref)
	first.shutdown(t)
	cleanupDetachedResume(t, f, first.ref)
	detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--wait-for-peer")
	c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
	path := c.deferredClaimPath(strings.TrimPrefix(first.ref, "uid:"))
	claim, err := readDeferredClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(claim.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released && deferredClaimLive(claim) {
			_ = writeDeferredClaim(path, claim)
			_ = process.Signal(syscall.SIGCONT)
		}
	})
	changed := claim
	changed.Binding.Generation = "gen-changed"
	if err = writeDeferredClaim(path, changed); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref).CombinedOutput()
	if err == nil {
		t.Fatalf("stale claim stop succeeded: %s", out)
	}
	after, _ := f.store.LoadReadOnly()
	if !reflect.DeepEqual(before, after) || !deferredClaimLive(claim) {
		t.Fatal("stale stop signaled owner or changed Wait")
	}
	if err = writeDeferredClaim(path, claim); err != nil {
		t.Fatal(err)
	}
	if err = process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
	released = true
}
