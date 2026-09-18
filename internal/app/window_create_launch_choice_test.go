package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// window_create_launch_choice_test.go covers the order of a generated Window
// create: ask on the Pane the key was pressed in, commit the Window, fill its
// shell Pane with the answer, and only then move the pressing client onto it.

const launchChoicePressedPane = "%9"

// answeringLaunchAICommand is the AI command a Window producer asks through,
// with the popup faked: the popup-toggle call it runs writes answer to the
// answer file the producer named, exactly as an answer-mode picker would, and
// popupErr stands in for a popup that could not be opened.
func answeringLaunchAICommand(t *testing.T, mode string, answer []byte, popupErr error) (*aiCommand, *replaceRecorder, *[][]string) {
	t.Helper()
	home := t.TempDir()
	cmd, recorder := launchDefaultAICommand(t, home)
	if mode != "" {
		if err := cmd.setMode(mode); err != nil {
			t.Fatalf("setMode(%s) error = %v", mode, err)
		}
	}
	popups := &[][]string{}
	cmd.runCommand = func(_ context.Context, name string, args ...string) error {
		if name != "/tmp/projmux" || !slices.Contains(args, "popup-toggle") {
			return nil
		}
		*popups = append(*popups, append([]string(nil), args...))
		if popupErr != nil {
			return popupErr
		}
		i := slices.Index(args, popupToggleAnswerFlag)
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("popup-toggle %v carries no answer file", args)
		}
		if answer != nil {
			if err := os.WriteFile(args[i+1], answer, 0o600); err != nil {
				t.Fatalf("write answer: %v", err)
			}
		}
		return nil
	}
	return cmd, recorder, popups
}

// encodedAnswer is what an answer-mode picker writes for intent.
func encodedAnswer(t *testing.T, intent agentPaneIntent) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "answer.json")
	if err := writeSplitSelectionAnswer(path, intent); err != nil {
		t.Fatalf("writeSplitSelectionAnswer() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestChooseLaunchDefaultAsksOnlyThePickerModesOnThePressedPane is the question
// half of the saved default, one row per saved mode. A provider mode and
// `shell` are already the answer and open nothing; the picker modes open the
// picker in answer mode on the exact pressing client, anchored on the exact
// Pane that client pressed the key in -- and reach no create route at all.
func TestChooseLaunchDefaultAsksOnlyThePickerModesOnThePressedPane(t *testing.T) {
	claudeAnswer := agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}
	for _, tt := range []struct {
		name       string
		mode       string
		want       launchChoice
		wantPopup  string
		pickerRows bool
	}{
		{name: "shell", mode: aiModeShell},
		{name: "claude", mode: aiModeClaude,
			want: launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right"}}},
		{name: "codex", mode: aiModeCodex,
			want: launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeCodex, placement: "right"}}},
		{name: "antigravity", mode: aiModeAntigravity,
			want: launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeAntigravity, placement: "right"}}},
		{name: "selective", mode: aiModeSelective, wantPopup: "ai-split-picker-right", want: launchChoice{intent: claudeAnswer}},
		{name: "resume", mode: aiModeResume, wantPopup: "ai-split-resume-right", want: launchChoice{intent: claudeAnswer}},
		{name: "unset", wantPopup: "ai-split-picker-right", want: launchChoice{intent: claudeAnswer}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, recorder, popups := answeringLaunchAICommand(t, tt.mode, encodedAnswer(t, claudeAnswer), nil)

			got := cmd.chooseLaunchDefault(launchChoicePressedPane, launchDefaultClient)

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("choice = %+v, want %+v", got, tt.want)
			}
			if len(recorder.intents) != 0 || len(recorder.deleted) != 0 {
				t.Fatalf("asking reached the canonical routes: intents=%+v deleted=%v", recorder.intents, recorder.deleted)
			}
			if tt.wantPopup == "" {
				if len(*popups) != 0 {
					t.Fatalf("popups = %v, want none", *popups)
				}
				return
			}
			if len(*popups) != 1 {
				t.Fatalf("popups = %v, want exactly one", *popups)
			}
			popup := (*popups)[0]
			i := slices.Index(popup, popupToggleAnswerFlag)
			want := []string{"internal", "tmux", "popup-toggle", "--client", launchDefaultClient,
				"--anchor", launchChoicePressedPane, popupToggleAnswerFlag, popup[i+1], tt.wantPopup}
			if !slices.Equal(popup, want) {
				t.Fatalf("popup = %v, want %v", popup, want)
			}
			if _, err := os.Stat(popup[i+1]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("answer file %s left behind (stat err %v)", popup[i+1], err)
			}
		})
	}
}

