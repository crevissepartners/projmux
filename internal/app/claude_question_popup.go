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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/terminaltext"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	// claudeQuestionPickerRoute is the hidden `internal` route the way-2
	// question popup runs: the projmux picker for one recorded question set.
	claudeQuestionPickerRoute = "claude-question-picker"
	// claudeQuestionPopupWidth and claudeQuestionPopupHeight are the popup's
	// share of the client, and the size used when the client size is unknown.
	claudeQuestionPopupWidth  = "80%"
	claudeQuestionPopupHeight = "70%"
	// claudeQuestionPopupMinWidth and claudeQuestionPopupMinHeight are the
	// smallest popup in cells: a small client gets a popup big enough to show
	// a wrapped question (or the whole client), and a large client keeps
	// 80% x 70%.
	claudeQuestionPopupMinWidth  = 72
	claudeQuestionPopupMinHeight = 22
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

// errClaudeQuestionPopupNotShown stands for a popup tmux never drew. A client
// that already shows a popup or another overlay takes a second display-popup
// with status 0 and never runs its command, so the picker never started: the
// question still waits, and the hook tries again later instead of giving it
// back.
var errClaudeQuestionPopupNotShown = errors.New("question popup was not shown")

// Catalog keys of the question popup's own chrome. The question text, option
// labels, record ids, and reason tokens it shows are data and stay verbatim.
const (
	keyClaudeQuestionTitle           i18n.Key = "agent.question.picker.title"
	keyClaudeQuestionTitleFrom       i18n.Key = "agent.question.picker.title_from"
	keyClaudeQuestionTitleFromAt     i18n.Key = "agent.question.picker.title_from_at"
	keyClaudeQuestionTitleProgress   i18n.Key = "agent.question.picker.title_progress"
	keyClaudeQuestionFooterSingle    i18n.Key = "agent.question.picker.footer_single"
	keyClaudeQuestionFooterMulti     i18n.Key = "agent.question.picker.footer_multi"
	keyClaudeQuestionDone            i18n.Key = "agent.question.picker.done"
	keyClaudeQuestionOther           i18n.Key = "agent.question.picker.other"
	keyClaudeQuestionDoneNeedsOption i18n.Key = "agent.question.picker.done_needs_option"
	keyClaudeQuestionTextPrompt      i18n.Key = "agent.question.picker.text_prompt"
	keyClaudeQuestionTextFooter      i18n.Key = "agent.question.picker.text_footer"
	keyClaudeQuestionAnswerNotUsed   i18n.Key = "agent.question.picker.answer_not_used"
)

// claudeQuestionText resolves the popup's chrome for one locale, the way the
// resource inspector resolves its own: a catalog key with the English text as
// the fallback, and named placeholders for the data it carries.
type claudeQuestionText struct{ locale i18n.Locale }

func (t claudeQuestionText) value(key i18n.Key, fallback string) string {
	return localizeText(t.locale, key, fallback)
}

func (t claudeQuestionText) format(key i18n.Key, fallback string, replacements ...string) string {
	return strings.NewReplacer(replacements...).Replace(t.value(key, fallback))
}

func (t claudeQuestionText) title() string {
	return t.value(keyClaudeQuestionTitle, "Claude question")
}

// popupTitle names the Agent that asks, and where it runs, in the popup's
// border title: the popup may open over any Window, so the title is what tells
// the operator whose question it is. Nothing known keeps the plain title.
func (t claudeQuestionText) popupTitle(asker claudeQuestionAsker) string {
	agent, location := strings.TrimSpace(asker.Agent), strings.TrimSpace(asker.Location)
	switch {
	case agent == "":
		return t.title()
	case location == "":
		return t.format(keyClaudeQuestionTitleFrom, "Claude question from {agent}", "{agent}", agent)
	default:
		return t.format(keyClaudeQuestionTitleFromAt, "Claude question from {agent} ({location})", "{agent}", agent, "{location}", location)
	}
}

// claudeQuestionAsker is who asks a question: the Agent's name and its
// <Project>/<Window> location, as far as the Registry knows them.
type claudeQuestionAsker struct {
	Agent    string
	Location string
}

