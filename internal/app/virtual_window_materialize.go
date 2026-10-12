package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// materializeVirtualPane uses the requested Pane as the Window's first runtime
// Pane. The surrounding create transaction owns both the metadata and rollback
// ledger; no temporary shell or second definition of runtime ownership is used.
func (c *createCommand) materializeVirtualPane(ctx context.Context, reg *coremetadata.Registry, mut coremetadata.Mutator, ledger *RuntimeLedger, project coremetadata.Project, windowUID string, pane coremetadata.Pane, activation superviseSpec, command []string) (string, error) {
	return c.createExecution().MaterializeVirtualPane(ctx, reg, mut, ledger, project, windowUID, pane, activation, command)
}

// virtualWindowCreator binds navigation's declared socket through the same
// exact-route proof as explicit topology materialization.
func virtualWindowCreator(runner tmuxCommandRunner, lookup func(string) string, store *resourceStore, socket string) (*createCommand, error) {
	c := newCreateCommandOn(runner, lookup)
	if store != nil {
		c.store = store
	}
	c.selectRuntimeAuthority(true)
	if socket != "" {
		target, err := tmuxSocketPathTarget(socket)
		if err != nil {
			return nil, err
		}
		c.bindExplicitRuntime = func(ctx context.Context) error {
			route, err := bindExplicitMaterializeSocket(ctx, runner, target, lookup)
			if err != nil {
				return err
			}
			exact := explicitTmuxRunner{Runner: runner, Target: route.Target}
			client := defaultTmuxClientWithSocketRunner(exact, route.SocketName)
			c.runtime.InvalidateRouteIdentity("virtual-window-navigation")
			c.reconciler = newRegistryReconcilerWithRoute(exact, client, route)
			c.reconciler.ShareRouteIdentityScope(c.runtime)
			c.reconciler.SessionSocketPath = func() string { return c.runtime.ExpectedSocketPath }
			c.runtime.Runner, c.runtime.Mirror, c.runtime.Sessions = exact, intmetadata.NewMirror(exact), client
			c.runtime.Target, c.runtime.ExpectedSocketPath, c.runtime.SocketName, c.runtime.RouteAuthority = route.Target, route.ExpectedSocketPath, route.SocketName, route.Authority
			return nil
		}
	}
	c.runtime.Warn = io.Discard
	return c, nil
}

type virtualWindowShellMaterialization struct {
	Window  coremetadata.Window
	Pane    coremetadata.Pane
	Created bool
}

func (c *createCommand) materializeVirtualShell(windowUID string) error {
	_, err := c.materializeVirtualShellResult(windowUID)
	return err
}

// Return only this transaction's new shell, so navigation receipts never infer
// their affected resources from a later Registry snapshot.
func (c *createCommand) materializeVirtualShellResult(windowUID string) (virtualWindowShellMaterialization, error) {
	var result virtualWindowShellMaterialization
	reg, err := c.store.load()
	if err != nil {
		return result, err
	}
	window, found := reg.Window(windowUID)
	if !found {
		return result, fmt.Errorf("materialize Window uid:%s: not in Registry", windowUID)
	}
	err = c.transact(diagnostics.CreateKindPane, func(ctx context.Context, working *coremetadata.Registry, mut coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		if !working.IsVirtualWindow(windowUID) {
			return nil
		}
		project, ok := working.Project(window.Metadata.OwnerUID())
		if !ok {
			return errors.New("materialize virtual Window: owning Project disappeared")
		}
		if err := c.refuseMissingRoot(*project); err != nil {
			return err
		}
		pane, err := mut.AddPane(working, windowUID, coremetadata.BootstrapPane{CWD: project.Spec.Root}, c.shell, operationID)
		if err != nil {
			return err
		}
		activation, err := c.issuePaneActivation(working, mut, pane.Metadata.UID, "", operationID)
		if err != nil {
			return err
		}
		if _, err := c.materializeVirtualPane(ctx, working, mut, ledger, *project, windowUID, pane, activation, nil); err != nil {
			return err
		}
		adopted, _, err := mut.AdoptWindowDefaultShell(working, windowUID, pane.Metadata.UID)
		if err != nil {
			return err
		}
		current, _ := working.Window(windowUID)
		result = virtualWindowShellMaterialization{Window: current.Clone(), Pane: adopted, Created: true}
		return nil
	}, c.exactProjectOwnershipGuard(window.Metadata.OwnerUID()))
	if err != nil {
		return virtualWindowShellMaterialization{}, err
	}
	return result, nil
}
