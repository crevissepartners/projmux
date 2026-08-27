package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// tmuxTransport is only a package-local spelling of the resourcegraph owner.
// It adds no fields or routing rules: raw name/path/inherited precedence is
// decided exclusively by resourcegraph.ResolveTransport.
type tmuxTransport = resourcegraph.Transport

const (
	tmuxSocketName       = resourcegraph.TransportSocketName
	tmuxSocketPath       = resourcegraph.TransportSocketPath
	tmuxSocketNameSource = resourcegraph.TransportSourceSocketName
	tmuxSocketPathSource = resourcegraph.TransportSourceSocketPath
	tmuxInheritedSource  = resourcegraph.TransportSourceInheritedEnv
)

func tmuxSocketNameTarget(name string) (tmuxTransport, error) {
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketName: name})
	if err != nil || !target.Present() {
		return tmuxTransport{}, errors.New("binding convergence requires an explicit tmux socket name")
	}
	return target, nil
}

func tmuxSocketPathTarget(path string) (tmuxTransport, error) {
	target, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketPath: path})
	if err != nil || !target.Present() {
		return tmuxTransport{}, errors.New("binding convergence requires an absolute tmux socket path")
	}
	return target, nil
}

// explicitTmuxRunner routes every tmux read and write through one exact server.
// It is deliberately below the existing registryReconciler and metadata Mirror,
// so matcher/reconciler/writer behavior is reused without adding a uid writer.
type explicitTmuxRunner struct {
	runner tmuxCommandRunner
	target tmuxTransport
}

func (r explicitTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.runner == nil {
		return nil, errors.New("binding convergence requires a tmux runner")
	}
	if name != "tmux" {
		return nil, fmt.Errorf("binding convergence cannot route executable %q", name)
	}
	if !r.target.Present() {
		return nil, errors.New("binding convergence has no explicit tmux target")
	}
	routed := make([]string, 0, len(args)+2)
	routed = append(routed, r.target.Args()...)
	routed = append(routed, args...)
	return r.runner.Run(ctx, name, routed...)
}
