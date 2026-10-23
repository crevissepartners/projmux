package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// agentResumeLaunch is one planned resume launch.
type agentResumeLaunch struct {
	title string
	argv  []string
	// personaUnavailable is set when the Agent records a persona this launch
	// could not re-pass. It never fails the resume: the Agent comes back
	// without its persona and the consumer discloses personaNotice.
	personaUnavailable *persona.Error
	// effortInvalid is the recorded effort this launch did not re-pass
	// because Claude would not take it. Like personaUnavailable it never
	// fails the resume: the consumer discloses effortNotice.
	// effortSkipped reports that effortInvalid holds such a value, which may
	// itself be empty.
	effortInvalid string
	effortSkipped bool
	// profileName and profileDigest are the profile this launch re-applied
	// and the digest of the content it applied, both empty when the Agent
	// records no profile. Every consumer records the digest on the Agent.
	profileName   string
	profileDigest string
	// projectLinksUnavailable is set when the launch annotations name Project
	// label link rules whose snapshot this launch could not pass. Like
	// personaUnavailable it never fails the resume: the consumer discloses
	// projectLinksNotice.
	projectLinksUnavailable error
	// agentGuidanceUnavailable is set when the launch annotations name agent
	// guidance whose snapshot this launch could not pass. Like
	// projectLinksUnavailable it never fails the resume: the consumer
	// discloses agentGuidanceNotice.
	agentGuidanceUnavailable error
}

// agentSettingsLaunch is the layered settings one `agent resume`, `agent
// relaunch`, Continue replay or resume-picker create launches an Agent with: the resolution, and the snapshot
// of the new instructions content when the launch passes one. The zero value
// means the launch is not layered and reads the Agent's annotations as they
// are.
type agentSettingsLaunch struct {
	managedBase *mutationruntime.ManagedCodexBase

	// projectGuidance is recorded with an ordinary process resume reservation.
	projectGuidance projectGuidanceLaunch
	layered         bool
	resolution      agentsettings.Resolution
	// instructionsErr is why the instructions the layers ask for cannot be
	// read now (resolution.InstructionsUnavailable names them).
	instructionsErr error
	// snapshot writes the snapshot of the new instructions content; nil when
	// the launch passes none. It is written only by a launch, never by a
	// preflight or a dry run.
	snapshot func() error
	// snapshotErr is why the new instructions snapshot could not be written.
	snapshotErr error
}

// agentSettingsRequest is what one launch asks of an Agent's layers: the
// explicit overrides (model, effort and instructions, recorded under source),
// the items whose override it removes (reset), and the profile it switches to
// (profile; "" for none). The zero value asks nothing: the launch runs what
// the layers resolve to now.
type agentSettingsRequest struct {
	managedCodexBase bool
	projectUID       string
	model, effort    string
	// instructions and profile are nil when not asked; a pointer to "" is an
	// explicit none.
	instructions *string
	profile      *string
	reset        []string
	source       string
	// guidance and linkRules are the digests of the agent guidance and the
	// Project's label link rules this launch passes (withPromptParts), nil
	// when it passes none of its own. They change no layer; the resolver
	// only reports that they differ from the recorded ones.
	guidance, projectGuidance, linkRules *string
}

// relaunchProfileError is a profile an `agent relaunch --profile` cannot
// switch to: gone, invalid, or for another provider. Reason is the store's
// own token, or profile-provider-mismatch.
type relaunchProfileError struct {
	name   string
	reason string
	detail string
}

func (e *relaunchProfileError) Error() string {
	return fmt.Sprintf("profile %q cannot be applied (%s): %s", e.name, e.reason, e.detail)
}
func (v agentResumeLaunch) shared() mutationruntime.AgentResumeLaunch {
	return mutationruntime.AgentResumeLaunch{Title: v.title, Argv: v.argv, PersonaUnavailable: v.personaUnavailable, EffortInvalid: v.effortInvalid, EffortSkipped: v.effortSkipped, ProfileName: v.profileName, ProfileDigest: v.profileDigest, ProjectLinksUnavailable: v.projectLinksUnavailable, AgentGuidanceUnavailable: v.agentGuidanceUnavailable}
}
func localAgentResumeLaunch(v mutationruntime.AgentResumeLaunch) agentResumeLaunch {
	return agentResumeLaunch{title: v.Title, argv: v.Argv, personaUnavailable: v.PersonaUnavailable, effortInvalid: v.EffortInvalid, effortSkipped: v.EffortSkipped, profileName: v.ProfileName, profileDigest: v.ProfileDigest, projectLinksUnavailable: v.ProjectLinksUnavailable, agentGuidanceUnavailable: v.AgentGuidanceUnavailable}
}

