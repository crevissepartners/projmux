package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// fakeQuestionPopup stands in for tmux. ViewingClient hands out clients in
// order, repeating the last. Open records its target, reports it on opened
// while that has room, and runs open, which by default blocks until Close or
// the hook's context ends.
type fakeQuestionPopup struct {
	mu      sync.Mutex
	clients []string
	views   int
	targets []claudeQuestionPopupTarget
	closes  []string
	opened  chan claudeQuestionPopupTarget
	closed  chan struct{}
	once    sync.Once
	open    func(ctx context.Context, target claudeQuestionPopupTarget, closed <-chan struct{}) error
}

func newFakeQuestionPopup(clients ...string) *fakeQuestionPopup {
	return &fakeQuestionPopup{clients: clients, opened: make(chan claudeQuestionPopupTarget, 4), closed: make(chan struct{})}
}

func (p *fakeQuestionPopup) ViewingClient(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.views++
	if len(p.clients) == 0 {
		return "", nil
	}
	client := p.clients[0]
	if len(p.clients) > 1 {
		p.clients = p.clients[1:]
	}
	return client, nil
}

func (p *fakeQuestionPopup) Open(ctx context.Context, target claudeQuestionPopupTarget) error {
	p.mu.Lock()
	p.targets = append(p.targets, target)
	open := p.open
	p.mu.Unlock()
	select {
	case p.opened <- target:
	default:
	}
	if open != nil {
		return open(ctx, target, p.closed)
	}
	select {
	case <-p.closed:
	case <-ctx.Done():
	}
	return nil
}

func (p *fakeQuestionPopup) Close(_ context.Context, client string) error {
	p.mu.Lock()
	p.closes = append(p.closes, client)
	p.mu.Unlock()
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *fakeQuestionPopup) counts() (views, opens, closes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.views, len(p.targets), len(p.closes)
}

func (p *fakeQuestionPopup) waitOpened(t *testing.T) claudeQuestionPopupTarget {
	t.Helper()
	select {
	case target := <-p.opened:
		return target
	case <-time.After(10 * time.Second):
		t.Fatal("the popup never opened")
		return claudeQuestionPopupTarget{}
	}
}

// scriptedQuestionPicker answers each picker run with the next step.
type scriptedQuestionPicker struct {
	mu    sync.Mutex
	steps []func(intpicker.Options) (intpicker.Result, error)
	runs  []intpicker.Options
}

func (p *scriptedQuestionPicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs = append(p.runs, options)
	if len(p.steps) == 0 {
		return intpicker.Result{}, errors.New("scripted picker ran out of steps")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	return step(options)
}

// pickRow chooses the row whose label contains label.
func pickRow(label string) func(intpicker.Options) (intpicker.Result, error) {
	return func(options intpicker.Options) (intpicker.Result, error) {
		for _, item := range options.Items {
			if strings.Contains(item.Label, label) {
				return intpicker.Result{Key: "enter", Value: item.Value}, nil
			}
		}
		return intpicker.Result{}, errors.New("no row " + label)
	}
}

func typeAnswer(text string) func(intpicker.Options) (intpicker.Result, error) {
	return func(options intpicker.Options) (intpicker.Result, error) {
		if !options.AcceptQuery {
			return intpicker.Result{}, errors.New("typed into a list")
		}
		return intpicker.Result{Key: "enter", Query: text}, nil
	}
}

func pressEsc(intpicker.Options) (intpicker.Result, error) {
	return intpicker.Result{Key: "esc", Closed: true}, nil
}

// TestCollectClaudeQuestionSelectionsAssemblesEveryAnswerShape is the picker
// answer assembly: what the rows the operator picks turn into, through the
// same BuildAnswers the command line uses.
func TestCollectClaudeQuestionSelectionsAssemblesEveryAnswerShape(t *testing.T) {
	t.Parallel()

	single := agentquestion.Question{Question: "Which branch?", Options: []agentquestion.Option{{Label: "main"}, {Label: "dev", Description: "the integration branch"}}}
	multi := agentquestion.Question{Question: "Which tools?", MultiSelect: true, Options: []agentquestion.Option{{Label: "make"}, {Label: "task"}, {Label: "just"}}}
	for _, test := range []struct {
		name      string
		questions []agentquestion.Question
		steps     []func(intpicker.Options) (intpicker.Result, error)
		want      map[string]string
		canceled  bool
	}{
		{name: "single select", questions: []agentquestion.Question{single}, steps: steps(pickRow("dev")), want: map[string]string{"Which branch?": "dev"}},
		{name: "single select free text", questions: []agentquestion.Question{single}, steps: steps(pickRow("Other"), typeAnswer("  release/1.0 ")), want: map[string]string{"Which branch?": "release/1.0"}},
		{name: "free text Esc goes back to the options", questions: []agentquestion.Question{single}, steps: steps(pickRow("Other"), pressEsc, pickRow("main")), want: map[string]string{"Which branch?": "main"}},
		{name: "empty free text goes back to the options", questions: []agentquestion.Question{single}, steps: steps(pickRow("Other"), typeAnswer("  "), pickRow("main")), want: map[string]string{"Which branch?": "main"}},
		{name: "multi select in option order", questions: []agentquestion.Question{multi}, steps: steps(pickRow("just"), pickRow("make"), pickRow("Done")), want: map[string]string{"Which tools?": "make, just"}},
		{name: "multi select toggles off", questions: []agentquestion.Question{multi}, steps: steps(pickRow("task"), pickRow("just"), pickRow("[x] task"), pickRow("Done")), want: map[string]string{"Which tools?": "just"}},
		{name: "multi select Done needs one option", questions: []agentquestion.Question{multi}, steps: steps(pickRow("Done"), pickRow("task"), pickRow("Done")), want: map[string]string{"Which tools?": "task"}},
		{name: "multi select free text", questions: []agentquestion.Question{multi}, steps: steps(pickRow("Other"), typeAnswer("bazel")), want: map[string]string{"Which tools?": "bazel"}},
		{name: "several questions", questions: []agentquestion.Question{multi, single}, steps: steps(pickRow("task"), pickRow("Done"), pickRow("Other"), typeAnswer("feature")), want: map[string]string{"Which tools?": "task", "Which branch?": "feature"}},
		{name: "Esc cancels the set", questions: []agentquestion.Question{multi, single}, steps: steps(pickRow("task"), pickRow("Done"), pressEsc), canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &scriptedQuestionPicker{steps: test.steps}
			selections, ok, err := collectClaudeQuestionSelections(runner, claudeQuestionText{locale: i18n.FallbackLocale}, test.questions)
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.steps) != 0 {
				t.Fatalf("%d scripted steps left unused", len(runner.steps))
			}
			if test.canceled {
				if ok || selections != nil {
					t.Fatalf("canceled set = %#v, %v", selections, ok)
				}
				return
			}
			if !ok {
				t.Fatal("the set was canceled")
			}
			answers, err := agentquestion.BuildAnswers(test.questions, selections)
			if err != nil || !reflect.DeepEqual(answers, test.want) {
				t.Fatalf("answers = %#v (%v), want %#v", answers, err, test.want)
			}
		})
	}

	t.Run("rows and notices", func(t *testing.T) {
		t.Parallel()
		runner := &scriptedQuestionPicker{steps: steps(pickRow("Done"), pickRow("make"), pickRow("Done"))}
		if _, ok, err := collectClaudeQuestionSelections(runner, claudeQuestionText{locale: i18n.FallbackLocale}, []agentquestion.Question{multi}); err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		labels := func(options intpicker.Options) []string {
			out := []string{}
			for _, item := range options.Items {
				out = append(out, item.Label)
			}
			return out
		}
		if got, want := labels(runner.runs[0]), []string{"[ ] make", "[ ] task", "[ ] just", "Done", "Other / type an answer"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rows = %q, want %q", got, want)
		}
		if !strings.Contains(runner.runs[1].Header, "at least one option") {
			t.Fatalf("header after an empty Done = %q", runner.runs[1].Header)
		}
		if got := labels(runner.runs[2]); got[0] != "[x] make" || runner.runs[2].InitialIndex != 0 {
			t.Fatalf("rows after a toggle = %q at %d", got, runner.runs[2].InitialIndex)
		}
		described := &scriptedQuestionPicker{steps: steps(pickRow("main"))}
		if _, _, err := collectClaudeQuestionSelections(described, claudeQuestionText{locale: i18n.FallbackLocale}, []agentquestion.Question{single}); err != nil {
			t.Fatal(err)
		}
		if got := labels(described.runs[0]); got[1] != "dev - the integration branch" || !described.runs[0].DisableSearch {
			t.Fatalf("single-select rows = %q", got)
		}
	})

	t.Run("the question wraps in both pickers", func(t *testing.T) {
		t.Parallel()
		long := agentquestion.Question{Question: "Which branch should the release train use?\nThe freeze starts \x1b[31mtomorrow.", Options: []agentquestion.Option{{Label: "main"}}}
		runner := &scriptedQuestionPicker{steps: steps(pickRow("Other"), typeAnswer("feature"))}
		if _, ok, err := collectClaudeQuestionSelections(runner, claudeQuestionText{locale: i18n.FallbackLocale}, []agentquestion.Question{long}); err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		want := "Which branch should the release train use?\nThe freeze starts \\x1b[31mtomorrow."
		for index, run := range runner.runs {
			if !run.WrapHeader || run.Header != want {
				t.Fatalf("run %d (%s) WrapHeader=%v header=%q, want wrapped %q", index, run.UI, run.WrapHeader, run.Header, want)
			}
		}
		if len(runner.runs) != 2 || runner.runs[1].UI != "claude-question-text" {
			t.Fatalf("runs = %d, want the question picker and the free-text picker", len(runner.runs))
		}
	})

	t.Run("a picker error ends the set", func(t *testing.T) {
		t.Parallel()
		failing := &scriptedQuestionPicker{steps: steps(func(intpicker.Options) (intpicker.Result, error) {
			return intpicker.Result{}, errors.New("no tty")
		})}
		if _, ok, err := collectClaudeQuestionSelections(failing, claudeQuestionText{locale: i18n.FallbackLocale}, []agentquestion.Question{single}); err == nil || ok {
			t.Fatalf("ok=%v err=%v, want the picker error", ok, err)
		}
	})
}

