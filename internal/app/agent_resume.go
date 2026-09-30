package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// agentResumeLauncher is the provider-launch seam of `agent resume`.
//
// It is deliberately a *different* interface from agentLauncher even though one
// object satisfies both, because the two verbs must never share a launch
// construction. agentLauncher.PlanAgentLaunch builds the argv that starts a
// fresh conversation; PlanAgentResume builds the provider's resume argv from a
// conversation id and has no way to produce a fresh-start argv at all. Keeping
// them apart is what makes "resume never falls through to create" a property of
// the type system rather than of a code review.
type agentResumeLauncher interface {
	// RequireAgentEnabled applies the Settings enabled-agents gate. It is the
	// same gate `create agent` runs: switching a provider off in Settings does
	// not become bypassable by resuming instead of creating.
	RequireAgentEnabled(provider string) error
	// PlanAgentResume builds the provider's *resume* launch for one stored
	// conversation id. It creates nothing, so every failure it can report --
	// a malformed conversation id, an unknown provider, a missing provider
	// binary -- costs zero mutations and zero tmux objects. annotations are the
	// resumed Agent's own metadata annotations, passed through unread: the
	// seam alone decides what they mean for the launch (the persona).
	PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error)
	BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error
}

// agentResumeModelLauncher is the optional seam of an `agent resume --model`.
// PlanAgentResumeWithModel is PlanAgentResume with the model passed once in
// the launch argv; an empty model is exactly PlanAgentResume. It is separate
// so the other resume consumers -- Continue replay and the resume picker --
// have no way to pass a model at all.
type agentResumeModelLauncher interface {
	PlanAgentResumeWithModel(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string, model string) (agentResumeLaunch, error)
}

var _ agentResumeModelLauncher = (*aiCommand)(nil)

// The aiCommand is the production implementation of both launch seams. The two
// methods below live here rather than beside PlanAgentLaunch so this Phase adds
// the resume seam without editing the file that owns the create seam.
var _ agentResumeLauncher = (*aiCommand)(nil)

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

// agentGuidanceNotice is the one-line disclosure of agent guidance the resume
// could not pass, or "" when there is nothing to disclose.
func (l agentResumeLaunch) agentGuidanceNotice(label string) string {
	if l.agentGuidanceUnavailable == nil {
		return ""
	}
	return agentGuidanceNotice(label, l.agentGuidanceUnavailable)
}

// projectLinksNotice is the one-line disclosure of Project label link rules
// the resume could not pass, or "" when there is nothing to disclose.
func (l agentResumeLaunch) projectLinksNotice(label string) string {
	if l.projectLinksUnavailable == nil {
		return ""
	}
	return projectLinksNotice(label, l.projectLinksUnavailable)
}

// personaNotice is the one-line disclosure of a persona the resume could not
// re-pass, or "" when there is nothing to disclose. label names the Agent the
// way the consumer's neighbouring notices do.
func (l agentResumeLaunch) personaNotice(label string) string {
	if l.personaUnavailable == nil {
		return ""
	}
	return fmt.Sprintf("projmux: agent/%s resumed without its instructions %s (%s): %s",
		label, l.personaUnavailable.Name, l.personaUnavailable.Reason, l.personaUnavailable.Detail)
}

// effortNotice is the one-line disclosure of a recorded effort the resume did
// not re-pass, or "" when there is nothing to disclose. label names the Agent
// the way personaNotice does.
func (l agentResumeLaunch) effortNotice(label string) string {
	if !l.effortSkipped {
		return ""
	}
	return fmt.Sprintf("projmux: agent/%s resumed without its effort %q (%s): not one of %s",
		label, l.effortInvalid, claudeEffortReasonInvalid, strings.Join(claudeEffortLevels, ", "))
}

// PlanAgentResume builds the provider resume launch for one stored conversation.
//
// It is the create seam's PlanAgentLaunch with exactly one substitution: the
// exec argv comes from the provider's own resume builder instead of the plain
// binary invocation. There is deliberately no fallback: if the provider cannot
// render a resume argv for this conversation id, the error is returned and the
// caller stops. Degrading to a fresh session here is what the interactive resume
// picker does (ai.go's runSelectedResumeSession), and it is precisely what this
// route must not do, because a resume that silently starts a new conversation
// loses the operator's context without telling them.
//
// A Claude Agent created with a persona is resumed with the snapshot its
// persona-digest annotation names. A snapshot that is gone does not stop the
// resume. The seam reads the annotations it is given: the `agent resume`
// rebind, Continue replay and a resume-picker create hand it the instructions
// the settings layers resolve to, with a new snapshot and the snapshot mode
// off when their name or current content changed
// (agentSettingsLaunch.launchAnnotations).
//
// A Claude Agent whose persona was attached or detached after its
// conversation started also records the system prompt snapshot mode `off`,
// and every resume of it passes `--system-prompt-snapshot off` so Claude
// rebuilds the system prompt instead of replaying the one it recorded before
// the attach. An Agent without that annotation gets exactly the argv it got
// before the annotation existed.
//
// An Agent created or resumed with --effort records it, and Claude and Codex
// resumes re-pass valid values. An invalid recorded value is skipped and
// disclosed. The model is not re-passed on ordinary resume: the conversation
// owns it. Only a layered launch passes one, through
// PlanAgentResumeWithModel: an `agent resume --model`, or the profile's model
// when it differs from the recorded one (agentsettings.Resolve); the launch
// records it on the Agent (AnnotationAgentModel), and later plain resumes do
// not re-pass it.
//
// A Claude Agent created with a profile is resumed with that profile's
// current permissions: the profile is re-read by name, its settings snapshot
// rebuilt and passed as --settings, and the launch reports the digest it
// applied. A profile that is gone or invalid fails the resume with
// profile-resume-unavailable; there is no resume without its permissions.
func (c *aiCommand) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	return c.PlanAgentResumeWithModel(provider, workspace, conversationID, annotations, "")
}

