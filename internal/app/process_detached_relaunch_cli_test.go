package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// A Pane init can precede the Agent's durable conversation pointer. Relaunch
// source proofs require both to identify the acknowledged initial conversation.
func awaitDetachedRelaunchInitialConversation(t *testing.T, ctx context.Context, f processCreateCLI, ref string) {
	t.Helper()
	awaitDetachedInitialConversation(t, ctx, f, ref)
	processCLIUntil(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		uid := strings.TrimPrefix(ref, "uid:")
		a, found := reg.Agent(uid)
		p, ambiguous := processResumePane(reg, uid)
		return found && !ambiguous && p != nil && p.Status.ProcessSession != nil && a.Status.Activation.State == coremetadata.ActivationAcknowledged && p.Status.ProcessSession.ConnectionID != "" && p.Status.ProcessSession.TurnID == "" && a.Status.SessionRef.ConversationID() != "" && a.Status.SessionRef.ConversationID() == processRelaunchConversation(p.Status.ProcessSession)
	})
}

func TestDetachedProcessRelaunchSameConversationCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			defer first.shutdown(t)
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
			cleanupDetachedResume(t, f, first.ref)
			// The Codex default shortcut still leaves its current owner alone.
			if provider == "codex" {
				out := detachedResumeCLI(t, ctx, f, "agent", "relaunch", first.ref)
				if !bytes.Contains(out, []byte("unchanged")) {
					t.Fatal(string(out))
				}
			}
			for attempt := range 2 {
				previous, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				previousPane, _ := previous.Pane(old.Binding.PaneUID)
				previousOwner := previousPane.Status.Activation.Process.HostProcess
				args := []string{"agent", "relaunch", first.ref, "--detach", "--yes"}
				if attempt == 1 {
					args = append(args, "-o", "json")
				}
				if provider == "claude" {
					args = append(args, "--", "continue")
				}
				out := detachedResumeCLI(t, ctx, f, args...)
				if !bytes.Contains(out, []byte("foreground=detached")) || bytes.Contains(out, []byte(" unchanged:")) {
					t.Fatal(string(out))
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
				if pane.Status.Activation.Process.HostProcess == previousOwner || !processIdentityAbsent(previousOwner, localipc.Process) {
					t.Fatal("old owner retained or not retired")
				}
				view, ok := observeProcessHostOwner(reg, f.store.Path(), pane.Clone())
				if !ok || view.OwnerMode != processHostOwnerDetached {
					t.Fatalf("owner %+v", view)
				}
				if !coremetadata.MatchesProcessWait(old.Binding, pane.Status.LastTermination) {
					t.Fatal("previous actual Wait missing")
				}
				old = next
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
		})
	}
}