func steps(list ...func(intpicker.Options) (intpicker.Result, error)) []func(intpicker.Options) (intpicker.Result, error) {
	return list
}

// questionFixturePicker is the picker the popup route runs, answering the
// fixture's two-question set with the given steps.
func (f *questionFixture) picker(list ...func(intpicker.Options) (intpicker.Result, error)) (claudeQuestionPicker, *bytes.Buffer) {
	var out bytes.Buffer
	return claudeQuestionPicker{store: f.store, runner: &scriptedQuestionPicker{steps: list}, text: claudeQuestionText{locale: i18n.FallbackLocale}, out: &out, pause: func(time.Duration) {}}, &out
}

// runPickerInPopup makes the fake popup run the picker route with steps, the
// way the real popup runs it in the popup's terminal.
func (f *questionFixture) runPickerInPopup(popup *fakeQuestionPopup, list ...func(intpicker.Options) (intpicker.Result, error)) {
	popup.open = func(_ context.Context, target claudeQuestionPopupTarget, _ <-chan struct{}) error {
		picker, _ := f.picker(list...)
		return picker.run(target.QuestionID, target.AgentUID)
	}
}

// TestClaudeQuestionHookResolutionOrder is the way table: the Agent's
// annotation is way 2 without reading the setting, the setting decides
// otherwise, and a question that is not a projmux Claude Agent's never reads
// it.
func TestClaudeQuestionHookResolutionOrder(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		annotation bool
		setting    string // "" leaves the file missing
		pane       string
		wantWay2   bool
		wantReads  int
	}{
		{name: "annotation on, setting missing", annotation: true, wantWay2: true},
		{name: "annotation on, setting claude", annotation: true, setting: "claude", wantWay2: true},
		{name: "annotation on, setting projmux", annotation: true, setting: "projmux", wantWay2: true},
		{name: "annotation off, setting missing", wantReads: 1},
		{name: "annotation off, setting claude", setting: "claude", wantReads: 1},
		{name: "annotation off, setting garbage", setting: "yes please", wantReads: 1},
		{name: "annotation off, setting projmux", setting: "projmux", wantWay2: true, wantReads: 1},
		{name: "annotation off, setting PROJMUX", setting: " PROJMUX\n", wantWay2: true, wantReads: 1},
		{name: "not a projmux Agent, setting projmux", setting: "projmux", pane: "pan-nowhere"},
		{name: "not a projmux Agent, annotation on", annotation: true, setting: "projmux", pane: "pan-alpha-zsh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, test.annotation)
			paths := config.DefaultPaths(t.TempDir(), t.TempDir())
			if test.setting != "" {
				if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.AgentQuestionAnsweringFile(), []byte(test.setting), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			hook := fixture.hook(time.Minute)
			hook.answering = func() config.AgentQuestionAnswering {
				fixture.answeringCalls++
				return claudeQuestionAnsweringFromPaths(paths)
			}
			pane := test.pane
			if pane == "" {
				pane = questionTestPane
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan string, 1)
			go func() {
				var stdout bytes.Buffer
				hook.run(ctx, []string{"--pane=" + pane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
				done <- stdout.String()
			}()
			if test.wantWay2 {
				// Way 2 records the question and waits; cancel it once recorded.
				for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
					if records, err := fixture.store.List(questionTestAgent); err == nil && len(records) == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("way 2 never recorded the question")
					}
				}
			}
			cancel()
			if got := waitHookOutput(t, done); got != "" {
				t.Fatalf("hook printed %q", got)
			}
			if fixture.answeringCalls != test.wantReads {
				t.Fatalf("answering setting read %d times, want %d", fixture.answeringCalls, test.wantReads)
			}
			if test.wantWay2 != (fixture.opened == 1) {
				t.Fatalf("store opened %d times, want way 2 = %v", fixture.opened, test.wantWay2)
			}
		})
	}
}