// PlanAgentResumeWithModel is PlanAgentResume with model passed once, where
// create puts it: ahead of the effort. The model is validated by the
// `agent resume` preflight and recorded by the rebind transaction, not here.
func (c *aiCommand) PlanAgentResumeWithModel(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string, model string) (agentResumeLaunch, error) {
	mode := normalizeAIMode(provider)
	resumeArgv, err := resumeArgsForAgent(mode, conversationID)
	if err != nil {
		return agentResumeLaunch{}, err
	}
	agentBin := c.findAgentBinary(mode)
	if agentBin == "" {
		// No displayMessage here: this route is detached and non-interactive, so
		// the diagnostic belongs on the error the caller propagates rather than
		// in a tmux status line the operator may not be looking at.
		return agentResumeLaunch{}, errors.New(c.missingAgentRunnerMessage(mode))
	}
	resumeArgv[0] = agentBin
	// The workspace half of the argv comes from the same provider grammar the
	// create seam uses, so a provider whose option arity is written down once
	// cannot be spelled two ways. There is no payload on this route -- the
	// conversation id is the provider's own resume option, not an operand -- so
	// no option terminator participates.
	workspaceArgs, err := providerLaunchArgs(mode, coremetadata.AgentWorkspace{
		CWD:                     strings.TrimSpace(workspace.CWD),
		AdditionalWritableRoots: workspace.AdditionalWritableRoots,
	}, nil)
	if err != nil {
		return agentResumeLaunch{}, err
	}
	// The model, the effort and the persona go where create puts them, before the
	// workspace arguments, so Claude's variadic --add-dir cannot take them.
	// The system prompt snapshot mode an attached persona recorded goes right
	// after them, for the same reason.
	effort, effortInvalid, effortSkipped := claudeResumeEffort(mode, annotations)
	personaFile, personaUnavailable := c.resumePersonaSnapshot(mode, annotations)
	// A Claude Agent launched with its Project's label link rules gets the
	// snapshot its digest annotation names, alone or after the persona in one
	// composite file: Claude keeps only the last --append-system-prompt-file.
	systemPromptFile, projectLinksUnavailable := c.resumeSystemPromptFile(mode, annotations, personaFile)
	// The agent guidance its digest annotation names goes in front of that
	// file, in one composite, for the same reason.
	systemPromptFile, agentGuidanceUnavailable := c.resumeGuidanceSystemPromptFile(mode, annotations, systemPromptFile)
	profileName, profileDigest, settingsFile, codexPolicy, err := c.resumeProfileSettings(mode, annotations)
	if err != nil {
		return agentResumeLaunch{}, err
	}
	prefix := append(claudeLaunchOptionArgs(model, effort, systemPromptFile), claudeSettingsArgs(settingsFile)...)
	if mode == aiModeCodex {
		prefix = append(codexLaunchOptionArgs(model, effort), codexCLIProfileArgs(codexPolicy)...)
	}
	if prefix = append(prefix, claudeResumeSnapshotArgs(mode, annotations)...); len(prefix) > 0 {
		workspaceArgs = append(prefix, workspaceArgs...)
	}
	resumeArgv = append(resumeArgv[:1], append(workspaceArgs, resumeArgv[1:]...)...)
	plan, err := c.planAgentLaunch(mode, workspace.CWD, nil, resumeArgv, filepath.Dir(agentBin))
	if err != nil {
		return agentResumeLaunch{}, err
	}
	return agentResumeLaunch{
		title: plan.title, argv: plan.commandArgs, personaUnavailable: personaUnavailable,
		effortInvalid: effortInvalid, effortSkipped: effortSkipped,
		profileName: profileName, profileDigest: profileDigest,
		projectLinksUnavailable: projectLinksUnavailable, agentGuidanceUnavailable: agentGuidanceUnavailable,
	}, nil
}

// resumePersonaSnapshot returns the persona snapshot a resumed Agent started
// with, or why it cannot be re-passed. An Agent without a persona annotation
// costs nothing; one with it costs the annotation read and one stat.
func (c *aiCommand) resumePersonaSnapshot(mode string, annotations map[string]string) (string, *persona.Error) {
	name := annotations[coremetadata.AnnotationAgentPersona]
	if name == "" {
		return "", nil
	}
	if mode == aiModeCodex {
		// A Codex persona is the developer instructions its thread was started
		// with, and the thread replays them on every resume -- upstream neither
		// records nor applies a persona given to thread/resume. So there is
		// nothing to re-pass and nothing was lost: no argv, and no notice,
		// which would otherwise report a working persona as unavailable.
		return "", nil
	}
	if mode != aiModeClaude {
		return "", &persona.Error{Reason: persona.ReasonUnavailable, Name: name,
			Detail: "is not re-passed: instructions apply only to --provider " + aiModeClaude}
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", &persona.Error{Reason: persona.ReasonUnavailable, Name: name, Detail: "snapshot cannot be located: " + err.Error()}
	}
	path, err := persona.NewDefaultStore(paths).RecordedSnapshotPath(annotations[coremetadata.AnnotationAgentPersonaDigest])
	if err != nil {
		var unavailable *persona.Error
		if !errors.As(err, &unavailable) {
			unavailable = &persona.Error{Reason: persona.ReasonUnavailable, Detail: err.Error()}
		}
		unavailable.Name = name
		return "", unavailable
	}
	return path, nil
}

func (c *aiCommand) BindResumedAgentPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title, conversationID string,
) error {
	return c.BindResumedAgentPaneWithSourceOnRoute(ctx, runner, paneID, provider, contextDir, title, conversationID, "")
}

func (c *aiCommand) BindResumedAgentPaneWithSourceOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title, conversationID, source string,
) error {
	return c.BindAgentPaneOnRoute(ctx, runner, agentPaneBinding{
		PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
		ConversationID: conversationID, ResumeSource: source,
	})
}

