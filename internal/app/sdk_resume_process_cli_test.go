package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// SDK resume cases use the same copied binary as the standard CLI suite.
func TestSDKDetachedProcessResumeFrozenDomainsAndClaims(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		for _, domain := range []string{"a", "b"} {
			t.Run(provider+"-"+domain, func(t *testing.T) {
				f := deferredResumeCLIFixture(t, provider)
				paths := config.Paths{ConfigDir: filepath.Join(f.root, domain, "deep", "settings"), StateDir: filepath.Join(f.root, domain, "deep", "records")}
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				original, err := os.ReadFile(f.store.Path())
				if err != nil {
					t.Fatal(err)
				}
				storage := metadata.NewStore(metadata.PathFor(paths.StateDir))
				if _, err = storage.Update(func(r *m.Registry) error { *r = reg.Clone(); return nil }); err != nil {
					t.Fatal(err)
				}
				c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary})
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
					t.Setenv(key, f.root+"/poison")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				created, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Payload: []string{"first dialogue"}})
				if err != nil {
					t.Fatal(err)
				}
				ref := "uid:" + created.Agent.Metadata.UID
				t.Cleanup(func() {
					clean, done := context.WithTimeout(context.Background(), 15*time.Second)
					defer done()
					if _, err := c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); err != nil {
						_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("SDK resume cleanup unconfirmed"), 0600)
						t.Error(err)
					}
				})
				await := func(predicate func(core.Pane) bool) core.Pane {
					t.Helper()
					for {
						resource, e := c.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + created.Binding.PaneUID})
						if e != nil {
							t.Fatal(e)
						}
						pane := resource.(core.Pane)
						if predicate(pane) {
							return pane
						}
						select {
						case <-ctx.Done():
							t.Fatal("SDK observation timed out")
							return pane
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				initial := await(func(p core.Pane) bool {
					return p.Status.ProcessSession != nil && p.Status.ProcessSession.ConnectionID != "" && (p.Status.ProcessSession.SessionID != "" || p.Status.ProcessSession.ThreadID != "") && p.Status.ProcessSession.TurnID == ""
				})
				for {
					resource, e := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
					if e != nil {
						t.Fatal(e)
					}
					confirmed := resource.(core.Agent)
					if confirmed.Status.SessionRef != nil {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("initial stream session was not confirmed")
					case <-time.After(10 * time.Millisecond):
					}
				}
				stopped, err := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: created.Binding})
				if err != nil || !stopped.Stopped || stopped.Termination == nil {
					t.Fatalf("initial Stop: %+v %v", stopped, err)
				}
				retired, e := storage.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				retiredPane, _ := retired.Pane(created.Binding.PaneUID)
				t.Logf("initial retired provider=%s session=%+v termination=%+v", provider, retiredPane.Status.ProcessSession, retiredPane.Status.LastTermination)
				if retiredPane.Status.ProcessSession.ResumeState != m.ProcessResumable {
					t.Fatal("confirmed initialized conversation did not become resumable after Stop")
				}
				before, err := os.ReadFile(storage.Path())
				if err != nil {
					t.Fatal(err)
				}
				stale := created.Binding
				stale.Generation = "stale"
				for _, req := range []core.ResumeAgentRequest{{Ref: ref, Detach: true, Binding: stale}, {Ref: ref}, {Ref: ref, Detach: true, DialogueReplyOnly: true}, {Ref: ref, Detach: true, WaitForPeer: true, Prompt: "forbidden"}, {Ref: ref, Detach: true, SocketPath: "/no/socket"}} {
					if _, e := c.ResumeAgent(ctx, req); e == nil {
						t.Fatalf("accepted unsupported request %+v", req)
					}
				}
				canceled, done := context.WithCancel(ctx)
				done()
				if _, e := c.ResumeAgent(canceled, core.ResumeAgentRequest{Ref: ref, Detach: true}); !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
				after, e := os.ReadFile(storage.Path())
				if e != nil || !bytes.Equal(before, after) {
					t.Fatal("preflight changed registry", e)
				}
				req := core.ResumeAgentRequest{Ref: ref, Detach: true, Binding: created.Binding}
				if provider == "claude" {
					req.Prompt = "continue dialogue"
				}
				startup, finish := context.WithCancel(ctx)
				resumed, err := c.ResumeAgent(startup, req)
				finish()
				if err != nil {
					t.Fatal(err)
				}
				if resumed.Binding.AgentUID != created.Binding.AgentUID || resumed.Binding.PaneUID != created.Binding.PaneUID || resumed.Binding.Generation == created.Binding.Generation || resumed.ClaimWaiting || resumed.MayBeTruncated || resumed.PaneID != "" || len(resumed.Projection.Items) != 1 {
					t.Fatalf("resume result %+v", resumed)
				}
				born := await(func(p core.Pane) bool {
					return p.Status.Activation.Process != nil && p.Status.ProcessSession.Binding.Generation == resumed.Binding.Generation && p.Status.ProcessSession.ConnectionID != "" && (p.Status.ProcessSession.SessionID != "" || p.Status.ProcessSession.ThreadID != "") && p.Status.ProcessSession.TurnID == ""
				})
				if born.Status.ProcessSession.SessionID != initial.Status.ProcessSession.SessionID || born.Status.ProcessSession.ThreadID != initial.Status.ProcessSession.ThreadID {
					t.Fatal("conversation replaced")
				}
				if _, e = c.ResumeAgent(ctx, req); e == nil {
					t.Fatal("stale binding accepted while running")
				}
				stopped, err = c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: resumed.Binding})
				if err != nil || !stopped.Stopped || stopped.Termination == nil {
					t.Fatalf("resumed Stop: %+v %v", stopped, err)
				}
				t.Run("child-free Stop", func(t *testing.T) {
					claim, e := c.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, WaitForPeer: true, Binding: resumed.Binding})
					if e != nil {
						t.Fatal(e)
					}
					if !claim.ClaimWaiting || claim.MayBeTruncated || claim.Binding != resumed.Binding {
						t.Fatalf("claim %+v", claim)
					}
					stop, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: claim.Binding})
					if e != nil || !stop.ClaimReleased || !reflect.DeepEqual(stop.Termination, stopped.Termination) {
						t.Fatalf("claim Stop: %+v %v", stop, e)
					}
					reg, e := storage.LoadReadOnly()
					if e != nil {
						t.Fatal(e)
					}
					pane, _ := reg.Pane(claim.Binding.PaneUID)
					if pane.Status.LastTermination == nil || pane.Status.LastTermination.Generation != stopped.Termination.Generation || !pane.Status.LastTermination.ObservedAt.Equal(stopped.Termination.ObservedAt) || pane.Status.LastTermination.OperationID != stopped.Termination.OperationID {
						t.Fatal("claim invented termination")
					}
				})
				t.Run("first peer birth", func(t *testing.T) {
					source, e := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Name: "peer-source", Payload: []string{"source dialogue"}})
					if e != nil {
						t.Fatal(e)
					}
					sourceRef := "uid:" + source.Agent.Metadata.UID
					defer func() {
						clean, done := context.WithTimeout(context.Background(), 15*time.Second)
						defer done()
						if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: sourceRef, Binding: source.Binding}); e != nil {
							t.Error(e)
						}
					}()

					for {
						resource, e := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: sourceRef})
						if e != nil {
							t.Fatal(e)
						}
						live := resource.(core.Agent)
						if live.Status.Phase == core.PhaseRunning && live.Status.SessionRef != nil {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("peer source session not ready")
						case <-time.After(10 * time.Millisecond):
						}
					}

					claim, e := c.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, WaitForPeer: true, Binding: resumed.Binding})
					if e != nil {
						t.Fatal(e)
					}
					if !claim.ClaimWaiting || claim.MayBeTruncated {
						t.Fatal("missing claim handshake")
					}
					receipt, e := c.SendMessage(ctx, projmux.SendMessageRequest{Source: sourceRef, Target: ref, Text: "peer payload"})
					if e != nil {
						t.Fatalf("first peer %+v %v", receipt, e)
					}
					born := await(func(p core.Pane) bool {
						return p.Status.Activation.Process != nil && p.Status.ProcessSession.Binding.Generation != resumed.Binding.Generation && p.Status.ProcessSession.ConnectionID != "" && (p.Status.ProcessSession.SessionID != "" || p.Status.ProcessSession.ThreadID != "") && p.Status.ProcessSession.TurnID == ""
					})
					if born.Status.ProcessSession.SessionID != initial.Status.ProcessSession.SessionID || born.Status.ProcessSession.ThreadID != initial.Status.ProcessSession.ThreadID {
						t.Fatal("peer birth lost conversation")
					}
					stop, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref})
					if e != nil || !stop.Stopped || stop.Termination == nil || stop.ClaimReleased {
						t.Fatalf("born Stop: %+v %v", stop, e)
					}
				})
				after, e = os.ReadFile(f.store.Path())
				if e != nil || !bytes.Equal(original, after) {
					t.Fatal("default Registry changed", e)
				}
			})
		}
	}
}

