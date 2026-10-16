package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Exercise the public API against the exact process peer wire, using only our
// protocol fixture. CLI and SDK each receive a fresh fixture and receipt store.
func TestSDKPeerMessageProcessSendStatusReplay(t *testing.T) {
	for _, lane := range []string{"sdk", "cli"} {
		t.Run(lane, func(t *testing.T) {
			f := newProcessCodexPeerFixture(t, processCodexSteerFixture("reply({})"))
			paths := config.Paths{ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: filepath.Join(f.root, "state", "projmux")}
			disk := store.NewStore(paths.StateDir)
			m := &codexProcessMessages{store: disk, endpoints: map[string]*codexProcessEndpoint{f.endpoint.binding.Agent: f.endpoint}}
			f.endpoint.messages.Store(m)
			f.turn(t, "original", "hold")
			f.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
			sdk, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, HomeDir: f.root, SocketPath: filepath.Join(f.root, "unused.sock")})
			if err != nil {
				t.Fatal(err)
			}
			cmd := newAgentCommand()
			cmd.messagePaths = agentMessagePaths{registryPath: f.path, loadRegistry: f.store.LoadReadOnly}
			cmd.messageStore = disk
			cmd.messageRoute = liveAgentMessageRouteResolver{registryPath: f.path}
			ref := "uid:" + f.endpoint.binding.Agent
			request := projmux.SendMessageRequest{Source: ref, Target: ref, Text: "peer context only", MessageRef: "message-sdk-peer", TTL: time.Minute}
			var first core.MessageReceipt
			for attempt := 0; attempt < 2; attempt++ {
				var got core.MessageReceipt
				if lane == "sdk" {
					got, err = sdk.SendMessage(context.Background(), request)
				} else {
					_, _, err = runRoute(t, cmd, "message", "send", ref, "--source", ref, "--message-ref", request.MessageRef, "--ttl", "1m", "--", request.Text)
					if err == nil {
						got, err = sdk.MessageStatus(context.Background(), request.MessageRef)
					}
				}
				if err != nil || got.Delivery.State != core.MessageDelivered || got.Delivery.Reason != "host-history-injected" || got.Delivery.OutcomeUnknown {
					t.Fatalf("attempt=%d receipt=%+v err=%v", attempt, got, err)
				}
				if attempt == 0 {
					first = got
				} else if got != first {
					t.Fatalf("replay changed receipt: %+v %+v", first, got)
				}
			}
			starts, items := processCodexTurnWrites(t, f)
			if starts != 1 || len(items) != 1 {
				t.Fatalf("peer replay/input boundary: turn starts=%d peer items=%d", starts, len(items))
			}
			if !strings.Contains(items[0]["text"], "untrusted-coordination-only") {
				t.Fatalf("missing peer envelope: %v", items)
			}
			var stdout, stderr bytes.Buffer
			if err := cmd.runMessageStatus([]string{request.MessageRef, "-o", "json"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(first)
			if strings.TrimSpace(stdout.String()) != string(want) {
				t.Fatalf("CLI/SDK receipt JSON differs: %s %s", stdout.String(), want)
			}
			stored, found, err := disk.Get(request.MessageRef)
			if err != nil || !found || stored.Envelope.Authority != message.PeerAuthority() || stored.Envelope.Operator() {
				t.Fatalf("authority changed: %+v %v", stored, err)
			}
		})
	}
}

