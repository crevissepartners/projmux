package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestCLIAsyncAskAdmissionDoesNotChangeInteraction(t *testing.T) {
	f := newQuestionFixture(t, false)
	started := 0
	f.command.questionAsyncStart = func() error { started++; return nil }
	before := f.resources.registry.Clone()
	out, _, e := runRoute(t, f.command, "question", "ask", "uid:"+questionTestAgent, "--questions", questionTestQuestions, "--expires-in", "1m", "-o", "json")
	if e != nil {
		t.Fatal(e)
	}
	var r agentquestion.Record
	if e = json.Unmarshal([]byte(out), &r); e != nil || !agentquestion.ValidID(r.ID) || r.Delivery != "async-message" || r.Deadline.Sub(r.CreatedAt) != time.Minute || started != 1 {
		t.Fatalf("%s %v started=%d", out, e, started)
	}
	a, _ := before.Agent(questionTestAgent)
	b, _ := f.resources.registry.Agent(questionTestAgent)
	if a.Status.Interaction != b.Status.Interaction {
		t.Fatal("ask changed provider interaction")
	}
	// Invalid input must neither record nor launch a process.
	for _, args := range [][]string{{"--questions", "[]"}, {"--questions", `[{"question":"Token?","options":[{"label":"other"}],"isSecret":true}]`}, {"--questions", questionTestQuestions, "--expires-in", "0s"}, {"--questions", questionTestQuestions, "--expires-in", "169h"}} {
		_, _, e = runRoute(t, f.command, append([]string{"question", "ask", "uid:" + questionTestAgent}, args...)...)
		if e == nil {
			t.Fatal("invalid ask accepted")
		}
		if strings.Contains(args[1], `"isSecret":true`) && !strings.Contains(e.Error(), "question-secret-native-only") {
			t.Fatal("secret refusal lacks its public reason", e)
		}
	}
	rs, _ := f.store.CLIAsyncStore().List("")
	if len(rs) != 1 || started != 1 {
		t.Fatal("invalid input had effects")
	}
	f.command.questionAsyncStart = func() error { return errors.New("fixture start failure") }
	if _, _, e = runRoute(t, f.command, "question", "ask", "uid:"+questionTestAgent, "--questions", questionTestQuestions); e == nil {
		t.Fatal("failed owner accepted")
	}
	rs, _ = f.store.CLIAsyncStore().List("")
	if len(rs) != 1 {
		t.Fatal("unstarted question retained")
	}
}