// agentResumePlan is one preflighted rebind: everything `agent resume` fixed
// from the read-only registry before it opened the store.
type agentResumePlan struct {
	dialogueReplyOnly bool
	agentUID          string
	agentName         string
	// provider is the union discriminator of the stored ref, which is the
	// provider whose resume argv will be built.
	provider string
	// conversationID is the identifier the provider's resume argv addresses.
	conversationID string
	// ref is the stored pointer, kept whole so the transaction can prove the
	// conversation did not change between the preflight read and the lock.
	ref *coremetadata.AgentSessionRef
	// The owner chain the new managed Pane is materialized into.
	projectUID  string
	projectRoot string
	// project is the owner Project as the plan read it, whose name and labels
	// are the variables of its label link rules.
	project   coremetadata.Project
	workspace coremetadata.AgentWorkspace
	topic     string
	windowUID string
	anchorUID string
	// shared names the other Agents that record the same conversation, in uid
	// order. It is disclosed, never decisive: see planAgentResume.
	shared []string
	// annotations are the Agent's own, handed to the resume seam unread.
	annotations map[string]string
	// modelOverride and effortOverride are `agent resume --model/--effort`,
	// both empty on every other rebind. Both are recorded on the Agent by the
	// rebind transaction and launched with. Later plain resumes re-pass the
	// effort but not the model, which the conversation owns.
	modelOverride  string
	effortOverride string
	// overrideSource is where the overrides came from, recorded beside them:
	// coremetadata.SettingSourceRelaunch for `agent relaunch`, and
	// coremetadata.SettingSourceResume (the empty value) for `agent resume`.
	overrideSource string
	// layerChanges are the changes `agent relaunch` makes to the layers
	// themselves: --profile, --instructions and --reset. Empty on every other
	// rebind.
	layerChanges agentSettingsRequest
	// stoppedPane is the managed Pane a restart closed right before this
	// rebind. Empty on every other rebind.
	stoppedPane stoppedAgentPane
}

// settingsRequest is what this rebind asks of the Agent's layers.
func (p agentResumePlan) settingsRequest() agentSettingsRequest {
	request := p.layerChanges
	request.model, request.effort, request.source = p.modelOverride, p.effortOverride, p.settingSource()
	return request
}

// settingSource is the source the rebind transaction records beside the
// model and effort overrides.
func (p agentResumePlan) settingSource() string {
	if p.overrideSource == "" {
		return coremetadata.SettingSourceResume
	}
	return p.overrideSource
}

// launchAnnotations are the annotations the resume seam reads: the Agent's
// own, with the effort override in place of the recorded effort. That is the
// value the rebind transaction records, so the launch reads what the Agent
// will record. Without an override they are the Agent's own map itself.
func (p agentResumePlan) launchAnnotations() map[string]string {
	if p.effortOverride == "" {
		return p.annotations
	}
	out := maps.Clone(p.annotations)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentEffort] = p.effortOverride
	return out
}