// TestChooseLaunchDefaultReadsEveryPickerAnswer covers what the popup can leave
// behind: every terminal row's selection, including a resume that names its
// conversation; an empty answer, which is a cancel; and a popup that could not
// be opened or an answer that cannot be read, which say so and create nothing.
func TestChooseLaunchDefaultReadsEveryPickerAnswer(t *testing.T) {
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "domain-a", EndpointGenerationID: "generation-b"}
	resume := agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right", conversationID: "conv-1",
		resumeSource: "codex-native", resumeEndpoint: endpoint, resumeGenerationState: coremetadata.CodexGenerationState("ready"),
	}
	for _, tt := range []struct {
		name        string
		answer      []byte
		popupErr    error
		want        launchChoice
		wantProblem string
	}{
		{name: "provider row", answer: encodedAnswer(t, agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"}),
			want: launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"}}},
		{name: "shell row", answer: encodedAnswer(t, agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"}),
			want: launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"}}},
		{name: "resume row", answer: encodedAnswer(t, resume), want: launchChoice{intent: resume}},
		{name: "cancelled picker", want: launchChoice{cancelled: true}},
		{name: "popup could not open", popupErr: errors.New("injected popup failure"),
			wantProblem: "could not open the launch picker: injected popup failure"},
		{name: "malformed answer", answer: []byte("not json"), wantProblem: "could not read the launch picker answer"},
		{name: "answer with no producer", answer: []byte(`["--provider","claude","right"]`), wantProblem: "could not read the launch picker answer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, recorder, _ := answeringLaunchAICommand(t, aiModeSelective, tt.answer, tt.popupErr)

			got := cmd.chooseLaunchDefault(launchChoicePressedPane, launchDefaultClient)

			if tt.wantProblem != "" {
				// The reason is bare: what it costs is the producer's to say
				// (a Window create adds notCreatedLine, a fresh open
				// keptOriginShellLine).
				if !strings.Contains(got.problem, tt.wantProblem) || strings.Contains(got.problem, "no Window was created") {
					t.Fatalf("problem = %q, want the bare reason %q", got.problem, tt.wantProblem)
				}
				if got.cancelled || got.intent != (agentPaneIntent{}) {
					t.Fatalf("choice = %+v, want only the problem", got)
				}
			} else if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("choice = %+v, want %+v", got, tt.want)
			}
			if len(recorder.intents) != 0 {
				t.Fatalf("asking created %+v", recorder.intents)
			}
		})
	}
}

// TestApplyLaunchChoiceReplacesTheShellOnlyForAnAgent is the fill half: the
// answer reaches the committed shell Pane of the new Window, a shell answer
// leaves that Pane alone, and an Agent answer is committed beside the shell
// before the shell is deleted.
func TestApplyLaunchChoiceReplacesTheShellOnlyForAnAgent(t *testing.T) {
	for _, tt := range []struct {
		name       string
		choice     launchChoice
		wantIntent *agentPaneIntent
	}{
		{name: "shell answer"},
		{name: "shell row", choice: launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"}}},
		{
			name:   "saved provider default",
			choice: launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right"}},
			wantIntent: &agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right",
				anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient},
		},
		{
			name: "resume row",
			choice: launchChoice{intent: agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeCodex,
				placement: "right", conversationID: "conv-1"}},
			wantIntent: &agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
				conversationID: "conv-1", anchorPaneID: launchDefaultOriginPane, targetClient: launchDefaultClient},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, recorder := launchDefaultAICommand(t, t.TempDir())

			result := cmd.applyLaunchChoice(launchDefaultOriginPane, launchDefaultClient, tt.choice)

			if result.problem != "" {
				t.Fatalf("problem = %q, want none", result.problem)
			}
			if tt.wantIntent == nil {
				if len(recorder.events) != 0 {
					t.Fatalf("a shell answer reached the routes: %v", recorder.events)
				}
				return
			}
			if !reflect.DeepEqual(recorder.intents, []agentPaneIntent{*tt.wantIntent}) {
				t.Fatalf("create intents = %+v, want %+v", recorder.intents, *tt.wantIntent)
			}
			if !slices.Equal(recorder.deleted, []string{launchDefaultOriginPane}) {
				t.Fatalf("deleted Panes = %v, want exactly the new shell", recorder.deleted)
			}
			if !slices.Equal(recorder.events, []string{"create", "delete"}) {
				t.Fatalf("route order = %v, want the Agent committed before the shell is deleted", recorder.events)
			}
		})
	}
}

