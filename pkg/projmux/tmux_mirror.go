package projmux

import (
	"context"
	"errors"
	"strings"

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
// $TMUX_PANE and $TMUX that lookupEnv returns. The SDK never reads the process
// environment itself, so a caller passes its own environment lookup, such as
// os.Getenv. Both values must be non-blank for Inside to be true. A nil
// lookupEnv is outside tmux: no environment is read and tmux is not contacted.
//
// Every tmux call is pinned to the server $TMUX names: the socket path is the
// part of $TMUX before the first comma and each call runs as `tmux -S <path>`,
// so the server asked is the one the decision was made on, not the one the
// process environment happens to name. A $TMUX that names no socket (it starts
// with a comma) still gives Inside and PaneID, but PaneUID and WindowUID are
// empty and tmux is not run.
//
// Unlike the CLI, which reads only the uid its route needs, this reads both the
// Pane and the Window uid when inside.
func ReadTmuxMirror(ctx context.Context, lookupEnv func(string) string) TmuxMirror {
	return readTmuxMirror(ctx, tmuxServerRunner(tmux.ExecRunner{}, lookupEnv), func() (string, bool) {
		return store.InheritedTmuxPane(lookupEnv)
	})
}

// ReadTmuxMirrorAt reads the mirror of an explicitly named tmux pane, for a
// caller that runs inside a tmux client but not inside that pane. $TMUX must
// still be non-blank in lookupEnv and paneID must be non-blank after trimming;
// otherwise (including a nil lookupEnv) the result is the empty not-inside value
// and tmux is not contacted. The server is chosen from lookupEnv's $TMUX exactly
// as ReadTmuxMirror describes.
func ReadTmuxMirrorAt(ctx context.Context, paneID string, lookupEnv func(string) string) TmuxMirror {
	return readTmuxMirror(ctx, tmuxServerRunner(tmux.ExecRunner{}, lookupEnv), func() (string, bool) {
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

// errNoTmuxSocket is the read failure of a $TMUX value that names no socket.
var errNoTmuxSocket = errors.New("projmux: $TMUX names no tmux socket")

// tmuxServerRunner pins every tmux call to the server that lookupEnv's $TMUX
// names, so the server a read asks is the one the inside-tmux decision was made
// on rather than whichever server the process environment happens to name. The
// socket path is the part of $TMUX before the first comma, the rule a tmux
// client applies to $TMUX, and each call is prefixed with `-S <path>`. A $TMUX
// that names no socket (empty before the first comma) fails every call without
// running tmux, which reads as an empty uid and never falls back to the process
// environment's server.
func tmuxServerRunner(inner store.Runner, lookupEnv func(string) string) store.Runner {
	socket := ""
	if lookupEnv != nil {
		socket, _, _ = strings.Cut(strings.TrimSpace(lookupEnv("TMUX")), ",")
	}
	return socketRunner{inner: inner, socket: socket}
}

type socketRunner struct {
	inner  store.Runner
	socket string
}

func (r socketRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.socket == "" {
		return nil, errNoTmuxSocket
	}
	return r.inner.Run(ctx, name, append([]string{"-S", r.socket}, args...)...)
}