func TestSDKDetachedProcessResumeIdentityDeadlinePreservesFailure(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "silent-launcher")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{ConfigDir: root + "/deep/settings", StateDir: root + "/deep/records"}
	storage := metadata.NewStore(metadata.PathFor(paths.StateDir))
	reg := processResumeQueryFixture(t)
	if _, err := storage.Update(func(r *m.Registry) error { *r = reg.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storage.Path())
	if err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	ctx := &sdkIdentitySocketFirstContext{Context: context.Background(), deadline: time.Now().Add(-time.Second), done: make(chan struct{})}
	result, err := c.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:" + reg.Agents[0].Metadata.UID, Detach: true, Prompt: "continue"})
	var refusal *projmux.Refusal
	var detail *projmux.ResumeProcessError
	var transport *net.OpError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &transport) || !errors.As(err, &refusal) || !errors.As(err, &detail) || refusal.Code != projmux.CodeResumeExecutionFailed || detail.Code != refusal.Code {
		t.Fatalf("typed timeout cause lost: %T %v", err, err)
	}
	if result.Binding != (core.ProcessBinding{}) || detail.Owner != (core.ProcessIdentity{}) || result.Agent.Metadata.UID != "" {
		t.Fatalf("identity failure invented resources %+v %+v", result, detail)
	}
	after, readErr := os.ReadFile(storage.Path())
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("identity timeout changed Registry", readErr)
	}
}

