package projmux

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// checkResumeSelectionShape refuses, before anything is read, a Resume the
// create cannot run: on a process host, without its identity, with launch
// settings of its own (a resume inherits them from the Agents recording the
// conversation, as the CLI picker's create does), or from a source Pane.
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
	case !resumeNativeSelection(selection) && !mutationruntime.ResumePickerSource(selection.Provider, selection.Source):
		return windowIntentRefusal("WindowCreateIntent.Agent.Resume.Source " + selection.Source + " is not a resumable " + selection.Provider + " source; nothing was created")
	}
	return nil
}

// checkResumeCandidate refuses a selection that is no longer a candidate:
// the discovery ListResumeCandidates runs for the selection's directory must
// still list the same conversation from the same source. It runs in
// PreflightWindowIntent and again in CreateWindowFromIntent right before the
// transaction, so a selection made stale since it was listed is refused
// before any effect. A Claude, Antigravity or Codex rollout selection is
// checked without the Codex catalog, so the check starts no process. A Codex
// app-server selection is checked only at commit, by nativeResumeRoute, which
// reads the catalog; the preview does not check it.
func (c *Client) checkResumeCandidate(ctx context.Context, project string, selection core.ResumeSelection) error {
	if resumeNativeSelection(selection) {
		return nil
	}
	candidates, err := c.listResumeCandidates(ctx, core.ResumeCandidateQuery{Project: project, Provider: selection.Provider, Cwd: selection.Cwd}, false)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Conversation == selection.Conversation && candidate.Source == selection.Source {
			return nil
		}
	}
	return resumeCandidateStale(selection)
}

func resumeCandidateStale(selection core.ResumeSelection) error {
	return &Refusal{Code: CodeCreateRequestInvalid, message: "resume-candidate-stale: " + selection.Provider + " conversation " + selection.Conversation +
		" from " + selection.Source + " is not a current resume candidate; nothing was created"}
}

// resumeNativeSelection reports a Codex app-server conversation, which
// resumes on the native lane.
func resumeNativeSelection(selection core.ResumeSelection) bool {
	return selection.Provider == "codex" && strings.TrimSpace(selection.Source) == aisessions.SourceCodexAppServer
}

// nativeResumeRoute is the commit-time check of a Codex app-server selection:
// the catalog discovery of the selection's directory must still list the
// conversation, and the endpoint and generation of that row must resolve to a
// current route (ResolveNativeCatalogResumeRoute, the CLI picker's rule). It
// returns the route and the Client's native session the create resumes with.
func (c *Client) nativeResumeRoute(ctx context.Context, project string, selection core.ResumeSelection, planner *mutationruntime.SDKAgentPlanner) (mutationruntime.NativeCodexRoute, *mutationruntime.SDKNativeSession, error) {
	cwd, enabled, err := c.resumeCandidateInputs(ctx, core.ResumeCandidateQuery{Project: project, Provider: selection.Provider, Cwd: selection.Cwd})
	if err != nil {
		return mutationruntime.NativeCodexRoute{}, nil, err
	}
	frame := c.resumeDiscovery(ctx, cwd, enabled, true).Populate()
	if err := ctx.Err(); err != nil {
		return mutationruntime.NativeCodexRoute{}, nil, err
	}
	var row *aisessions.ResumeSummary
	for i := range frame.Summaries {
		if frame.Summaries[i].ResumeID == selection.Conversation && frame.Summaries[i].Source == selection.Source {
			row = &frame.Summaries[i]
			break
		}
	}
	if row == nil {
		return mutationruntime.NativeCodexRoute{}, nil, resumeCandidateStale(selection)
	}
	native, err := planner.NativeSession()
	if err != nil {
		return mutationruntime.NativeCodexRoute{}, nil, createRefusal(mutationruntime.NativeResumePreparationRefusal("create agent", err), "WindowCreateIntent")
	}
	routeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	route, err := mutationruntime.ResolveNativeCatalogResumeRoute(routeCtx, mutationruntime.CodexSummaryEndpoint(*row), metadata.CodexGenerationState(row.GenerationState), native.Resolve)
	cancel()
	if err != nil {
		return mutationruntime.NativeCodexRoute{}, nil, createRefusal(mutationruntime.NativeResumePreparationRefusal("create agent", err), "WindowCreateIntent")
	}
	native.Route = route
	return route, native, nil
}

// nativeResumeEngine resumes the selection's thread through native on route
// and binds the new Pane as a native Codex Pane.
func nativeResumeEngine(planner *mutationruntime.SDKAgentPlanner, execution *mutationruntime.CreateExecution, native *mutationruntime.SDKNativeSession, route mutationruntime.NativeCodexRoute, conversation string) *mutationruntime.NativeResumeEngine {
	return &mutationruntime.NativeResumeEngine{Spelling: "create agent", Route: route, Conversation: conversation, Resume: native.Resume, Plan: native.Plan, SafeFallback: codexappserver.CanFallback,
		BindPane: func(ctx context.Context, pane string, w metadata.AgentWorkspace, b mutationruntime.NativeAgentBinding) error {
			return planner.Launch.BindAgentPaneOnRoute(ctx, execution.Runtime.Runner, mutationruntime.AgentPaneBinding{PaneID: pane, Provider: "codex", ContextDir: w.CWD, Title: b.Title, ConversationID: b.ThreadID, ResumeSource: aisessions.SourceCodexAppServer, ThreadID: b.ThreadID, NativeCodex: true})
		}}
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
