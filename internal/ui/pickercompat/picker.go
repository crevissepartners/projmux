package pickercompat

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/ui/picker"
)

func PickerOptions(options Options) picker.Options {
	initialIndex, initialIndexSet := pickerInitialIndex(options)
	return picker.Options{
		UI:                    options.UI,
		Items:                 pickerItems(options),
		Title:                 options.Title,
		TitleChips:            options.TitleChips,
		Prompt:                options.Prompt,
		Header:                options.Header,
		Footer:                options.Footer,
		MoreNotLoaded:         options.MoreNotLoaded,
		Locale:                options.Locale,
		Actions:               pickerActions(options),
		Preview:               picker.Preview{Command: options.PreviewCommand, Window: options.PreviewWindow},
		SelectionDetail:       options.SelectionDetail,
		Theme:                 options.Theme,
		InitialQuery:          options.InitialQuery,
		InitialIndex:          initialIndex,
		InitialIndexSet:       initialIndexSet,
		DisableSearch:         options.DisableSearch,
		AcceptQuery:           options.AcceptQuery,
		ColorGrid:             options.ColorGrid,
		Recorder:              options.Recorder,
		MultiLine:             options.Read0,
		DeferredUpdate:        options.DeferredUpdate,
		DeferredUpdateTrigger: options.DeferredUpdateTrigger,
		FocusChanged:          options.FocusChanged,
	}
}

func ResultFromPicker(result picker.Result) Result {
	return Result{
		Key:   result.Key,
		Value: result.Value,
		Query: result.Query,
	}
}

func pickerItems(options Options) []picker.Item {
	if len(options.Entries) != 0 {
		return pickerItemsFromEntries(options.Entries)
	}
	items := make([]picker.Item, 0, len(options.Candidates))
	for _, candidate := range options.Candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		items = append(items, picker.Item{
			Label:      candidate,
			Title:      candidate,
			Value:      candidate,
			SearchText: candidate,
		})
	}
	return items
}

func pickerItemsFromEntries(entries []Entry) []picker.Item {
	items := make([]picker.Item, 0, len(entries))
	for _, entry := range entries {
		items = append(items, picker.Item{
			Label:      entry.Label,
			Title:      entry.Label,
			Value:      entry.Value,
			SearchText: entry.SearchKey,
			SearchOnly: entry.SearchOnly,
		})
	}
	return items
}

func pickerActions(options Options) []picker.Action {
	actions := make([]picker.Action, 0, len(options.ExpectKeys)+len(options.Bindings))
	for _, key := range options.ExpectKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, picker.Action{Key: key, Intent: picker.ActionAccept})
	}
	for _, binding := range options.Bindings {
		key, action, ok := strings.Cut(strings.TrimSpace(binding), ":")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		if strings.TrimSpace(key) == "start" {
			continue
		}
		switch strings.TrimSpace(action) {
		case "abort":
			actions = append(actions, picker.Action{Key: key, Intent: picker.ActionClose})
		default:
			actions = append(actions, picker.Action{
				Key:     key,
				Intent:  picker.ActionCustom,
				Command: PickerCommandFromBinding(action),
				Refresh: strings.Contains(action, "+refresh-preview"),
			})
		}
	}
	return actions
}

func pickerInitialIndex(options Options) (int, bool) {
	for _, binding := range options.Bindings {
		key, action, ok := strings.Cut(strings.TrimSpace(binding), ":")
		if !ok || strings.TrimSpace(key) != "start" {
			continue
		}
		action = strings.TrimSpace(action)
		const prefix = "pos("
		if !strings.HasPrefix(action, prefix) {
			continue
		}
		rest := strings.TrimPrefix(action, prefix)
		before, _, ok0 := strings.Cut(rest, ")")
		if !ok0 {
			continue
		}
		pos, err := strconv.Atoi(strings.TrimSpace(before))
		if err == nil && pos > 0 {
			return pos - 1, true
		}
	}
	return 0, false
}

// bindingActionTail matches the chained actions allowed after the closing
// paren of execute-silent(...), such as "+refresh-preview".
var bindingActionTail = regexp.MustCompile(`^(\+[A-Za-z][A-Za-z0-9-]*)*$`)

// PickerCommandFromBinding returns <cmd> from "execute-silent(<cmd>)[+<action>...]".
// The command is shell-quoted and may itself contain ")" (for example a binary
// path like /tmp/a)b/projmux), while the action tail never does, so the closing
// paren is the last ")" in the binding rather than the first.
func PickerCommandFromBinding(action string) string {
	action = strings.TrimSpace(action)
	const prefix = "execute-silent("
	if !strings.HasPrefix(action, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(action, prefix)
	end := strings.LastIndex(rest, ")")
	if end < 0 || !bindingActionTail.MatchString(rest[end+1:]) {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}