// claudeQuestionAskerOf names agent from the Registry: its metadata name (its
// uid when it has none), and the names of its owner Window and that Window's
// Project, keeping whichever of the two is known.
func claudeQuestionAskerOf(registry coremetadata.Registry, agent coremetadata.Agent) claudeQuestionAsker {
	asker := claudeQuestionAsker{Agent: strings.TrimSpace(agent.Metadata.Name)}
	if asker.Agent == "" {
		asker.Agent = strings.TrimSpace(agent.Metadata.UID)
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return asker
	}
	parts := make([]string, 0, 2)
	if project, ok := registry.Project(window.Metadata.OwnerUID()); ok && strings.TrimSpace(project.Metadata.Name) != "" {
		parts = append(parts, strings.TrimSpace(project.Metadata.Name))
	}
	if name := strings.TrimSpace(window.Metadata.Name); name != "" {
		parts = append(parts, name)
	}
	asker.Location = strings.Join(parts, "/")
	return asker
}

// claudeQuestionPopupTarget names one popup: the client it is drawn on, the
// Pane it is placed over, the record its picker answers, and who asks.
type claudeQuestionPopupTarget struct {
	Client     string
	PaneID     string
	QuestionID string
	AgentUID   string
	StorePath  string
	Asker      claudeQuestionAsker
}

// claudeQuestionPopup is the tmux side of way 2. ViewingClient names the
// terminal client the operator used most recently, whatever it shows, or ""
// when there is none; paneID only breaks a tie. Open draws the picker popup and
// returns when it ends, with errClaudeQuestionPopupNotShown when tmux never
// drew it. Close closes the popup on client.
type claudeQuestionPopup interface {
	ViewingClient(ctx context.Context, paneID string) (string, error)
	Open(ctx context.Context, target claudeQuestionPopupTarget) error
	Close(ctx context.Context, client string) error
}

// tmuxClaudeQuestionPopup talks to the tmux server the hook's own $TMUX names,
// which is the server of the Agent Pane Claude Code runs in, through the same
// plain `tmux` runner the hook-trust popup uses. Its output is captured, so
// nothing is drawn on Claude Code's terminal.
//
// newMarker makes the empty file the picker writes once it runs, which is how
// Open tells a popup that showed from one tmux never drew; nil makes it in the
// temporary directory.
type tmuxClaudeQuestionPopup struct {
	runner     tmuxRunner
	executable func() (string, error)
	lookupEnv  func(string) string
	newMarker  func() (string, error)
}

func defaultClaudeQuestionPopup() claudeQuestionPopup {
	return tmuxClaudeQuestionPopup{runner: inttmux.ExecRunner{}, executable: rawExecutablePath, lookupEnv: os.Getenv}
}

// ViewingClient picks the terminal client of the Agent's tmux server that the
// operator used most recently, by #{client_activity} (the last input), so the
// popup opens on the terminal they are looking at, whatever Pane, Window, or
// session it shows. #{client_flags} cannot tell: without focus events every
// client reads as focused. On a tie the client whose active Pane is paneID
// wins, then the first listed. In list-clients, #{pane_id} is the active Pane
// of the client's current Window; tmux 3.6 expands #{client_active_pane} to
// nothing. A control-mode client cannot draw a popup and is never picked.
// Without $TMUX there is no server to ask; clients of other servers are never
// seen.
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
	best, bestActivity, bestOnPane := "", int64(-1), false
	for line := range strings.SplitSeq(strings.TrimRight(rows, "\r\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[2]) == "1" {
			continue
		}
		activity, _ := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		onPane := strings.TrimSpace(fields[1]) == paneID
		if activity > bestActivity || (activity == bestActivity && onPane && !bestOnPane) {
			best, bestActivity, bestOnPane = strings.TrimSpace(fields[0]), activity, onPane
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
	newMarker := p.newMarker
	if newMarker == nil {
		newMarker = newClaudeQuestionShownMarker
	}
	marker, err := newMarker()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(marker) }()
	clientSize, sizeErr := p.runner.Run(ctx, "tmux", "display-message", "-p", "-c", strings.TrimSpace(target.Client), "#{client_width} #{client_height}")
	size := claudeQuestionPopupSizeFor(string(clientSize), sizeErr)
	args, err := buildClaudeQuestionPopupArgs(binaryPath, claudeQuestionText{locale: settingsLocale()}.popupTitle(target.Asker), marker, target, size)
	if err != nil {
		return err
	}
	if _, err := p.runner.Run(ctx, "tmux", args...); err != nil {
		return err
	}
	// A popup that ran its picker left a byte in the marker. None means tmux
	// took the popup without drawing it, which it does while the client shows
	// another popup.
	if info, err := os.Stat(marker); err != nil || info.Size() == 0 {
		return errClaudeQuestionPopupNotShown
	}
	return nil
}

