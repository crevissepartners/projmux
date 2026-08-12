package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	NativeLaunchKeyEnv   = "PROJMUX_NATIVE_LAUNCH_KEY"
	NativeDebugLogEnv    = "PROJMUX_NATIVE_DEBUG_LOG"
	NativeTTYFallbackEnv = "PROJMUX_NATIVE_TTY_FALLBACK"
)

type ActionIntent string

const (
	ActionAccept ActionIntent = "accept"
	ActionClose  ActionIntent = "close"
	ActionCustom ActionIntent = "custom"
)

type Action struct {
	Key     string
	Intent  ActionIntent
	Label   string
	Command string
	Refresh bool
	Mutate  func(ActionContext) (DeferredUpdate, error)
}

type ActionContext struct {
	Key           string
	Value         string
	Query         string
	SelectedIndex int
}

type Preview struct {
	Command string
	Window  string
}

type DeferredUpdate struct {
	Items   []Item
	Preview Preview
	// Header and Footer replace live chrome when their corresponding Set flag
	// is true. The explicit flags allow a refresh to intentionally clear text.
	Header    string
	Footer    string
	SetHeader bool
	SetFooter bool
	// ChromeBands are structured header rows rendered above the search/list
	// surface. Callers provide semantic content while the native renderer owns
	// theme tokens and width clipping.
	ChromeBands    []ChromeBand
	SetChromeBands bool
	// ResourceSummaryDock is a fixed five-row resource-inspector seam: one
	// renderer-owned divider plus four structured bands. It is reserved below
	// the list and above the independent action footer.
	ResourceSummaryDock    []ChromeBand
	SetResourceSummaryDock bool
	// Interaction updates let a live surface move between an actionable list
	// and a read-only information panel without restarting the picker.
	DisableSearch  bool
	ReadOnly       bool
	SetInteraction bool
	Actions        []Action
	SetActions     bool
	Title          string
	SetTitle       bool
	Result         *Result
	// FocusValue, when non-empty, moves the selection cursor to the item
	// whose Value matches it after the update is applied. It overrides the
	// default behaviour of preserving the previously selected value, which
	// breaks when that value was removed from the list (e.g. the sidebar
	// row for a just-killed session). When empty, the previous value is
	// preserved as before. Falls back safely (0/clamp) if the value is not
	// present in the new item list.
	FocusValue string
}

// ChromeBand is one structured visual band in native picker chrome. Label uses
// the accent semantic token and Secondary uses the muted semantic token.
type ChromeBand struct {
	Label     string
	Value     string
	Secondary string
}

type Options struct {
	UI                  string
	Items               []Item
	Title               string
	TitleChips          []projmuxpicker.Chip
	Prompt              string
	Header              string
	ChromeBands         []ChromeBand
	ResourceSummaryDock []ChromeBand
	Footer              string
	Locale              i18n.Locale
	Actions             []Action
	Preview             Preview
	InitialQuery        string
	InitialIndex        int
	InitialIndexSet     bool
	DisableSearch       bool
	// ReadOnly renders Items as an information panel: no selection pointer,
	// mouse acceptance, or Enter acceptance. Contextual actions still work.
	ReadOnly    bool
	AcceptQuery bool
	MultiLine   bool
	// ColorGrid switches runNativeInteractive into the xterm-256 color grid
	// mode: a navigable swatch grid with a live preview instead of the list
	// filter/preview machinery. Purely additive; ignored by list pickers.
	ColorGrid bool
	// Recorder turns the navigation/search surface into a purpose-built
	// single-chord recorder while retaining this native picker's input reader
	// and lifecycle. It is ignored by the color-grid mode.
	Recorder              *RecorderOptions
	Theme                 *theme.EffectiveTheme
	DeferredUpdate        func() (DeferredUpdate, error)
	DeferredUpdateTrigger <-chan struct{}
}

type Result struct {
	Key    string
	Value  string
	Query  string
	Closed bool
}

type Runner interface {
	Run(options Options) (Result, error)
}

func nativeLocalizedText(key i18n.Key, fallback string) string {
	text, err := i18n.NewLocalizer(nativeLocale()).Text(key)
	if err != nil {
		return fallback
	}
	return text.String()
}

func nativeLocalizedTextForOptions(options Options, key i18n.Key, fallback string) string {
	locale := options.Locale
	if locale == "" {
		locale = nativeLocale()
	}
	text, err := i18n.NewLocalizer(locale).Text(key)
	if err != nil {
		return fallback
	}
	return text.String()
}

func nativeLocale() i18n.Locale {
	return i18n.ResolveLocale(i18n.LocaleOptions{
		LookupEnv: func(name string) (string, bool) {
			value, ok := os.LookupEnv(name)
			return value, ok && strings.TrimSpace(value) != ""
		},
	}).Locale
}

func CloseActions(keys ...string) []Action {
	actions := make([]Action, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, Action{Key: key, Intent: ActionClose})
	}
	return actions
}

func CustomActions(keys ...string) []Action {
	actions := make([]Action, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, Action{Key: key, Intent: ActionCustom})
	}
	return actions
}

func FilterItems(items []Item, query string) []Item {
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]Item(nil), items...)
	}
	caseSensitive := nativeSmartCaseSensitive(query)
	needle := nativeSearchPattern(query, caseSensitive)

	if hasNativeSearchKey(items) {
		filtered := make([]Item, 0, len(items))
		for _, item := range items {
			if _, ok := fuzzyScore(item.EffectiveSearchText(), needle, caseSensitive); ok {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}

	filtered := make([]nativeScoredItem, 0, len(items))
	for _, item := range items {
		if score, ok := fuzzyScore(item.EffectiveSearchText(), needle, caseSensitive); ok {
			filtered = append(filtered, nativeScoredItem{Item: item, Score: score, Index: len(filtered)})
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Score != filtered[j].Score {
			return filtered[i].Score > filtered[j].Score
		}
		return filtered[i].Index < filtered[j].Index
	})

	items = make([]Item, 0, len(filtered))
	for _, item := range filtered {
		items = append(items, item.Item)
	}
	return items
}

func nativeSearchPattern(query string, caseSensitive bool) []rune {
	if caseSensitive {
		return []rune(query)
	}
	return []rune(strings.ToLower(query))
}

func nativeDebugLogf(format string, args ...any) {
	path := strings.TrimSpace(os.Getenv(NativeDebugLogEnv))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "[%s] ", time.Now().Format(time.RFC3339))
	fmt.Fprintf(file, format, args...)
	fmt.Fprintln(file)
}

