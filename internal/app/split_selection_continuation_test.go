package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// split_selection_continuation_test.go is the enforcement of the split picker
// popup contract: a selection closes the popup instead of holding it until the
// create commits. "Closes" is not observable without a screen, so it is fixed
// at the structural point that decides it -- the picker process calls no create
// and hands exactly one detached continuation to tmux -- and the continuation is
// held to carrying the whole intent to the one create funnel.

const (
	splitContinuationOrigin  = "%46"
	splitContinuationClient  = "/dev/pts/7"
	splitContinuationContext = "/work/repo"
)

// splitContinuationPopupEnv is the environment a split picker popup hands its
// picker process: HOME and TMUX plus the popup origin keys.
func splitContinuationPopupEnv(home string, extra map[string]string) func(string) string {
	env := map[string]string{
		"HOME":                         home,
		"TMUX":                         "/tmp/tmux-1000/projmux,7,0",
		"TMUX_SPLIT_TARGET_PANE":       splitContinuationOrigin,
		canonicalCreateTargetClientEnv: splitContinuationClient,
		"TMUX_SPLIT_CONTEXT_DIR":       splitContinuationContext,
	}
	maps.Copy(env, extra)
	return func(key string) string { return env[key] }
}

// splitContinuationDispatches returns the command of every detached `run-shell
// -b` the command issued.
func splitContinuationDispatches(commands []recordedAICommand) []string {
	var out []string
	for _, command := range commands {
		if command.name == "tmux" && len(command.args) == 3 && command.args[0] == "run-shell" && command.args[1] == "-b" {
			out = append(out, command.args[2])
		}
	}
	return out
}

// displayMessageWrites returns every `display-message` that writes to a client,
// leaving out `-p` reads, which draw nothing.
func displayMessageWrites(commands []recordedAICommand) [][]string {
	var out [][]string
	for _, command := range commands {
		if command.name == "tmux" && len(command.args) > 0 && command.args[0] == "display-message" && !slices.Contains(command.args, "-p") {
			out = append(out, command.args)
		}
	}
	return out
}

// parseSplitSelectionContinuation splits a continuation command built by
// buildShellCommand back into its env prefix and argv, and asserts the detached
// job's exit guard.
func parseSplitSelectionContinuation(t *testing.T, command string) (map[string]string, []string) {
	t.Helper()
	body, guarded := strings.CutSuffix(command, " || :")
	if !guarded {
		t.Fatalf("continuation %q does not end in the `|| :` exit guard", command)
	}
	var words []string
	var word strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(body); i++ {
		switch ch := body[i]; {
		case quoted && ch == '\'':
			quoted = false
		case quoted:
			word.WriteByte(ch)
		case ch == '\'':
			quoted, inWord = true, true
		case ch == '\\' && i+1 < len(body):
			i++
			word.WriteByte(body[i])
			inWord = true
		case ch == ' ':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(ch)
			inWord = true
		}
	}
	if quoted {
		t.Fatalf("continuation %q has an unterminated quote", command)
	}
	if inWord {
		words = append(words, word.String())
	}
	env := map[string]string{}
	for len(words) > 0 {
		key, value, ok := strings.Cut(words[0], "=")
		if !ok || strings.HasPrefix(key, "/") {
			break
		}
		env[key] = value
		words = words[1:]
	}
	if len(words) == 0 {
		t.Fatalf("continuation %q names no executable", command)
	}
	return env, words[1:]
}

// splitContinuationPopupKeys are the keys a continuation gets only from its own
// env prefix: a detached job inherits the server's environment, not the popup's.
var splitContinuationPopupKeys = []string{
	"TMUX_SPLIT_TARGET_PANE", runtimeMutationAnchorPaneEnv, canonicalCreateTargetClientEnv,
	"TMUX_SPLIT_CONTEXT_DIR",
}