// newClaudeQuestionShownMarker makes an empty marker file in the temporary
// directory and returns its absolute path.
func newClaudeQuestionShownMarker() (string, error) {
	file, err := os.CreateTemp("", "projmux-question-shown-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// claudeQuestionPopupSize is the display-popup -w and -h values.
type claudeQuestionPopupSize struct{ Width, Height string }

// claudeQuestionPopupSizeFor turns a "<cols> <rows>" client size reply into
// the popup size: 80% x 70% of the client, rounded down as tmux rounds a
// percentage, but at least claudeQuestionPopupMinWidth x
// claudeQuestionPopupMinHeight and never more than the client. A failed or
// unreadable reply keeps the percentages, so sizing never fails the popup.
func claudeQuestionPopupSizeFor(reply string, err error) claudeQuestionPopupSize {
	fallback := claudeQuestionPopupSize{Width: claudeQuestionPopupWidth, Height: claudeQuestionPopupHeight}
	if err != nil {
		return fallback
	}
	fields := strings.Fields(reply)
	if len(fields) != 2 {
		return fallback
	}
	cols, colsErr := strconv.Atoi(fields[0])
	rows, rowsErr := strconv.Atoi(fields[1])
	if colsErr != nil || rowsErr != nil || cols <= 0 || rows <= 0 {
		return fallback
	}
	width := min(cols, max(cols*80/100, claudeQuestionPopupMinWidth))
	height := min(rows, max(rows*70/100, claudeQuestionPopupMinHeight))
	return claudeQuestionPopupSize{Width: strconv.Itoa(width), Height: strconv.Itoa(height)}
}

func (p tmuxClaudeQuestionPopup) Close(ctx context.Context, client string) error {
	if p.runner == nil || strings.TrimSpace(client) == "" {
		return nil
	}
	_, err := p.runner.Run(ctx, "tmux", "display-popup", "-C", "-c", strings.TrimSpace(client))
	return err
}

// buildClaudeQuestionPopupArgs spells the blocking display-popup that runs the
// picker route. Everything the picker needs travels as argv; shown, when set,
// is the marker the picker writes as it starts. tmux expands the -T title as a
// format, so the title's `#` is doubled and its control characters escaped: an
// Agent, Project, or Window name is data.
func buildClaudeQuestionPopupArgs(binaryPath, title, shown string, target claudeQuestionPopupTarget, size claudeQuestionPopupSize) ([]string, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return nil, errors.New("question popup binary path is required")
	}
	if strings.TrimSpace(target.Client) == "" || strings.TrimSpace(target.QuestionID) == "" || strings.TrimSpace(target.AgentUID) == "" || strings.TrimSpace(target.StorePath) == "" {
		return nil, errors.New("question popup needs a client, a question, an Agent, and a store")
	}
	words := []string{
		tmuxShellQuote(binaryPath),
		"internal",
		claudeQuestionPickerRoute,
		"--question", tmuxShellQuote(target.QuestionID),
		"--agent", tmuxShellQuote(target.AgentUID),
		"--store", tmuxShellQuote(target.StorePath),
	}
	if shown = strings.TrimSpace(shown); shown != "" {
		words = append(words, "--shown", tmuxShellQuote(shown))
	}
	command := strings.Join(words, " ")
	return inttmux.BuildDisplayPopupArgs(command, inttmux.PopupOptions{
		Client:        strings.TrimSpace(target.Client),
		Target:        strings.TrimSpace(target.PaneID),
		CloseBehavior: inttmux.PopupCloseOnExit,
		Width:         size.Width,
		Height:        size.Height,
		Title:         strings.ReplaceAll(terminaltext.EscapeControls(title), "#", "##"),
	})
}

// claudeQuestionPopupDriver keeps at most one popup open for one waiting
// record, and never a second one after the first has shown and ended. A popup
// tmux never drew does not count: the driver looks for a client again after
// its interval, so questions of several Agents on one client show one at a
// time, each once the popup before it closes.
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

func newClaudeQuestionPopupDriver(popup claudeQuestionPopup, interval time.Duration, paneID string, asker claudeQuestionAsker, store *agentquestion.Store, record agentquestion.Record) *claudeQuestionPopupDriver {
	if interval <= 0 {
		interval = claudeQuestionClientPoll
	}
	return &claudeQuestionPopupDriver{
		popup:    popup,
		interval: interval,
		target:   claudeQuestionPopupTarget{PaneID: strings.TrimSpace(paneID), QuestionID: record.ID, AgentUID: record.AgentUID, StorePath: store.Path(), Asker: asker},
	}
}

// maybeOpen opens the popup on the client the operator used last, looking at
// most once per interval. Not finding one, or failing to ask, is left for the
// next look.
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
	// The popup outlives a canceled wait: Open ending because its tmux
	// command was killed would read as a popup that ended on its own, which
	// stop never closes, and would leave it on the client. stop closes it,
	// and markEnded cancels this context.
	openCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
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

// markNotShown records that tmux never drew the popup, so none is open and
// the next look may open one again.
func (d *claudeQuestionPopupDriver) markNotShown() {
	d.ended, d.open = nil, false
	if d.cancel != nil {
		d.cancel()
	}
}

// stop lets an answered popup finish on its own before closing it. The answer
// may have reached the store before the picker process exits, while a command-
// line answer leaves that picker open and still needs a Close. The wait is
// bounded for that case. A popup that already ended, shown or not, is never
// closed: Close closes whatever popup the client shows, which may be another
// question's or the operator's own. It is safe to call more than once.
func (d *claudeQuestionPopupDriver) stop(answered bool) {
	if !d.open {
		return
	}
	ended := d.ended
	select {
	case <-ended:
		d.markEnded()
		return
	default:
	}
	if answered {
		// Open may have returned already even though wait selected the
		// answer first. In that case Close could hit a later popup.
		select {
		case <-ended:
			d.markEnded()
			return
		case <-time.After(claudeQuestionPopupStopWait):
		}
	}
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
	shown := fs.String("shown", "", "marker file to write once the popup runs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// First of all, tell the hook the popup showed. The flag is optional, so
	// a hook from before it still runs this picker.
	markClaudeQuestionPopupShown(*shown)
	if fs.NArg() != 0 || strings.TrimSpace(*questionID) == "" || strings.TrimSpace(*agentUID) == "" || strings.TrimSpace(*storePath) == "" {
		return usageError("internal " + claudeQuestionPickerRoute + " requires --question <id> --agent <uid> --store <path>")
	}
	defer applyNativeUIThemeFromConfig(os.UserHomeDir, os.Getenv, "")()
	picker := claudeQuestionPicker{
		store:  agentquestion.NewStoreAt(strings.TrimSpace(*storePath)),
		runner: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		text:   claudeQuestionText{locale: settingsLocale()},
		out:    stdout,
		pause:  time.Sleep,
	}
	return picker.run(strings.TrimSpace(*questionID), strings.TrimSpace(*agentUID))
}

// markClaudeQuestionPopupShown writes one byte to the marker the hook made.
// It never creates the file: a path that is gone is left alone.
func markClaudeQuestionPopupShown(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) // #nosec G304 -- the empty marker the hook made for this popup; opened write-only, never created.
	if err != nil {
		return
	}
	_, _ = file.Write([]byte{'1'})
	_ = file.Close()
}

// claudeQuestionPicker asks one recorded question set with the native picker
// and settles the record through the same store path `agent question answer`
// uses. Esc, and a picker that fails, close the record, which hands the
// question back to Claude Code's own prompt.
type claudeQuestionPicker struct {
	store  *agentquestion.Store
	runner intpicker.Runner
	text   claudeQuestionText
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
	selections, ok, err := collectClaudeQuestionSelections(p.runner, p.text, questions)
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
		reason, _ := questionStoreRefusal(err)
		if reason == "" {
			reason = "question-store-error"
		}
		p.notice(p.text.format(keyClaudeQuestionAnswerNotUsed, "Question {id}: this answer was not used ({reason}).", "{id}", record.ID, "{reason}", reason))
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
func collectClaudeQuestionSelections(runner intpicker.Runner, text claudeQuestionText, questions []agentquestion.Question) (map[int]agentquestion.Selection, bool, error) {
	selections := make(map[int]agentquestion.Selection, len(questions))
	for index, question := range questions {
		selection, ok, err := askClaudeQuestion(runner, text, index, len(questions), question)
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
func askClaudeQuestion(runner intpicker.Runner, text claudeQuestionText, index, count int, question agentquestion.Question) (agentquestion.Selection, bool, error) {
	chosen := make([]bool, len(question.Options))
	cursor, notice := 0, ""
	title := text.format(keyClaudeQuestionTitleProgress, "Claude question {index}/{count}", "{index}", strconv.Itoa(index+1), "{count}", strconv.Itoa(count))
	if header := strings.TrimSpace(question.Header); header != "" {
		title += " - " + terminaltext.EscapeControls(header)
	}
	footer := text.value(keyClaudeQuestionFooterSingle, "Enter: choose  Esc: give the question back to Claude")
	if question.MultiSelect {
		footer = text.value(keyClaudeQuestionFooterMulti, "Enter: toggle, then Done  Esc: give the question back to Claude")
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
			items = append(items, intpicker.Item{Label: text.value(keyClaudeQuestionDone, "Done"), Value: claudeQuestionDoneValue})
		}
		items = append(items, intpicker.Item{Label: text.value(keyClaudeQuestionOther, "Other / type an answer"), Value: claudeQuestionOtherValue})
		header := claudeQuestionHeaderText(question.Question)
		if notice != "" {
			header += "\n" + notice
		}
		result, err := runner.Run(intpicker.Options{
			UI:              "claude-question",
			Items:           items,
			Title:           title,
			Header:          header,
			Footer:          footer,
			WrapHeader:      true,
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
			answer, ok, err := askClaudeQuestionText(runner, text, title, question)
			if err != nil {
				return agentquestion.Selection{}, false, err
			}
			if ok {
				return agentquestion.Selection{Text: answer, HasText: true}, true, nil
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
			notice = text.value(keyClaudeQuestionDoneNeedsOption, "Choose at least one option before Done.")
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
func askClaudeQuestionText(runner intpicker.Runner, text claudeQuestionText, title string, question agentquestion.Question) (string, bool, error) {
	result, err := runner.Run(intpicker.Options{
		UI:          "claude-question-text",
		Title:       title,
		Header:      claudeQuestionHeaderText(question.Question),
		WrapHeader:  true,
		Prompt:      text.value(keyClaudeQuestionTextPrompt, "Answer > "),
		Footer:      text.value(keyClaudeQuestionTextFooter, "Enter: use this answer  Esc: back to the options"),
		AcceptQuery: true,
	})
	if err != nil {
		return "", false, err
	}
	answer := strings.TrimSpace(result.Query)
	if result.Closed || answer == "" {
		return "", false, nil
	}
	return answer, true, nil
}

// claudeQuestionHeaderText escapes control characters in a question for the
// picker header but keeps its line breaks, so a multi-line question wraps as
// separate paragraphs instead of showing a literal \n.
func claudeQuestionHeaderText(question string) string {
	lines := strings.Split(strings.ReplaceAll(question, "\r\n", "\n"), "\n")
	for index, line := range lines {
		lines[index] = terminaltext.EscapeControls(line)
	}
	return strings.Join(lines, "\n")
}
