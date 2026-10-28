package projmux

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// checkResumeSelectionShape refuses, before anything is read, a Resume the
// create cannot run: on a process host, without its identity, with launch
// settings of its own (a resume inherits them from the Agents recording the
// conversation, as the CLI picker's create does), from a source Pane, or of a
// Codex app-server conversation.
func checkResumeSelectionShape(host string, agent core.WindowIntentAgent) error {
	selection := *agent.Resume
	switch {
	case host == "process":
		return windowIntentRefusal("process-resume-unsupported: WindowCreateIntent.Agent.Resume requires host tmux; nothing was created")
	case strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Conversation) == "" || strings.TrimSpace(selection.Source) == "":
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume requires Provider, Conversation and Source")
	case agent.Provider != "" && agent.Provider != selection.Provider:
		return windowIntentRefusal("WindowCreateIntent.Agent.Provider " + agent.Provider + " does not match Resume.Provider " + selection.Provider + "; nothing was created")
	case agent.Profile != "" || agent.Instructions != "" || agent.Model != "" || agent.Effort != "" || len(agent.Payload) != 0:
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume inherits its launch settings; Profile, Instructions, Model, Effort and Payload must be empty")
	case strings.TrimSpace(agent.CwdFrom) == "pane":
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume starts in Resume.Cwd; CwdFrom pane is not supported with it")
	case selection.Cwd != "" && !filepath.IsAbs(selection.Cwd):
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume.Cwd must be absolute")
	case selection.Provider == "codex" && strings.TrimSpace(selection.Source) == aisessions.SourceCodexAppServer:
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume of a Codex app-server conversation is not supported yet; nothing was created")
	case !mutationruntime.ResumePickerSource(selection.Provider, selection.Source):
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume.Source " + selection.Source + " is not a resumable " + selection.Provider + " source; nothing was created")
	}
	return nil
}

// checkResumeCandidate refuses a selection that is no longer a candidate:
// the discovery ListResumeCandidates runs for the selection's directory must
// still list the same conversation from the same source. It runs in
// PreflightWindowIntent and again in CreateWindowFromIntent right before the
// transaction, so a selection made stale since it was listed is refused
// before any effect.
func (c *Client) checkResumeCandidate(ctx context.Context, project string, selection core.ResumeSelection) error {
	candidates, err := c.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project: project, Provider: selection.Provider, Cwd: selection.Cwd})
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Conversation == selection.Conversation && candidate.Source == selection.Source {
			return nil
		}
	}
	return &Refusal{Code: CodeCreateRequestInvalid, message: "resume-candidate-stale: " + selection.Provider + " conversation " + selection.Conversation +
		" from " + selection.Source + " is not a current resume candidate; nothing was created"}
}

// resumePickerCreate is the service's resume preparation of one selection,
// planned through the create's own launcher and bound on its tmux route.
func (c *Client) resumePickerCreate(planner *mutationruntime.SDKAgentPlanner, execution *mutationruntime.CreateExecution, selection core.ResumeSelection) *mutationruntime.ResumePickerCreate {
	paths := func() (config.Paths, error) { return c.paths, nil }
	guidance := &mutationruntime.GuidanceService{Paths: paths}
	return &mutationruntime.ResumePickerCreate{
		Paths: c.paths, Provider: selection.Provider, Conversation: selection.Conversation, Source: selection.Source, Guidance: guidance,
		Launch: &mutationruntime.ResumeLaunchService{Command: planner.Command, Paths: paths, Guidance: guidance, MissingBinary: func(p string) string { return "selected runner is not installed: " + p }},
		BindPane: func(ctx context.Context, binding mutationruntime.AgentPaneBinding) error {
			return planner.Launch.BindAgentPaneOnRoute(ctx, execution.Runtime.Runner, binding)
		},
	}
}