func (v agentSettingsLaunch) shared() mutationruntime.AgentSettingsLaunch {
	return mutationruntime.AgentSettingsLaunch{ManagedBase: v.managedBase, ProjectGuidance: v.projectGuidance.shared(), Layered: v.layered, Resolution: v.resolution, InstructionsErr: v.instructionsErr, Snapshot: v.snapshot, SnapshotErr: v.snapshotErr}
}
func localAgentSettingsLaunch(v mutationruntime.AgentSettingsLaunch) agentSettingsLaunch {
	return agentSettingsLaunch{managedBase: v.ManagedBase, projectGuidance: localProjectGuidanceLaunch(v.ProjectGuidance), layered: v.Layered, resolution: v.Resolution, instructionsErr: v.InstructionsErr, snapshot: v.Snapshot, snapshotErr: v.SnapshotErr}
}

func (v agentSettingsRequest) shared() mutationruntime.AgentSettingsRequest {
	return mutationruntime.AgentSettingsRequest{ManagedCodexBase: v.managedCodexBase, ProjectUID: v.projectUID, Model: v.model, Effort: v.effort, Instructions: v.instructions, Profile: v.profile, Reset: v.reset, Source: v.source, Guidance: v.guidance, ProjectGuidance: v.projectGuidance, LinkRules: v.linkRules}
}
func localAgentSettingsRequest(v mutationruntime.AgentSettingsRequest) agentSettingsRequest {
	return agentSettingsRequest{managedCodexBase: v.ManagedCodexBase, projectUID: v.ProjectUID, model: v.Model, effort: v.Effort, instructions: v.Instructions, profile: v.Profile, reset: v.Reset, source: v.Source, guidance: v.Guidance, projectGuidance: v.ProjectGuidance, linkRules: v.LinkRules}
}

func (l agentResumeLaunch) agentGuidanceNotice(n string) string {
	return l.shared().AgentGuidanceNotice(n)
}
func (l agentResumeLaunch) projectLinksNotice(n string) string {
	return l.shared().ProjectLinksNotice(n)
}
func (l agentResumeLaunch) personaNotice(n string) string { return l.shared().PersonaNotice(n) }
func (l agentResumeLaunch) effortNotice(n string) string  { return l.shared().EffortNotice(n) }
func (q agentSettingsRequest) changesLayers() bool        { return q.shared().ChangesLayers() }
func (q agentSettingsRequest) withPromptParts(g agentGuidanceLaunch, l projectLinksLaunch) agentSettingsRequest {
	return localAgentSettingsRequest(q.shared().WithPromptParts(g.shared(), l.shared()))
}
func (l agentSettingsLaunch) writeSnapshot() agentSettingsLaunch {
	return localAgentSettingsLaunch(l.shared().WriteSnapshot())
}
func (l agentSettingsLaunch) launchAnnotations(a map[string]string) map[string]string {
	return l.shared().LaunchAnnotations(a)
}
func (l agentSettingsLaunch) model(s string) string  { return l.shared().Model(s) }
func (l agentSettingsLaunch) notice(s string) string { return l.shared().Notice(s) }
func (l agentSettingsLaunch) record(r *coremetadata.Registry, m coremetadata.Mutator, u string) error {
	return l.shared().Record(r, m, u)
}

func resolveAgentSettings(h func() (string, error), e func(string) string, p string, a map[string]string, q agentSettingsRequest) (agentSettingsLaunch, error) {
	l, err := mutationruntime.ResolveAgentSettingsWithPaths(func() (config.Paths, error) { return configPaths(h, e) }, p, a, q.shared())
	return localAgentSettingsLaunch(l), localResumeError(err)
}

func resolveSwitchProfile(h func() (string, error), e func(string) string, p, n string, u ...string) (profile.Profile, profile.Spec, error) {
	r, s, err := mutationruntime.ResolveSwitchProfileWithPaths(func() (config.Paths, error) { return configPaths(h, e) }, p, n, u...)
	return r, s, localResumeError(err)
}
func requireResumablePhase(s string, a *coremetadata.Agent) error {
	return mutationruntime.RequireResumablePhase(s, a)
}

func resumeArgsForAgent(p, id string) ([]string, error) {
	return mutationruntime.ResumeArgsForAgent(p, id)
}
func claudeResumeSnapshotArgs(p string, a map[string]string) []string {
	return mutationruntime.ClaudeResumeSnapshotArgs(p, a)
}
func claudeResumeEffort(p string, a map[string]string) (string, string, bool) {
	return mutationruntime.ClaudeResumeEffort(p, a)
}
func (c *aiCommand) PlanAgentResume(p string, w coremetadata.AgentWorkspace, id string, a map[string]string) (agentResumeLaunch, error) {
	return c.PlanAgentResumeWithModel(p, w, id, a, "")
}
func (c *aiCommand) PlanAgentResumeWithModel(p string, w coremetadata.AgentWorkspace, id string, a map[string]string, m string) (agentResumeLaunch, error) {
	v, err := c.resumeLaunchService().PlanAgentResumeWithModel(p, w, id, a, m)
	return localAgentResumeLaunch(v), err
}
func (c *aiCommand) resumePersonaSnapshot(p string, a map[string]string) (string, *persona.Error) {
	return c.resumeLaunchService().ResumePersonaSnapshot(p, a)
}

func (c *aiCommand) BindResumedAgentPaneWithSourceOnRoute(ctx context.Context, r tmuxCommandRunner, id, p, cwd, t, conv, src string) error {
	return c.sharedAgentLaunch().BindResumedAgentPaneWithSourceOnRoute(ctx, r, id, p, cwd, t, conv, src)
}

