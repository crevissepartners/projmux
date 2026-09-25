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
		Footer:        "Showing latest 1 resume sessions.",
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
	if options.Footer != "Showing latest 1 resume sessions." || !options.MoreNotLoaded {
		t.Fatalf("footer = %q more=%t", options.Footer, options.MoreNotLoaded)
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

func TestPickerCommandFromBindingKeepsParensInsideCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
		want   string
	}{
		{name: "plain no tail", action: "execute-silent(cycle {2})", want: "cycle {2}"},
		{name: "plain refresh tail", action: "execute-silent(cycle {2})+refresh-preview", want: "cycle {2}"},
		{name: "outer whitespace trimmed", action: "  execute-silent( cycle {2} )+refresh-preview  ", want: "cycle {2}"},
		{name: "paren in path no tail", action: "execute-silent('/tmp/a)b/projmux' cycle {2})", want: "'/tmp/a)b/projmux' cycle {2}"},
		{name: "paren in path refresh tail", action: "execute-silent('/tmp/a)b/projmux' cycle {2} 'next')+refresh-preview", want: "'/tmp/a)b/projmux' cycle {2} 'next'"},
		{name: "paren in path multiple tail actions", action: "execute-silent('/tmp/(x)/projmux' cycle {2})+refresh-preview+other-action", want: "'/tmp/(x)/projmux' cycle {2}"},
		{name: "space and single quote in path", action: `execute-silent('/tmp/it'\''s a)dir/projmux' cycle {2})+refresh-preview`, want: `'/tmp/it'\''s a)dir/projmux' cycle {2}`},
		{name: "no prefix", action: "cycle {2})+refresh-preview", want: ""},
		{name: "other action", action: "execute(cycle {2})+refresh-preview", want: ""},
		{name: "no closing paren", action: "execute-silent(cycle {2}", want: ""},
		{name: "garbage after closing paren", action: "execute-silent(cycle {2})x", want: ""},
		{name: "garbage after tail", action: "execute-silent(cycle {2})+refresh-preview junk", want: ""},
		{name: "empty tail action", action: "execute-silent(cycle {2})+", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := PickerCommandFromBinding(tt.action); got != tt.want {
				t.Fatalf("PickerCommandFromBinding(%q) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}
