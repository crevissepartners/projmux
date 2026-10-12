package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"strings"
)

type ShellCreateResource struct {
	Kind                                                  coremetadata.Kind
	UID, Name, PaneID, ProjectName, WindowName, WindowUID string
}
type ShellCreateResult struct {
	Results            []ShellCreateResource
	SelectedWindowUIDs []string
	Notices            []string
}
type ShellPaneRequest struct {
	CreateSelection
	Payload            []string
	Placement, CwdFrom string
}
type PaneWork struct {
	Target     PaneTarget
	WindowName string
	Pane       coremetadata.Pane
	Activation SuperviseSpec
}

func (c *CreateExecution) PaneOperation(scope CreateScope, flags ShellPaneRequest, labels map[string]string, out *ShellCreateResult) CreateOperation {
	const spelling = "create pane"
	return func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {

		project, err := c.ResolveProject(*working, scope)
		if err != nil {
			return err
		}
		if err := c.RefuseMissingRoot(project); err != nil {
			return err
		}
		// A CLI split follows its own argv: only --cwd-from decides, and no
		// config file is opened. See cliSplitCWDSource.
		source := strings.TrimSpace(flags.CwdFrom)

		// Full preflight plus the metadata half of the Window ensure. Every
		// target Window and every anchor Pane is fixed, and every Window this
		// operation must create is allocated, before the first tmux call.
		plan, windows, err := c.ResolveSplitTargets(working, mutator, project, scope, flags.CreateSelection,
			selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindWindow}, spelling, operationID)
		if err != nil {
			return err
		}
		out.SelectedWindowUIDs = plan.SelectedWindowUIDs()
		if len(plan.Targets) == 0 {
			return createUsage(fmt.Sprintf("%s resolved no target Window; %s matched at least one Window is required",
				spelling, selector.DescribeSelector(plan.Query)))
		}

		panes := make([]PaneWork, 0, len(plan.Targets))
		for _, Target := range plan.Targets {
			window, ok := working.Window(Target.WindowUID)
			if !ok {
				return fmt.Errorf("%s: window %q disappeared during preflight", spelling, Target.WindowUID)
			}
			pane, err := mutator.AddPane(working, Target.WindowUID, coremetadata.BootstrapPane{
				Name:    flags.Name,
				Command: PayloadCommand(flags.Payload),
				CWD:     project.Spec.Root,
				Labels:  labels,
			}, c.Shell, operationID)
			if err != nil {
				return MapMetadataError(err)
			}
			activation, err := c.IssuePaneActivation(working, mutator, pane.Metadata.UID, "", operationID)
			if err != nil {
				return err
			}
			panes = append(panes, PaneWork{
				Target:     Target,
				WindowName: window.Metadata.Name,
				Pane:       pane,
				Activation: activation,
			})
		}

		// Runtime phase.
		var sessionName string
		if len(windows) > 0 {
			sessionName, err = c.EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
			if err != nil {
				return err
			}
		}
		for i := range windows {
			if err := c.MaterializeWindow(ctx, working, mutator, ledger, project, sessionName, &windows[i]); err != nil {
				return err
			}
		}
		for _, work := range panes {
			if work.Target.Virtual {
				paneID, err := c.MaterializeVirtualPane(ctx, working, mutator, ledger, project, work.Target.WindowUID, work.Pane, work.Activation, flags.Payload)
				if err != nil {
					return err
				}
				if _, _, err := mutator.AdoptWindowDefaultShell(working, work.Target.WindowUID, work.Pane.Metadata.UID); err != nil {
					return err
				}
				out.Results = append(out.Results, ShellCreateResource{Kind: coremetadata.KindPane, UID: work.Pane.Metadata.UID, Name: work.Pane.Metadata.Name, PaneID: paneID, ProjectName: project.Metadata.Name, WindowName: work.WindowName, WindowUID: work.Target.WindowUID})
				continue
			}
			if sessionName == "" {
				sessionName, err = c.EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
				if err != nil {
					return err
				}
			}
			anchorPaneID, err := c.EnsureAnchorPane(ctx, working, mutator, ledger, project, sessionName, operationID, work.Target)
			if err != nil {
				return err
			}
			launchDir := project.Spec.Root
			if source == "pane" {
				var notice string
				launchDir, notice = c.SplitPaneLaunchDir(ctx, anchorPaneID, project.Spec.Root)
				if notice != "" {
					out.Notices = append(out.Notices, fmt.Sprintf("%s: window/%s: %s", spelling, work.WindowName, notice))
				}
				if launchDir != project.Spec.Root {
					// The Pane this operation allocated starts where its split
					// starts; the Window's own anchor and owner are untouched.
					if stored, ok := working.Pane(work.Pane.Metadata.UID); ok {
						stored.Spec.CWD = launchDir
					}
					work.Pane.Spec.CWD = launchDir
				}
			}
			launch := c.Runtime.SupervisedLaunch(ctx, work.Activation, flags.Payload)
			paneID, err := c.Runtime.SplitPane(ctx, anchorPaneID, flags.Placement, launchDir, launch, nil)
			if paneID != "" {
				if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, paneID, work.Pane.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(err, claimErr)
				}
				if mirrorErr := c.Runtime.MirrorPaneIdentity(ctx, paneID, work.Pane); mirrorErr != nil {
					return errors.Join(err, mirrorErr)
				}
				ObserveActivationRuntime(working, mutator, work.Activation, paneID, c.Runtime.Warn)
			}
			if err != nil {
				return err
			}
			// A shell-required offline bootstrap must first materialize the exact
			// stored anchor. Only after this new shell Pane exists do we adopt it
			// as an empty optional default.
			if _, _, err := mutator.AdoptWindowDefaultShell(working, work.Target.WindowUID, work.Pane.Metadata.UID); err != nil {
				return MapMetadataError(err)
			}
			// Equalize before releasing the create transaction: the next serialized
			// create may split the same exact anchor Pane and needs the capacity restored
			// first. equalizeSplitLayout keeps this lock-held work bounded by sharing
			// one route/inventory guard and one effect receipt across the complete
			// printable resize batch.
			c.Runtime.EqualizeSplitLayout(ctx, anchorPaneID, flags.Placement)
			out.Results = append(out.Results, ShellCreateResource{
				Kind:        coremetadata.KindPane,
				UID:         work.Pane.Metadata.UID,
				Name:        work.Pane.Metadata.Name,
				PaneID:      paneID,
				ProjectName: project.Metadata.Name,
				WindowName:  work.WindowName,
				WindowUID:   work.Target.WindowUID,
			})
		}
		return nil
	}
}

