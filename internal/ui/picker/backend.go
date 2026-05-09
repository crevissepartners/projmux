package picker

import (
	"context"
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

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

const (
	BackendEnv           = "PROJMUX_PICKER_BACKEND"
	NativeLaunchKeyEnv   = "PROJMUX_NATIVE_LAUNCH_KEY"
	NativeDebugLogEnv    = "PROJMUX_NATIVE_DEBUG_LOG"
	NativeTTYFallbackEnv = "PROJMUX_NATIVE_TTY_FALLBACK"
)

type Backend string

const (
	BackendFZF    Backend = "fzf"
	BackendNative Backend = "native"
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
}

type Preview struct {
	Command string
	Window  string
}

type Options struct {
	UI           string
	Items        []Item
	Prompt       string
	Header       string
	Footer       string
	Actions      []Action
	Preview      Preview
	InitialQuery string
	InitialIndex int
	AcceptQuery  bool
	MultiLine    bool
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

func ResolveBackend(lookup func(string) string) Backend {
	raw := ""
	if lookup != nil {
		raw = lookup(BackendEnv)
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(BackendFZF):
		return BackendFZF
	case string(BackendNative):
		return BackendNative
	default:
		return BackendFZF
	}
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
	nativeCurrentStart      = projmuxpicker.CurrentStart
	nativePointer           = projmuxpicker.Pointer
	nativeReset             = projmuxpicker.Reset
	nativeInverseStart      = projmuxpicker.InverseStart
	nativeCursorStart       = projmuxpicker.CursorStart
	nativeScreenEnter       = "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H"
	nativeScreenLeave       = "\x1b[?25h\x1b[?1049l\r\n"
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
	for {
		items := FilterItems(options.Items, query)
		renderNative(out, options, items, query)

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
	Name string
	Text string
}

func runNativeInteractive(in io.Reader, out io.Writer, options Options) (Result, error) {
	query := strings.TrimSpace(options.InitialQuery)
	queryCursor := nativeRuneLen(query)
	selected := options.InitialIndex
	focusedValue := ""
	previewOffset := 0
	launchKey := strings.ToLower(strings.TrimSpace(os.Getenv(NativeLaunchKeyEnv)))
	layout := detectNativeLayout(in)
	nativeDebugLogf("interactive ui=%q start items=%d launch_key=%q layout=%dx%d", options.UI, len(options.Items), launchKey, layout.Cols, layout.Rows)
	fmt.Fprint(out, nativeScreenEnter)
	defer fmt.Fprint(out, nativeScreenLeave)

	for {
		items := FilterItems(options.Items, query)
		if selected >= len(items) {
			selected = len(items) - 1
		}
		if selected < 0 {
			selected = 0
		}
		if value := selectedNativeValue(items, selected); value != focusedValue {
			runNativeFocusAction(options.Actions, value)
			focusedValue = value
			previewOffset = 0
		}
		renderNativeInteractiveWithCursor(out, options, items, query, queryCursor, selected, previewOffset, layout)

		key, err := readNativeKey(in)
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

		if action, ok := findAction(options.Actions, key.Name); ok {
			result, refresh := runNativePickerAction(action, options, items, selected, query)
			nativeDebugLogf("interactive ui=%q action key=%q intent=%q refresh=%t result_key=%q closed=%t value=%q query=%q", options.UI, action.Key, action.Intent, refresh, result.Key, result.Closed, result.Value, result.Query)
			if refresh {
				continue
			}
			return result, nil
		}
		if key.Text != "" {
			if action, ok := findAction(options.Actions, key.Text); ok {
				result, refresh := runNativePickerAction(action, options, items, selected, query)
				nativeDebugLogf("interactive ui=%q text_action key=%q intent=%q refresh=%t result_key=%q closed=%t value=%q query=%q", options.UI, action.Key, action.Intent, refresh, result.Key, result.Closed, result.Value, result.Query)
				if refresh {
					continue
				}
				return result, nil
			}
		}

		switch key.Name {
		case "enter":
			if options.AcceptQuery {
				nativeDebugLogf("interactive ui=%q result=accept_query key=enter query=%q", options.UI, query)
				return Result{Key: "enter", Query: query}, nil
			}
			if len(items) == 0 {
				continue
			}
			nativeDebugLogf("interactive ui=%q result=select key=enter value=%q query=%q", options.UI, items[selected].Value, query)
			return Result{Key: "enter", Value: items[selected].Value, Query: query}, nil
		case "esc", "ctrl-c":
			nativeDebugLogf("interactive ui=%q result=closed key=%q query=%q", options.UI, key.Name, query)
			return Result{Key: key.Name, Query: query, Closed: true}, nil
		case "up":
			if selected > 0 {
				selected--
			}
		case "down":
			if selected < len(items)-1 {
				selected++
			}
		case "home":
			selected = 0
		case "end":
			if len(items) > 0 {
				selected = len(items) - 1
			}
		case "page-up":
			selected -= nativePageSize
			if selected < 0 {
				selected = 0
			}
		case "page-down":
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
			if queryCursor > 0 {
				queryCursor--
			}
		case "right":
			if queryCursor < nativeRuneLen(query) {
				queryCursor++
			}
		case "backspace":
			query, queryCursor = deleteNativeQueryBeforeCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		case "delete":
			query, queryCursor = deleteNativeQueryAtCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		case "ctrl-a":
			queryCursor = 0
		case "ctrl-e":
			queryCursor = nativeRuneLen(query)
		case "ctrl-u":
			query, queryCursor = deleteNativeQueryBeforeCursorN(query, queryCursor, queryCursor)
			selected = 0
			previewOffset = 0
		case "ctrl-w":
			query, queryCursor = trimNativeQueryWordBeforeCursor(query, queryCursor)
			selected = 0
			previewOffset = 0
		default:
			if key.Text != "" {
				query, queryCursor = insertNativeQueryText(query, queryCursor, key.Text)
				selected = 0
				previewOffset = 0
			}
		}
	}
}

func runNativePickerAction(action Action, options Options, items []Item, selected int, query string) (Result, bool) {
	value := selectedNativeValue(items, selected)
	switch action.Intent {
	case ActionClose:
		return Result{Key: action.Key, Query: query, Closed: true}, false
	case ActionCustom:
		if strings.TrimSpace(action.Command) != "" {
			runNativeActionCommand(action.Command, value)
			return Result{}, true
		}
		return Result{Key: action.Key, Value: value, Query: query}, false
	case ActionAccept:
		if options.AcceptQuery {
			return Result{Key: action.Key, Query: query}, false
		}
		return Result{Key: action.Key, Value: value, Query: query}, false
	default:
		return Result{}, false
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
	case "5":
		return nativeKey{Name: "ctrl-" + name}
	default:
		return nativeKey{Name: name}
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
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], nil
		}
		if err == io.EOF {
			if _, ok := r.(*os.File); ok {
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
	for attempt := 0; attempt < nativeMaybeReadAttempts; attempt++ {
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], true, nil
		}
		if err == io.EOF {
			if _, ok := r.(*os.File); ok {
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
	start := cursor - count
	if start < 0 {
		start = 0
	}
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
	fmt.Fprint(w, "\x1b[H")
	contentLayout := nativeContentLayout(layout)
	var body strings.Builder
	renderNativeInteractiveContent(&body, options, items, query, queryCursor, selected, previewOffset, contentLayout)
	renderNativeFrame(w, body.String(), layout)
}

func nativeContentLayout(layout nativeLayout) nativeLayout {
	content := projmuxpicker.DefaultRenderer().ContentLayout(projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
	return nativeLayout{Rows: content.Rows, Cols: content.Cols}
}

func renderNativeInteractiveContent(w io.Writer, options Options, items []Item, query string, queryCursor, selected, previewOffset int, layout nativeLayout) {
	var screen strings.Builder
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(&screen, header)
	}
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
	}
	fmt.Fprintln(&screen, nativePromptLineWithCursor(prompt, query, queryCursor, len(items), len(options.Items), layout.Cols))

	placement := nativePreviewPlacement(options.Preview.Window)
	previewHeight := nativePreviewHeight(layout.Rows, options.Preview.Window)
	previewLimit := maxInt(1, layout.Rows-nativeChromeLineCount(options))
	if placement == "down" {
		previewLimit = previewHeight
	}
	previewLines := nativePreviewLines(options, items, selected, previewOffset, previewLimit)
	listLimit := nativeListLimit(options, layout, placement, previewHeight, len(previewLines) > 0)
	start, end := nativeVisibleRange(len(items), selected, listLimit)
	if options.MultiLine {
		start, end = nativeVisibleRangeByRenderedRows(items, selected, listLimit)
	}
	listLines := nativeInteractiveListLines(items, start, end, selected, options.MultiLine)
	var main strings.Builder
	if len(items) == 0 {
		fmt.Fprintln(&main, "  no matches")
		writeNativeContentWithFooter(w, screen.String(), main.String(), options.Footer, layout)
		return
	}
	if len(previewLines) > 0 && placement == "right" && layout.Cols >= 88 {
		renderNativeSplitPreview(&main, listLines, previewLines, layout, options.Preview.Window, len(items), start, end, listLimit)
		writeNativeContentWithFooter(w, screen.String(), main.String(), options.Footer, layout)
		return
	}
	listLines = nativeListLinesWithScrollbar(listLines, len(items), start, end, layout.Cols)
	for _, line := range listLines {
		fmt.Fprintln(&main, line)
	}
	if len(previewLines) > 0 && placement == "down" {
		renderNativeDownPreview(&main, previewLines, layout)
		writeNativeContentWithFooter(w, screen.String(), main.String(), options.Footer, layout)
		return
	}
	if len(previewLines) > 0 {
		renderNativeInlinePreview(&main, previewLines)
	}
	writeNativeContentWithFooter(w, screen.String(), main.String(), options.Footer, layout)
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

func nativeChromeLineCount(options Options) int {
	lines := 1 // prompt
	if header := strings.TrimSpace(options.Header); header != "" {
		lines += nativeTextLineCount(header)
	}
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		lines += 1 + nativeTextLineCount(footer) // fzf footer border + footer text
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

func renderNativeFrame(w io.Writer, content string, layout nativeLayout) {
	projmuxpicker.DefaultRenderer().RenderFrame(w, content, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
}

func writeNativeContentWithFooter(w io.Writer, top, main, footer string, layout nativeLayout) {
	projmuxpicker.WriteContentWithFooter(w, top, main, footer, projmuxpicker.Layout{Rows: layout.Rows, Cols: layout.Cols})
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
	return projmuxpicker.PromptLineWithCursor(prompt, query, cursor, matches, total, cols)
}

func nativePromptLineWithRenderedQuery(prompt, query, renderedQuery string, matches, total, cols int) string {
	return projmuxpicker.PromptLineWithRenderedQuery(prompt, query, renderedQuery, matches, total, cols)
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
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
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

func nativeListLinesWithScrollbar(lines []string, total, start, end, width int) []string {
	return projmuxpicker.ListLinesWithScrollbar(lines, total, start, end, width)
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

func renderNativeInlinePreview(w io.Writer, previewLines []string) {
	projmuxpicker.RenderInlinePreview(w, previewLines)
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
	if output == "" {
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
		lines = append(lines[:limit], fmt.Sprintf("... %d more lines", len(lines)-limit))
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
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
	}
	if query != "" {
		fmt.Fprintf(w, "%s query: %s\n", prompt, query)
	} else {
		fmt.Fprintln(w, prompt)
	}
	for i, item := range items {
		fmt.Fprintf(w, "%d. %s\n", i+1, item.EffectiveLabel())
		for _, meta := range item.MetaLines {
			if meta = strings.TrimSpace(meta); meta != "" {
				fmt.Fprintf(w, "   %s\n", meta)
			}
		}
	}
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		fmt.Fprintln(w, footer)
	}
	fmt.Fprint(w, "number, search, or empty to close: ")
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

	score := nativeFZFLikeScore(sourceRunes, pattern, start, end, caseSensitive)
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
	for row := 0; row < height; row++ {
		inGap := false
		for col := 0; col < width; col++ {
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

func nativeFZFLikeScore(source, pattern []rune, start, end int, caseSensitive bool) int {
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
