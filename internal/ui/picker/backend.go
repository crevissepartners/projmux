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
	"unicode/utf8"
)

const BackendEnv = "PROJMUX_PICKER_BACKEND"

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
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]Item(nil), items...)
	}

	if hasNativeSearchKey(items) {
		filtered := make([]Item, 0, len(items))
		for _, item := range items {
			if _, ok := fuzzyScore(strings.ToLower(item.EffectiveSearchText()), query); ok {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}

	filtered := make([]nativeScoredItem, 0, len(items))
	for _, item := range items {
		if score, ok := fuzzyScore(strings.ToLower(item.EffectiveSearchText()), query); ok {
			filtered = append(filtered, nativeScoredItem{Item: item, Score: score, Index: len(filtered)})
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Score != filtered[j].Score {
			return filtered[i].Score < filtered[j].Score
		}
		return filtered[i].Index < filtered[j].Index
	})

	items = make([]Item, 0, len(filtered))
	for _, item := range filtered {
		items = append(items, item.Item)
	}
	return items
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
	defaultNativeRows  = 30
	defaultNativeCols  = 100
	nativeCurrentStart = "\x1b[48;2;38;50;56m\x1b[38;2;255;255;255m"
	nativePointer      = "\x1b[38;2;225;38;114m▌\x1b[0m "
	nativeReset        = "\x1b[0m"
	nativeScreenEnter  = "\x1b[?1049h\x1b[?25l"
	nativeScreenLeave  = "\x1b[?25h\x1b[?1049l\r\n"
	nativeScrollbar    = "█"
	nativeGapLine      = "─"
	nativeGapSentinel  = "\x00projmux-native-gap\x00"
)

func (r NativeRunner) Run(options Options) (Result, error) {
	in := r.In
	if in == nil {
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
			return runNativeInteractive(tty, tty, options)
		}
	}

	if restore, ok := enableRawTerminal(in); ok {
		defer restore()
		return runNativeInteractive(in, out, options)
	}

	if !allowNativeLineMode(in, os.Getenv) {
		return Result{}, fmt.Errorf("native picker requires a TTY; run from an interactive terminal or set PROJMUX_NATIVE_LINE_MODE=1 for scripted line mode")
	}

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
				return Result{Closed: true, Query: query}, nil
			}
			if err != nil && err != io.EOF {
				return Result{}, fmt.Errorf("read native picker input: %w", err)
			}
		}
		input := strings.TrimSpace(line)
		if input == "" {
			return Result{Closed: true, Query: query}, nil
		}
		if action, ok := findAction(options.Actions, input); ok && action.Intent == ActionClose {
			return Result{Key: action.Key, Query: query, Closed: true}, nil
		}
		if options.AcceptQuery {
			return Result{Key: "enter", Query: input}, nil
		}
		if index, err := strconv.Atoi(input); err == nil {
			if index < 1 || index > len(items) {
				fmt.Fprintf(out, "invalid selection: %d\n", index)
				continue
			}
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
	rawCmd := exec.Command("stty", "raw", "-echo", "min", "0", "time", "1")
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
	if strings.TrimSpace(lookup("PROJMUX_NATIVE_TTY_FALLBACK")) != "" {
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
	layout := detectNativeLayout(in)
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
		renderNativeInteractive(out, options, items, query, selected, previewOffset, layout)

		key, err := readNativeKey(in)
		if err != nil {
			if err == io.EOF {
				return Result{Closed: true, Query: query}, nil
			}
			return Result{}, fmt.Errorf("read native picker key: %w", err)
		}
		if key.Name == "" && key.Text == "" {
			continue
		}

		if action, ok := findAction(options.Actions, key.Name); ok {
			result, refresh := runNativePickerAction(action, options, items, selected, query)
			if refresh {
				continue
			}
			return result, nil
		}
		if key.Text != "" {
			if action, ok := findAction(options.Actions, key.Text); ok {
				result, refresh := runNativePickerAction(action, options, items, selected, query)
				if refresh {
					continue
				}
				return result, nil
			}
		}

		switch key.Name {
		case "enter":
			if options.AcceptQuery {
				return Result{Key: "enter", Query: query}, nil
			}
			if len(items) == 0 {
				continue
			}
			return Result{Key: "enter", Value: items[selected].Value, Query: query}, nil
		case "esc", "ctrl-c":
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
		if err != nil {
			return 0, err
		}
	}
}

func readNativeByteMaybe(r io.Reader) (byte, bool, error) {
	var buf [1]byte
	for attempt := 0; attempt < 20; attempt++ {
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], true, nil
		}
		if err == io.EOF {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		time.Sleep(5 * time.Millisecond)
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
	fmt.Fprint(w, "\x1b[2J\x1b[H")
	contentLayout := nativeContentLayout(layout)
	var body strings.Builder
	renderNativeInteractiveContent(&body, options, items, query, selected, previewOffset, contentLayout)
	renderNativeFrame(w, body.String(), layout)
}

func nativeContentLayout(layout nativeLayout) nativeLayout {
	if layout.Rows <= 0 {
		layout.Rows = defaultNativeRows
	}
	if layout.Cols <= 0 {
		layout.Cols = defaultNativeCols
	}
	rows := layout.Rows - 2
	if rows < 1 {
		rows = 1
	}
	cols := layout.Cols - 4
	if cols < 20 {
		cols = layout.Cols
	}
	return nativeLayout{Rows: rows, Cols: cols}
}

func renderNativeInteractiveContent(w io.Writer, options Options, items []Item, query string, selected, previewOffset int, layout nativeLayout) {
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(w, header)
	}
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
	}
	fmt.Fprintln(w, nativePromptLine(prompt, query, len(items), len(options.Items), layout.Cols))
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		fmt.Fprintln(w, footer)
	}
	fmt.Fprintln(w)

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
	if len(items) == 0 {
		fmt.Fprintln(w, "  no matches")
		return
	}
	if len(previewLines) > 0 && placement == "right" && layout.Cols >= 88 {
		renderNativeSplitPreview(w, listLines, previewLines, layout, options.Preview.Window, len(items), start, end)
		return
	}
	listLines = nativeListLinesWithScrollbar(listLines, len(items), start, end, layout.Cols)
	for _, line := range listLines {
		fmt.Fprintln(w, line)
	}
	if len(previewLines) > 0 && placement == "down" {
		renderNativeDownPreview(w, previewLines, layout)
		return
	}
	if len(previewLines) > 0 {
		renderNativeInlinePreview(w, previewLines)
	}
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
		lines += nativeTextLineCount(footer)
	}
	return lines + 1 // blank line between chrome and list
}

