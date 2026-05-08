package app

import (
	"fmt"
	"strconv"
	"strings"

	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func runPickerOptionBackend(lookupEnv func(string) string, native intpicker.Runner, fzf intfzf.Runner, options intfzf.Options) (intfzf.Result, error) {
	if intpicker.ResolveBackend(lookupEnv) == intpicker.BackendNative {
		if native == nil {
			return intfzf.Result{}, fmt.Errorf("native picker is not configured")
		}
		result, err := native.Run(pickerOptionsFromFZF(options))
		return fzfResultFromPicker(result), err
	}
	if fzf == nil {
		return intfzf.Result{}, fmt.Errorf("fzf picker is not configured")
	}
	return fzf.Run(options)
}

func pickerOptionsFromFZF(options intfzf.Options) intpicker.Options {
	return intpicker.Options{
		UI:           options.UI,
		Items:        pickerItemsFromFZF(options),
		Prompt:       options.Prompt,
		Header:       options.Header,
		Footer:       options.Footer,
		Actions:      pickerActionsFromFZF(options),
		Preview:      intpicker.Preview{Command: options.PreviewCommand, Window: options.PreviewWindow},
		InitialQuery: options.InitialQuery,
		InitialIndex: pickerInitialIndexFromFZF(options),
		AcceptQuery:  options.AcceptQuery,
		MultiLine:    options.Read0,
	}
}

func pickerItemsFromFZF(options intfzf.Options) []intpicker.Item {
	if len(options.Entries) != 0 {
		return pickerItemsFromFZFEntries(options.Entries)
	}
	items := make([]intpicker.Item, 0, len(options.Candidates))
	for _, candidate := range options.Candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		items = append(items, intpicker.Item{
			Label:      candidate,
			Title:      candidate,
			Value:      candidate,
			SearchText: candidate,
		})
	}
	return items
}

func pickerItemsFromFZFEntries(entries []intfzf.Entry) []intpicker.Item {
	items := make([]intpicker.Item, 0, len(entries))
	for _, entry := range entries {
		items = append(items, intpicker.Item{
			Label:      entry.Label,
			Title:      entry.Label,
			Value:      entry.Value,
			SearchText: entry.SearchKey,
		})
	}
	return items
}

func pickerActionsFromFZF(options intfzf.Options) []intpicker.Action {
	actions := make([]intpicker.Action, 0, len(options.ExpectKeys)+len(options.Bindings))
	for _, key := range options.ExpectKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionAccept})
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
			actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionClose})
		default:
			actions = append(actions, intpicker.Action{
				Key:     key,
				Intent:  intpicker.ActionCustom,
				Command: pickerCommandFromFZFBinding(action),
			})
		}
	}
	return actions
}

func pickerInitialIndexFromFZF(options intfzf.Options) int {
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
		idx := strings.Index(rest, ")")
		if idx < 0 {
			continue
		}
		pos, err := strconv.Atoi(strings.TrimSpace(rest[:idx]))
		if err == nil && pos > 0 {
			return pos - 1
		}
	}
	return 0
}

func pickerCommandFromFZFBinding(action string) string {
	action = strings.TrimSpace(action)
	const prefix = "execute-silent("
	if !strings.HasPrefix(action, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(action, prefix)
	idx := strings.Index(rest, ")")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:idx])
}

func fzfResultFromPicker(result intpicker.Result) intfzf.Result {
	return intfzf.Result{
		Key:   result.Key,
		Value: result.Value,
		Query: result.Query,
	}
}