// runSplitPickerThroughContinuation runs one picker action, then replays the
// continuation it dispatched the way tmux would: as a separate invocation of the
// route, whose popup keys come only from the continuation's env prefix. A
// picker that dispatched nothing (a cancelled picker) replays nothing.
func runSplitPickerThroughContinuation(t *testing.T, ai *aiCommand, pick func() error) error {
	t.Helper()
	var dispatched []string
	next := ai.runCommand
	ai.runCommand = func(ctx context.Context, name string, args ...string) error {
		if name == "tmux" && len(args) == 3 && args[0] == "run-shell" && args[1] == "-b" {
			dispatched = append(dispatched, args[2])
			return nil
		}
		return next(ctx, name, args...)
	}
	if err := pick(); err != nil {
		return err
	}
	ai.runCommand = next
	switch len(dispatched) {
	case 0:
		return nil
	case 1:
	default:
		t.Fatalf("picker dispatched %d continuations, want at most one: %q", len(dispatched), dispatched)
	}
	env, argv := parseSplitSelectionContinuation(t, dispatched[0])
	if !slices.Equal(argv[:len(splitSelectionContinuationRoute)], splitSelectionContinuationRoute) {
		t.Fatalf("continuation argv = %q, want the %q route", argv, splitSelectionContinuationRoute)
	}
	server := ai.lookupEnv
	ai.lookupEnv = func(key string) string {
		if slices.Contains(splitContinuationPopupKeys, key) {
			return env[key]
		}
		return server(key)
	}
	return ai.Run(argv[2:], io.Discard, io.Discard)
}

// TestAPopupHostedPickerSelectionHandsOffAndCreatesNothing is C-2's guarantee at
// the dispatch: every terminal action except Codex advanced launch returns
// without calling create and hands exactly one detached continuation to tmux.
func TestAPopupHostedPickerSelectionHandsOffAndCreatesNothing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(*aiCommand) error
	}{
		{name: "claude row", run: func(c *aiCommand) error {
			stubAIPickerSelection(c, aiModeClaude)
			return c.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
		}},
		{name: "codex row", run: func(c *aiCommand) error {
			stubAIPickerSelection(c, aiModeCodex)
			return c.Run([]string{"picker", "--inside", "down"}, &bytes.Buffer{}, &bytes.Buffer{})
		}},
		{name: "antigravity row", run: func(c *aiCommand) error {
			stubAIPickerSelection(c, aiModeAntigravity)
			return c.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
		}},
		{name: "shell row", run: func(c *aiCommand) error {
			stubAIPickerSelection(c, aiModeShell)
			return c.Run([]string{"picker", "--inside", "down"}, &bytes.Buffer{}, &bytes.Buffer{})
		}},
		{name: "resume conversation", run: func(c *aiCommand) error {
			return c.runSelectedResumeSession(aiResumeSelection{
				agent: aiModeClaude, resumeID: "11111111-2222-3333-4444-555555555555",
			}, "right")
		}},
		{name: "resume picker new row", run: func(c *aiCommand) error {
			c.nativePicker = nativePickerFromCompatRunner(&sequencingAIRunner{results: []intpickercompat.Result{
				{Key: "enter", Value: aiResumeNewValue},
				{Key: "enter", Value: aiModeCodex},
			}})
			return c.runResumePicker("down")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			cmd.lookupEnv = splitContinuationPopupEnv(home, nil)

			if err := test.run(cmd); err != nil {
				t.Fatalf("%s error = %v", test.name, err)
			}
			if len(creator.intents) != 0 {
				t.Fatalf("picker process called create %d times (%+v); the popup would stay up until it committed", len(creator.intents), creator.intents)
			}
			if got := splitContinuationDispatches(cmdRecorder(cmd).commands); len(got) != 1 {
				t.Fatalf("continuation dispatches = %q, want exactly one", got)
			}
		})
	}
}

