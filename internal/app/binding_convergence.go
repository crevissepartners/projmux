package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// explicitTmuxTarget is the closed routing input of binding convergence.
// Apply supplies a -L socket name; generated lifecycle hooks supply tmux's
// expanded absolute #{socket_path} and therefore use -S. There is no empty
// target and no fallback to the default socket or inherited $TMUX.
type explicitTmuxTarget struct {
	flag  string
	value string
}

// label renders the target the way the delete result reports it: the flag the
// route used and the value it was given, never the app socket's default name.
// Reporting a fixed `-L/projmux` was accurate only while the delete routes
// could reach exactly one server; now that they resolve one, saying which is
// the whole point of the line.
func (t explicitTmuxTarget) label() string {
	if t.flag == "" || t.value == "" {
		return "none"
	}
	return t.flag + "/" + t.value
}

func tmuxSocketNameTarget(name string) (explicitTmuxTarget, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return explicitTmuxTarget{}, errors.New("binding convergence requires an explicit tmux socket name")
	}
	return explicitTmuxTarget{flag: "-L", value: name}, nil
}

func tmuxSocketPathTarget(path string) (explicitTmuxTarget, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return explicitTmuxTarget{}, errors.New("binding convergence requires an absolute tmux socket path")
	}
	return explicitTmuxTarget{flag: "-S", value: filepath.Clean(path)}, nil
}

// explicitTmuxRunner routes every tmux read and write through one exact server.
// It is deliberately below the existing registryReconciler and metadata Mirror,
// so matcher/reconciler/writer behavior is reused without adding a uid writer.
type explicitTmuxRunner struct {
	runner tmuxCommandRunner
	target explicitTmuxTarget
}

func (r explicitTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.runner == nil {
		return nil, errors.New("binding convergence requires a tmux runner")
	}
	if name != "tmux" {
		return nil, fmt.Errorf("binding convergence cannot route executable %q", name)
	}
	if r.target.flag == "" || r.target.value == "" {
		return nil, errors.New("binding convergence has no explicit tmux target")
	}
	routed := make([]string, 0, len(args)+2)
	routed = append(routed, r.target.flag, r.target.value)
	routed = append(routed, args...)
	return r.runner.Run(ctx, name, routed...)
}

// convergeRuntimeBindings runs the existing mutation-route reconciler against
// exactly target. Read routes never call this function.
func (c *tmuxCommand) convergeRuntimeBindings(ctx context.Context, target explicitTmuxTarget) error {
	if c == nil {
		return errors.New("binding convergence command is not configured")
	}
	// A nil store exists only on narrow legacy unit fixtures that construct a
	// tmuxCommand literal instead of the production graph. Do not let those
	// fixtures reach the caller's real state directory. NewTmuxCommand always
	// supplies the store, and lifecycle convergence tests install an explicit
	// fake one.
	if c.resources == nil {
		return nil
	}
	runner := explicitTmuxRunner{runner: c.runner, target: target}
	sessions := inttmux.NewClient(runner)
	newReconciler := c.bindingReconciler
	if newReconciler == nil {
		newReconciler = newRegistryReconciler
	}
	reconciler := newReconciler(runner, sessions)
	operationID, err := newCreateOperationID()
	if err != nil {
		return err
	}
	_, err = c.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		return reconciler.reconcile(ctx, working, mutator, operationID)
	})
	return err
}

func (c *tmuxCommand) convergeBindings(ctx context.Context, target explicitTmuxTarget) error {
	if c.bindingConverger != nil {
		return c.bindingConverger(ctx, target)
	}
	return c.convergeRuntimeBindings(ctx, target)
}