// TestClaudeQuestionHookWayOneReadsTheSettingOnceAndOpensNothing is way 1 for
// a confirmed, not-opted-in projmux Claude Agent: exactly one setting read,
// and otherwise today's silence: no store, no window, no tmux, no output.
func TestClaudeQuestionHookWayOneReadsTheSettingOnceAndOpensNothing(t *testing.T) {
	t.Parallel()

	for _, setting := range []config.AgentQuestionAnswering{"", config.AgentQuestionAnsweringClaude, "garbage"} {
		fixture := newQuestionFixture(t, false)
		fixture.answering = setting
		popup := newFakeQuestionPopup("client-1")
		fixture.popup = popup
		var stdout, stderr bytes.Buffer
		fixture.hook(time.Minute).run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &stderr)
		if stdout.Len() != 0 || stderr.Len() != 0 || fixture.opened != 0 || fixture.windowCalls != 0 || fixture.answeringCalls != 1 {
			t.Fatalf("setting %q: stdout=%q stderr=%q store=%d window=%d answering=%d", setting, stdout.String(), stderr.String(), fixture.opened, fixture.windowCalls, fixture.answeringCalls)
		}
		if views, opens, _ := popup.counts(); views != 0 || opens != 0 {
			t.Fatalf("setting %q: popup views=%d opens=%d", setting, views, opens)
		}
		if _, err := os.Stat(filepath.Dir(fixture.store.Path())); !os.IsNotExist(err) {
			t.Fatalf("store directory stat err = %v, want not exist", err)
		}
	}
}

// TestClaudeQuestionHookWayTwoAnswersFromThePopup is way 2 from the global
// setting: the question is recorded, the popup opens on the client the
// operator used last, placed over the Agent's Pane and titled with the asking
// Agent, and its picker answer is the tool result.
func TestClaudeQuestionHookWayTwoAnswersFromThePopup(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	popup := newFakeQuestionPopup("/dev/pts/9")
	fixture.runPickerInPopup(popup, pickRow("task"), pickRow("just"), pickRow("Done"), pickRow("dev"))
	fixture.popup = popup
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":` +
		questionTestQuestions + `,"answers":{"Which branch?":"dev","Which build tool?":"task, just"}}}}` + "\n"
	if got := waitHookOutput(t, done); got != want {
		t.Fatalf("decision =\n%s\nwant\n%s", got, want)
	}
	target := popup.waitOpened(t)
	if target != (claudeQuestionPopupTarget{Client: "/dev/pts/9", PaneID: "%7", QuestionID: id, AgentUID: questionTestAgent, StorePath: fixture.store.Path(), Asker: claudeQuestionAsker{Agent: "codex", Location: "alpha/main"}}) {
		t.Fatalf("popup target = %#v", target)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateAnswered {
		t.Fatalf("state = %s, want answered", record.State)
	}
	// The popup ended on its own, so the hook does not close it again: a
	// later popup on that client is not its to close. Nor does it open again.
	if _, opens, closes := popup.counts(); closes != 0 || opens != 1 {
		t.Fatalf("popup opened %d and closed %d times after it ended on its own, want 1 and 0", opens, closes)
	}
}

// TestClaudeQuestionHookPopupAnswerReadBeforePopupEnds fixes the ordering:
// the ticker reads the picker's answer while Open is still running, then the
// popup ends before the hook can process that read. Cleanup must not Close a
// popup that has already ended on its own.
func TestClaudeQuestionHookPopupAnswerReadBeforePopupEnds(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	popup := newFakeQuestionPopup("client-1")
	fixture.popup = popup
	pickerAnswered := make(chan error, 1)
	releasePopup := make(chan struct{})
	popupReturned := make(chan struct{})
	popup.open = func(_ context.Context, target claudeQuestionPopupTarget, _ <-chan struct{}) error {
		picker, _ := fixture.picker(pickRow("make"), pickRow("Done"), pickRow("main"))
		err := picker.run(target.QuestionID, target.AgentUID)
		pickerAnswered <- err
		<-releasePopup
		close(popupReturned)
		return err
	}
	answerRead := make(chan struct{})
	allowRead := make(chan struct{})
	hook := fixture.hook(time.Minute)
	hook.readRecord = func(store *agentquestion.Store, id string) (agentquestion.Record, bool, error) {
		current, found, err := store.Get(id)
		if err == nil && found && current.State == agentquestion.StateAnswered {
			close(answerRead)
			<-allowRead
		}
		return current, found, err
	}
	done := make(chan string, 1)
	go func() {
		var stdout bytes.Buffer
		hook.run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	target := popup.waitOpened(t)
	select {
	case err := <-pickerAnswered:
		if err != nil {
			t.Fatalf("picker answer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("picker did not answer")
	}
	select {
	case <-answerRead:
	case <-time.After(10 * time.Second):
		t.Fatal("ticker did not read the picker answer")
	}
	close(releasePopup)
	select {
	case <-popupReturned:
	case <-time.After(10 * time.Second):
		t.Fatal("popup did not end after its answer")
	}
	close(allowRead)
	wantAnswer := `"answers":{"Which branch?":"main","Which build tool?":"make"}`
	if got := waitHookOutput(t, done); !strings.Contains(got, wantAnswer) {
		t.Fatalf("decision = %q, want picker answer %s", got, wantAnswer)
	}
	if _, _, closes := popup.counts(); closes != 0 {
		t.Fatalf("popup Close called %d times after Answer -> ticker read -> popup ended, want 0", closes)
	}
	if target.Client != "client-1" {
		t.Fatalf("popup client = %q, want client-1", target.Client)
	}
}

// TestClaudeQuestionHookCommandLineAnswerWinsAndClosesThePopup is the race:
// the command line answers while the popup's picker is still open, the hook
// closes the popup and returns that answer, and the picker's own answer that
// comes after is refused as question-not-pending.
func TestClaudeQuestionHookCommandLineAnswerWinsAndClosesThePopup(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	fixture.command.questionAnswering = func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }
	popup := newFakeQuestionPopup("client-1")
	fixture.popup = popup
	cliAnswered := make(chan error, 1)
	pickerDone := make(chan string, 1)
	popup.open = func(_ context.Context, target claudeQuestionPopupTarget, closed <-chan struct{}) error {
		picker, out := fixture.picker(
			pickRow("task"),
			func(options intpicker.Options) (intpicker.Result, error) {
				// The command line answers while this picker is open, and the
				// hook closes the popup before the operator finishes here.
				_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, target.QuestionID, "--option", "1=make", "--option", "2=main")
				cliAnswered <- err
				<-closed
				return pickRow("Done")(options)
			},
			pickRow("dev"),
		)
		err := picker.run(target.QuestionID, target.AgentUID)
		pickerDone <- out.String()
		return err
	}
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	if err := <-cliAnswered; err != nil {
		t.Fatalf("command-line answer under way 2: %v", err)
	}
	if got := waitHookOutput(t, done); !strings.Contains(got, `"answers":{"Which branch?":"main","Which build tool?":"make"}`) {
		t.Fatalf("decision = %q, want the command-line answer", got)
	}
	if _, _, closes := popup.counts(); closes != 1 || popup.closes[0] != "client-1" {
		t.Fatalf("popup closes = %q, want one on client-1", popup.closes)
	}
	select {
	case out := <-pickerDone:
		if !strings.Contains(out, "("+questionReasonNotPending+")") {
			t.Fatalf("late popup answer printed %q, want %s", out, questionReasonNotPending)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the picker did not finish")
	}
	if record, _, _ := fixture.store.Get(id); record.Answers["Which build tool?"] != "make" {
		t.Fatalf("answers = %#v, want the command-line answer kept", record.Answers)
	}
}

// TestAgentQuestionAnswerFollowsTheAnsweringSetting holds the command line
// side of way 2: an Agent that is not opted in takes an answer exactly while
// the setting is projmux.
func TestAgentQuestionAnswerFollowsTheAnsweringSetting(t *testing.T) {
	t.Parallel()

	for _, setting := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
		fixture := newQuestionFixture(t, false)
		fixture.command.questionAnswering = func() config.AgentQuestionAnswering { return setting }
		record := fixture.createQuestionRecord(t)
		_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, record.ID, "--option", "1=make", "--option", "2=main")
		if setting == config.AgentQuestionAnsweringProjmux && err != nil {
			t.Fatalf("way 2 answer: %v", err)
		}
		if setting == config.AgentQuestionAnsweringClaude && (err == nil || !strings.Contains(err.Error(), "(question-channel-off)")) {
			t.Fatalf("way 1 answer err = %v, want question-channel-off", err)
		}
	}
}

// TestClaudeQuestionHookFailurePathsGiveTheQuestionBack runs each failure a
// way-2 question can meet. Every one prints nothing and returns, which Claude
// Code reads as no decision, and a record that was created is left closed.
func TestClaudeQuestionHookFailurePathsGiveTheQuestionBack(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*questionFixture, *claudeQuestionHook, *fakeQuestionPopup)
		// recorded is whether the path gets as far as recording the question.
		recorded bool
	}{
		{name: "hook failure: no question id", setup: func(_ *questionFixture, hook *claudeQuestionHook, _ *fakeQuestionPopup) {
			hook.newID = func() (string, error) { return "", errors.New("no entropy") }
		}},
		{name: "store failure", setup: func(_ *questionFixture, hook *claudeQuestionHook, _ *fakeQuestionPopup) {
			hook.store = func() (*agentquestion.Store, error) { return nil, errors.New("no state dir") }
		}},
		{name: "popup open failure", recorded: true, setup: func(_ *questionFixture, _ *claudeQuestionHook, popup *fakeQuestionPopup) {
			popup.open = func(context.Context, claudeQuestionPopupTarget, <-chan struct{}) error {
				return errors.New("display-popup: no current client")
			}
		}},
		{name: "picker crash", recorded: true, setup: func(_ *questionFixture, _ *claudeQuestionHook, popup *fakeQuestionPopup) {
			// The popup's process ends without touching the record.
			popup.open = func(context.Context, claudeQuestionPopupTarget, <-chan struct{}) error {
				return errors.New("exit status 2")
			}
		}},
		{name: "popup goroutine panic", recorded: true, setup: func(_ *questionFixture, _ *claudeQuestionHook, popup *fakeQuestionPopup) {
			popup.open = func(context.Context, claudeQuestionPopupTarget, <-chan struct{}) error { panic("popup bug") }
		}},
		{name: "Esc in the popup", recorded: true, setup: func(fixture *questionFixture, _ *claudeQuestionHook, popup *fakeQuestionPopup) {
			fixture.runPickerInPopup(popup, pickRow("make"), pressEsc)
		}},
		{name: "picker error in the popup", recorded: true, setup: func(fixture *questionFixture, _ *claudeQuestionHook, popup *fakeQuestionPopup) {
			fixture.runPickerInPopup(popup, func(intpicker.Options) (intpicker.Result, error) { return intpicker.Result{}, errors.New("no tty") })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, false)
			fixture.answering = config.AgentQuestionAnsweringProjmux
			popup := newFakeQuestionPopup("client-1")
			fixture.popup = popup
			hook := fixture.hook(time.Minute)
			test.setup(fixture, &hook, popup)
			var stdout bytes.Buffer
			done := make(chan struct{})
			go func() {
				defer close(done)
				hook.run(context.Background(), []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the hook did not return")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want no decision", stdout.String())
			}
			records, _ := fixture.store.List(questionTestAgent)
			if !test.recorded {
				if len(records) != 0 {
					t.Fatalf("records = %#v, want none", records)
				}
				return
			}
			if len(records) != 1 || records[0].State != agentquestion.StateClosed {
				t.Fatalf("records = %#v, want one closed", records)
			}
			// A popup that showed and ended is never opened again.
			if _, opens, _ := popup.counts(); opens != 1 {
				t.Fatalf("popup opened %d times, want 1", opens)
			}
		})
	}
}

