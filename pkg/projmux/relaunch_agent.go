package projmux

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeRelaunchProcessUnsupported  Code = "relaunch-process-unsupported"
	CodeRelaunchRequestInvalid      Code = "relaunch-request-invalid"
	CodeRelaunchAgentBusy           Code = "relaunch-agent-busy"
	CodeRelaunchNoConversation      Code = "relaunch-no-conversation"
	CodeRelaunchProviderUnsupported Code = "relaunch-provider-unsupported"
	CodeRelaunchExecutionFailed     Code = "relaunch-execution-failed"
	CodeRelaunchStopAlive           Code = "relaunch-stop-alive"
	CodeRelaunchStopUnknown         Code = "relaunch-stop-unknown"
)

// RelaunchAgent shares the CLI restart plan and execution sequence, including
// detached process relaunch and public execution host moves.
// Cancellation prevents admission; after admission Stop and its rebind or
// recovery complete independently of caller cancellation. DryRun changes nothing.
// A successful commit does not acknowledge provider activation or a response.
func (c *Client) RelaunchAgent(ctx context.Context, q core.RelaunchAgentRequest) (core.RelaunchAgentResult, error) {
	return c.relaunchAgent(ctx, q, tmux.ExecRunner{})
}
func (c *Client) relaunchAgent(ctx context.Context, q core.RelaunchAgentRequest, runner runtime.TmuxCommandRunner) (core.RelaunchAgentResult, error) {
	var out core.RelaunchAgentResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(q.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "RelaunchAgentRequest.Ref must name one Agent"}
	}
	if err := validateRelaunchLayers(q); err != nil {
		return out, err
	}
	query, err := mutationQuery(core.KindAgent, q.Ref, q.Project, q.Window, "RelaunchAgentRequest")
	if err != nil {
		return out, resourceRefusal(err)
	}
	socket := q.SocketPath
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	q.SocketPath = socket // Recovery carries the same frozen explicit transport.
	if socket != "" && !filepath.IsAbs(socket) {
		return out, &Refusal{Code: CodeRuntimeRequestInvalid, message: "RelaunchAgentRequest.SocketPath must be absolute"}
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	selected, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbResume, Kind: metadata.KindAgent}, query, selected); err != nil {
		return out, resourceRefusal(err)
	}
	target, _ := reg.Agent(selected.Matches[0].UID)
	processSource := false
	for _, p := range reg.PanesOf(target.Metadata.UID) {
		if p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
			processSource = true
		}
	}
	if processSource || (q.Host != "" && q.Host != "tmux") {
		return c.relaunchProcessAgent(ctx, q, target.Clone(), reg)
	}
	if q.Detach || q.Prompt != "" || q.Binding != (core.ProcessBinding{}) {
		return out, &Refusal{Code: CodeRelaunchRequestInvalid, message: "RelaunchAgentRequest process inputs require a process destination or source"}
	}

	refuse := func(reason, detail string) error {
		return &Refusal{Code: Code(reason), message: fmt.Sprintf("agent relaunch: agent/%s %s (%s); nothing was changed", target.Metadata.Name, detail, reason)}
	}
	provider := metadata.NormalizeProvider(target.Spec.Provider)
	if provider == "" && target.Status.SessionRef != nil {
		provider = metadata.NormalizeProvider(target.Status.SessionRef.Provider)
	}
	if provider != "claude" && provider != "codex" {
		return out, refuse(string(CodeRelaunchProviderUnsupported), fmt.Sprintf("is a %q Agent; --model and --effort apply only to --provider claude or codex", target.Spec.Provider))
	}
	if err := (runtime.AgentInputFields{Prefix: "RelaunchAgentRequest"}).RequireLaunchOptions("agent relaunch", provider, q.Model, q.Effort, false, "nothing was changed"); err != nil {
		return out, &Refusal{Code: CodeRelaunchRequestInvalid, message: err.Error(), cause: err}
	}
	tokens := runtime.AgentRestartTokens{Busy: string(CodeRelaunchAgentBusy), NoConversation: string(CodeRelaunchNoConversation), Phase: func(p metadata.AgentPhase) string {
		return fmt.Sprintf("is %s; only a Running, Offline, or Failed Agent is relaunched", p)
	}}
	p := runtime.AgentRestartPlan{Target: target.Clone(), Provider: provider, Interaction: target.EffectiveInteraction(time.Now()).Kind}
	if err := p.CheckTarget(reg, tokens, refuse); err != nil {
		return out, err
	}
	settings := runtime.AgentSettingsRequest{Model: q.Model, Effort: q.Effort, Profile: q.Profile, Instructions: q.Instructions, Reset: q.Reset, Source: metadata.SettingSourceRelaunch}
	paths := func() (config.Paths, error) { return c.paths, nil }
	guidance := &runtime.GuidanceService{Paths: paths}
	err = p.Plan(settings, runtime.AgentRestartPlanning{
		Tokens: tokens, Refuse: refuse,
		Predict: func(pane string) (runtime.AgentResumePlan, error) {
			predicted := reg.Clone()
			if pane != "" {
				s := mutations.NewStore("")
				if err := s.Mutator().DeletePane(&predicted, pane); err != nil {
					return runtime.AgentResumePlan{}, err
				}
			}
			a, ok := predicted.Agent(target.Metadata.UID)
			if !ok {
				return runtime.AgentResumePlan{}, errors.New("target Agent disappeared")
			}
			plan, err := runtime.BuildAgentResumePlan("agent resume", predicted, a)
			if err != nil {
				return plan, err
			}
			project, _ := predicted.Project(plan.ProjectUID)
			plan.Workspace, err = runtime.ResolveAgentWorkspaceFor("agent resume", predicted, *project, plan.Provider, plan.Workspace.CWD, plan.Workspace.AdditionalWritableRoots)
			return plan, err
		},
		ResolveSettings: func(plan runtime.AgentResumePlan, q runtime.AgentSettingsRequest) (runtime.AgentSettingsLaunch, error) {
			if !plan.DialogueReplyOnly {
				q = q.WithPromptParts(guidance.PlanAgentGuidance(provider, target.Metadata.Annotations), guidance.PlanProjectLinks(provider, plan.Project, target.Metadata.Annotations))
			}
			return runtime.ResolveAgentSettings(c.paths, provider, target.Metadata.Annotations, q)
		},
		PreflightCodex: func(plan runtime.AgentResumePlan, s runtime.AgentSettingsLaunch) error {
			if _, _, err := runtime.NativeCodexResumeProfile(paths, s.LaunchAnnotations(plan.Annotations), plan.ProjectUID); err != nil {
				return refuse(string(CodeRelaunchNoConversation), "cannot be resumed: "+err.Error())
			}
			if err := runtime.ValidateStoredCodexNativeResumeRoute(plan.Ref); err != nil {
				return refuse(string(CodeRelaunchNoConversation), "cannot be resumed: "+err.Error())
			}
			return relaunchCodexPolicy(plan, s, paths, refuse)
		},
	})
	if err != nil {
		return out, err
	}
	out = core.RelaunchAgentResult{AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: provider, Phase: core.AgentPhase(target.Status.Phase), Interaction: string(p.Interaction), PaneUID: p.PaneUID, ConversationID: p.Resume.ConversationID, DryRun: q.DryRun, Restart: p.Running, ConfirmationRequired: p.ConfirmationRequired(), CurrentSettings: publicRelaunchSettings(p.Settings.Resolution.Current), NewSettings: publicRelaunchSettings(p.Settings.Resolution.New), RelaunchReasons: append([]string{}, p.Settings.Resolution.Reasons...)}
	out.Outcome = p.RelaunchDisposition(settings, q.DryRun)
	if out.Outcome == "unchanged" {
		out.Unchanged = true
		out.Restart = false
		out.ConfirmationRequired = false
		out.NewPaneUID = p.PaneUID
		return out, nil
	}
	if q.DryRun {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// There is no further caller cancellation gate once the operation is admitted.
	admitted := context.WithoutCancel(ctx)
	freshPhaseRefused := false
	var freshPhase metadata.AgentPhase
	_, err = p.Run(runtime.AgentRestartExecution{
		Confirm: q.Confirm, Tokens: tokens, Refuse: refuse,
		ValidateTransport: func() error { _, err := runtime.ResolveDeleteTarget("agent relaunch", "", socket, nil); return err },
		BeforeStop: func() error {
			// Executable and launch prerequisites are checked before Stop, without
			// preparing snapshots or starting/resuming a provider.
			if c.projmuxExecutable == "" || !filepath.IsAbs(c.projmuxExecutable) {
				return &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
			}
			info, err := os.Stat(c.projmuxExecutable)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return &Refusal{Code: CodeExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: err}
			}
			execution, transaction := runtime.NewSDKCreate(c.paths, socket, c.projectShell, c.projmuxExecutable, c.projectSessionName, runner, c.createDiscoveryRoots)
			planner := &runtime.SDKAgentPlanner{Paths: c.paths, HomeDir: c.discoveryHome, Shell: c.projectShell, Executable: c.projmuxExecutable, Environment: c.agentEnvironment, Store: transaction.Store, Runtime: execution.Runtime}
			if err := planner.RequireEnabled(provider); err != nil {
				return resumeRefusal(err)
			}
			planner.Initialize()
			if planner.Command.LookupBinary(provider) == "" {
				return &Refusal{Code: CodeRelaunchExecutionFailed, message: "selected runner is not installed: " + provider}
			}

			if p.Running && provider == "codex" {
				if err := relaunchCodexProbe(ctx, planner, p, refuse); err != nil {
					return err
				}
			}
			return ctx.Err()
		},
		Stop: func() error {
			var err error
			out.Stop, err = c.deleteWithRunner(admitted, core.DeleteRequest{Kind: core.KindPane, Ref: "uid:" + p.PaneUID, SocketPath: socket, Confirm: true}, runner)
			return err
		},
		Observe: func() (runtime.AgentRestartLiveness, error) { return c.relaunchLiveness(p, socket, runner) },
		StopFailed: func(err error, live runtime.AgentRestartLiveness, observe error) error {
			if live == runtime.AgentRestartClosed {
				out.Notices = append(out.Notices, "closing the managed pane reported an error, but that pane is already closed; continuing resume: "+err.Error())
				return nil
			}
			out.Outcome = "stop-failed"
			out.Recovery = strings.Replace(relaunchRecovery(q, target.Metadata.UID, p.Resume, false), "agent resume", "agent relaunch", 1)
			if live == runtime.AgentRestartUnknown {
				return &Refusal{Code: CodeRelaunchStopUnknown, message: "could not observe whether the managed Pane is still alive: " + fmt.Sprint(observe), cause: err}
			}
			return &Refusal{Code: CodeRelaunchStopAlive, message: "managed Pane keeps running with its previous launch options", cause: err}
		},
		Resume: func(stopped runtime.StoppedAgentPane) error {
			fresh, err := resources.Load(c.paths)
			if err != nil {
				return err
			}
			a, ok := fresh.Agent(target.Metadata.UID)
			if !ok {
				return errors.New("target Agent disappeared before resume")
			}
			if err := runtime.RequireResumablePhase("agent resume", a); err != nil {
				freshPhaseRefused, freshPhase = true, a.Status.Phase
				return err
			}
			plan, err := runtime.BuildAgentResumePlan("agent resume", fresh, a)
			if err != nil {
				return err
			}
			plan.ModelOverride, plan.EffortOverride, plan.OverrideSource = q.Model, q.Effort, metadata.SettingSourceRelaunch
			plan.LayerChanges = settings
			plan.StoppedPane = stopped
			project, _ := fresh.Project(plan.ProjectUID)
			plan.Workspace, err = runtime.ResolveAgentWorkspaceFor("agent resume", fresh, *project, plan.Provider, plan.Workspace.CWD, plan.Workspace.AdditionalWritableRoots)
			if err != nil {
				return err
			}
			out.Resume, err = c.resumeAgentPlan(admitted, plan, socket, runner)
			return err
		},
		ResumeFailed: func(err error) error {
			out.Outcome = "resume-failed"
			if freshPhaseRefused {
				out.Recovery = ""
				stage := ""
				if p.Running {
					stage = " after Stop of Pane uid:" + p.PaneUID
				}
				detail := fmt.Sprintf("Agent uid:%s cannot resume%s: Registry phase is %s (selected socket %q). Inspect the exact Registry and runtime state; additional action is required before another operation. No immediate recovery command is available.", target.Metadata.UID, stage, freshPhase, socket)
				out.Notices = append(out.Notices, detail)
				return &Refusal{Code: CodeRelaunchExecutionFailed, message: detail, cause: err}
			}
			plain := errors.Is(err, codexappserver.ErrSettingsNotApplied) || errors.Is(err, codexappserver.ErrPolicyMismatch)
			out.Recovery = relaunchRecovery(q, target.Metadata.UID, p.Resume, plain)
			return &Refusal{Code: CodeRelaunchExecutionFailed, message: "Agent did not resume; inspect the receipt and recover with: " + out.Recovery, cause: err}
		},
		NewPane: func() string { return out.Resume.Agent.Status.PaneRef },
	})
	if err != nil {
		if out.Outcome == "restarted" || out.Outcome == "resumed" {
			out.Outcome = "refused"
		}
		return out, relaunchRefusal(err)
	}
	out.NewPaneUID = out.Resume.Agent.Status.PaneRef
	out.Notices = append(out.Notices, out.Resume.Notices...)
	return out, nil
}

