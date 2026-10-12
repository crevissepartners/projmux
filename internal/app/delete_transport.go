package app

import (
	"os"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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
	target, err := mutationruntime.ResolveDeleteTarget(spelling, flags.socket, flags.socketPath, lookupEnv)
	if err != nil {
		return target, usageError(err.Error())
	}
	return target, nil
}
