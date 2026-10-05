package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// In-process Go seam tests launch the same public supervisor and endpoint
// routes as a copied CLI. Dispatch only children marked by that fixture.
func init() {
	if os.Getenv("PMX_TEST_DEFERRED_INTERNAL") != "1" || len(os.Args) < 3 || os.Args[1] != "internal" {
		return
	}
	if err := Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func deferredClaimFixture(t *testing.T) (*agentCommand, processAgentResumeOptions) {
	t.Helper()
	reg := processResumeQueryFixture(t)
	uid := listResumableProcessAgents(reg, processResumeFilter{})[0].Agent.Metadata.UID
	state := t.TempDir()
	command := &agentCommand{messageNow: time.Now, loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }, messagePaths: agentMessagePaths{registryPath: filepath.Join(state, "registry", "registry.json")}, messageStore: messagestore.NewStore(state)}
	return command, processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: uid}}
}

func TestDeferredClaimExactIdentityDuplicateStaleAndRelease(t *testing.T) {
	c, options := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil || claim.record.Process != identity {
		t.Fatal("not exact claimant identity", err)
	}
	if _, err = c.claimDeferredProcessAgent(context.Background(), options); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("duplicate accepted", err)
	}
	req, _ := newProcessAgentResumeRequest(options)
	if _, err = c.processResumeCandidate(req); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatal("ordinary resume accepted claim", err)
	}
	old := claim.record
	old.Process.Start += "-different-birth"
	if err = writeDeferredClaim(claim.path, old); err != nil {
		t.Fatal(err)
	}
	replacement, err := c.claimDeferredProcessAgent(context.Background(), options)
	if err != nil {
		t.Fatal("stale claim not replaced", err)
	}
	defer replacement.Close()
	if replacement.record.Nonce == claim.record.Nonce {
		t.Fatal("claim nonce reused")
	}
	if err = claim.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := readDeferredClaim(claim.path)
	if err != nil || current.Nonce != replacement.record.Nonce {
		t.Fatal("old release erased replacement", err)
	}
	info, err := os.Stat(claim.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("claim permissions", err)
	}
}

