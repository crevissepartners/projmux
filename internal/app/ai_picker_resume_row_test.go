package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// ai_picker_resume_row_test.go pins the launch picker's resume row: a fixed row
// after the provider rows that swaps the popup's list for the resume session
// list in the same process. It is the mirror of the resume picker's `new` row.

const resumeRowConversation = "11111111-2222-3333-4444-555555555555"

// scriptedAIPickerRunner answers each native picker call with the next step,
// which sees the options that call was opened with.
type scriptedAIPickerRunner struct {
	options []intpickercompat.Options
	steps   []func(intpickercompat.Options) intpickercompat.Result
}

func (r *scriptedAIPickerRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.options = append(r.options, options)
	if len(r.steps) == 0 {
		return intpickercompat.Result{}, nil
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	return step(options), nil
}

func (r *scriptedAIPickerRunner) uis() []string {
	out := make([]string, 0, len(r.options))
	for _, options := range r.options {
		out = append(out, options.UI)
	}
	return out
}

func pickValue(value string) func(intpickercompat.Options) intpickercompat.Result {
	return func(intpickercompat.Options) intpickercompat.Result {
		return intpickercompat.Result{Key: "enter", Value: value}
	}
}

func pickEsc(intpickercompat.Options) intpickercompat.Result {
	return intpickercompat.Result{Key: "esc"}
}

// pickFirstResumeSession chooses the first conversation row the resume list
// shows, whatever value the list rendered for it.
func pickFirstResumeSession(options intpickercompat.Options) intpickercompat.Result {
	for _, entry := range options.Entries {
		if strings.HasPrefix(entry.Value, "resume\t") {
			return intpickercompat.Result{Key: "enter", Value: entry.Value}
		}
	}
	return intpickercompat.Result{Key: "esc"}
}

// stubResumeDiscovery replaces the resume scan with one Claude conversation and
// counts every provider scan the resume list starts.
func stubResumeDiscovery(cmd *aiCommand) *atomic.Int32 {
	var scans atomic.Int32
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		scans.Add(1)
		if provider != aiModeClaude {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		return summaryDiscovery(aiModeClaude, resumeRowConversation, aisessions.SourceClaudeTranscript, time.Time{}), nil
	}
	return &scans
}

func homeOf(t *testing.T, cmd *aiCommand) string {
	t.Helper()
	home, err := cmd.homeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func closeBindings(bindings []string) []string {
	var out []string
	for _, binding := range bindings {
		if strings.HasSuffix(binding, ":abort") {
			out = append(out, binding)
		}
	}
	slices.Sort(out)
	return out
}

func TestAgentPickerOffersResumeRowBetweenProvidersAndShell(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		enabled []config.AIAgentProvider
		want    []string
	}{
		{
			name:    "all providers",
			enabled: []config.AIAgentProvider{config.AIAgentCodex, config.AIAgentClaude, config.AIAgentAntigravity},
			want:    []string{aiModeCodex, aiModeClaude, aiModeAntigravity, aiModeResume, aiModeShell},
		},
		{name: "claude only", enabled: []config.AIAgentProvider{config.AIAgentClaude}, want: []string{aiModeClaude, aiModeResume, aiModeShell}},
		{name: "codex only", enabled: []config.AIAgentProvider{config.AIAgentCodex}, want: []string{aiModeCodex, aiModeResume, aiModeShell}},
		{name: "no providers keeps guidance and shell only", enabled: nil, want: []string{"", aiModeShell}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			enableAgents(t, home, test.enabled...)
			cmd := testAICommand(home)

			if got := entryValues(cmd.agentRows()); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("agentRows values = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestAgentPickerResumeRowLabelIsLocalized(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		locale i18n.Locale
		want   string
	}{
		{locale: i18n.FallbackLocale, want: "resume   [READY] Resume a previous session"},
		{locale: i18n.Locale("ko-KR"), want: "resume   [READY] 이전 세션 이어서 열기"},
	} {
		row := aiResumeAgentRow(test.locale)
		if got := stripANSI(row.Label); got != test.want {
			t.Errorf("locale %s resume row = %q, want %q", test.locale, got, test.want)
		}
		if row.Value != aiModeResume {
			t.Errorf("locale %s resume row value = %q, want %q", test.locale, row.Value, aiModeResume)
		}
		for _, want := range []string{aiModeResume, "Resume a previous session"} {
			if !strings.Contains(row.SearchKey, want) {
				t.Errorf("locale %s resume row SearchKey = %q, want it to contain %q", test.locale, row.SearchKey, want)
			}
		}
	}
}

func TestOpeningTheAgentPickerStartsNoResumeScan(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		step func(intpickercompat.Options) intpickercompat.Result
	}{
		{name: "cancelled", step: pickEsc},
		{name: "shell chosen", step: pickValue(aiModeShell)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, _ := intentAICommand(t, t.TempDir())
			scans := stubResumeDiscovery(cmd)
			runner := &scriptedAIPickerRunner{steps: []func(intpickercompat.Options) intpickercompat.Result{test.step}}
			cmd.nativePicker = nativePickerFromCompatRunner(runner)

			if err := cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("picker error = %v", err)
			}
			if got := runner.uis(); !reflect.DeepEqual(got, []string{"ai-picker"}) {
				t.Fatalf("picker calls = %q, want only the launch picker", got)
			}
			if !hasEntryValue(runner.options[0].Entries, aiModeResume) {
				t.Fatalf("launch picker entries = %#v, want the resume row", runner.options[0].Entries)
			}
			if got := scans.Load(); got != 0 {
				t.Fatalf("opening the launch picker ran %d resume scans, want 0", got)
			}
		})
	}
}

