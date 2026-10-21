package runtime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// AgentRestartPlan is the shared, read-only plan of a tmux restart. Adapters
// supply their existing resume, exact transport and caller-observation seams.
// No ambient actor is inferred by this service.
type AgentRestartPlan struct {
	Target            metadata.Agent
	Provider          string
	Running           bool
	PaneUID, PaneName string
	Interaction       metadata.AgentInteractionKind
	Settings          AgentSettingsLaunch
	Resume            AgentResumePlan
}

type AgentRestartTokens struct {
	Busy, SelfTarget, NoConversation string
	Phase                            func(metadata.AgentPhase) string
}

type AgentRestartPlanning struct {
	Registry        metadata.Registry
	Tokens          AgentRestartTokens
	Refuse          func(string, string) error
	Predict         func(string) (AgentResumePlan, error)
	ResolveSettings func(AgentResumePlan, AgentSettingsRequest) (AgentSettingsLaunch, error)
	PreflightCodex  func(AgentResumePlan, AgentSettingsLaunch) error
	SelfTarget      func() error
}

func (p AgentRestartPlan) ConfirmationRequired() bool {
	return p.Running && p.Interaction != metadata.InteractionIdle && p.Interaction != metadata.InteractionResponseComplete
}

func (p *AgentRestartPlan) CheckTarget(reg metadata.Registry, tokens AgentRestartTokens, refuse func(string, string) error) error {
	target := p.Target
	if target.Status.SessionRef.Empty() || strings.TrimSpace(target.Status.SessionRef.ConversationID()) == "" {
		return refuse(tokens.NoConversation, "has no provider conversation to resume; projmux records one the first time its provider hook fires")
	}
	p.Running = target.Status.Phase == metadata.PhaseRunning
	if !p.Running && !slices.Contains(ResumableAgentPhases, target.Status.Phase) {
		return refuse(tokens.NoConversation, tokens.Phase(target.Status.Phase))
	}
	if p.Running {
		p.PaneUID = strings.TrimSpace(target.Status.PaneRef)
		pane, ok := reg.Pane(p.PaneUID)
		if !ok {
			return refuse(tokens.NoConversation, fmt.Sprintf("is %s but its managed pane %q is not in the registry", target.Status.Phase, p.PaneUID))
		}
		if AgentPaneNameCandidate(*pane, target.Metadata.UID) {
			p.PaneName = pane.Metadata.Name
		}
	}
	return nil
}

// Plan performs the same read-only gates for CLI relaunch and SDK relaunch.
// The CLI also uses this for instructions/persona restarts.
func (p *AgentRestartPlan) Plan(q AgentSettingsRequest, d AgentRestartPlanning) error {
	if metadata.RecordsDialogueReplyOnly(p.Target.Metadata.Annotations) {
		reason, carry := "", ""
		switch {
		case q.Profile != nil:
			reason, carry = profileReasonLaneUnsupported, "a profile"
		case q.Instructions != nil || slices.Contains(q.Reset, agentsettings.ItemInstructions):
			reason, carry = persona.ReasonProviderUnsupported, "instructions"
		case q.Model != "" || q.Effort != "" || len(q.Reset) > 0:
			reason, carry = "reply-only-launch-fixed", "a model, an effort, or a reset"
		}
		if reason != "" {
			return d.Refuse(reason, fmt.Sprintf("records the reply-only activation (--dialogue-reply-only), whose fixed launch cannot carry %s; create a new Agent to run with it", carry))
		}
	}
	plan, err := d.Predict(p.PaneUID)
	if err != nil {
		return d.Refuse(d.Tokens.NoConversation, "cannot be resumed: "+err.Error())
	}
	p.Resume = plan
	q.ProjectUID = plan.ProjectUID
	if d.ResolveSettings != nil {
		p.Settings, err = d.ResolveSettings(plan, q)
		if err != nil {
			if e, ok := err.(*RelaunchProfileError); ok {
				return d.Refuse(e.Reason, fmt.Sprintf("cannot take profile %q: %s", e.Name, e.Detail))
			}
			return d.Refuse(d.Tokens.NoConversation, "cannot be resumed: "+err.Error())
		}
		if err := p.RefuseLayerChange(q, d.Refuse); err != nil {
			return err
		}
	}
	if p.Provider == "codex" && d.PreflightCodex != nil {
		if err := d.PreflightCodex(plan, p.Settings); err != nil {
			return err
		}
	}
	if p.Running && d.SelfTarget != nil {
		return d.SelfTarget()
	}
	return nil
}