func nativeTextLineCount(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return len(strings.Split(value, "\n"))
}

func renderNativeFrame(w io.Writer, content string, layout nativeLayout) {
	width := layout.Cols
	if width <= 0 {
		width = defaultNativeCols
	}
	if width < 4 {
		fmt.Fprint(w, content)
		return
	}
	height := layout.Rows
	if height <= 0 {
		height = defaultNativeRows
	}
	innerWidth := width - 2
	innerHeight := height - 2
	if innerHeight < 1 {
		innerHeight = 1
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	fmt.Fprintf(w, "┌%s┐\n", strings.Repeat("─", innerWidth))
	for i := 0; i < innerHeight; i++ {
		line := ""
		if i < len(lines) {
			line = nativeTruncateANSI(strings.TrimRight(lines[i], "\r"), innerWidth)
		}
		fmt.Fprintf(w, "│%s│\n", nativePadRight(line, innerWidth))
	}
	fmt.Fprintf(w, "└%s┘\n", strings.Repeat("─", innerWidth))
}

func nativePromptLine(prompt, query string, matches, total, cols int) string {
	prompt = strings.TrimRight(prompt, " ")
	line := strings.TrimRight(prompt+" "+query, " ")
	info := strconv.Itoa(matches)
	if query != "" || matches != total {
		info = fmt.Sprintf("%d/%d", matches, total)
	}
	if cols <= 0 {
		cols = defaultNativeCols
	}
	padding := cols - nativeVisibleLen(line) - len(info)
	if padding < 2 {
		return line + "  " + info
	}
	return line + strings.Repeat(" ", padding) + info
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
	if start < 0 {
		start = 0
	}
	if end > len(items) {
		end = len(items)
	}
	if start >= end {
		return 0
	}
	total := 0
	for i := start; i < end; i++ {
		total += nativeItemLineCount(items[i])
	}
	if multiLine {
		total += end - start - 1
	}
	return total
}

func nativeItemLineCount(item Item) int {
	count := len(strings.Split(item.EffectiveLabel(), "\n"))
	if count == 0 {
		count = 1
	}
	for _, meta := range item.MetaLines {
		if strings.TrimSpace(meta) != "" {
			count++
		}
	}
	return count
}

func nativeInteractiveListLines(items []Item, start, end, selected int, multiLine bool) []string {
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, nativeInteractiveItemLines(items[i], i == selected, multiLine)...)
		if multiLine && i < end-1 {
			lines = append(lines, nativeGapSentinel)
		}
	}
	return lines
}

