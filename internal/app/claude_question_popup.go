package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/terminaltext"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	// claudeQuestionPickerRoute is the hidden `internal` route the way-2
	// question popup runs: the projmux picker for one recorded question set.
	claudeQuestionPickerRoute = "claude-question-picker"
	claudeQuestionPopupTitle  = "Claude question"
	claudeQuestionPopupWidth  = "80%"
	claudeQuestionPopupHeight = "70%"
	// claudeQuestionPopupStopWait bounds how long a finished hook waits for
	// the popup it closes to report that it ended.
	claudeQuestionPopupStopWait = 2 * time.Second
	// claudeQuestionPickerNotice is how long the picker shows why its answer
	// was not taken before the popup closes.
	claudeQuestionPickerNotice = 2 * time.Second
)

// errClaudeQuestionPopupPanic stands for a popup goroutine that panicked: the
// popup is treated as ended, which gives a still-waiting question back.
var errClaudeQuestionPopupPanic = errors.New("question popup panicked")

// claudeQuestionPopupTarget names one popup: the client it is drawn on, the
// Pane it is placed over, and the record its picker answers.
type claudeQuestionPopupTarget struct {
	Client     string
	PaneID     string
	QuestionID string
	AgentUID   string
	StorePath  string
}

// claudeQuestionPopup is the tmux side of way 2. ViewingClient names a client
// whose active Pane is paneID, or "" when none is. Open draws the picker popup
// and returns when it ends. Close closes the popup on client.
type claudeQuestionPopup interface {
	ViewingClient(ctx context.Context, paneID string) (string, error)
	Open(ctx context.Context, target claudeQuestionPopupTarget) error
	Close(ctx context.Context, client string) error
}

// tmuxClaudeQuestionPopup talks to the tmux server the hook's own $TMUX names,
// which is the server of the Agent Pane Claude Code runs in, through the same
// plain `tmux` runner the hook-trust popup uses. Its output is captured, so
// nothing is drawn on Claude Code's terminal.
type tmuxClaudeQuestionPopup struct {
	runner     tmuxRunner
	executable func() (string, error)
	lookupEnv  func(string) string
}

func defaultClaudeQuestionPopup() claudeQuestionPopup {
	return tmuxClaudeQuestionPopup{runner: inttmux.ExecRunner{}, executable: rawExecutablePath, lookupEnv: os.Getenv}
}

// ViewingClient picks, among the terminal clients whose active Pane is paneID,
// the one used most recently. In list-clients, #{pane_id} is the active Pane
// of the client's current Window; tmux 3.6 expands #{client_active_pane} to
// nothing, so that spelling would never find a client. A control-mode client cannot draw a popup and is
// never picked. Without $TMUX there is no server to ask.
func (p tmuxClaudeQuestionPopup) ViewingClient(ctx context.Context, paneID string) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" || p.runner == nil || p.lookupEnv == nil || strings.TrimSpace(p.lookupEnv("TMUX")) == "" {
		return "", nil
	}
	out, err := p.runner.Run(ctx, "tmux", "list-clients", "-F", "#{client_name}\t#{pane_id}\t#{client_control_mode}\t#{client_activity}")
	if err != nil {
		return "", err
	}
	return claudeQuestionViewingClient(string(out), paneID), nil
}

// claudeQuestionViewingClient reads list-clients rows of name, active Pane,
// control-mode flag, and activity.
func claudeQuestionViewingClient(rows, paneID string) string {
	best, bestActivity := "", int64(-1)
	for line := range strings.SplitSeq(strings.TrimRight(rows, "\r\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[1]) != paneID || strings.TrimSpace(fields[2]) == "1" {
			continue
		}
		activity, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		if activity > bestActivity {
			best, bestActivity = strings.TrimSpace(fields[0]), activity
		}
	}
	return best
}

func (p tmuxClaudeQuestionPopup) Open(ctx context.Context, target claudeQuestionPopupTarget) error {
	if p.runner == nil || p.executable == nil {
		return errors.New("question popup is not configured")
	}
	binaryPath, err := p.executable()
	if err != nil {
		return err
	}
	args, err := buildClaudeQuestionPopupArgs(binaryPath, target)
	if err != nil {
		return err
	}
	_, err = p.runner.Run(ctx, "tmux", args...)
	return err
}