func TestSDKDetachedProcessResumeCancellationSettlesBornGeneration(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
			if err != nil {
				t.Fatal(err)
			}
			ctx, done := context.WithTimeout(context.Background(), 35*time.Second)
			defer done()
			created, err := c.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:" + f.project, Window: "uid:" + f.window, Provider: provider, Profile: "none", Model: "stub-model", Effort: "low", Host: "process", Detach: true, Payload: []string{"initial"}})
			if err != nil {
				t.Fatal(err)
			}
			ref := "uid:" + created.Agent.Metadata.UID
			t.Cleanup(func() {
				clean, finish := context.WithTimeout(context.Background(), 15*time.Second)
				defer finish()
				if _, e := c.StopAgent(clean, core.StopAgentRequest{Ref: ref}); e != nil {
					t.Error(e)
					_ = os.WriteFile(filepath.Join(f.root, firstInputCleanupPending), []byte("resume cancellation cleanup unconfirmed"), 0600)
				}
			})
			processCLIUntil(t, ctx, func() bool {
				r, e := c.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
				if e != nil {
					t.Fatal(e)
				}
				return r.(core.Agent).Status.SessionRef != nil
			})
			if stopped, e := c.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: created.Binding}); e != nil || !stopped.Stopped {
				t.Fatalf("initial Stop %+v %v", stopped, e)
			}
			// Birth succeeds, but provider initialization waits for owner cancellation.
			python, e := exec.LookPath("python3")
			if e != nil {
				t.Fatal(e)
			}
			script := fmt.Sprintf("#!/bin/sh\nexec %q -u -c 'import sys; sys.stdin.buffer.read()'\n", python)
			if e = os.WriteFile(filepath.Join(f.root, provider), []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			startup, cancel := context.WithCancel(ctx)
			defer cancel()
			type outcome struct {
				result core.ResumeAgentResult
				err    error
			}
			returned := make(chan outcome, 1)
			go func() {
				r, e := c.ResumeAgent(startup, core.ResumeAgentRequest{Ref: ref, Detach: true, Binding: created.Binding, Prompt: "resume"})
				returned <- outcome{r, e}
			}()
			var activation m.ProcessActivation
			processCLIUntil(t, ctx, func() bool {
				reg, e := f.store.LoadReadOnly()
				if e != nil {
					t.Fatal(e)
				}
				pane, _ := reg.Pane(created.Binding.PaneUID)
				if pane.Status.Activation.Process == nil || pane.Status.ProcessSession.Binding.Generation == created.Binding.Generation {
					return false
				}
				activation = *pane.Status.Activation.Process
				return true
			})
			cancel()
			var out outcome
			select {
			case out = <-returned:
			case <-ctx.Done():
				t.Fatal("born startup cancellation did not settle")
			}
			var detail *projmux.ResumeProcessError
			if !errors.Is(out.err, context.Canceled) || !errors.As(out.err, &detail) || out.result.Binding.Generation != activation.Binding.Generation || detail.Binding != out.result.Binding {
				t.Fatalf("startup cause/binding lost: result=%+v detail=%+v error=%v", out.result.Binding, detail, out.err)
			}
			if strings.Contains(out.err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("actual retired startup reported unknown cleanup: %v", out.err)
			}
			reg, e := f.store.LoadReadOnly()
			if e != nil {
				t.Fatal(e)
			}
			agent, _ := reg.Agent(created.Agent.Metadata.UID)
			pane, _ := reg.Pane(created.Binding.PaneUID)
			if agent.Status.Phase != m.PhaseOffline || !m.MatchesProcessWait(activation.Binding, pane.Status.LastTermination) || !m.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination) || !processActivationAbsent(activation, localipc.Process) {
				t.Fatal("born cancellation lost exact actual Wait/retirement")
			}
		})
	}
}