func TestDetachedProcessRelaunchClaimStopAndFrozenPeerCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := deferredResumeCLIFixture(t, "claude")
	diagnostic := newClaudeAuthorityFailureDiagnostics(t)
	diagnostic.paths = []string{f.store.Path()}
	defer diagnostic.beforeCleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	defer first.shutdown(t)
	defer diagnostic.beforeCleanup()
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	old, _ := f.store.LoadReadOnly()
	a, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
	pane, _ := old.Pane(a.Status.PaneRef)
	cleanupDetachedResume(t, f, first.ref)
	out := detachedResumeCLI(t, ctx, f, "agent", "relaunch", first.ref, "--detach", "--model", "frozen-model", "--yes")
	if !bytes.Contains(out, []byte("claim=waiting")) {
		t.Fatal(string(out))
	}
	c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
	claim, err := readDeferredClaim(c.deferredClaimPath(a.Metadata.UID))
	if err != nil || !claim.Detached || !deferredClaimLive(claim) {
		t.Fatalf("claim %+v %v", claim, err)
	}
	reg, _ := f.store.LoadReadOnly()
	retired, _ := reg.Pane(pane.Metadata.UID)
	if !retired.Status.Activation.IsZero() || retired.Status.ProcessSession.Binding != pane.Status.ProcessSession.Binding || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, retired.Status.LastTermination) {
		t.Fatal("claim invented a generation or lacks old Wait")
	}
	before := retired.Clone()
	detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
	reg, _ = f.store.LoadReadOnly()
	retired, _ = reg.Pane(pane.Metadata.UID)
	if retired.Status.ProcessSession == nil || !retired.Status.ProcessSession.StopRequested {
		t.Fatal("child-free stop lost explicit request mark")
	}
	before.Status.ProcessSession.StopRequested = true
	if !reflect.DeepEqual(before.Status, retired.Status) {
		t.Fatal("child-free stop changed status beyond the explicit request mark")
	}
	claim, err = readDeferredClaim(c.deferredClaimPath(a.Metadata.UID))
	if err != nil || claim.Agent != "" {
		t.Fatal("claim not released", err)
	}
	// A fresh claim retains the prepared recipe; its first peer creates the child.
	detachedResumeCLI(t, ctx, f, "agent", "relaunch", first.ref, "--detach")
	source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "peer-source", "--", "source"))
	defer source.shutdown(t)
	defer diagnostic.beforeCleanup()
	awaitDetachedRelaunchInitialConversation(t, ctx, f, source.ref)
	detachedResumeCLI(t, ctx, f, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", "detached-relaunch-peer", "--", "resume frozen conversation")
	deferredCLIStatus(t, ctx, f, "detached-relaunch-peer", "delivered")
	next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != pane.Status.ProcessSession.Binding.Generation && r.ConnectionID != ""
	})
	if next.SessionID != pane.Status.ProcessSession.SessionID {
		t.Fatal("peer replaced conversation")
	}
	reg, _ = f.store.LoadReadOnly()
	a, _ = reg.Agent(a.Metadata.UID)
	if a.Metadata.Annotations[coremetadata.AnnotationAgentModel] != "frozen-model" {
		t.Fatal("frozen recipe changed")
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
}

func TestDetachedProcessRelaunchRefusalsAndPreviewCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := deferredResumeCLIFixture(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	defer first.shutdown(t)
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	before, _ := f.store.LoadReadOnly()
	a, _ := before.Agent(strings.TrimPrefix(first.ref, "uid:"))
	p, _ := before.Pane(a.Status.PaneRef)
	for _, args := range [][]string{{"--detach", "--host", "tmux"}, {"--detach", "--socket-path", "/not-a-process-socket"}} {
		out, err := exec.CommandContext(ctx, f.binary, append([]string{"agent", "relaunch", first.ref}, args...)...).CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte("nothing was changed")) {
			t.Fatalf("%v %v %s", args, err, out)
		}
	}
	out := detachedResumeCLI(t, ctx, f, "agent", "relaunch", first.ref, "--detach", "--dry-run", "-o", "json")
	var preview agentRelaunchResult
	if err := json.Unmarshal(out, &preview); err != nil || !preview.DryRun || preview.Unchanged || preview.Outcome != personaOutcomeWouldRestart {
		t.Fatalf("preview %s %v", out, err)
	}
	after, _ := f.store.LoadReadOnly()
	next, _ := after.Pane(p.Metadata.UID)
	if !reflect.DeepEqual(next.Status.Activation, p.Status.Activation) || next.Status.LastTermination != nil {
		t.Fatal("preview/refusal stopped source")
	}
}

func TestDetachedProcessRelaunchHostTransferCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f, source, _, socket := hostMoveCLIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref := "uid:" + source.Metadata.UID
	cleanupDetachedResume(t, f, ref)
	out := detachedResumeCLI(t, ctx, f, "agent", "relaunch", ref, "--host", "process", "--detach", "--socket-path", socket, "--yes", "--", "continue")
	if !bytes.Contains(out, []byte("foreground=detached")) {
		t.Fatal(string(out))
	}
	next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ConnectionID != "" })
	if next.Binding.AgentUID != source.Metadata.UID || next.SessionID != source.Status.SessionRef.ConversationID() {
		t.Fatal("transfer replaced source")
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
}

