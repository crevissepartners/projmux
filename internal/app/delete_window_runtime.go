package app

import (
	"os"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// windowDeleteRuntime is the live half of delete window. The route owns the
// resource transaction; this seam owns only exact-server inventory and exact
// tmux Window mutation.
type windowDeleteRuntime = mutationruntime.WindowDeleteRuntime

// newTmuxWindowDeleteRuntime builds the live half with no server bound yet.
// See newTmuxPaneDeleteRuntime for why there is no default target.
func newTmuxWindowDeleteRuntime() *tmuxWindowDeleteRuntime {
	return &tmuxWindowDeleteRuntime{
		Runner: inttmux.ExecRunner{},
		Getenv: os.Getenv,
	}
}
