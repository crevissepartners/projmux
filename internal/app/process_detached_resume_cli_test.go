package app

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"github.com/crevissepartners/projmux/internal/cli"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	localstate "github.com/crevissepartners/projmux/internal/state"
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
			expected := p.Clone().Status
			if expected.ProcessSession == nil || expected.ProcessSession.StopRequested {
				t.Fatal("initial natural retirement must have no explicit stop mark")
			}
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
				if !reflect.DeepEqual(current.Status, expected) {
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
				if current.Status.ProcessSession == nil || !current.Status.ProcessSession.StopRequested {
					t.Fatal("claim stop lost explicit request mark")
				}
				expected.ProcessSession.StopRequested = true
				if !reflect.DeepEqual(current.Status, expected) || !reflect.DeepEqual(target.Status, a.Status) {
					t.Fatal("claim stop changed status beyond the explicit request mark")
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
			// Cache only values observed by this test's original calls. Emission
			// does no state/IPC/process reads and never replays a failed poll.
			started := time.Now()
			type validReceipt struct {
				At                     time.Duration
				State, Reason, Unknown string
			}
			type peerPoll struct {
				Ref, Stage                         string
				Started, PollStarted, PollFinished time.Duration
				Polls                              int
				Command, Decode                    string
				LastValid                          validReceipt
			}
			var diagnostic struct {
				Stage, Phase                  string
				BodyPassedBeforeSourceCleanup bool
				StageAt, EmittedAt            time.Duration
				EnqueueRef, EnqueueResult     string
				Record                        struct {
					Sampled                                                 bool
					At                                                      time.Duration
					BindingOwnerSame, GenerationChanged                     bool
					SessionSame, ThreadSame, ConnectionPresent, TurnPresent bool
				}
				Pause struct {
					Started, Finished                          time.Duration
					Inbox, Claim, Identity, Input, Stop, Proof string
					ClaimUnlock, InboxClose, Rescue, Preserve  string
					StopSent, ProofOK                          bool
				}
				NotSampled [4]string
				Peers      [3]peerPoll
			}
			diagnostic.NotSampled = [4]string{"source-record", "host", "lease", "pending"}
			diagnostic.Pause.Inbox, diagnostic.Pause.Claim = "not-sampled", "not-sampled"
			diagnostic.Pause.Identity, diagnostic.Pause.Input = "not-sampled", "not-sampled"
			diagnostic.Pause.Stop, diagnostic.Pause.Proof = "not-sampled", "not-sampled"
			diagnostic.Pause.ClaimUnlock, diagnostic.Pause.InboxClose, diagnostic.Pause.Rescue, diagnostic.Pause.Preserve = "not-called", "not-called", "not-needed", "not-needed"
			for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
				diagnostic.Peers[i] = peerPoll{Ref: ref, Stage: "not-sampled", Command: "not-sampled", Decode: "not-sampled", LastValid: validReceipt{State: "not-sampled", Reason: "not-sampled", Unknown: "not-sampled"}}
			}
			stage := func(name string) {
				diagnostic.Stage, diagnostic.StageAt = name, time.Since(started)
			}
			emitted := false
			emit := func(phase string) {
				if !t.Failed() || emitted {
					return
				}
				emitted = true
				diagnostic.Phase = phase
				diagnostic.EmittedAt = time.Since(started)
				raw, err := json.Marshal(diagnostic)
				const prefix = "detached peer cached failure: "
				if err != nil || len(prefix)+len(raw) > 4096 {
					t.Log("detached peer cached failure: encoding unavailable or size limit")
					return
				}
				t.Log(prefix + string(raw))
			}
			defer func() {
				phase := "setup-fallback"
				if diagnostic.BodyPassedBeforeSourceCleanup {
					phase = "source-cleanup-failure-after-body-pass"
				}
				emit(phase)
			}()
			stage("initial-owned")
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			stage("initial-conversation")
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			stage("initial-wait")
			first.shutdown(t)
			stage("retired-record")
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			stage("source-owned")
			source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source"))
			defer source.shutdown(t)
			defer func() {
				// This boundary precedes the original source shutdown defer.
				// Passing it is not evidence of source cleanup or provider Wait.
				diagnostic.BodyPassedBeforeSourceCleanup = !t.Failed()
				emit("body-failure-before-source-cleanup")
			}()
			cleanupDetachedResume(t, f, first.ref)
			stage("claim-handshake")
			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--wait-for-peer", "--detach")
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			stage("claim-read")
			claim, err := readDeferredClaim(c.deferredClaimPath(old.Binding.AgentUID))
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(claim.Process.PID)
			if err != nil {
				t.Fatal(err)
			}
			// Only this fresh fixture's pre-send, input-free claim may use
			// inbox -> claim order. No Store method may reenter the held inbox.
			sameClaimProcess := func() string {
				identity, _, identityErr := localipc.Process(claim.Process.PID)
				if errors.Is(identityErr, localipc.ErrProcessAbsent) {
					return "absent"
				}
				if identityErr != nil {
					return "unconfirmed"
				}
				if identity != claim.Process {
					return "different"
				}
				return "same"
			}
			// The existing fixture sentinel also preserves other unresolved owned
			// cleanup cases. Never move/delete this or any other fixture root here.
			preservePauseRoot := func() {
				diagnostic.Pause.Preserve = "requested"
				if os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("claim pause cleanup unresolved\n"), 0600) != nil {
					diagnostic.Pause.Preserve = "write-error"
				}
			}
			continueClaim := func() string {
				if identity := sameClaimProcess(); identity != "same" {
					return identity
				}
				if process.Signal(syscall.SIGCONT) != nil {
					return "signal-error"
				}
				return "sent"
			}
			var inboxCloseErr error
			func() {
				diagnostic.Pause.Started = time.Since(started)
				var inboxGuard *os.File
				var claimUnlock func()
				// t.Fatal/Goexit also runs this finalizer, before source.shutdown.
				defer func() {
					if claimUnlock != nil {
						diagnostic.Pause.ClaimUnlock = "called"
						claimUnlock()
						// The func() API hides Close errors; this is not release proof.
						diagnostic.Pause.ClaimUnlock = "returned"
					}
					if inboxGuard != nil {
						inboxCloseErr = inboxGuard.Close()
						diagnostic.Pause.InboxClose = "returned-nil"
						if inboxCloseErr != nil {
							diagnostic.Pause.InboxClose = "returned-error"
						}
					}
					if diagnostic.Pause.StopSent && (!diagnostic.Pause.ProofOK || t.Failed() || inboxCloseErr != nil) {
						diagnostic.Pause.Rescue = continueClaim()
						if diagnostic.Pause.Rescue != "sent" {
							preservePauseRoot()
						}
					}
					if inboxCloseErr != nil {
						preservePauseRoot()
					}
					diagnostic.Pause.Finished = time.Since(started)
				}()
				stage("pause-inbox-guard")
				inbox := messagestore.NewStore(filepath.Dir(filepath.Dir(f.store.Path()))).Path()
				relative, pathErr := filepath.Rel(f.root, inbox)
				if pathErr != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
					diagnostic.Pause.Inbox = "outside-fixture"
					t.Fatal("claim pause inbox outside fixture")
				}
				if err = localstate.EnsurePrivateDir(filepath.Dir(inbox)); err != nil {
					diagnostic.Pause.Inbox = "prepare-error"
					t.Fatal("claim pause inbox directory unavailable")
				}
				inboxGuard, err = localstate.AcquireFileLockContext(ctx, inbox+".flock", localstate.LockWaitLimit, nil)
				if err != nil {
					diagnostic.Pause.Inbox = "acquire-error"
					t.Fatal("claim pause inbox guard unavailable")
				}
				diagnostic.Pause.Inbox = "acquired"
				stage("pause-claim-guard")
				claimPath := c.deferredClaimPath(old.Binding.AgentUID)
				claimUnlock, err = mutationruntime.LockDeferredClaimContext(ctx, claimPath)
				if err != nil {
					diagnostic.Pause.Claim = "acquire-error"
					t.Fatal("claim pause claim guard unavailable")
				}
				diagnostic.Pause.Claim = "acquired"
				stage("pause-authority")
				current, claimErr := readDeferredClaim(claimPath)
				reg, registryErr := f.store.LoadReadOnly()
				pane, paneOK := reg.Pane(old.Binding.PaneUID)
				agent, agentOK := reg.Agent(old.Binding.AgentUID)
				if claimErr != nil || registryErr != nil || !claim.Process.Valid() || claim.Nonce == "" ||
					current.Nonce != claim.Nonce || current.Agent != claim.Agent || current.Agent != old.Binding.AgentUID ||
					current.Process != claim.Process || current.Binding != claim.Binding || current.Binding != old.Binding ||
					current.Provider != claim.Provider || current.Provider != old.Provider ||
					current.Session != claim.Session || current.Session != old.SessionID ||
					current.Thread != claim.Thread || current.Thread != old.ThreadID ||
					!current.Detached || !claim.Detached || current.StopRequested || current.Inflight != "" ||
					!deferredClaimMatches(current, reg, old.Binding.AgentUID) || !paneOK || !agentOK ||
					pane.Status.ProcessSession == nil || !pane.Status.Activation.IsZero() ||
					pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable ||
					agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != pane.Metadata.UID ||
					sameClaimProcess() != "same" {
					diagnostic.Pause.Identity = "refused"
					t.Fatal("claim pause authority changed or unavailable")
				}
				diagnostic.Pause.Identity = "exact"
				input, inputErr := c.readDeferredInput(old.Binding.AgentUID)
				if inputErr != nil {
					diagnostic.Pause.Input = "read-error"
					t.Fatal("claim pause input unavailable")
				}
				if input != nil {
					diagnostic.Pause.Input = "present"
					t.Fatal("claim pause requires no user input")
				}
				diagnostic.Pause.Input = "absent"
				stage("sigstop")
				diagnostic.Pause.Stop = "attempted"
				if err = process.Signal(syscall.SIGSTOP); err != nil {
					diagnostic.Pause.Stop = "signal-error"
					t.Fatal(err)
				}
				diagnostic.Pause.Stop, diagnostic.Pause.StopSent = "sent", true
				t.Cleanup(func() {
					// Absence suppresses the signal; it is not provider Wait evidence.
					if result := continueClaim(); result != "sent" && result != "absent" {
						preservePauseRoot()
					}
				})
				stage("pause-stopped-proof")
				processCLIUntil(t, ctx, func() bool {
					if sameClaimProcess() != "same" {
						diagnostic.Pause.Proof = "identity-error"
						t.Fatal("claim pause identity unavailable before stopped proof")
					}
					out, proofErr := exec.CommandContext(ctx, "/bin/ps", "-o", "state=", "-p", fmt.Sprint(claim.Process.PID)).CombinedOutput()
					if proofErr != nil {
						diagnostic.Pause.Proof = "command-error"
						t.Fatal("claim pause stopped observation unavailable")
					}
					if sameClaimProcess() != "same" {
						diagnostic.Pause.Proof = "identity-error"
						t.Fatal("claim pause identity unavailable after stopped proof")
					}
					if !strings.HasPrefix(strings.TrimSpace(string(out)), "T") {
						diagnostic.Pause.Proof = "not-yet-stopped"
						return false
					}
					diagnostic.Pause.Proof, diagnostic.Pause.ProofOK = "stopped", true
					return true
				})
			}()
			if inboxCloseErr != nil {
				t.Fatal("claim pause inbox close failed")
			}
			for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
				stage("enqueue")
				diagnostic.EnqueueRef, diagnostic.EnqueueResult = ref, "in-flight"
				processCLIUntil(t, ctx, func() bool {
					out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--", "peer-"+string(rune('0'+i))).CombinedOutput()
					diagnostic.EnqueueResult = "other-error"
					if err == nil {
						held := bytes.Contains(out, []byte("held"))
						diagnostic.EnqueueResult = "ok-not-held"
						if held {
							diagnostic.EnqueueResult = "ok-held"
						}
						return held
					}
					if bytes.Contains(out, []byte("source Agent is not eligible")) {
						diagnostic.EnqueueResult = "source-ineligible"
						return false
					}
					t.Fatalf("peer: %v %s", err, out)
					return false
				})
			}
			stage("sigcont")
			if sameClaimProcess() != "same" {
				preservePauseRoot()
				t.Fatal("claim identity unavailable before continue")
			}
			if err = process.Signal(syscall.SIGCONT); err != nil {
				preservePauseRoot()
				t.Fatal(err)
			}
			stage("resumed-record")
			next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				diagnostic.Record.Sampled, diagnostic.Record.At = true, time.Since(started)
				diagnostic.Record.BindingOwnerSame = r.Binding.AgentUID == old.Binding.AgentUID && r.Binding.PaneUID == old.Binding.PaneUID && r.Binding.ProjectUID == old.Binding.ProjectUID && r.Binding.WindowUID == old.Binding.WindowUID
				diagnostic.Record.GenerationChanged = r.Binding.Generation != old.Binding.Generation
				diagnostic.Record.SessionSame, diagnostic.Record.ThreadSame = r.SessionID == old.SessionID, r.ThreadID == old.ThreadID
				diagnostic.Record.ConnectionPresent, diagnostic.Record.TurnPresent = r.ConnectionID != "", r.TurnID != ""
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			stage("conversation-check")
			if next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("peer replaced dialogue")
			}
			// Same Output/JSON/terminal/return operations as deferredCLIStatus;
			// the extra fields are value copies, never aliases of its decode map.
			status := func(index int, ref, state string) map[string]any {
				t.Helper()
				poll := &diagnostic.Peers[index]
				poll.Started = time.Since(started)
				var receipt map[string]any
				processCLIUntil(t, ctx, func() bool {
					poll.Stage = "command"
					poll.Polls++
					poll.PollStarted, poll.Command, poll.Decode = time.Since(started), "in-flight", "not-sampled"
					raw, err := exec.CommandContext(ctx, f.binary, "agent", "message", "status", ref, "-o", "json").Output()
					poll.PollFinished = time.Since(started)
					poll.Stage = "command-returned"
					poll.Command, poll.Decode = "ok", "not-sampled"
					if err != nil {
						poll.Command = "start-or-wait-error"
						var exit *exec.ExitError
						switch {
						case errors.Is(ctx.Err(), context.DeadlineExceeded):
							poll.Command = "context-deadline"
						case errors.Is(ctx.Err(), context.Canceled):
							poll.Command = "context-cancel"
						case errors.As(err, &exit):
							poll.Command = "exit-nonzero"
						}
						return false
					}
					poll.Stage = "decode"
					decodeErr := json.Unmarshal(raw, &receipt)
					if decodeErr != nil {
						poll.Decode = "decode-error"
						var syntax *json.SyntaxError
						var shape *json.UnmarshalTypeError
						if errors.As(decodeErr, &syntax) {
							poll.Decode = "syntax-error"
						} else if errors.As(decodeErr, &shape) {
							poll.Decode = "type-error"
						}
						return false
					}
					delivery, _ := receipt["delivery"].(map[string]any)
					poll.Decode = "valid"
					if delivery == nil {
						poll.Decode = "missing-delivery"
					}
					latest := validReceipt{At: poll.PollFinished, State: "other", Reason: "other", Unknown: "not-sampled"}
					if value, ok := delivery["state"].(string); ok {
						switch value {
						case "held", "accepted", "delivered", "failed", "refused", "expired", "stale":
							latest.State = value
						}
					}
					if value, ok := delivery["reason"].(string); ok {
						switch value {
						case "", "target-awaiting-resume", "target-turn-active", "provider-resume-first-frame", "provider-native-peer-joined", "provider-prewrite-refused", "broker-handoff-persist-failed", "broker-delivery-persist-failed", "provider-handoff-outcome-unknown", "stale-binding", "target-activation-stale", "deadline-expired":
							latest.Reason = value
						}
					}
					if value, ok := delivery["outcomeUnknown"].(bool); ok {
						latest.Unknown = fmt.Sprint(value)
					}
					if delivery != nil {
						poll.LastValid = latest
					}
					poll.Stage = "terminal-check"
					if terminal := delivery["state"]; terminal != "held" && terminal != "accepted" && terminal != state {
						// Every peer ref has the same source, target, and target pane.
						if provider == "claude" {
							logDetachedPeerPrewriteFailure(t, f, raw, ref, strings.TrimPrefix(source.ref, "uid:"), strings.TrimPrefix(first.ref, "uid:"), next.Binding.PaneUID)
						}
						t.Fatalf("unexpected terminal state want %s: %s", state, raw)
					}
					return delivery["state"] == state
				})
				poll.Stage = "complete"
				return receipt
			}
			for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
				stage("receipt-" + ref)
				status(i, ref, "delivered")
			}
			stage("target-stop-wait")
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
			stage("wire-read")
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
			stage("wire-count-order")
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
			diagnostic := newClaudeAuthorityFailureDiagnostics(t)
			if provider == "claude" {
				diagnostic.paths = []string{f.store.Path()}
			}
			defer diagnostic.beforeCleanup()
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