func (c *CreateExecution) MaterializeVirtualPane(ctx context.Context, reg *coremetadata.Registry, mut coremetadata.Mutator, ledger *RuntimeLedger, project coremetadata.Project, windowUID string, pane coremetadata.Pane, activation SuperviseSpec, command []string) (string, error) {
	window, ok := reg.Window(windowUID)
	if !ok {
		return "", errors.New("materialize virtual Window: Window disappeared")
	}
	if err := c.EnsureRuntimeRoute(ctx); err != nil {
		return "", err
	}
	sessionName := c.Reconciler.ProjectPhysicalSessionName(*reg, project)
	if sessionName == "" {
		return "", errors.New("materialize virtual Window: no physical session name")
	}
	created, _, err := c.Runtime.EnsureSessionLaunching(ctx, project, sessionName, pane.Spec.CWD, window.Metadata.Name, func() (SuperviseSpec, error) { return activation, nil }, ledger, command)
	if err != nil {
		return "", err
	}
	windowID, paneID := created.WindowID, created.PaneID
	if !created.Created {
		// Refuse an unexpected live claim rather than creating a duplicate UID.
		inventory, err := c.Runtime.WindowRuntimeInventory(ctx, created.SessionID)
		if err != nil {
			return "", err
		}
		if _, found, err := ResolveWindowRuntimeUID(inventory, windowUID); err != nil {
			return "", err
		} else if found {
			return "", fmt.Errorf("virtual Window uid:%s already has a runtime", windowUID)
		}
		made, err := c.Runtime.NewWindow(ctx, created.SessionID, window.Metadata.Name, pane.Spec.CWD, c.Runtime.SupervisedLaunch(ctx, activation, command))
		windowID, paneID = made.WindowID, made.PaneID
		if windowID != "" {
			if claimErr := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimeWindow, windowID, windowUID, ledger); claimErr != nil {
				return "", errors.Join(err, claimErr)
			}
		}
		if err != nil {
			return "", err
		}
	} else if err := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimeWindow, windowID, windowUID, ledger); err != nil {
		return "", err
	}
	MarkSupervisedSpawn(ctx)
	if err := c.Runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, paneID, pane.Metadata.UID, ledger); err != nil {
		return "", err
	}
	if err := c.Runtime.MirrorWindowIdentity(ctx, windowID, *window); err != nil {
		return "", err
	}
	if err := c.Runtime.MirrorPaneIdentity(ctx, paneID, pane); err != nil {
		return "", err
	}
	if _, err := mut.SetWindowAnchor(reg, windowUID, pane.Metadata.UID); err != nil {
		return "", err
	}
	if _, err := mut.ObserveWindowRuntimeBinding(reg, windowUID, created.SessionID, windowID); err != nil {
		return "", err
	}
	ledger.ObserveCurrentWindow(windowUID, RuntimeOwner{SessionID: created.SessionID, WindowID: windowID})
	ObserveActivationRuntime(reg, mut, activation, paneID, c.Runtime.Warn)
	if _, err := mut.BindLiveProjectSession(reg, project.Metadata.UID, sessionName, c.Runtime.ExpectedSocketPath); err != nil {
		return "", err
	}
	if err := c.Runtime.FinalizeSessionStartup(ctx, created, sessionName, project.Spec.Root, ledger); err != nil {
		return "", err
	}
	return paneID, nil
}

func (c *CreateExecution) WindowOperation(scope CreateScope, req WindowRequest, out *ShellCreateResult) CreateOperation {
	return func(ctx context.Context, reg *coremetadata.Registry, mut coremetadata.Mutator, op string, ledger *RuntimeLedger) error {
		project, err := c.ResolveProject(*reg, scope)
		if err != nil {
			return err
		}
		if err = c.RefuseMissingRoot(project); err != nil {
			return err
		}
		req.OperationID = op
		work, err := c.AllocateWindow(reg, mut, project, req)
		if err != nil {
			return err
		}
		session, err := c.EnsureProjectRuntime(ctx, reg, mut, project, op, ledger)
		if err != nil {
			return err
		}
		if err = c.MaterializeWindow(ctx, reg, mut, ledger, project, session, &work); err != nil {
			return err
		}
		out.Results = []ShellCreateResource{{Kind: coremetadata.KindWindow, UID: work.Window.Metadata.UID, Name: work.Window.Metadata.Name, PaneID: work.InitialPaneID, ProjectName: project.Metadata.Name, WindowName: work.Window.Metadata.Name, WindowUID: work.Window.Metadata.UID}}
		out.SelectedWindowUIDs = []string{work.Window.Metadata.UID}
		return nil
	}
}