// TestTheSplitSelectionContinuationCarriesTheWholeIntent is the golden of the
// hand-off: the four split picker popup modes. The picker's environment is
// derived from the popup builder itself, so this pins the chain popup env ->
// continuation end to end.
func TestTheSplitSelectionContinuationCarriesTheWholeIntent(t *testing.T) {
	t.Parallel()

	const (
		keyShape = `PROJMUX_POPUP_TARGET_CLIENT='/dev/pts/7' TMUX_SPLIT_CONTEXT_DIR='/work/repo' TMUX_SPLIT_TARGET_PANE='%46' '/tmp/projmux' 'internal' 'agent-pane' 'launch-selection' `
		provider = `'--producer' 'provider-picker' '--provider' 'claude' `
		resume   = `'--producer' 'resume-picker' '--provider' 'claude' '--conversation' '11111111-2222-3333-4444-555555555555' '--resume-source' 'claude-transcript' `
	)
	for _, test := range []struct {
		mode string
		want string
	}{
		{mode: "ai-split-picker-right", want: keyShape + provider + `'right' || :`},
		{mode: "ai-split-picker-down", want: keyShape + provider + `'down' || :`},
		{mode: "ai-split-resume-right", want: keyShape + resume + `'right' || :`},
		{mode: "ai-split-resume-down", want: keyShape + resume + `'down' || :`},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Parallel()
			// The key binding opens the picker with --client.
			args := []string{"--client", splitContinuationClient}
			mode, err := parseTmuxPopupToggleArgs(append(args, test.mode), io.Discard)
			if err != nil {
				t.Fatalf("parse popup-toggle %v: %v", args, err)
			}
			_, options, err := buildPopupToggleWithStyle(mode, "/tmp/projmux", "/tmp/marker", tmuxPopupContext{
				OriginPane: splitContinuationOrigin, OriginSession: "repo", ContextDir: splitContinuationContext,
				TargetClient: splitContinuationClient,
			}, func(string) string { return "" }, "")
			if err != nil {
				t.Fatalf("build popup %s: %v", test.mode, err)
			}

			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			env := map[string]string{"HOME": home, "TMUX": "/tmp/tmux-1000/projmux,7,0"}
			maps.Copy(env, options.Env)
			cmd.lookupEnv = func(key string) string { return env[key] }
			if strings.HasPrefix(test.mode, "ai-split-resume-") {
				err = cmd.runSelectedResumeSession(aiResumeSelection{
					agent: aiModeClaude, resumeID: "11111111-2222-3333-4444-555555555555", source: "claude-transcript",
				}, mode.Direction)
			} else {
				stubAIPickerSelection(cmd, aiModeClaude)
				err = cmd.Run([]string{"picker", "--inside", mode.Direction}, &bytes.Buffer{}, &bytes.Buffer{})
			}
			if err != nil {
				t.Fatalf("selection error = %v", err)
			}
			if len(creator.intents) != 0 {
				t.Fatalf("picker process created %+v", creator.intents)
			}
			got := splitContinuationDispatches(cmdRecorder(cmd).commands)
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("continuation =\n%q\nwant\n%q", got, test.want)
			}
		})
	}
}

// TestTheSplitSelectionContinuationReachesTheOneCreateFunnel replays the
// dispatched continuation as its own invocation and checks that it reaches the
// existing funnel once, with the intent the picker decided and the origin the
// popup named.
func TestTheSplitSelectionContinuationReachesTheOneCreateFunnel(t *testing.T) {
	t.Parallel()

	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "domain-a", EndpointGenerationID: "generation-b"}
	for _, test := range []struct {
		name string
		run  func(*aiCommand) error
		want agentPaneIntent
	}{
		{
			name: "provider row",
			run: func(c *aiCommand) error {
				stubAIPickerSelection(c, aiModeCodex)
				return c.runAgentPickerSelection("down")
			},
			want: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "down"},
		},
		{
			name: "shell row",
			run: func(c *aiCommand) error {
				stubAIPickerSelection(c, aiModeShell)
				return c.runAgentPickerSelection("right")
			},
			want: agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"},
		},
		{
			name: "native Codex resume row",
			run: func(c *aiCommand) error {
				return c.runSelectedResumeSession(aiResumeSelection{
					agent: aiModeCodex, resumeID: "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee", source: aisessions.SourceCodexAppServer,
					endpoint: endpoint, state: coremetadata.CodexGenerationCurrent,
				}, "right")
			},
			want: agentPaneIntent{
				producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
				conversationID: "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee", resumeSource: aisessions.SourceCodexAppServer,
				resumeEndpoint: endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			cmd.lookupEnv = splitContinuationPopupEnv(home, nil)

			if err := runSplitPickerThroughContinuation(t, cmd, func() error { return test.run(cmd) }); err != nil {
				t.Fatalf("continuation error = %v", err)
			}
			want := test.want
			want.anchorPaneID, want.targetClient = splitContinuationOrigin, splitContinuationClient
			if !reflect.DeepEqual(creator.intents, []agentPaneIntent{want}) {
				t.Fatalf("intents = %+v, want %+v", creator.intents, []agentPaneIntent{want})
			}
		})
	}
}

// splitNoticePaneCreator commits a Pane and writes a split start notice, the
// one line a successful split still owes its client.
type splitNoticePaneCreator struct{ notice string }

func (c splitNoticePaneCreator) createFromIntent(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
	_, _ = io.WriteString(stderr, c.notice)
	return createdPaneRuntime{}, nil
}

