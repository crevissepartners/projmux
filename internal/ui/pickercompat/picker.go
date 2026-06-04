package pickercompat

import (
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/ui/picker"
)

func OptionsFromPicker(options picker.Options) Options {
	entries := make([]Entry, 0, len(options.Items))
	for _, item := range options.Items {
		entries = append(entries, Entry{
			Label:     item.EffectiveLabel(),
			Value:     item.Value,
			SearchKey: item.EffectiveSearchText(),
		})
	}

	compatOptions := Options{
		UI:             options.UI,
		Entries:        entries,
		Read0:          options.MultiLine,
		Title:          options.Title,
		TitleChips:     options.TitleChips,
		Prompt:         options.Prompt,
		Header:         options.Header,
		Footer:         options.Footer,
		Locale:         options.Locale,
		InitialQuery:   options.InitialQuery,
		DisableSearch:  options.DisableSearch,
		AcceptQuery:    options.AcceptQuery,
		PreviewCommand: options.Preview.Command,
		PreviewWindow:  options.Preview.Window,
		Theme:          options.Theme,
	}
	for _, action := range options.Actions {
		key := strings.TrimSpace(action.Key)
		if key == "" {
			continue
		}
		switch action.Intent {
		case picker.ActionClose:
			compatOptions.Bindings = append(compatOptions.Bindings, key+":abort")
		case picker.ActionAccept, picker.ActionCustom:
			if action.Intent == picker.ActionCustom && strings.TrimSpace(action.Command) != "" {
				binding := key + ":execute-silent(" + strings.TrimSpace(action.Command) + ")"
				if action.Refresh {
					binding += "+refresh-preview"
				}
				compatOptions.Bindings = append(compatOptions.Bindings, binding)
				continue
			}
			compatOptions.ExpectKeys = append(compatOptions.ExpectKeys, key)
		}
	}
	if options.InitialIndexSet || options.InitialIndex > 0 {
		compatOptions.Bindings = append(compatOptions.Bindings, "start:pos("+strconv.Itoa(options.InitialIndex+1)+")")
	}
	return compatOptions
}

func ResultToPicker(result Result) picker.Result {
	return picker.Result{
		Key:    result.Key,
		Value:  result.Value,
		Query:  result.Query,
		Closed: result.Value == "" && result.Key == "",
	}
}

func PickerOptions(options Options) picker.Options {
	initialIndex, initialIndexSet := pickerInitialIndex(options)
	return picker.Options{
		UI:              options.UI,
		Items:           pickerItems(options),
		Title:           options.Title,
		TitleChips:      options.TitleChips,
		Prompt:          options.Prompt,
		Header:          options.Header,
		Footer:          options.Footer,
		Locale:          options.Locale,
		Actions:         pickerActions(options),
		Preview:         picker.Preview{Command: options.PreviewCommand, Window: options.PreviewWindow},
		Theme:           options.Theme,
		InitialQuery:    options.InitialQuery,
		InitialIndex:    initialIndex,
		InitialIndexSet: initialIndexSet,
		DisableSearch:   options.DisableSearch,
		AcceptQuery:     options.AcceptQuery,
		MultiLine:       options.Read0,
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

func PickerCommandFromBinding(action string) string {
	action = strings.TrimSpace(action)
	const prefix = "execute-silent("
	if !strings.HasPrefix(action, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(action, prefix)
	before, _, ok := strings.Cut(rest, ")")
	if !ok {
		return ""
	}
	return strings.TrimSpace(before)
}