// TestClaudeQuestionHookWaitsForAViewingClient is no client attached: the hook
// keeps waiting, looks again, and opens the popup once a client attaches; an
// answer from the popup then settles it.
func TestClaudeQuestionHookWaitsForAViewingClient(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	popup := newFakeQuestionPopup("", "", "", "client-late")
	fixture.runPickerInPopup(popup, pickRow("make"), pickRow("Done"), pickRow("main"))
	fixture.popup = popup
	_, done := fixture.startHook(t, context.Background(), time.Minute)
	if got := waitHookOutput(t, done); !strings.Contains(got, `"permissionDecision":"allow"`) {
		t.Fatalf("decision = %q", got)
	}
	if views, opens, _ := popup.counts(); views != 4 || opens != 1 || popup.targets[0].Client != "client-late" {
		t.Fatalf("views=%d opens=%d targets=%#v, want the popup on the fourth look", views, opens, popup.targets)
	}
}

// TestClaudeQuestionHookWithNoClientExpiresToTheWidget is the window running
// out with nobody viewing the Pane: no popup, no decision, an expired record.
func TestClaudeQuestionHookWithNoClientExpiresToTheWidget(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	popup := newFakeQuestionPopup("")
	fixture.popup = popup
	id, done := fixture.startHook(t, context.Background(), 200*time.Millisecond)
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("expired hook printed %q", got)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateExpired {
		t.Fatalf("state = %s, want expired", record.State)
	}
	if views, opens, _ := popup.counts(); views < 2 || opens != 0 {
		t.Fatalf("views=%d opens=%d, want repeated looks and no popup", views, opens)
	}
}

// notShownThen makes the fake popup report a popup tmux never drew for its
// first count opens, and run then from there on.
func (p *fakeQuestionPopup) notShownThen(count int, then func(ctx context.Context, target claudeQuestionPopupTarget, closed <-chan struct{}) error) {
	calls := 0
	p.open = func(ctx context.Context, target claudeQuestionPopupTarget, closed <-chan struct{}) error {
		calls++
		if calls <= count || then == nil {
			return errClaudeQuestionPopupNotShown
		}
		return then(ctx, target, closed)
	}
}