// TestApplyLaunchChoiceFailuresKeepTheWindowAndSayOneThing is the negative half
// of the fill: nothing is rolled back, the Window keeps its shell, and the
// producer gets one line.
func TestApplyLaunchChoiceFailuresKeepTheWindowAndSayOneThing(t *testing.T) {
	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right"}}
	t.Run("saved provider disabled in Settings", func(t *testing.T) {
		home := t.TempDir()
		enableAgents(t, home, config.AIAgentCodex)
		cmd, recorder := launchDefaultAICommand(t, home)

		result := cmd.applyLaunchChoice(launchDefaultOriginPane, launchDefaultClient, claude)

		for _, want := range []string{"AI split default claude is disabled", "keeps its shell Pane"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
		if len(recorder.events) != 0 {
			t.Fatalf("a disabled provider reached the routes: %v", recorder.events)
		}
	})
	t.Run("Agent create refused", func(t *testing.T) {
		cmd, recorder := launchDefaultAICommand(t, t.TempDir())
		recorder.createErr = errors.New("injected canonical create refusal")

		result := cmd.applyLaunchChoice(launchDefaultOriginPane, launchDefaultClient, claude)

		for _, want := range []string{"injected canonical create refusal", "keeps its shell Pane"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
		if len(recorder.deleted) != 0 {
			t.Fatalf("a refused create still deleted %v", recorder.deleted)
		}
	})
	t.Run("shell delete refused", func(t *testing.T) {
		cmd, recorder := launchDefaultAICommand(t, t.TempDir())
		recorder.deleteErr = errors.New("injected canonical delete refusal")

		result := cmd.applyLaunchChoice(launchDefaultOriginPane, launchDefaultClient, claude)

		for _, want := range []string{launchDefaultOriginPane, "injected canonical delete refusal", "both Panes stay"} {
			if !strings.Contains(result.problem, want) {
				t.Fatalf("problem = %q, want substring %q", result.problem, want)
			}
		}
	})
}

// orderedWindowCreateRoute is a generated Window create whose seams all log
// into one ordered list, so the test can read the order the route runs them in.
// The tmux runner records the client move (switch-client and select-window)
// into the same log.
type orderedWindowCreateRoute struct {
	cmd     *tmuxCommand
	runner  *recordingTmuxRunner
	events  []string
	creates int
	applied []launchChoice
	origins [][2]string
}

func newOrderedWindowCreateRoute(t *testing.T, choice launchChoice, applied launchDefaultResult, attached bool) *orderedWindowCreateRoute {
	t.Helper()
	clients := "\n"
	if attached {
		clients = launchDefaultClient + focusFieldSeparator + "alpha\n"
	}
	route := &orderedWindowCreateRoute{runner: &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "list-clients", "-F", "#{client_name}"+focusFieldSeparator+"#{client_session}"): clients,
	}}}
	route.cmd = &tmuxCommand{
		runner: route.runner,
		launchChoose: func(anchorPaneID, client string) launchChoice {
			route.events = append(route.events, "ask "+anchorPaneID+" "+client)
			return choice
		},
		windowCreate: func(_ windowCreateIntent, _, _ io.Writer) (createdWindowRuntime, error) {
			route.creates++
			route.events = append(route.events, "commit")
			return createdWindowRuntime{sessionID: "$1", windowID: "@5", paneID: launchDefaultOriginPane}, nil
		},
		launchApply: func(originPaneID, client string, got launchChoice) launchDefaultResult {
			route.events = append(route.events, "fill")
			route.applied = append(route.applied, got)
			route.origins = append(route.origins, [2]string{originPaneID, client})
			for _, call := range route.runner.calls {
				if len(call.args) > 0 && (call.args[0] == "switch-client" || call.args[0] == "select-window") {
					t.Fatalf("the client was moved (%v) before the new Window was filled", call.args)
				}
			}
			return applied
		},
	}
	return route
}

