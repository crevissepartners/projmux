package pickercompat

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestOptionsFromPickerMapsItemsActionsAndPreview(t *testing.T) {
	t.Parallel()

	options := OptionsFromPicker(picker.Options{
		UI:              "switch",
		MultiLine:       true,
		Title:           "Projects",
		Prompt:          "> ",
		Header:          "header",
		Footer:          "footer",
		Preview:         picker.Preview{Command: "preview {2}", Window: "right"},
		InitialIndex:    0,
		InitialIndexSet: true,
		DisableSearch:   true,
		AcceptQuery:     true,
		Items: []picker.Item{{
			Label:      "API\n  branch main",
			Title:      "api",
			Value:      "/repo/api",
			SearchText: "api service",
		}},
		Actions: append(
			picker.CloseActions("esc", "alt-4"),
			append(
				picker.CustomActions("ctrl-x"),
				picker.Action{Key: "right", Intent: picker.ActionCustom, Command: "cycle {2}", Refresh: true},
			)...,
		),
	})

	if !options.Read0 {
		t.Fatalf("Read0 = false, want true")
	}
	if !options.DisableSearch || !options.AcceptQuery {
		t.Fatalf("DisableSearch/AcceptQuery = %t/%t, want true/true", options.DisableSearch, options.AcceptQuery)
	}
	if got, want := options.Entries, []Entry{{Label: "API\n  branch main", Value: "/repo/api", SearchKey: "api service"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries = %#v, want %#v", got, want)
	}
	if got, want := options.Title, "Projects"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if got, want := options.Bindings, []string{
		"esc:abort",
		"alt-4:abort",
		"right:execute-silent(cycle {2})+refresh-preview",
		"start:pos(1)",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bindings = %#v, want %#v", got, want)
	}
	if got, want := options.ExpectKeys, []string{"ctrl-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpectKeys = %#v, want %#v", got, want)
	}
	if options.PreviewCommand != "preview {2}" || options.PreviewWindow != "right" {
		t.Fatalf("preview = %q/%q, want command/window", options.PreviewCommand, options.PreviewWindow)
	}
}

func TestPickerOptionsMapsCompatBindingsToContractActions(t *testing.T) {
	t.Parallel()

	options := PickerOptions(Options{
		UI:            "switch",
		Entries:       []Entry{{Label: "api", Value: "/repo/api", SearchKey: "api service"}},
		Title:         "Projects",
		Read0:         true,
		DisableSearch: true,
		AcceptQuery:   true,
		Bindings:      []string{"esc:abort", "right:execute-silent(cycle {2})+refresh-preview", "start:pos(1)"},
	})

	if got, want := options.UI, "switch"; got != want {
		t.Fatalf("UI = %q, want %q", got, want)
	}
	if got, want := options.Title, "Projects"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if !options.MultiLine {
		t.Fatal("MultiLine = false, want true")
	}
	if !options.DisableSearch || !options.AcceptQuery {
		t.Fatalf("DisableSearch/AcceptQuery = %t/%t, want true/true", options.DisableSearch, options.AcceptQuery)
	}
	if len(options.Items) != 1 || options.Items[0].SearchText != "api service" {
		t.Fatalf("Items = %#v, want compat entry mapped to picker item", options.Items)
	}
	if len(options.Actions) != 2 {
		t.Fatalf("Actions = %#v, want close and command actions", options.Actions)
	}
	if got := options.Actions[0]; got.Key != "esc" || got.Intent != picker.ActionClose {
		t.Fatalf("close action = %#v, want esc close", got)
	}
	if got := options.Actions[1]; got.Key != "right" || got.Command != "cycle {2}" || !got.Refresh {
		t.Fatalf("command action = %#v, want refresh command action", got)
	}
	if options.InitialIndex != 0 || !options.InitialIndexSet {
		t.Fatalf("InitialIndex = %d/%t, want explicit zero index", options.InitialIndex, options.InitialIndexSet)
	}
}

func TestPickerOptionsPreservesRecorderStateSlice(t *testing.T) {
	t.Parallel()

	recorder := &picker.RecorderOptions{}
	options := PickerOptions(Options{
		DisableSearch: true,
		Recorder:      recorder,
	})
	if options.Recorder != recorder {
		t.Fatalf("PickerOptions recorder = %p, want %p", options.Recorder, recorder)
	}
	if roundTrip := OptionsFromPicker(options); roundTrip.Recorder != recorder {
		t.Fatalf("OptionsFromPicker recorder = %p, want %p", roundTrip.Recorder, recorder)
	}
}

func TestResultToPickerMarksEmptyResultClosed(t *testing.T) {
	t.Parallel()

	if got := ResultToPicker(Result{}); !got.Closed {
		t.Fatalf("ResultToPicker(empty).Closed = false, want true")
	}
	if got := ResultToPicker(Result{Key: "ctrl-x"}); got.Closed {
		t.Fatalf("ResultToPicker(key).Closed = true, want false")
	}
}
