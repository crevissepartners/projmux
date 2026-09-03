package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// reconcileResult is the summary returned by the reconcile pass.
type reconcileResult struct {
	Pushed  int      `json:"pushed"`
	Acked   int      `json:"acked"`
	Kept    int      `json:"kept"`
	Stale   int      `json:"stale"`
	Evicted int      `json:"evicted"`
	Errors  []string `json:"errors"`
}

// reconcileShouldHaveQueueEntry reports whether the pane's live state means
// it must be present in the notify queue. The producer pushes when the
// attention state machine reports `reply` AND there is an AI agent
// associated with the pane (manual `attention toggle` on a shell pane is
// intentionally skipped). The reconcile pass mirrors that contract.
func reconcileShouldHaveQueueEntry(p livePaneRow) bool {
	if strings.TrimSpace(p.Agent) == "" {
		return false
	}
	if p.ReplyState {
		return true
	}
	// The AI flow flips attention to reply on `status set waiting` but the
	// option is set on the pane via tmux which is observed through the
	// attention option; if the attention option is unset (e.g. via
	// `attention clear`) we do not consider the pane reply-state.
	return false
}

// reconcileEntryID returns the canonical queue id used by the producer.
// Reusing the shared helper guarantees push/ack/reconcile all agree on the
// key.
func reconcileEntryID(p livePaneRow) string {
	return buildAttentionNotifyID(p.Session, p.Pane)
}

// reconcileEntryText returns the canonical queue text used by the producer.
// Reusing the shared helper guarantees the agent/topic rendering stays in
// lockstep with the event-driven path.
func reconcileEntryText(p livePaneRow) string {
	return composeAttentionReplyText(p.Agent, p.Topic)
}

// runReconcile walks every tmux pane on the host, compares the live state
// against the persistent notify queue, back-fills entries whose derived AI
// reply rows are missing or stale, and applies the bounded eviction policy.
// Live rows remain explicit-ack-only unless they are hard-cap overflow.
// Returns an error only for argument parsing or store failures; tmux call
// failures are surfaced through the `errors` field of the summary so install
// scripts get a single non-fatal pass.
func (c *notifyCommand) runReconcile(args []string, stdout, stderr io.Writer) error {
	return c.runReconcileWithOwnership(args, stdout, stderr, true)
}

func (c *notifyCommand) runReconcileWithOwnership(args []string, stdout, stderr io.Writer, ownsTopLevel bool) error {
	fs := flag.NewFlagSet("notify reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printNotifyReconcileUsage(stderr) }
	asJSON := fs.Bool("json", false, "emit json instead of human output")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageError(fmt.Sprintf("parse notify reconcile flags: %v", err))
	}
	if fs.NArg() != 0 {
		printNotifyUsage(stderr)
		return usageError("notify reconcile does not accept positional arguments")
	}

	store, err := c.requireStore()
	if err != nil {
		return err
	}

	result := reconcileResult{Errors: []string{}}

	panes, listErr := c.listLivePaneRows()
	if listErr != nil {
		// Common case: tmux is not running. Treat as soft failure so the
		// post-install hook does not break. Inventory-dependent TTL eviction
		// is skipped, but the hard cap remains safe to enforce.
		result.Errors = append(result.Errors, listErr.Error())
		eviction, reconcileErr := store.Reconcile(nil)
		if reconcileErr != nil {
			return fmt.Errorf("reconcile notifications: %w", reconcileErr)
		}
		result.Evicted = eviction.Removed()
		if result.Evicted > 0 {
			c.publishNotifyQueueRefreshBestEffort()
		}
		return writeReconcileSummary(stdout, result, *asJSON)
	}

	paneSet := newNotifyLivePaneSet(panes)
	sessionSet := newNotifyLiveSessionSet(panes)
	wantByID := make(map[string]livePaneRow, len(panes))
	for _, p := range panes {
		if !reconcileShouldHaveQueueEntry(p) {
			continue
		}
		wantByID[reconcileEntryID(p)] = p
	}

	existing, listQueueErr := store.List()
	if listQueueErr != nil {
		return fmt.Errorf("list notifications: %w", listQueueErr)
	}

	existingByID := make(map[string]notify.Notification, len(existing))
	for _, e := range existing {
		existingByID[e.ID] = e
	}

	// Push pass: for every pane that should be in the queue, add or refresh.
	for id, pane := range wantByID {
		want := reconcileEntryText(pane)
		metadata := mergeAttentionNotifyMetadata(nil, pane.Agent, pane.Topic, notify.SeverityInfo)
		if pane.AuthorityFence != "" {
			metadata[notify.MetaAgentUID] = pane.AgentUID
			metadata[notify.MetaPaneUID] = pane.PaneUID
			metadata[notify.MetaStateDomainID] = pane.StateDomainID
			metadata[notify.MetaEndpointGenerationID] = pane.EndpointGenerationID
			metadata[notify.MetaAuthorityFence] = pane.AuthorityFence
		}
		if cur, ok := existingByID[id]; ok && cur.Text == want && attentionNotifyMetadataMatches(cur.Metadata, metadata) {
			result.Kept++
			continue
		}
		in := notify.PushInput{
			ID:       id,
			Text:     want,
			Severity: notify.SeverityInfo,
			Source:   notify.SourceAI,
			Metadata: metadata,
			TTL:      attentionNotifyTTL,
			Target: notify.Target{
				Socket:  pane.Socket,
				Session: pane.Session,
				Window:  pane.Window,
				Pane:    pane.Pane,
			},
		}
		started := c.clock()
		_, pushResult, err := store.Push(in)
		if err != nil {
			recordNotifyEnqueue(c.diagnostics, in, pushResult, err, started, ownsTopLevel)
			result.Errors = append(result.Errors, fmt.Sprintf("push %s: %v", id, err))
			continue
		}
		recordNotifyEnqueue(c.diagnostics, in, pushResult, nil, started, ownsTopLevel)
		c.publishNotifyQueueRefreshBestEffort()
		result.Pushed++
	}

	eviction, reconcileErr := store.Reconcile(func(entry notify.Notification) bool {
		return reconcileTargetExists(entry, paneSet, sessionSet)
	})
	if reconcileErr != nil {
		return fmt.Errorf("reconcile notifications: %w", reconcileErr)
	}
	result.Evicted = eviction.Removed()
	if result.Evicted > 0 {
		c.publishNotifyQueueRefreshBestEffort()
		existing, listQueueErr = store.List()
		if listQueueErr != nil {
			return fmt.Errorf("list reconciled notifications: %w", listQueueErr)
		}
	}

	// Stale pass: for every remaining queue entry whose key starts with `ai:`,
	// report it if the pane is no longer reply-state with an agent. Do not ack
	// it; users must explicitly clear retained rows under the notify SOT
	// contract.
	for _, e := range existing {
		if !strings.HasPrefix(e.ID, "ai:") {
			continue
		}
		if _, ok := wantByID[e.ID]; ok {
			continue
		}
		result.Stale++
	}

	return writeReconcileSummary(stdout, result, *asJSON)
}

