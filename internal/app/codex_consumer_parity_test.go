package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type phase6StaticLivePaneLister struct{ rows []livePaneRow }

func (l phase6StaticLivePaneLister) ListLivePanes() ([]livePaneRow, error) {
	return append([]livePaneRow(nil), l.rows...), nil
}

func TestGenerationConsumersShareExactEndpointFenceAndStaleActionZero(t *testing.T) {
	store, identity, endpoint, authority := phase1GenerationAuthorityFixture(t)
	agent, _ := store.registry.Agent(identity.AgentUID)
	agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: coremetadata.InteractionApprovalRequired, Source: string(coremetadata.InteractionSourceProviderControl), ObservedAt: time.Now(),
	}
	row := livePaneRow{Session: "main", Window: "@1", Pane: identity.RuntimeID, Agent: aiModeCodex, AttentionState: attentionStateReply, AIState: codexgeneration.LifecycleStateDraining, ReplyState: true}
	decorateGenerationLivePane(&row, store.registry)
	wantFence := codexgeneration.ConsumerFence(*endpoint, *authority)
	if row.AgentUID != identity.AgentUID || row.PaneUID != identity.PaneUID || row.StateDomainID != endpoint.StateDomainID ||
		row.EndpointGenerationID != endpoint.EndpointGenerationID || row.AuthorityFence != wantFence || !row.ReplyState {
		t.Fatalf("decorated live consumer = %#v", row)
	}
	entry := notify.Notification{
		ID: buildAttentionNotifyID(row.Session, row.Pane), Session: row.Session, Pane: row.Pane, Source: notify.SourceAI,
		Metadata: map[string]string{
			notify.MetaAgentUID: identity.AgentUID, notify.MetaPaneUID: identity.PaneUID,
			notify.MetaStateDomainID: endpoint.StateDomainID, notify.MetaEndpointGenerationID: endpoint.EndpointGenerationID,
			notify.MetaAuthorityFence: wantFence,
		},
	}
	live := notifyLivePanesFromRows([]livePaneRow{row})[0]
	if got := classifyNotifyRowState(entry, map[string]notifyLivePane{entry.ID: live}, newNotifyLivePaneSet([]livePaneRow{row})); got != notifyDisplayLive {
		t.Fatalf("exact notification classified %v", got)
	}

	binding, err := resolveExactAgentControlBinding(store.registry, *agent, agentControlLive{
		RuntimeID: identity.RuntimeID, PaneUID: identity.PaneUID, ThreadID: identity.ThreadID,
		Authority: codexAuthorityControlPlane, Epoch: "observer-7", Reason: "ready",
	}, true, "/state")
	if err != nil {
		t.Fatal(err)
	}
	if binding.Endpoint != *endpoint || binding.Fence != row.AuthorityFence {
		t.Fatalf("reply endpoint/fence = %#v, live row=%#v", binding, row)
	}

	stale := *authority
	stale.ConnectionEpoch++
	pane, _ := store.registry.Pane(identity.PaneUID)
	pane.Status.Activation.Codex.Authority = &stale
	staleRow := row
	decorateGenerationLivePane(&staleRow, store.registry)
	staleLive := notifyLivePanesFromRows([]livePaneRow{staleRow})[0]
	if got := classifyNotifyRowState(entry, map[string]notifyLivePane{entry.ID: staleLive}, newNotifyLivePaneSet([]livePaneRow{staleRow})); got != notifyDisplayStale {
		t.Fatalf("stale notification classified %v, row=%#v", got, staleRow)
	}
}

func TestGenerationObserverRefreshesDurableDrainingOperationForConsumerProjection(t *testing.T) {
	store, identity, endpoint, authority := phase1GenerationAuthorityFixture(t)
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = store.store().load
	observer := codexNativeObserver{
		identity: identity, endpoint: *endpoint,
		// A long-lived observer can retain its launch-time Current state after
		// admission moves this exact Agent to Draining.
		generationState: codexgeneration.StateCurrent,
		authority:       authority,
		sink:            aiCodexLifecycleSink{command: cmd},
	}
	projection := observer.decorateGenerationProjection(codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionResponseComplete,
	})
	if projection.GenerationState != codexgeneration.StateDraining || projection.Operation == nil ||
		projection.Operation.ID != "drain-operation" || !projection.Operation.ValidFor(endpoint) ||
		!projection.generationInput().Authoritative() {
		t.Fatalf("durable planned projection = %#v", projection)
	}
}