func TestCLIAsyncOutboxOfflineResumeSameUIDPeerDelivery(t *testing.T) {
	cmd, resources, _ := exactControlCLICommand(t)
	now := time.Now().UTC()
	cmd.now = func() time.Time { return now }
	cmd.messageNow = cmd.now
	cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
	cmd.messagePaths = agentMessagePaths{loadRegistry: cmd.loadRegistry}
	cmd.messageStore = messagestore.NewStore(t.TempDir())
	cmd.messageRoute = &traceMessageRouteResolver{route: mustMessageRoute(t, resources.registry, "agt-alpha-codex")}
	writes := 0
	cmd.controlCall = func(context.Context, string, metadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
		writes++
		return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
	}
	st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
	r, e := st.Create("agt-alpha-codex", json.RawMessage(`[{"question":"Which?","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	r, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": "yes"}, "")
	if e != nil {
		t.Fatal(e)
	}
	a, _ := resources.registry.Agent(r.AgentUID)
	a.Status.Phase = metadata.PhaseOffline
	if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
		t.Fatal(e)
	}
	if writes != 0 {
		t.Fatal("offline provider contacted")
	}
	pane, _ := resources.registry.Pane(a.Status.PaneRef)
	pane.Status.Activation.Generation = "generation-question-resumed"
	a.Status.Phase = metadata.PhaseRunning
	cmd.messageRoute.(*traceMessageRouteResolver).route = mustMessageRoute(t, resources.registry, r.AgentUID)
	for range 2 {
		if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
			t.Fatal(e)
		}
	}
	got, _, _ := st.Get(r.ID)
	if got.Outcome != "delivered" || writes != 1 {
		t.Fatalf("outbox %+v writes=%d", got, writes)
	}
	receipt, found, e := cmd.messageStore.Get(got.Parts[0].MessageRef)
	if e != nil || !found {
		t.Fatal(e)
	}
	if receipt.Envelope.Target.ActivationGeneration != "generation-question-resumed" || receipt.Envelope.Source.AgentUID != r.AgentUID || receipt.Envelope.Target.AgentUID != r.AgentUID || receipt.Envelope.Operator() || !strings.Contains(receipt.Envelope.Payload, r.ID) || !strings.Contains(receipt.Envelope.Payload, `"1":"yes"`) {
		t.Fatalf("peer result %+v", receipt)
	}
}
func TestCLIAsyncOutboxDeletedExpiredAndUnknown(t *testing.T) {
	for _, mode := range []string{"deleted", "expired", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			cmd, resources, _ := exactControlCLICommand(t)
			cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
			cmd.messageStore = messagestore.NewStore(t.TempDir())
			now := time.Now()
			cmd.now = func() time.Time { return now }
			st := agentquestion.NewStore(t.TempDir()).WithClock(func() time.Time { return now }).CLIAsyncStore()
			uid := "agt-alpha-codex"
			if mode == "deleted" {
				uid = "agent-deleted-uid"
			}
			r, e := st.Create(uid, json.RawMessage(`[{"question":"Which?","options":[{"label":"yes"}]}]`), time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			a, _ := resources.registry.Agent("agt-alpha-codex")
			a.Status.Phase = metadata.PhaseOffline
			now = now.Add(time.Minute)
			if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil && mode == "deleted" {
				t.Fatal(e)
			}
			r, _, _ = st.Get(r.ID)
			if mode == "deleted" {
				if r.Outcome != "target-deleted" || r.ResultReason != "target-deleted" {
					t.Fatal(r)
				}
				return
			}
			if mode == "expired" {
				now = now.Add(24 * time.Hour)
			} else {
				if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error { v.Parts[0].State = "sending"; return nil }); e != nil {
					t.Fatal(e)
				}
			}
			if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
				t.Fatal(e)
			}
			r, _, _ = st.Get(r.ID)
			want := "unknown"
			if mode == "expired" {
				want = "result-expired"
			}
			if r.Outcome != want {
				t.Fatal(r)
			}
		})
	}
}

// Invalid mutations must not recover unrelated pending questions as a side
// effect. The seam counts the owner-start boundary without creating a process.
func TestCLIAsyncInvalidMutationDoesNotStartRecovery(t *testing.T) {
	for _, args := range [][]string{{"agent", "resume"}, {"agent", "relaunch", "--invalid"}, {"create", "agent", "--invalid"}, {"agent", "question", "answer"}, {"agent", "question", "ask", "uid:missing", "--questions", "[]"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app := New()
			starts := 0
			app.questionOutboxRecovery = func([]string) { starts++ }
			if e := app.Run(args, io.Discard, io.Discard); e == nil {
				t.Fatal("invalid mutation accepted")
			}
			if starts != 0 {
				t.Fatalf("invalid mutation invoked owner recovery %d times", starts)
			}
		})
	}
}

func TestCLIAsyncOutboxRetriesOnlyProvedNonWrites(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "known refusal", true: "unknown write"}[unknown], func(t *testing.T) {
			cmd, resources, _ := exactControlCLICommand(t)
			cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
			cmd.messagePaths = agentMessagePaths{loadRegistry: cmd.loadRegistry}
			cmd.messageStore = messagestore.NewStore(t.TempDir())
			cmd.messageRoute = &traceMessageRouteResolver{route: mustMessageRoute(t, resources.registry, "agt-alpha-codex")}
			now := time.Now().UTC()
			cmd.now = func() time.Time { return now }
			cmd.messageNow = cmd.now
			writes := 0
			cmd.controlCall = func(context.Context, string, metadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
				writes++
				if writes == 1 {
					if unknown {
						return agentControlResponse{}, errors.New("fixture response lost after write")
					}
					return agentControlResponse{Code: "lifecycle-busy"}, nil
				}
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
			r, e := st.Create("agt-alpha-codex", json.RawMessage(`[{"question":"Which?","options":[{"label":"yes"}]}]`), time.Hour)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": "yes"}, ""); e != nil {
				t.Fatal(e)
			}
			if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
				t.Fatal(e)
			}
			first, _, e := st.Get(r.ID)
			if e != nil || writes != 1 {
				t.Fatal(first, e, writes)
			}
			if unknown {
				if first.Outcome != "unknown" {
					t.Fatal(first)
				}
			} else if !first.Pending() || len(first.Parts[0].Receipt) == 0 {
				t.Fatal("proved refusal lost its durable retry", first)
			}
			now = now.Add(11 * time.Second)
			for range 2 {
				if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
					t.Fatal(e)
				}
			}
			last, _, _ := st.Get(r.ID)
			if unknown {
				if writes != 1 || last.Outcome != "unknown" {
					t.Fatal("unknown write resent", writes, last)
				}
			} else if writes != 2 || last.Outcome != "delivered" {
				t.Fatal("proved non-write did not recover", writes, last)
			}
		})
	}
}

func TestCLIAsyncInterruptedDispatchWithAcceptedReceiptIsUnknown(t *testing.T) {
	cmd, resources, _ := exactControlCLICommand(t)
	now := time.Now().UTC()
	cmd.now = func() time.Time { return now }
	cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
	disk := messagestore.NewStore(t.TempDir())
	cmd.messageStore = disk
	st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
	r, e := st.Create("agt-alpha-codex", json.RawMessage(`[{"question":"Which?","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	r, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": "yes"}, "")
	if e != nil {
		t.Fatal(e)
	}
	bodies, _ := r.Messages()
	route := publicMessageRoute(mustMessageRoute(t, resources.registry, r.AgentUID))
	envelope := message.Envelope{Version: message.Version, MessageRef: r.Parts[0].MessageRef, ConversationRef: "conversation-interrupted", Source: route, Target: route, Authority: message.PeerAuthority(), Payload: bodies[0], AcceptedAt: now, Deadline: r.ResultDeadline}
	if _, _, e = disk.PutAccepted(envelope, "codex-inbox"); e != nil {
		t.Fatal(e)
	}
	if e = st.MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error { v.Parts[0].State = "sending"; return nil }); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
			t.Fatal(e)
		}
	}
	got, _, e := st.Get(r.ID)
	if e != nil || got.Outcome != "unknown" || got.Parts[0].State != "unknown" || len(got.Parts[0].Receipt) == 0 {
		t.Fatalf("lost dispatch claim must remain unknown: %+v %v", got, e)
	}
}

