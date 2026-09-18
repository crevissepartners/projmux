package app

import (
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// The committed-result seam.
//
// An interactive tmux adapter route finishes its intent without error -- it
// commits a canonical Pane, Window, rename or deletion, or it establishes that
// the operator asked for nothing -- and then puts one bounded line on the exact
// client that asked for it. Until this seam existed, nine of those sites returned
// the failure to *show* that line, and the generated producer on the other end of
// the call is a foreground tmux `run-shell` job: tmux paints its non-zero exit as
// a `'<command>' returned 1` view-mode screen over the pane the operator was
// working in. The Pane was there. The action read as if it had failed.
//
// run_shell_output_ledger.go owns the classification that forbids that overlay.
// This file owns the half the ledger could not state: once the intent is past
// refusing, the route returns nil whatever happens to the line. The loss is not
// silent -- it becomes one journal record, and nothing else.
//
// The predicate the rows below are classified by is "a line this adapter owed
// the client after an intent that did not fail", which is slightly wider than
// "the mutation committed": a blank rename response commits nothing and still
// owes the operator the line that says so. Using the narrower predicate would
// make the journal record something untrue about that one path.
//
// Refusals are deliberately not routed here. A refusal that reached nobody is a
// refusal the operator has to learn about some other way, and its non-zero exit
// is the only remaining channel.

// committedResultJournal is the journal half of the seam. Both recorders the
// interactive routes already carry satisfy it -- aiCommand.operationalDiagnostics
// and tmuxCommand.diagnostics -- so no new wiring reaches these routes.
type committedResultJournal interface {
	RecordUnshownResult(site diagnostics.SurfaceSite, started time.Time)
}

// showCommittedResult shows one result line for an already-committed mutation.
//
// It returns nothing, on purpose. A caller cannot convert the display failure
// back into the route's exit status, which is the whole of the contract: the
// type is the enforcement, and the source sweep in committed_result_test.go
// only has to prove every committed site comes through here.
func showCommittedResult(journal committedResultJournal, site diagnostics.SurfaceSite, show func() error) {
	if show == nil {
		return
	}
	started := time.Now()
	if err := show(); err != nil {
		recordUnshownCommittedResult(journal, site, started)
	}
}

// recordUnshownCommittedResult keeps the nil-interface check in one place. The
// recorder methods tolerate a nil receiver, but a command literal that never set
// the field at all hands over a nil interface value.
func recordUnshownCommittedResult(journal committedResultJournal, site diagnostics.SurfaceSite, started time.Time) {
	if journal == nil {
		return
	}
	journal.RecordUnshownResult(site, started)
}

// showCommittedSplitResult is the split funnel's entry point into the seam. The
// line goes to the exact client that asked for the Pane, or to whatever client
// tmux resolves when the producer carried none, exactly as displaySplitLine has
// always addressed it.
func (c *aiCommand) showCommittedSplitResult(site diagnostics.SurfaceSite, client, line string) {
	showCommittedResult(c.operationalDiagnostics, site, func() error {
		return c.displaySplitLine(client, line)
	})
}

// showCommittedIntentResult is the pane-menu and Window intent entry point.
func (c *tmuxCommand) showCommittedIntentResult(site diagnostics.SurfaceSite, client, message string) {
	showCommittedResult(c.diagnostics, site, func() error {
		return c.displayPaneMenuMessage(client, message)
	})
}

// committedResultKind is how one registered source occurrence relates to the
// contract. It is the whole classification: a row is exactly one of the three.
type committedResultKind string

const (
	// committedResultCommitted is a call made after the intent is past refusing:
	// the mutation is durable, or there was nothing to mutate. It must go through
	// showCommittedResult, so the display failure cannot become the route's exit
	// status.
	committedResultCommitted committedResultKind = "committed"
	// committedResultPreCommit is a call made on a refusal. Its display failure
	// stays the route's result: a refusal that reached nobody has no other
	// channel left.
	committedResultPreCommit committedResultKind = "pre-commit"
	// committedResultTransport is the body of a shared display helper. It
	// carries no classification of its own -- its callers do.
	committedResultTransport committedResultKind = "transport"
)

// committedResultSite is one source occurrence that puts an operator-facing
// result line on a client for an interactive intent of this adapter.
type committedResultSite struct {
	// File and Snippet attribute one source occurrence to this row. Snippet is a
	// substring of the source line that makes the call.
	File    string
	Snippet string
	// Kind is the one classification this row holds.
	Kind committedResultKind
	// Site is the journal identity a committed row records. It is empty for the
	// other two kinds, which write no record.
	Site diagnostics.SurfaceSite
	// Note records why the row is classified the way it is.
	Note string
}

// committedResultDisplaySites is the closed set of result-line call sites in the
// interactive tmux adapter layer -- the split funnel, the pane context menu, and
// the Window intents. A new one fails the source sweep in
// committed_result_test.go until it is registered and classified, which is the
// whole point: the overlay this contract removes was not a typo, it was an
// unclassified post-commit return.
func committedResultDisplaySites() []committedResultSite {
	return []committedResultSite{
		// --- split funnel: internal/app/ai.go --------------------------------
		{
			File: "ai.go", Snippet: "c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitFocus",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteSplitFocus,
			Note: "the split committed and the new Pane could not be focused; the Pane stays either way",
		},
		{
			File: "ai.go", Snippet: "c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitNotice",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteSplitNotice,
			Note: "the split committed and its start notice says the requested Pane directory was not used",
		},
		{
			File: "ai.go", Snippet: `c.run("tmux", "display-message", "-c", intent.targetClient, "-d", "10000", reason)`,
			Kind: committedResultPreCommit,
			Note: "canonical create refused before committing anything; the refusal is the route's own result",
		},

		// --- split funnel: internal/app/launch_default.go ---------------------
		{
			File: "launch_default.go", Snippet: "c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitReplace",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteSplitReplace,
			Note: "the replacing Agent committed; a failed shell delete or a start notice rides on this line",
		},
		{
			File: "launch_default.go", Snippet: "return c.displaySplitLine(intent.targetClient, line)",
			Kind: committedResultPreCommit,
			Note: "the replacing create refused before committing; nothing was created and nothing was removed",
		},
		{
			File: "launch_default.go", Snippet: `displayErr = c.run("tmux", "display-message", "-c", client, "-d", "10000", message)`,
			Kind: committedResultTransport,
			Note: "displaySplitLine, the transport both halves of the split funnel share",
		},

		// --- pane context menu and Window intents: internal/app/tmux.go -------
		{
			File: "tmux.go", Snippet: `return c.displayPaneMenuMessage(strings.TrimSpace(*client), "projmux "+paneMenuActionLabel(action)+" failed: "+reason)`,
			Kind: committedResultPreCommit,
			Note: "the pane-menu create or delete refused; nothing was committed",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuKill",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSitePaneMenuKill,
			Note: "the Pane is already gone; its summary is the only description left of it",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuSplit, strings.TrimSpace(*client), splitFocusFailureLine(",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSitePaneMenuSplit,
			Note: "the pane-menu split committed and the clicking client could not be moved onto it",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuSplit, strings.TrimSpace(*client), message)",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSitePaneMenuSplit,
			Note: "the pane-menu split committed and focused; the bounded success line carries any start notice",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSiteWindowIntent, pressing, line)",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteWindowIntent,
			Note: "the Window committed and was filled with the chosen first Pane, and the pressing client could not be moved onto it; a failed fill rides on this line",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSiteWindowIntent, pressing, applied.problem)",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteWindowIntent,
			Note: "the Window and its shell Pane committed; filling it with the chosen first Pane is what did not happen",
		},
		{
			File: "tmux.go", Snippet: `return c.displayPaneMenuMessage(strings.TrimSpace(client), "projmux "+label+" failed: "+reason)`,
			Kind: committedResultPreCommit,
			Note: "finishWindowIntent's failure half: the Window intent refused and committed nothing",
		},
		{
			File: "tmux.go", Snippet: "c.showCommittedIntentResult(diagnostics.SurfaceSiteWindowIntent, strings.TrimSpace(client), success)",
			Kind: committedResultCommitted, Site: diagnostics.SurfaceSiteWindowIntent,
			Note: "finishWindowIntent's success half: the create, rename, or delete it reports is durable, and a blank rename response owes the line that says nothing was requested",
		},
		{
			File: "tmux.go", Snippet: `c.runner.Run(context.Background(), "tmux", "display-message", "-c", client, "-d", "10000", tmuxLiteralMessage(message))`,
			Kind: committedResultTransport,
			Note: "displayPaneMenuMessage, the transport the pane-menu and Window intents share",
		},
	}
}

// committedResultSweepFiles is the source the sweep reads. It is the interactive
// tmux adapter layer C-1 scopes: the split funnel, the pane context menu, and the
// Window intents. Startup paths own their own reporting and are audited, not
// swept, so this contract never edits a file another track owns.
func committedResultSweepFiles() []string {
	return []string{"ai.go", "launch_default.go", "tmux.go"}
}

// committedResultSweepTokens is the closed set of ways this layer puts a line on
// a client: the two shared helpers, the two committed entry points, and a raw
// `display-message` addressed at an exact client.
func committedResultSweepTokens() []string {
	return []string{
		"c.displaySplitLine(",
		"c.displayPaneMenuMessage(",
		"c.showCommittedSplitResult(",
		"c.showCommittedIntentResult(",
		`"display-message", "-c"`,
	}
}