// planAgentResume fixes one rebind from the read-only registry.
//
// Nothing here mutates, opens the store, or calls tmux, so every refusal it can
// produce leaves zero transactions, zero tmux objects, and zero bytes on stdout.
//
// # What resume does when several Agents point at one conversation
//
// Phase 0 deliberately did not enforce "one conversation <-> at most one live
// Agent": the ref is a best-effort observation rather than a declaration, and
// enforcing uniqueness at write time deadlocks, because an Offline Agent holds
// its conversation forever and a later Agent observing the same conversation
// could then never record it. The consequence is that several Agents may point
// at one conversation, and this Phase owes that state a rule.
//
// The rule is: **the conversation is never a selector.** `agent resume <ref>`
// rebinds exactly the Agent the reference resolves to, and nothing in this route
// ever searches the registry by conversation id to decide *which* Agent to
// rebind. Duplicates therefore neither redirect the rebind nor block it. That is
// total (defined for every duplicate count), deterministic (it depends on the
// operator's reference and the selector's exact-one cardinality, not on registry
// order, map iteration, or observedAt), and it is the only rule that does not
// re-create Phase 0's deadlock somewhere else: refusing a duplicate at resume
// time would make a state Phase 0 declared legal permanently unusable.
//
// Duplicates are not silent, though. They are disclosed on stderr in uid order,
// so an operator who is about to run two live panes on one provider conversation
// finds that out from projmux rather than from the provider.
//
// The ambiguity this resolves is "which conversation-sharing Agent gets the new
// Pane". The ambiguity it does *not* resolve is "which Agent does a bare
// reference mean" -- that one is already answered upstream by the selector
// engine's <resume, Agent> exact-one cell, which refuses rather than guesses.
//
// # What resume does with observedAt
//
// Nothing. It is deliberately not consulted by any gate on this path.
// `observedAt` records when projmux last *saw* the conversation; it is not a
// timestamp the provider supplied and it says nothing about whether the provider
// still holds the conversation. A wall-clock staleness heuristic built on it
// would be wrong in both directions: a conversation untouched for a month is
// perfectly resumable, and a conversation observed a minute ago may already have
// been deleted. The only authority on whether a conversation can be revived is
// the provider, and reading the provider's own store to ask is permanently out
// of scope. So projmux checks everything it can see -- a ref exists, it names a
// known provider, that provider is enabled, its conversation id is well formed,
// its binary is installed -- and hands the rest to the provider's resume argv.
func planAgentResume(spelling string, registry coremetadata.Registry, agent *coremetadata.Agent) (agentResumePlan, error) {
	name := agent.Metadata.Name

	// (d) An Agent whose provider hook never ran has nothing to revive. This is
	// the most important refusal in the route: the tempting behavior is to start
	// a fresh conversation "since there is nothing to resume", and that is
	// exactly the silent context loss `create` and `resume` are separate verbs
	// to prevent. The message names the other verb rather than performing it.
	ref := agent.Status.SessionRef
	if ref.Empty() {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has no provider session ref, so there is no conversation to resume; "+
				"projmux records one the first time that Agent's provider hook fires. "+
				"To start a new conversation instead, run `projmux create agent --provider <provider>`, which mints a new Agent",
			spelling, name)
	}
	conversationID := strings.TrimSpace(ref.ConversationID())
	if conversationID == "" {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a %s session ref that carries no conversation id; it cannot be resumed",
			spelling, name, ref.Provider)
	}
	provider := strings.TrimSpace(ref.Provider)
	if provider == "" {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a session ref with no provider discriminator; it cannot be resumed", spelling, name)
	}
	// spec.provider is cross-checked only when the Agent declares one. Phase 0
	// deliberately kept an Agent whose provider never normalized recordable, and
	// punishing it here would undo that leniency.
	if declared := strings.TrimSpace(agent.Spec.Provider); declared != "" && declared != provider {
		return agentResumePlan{}, fmt.Errorf(
			"%s: agent/%s is a %s Agent but its session ref is a %s conversation; refusing to resume a mismatched conversation",
			spelling, name, declared, provider)
	}

	// A resumable Agent has already given its Pane up. A surviving paneRef means
	// the registry disagrees with itself, and binding a second Pane would orphan
	// the first, so this refuses rather than guessing which one is real.
	if paneUID := strings.TrimSpace(agent.Status.PaneRef); paneUID != "" {
		if pane, ok := registry.Pane(paneUID); ok {
			return agentResumePlan{}, fmt.Errorf(
				"%s: agent/%s is %s but still owns managed pane/%s; refusing to bind a second managed Pane",
				spelling, name, agent.Status.Phase, pane.Metadata.Name)
		}
	}

	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return agentResumePlan{}, fmt.Errorf("%s: agent/%s has no owning Window in the registry", spelling, name)
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return agentResumePlan{}, fmt.Errorf("%s: window/%s has no owning Project in the registry", spelling, window.Metadata.Name)
	}
	// The same rule `create` applies: a Project whose root has disappeared is
	// preserved and still resolves, but nothing may be materialized under a
	// directory tmux cannot enter. The check is restated with this route's own
	// spelling rather than borrowed, so the message names the verb the operator
	// actually ran.
	if condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot); ok && condition.Status == coremetadata.ConditionTrue {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: project/%s carries a MissingRoot condition for %q; rebind it before resuming an Agent under it",
			spelling, project.Metadata.Name, project.Spec.Root))
	}
	// Resume uses the stable role-agnostic Window anchor. There is no fallback to
	// the active, last-used, or an alternate live Pane.
	anchorUID := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if anchorUID == "" {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef, so there is no anchor to split",
			spelling, window.Metadata.Name, project.Metadata.Name))
	}
	if anchor, ok := registry.WindowAnchor(window.Metadata.UID); !ok || anchor.Metadata.UID != anchorUID {
		return agentResumePlan{}, usageError(fmt.Sprintf(
			"%s: window/%s (project/%s) anchorPaneRef %q is dangling or cross-Window",
			spelling, window.Metadata.Name, project.Metadata.Name, anchorUID))
	}

	return agentResumePlan{
		agentUID:       agent.Metadata.UID,
		agentName:      name,
		provider:       provider,
		conversationID: conversationID,
		ref:            ref.Clone(),
		projectUID:     project.Metadata.UID,
		projectRoot:    project.Spec.Root,
		project:        project.Clone(),
		workspace:      agent.Spec.Workspace,
		topic:          agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic],
		windowUID:      window.Metadata.UID,
		anchorUID:      anchorUID,
		shared:         sharedConversationAgents(registry, agent.Metadata.UID, ref),
		annotations:    agent.Metadata.Annotations,
	}, nil
}

// sharedConversationAgents lists the other Agents recording the same
// conversation, in uid order.
//
// The order is the point: it makes the disclosure byte-identical regardless of
// the order the Agents happen to sit in the registry file, which is what a
// determinism assertion can pin.
func sharedConversationAgents(registry coremetadata.Registry, selfUID string, ref *coremetadata.AgentSessionRef) []string {
	type row struct{ uid, name string }
	var rows []row
	for i := range registry.Agents {
		other := registry.Agents[i]
		if other.Metadata.UID == selfUID || !other.Status.SessionRef.SameConversation(ref) {
			continue
		}
		rows = append(rows, row{uid: other.Metadata.UID, name: other.Metadata.Name})
	}
	slices.SortStableFunc(rows, func(a, b row) int { return cmp.Compare(a.uid, b.uid) })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("agent/%s (uid:%s)", r.name, r.uid))
	}
	return out
}