func (p tmuxClaudeQuestionPopup) Close(ctx context.Context, client string) error {
	if p.runner == nil || strings.TrimSpace(client) == "" {
		return nil
	}
	_, err := p.runner.Run(ctx, "tmux", "display-popup", "-C", "-c", strings.TrimSpace(client))
	return err
}

// buildClaudeQuestionPopupArgs spells the blocking display-popup that runs the
// picker route. Everything the picker needs travels as argv.
func buildClaudeQuestionPopupArgs(binaryPath string, target claudeQuestionPopupTarget) ([]string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return nil, errors.New("question popup binary path is required")
	}
	if strings.TrimSpace(target.Client) == "" || strings.TrimSpace(target.QuestionID) == "" || strings.TrimSpace(target.AgentUID) == "" || strings.TrimSpace(target.StorePath) == "" {
		return nil, errors.New("question popup needs a client, a question, an Agent, and a store")
	}
	command := strings.Join([]string{
		tmuxShellQuote(binaryPath),
		"internal",
		claudeQuestionPickerRoute,
		"--question", tmuxShellQuote(target.QuestionID),
		"--agent", tmuxShellQuote(target.AgentUID),
		"--store", tmuxShellQuote(target.StorePath),
	}, " ")
	return inttmux.BuildDisplayPopupArgs(command, inttmux.PopupOptions{
		Client:        strings.TrimSpace(target.Client),
		Target:        strings.TrimSpace(target.PaneID),
		CloseBehavior: inttmux.PopupCloseOnExit,
		Width:         claudeQuestionPopupWidth,
		Height:        claudeQuestionPopupHeight,
		Title:         claudeQuestionPopupTitle,
	})
}

// claudeQuestionPopupDriver keeps at most one popup open for one waiting
// record, and never a second one after the first has ended.
type claudeQuestionPopupDriver struct {
	popup    claudeQuestionPopup
	interval time.Duration
	target   claudeQuestionPopupTarget
	nextLook time.Time
	// ended receives once, when the open popup ends; nil while none is open.
	ended    chan error
	open     bool
	finished bool
	cancel   context.CancelFunc
}

func newClaudeQuestionPopupDriver(popup claudeQuestionPopup, interval time.Duration, paneID string, store *agentquestion.Store, record agentquestion.Record) *claudeQuestionPopupDriver {
	if interval <= 0 {
		interval = claudeQuestionClientPoll
	}
	return &claudeQuestionPopupDriver{
		popup:    popup,
		interval: interval,
		target:   claudeQuestionPopupTarget{PaneID: strings.TrimSpace(paneID), QuestionID: record.ID, AgentUID: record.AgentUID, StorePath: store.Path()},
	}
}

// maybeOpen opens the popup on the client viewing the Pane, looking at most
// once per interval. Not finding one, or failing to ask, is left for the next
// look.
func (d *claudeQuestionPopupDriver) maybeOpen(ctx context.Context) {
	if d.popup == nil || d.target.PaneID == "" || d.open || d.finished {
		return
	}
	now := time.Now()
	if now.Before(d.nextLook) {
		return
	}
	d.nextLook = now.Add(d.interval)
	client, err := d.popup.ViewingClient(ctx, d.target.PaneID)
	if err != nil || strings.TrimSpace(client) == "" {
		return
	}
	target := d.target
	target.Client = strings.TrimSpace(client)
	d.target = target
	openCtx, cancel := context.WithCancel(ctx)
	ended := make(chan error, 1)
	d.ended, d.open, d.cancel = ended, true, cancel
	go func() {
		defer func() {
			if recover() != nil {
				ended <- errClaudeQuestionPopupPanic
			}
		}()
		ended <- d.popup.Open(openCtx, target)
	}()
}

// markEnded records that the open popup ended on its own.
func (d *claudeQuestionPopupDriver) markEnded() {
	d.ended, d.open, d.finished = nil, false, true
	if d.cancel != nil {
		d.cancel()
	}
}