func nativeSmartCaseSensitive(query string) bool {
	for _, r := range query {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func hasNativeSearchKey(items []Item) bool {
	for _, item := range items {
		if strings.TrimSpace(item.SearchText) != "" {
			return true
		}
	}
	return false
}

type nativeScoredItem struct {
	Item  Item
	Score int
	Index int
}

type NativeRunner struct {
	In  io.Reader
	Out io.Writer
}

const nativePageSize = 12
const (
	defaultNativeRows       = projmuxpicker.DefaultRows
	defaultNativeCols       = projmuxpicker.DefaultCols
	nativeReadPollDelay     = 5 * time.Millisecond
	nativeMaybeReadAttempts = 50
	nativeScreenLeaveDelay  = 10 * time.Millisecond
	nativeCurrentStart      = projmuxpicker.CurrentStart
	nativeHighlightStart    = projmuxpicker.HighlightStart
	nativePointer           = projmuxpicker.Pointer
	nativeContinuation      = projmuxpicker.Continuation
	nativeReset             = projmuxpicker.Reset
	nativeInverseStart      = projmuxpicker.InverseStart
	nativeCursorStart       = projmuxpicker.CursorStart
	nativeScreenEnter       = "\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?25l\x1b[2J\x1b[H"
	nativeScreenLeave       = "\r\x1b[0m\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[H\x1b[J\x1b[?25h\x1b[?1049l\r\n"
	nativeSyncUpdateEnter   = projmuxpicker.SyncUpdateEnter
	nativeSyncUpdateLeave   = projmuxpicker.SyncUpdateLeave
	nativeScrollbar         = projmuxpicker.Scrollbar
	nativeGapLine           = projmuxpicker.GapLine
	nativeGapSentinel       = "\x00projmux-native-gap\x00"
)

func (r NativeRunner) Run(options Options) (Result, error) {
	in := r.In
	if in == nil {
		nativeDebugLogf("run ui=%q input=nil result=closed", options.UI)
		return Result{Closed: true}, nil
	}
	out := r.Out
	if out == nil {
		out = io.Discard
	}

	if tty, ok := openNativeTTYFallback(in); ok {
		defer tty.Close()
		if restore, ok := enableRawTerminal(tty); ok {
			defer restore()
			nativeDebugLogf("run ui=%q mode=interactive tty_fallback=true", options.UI)
			return runNativeInteractive(tty, tty, options)
		}
		nativeDebugLogf("run ui=%q tty_fallback_opened=true raw=false", options.UI)
	} else {
		nativeDebugLogf("run ui=%q tty_fallback=false", options.UI)
	}

	if restore, ok := enableRawTerminal(in); ok {
		defer restore()
		nativeDebugLogf("run ui=%q mode=interactive tty_fallback=false", options.UI)
		return runNativeInteractive(in, out, options)
	}

	if !allowNativeLineMode(in, os.Getenv) {
		nativeDebugLogf("run ui=%q mode=none error=requires_tty", options.UI)
		return Result{}, fmt.Errorf("native picker requires a TTY; run from an interactive terminal or set PROJMUX_NATIVE_LINE_MODE=1 for scripted line mode")
	}

	nativeDebugLogf("run ui=%q mode=line", options.UI)
	return runNativeLineMode(in, out, options)
}

func runNativeLineMode(in io.Reader, out io.Writer, options Options) (Result, error) {
	query := strings.TrimSpace(options.InitialQuery)
	if options.DisableSearch {
		query = ""
	}
	if options.Recorder != nil && options.Recorder.State.Phase == "" {
		options.Recorder.State = newRecorderState()
	}
	deferredApplied := false
	for {
		items := nativeFilteredItems(options, query)
		renderNative(out, options, items, query)
		if !deferredApplied && options.DeferredUpdate != nil {
			deferredApplied = true
			if update, err := options.DeferredUpdate(); err == nil {
				options = applyNativeDeferredUpdate(options, update)
				items = nativeFilteredItems(options, query)
				renderNative(out, options, items, query)
			} else {
				nativeDebugLogf("line ui=%q deferred_update_error=%q", options.UI, err.Error())
			}
		}

		line, err := readNativeLine(in)
		if err != nil && !strings.HasSuffix(line, "\n") {
			if err == io.EOF && strings.TrimSpace(line) == "" {
				nativeDebugLogf("line ui=%q result=closed reason=eof_empty query=%q", options.UI, query)
				return Result{Closed: true, Query: query}, nil
			}
			if err != nil && err != io.EOF {
				nativeDebugLogf("line ui=%q error=%q", options.UI, err.Error())
				return Result{}, fmt.Errorf("read native picker input: %w", err)
			}
		}
		input := strings.TrimSpace(line)
		if input == "" {
			nativeDebugLogf("line ui=%q result=closed reason=empty_input query=%q", options.UI, query)
			return Result{Closed: true, Query: query}, nil
		}
		if action, ok := findAction(options.Actions, input); ok && action.Intent == ActionClose {
			nativeDebugLogf("line ui=%q result=closed reason=action key=%q query=%q", options.UI, action.Key, query)
			return Result{Key: action.Key, Query: query, Closed: true}, nil
		}
		if options.AcceptQuery {
			nativeDebugLogf("line ui=%q result=accept_query input=%q", options.UI, input)
			return Result{Key: "enter", Query: input}, nil
		}
		if index, err := strconv.Atoi(input); err == nil {
			if options.ReadOnly {
				fmt.Fprintln(out, "no actionable rows")
				continue
			}
			if index < 1 || index > len(items) {
				fmt.Fprintf(out, "invalid selection: %d\n", index)
				continue
			}
			nativeDebugLogf("line ui=%q result=select index=%d value=%q query=%q", options.UI, index, items[index-1].Value, query)
			return Result{Key: "enter", Value: items[index-1].Value, Query: query}, nil
		}
		query = input
	}
}

func enableRawTerminal(in io.Reader) (func(), bool) {
	file, ok := in.(*os.File)
	if !ok {
		return func() {}, false
	}

	stateCmd := exec.Command("stty", "-g")
	stateCmd.Stdin = file
	stateBytes, err := stateCmd.Output()
	if err != nil {
		return func() {}, false
	}
	state := strings.TrimSpace(string(stateBytes))
	rawCmd := exec.Command("stty", "raw", "-echo", "min", "0", "time", "0")
	rawCmd.Stdin = file
	if err := rawCmd.Run(); err != nil {
		return func() {}, false
	}

	return func() {
		if state == "" {
			return
		}
		restoreCmd := exec.Command("stty", state)
		restoreCmd.Stdin = file
		_ = restoreCmd.Run()
	}, true
}

func openNativeTTYFallback(in io.Reader) (*os.File, bool) {
	file, ok := in.(*os.File)
	if !ok {
		return nil, false
	}
	if !shouldOpenNativeTTYFallback(file, os.Getenv) {
		return nil, false
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	return tty, true
}

func shouldOpenNativeTTYFallback(file *os.File, lookup func(string) string) bool {
	if file == nil {
		return false
	}
	if raw := strings.ToLower(strings.TrimSpace(lookup(NativeTTYFallbackEnv))); raw != "" {
		if raw == "0" || raw == "false" || raw == "no" || raw == "off" {
			return false
		}
		return true
	}
	if strings.TrimSpace(lookup("TMUX")) != "" {
		return true
	}
	return file.Fd() == os.Stdin.Fd()
}

func allowNativeLineMode(in io.Reader, lookup func(string) string) bool {
	if _, ok := in.(*os.File); !ok {
		return true
	}
	return strings.TrimSpace(lookup("PROJMUX_NATIVE_LINE_MODE")) != ""
}

type nativeKey struct {
	Name     string
	Text     string
	Mouse    nativeMouseEvent
	HasMouse bool
}

type nativeMouseEvent struct {
	Button  int
	X       int
	Y       int
	Release bool
}

type nativeKeyRead struct {
	key nativeKey
	err error
}

var errNativeKeyReaderStopped = errors.New("native key reader stopped")

type nativeKeyReaderInput struct {
	io.Reader
	stop <-chan struct{}
}

type nativeKeyReaderLifecycle interface {
	nativeKeyReaderStarted()
	nativeKeyReaderStopped()
}

type nativeDeferredRead struct {
	update DeferredUpdate
	err    error
}

func runNativeInteractive(in io.Reader, out io.Writer, options Options) (Result, error) {
	if options.ColorGrid {
		return runNativeColorGrid(in, out, options)
	}
	query := strings.TrimSpace(options.InitialQuery)
	if options.DisableSearch {
		query = ""
	}
	if options.Recorder != nil && options.Recorder.State.Phase == "" {
		options.Recorder.State = newRecorderState()
	}
	queryCursor := nativeRuneLen(query)
	selected := options.InitialIndex
	focusedValue := ""
	previewOffset := 0
	primaryMouseDown := false
	launchKey := strings.ToLower(strings.TrimSpace(os.Getenv(NativeLaunchKeyEnv)))
	layout := detectNativeLayout(in)
	renderer := projmuxpicker.FrameUpdateRenderer{}
	deferredStarted := false
	var keyCh <-chan nativeKeyRead
	var deferredCh <-chan nativeDeferredRead
	var stopKeyReader chan struct{}
	var stopDeferred chan struct{}
	nativeDebugLogf("interactive ui=%q start items=%d launch_key=%q layout=%dx%d", options.UI, len(options.Items), launchKey, layout.Cols, layout.Rows)
	fmt.Fprint(out, nativeScreenEnter)
	defer leaveNativeInteractiveScreen(out)
	defer func() {
		if stopKeyReader != nil {
			close(stopKeyReader)
		}
	}()
	defer func() {
		if stopDeferred != nil {
			close(stopDeferred)
		}
	}()

	for {
		items := nativeFilteredItems(options, query)
		if selected >= len(items) {
			selected = len(items) - 1
		}
		if selected < 0 {
			selected = 0
		}
		focusValue := selectedNativeValue(items, selected)
		focusChanged := focusValue != focusedValue
		if focusChanged {
			focusedValue = focusValue
			previewOffset = 0
		}
		renderer.Render(out, nativeInteractiveFrame(options, items, query, queryCursor, selected, previewOffset, layout))
		if focusChanged {
			runNativeFocusAction(options.Actions, focusValue)
		}
		if !deferredStarted && options.DeferredUpdate != nil {
			deferredStarted = true
			stopKeyReader = make(chan struct{})
			keyCh = startNativeKeyReader(in, stopKeyReader)
			stopDeferred = make(chan struct{})
			deferredCh = startNativeDeferredUpdate(options.DeferredUpdate, options.DeferredUpdateTrigger, stopDeferred)
		}

		key, err := nextNativeInteractiveKey(in, keyCh, &deferredCh, func(update DeferredUpdate) {
			options = applyNativeDeferredUpdate(options, update)
			nextItems := nativeFilteredItems(options, query)
			selected = nativeSelectedIndexForValue(nextItems, nativeDeferredFocusValue(update, focusValue), selected)
			previewOffset = 0
		}, options.UI)
		if key.Name == "" && key.Text == "" && err == nil {
			continue
		}
		if err != nil {
			if err == io.EOF {
				nativeDebugLogf("interactive ui=%q result=closed reason=eof query=%q", options.UI, query)
				return Result{Closed: true, Query: query}, nil
			}
			nativeDebugLogf("interactive ui=%q error=%q", options.UI, err.Error())
			return Result{}, fmt.Errorf("read native picker key: %w", err)
		}
		nativeDebugLogf("interactive ui=%q key name=%q text=%q query=%q selected=%d items=%d", options.UI, key.Name, key.Text, query, selected, len(items))
		if key.Name == "" && key.Text == "" {
			continue
		}
		if key.HasMouse {
			if options.ReadOnly {
				primaryMouseDown = false
				continue
			}
			if key.Mouse.Release {
				if nativeMouseIsPrimaryButton(key.Mouse.Button) && primaryMouseDown {
					primaryMouseDown = false
					if nextSelected, ok := nativeMouseItemIndex(options, items, selected, layout, key.Mouse.X, key.Mouse.Y); ok {
						selected = nextSelected
						result := nativeAcceptSelectedResult(options, items, selected, query)
						nativeDebugLogf("interactive ui=%q result=select mouse=release value=%q query=%q", options.UI, result.Value, result.Query)
						return result, nil
					}
				}
				if nativeMouseIsPrimaryButton(key.Mouse.Button) {
					primaryMouseDown = false
				}
				continue
			}
			if nativeMouseIsPrimaryButton(key.Mouse.Button) {
				// Chip click takes priority over list hit detection: chips
				// live on the titlebar row above the content area so the
				// regions never overlap, but resolving the chip first
				// keeps the precedence explicit and matches the tab-style
				// metaphor where the strip is its own click target.
				if chipResult, ok := nativeMouseChipResult(options, layout, key.Mouse.X, key.Mouse.Y, query); ok {
					primaryMouseDown = false
					nativeDebugLogf("interactive ui=%q result=chip mouse=press value=%q query=%q", options.UI, chipResult.Value, chipResult.Query)
					return chipResult, nil
				}
			}
			if primaryMouseDown && nativeMouseIsPrimaryDrag(key.Mouse.Button) {
				if nextSelected, ok := nativeMouseItemIndex(options, items, selected, layout, key.Mouse.X, key.Mouse.Y); ok {
					selected = nextSelected
				}
				continue
			}
			if nextSelected, ok := nativeMouseSelection(options, items, selected, layout, key.Mouse); ok {
				selected = nextSelected
				if nativeMouseIsPrimaryButton(key.Mouse.Button) {
					primaryMouseDown = true
				}
				continue
			}
			if nativeMouseIsPrimaryButton(key.Mouse.Button) {
				primaryMouseDown = false
			}
			continue
		}
		if options.Recorder != nil && (key.Name == "enter" || key.Name == "esc") {
			kind := recorderEnter
			if key.Name == "esc" {
				kind = recorderEscape
			}
			state, outcome := reduceRecorderState(options.Recorder.State, recorderEvent{kind: kind}, options.Recorder.Normalize, options.Recorder.Validate)
			options.Recorder.State = state
			switch outcome {
			case recorderConfirm:
				return Result{Key: "enter", Value: state.Candidate}, nil
			case recorderCancel:
				return Result{Key: "esc", Closed: true}, nil
			default:
				continue
			}
		}

		if action, ok := findAction(options.Actions, key.Name); ok {
			result, refresh, update, err := runNativePickerAction(action, options, items, selected, query)
			if err != nil {
				return Result{}, err
			}
			if refresh {
				options = applyNativeDeferredUpdate(options, update)
				nextItems := nativeFilteredItems(options, query)
				selected = nativeSelectedIndexForValue(nextItems, nativeDeferredFocusValue(update, selectedNativeValue(items, selected)), selected)
				previewOffset = 0
			}
			nativeDebugLogf("interactive ui=%q action key=%q intent=%q refresh=%t result_key=%q closed=%t value=%q query=%q", options.UI, action.Key, action.Intent, refresh, result.Key, result.Closed, result.Value, result.Query)
			if refresh {
				continue
			}
			return result, nil
		}
		if key.Text != "" {
			if action, ok := findAction(options.Actions, key.Text); ok {
				result, refresh, update, err := runNativePickerAction(action, options, items, selected, query)
				if err != nil {
					return Result{}, err
				}
				if refresh {
					options = applyNativeDeferredUpdate(options, update)
					nextItems := nativeFilteredItems(options, query)
					selected = nativeSelectedIndexForValue(nextItems, nativeDeferredFocusValue(update, selectedNativeValue(items, selected)), selected)
					previewOffset = 0
				}
				nativeDebugLogf("interactive ui=%q text_action key=%q intent=%q refresh=%t result_key=%q closed=%t value=%q query=%q", options.UI, action.Key, action.Intent, refresh, result.Key, result.Closed, result.Value, result.Query)
				if refresh {
					continue
				}
				return result, nil
			}
		}

		if options.Recorder != nil {
			event := recorderEvent{kind: recorderCandidate, key: RecorderKey{Name: key.Name, Text: key.Text}}
			state, outcome := reduceRecorderState(options.Recorder.State, event, options.Recorder.Normalize, options.Recorder.Validate)
			options.Recorder.State = state
			switch outcome {
			case recorderConfirm:
				return Result{Key: "enter", Value: state.Candidate}, nil
			case recorderCancel:
				return Result{Key: "esc", Closed: true}, nil
			default:
				continue
			}
		}

		switch key.Name {
		case "enter":
			if options.AcceptQuery {
				nativeDebugLogf("interactive ui=%q result=accept_query key=enter query=%q", options.UI, query)
				return Result{Key: "enter", Query: query}, nil
			}
			if len(items) == 0 || options.ReadOnly {
				continue
			}
			result := nativeAcceptSelectedResult(options, items, selected, query)
			nativeDebugLogf("interactive ui=%q result=select key=enter value=%q query=%q", options.UI, result.Value, result.Query)
			return result, nil
		case "esc", "ctrl-c":
			nativeDebugLogf("interactive ui=%q result=closed key=%q query=%q", options.UI, key.Name, query)
			return Result{Key: key.Name, Query: query, Closed: true}, nil
		case "up", "ctrl-p", "ctrl-k":
			if len(items) > 0 && !options.ReadOnly {
				selected = (selected - 1 + len(items)) % len(items)
			}
		case "down", "ctrl-n", "ctrl-j":
			if len(items) > 0 && !options.ReadOnly {
				selected = (selected + 1) % len(items)
			}
		case "home":
			if !options.ReadOnly {
				selected = 0
			}
		case "end":
			if len(items) > 0 && !options.ReadOnly {
				selected = len(items) - 1
			}
		case "page-up":
			if options.ReadOnly {
				continue
			}
			selected -= nativePageSize
			if selected < 0 {
				selected = 0
			}
		case "page-down":
			if options.ReadOnly {
				continue
			}
			selected += nativePageSize
			if selected >= len(items) {
				selected = len(items) - 1
			}
		case "shift-up":
			if previewOffset > 0 {
				previewOffset--
			}
		case "shift-down":
			previewOffset++
		case "left":
			if options.DisableSearch {
				continue
			}
			if queryCursor > 0 {
				queryCursor--
			}
		case "right":
			if options.DisableSearch {
				continue
			}
			if queryCursor < nativeRuneLen(query) {
				queryCursor++
			}
		case "backspace":
			if options.DisableSearch {
				continue
			}
			query, queryCursor = deleteNativeQueryBeforeCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		case "delete":
			if options.DisableSearch {
				continue
			}
			query, queryCursor = deleteNativeQueryAtCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		case "ctrl-a":
			if options.DisableSearch {
				continue
			}
			queryCursor = 0
		case "ctrl-e":
			if options.DisableSearch {
				continue
			}
			queryCursor = nativeRuneLen(query)
		case "ctrl-u":
			if options.DisableSearch {
				continue
			}
			query, queryCursor = deleteNativeQueryBeforeCursorN(query, queryCursor, queryCursor)
			selected = 0
			previewOffset = 0
		case "ctrl-w":
			if options.DisableSearch {
				continue
			}
			query, queryCursor = trimNativeQueryWordBeforeCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		default:
			if key.Text != "" && !options.DisableSearch {
				query, queryCursor = insertNativeQueryText(query, queryCursor, key.Text)
				selected = 0
				previewOffset = 0
			}
		}
	}
}

func startNativeKeyReader(in io.Reader, stop <-chan struct{}) <-chan nativeKeyRead {
	ch := make(chan nativeKeyRead, 1)
	go func() {
		defer close(ch)
		if lifecycle, ok := in.(nativeKeyReaderLifecycle); ok {
			lifecycle.nativeKeyReaderStarted()
			defer lifecycle.nativeKeyReaderStopped()
		}
		in := nativeKeyReaderInput{Reader: in, stop: stop}
		for {
			key, err := readNativeKey(in)
			if errors.Is(err, errNativeKeyReaderStopped) {
				return
			}
			select {
			case ch <- nativeKeyRead{key: key, err: err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

func startNativeDeferredUpdate(update func() (DeferredUpdate, error), triggers <-chan struct{}, stop <-chan struct{}) <-chan nativeDeferredRead {
	ch := make(chan nativeDeferredRead, 1)
	go func() {
		defer close(ch)
		run := func() bool {
			result, err := update()
			select {
			case ch <- nativeDeferredRead{update: result, err: err}:
				return true
			case <-stop:
				return false
			}
		}
		if triggers == nil {
			_ = run()
			return
		}
		for {
			select {
			case _, ok := <-triggers:
				if !ok {
					return
				}
				if !run() {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	return ch
}

func nextNativeInteractiveKey(in io.Reader, keyCh <-chan nativeKeyRead, deferredCh *<-chan nativeDeferredRead, applyDeferred func(DeferredUpdate), ui string) (nativeKey, error) {
	if keyCh == nil {
		return readNativeKey(in)
	}
	for {
		select {
		case deferred, ok := <-*deferredCh:
			if !ok {
				*deferredCh = nil
				continue
			}
			if deferred.err != nil {
				nativeDebugLogf("interactive ui=%q deferred_update_error=%q", ui, deferred.err.Error())
				continue
			}
			applyDeferred(deferred.update)
			return nativeKey{}, nil
		case key := <-keyCh:
			return key.key, key.err
		}
	}
}

func nativeAcceptSelectedResult(options Options, items []Item, selected int, query string) Result {
	if options.AcceptQuery {
		return Result{Key: "enter", Query: query}
	}
	return Result{Key: "enter", Value: selectedNativeValue(items, selected), Query: query}
}

func leaveNativeInteractiveScreen(out io.Writer) {
	fmt.Fprint(out, nativeScreenLeave)
	if _, ok := out.(*os.File); ok {
		time.Sleep(nativeScreenLeaveDelay)
	}
}

func nativeFilteredItems(options Options, query string) []Item {
	if options.DisableSearch {
		return options.Items
	}
	return FilterItems(options.Items, query)
}

func applyNativeDeferredUpdate(options Options, update DeferredUpdate) Options {
	if update.Items != nil {
		options.Items = append([]Item(nil), update.Items...)
	}
	if strings.TrimSpace(update.Preview.Command) != "" || strings.TrimSpace(update.Preview.Window) != "" {
		options.Preview = update.Preview
	}
	if update.SetHeader {
		options.Header = update.Header
	}
	if update.SetFooter {
		options.Footer = update.Footer
	}
	if update.SetChromeBands {
		options.ChromeBands = append([]ChromeBand(nil), update.ChromeBands...)
	}
	if update.SetResourceSummaryDock {
		options.ResourceSummaryDock = append([]ChromeBand(nil), update.ResourceSummaryDock...)
	}
	if update.SetInteraction {
		options.DisableSearch = update.DisableSearch
		options.ReadOnly = update.ReadOnly
	}
	if update.SetActions {
		options.Actions = append([]Action(nil), update.Actions...)
	}
	if update.SetTitle {
		options.Title = update.Title
	}
	return options
}

// nativeDeferredFocusValue resolves which item value the cursor should track
// after a deferred update. An explicit DeferredUpdate.FocusValue wins so a
// refresh can move the cursor to a specific row (e.g. the newly active session
// after a sidebar kill); otherwise the previously focused value is preserved.
func nativeDeferredFocusValue(update DeferredUpdate, fallbackValue string) string {
	if value := strings.TrimSpace(update.FocusValue); value != "" {
		return value
	}
	return fallbackValue
}

func nativeSelectedIndexForValue(items []Item, value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value != "" {
		for idx, item := range items {
			if strings.TrimSpace(item.Value) == value {
				return idx
			}
		}
	}
	if len(items) == 0 {
		return 0
	}
	if fallback < 0 {
		return 0
	}
	if fallback >= len(items) {
		return len(items) - 1
	}
	return fallback
}

func runNativePickerAction(action Action, options Options, items []Item, selected int, query string) (Result, bool, DeferredUpdate, error) {
	value := selectedNativeValue(items, selected)
	switch action.Intent {
	case ActionClose:
		return Result{Key: action.Key, Query: query, Closed: true}, false, DeferredUpdate{}, nil
	case ActionCustom:
		if action.Mutate != nil {
			update, err := action.Mutate(ActionContext{
				Key:           action.Key,
				Value:         value,
				Query:         query,
				SelectedIndex: selected,
			})
			if err != nil {
				return Result{}, false, DeferredUpdate{}, fmt.Errorf("run native picker action %q: %w", action.Key, err)
			}
			if update.Result != nil {
				result := *update.Result
				if result.Key == "" {
					result.Key = action.Key
				}
				if result.Value == "" && !result.Closed {
					result.Value = value
				}
				if result.Query == "" {
					result.Query = query
				}
				return result, false, DeferredUpdate{}, nil
			}
			return Result{}, true, update, nil
		}
		if strings.TrimSpace(action.Command) != "" {
			runNativeActionCommand(action.Command, value)
			return Result{}, true, DeferredUpdate{}, nil
		}
		return Result{Key: action.Key, Value: value, Query: query}, false, DeferredUpdate{}, nil
	case ActionAccept:
		if options.AcceptQuery {
			return Result{Key: action.Key, Query: query}, false, DeferredUpdate{}, nil
		}
		return Result{Key: action.Key, Value: value, Query: query}, false, DeferredUpdate{}, nil
	default:
		return Result{}, false, DeferredUpdate{}, nil
	}
}

type nativeLayout struct {
	Rows int
	Cols int
}

func detectNativeLayout(in io.Reader) nativeLayout {
	layout := nativeLayout{Rows: defaultNativeRows, Cols: defaultNativeCols}
	file, ok := in.(*os.File)
	if !ok {
		return layout
	}
	cmd := exec.Command("stty", "size")
	cmd.Stdin = file
	out, err := cmd.Output()
	if err != nil {
		return layout
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return layout
	}
	rows, rowErr := strconv.Atoi(fields[0])
	cols, colErr := strconv.Atoi(fields[1])
	if rowErr == nil && rows > 0 {
		layout.Rows = rows
	}
	if colErr == nil && cols > 0 {
		layout.Cols = cols
	}
	return layout
}

func runNativeFocusAction(actions []Action, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	for _, action := range actions {
		if action.Key == "focus" && action.Intent == ActionCustom && strings.TrimSpace(action.Command) != "" {
			runNativeActionCommand(action.Command, value)
			return
		}
	}
}

func selectedNativeValue(items []Item, selected int) string {
	if selected < 0 || selected >= len(items) {
		return ""
	}
	return items[selected].Value
}

func nativeMouseSelection(options Options, items []Item, selected int, layout nativeLayout, event nativeMouseEvent) (int, bool) {
	if event.Release || len(items) == 0 {
		return selected, false
	}
	switch event.Button {
	case 64: // wheel up
		if selected > 0 {
			return selected - 1, true
		}
		return selected, true
	case 65: // wheel down
		if selected < len(items)-1 {
			return selected + 1, true
		}
		return selected, true
	case 0: // primary click
		return nativeMouseItemIndex(options, items, selected, layout, event.X, event.Y)
	default:
		return selected, false
	}
}

// nativeMouseChipResult resolves a primary-button press into a chip click
// result. It returns ok=false when no chip strip is rendered, when the
// click lands outside any chip's hit region, or when the chip has no
// ClickValue (chord-only chip). Disabled chips still match the row but
// resolve to ok=false so the press becomes a no-op — chord and click
// behaviour stay in lockstep.
func nativeMouseChipResult(options Options, layout nativeLayout, x, y int, query string) (Result, bool) {
	if len(options.TitleChips) == 0 {
		return Result{}, false
	}
	if y != projmuxpicker.ChipsTitlebarRow() {
		return Result{}, false
	}
	innerWidth := layout.Cols - 2
	if innerWidth < 4 {
		return Result{}, false
	}
	for _, hit := range projmuxpicker.ChipsHitRegions(options.TitleChips, innerWidth) {
		if x < hit.ColStart || x > hit.ColEnd {
			continue
		}
		if hit.Disabled {
			return Result{}, false
		}
		if strings.TrimSpace(hit.Value) == "" {
			return Result{}, false
		}
		return Result{Key: "chip", Value: hit.Value, Query: query}, true
	}
	return Result{}, false
}

func nativeMouseIsPrimaryButton(button int) bool {
	return button&32 == 0 && button&3 == 0
}

func nativeMouseIsPrimaryDrag(button int) bool {
	return button&32 != 0 && button&3 == 0
}

func nativeMouseItemIndex(options Options, items []Item, selected int, layout nativeLayout, x, y int) (int, bool) {
	if x <= 1 || y <= 1 {
		return selected, false
	}
	contentLayout := nativeContentLayoutForOptions(layout, options)
	contentX := x - 2
	if contentX < 0 || contentX >= contentLayout.Cols {
		return selected, false
	}
	contentRow := y - 2
	contentRow -= nativeTitlebarRowsForOptions(options)
	if contentRow < 0 || contentRow >= contentLayout.Rows {
		return selected, false
	}

	listStart := nativeListStartLine(options)
	listRow := contentRow - listStart
	if listRow < 0 {
		return selected, false
	}

	placement := nativePreviewPlacement(options.Preview.Window)
	hasPreview := strings.TrimSpace(options.Preview.Command) != "" && selected >= 0 && selected < len(items)
	if hasPreview && placement == "right" && contentLayout.Cols >= 88 {
		previewWidth := nativePreviewWidth(contentLayout.Cols, options.Preview.Window)
		listWidth := max(contentLayout.Cols-previewWidth-1, 32)
		if contentX >= listWidth {
			return selected, false
		}
	}

	previewHeight := nativePreviewHeight(contentLayout.Rows, options.Preview.Window)
	listLimit := nativeListLimit(options, contentLayout, placement, previewHeight, hasPreview)
	start, end := nativeVisibleRange(len(items), selected, listLimit)
	if options.MultiLine {
		start, end = nativeVisibleRangeByRenderedRows(items, selected, listLimit)
	}

	offset := 0
	for index := start; index < end; index++ {
		rowHeight := nativeItemLineCount(items[index])
		if listRow >= offset && listRow < offset+rowHeight {
			return index, true
		}
		offset += rowHeight
		if options.MultiLine && index < end-1 {
			if listRow == offset {
				return selected, false
			}
			offset++
		}
	}
	return selected, false
}

func nativeListStartLine(options Options) int {
	lines := 0
	if header := strings.TrimSpace(options.Header); header != "" {
		lines += nativeTextLineCount(header)
	}
	lines += len(options.ChromeBands)
	if !options.DisableSearch {
		lines += 2 // prompt plus search/list separator
	}
	return lines
}

func readNativeKey(r io.Reader) (nativeKey, error) {
	b, err := readNativeByte(r)
	if err != nil {
		return nativeKey{}, err
	}
	switch b {
	case 0:
		return nativeKey{}, nil
	case '\r', '\n':
		return nativeKey{Name: "enter"}, nil
	case 0x01:
		return nativeKey{Name: "ctrl-a"}, nil
	case 0x03:
		return nativeKey{Name: "ctrl-c"}, nil
	case 0x05:
		return nativeKey{Name: "ctrl-e"}, nil
	case 0x0b:
		return nativeKey{Name: "ctrl-k"}, nil
	case 0x0e:
		return nativeKey{Name: "ctrl-n"}, nil
	case 0x10:
		return nativeKey{Name: "ctrl-p"}, nil
	case 0x13:
		return nativeKey{Name: "ctrl-s"}, nil
	case 0x15:
		return nativeKey{Name: "ctrl-u"}, nil
	case 0x17:
		return nativeKey{Name: "ctrl-w"}, nil
	case 0x18:
		return nativeKey{Name: "ctrl-x"}, nil
	case 0x7f, 0x08:
		return nativeKey{Name: "backspace"}, nil
	case 0x1b:
		return readNativeEscapeKey(r)
	default:
		if b >= 0x01 && b <= 0x1a {
			return nativeKey{Name: "ctrl-" + string(rune('a'+b-1))}, nil
		}
		return nativePrintableKey(b, r)
	}
}

func readNativeEscapeKey(r io.Reader) (nativeKey, error) {
	b, ok, err := readNativeByteMaybe(r)
	if err != nil {
		return nativeKey{}, err
	}
	if !ok {
		return nativeKey{Name: "esc"}, nil
	}
	if b == '[' {
		next, ok, err := readNativeByteMaybe(r)
		if err != nil {
			return nativeKey{}, err
		}
		if !ok {
			return nativeKey{Name: "esc"}, nil
		}
		return readNativeCSIKey(r, next)
	}
	if b == 'O' {
		next, ok, err := readNativeByteMaybe(r)
		if err != nil {
			return nativeKey{}, err
		}
		if !ok {
			return nativeKey{Name: "esc"}, nil
		}
		return nativeKeyFromSS3(next)
	}
	if b >= '1' && b <= '9' {
		return nativeKey{Name: "alt-" + string([]byte{b})}, nil
	}
	if b >= 'a' && b <= 'z' {
		return nativeKey{Name: "alt-" + string([]byte{b})}, nil
	}
	if b >= 'A' && b <= 'Z' {
		return nativeKey{Name: "alt-" + strings.ToLower(string([]byte{b}))}, nil
	}
	if b >= 0x01 && b <= 0x1a {
		return nativeKey{Name: "ctrl-alt-" + string(rune('a'+b-1))}, nil
	}
	return nativeKey{Name: "esc"}, nil
}

func readNativeCSIKey(r io.Reader, first byte) (nativeKey, error) {
	seq := []byte{first}
	for !isNativeCSIFinal(seq[len(seq)-1]) && len(seq) < 16 {
		next, ok, err := readNativeByteMaybe(r)
		if err != nil {
			return nativeKey{}, err
		}
		if !ok {
			return nativeKey{Name: "esc"}, nil
		}
		seq = append(seq, next)
	}
	if !isNativeCSIFinal(seq[len(seq)-1]) {
		return nativeKey{Name: "esc"}, nil
	}
	return nativeKeyFromCSI(seq), nil
}

func isNativeCSIFinal(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

func nativeKeyFromCSI(seq []byte) nativeKey {
	final := seq[len(seq)-1]
	params := string(seq[:len(seq)-1])
	if strings.HasPrefix(params, "<") && (final == 'M' || final == 'm') {
		return nativeMouseKey(params, final == 'm')
	}
	if final == 'u' {
		if key := nativeKeyFromCSIu(params); key.Name != "" || key.Text != "" {
			return key
		}
	}
	name := nativeBaseCSIName(final, params)
	if name == "" {
		return nativeKey{Name: "esc"}
	}
	mod := nativeCSIModifier(params)
	switch mod {
	case "2":
		return nativeKey{Name: "shift-" + name}
	case "3":
		return nativeKey{Name: "alt-" + name}
	case "4":
		// CSI modifier 4 = Shift+Alt. xterm encodes Alt-Shift-Left as
		// "\x1b[1;4D" — normalize to alt-shift-<name> so popup chord
		// matching uses the same surface form as keyboard chord catalog
		// entries.
		return nativeKey{Name: "alt-shift-" + name}
	case "5":
		return nativeKey{Name: "ctrl-" + name}
	case "6":
		return nativeKey{Name: "ctrl-shift-" + name}
	case "7":
		return nativeKey{Name: "ctrl-alt-" + name}
	case "8":
		return nativeKey{Name: "ctrl-alt-shift-" + name}
	default:
		return nativeKey{Name: name}
	}
}

func nativeMouseKey(params string, release bool) nativeKey {
	fields := strings.Split(strings.TrimPrefix(params, "<"), ";")
	if len(fields) != 3 {
		return nativeKey{Name: "mouse"}
	}
	button, buttonErr := strconv.Atoi(strings.TrimSpace(fields[0]))
	x, xErr := strconv.Atoi(strings.TrimSpace(fields[1]))
	y, yErr := strconv.Atoi(strings.TrimSpace(fields[2]))
	if buttonErr != nil || xErr != nil || yErr != nil {
		return nativeKey{Name: "mouse"}
	}
	return nativeKey{
		Name: "mouse",
		Mouse: nativeMouseEvent{
			Button:  button,
			X:       x,
			Y:       y,
			Release: release,
		},
		HasMouse: true,
	}
}

func nativeKeyFromCSIu(params string) nativeKey {
	switch strings.TrimSpace(params) {
	case "9003":
		return nativeKey{Name: "alt-2"}
	case "9004":
		return nativeKey{Name: "alt-3"}
	case "9005":
		return nativeKey{Name: "alt-1"}
	case "9006":
		return nativeKey{Name: "alt-4"}
	case "9007":
		return nativeKey{Name: "alt-5"}
	case "9008":
		return nativeKey{Name: "ctrl-n"}
	case "9013":
		return nativeKey{Name: "alt-6"}
	}

	fields := strings.Split(params, ";")
	if len(fields) == 0 {
		return nativeKey{Name: "esc"}
	}
	codepoint, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil || codepoint <= 0 {
		return nativeKey{Name: "esc"}
	}
	mod := ""
	if len(fields) > 1 {
		mod = strings.TrimSpace(fields[len(fields)-1])
	}
	switch codepoint {
	case 9:
		return nativeModifiedNamedKey("tab", mod)
	case 13:
		return nativeModifiedNamedKey("enter", mod)
	case 27:
		return nativeModifiedNamedKey("esc", mod)
	case 127, 8:
		return nativeModifiedNamedKey("backspace", mod)
	}
	if codepoint >= 'a' && codepoint <= 'z' {
		letter := string(rune(codepoint))
		switch mod {
		case "3":
			return nativeKey{Name: "alt-" + letter}
		case "5":
			return nativeKey{Name: "ctrl-" + letter}
		case "7":
			return nativeKey{Name: "ctrl-alt-" + letter}
		}
	}
	if codepoint >= 1 && codepoint <= 26 {
		letter := string(rune('a' + codepoint - 1))
		switch mod {
		case "", "5":
			return nativeKey{Name: "ctrl-" + letter}
		case "3", "7":
			return nativeKey{Name: "ctrl-alt-" + letter}
		}
	}
	if codepoint >= 'A' && codepoint <= 'Z' {
		letter := strings.ToLower(string(rune(codepoint)))
		switch mod {
		case "3", "4":
			return nativeKey{Name: "alt-" + letter}
		case "5", "6":
			return nativeKey{Name: "ctrl-" + letter}
		case "7", "8":
			return nativeKey{Name: "ctrl-alt-" + letter}
		}
	}
	if codepoint >= '0' && codepoint <= '9' {
		digit := string(rune(codepoint))
		switch mod {
		case "3", "4":
			return nativeKey{Name: "alt-" + digit}
		case "5", "6":
			return nativeKey{Name: "ctrl-" + digit}
		case "7", "8":
			return nativeKey{Name: "ctrl-alt-" + digit}
		}
	}
	return nativeKey{Name: "esc"}
}

func nativeModifiedNamedKey(name, modifier string) nativeKey {
	switch modifier {
	case "2":
		return nativeKey{Name: "shift-" + name}
	case "3":
		return nativeKey{Name: "alt-" + name}
	case "4":
		return nativeKey{Name: "alt-shift-" + name}
	case "5":
		return nativeKey{Name: "ctrl-" + name}
	case "6":
		return nativeKey{Name: "ctrl-shift-" + name}
	case "7":
		return nativeKey{Name: "ctrl-alt-" + name}
	case "8":
		return nativeKey{Name: "ctrl-alt-shift-" + name}
	default:
		return nativeKey{Name: name}
	}
}

func nativeBaseCSIName(final byte, params string) string {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	case 'F':
		return "end"
	case 'H':
		return "home"
	case 'Z':
		return "shift-tab"
	case '~':
		switch nativeCSIPrimaryParam(params) {
		case "1", "7":
			return "home"
		case "3":
			return "delete"
		case "4", "8":
			return "end"
		case "5":
			return "page-up"
		case "6":
			return "page-down"
		default:
			return ""
		}
	default:
		return ""
	}
}

func nativeCSIPrimaryParam(params string) string {
	if params == "" {
		return ""
	}
	primary, _, _ := strings.Cut(params, ";")
	return primary
}

func nativeCSIModifier(params string) string {
	fields := strings.Split(params, ";")
	if len(fields) < 2 {
		return ""
	}
	return fields[len(fields)-1]
}

func nativeKeyFromSS3(b byte) (nativeKey, error) {
	switch b {
	case 'A':
		return nativeKey{Name: "up"}, nil
	case 'B':
		return nativeKey{Name: "down"}, nil
	case 'C':
		return nativeKey{Name: "right"}, nil
	case 'D':
		return nativeKey{Name: "left"}, nil
	case 'F':
		return nativeKey{Name: "end"}, nil
	case 'H':
		return nativeKey{Name: "home"}, nil
	default:
		return nativeKey{Name: "esc"}, nil
	}
}

func nativePrintableKey(first byte, r io.Reader) (nativeKey, error) {
	if first < 0x20 {
		return nativeKey{}, nil
	}
	if first < utf8.RuneSelf {
		return nativeKey{Text: string([]byte{first})}, nil
	}

	buf := []byte{first}
	for !utf8.FullRune(buf) && len(buf) < utf8.UTFMax {
		b, ok, err := readNativeByteMaybe(r)
		if err != nil {
			return nativeKey{}, err
		}
		if !ok {
			break
		}
		buf = append(buf, b)
	}
	if !utf8.Valid(buf) {
		return nativeKey{}, nil
	}
	return nativeKey{Text: string(buf)}, nil
}

func readNativeByte(r io.Reader) (byte, error) {
	var buf [1]byte
	for {
		if nativeKeyReadStopped(r) {
			return 0, errNativeKeyReaderStopped
		}
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], nil
		}
		if err == io.EOF {
			if nativeReaderIsFile(r) {
				time.Sleep(nativeReadPollDelay)
				continue
			}
			return 0, err
		}
		if err != nil {
			return 0, err
		}
		time.Sleep(nativeReadPollDelay)
	}
}

func readNativeByteMaybe(r io.Reader) (byte, bool, error) {
	var buf [1]byte
	for range nativeMaybeReadAttempts {
		if nativeKeyReadStopped(r) {
			return 0, false, errNativeKeyReaderStopped
		}
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], true, nil
		}
		if err == io.EOF {
			if nativeReaderIsFile(r) {
				time.Sleep(nativeReadPollDelay)
				continue
			}
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		time.Sleep(nativeReadPollDelay)
	}
	return 0, false, nil
}

func nativeKeyReadStopped(r io.Reader) bool {
	input, ok := r.(nativeKeyReaderInput)
	if !ok || input.stop == nil {
		return false
	}
	select {
	case <-input.stop:
		return true
	default:
		return false
	}
}

func nativeReaderIsFile(r io.Reader) bool {
	switch reader := r.(type) {
	case *os.File:
		return true
	case nativeKeyReaderInput:
		_, ok := reader.Reader.(*os.File)
		return ok
	default:
		return false
	}
}

func readNativeLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			b.WriteByte(buf[0])
			if buf[0] == '\n' {
				return b.String(), nil
			}
		}
		if err != nil {
			return b.String(), err
		}
	}
}

func nativeRuneLen(value string) int {
	return utf8.RuneCountInString(value)
}

func insertNativeQueryText(query string, cursor int, text string) (string, int) {
	runes := []rune(query)
	cursor = clampNativeQueryCursor(runes, cursor)
	insert := []rune(text)
	next := make([]rune, 0, len(runes)+len(insert))
	next = append(next, runes[:cursor]...)
	next = append(next, insert...)
	next = append(next, runes[cursor:]...)
	return string(next), cursor + len(insert)
}

func deleteNativeQueryBeforeCursor(query string, cursor int) (string, int) {
	return deleteNativeQueryBeforeCursorN(query, cursor, 1)
}

func deleteNativeQueryBeforeCursorN(query string, cursor, count int) (string, int) {
	runes := []rune(query)
	cursor = clampNativeQueryCursor(runes, cursor)
	if cursor == 0 || count <= 0 {
		return query, cursor
	}
	start := max(cursor-count, 0)
	next := make([]rune, 0, len(runes)-(cursor-start))
	next = append(next, runes[:start]...)
	next = append(next, runes[cursor:]...)
	return string(next), start
}

func deleteNativeQueryAtCursor(query string, cursor int) (string, int) {
	runes := []rune(query)
	cursor = clampNativeQueryCursor(runes, cursor)
	if cursor >= len(runes) {
		return query, cursor
	}
	next := make([]rune, 0, len(runes)-1)
	next = append(next, runes[:cursor]...)
	next = append(next, runes[cursor+1:]...)
	return string(next), cursor
}

func trimNativeQueryWordBeforeCursor(query string, cursor int) (string, int) {
	runes := []rune(query)
	cursor = clampNativeQueryCursor(runes, cursor)
	if cursor == 0 {
		return query, cursor
	}
	start := cursor
	for start > 0 && (runes[start-1] == ' ' || runes[start-1] == '\t') {
		start--
	}
	for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\t' {
		start--
	}
	next := make([]rune, 0, len(runes)-(cursor-start))
	next = append(next, runes[:start]...)
	next = append(next, runes[cursor:]...)
	return string(next), start
}

func clampNativeQueryCursor(runes []rune, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(runes) {
		return len(runes)
	}
	return cursor
}

func renderNativeInteractive(w io.Writer, options Options, items []Item, query string, selected, previewOffset int, layout nativeLayout) {
	renderNativeInteractiveWithCursor(w, options, items, query, nativeRuneLen(query), selected, previewOffset, layout)
}

func renderNativeInteractiveWithCursor(w io.Writer, options Options, items []Item, query string, queryCursor, selected, previewOffset int, layout nativeLayout) {
	frame := nativeInteractiveFrame(options, items, query, queryCursor, selected, previewOffset, layout)
	projmuxpicker.RenderFullFrameUpdate(w, frame)
}

func nativeInteractiveFrame(options Options, items []Item, query string, queryCursor, selected, previewOffset int, layout nativeLayout) string {
	contentLayout := nativeContentLayoutForOptions(layout, options)
	var body strings.Builder
	renderNativeInteractiveContent(&body, options, items, query, queryCursor, selected, previewOffset, contentLayout)
	var frame strings.Builder
	if len(options.TitleChips) > 0 {
		renderNativeFrameWithChips(&frame, body.String(), options.TitleChips, layout, options)
	} else {
		renderNativeFrameWithTitle(&frame, body.String(), options.Title, layout, options)
	}
	return frame.String()
}

func nativeContentLayout(layout nativeLayout, title string) nativeLayout {
	content := projmuxpicker.DefaultRenderer().ContentLayoutWithTitle(projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols}, title)
	return nativeLayout{Rows: content.Rows, Cols: content.Cols}
}

func nativeContentLayoutForOptions(layout nativeLayout, options Options) nativeLayout {
	if len(options.TitleChips) > 0 {
		content := projmuxpicker.DefaultRenderer().ContentLayoutWithChips(projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols}, options.TitleChips)
		return nativeLayout{Rows: content.Rows, Cols: content.Cols}
	}
	return nativeContentLayout(layout, options.Title)
}

func renderNativeInteractiveContent(w io.Writer, options Options, items []Item, query string, queryCursor, selected, previewOffset int, layout nativeLayout) {
	var screen strings.Builder
	pickerTheme := nativeTheme(options)
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(&screen, nativeHeaderLineWithTheme(pickerTheme, header, layout.Cols))
	}
	for _, band := range options.ChromeBands {
		fmt.Fprintln(&screen, projmuxpicker.BandLineWithTheme(pickerTheme, band.Label, band.Value, band.Secondary, layout.Cols))
	}
	if options.Recorder != nil {
		renderNativeRecorderContent(w, pickerTheme, screen.String(), options, layout)
		return
	}
	if !options.DisableSearch {
		prompt := strings.TrimSpace(options.Prompt)
		if prompt == "" {
			prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
		}
		fmt.Fprintln(&screen, nativePromptLineWithCursorAndThemeForOptions(pickerTheme, options, prompt, query, queryCursor, len(items), len(options.Items), layout.Cols))
		fmt.Fprintln(&screen, nativeSearchSeparatorLineWithTheme(pickerTheme, layout.Cols))
	}

	placement := nativePreviewPlacement(options.Preview.Window)
	previewHeight := nativePreviewHeight(layout.Rows, options.Preview.Window)
	previewLimit := maxInt(1, layout.Rows-nativeChromeLineCount(options))
	if placement == "down" {
		previewLimit = previewHeight
	}
	previewLines := nativePreviewLines(options, items, selected, previewOffset, previewLimit)
	reservePreview := nativeReservePreviewFrame(options, placement)
	if reservePreview && len(previewLines) == 0 {
		previewLines = nativeBlankPreviewLines(previewHeight)
	}
	listLimit := nativeListLimit(options, layout, placement, previewHeight, len(previewLines) > 0)
	start, end := nativeVisibleRange(len(items), selected, listLimit)
	if options.MultiLine {
		start, end = nativeVisibleRangeByRenderedRows(items, selected, listLimit)
	}
	displayItems := items
	if !options.MultiLine {
		displayItems = nativeHighlightSimpleItemsWithTheme(pickerTheme, options, items, query)
	}
	renderSelected := selected
	if options.ReadOnly {
		renderSelected = -1
	}
	listLines := nativeInteractiveListLinesWithTheme(pickerTheme, displayItems, start, end, renderSelected, options.MultiLine)
	prependedRows := 0
	if options.MultiLine {
		listLines = nativeAppendPartialNextItemLinesWithTheme(pickerTheme, displayItems, listLines, end, selected, listLimit)
		listLines, prependedRows = nativePrependPartialPreviousItemLinesWithTheme(pickerTheme, displayItems, listLines, start, selected, listLimit)
	}
	var main strings.Builder
	if len(items) == 0 {
		fmt.Fprintln(&main, "  "+nativeLocalizedTextForOptions(options, i18n.KeyPickerEmptyNoMatches, "no matches"))
		writeNativeContentWithResourceDockAndFooter(w, pickerTheme, screen.String(), main.String(), options.ResourceSummaryDock, options.Footer, layout)
		return
	}
	if len(previewLines) > 0 && placement == "right" && layout.Cols >= 88 {
		scrollTotal, scrollStart, scrollEnd := len(items), start, end
		if options.MultiLine {
			scrollTotal, scrollStart, _ = nativeListScrollbarUnits(items, start, end, true)
			scrollStart = max(scrollStart-prependedRows, 0)
			scrollEnd = min(scrollStart+len(listLines), scrollTotal)
		}
		renderNativeSplitPreview(&main, listLines, previewLines, layout, options.Preview.Window, scrollTotal, scrollStart, scrollEnd, listLimit)
		writeNativeContentWithResourceDockAndFooter(w, pickerTheme, screen.String(), main.String(), options.ResourceSummaryDock, options.Footer, layout)
		return
	}
	scrollRows := listLimit
	if len(previewLines) > 0 && placement != "down" {
		scrollRows = len(listLines)
	}
	scrollTotal, scrollStart, scrollEnd := nativeListScrollbarUnits(items, start, end, options.MultiLine)
	if options.MultiLine {
		scrollStart = max(scrollStart-prependedRows, 0)
		scrollEnd = min(scrollStart+len(listLines), scrollTotal)
	}
	listLines = nativeListLinesWithScrollbarRowsWithTheme(pickerTheme, listLines, scrollTotal, scrollStart, scrollEnd, layout.Cols, scrollRows)
	for _, line := range listLines {
		fmt.Fprintln(&main, line)
	}
	if len(previewLines) > 0 && placement == "down" {
		renderNativeDownPreview(&main, previewLines, layout)
		writeNativeContentWithResourceDockAndFooter(w, pickerTheme, screen.String(), main.String(), options.ResourceSummaryDock, options.Footer, layout)
		return
	}
	if len(previewLines) > 0 {
		renderNativeInlinePreview(&main, previewLines, layout)
	}
	writeNativeContentWithResourceDockAndFooter(w, pickerTheme, screen.String(), main.String(), options.ResourceSummaryDock, options.Footer, layout)
}

func renderNativeRecorderContent(w io.Writer, pickerTheme projmuxpicker.Theme, top string, options Options, layout nativeLayout) {
	state := options.Recorder.State
	if state.Phase == "" {
		state = newRecorderState()
	}
	var main strings.Builder
	switch state.Phase {
	case RecorderStaged, RecorderConfirmed:
		fmt.Fprintln(&main, "  Staged: "+state.Candidate)
		fmt.Fprintln(&main, "  Not saved yet. Press Enter to save and apply, or Esc to discard.")
	default:
		fmt.Fprintln(&main, "  Recording")
		fmt.Fprintln(&main, "  Press a key combination")
		fmt.Fprintln(&main, "  Not saved yet. Esc cancels without changes.")
	}
	if strings.TrimSpace(state.Message) != "" {
		fmt.Fprintln(&main)
		fmt.Fprintln(&main, "  "+state.Message)
	}
	writeNativeContentWithFooterWithTheme(w, pickerTheme, top, main.String(), options.Footer, layout)
}

func nativeListLimit(options Options, layout nativeLayout, previewPlacement string, previewHeight int, hasPreview bool) int {
	available := layout.Rows - nativeChromeLineCount(options)
	if hasPreview && previewPlacement == "down" {
		available -= previewHeight + 1
	}
	if available < 1 {
		return 1
	}
	return available
}

func nativeReservePreviewFrame(options Options, placement string) bool {
	return strings.TrimSpace(options.UI) == "sidebar" && placement == "down" && strings.TrimSpace(options.Preview.Window) != ""
}

func nativeBlankPreviewLines(height int) []string {
	if height <= 0 {
		return nil
	}
	return make([]string, height)
}

func nativeAppendPartialNextItemLines(items []Item, lines []string, next, selected, limit int) []string {
	return nativeAppendPartialNextItemLinesWithTheme(projmuxpicker.DefaultTheme, items, lines, next, selected, limit)
}

func nativeAppendPartialNextItemLinesWithTheme(pickerTheme projmuxpicker.Theme, items []Item, lines []string, next, selected, limit int) []string {
	if limit <= 0 || len(lines) >= limit || next < 0 || next >= len(items) {
		return lines
	}
	remaining := limit - len(lines)
	if remaining <= 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	nextLines := nativePartialNextItemLinesWithTheme(pickerTheme, items, next, selected)
	if len(nextLines) > remaining {
		nextLines = nextLines[:remaining]
	}
	return append(out, nextLines...)
}

func nativePrependPartialPreviousItemLines(items []Item, lines []string, start, selected, limit int) ([]string, int) {
	return nativePrependPartialPreviousItemLinesWithTheme(projmuxpicker.DefaultTheme, items, lines, start, selected, limit)
}

func nativePrependPartialPreviousItemLinesWithTheme(pickerTheme projmuxpicker.Theme, items []Item, lines []string, start, selected, limit int) ([]string, int) {
	if limit <= 0 || len(lines) >= limit || start <= 0 || start > len(items) {
		return lines, 0
	}
	remaining := limit - len(lines)
	if remaining <= 0 {
		return lines, 0
	}
	prefix := nativeLinesBeforeItemWithTheme(pickerTheme, items, start, selected)
	if len(prefix) > remaining {
		prefix = prefix[len(prefix)-remaining:]
	}
	out := make([]string, 0, len(prefix)+len(lines))
	out = append(out, prefix...)
	out = append(out, lines...)
	return out, len(prefix)
}

func nativeLinesBeforeItem(items []Item, index, selected int) []string {
	return nativeLinesBeforeItemWithTheme(projmuxpicker.DefaultTheme, items, index, selected)
}

func nativeLinesBeforeItemWithTheme(pickerTheme projmuxpicker.Theme, items []Item, index, selected int) []string {
	if index <= 0 || index >= len(items) {
		return nil
	}
	withCurrent := nativeInteractiveListLinesWithTheme(pickerTheme, items, 0, index+1, selected, true)
	current := nativeInteractiveListLinesWithTheme(pickerTheme, items, index, index+1, selected, true)
	if len(current) == 0 || len(withCurrent) <= len(current) {
		return nil
	}
	return withCurrent[:len(withCurrent)-len(current)]
}

func nativePartialNextItemLines(items []Item, next, selected int) []string {
	return nativePartialNextItemLinesWithTheme(projmuxpicker.DefaultTheme, items, next, selected)
}

func nativePartialNextItemLinesWithTheme(pickerTheme projmuxpicker.Theme, items []Item, next, selected int) []string {
	if next < 0 || next >= len(items) {
		return nil
	}
	if next == 0 {
		return nativeInteractiveItemLinesWithTheme(pickerTheme, items[next], next == selected, true)
	}
	withNext := nativeInteractiveListLinesWithTheme(pickerTheme, items, next-1, next+1, selected, true)
	withoutNext := nativeInteractiveListLinesWithTheme(pickerTheme, items, next-1, next, selected, true)
	if len(withNext) <= len(withoutNext) {
		return nativeInteractiveItemLinesWithTheme(pickerTheme, items[next], next == selected, true)
	}
	return withNext[len(withoutNext):]
}

func nativeChromeLineCount(options Options) int {
	lines := 0
	if !options.DisableSearch {
		lines++ // prompt
		lines++ // search/list separator
	}
	if header := strings.TrimSpace(options.Header); header != "" {
		lines += nativeTextLineCount(header)
	}
	lines += len(options.ChromeBands)
	if len(options.ResourceSummaryDock) > 0 {
		lines += 1 + len(options.ResourceSummaryDock)
	}
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		lines += 1 + nativeTextLineCount(footer) // footer separator + footer text
	}
	return lines
}

func nativeTextLineCount(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return len(strings.Split(value, "\n"))
}

func nativeSearchSeparatorLine(cols int) string {
	return projmuxpicker.SeparatorLine(cols)
}

func nativeSearchSeparatorLineWithTheme(pickerTheme projmuxpicker.Theme, cols int) string {
	return projmuxpicker.SeparatorLineWithTheme(pickerTheme, cols)
}

func nativeHeaderLine(header string, cols int) string {
	return projmuxpicker.HeaderLine(header, cols)
}

func nativeHeaderLineWithTheme(pickerTheme projmuxpicker.Theme, header string, cols int) string {
	return projmuxpicker.HeaderLineWithTheme(pickerTheme, header, cols)
}

func renderNativeFrame(w io.Writer, content string, layout nativeLayout) {
	projmuxpicker.DefaultRenderer().RenderFrame(w, content, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func renderNativeFrameWithTitle(w io.Writer, content, title string, layout nativeLayout, options Options) {
	nativeRenderer(options).RenderFrameWithTitle(w, content, title, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func renderNativeFrameWithChips(w io.Writer, content string, chips []projmuxpicker.Chip, layout nativeLayout, options Options) {
	nativeRenderer(options).RenderFrameWithChips(w, content, chips, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func nativeRenderer(options Options) projmuxpicker.Renderer {
	return projmuxpicker.NewRenderer(nativeTheme(options))
}

func nativeTheme(options Options) projmuxpicker.Theme {
	if options.Theme == nil {
		return projmuxpicker.DefaultTheme
	}
	return projmuxpicker.ThemeFromEffective(*options.Theme)
}

func nativeTitlebarRowsForOptions(options Options) int {
	if len(options.TitleChips) > 0 {
		return projmuxpicker.ChipsTitlebarRows(options.TitleChips)
	}
	return projmuxpicker.TitlebarRows(options.Title)
}

func writeNativeContentWithFooter(w io.Writer, top, main, footer string, layout nativeLayout) {
	projmuxpicker.WriteContentWithFooter(w, top, main, footer, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func writeNativeContentWithFooterWithTheme(w io.Writer, pickerTheme projmuxpicker.Theme, top, main, footer string, layout nativeLayout) {
	writeNativeContentWithResourceDockAndFooter(w, pickerTheme, top, main, nil, footer, layout)
}

func writeNativeContentWithResourceDockAndFooter(w io.Writer, pickerTheme projmuxpicker.Theme, top, main string, dock []ChromeBand, footer string, layout nativeLayout) {
	var screen strings.Builder
	screen.WriteString(top)
	screen.WriteString(main)
	dockLines := nativeResourceSummaryDockLines(pickerTheme, dock, layout.Cols)
	footerLines := projmuxpicker.FooterBlockLinesWithTheme(pickerTheme, footer, layout.Cols)
	if len(dockLines) == 0 && len(footerLines) == 0 {
		fmt.Fprint(w, screen.String())
		return
	}
	remaining := layout.Rows - nativeRenderedTextLineCount(screen.String()) - len(dockLines) - len(footerLines)
	for range remaining {
		fmt.Fprintln(&screen)
	}
	for _, line := range dockLines {
		fmt.Fprintln(&screen, line)
	}
	for _, line := range footerLines {
		fmt.Fprintln(&screen, line)
	}
	fmt.Fprint(w, screen.String())
}

func nativeResourceSummaryDockLines(pickerTheme projmuxpicker.Theme, bands []ChromeBand, cols int) []string {
	if len(bands) == 0 {
		return nil
	}
	lines := make([]string, 0, 1+len(bands))
	lines = append(lines, projmuxpicker.SeparatorLineWithTheme(pickerTheme, cols))
	for _, band := range bands {
		lines = append(lines, projmuxpicker.BandLineWithTheme(pickerTheme, band.Label, band.Value, band.Secondary, cols))
	}
	return lines
}

func nativeFooterBlockLines(footer string, cols int) []string {
	return projmuxpicker.FooterBlockLines(footer, cols)
}

func nativeRenderedTextLineCount(value string) int {
	return projmuxpicker.RenderedTextLineCount(value)
}

func nativePromptLine(prompt, query string, matches, total, cols int) string {
	return projmuxpicker.PromptLine(prompt, query, matches, total, cols)
}

func nativePromptLineWithCursor(prompt, query string, cursor, matches, total, cols int) string {
	return projmuxpicker.PromptLineWithCursorLabel(nativeLocalizedText(i18n.KeyPickerPromptSearch, "Search"), prompt, query, cursor, matches, total, cols)
}

func nativePromptLineWithCursorAndTheme(pickerTheme projmuxpicker.Theme, prompt, query string, cursor, matches, total, cols int) string {
	return projmuxpicker.PromptLineWithRenderedQueryLabelAndTheme(pickerTheme, nativeLocalizedText(i18n.KeyPickerPromptSearch, "Search"), prompt, query, projmuxpicker.QueryWithCursorAndTheme(pickerTheme, query, cursor), matches, total, cols)
}

func nativePromptLineWithCursorAndThemeForOptions(pickerTheme projmuxpicker.Theme, options Options, prompt, query string, cursor, matches, total, cols int) string {
	return projmuxpicker.PromptLineWithRenderedQueryLabelAndTheme(pickerTheme, nativeLocalizedTextForOptions(options, i18n.KeyPickerPromptSearch, "Search"), prompt, query, projmuxpicker.QueryWithCursorAndTheme(pickerTheme, query, cursor), matches, total, cols)
}

func nativePromptLineWithRenderedQuery(prompt, query, renderedQuery string, matches, total, cols int) string {
	return projmuxpicker.PromptLineWithRenderedQueryLabel(nativeLocalizedText(i18n.KeyPickerPromptSearch, "Search"), prompt, query, renderedQuery, matches, total, cols)
}

func nativeQueryWithCursor(query string, cursor int) string {
	return projmuxpicker.QueryWithCursor(query, cursor)
}

func nativeVisibleRange(total, selected, limit int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if limit <= 0 || total <= limit {
		return 0, total
	}
	start := max(selected-limit/2, 0)
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func nativeVisibleRangeByRenderedRows(items []Item, selected, limit int) (int, int) {
	total := len(items)
	if total <= 0 {
		return 0, 0
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	if limit <= 0 {
		return selected, selected + 1
	}

	start, end := nativeVisibleRange(total, selected, limit)
	for nativeRenderedListLineCount(items, start, end, true) > limit {
		left := selected - start
		right := end - selected - 1
		switch {
		case left > right && start < selected:
			start++
		case end-1 > selected:
			end--
		case start < selected:
			start++
		default:
			return start, end
		}
	}

	for {
		expanded := false
		if start > 0 && nativeRenderedListLineCount(items, start-1, end, true) <= limit {
			start--
			expanded = true
		}
		if end < total && nativeRenderedListLineCount(items, start, end+1, true) <= limit {
			end++
			expanded = true
		}
		if !expanded {
			return start, end
		}
	}
}

func nativeRenderedListLineCount(items []Item, start, end int, multiLine bool) int {
	return projmuxpicker.RenderedListLineCount(nativeRows(items), start, end, multiLine)
}

func nativeItemLineCount(item Item) int {
	return projmuxpicker.RowLineCount(nativeRow(item))
}

func nativeInteractiveListLines(items []Item, start, end, selected int, multiLine bool) []string {
	return projmuxpicker.InteractiveListLines(nativeRows(items), start, end, selected, multiLine)
}

func nativeInteractiveListLinesWithTheme(pickerTheme projmuxpicker.Theme, items []Item, start, end, selected int, multiLine bool) []string {
	return projmuxpicker.InteractiveListLinesWithTheme(pickerTheme, nativeRows(items), start, end, selected, multiLine)
}

func nativeListLinesWithScrollbar(lines []string, total, start, end, width int) []string {
	return projmuxpicker.ListLinesWithScrollbar(lines, total, start, end, width)
}

func nativeListLinesWithScrollbarRows(lines []string, total, start, end, width, rows int) []string {
	return projmuxpicker.ListLinesWithScrollbarRows(lines, total, start, end, width, rows)
}

func nativeListLinesWithScrollbarRowsWithTheme(pickerTheme projmuxpicker.Theme, lines []string, total, start, end, width, rows int) []string {
	return projmuxpicker.ListLinesWithScrollbarRowsWithTheme(pickerTheme, lines, total, start, end, width, rows)
}

func nativeListScrollbarUnits(items []Item, start, end int, multiLine bool) (int, int, int) {
	if !multiLine {
		return len(items), start, end
	}
	rows := nativeRows(items)
	total := projmuxpicker.RenderedListLineCount(rows, 0, len(rows), true)
	before := projmuxpicker.RenderedListLineCount(rows, 0, start, true)
	if start > 0 {
		before++
	}
	visible := projmuxpicker.RenderedListLineCount(rows, start, end, true)
	return total, before, before + visible
}

func nativeRenderableListLines(lines []string, width int) []string {
	return projmuxpicker.RenderableListLines(lines, width)
}

func nativeRenderableListLine(line string, width int) string {
	return projmuxpicker.RenderableListLine(line, width)
}

func nativePadStyledLine(line string, width int) string {
	return projmuxpicker.PadStyledLine(line, width)
}

func nativeInteractiveItemLines(item Item, selected, multiLine bool) []string {
	return projmuxpicker.InteractiveRowLines(nativeRow(item), selected, multiLine)
}

func nativeInteractiveItemLinesWithTheme(pickerTheme projmuxpicker.Theme, item Item, selected, multiLine bool) []string {
	return projmuxpicker.InteractiveRowLinesWithTheme(pickerTheme, nativeRow(item), selected, multiLine)
}

func nativeSelectedContent(value string) string {
	return projmuxpicker.SelectedContent(value)
}

func nativeInverseSelectedContent(value string) string {
	return projmuxpicker.InverseSelectedContent(value)
}

func nativeRows(items []Item) []projmuxpicker.Row {
	rows := make([]projmuxpicker.Row, 0, len(items))
	for _, item := range items {
		rows = append(rows, nativeRow(item))
	}
	return rows
}

func nativeRow(item Item) projmuxpicker.Row {
	return projmuxpicker.Row{
		Label:     item.EffectiveLabel(),
		MetaLines: item.MetaLines,
	}
}

func nativeHighlightSimpleItems(options Options, items []Item, query string) []Item {
	return nativeHighlightSimpleItemsWithTheme(projmuxpicker.DefaultTheme, options, items, query)
}

func nativeHighlightSimpleItemsWithTheme(pickerTheme projmuxpicker.Theme, options Options, items []Item, query string) []Item {
	query = strings.TrimSpace(query)
	if query == "" || options.DisableSearch || hasNativeSearchKey(options.Items) {
		return items
	}
	caseSensitive := nativeSmartCaseSensitive(query)
	pattern := nativeSearchPattern(query, caseSensitive)
	highlighted := make([]Item, len(items))
	copy(highlighted, items)
	for i := range highlighted {
		label := highlighted[i].EffectiveLabel()
		positions, ok := nativeFuzzyMatchPositions(stripANSISequences(label), pattern, caseSensitive)
		if !ok {
			continue
		}
		highlighted[i].Label = nativeHighlightANSIVisiblePositionsWithTheme(pickerTheme, label, positions)
	}
	return highlighted
}

func nativeFuzzyMatchPositions(source string, pattern []rune, caseSensitive bool) ([]int, bool) {
	if len(pattern) == 0 {
		return nil, true
	}
	sourceRunes := []rune(source)
	if len(pattern) > len(sourceRunes) {
		return nil, false
	}
	pidx := 0
	end := -1
	for idx, r := range sourceRunes {
		if nativeSearchRune(r, caseSensitive) != pattern[pidx] {
			continue
		}
		pidx++
		if pidx == len(pattern) {
			end = idx
			break
		}
	}
	if end < 0 {
		return nil, false
	}
	positions := make([]int, len(pattern))
	pidx = len(pattern) - 1
	for idx := end; idx >= 0; idx-- {
		if nativeSearchRune(sourceRunes[idx], caseSensitive) != pattern[pidx] {
			continue
		}
		positions[pidx] = idx
		pidx--
		if pidx < 0 {
			return positions, true
		}
	}
	return nil, false
}

func nativeHighlightANSIVisiblePositions(value string, positions []int) string {
	return nativeHighlightANSIVisiblePositionsWithTheme(projmuxpicker.DefaultTheme, value, positions)
}

func nativeHighlightANSIVisiblePositionsWithTheme(pickerTheme projmuxpicker.Theme, value string, positions []int) string {
	if len(positions) == 0 || value == "" {
		return value
	}
	highlightStart := pickerTheme.Highlight
	if highlightStart == "" {
		highlightStart = nativeHighlightStart
	}
	positionSet := make(map[int]struct{}, len(positions))
	for _, position := range positions {
		positionSet[position] = struct{}{}
	}
	var out strings.Builder
	activeSGR := ""
	visible := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			end := i + 1
			for end < len(value) && value[end] != 'm' {
				end++
			}
			if end < len(value) {
				seq := value[i : end+1]
				out.WriteString(seq)
				if nativeANSIReset(seq) {
					activeSGR = ""
				} else {
					activeSGR += seq
				}
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if _, ok := positionSet[visible]; ok {
			out.WriteString(highlightStart)
			out.WriteRune(r)
			out.WriteString(nativeReset)
			out.WriteString(activeSGR)
		} else {
			out.WriteRune(r)
		}
		visible++
		i += size
	}
	return out.String()
}

func nativeANSIReset(seq string) bool {
	return seq == "\x1b[0m" || seq == "\x1b[m"
}

func nativePreviewLines(options Options, items []Item, selected, offset, limit int) []string {
	command := strings.TrimSpace(options.Preview.Command)
	if command == "" || selected < 0 || selected >= len(items) {
		return nil
	}
	target := strings.TrimSpace(items[selected].PreviewTarget)
	if target == "" {
		target = items[selected].Value
	}
	if target == "" {
		return nil
	}
	output := runNativePreviewCommand(command, target)
	if strings.TrimSpace(output) == "" {
		return nil
	}
	return limitedNativePreviewLines(output, offset, limit)
}

func nativePreviewPlacement(window string) string {
	return projmuxpicker.PreviewPlacement(window)
}

func nativePreviewHeight(rows int, window string) int {
	return projmuxpicker.PreviewHeight(rows, window)
}

func renderNativeSplitPreview(w io.Writer, listLines, previewLines []string, layout nativeLayout, window string, total, start, end, rows int) {
	projmuxpicker.RenderSplitPreviewRows(w, listLines, previewLines, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols}, window, total, start, end, rows)
}

func nativePreviewWidth(cols int, window string) int {
	return projmuxpicker.PreviewWidth(cols, window)
}

func renderNativeDownPreview(w io.Writer, previewLines []string, layout nativeLayout) {
	projmuxpicker.RenderDownPreview(w, previewLines, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func renderNativeInlinePreview(w io.Writer, previewLines []string, layout nativeLayout) {
	projmuxpicker.RenderInlinePreviewRows(w, previewLines, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func nativePadRight(value string, width int) string {
	return projmuxpicker.PadRight(value, width)
}

func nativeTruncateANSI(value string, width int) string {
	return projmuxpicker.TruncateANSI(value, width)
}

func nativeVisibleLen(value string) int {
	return projmuxpicker.VisibleLen(value)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt3(a, b, c int) int {
	return maxInt(maxInt(a, b), c)
}

func runNativePreviewCommand(command, target string) string {
	command = expandNativeCommand(command, target)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out)
}

func runNativeActionCommand(command, target string) {
	command = expandNativeCommand(command, target)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "sh", "-c", command).Run()
}

func expandNativeCommand(command, target string) string {
	command = strings.ReplaceAll(command, "{2}", shellQuoteNative(target))
	command = strings.ReplaceAll(command, "{}", shellQuoteNative(target))
	return command
}

func limitedNativePreviewLines(output string, offset, limit int) []string {
	output = strings.TrimRight(output, "\r\n")
	if output == "" || limit <= 0 {
		return nil
	}
	lines := strings.Split(output, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	lines = lines[offset:]
	if limit > 0 && len(lines) > limit {
		if limit == 1 {
			return []string{fmt.Sprintf("... %d more lines", len(lines))}
		}
		lines = append(lines[:limit-1], fmt.Sprintf("... %d more lines", len(lines)-limit+1))
	}
	return lines
}

func shellQuoteNative(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func renderNative(w io.Writer, options Options, items []Item, query string) {
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(w, header)
	}
	for _, band := range options.ChromeBands {
		line := strings.TrimSpace(strings.Join([]string{band.Label, band.Value, band.Secondary}, " "))
		if line != "" {
			fmt.Fprintln(w, line)
		}
	}
	if !options.DisableSearch {
		prompt := strings.TrimSpace(options.Prompt)
		if prompt == "" {
			prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
		}
		if query != "" {
			fmt.Fprintf(w, "%s query: %s\n", prompt, query)
		} else {
			fmt.Fprintln(w, prompt)
		}
	}
	for i, item := range items {
		if options.ReadOnly {
			fmt.Fprintf(w, "%s\n", item.EffectiveLabel())
		} else {
			fmt.Fprintf(w, "%d. %s\n", i+1, item.EffectiveLabel())
		}
		for _, meta := range item.MetaLines {
			if meta = strings.TrimSpace(meta); meta != "" {
				fmt.Fprintf(w, "   %s\n", meta)
			}
		}
	}
	for _, line := range nativeResourceSummaryDockLines(nativeTheme(options), options.ResourceSummaryDock, defaultNativeCols) {
		fmt.Fprintln(w, line)
	}
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		fmt.Fprintln(w, footer)
	}
	if options.ReadOnly {
		fmt.Fprint(w, nativeLocalizedTextForOptions(options, "picker.read_only.prompt", "action or empty to close: "))
	} else {
		fmt.Fprint(w, nativeLocalizedTextForOptions(options, i18n.KeyPickerLinePrompt, "number, search, or empty to close: "))
	}
}

func findAction(actions []Action, key string) (Action, bool) {
	key = strings.TrimSpace(key)
	for _, action := range actions {
		if strings.TrimSpace(action.Key) == key {
			return action, true
		}
	}
	return Action{}, false
}

func fuzzyScore(source string, pattern []rune, caseSensitive bool) (int, bool) {
	if len(pattern) == 0 {
		return 0, true
	}
	sourceRunes := []rune(source)
	if len(pattern) > len(sourceRunes) {
		return 0, false
	}
	if len(pattern) > 1000 || len(pattern)*len(sourceRunes) > 1_000_000 {
		return nativeFuzzyScoreGreedy(sourceRunes, pattern, caseSensitive)
	}
	return nativeFuzzyScoreV2(sourceRunes, pattern, caseSensitive)
}

func nativeFuzzyScoreGreedy(sourceRunes, pattern []rune, caseSensitive bool) (int, bool) {
	pidx := 0
	start := -1
	end := -1
	for idx, r := range sourceRunes {
		if nativeSearchRune(r, caseSensitive) != pattern[pidx] {
			continue
		}
		if start < 0 {
			start = idx
		}
		pidx++
		if pidx == len(pattern) {
			end = idx + 1
			break
		}
	}
	if end < 0 {
		return 0, false
	}

	pidx = len(pattern) - 1
	for idx := end - 1; idx >= start; idx-- {
		if nativeSearchRune(sourceRunes[idx], caseSensitive) != pattern[pidx] {
			continue
		}
		pidx--
		if pidx < 0 {
			start = idx
			break
		}
	}

	score := nativeFuzzyReferenceScore(sourceRunes, pattern, start, end, caseSensitive)
	return score, true
}

func nativeSearchRune(r rune, caseSensitive bool) rune {
	if caseSensitive {
		return r
	}
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	if r > unicode.MaxASCII {
		return unicode.ToLower(r)
	}
	return r
}

const (
	nativeScoreMatch               = 16
	nativeScoreGapStart            = -3
	nativeScoreGapExtension        = -1
	nativeBonusBoundary            = nativeScoreMatch / 2
	nativeBonusBoundaryWhite       = nativeBonusBoundary + 2
	nativeBonusBoundaryDelimiter   = nativeBonusBoundary + 1
	nativeBonusNonWord             = nativeScoreMatch / 2
	nativeBonusCamel123            = nativeBonusBoundary + nativeScoreGapExtension
	nativeBonusConsecutive         = -(nativeScoreGapStart + nativeScoreGapExtension)
	nativeBonusFirstCharMultiplier = 2
)

func nativeFuzzyScoreV2(source, pattern []rune, caseSensitive bool) (int, bool) {
	normalized := make([]rune, len(source))
	bonuses := make([]int, len(source))
	prevClass := nativeCharWhite
	matched := 0
	for idx, r := range source {
		class := nativeClassOf(r)
		normalized[idx] = nativeSearchRune(r, caseSensitive)
		bonuses[idx] = nativeBonusFor(prevClass, class)
		if matched < len(pattern) && normalized[idx] == pattern[matched] {
			matched++
		}
		prevClass = class
	}
	if matched != len(pattern) {
		return 0, false
	}

	width := len(source)
	height := len(pattern)
	scores := make([]int, width*height)
	consecutive := make([]int, width*height)
	maxScore := 0
	for row := range height {
		inGap := false
		for col := range width {
			leftScore := 0
			if col > 0 {
				leftScore = scores[row*width+col-1]
			}
			gapScore := 0
			if leftScore > 0 {
				if inGap {
					gapScore = leftScore + nativeScoreGapExtension
				} else {
					gapScore = leftScore + nativeScoreGapStart
				}
			}

			matchScore := 0
			runLength := 0
			if normalized[col] == pattern[row] {
				if row == 0 {
					bonus := bonuses[col]
					matchScore = nativeScoreMatch + bonus*nativeBonusFirstCharMultiplier
					runLength = 1
				} else if col > 0 {
					diag := scores[(row-1)*width+col-1]
					if diag > 0 {
						bonus := bonuses[col]
						runLength = consecutive[(row-1)*width+col-1] + 1
						if runLength > 1 {
							firstBonus := bonuses[col-runLength+1]
							if bonus >= nativeBonusBoundary && bonus > firstBonus {
								runLength = 1
							} else {
								bonus = maxInt3(bonus, firstBonus, nativeBonusConsecutive)
							}
						}
						matchScore = diag + nativeScoreMatch
						if matchScore+bonus < gapScore {
							matchScore += bonuses[col]
							runLength = 0
						} else {
							matchScore += bonus
						}
					}
				}
			}

			score := maxInt3(matchScore, gapScore, 0)
			if score != matchScore {
				runLength = 0
			}
			consecutive[row*width+col] = runLength
			scores[row*width+col] = score
			inGap = matchScore < gapScore
			if row == height-1 && score > maxScore {
				maxScore = score
			}
		}
	}
	if maxScore == 0 {
		return 0, false
	}
	return maxScore, true
}

type nativeCharClass int

const (
	nativeCharWhite nativeCharClass = iota
	nativeCharNonWord
	nativeCharDelimiter
	nativeCharLower
	nativeCharUpper
	nativeCharLetter
	nativeCharNumber
)

func nativeFuzzyReferenceScore(source, pattern []rune, start, end int, caseSensitive bool) int {
	score := 0
	pidx := 0
	inGap := false
	consecutive := 0
	firstBonus := 0
	prevClass := nativeCharWhite
	if start > 0 {
		prevClass = nativeClassOf(source[start-1])
	}
	for idx := start; idx < end; idx++ {
		r := source[idx]
		class := nativeClassOf(r)
		if nativeSearchRune(r, caseSensitive) == pattern[pidx] {
			score += nativeScoreMatch
			bonus := nativeBonusFor(prevClass, class)
			if consecutive == 0 {
				firstBonus = bonus
			} else {
				if bonus >= nativeBonusBoundary && bonus > firstBonus {
					firstBonus = bonus
				}
				bonus = maxInt(bonus, maxInt(firstBonus, nativeBonusConsecutive))
			}
			if pidx == 0 {
				score += bonus * nativeBonusFirstCharMultiplier
			} else {
				score += bonus
			}
			inGap = false
			consecutive++
			pidx++
			if pidx == len(pattern) {
				break
			}
		} else {
			if inGap {
				score += nativeScoreGapExtension
			} else {
				score += nativeScoreGapStart
			}
			inGap = true
			consecutive = 0
			firstBonus = 0
		}
		prevClass = class
	}
	return score
}

func nativeClassOf(r rune) nativeCharClass {
	switch {
	case r >= 'a' && r <= 'z':
		return nativeCharLower
	case r >= 'A' && r <= 'Z':
		return nativeCharUpper
	case r >= '0' && r <= '9':
		return nativeCharNumber
	case strings.ContainsRune(" \t\n\v\f\r\x85\u00a0", r):
		return nativeCharWhite
	case strings.ContainsRune("/,:;|", r):
		return nativeCharDelimiter
	case unicode.IsLower(r):
		return nativeCharLower
	case unicode.IsUpper(r):
		return nativeCharUpper
	case unicode.IsNumber(r):
		return nativeCharNumber
	case unicode.IsLetter(r):
		return nativeCharLetter
	case unicode.IsSpace(r):
		return nativeCharWhite
	default:
		return nativeCharNonWord
	}
}

func nativeBonusFor(prev, class nativeCharClass) int {
	if class > nativeCharNonWord {
		switch prev {
		case nativeCharWhite:
			return nativeBonusBoundaryWhite
		case nativeCharDelimiter:
			return nativeBonusBoundaryDelimiter
		case nativeCharNonWord:
			return nativeBonusBoundary
		}
	}
	if prev == nativeCharLower && class == nativeCharUpper || prev != nativeCharNumber && class == nativeCharNumber {
		return nativeBonusCamel123
	}
	switch class {
	case nativeCharNonWord, nativeCharDelimiter:
		return nativeBonusNonWord
	case nativeCharWhite:
		return nativeBonusBoundaryWhite
	default:
		return 0
	}
}