// TestClaudeQuestionHookRetriesAPopupThatNeverShowed is a client that already
// shows a popup, say another Agent's question: tmux takes the display-popup
// without drawing it. The record keeps waiting, the hook tries again, and the
// popup that finally shows answers the question. Nothing is closed.
func TestClaudeQuestionHookRetriesAPopupThatNeverShowed(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	popup := newFakeQuestionPopup("client-1")
	popup.opened = make(chan claudeQuestionPopupTarget, 8)
	fixture.popup = popup
	popup.notShownThen(2, func(_ context.Context, target claudeQuestionPopupTarget, _ <-chan struct{}) error {
		if record, _, _ := fixture.store.Get(target.QuestionID); record.State != agentquestion.StateWaiting {
			return errors.New("the record stopped waiting while the popup could not show: " + string(record.State))
		}
		picker, _ := fixture.picker(pickRow("make"), pickRow("Done"), pickRow("dev"))
		return picker.run(target.QuestionID, target.AgentUID)
	})
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	if got := waitHookOutput(t, done); !strings.Contains(got, `"answers":{"Which branch?":"dev","Which build tool?":"make"}`) {
		t.Fatalf("decision = %q, want the answer from the popup that showed", got)
	}
	if _, opens, closes := popup.counts(); opens != 3 || closes != 0 {
		t.Fatalf("opens=%d closes=%d, want two popups that never showed, one that did, and no Close", opens, closes)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateAnswered {
		t.Fatalf("state = %s, want answered", record.State)
	}
}

// TestClaudeQuestionHookCommandLineAnswerWhileThePopupCannotShow is a question
// whose popup never gets drawn: it waits, a command-line answer ends the wait,
// and the hook sends no Close, which would shut whatever popup the client does
// show.
func TestClaudeQuestionHookCommandLineAnswerWhileThePopupCannotShow(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	fixture.answering = config.AgentQuestionAnsweringProjmux
	fixture.command.questionAnswering = func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux }
	popup := newFakeQuestionPopup("client-1")
	popup.opened = make(chan claudeQuestionPopupTarget, 64)
	popup.notShownThen(0, nil)
	fixture.popup = popup
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	popup.waitOpened(t)
	popup.waitOpened(t)
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateWaiting {
		t.Fatalf("state = %s, want waiting while the popup cannot show", record.State)
	}
	if _, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, id, "--option", "1=make", "--option", "2=main"); err != nil {
		t.Fatalf("command-line answer: %v", err)
	}
	if got := waitHookOutput(t, done); !strings.Contains(got, `"answers":{"Which branch?":"main","Which build tool?":"make"}`) {
		t.Fatalf("decision = %q, want the command-line answer", got)
	}
	if _, _, closes := popup.counts(); closes != 0 {
		t.Fatalf("popup closed %d times, want none for a popup that never showed", closes)
	}
}

// TestClaudeQuestionPopupDriverStopLeavesAnEndedPopupAlone is stop meeting a
// popup that already ended, whether the wait saw it or not: it sends no Close,
// since the client may show another popup by now, and never reopens.
func TestClaudeQuestionPopupDriverStopLeavesAnEndedPopupAlone(t *testing.T) {
	t.Parallel()

	for _, answered := range []bool{false, true} {
		for _, end := range []error{nil, errClaudeQuestionPopupNotShown} {
			popup := newFakeQuestionPopup("client-1")
			ended := make(chan error, 1)
			ended <- end
			driver := &claudeQuestionPopupDriver{popup: popup, interval: time.Millisecond, target: claudeQuestionPopupTarget{Client: "client-1", PaneID: "%7"}, ended: ended, open: true}
			driver.stop(answered)
			if _, opens, closes := popup.counts(); closes != 0 || opens != 0 || driver.open || !driver.finished {
				t.Fatalf("answered=%v end=%v: opens=%d closes=%d open=%v finished=%v", answered, end, opens, closes, driver.open, driver.finished)
			}
			driver.maybeOpen(context.Background())
			if _, opens, _ := popup.counts(); opens != 0 || driver.open {
				t.Fatalf("answered=%v end=%v: a stopped driver opened a popup", answered, end)
			}
		}
	}
}

// TestClaudeQuestionAskerNamesTheAgentAndItsLocation reads who asks from the
// Registry: the Agent's name, then its Project and Window names as far as
// they are known.
func TestClaudeQuestionAskerNamesTheAgentAndItsLocation(t *testing.T) {
	t.Parallel()

	registry := newQuestionFixture(t, false).resources.registry
	agent, _ := registry.Agent(questionTestAgent)
	if got := claudeQuestionAskerOf(registry, *agent); got != (claudeQuestionAsker{Agent: "codex", Location: "alpha/main"}) {
		t.Fatalf("asker = %+v, want codex at alpha/main", got)
	}
	noProject := registry.Clone()
	window, _ := noProject.Window(agent.Metadata.OwnerUID())
	window.Metadata.OwnerRef = nil
	if got := claudeQuestionAskerOf(noProject, *agent); got != (claudeQuestionAsker{Agent: "codex", Location: "main"}) {
		t.Fatalf("asker without a Project = %+v, want codex at main", got)
	}
	orphan := agent.Clone()
	orphan.Metadata.OwnerRef = nil
	orphan.Metadata.Name = ""
	if got := claudeQuestionAskerOf(registry, orphan); got != (claudeQuestionAsker{Agent: questionTestAgent}) {
		t.Fatalf("asker without a Window or name = %+v, want the uid alone", got)
	}
}

// TestClaudeQuestionHookDeadlineAndCancelCloseAnOpenPopup holds that the hook
// never leaves its popup behind: the window ending and a cancellation both
// close it, and both give the question back.
func TestClaudeQuestionHookDeadlineAndCancelCloseAnOpenPopup(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		window time.Duration
		cancel bool
		state  agentquestion.State
	}{
		{name: "window ends", window: 300 * time.Millisecond, state: agentquestion.StateExpired},
		{name: "canceled", window: time.Minute, cancel: true, state: agentquestion.StateClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, true)
			popup := newFakeQuestionPopup("client-1")
			fixture.popup = popup
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, done := fixture.startHook(t, ctx, test.window)
			popup.waitOpened(t)
			if test.cancel {
				cancel()
			}
			if got := waitHookOutput(t, done); got != "" {
				t.Fatalf("hook printed %q", got)
			}
			if _, _, closes := popup.counts(); closes != 1 {
				t.Fatalf("popup closed %d times, want 1", closes)
			}
			if record, _, _ := fixture.store.Get(id); record.State != test.state {
				t.Fatalf("state = %s, want %s", record.State, test.state)
			}
		})
	}
}

