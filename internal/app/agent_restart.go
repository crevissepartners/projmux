package app

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// relaunchReasonCodexPermissionsKept refuses a Codex profile switch whose new
// profile requests no sandbox or no approval where the Agent's thread may
// have one: thread/resume sends only the keys a profile requests, so it can
// set a sandbox or an approval but never remove one, and the thread would go
// on running with the old profile's.
const relaunchReasonCodexPermissionsKept = "relaunch-codex-permissions-kept"

// agentRestart is one restart of an existing Claude or Codex Agent on its
// provider conversation: the one implementation `agent relaunch` and `agent
// instructions|persona attach|detach` share.
//
// A restart is planned while nothing has changed: the target is checked
// (checkTarget), the resume it ends with is predicted against the registry
// the stop will leave, and the settings it launches with are resolved from
// the Agent's layers and what the command asks of them (plan). Every refusal
// happens there and leaves no trace. It then runs (run): a Running Agent's
// managed Pane is closed through `delete pane`, which leaves the Agent
// Offline, and the Agent is brought back through the `agent resume` rebinder,
// which keeps its uid and its conversation and records the settings in its
// launch transaction. An Offline or Failed Agent is only resumed.
//
// The commands differ only in what they print and when they record: a
// relaunch writes nothing before the stop, while an attach records the new
// instructions before it (agentRestartSteps.beforeStop), so a resume that
// fails leaves them for a plain `agent resume` to finish.
type agentRestart struct {
	spelling string
	registry coremetadata.Registry
	target   coremetadata.Agent
	provider string
	running  bool
	paneUID  string
	// paneName is the stopped managed Pane's non-automatic name, "" when it
	// is named by its own UID. The stop drops that Pane's row and with it the
	// name, so the resume could not find it among the Agent's old rows.
	paneName    string
	interaction coremetadata.AgentInteractionKind
	// settings are the layered settings the restart launches with.
	settings agentSettingsLaunch
	tokens   agentRestartTokens
	// refuse is the command's own refusal: every refusal of the restart,
	// whichever step makes it, reads as that command's.
	refuse func(reason, detail string) error
	// comparesPromptParts makes the plan compare the agent guidance and the
	// Project's label link rules the resume would pass with the recorded
	// ones too, so their change is a reason to restart (`agent relaunch`).
	comparesPromptParts bool
}

// agentRestartTokens are the refusal reason tokens of one restarting command,
// and how it says which phases it restarts.
type agentRestartTokens struct {
	busy           string
	selfTarget     string
	noConversation string
	phase          func(coremetadata.AgentPhase) string
}

// newAgentRestart starts the plan of one restart of target. refuse is the
// command's refusal, which says nothing was changed.
func (c *agentCommand) newAgentRestart(spelling string, registry coremetadata.Registry, target coremetadata.Agent, provider string, tokens agentRestartTokens, refuse func(reason, detail string) error) *agentRestart {
	return &agentRestart{
		spelling: spelling, registry: registry, target: target, provider: provider, tokens: tokens, refuse: refuse,
		interaction: target.EffectiveInteraction(c.clock()).Kind,
	}
}

// confirmationRequired reports a Running Agent whose interaction does not say
// it is between turns: restarting it may cut a turn.
func (r *agentRestart) confirmationRequired() bool {
	return r.running && r.interaction != coremetadata.InteractionIdle && r.interaction != coremetadata.InteractionResponseComplete
}

// checkTarget refuses an Agent with no provider conversation, in a phase no
// restart applies to, or Running without its managed Pane.
func (r *agentRestart) checkTarget() error {
	target := r.target
	if target.Status.SessionRef.Empty() || strings.TrimSpace(target.Status.SessionRef.ConversationID()) == "" {
		return r.refuse(r.tokens.noConversation, "has no provider conversation to resume; projmux records one the first time its provider hook fires")
	}
	r.running = target.Status.Phase == coremetadata.PhaseRunning
	if !r.running && !slices.Contains(resumableAgentPhases, target.Status.Phase) {
		return r.refuse(r.tokens.noConversation, r.tokens.phase(target.Status.Phase))
	}
	if r.running {
		r.paneUID = strings.TrimSpace(target.Status.PaneRef)
		pane, ok := r.registry.Pane(r.paneUID)
		if !ok {
			return r.refuse(r.tokens.noConversation, fmt.Sprintf("is %s but its managed pane %q is not in the registry", target.Status.Phase, r.paneUID))
		}
		if agentPaneNameCandidate(*pane, target.Metadata.UID) {
			r.paneName = pane.Metadata.Name
		}
	}
	return nil
}