func TestAgentPickerResumeRowOpensTheResumeListInTheSameProcess(t *testing.T) {
	t.Parallel()

	for _, direction := range []string{"right", "down"} {
		t.Run(direction, func(t *testing.T) {
			t.Parallel()
			cmd, creator := intentAICommand(t, t.TempDir())
			scans := stubResumeDiscovery(cmd)
			runner := &scriptedAIPickerRunner{steps: []func(intpickercompat.Options) intpickercompat.Result{pickValue(aiModeResume), pickEsc}}
			cmd.nativePicker = nativePickerFromCompatRunner(runner)

			if err := cmd.Run([]string{"picker", "--inside", direction}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("picker error = %v", err)
			}
			if got, want := runner.uis(), []string{"ai-picker", "ai-resume-picker"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("picker calls = %q, want %q", got, want)
			}
			if scans.Load() == 0 {
				t.Fatal("the resume list ran no resume scan; the counted seam is not the one the resume list uses")
			}
			for _, command := range cmdRecorder(cmd).commands {
				if strings.Contains(strings.Join(command.args, " "), "popup") {
					t.Fatalf("the resume row issued %s %q; the resume list must open in the same popup", command.name, command.args)
				}
			}
			if len(creator.intents) != 0 {
				t.Fatalf("an escaped resume list created %+v", creator.intents)
			}

			splitKeys := pickerCloseBindingsForPopupToggleMode(cmd.homeDir, cmd.lookupEnv, aiSplitPickerPopupMode(direction), "esc", "ctrl-c", "ctrl-alt-s")
			resumeKeys := pickerCloseBindingsForPopupToggleMode(cmd.homeDir, cmd.lookupEnv, aiResumePickerPopupMode(direction), "esc", "ctrl-c", "ctrl-alt-s")
			want := closeBindings(uniqueNonEmptyStrings(append(append([]string{}, resumeKeys...), splitKeys...)))
			if got := closeBindings(runner.options[1].Bindings); !reflect.DeepEqual(got, want) {
				t.Fatalf("resume list close bindings = %q, want the resume and launch popup keys %q", got, want)
			}
			for _, key := range []string{"alt-7:abort", "alt-4:abort", "esc:abort"} {
				if !slices.Contains(runner.options[1].Bindings, key) {
					t.Fatalf("resume list bindings = %q, want %q", runner.options[1].Bindings, key)
				}
			}

			// Opened on its own, the resume list keeps exactly its own keys.
			direct := &scriptedAIPickerRunner{steps: []func(intpickercompat.Options) intpickercompat.Result{pickEsc}}
			cmd.nativePicker = nativePickerFromCompatRunner(direct)
			if err := cmd.runResumePicker(direction); err != nil {
				t.Fatalf("runResumePicker error = %v", err)
			}
			if got := closeBindings(direct.options[0].Bindings); !reflect.DeepEqual(got, closeBindings(resumeKeys)) {
				t.Fatalf("direct resume list close bindings = %q, want %q", got, closeBindings(resumeKeys))
			}
			if slices.Contains(direct.options[0].Bindings, "alt-7:abort") {
				t.Fatalf("direct resume list bindings = %q, must not carry the launch picker key", direct.options[0].Bindings)
			}
		})
	}
}

