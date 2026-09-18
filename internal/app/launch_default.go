package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// The saved launch default applied to a Window's first Pane.
//
// A generated Window create commits a Window whose first Pane is the topology
// engine's default shell, and that is the Pane this file replaces -- or keeps.
// The saved mode file is hidden state, so reading it stays where it has always
// lived: on aiCommand, beside runLaunchDefault. The Window producers reach it
// only through applyLaunchDefault, which is why this helper states its whole
// input -- the origin shell `%N` and the exact pressing client -- instead of
// looking at $TMUX_PANE, the Window's Pane order, or the client tmux happens to
// consider current.

// splitReplaceOriginEnv marks a split UI producer whose committed Agent Pane
// replaces the Pane it split off. It travels from the Window producer into the
// picker popup, which is a separate process, and it is private: no public CLI
// flag spells it, because "replace the Pane I started from" is not something an
// operator types -- it is what the Window producer already decided when it
// created a shell Pane only so the picker would have an origin.
const splitReplaceOriginEnv = "PROJMUX_SPLIT_REPLACE_ORIGIN"

// launchDefaultResult is what applying the saved launch default leaves for the
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
	// picker is true once a picker popup owns the rest of the action. The
	// producer then says nothing: the popup is the feedback, and a line
	// displayed after it closed would overwrite whatever the picker reported.
	picker bool
	// committed is true once the canonical create behind this result has
	// committed, whatever happened afterwards. It is what tells the replacing
	// split funnel whether its line describes a durable mutation -- and so
	// whether a failure to show that line may become the route's exit status.
	committed bool
}

// launchDefaultFunc is the injected route a Window producer applies its saved
// launch default through. It is a func so a unit test can fake the whole
// application without an aiCommand, and so the mode file stays readable in
// exactly one place.
type launchDefaultFunc func(originPaneID, client string) launchDefaultResult

// applyLaunchDefault opens the saved launch default on an already-committed
// shell Pane.
//
// `shell` is the one mode with nothing to do: the Pane the Window was created
// with is already the answer. A provider mode replaces that Pane with an Agent
// through the same canonical create funnel a split uses, then removes the shell
// through the canonical Pane delete. The picker modes hand the choice to the
// operator in the popup they already have, carrying the replacement intent with
// them.
func (c *aiCommand) applyLaunchDefault(originPaneID, client string) launchDefaultResult {
	origin := exactTmuxHandle(strings.TrimSpace(originPaneID), "%")
	client = strings.TrimSpace(client)
	if origin == "" || client == "" {
		return launchDefaultResult{problem: fmt.Sprintf(
			"projmux could not apply the saved launch default: origin Pane %q, client %q", originPaneID, client)}
	}
	mode := c.getMode()
	switch mode {
	case aiModeClaude, aiModeCodex, aiModeAntigravity:
		// The Settings gate runs before the intent is built, exactly as the
		// saved-default split key runs it: a default that has since been
		// switched off keeps the shell and costs zero mutations.
		if message, disabled := c.aiAgentDisabledLaunchMessage(mode, aiSplitLaunchDefault); disabled {
			return launchDefaultResult{problem: keptOriginShellLine(message)}
		}
		return c.replaceOriginShellWithAgent(agentPaneIntent{
			producer: canonicalProducerSavedDefault, provider: mode, placement: "right",
			anchorPaneID: origin, targetClient: client,
		})
	case aiModeShell:
		return launchDefaultResult{}
	case aiModeResume:
		return c.openReplacingPicker(origin, client, aiResumePickerPopupMode("right"))
	default:
		// aiModeSelective and any unrecognized saved value open the picker,
		// which is what an unset mode has always meant.
		return c.openReplacingPicker(origin, client, aiSplitPickerPopupMode("right"))
	}
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
		return launchDefaultResult{problem: strings.TrimSpace(err.Error()), committed: true}
	}
	return launchDefaultResult{notice: notice, committed: true}
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

// openReplacingPicker opens one of the existing split pickers on the exact
// pressing client, anchored on the exact origin shell, and marks the popup so
// whatever the operator picks replaces that shell.
//
// It goes through `internal tmux popup-toggle` like every other picker opener,
// which is what gives the popup its marker, its geometry, and its own close
// key; the two things this caller adds are the exact client and anchor it was
// handed, so nothing here consults an ambient `display-message -p` client.
func (c *aiCommand) openReplacingPicker(originPaneID, client, mode string) launchDefaultResult {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return launchDefaultResult{problem: keptOriginShellLine("projmux could not resolve its own binary: " + strings.TrimSpace(err.Error()))}
	}
	args := []string{"internal", "tmux", "popup-toggle", "--client", client, "--anchor", originPaneID,
		popupToggleReplaceOriginFlag, mode}
	if err := c.run(binaryPath, args...); err != nil && !isNoSelectionExit(err) {
		return launchDefaultResult{problem: keptOriginShellLine("projmux could not open the launch picker: " + strings.TrimSpace(err.Error()))}
	}
	return launchDefaultResult{picker: true}
}

// keptOriginShellLine appends what did not change to a failure reason. The
// Window and its shell Pane are committed by the time any of this runs, and a
// bare failure line reads like a rollback that never happened.
func keptOriginShellLine(reason string) string {
	reason = strings.Join(strings.Fields(strings.TrimSpace(reason)), " ")
	return reason + "; the Window keeps its shell Pane"
}

// splitReplacesOrigin reports whether this split UI process was started to
// replace its origin Pane. Like splitOriginPane it is read here and only here:
// the marker is the popup's own knowledge, not an ambient mode other verbs may
// consult.
func (c *aiCommand) splitReplacesOrigin() bool {
	return strings.TrimSpace(c.env(splitReplaceOriginEnv)) == "1"
}

// finishReplacingSplit is the replace-mode half of createPaneFromIntent's
// terminal actions. Every picker row lands here: a provider row, the Codex
// advanced row, a resume row, and the resume picker's `new` row all commit an
// Agent that takes the origin shell's place, while a `shell` row has nothing to
// create -- the origin shell already is a shell, and creating a second one is
// the bug this branch exists to prevent.
func (c *aiCommand) finishReplacingSplit(intent agentPaneIntent) error {
	if strings.TrimSpace(intent.provider) == "" {
		return nil
	}
	result := c.replaceOriginShellWithAgent(intent)
	line := result.problem
	if line == "" && result.notice != "" {
		line = "projmux: " + result.notice
	}
	if line == "" {
		return nil
	}
	if result.committed {
		// The Agent is durable. Whether the line is the start notice or the
		// shell that could not be removed, failing to show it does not undo the
		// create, so it goes to the journal instead of this route's exit status
		// (committed_result.go).
		c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitReplace, intent.targetClient, line)
		return nil
	}
	return c.displaySplitLine(intent.targetClient, line)
}

// displaySplitLine shows one bounded line on the exact client that asked for
// the Pane, or on whatever client tmux resolves when the producer carried
// none. It is the transport both halves of the split funnel share; the display
// failure it returns is only the route's result for a caller whose mutation did
// not commit. A committed caller reaches it through showCommittedSplitResult,
// which swallows that failure and journals it.
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