func (p AgentRestartPlan) RefuseLayerChange(q AgentSettingsRequest, refuse func(string, string) error) error {
	if p.Provider == "codex" && p.Settings.ManagedBase != nil && p.Settings.ManagedBase.Fallback {
		return nil
	}
	r := p.Settings.Resolution
	if q.Instructions == nil && !slices.Contains(q.Reset, agentsettings.ItemInstructions) && !r.ProfileSwitched {
		return nil
	}
	if name := r.InstructionsUnavailable; name != "" {
		reason := persona.ReasonOf(p.Settings.InstructionsErr)
		if reason == "" {
			reason = persona.ReasonNotFound
		}
		detail := "cannot be read"
		if p.Settings.InstructionsErr != nil {
			detail = p.Settings.InstructionsErr.Error()
		}
		return refuse(reason, fmt.Sprintf("cannot take instructions %s: %s", name, detail))
	}
	if r.InstructionsNotApplied {
		return refuse(PersonaReasonCodexInstructionsImmutable, "cannot change its instructions: Codex fixes developer instructions when the thread starts, and resume keeps the original message; create a new Codex Agent with a prompt to use different instructions")
	}
	return nil
}

// AgentRestartLiveness distinguishes a failed stop from the state it left.
type AgentRestartLiveness int

const (
	AgentRestartAlive AgentRestartLiveness = iota
	AgentRestartClosed
	AgentRestartUnknown
)

type AgentRestartExecution struct {
	Confirm           bool
	Tokens            AgentRestartTokens
	Refuse            func(string, string) error
	ValidateTransport func() error
	BeforeStop        func() error
	Stop              func() error
	Observe           func() (AgentRestartLiveness, error)
	StopFailed        func(error, AgentRestartLiveness, error) error
	Resume            func(StoppedAgentPane) error
	ResumeFailed      func(error) error
	NewPane           func() string
}

// Run owns the shared confirmation/stop/observe/resume sequence. A failed or
// uncertain operation is never replayed. Recovery presentation belongs to the adapter.
func (p AgentRestartPlan) Run(d AgentRestartExecution) (string, error) {
	if p.ConfirmationRequired() && !d.Confirm {
		return "", d.Refuse(d.Tokens.Busy, fmt.Sprintf("is %s with interaction %s; restarting it would cut that turn. Re-run with --yes to restart it anyway, or with --dry-run to review", p.Target.Status.Phase, p.Interaction))
	}
	if p.Running {
		if err := d.ValidateTransport(); err != nil {
			return "", err
		}
	}
	if d.BeforeStop != nil {
		if err := d.BeforeStop(); err != nil {
			return "", err
		}
	}
	if p.Running {
		if err := d.Stop(); err != nil {
			live, observeErr := d.Observe()
			if err := d.StopFailed(err, live, observeErr); err != nil {
				return "", err
			}
		}
	}
	if err := d.Resume(StoppedAgentPane{UID: p.PaneUID, Name: p.PaneName}); err != nil {
		return "", d.ResumeFailed(err)
	}
	return d.NewPane(), nil
}

// RelaunchDisposition is evaluated before confirmation, matching the CLI's
// unchanged and dry-run ordering. Explicit model always requests a restart.
func (p AgentRestartPlan) RelaunchDisposition(q AgentSettingsRequest, dryRun bool) string {
	r := p.Settings.Resolution
	changes := r.ProfileSwitched || q.Instructions != nil || len(q.Reset) > 0
	if p.Running && q.Model == "" && len(r.Reasons) == 0 && !(changes && r.LayersChanged()) {
		return "unchanged"
	}
	if dryRun {
		if p.Running {
			return "would-restart"
		}
		return "would-resume"
	}
	if p.Running {
		return "restarted"
	}
	return "resumed"
}
