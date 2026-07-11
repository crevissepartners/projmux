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
	Pushed int      `json:"pushed"`
	Acked  int      `json:"acked"`
	Kept   int      `json:"kept"`
	Stale  int      `json:"stale"`
	Errors []string `json:"errors"`
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
// against the persistent notify queue, and back-fills entries whose derived
// AI reply rows are missing or stale. It never removes unacknowledged rows:
// ack is the user's consume signal. Returns an error only for argument parsing
// or store failures; tmux call failures are surfaced through the
// `errors` field of the summary so install scripts get a single
// non-fatal pass.
func (c *notifyCommand) runReconcile(args []string, stdout, stderr io.Writer) error {
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
		// post-install hook does not break.
		result.Errors = append(result.Errors, listErr.Error())
		return writeReconcileSummary(stdout, result, *asJSON)
	}

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
		if _, _, err := store.Push(in); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("push %s: %v", id, err))
			continue
		}
		c.publishNotifyQueueRefreshBestEffort()
		result.Pushed++
	}

	// Stale pass: for every queue entry whose key starts with `ai:`, report it
	// if the pane is no longer reply-state with an agent. Do not ack it; users
	// must explicitly clear rows under the notify SOT contract.
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

func attentionNotifyMetadataMatches(got, want map[string]string) bool {
	for _, key := range []string{notify.MetaAgent, notify.MetaCategory, notify.MetaState, notify.MetaTopic} {
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
	_, err := fmt.Fprintf(w, "reconcile: pushed %d, acked %d, kept %d, stale %d\n", r.Pushed, r.Acked, r.Kept, r.Stale)
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