// TestTheSplitSelectionContinuationSpeaksOnlyWhenTheSplitDidNotSimplySucceed is
// the report half: a success writes nothing to any client, and a refusal or a
// split start notice is exactly one bounded line on the pressing client.
func TestTheSplitSelectionContinuationSpeaksOnlyWhenTheSplitDidNotSimplySucceed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		creator   canonicalPaneCreator
		wantLines int
		wantText  string
	}{
		{name: "success", creator: &recordingPaneCreator{}, wantLines: 0},
		{name: "create refusal", creator: &recordingPaneCreator{err: errors.New("origin Pane left the managed Window")}, wantLines: 1,
			wantText: "projmux create failed: origin Pane left the managed Window"},
		{name: "split start notice", creator: splitNoticePaneCreator{notice: "requested Pane directory /gone was not used"}, wantLines: 1,
			wantText: "projmux: requested Pane directory /gone was not used"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.panes = test.creator
			cmd.lookupEnv = splitContinuationPopupEnv(home, nil)
			stubAIPickerSelection(cmd, aiModeClaude)

			if err := runSplitPickerThroughContinuation(t, cmd, func() error { return cmd.runAgentPickerSelection("right") }); err != nil {
				t.Fatalf("continuation error = %v", err)
			}
			lines := displayMessageWrites(cmdRecorder(cmd).commands)
			if len(lines) != test.wantLines {
				t.Fatalf("display-message writes = %q, want %d", lines, test.wantLines)
			}
			if test.wantLines == 0 {
				return
			}
			line := lines[0]
			if i := slices.Index(line, "-c"); i < 0 || i+1 >= len(line) || line[i+1] != splitContinuationClient {
				t.Fatalf("line %q is not on the pressing client %q", line, splitContinuationClient)
			}
			if !strings.Contains(line[len(line)-1], test.wantText) {
				t.Fatalf("line = %q, want it to carry %q", line[len(line)-1], test.wantText)
			}
		})
	}
}

// TestAContinuationTheRouteCannotRunConvergesOnThePressingClient covers the
// continuation failing before it reaches create: the app guard turns it into
// one bounded line on the exact client and a zero exit, so the detached job has
// nothing for tmux to paint.
func TestAContinuationTheRouteCannotRunConvergesOnThePressingClient(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai, creator := intentAICommand(t, home)
	ai.lookupEnv = splitContinuationPopupEnv(home, nil)
	runner := &recordingTmuxRunner{}
	app := &App{
		internal:          &internalCommand{ai: ai},
		interactiveRunner: runner,
		lookupEnv:         splitContinuationPopupEnv(home, nil),
	}

	var stdout, stderr bytes.Buffer
	err := app.Run([]string{"internal", "agent-pane", "launch-selection", "--producer", "saved-default", "--provider", "claude", "right"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("a refused continuation escaped as an exit status: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("refused continuation wrote stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(creator.intents) != 0 {
		t.Fatalf("a refused continuation created %+v", creator.intents)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("guard calls = %#v, want one client line", runner.calls)
	}
	args := runner.calls[0].args
	if len(args) < 3 || args[0] != "display-message" || args[1] != "-c" || args[2] != splitContinuationClient {
		t.Fatalf("guard call = %q, want a display-message on %q", args, splitContinuationClient)
	}
	if line := args[len(args)-1]; !strings.HasPrefix(line, "projmux create Pane failed:") || len([]rune(line)) > interactiveRunShellMessageLimit {
		t.Fatalf("guard line = %q, want one bounded create Pane failure", line)
	}
}

// TestASplitPickerWithNoPopupOriginStillCreatesInProcess keeps the one runtime
// with no popup to close unchanged: a picker run in the Pane it acts on has no
// origin env, so it creates directly and hands nothing off.
func TestASplitPickerWithNoPopupOriginStillCreatesInProcess(t *testing.T) {
	t.Parallel()

	cmd, creator := intentAICommand(t, t.TempDir())
	stubAIPickerSelection(cmd, aiModeClaude)
	if err := cmd.runAgentPickerSelection("right"); err != nil {
		t.Fatal(err)
	}
	if len(creator.intents) != 1 {
		t.Fatalf("intents = %+v, want one in-process create", creator.intents)
	}
	if got := splitContinuationDispatches(cmdRecorder(cmd).commands); len(got) != 0 {
		t.Fatalf("a picker with no popup dispatched %q", got)
	}
}
