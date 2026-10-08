package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processPeerFixture is one Registry holding a live process Codex Agent and a
// live process Claude Agent, each with the route its own sends resolve.
type processPeerFixture struct {
	codex                   *processCodexFixture
	claude                  *processClaudeFixture
	claudeProof             claudeProcessProof
	codexRoute, claudeRoute coremetadata.AgentRouteRef
}

func newProcessPeerFixture(t *testing.T) *processPeerFixture {
	t.Helper()
	codex := newProcessCodexFixture(t, nil)
	claude := newProcessClaudeFixtureAt(t, nil, codex.path)
	f := &processPeerFixture{codex: codex, claude: claude, claudeProof: claude.proof(t)}
	claude.turn(t, "first", "ordinary")
	claude.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	deadline := time.Now().Add(12 * time.Second)
	for {
		reg := f.registry(t)
		ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
		route, err := resolveLiveProcessCodexRoute(ctx, codex.path, reg, codex.endpoint.binding.Agent)
		cancel()
		if err == nil {
			f.codexRoute = route
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process Codex sender route: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	route, reason := processClaudeRouteResolver(claude.path, f.claudeProof)(f.registry(t), claude.binding.Agent)
	if reason != "" {
		t.Fatalf("process Claude sender route: %s", reason)
	}
	f.claudeRoute = route
	return f
}

func (f *processPeerFixture) registry(t *testing.T) coremetadata.Registry {
	t.Helper()
	reg, err := f.codex.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// tmuxHelperRoute is the resolver a tmux Claude target helper serves with. Its
// own Agent is not in this Registry; only peers are resolved through it.
func (f *processPeerFixture) tmuxHelperRoute(t *testing.T) func(coremetadata.Registry, string) (coremetadata.AgentRouteRef, string) {
	t.Helper()
	resolve, reason := claudeRegistrationRoute(claudeEndpointBootstrap{RegistryPath: f.codex.path, AgentUID: "agent-tmux-helper"})
	if reason != claudeRegistrationProceed || resolve == nil {
		t.Fatalf("tmux helper route: %s", reason)
	}
	return resolve
}

// A Claude target helper, tmux or process, proves a process-hosted Claude or
// Codex source to the exact route that source's own send stored, and records
// the durable handoff. Before the shared resolver the tmux helper resolved no
// process source and the process helper no process Codex source, so each
// handoff failed as broker-handoff-persist-failed.
func TestClaudeHelperHandoffProvesProcessHostedSources(t *testing.T) {
	f := newProcessPeerFixture(t)
	helpers := []struct {
		name    string
		resolve func(coremetadata.Registry, string) (coremetadata.AgentRouteRef, string)
		sources []coremetadata.AgentRouteRef
	}{
		{"tmux-helper", f.tmuxHelperRoute(t), []coremetadata.AgentRouteRef{f.claudeRoute, f.codexRoute}},
		// process Claude to another process Claude is the end-to-end
		// TestClaudeProcessBidirectionalEndpointReceiptsAndStaleWireZero.
		{"process-helper", processClaudeRouteResolver(f.claude.path, f.claudeProof), []coremetadata.AgentRouteRef{f.codexRoute}},
	}
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.codex.path)))
	for _, helper := range helpers {
		for _, source := range helper.sources {
			name := helper.name + "/" + source.Authority().Provider()
			reg := f.registry(t)
			route, reason := helper.resolve(reg, source.AgentUID)
			if reason != "" || !messageRouteAccepts(route, publicMessageRoute(source)) {
				t.Fatalf("%s: source route %+v reason=%q", name, route, reason)
			}
			broker, err := newLiveClaudeDialogueBroker(f.codex.path)
			if err != nil {
				t.Fatal(err)
			}
			broker.resolveRoute = helper.resolve
			var refused []diagnostics.ClaudeHandoffRouteRecord
			broker.handoffRoute = func(r diagnostics.ClaudeHandoffRouteRecord) { refused = append(refused, r) }
			envelope := dialogueForRoute("message-peer-"+helper.name+"-"+source.Authority().Provider(), f.claudeRoute, time.Now().UTC())
			envelope.BrokerEnvelope.Source = publicMessageRoute(source)
			if _, _, err := store.PutAccepted(*envelope.BrokerEnvelope, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			if err := broker.MarkHandoff(*envelope.BrokerEnvelope); err != nil || len(refused) != 0 {
				t.Fatalf("%s: handoff err=%v refused=%+v", name, err, refused)
			}
			record, found, err := store.Get(envelope.MessageRef)
			if err != nil || !found || !record.HandoffObserved {
				t.Fatalf("%s: durable handoff %+v found=%v err=%v", name, record, found, err)
			}
		}
	}
}

// A process Codex sender's push reaches a live process Claude target through
// its real helper: delivered, durable, and one provider write.
func TestClaudeProcessHelperDeliversProcessCodexSource(t *testing.T) {
	f := newProcessPeerFixture(t)
	const ref = "message-process-codex-to-claude"
	envelope := dialogueForRoute(ref, f.claudeRoute, time.Now().UTC())
	envelope.BrokerEnvelope.Source = publicMessageRoute(f.codexRoute)
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(f.codex.path)))
	if _, _, err := store.PutAccepted(*envelope.BrokerEnvelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	target, _ := claudeTargetForRoute(f.claudeRoute)
	request := claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "submit", Target: target, Envelope: &envelope}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	response, err := callClaudeCoordination(ctx, f.codex.path, f.claudeRoute, request)
	cancel()
	if err != nil || response.Delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("receipt: %+v %v", response, err)
	}
	answerProcessEndpointQuestion(t, f.claude)
	f.claude.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	record, found, err := store.Get(ref)
	if err != nil || !found || !record.HandoffObserved || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("durable receipt: %+v %v", record, err)
	}
	events, _, _ := f.claude.handle.Events(f.claude.binding, 0)
	writes := 0
	for _, e := range events {
		if bytes.Contains(e.Raw, []byte(`"message_echo"`)) && bytes.Contains(e.Raw, []byte(ref)) {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("provider writes=%d", writes)
	}
}

