package fzf

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestOptionsFromPickerMapsItemsActionsAndPreview(t *testing.T) {
	t.Parallel()

	options := OptionsFromPicker(picker.Options{
		UI:        "switch",
		MultiLine: true,
		Prompt:    "> ",
		Header:    "header",
		Footer:    "footer",
		Preview:   picker.Preview{Command: "preview {2}", Window: "right"},
		Items: []picker.Item{{
			Label:      "API\n  branch main",
			Title:      "api",
			Value:      "/repo/api",
			SearchText: "api service",
		}},
		Actions: append(
			picker.CloseActions("esc", "alt-4"),
			picker.CustomActions("ctrl-x")...,
		),
	})

	if !options.Read0 {
		t.Fatalf("Read0 = false, want true")
	}
	if got, want := options.Entries, []Entry{{Label: "API\n  branch main", Value: "/repo/api", SearchKey: "api service"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries = %#v, want %#v", got, want)
	}
	if got, want := options.Bindings, []string{"esc:abort", "alt-4:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Bindings = %#v, want %#v", got, want)
	}
	if got, want := options.ExpectKeys, []string{"ctrl-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpectKeys = %#v, want %#v", got, want)
	}
	if options.PreviewCommand != "preview {2}" || options.PreviewWindow != "right" {
		t.Fatalf("preview = %q/%q, want command/window", options.PreviewCommand, options.PreviewWindow)
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
