package runtime

import (
	"context"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// WindowAgentRequest is one new Window whose Agent replaces the shell Pane the
// Window is created with. Window names and labels the Window; its Payload only
// derives the Window name and stored command, as `create window --provider`
// allocates it, and is never run in the shell. Agent is the Agent half; its
// CreateSelection is filled in by the operation and its Name stays empty, so
// the Agent takes a generated name. CwdPaneUID, when set, is the exact Pane
// whose current directory the Agent starts in, under the split cwd rule.
type WindowAgentRequest struct {
	Window     WindowRequest
	Agent      AgentCreateRequest
	CwdPaneUID string
}

// WindowAgentResult is the committed Window row, whose PaneID is the Agent's
// Pane, and the Agent half's result.
type WindowAgentResult struct {
	Window ShellCreateResource
	Agent  AgentCreateResult
}

// WindowOperation commits one Window with its Agent in one transaction: the
// Window and its initial shell are allocated and materialized, the Agent is
// created by the same Operation `create agent` runs, split off that shell, and
// the shell is then retired in both authorities, so the Window keeps exactly
// the Agent Pane with no default shell. Any error leaves the rollback of all
// of it to the caller's transaction.
func (c *AgentCreation) WindowOperation(scope CreateScope, req WindowAgentRequest, spelling string, out *WindowAgentResult) CreateOperation {
	return func(ctx context.Context, working *coremetadata.Registry, mutator coremetadata.Mutator, operationID string, ledger *RuntimeLedger) error {
		*out = WindowAgentResult{}
		project, err := c.Execution.ResolveWindowProject(*working, scope)
		if err != nil {
			return err
		}
		windowReq := req.Window
		windowReq.OperationID = operationID
		work, err := c.Execution.AllocateWindow(working, mutator, project, windowReq)
		if err != nil {
			return err
		}
		// The payload is the Agent's initial task. The shell only anchors the
		// Agent's split, so it starts with no command.
		work.Payload = nil
		sessionName, err := c.Execution.EnsureProjectRuntime(ctx, working, mutator, project, operationID, ledger)
		if err != nil {
			return err
		}
		agentReq := req.Agent
		if req.CwdPaneUID != "" {
			// Read before the new Window exists, so the source cannot be it.
			dir, notice := c.sourcePaneLaunchDir(ctx, *working, project, sessionName, req.CwdPaneUID)
			if notice != "" {
				out.Agent.Notices = append(out.Agent.Notices, fmt.Sprintf("%s: pane/%s: %s", spelling, req.CwdPaneUID, notice))
			}
			if dir != project.Spec.Root {
				agentReq.Cwd, agentReq.CwdSet = dir, true
			}
		}
		if err := c.Execution.MaterializeWindow(ctx, working, mutator, ledger, project, sessionName, &work); err != nil {
			return err
		}
		windowUID, shellUID := work.Window.Metadata.UID, work.Initial.Metadata.UID
		agentReq.CreateSelection = CreateSelection{Windows: []string{"uid:" + windowUID}, Panes: []string{"uid:" + shellUID}}
		agentReq.CwdFrom = "project"
		agentReq.NoEqualize = true
		if err := c.Operation(scope, agentReq, spelling, &out.Agent)(ctx, working, mutator, operationID, ledger); err != nil {
			return err
		}
		if len(out.Agent.Results) != 1 {
			return fmt.Errorf("%s: window/%s opened %d Agents, want 1", spelling, work.Window.Metadata.Name, len(out.Agent.Results))
		}
		// The Registry row goes first: DeletePane reselects the Window's anchor
		// onto the Agent Pane. The tmux half names the exact handle this
		// operation mirrored its own uid onto.
		if err := mutator.DeletePane(working, shellUID); err != nil {
			return MapMetadataError(err)
		}
		if err := c.Execution.Runtime.RetireOwnedPane(ctx, work.InitialPaneID, shellUID); err != nil {
			return err
		}
		out.Window = ShellCreateResource{Kind: coremetadata.KindWindow, UID: windowUID, Name: work.Window.Metadata.Name,
			PaneID: out.Agent.Results[0].PaneID, ProjectName: project.Metadata.Name, WindowName: work.Window.Metadata.Name, WindowUID: windowUID}
		return nil
	}
}

// sourcePaneLaunchDir is the split cwd rule applied to an exact source Pane of
// project: its live directory when that is inside the Project tree, otherwise
// the Project root with the rule's notice. A Pane with no live tmux runtime in
// the Project's session has no directory to read.
func (c *AgentCreation) sourcePaneLaunchDir(ctx context.Context, reg coremetadata.Registry, project coremetadata.Project, sessionName, paneUID string) (string, string) {
	paneID, err := c.sourcePaneRuntime(ctx, reg, project, sessionName, paneUID)
	if err != nil {
		return ResolveSplitPaneCWD(project.Spec.Root, "", err)
	}
	return c.Execution.SplitPaneLaunchDir(ctx, paneID, project.Spec.Root)
}

func (c *AgentCreation) sourcePaneRuntime(ctx context.Context, reg coremetadata.Registry, project coremetadata.Project, sessionName, paneUID string) (string, error) {
	windowUID, ok := ProjectPaneWindow(reg, project.Metadata.UID, paneUID)
	if !ok {
		return "", fmt.Errorf("pane uid %q is not a Pane of this Project", paneUID)
	}
	windowID, err := c.Execution.Runtime.WindowIDForUID(ctx, sessionName, windowUID)
	if err != nil {
		return "", err
	}
	if windowID == "" {
		return "", fmt.Errorf("pane uid %q has no live Window", paneUID)
	}
	panes, err := c.Execution.Runtime.PanesOf(ctx, windowID)
	if err != nil {
		return "", err
	}
	for _, row := range panes {
		if row[0] == paneUID && strings.HasPrefix(row[1], "%") {
			return row[1], nil
		}
	}
	return "", fmt.Errorf("pane uid %q has no live tmux pane", paneUID)
}

// ProjectPaneWindow is the Window of project that holds the Pane paneUID.
func ProjectPaneWindow(reg coremetadata.Registry, projectUID, paneUID string) (string, bool) {
	for _, window := range reg.WindowsOf(projectUID) {
		if _, ok := reg.PaneInWindow(window.Metadata.UID, paneUID); ok {
			return window.Metadata.UID, true
		}
	}
	return "", false
}
