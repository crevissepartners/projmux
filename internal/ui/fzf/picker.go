package fzf

import (
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

	fzfOptions := Options{
		UI:             options.UI,
		Entries:        entries,
		Read0:          options.MultiLine,
		Prompt:         options.Prompt,
		Header:         options.Header,
		Footer:         options.Footer,
		InitialQuery:   options.InitialQuery,
		PreviewCommand: options.Preview.Command,
		PreviewWindow:  options.Preview.Window,
	}
	for _, action := range options.Actions {
		key := strings.TrimSpace(action.Key)
		if key == "" {
			continue
		}
		switch action.Intent {
		case picker.ActionClose:
			fzfOptions.Bindings = append(fzfOptions.Bindings, key+":abort")
		case picker.ActionAccept, picker.ActionCustom:
			fzfOptions.ExpectKeys = append(fzfOptions.ExpectKeys, key)
		}
	}
	return fzfOptions
}

func ResultToPicker(result Result) picker.Result {
	return picker.Result{
		Key:    result.Key,
		Value:  result.Value,
		Query:  result.Query,
		Closed: result.Value == "" && result.Key == "",
	}
}
