package app

import (
	"errors"
	"os"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// deleteSocketFlags are the two mutually exclusive exact-target flags every
// registry delete accepts.
type deleteSocketFlags struct {
	socket     string
	socketPath string
}

// resolveDeleteTarget fixes the one tmux server a delete's live half addresses.
//
// It is the same precedence `reconcile resources` uses -- explicit --socket,
// explicit --socket-path, inherited absolute $TMUX -- and it deliberately has
// no fourth branch.
//
// The removed fourth branch was a hardcoded `-L projmux`. That default is what
// made a delete issued from an isolated server inventory and kill objects on
// the app's own socket instead: the route asked one server for its resources
// and answered by mutating another. Refusing outside tmux is the only remaining
// honest answer, and it names the two flags that fix it.
func resolveDeleteTarget(spelling string, flags deleteSocketFlags, lookupEnv func(string) string) (tmuxTransport, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{
		SocketName:    flags.socket,
		SocketPath:    flags.socketPath,
		InheritedTMUX: lookupEnv("TMUX"),
	})
	if err != nil {
		if errors.Is(err, resourcegraph.ErrTransportConflict) {
			return tmuxTransport{}, usageError(spelling + " accepts only one of --socket and --socket-path")
		}
		return tmuxTransport{}, usageError(spelling + ": --socket-path must be absolute")
	}
	if !target.Present() {
		return tmuxTransport{}, usageError(spelling +
			" requires --socket <name> or --socket-path <absolute> outside tmux")
	}
	return target, nil
}
