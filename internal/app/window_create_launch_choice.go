package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The new Window's first Pane, decided before the Window exists.
//
// A generated Window create used to commit the Window with its default shell
// Pane, move the pressing client onto it, and only then open the saved launch
// default on that shell -- so a picker appeared over a Window the operator had
// not finished choosing, and a provider default swapped the shell for an Agent
// in front of them. The create is now split in two around the commit:
//
//  1. chooseLaunchDefault reads the saved mode and, for the picker modes, asks
//     on the Pane the operator pressed the key in. Nothing exists yet, so a
//     cancelled picker creates nothing.
//  2. The producer commits the Window, applyLaunchChoice fills its shell Pane
//     with the answer, and only then is the pressing client moved onto it.
//
// A fresh Project open is the other producer that asks this way: its one
// Window is the new Window, and the question runs before anything is pruned
// (project_startup_fresh.go).
//
// The picker still cannot create here: its selection would commit into the
// Window the key was pressed in. In answer mode it writes the selection to a
// file its producer made and exits, the way the hook trust prompt hands its
// decision back (hook_trust_popup.go). The selection is the same argv the split
// selection continuation carries (split_selection_continuation.go), so there is
// one encoding of "what the operator picked" and one parser for it.

// splitAnswerFileEnv hands an answer-mode split picker the file its producer
// reads the selection from. It is private producer evidence: no public CLI
// flag spells it, and only a Window producer that asks before it creates may
// state it.
const splitAnswerFileEnv = "PROJMUX_SPLIT_ANSWER_FILE"

// popupToggleAnswerFlag is the private popup-toggle flag that puts a split
// picker in answer mode. It is not a public spelling: `internal tmux` is the
// generated-artifact surface.
const popupToggleAnswerFlag = "--answer"

// splitSelectionAnswerSpelling names the answer in the messages its parser
// writes; the continuation route's own spelling would point at a route the
// answer never ran.
const splitSelectionAnswerSpelling = "launch picker answer"

// launchChoice is the answer to "what does the new Window open with", held
// between the question and the commit.
type launchChoice struct {
	// cancelled is true when the operator closed the picker without choosing.
	// Nothing is created, and nothing is said: closing a picker is not a
	// failure.
	cancelled bool
	// problem is set when the question could not be asked or its answer could
	// not be read. It is a bare reason: what that costs is the producer's to
	// say -- a Window create commits nothing (notCreatedLine), a fresh Project
	// open proceeds with its shell Pane (keptOriginShellLine).
	problem string
	// intent is the answer. An empty provider keeps the shell Pane the create
	// makes; anything else replaces that shell with an Agent.
	intent agentPaneIntent
}

// launchChooseFunc and launchApplyFunc are the two halves a Window producer
// reaches the saved launch default through. They are funcs so a unit test can
// fake the whole application without an aiCommand, and so the mode file stays
// readable in exactly one place.
type (
	launchChooseFunc func(anchorPaneID, client string) launchChoice
	launchApplyFunc  func(originPaneID, client string, choice launchChoice) launchDefaultResult
)

// chooseLaunchDefault decides the new Window's first Pane before the Window is
// committed. A provider mode is already the answer and asks nothing; `shell`
// keeps the Pane the create will make; the picker modes ask the operator on
// the Pane they pressed the key in.
func (c *aiCommand) chooseLaunchDefault(anchorPaneID, client string) launchChoice {
	anchor := exactTmuxHandle(strings.TrimSpace(anchorPaneID), "%")
	client = strings.TrimSpace(client)
	if anchor == "" || client == "" {
		return launchChoice{problem: fmt.Sprintf(
			"could not ask for the new Window's first Pane: pressed Pane %q, client %q", anchorPaneID, client)}
	}
	switch mode := c.getMode(); mode {
	case aiModeClaude, aiModeCodex, aiModeAntigravity:
		return launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: mode, placement: "right"}}
	case aiModeShell:
		return launchChoice{}
	case aiModeResume:
		return c.askLaunchPicker(anchor, client, aiResumePickerPopupMode("right"))
	default:
		// aiModeSelective and any unrecognized saved value open the picker,
		// which is what an unset mode has always meant.
		return c.askLaunchPicker(anchor, client, aiSplitPickerPopupMode("right"))
	}
}