// stop closes a popup that is still open, waits a bounded moment for it to
// end, and releases it. It is safe to call more than once.
func (d *claudeQuestionPopupDriver) stop() {
	if !d.open {
		return
	}
	ended := d.ended
	func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), claudeQuestionPopupStopWait)
		defer cancel()
		_ = d.popup.Close(ctx, d.target.Client)
	}()
	select {
	case <-ended:
	case <-time.After(claudeQuestionPopupStopWait):
	}
	d.markEnded()
}

// runClaudeQuestionPicker is the process entry of the popup route.
func runClaudeQuestionPicker(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal "+claudeQuestionPickerRoute, flag.ContinueOnError)
	fs.SetOutput(stderr)
	questionID := fs.String("question", "", "question record id")
	agentUID := fs.String("agent", "", "uid of the Agent that asked")
	storePath := fs.String("store", "", "question store file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*questionID) == "" || strings.TrimSpace(*agentUID) == "" || strings.TrimSpace(*storePath) == "" {
		return usageError("internal " + claudeQuestionPickerRoute + " requires --question <id> --agent <uid> --store <path>")
	}
	defer applyNativeUIThemeFromConfig(os.UserHomeDir, os.Getenv, "")()
	picker := claudeQuestionPicker{
		store:  agentquestion.NewStoreAt(strings.TrimSpace(*storePath)),
		runner: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		out:    stdout,
		pause:  time.Sleep,
	}
	return picker.run(strings.TrimSpace(*questionID), strings.TrimSpace(*agentUID))
}

// claudeQuestionPicker asks one recorded question set with the native picker
// and settles the record through the same store path `agent question answer`
// uses. Esc, and a picker that fails, close the record, which hands the
// question back to Claude Code's own prompt.
type claudeQuestionPicker struct {
	store  *agentquestion.Store
	runner intpicker.Runner
	out    io.Writer
	pause  func(time.Duration)
}

func (p claudeQuestionPicker) run(questionID, agentUID string) error {
	if p.store == nil || p.runner == nil {
		return errors.New("question picker is not configured")
	}
	record, found, err := p.store.Get(questionID)
	if err != nil {
		return err
	}
	if !found || record.AgentUID != agentUID || record.State != agentquestion.StateWaiting {
		return nil
	}
	questions, err := record.ParsedQuestions()
	if err != nil {
		_, _ = p.store.Close(record.ID)
		return err
	}
	selections, ok, err := collectClaudeQuestionSelections(p.runner, questions)
	if err != nil || !ok {
		_, _ = p.store.Close(record.ID)
		return err
	}
	answers, err := agentquestion.BuildAnswers(questions, selections)
	if err != nil {
		_, _ = p.store.Close(record.ID)
		return err
	}
	if _, err := p.store.Answer(record.ID, agentUID, answers); err != nil {
		reason, detail := questionStoreRefusal(err)
		if reason == "" {
			reason, detail = "question-store-error", err.Error()
		}
		p.notice(fmt.Sprintf("question %s %s (%s); this answer was not used", record.ID, detail, reason))
	}
	return nil
}

func (p claudeQuestionPicker) notice(message string) {
	if p.out != nil {
		_, _ = fmt.Fprintln(p.out, message)
	}
	if p.pause != nil {
		p.pause(claudeQuestionPickerNotice)
	}
}

// Picker row values. An option row's value is its 0-based position.
const (
	claudeQuestionOtherValue = "other"
	claudeQuestionDoneValue  = "done"
	claudeQuestionOptionTag  = "option:"
)

// collectClaudeQuestionSelections asks every question in order and returns one
// Selection per question index. ok is false when the operator canceled.
func collectClaudeQuestionSelections(runner intpicker.Runner, questions []agentquestion.Question) (map[int]agentquestion.Selection, bool, error) {
	selections := make(map[int]agentquestion.Selection, len(questions))
	for index, question := range questions {
		selection, ok, err := askClaudeQuestion(runner, index, len(questions), question)
		if err != nil || !ok {
			return nil, false, err
		}
		selections[index] = selection
	}
	return selections, true, nil
}

