package app

import (
	"fmt"
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
		Items:        pickerItemsFromFZFEntries(options.Entries),
		Prompt:       options.Prompt,
		Header:       options.Header,
		Footer:       options.Footer,
		Actions:      pickerActionsFromFZF(options),
		Preview:      intpicker.Preview{Command: options.PreviewCommand, Window: options.PreviewWindow},
		InitialQuery: options.InitialQuery,
		AcceptQuery:  options.AcceptQuery,
		MultiLine:    options.Read0,
	}
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
		switch strings.TrimSpace(action) {
		case "abort":
			actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionClose})
		default:
			actions = append(actions, intpicker.Action{Key: key, Intent: intpicker.ActionCustom})
		}
	}
	return actions
}

func fzfResultFromPicker(result intpicker.Result) intfzf.Result {
	return intfzf.Result{
		Key:   result.Key,
		Value: result.Value,
		Query: result.Query,
	}
}
