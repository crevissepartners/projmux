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
func (c *createCommand) materializeVirtualPane(ctx context.Context, reg *coremetadata.Registry, mut coremetadata.Mutator, ledger *runtimeLedger, project coremetadata.Project, windowUID string, pane coremetadata.Pane, activation superviseSpec, command []string) (string, error) {
	window, ok := reg.Window(windowUID)
	if !ok {
		return "", errors.New("materialize virtual Window: Window disappeared")
	}
	if err := c.ensureRuntimeRoute(ctx); err != nil {
		return "", err
	}
	sessionName := c.reconciler.projectPhysicalSessionName(*reg, project)
	if sessionName == "" {
		return "", errors.New("materialize virtual Window: no physical session name")
	}
	created, _, err := c.runtime.ensureSessionLaunching(ctx, project, sessionName, pane.Spec.CWD, window.Metadata.Name, func() (superviseSpec, error) { return activation, nil }, ledger, command)
	if err != nil {
		return "", err
	}
	windowID, paneID := created.WindowID, created.PaneID
	if !created.Created {
		// Refuse an unexpected live claim rather than creating a duplicate UID.
		inventory, err := c.runtime.windowRuntimeInventory(ctx, created.SessionID)
		if err != nil {
			return "", err
		}
		if _, found, err := resolveWindowRuntimeUID(inventory, windowUID); err != nil {
			return "", err
		} else if found {
			return "", fmt.Errorf("virtual Window uid:%s already has a runtime", windowUID)
		}
		made, err := c.runtime.newWindow(ctx, created.SessionID, window.Metadata.Name, pane.Spec.CWD, c.runtime.supervisedLaunch(ctx, activation, command))
		windowID, paneID = made.WindowID, made.PaneID
		if windowID != "" {
			if claimErr := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, windowID, windowUID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
		}
		if err != nil {
			return "", err
		}
	} else if err := c.runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, windowID, windowUID, ledger); err != nil {
		return "", err
	}
	markSupervisedSpawn(ctx)
	if err := c.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); err != nil {
		return "", err
	}
	if err := c.runtime.mirrorWindow(ctx, windowID, *window); err != nil {
		return "", err
	}
	if err := c.runtime.mirrorPane(ctx, paneID, pane); err != nil {
		return "", err
	}
	if _, err := mut.SetWindowAnchor(reg, windowUID, pane.Metadata.UID); err != nil {
		return "", err
	}
	if _, err := mut.ObserveWindowRuntimeBinding(reg, windowUID, created.SessionID, windowID); err != nil {
		return "", err
	}
	ledger.observeCurrentWindow(windowUID, runtimeOwner{SessionID: created.SessionID, WindowID: windowID})
	observeActivationRuntime(reg, mut, activation, paneID, c.runtime.warn)
	if _, err := mut.BindLiveProjectSession(reg, project.Metadata.UID, sessionName, c.runtime.expectedSocketPath); err != nil {
		return "", err
	}
	if err := c.runtime.finalizeSessionStartup(ctx, created, sessionName, project.Spec.Root, ledger); err != nil {
		return "", err
	}
	return paneID, nil
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
			exact := explicitTmuxRunner{runner: runner, target: route.target}
			client := defaultTmuxClientWithSocketRunner(exact, route.socketName)
			c.runtime.invalidateRouteIdentity("virtual-window-navigation")
			c.reconciler = newRegistryReconcilerWithRoute(exact, client, route)
			c.reconciler.shareRouteIdentityScope(c.runtime)
			c.reconciler.sessionSocketPath = func() string { return c.runtime.expectedSocketPath }
			c.runtime.runner, c.runtime.mirror, c.runtime.sessions = exact, intmetadata.NewMirror(exact), client
			c.runtime.target, c.runtime.expectedSocketPath, c.runtime.socketName, c.runtime.routeAuthority = route.target, route.expectedSocketPath, route.socketName, route.authority
			return nil
		}
	}
	c.runtime.warn = io.Discard
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
	err = c.transact(diagnostics.CreateKindPane, func(ctx context.Context, working *coremetadata.Registry, mut coremetadata.Mutator, operationID string, ledger *runtimeLedger) error {
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