type detachedRelaunchCancelWriter struct{ cancel context.CancelFunc }

func (w detachedRelaunchCancelWriter) Write(b []byte) (int, error) { w.cancel(); return len(b), nil }

func TestDetachedProcessRelaunchRollbackCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	for _, mode := range []string{"claim-output", "provider-output", "caller-cancel", "same-recipe-running-output", "stale-source"} {
		t.Run(mode, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, "claude")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			if mode != "same-recipe-running-output" && mode != "stale-source" {
				first.shutdown(t)
			} else {
				defer first.shutdown(t)
			}
			old, _ := f.store.LoadReadOnly()
			agent, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			pane, _ := old.Pane(agent.Status.PaneRef)
			sibling := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "sibling", "--", "sibling"))
			defer sibling.shutdown(t)
			awaitDetachedRelaunchInitialConversation(t, ctx, f, sibling.ref)
			before, _ := f.store.LoadReadOnly()
			other, _ := before.Agent(strings.TrimPrefix(sibling.ref, "uid:"))
			otherPane, _ := before.Pane(other.Status.PaneRef)
			cleanupDetachedResume(t, f, first.ref)
			wire := processDetachedRelaunchRequest{Source: agent.Clone(), Pane: func() *coremetadata.Pane { p := pane.Clone(); return &p }(), Args: []string{first.ref, "--detach", "--model", "rollback-model"}}
			if mode == "same-recipe-running-output" {
				wire.Args = []string{first.ref, "--detach", "--yes"}
			}
			if mode != "claim-output" {
				wire.Args = append(wire.Args, "--", "rollback")
			}
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stale-source" {
				// Reproduce the stale conversation proof without racing readiness.
				wire.Source.Status.SessionRef = nil
				pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				own := os.NewFile(uintptr(pair[0]), "stale-caller")
				child := os.NewFile(uintptr(pair[1]), "stale-owner")
				defer child.Close()
				conn, err := net.FileConn(own)
				_ = own.Close()
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				deadline, _ := ctx.Deadline()
				if err = conn.SetDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				payload, err := os.CreateTemp(f.root, "stale-request-*")
				if err != nil {
					t.Fatal(err)
				}
				defer payload.Close()
				if err = os.Remove(payload.Name()); err != nil {
					t.Fatal(err)
				}
				if err = json.NewEncoder(payload).Encode(processDetachedRequest{Relaunch: &wire, Caller: caller, RegistryPath: f.store.Path()}); err != nil {
					t.Fatal(err)
				}
				if _, err = payload.Seek(0, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				owner := exec.CommandContext(ctx, f.binary, "internal", "process-agent-owner")
				owner.ExtraFiles = []*os.File{child, payload}
				var ownerErrors bytes.Buffer
				owner.Stderr = &ownerErrors
				if err = owner.Start(); err != nil {
					t.Fatal(err)
				}
				_ = child.Close()
				decoder := json.NewDecoder(conn)
				var identity, ready processDetachedReply
				if err = decoder.Decode(&identity); err != nil {
					_ = owner.Process.Kill()
					_ = owner.Wait()
					t.Fatal(err)
				}
				if !identity.Owner.Valid() || identity.Owner.PID != owner.Process.Pid {
					t.Fatal("invalid owner handshake")
				}
				if err = decoder.Decode(&ready); err != nil || ready.Owner != identity.Owner || ready.Error != "stale process binding or request" || ready.Binding.Agent != "" || ready.Claim != nil || ready.Phase != "" || ready.Stdout != "" {
					_ = owner.Process.Kill()
					_ = owner.Wait()
					t.Fatalf("stale source returned startup/claim success: %+v %v", ready, err)
				}
				if err = owner.Wait(); err == nil || !strings.Contains(ownerErrors.String(), "stale process binding or request") {
					t.Fatalf("stale source not refused: %v %s", err, ownerErrors.String())
				}
				if !processIdentityAbsent(identity.Owner, localipc.Process) {
					t.Fatal("rejected owner survived")
				}
				after, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := after.Agent(agent.Metadata.UID)
				p, _ := after.Pane(pane.Metadata.UID)
				otherNext, _ := after.Pane(otherPane.Metadata.UID)
				if !reflect.DeepEqual(a, agent) || !reflect.DeepEqual(p.Status, pane.Status) || !reflect.DeepEqual(otherNext.Status.Activation, otherPane.Status.Activation) {
					t.Fatal("early refusal changed existing owners, conversation or Wait")
				}
				for _, live := range []coremetadata.ProcessIdentity{pane.Status.Activation.Process.HostProcess, pane.Status.Activation.Process.Child} {
					current, _, err := localipc.Process(live.PID)
					if err != nil || current != live {
						t.Fatal("early refusal stopped existing owner/provider", err)
					}
				}
				return
			}
			admission, stop := context.WithCancel(ctx)
			defer stop()
			writer := io.Writer(detachedOutputFailureWriter{})
			if mode == "caller-cancel" {
				writer = detachedRelaunchCancelWriter{stop}
			}
			err = launchProcessDetached(admission, f.binary, processDetachedRequest{Relaunch: &wire, Caller: caller, RegistryPath: f.store.Path()}, &createCommand{store: newResourceStore()}, writer, &bytes.Buffer{})
			if err == nil || strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("rollback %v", err)
			}
			if mode != "caller-cancel" && !errors.Is(err, errDetachedOutputFixture) {
				t.Fatal(err)
			}
			after, _ := f.store.LoadReadOnly()
			a, _ := after.Agent(agent.Metadata.UID)
			p, _ := after.Pane(pane.Metadata.UID)
			otherNext, _ := after.Pane(otherPane.Metadata.UID)
			if a.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(a.Metadata.Annotations, agent.Metadata.Annotations) || p.Status.ProcessSession.SessionID != pane.Status.ProcessSession.SessionID {
				t.Fatalf("rollback lost previous recipe/conversation: error=%v phase=%s annotations=%v previous=%v", err, a.Status.Phase, a.Metadata.Annotations, agent.Metadata.Annotations)
			}
			if !reflect.DeepEqual(otherNext.Status.Activation, otherPane.Status.Activation) {
				t.Fatal("rollback stopped sibling")
			}
			if mode == "claim-output" {
				if !reflect.DeepEqual(p.Status, pane.Status) {
					t.Fatal("child-free rollback invented Wait")
				}
			} else if !coremetadata.MatchesProcessWait(p.Status.ProcessSession.Binding, p.Status.LastTermination) {
				t.Fatal("provider rollback lacks exact Wait")
			}
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			claim, err := readDeferredClaim(c.deferredClaimPath(agent.Metadata.UID))
			if err != nil || claim.Agent != "" {
				t.Fatal("claim leak", err)
			}
		})
	}
}