func (r *orderedWindowCreateRoute) run(t *testing.T) {
	t.Helper()
	if err := r.cmd.Run([]string{"window-create", "--client", launchDefaultClient, "--anchor", launchChoicePressedPane},
		ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("window-create route: %v", err)
	}
}

func (r *orderedWindowCreateRoute) moved() bool {
	for _, call := range r.runner.calls {
		if len(call.args) > 0 && call.args[0] == "switch-client" {
			return true
		}
	}
	return false
}

func (r *orderedWindowCreateRoute) lines() []string {
	var lines []string
	for _, call := range r.runner.calls {
		if len(call.args) > 0 && call.args[0] == "display-message" {
			lines = append(lines, call.args[len(call.args)-1])
		}
	}
	return lines
}

// TestWindowCreateIntentAsksBeforeItCreatesAndFillsBeforeItShows is the
// condition table of the generated route's order. Every row asks first, on the
// pressed Pane; a cancelled or unaskable question commits nothing; an answer is
// committed, then filled into the new Window's shell Pane, and only then is the
// pressing client moved -- with the one line the fill left.
func TestWindowCreateIntentAsksBeforeItCreatesAndFillsBeforeItShows(t *testing.T) {
	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}}
	ask := "ask " + launchChoicePressedPane + " " + launchDefaultClient
	for _, tt := range []struct {
		name       string
		choice     launchChoice
		applied    launchDefaultResult
		wantEvents []string
		wantMoved  bool
		wantLines  []string
	}{
		{name: "cancelled picker creates nothing and says nothing", choice: launchChoice{cancelled: true},
			wantEvents: []string{ask}},
		{name: "unaskable question creates nothing and says why",
			choice:     launchChoice{problem: "could not open the launch picker: injected"},
			wantEvents: []string{ask},
			wantLines:  []string{"projmux Create Window failed: could not open the launch picker: injected; no Window was created"}},
		{name: "shell answer", wantEvents: []string{ask, "commit", "fill"}, wantMoved: true,
			wantLines: []string{windowCreatedMessage}},
		{name: "Agent answer", choice: claude, wantEvents: []string{ask, "commit", "fill"}, wantMoved: true,
			wantLines: []string{windowCreatedMessage}},
		{name: "notice rides on the created line", choice: claude, applied: launchDefaultResult{notice: "started in /srv/alpha"},
			wantEvents: []string{ask, "commit", "fill"}, wantMoved: true,
			wantLines: []string{windowCreatedMessage + ": started in /srv/alpha"}},
		{name: "a failed fill keeps the Window and replaces the line", choice: claude,
			applied:    launchDefaultResult{problem: "projmux create failed: injected; the Window keeps its shell Pane"},
			wantEvents: []string{ask, "commit", "fill"}, wantMoved: true,
			wantLines: []string{"projmux create failed: injected; the Window keeps its shell Pane"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := newOrderedWindowCreateRoute(t, tt.choice, tt.applied, true)

			route.run(t)

			if !slices.Equal(route.events, tt.wantEvents) {
				t.Fatalf("route order = %v, want %v", route.events, tt.wantEvents)
			}
			if slices.Contains(tt.wantEvents, "fill") {
				if want := [][2]string{{launchDefaultOriginPane, launchDefaultClient}}; !reflect.DeepEqual(route.origins, want) {
					t.Fatalf("filled %v, want the committed shell Pane and the pressing client %v", route.origins, want)
				}
				if !reflect.DeepEqual(route.applied, []launchChoice{tt.choice}) {
					t.Fatalf("filled with %+v, want the answer %+v", route.applied, tt.choice)
				}
			}
			if got := route.moved(); got != tt.wantMoved {
				t.Fatalf("client moved = %v, want %v", got, tt.wantMoved)
			}
			if got := route.lines(); !slices.Equal(got, tt.wantLines) {
				t.Fatalf("client lines = %q, want %q", got, tt.wantLines)
			}
		})
	}
}

