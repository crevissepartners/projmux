package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The foreground owner explicitly activates the store generation before
// installing this seam. A nil seam keeps both existing control paths unchanged.
type processAttentionProjection struct {
	store *processAttentionStore
	queue notifyStore
}

func processAttentionID(binding processhost.Binding, sequence uint64) string {
	raw, _ := json.Marshal(binding)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("ai:process:%s:%d", hex.EncodeToString(sum[:16]), sequence)
}

func processAttentionFailed(raw json.RawMessage) bool {
	var result struct {
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
		Status  string `json:"status"`
		Turn    struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return true
	}
	return result.IsError || result.Status == "failed" || result.Turn.Status == "failed" || (result.Subtype != "" && result.Subtype != "success")
}

func processAttentionInput(r processAttentionRecord, sequence uint64, kind string) notify.PushInput {
	severity, text, event := notify.SeverityInfo, "Ready", "turn-complete"
	switch kind {
	case aibadge.InputRequired:
		severity, text, event = notify.SeverityCritical, "Input required", "question"
	case aibadge.ApprovalRequired:
		severity, text, event = notify.SeverityCritical, "Approval required", "permission"
	case "error":
		severity, text, event = notify.SeverityCritical, "Process attention error", "stop-failure"
	}
	return notify.PushInput{ID: processAttentionID(r.Binding, sequence), Text: text, Severity: severity, Source: notify.SourceAI, TTL: attentionNotifyTTL,
		Target: notify.Target{Session: r.Binding.Project, Window: r.Binding.Window, Pane: r.Binding.Pane},
		Metadata: mergeAttentionNotifyMetadata(map[string]string{notify.MetaEvent: event, notify.MetaAgentUID: r.Binding.Agent, notify.MetaPaneUID: r.Binding.Pane,
			notify.MetaAuthorityFence: processAttentionID(r.Binding, 0), notify.MetaCategory: kind}, r.Provider, "", severity)}
}

// sync holds the attention writer lock while obtaining a bounded host view and
// publishing idempotent queue IDs. The cursor commits only after queue writes:
// a retry can replace an entry, but can never lose an observed request/result.
type processAttentionHost interface {
	Events(processhost.Binding, uint64) ([]processhost.Event, processhost.Snapshot, error)
}

func (p *processAttentionProjection) sync(handle processAttentionHost, binding processhost.Binding) error {
	if p == nil {
		return nil
	}
	if p.store == nil || p.queue == nil || handle == nil {
		return errors.New("incomplete process attention projection")
	}
	return p.store.update(func(records map[string]processAttentionRecord) error {
		r, ok := records[binding.Pane]
		if !ok || r.Binding != binding {
			return processhost.ErrStale
		}
		if r.Pending == nil {
			r.Pending = map[string]processAttentionPending{}
		}
		events, snap, err := handle.Events(binding, r.Sequence)
		if err != nil {
			return err
		}
		// Events prepends its ring-gap marker ahead of retained critical events.
		// Consume by sequence so that marker cannot hide an earlier request.
		sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
		for _, event := range events {
			if event.Sequence <= r.Sequence {
				continue
			}
			kind, applyErr := r.applyEvent(event)
			if applyErr != nil {
				return applyErr
			}
			if kind != "" {
				if err = p.push(processAttentionInput(r, event.Sequence, kind)); err != nil {
					return err
				}
				if kind == "error" || kind == aibadge.ResponseComplete {
					r.NoticeSequence, r.NoticeKind = event.Sequence, kind
				}
			}
			r.Sequence = event.Sequence
		}
		if err = r.reconcilePending(snap, p.push); err != nil {
			return err
		}
		records[binding.Pane] = r
		return nil
	})
}

func (r *processAttentionRecord) applyEvent(event processhost.Event) (string, error) {
	kind := ""
	switch event.Kind {
	case "turn-submitted", "message-reserved":
		r.PendingEnded = false
		r.Badge = aibadge.InProgress
	case "control-pending":
		if event.Request == nil {
			return "", errors.New("process request has no identity")
		}
		kind = aibadge.ApprovalRequired
		if event.Request.Kind == "question" {
			kind = aibadge.InputRequired
		}
		r.Pending[event.Request.ID] = processAttentionPending{Kind: kind, Sequence: event.Sequence}
	case "turn-result":
		r.PendingEnded = false
		r.Badge, kind = aibadge.ResponseComplete, aibadge.ResponseComplete
		if processAttentionFailed(event.Raw) {
			kind = "error"
		}
	case "protocol-error", "host-unknown", "stream-gap":
		r.Badge, kind = aibadge.ResponseComplete, "error"
	case "control-expired", "control-answered":
		if event.Request != nil {
			if _, exists := r.Pending[event.Request.ID]; exists && event.Kind == "control-expired" {
				r.PendingEnded = true
			}
			delete(r.Pending, event.Request.ID)
		}
	case "process-exited":
		r.Terminal = true
		if r.PendingEnded || len(r.Pending) > 0 || (event.Exit != nil && (event.Exit.Code != 0 || event.Exit.Signal != "")) {
			r.Badge, kind = aibadge.ResponseComplete, "error"
		}
	}
	return kind, nil
}