// detachedPeerRef admits only the fixed peer messages PeerOrder sends.
func detachedPeerRef(ref string) bool {
	switch ref {
	case "detached-first", "detached-second", "detached-third":
		return true
	}
	return false
}

// detachedPeerReceiptRef reports whether the original receipt bytes are the
// receipt of the admitted failed peer ref, so the evidence never compares one
// peer's receipt with another peer's durable record.
func detachedPeerReceiptRef(ref string, primary []byte) bool {
	var receipt struct {
		MessageRef string `json:"messageRef"`
	}
	return detachedPeerRef(ref) && json.Unmarshal(primary, &receipt) == nil && receipt.MessageRef == ref
}

// The PeerOrder failure evidence admits each fixed peer ref with its own
// receipt only: never another peer's receipt, and never a ref outside the set.
func TestDetachedPeerFailureEvidenceMatchesItsOwnRef(t *testing.T) {
	t.Parallel()
	refs := []string{"detached-first", "detached-second", "detached-third"}
	for _, ref := range refs {
		for _, other := range refs {
			primary := []byte(`{"messageRef":"` + other + `"}`)
			if got := detachedPeerReceiptRef(ref, primary); got != (ref == other) {
				t.Fatalf("ref %s with %s receipt admitted=%v", ref, other, got)
			}
		}
	}
	for _, ref := range []string{"", "detached-fourth", "Detached-second", "detached-second ", "message-peer"} {
		if detachedPeerRef(ref) || detachedPeerReceiptRef(ref, []byte(`{"messageRef":"`+ref+`"}`)) {
			t.Fatalf("ref %q admitted", ref)
		}
	}
	if detachedPeerReceiptRef("detached-second", []byte("not json")) {
		t.Fatal("undecodable receipt admitted")
	}
}