func relaunchRefusal(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var f *mutations.Failure
	if errors.As(err, &f) {
		return &Refusal{Code: Code(f.Code), message: err.Error(), cause: err}
	}
	var r *Refusal
	if errors.As(err, &r) {
		return err
	}
	return &Refusal{Code: CodeRelaunchExecutionFailed, message: err.Error(), cause: err}
}

func publicRelaunchSettings(s agentsettings.Settings) core.RelaunchAgentSettings {
	setting := func(v agentsettings.Setting) core.RelaunchAgentSetting {
		return core.RelaunchAgentSetting{Value: v.Value, Source: v.Source, ProfileValue: v.ProfileValue, Override: v.Override}
	}
	return core.RelaunchAgentSettings{Profile: core.RelaunchAgentProfile{Name: s.Profile.Name, Digest: s.Profile.Digest, Source: s.Profile.Source}, Instructions: setting(s.Instructions), Model: setting(s.Model), Effort: setting(s.Effort)}
}
func validateRelaunchLayers(q core.RelaunchAgentRequest) error {
	bad := func(s string) error { return &Refusal{Code: CodeRelaunchRequestInvalid, message: s} }
	if q.Profile != nil && *q.Profile != "" {
		if err := profile.ValidateName(*q.Profile); err != nil {
			return bad(err.Error())
		}
	}
	if q.Instructions != nil && *q.Instructions != "" {
		if err := persona.ValidateName(*q.Instructions); err != nil {
			return bad(err.Error())
		}
	}
	for _, item := range q.Reset {
		if !slices.Contains(agentsettings.Items(), item) {
			return bad("RelaunchAgentRequest.Reset must contain instructions, model or effort")
		}
	}
	for i, given := range []bool{q.Instructions != nil, q.Model != "", q.Effort != ""} {
		if given && slices.Contains(q.Reset, agentsettings.Items()[i]) {
			return bad("RelaunchAgentRequest cannot set and reset " + agentsettings.Items()[i] + " together")
		}
	}
	return nil
}