// plan predicts the resume the restart ends with, against the registry the
// stop will leave behind, and resolves the settings it launches with for
// request, so a resume `agent resume` would refuse, and a change to the
// layers that cannot be made, are refused here while nothing has changed.
func (c *agentCommand) plan(r *agentRestart, request agentSettingsRequest) error {
	resumePlan, err := c.predictStoppedAgentResume(r.registry, r.target, r.paneUID)
	if err != nil {
		return r.refuse(r.tokens.noConversation, "cannot be resumed: "+err.Error())
	}
	if c.rebind != nil && c.rebind.create != nil {
		if r.comparesPromptParts && !resumePlan.dialogueReplyOnly {
			// The same reads the resume makes, from the same Project.
			request = request.withPromptParts(planAgentGuidanceWith(c.rebind.launcher, r.provider, r.target.Metadata.Annotations),
				planProjectLinksWith(c.rebind.launcher, r.provider, resumePlan.project, r.target.Metadata.Annotations))
		}
		if r.settings, err = c.rebind.resolveSettings(r.provider, r.target.Metadata.Annotations, request); err != nil {
			var switchErr *relaunchProfileError
			if errors.As(err, &switchErr) {
				return r.refuse(switchErr.reason, fmt.Sprintf("cannot take profile %q: %s", switchErr.name, switchErr.detail))
			}
			return r.refuse(r.tokens.noConversation, "cannot be resumed: "+err.Error())
		}
		if err := r.refuseLayerChange(request); err != nil {
			return err
		}
	}
	if r.provider == aiModeCodex {
		if err := c.preflightCodexRelaunch(resumePlan, r.settings.launchAnnotations(resumePlan.annotations)); err != nil {
			return r.refuse(r.tokens.noConversation, "cannot be resumed: "+err.Error())
		}
		if r.settings.resolution.ProfileSwitched {
			if err := c.refuseCodexPermissionsKept(r); err != nil {
				return err
			}
		}
	}
	if r.running && c.invokedFromAgentPane(r.registry, r.target.Metadata.UID) {
		return r.refuse(r.tokens.selfTarget, "owns the Pane this command runs in; closing that Pane would end the command before the resume. Run it from another Pane")
	}
	return nil
}

// asksInstructions reports a request that decides the Agent's instructions:
// --instructions, a --reset of them, or a profile switch.
func (r *agentRestart) asksInstructions(request agentSettingsRequest) bool {
	return request.instructions != nil || slices.Contains(request.reset, agentsettings.ItemInstructions) || r.settings.resolution.ProfileSwitched
}

// refuseLayerChange refuses a change to the instructions that the launch could
// not honor: instructions that cannot be read, and any change to a Codex
// thread's, which keeps the developer message it started with. A plain resume
// launches with the recorded instructions instead and says so; a command that
// asked for other ones must not.
func (r *agentRestart) refuseLayerChange(request agentSettingsRequest) error {
	resolution := r.settings.resolution
	if !r.asksInstructions(request) {
		return nil
	}
	if name := resolution.InstructionsUnavailable; name != "" {
		reason := persona.ReasonOf(r.settings.instructionsErr)
		if reason == "" {
			reason = persona.ReasonNotFound
		}
		detail := "cannot be read"
		if r.settings.instructionsErr != nil {
			detail = r.settings.instructionsErr.Error()
		}
		return r.refuse(reason, fmt.Sprintf("cannot take instructions %s: %s", name, detail))
	}
	if resolution.InstructionsNotApplied {
		return r.refuse(personaReasonCodexInstructionsImmutable, "cannot change its instructions: Codex fixes developer instructions when the thread starts, and resume keeps the original message; create a new Codex Agent with a prompt to use different instructions")
	}
	return nil
}

