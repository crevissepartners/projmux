package runtime

import (
	"errors"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// ResolveDeleteTarget admits only explicit flags or the caller-supplied CLI
// inherited route. A nil lookup means no ambient context, as required by SDK.
func ResolveDeleteTarget(spelling, socket, socketPath string, lookupEnv func(string) string) (TmuxTransport, error) {
	inherited := ""
	if lookupEnv != nil {
		inherited = lookupEnv("TMUX")
	}
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketName: socket, SocketPath: socketPath, InheritedTMUX: inherited})
	if err != nil {
		message := spelling + ": --socket-path must be absolute"
		if errors.Is(err, resourcegraph.ErrTransportConflict) {
			message = spelling + " accepts only one of --socket and --socket-path"
		}
		return TmuxTransport{}, &mutations.Failure{Code: "runtime-request-invalid", Cause: errors.New(message)}
	}
	if !target.Present() {
		return TmuxTransport{}, &mutations.Failure{Code: "delete-transport-required", Cause: errors.New(spelling + " requires --socket <name> or --socket-path <absolute> outside tmux")}
	}
	return target, nil
}
