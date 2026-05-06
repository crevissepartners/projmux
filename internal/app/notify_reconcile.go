package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/notify"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// reconcileListPanesFormat is the tab/pipe-separated format used by the
// reconcile pass. The producer key set (session, pane id, agent, topic,
// socket) drives id construction and queue text composition; the
// attention/ai state fields decide whether the pane should be in the queue.
const reconcileListPanesFormat = "" +
	"#{session_name}|" +
	"#{window_id}|" +
	"#{pane_id}|" +
	"#{" + attentionStateOption + "}|" +
	"#{" + aiPaneStateOption + "}|" +
	"#{" + aiPaneAgentOption + "}|" +
	"#{" + aiPaneTopicOption + "}|" +
	"#{socket_path}"

// reconcileResult is the summary returned by the reconcile pass.
type reconcileResult struct {
	Pushed int      `json:"pushed"`
	Acked  int      `json:"acked"`
	Kept   int      `json:"kept"`
	Errors []string `json:"errors"`
}

// reconcilePane is the projection of one row from
// `tmux list-panes -a -F <reconcileListPanesFormat>` after parsing.
type reconcilePane struct {
	Session        string
	Window         string
	Pane           string
	AttentionState string
	AIState        string
	Agent          string
	Topic          string
	Socket         string
}

// shouldHaveQueueEntry reports whether the pane's live state means it must
// be present in the notify queue. The producer pushes when the attention
// state machine reports `reply` AND there is an AI agent associated with
// the pane (manual `attention toggle` on a shell pane is intentionally
// skipped). The reconcile pass mirrors that contract.
func (p reconcilePane) shouldHaveQueueEntry() bool {
	if strings.TrimSpace(p.Agent) == "" {
		return false
	}
	if p.AttentionState == attentionStateReply {
		return true
	}
	// The AI flow flips attention to reply on `status set waiting` but the
	// option is set on the pane via tmux which is observed through the
	// attention option; if the attention option is unset (e.g. via
	// `attention clear`) we do not consider the pane reply-state.
	return false
}

// id returns the canonical queue id used by the producer. Reusing the
// shared helper guarantees push/ack/reconcile all agree on the key.
func (p reconcilePane) id() string {
	return buildAttentionNotifyID(p.Session, p.Pane)
}

// text returns the canonical queue text used by the producer. Reusing the
// shared helper guarantees the agent/topic rendering stays in lockstep
// with the event-driven path.
func (p reconcilePane) text() string {
	return composeAttentionReplyText(p.Agent, p.Topic)
}

// runReconcile walks every tmux pane on the host, compares the live state
// against the persistent notify queue, and back-fills/clears entries so
// the queue matches reality. Returns an error only for argument parsing
// or store failures; tmux call failures are surfaced through the
// `errors` field of the summary so install scripts get a single
// non-fatal pass.
func (c *notifyCommand) runReconcile(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("notify reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit json instead of human output")

	if err := fs.Parse(args); err != nil {
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

	panes, listErr := c.listReconcilePanes()
	if listErr != nil {
		// Common case: tmux is not running. Treat as soft failure so the
		// post-install hook does not break.
		result.Errors = append(result.Errors, listErr.Error())
		return writeReconcileSummary(stdout, result, *asJSON)
	}

	wantByID := make(map[string]reconcilePane, len(panes))
	for _, p := range panes {
		if !p.shouldHaveQueueEntry() {
			continue
		}
		wantByID[p.id()] = p
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
		want := pane.text()
		if cur, ok := existingByID[id]; ok && cur.Text == want {
			result.Kept++
			continue
		}
		in := notify.PushInput{
			ID:       id,
			Text:     want,
			Severity: notify.SeverityInfo,
			Source:   notify.SourceAI,
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
		result.Pushed++
	}

	// Ack pass: for every queue entry whose key starts with `ai:`, drop it
	// if the pane is no longer reply-state with an agent.
	for _, e := range existing {
		if !strings.HasPrefix(e.ID, "ai:") {
			continue
		}
		if _, ok := wantByID[e.ID]; ok {
			continue
		}
		if err := store.Ack(e.ID); err != nil && !errors.Is(err, notify.ErrNotFound) {
			result.Errors = append(result.Errors, fmt.Sprintf("ack %s: %v", e.ID, err))
			continue
		}
		result.Acked++
	}

	return writeReconcileSummary(stdout, result, *asJSON)
}

// listReconcilePanes shells out to `tmux list-panes -a -F ...` and parses
// every live pane row. Empty/blank rows are skipped. A nil runner short-
// circuits to an empty slice so unit tests that exercise unrelated paths
// do not need to wire a fake.
func (c *notifyCommand) listReconcilePanes() ([]reconcilePane, error) {
	if c == nil || c.runner == nil {
		return nil, errors.New("tmux runner is not configured")
	}
	output, err := c.runner.Run(context.Background(), "tmux", "list-panes", "-a", "-F", reconcileListPanesFormat)
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	out := make([]reconcilePane, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		// The format string emits exactly 8 fields; tmux silently substitutes
		// empty strings for unset options so a short row is malformed.
		if len(fields) < 8 {
			continue
		}
		p := reconcilePane{
			Session:        strings.TrimSpace(fields[0]),
			Window:         strings.TrimSpace(fields[1]),
			Pane:           strings.TrimSpace(fields[2]),
			AttentionState: strings.TrimSpace(fields[3]),
			AIState:        strings.TrimSpace(fields[4]),
			Agent:          strings.TrimSpace(fields[5]),
			Topic:          strings.TrimSpace(fields[6]),
			Socket:         strings.TrimSpace(fields[7]),
		}
		if p.Session == "" || p.Pane == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
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
	_, err := fmt.Fprintf(w, "reconcile: pushed %d, acked %d, kept %d\n", r.Pushed, r.Acked, r.Kept)
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