// TestAResumeSessionChosenThroughTheResumeRowIsTheDirectResumeAnswer holds the
// resume row to adding no encoding: in answer mode the answer file a session
// chosen through the row writes is byte-identical to the one the directly
// opened resume list writes.
func TestAResumeSessionChosenThroughTheResumeRowIsTheDirectResumeAnswer(t *testing.T) {
	answerFor := func(t *testing.T, args []string, steps ...func(intpickercompat.Options) intpickercompat.Result) []byte {
		t.Helper()
		cmd, recorder, answer := answeringPickerAICommand(t)
		enableAgents(t, homeOf(t, cmd), config.AIAgentClaude)
		stubResumeDiscovery(cmd)
		cmd.nativePicker = nativePickerFromCompatRunner(&scriptedAIPickerRunner{steps: steps})

		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("picker %q error = %v", args, err)
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
		return raw
	}

	viaRow := answerFor(t, []string{"picker", "--inside", "right"}, pickValue(aiModeResume), pickFirstResumeSession)
	direct := answerFor(t, []string{"picker", "--inside", "--resume", "right"}, pickFirstResumeSession)

	if len(direct) == 0 {
		t.Fatal("the direct resume list wrote no answer")
	}
	if !bytes.Equal(viaRow, direct) {
		t.Fatalf("answer through the resume row = %s, want the direct resume answer %s", viaRow, direct)
	}
	got, err := decodeSplitSelectionAnswer(viaRow)
	if err != nil {
		t.Fatalf("decode answer %q: %v", viaRow, err)
	}
	if got.producer != canonicalProducerResumePicker || got.provider != aiModeClaude || got.conversationID != resumeRowConversation {
		t.Fatalf("answer = %+v, want the Claude resume of %s", got, resumeRowConversation)
	}
}

