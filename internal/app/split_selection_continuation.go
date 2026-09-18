package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The split picker's selection continuation.
//
// tmux closes a popup when the process inside it exits, and nothing else closes
// it on that process's behalf. The split pickers used to run the canonical
// create in that same process, so after Enter the picker had already cleared its
// screen but the popup stayed up as a blank frame until the create committed.
// The operator's choice was made; what they were waiting for was a Registry
// transaction and a provider start.
//
// A selection is therefore handed to a detached `run-shell -b` job on the same
// tmux server and the picker returns, which closes the popup. The job runs the
// hidden `internal agent-pane launch-selection` route, which rebuilds the intent
// and calls createPaneFromIntent -- the one funnel every split UI producer uses.
// There is no second create path here: this file moves where the funnel is
// called from, not what it does. The committed-result seam and the refusal line
// inside that funnel are unchanged, so a successful selection still writes
// nothing and anything else reaches the pressing client as one bounded line.
//
// Every terminal action travels: a picker selection is a plain value -- a
// provider, and for a resume the conversation it names -- with no live handle
// left behind in the picker process.

// splitSelectionContinuationRoute is the argv prefix of the continuation route.
var splitSelectionContinuationRoute = []string{"internal", "agent-pane", "launch-selection"}

// The flags the picker writes and the continuation route reads. Each one is a
// field of agentPaneIntent the picker decided; the origin Pane, client, context
// directory, and replace marker travel as the same env the popup handed the
// picker.
const (
	splitSelectionProducerFlag           = "producer"
	splitSelectionProviderFlag           = "provider"
	splitSelectionConversationFlag       = "conversation"
	splitSelectionResumeSourceFlag       = "resume-source"
	splitSelectionResumeStateDomainFlag  = "resume-state-domain"
	splitSelectionResumeGenerationFlag   = "resume-endpoint-generation"
	splitSelectionResumeGenerationStFlag = "resume-generation-state"
)

// launchPickerSelection is where a split picker's terminal action leaves the
// picker. An answer-mode picker records the intent for the Window producer that
// asked and creates nothing (window_create_launch_choice.go). A picker hosted in
// a popup hands the intent to the detached continuation so the popup closes now;
// a picker with no popup origin -- run in the Pane it acts on -- has no popup to
// close and keeps the in-process create.
func (c *aiCommand) launchPickerSelection(intent agentPaneIntent) error {
	if answer := c.splitAnswerFile(); answer != "" {
		return writeSplitSelectionAnswer(answer, intent)
	}
	origin := c.splitOriginPane()
	if origin == "" {
		return c.createPaneFromIntent(intent)
	}
	return c.launchSplitSelectionContinuation(intent, origin)
}

// launchSplitSelectionContinuation starts the continuation on the inherited tmux
// server. Its shape is the sidebar open continuation's
// ((*switchCommand).launchSidebarOpenContinuation): an exact origin %N is
// required, the origin travels as env, and the job ends in `|| :` so a failed
// continuation is never painted as its raw shell command.
func (c *aiCommand) launchSplitSelectionContinuation(intent agentPaneIntent, origin string) error {
	binaryPath, err := c.binaryPath()
	if err != nil {
		return fmt.Errorf("resolve split selection continuation executable: %w", err)
	}
	origin = exactTmuxHandle(origin, "%")
	if origin == "" {
		return errors.New("launch split selection continuation: exact popup origin %N is required")
	}
	command := buildShellCommand(binaryPath, splitSelectionContinuationArgs(intent), c.splitSelectionContinuationEnv(origin))
	// The continuation reports through the create funnel's own client line, and
	// the app-level guard converges anything else onto that client. Keep the
	// detached job successful so tmux never replaces that with its command.
	command += " || :"
	if err := c.run("tmux", "run-shell", "-b", command); err != nil {
		return fmt.Errorf("launch split selection continuation: %w", err)
	}
	return nil
}

// splitSelectionContinuationArgs renders the picker's half of the intent -- the
// fields the operator chose -- as the argv the continuation route parses.
func splitSelectionContinuationArgs(intent agentPaneIntent) []string {
	args := append([]string{}, splitSelectionContinuationRoute...)
	args = append(args, "--"+splitSelectionProducerFlag, string(intent.producer))
	if provider := strings.TrimSpace(intent.provider); provider != "" {
		args = append(args, "--"+splitSelectionProviderFlag, provider)
	}
	for _, field := range []struct{ flag, value string }{
		{splitSelectionConversationFlag, intent.conversationID},
		{splitSelectionResumeSourceFlag, intent.resumeSource},
		{splitSelectionResumeStateDomainFlag, intent.resumeEndpoint.StateDomainID},
		{splitSelectionResumeGenerationFlag, intent.resumeEndpoint.EndpointGenerationID},
		{splitSelectionResumeGenerationStFlag, string(intent.resumeGenerationState)},
	} {
		if value := strings.TrimSpace(field.value); value != "" {
			args = append(args, "--"+field.flag, value)
		}
	}
	return append(args, intent.placement)
}