// refuseCodexPermissionsKept refuses a Codex profile switch that would leave
// the thread running with the old profile's sandbox or approval: the new
// profile requests none where the old one requested one, or where the old
// one cannot be read to tell.
func (c *agentCommand) refuseCodexPermissionsKept(r *agentRestart) error {
	annotations := r.target.Metadata.Annotations
	var oldPolicy codexappserver.ThreadPolicy
	oldKnown := true
	if name := annotations[coremetadata.AnnotationAgentProfile]; name != "" {
		_, policy, err := c.rebind.create.codexResumeProfile(annotations)
		oldPolicy, oldKnown = policy, err == nil
	}
	_, newPolicy, err := c.rebind.create.codexResumeProfile(r.settings.launchAnnotations(annotations))
	if err != nil {
		return r.refuse(r.tokens.noConversation, "cannot be resumed: "+err.Error())
	}
	var kept []string
	if newPolicy.Sandbox == "" && (!oldKnown || oldPolicy.Sandbox != "") {
		kept = append(kept, "sandbox")
	}
	if newPolicy.ApprovalPolicy == "" && (!oldKnown || oldPolicy.ApprovalPolicy != "") {
		kept = append(kept, "approval")
	}
	if len(kept) == 0 {
		return nil
	}
	from := fmt.Sprintf("profile %s", annotations[coremetadata.AnnotationAgentProfile])
	if !oldKnown {
		from += ", which cannot be read now,"
	}
	to := "no profile"
	if name := r.settings.resolution.New.Profile.Name; name != "" {
		to = "profile " + name
	}
	return r.refuse(relaunchReasonCodexPermissionsKept, fmt.Sprintf("cannot drop the %s %s may have given its Codex thread: %s sets none, and a Codex resume can set a sandbox or an approval but not remove one; switch to a profile that sets them",
		strings.Join(kept, " and "), from, to))
}

// agentRestartSteps are the parts of one restart its command owns.
type agentRestartSteps struct {
	yes    bool
	socket deleteSocketFlags
	json   bool
	// beforeStop runs once every check passed, right before the stop; nil
	// writes nothing before it.
	beforeStop func() error
	// stopFailed is what the command does with a stop that reported err,
	// given what the stop left of the managed Pane; returning nil goes on to
	// the resume.
	stopFailed func(err error, liveness personaPaneLiveness, observeErr error) error
	// resume is what the resume asks of the Agent's layers.
	resume agentSettingsRequest
	// resumeFailed is the error of a resume that failed after the stop.
	resumeFailed func(err error) error
}

// run restarts the planned Agent: the confirmation and socket refusals, the
// stop of a Running Agent's managed Pane, and the resume. It returns the
// managed Pane the Agent runs in afterwards, "" when that cannot be read.
func (c *agentCommand) run(r *agentRestart, steps agentRestartSteps, stdout, stderr io.Writer) (string, error) {
	if r.confirmationRequired() && !steps.yes {
		return "", r.refuse(r.tokens.busy, fmt.Sprintf("is %s with interaction %s; restarting it would cut that turn. Re-run with --yes to restart it anyway, or with --dry-run to review",
			r.target.Status.Phase, r.interaction))
	}
	if r.running {
		// The stop runs through `delete pane`, which refuses outside tmux
		// without an exact socket. Refuse that here, before anything changes.
		if _, err := resolveDeleteTarget(r.spelling, steps.socket, c.lookupEnv); err != nil {
			return "", err
		}
	}
	if steps.beforeStop != nil {
		if err := steps.beforeStop(); err != nil {
			return "", err
		}
	}
	forward := stdout
	if steps.json {
		forward = io.Discard
	}
	if r.running {
		if err := c.stopAgentPane(steps.socket, r.paneUID, forward, stderr); err != nil {
			// `delete pane` can fail after its live half already closed the
			// Pane, so the error alone does not say whether the old provider
			// session still runs.
			liveness, observeErr := c.observeStoppedAgentPane(r.spelling, steps.socket, r.target.Metadata.UID, r.paneUID)
			if err := steps.stopFailed(err, liveness, observeErr); err != nil {
				return "", err
			}
		}
	}
	stopped := stoppedAgentPane{uid: r.paneUID, name: r.paneName}
	if err := c.resumeStoppedAgent(r.target.Metadata.UID, stopped, steps.resume, forward, stderr); err != nil {
		return "", steps.resumeFailed(err)
	}
	if after, err := c.loadRegistry(); err == nil {
		if resumed, ok := after.Agent(r.target.Metadata.UID); ok {
			return resumed.Status.PaneRef, nil
		}
	}
	return "", nil
}
