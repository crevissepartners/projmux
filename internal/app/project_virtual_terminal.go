package app

import (
	"context"
	"errors"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func terminalWindows(reg coremetadata.Registry, projectUID string) []coremetadata.Window {
	var windows []coremetadata.Window
	for _, window := range reg.WindowsOf(projectUID) {
		if !reg.IsVirtualWindow(window.Metadata.UID) {
			windows = append(windows, window)
		}
	}
	return windows
}

func (c *switchCommand) prepareVirtualTerminalProject(ctx context.Context, target string) (virtualWindowShellMaterialization, error) {
	if c.managedStopStore == nil || c.managedStopStore.load == nil {
		return virtualWindowShellMaterialization{}, nil
	}
	reg, err := c.managedStopStore.load()
	if err != nil {
		return virtualWindowShellMaterialization{}, err
	}
	project, found := reg.ProjectByRoot(cleanOptionalPath(target))
	if !found || !reg.IsVirtualWindow(project.Spec.PrimaryWindowRef) || len(terminalWindows(reg, project.Metadata.UID)) > 0 {
		return virtualWindowShellMaterialization{}, nil
	}
	trusted, err := c.authorizeProjectOpen(ctx, target)
	if err != nil {
		return virtualWindowShellMaterialization{}, errProjectTrustGate{err: err}
	}
	if !trusted {
		return virtualWindowShellMaterialization{}, errProjectTrustDenied
	}
	if c.materializeVirtualWindow == nil {
		return virtualWindowShellMaterialization{}, errors.New("open: virtual Window materializer is not configured")
	}
	return c.materializeVirtualWindow(ctx, project.Spec.PrimaryWindowRef)
}

// Only canonical open/attach set allowVirtualTerminal. Picker and sidebar
// callers retain their admission until they acquire virtual navigation support.
func (c *switchCommand) selectVirtualTerminalArrival(ctx context.Context, sessionName string) error {
	if c.managedStopStore == nil || c.managedStopStore.load == nil {
		return nil
	}
	reg, err := c.managedStopStore.load()
	if err != nil {
		return err
	}
	project, _, err := reg.ProjectBySession(sessionName)
	if err != nil {
		return err
	}
	if project == nil || !reg.IsVirtualWindow(project.Spec.PrimaryWindowRef) {
		return nil
	}
	windows := terminalWindows(reg, project.Metadata.UID)
	if len(windows) == 0 {
		return errors.New("open: Project has no terminal Window after materialization")
	}
	// Canonical open/attach and Closed-Project topology activation use the app
	// socket (newSwitchCommand wiring), never an inherited client socket.
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		return err
	}
	runner := explicitTmuxRunner{runner: c.tmuxRunner, target: target}
	out, err := runner.Run(ctx, "tmux", "list-windows", "-t", sessionName, "-F", tmuxRowFormat("#{window_id}", "#{window_active}"))
	if err != nil {
		return err
	}
	rows := splitTmuxRows(string(out), 2)
	for _, row := range rows {
		if row[1] != "1" {
			continue
		}
		for _, window := range windows {
			if window.Status.RuntimeID == row[0] {
				return nil
			}
		}
	}
	for _, window := range windows {
		if strings.TrimSpace(window.Status.RuntimeID) == "" {
			continue
		}
		for _, row := range rows {
			if row[0] == window.Status.RuntimeID {
				focus := newFocusCommand()
				focus.runner, focus.lookupEnv = runner, c.lookupEnv
				return focus.selectWindow(ctx, "", sessionName+":"+row[0])
			}
		}
	}
	return errors.New("open: no declared terminal Window is live in the Project session")
}
