package pickercompat

import (
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

type Options struct {
	UI             string
	Candidates     []string
	Entries        []Entry
	Read0          bool
	Title          string
	TitleChips     []projmuxpicker.Chip
	Prompt         string
	Header         string
	Footer         string
	Locale         i18n.Locale
	ExpectKeys     []string
	PreviewCommand string
	PreviewWindow  string
	Theme          *theme.EffectiveTheme
	Bindings       []string
	InitialQuery   string
	// DisableSearch makes the legacy option shape a navigation-only list.
	DisableSearch bool
	// AcceptQuery surfaces the user-typed query alongside any selection.
	AcceptQuery bool
	// ColorGrid renders the native picker as an xterm-256 swatch grid with a
	// live preview instead of a filtered list.
	ColorGrid bool
	// Recorder is native-only purpose-built input state carried through the
	// compatibility option shape used by Settings.
	Recorder *picker.RecorderOptions
	// DeferredUpdate, when set, is run by the native picker after the first
	// render to fill in expensive fields (e.g. a background pass that computes
	// per-row data) without blocking the initial list. DeferredUpdateTrigger, if
	// non-nil, re-runs it on every signal; nil runs it once. Both pass straight
	// through to the native picker Options.
	DeferredUpdate        func() (picker.DeferredUpdate, error)
	DeferredUpdateTrigger <-chan struct{}
}

type Entry struct {
	Label     string
	Value     string
	SearchKey string
}

type Result struct {
	Key   string
	Value string
	Query string
}

type Runner interface {
	Run(options Options) (Result, error)
}