// askClaudeQuestion asks one question. A single-select question takes the
// option Enter lands on. A multi-select question toggles options with Enter
// and ends on the Done row, which needs at least one option. The "Other" row
// asks for free text; Esc there, or empty text, goes back to the options.
// Esc on the options cancels the whole question set.
func askClaudeQuestion(runner intpicker.Runner, index, count int, question agentquestion.Question) (agentquestion.Selection, bool, error) {
	chosen := make([]bool, len(question.Options))
	cursor, notice := 0, ""
	title := fmt.Sprintf("%s %d/%d", claudeQuestionPopupTitle, index+1, count)
	if header := strings.TrimSpace(question.Header); header != "" {
		title += " - " + terminaltext.EscapeControls(header)
	}
	footer := "Enter: choose  Esc: give the question back to Claude"
	if question.MultiSelect {
		footer = "Enter: toggle, then Done  Esc: give the question back to Claude"
	}
	for {
		items := make([]intpicker.Item, 0, len(question.Options)+2)
		for position, option := range question.Options {
			label := terminaltext.EscapeControls(option.Label)
			if question.MultiSelect {
				mark := "[ ] "
				if chosen[position] {
					mark = "[x] "
				}
				label = mark + label
			}
			if description := strings.TrimSpace(option.Description); description != "" {
				label += " - " + terminaltext.EscapeControls(description)
			}
			items = append(items, intpicker.Item{Label: label, Value: claudeQuestionOptionTag + strconv.Itoa(position)})
		}
		if question.MultiSelect {
			items = append(items, intpicker.Item{Label: "Done", Value: claudeQuestionDoneValue})
		}
		items = append(items, intpicker.Item{Label: "Other / type an answer", Value: claudeQuestionOtherValue})
		header := terminaltext.EscapeControls(question.Question)
		if notice != "" {
			header += "\n" + notice
		}
		result, err := runner.Run(intpicker.Options{
			UI:              "claude-question",
			Items:           items,
			Title:           title,
			Header:          header,
			Footer:          footer,
			DisableSearch:   true,
			InitialIndex:    cursor,
			InitialIndexSet: true,
		})
		if err != nil {
			return agentquestion.Selection{}, false, err
		}
		if result.Closed {
			return agentquestion.Selection{}, false, nil
		}
		notice = ""
		switch value := result.Value; {
		case value == claudeQuestionOtherValue:
			text, ok, err := askClaudeQuestionText(runner, title, question)
			if err != nil {
				return agentquestion.Selection{}, false, err
			}
			if ok {
				return agentquestion.Selection{Text: text, HasText: true}, true, nil
			}
			cursor = len(items) - 1
		case value == claudeQuestionDoneValue:
			labels := make([]string, 0, len(question.Options))
			for position, option := range question.Options {
				if chosen[position] {
					labels = append(labels, option.Label)
				}
			}
			if len(labels) != 0 {
				return agentquestion.Selection{Labels: labels}, true, nil
			}
			notice = "Choose at least one option before Done."
			cursor = len(question.Options)
		case strings.HasPrefix(value, claudeQuestionOptionTag):
			position, err := strconv.Atoi(strings.TrimPrefix(value, claudeQuestionOptionTag))
			if err != nil || position < 0 || position >= len(question.Options) {
				continue
			}
			if !question.MultiSelect {
				return agentquestion.Selection{Labels: []string{question.Options[position].Label}}, true, nil
			}
			chosen[position] = !chosen[position]
			cursor = position
		}
	}
}

// askClaudeQuestionText reads one free-text answer. ok is false for Esc and
// for empty text.
func askClaudeQuestionText(runner intpicker.Runner, title string, question agentquestion.Question) (string, bool, error) {
	result, err := runner.Run(intpicker.Options{
		UI:          "claude-question-text",
		Title:       title,
		Header:      terminaltext.EscapeControls(question.Question),
		Prompt:      "Answer > ",
		Footer:      "Enter: use this answer  Esc: back to the options",
		AcceptQuery: true,
	})
	if err != nil {
		return "", false, err
	}
	text := strings.TrimSpace(result.Query)
	if result.Closed || text == "" {
		return "", false, nil
	}
	return text, true, nil
}
