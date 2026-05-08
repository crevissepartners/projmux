package picker

import (
	"fmt"
	"io"
	"strconv"
	"strings"
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

func (r NativeRunner) Run(options Options) (Result, error) {
	in := r.In
	if in == nil {
		return Result{Closed: true}, nil
	}
	out := r.Out
	if out == nil {
		out = io.Discard
	}

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