// TestClaudeQuestionViewingClientPicksTheMostRecentTerminalClient holds the
// client choice: the terminal client with the latest input wins whatever it
// shows, a tie goes to the client showing the Agent's Pane and then to the
// first listed, and a control-mode client is never picked.
func TestClaudeQuestionViewingClientPicksTheMostRecentTerminalClient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		rows []string
		want string
	}{
		{name: "most recent wins while it shows another Pane", rows: []string{
			"/dev/pts/1\t%7\t0\t100",
			"/dev/pts/2\t%7\t0\t300",
			"control-1\t%7\t1\t900",
			"/dev/pts/3\t%8\t0\t999",
			"malformed",
		}, want: "/dev/pts/3"},
		{name: "a tie goes to the Agent Pane's client", rows: []string{
			"/dev/pts/1\t%8\t0\t500",
			"/dev/pts/2\t%7\t0\t500",
			"/dev/pts/3\t%9\t0\t500",
		}, want: "/dev/pts/2"},
		{name: "a tie away from the Agent Pane goes to the first listed", rows: []string{
			"/dev/pts/1\t%8\t0\t500",
			"/dev/pts/2\t%9\t0\t500",
			"/dev/pts/3\t%7\t0\t400",
		}, want: "/dev/pts/1"},
		{name: "control mode is skipped", rows: []string{
			"control-1\t%7\t1\t900",
			"/dev/pts/1\t%8\t0\t100",
		}, want: "/dev/pts/1"},
		{name: "only control mode", rows: []string{"control-1\t%7\t1\t900"}},
		{name: "no rows", rows: nil},
		{name: "no name", rows: []string{"\t%7\t0\t900"}},
	} {
		rows := strings.Join(test.rows, "\n") + "\n"
		if got := claudeQuestionViewingClient(rows, "%7"); got != test.want {
			t.Errorf("%s: client = %q, want %q", test.name, got, test.want)
		}
	}
	runner := &noCallTmuxRunner{}
	popup := tmuxClaudeQuestionPopup{runner: runner, lookupEnv: func(string) string { return "" }}
	if got, err := popup.ViewingClient(context.Background(), "%7"); got != "" || err != nil || len(runner.calls) != 0 {
		t.Fatalf("without $TMUX = %q, %v, calls %q; want no client and no tmux call", got, err, runner.calls)
	}
	routed := &routedPopupClientRunner{}
	target, err := tmuxSocketPathTarget("/tmp/projmux-question-test/socket")
	if err != nil {
		t.Fatal(err)
	}
	popup = tmuxClaudeQuestionPopup{runner: explicitTmuxRunner{runner: routed, target: target}, routed: true}
	if got, err := popup.ViewingClient(context.Background(), "%7"); err != nil || got != "/dev/pts/9" {
		t.Fatalf("routed Codex client = %q, %v", got, err)
	}
	if len(routed.calls) != 1 || !reflect.DeepEqual(routed.calls[0][:3], []string{"-S", target.Value, "list-clients"}) {
		t.Fatalf("Codex popup used a non-exact tmux route: %q", routed.calls)
	}
}

type routedPopupClientRunner struct{ calls [][]string }

func (r *routedPopupClientRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, slices.Clone(args))
	return []byte("/dev/pts/9\t%7\t0\t900\n"), nil
}

func TestBuildClaudeQuestionPopupArgs(t *testing.T) {
	t.Parallel()

	target := claudeQuestionPopupTarget{
		Client: "/dev/pts/3", PaneID: "%7", QuestionID: "question-0123456789abcdef", AgentUID: "agt-1", StorePath: "/state/agent-questions/questions.json",
	}
	args, err := buildClaudeQuestionPopupArgs("/opt/pm x/projmux", claudeQuestionText{locale: i18n.FallbackLocale}.title(), "", target, claudeQuestionPopupSize{Width: "80%", Height: "70%"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"display-popup", "-c", "/dev/pts/3", "-t", "%7", "-E", "-w", "80%", "-h", "70%", "-T", "Agent question",
		"'/opt/pm x/projmux' internal claude-question-picker --question 'question-0123456789abcdef' --agent 'agt-1' --store '/state/agent-questions/questions.json'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args =\n%q\nwant\n%q", args, want)
	}
	// The asker is named in the title, which tmux expands as a format: a `#`
	// in a name is doubled and a control character escaped. The marker
	// travels as --shown.
	target.Asker = claudeQuestionAsker{Agent: "fix#1", Location: "alpha/ma\x1bin"}
	title := claudeQuestionText{locale: i18n.FallbackLocale}.popupTitle(target.Asker)
	args, err = buildClaudeQuestionPopupArgs("/bin/projmux", title, "/tmp/projmux-question-shown-1", target, claudeQuestionPopupSize{Width: "80%", Height: "70%"})
	if err != nil {
		t.Fatal(err)
	}
	if index := slices.Index(args, "-T"); index < 0 || args[index+1] != `Agent question from fix##1 (alpha/ma\x1bin)` {
		t.Fatalf("args = %q, want the escaped asker title", args)
	}
	if command := args[len(args)-1]; !strings.HasSuffix(command, " --shown '/tmp/projmux-question-shown-1'") {
		t.Fatalf("command = %q, want the --shown marker", command)
	}
	if _, err := buildClaudeQuestionPopupArgs("/bin/projmux", "Claude question", "", claudeQuestionPopupTarget{PaneID: "%7"}, claudeQuestionPopupSize{Width: "80%", Height: "70%"}); err == nil {
		t.Fatal("a popup without a client was built")
	}
	if shouldRunLegacyHookMigrations([]string{"internal", claudeQuestionPickerRoute, "--question", "q"}) {
		t.Fatal("the picker route runs the automatic hook migration")
	}
}

func TestClaudeQuestionPopupSizeKeepsAMinimumOnSmallClients(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		reply string
		err   error
		want  claudeQuestionPopupSize
	}{
		{reply: "120 40\n", want: claudeQuestionPopupSize{Width: "96", Height: "28"}},
		{reply: "60 24\n", want: claudeQuestionPopupSize{Width: "60", Height: "22"}},
		{reply: "200 60\n", want: claudeQuestionPopupSize{Width: "160", Height: "42"}},
		{reply: "40 16\n", want: claudeQuestionPopupSize{Width: "40", Height: "16"}},
		{reply: "120 40\n", err: errors.New("no client"), want: claudeQuestionPopupSize{Width: "80%", Height: "70%"}},
		{reply: "\n", want: claudeQuestionPopupSize{Width: "80%", Height: "70%"}},
		{reply: "wide 40\n", want: claudeQuestionPopupSize{Width: "80%", Height: "70%"}},
		{reply: "0 0\n", want: claudeQuestionPopupSize{Width: "80%", Height: "70%"}},
	} {
		if got := claudeQuestionPopupSizeFor(test.reply, test.err); got != test.want {
			t.Fatalf("size for %q (%v) = %+v, want %+v", test.reply, test.err, got, test.want)
		}
	}
}

func TestClaudeQuestionPopupOpenSizesThePopupFromTheClient(t *testing.T) {
	t.Parallel()

	target := claudeQuestionPopupTarget{Client: "/dev/pts/3", PaneID: "%7", QuestionID: "question-1", AgentUID: "agt-1", StorePath: "/state/questions.json"}
	for _, test := range []struct {
		name          string
		reply         string
		err           error
		width, height string
	}{
		{name: "small client", reply: "60 24\n", width: "60", height: "22"},
		{name: "size query fails", err: errors.New("no such client"), width: "80%", height: "70%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &popupSizeTmuxRunner{reply: test.reply, err: test.err, show: true}
			popup := tmuxClaudeQuestionPopup{runner: runner, executable: func() (string, error) { return "/bin/projmux", nil }}
			if err := popup.Open(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 2 {
				t.Fatalf("tmux calls = %q, want the size query and the popup", runner.calls)
			}
			if want := []string{"tmux", "display-message", "-p", "-c", "/dev/pts/3", "#{client_width} #{client_height}"}; !reflect.DeepEqual(runner.calls[0], want) {
				t.Fatalf("size query = %q, want %q", runner.calls[0], want)
			}
			args := runner.calls[1]
			if index := slices.Index(args, "-w"); args[1] != "display-popup" || index < 0 || args[index+1] != test.width || args[index+3] != test.height {
				t.Fatalf("popup args = %q, want -w %s -h %s", args, test.width, test.height)
			}
		})
	}
}