func (c *Client) relaunchLiveness(p runtime.AgentRestartPlan, socket string, runner runtime.TmuxCommandRunner) (runtime.AgentRestartLiveness, error) {
	reg, err := resources.Load(c.paths)
	if err != nil {
		return runtime.AgentRestartUnknown, err
	}
	a, ok := reg.Agent(p.Target.Metadata.UID)
	if !ok {
		return runtime.AgentRestartUnknown, errors.New("target Agent disappeared")
	}
	if a.Status.Phase != metadata.PhaseRunning || a.Status.PaneRef != p.PaneUID {
		return runtime.AgentRestartClosed, nil
	}
	if _, ok := reg.Pane(p.PaneUID); !ok {
		return runtime.AgentRestartClosed, nil
	}
	route, err := runtime.ResolveDeleteTarget("agent relaunch", "", socket, nil)
	if err != nil {
		return runtime.AgentRestartUnknown, err
	}
	live := &runtime.TmuxPaneDeleteRuntime{Runner: runner}
	live.UseExactTarget(route)
	rows, err := live.Inventory(context.Background())
	if err != nil {
		return runtime.AgentRestartUnknown, err
	}
	if len(rows) == 0 {
		return runtime.AgentRestartUnknown, errors.New("exact tmux inventory was empty")
	}
	for _, r := range rows {
		if r.PaneUID == p.PaneUID {
			return runtime.AgentRestartAlive, nil
		}
	}
	return runtime.AgentRestartClosed, nil
}
func relaunchRecovery(q core.RelaunchAgentRequest, uid string, p runtime.AgentResumePlan, plain bool) string {
	command := "projmux agent resume uid:" + uid + " --project uid:" + p.ProjectUID + " --window uid:" + p.WindowUID
	if !plain {
		if q.Profile != nil || q.Instructions != nil || len(q.Reset) > 0 {
			command = strings.Replace(command, "agent resume", "agent relaunch", 1)
		}
		if q.Profile != nil {
			v := *q.Profile
			if v == "" {
				v = "none"
			}
			command += " --profile " + runtime.ShellQuote(v)
		}
		if q.Instructions != nil {
			v := *q.Instructions
			if v == "" {
				v = "none"
			}
			command += " --instructions " + runtime.ShellQuote(v)
		}
		if len(q.Reset) > 0 {
			command += " --reset " + runtime.ShellQuote(strings.Join(q.Reset, ","))
		}
		if q.Model != "" {
			command += " --model " + runtime.ShellQuote(q.Model)
		}
		if q.Effort != "" {
			command += " --effort " + runtime.ShellQuote(q.Effort)
		}
	}
	if q.SocketPath != "" {
		command += " --socket-path " + runtime.ShellQuote(q.SocketPath)
	}
	return command
}
func relaunchCodexPolicy(p runtime.AgentResumePlan, s runtime.AgentSettingsLaunch, paths func() (config.Paths, error), refuse func(string, string) error) error {
	if !s.Resolution.ProfileSwitched {
		return nil
	}
	var old codexappserver.ThreadPolicy
	known := true
	if p.Annotations[metadata.AnnotationAgentProfile] != "" {
		_, policy, err := runtime.NativeCodexResumeProfile(paths, p.Annotations)
		old, known = policy, err == nil
	}
	_, next, err := runtime.NativeCodexResumeProfile(paths, s.LaunchAnnotations(p.Annotations))
	if err != nil {
		return refuse(string(CodeRelaunchNoConversation), "cannot be resumed: "+err.Error())
	}
	var kept []string
	if next.Sandbox == "" && (!known || old.Sandbox != "") {
		kept = append(kept, "sandbox")
	}
	if next.ApprovalPolicy == "" && (!known || old.ApprovalPolicy != "") {
		kept = append(kept, "approval")
	}
	if len(kept) > 0 {
		return refuse("relaunch-codex-permissions-kept", "cannot drop the "+strings.Join(kept, " and ")+" its Codex thread may have; a Codex resume can set a sandbox or an approval but not remove one")
	}
	return nil
}
func relaunchCodexProbe(ctx context.Context, planner *runtime.SDKAgentPlanner, p runtime.AgentRestartPlan, refuse func(string, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fail := func(err error) error {
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return refuse("relaunch-codex-settings-unsupported", "cannot have its Codex thread's model, effort, sandbox and approval changed: the Codex app server did not take thread/settings/update: "+err.Error())
	}
	native, err := planner.NativeSession()
	if err != nil {
		return fail(err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	_, err = runtime.ResolveNativeCodexResumeRoute(probeCtx, p.Resume.Ref, "uid:"+p.Target.Metadata.UID, native.Resolve)
	if err != nil {
		return fail(err)
	}
	client, err := native.Open(probeCtx, true)
	if err != nil {
		return fail(err)
	}
	defer client.Close()
	if err := client.UpdateThreadSettings(probeCtx, p.Resume.ConversationID, codexappserver.ThreadSettings{}); err != nil {
		return fail(err)
	}
	return nil
}