// TestWindowCreateIntentWithAnUnmovedClientStillFillsTheWindow pins the
// decision for a create whose client could not be moved. The operator already
// answered before the commit, so the answer is still filled in; the one line is
// the unshown-create line, carrying what the fill could not do if anything.
func TestWindowCreateIntentWithAnUnmovedClientStillFillsTheWindow(t *testing.T) {
	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}}
	for _, tt := range []struct {
		name     string
		applied  launchDefaultResult
		wantTail string
	}{
		{name: "filled"},
		{name: "fill failed", applied: launchDefaultResult{problem: "injected fill failure"}, wantTail: "; injected fill failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := newOrderedWindowCreateRoute(t, claude, tt.applied, false)

			route.run(t)

			if len(route.applied) != 1 {
				t.Fatalf("fills = %d, want the answer filled once", len(route.applied))
			}
			lines := route.lines()
			if len(lines) != 1 || !strings.HasPrefix(lines[0], windowCreatedUnshownMessage) || !strings.HasSuffix(lines[0], tt.wantTail) {
				t.Fatalf("client lines = %q, want one unshown-create line ending %q", lines, tt.wantTail)
			}
		})
	}
}

// answeringPickerAICommand is the split UI running inside a popup a Window
// producer opened in answer mode: the pressed Pane and the exact client arrive
// as env, as they always do, plus the answer file.
func answeringPickerAICommand(t *testing.T) (*aiCommand, *replaceRecorder, string) {
	t.Helper()
	home := t.TempDir()
	cmd, recorder := launchDefaultAICommand(t, home)
	answer := filepath.Join(t.TempDir(), "answer.json")
	if err := os.WriteFile(answer, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HOME":                         home,
		"TMUX":                         "/tmp/tmux-1000/projmux,7,0",
		"TMUX_SPLIT_TARGET_PANE":       launchChoicePressedPane,
		canonicalCreateTargetClientEnv: launchDefaultClient,
		splitAnswerFileEnv:             answer,
	}
	cmd.lookupEnv = func(key string) string { return env[key] }
	return cmd, recorder, answer
}

// TestAnswerModePickerRecordsTheSelectionAndCreatesNothing is the picker's half
// of the question. Every terminal row writes its selection to the answer file
// -- nothing is committed into the Window the key was pressed in, and nothing is
// handed to the create continuation -- and a cancelled picker writes nothing.
func TestAnswerModePickerRecordsTheSelectionAndCreatesNothing(t *testing.T) {
	for _, tt := range []struct {
		name string
		pick func(*aiCommand) error
		want *agentPaneIntent
	}{
		{
			name: "provider row",
			pick: func(cmd *aiCommand) error {
				stubAIPickerSelection(cmd, aiModeClaude)
				return cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
			},
			want: &agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"},
		},
		{
			name: "shell row",
			pick: func(cmd *aiCommand) error {
				stubAIPickerSelection(cmd, aiModeShell)
				return cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
			},
			want: &agentPaneIntent{producer: canonicalProducerProviderPicker, placement: "right"},
		},
		{
			name: "resume row",
			pick: func(cmd *aiCommand) error {
				return cmd.runSelectedResumeSession(aiResumeSelection{agent: aiModeClaude, resumeID: "11111111-2222-3333-4444-555555555555"}, "right")
			},
			want: &agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
				conversationID: "11111111-2222-3333-4444-555555555555"},
		},
		{
			name: "cancelled picker",
			pick: func(cmd *aiCommand) error {
				stubAIPickerSelection(cmd, "")
				return cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, recorder, answer := answeringPickerAICommand(t)

			if err := tt.pick(cmd); err != nil {
				t.Fatalf("picker error = %v", err)
			}

			if len(recorder.events) != 0 {
				t.Fatalf("an answer-mode picker reached the canonical routes: %v", recorder.events)
			}
			if got := splitContinuationDispatches(cmdRecorder(cmd).commands); len(got) != 0 {
				t.Fatalf("an answer-mode picker handed a create to the continuation: %q", got)
			}
			raw, err := os.ReadFile(answer)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if len(raw) != 0 {
					t.Fatalf("a cancelled picker wrote %q", raw)
				}
				return
			}
			got, err := decodeSplitSelectionAnswer(raw)
			if err != nil {
				t.Fatalf("decode answer %q: %v", raw, err)
			}
			if !reflect.DeepEqual(got, *tt.want) {
				t.Fatalf("answer = %+v, want %+v", got, *tt.want)
			}
		})
	}
}