// A child-free protocol fixture owns both sides of one exact socket. No provider
// executable or user turn is started; every receipt outcome is deterministic.
func TestSDKPeerMessageReceiptStateParity(t *testing.T) {
	for _, state := range []string{"delivered", "held", "refused", "expired", "stale", "unknown"} {
		t.Run(state, func(t *testing.T) {
			for _, lane := range []string{"sdk", "cli"} {
				t.Run(lane, func(t *testing.T) {
					root := t.TempDir()
					paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
					registryPath := intmetadata.PathFor(paths.StateDir)
					h := newSessionRefHarness(t, "codex")
					reg := h.registry.Clone()
					agent, _ := reg.Agent(h.agentUID)
					pane, _ := reg.Pane(h.paneUID)
					window, _ := reg.Window(agent.Metadata.OwnerUID())
					binding := coremetadata.ProcessBinding{HostInstanceID: "host-sdk-parity", ProjectUID: window.Metadata.OwnerUID(), WindowUID: window.Metadata.UID, AgentUID: h.agentUID, PaneUID: h.paneUID, Generation: "gen-sdk-parity", OperationID: "op-sdk-parity"}
					identity, _, err := localipc.Process(os.Getpid())
					if err != nil {
						t.Fatal(err)
					}
					pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
					pane.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, AgentUID: h.agentUID, Generation: binding.Generation, OperationID: binding.OperationID, Process: &coremetadata.ProcessActivation{Binding: binding, HostProcess: identity, Child: identity}}
					pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: "codex", Binding: binding, ThreadID: "thread-sdk-parity", ConnectionID: binding.OperationID, ResumeState: coremetadata.ProcessResumeUnknown}
					agent.Status.Activation = coremetadata.AgentActivation{}
					registry := intmetadata.NewStore(registryPath)
					if _, err := registry.Update(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
						t.Fatal(err)
					}
					disk := store.NewStore(paths.StateDir)
					socket := processCodexHostSocket(registryPath, h.paneUID, binding.Generation)
					listener, err := localipc.Listen(socket)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = listener.Close(); _ = os.Remove(filepath.Dir(socket)) })
					var writes atomic.Int32
					done := make(chan struct{})
					go func() {
						defer close(done)
						for {
							conn, err := listener.Unix.AcceptUnix()
							if err != nil {
								return
							}
							func() {
								defer conn.Close()
								var req codexProcessExchange
								if localipc.ReadJSON(conn, &req) != nil || req.Foreground == nil {
									return
								}
								if req.Foreground.Action == "validate" {
									_ = localipc.WriteJSON(conn, processForegroundResult{Accepted: true})
									return
								}
								if req.Foreground.Action != "peer-message" {
									return
								}
								writes.Add(1)
								if state == "unknown" {
									return
								}
								result := processForegroundResult{}
								if state == "refused" {
									result.Unsupported = true
								} else if state == "stale" {
									result.Stale = true
								} else {
									r, found, err := disk.Get(req.Foreground.MessageRef)
									if err != nil || !found {
										return
									}
									kind := map[string]message.EventKind{"delivered": message.EventDeliver, "held": message.EventHold, "expired": message.EventExpire}[state]
									updated, _, err := disk.Apply(r.Envelope.MessageRef, message.Event{Kind: kind, MessageRef: r.Envelope.MessageRef, ConversationRef: r.Envelope.ConversationRef, Target: r.Envelope.Target, Reason: "fixture-" + state, ObservedAt: time.Now().UTC()})
									if err != nil {
										return
									}
									result.Accepted = true
									result.Receipt = &codexProcessReceipt{Delivery: updated.Delivery}
								}
								_ = localipc.WriteJSON(conn, result)
							}()
						}
					}()
					t.Cleanup(func() { _ = listener.Unix.Close(); <-done })
					sdk, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, SocketPath: filepath.Join(root, "unused.sock")})
					if err != nil {
						t.Fatal(err)
					}
					cmd := newAgentCommand()
					cmd.messagePaths = agentMessagePaths{registryPath: registryPath, loadRegistry: registry.LoadReadOnly}
					cmd.messageStore = disk
					cmd.messageRoute = liveAgentMessageRouteResolver{registryPath: registryPath}
					ref := "uid:" + h.agentUID
					request := projmux.SendMessageRequest{Source: ref, Target: ref, Text: "peer", MessageRef: "message-state-parity", TTL: time.Minute}
					expectedState := map[string]core.MessageState{"delivered": core.MessageDelivered, "held": core.MessageHeld, "refused": core.MessageRefused, "expired": core.MessageExpired, "stale": core.MessageStale, "unknown": core.MessageFailed}[state]
					var first core.MessageReceipt
					for attempt := 0; attempt < 2; attempt++ {
						var got core.MessageReceipt
						if lane == "sdk" {
							got, err = sdk.SendMessage(context.Background(), request)
						} else {
							_, _, err = runRoute(t, cmd, "message", "send", ref, "--source", ref, "--message-ref", request.MessageRef, "--ttl", "1m", "--", request.Text)
						}
						shouldFail := state != "delivered" && state != "held"
						if (err != nil) != shouldFail {
							t.Fatalf("send error=%v state=%s", err, state)
						}
						observed, statusErr := sdk.MessageStatus(context.Background(), request.MessageRef)
						if statusErr != nil {
							t.Fatal(statusErr)
						}
						if lane == "sdk" && got != observed {
							t.Fatalf("partial SDK receipt differs from status: %+v %+v", got, observed)
						}
						if observed.Delivery.State != expectedState || observed.Delivery.OutcomeUnknown != (state == "unknown") || observed.Deadline.Sub(observed.Delivery.AcceptedAt) != time.Minute {
							t.Fatalf("receipt=%+v", observed)
						}
						if attempt == 0 {
							first = observed
						} else if observed != first {
							t.Fatalf("replay changed receipt: %+v %+v", first, observed)
						}
						var out, diagnostics bytes.Buffer
						if e := cmd.runMessageStatus([]string{request.MessageRef, "-o", "json"}, &out, &diagnostics); e != nil {
							t.Fatal(e)
						}
						encoded, _ := json.Marshal(observed)
						if strings.TrimSpace(out.String()) != string(encoded) {
							t.Fatalf("CLI/SDK JSON differs: %s %s", out.String(), encoded)
						}
					}
					changed := request
					changed.Text = "changed immutable payload"
					_, mismatch := sdk.SendMessage(context.Background(), changed)
					var refusal *projmux.Refusal
					if !errors.As(mismatch, &refusal) || refusal.Code != projmux.CodeMessageRetryMismatch {
						t.Fatalf("immutable mismatch: %v", mismatch)
					}
					if writes.Load() != 1 {
						t.Fatalf("replayed peer transport %d times", writes.Load())
					}
				})
			}
		})
	}
}

