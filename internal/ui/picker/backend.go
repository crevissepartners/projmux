package picker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	Key    string
	Intent ActionIntent
	Label  string
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

	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if fuzzyMatch(strings.ToLower(item.EffectiveSearchText()), query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

type NativeRunner struct {
	In  io.Reader
	Out io.Writer
}

const nativePageSize = 12

func (r NativeRunner) Run(options Options) (Result, error) {
	in := r.In
	if in == nil {
		return Result{Closed: true}, nil
	}
	out := r.Out
	if out == nil {
		out = io.Discard
	}

	if restore, ok := enableRawTerminal(in); ok {
		defer restore()
		return runNativeInteractive(in, out, options)
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

type nativeKey struct {
	Name string
	Text string
}

func runNativeInteractive(in io.Reader, out io.Writer, options Options) (Result, error) {
	query := strings.TrimSpace(options.InitialQuery)
	selected := 0
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\r\n")

	for {
		items := FilterItems(options.Items, query)
		if selected >= len(items) {
			selected = len(items) - 1
		}
		if selected < 0 {
			selected = 0
		}
		renderNativeInteractive(out, options, items, query, selected)

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
			switch action.Intent {
			case ActionClose:
				return Result{Key: action.Key, Query: query, Closed: true}, nil
			case ActionCustom:
				return Result{Key: action.Key, Value: selectedNativeValue(items, selected), Query: query}, nil
			case ActionAccept:
				if options.AcceptQuery {
					return Result{Key: action.Key, Query: query}, nil
				}
				return Result{Key: action.Key, Value: selectedNativeValue(items, selected), Query: query}, nil
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
		case "backspace":
			query = trimLastRune(query)
			selected = 0
		case "delete":
			// The native picker has no cursor movement yet, so Delete is a no-op.
		case "ctrl-u":
			query = ""
			selected = 0
		case "ctrl-w":
			query = trimLastWord(query)
			selected = 0
		default:
			if key.Text != "" {
				query += key.Text
				selected = 0
			}
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
	case 0x03:
		return nativeKey{Name: "ctrl-c"}, nil
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
		switch next {
		case 'A':
			return nativeKey{Name: "up"}, nil
		case 'B':
			return nativeKey{Name: "down"}, nil
		case 'C':
			return nativeKey{Name: "right"}, nil
		case 'D':
			return nativeKey{Name: "left"}, nil
		case 'H':
			return nativeKey{Name: "home"}, nil
		case 'F':
			return nativeKey{Name: "end"}, nil
		case '1':
			return readNativeCSI1Key(r)
		case '5':
			_, _, _ = readNativeByteMaybe(r)
			return nativeKey{Name: "page-up"}, nil
		case '6':
			_, _, _ = readNativeByteMaybe(r)
			return nativeKey{Name: "page-down"}, nil
		case '3':
			_, _, _ = readNativeByteMaybe(r)
			return nativeKey{Name: "delete"}, nil
		default:
			return nativeKey{Name: "esc"}, nil
		}
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
	return nativeKey{Name: "esc"}, nil
}

func readNativeCSI1Key(r io.Reader) (nativeKey, error) {
	sep, ok, err := readNativeByteMaybe(r)
	if err != nil {
		return nativeKey{}, err
	}
	if !ok {
		return nativeKey{Name: "esc"}, nil
	}
	if sep != ';' {
		return nativeKey{Name: "esc"}, nil
	}
	mod, ok, err := readNativeByteMaybe(r)
	if err != nil {
		return nativeKey{}, err
	}
	if !ok {
		return nativeKey{Name: "esc"}, nil
	}
	final, ok, err := readNativeByteMaybe(r)
	if err != nil {
		return nativeKey{}, err
	}
	if !ok {
		return nativeKey{Name: "esc"}, nil
	}
	name := ""
	switch final {
	case 'A':
		name = "up"
	case 'B':
		name = "down"
	case 'C':
		name = "right"
	case 'D':
		name = "left"
	default:
		return nativeKey{Name: "esc"}, nil
	}
	switch mod {
	case '3':
		return nativeKey{Name: "alt-" + name}, nil
	case '5':
		return nativeKey{Name: "ctrl-" + name}, nil
	default:
		return nativeKey{Name: name}, nil
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
	return 0, false, nil
}

func trimLastRune(value string) string {
	if value == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(value)
	if size <= 0 {
		return ""
	}
	return value[:len(value)-size]
}

func trimLastWord(value string) string {
	value = strings.TrimRight(value, " \t")
	if value == "" {
		return ""
	}
	cut := strings.LastIndexAny(value, " \t")
	if cut < 0 {
		return ""
	}
	return value[:cut+1]
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

func renderNativeInteractive(w io.Writer, options Options, items []Item, query string, selected int) {
	fmt.Fprint(w, "\x1b[2J\x1b[H")
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(w, header)
	}
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		prompt = "projmux " + strings.TrimSpace(options.UI) + ">"
	}
	fmt.Fprintf(w, "%s %s\n", prompt, query)
	fmt.Fprintln(w, "Type to search | Up/Down select | Enter choose | Esc close")
	if footer := strings.TrimSpace(options.Footer); footer != "" {
		fmt.Fprintln(w, footer)
	}
	fmt.Fprintln(w)

	start, end := nativeVisibleRange(len(items), selected, nativePageSize)
	if len(items) == 0 {
		fmt.Fprintln(w, "  no matches")
		return
	}
	if start > 0 {
		fmt.Fprintf(w, "  ... %d more above\n", start)
	}
	for i := start; i < end; i++ {
		renderNativeInteractiveItem(w, items[i], i == selected)
	}
	if end < len(items) {
		fmt.Fprintf(w, "  ... %d more below\n", len(items)-end)
	}
	renderNativePreview(w, options, items, selected)
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

func renderNativeInteractiveItem(w io.Writer, item Item, selected bool) {
	lines := strings.Split(item.EffectiveLabel(), "\n")
	prefix := "  "
	if selected {
		prefix = "> "
		fmt.Fprint(w, "\x1b[7m")
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	fmt.Fprintf(w, "%s%s", prefix, strings.TrimRight(lines[0], "\r"))
	if selected {
		fmt.Fprint(w, "\x1b[0m")
	}
	fmt.Fprintln(w)
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "    %s\n", strings.TrimRight(line, "\r"))
	}
	for _, meta := range item.MetaLines {
		if meta = strings.TrimSpace(meta); meta != "" {
			fmt.Fprintf(w, "    %s\n", meta)
		}
	}
}

func renderNativePreview(w io.Writer, options Options, items []Item, selected int) {
	command := strings.TrimSpace(options.Preview.Command)
	if command == "" || selected < 0 || selected >= len(items) {
		return
	}
	target := strings.TrimSpace(items[selected].PreviewTarget)
	if target == "" {
		target = items[selected].Value
	}
	if target == "" {
		return
	}
	output := runNativePreviewCommand(command, target)
	if strings.TrimSpace(output) == "" {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "──────────────── preview ────────────────")
	for _, line := range limitedNativePreviewLines(output, 18) {
		fmt.Fprintln(w, line)
	}
}

func runNativePreviewCommand(command, target string) string {
	command = strings.ReplaceAll(command, "{2}", shellQuoteNative(target))
	command = strings.ReplaceAll(command, "{}", shellQuoteNative(target))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out)
}

func limitedNativePreviewLines(output string, limit int) []string {
	output = strings.TrimRight(output, "\r\n")
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
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

func fuzzyMatch(source, query string) bool {
	if query == "" {
		return true
	}
	sourceRunes := []rune(source)
	index := 0
	for _, ch := range query {
		found := false
		for index < len(sourceRunes) {
			if sourceRunes[index] == ch {
				index++
				found = true
				break
			}
			index++
		}
		if !found {
			return false
		}
	}
	return true
}