// CLI, ordinary SDK and explicit prepared SDK use the same lost-host seed,
// provider protocol fixtures, lifecycle engine and compared result fields.
func TestSDKHostLostResumeParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	for _, provider := range []string{"claude", "codex"} {
		modes := []string{"cli", "sdk"}
		if provider == "claude" {
			modes = append(modes, "prepared")
		}
		for _, mode := range modes {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				f := hostLostCLIProviderFixture(t, provider)
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				ref, before, old := recordedLostHostCLI(t, ctx, f, provider)
				cleanupDetachedResume(t, f, ref)
				client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
				if err != nil {
					t.Fatal(err)
				}
				var binding core.ProcessBinding
				if err = func() error {
					raw, e := json.Marshal(old.Binding)
					if e != nil {
						return e
					}
					return json.Unmarshal(raw, &binding)
				}(); err != nil {
					t.Fatal(err)
				}
				req := core.ResumeAgentRequest{Ref: ref, Detach: true, Binding: binding}
				if provider == "claude" {
					req.Prompt = "paired first input"
				}
				if mode == "prepared" {
					empty := req
					empty.Prompt = ""
					_, err = client.ResumeAgent(ctx, empty)
					var refused *projmux.Refusal
					if !errors.As(err, &refused) || refused.Code != projmux.CodeResumeRequestInvalid {
						t.Fatal("ordinary empty-prompt admission changed", err)
					}
					_, err = client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, Binding: binding})
					if err == nil {
						t.Fatal("false AllowHostLost accepted lost host")
					}
					prepared, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, Binding: binding, AllowHostLost: true})
					if err != nil {
						t.Fatal(err)
					}
					if prepared.Preparation.Binding != binding || !prepared.Preparation.HostLost || prepared.Preparation.Conversation != old.SessionID {
						t.Fatal("wrong preparation", prepared)
					}
					command := New().agent
					direct, err := command.prepareOwnedClaudeResume(ctx, processAgentResumeOptions{Agent: selector.Ref{Kind: m.KindAgent, UID: old.Binding.AgentUID}, allowHostLost: true})
					if err != nil || direct.Prepared == nil || direct.Prepared.LaunchDigest != prepared.Preparation.LaunchDigest {
						t.Fatal("CORE/SDK preparation mismatch", err)
					}
					after := mustRegistry(t, f)
					if !reflect.DeepEqual(before, after) {
						t.Fatal("preparation changed Registry")
					}
					claim, err := readDeferredClaim(command.deferredClaimPath(old.Binding.AgentUID))
					if err != nil || deferredClaimLive(claim) {
						t.Fatal("preparation retained claim", err)
					}
					snapshot := sdkCreateDirectorySnapshot(t, f.root+"/state/projmux")
					valid, err := client.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: ref, Preparation: prepared.Preparation})
					if err != nil || valid != prepared.Preparation {
						t.Fatal("Validate", valid, err)
					}
					if !reflect.DeepEqual(snapshot, sdkCreateDirectorySnapshot(t, f.root+"/state/projmux")) {
						t.Fatal("Validate changed state")
					}
					again, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, Binding: binding, AllowHostLost: true})
					if err != nil || again != prepared {
						t.Fatal("preparation not reused", again, err)
					}
					req.Preparation = &prepared.Preparation
				}
				if mode == "cli" {
					args := []string{"agent", "resume", ref, "--detach"}
					if req.Prompt != "" {
						args = append(args, "--", req.Prompt)
					}
					output := detachedResumeCLI(t, ctx, f, args...)
					if !strings.Contains(string(output), "previous generation: reconcile/unknown; last turn may have been truncated") {
						t.Fatal("CLI previous truncation advisory missing", string(output))
					}
					t.Logf("TRUNCATION provider=%s mode=cli advisory=true", provider)
				} else {
					result, err := client.ResumeAgent(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					if !result.MayBeTruncated || result.Preparation != nil || result.Binding.AgentUID != binding.AgentUID || result.Binding.PaneUID != binding.PaneUID || result.Binding.Generation == binding.Generation {
						t.Fatal("SDK identity/consumption", result)
					}
					t.Logf("TRUNCATION provider=%s mode=%s advisory=%t", provider, mode, result.MayBeTruncated)
				}
				next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
				})
				if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&next) != processRelaunchConversation(&old) || next.History == nil || next.History.Binding != old.Binding {
					t.Fatal("paired identity/history/conversation mismatch")
				}
				after := mustRegistry(t, f)
				agent, _ := after.Agent(old.Binding.AgentUID)
				source, _ := before.Agent(old.Binding.AgentUID)
				pane, _ := after.Pane(old.Binding.PaneUID)
				if !reflect.DeepEqual(agent.Spec, source.Spec) || !reflect.DeepEqual(agent.Metadata.Annotations, source.Metadata.Annotations) {
					t.Fatal("recipe changed")
				}
				if pane.Status.LastTermination == nil || pane.Status.LastTermination.Source != m.TerminationSourceReconcile || pane.Status.LastTermination.Classification != m.TerminationUnknown || pane.Status.LastTermination.ExitCode != nil {
					t.Fatal("previous unknown lost")
				}
				// Observe previous-generation evidence through the public resource API,
				// rather than relying only on the fixture's internal Registry read.
				resource, err := client.GetResource(ctx, core.Ref{Kind: core.KindPane, Value: "uid:" + binding.PaneUID})
				if err != nil {
					t.Fatal("public Pane observation", err)
				}
				publicPane, ok := resource.(core.Pane)
				if !ok || publicPane.Status.ProcessSession == nil || publicPane.Status.ProcessSession.History == nil {
					t.Fatal("public previous history missing", resource)
				}
				observed := publicPane.Status.ProcessSession
				termination := publicPane.Status.LastTermination
				if observed.Binding.AgentUID != binding.AgentUID || observed.Binding.PaneUID != binding.PaneUID || observed.Binding.Generation == binding.Generation || observed.History.Binding != binding || observed.History.InterruptedTurnID != old.TurnID || len(observed.History.Expired) != len(old.Pending) {
					t.Fatal("public identity/history changed", observed)
				}
				if termination == nil || termination.Source != core.TerminationSourceReconcile || termination.Classification != core.TerminationUnknown || termination.Generation != binding.Generation || termination.OperationID != binding.OperationID || termination.AgentUID != binding.AgentUID || termination.PaneUID != binding.PaneUID || termination.ExitCode != nil {
					t.Fatal("public previous unknown evidence changed", termination)
				}
				resource, err = client.GetResource(ctx, core.Ref{Kind: core.KindAgent, Value: ref})
				if err != nil {
					t.Fatal("public Agent observation", err)
				}
				publicAgent, ok := resource.(core.Agent)
				if !ok || !reflect.DeepEqual(publicAgent.Status.LastTermination, termination) {
					t.Fatal("public Agent/Pane previous evidence differs", resource)
				}
				t.Logf("PUBLIC_OBSERVATION provider=%s mode=%s sameAgent=true samePane=true newGeneration=true previousHistory=true interruptedTurnAndExpiredCount=true previousReconcileUnknown=true inventedExitCode=false", provider, mode)
				detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
			})
		}
	}
}

