package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// tmuxTransport is only a package-local spelling of the resourcegraph owner.
// It adds no fields or routing rules: raw name/path/inherited precedence is
// decided exclusively by resourcegraph.ResolveTransport.
type TmuxTransport = resourcegraph.Transport

const (
	TmuxSocketName       = resourcegraph.TransportSocketName
	TmuxSocketPath       = resourcegraph.TransportSocketPath
	TmuxSocketNameSource = resourcegraph.TransportSourceSocketName
	TmuxSocketPathSource = resourcegraph.TransportSourceSocketPath
	TmuxInheritedSource  = resourcegraph.TransportSourceInheritedEnv
)

func TmuxSocketNameTarget(name string) (TmuxTransport, error) {
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketName: name})
	if err != nil || !target.Present() {
		return TmuxTransport{}, errors.New("binding convergence requires an explicit tmux socket name")
	}
	return target, nil
}

func TmuxSocketPathTarget(path string) (TmuxTransport, error) {
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketPath: path})
	if err != nil || !target.Present() {
		return TmuxTransport{}, errors.New("binding convergence requires an absolute tmux socket path")
	}
	return target, nil
}

// explicitTmuxRunner routes every tmux read and write through one exact server.
// It is deliberately below the existing registryReconciler and metadata Mirror,
// so matcher/reconciler/writer behavior is reused without adding a uid writer.
type ExplicitTmuxRunner struct {
	Runner TmuxCommandRunner
	Target TmuxTransport
}

func (r ExplicitTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.Runner == nil {
		return nil, errors.New("binding convergence requires a tmux runner")
	}
	if name != "tmux" {
		return nil, fmt.Errorf("binding convergence cannot route executable %q", name)
	}
	if !r.Target.Present() {
		return nil, errors.New("binding convergence has no explicit tmux target")
	}
	routed := make([]string, 0, len(args)+2)
	routed = append(routed, r.Target.Args()...)
	routed = append(routed, args...)
	return r.Runner.Run(ctx, name, routed...)
}