// A source the helper cannot prove keeps the known zero-write receipt every
// sender version reads, writes nothing, and journals which end failed and the
// Registry host and provider of that Agent.
func TestClaudeHelperHandoffNamesUnprovenSource(t *testing.T) {
	f := newProcessPeerFixture(t)
	unknown := publicMessageRoute(f.codexRoute)
	unknown.AgentUID = "agent-absent"
	foreign := publicMessageRoute(f.codexRoute)
	foreign.Incarnation = "foreign-incarnation"
	for _, test := range []struct {
		name   string
		source coremessage.Route
		peer   diagnostics.ClaudeHandoffPeer
	}{
		{"absent-agent", unknown, diagnostics.ClaudeHandoffPeerUnknown},
		{"stale-process-codex", foreign, diagnostics.ClaudeHandoffPeerProcessCodex},
	} {
		broker, err := newLiveClaudeDialogueBroker(f.codex.path)
		if err != nil {
			t.Fatal(err)
		}
		broker.resolveRoute = f.tmuxHelperRoute(t)
		var refused []diagnostics.ClaudeHandoffRouteRecord
		broker.handoffRoute = func(r diagnostics.ClaudeHandoffRouteRecord) { refused = append(refused, r) }
		envelope := dialogueForRoute("message-unproven-"+test.name, f.claudeRoute, time.Now().UTC())
		envelope.BrokerEnvelope.Source = test.source
		hub := newClaudeCoordinationHub()
		setFrameBudgetReplyExecutable(hub)
		poster := &frameBudgetRecordingPoster{token: "token"}
		delivery := hub.submitPush(envelope, broker, poster)
		if delivery.State != agentdelivery.StateFailed || delivery.Reason != "broker-handoff-persist-failed" || delivery.Ambiguous || poster.calls != 0 {
			t.Fatalf("%s: receipt %+v provider calls=%d", test.name, delivery, poster.calls)
		}
		want := diagnostics.ClaudeHandoffRouteRecord{Side: diagnostics.ClaudeHandoffSourceUnproven, Peer: test.peer, AgentUID: test.source.AgentUID}
		if len(refused) != 1 || refused[0] != want {
			t.Fatalf("%s: journal %+v, want %+v", test.name, refused, want)
		}
	}
}

// The tmux helper still proves its own Agent and every tmux peer by the tmux
// registration alone, with the exact refusal text it had.
func TestClaudeTmuxHelperKeepsTmuxRouteResolution(t *testing.T) {
	h := newSessionRefHarness(t, "claude")
	resolve, reason := claudeRegistrationRoute(claudeEndpointBootstrap{RegistryPath: "/unused/registry.json", AgentUID: h.agentUID})
	if reason != claudeRegistrationProceed {
		t.Fatal(reason)
	}
	for _, uid := range []string{h.agentUID, "agent-absent"} {
		gotRoute, gotReason := resolve(*h.registry, uid)
		wantRoute, wantReason := coremetadata.ResolveAgentRoute(*h.registry, uid)
		if gotReason != wantReason || gotRoute.AgentUID != wantRoute.AgentUID || !gotRoute.Same(wantRoute) && wantReason == "" {
			t.Fatalf("%s: got %+v %q, want %+v %q", uid, gotRoute, gotReason, wantRoute, wantReason)
		}
	}
}