// TestClaudeQuestionPopupOpenTellsAPopupThatNeverShowed holds the marker: a
// popup whose picker wrote it showed, one that left it empty never did, a
// failed display-popup is that failure, and the marker is always removed.
func TestClaudeQuestionPopupOpenTellsAPopupThatNeverShowed(t *testing.T) {
	t.Parallel()

	target := claudeQuestionPopupTarget{Client: "/dev/pts/3", PaneID: "%7", QuestionID: "question-1", AgentUID: "agt-1", StorePath: "/state/questions.json"}
	popupFailed := errors.New("exit status 127")
	for _, test := range []struct {
		name     string
		show     bool
		popupErr error
		want     error
	}{
		{name: "shown", show: true},
		{name: "never shown", want: errClaudeQuestionPopupNotShown},
		{name: "display-popup failed", popupErr: popupFailed, want: popupFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var marker string
			runner := &popupSizeTmuxRunner{reply: "120 40\n", show: test.show, popupErr: test.popupErr}
			popup := tmuxClaudeQuestionPopup{
				runner:     runner,
				executable: func() (string, error) { return "/bin/projmux", nil },
				newMarker: func() (string, error) {
					marker = filepath.Join(dir, "shown")
					return marker, os.WriteFile(marker, nil, 0o600)
				},
			}
			if err := popup.Open(context.Background(), target); !errors.Is(err, test.want) {
				t.Fatalf("Open = %v, want %v", err, test.want)
			}
			if got := popupShownMarker(runner.calls[len(runner.calls)-1]); got != marker {
				t.Fatalf("--shown = %q, want %q", got, marker)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("marker stat err = %v, want it removed", err)
			}
		})
	}
}

// TestClaudeQuestionPickerWritesTheShownMarkerFirst holds the picker side:
// --shown is written before the route even checks its other flags, and the
// route still runs without it, as an older hook invokes it.
func TestClaudeQuestionPickerWritesTheShownMarkerFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	marker := filepath.Join(dir, "shown")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runClaudeQuestionPicker([]string{"--shown", marker}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("the route ran without a question")
	}
	if data, err := os.ReadFile(marker); err != nil || len(data) == 0 {
		t.Fatalf("marker = %q (%v), want it written before the flags are checked", data, err)
	}
	// A marker that is gone is not created again.
	gone := filepath.Join(dir, "gone")
	store := filepath.Join(dir, "questions.json")
	if err := runClaudeQuestionPicker([]string{"--question", "question-1", "--agent", "agt-1", "--store", store, "--shown", gone}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("picker with a missing marker: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("missing marker stat err = %v, want it still missing", err)
	}
	if err := runClaudeQuestionPicker([]string{"--question", "question-1", "--agent", "agt-1", "--store", store}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("picker without --shown: %v", err)
	}
}

// popupSizeTmuxRunner answers the client size query and records every call.
// With show set, display-popup writes the --shown marker the way the picker
// does when tmux draws the popup; popupErr is what display-popup returns.
type popupSizeTmuxRunner struct {
	reply    string
	err      error
	show     bool
	popupErr error
	calls    [][]string
}

func (r *popupSizeTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(args) > 0 && args[0] == "display-message" {
		return []byte(r.reply), r.err
	}
	if len(args) > 0 && args[0] == "display-popup" && r.show {
		markClaudeQuestionPopupShown(popupShownMarker(args))
	}
	return nil, r.popupErr
}

// popupShownMarker is the --shown path in a display-popup's command.
func popupShownMarker(args []string) string {
	_, marker, found := strings.Cut(args[len(args)-1], " --shown ")
	if !found {
		return ""
	}
	return strings.Trim(marker, "'")
}

// noCallTmuxRunner records any tmux call and fails it.
type noCallTmuxRunner struct{ calls [][]string }

func (r *noCallTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, errors.New("unexpected tmux call")
}

// Panic safety runs the real process entry in a child process: an unrecovered
// Go panic exits 2, and Claude Code reads a PreToolUse exit 2 as blocking the
// tool, so only a separate process shows the exit status Claude Code sees.
const (
	claudeQuestionHookChildEnv = "PMX_TEST_CLAUDE_QUESTION_HOOK_CHILD"
	claudeQuestionHookDirEnv   = "PMX_TEST_CLAUDE_QUESTION_HOOK_DIR"
)

// panicQuestionPopup panics where the hook calls it, after the question is
// recorded.
type panicQuestionPopup struct{}

func (panicQuestionPopup) ViewingClient(context.Context, string) (string, error) {
	panic("viewing client bug")
}
func (panicQuestionPopup) Open(context.Context, claudeQuestionPopupTarget) error { return nil }
func (panicQuestionPopup) Close(context.Context, string) error                   { return nil }

// exitIfClaudeQuestionHookChild is the child side, run from TestMain before
// any test: it drives the hook entry with a panic injected and exits with
// whatever status the entry leaves.
func exitIfClaudeQuestionHookChild() {
	mode := os.Getenv(claudeQuestionHookChildEnv)
	if mode == "" {
		return
	}
	dir := os.Getenv(claudeQuestionHookDirEnv)
	newHook := func() claudeQuestionHook {
		return claudeQuestionHook{
			loadRegistry: func() (coremetadata.Registry, error) {
				if strings.HasSuffix(mode, "registry-panic") {
					panic("registry bug")
				}
				data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
				if err != nil {
					return coremetadata.Registry{}, err
				}
				var registry coremetadata.Registry
				err = json.Unmarshal(data, &registry)
				return registry, err
			},
			store: func() (*agentquestion.Store, error) {
				return agentquestion.NewStoreAt(filepath.Join(dir, "questions.json")), nil
			},
			answering: func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringProjmux },
			window:    func() time.Duration { return time.Minute },
			popup:     panicQuestionPopup{},
			poll:      10 * time.Millisecond,
			newID:     agentquestion.NewID,
			now:       time.Now,
		}
	}
	args := []string{"--pane=" + questionTestPane}
	if strings.HasPrefix(mode, "unwrapped-") {
		// The control: the hook run without the entry's recover.
		newHook().run(context.Background(), args, os.Stdin, os.Stdout, os.Stderr)
		os.Exit(0)
	}
	if err := runClaudeQuestionHookWith(newHook, args, os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func runClaudeQuestionHookChild(t *testing.T, mode, dir string) (int, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^$")
	command.Env = append(os.Environ(), claudeQuestionHookChildEnv+"="+mode, claudeQuestionHookDirEnv+"="+dir)
	command.Stdin = strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion"))
	var stdout bytes.Buffer
	command.Stdout = &stdout
	err = command.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stdout.String()
	default:
		t.Fatalf("run child: %v", err)
		return -1, ""
	}
}

