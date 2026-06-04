package pickercompat

import (
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
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
