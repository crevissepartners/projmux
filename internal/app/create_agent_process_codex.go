package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

func processAgentAnswers(registry coremetadata.Registry, agent coremetadata.Agent) bool {
	pane, found := registry.Pane(agent.Status.PaneRef)
	return (agent.Spec.Provider == aiModeClaude || agent.Spec.Provider == aiModeCodex) && found && pane.Metadata.OwnerUID() == agent.Metadata.UID && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess
}

func (c *agentCommand) callProcessCodexTurn(reg coremetadata.Registry, agent coremetadata.Agent, action, text string) (string, error) {
	pane, found := reg.Pane(agent.Status.PaneRef)
	if !found || pane.Status.ProcessSession == nil || agent.Spec.Provider != aiModeCodex || c.controlPaths == nil {
		return "", processhost.ErrStale
	}
	session := pane.Status.ProcessSession
	activation, provider, current := reg.CurrentProcessActivation(session.Binding)
	if !current || provider != aiModeCodex {
		return "", processhost.ErrStale
	}
	paths, err := c.controlPaths()
	if err != nil {
		return "", err
	}
	socket := processCodexHostSocket(intmetadata.PathFor(paths.StateDir), pane.Metadata.UID, session.Binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return "", fmt.Errorf("process-host-unavailable: %w", err)
	}
	operation, err := newCreateOperationID()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	authority := processhost.Authority{Binding: processSchemaBinding(session.Binding), Connection: session.Binding.OperationID, Session: session.ThreadID}
	result, err := callProcessForeground(ctx, socket, identity, activation.HostProcess, codexProcessExchange{Foreground: &processForegroundRequest{Authority: authority, Action: action, Operation: operation, Prompt: text, Turn: session.TurnID}})
	if err != nil {
		return "", fmt.Errorf("process-host-unavailable: %w", err)
	}
	return operation, processClaudeTurnAcceptance(result)
}

type processCodexCommandPlanner interface {
	PlanProcessCodexCommand(coremetadata.AgentWorkspace) (processhost.Command, error)
}

// Publish exact owned birth before provider initialization can fail. This lets
// the shared creator persist actual Wait and dispose its reservation on failure.
func processCodexCreateSpawn(path string, binding processhost.Binding) *processhost.SpawnCallback {
	return &processhost.SpawnCallback{Publish: func(ctx context.Context, handle *processhost.Handle) error {
		snapshot, err := handle.Observe(binding)
		if err != nil {
			return err
		}
		child, supervisor, err := localipc.Process(snapshot.PID)
		if err != nil {
			return err
		}
		_, hostPID, err := localipc.Process(supervisor)
		if err != nil || hostPID != os.Getpid() {
			return processhost.ErrStale
		}
		host, _, err := localipc.Process(hostPID)
		if err != nil {
			return err
		}
		activation := coremetadata.ProcessActivation{Binding: metadataProcessBinding(binding), HostProcess: host, Child: child}
		_, _, err = intmetadata.NewStore(path).UpdateConvergent(func(reg *coremetadata.Registry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return intmetadata.DefaultMutator().RecordProcessChild(reg, activation)
		})
		return err
	}}
}

// Settings ride the typed thread/start request; no CLI conversation, daemon,
// broker or fallback endpoint is started by this planner.
func (c *aiCommand) PlanProcessCodexCommand(workspace coremetadata.AgentWorkspace) (processhost.Command, error) {
	path := c.findAgentBinary(aiModeCodex)
	if path == "" {
		return processhost.Command{}, fmt.Errorf("%s", c.missingAgentRunnerMessage(aiModeCodex))
	}
	return processhost.CodexCommand(path, workspace.CWD, os.Environ(), nil)
}

func (c *createCommand) prepareProcessCodexLaunch(plan *processAgentCreatePlan) error {
	planner, ok := c.agents.(processCodexCommandPlanner)
	if !ok {
		return errors.New("process Codex launcher is not configured")
	}
	command, err := planner.PlanProcessCodexCommand(plan.workspace)
	plan.command = command
	return err
}

func processCodexCreateConfig(plan processAgentCreatePlan, agent string) processhost.CodexConfig {
	flags := plan.flags
	return processhost.CodexConfig{Version: version.String(), Roots: plan.workspace.AdditionalWritableRoots,
		Settings:              codexappserver.ThreadSettings{Model: flags.model, Effort: flags.effort, Policy: flags.profileLaunch.codexPolicy},
		DeveloperInstructions: flags.agentGuidance.codexDeveloperInstructions(agent, flags.projectLinks.developerInstructions(flags.personaLaunch.content))}
}

type processCreateControl struct {
	sync, syncControls func(context.Context) error
	attention          *processAttentionProjection
}

func (c *createCommand) newProcessCreateControl(result processAgentCreateResult) (processCreateControl, error) {
	if result.Provider != aiModeCodex {
		control, err := c.newProcessClaudeControl(result)
		if err != nil {
			return processCreateControl{}, err
		}
		return processCreateControl{sync: control.sync, syncControls: control.syncControls, attention: control.attention}, nil
	}
	if result.codexEndpoint == nil {
		return processCreateControl{}, errors.New("process Codex endpoint is unavailable")
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return processCreateControl{}, err
	}
	paths.StateDir = filepath.Dir(filepath.Dir(result.registryPath))
	endpoint := result.codexEndpoint
	endpoint.messages.Store(&codexProcessMessages{store: messagestore.NewStore(paths.StateDir), endpoints: map[string]*codexProcessEndpoint{result.Binding.Agent: endpoint}, resolveSource: func(ctx context.Context, uid string) (coremessage.Route, error) {
		reg, err := intmetadata.NewStore(result.registryPath).LoadDegradedReadOnly()
		if err != nil {
			return coremessage.Route{}, err
		}
		agent, found := reg.Agent(uid)
		if !found {
			return coremessage.Route{}, processhost.ErrStale
		}
		route, err := (liveAgentMessageRouteResolver{registryPath: result.registryPath}).Resolve(reg, *agent)
		if err != nil {
			return coremessage.Route{}, err
		}
		return publicMessageRoute(route), nil
	}})
	attention := newProcessAttentionStore(paths.StateDir)
	if err = attention.activate(result.Binding, aiModeCodex, ""); err != nil {
		return processCreateControl{}, err
	}
	control := &codexProcessControl{endpoint: result.codexEndpoint, questions: agentquestion.NewStore(paths.StateDir), approvals: agentapproval.NewStore(paths.StateDir), now: time.Now,
		questionWindow: time.Duration(loadCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second,
		approvalWindow: time.Duration(loadCentralAgentApprovalWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second,
		attention:      &processAttentionProjection{store: attention, queue: notify.NewDefaultStore(paths)}}
	return processCreateControl{sync: control.sync, syncControls: control.syncControls, attention: control.attention}, nil
}