func TestClaudeQuestionHookPanicExitsZeroWithNoOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		mode     string
		recorded bool
		wantExit int
	}{
		// Way 1's own path: the Registry read runs for every Claude question.
		{mode: "registry-panic", wantExit: 0},
		// A panic after the question was recorded also closes the record.
		{mode: "popup-panic", recorded: true, wantExit: 0},
		// The control shows the hazard the recover removes.
		{mode: "unwrapped-registry-panic", wantExit: 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, false)
			dir := t.TempDir()
			data, err := json.Marshal(fixture.resources.registry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "registry.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout := runClaudeQuestionHookChild(t, test.mode, dir)
			if code != test.wantExit || stdout != "" {
				t.Fatalf("child exit %d stdout %q, want exit %d and no output", code, stdout, test.wantExit)
			}
			records, err := agentquestion.NewStoreAt(filepath.Join(dir, "questions.json")).List(questionTestAgent)
			if err != nil {
				t.Fatal(err)
			}
			if test.recorded != (len(records) == 1) || (test.recorded && records[0].State != agentquestion.StateClosed) {
				t.Fatalf("records = %#v, want recorded=%v and closed", records, test.recorded)
			}
		})
	}
}

// claudeQuestionPickerChildEnv makes the test binary run the popup route: the
// real popup execs a wrapper script that sets it and passes the popup argv on.
const claudeQuestionPickerChildEnv = "PMX_TEST_CLAUDE_QUESTION_PICKER_CHILD"

// exitIfClaudeQuestionPickerChild is the popup side, run from TestMain.
func exitIfClaudeQuestionPickerChild() {
	if os.Getenv(claudeQuestionPickerChildEnv) != "1" {
		return
	}
	args := os.Args[1:]
	if len(args) < 2 || args[0] != "internal" || args[1] != claudeQuestionPickerRoute {
		os.Exit(64)
	}
	if err := runClaudeQuestionPicker(args[2:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// TestClaudeQuestionPickerChromeResolvesInBothLocales holds the popup chrome
// in the catalog: every key has an English entry equal to the fallback the
// code carries and a distinct Korean entry, the rendered picker shows the
// Korean chrome while Claude's question and labels stay verbatim, and the
// lost-race notice keeps its id and reason token as data.
func TestClaudeQuestionPickerChromeResolvesInBothLocales(t *testing.T) {
	t.Parallel()

	fallbacks := map[i18n.Key]string{
		keyClaudeQuestionTitle:           "Agent question",
		keyClaudeQuestionTitleFrom:       "Agent question from {agent}",
		keyClaudeQuestionTitleFromAt:     "Agent question from {agent} ({location})",
		keyClaudeQuestionTitleProgress:   "Agent question {index}/{count}",
		keyClaudeQuestionFooterSingle:    "Enter: choose  Esc: return the question to the agent",
		keyClaudeQuestionFooterMulti:     "Enter: toggle, then Done  Esc: return the question to the agent",
		keyClaudeQuestionDone:            "Done",
		keyClaudeQuestionOther:           "Other / type an answer",
		keyClaudeQuestionDoneNeedsOption: "Choose at least one option before Done.",
		keyClaudeQuestionTextPrompt:      "Answer > ",
		keyClaudeQuestionTextFooter:      "Enter: use this answer  Esc: back to the options",
		keyClaudeQuestionAnswerNotUsed:   "Question {id}: this answer was not used ({reason}).",
	}
	ko := i18n.Locale("ko-KR")
	for key, fallback := range fallbacks {
		en, err := i18n.NewLocalizer(i18n.FallbackLocale).Text(key)
		if err != nil || en.String() != fallback {
			t.Errorf("%s en = %q (%v), want %q", key, en.String(), err, fallback)
		}
		korean, err := i18n.NewLocalizer(ko).Text(key)
		if err != nil || korean.Locale() != ko || korean.String() == fallback {
			t.Errorf("%s ko = %q from %s (%v), want a Korean entry", key, korean.String(), korean.Locale(), err)
		}
		if strings.Contains(strings.ToLower(en.String()), "claude") || strings.Contains(strings.ToLower(korean.String()), "claude") {
			t.Errorf("%s has a provider-specific popup label", key)
		}
		for _, placeholder := range []string{"{index}", "{count}", "{id}", "{reason}", "{agent}", "{location}"} {
			if strings.Contains(fallback, placeholder) != strings.Contains(korean.String(), placeholder) {
				t.Errorf("%s ko %q does not carry %s like the English text", key, korean.String(), placeholder)
			}
		}
	}

	question := agentquestion.Question{Question: "Which tools?", Header: "Build", MultiSelect: true, Options: []agentquestion.Option{{Label: "make"}}}
	runner := &scriptedQuestionPicker{steps: steps(pickRow("완료"), pickRow("make"), pickRow("기타"), pressEsc, pickRow("완료"))}
	selections, ok, err := collectClaudeQuestionSelections(runner, claudeQuestionText{locale: ko}, []agentquestion.Question{question})
	if err != nil || !ok || selections[0].Labels[0] != "make" {
		t.Fatalf("selections = %#v, %v, %v", selections, ok, err)
	}
	first := runner.runs[0]
	if first.Title != "에이전트 질문 1/1 - Build" || first.Footer != "Enter: 선택 전환 후 완료  Esc: 질문을 에이전트에게 돌려주기" || first.Header != "Which tools?" {
		t.Fatalf("ko chrome title=%q footer=%q header=%q", first.Title, first.Footer, first.Header)
	}
	if got := []string{first.Items[0].Label, first.Items[1].Label, first.Items[2].Label}; !reflect.DeepEqual(got, []string{"[ ] make", "완료", "기타 / 직접 입력"}) {
		t.Fatalf("ko rows = %q", got)
	}
	if !strings.Contains(runner.runs[1].Header, "완료하기 전에 옵션을 하나 이상 선택하세요.") {
		t.Fatalf("ko notice header = %q", runner.runs[1].Header)
	}
	if text := runner.runs[3]; text.Prompt != "답변 > " || text.Footer != "Enter: 이 답변 사용  Esc: 옵션으로 돌아가기" {
		t.Fatalf("ko free-text prompt=%q footer=%q", text.Prompt, text.Footer)
	}
	notice := claudeQuestionText{locale: ko}.format(keyClaudeQuestionAnswerNotUsed, "", "{id}", "question-0123456789abcdef", "{reason}", questionReasonNotPending)
	if notice != "질문 question-0123456789abcdef: 이 답변은 사용되지 않았습니다 (question-not-pending)." {
		t.Fatalf("ko notice = %q", notice)
	}
	if got := (claudeQuestionText{locale: ko}).title(); got != "에이전트 질문" {
		t.Fatalf("ko popup title = %q", got)
	}
	for _, test := range []struct {
		asker  claudeQuestionAsker
		locale i18n.Locale
		want   string
	}{
		{asker: claudeQuestionAsker{Agent: "codex", Location: "alpha/main"}, locale: i18n.FallbackLocale, want: "Agent question from codex (alpha/main)"},
		{asker: claudeQuestionAsker{Agent: "codex"}, locale: i18n.FallbackLocale, want: "Agent question from codex"},
		{asker: claudeQuestionAsker{}, locale: i18n.FallbackLocale, want: "Agent question"},
		{asker: claudeQuestionAsker{Agent: "codex", Location: "alpha/main"}, locale: ko, want: "codex의 질문 (alpha/main)"},
		{asker: claudeQuestionAsker{Agent: "codex"}, locale: ko, want: "codex의 질문"},
	} {
		if got := (claudeQuestionText{locale: test.locale}).popupTitle(test.asker); got != test.want {
			t.Errorf("%s popup title for %+v = %q, want %q", test.locale, test.asker, got, test.want)
		}
	}
}