func (c *aiCommand) resumeProjectSystemPromptFile(p string, a map[string]string, base string) (string, error) {
	return c.sharedGuidance().ResumeProjectSystemPromptFile(p, a, base)
}

func (c *aiCommand) resumeGuidanceSystemPromptFile(p string, a map[string]string, base string) (string, error) {
	return c.sharedGuidance().ResumeGuidanceSystemPromptFile(p, a, base)
}

func (c *aiCommand) resumeLaunchService() *mutationruntime.ResumeLaunchService {
	return &mutationruntime.ResumeLaunchService{Command: c.sharedAgentCommand(), Paths: c.processCreatePaths, Guidance: c.sharedGuidance(), MissingBinary: c.missingAgentRunnerMessage}
}

func (p agentResumePlan) sharedPlan() mutationruntime.AgentResumePlan {
	return mutationruntime.AgentResumePlan{DialogueReplyOnly: p.dialogueReplyOnly, AgentUID: p.agentUID, AgentName: p.agentName, Provider: p.provider, ConversationID: p.conversationID, Ref: p.ref, ProjectUID: p.projectUID, ProjectRoot: p.projectRoot, Project: p.project, Workspace: p.workspace, Topic: p.topic, WindowUID: p.windowUID, AnchorUID: p.anchorUID, Shared: p.shared, Annotations: p.annotations, ModelOverride: p.modelOverride, EffortOverride: p.effortOverride, OverrideSource: p.overrideSource, LayerChanges: p.layerChanges.shared(), StoppedPane: mutationruntime.StoppedAgentPane{UID: p.stoppedPane.uid, Name: p.stoppedPane.name}, VirtualWindow: p.retiredProcess != nil && p.virtualWindow}
}
func localAgentResumePlan(p mutationruntime.AgentResumePlan) agentResumePlan {
	return agentResumePlan{dialogueReplyOnly: p.DialogueReplyOnly, agentUID: p.AgentUID, agentName: p.AgentName, provider: p.Provider, conversationID: p.ConversationID, ref: p.Ref, projectUID: p.ProjectUID, projectRoot: p.ProjectRoot, project: p.Project, workspace: p.Workspace, topic: p.Topic, windowUID: p.WindowUID, anchorUID: p.AnchorUID, shared: p.Shared, annotations: p.Annotations, modelOverride: p.ModelOverride, effortOverride: p.EffortOverride, overrideSource: p.OverrideSource, layerChanges: localAgentSettingsRequest(p.LayerChanges), stoppedPane: stoppedAgentPane{uid: p.StoppedPane.UID, name: p.StoppedPane.Name}, virtualWindow: p.VirtualWindow}
}
func planAgentResume(s string, r coremetadata.Registry, a *coremetadata.Agent, retiring ...bool) (agentResumePlan, error) {
	p, e := mutationruntime.BuildAgentResumePlan(s, r, a, retiring...)
	return localAgentResumePlan(p), e
}

func (r *agentRebinder) sharedResumePreparation() mutationruntime.AgentResumePreparation {
	p := mutationruntime.AgentResumePreparation{
		Plan: func(provider string, w coremetadata.AgentWorkspace, id string, a map[string]string) (mutationruntime.AgentResumeLaunch, error) {
			v, e := r.launcher.PlanAgentResume(provider, w, id, a)
			return v.shared(), e
		},
		PlanLinks: func(provider string, p coremetadata.Project, a map[string]string) mutationruntime.ProjectLinksLaunch {
			return planProjectLinksWith(r.launcher, provider, p, a).shared()
		},
		PlanGuidance: func(provider string, a map[string]string) mutationruntime.AgentGuidanceLaunch {
			return planAgentGuidanceWith(r.launcher, provider, a).shared()
		},
		ResolveSettings: func(provider string, a map[string]string, q mutationruntime.AgentSettingsRequest) (mutationruntime.AgentSettingsLaunch, error) {
			v, e := r.resolveSettings(provider, a, localAgentSettingsRequest(q))
			return v.shared(), e
		},
	}
	if l, ok := r.launcher.(claudeDialogueLauncher); ok {
		p.Dialogue = l.PlanClaudeDialogueLaunch
	}
	if l, ok := r.launcher.(agentResumeModelLauncher); ok {
		p.PlanWithModel = func(provider string, w coremetadata.AgentWorkspace, id string, a map[string]string, m string) (mutationruntime.AgentResumeLaunch, error) {
			v, e := l.PlanAgentResumeWithModel(provider, w, id, a, m)
			return v.shared(), e
		}
	}
	return p
}

func localResumeError(err error) error {
	var p *mutationruntime.ProfileResumeError
	if errors.As(err, &p) {
		return &profileResumeError{name: p.Name, reason: p.Reason, detail: p.Detail}
	}
	var r *mutationruntime.RelaunchProfileError
	if errors.As(err, &r) {
		return &relaunchProfileError{name: r.Name, reason: r.Reason, detail: r.Detail}
	}
	return err
}