func (r *processAttentionRecord) reconcilePending(snap processhost.Snapshot, push func(notify.PushInput) error) error {
	previous := r.Pending
	r.Pending = map[string]processAttentionPending{}
	if snap.Exit != nil || snap.State == "unknown" || snap.State == "failed" {
		r.Terminal = true
	}
	if !r.Terminal {
		for _, request := range snap.Pending {
			kind := aibadge.ApprovalRequired
			if request.Kind == "question" {
				kind = aibadge.InputRequired
			}
			pending, exists := previous[request.ID]
			if !exists {
				pending = processAttentionPending{Kind: kind, Sequence: snap.Sequence}
				if err := push(processAttentionInput(*r, pending.Sequence, kind)); err != nil {
					return err
				}
			}
			r.Pending[request.ID] = pending
		}
	}
	// Pending is authoritative, so settling one request cannot clear another
	// request or leave an obsolete required-action badge after an answer.
	if snap.Turn != "" && r.Badge != aibadge.ResponseComplete {
		r.Badge = aibadge.InProgress
	}
	r.Sequence = snap.Sequence
	return nil
}

func (p *processAttentionProjection) push(in notify.PushInput) error {
	entry, _, err := p.queue.Push(in)
	if err != nil {
		return err
	}
	if entry.Severity == notify.SeverityCritical {
		return nil
	}
	entries, err := p.queue.List()
	if err != nil {
		return err
	}
	return ackOlderSameTargetAINotifications(p.queue, entry, entries)
}

func (r processAttentionRecord) badge() string {
	badge := r.Badge
	ids := make([]string, 0, len(r.Pending))
	for id := range r.Pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		badge = aibadge.Aggregate(badge, r.Pending[id].Kind)
	}
	return badge
}

// processAttentionConsumer is invocation-scoped; declarations are supplied by
// the owner, including unavailable hosts. Reads neither discover nor start a
// runtime. The durable store binding must match the current declaration.
type processAttentionConsumer struct {
	store    *processAttentionStore
	bindings []processhost.Binding
}

func (c *processAttentionConsumer) records() ([]processAttentionRecord, error) {
	if c == nil {
		return nil, nil
	}
	records, err := c.store.read()
	if err != nil {
		return nil, err
	}
	out := make([]processAttentionRecord, 0, len(c.bindings))
	for _, binding := range c.bindings {
		if r, ok := records[binding.Pane]; ok && r.Binding == binding {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *processAttentionConsumer) clear(pane string) (bool, error) {
	if c == nil {
		return false, nil
	}
	for _, binding := range c.bindings {
		if binding.Pane != pane {
			continue
		}
		records, err := c.store.read()
		if err != nil {
			return true, err
		}
		r, ok := records[pane]
		if !ok || r.Binding != binding {
			return true, processhost.ErrStale
		}
		return true, c.store.clear(binding, r.Sequence)
	}
	return false, nil
}

func (c *processAttentionConsumer) liveRows() ([]livePaneRow, error) {
	records, err := c.records()
	if err != nil {
		return nil, err
	}
	rows := []livePaneRow{}
	for _, r := range records {
		inputs := []notify.PushInput{}
		for _, pending := range r.Pending {
			inputs = append(inputs, processAttentionInput(r, pending.Sequence, pending.Kind))
		}
		if r.Badge == aibadge.ResponseComplete && r.NoticeSequence != 0 {
			inputs = append(inputs, processAttentionInput(r, r.NoticeSequence, r.NoticeKind))
		}
		// Include idle/terminal panes in inventory so retained notices follow
		// the existing expired+gone rule rather than tmux absence.
		if len(inputs) == 0 {
			rows = append(rows, livePaneRow{Session: r.Binding.Project, Window: r.Binding.Window, Pane: r.Binding.Pane, Agent: r.Provider})
		}
		for i := range inputs {
			in := inputs[i]
			rows = append(rows, livePaneRow{Session: in.Target.Session, Window: in.Target.Window, Pane: in.Target.Pane, Agent: r.Provider,
				AttentionState: attentionStateReply, AIState: "waiting", ReplyState: true,
				AgentUID: r.Binding.Agent, PaneUID: r.Binding.Pane, AuthorityFence: in.Metadata[notify.MetaAuthorityFence], processNotice: &in})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Pane < rows[j].Pane || (rows[i].Pane == rows[j].Pane && reconcileEntryID(rows[i]) < reconcileEntryID(rows[j]))
	})
	return rows, nil
}