func TestSDKPreparedResumeFailureRetry(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	f := hostLostCLIProviderFixture(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	ref, _, old := recordedLostHostCLI(t, ctx, f, "claude")
	cleanupDetachedResume(t, f, ref)
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, AllowHostLost: true})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(f.root, "claude")
	original, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nIFS= read -r frame\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	failed, err := client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, Prompt: "failed first input", Binding: prepared.Preparation.Binding, Preparation: &prepared.Preparation})
	if err == nil || failed.MayBeTruncated || failed.Preparation == nil || failed.Preparation.HostLost || failed.Preparation.Binding == prepared.Preparation.Binding {
		t.Fatalf("failed child did not return same frozen retry proof: %+v %v", failed, err)
	}
	after := mustRegistry(t, f)
	pane, _ := after.Pane(old.Binding.PaneUID)
	if !m.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) || pane.Status.ProcessSession.History == nil || pane.Status.ProcessSession.History.Binding != old.Binding {
		t.Fatal("retry proof lacks actual Wait/history")
	}
	if err = os.WriteFile(wrapper, original, 0700); err != nil {
		t.Fatal(err)
	}
	valid, err := client.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: ref, Preparation: *failed.Preparation})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: ref, Detach: true, Prompt: "retry input exact", Binding: valid.Binding, Preparation: &valid})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.MayBeTruncated || resumed.Preparation != nil || resumed.Binding.Generation == valid.Binding.Generation {
		t.Fatal("retry did not consume proof")
	}
	next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *m.ProcessSessionRecord) bool {
		return r.Binding.Generation == resumed.Binding.Generation && r.SessionID == old.SessionID
	})
	if next.SessionID != old.SessionID {
		t.Fatal("retry changed conversation")
	}
	wire, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(wire, []byte("retry input exact")) != 1 {
		t.Fatal("retry input duplicated", string(wire))
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
}