// This evidence is a new, potentially delayed observation after the original
// terminal assertion, before its Fatal and resource cleanup. It cannot restore
// primary-time state or identify the writer of a later durable record. ref is
// the failed peer message; the receipt check and the durable Get both use it.
func logDetachedPeerPrewriteFailure(t *testing.T, f processCreateCLI, primary []byte, ref, source, target, pane string) {
	t.Helper()
	type getEvidence struct {
		Started, Finished                                                                   time.Time
		Read, Handoff                                                                       string
		Found, Error, AdapterIsClaude, TerminalMatches                                      bool
		StateMatches, ReasonMatches, UnknownMatches                                         bool
		MessageMatches, ConversationMatches, SourceRouteSame, TargetRouteSame, DeadlineSame bool
	}
	// Stage is the refusing proof step a handoff record names, "absent" when
	// it names none, and empty for registration records.
	type eventEvidence struct {
		Event, Source, Code, Stage, Result, At, RunSHA256, Phase string
		AgentIsSource, AgentIsTarget, PaneMatchesTarget          bool
	}
	type evidence struct {
		Phase, Ref                 string
		PrimaryAt, JournalFinished time.Time
		Get                        getEvidence
		Journal                    string
		Records, Matched           int
		Events                     []eventEvidence
	}
	result := evidence{Phase: "post-primary-pre-cleanup", Ref: "refused", PrimaryAt: time.Now(), Journal: "read-unconfirmed"}
	if detachedPeerRef(ref) {
		result.Ref = ref
	}
	result.Get.Read, result.Get.Handoff = "error", "unconfirmed"
	// Cache only the original receipt bytes. No new status command or IPC.
	var receipt struct {
		MessageRef      string               `json:"messageRef"`
		ConversationRef string               `json:"conversationRef"`
		Source          coremessage.Route    `json:"source"`
		Target          coremessage.Route    `json:"target"`
		Deadline        time.Time            `json:"deadline"`
		Delivery        coremessage.Delivery `json:"delivery"`
	}
	primaryValid := detachedPeerReceiptRef(ref, primary) && json.Unmarshal(primary, &receipt) == nil && receipt.Source.Valid() && receipt.Target.Valid() && receipt.Source.Provider == "claude" && receipt.Target.Provider == "claude" && receipt.Source.AgentUID == source && receipt.Target.AgentUID == target && receipt.Target.PaneUID == pane && !receipt.Deadline.IsZero()
	stateDir := filepath.Dir(filepath.Dir(f.store.Path()))
	journal := filepath.Join(stateDir, "logs", "operations.jsonl")
	initial, initialErr := os.Lstat(journal)
	result.Get.Started = time.Now()
	// This existing bounded store may ensure its directory and create/open
	// messages.json.flock. It waits at most two seconds; it does not Apply.
	if !detachedPeerRef(ref) {
		t.Log("detached prewrite evidence: ref-refused")
		return
	}
	record, found, getErr := messagestore.NewBoundedWaitStore(stateDir).Get(ref)
	result.Get.Finished = time.Now()
	g := &result.Get
	g.Found, g.Error = found, getErr != nil
	if getErr == nil {
		g.Read = "missing"
		if found {
			g.Read = "mismatch"
			g.AdapterIsClaude = record.Adapter == "claude-coordination"
			g.TerminalMatches = record.Delivery.State.Terminal() && receipt.Delivery.State.Terminal()
			g.MessageMatches = record.Envelope.MessageRef == receipt.MessageRef && record.Delivery.MessageRef == receipt.MessageRef && receipt.Delivery.MessageRef == receipt.MessageRef
			g.ConversationMatches = receipt.ConversationRef != "" && record.Envelope.ConversationRef == receipt.ConversationRef && record.Delivery.ConversationRef == receipt.ConversationRef && receipt.Delivery.ConversationRef == receipt.ConversationRef
			g.StateMatches = record.Delivery.State == coremessage.StateFailed && receipt.Delivery.State == coremessage.StateFailed
			g.ReasonMatches = record.Delivery.Reason == "provider-prewrite-refused" && receipt.Delivery.Reason == "provider-prewrite-refused"
			g.UnknownMatches = record.Delivery.OutcomeUnknown == receipt.Delivery.OutcomeUnknown && !receipt.Delivery.OutcomeUnknown
			g.SourceRouteSame = record.Envelope.Source.Same(receipt.Source)
			g.TargetRouteSame = record.Envelope.Target.Same(receipt.Target)
			g.DeadlineSame = record.Envelope.Deadline.Equal(receipt.Deadline)
			if primaryValid && g.AdapterIsClaude && g.TerminalMatches && g.MessageMatches && g.ConversationMatches && g.StateMatches && g.ReasonMatches && g.UnknownMatches && g.SourceRouteSame && g.TargetRouteSame && g.DeadlineSame {
				g.Read, g.Handoff = "ok", "not-observed"
				if record.HandoffObserved {
					g.Handoff = "observed"
				}
			}
		}
	}
	// Output is constructed entirely from closed fields. If limits would be
	// exceeded, emit no event rows, including no "first N" partial sample.
	emit := func() {
		result.JournalFinished = time.Now()
		const prefix = "detached prewrite evidence (post-primary; writer/cause unproven): "
		raw, err := json.Marshal(result)
		// Leave 512 bytes for the Go test log's file/line framing. The new
		// diagnostic is emitted as one line; overflow emits counts only.
		if result.Matched > 16 || err != nil || len(prefix)+len(raw) > 8*1024-512 {
			raw, err = json.Marshal(struct {
				Journal          string
				Records, Matched int
			}{"truncated-unconfirmed", result.Records, result.Matched})
		}
		if err != nil || len(prefix)+len(raw) > 8*1024-512 {
			t.Log("detached prewrite evidence: truncated-unconfirmed")
			return
		}
		t.Log(prefix + string(raw))
	}
	defer emit()
	private := func(info os.FileInfo) bool {
		if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		return ok && stat.Uid == uint32(os.Getuid())
	}
	if errors.Is(initialErr, os.ErrNotExist) {
		result.Journal = "missing"
		return
	}
	pre, preErr := os.Lstat(journal)
	if initialErr != nil || preErr != nil || !private(initial) || !private(pre) || !os.SameFile(initial, pre) || initial.Size() != pre.Size() || !initial.ModTime().Equal(pre.ModTime()) {
		result.Journal = "metadata-unconfirmed"
		return
	}
	data, ok := readSDKResumeRefusalJournal(journal, initial)
	post, postErr := os.Lstat(journal)
	if !ok || postErr != nil || !private(post) || !os.SameFile(initial, post) || post.Size() != int64(len(data)) || pre.Size() != post.Size() || !pre.ModTime().Equal(post.ModTime()) {
		result.Journal = "read-unconfirmed"
		return
	}
	if len(data) == 0 {
		result.Journal = "empty"
		return
	}
	if data[len(data)-1] != '\n' {
		result.Journal = "schema-unconfirmed"
		return
	}
	// The same closed recorder pairs as logClaudeFailureRegistration. Keep
	// that helper and the SDK reader unchanged; no product schema extension.
	registrationPairs := map[string]bool{
		"hook:hook-arguments-present":         true,
		"hook:registry-path-invalid":          true,
		"hook:registry-unreadable":            true,
		"helper:registry-unreadable":          true,
		"hook:hook-input-unreadable":          true,
		"hook:payload-not-session-start":      true,
		"hook:pane-binding-mismatch":          true,
		"hook:agent-mismatch":                 true,
		"hook:provider-process-mismatch":      true,
		"hook:messaging-credential-invalid":   true,
		"hook:session-id-embeds-credential":   true,
		"hook:messaging-socket-unavailable":   true,
		"helper:messaging-socket-unavailable": true,
		"hook:nonce-unavailable":              true,
		"hook:authority-invalid":              true,
		"helper:authority-invalid":            true,
		"hook:hook-identity-unavailable":      true,
		"hook:reply-tool-policy-unavailable":  true,
		"hook:helper-executable-unavailable":  true,
		"hook:helper-bootstrap-unavailable":   true,
		"hook:helper-ack-pipe-unavailable":    true,
		"hook:helper-start-failed":            true,
		"hook:helper-admission-unconfirmed":   true,
		"helper:helper-arguments-invalid":     true,
		"helper:helper-ack-missing":           true,
		"helper:helper-ack-not-pipe":          true,
		"helper:helper-input-unreadable":      true,
		"helper:producer-mismatch":            true,
		"helper:bootstrap-invalid":            true,
		"helper:helper-identity-unavailable":  true,
		"helper:lease-unavailable":            true,
		"helper:coordination-unavailable":     true,
		"helper:lease-owner-unavailable":      true,
		"helper:provider-process-gone":        true,
		"helper:claim-refused-activation":     true,
		"helper:claim-refused-competing":      true,
		"helper:claim-refused-newer":          true,
		"helper:lock-timeout":                 true,
		"helper:lock-acquire-failed":          true,
		"helper:registry-degraded":            true,
		"helper:registry-write-failed":        true,
		"helper:route-mismatch":               true,
		"helper:dialogue-broker-unavailable":  true,
		"helper:reply-tool-gate-unavailable":  true,
		"helper:stale-before-ack":             true,
		"helper:ready":                        true,
		"helper:ended-context-done":           true,
		"helper:ended-not-current":            true,
		"helper:ended-accept-failed":          true,
	}
	knownKeys := map[string]bool{}
	eventType := reflect.TypeFor[diagnostics.Event]()
	for i := 0; i < eventType.NumField(); i++ {
		key, _, _ := strings.Cut(eventType.Field(i).Tag.Get("json"), ",")
		if key != "" && key != "-" {
			knownKeys[key] = true
		}
	}
	var rows []eventEvidence
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		fields := json.NewDecoder(bytes.NewReader(line))
		opening, err := fields.Token()
		if err != nil || opening != json.Delim('{') {
			result.Journal = "schema-unconfirmed"
			return
		}
		keys := map[string]bool{}
		for fields.More() {
			key, err := fields.Token()
			name, named := key.(string)
			var value json.RawMessage
			if err != nil || !named || !knownKeys[name] || keys[name] || fields.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				result.Journal = "schema-unconfirmed"
				return
			}
			keys[name] = true
		}
		ending, err := fields.Token()
		var event diagnostics.Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err != nil || ending != json.Delim('}') || fields.Decode(new(any)) != io.EOF || decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
			result.Journal = "schema-unconfirmed"
			return
		}
		result.Records++
		handoff := event.Event == "agent.message.claude-handoff-route" && (event.AgentUID == source || event.AgentUID == target)
		// The source's own helper records sit in the same journal already read;
		// the source pane is not known here, so only the target matches a pane.
		targetRegistration := event.Event == "agent.claude.registration" && event.AgentUID == target && event.PaneUID == pane
		registration := targetRegistration || event.Event == "agent.claude.registration" && event.AgentUID == source
		if !handoff && !registration {
			continue
		}
		shape := true
		for _, key := range []string{"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "source", "code", "agent_uid"} {
			shape = shape && keys[key]
		}
		for key := range keys {
			switch key {
			case "at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "source", "code", "agent_uid", "pane_uid", "kind":
			case "classification":
				shape = shape && handoff
			default:
				shape = false
			}
		}
		at, atErr := time.Parse(time.RFC3339Nano, event.At)
		shape = shape && atErr == nil && event.Component == "agent"
		if handoff {
			shape = shape && !keys["pane_uid"] && event.PaneUID == "" && keys["kind"] && event.Level == "error" && event.Result == "error" && event.Kind == "runtime" && event.DurationMS == 0
			shape = shape && (event.Code == "claude.handoff.source-route-unproven" || event.Code == "claude.handoff.target-route-unproven")
			switch event.Source {
			case "tmux-claude", "tmux-codex", "process-claude", "process-codex", "unknown":
			default:
				shape = false
			}
			switch diagnostics.ClaudeHandoffRouteStage(event.Classification) {
			case "", diagnostics.ClaudeHandoffStageRouteUnresolved, diagnostics.ClaudeHandoffStageRouteFenceMismatch,
				diagnostics.ClaudeHandoffStageCodexAuthorityUnproven, diagnostics.ClaudeHandoffStageProviderProcessMismatch,
				diagnostics.ClaudeHandoffStageLeaseHelperProcessMismatch, diagnostics.ClaudeHandoffStageLeaseAuthorityInvalid,
				diagnostics.ClaudeHandoffStageLeaseProcessMismatch, diagnostics.ClaudeHandoffStageLeaseSocketUnavailable,
				diagnostics.ClaudeHandoffStageLeaseDialStale, diagnostics.ClaudeHandoffStageLeaseDialUnanswered,
				diagnostics.ClaudeHandoffStageLeasePeerMismatch, diagnostics.ClaudeHandoffStageLeaseReadinessStale,
				diagnostics.ClaudeHandoffStageLeaseReadinessUnanswered, diagnostics.ClaudeHandoffStageLeaseNotReady,
				diagnostics.ClaudeHandoffStageLeaseTargetInvalid, diagnostics.ClaudeHandoffStageLeaseProbeStale,
				diagnostics.ClaudeHandoffStageLeaseProbeUnanswered, diagnostics.ClaudeHandoffStageLeaseProbeNotReady,
				diagnostics.ClaudeHandoffStageLeaseUnclassified:
			default:
				shape = false
			}
		} else {
			reason, prefix := strings.CutPrefix(event.Code, "claude.registration.")
			shape = shape && prefix && registrationPairs[event.Source+":"+reason] && keys["pane_uid"] && event.DurationMS >= 0
			if diagnostics.ClaudeRegistrationReason(reason).Refusal() {
				shape = shape && keys["kind"] && event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
			} else {
				shape = shape && !keys["kind"] && event.Level == "info" && event.Result == "success" && event.Kind == ""
			}
		}
		if !shape {
			result.Journal = "schema-unconfirmed"
			return
		}
		result.Matched++
		if result.Matched <= 16 {
			phase := "timestamp-before-primary"
			if !at.Before(result.PrimaryAt) {
				phase = "timestamp-at-or-after-primary"
			}
			stage := ""
			if handoff {
				stage = event.Classification
				if stage == "" {
					stage = "absent"
				}
			}
			rows = append(rows, eventEvidence{Event: event.Event, Source: event.Source, Code: event.Code, Stage: stage, Result: event.Result, At: at.UTC().Format(time.RFC3339Nano), RunSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(event.RunID))), Phase: phase, AgentIsSource: event.AgentUID == source, AgentIsTarget: event.AgentUID == target, PaneMatchesTarget: targetRegistration})
		}
	}
	result.Journal, result.Events = "complete", rows
}