// TestAnswerModePickerNeverCreatesThroughTheFunnel pins the guard at the one
// create funnel: whatever route reaches it inside an answer-mode popup, the
// selection is recorded and nothing is committed.
func TestAnswerModePickerNeverCreatesThroughTheFunnel(t *testing.T) {
	cmd, recorder, answer := answeringPickerAICommand(t)

	if err := cmd.createPaneFromIntent(agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"}); err != nil {
		t.Fatalf("createPaneFromIntent() error = %v", err)
	}

	if len(recorder.events) != 0 {
		t.Fatalf("the funnel created in answer mode: %v", recorder.events)
	}
	raw, err := os.ReadFile(answer)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeSplitSelectionAnswer(raw); err != nil || got.provider != aiModeCodex {
		t.Fatalf("answer = %+v (err %v), want the Codex selection", got, err)
	}
}

// TestAnswerModePopupToggleCarriesTheAnswerFileAndTheExactClient is the
// transport half: popup-toggle hands the answer file to the picker beside the
// origin it always carried, and pins the popup to the exact pressing client.
func TestAnswerModePopupToggleCarriesTheAnswerFileAndTheExactClient(t *testing.T) {
	popupContext := tmuxPopupContext{
		OriginPane: launchChoicePressedPane, TargetClient: launchDefaultClient,
		OriginSession: "alpha", ContextDir: "/srv/alpha", ClientWidth: 200, ClientHeight: 50,
	}
	for _, mode := range []string{"ai-split-picker-right", "ai-split-resume-right"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"--client", launchDefaultClient, "--anchor", launchChoicePressedPane,
				popupToggleAnswerFlag, "/tmp/projmux-launch-answer-1.json", mode}
			parsed, err := parseTmuxPopupToggleArgs(args, io.Discard)
			if err != nil {
				t.Fatalf("parse popup-toggle %v: %v", args, err)
			}
			if parsed.AnswerFile != "/tmp/projmux-launch-answer-1.json" {
				t.Fatalf("mode = %+v, want the answer file", parsed)
			}
			command, options, err := buildPopupToggle(parsed, "/tmp/projmux", "/tmp/marker", popupContext)
			if err != nil {
				t.Fatalf("buildPopupToggle() error = %v", err)
			}
			for _, want := range []string{
				splitAnswerFileEnv + "='/tmp/projmux-launch-answer-1.json'",
				"TMUX_SPLIT_TARGET_PANE='" + launchChoicePressedPane + "'",
			} {
				if !strings.Contains(command, want) {
					t.Fatalf("popup command = %q, want %q", command, want)
				}
			}
			if options.Client != launchDefaultClient {
				t.Fatalf("popup client = %q, want the exact pressing client", options.Client)
			}
		})
	}
}

// TestPopupToggleRefusesAnUnroutableAnswer keeps the private flag from meaning
// anything on its own: it needs the pressed Pane, the pressing client, a split
// picker, and an absolute file.
func TestPopupToggleRefusesAnUnroutableAnswer(t *testing.T) {
	const answer = "/tmp/projmux-launch-answer-1.json"
	for _, args := range [][]string{
		{"--client", launchDefaultClient, popupToggleAnswerFlag, answer, "ai-split-picker-right"},
		{"--anchor", launchChoicePressedPane, popupToggleAnswerFlag, answer, "ai-split-picker-right"},
		{"--client", launchDefaultClient, "--anchor", launchChoicePressedPane, popupToggleAnswerFlag, answer, "sessionizer"},
		{"--client", launchDefaultClient, "--anchor", launchChoicePressedPane, popupToggleAnswerFlag, "relative.json", "ai-split-picker-right"},
	} {
		if _, err := parseTmuxPopupToggleArgs(args, io.Discard); err == nil {
			t.Fatalf("popup-toggle accepted %v", args)
		}
	}
}
