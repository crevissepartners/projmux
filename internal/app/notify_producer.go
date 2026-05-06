package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

// attentionNotifyTTL is the lifetime of a "reply ready" queue entry. Stale
// entries should never sit on the bar forever even if the ack path fails;
// 10 minutes is the spec value.
const attentionNotifyTTL = 10 * time.Minute

// attentionNotifyProducer pushes "reply ready" entries into the projmux
// notify queue when an AI pane transitions to the reply state, and acks the
// matching entry when the pane leaves that state. Implementations are
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
	PaneID string
	Lookup attentionNotifyLookup
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

	ttl := p.ttl
	if ttl <= 0 {
		ttl = attentionNotifyTTL
	}

	_, _, _ = p.store.Push(notify.PushInput{
		ID:       buildAttentionNotifyID(session, resolvedPane),
		Text:     composeAttentionReplyText(agent, topic),
		Severity: notify.SeverityInfo,
		Source:   notify.SourceAI,
		TTL:      ttl,
		Target: notify.Target{
			Socket:  socket,
			Session: session,
			Window:  window,
			Pane:    resolvedPane,
		},
	})
}

// AckReplyReady removes the matching queue entry. The session and pane id
// are read off the lookup so the composite id matches whatever was queued.
// A missing id is treated as a no-op since acking is advisory.
func (p *storeAttentionNotifyProducer) AckReplyReady(in attentionNotifyInput) {
	if p == nil || p.store == nil || in.Lookup == nil {
		return
	}
	paneID := strings.TrimSpace(in.PaneID)
	if paneID == "" {
		return
	}
	session := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#S"))
	resolvedPane := strings.TrimSpace(in.Lookup.PaneFormat(paneID, "#{pane_id}"))
	if resolvedPane == "" {
		resolvedPane = paneID
	}
	if session == "" {
		return
	}
	id := buildAttentionNotifyID(session, resolvedPane)
	if err := p.store.Ack(id); err != nil && !errors.Is(err, notify.ErrNotFound) {
		// Best-effort: keep the attention flow going.
		return
	}
}

// buildAttentionNotifyID renders the composite id that pairs a push with its
// ack. Trimmed values are used so callers do not need to normalize.
func buildAttentionNotifyID(session, paneID string) string {
	return fmt.Sprintf("ai:%s:%s", strings.TrimSpace(session), strings.TrimSpace(paneID))
}

// composeAttentionReplyText renders the queue-row text. The agent label is
// lower-cased to match the existing AI desktop notification convention
// (`claude:` / `codex:`), and the optional topic is appended after a
// middle-dot separator. The store truncates to 80 runes; we let it do that
// rather than duplicating the rule here.
func composeAttentionReplyText(agent, topic string) string {
	label := strings.ToLower(strings.TrimSpace(agent))
	if label == "" {
		label = "agent"
	}
	text := label + ": reply ready"
	if t := strings.TrimSpace(topic); t != "" {
		text += " · " + t
	}
	return text
}