// TestAResumeSessionChosenThroughTheResumeRowHandsOffTheDirectContinuation is
// the popup split half: the resume row hands tmux the same continuation, and
// the continuation creates the same intent, as the directly opened resume list.
func TestAResumeSessionChosenThroughTheResumeRowHandsOffTheDirectContinuation(t *testing.T) {
	t.Parallel()

	newCmd := func(t *testing.T, steps ...func(intpickercompat.Options) intpickercompat.Result) (*aiCommand, *recordingPaneCreator) {
		t.Helper()
		home := t.TempDir()
		enableAgents(t, home, config.AIAgentClaude)
		cmd, creator := intentAICommand(t, home)
		cmd.lookupEnv = splitContinuationPopupEnv(home, nil)
		stubResumeDiscovery(cmd)
		cmd.nativePicker = nativePickerFromCompatRunner(&scriptedAIPickerRunner{steps: steps})
		return cmd, creator
	}
	viaRowArgs := []string{"picker", "--inside", "down"}
	directArgs := []string{"picker", "--inside", "--resume", "down"}
	viaRowSteps := []func(intpickercompat.Options) intpickercompat.Result{pickValue(aiModeResume), pickFirstResumeSession}
	directSteps := []func(intpickercompat.Options) intpickercompat.Result{pickFirstResumeSession}

	dispatchOf := func(t *testing.T, args []string, steps []func(intpickercompat.Options) intpickercompat.Result) string {
		t.Helper()
		cmd, creator := newCmd(t, steps...)
		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("picker %q error = %v", args, err)
		}
		if len(creator.intents) != 0 {
			t.Fatalf("picker process created %+v; the popup would stay up until it committed", creator.intents)
		}
		dispatches := splitContinuationDispatches(cmdRecorder(cmd).commands)
		if len(dispatches) != 1 {
			t.Fatalf("continuation dispatches = %q, want exactly one", dispatches)
		}
		return dispatches[0]
	}
	if viaRow, direct := dispatchOf(t, viaRowArgs, viaRowSteps), dispatchOf(t, directArgs, directSteps); viaRow != direct {
		t.Fatalf("continuation through the resume row = %q, want the direct resume continuation %q", viaRow, direct)
	}

	createdOf := func(t *testing.T, args []string, steps []func(intpickercompat.Options) intpickercompat.Result) agentPaneIntent {
		t.Helper()
		cmd, creator := newCmd(t, steps...)
		if err := runSplitPickerThroughContinuation(t, cmd, func() error {
			return cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
		}); err != nil {
			t.Fatalf("picker %q through continuation error = %v", args, err)
		}
		if len(creator.intents) != 1 {
			t.Fatalf("continuation created %d intents, want 1: %+v", len(creator.intents), creator.intents)
		}
		return creator.intents[0]
	}
	viaRow, direct := createdOf(t, viaRowArgs, viaRowSteps), createdOf(t, directArgs, directSteps)
	if !reflect.DeepEqual(viaRow, direct) {
		t.Fatalf("created through the resume row = %+v, want the direct resume intent %+v", viaRow, direct)
	}
	if viaRow.producer != canonicalProducerResumePicker || viaRow.conversationID != resumeRowConversation {
		t.Fatalf("created = %+v, want the Claude resume of %s", viaRow, resumeRowConversation)
	}
}

func TestResumeListReachedThroughTheResumeRowInAnswerMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		steps []func(intpickercompat.Options) intpickercompat.Result
		uis   []string
		want  *agentPaneIntent
	}{
		{
			name:  "esc writes nothing",
			steps: []func(intpickercompat.Options) intpickercompat.Result{pickValue(aiModeResume), pickEsc},
			uis:   []string{"ai-picker", "ai-resume-picker"},
		},
		{
			name:  "new row returns to the launch picker",
			steps: []func(intpickercompat.Options) intpickercompat.Result{pickValue(aiModeResume), pickValue(aiResumeNewValue), pickValue(aiModeClaude)},
			uis:   []string{"ai-picker", "ai-resume-picker", "ai-picker"},
			want:  &agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, recorder, answer := answeringPickerAICommand(t)
			enableAgents(t, homeOf(t, cmd), config.AIAgentClaude)
			stubResumeDiscovery(cmd)
			runner := &scriptedAIPickerRunner{steps: test.steps}
			cmd.nativePicker = nativePickerFromCompatRunner(runner)

			if err := cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("picker error = %v", err)
			}
			if got := runner.uis(); !reflect.DeepEqual(got, test.uis) {
				t.Fatalf("picker calls = %q, want %q", got, test.uis)
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
			if test.want == nil {
				if len(raw) != 0 {
					t.Fatalf("a cancelled resume list wrote %q", raw)
				}
				return
			}
			got, err := decodeSplitSelectionAnswer(raw)
			if err != nil {
				t.Fatalf("decode answer %q: %v", raw, err)
			}
			if !reflect.DeepEqual(got, *test.want) {
				t.Fatalf("answer = %+v, want %+v", got, *test.want)
			}
		})
	}
}
