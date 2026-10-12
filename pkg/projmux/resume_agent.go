package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeResumeProcessUnsupported     Code = "resume-process-unsupported"
	CodeResumeNativeCodexUnsupported Code = "resume-native-codex-unsupported"
	CodeResumePhaseNotResumable      Code = "resume-phase-not-resumable"
	CodeResumePlanInvalid            Code = "resume-plan-invalid"
	CodeResumeRequestInvalid         Code = "resume-request-invalid"
	CodeResumeExecutionFailed        Code = "resume-execution-failed"
	CodeProfileResumeUnavailable     Code = "profile-resume-unavailable"
	CodeReplyOnlyLaunchFixed         Code = "reply-only-launch-fixed"
)

// ResumeAgent uses the CLI's shared rebind transaction. Context cancellation
// prevents admission only; an admitted operation completes its commit/rollback.
// The result does not imply provider activation acknowledgement.
func (c *Client) ResumeAgent(ctx context.Context, req core.ResumeAgentRequest) (core.ResumeAgentResult, error) {
	return c.resumeAgent(ctx, req, tmux.ExecRunner{})
}
func (c *Client) resumeAgent(ctx context.Context, req core.ResumeAgentRequest, runner mutationruntime.TmuxCommandRunner) (core.ResumeAgentResult, error) {
	var result core.ResumeAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "ResumeAgentRequest.Ref must name one Agent"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "ResumeAgentRequest")
	if err != nil {
		return result, resourceRefusal(err)
	}
	if req.SocketPath != "" && !filepath.IsAbs(req.SocketPath) {
		return result, &Refusal{Code: CodeRuntimeRequestInvalid, message: "ResumeAgentRequest.SocketPath must be absolute"}
	}
	if c.pathsErr != nil {
		return result, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return result, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbResume, Kind: metadata.KindAgent}, query, resolution); err != nil {
		return result, resourceRefusal(err)
	}
	agent, _ := reg.Agent(resolution.Matches[0].UID)
	for _, pane := range reg.PanesOf(agent.Metadata.UID) {
		if pane.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
			return result, &Refusal{Code: CodeResumeProcessUnsupported, message: "ResumeAgent cannot resume a process Agent; use projmux agent resume"}
		}
	}
	fields := mutationruntime.AgentInputFields{Prefix: "ResumeAgentRequest"}
	if err := mutationruntime.ValidateResumeAgent("agent resume", agent, req.Model, req.Effort, req.DialogueReplyOnly, fields); err != nil {
		return result, resumeRefusal(err)
	}
	provider := metadata.NormalizeProvider(agent.Spec.Provider)
	if provider == "" && agent.Status.SessionRef != nil {
		provider = metadata.NormalizeProvider(agent.Status.SessionRef.Provider)
	}
	if provider == "codex" || agent.Status.SessionRef != nil && agent.Status.SessionRef.Provider == "codex" {
		return result, &Refusal{Code: CodeResumeNativeCodexUnsupported, message: "Codex ResumeAgent requires native observation which is not yet available in the SDK; use projmux agent resume"}
	}
	plan, err := mutationruntime.BuildAgentResumePlan("agent resume", reg, agent)
	if err != nil {
		return result, &Refusal{Code: CodeResumePlanInvalid, message: err.Error(), cause: err}
	}
	project, _ := reg.Project(plan.ProjectUID)
	plan.Workspace, err = mutationruntime.ResolveAgentWorkspaceFor("agent resume", reg, *project, plan.Provider, plan.Workspace.CWD, plan.Workspace.AdditionalWritableRoots)
	if err != nil {
		return result, resumeRefusal(err)
	}
	plan.DialogueReplyOnly = metadata.RecordsDialogueReplyOnly(agent.Metadata.Annotations) || req.DialogueReplyOnly
	plan.ModelOverride, plan.EffortOverride = req.Model, req.Effort
	socket := req.SocketPath
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	if c.projmuxExecutable == "" || !filepath.IsAbs(c.projmuxExecutable) {
		return result, &Refusal{Code: CodeCreateExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
	}
	info, err := os.Stat(c.projmuxExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return result, &Refusal{Code: CodeCreateExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: err}
	}
	execution, transaction := mutationruntime.NewSDKCreate(c.paths, socket, c.projectShell, c.projmuxExecutable, c.projectSessionName, runner, c.createDiscoveryRoots)
	planner := &mutationruntime.SDKAgentPlanner{Paths: c.paths, HomeDir: c.discoveryHome, Shell: c.projectShell, Executable: c.projmuxExecutable, Environment: c.agentEnvironment, Store: transaction.Store, Runtime: execution.Runtime}
	if err := planner.RequireEnabled(plan.Provider); err != nil {
		return result, resumeRefusal(err)
	}
	planner.Initialize()
	paths := func() (config.Paths, error) { return c.paths, nil }
	guidance := &mutationruntime.GuidanceService{Paths: paths}
	launch := &mutationruntime.ResumeLaunchService{Command: planner.Command, Paths: paths, Guidance: guidance, MissingBinary: func(p string) string { return "selected runner is not installed: " + p }}
	prep := mutationruntime.AgentResumePreparation{Plan: launch.PlanAgentResume, PlanWithModel: launch.PlanAgentResumeWithModel, Dialogue: func(w metadata.AgentWorkspace, id string) (string, []string, error) {
		return planner.Command.PlanDialogueLaunch(w, id, func() (string, error) { return c.projmuxExecutable, nil })
	}, PlanLinks: guidance.PlanProjectLinks, PlanGuidance: guidance.PlanAgentGuidance, ResolveSettings: func(p string, a map[string]string, q mutationruntime.AgentSettingsRequest) (mutationruntime.AgentSettingsLaunch, error) {
		return mutationruntime.ResolveAgentSettings(c.paths, p, a, q)
	}}
	// Snapshot preparation and transaction follow the last caller Context check.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	prepared, err := prep.Prepare("agent resume", plan)
	if err != nil {
		return result, resumeRefusal(err)
	}
	machine := &mutationruntime.AgentResumeExecution{Execution: execution, ResolveWorkspace: mutationruntime.ResolveAgentWorkspaceFor, Record: prepared.Record, Layered: prepared.Settings.Layered}
	machine.Handoff = machine.HandOffResumedPaneName
	machine.Bind = func(ctx context.Context, _ *metadata.Registry, _ metadata.Pane, _ mutationruntime.SuperviseSpec, id, cwd, title string) error {
		return planner.Launch.BindAgentPaneOnRoute(ctx, execution.Runtime.Runner, mutationruntime.AgentPaneBinding{PaneID: id, Provider: plan.Provider, ContextDir: cwd, Title: title, Topic: plan.Topic, TopicManual: strings.TrimSpace(plan.Topic) != "", ConversationID: plan.ConversationID})
	}
	var out mutationruntime.AgentResumeResult
	if err := transaction.Transact(mutationruntime.CreateKindResume, machine.Operation("agent resume", plan, prepared.Title, prepared.Argv, &out), execution.ExactProjectOwnershipGuard(plan.ProjectUID)); err != nil {
		return result, resumeRefusal(err)
	}
	projection, err := c.createProjection(socket, core.KindAgent, mutationruntime.ShellCreateResource{UID: plan.AgentUID})
	if err != nil {
		return result, err
	}
	result.Projection = projection
	result.Agent = projection.Items[0].(core.Agent)
	result.PaneID = out.PaneID
	result.Notices = prepared.Notices
	if out.NameReason != "" {
		result.Notices = append([]string{mutationruntime.AgentPaneNameNotice(plan.AgentName, out.NameReason)}, result.Notices...)
	}
	result.Notices = append(plan.SharedConversationNotices("agent resume"), result.Notices...)
	after, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	record := mutationruntime.ResumeReceipt(after, plan.AgentUID, out.PaneUID)
	if record.APIVersion == "" {
		return result, &Refusal{Code: CodeResumeExecutionFailed, message: "committed resume resources changed before receipt projection"}
	}
	if err := unregisterCopy(record, &result.Receipt); err != nil {
		return result, err
	}
	return result, nil
}
func resumeRefusal(err error) error {
	if err == nil {
		return nil
	}
	var failure *mutations.Failure
	if errors.As(err, &failure) {
		return &Refusal{Code: Code(failure.Code), message: err.Error(), cause: err}
	}
	var profile *mutationruntime.ProfileResumeError
	if errors.As(err, &profile) {
		return &Refusal{Code: CodeProfileResumeUnavailable, message: err.Error(), cause: err}
	}
	var agent *mutationruntime.AgentCreateRefusal
	if errors.As(err, &agent) {
		return &Refusal{Code: Code(agent.Reason), message: err.Error(), cause: err}
	}
	return &Refusal{Code: CodeResumeExecutionFailed, message: err.Error(), cause: err}
}
