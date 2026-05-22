package app

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

// attentionNotifyTTL is the freshness window of a "reply ready" queue entry.
// It is display metadata only; pending rows remain until explicit ack.
const attentionNotifyTTL = 10 * time.Minute

// attentionNotifyProducer pushes "reply ready" entries into the projmux
// notify queue when an AI pane transitions to the reply state. The clear
// callback is intentionally a no-op: ack is the user's consume signal under
// the notify SOT contract. Implementations are
// best-effort: every queue or runner failure is swallowed so the
// pane-attention state machine never blocks on disk IO.
type attentionNotifyProducer interface {
	PushReplyReady(in attentionNotifyInput)
	AckReplyReady(in attentionNotifyInput)
}

// attentionNotifyInput is the shared call shape for push/ack. The PaneID is
// the only mandatory field; the producer reads everything else off tmux via
// Lookup.
type attentionNotifyInput struct {
	PaneID        string
	Lookup        attentionNotifyLookup
	ID            string
	Text          string
	Severity      string
	Metadata      map[string]string
	Force         bool
	SuppressHooks bool
	BadgeKind     string
}

// attentionNotifyLookup is the minimal tmux read surface the producer needs.
// Implementations resolve the requested pane option (e.g. via
// `tmux display-message -p -t <pane> #{<option>}`) and return the trimmed
// value. An empty string is returned on any error so the producer can fall
// back to defaults.
type attentionNotifyLookup interface {
	PaneOption(paneID, option string) string
	PaneFormat(paneID, format string) string
}

// noopAttentionNotifyProducer is the zero-value used when a notify store is
// unavailable. It silently discards every event so callers can always invoke
// it without nil checks.
type noopAttentionNotifyProducer struct{}

func (noopAttentionNotifyProducer) PushReplyReady(attentionNotifyInput) {}

func (noopAttentionNotifyProducer) AckReplyReady(attentionNotifyInput) {}

// storeAttentionNotifyProducer is the production implementation that writes
// to the shared notify queue file.
type storeAttentionNotifyProducer struct {
	store notifyStore
	ttl   time.Duration
	hooks *sendNotiHookDispatcher
}

// newAttentionNotifyProducer builds a producer that uses the default notify
// store on disk. If the store cannot be resolved (e.g. because the user has
// no XDG_STATE_HOME and no $HOME) the producer degrades to a noop so the
// caller is never crippled by configuration drift.
func newAttentionNotifyProducer() attentionNotifyProducer {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return noopAttentionNotifyProducer{}
	}
	return &storeAttentionNotifyProducer{
		store: notify.NewDefaultStore(paths),
		ttl:   attentionNotifyTTL,
		hooks: newSendNotiHookDispatcher(),
	}
}

// PushReplyReady writes an `ai:<session>:<pane>` entry into the queue. The
// guard is the AI agent option: if it is empty the pane was not driven by
// the AI flow (a user manually toggled attention on a shell pane), so we do
// not push.
func (p *storeAttentionNotifyProducer) PushReplyReady(in attentionNotifyInput) {
	if p == nil || p.store == nil || in.Lookup == nil {
		return
	}
	paneID := strings.TrimSpace(in.PaneID)
	if paneID == "" {
		return
	}

	agent := strings.TrimSpace(in.Lookup.PaneOption(paneID, aiPaneAgentOption))
	if agent == "" {
		return
	}

	session := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#S"))
	if session == "" {
		return
	}
	window := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#{window_id}"))
	resolvedPane := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#{pane_id}"))
	if resolvedPane == "" {
		resolvedPane = paneID
	}
	socket := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#{socket_path}"))

	topic := strings.TrimSpace(in.Lookup.PaneOption(paneID, aiPaneTopicOption))
	text := strings.TrimSpace(in.Text)
	if text == "" {
		text = composeAttentionReplyText(agent, topic)
	}
	severity := strings.TrimSpace(in.Severity)
	if severity == "" {
		severity = notify.SeverityInfo
	}
	metadata := mergeAttentionNotifyMetadata(in.Metadata, agent, topic, severity)
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = buildAttentionNotifyID(session, resolvedPane)
	}

	ttl := p.ttl
	if ttl <= 0 {
		ttl = attentionNotifyTTL
	}

	entry, _, err := p.store.Push(notify.PushInput{
		ID:       id,
		Text:     text,
		Severity: severity,
		Source:   notify.SourceAI,
		Metadata: metadata,
		TTL:      ttl,
		Target: notify.Target{
			Socket:  socket,
			Session: session,
			Window:  window,
			Pane:    resolvedPane,
		},
	})
	if err != nil {
		return
	}
	if p.hooks != nil && !in.SuppressHooks {
		p.hooks.Dispatch(entry, notifyHookMeta{
			Type:    "ai-reply-ready",
			Agent:   agent,
			Topic:   topic,
			Message: entry.Text,
		})
	}
	if entry.Severity != notify.SeverityCritical {
		if entries, err := p.store.List(); err == nil {
			_ = ackOlderSameTargetAINotifications(p.store, entry, entries)
		}
	}
}

// AckReplyReady is kept for the attention state-machine seam, but it no
// longer removes rows. Leaving reply state does not mean the user consumed the
// notification; only explicit `notify ack` does.
func (p *storeAttentionNotifyProducer) AckReplyReady(in attentionNotifyInput) {
}

// buildAttentionNotifyID renders the composite id that pairs a push with its
// ack. Trimmed values are used so callers do not need to normalize.
func buildAttentionNotifyID(session, paneID string) string {
	return fmt.Sprintf("ai:%s:%s", strings.TrimSpace(session), strings.TrimSpace(paneID))
}

// composeAttentionReplyText renders the queue-row body text. Agent/category
// information is carried in metadata so rendered notification bodies stay
// focused on actionable context.
func composeAttentionReplyText(agent, topic string) string {
	if t := strings.TrimSpace(topic); t != "" {
		return t
	}
	return "Ready"
}

func mergeAttentionNotifyMetadata(metadata map[string]string, agent, topic, severity string) map[string]string {
	merged := make(map[string]string, len(metadata)+3)
	maps.Copy(merged, metadata)
	if strings.TrimSpace(merged["agent"]) == "" {
		merged["agent"] = strings.ToLower(strings.TrimSpace(agent))
	}
	if strings.TrimSpace(merged["topic"]) == "" {
		merged["topic"] = strings.TrimSpace(topic)
	}
	if strings.TrimSpace(merged["category"]) == "" {
		if severity == notify.SeverityCritical {
			merged["category"] = "approval_required"
		} else {
			merged["category"] = "response_complete"
		}
	}
	if strings.TrimSpace(merged["state"]) == "" {
		merged["state"] = "need"
	}
	return merged
}