// agentRebinder materializes the new managed Pane of a resumed Agent.
//
// It holds the create command rather than re-deriving its plumbing: the
// transaction order, the runtime ledger, the rollback, the Project runtime
// ensure and the anchor resolution are the ones `create` ships, and a second
// implementation of any of them would be a second set of bugs. What it does not
// share is the metadata half. `create agent` mints an Agent; this route never
// calls CreateAgent at all, so the uid and metadata.name of the resumed Agent
// cannot change no matter what happens on the runtime side.
type agentRebinder struct {
	create           *createCommand
	launcher         agentResumeLauncher
	resolveWorkspace func(string, coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
}

func newAgentRebinder(create *createCommand, launcher agentResumeLauncher) *agentRebinder {
	return &agentRebinder{create: create, launcher: launcher, resolveWorkspace: resolveAgentWorkspaceFor}
}

// rebind attaches a new managed Pane, launched with the provider's resume argv,
// to the Agent the plan named.
//
// The launch is constructed before the store is opened, which is what makes the
// route's central guarantee measurable: on every failure path the number of
// split-window calls issued is zero, so no conversation -- neither the stored one
// nor a fresh one -- is ever started by a failed resume.
func (r *agentRebinder) rebind(spelling string, plan agentResumePlan, stdout, stderr io.Writer) error {
	if r == nil || r.create == nil || r.launcher == nil {
		return errors.New(spelling + ": the resume materialization seam is not configured")
	}

	// The Settings gate runs before anything else the store would see, exactly
	// as on `create agent`.
	if err := r.launcher.RequireAgentEnabled(plan.provider); err != nil {
		return err
	}
	// Resume is selector-authoritative, so it binds the app's logical route
	// without an anchor Pane. `agent resume <ref>` has already resolved one
	// exact Agent through the selector engine's exact-one cell, and planAgentResume
	// has proven that Agent's owning Window and that Window's stored
	// anchorPaneRef; the split target is that stored anchor, not whatever Pane
	// the operator happens to be standing in. Requiring an inherited or typed
	// `%N` on top of it blocked the one invocation resume exists for -- reviving
	// an Agent whose own Pane is, by definition, already gone -- and it blocked
	// it with a containment refusal about a socket and a server that were both
	// correct. Physical socket, ownership marker, server generation and the
	// typed object guards below are all unchanged: what is dropped is only the
	// requirement that the caller supply an anchor Pane by typing or inheriting
	// one. A standalone server still gets none of this: it keeps requiring the
	// inherited $/@/% receipt.
	r.create.selectRuntimeAuthority(true)
	// The provider resume argv is the only argv this route can produce. If the
	// stored conversation id cannot be rendered into one -- malformed, wrong
	// shape for the provider, provider binary absent -- the route stops here,
	// with the store still unopened.
	contextDir := plan.workspace.CWD
	if contextDir == "" {
		contextDir = plan.projectRoot
	}
	workspace := plan.workspace
	workspace.CWD = contextDir
	nativeLauncher, nativeLaunchCapable := r.launcher.(codexNativeAgentLauncher)
	nativeLifecycle, nativeLifecycleCapable := r.launcher.(codexNativeLifecycleStarter)
	var nativeRoute codexNativeEndpointRoute
	var title string
	var launchArgv []string
	var personaNotice, effortNotice, linksNotice, guidanceNotice, settingsNotice string
	var resumed agentResumeLaunch
	// settings are the layered settings this launch runs with: the
	// overrides, then the profile as it is now (agentsettings.Resolve). The
	// rebind transaction records them.
	var settings agentSettingsLaunch
	var links projectLinksLaunch
	var guidance agentGuidanceLaunch
	var nativePolicy codexappserver.ThreadPolicy
	var err error
	if plan.provider == aiModeCodex {
		// The profile the Agent records is re-read by name before anything
		// else, so a profile that is gone or invalid refuses with zero writes
		// and zero provider calls. Its current sandbox and approval ride the
		// thread/resume below, and resumed carries the digest the transaction
		// records -- the same record a Claude resume makes.
		settings, err = r.resolveSettings(plan.provider, plan.annotations, plan.settingsRequest())
		if err == nil {
			// A relaunch that switches the profile resumes with the new one.
			resumed, nativePolicy, err = r.create.codexResumeProfile(settings.launchAnnotations(plan.annotations))
		}
		if err == nil {
			nativeCtx, cancel := prepareNativeContext(context.Background())
			nativeRoute, err = resolveCodexNativeResumeRoute(nativeCtx, r.create.codexNative, plan.ref, "uid:"+plan.agentUID)
			cancel()
			if err != nil {
				return nativeResumePreparationRefusal(spelling, err)
			}
			if !nativeLaunchCapable {
				return nativeResumePreparationRefusal(spelling, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable})
			}
			effort, invalid, skipped := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
			resumed.effortInvalid, resumed.effortSkipped = invalid, skipped
			title, launchArgv, err = planNativeCodexResumeOptions(nativeLauncher, nativeRoute, workspace, plan.conversationID, settings.model(plan.modelOverride), effort)
			effortNotice = resumed.effortNotice(plan.agentName)
			settingsNotice = settings.notice(plan.agentName)
		}
	} else {
		if plan.dialogueReplyOnly && plan.annotations[coremetadata.AnnotationAgentProfile] != "" {
			// The reply-only launch is fixed and cannot carry a profile's
			// permissions, so it is refused rather than resumed without them.
			err = &profileResumeError{name: plan.annotations[coremetadata.AnnotationAgentProfile], reason: profileReasonLaneUnsupported,
				detail: "the reply-only activation cannot carry a profile"}
		} else if plan.dialogueReplyOnly {
			launcher, ok := r.launcher.(claudeDialogueLauncher)
			if !ok {
				return errors.New("claude reply-only resume launcher is unavailable")
			}
			title, launchArgv, err = launcher.PlanClaudeDialogueLaunch(workspace, plan.conversationID)
		} else {
			// The Project's current label link rules are read from the Project
			// that owns the Agent's Window. Rules that differ from the recorded
			// digest launch with the snapshot off, and the transaction below
			// records exactly the digest and mode this launch reads.
			links = planProjectLinksWith(r.launcher, plan.provider, plan.project, plan.annotations)
			// The agent guidance is compared with its recorded digest the same
			// way, and launched and recorded the same way.
			guidance = planAgentGuidanceWith(r.launcher, plan.provider, plan.annotations)
			// The settings are resolved from the layers. A profile that is
			// gone or invalid leaves them unlayered, so the seam below
			// refuses the resume exactly as it did before layers existed.
			if resolved, resolveErr := r.resolveSettings(plan.provider, plan.annotations, plan.settingsRequest().withPromptParts(guidance, links)); resolveErr == nil {
				settings = resolved.writeSnapshot()
			} else if plan.layerChanges.changesLayers() {
				// A change to the layers is never launched without them.
				err = resolveErr
			}
			launchAnnotations := guidance.resumeLaunchAnnotations(links.resumeLaunchAnnotations(settings.launchAnnotations(plan.launchAnnotations())))
			switch model := settings.model(plan.modelOverride); {
			case err != nil:
			case settings.snapshotErr != nil:
				err = settings.snapshotErr
			case model != "":
				launcher, ok := r.launcher.(agentResumeModelLauncher)
				if !ok {
					return errors.New(spelling + ": the resume launcher cannot pass --model")
				}
				resumed, err = launcher.PlanAgentResumeWithModel(plan.provider, workspace, plan.conversationID, launchAnnotations, model)
			default:
				resumed, err = r.launcher.PlanAgentResume(plan.provider, workspace, plan.conversationID, launchAnnotations)
			}
			title, launchArgv = resumed.title, resumed.argv
			personaNotice, effortNotice = resumed.personaNotice(plan.agentName), resumed.effortNotice(plan.agentName)
			linksNotice = cmp.Or(links.notice(plan.agentName), resumed.projectLinksNotice(plan.agentName))
			guidanceNotice = cmp.Or(guidance.notice(plan.agentName), resumed.agentGuidanceNotice(plan.agentName))
			settingsNotice = settings.notice(plan.agentName)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w",
			spelling, plan.agentName, plan.provider, plan.conversationID, err)
	}
	var nativeLifecycleTargetAfterCommit codexLifecycleObserverTarget

	for _, other := range plan.shared {
		if _, err := fmt.Fprintf(stderr,
			"projmux: conversation %s:%s is also recorded on %s; %s rebinds only agent/%s\n",
			plan.provider, plan.conversationID, other, spelling, plan.agentName); err != nil {
			return err
		}
	}

	nameReason := ""
	if err := r.create.transact(diagnostics.CreateKindResume, func(
		ctx context.Context,
		working *coremetadata.Registry,
		mutator coremetadata.Mutator,
		operationID string,
		ledger *runtimeLedger,
	) error {
		nameReason = ""
		agent, ok := working.Agent(plan.agentUID)
		if !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.agentUID)
		}
		// The preflight ran against a read-only snapshot and the reconciler has
		// since run inside this transaction. Re-checking the two facts the plan
		// rests on -- the Agent is still resumable, and it is still the same
		// conversation -- is what stops a concurrent hook or transition from
		// turning this into a rebind of something else.
		if err := requireResumablePhase(spelling, agent); err != nil {
			return err
		}
		if !agent.Status.SessionRef.SameConversation(plan.ref) {
			return fmt.Errorf(
				"%s: agent/%s now points at a different conversation than the one this resume planned; re-run it",
				spelling, plan.agentName)
		}
		project, ok := working.Project(plan.projectUID)
		if !ok {
			return fmt.Errorf("%s: project %q disappeared before the rebind ran", spelling, plan.projectUID)
		}
		resolver := r.resolveWorkspace
		if resolver == nil {
			resolver = resolveAgentWorkspaceFor
		}
		workspace, err := resolver(spelling, *working, *project, plan.provider, agent.Spec.Workspace.CWD, agent.Spec.Workspace.AdditionalWritableRoots)
		if err != nil {
			return err
		}
		if workspace.CWD != plan.workspace.CWD || !slices.Equal(workspace.AdditionalWritableRoots, plan.workspace.AdditionalWritableRoots) {
			return fmt.Errorf("%s: agent/%s workspace changed after preflight; re-run it", spelling, plan.agentName)
		}
		// Persist the normalized effective workspace for legacy Agents whose
		// pre-Phase6 spec was empty, before any runtime object is created.
		agent.Spec.Workspace = workspace
		// The layered settings are recorded with their sources in the same
		// transaction, before any runtime object exists: the profile a
		// relaunch switched to, the effort, the model when it was passed, and
		// the instructions with the snapshot mode off when their content
		// changed. A failure anywhere later rolls the whole transaction back,
		// so a resume that does not launch records neither value nor source.
		if err := settings.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// The profile this launch re-applied is recorded with the digest of
		// the content it applied, in the same transaction.
		if err := recordResumedProfileDigest(working, mutator, plan.agentUID, resumed); err != nil {
			return err
		}
		// So are the Project label link rules this launch reads, when they
		// changed, with the sticky snapshot mode off.
		if err := links.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// And the agent guidance, the same way.
		if err := guidance.record(working, mutator, plan.agentUID); err != nil {
			return err
		}
		// The reply-only activation is recorded, so every later resume of
		// the Agent launches it again. An Agent that records it already is
		// left as it was.
		if plan.dialogueReplyOnly {
			if _, err := mutator.SetAgentDialogueReplyOnly(working, plan.agentUID); err != nil {
				return MapMetadataError(err)
			}
		}
		// An unlayered launch records only its overrides, as before.
		if plan.effortOverride != "" && !settings.layered {
			if _, err := mutator.SetAgentEffort(working, plan.agentUID, plan.effortOverride, plan.settingSource()); err != nil {
				return MapMetadataError(err)
			}
		}
		if plan.modelOverride != "" && !settings.layered {
			if _, err := mutator.SetAgentModel(working, plan.agentUID, plan.modelOverride, plan.settingSource()); err != nil {
				return MapMetadataError(err)
			}
		}

		// Name handoff. The new managed Pane carries the non-automatic name of
		// the Agent's old Pane row that selectAgentPaneNameHandoff picks, and
		// that row is released first so the name has exactly one holder; with no
		// such row and no restart, it gets the `<agent>-pane` name `create agent`
		// gives. A name that cannot be carried is disclosed and never refuses
		// the resume.
		paneName, reason := r.handOffResumedPaneName(ctx, working, mutator, *agent, plan)
		nameReason = reason
		if agent, ok = working.Agent(plan.agentUID); !ok {
			return fmt.Errorf("%s: agent %q disappeared before the rebind ran", spelling, plan.agentUID)
		}

		// Metadata phase. AttachAgentPane creates the managed Pane owned by this
		// existing Agent and moves it Offline/Failed -> Running through the
		// closed transition table. No Agent is created and no name is allocated
		// for one, so the uid and metadata.name are structurally untouchable
		// here.
		pane, attachReason, err := attachAgentPaneWithName(working, mutator, plan.agentUID, contextDir, paneName, operationID)
		if err != nil {
			return MapMetadataError(err)
		}
		if attachReason != "" {
			nameReason = attachReason
		}

		// Runtime phase, on the create routes' own materializer.
		sessionName, err := r.create.ensureProjectRuntime(ctx, working, mutator, *project, operationID, ledger)
		if err != nil {
			return err
		}
		anchorPaneID, err := r.create.ensureAnchorPane(ctx, working, mutator, ledger, *project, sessionName, operationID, paneTarget{
			windowUID:    plan.windowUID,
			anchorUID:    plan.anchorUID,
			storedAnchor: true,
		})
		if err != nil {
			return err
		}
		// A resume is a new materialization of the same Agent, so it issues a
		// fresh generation. That is what makes a late receipt from the process
		// this resume replaced recognizable as stale instead of being applied
		// to the Pane the operator is now looking at.
		activation, err := r.create.issuePaneActivation(working, mutator, pane.Metadata.UID, plan.agentUID, operationID)
		if err != nil {
			return err
		}
		activation.DialogueReplyOnly = plan.dialogueReplyOnly
		workTitle := title
		workLaunchArgv := launchArgv
		usedNative := false
		nativeThreadID := ""
		if plan.provider == aiModeCodex {
			if routeErr := validateCodexNativeResumeRoute(agent.Status.SessionRef, nativeRoute); routeErr != nil {
				return nativeResumePreparationRefusal(spelling, bindCodexResumeAgentRef(routeErr, "uid:"+plan.agentUID))
			}
			nativeCtx, cancel := prepareNativeContext(ctx)
			// A thread that answers with another policy than the profile's is
			// a *PolicyMismatchError, never a safe fallback: it lands in the
			// default arm and the whole transaction rolls back.
			prepared, nativeErr := r.create.codexNative.Resume(nativeCtx, nativeRoute, workspace, plan.conversationID, nativePolicy)
			cancel()
			switch {
			case nativeErr == nil && strings.TrimSpace(prepared.ThreadID) == strings.TrimSpace(plan.conversationID):
				effort, _, _ := claudeResumeEffort(aiModeCodex, settings.launchAnnotations(plan.launchAnnotations()))
				workTitle, workLaunchArgv, err = planNativeCodexResumeOptions(nativeLauncher, nativeRoute, workspace, prepared.ThreadID, settings.model(plan.modelOverride), effort)
				if err != nil {
					return nativeLaunchError(spelling, err)
				}
				// A stored ref from a retired generation (or a draining /
				// handover-pending marker) is moved onto the endpoint that just
				// resumed the thread, inside this transaction, so the committed
				// Agent names the default endpoint with a current lifecycle.
				if _, err := mutator.AdoptCodexResumeEndpoint(working, plan.agentUID, nativeRoute.Endpoint); err != nil {
					return MapMetadataError(err)
				}
				if _, err := mutator.BindCodexActivation(working, coremetadata.CodexActivationObservation{
					AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, Generation: activation.Generation,
					ThreadID: prepared.ThreadID, TurnID: prepared.TurnID, Endpoint: nativeRoute.Endpoint,
				}); err != nil {
					return MapMetadataError(err)
				}
				nativeThreadID = prepared.ThreadID
				usedNative = true
			case nativeErr == nil:
				return nativeLaunchError(spelling, fmt.Errorf("%w: native resume returned a different thread", codexappserver.ErrProtocol))
			case nativeFallbackAllowed(r.create.codexNative, nativeErr):
				return nativeResumePreparationRefusal(spelling, nativeErr)
			default:
				return nativeLaunchError(spelling, nativeErr)
			}
		}
		paneID, err := r.create.runtime.splitPane(ctx, anchorPaneID, defaultPlacement, contextDir,
			r.create.runtime.supervisedLaunch(ctx, activation, workLaunchArgv))
		if paneID != "" {
			// The supervised child now runs and will want the Registry lock
			// this transaction holds; create.outcome measures the rest of the hold.
			markSupervisedSpawn(ctx)
			if claimErr := r.create.runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); claimErr != nil {
				return errors.Join(err, claimErr)
			}
			if mirrorErr := r.create.runtime.mirror.MirrorPane(ctx, paneID, pane); mirrorErr != nil {
				return errors.Join(err, mirrorErr)
			}
			observeActivationRuntime(working, mutator, activation, paneID, r.create.runtime.warn)
		}
		if err != nil {
			return err
		}
		if usedNative {
			if err := bindNativeCodexPaneOnRoute(ctx, nativeLauncher, r.create.runtime.runner, paneID, contextDir, workTitle, plan.topic, nativeThreadID); err != nil {
				return fmt.Errorf("%s: bind native Codex Pane %s presentation metadata: %w", spelling, paneID, err)
			}
			if nativeLifecycleCapable {
				nativeLifecycleTargetAfterCommit = codexLifecycleObserverTarget{
					Identity: codexLifecycleIdentity{
						AgentUID: plan.agentUID, PaneUID: pane.Metadata.UID, RuntimeID: paneID,
						Generation: activation.Generation, ThreadID: nativeThreadID,
					},
					Route: r.create.runtime.target, NativeRoute: nativeRoute,
				}
			}
		} else if err := r.launcher.BindAgentPaneOnRoute(ctx, r.create.runtime.runner, agentPaneBinding{
			PaneID: paneID, Provider: plan.provider, ContextDir: contextDir, Title: workTitle,
			Topic: plan.topic, TopicManual: strings.TrimSpace(plan.topic) != "", ConversationID: plan.conversationID,
		}); err != nil {
			return fmt.Errorf("%s: bind resumed Agent Pane %s presentation metadata: %w", spelling, paneID, err)
		}
		return nil
	}, r.create.exactProjectOwnershipGuard(plan.projectUID)); err != nil {
		return err
	}
	if nativeLifecycleTargetAfterCommit.valid() {
		nativeLifecycle.startNativeCodexLifecycleObserver(nativeLifecycleTargetAfterCommit)
	}
	if nameReason != "" {
		// A lost disclosure must not turn a committed resume into a failure.
		fmt.Fprintln(stderr, agentPaneNameNotice(plan.agentName, nameReason))
	}
	if personaNotice != "" {
		// The Agent is back without its persona; like the name notice, a lost
		// disclosure must not turn a committed resume into a failure.
		fmt.Fprintln(stderr, personaNotice)
	}
	if effortNotice != "" {
		// The Agent is back without the effort it recorded, for the same reason
		// and with the same best-effort disclosure.
		fmt.Fprintln(stderr, effortNotice)
	}
	if linksNotice != "" {
		// The Agent is back without its Project's label link rules, disclosed
		// the same way.
		fmt.Fprintln(stderr, linksNotice)
	}
	if guidanceNotice != "" {
		// The Agent is back without the agent guidance, disclosed the same
		// way.
		fmt.Fprintln(stderr, guidanceNotice)
	}
	if settingsNotice != "" {
		// The Agent is back with its recorded instructions instead of the
		// ones its layers ask for, disclosed the same way.
		fmt.Fprintln(stderr, settingsNotice)
	}

	_, err = fmt.Fprintf(stdout, "agent/%s resumed\n", plan.agentName)
	return err
}

