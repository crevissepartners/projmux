package app

import (
	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func layoutPresetSource(name string, preset corelayout.Preset) string {
	if preset.Normalize().Mode == corelayout.ModeFreshEachTime {
		return sessionstate.SourceFresh
	}
	return sessionstate.LayoutSource(name)
}