func TestCLIAsyncDryRunDoesNotRecover(t *testing.T) {
	f := newRelaunchFixture(t)
	app := New()
	app.agent = f.command
	starts := 0
	app.questionOutboxRecovery = func([]string) { starts++ }
	if e := app.Run([]string{"agent", "relaunch", "uid:" + personaAttachAgent, "--model", "opus", "--dry-run"}, io.Discard, io.Discard); e != nil {
		t.Fatal(e)
	}
	if starts != 0 {
		t.Fatalf("read-only dry run invoked question owner %d times", starts)
	}
}

func TestCLIAsyncExistingReferenceMustMatchResult(t *testing.T) {
	for _, mode := range []string{"payload", "source", "target"} {
		t.Run(mode, func(t *testing.T) {
			cmd, resources, _ := exactControlCLICommand(t)
			now := time.Now().UTC()
			cmd.now = func() time.Time { return now }
			cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
			disk := messagestore.NewStore(t.TempDir())
			cmd.messageStore = disk
			st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
			r, e := st.Create("agt-alpha-codex", json.RawMessage(`[{"question":"Which?","options":[{"label":"yes"}]}]`), time.Hour)
			if e != nil {
				t.Fatal(e)
			}
			r, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Which?": "yes"}, "")
			if e != nil {
				t.Fatal(e)
			}
			bodies, _ := r.Messages()
			route := publicMessageRoute(mustMessageRoute(t, resources.registry, r.AgentUID))
			env := message.Envelope{Version: message.Version, MessageRef: r.Parts[0].MessageRef, ConversationRef: "conversation-unrelated", Source: route, Target: route, Authority: message.PeerAuthority(), Payload: bodies[0], AcceptedAt: now, Deadline: r.ResultDeadline}
			switch mode {
			case "payload":
				env.Payload = "unrelated peer context"
			case "source":
				env.Source.AgentUID = "agent-other"
			case "target":
				env.Target.AgentUID = "agent-other"
			}
			if _, _, e = disk.PutAccepted(env, "codex-inbox"); e != nil {
				t.Fatal(e)
			}
			if _, _, e = disk.Apply(env.MessageRef, message.Event{Kind: message.EventDeliver, MessageRef: env.MessageRef, ConversationRef: env.ConversationRef, Target: env.Target, Reason: "fixture-delivered", ObservedAt: now}); e != nil {
				t.Fatal(e)
			}
			for range 2 {
				if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
					t.Fatal(e)
				}
			}
			got, _, e := st.Get(r.ID)
			if e != nil || got.Outcome != "unknown" || got.Parts[0].State != "unknown" {
				t.Fatalf("unrelated receipt was accepted as this question result: %+v %v", got, e)
			}
		})
	}
}

