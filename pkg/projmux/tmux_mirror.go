package projmux

import (
	"context"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// TmuxMirror is the Projmux identity mirrored onto one tmux pane.
//
// Inside is false when the caller is not inside a tmux client (or the named
// pane has no client anchor); every other field is then empty and tmux was not
// contacted. PaneUID and WindowUID are empty when the pane carries no mirror or
// the read failed: a read failure is an empty value, never an error.
type TmuxMirror struct {
	PaneID    string
	PaneUID   string
	WindowUID string
	Inside    bool
}

// ReadTmuxMirror reads the mirror of the pane the caller runs in, from the
// $TMUX_PANE and $TMUX that lookupEnv returns; the SDK never reads the process
// environment itself, so a caller passes its own environment lookup, such as os.Getenv. Both values must
// be non-blank for Inside to be true. A nil lookupEnv is outside tmux: no
// environment is read and tmux is not contacted. Unlike the CLI, which reads only the uid its route needs, this reads
// both the Pane and the Window uid when inside.
func ReadTmuxMirror(ctx context.Context, lookupEnv func(string) string) TmuxMirror {
	return readTmuxMirror(ctx, tmux.ExecRunner{}, func() (string, bool) {
		return store.InheritedTmuxPane(lookupEnv)
	})
}

// ReadTmuxMirrorAt reads the mirror of an explicitly named tmux pane, for a
// caller that runs inside a tmux client but not inside that pane. $TMUX must
// still be non-blank in lookupEnv and paneID must be non-blank after trimming;
// otherwise (including a nil lookupEnv) the result is the empty not-inside value
// and tmux is not contacted.
func ReadTmuxMirrorAt(ctx context.Context, paneID string, lookupEnv func(string) string) TmuxMirror {
	return readTmuxMirror(ctx, tmux.ExecRunner{}, func() (string, bool) {
		return store.AnchoredTmuxPane(paneID, lookupEnv)
	})
}

// readTmuxMirror is the private seam: the runner and the pane decision are
// injected so tests record tmux calls without contacting a server.
func readTmuxMirror(ctx context.Context, runner store.Runner, pane func() (string, bool)) TmuxMirror {
	paneID, inside := pane()
	if !inside {
		return TmuxMirror{}
	}
	mirror := store.NewMirror(runner)
	return TmuxMirror{
		PaneID:    paneID,
		PaneUID:   mirror.MirroredPaneUID(ctx, paneID),
		WindowUID: mirror.MirroredWindowUID(ctx, paneID),
		Inside:    true,
	}
}