// askLaunchPicker opens a split picker in answer mode on the exact pressing
// client, anchored on the exact Pane that client pressed the key in, and reads
// back what the operator chose. popup-toggle returns once the popup has closed,
// so the answer file is final when it is read.
func (c *aiCommand) askLaunchPicker(anchor, client, mode string) launchChoice {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return launchChoice{problem: "could not resolve the projmux binary: " + err.Error()}
	}
	// The answer file stays open for the whole question and is read back
	// through this handle, never re-opened by path: the picker truncates and
	// writes the same file, and nothing that replaces the path afterwards can
	// be read as an answer.
	answer, err := os.CreateTemp("", "projmux-launch-answer-*.json")
	if err != nil {
		return launchChoice{problem: "could not prepare the launch picker: " + err.Error()}
	}
	defer os.Remove(answer.Name())
	defer answer.Close()
	if err := answer.Chmod(0o600); err != nil {
		return launchChoice{problem: "could not prepare the launch picker: " + err.Error()}
	}
	args := []string{"internal", "tmux", "popup-toggle", "--client", client, "--anchor", anchor,
		popupToggleAnswerFlag, answer.Name(), mode}
	if err := c.run(binaryPath, args...); err != nil && !isNoSelectionExit(err) {
		return launchChoice{problem: "could not open the launch picker: " + err.Error()}
	}
	raw, err := readAnswerFromStart(answer)
	if err != nil {
		return launchChoice{problem: "could not read the launch picker answer: " + err.Error()}
	}
	if strings.TrimSpace(string(raw)) == "" {
		return launchChoice{cancelled: true}
	}
	intent, err := decodeSplitSelectionAnswer(raw)
	if err != nil {
		return launchChoice{problem: "could not read the launch picker answer: " + err.Error()}
	}
	return launchChoice{intent: intent}
}

// applyLaunchChoice fills a committed Window's shell Pane with the answer. A
// shell answer has nothing to do. Anything else goes through the same
// replacement a split uses: the Agent is committed beside the shell, then the
// shell is removed through the canonical Pane delete, and every failure keeps
// the Window with its shell.
func (c *aiCommand) applyLaunchChoice(originPaneID, client string, choice launchChoice) launchDefaultResult {
	if strings.TrimSpace(choice.intent.provider) == "" {
		return launchDefaultResult{}
	}
	origin := exactTmuxHandle(strings.TrimSpace(originPaneID), "%")
	client = strings.TrimSpace(client)
	if origin == "" || client == "" {
		return launchDefaultResult{problem: keptOriginShellLine(fmt.Sprintf(
			"projmux could not open the new Window's first Pane: origin Pane %q, client %q", originPaneID, client))}
	}
	intent := choice.intent
	if intent.producer == canonicalProducerSavedDefault {
		// The Settings gate runs exactly as the saved-default split key runs
		// it: a default that has since been switched off keeps the shell and
		// costs zero further mutations.
		if message, disabled := c.aiAgentDisabledLaunchMessage(intent.provider, aiSplitLaunchDefault); disabled {
			return launchDefaultResult{problem: keptOriginShellLine(message)}
		}
	}
	intent.anchorPaneID, intent.targetClient = origin, client
	return c.replaceOriginShellWithAgent(intent)
}

// readAnswerFromStart reads the whole answer through the handle the producer
// kept open.
func readAnswerFromStart(answer *os.File) ([]byte, error) {
	if _, err := answer.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(answer)
}

// notCreatedLine appends what did not happen to a reason a Window create's
// question could not be asked. Nothing has been created at that point, and a
// bare reason reads like a Window that is half there.
func notCreatedLine(reason string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(reason)), " ") + "; no Window was created"
}

// splitAnswerFile is the answer file an answer-mode picker writes to, or empty
// for a picker that creates. It is the popup's own knowledge, read here and
// only here.
func (c *aiCommand) splitAnswerFile() string {
	return strings.TrimSpace(c.env(splitAnswerFileEnv))
}

// writeSplitSelectionAnswer is an answer-mode picker's terminal action: it
// records the selection instead of creating anything.
func writeSplitSelectionAnswer(path string, intent agentPaneIntent) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("launch picker answer file %q is not an absolute path", path)
	}
	args := splitSelectionContinuationArgs(intent)[len(splitSelectionContinuationRoute):]
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode launch picker answer: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write launch picker answer: %w", err)
	}
	return nil
}

// decodeSplitSelectionAnswer reads an answer back through the continuation's own
// parser, so an answer is held to exactly the rules a handed-off selection is.
func decodeSplitSelectionAnswer(raw []byte) (agentPaneIntent, error) {
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		return agentPaneIntent{}, fmt.Errorf("malformed answer: %w", err)
	}
	return parseSplitSelectionArgs(splitSelectionAnswerSpelling, args, io.Discard)
}