func TestDetachedProcessRelaunchCodexHostTransferCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f, source, _, socket := codexHostMoveCLIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ref := "uid:" + source.Metadata.UID
	cleanupDetachedResume(t, f, ref)
	detachedResumeCLI(t, ctx, f, "agent", "relaunch", ref, "--host", "process", "--detach", "--socket-path", socket, "--yes")
	record := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ThreadID != "" && r.ConnectionID != "" })
	if record.Binding.AgentUID != source.Metadata.UID || record.ThreadID != source.Status.SessionRef.ConversationID() {
		t.Fatal("writer transfer lost conversation")
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
}

func TestDetachedProcessRelaunchTranscriptCLI(t *testing.T) {
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
				text = patchFixture(t, text, "with write_lock: print(json.dumps(frame),flush=True)", "with write_lock:\n  if frame.get('type')=='assistant':open("+string(pathJSON)+",'a').write("+string(responseJSON)+")\n  print(json.dumps(frame),flush=True)")
				text = patchFixture(t, text, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();emit({'type':'result','subtype':'success','session_id':'process-session'});continue")
			} else {
				response = `{"type":"event_msg","payload":{"type":"agent_message","message":"detached fixture reply"}}` + "\n"
				responseJSON, _ := json.Marshal(response)
				text = patchFixture(t, text, "if prompt=='controls' or prompt.startswith('{'):", "if prompt=='controls':")
				text = patchFixture(t, text, "def complete():open", "def complete():open("+string(pathJSON)+",'a').write("+string(responseJSON)+");notify('item/agentMessage/delta',{'threadId':'process-thread','turnId':current,'itemId':'reply-'+current,'delta':'detached fixture reply'});open")
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
			detachedResumeCLI(t, ctx, f, "agent", "relaunch", ref, "--detach", "--", "continue same dialogue")
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && (r.SessionID != "" || r.ThreadID != "")
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("detached relaunch replaced dialogue")
			}
			before, _ := os.ReadFile(transcript)
			processCLIUntil(t, ctx, func() bool {
				out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "reply after detached relaunch").CombinedOutput()
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

func TestDetachedProcessRelaunchSelfTargetCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := processResumeCLIFixture(t, aiModeClaude)
	path := filepath.Join(f.root, "provider.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := patchFixture(t, string(raw), "elif prompt=='register-again':", `elif prompt=='self-relaunch':
   b=json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])
   p=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'agent','relaunch','uid:'+b['Agent'],'--detach','--model','new-model','--yes','--','unsafe'],capture_output=True)
   open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'self-refusal'),'wb').write(p.stdout+p.stderr)
   emit({'type':'result','subtype':'success','session_id':'process-session'})
  elif prompt=='register-again':`)
	if err = os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "self-relaunch"))
	for {
		text, _ := os.ReadFile(filepath.Join(f.root, "self-refusal"))
		if bytes.Contains(text, []byte(relaunchReasonSelfTarget)) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("self refusal %s", text)
		case <-time.After(10 * time.Millisecond):
		}
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] == "new-model" {
		t.Fatal("self-target recorded settings")
	}
	first.shutdown(t)
	argv, _ := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if len(bytes.Split(bytes.TrimSpace(argv), []byte("\n"))) != 1 {
		t.Fatal("self-target started a second child")
	}
}

func TestDetachedProcessRelaunchStartupFailureCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	first.shutdown(t)
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
	// An executable that fails after spawn must be reaped before restoring settings.
	if err = os.WriteFile(filepath.Join(f.root, "claude"), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--detach", "--model", "rejected-model", "--", "retry task").CombinedOutput()
	if err == nil || bytes.Contains(output, []byte("foreground=detached")) || bytes.Contains(output, []byte("cleanup-unconfirmed")) || !bytes.Contains(output, []byte("recover with:")) || !bytes.Contains(output, []byte("retry task")) {
		t.Fatalf("failure %v %s", err, output)
	}
	after, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := after.Agent(previous.Metadata.UID)
	if agent.Status.Phase != coremetadata.PhaseOffline || !reflect.DeepEqual(previous.Metadata.Annotations, agent.Metadata.Annotations) {
		t.Fatalf("recipe not restored %+v: %s", agent, output)
	}
	pane, _ := processResumePane(after, agent.Metadata.UID)
	if pane.Status.ProcessSession.SessionID != old.SessionID || !pane.Status.Activation.IsZero() {
		t.Fatal("failed writer/conversation leak")
	}
}

func TestDetachedProcessRelaunchBusyRefusalCLI(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f := processResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "question"))
	defer first.shutdown(t)
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return len(r.Pending) > 0 })
	before, _ := f.store.LoadReadOnly()
	wireBefore, _ := os.ReadFile(f.trace)
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--detach", "--", "unconfirmed").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte(relaunchReasonAgentBusy)) || bytes.Contains(out, []byte("cleanup-unconfirmed")) {
		t.Fatalf("busy refusal %v %s", err, out)
	}
	after, _ := f.store.LoadReadOnly()
	wireAfter, _ := os.ReadFile(f.trace)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wireAfter) {
		t.Fatal("busy refusal wrote state/provider frame")
	}
}