// stoppedAgentPane is the managed Pane a restart closed: its uid and its
// non-automatic name, "" when it was named by its own UID.
type stoppedAgentPane struct {
	uid  string
	name string
}

// handOffResumedPaneName chooses the name the resumed Agent's new Pane carries
// and releases the old Pane row that held it.
//
// A restart's stopped Pane comes first: its name is carried once the stop has
// dropped its row.
//
// Resume keeps an Offline Agent's old Pane rows as evidence, and each row still
// holds its name reservation. The candidates are the rows proven live nowhere
// by the same server-wide owner inventory Continue replay uses; only the
// selected source row is released, and only when releasing it leaves the
// Window's stored anchor where it is. Every outcome that
// carries no non-automatic name comes back as a reason instead of an error.
//
// When there is neither a stopped Pane nor any candidate row -- a resume after
// `delete pane` dropped the old row -- the name is derivedAgentPaneName's
// `<agent>-pane`, the one `create agent` gives. A candidate row that cannot be
// carried keeps its reason and the automatic name.
func (r *agentRebinder) handOffResumedPaneName(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	agent coremetadata.Agent,
	plan agentResumePlan,
) (string, string) {
	if stopped := plan.stoppedPane; stopped.name != "" {
		if _, ok := working.Pane(stopped.uid); !ok {
			// The restart's stop dropped the row and released its name, so
			// no old row holds it. A holder that took it since is disclosed
			// by attachAgentPaneWithName.
			return stopped.name, ""
		}
	}
	var owned []coremetadata.Pane
	named := false
	for _, pane := range working.PanesOf(agent.Metadata.UID) {
		if pane.Metadata.UID == agent.Status.PaneRef {
			continue
		}
		owned = append(owned, pane)
		named = named || agentPaneNameCandidate(pane, agent.Metadata.UID)
	}
	if !named {
		// Nothing could be carried, so no runtime inventory is taken. With no
		// restart behind it either, the new Pane gets the name `create agent`
		// gives an Agent's Pane; attachAgentPaneWithName discloses a holder
		// or an invalid name and falls back to the automatic one.
		if plan.stoppedPane.uid == "" {
			return derivedAgentPaneName(agent.Metadata.Name), ""
		}
		return "", ""
	}
	guard, err := newTopologyOwnerGuard(ctx, r.create.runtime)
	if err != nil {
		return "", fmt.Sprintf("old Pane rows could not be proven live nowhere on this socket: %v", err)
	}
	var nonLive []string
	var liveCandidate error
	for _, pane := range owned {
		if claimErr := guard.requireSolePaneUID(pane.Metadata.UID, "", plan.agentName); claimErr != nil {
			if liveCandidate == nil && agentPaneNameCandidate(pane, agent.Metadata.UID) {
				liveCandidate = claimErr
			}
			continue
		}
		nonLive = append(nonLive, pane.Metadata.UID)
	}
	handoff := selectAgentPaneNameHandoff(*working, agent, nonLive)
	if handoff.name == "" {
		if handoff.reason == "" && liveCandidate != nil {
			return "", liveCandidate.Error()
		}
		return "", handoff.reason
	}
	window, ok := working.Window(plan.windowUID)
	if !ok {
		return "", fmt.Sprintf("window %q is not in the Registry", plan.windowUID)
	}
	anchor := window.Spec.AnchorPaneRef
	before := working.Clone()
	if err := mutator.DeletePane(working, handoff.sourceUID); err != nil {
		*working = before
		return "", fmt.Sprintf("old pane/%s (uid:%s) could not be released: %v", handoff.name, handoff.sourceUID, err)
	}
	// The rebind splits the Window's stored anchor, so a release that re-anchored
	// the Window would turn a name into a refused resume. Keep the row instead.
	if window, ok := working.Window(plan.windowUID); !ok || window.Spec.AnchorPaneRef != anchor {
		*working = before
		return "", fmt.Sprintf("releasing old pane/%s (uid:%s) would move window %s's anchor", handoff.name, handoff.sourceUID, plan.windowUID)
	}
	return handoff.name, ""
}