// splitSelectionContinuationEnv hands the continuation the popup origin exactly
// as the picker received it -- the origin Pane is written here and read back
// only by splitOriginPane -- so createPaneFromIntent reads the same origin Pane,
// client, context directory, and replace marker it would have read here.
func (c *aiCommand) splitSelectionContinuationEnv(origin string) map[string]string {
	env := map[string]string{"TMUX_SPLIT_TARGET_PANE": origin}
	for _, key := range []string{
		runtimeMutationAnchorPaneEnv,
		canonicalCreateTargetClientEnv,
		"TMUX_SPLIT_CONTEXT_DIR",
	} {
		if value := strings.TrimSpace(c.env(key)); value != "" {
			env[key] = value
		}
	}
	if c.splitReplacesOrigin() {
		env[splitReplaceOriginEnv] = "1"
	}
	return env
}

// runLaunchSelection is the continuation route. It accepts only the two picker
// producers: it is the far half of a picker selection, not a general create
// entry, and it requires the exact origin Pane the picker named.
func (c *aiCommand) runLaunchSelection(args []string, stderr io.Writer) error {
	const spelling = "internal agent-pane launch-selection"
	intent, err := parseSplitSelectionArgs(spelling, args, stderr)
	if err != nil {
		return err
	}
	if exactTmuxHandle(c.splitOriginPane(), "%") == "" {
		return usageError(spelling + " requires the exact popup origin %N its picker handed it")
	}
	return c.createPaneFromIntent(intent)
}

// parseSplitSelectionArgs rebuilds a picker selection from the argv
// splitSelectionContinuationArgs rendered. It is the one reader of that
// encoding: the continuation route and a Window producer's launch picker answer
// both go through it, so a selection is held to the same rules wherever it
// travels.
func parseSplitSelectionArgs(spelling string, args []string, stderr io.Writer) (agentPaneIntent, error) {
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	producer := fs.String(splitSelectionProducerFlag, "", "split picker that made the selection")
	provider := fs.String(splitSelectionProviderFlag, "", "selected provider; empty for a shell Pane")
	conversation := fs.String(splitSelectionConversationFlag, "", "selected resume conversation")
	resumeSource := fs.String(splitSelectionResumeSourceFlag, "", "exact resume picker source")
	stateDomain := fs.String(splitSelectionResumeStateDomainFlag, "", "resume Codex endpoint state domain")
	generation := fs.String(splitSelectionResumeGenerationFlag, "", "resume Codex endpoint generation")
	generationState := fs.String(splitSelectionResumeGenerationStFlag, "", "resume Codex endpoint generation state")
	if err := fs.Parse(args); err != nil {
		return agentPaneIntent{}, err
	}
	if fs.NArg() != 1 {
		printAIUsage(stderr)
		return agentPaneIntent{}, usageError(spelling + " requires exactly 1 <right|down> argument")
	}
	direction, err := parseAISplitDirection(fs.Args(), spelling, stderr)
	if err != nil {
		return agentPaneIntent{}, err
	}
	intent := agentPaneIntent{
		producer:       canonicalCreateProducer(strings.TrimSpace(*producer)),
		placement:      direction,
		conversationID: strings.TrimSpace(*conversation),
		resumeSource:   strings.TrimSpace(*resumeSource),
		resumeEndpoint: coremetadata.CodexEndpointRef{
			StateDomainID: strings.TrimSpace(*stateDomain), EndpointGenerationID: strings.TrimSpace(*generation),
		},
		resumeGenerationState: coremetadata.CodexGenerationState(strings.TrimSpace(*generationState)),
	}
	switch intent.producer {
	case canonicalProducerProviderPicker, canonicalProducerResumePicker:
	default:
		return agentPaneIntent{}, usageError(fmt.Sprintf("%s: --%s must be %s or %s, got %q", spelling, splitSelectionProducerFlag,
			canonicalProducerProviderPicker, canonicalProducerResumePicker, intent.producer))
	}
	if raw := strings.TrimSpace(*provider); raw != "" {
		if intent.provider, err = requireCanonicalProvider(spelling, raw); err != nil {
			return agentPaneIntent{}, err
		}
	}
	if intent.producer == canonicalProducerResumePicker && (intent.provider == "" || intent.conversationID == "") {
		return agentPaneIntent{}, usageError(fmt.Sprintf("%s: a resume picker selection requires --%s and --%s", spelling,
			splitSelectionProviderFlag, splitSelectionConversationFlag))
	}
	if intent.producer == canonicalProducerProviderPicker && (intent.conversationID != "" || intent.resumeSource != "" ||
		intent.resumeEndpoint != (coremetadata.CodexEndpointRef{}) || intent.resumeGenerationState != "") {
		return agentPaneIntent{}, usageError(fmt.Sprintf("%s: a provider picker selection carries no resume conversation", spelling))
	}
	return intent, nil
}