func TestSDKPeerMessageReleaseFreezesRegistryCASAndMirror(t *testing.T) {
	root := t.TempDir()
	poison := filepath.Join(root, "ambient")
	t.Setenv("XDG_STATE_HOME", poison)
	t.Setenv("TMUX", filepath.Join(root, "foreign.sock")+",1,1")
	paths := config.Paths{ConfigDir: filepath.Join(root, "arbitrary-config"), StateDir: filepath.Join(root, "arbitrary-state")}
	h := newSessionRefHarness(t, "claude")
	reg := h.registry.Clone()
	target, _ := reg.Agent(h.agentUID)
	target.Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionApprovalRequired, ObservedAt: time.Now().UTC(), Source: string(coremetadata.InteractionSourceProviderHook)}
	blocked := target.Status.Interaction
	disk := intmetadata.NewDefaultStore(paths)
	if _, err := disk.Update(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
		t.Fatal(err)
	}
	command, err := explicitMessageReleaseCommand(paths, "")
	if err != nil {
		t.Fatal(err)
	}
	if command.mirror != nil {
		t.Fatal("release inherited a foreign tmux mirror")
	}
	if err := command.commitClaudeTurnEnd(target.Clone(), blocked); err != nil {
		t.Fatal(err)
	}
	after, err := disk.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := after.Agent(h.agentUID)
	if updated.Status.Interaction.Kind != coremetadata.InteractionResponseComplete {
		t.Fatalf("wrong Registry received release CAS: %+v", updated.Status.Interaction)
	}
	if _, err := os.Stat(poison); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release touched ambient state: %v", err)
	}
}
