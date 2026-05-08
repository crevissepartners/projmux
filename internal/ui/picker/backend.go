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
	defaultNativeRows = 30
	defaultNativeCols = 100
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
	selected := options.InitialIndex
	focusedValue := ""
	layout := detectNativeLayout(in)
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
		if value := selectedNativeValue(items, selected); value != focusedValue {
			runNativeFocusAction(options.Actions, value)
			focusedValue = value
		}
		renderNativeInteractive(out, options, items, query, selected, layout)

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
				if strings.TrimSpace(action.Command) != "" {
					runNativeActionCommand(action.Command, selectedNativeValue(items, selected))
					continue
				}
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
	name := nativeBaseCSIName(final, params)
	if name == "" {
		return nativeKey{Name: "esc"}
	}
	mod := nativeCSIModifier(params)
	switch mod {
	case "3":
		return nativeKey{Name: "alt-" + name}
	case "5":
		return nativeKey{Name: "ctrl-" + name}
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
	for attempt := 0; attempt < 3; attempt++ {
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

func renderNativeInteractive(w io.Writer, options Options, items []Item, query string, selected int, layout nativeLayout) {
	fmt.Fprint(w, "\x1b[2J\x1b[H")
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
	previewLimit := maxInt(4, layout.Rows-8)
	if placement == "down" {
		previewLimit = previewHeight
	}
	previewLines := nativePreviewLines(options, items, selected, previewLimit)
	listLimit := nativePageSize
	if len(previewLines) > 0 && placement == "right" && layout.Cols >= 88 {
		listLimit = maxInt(6, layout.Rows-7)
	} else if len(previewLines) > 0 && placement == "down" {
		listLimit = maxInt(4, layout.Rows-previewHeight-8)
	}
	start, end := nativeVisibleRange(len(items), selected, listLimit)
	listLines := nativeInteractiveListLines(items, start, end, selected)
	if len(items) == 0 {
		fmt.Fprintln(w, "  no matches")
		return
	}
	if start > 0 {
		listLines = append([]string{fmt.Sprintf("  ... %d more above", start)}, listLines...)
	}
	if end < len(items) {
		listLines = append(listLines, fmt.Sprintf("  ... %d more below", len(items)-end))
	}
	if len(previewLines) > 0 && placement == "right" && layout.Cols >= 88 {
		renderNativeSplitPreview(w, listLines, previewLines, layout)
		return
	}
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

func nativeInteractiveListLines(items []Item, start, end, selected int) []string {
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, nativeInteractiveItemLines(items[i], i == selected)...)
	}
	return lines
}

func nativeInteractiveItemLines(item Item, selected bool) []string {
	lines := strings.Split(item.EffectiveLabel(), "\n")
	prefix := "  "
	if selected {
		prefix = "> "
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	rendered := make([]string, 0, len(lines)+len(item.MetaLines))
	first := fmt.Sprintf("%s%s", prefix, strings.TrimRight(lines[0], "\r"))
	if selected {
		first = "\x1b[7m" + first + "\x1b[0m"
	}
	rendered = append(rendered, first)
	for _, line := range lines[1:] {
		rendered = append(rendered, fmt.Sprintf("    %s", strings.TrimRight(line, "\r")))
	}
	for _, meta := range item.MetaLines {
		if meta = strings.TrimSpace(meta); meta != "" {
			rendered = append(rendered, fmt.Sprintf("    %s", meta))
		}
	}
	return rendered
}

func nativePreviewLines(options Options, items []Item, selected, limit int) []string {
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
	return limitedNativePreviewLines(output, limit)
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

func renderNativeSplitPreview(w io.Writer, listLines, previewLines []string, layout nativeLayout) {
	previewWidth := nativePreviewWidth(layout.Cols)
	listWidth := layout.Cols - previewWidth - 3
	if listWidth < 32 {
		listWidth = 32
		previewWidth = layout.Cols - listWidth - 3
	}
	rows := maxInt(len(listLines), len(previewLines)+1)
	fmt.Fprintf(w, "%s | %s\n", nativePadRight("", listWidth), "preview")
	for i := 0; i < rows; i++ {
		left := ""
		if i < len(listLines) {
			left = listLines[i]
		}
		right := ""
		if i < len(previewLines) {
			right = previewLines[i]
		}
		fmt.Fprintf(w, "%s | %s\n", nativePadRight(nativeTruncateANSI(left, listWidth), listWidth), nativeTruncateANSI(right, previewWidth))
	}
}

func nativePreviewWidth(cols int) int {
	if cols <= 0 {
		cols = defaultNativeCols
	}
	width := cols * 55 / 100
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
	fmt.Fprintln(w)
	fmt.Fprintln(w, nativeTruncateANSI(strings.Repeat("-", width), width))
	fmt.Fprintln(w, "preview")
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