func TestCLIAsyncSuccessfulAnswerRecoversOnlyPendingOwner(t *testing.T) {
	f := newQuestionFixture(t, false)
	starts := 0
	f.command.questionAsyncStart = func() error { starts++; return nil }
	f.command.recoverQuestionOwner()
	if starts != 0 {
		t.Fatal("empty namespace started owner")
	}
	r, e := f.store.CLIAsyncStore().Create(questionTestAgent, json.RawMessage(`[{"question":"Pick?","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	f.command.recoverQuestionOwner()
	if starts != 1 {
		t.Fatal("waiting question did not recover owner")
	}
	app := New()
	app.agent = f.command
	app.questionOutboxRecovery = func([]string) { f.command.recoverQuestionOwner() }
	if e = app.Run([]string{"agent", "question", "answer", "uid:" + questionTestAgent, r.ID, "--index", "1=1"}, io.Discard, io.Discard); e != nil {
		t.Fatal(e)
	}
	if starts != 2 {
		t.Fatal("successful answer did not recover durable outbox", starts)
	}
	if e = f.store.CLIAsyncStore().MutateResult(r.ID, func(v *agentquestion.CLIAsyncRecord) error {
		v.Outcome = "unknown"
		v.Parts[0].State = "unknown"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.command.recoverQuestionOwner()
	if starts != 2 {
		t.Fatal("terminal unknown restarted owner")
	}
}

type questionClaudePeerFixture struct{ envelopes []message.Envelope }

func (f *questionClaudePeerFixture) Submit(_ context.Context, _ string, _ metadata.AgentRouteRef, e message.Envelope) (agentdelivery.Delivery, error) {
	f.envelopes = append(f.envelopes, e)
	return agentdelivery.Delivery{MessageRef: e.MessageRef, State: agentdelivery.StateDelivered, Reason: "fixture-delivered"}, nil
}
func (f *questionClaudePeerFixture) Status(_ context.Context, _ string, _ metadata.AgentRouteRef, ref string) (agentdelivery.Delivery, error) {
	return agentdelivery.Delivery{MessageRef: ref, State: agentdelivery.StateDelivered, Reason: "fixture-delivered"}, nil
}

func TestCLIAsyncTmuxClaudePeerResultFixture(t *testing.T) {
	f := newClaudeCoordinationTestFixture(t)
	cmd := newAgentCommand()
	now := time.Now().UTC()
	cmd.now = func() time.Time { return now }
	cmd.messageNow = cmd.now
	cmd.loadRegistry = intmetadata.NewStore(f.registryPath).LoadReadOnly
	cmd.messagePaths = agentMessagePaths{registryPath: f.registryPath, loadRegistry: cmd.loadRegistry}
	cmd.messageStore = messagestore.NewStore(t.TempDir())
	cmd.messageRoute = &traceMessageRouteResolver{route: f.route}
	peer := &questionClaudePeerFixture{}
	cmd.messageClaude = peer
	route := publicMessageRoute(f.route)
	reg, e := cmd.loadRegistry()
	if e != nil {
		t.Fatal(e)
	}
	pane, found := reg.Pane(route.PaneUID)
	if !found || pane.Spec.Runtime.EffectiveKind() != metadata.RuntimeTmux {
		t.Fatal("fixture must be tmux Claude")
	}
	st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
	r, e := st.Create(route.AgentUID, json.RawMessage(`[{"question":"Pick?","options":[{"label":"yes"}]}]`), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.Resolve(r.ID, r.AgentUID, map[string]string{"Pick?": "yes"}, ""); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = cmd.dispatchQuestionOutbox(context.Background(), st); e != nil {
			t.Fatal(e)
		}
	}
	got, _, e := st.Get(r.ID)
	if e != nil || got.Outcome != "delivered" || len(peer.envelopes) != 1 {
		t.Fatal(got, e, len(peer.envelopes))
	}
	env := peer.envelopes[0]
	if env.Source.AgentUID != r.AgentUID || env.Target.AgentUID != r.AgentUID || env.Operator() || !strings.Contains(env.Payload, `"Pick?":"yes"`) {
		t.Fatal("Claude result changed peer authority or answer", env)
	}
}

func TestCLIAsyncExistingSecretOutboxDoesNotDispatch(t *testing.T) {
	f := newQuestionFixture(t, false)
	st := f.store.CLIAsyncStore()
	r, err := st.Create(questionTestAgent, json.RawMessage(`[{"question":"Token?","options":[{"label":"other"}]}]`), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Resolve(r.ID, r.AgentUID, map[string]string{"Token?": "private-fixture-value"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"question":"Token?"`), []byte(`"question":"Token?","isSecret":true`), 1)
	if err = os.WriteFile(st.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runRoute(t, f.command, "question", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil || strings.Contains(out, "private-fixture-value") {
		t.Fatal("CLI JSON secret answer", err)
	}
	loads := 0
	f.command.loadRegistry = func() (metadata.Registry, error) {
		loads++
		return metadata.Registry{}, errors.New("secret must never reach delivery admission")
	}
	if err = f.command.dispatchQuestionOutbox(context.Background(), st); err != nil || loads != 0 {
		t.Fatal("secret dispatch admitted", loads, err)
	}
	after, _ := os.ReadFile(st.Path())
	if !bytes.Equal(raw, after) {
		t.Fatal("secret dispatch changed record")
	}
	release, owned, err := st.LockOwner()
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	idle, err := st.ReleaseIfIdle(release)
	if !idle {
		release()
	}
	if err != nil || !idle {
		t.Fatal("secret keeps outbox owner alive", idle, err)
	}
}

func TestCLIAsyncLegacyOutboxRetriesOriginalBodies(t *testing.T) {
	raw, err := os.ReadFile("../integrations/agents/agentquestion/testdata/cli_async_legacy_results.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Record agentquestion.CLIAsyncRecord
		Bodies []string
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Record.ID, func(t *testing.T) {
			cmd, resources, _ := exactControlCLICommand(t)
			cmd.loadRegistry = func() (metadata.Registry, error) { return resources.registry.Clone(), nil }
			cmd.messagePaths = agentMessagePaths{loadRegistry: cmd.loadRegistry}
			cmd.messageStore = messagestore.NewStore(t.TempDir())
			cmd.messageRoute = &traceMessageRouteResolver{route: mustMessageRoute(t, resources.registry, "agt-alpha-codex")}
			r := fixture.Record
			r.AgentUID = "agt-alpha-codex"
			now := r.CreatedAt.Add(2 * time.Minute)
			cmd.now = func() time.Time { return now }
			cmd.messageNow = cmd.now
			writes := 0
			cmd.controlCall = func(context.Context, string, metadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
				writes++
				if writes == 1 {
					return agentControlResponse{Code: "lifecycle-busy"}, nil
				}
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			st := agentquestion.NewStore(t.TempDir()).WithClock(cmd.now).CLIAsyncStore()
			disk, _ := json.Marshal(struct {
				Version int                            `json:"version"`
				Records []agentquestion.CLIAsyncRecord `json:"records"`
			}{1, []agentquestion.CLIAsyncRecord{r}})
			if err = os.MkdirAll(filepath.Dir(st.Path()), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(st.Path(), disk, 0600); err != nil {
				t.Fatal(err)
			}
			if err = cmd.dispatchQuestionOutbox(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			first, found, err := st.Get(r.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			if first.Parts[0].State != "unknown" || !bytes.Equal(first.Parts[0].Receipt, r.Parts[0].Receipt) || first.Parts[0].MessageRef != r.Parts[0].MessageRef {
				t.Fatal("unknown evidence changed")
			}
			if len(r.Parts) == 1 {
				if writes != 0 {
					t.Fatal("unknown part was resent")
				}
				return
			}
			if first.Parts[1].Attempt != 3 || first.Parts[1].MessageRef != "message-"+r.ID+"-2-retry-3" || len(first.Parts[1].Receipt) == 0 {
				t.Fatal("legacy retry lost ref or receipt", first.Parts[1])
			}
			now = now.Add(31 * time.Second)
			for range 2 {
				if err = cmd.dispatchQuestionOutbox(context.Background(), st); err != nil {
					t.Fatal(err)
				}
			}
			got, found, err := st.Get(r.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			if writes != len(r.Parts) || got.Outcome != "unknown" || got.Parts[0].State != "unknown" || !bytes.Equal(got.Parts[0].Receipt, r.Parts[0].Receipt) {
				t.Fatal("legacy dispatch duplicated or lost unknown evidence", writes, got.Outcome)
			}
			for i := 1; i < len(got.Parts); i++ {
				delivered, found, err := cmd.messageStore.Get(got.Parts[i].MessageRef)
				if err != nil || !found || delivered.Envelope.Payload != fixture.Bodies[i] || got.Parts[i].State != "delivered" {
					t.Fatalf("legacy body/ref %d changed: found=%v err=%v", i, found, err)
				}
			}
		})
	}
}
