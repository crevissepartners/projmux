package pickercompat

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestPickerOptionsMapsCompatBindingsToContractActions(t *testing.T) {
	t.Parallel()

	options := PickerOptions(Options{
		UI:            "switch",
		Entries:       []Entry{{Label: "api", Value: "/repo/api", SearchKey: "api service"}},
		Title:         "Projects",
		ChromeBands:   []picker.ChromeBand{{Label: "Codex", Value: "fallback"}},
		MoreNotLoaded: true,
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
	if len(options.ChromeBands) != 1 || options.ChromeBands[0].Value != "fallback" || !options.MoreNotLoaded {
		t.Fatalf("fixed chrome = %#v more=%t", options.ChromeBands, options.MoreNotLoaded)
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
}