func TestDeferredConcurrentClaimsHaveOneWinner(t *testing.T) {
	c, opts := deferredClaimFixture(t)
	var wg sync.WaitGroup
	results := make(chan *deferredProcessClaim, 8)
	for range 8 {
		wg.Go(func() { claim, _ := c.claimDeferredProcessAgent(context.Background(), opts); results <- claim })
	}
	wg.Wait()
	close(results)
	winners := 0
	for claim := range results {
		if claim != nil {
			winners++
			t.Cleanup(func() { _ = claim.Close() })
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestDeferredWaitCancelsWithoutProvider(t *testing.T) {
	c, opts := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result, err := claim.WaitPeer(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || result.Handle != nil {
		t.Fatal("wait spawned provider", err)
	}
}

func TestDeferredDeadClaimInflightIsUnknownAndNotReplayed(t *testing.T) {
	c, opts := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	store := c.messageStore.(*messagestore.Store)
	now := time.Now().UTC()
	route := deferredMessageRoute(claim.record)
	source := route
	source.AgentUID = "source-agent"
	source.PaneUID = "source-pane"
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "inflight-peer", ConversationRef: "inflight-conversation", Source: source, Target: route, Authority: coremessage.PeerAuthority(), Payload: "peer content", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err = store.PutDeferred(envelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if err = claim.markInflight(envelope.MessageRef); err != nil {
		t.Fatal(err)
	}
	dead, err := readDeferredClaim(claim.path)
	if err != nil {
		t.Fatal(err)
	}
	dead.Process.Start += "-dead"
	if err = writeDeferredClaim(claim.path, dead); err != nil {
		t.Fatal(err)
	}
	replacement, err := c.claimDeferredProcessAgent(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	got, found, err := store.Get(envelope.MessageRef)
	if err != nil || !found || got.Delivery.State != coremessage.StateFailed || !got.Delivery.OutcomeUnknown {
		t.Fatalf("inflight recovery: %+v %v", got.Delivery, err)
	}
	held, err := replacement.held()
	if err != nil || len(held) != 0 {
		t.Fatal("ambiguous frame remained replayable", err)
	}
}

func TestDeferredCancelledResumePreservesUndispatchedPeer(t *testing.T) {
	c, opts := deferredClaimFixture(t)
	claim, err := c.claimDeferredProcessAgent(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	store := c.messageStore.(*messagestore.Store)
	now := time.Now().UTC()
	route := deferredMessageRoute(claim.record)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "cancelled-peer", ConversationRef: "cancelled-conversation", Source: route, Target: route, Authority: coremessage.PeerAuthority(), Payload: "peer", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	record, _, err := store.PutDeferred(envelope, "claude-coordination")
	if err != nil {
		t.Fatal(err)
	}
	text, err := deferredPeerText(record)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "peer", Text: text})
	if err != context.Canceled || result.Handle != nil {
		t.Fatal("cancelled resume started a provider", err)
	}
	held, err := claim.held()
	if err != nil || len(held) != 1 || held[0].Delivery.State != coremessage.StateHeld {
		t.Fatal("undispatched peer was lost", err)
	}
	if err = claim.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := c.claimDeferredProcessAgent(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	held, err = replacement.held()
	if err != nil || len(held) != 1 {
		t.Fatal("closed claim lost peer", err)
	}
}

func TestDeferredClaimFencesRelaunchReservation(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, stale := range []bool{false, true} {
			mode := "live"
			if stale {
				mode = "stale"
			}
			t.Run(provider+"/"+mode, func(t *testing.T) {
				store, binding := sessionBindingFixture(t, provider)
				if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					recordFixtureWait(t, reg, binding.Pane)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				state := filepath.Dir(filepath.Dir(store.Path()))
				resource := &resourceStore{stateDir: func() (string, error) { return state, nil }, mutator: intmetadata.DefaultMutator, update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					reg, _, err := store.UpdateConvergent(fn)
					return reg, err
				}}
				command := &agentCommand{loadRegistry: store.LoadReadOnly, messagePaths: agentMessagePaths{registryPath: store.Path()}, messageStore: messagestore.NewStore(state), rebind: &agentRebinder{create: &createCommand{store: resource}}}
				options := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: binding.Agent}}
				candidate, err := command.processResumeCandidate(processAgentResumeRequest{options: options})
				if err != nil {
					t.Fatal(err)
				}
				candidate, err = command.awaitProcessRelaunchRetirement(context.Background(), candidate.Agent, candidate.Pane, false)
				if err != nil {
					t.Fatal(err)
				}
				// Acquire after relaunch's candidate check, before its reservation CAS.
				claim, err := command.claimDeferredProcessAgent(context.Background(), options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = claim.Close() })
				before, err := store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				next := binding
				next.Host, next.Generation, next.Operation = "relaunch-host", "relaunch-generation", "relaunch-operation"
				if stale {
					record := claim.record
					record.Process.Start += "-different-birth"
					if err := writeDeferredClaim(claim.path, record); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := command.reserveProcessResume(context.Background(), candidate, agentSettingsLaunch{}, next, nil); !errors.Is(err, processhost.ErrResumeRefused) || !strings.HasPrefix(err.Error(), processResumeOwned+":") {
						t.Fatal("ordinary resume bypassed claim", err)
					}
					if err := command.reserveProcessRelaunch(context.Background(), candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, next); !errors.Is(err, processhost.ErrResumeRefused) || !strings.HasPrefix(err.Error(), processResumeOwned+":") {
						t.Fatal("relaunch bypassed claim", err)
					}
					after, err := store.LoadReadOnly()
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("refused reservation changed registry", err)
					}
					record, err := readDeferredClaim(claim.path)
					if err != nil || record.Nonce != claim.record.Nonce || !deferredClaimLive(record) {
						t.Fatal("refusal changed the live claim", err)
					}
					if err := claim.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err := command.reserveProcessRelaunch(context.Background(), candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, next); err != nil {
					t.Fatal("released/stale claim blocked relaunch", err)
				}
				after, err := store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := after.Agent(binding.Agent)
				pane, _ := after.Pane(binding.Pane)
				if agent.Status.Phase != coremetadata.PhasePending || pane.Status.ProcessSession.Binding != metadataProcessBinding(next) || pane.Status.ProcessSession.SessionID != candidate.Record.SessionID || pane.Status.ProcessSession.ThreadID != candidate.Record.ThreadID {
					t.Fatal("reservation changed identity or conversation")
				}
			})
		}
	}
}
