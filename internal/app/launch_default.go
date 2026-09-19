package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The saved launch default applied to a Window's first Pane.
//
// A Window producer commits a Window whose first Pane is the topology engine's
// default shell, and that is the Pane this file replaces -- or keeps. The
// answer is decided before the Window is committed (window_create_launch_choice.go);
// what is left here is filling it in: the Agent is committed beside the shell,
// the shell is removed through the canonical Pane delete, and the helpers state
// their whole input -- the origin shell `%N` and the exact pressing client --
// instead of looking at $TMUX_PANE, the Window's Pane order, or the client tmux
// happens to consider current.

// launchDefaultResult is what filling a Window's first Pane leaves for the
// Window producer to say on the pressing client. It never carries a rollback:
// the Window and its shell Pane are committed before this runs, and every
// failure below keeps them.
type launchDefaultResult struct {
	// problem replaces the producer's success line with one bounded reason:
	// a Settings refusal, a create that did not commit, or a shell Pane that
	// could not be removed after one did.
	problem string
	// notice is the split start notice of a committed Agent create, which
	// rides on the producer's success line exactly as it does for a split.
	notice string
}

// replaceOriginShellWithAgent commits the Agent beside the origin shell and
// then deletes the shell, in that order: the Window never sits Paneless, and a
// create that refuses leaves the operator with the shell they can work in.
func (c *aiCommand) replaceOriginShellWithAgent(intent agentPaneIntent) launchDefaultResult {
	if c.panes == nil {
		return launchDefaultResult{problem: "the Projmux split UI has no canonical create route configured"}
	}
	var diagnostics bytes.Buffer
	created, err := c.panes.createFromIntent(intent, io.Discard, &diagnostics)
	notice := strings.Join(strings.Fields(diagnostics.String()), " ")
	if err != nil {
		return launchDefaultResult{problem: keptOriginShellLine(canonicalCreateFailureReason(err, diagnostics.String()))}
	}
	// The committed Agent is focused the way every UI split focuses its new
	// Pane. A failed focus is not reported: the delete below leaves the Agent
	// as the Window's only Pane, and if the delete fails instead, its own line
	// is the one thing the operator needs.
	focusRunner := splitFocusRunner{runCommand: c.runCommand, readCommand: c.readCommand}
	_ = focusCreatedSplitPane(context.Background(), focusRunner, intent.targetClient, created)
	if err := c.deleteOriginShell(intent.anchorPaneID); err != nil {
		return launchDefaultResult{problem: strings.TrimSpace(err.Error())}
	}
	return launchDefaultResult{notice: notice}
}

// deleteOriginShell removes the replaced shell through the canonical Pane
// delete -- the same route the Pane menu Kill item uses -- naming the exact
// Pane instead of letting the delete resolve an active target. A refusal here
// leaves both Panes: the Agent is live, and inventing a raw `kill-pane` to
// tidy up would be the one mutation the Registry never heard about.
func (c *aiCommand) deleteOriginShell(paneID string) error {
	pane := exactTmuxHandle(strings.TrimSpace(paneID), "%")
	if pane == "" {
		return errors.New("projmux opened the Agent, but the replaced shell Pane is not an exact %N; both Panes stay")
	}
	if c.paneDelete == nil {
		return errors.New("projmux opened the Agent, but no canonical Pane delete route is configured; both Panes stay")
	}
	var deleted, refusal bytes.Buffer
	if err := c.paneDelete(pane, &deleted, &refusal); err != nil {
		reason := strings.TrimSpace(err.Error())
		if detail := strings.TrimSpace(refusal.String()); detail != "" && !strings.Contains(reason, detail) {
			reason += ": " + detail
		}
		return fmt.Errorf("projmux opened the Agent, but could not remove the shell Pane %s: %s; both Panes stay",
			pane, strings.Join(strings.Fields(reason), " "))
	}
	return nil
}

// keptOriginShellHead leads a failure line whose Window and shell Pane are
// already committed. What did not change comes first: a bare failure line reads
// like a rollback that never happened, and a reason long enough to fill the
// status line would push an outcome written after it off the screen.
const keptOriginShellHead = "the Window keeps its shell Pane: "

// keptOriginShellLine leads a failure reason with what did not change. The
// display site fits it to the client with the head kept whole
// (displayFreshLaunchDefaultLine).
func keptOriginShellLine(reason string) string {
	return keptOriginShellHead + strings.Join(strings.Fields(reason), " ")
}

// displaySplitLine shows one bounded line on the exact client that asked for
// the Pane, or on whatever client tmux resolves when the producer carried
// none. It is the transport of the split funnel's committed lines: every caller
// reaches it through showCommittedSplitResult, which swallows the display
// failure it returns and journals it.
func (c *aiCommand) displaySplitLine(client, line string) error {
	message := tmuxLiteralMessage(line)
	var displayErr error
	if client = strings.TrimSpace(client); client != "" {
		displayErr = c.run("tmux", "display-message", "-c", client, "-d", "10000", message)
	} else {
		displayErr = c.run("tmux", "display-message", "-d", "10000", message)
	}
	if displayErr != nil {
		return fmt.Errorf("%s; display the split result to client %q: %v", line, client, displayErr)
	}
	return nil
}