// Cancellation before preparation can acquire its existing claim guard must
// reap the actual helper, including before its first response. No SDK-only
// preflight assertion can satisfy this test.
func TestSDKPreparedResumeCancellationReapsHelper(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("procfs required for exact helper birth")
	}
	f := hostLostCLIProviderFixture(t, "claude")
	lifetime, finish := context.WithTimeout(context.Background(), 20*time.Second)
	defer finish()
	ref, _, old := recordedLostHostCLI(t, lifetime, f, "claude")
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	command := New().agent
	unlock, err := lockDeferredClaim(command.deferredClaimPath(old.Binding.AgentUID))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(lifetime)
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		_, e := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, AllowHostLost: true})
		completed <- e
	}()
	var helper m.ProcessIdentity
	processCLIUntil(t, lifetime, func() bool {
		entries, e := os.ReadDir("/proc")
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			exe, e := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
			if e != nil || exe != f.binary {
				continue
			}
			var pid int
			if _, e = fmt.Sscan(entry.Name(), &pid); e != nil {
				continue
			}
			id, parent, e := localipc.Process(pid)
			if e == nil && parent == os.Getpid() {
				descriptors, e := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
				if e != nil {
					continue
				}
				guardOpened := false
				for _, fd := range descriptors {
					target, e := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
					if e == nil && target == command.deferredClaimPath(old.Binding.AgentUID)+".lock" {
						guardOpened = true
						break
					}
				}
				if !guardOpened {
					continue
				}
				helper = id
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case err = <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-lifetime.Done():
		t.Fatal("cancelled preparation helper was not reaped")
	}
	if _, _, err = localipc.Process(helper.PID); !errors.Is(err, localipc.ErrProcessAbsent) {
		t.Fatal("helper remains after cancelled API call", helper.PID, err)
	}
	after, err := os.ReadFile(f.store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cancelled preparation changed Registry", err)
	}
	if record, e := command.readDeferredLaunch(old.Binding.AgentUID); e != nil || record != nil {
		t.Fatal("cancelled preparation wrote a plan", e)
	}
	claim, err := readDeferredClaim(command.deferredClaimPath(old.Binding.AgentUID))
	if err != nil || deferredClaimLive(claim) {
		t.Fatal("cancelled preparation retained a claim", err)
	}
}

func TestSDKPreparedResumeRefusals(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("copied CLI required")
	}
	f := hostLostCLIProviderFixture(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref, _, _ := recordedLostHostCLI(t, ctx, f, "claude")
	client, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: f.root + "/config/projmux", StateDir: f.root + "/state/projmux", ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{Ref: ref, AllowHostLost: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"digest", "pane", "conversation", "binding", "host-lost", "missing", "request-binding", "wait-for-peer", "override"} {
		t.Run(change, func(t *testing.T) {
			p := prepared.Preparation
			req := core.ResumeAgentRequest{Ref: ref, Detach: true, Prompt: "must not deliver", Binding: p.Binding, Preparation: &p}
			want := projmux.CodeResumePreparationStale
			var restore func()
			switch change {
			case "digest":
				p.LaunchDigest = "changed"
			case "pane":
				p.PaneUID = "wrong-pane"
			case "conversation":
				p.Conversation = "other-session"
			case "binding":
				p.Binding.Generation = "other-generation"
			case "host-lost":
				p.HostLost = false
			case "missing":
				command := New().agent
				path := command.deferredStatePath("deferred-launches", p.AgentUID)
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
				restore = func() {
					if e := os.WriteFile(path, raw, 0600); e != nil {
						t.Error(e)
					}
				}
				defer restore()
			case "request-binding":
				req.Binding.Generation = "stale-request"
				want = projmux.CodeResumePlanInvalid
			case "wait-for-peer":
				req.WaitForPeer = true
				want = projmux.CodeResumeRequestInvalid
			case "override":
				req.Model = "different-model"
				want = projmux.CodeResumeRequestInvalid
			}
			before := sdkCreateDirectorySnapshot(t, f.root+"/state/projmux")
			if change != "request-binding" && change != "wait-for-peer" && change != "override" {
				_, e := client.ValidateAgentResumePreparation(ctx, core.ValidateAgentResumePreparationRequest{Ref: ref, Preparation: p})
				var refused *projmux.Refusal
				if !errors.As(e, &refused) || refused.Code != projmux.CodeResumePreparationStale {
					t.Fatalf("Validate %s: %v", change, e)
				}
			}
			result, e := client.ResumeAgent(ctx, req)
			var refused *projmux.Refusal
			if !errors.As(e, &refused) || refused.Code != want || result.Preparation != nil || result.MayBeTruncated {
				t.Fatalf("Resume %s returned %+v %v; want %s", change, result, e, want)
			}
			if !reflect.DeepEqual(before, sdkCreateDirectorySnapshot(t, f.root+"/state/projmux")) {
				t.Fatal("refusal changed Registry/sidecar/claim")
			}
		})
	}
}
