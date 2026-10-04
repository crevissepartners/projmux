package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

type processCodexCommandPlanner interface {
	PlanProcessCodexCommand(coremetadata.AgentWorkspace) (processhost.Command, error)
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