func TestGenerationAwareListerRegistryReadErrorMakesExistingNoticeStaleAndActionZero(t *testing.T) {
	entry := notify.Notification{
		ID: "ai:main:%7", Session: "main", Pane: "%7", Source: notify.SourceAI,
		Metadata: map[string]string{
			notify.MetaAgentUID: "agent-old", notify.MetaPaneUID: "pane-old",
			notify.MetaStateDomainID: "state-main", notify.MetaEndpointGenerationID: "generation-n",
			notify.MetaAuthorityFence: "sha256:previous",
		},
	}
	base := phase6StaticLivePaneLister{rows: []livePaneRow{{
		Session: "main", Pane: "%7", Agent: aiModeCodex,
		AttentionState: attentionStateReply, ReplyState: true,
	}}}
	rows, err := newGenerationAwareLivePaneLister(base, func() (coremetadata.Registry, error) {
		return coremetadata.Registry{}, errors.New("Registry unavailable")
	}).ListLivePanes()
	if err != nil || len(rows) != 1 || rows[0].ReplyState {
		t.Fatalf("read-error rows=%#v err=%v, want Codex action zero", rows, err)
	}
	if got := classifyNotifyRowState(entry, map[string]notifyLivePane{}, newNotifyLivePaneSet(rows)); got != notifyDisplayStale {
		t.Fatalf("generation notice after Registry read error = %v, want stale", got)
	}
}

func TestGenerationNotificationProducerCarriesCanonicalFence(t *testing.T) {
	store, identity, endpoint, authority := phase1GenerationAuthorityFixture(t)
	cmd := testAICommand(t.TempDir())
	notifications := &stubNotifyStore{}
	cmd.notifyStore = notifications
	cmd.producer = &storeAttentionNotifyProducer{store: notifications, ttl: time.Minute}
	cmd.loadRegistry = store.store().load
	cmd.updateRegistry = store.store().update
	cmd.acquireCodexAuthority = func(string) (func(), error) { return func() {}, nil }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-options", "-pqv", "-t", identity.RuntimeID, tmuxopts.PaneUID}) {
			return []byte(identity.PaneUID + "\n"), nil
		}
		if name == "tmux" && len(args) == 5 && reflect.DeepEqual(args[:4], []string{"show-options", "-pqv", "-t", identity.RuntimeID}) {
			switch args[4] {
			case aiPaneAgentOption:
				return []byte("codex\n"), nil
			case aiPaneTopicOption:
				return []byte("phase-6\n"), nil
			}
		}
		if name == "tmux" && len(args) == 5 && reflect.DeepEqual(args[:4], []string{"display-message", "-p", "-t", identity.RuntimeID}) {
			switch args[4] {
			case "#S":
				return []byte("main\n"), nil
			case "#{window_id}":
				return []byte("@1\n"), nil
			case "#{pane_id}":
				return []byte(identity.RuntimeID + "\n"), nil
			case "#{socket_path}":
				return []byte("/tmp/projmux.sock\n"), nil
			}
		}
		return nil, os.ErrNotExist
	}
	operation := coremetadata.CodexGenerationOperationRef{ID: "drain-operation", Endpoint: *endpoint}
	projection := codexLifecycleProjection{
		Accepted: true, Interaction: coremetadata.InteractionApprovalRequired,
		Endpoint: endpoint, GenerationState: codexgeneration.StateDraining, Operation: &operation, Authority: authority,
		Notices: []codexLifecycleNotice{{ID: "notice-generation", Category: "approval_required", Severity: notify.SeverityCritical, ThreadID: identity.ThreadID, RequestID: "request-1"}},
	}
	if err := testCodexLifecycleSink(cmd).Apply(identity, projection); err != nil {
		t.Fatal(err)
	}
	if len(notifications.pushed) != 1 {
		t.Fatalf("notifications = %#v", notifications.pushed)
	}
	metadata := notifications.pushed[0].Metadata
	if metadata[notify.MetaAgentUID] != identity.AgentUID || metadata[notify.MetaPaneUID] != identity.PaneUID ||
		metadata[notify.MetaStateDomainID] != endpoint.StateDomainID || metadata[notify.MetaEndpointGenerationID] != endpoint.EndpointGenerationID ||
		metadata[notify.MetaAuthorityFence] != codexgeneration.ConsumerFence(*endpoint, *authority) {
		t.Fatalf("generation notification metadata = %#v", metadata)
	}
}

func TestGenerationNotificationPartialFenceIsNeverActionable(t *testing.T) {
	entry := notify.Notification{ID: "ai:main:%7", Session: "main", Pane: "%7", Source: notify.SourceAI, Metadata: map[string]string{notify.MetaAgentUID: "agent-only"}}
	live := notifyLivePane{ID: entry.ID, Session: "main", Pane: "%7", AgentUID: "agent-only", ShouldQueue: true}
	if got := classifyNotifyRowState(entry, map[string]notifyLivePane{entry.ID: live}, notifyLivePaneSet{notifyLivePaneSetKey("main", "%7"): struct{}{}}); got != notifyDisplayStale {
		t.Fatalf("partial generation fence classified %v, want stale", got)
	}
}