type notifyLiveSessionSet map[string]struct{}

func newNotifyLiveSessionSet(rows []livePaneRow) notifyLiveSessionSet {
	if len(rows) == 0 {
		return nil
	}
	set := make(notifyLiveSessionSet, len(rows))
	for _, row := range rows {
		session := strings.TrimSpace(row.Session)
		if session != "" {
			set[session] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// reconcileTargetExists reuses the same real pane inventory and pane-first
// GONE classification as the notify UI. Session/window-only rows fall back to
// session membership because they do not carry a concrete pane id.
func reconcileTargetExists(entry notify.Notification, paneSet notifyLivePaneSet, sessionSet notifyLiveSessionSet) bool {
	if paneSet == nil || sessionSet == nil {
		return true
	}
	if strings.TrimSpace(entry.Pane) != "" {
		return classifyNotifyRowState(entry, nil, paneSet) != notifyDisplayGone
	}
	_, ok := sessionSet[strings.TrimSpace(entry.Session)]
	return ok
}

func attentionNotifyMetadataMatches(got, want map[string]string) bool {
	for _, key := range []string{
		notify.MetaAgent, notify.MetaCategory, notify.MetaState, notify.MetaTopic,
		notify.MetaAgentUID, notify.MetaPaneUID, notify.MetaStateDomainID,
		notify.MetaEndpointGenerationID, notify.MetaAuthorityFence,
	} {
		if strings.TrimSpace(got[key]) != strings.TrimSpace(want[key]) {
			return false
		}
	}
	return true
}

// writeReconcileSummary renders the result as either JSON or a single
// human-readable line.
func writeReconcileSummary(w io.Writer, r reconcileResult, asJSON bool) error {
	if r.Errors == nil {
		r.Errors = []string{}
	}
	if asJSON {
		return writeJSON(w, r)
	}
	_, err := fmt.Fprintf(w, "reconcile: pushed %d, acked %d, kept %d, stale %d, evicted %d\n", r.Pushed, r.Acked, r.Kept, r.Stale, r.Evicted)
	if err != nil {
		return err
	}
	for _, e := range r.Errors {
		if _, err := fmt.Fprintf(w, "reconcile error: %s\n", e); err != nil {
			return err
		}
	}
	return nil
}

// reconcileDefaultRunner builds the production tmux runner for the
// reconcile pass. Kept as a small helper so wiring stays in one place.
func reconcileDefaultRunner() tmuxRunner {
	return inttmux.ExecRunner{}
}