func nativeListLinesWithScrollbar(lines []string, total, start, end, width int) []string {
	visible := end - start
	hasScrollbar := total > visible && len(lines) > 0 && width > 1
	if !hasScrollbar {
		return nativeRenderableListLines(lines, width)
	}
	scrollbarIndex := 0
	if maxStart := total - visible; maxStart > 0 && len(lines) > 1 {
		scrollbarIndex = start * (len(lines) - 1) / maxStart
	}
	rendered := make([]string, 0, len(lines))
	for i, line := range lines {
		marker := " "
		if i == scrollbarIndex {
			marker = nativeScrollbar
		}
		line = nativeRenderableListLine(line, width-1)
		rendered = append(rendered, nativePadRight(nativeTruncateANSI(line, width-1), width-1)+marker)
	}
	return rendered
}

func nativeRenderableListLines(lines []string, width int) []string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, nativeRenderableListLine(line, width))
	}
	return rendered
}

func nativeRenderableListLine(line string, width int) string {
	if line != nativeGapSentinel {
		return line
	}
	if width <= 4 {
		return nativeGapLine
	}
	return "  " + strings.Repeat(nativeGapLine, width-2)
}

func nativeInteractiveItemLines(item Item, selected, multiLine bool) []string {
	lines := strings.Split(item.EffectiveLabel(), "\n")
	prefix := "  "
	if selected {
		prefix = "> "
		if multiLine {
			prefix = nativePointer
		}
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	rendered := make([]string, 0, len(lines)+len(item.MetaLines))
	first := fmt.Sprintf("%s%s", prefix, strings.TrimRight(lines[0], "\r"))
	if selected {
		if multiLine {
			first = prefix + nativeSelectedContent(strings.TrimRight(lines[0], "\r"))
		} else {
			first = "\x1b[7m" + first + nativeReset
		}
	}
	rendered = append(rendered, first)
	for _, line := range lines[1:] {
		line = fmt.Sprintf("    %s", strings.TrimRight(line, "\r"))
		if selected && multiLine {
			line = "  " + nativeSelectedContent(strings.TrimSpace(line))
		}
		rendered = append(rendered, line)
	}
	for _, meta := range item.MetaLines {
		if meta = strings.TrimSpace(meta); meta != "" {
			line := fmt.Sprintf("    %s", meta)
			if selected && multiLine {
				line = "  " + nativeSelectedContent(meta)
			}
			rendered = append(rendered, line)
		}
	}
	return rendered
}

func nativeSelectedContent(value string) string {
	return nativeCurrentStart + strings.ReplaceAll(value, nativeReset, nativeReset+nativeCurrentStart) + nativeReset
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
	window = strings.ToLower(strings.TrimSpace(window))
	switch {
	case strings.HasPrefix(window, "down"):
		return "down"
	case strings.HasPrefix(window, "right"), window == "":
		return "right"
	default:
		return "inline"
	}
}

func nativePreviewHeight(rows int, window string) int {
	if rows <= 0 {
		rows = defaultNativeRows
	}
	percent := nativePreviewPercent(window)
	if percent <= 0 {
		percent = 25
	}
	height := rows * percent / 100
	if height < 4 {
		return 4
	}
	if height > rows-8 {
		return maxInt(4, rows-8)
	}
	return height
}

func nativePreviewPercent(window string) int {
	for _, part := range strings.Split(window, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasSuffix(part, "%") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSuffix(part, "%"))
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func renderNativeSplitPreview(w io.Writer, listLines, previewLines []string, layout nativeLayout, window string, total, start, end int) {
	previewWidth := nativePreviewWidth(layout.Cols, window)
	listWidth := layout.Cols - previewWidth - 1
	if listWidth < 32 {
		listWidth = 32
		previewWidth = layout.Cols - listWidth - 1
	}
	listLines = nativeListLinesWithScrollbar(listLines, total, start, end, listWidth)
	rows := maxInt(len(listLines), len(previewLines))
	for i := 0; i < rows; i++ {
		left := ""
		if i < len(listLines) {
			left = listLines[i]
		}
		right := ""
		if i < len(previewLines) {
			right = previewLines[i]
		}
		fmt.Fprintf(w, "%s│%s\n", nativePadRight(nativeTruncateANSI(left, listWidth), listWidth), nativeTruncateANSI(right, previewWidth))
	}
}

func nativePreviewWidth(cols int, window string) int {
	if cols <= 0 {
		cols = defaultNativeCols
	}
	percent := nativePreviewPercent(window)
	if percent <= 0 {
		percent = 50
	}
	width := cols * percent / 100
	if width < 36 {
		return 36
	}
	return width
}

func renderNativeDownPreview(w io.Writer, previewLines []string, layout nativeLayout) {
	width := layout.Cols
	if width <= 0 {
		width = defaultNativeCols
	}
	fmt.Fprintln(w, nativeTruncateANSI(strings.Repeat("─", width), width))
	for _, line := range previewLines {
		fmt.Fprintln(w, nativeTruncateANSI(line, width))
	}
}

func renderNativeInlinePreview(w io.Writer, previewLines []string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- preview ---")
	for _, line := range previewLines {
		fmt.Fprintln(w, line)
	}
}

func nativePadRight(value string, width int) string {
	length := nativeVisibleLen(value)
	if length >= width {
		return value
	}
	return value + strings.Repeat(" ", width-length)
}

func nativeTruncateANSI(value string, width int) string {
	if width <= 0 || nativeVisibleLen(value) <= width {
		return value
	}
	var out strings.Builder
	visible := 0
	for i := 0; i < len(value) && visible < width; {
		if value[i] == '\x1b' {
			end := i + 1
			for end < len(value) && value[end] != 'm' {
				end++
			}
			if end < len(value) {
				out.WriteString(value[i : end+1])
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		out.WriteRune(r)
		visible++
		i += size
	}
	return out.String()
}

func nativeVisibleLen(value string) int {
	length := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			i++
			for i < len(value) && value[i] != 'm' {
				i++
			}
			if i < len(value) {
				i++
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			break
		}
		length++
		i += size
	}
	return length
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

func fuzzyScore(source, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	if source == query {
		return 0, true
	}
	if idx := strings.Index(source, query); idx >= 0 {
		return idx*10 + len([]rune(source)) - len([]rune(query)), true
	}
	sourceRunes := []rune(source)
	index := 0
	first := -1
	last := -1
	gaps := 0
	for _, ch := range query {
		found := false
		for index < len(sourceRunes) {
			if sourceRunes[index] == ch {
				if first < 0 {
					first = index
				}
				if last >= 0 {
					gaps += index - last - 1
				}
				last = index
				index++
				found = true
				break
			}
			index++
		}
		if !found {
			return 0, false
		}
	}
	return first*25 + gaps*10 + len(sourceRunes) - len([]rune(query)), true
}
