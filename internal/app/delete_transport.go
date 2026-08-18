package app

import (
	"os"
	"path/filepath"
	"strings"
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
func resolveDeleteTarget(spelling string, flags deleteSocketFlags, lookupEnv func(string) string) (explicitTmuxTarget, error) {
	name := strings.TrimSpace(flags.socket)
	path := strings.TrimSpace(flags.socketPath)
	if name != "" && path != "" {
		return explicitTmuxTarget{}, usageError(spelling + " accepts only one of --socket and --socket-path")
	}
	if name != "" {
		target, err := tmuxSocketNameTarget(name)
		if err != nil {
			return explicitTmuxTarget{}, usageError(spelling + ": --socket must not be empty")
		}
		return target, nil
	}
	if path != "" {
		target, err := tmuxSocketPathTarget(path)
		if err != nil {
			return explicitTmuxTarget{}, usageError(spelling + ": --socket-path must be absolute")
		}
		return target, nil
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	inherited, _, _ := strings.Cut(strings.TrimSpace(lookupEnv("TMUX")), ",")
	inherited = strings.TrimSpace(inherited)
	if inherited == "" || !filepath.IsAbs(inherited) {
		return explicitTmuxTarget{}, usageError(spelling +
			" requires --socket <name> or --socket-path <absolute> outside tmux")
	}
	target, err := tmuxSocketPathTarget(inherited)
	if err != nil {
		return explicitTmuxTarget{}, usageError(spelling + ": inherited $TMUX socket path is not absolute")
	}
	return target, nil
}
