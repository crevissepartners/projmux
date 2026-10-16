package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	metadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Supplemental SDK cases use the same copied binary as the standard CLI suite.
func TestSDKDetachedProcessResumeFrozenDomainsAndClaims(t *testing.T) {
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
				if resumed.Binding.AgentUID != created.Binding.AgentUID || resumed.Binding.PaneUID != created.Binding.PaneUID || resumed.Binding.Generation == created.Binding.Generation || resumed.ClaimWaiting || resumed.PaneID != "" || len(resumed.Projection.Items) != 1 {
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
					if !claim.ClaimWaiting || claim.Binding != resumed.Binding {
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
					if !claim.ClaimWaiting {
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
