package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"strings"
)

// AgentPersonaRefusal is the typed cause of restart-specific persona refusals.
type AgentPersonaRefusal struct{ Reason string }

func (e *AgentPersonaRefusal) Error() string { return e.Reason }

func personaRefusalCause(reason, name, detail string) error {
	switch reason {
	case personaReasonAgentBusy, personaReasonSelfTarget, personaReasonNoConversation:
		return &AgentPersonaRefusal{Reason: reason}
	}
	return &persona.Error{Reason: reason, Name: name, Detail: detail}
}

type AgentPersonaRequest struct {
	Action, Spelling, Noun, Persona, Socket, SocketPath string
	DryRun, Confirm                                     bool
}
type AgentPersonaDependencies struct {
	Request     AgentPersonaRequest
	Registry    coremetadata.Registry
	Target      coremetadata.Agent
	Interaction coremetadata.AgentInteractionKind
	OpenStore   func() (persona.Store, error)
	Refuse      func(reason, message string, cause error) error
	Plan        func(*AgentRestartPlan, AgentSettingsRequest, func(string, string) error) error
	SetPersona  func(coremetadata.Agent, coremetadata.AgentPersonaAnnotations) error
	// Notice emits each diagnostic immediately for CLI stderr ordering.
	Notice    func(string)
	Execution AgentRestartExecution
}

const (
	// personaReasonAgentBusy refuses to cut a turn the operator did not
	// confirm: the Agent is Running and its interaction is not idle or
	// response_complete (unknown included).
	personaReasonAgentBusy = "persona-agent-busy"
	// personaReasonSelfTarget refuses to restart the Agent that owns the Pane
	// the command runs in: closing that Pane would end the command before the
	// resume.
	personaReasonSelfTarget = "persona-self-target"
	// personaReasonNoConversation refuses an Agent with no provider
	// conversation to resume, or one whose resume would be refused.
	personaReasonNoConversation = "persona-no-conversation"
)

const (
	personaOutcomeUnchanged    = "unchanged"
	personaOutcomeRestarted    = "restarted"
	personaOutcomeResumed      = "resumed"
	personaOutcomeWouldRestart = "would-restart"
	personaOutcomeWouldResume  = "would-resume"
)

const personaReasonCodexInstructionsImmutable = PersonaReasonCodexInstructionsImmutable

type AgentPersonaResult struct {
	Action               string                            `json:"action"`
	DryRun               bool                              `json:"dryRun"`
	Outcome              string                            `json:"outcome"`
	AgentUID             string                            `json:"agentUID"`
	AgentName            string                            `json:"agentName"`
	Provider             string                            `json:"provider"`
	Phase                coremetadata.AgentPhase           `json:"phase"`
	Interaction          coremetadata.AgentInteractionKind `json:"interaction"`
	PaneUID              string                            `json:"paneUID,omitempty"`
	NewPaneUID           string                            `json:"newPaneUID,omitempty"`
	CurrentPersona       string                            `json:"currentPersona,omitempty"`
	CurrentPersonaDigest string                            `json:"currentPersonaDigest,omitempty"`
	NewPersona           string                            `json:"newPersona,omitempty"`
	NewPersonaDigest     string                            `json:"newPersonaDigest,omitempty"`
	SystemPromptSnapshot string                            `json:"systemPromptSnapshot"`
	Restart              bool                              `json:"restart"`
	ConfirmationRequired bool                              `json:"confirmationRequired"`
	Unchanged            bool                              `json:"unchanged"`
	Notices              []string                          `json:"-"`
	Recovery             string                            `json:"-"`
}

func RunAgentPersona(d AgentPersonaDependencies) (out AgentPersonaResult, retErr error) {
	target := d.Target.Clone()
	registry := d.Registry
	request := d.Request
	noun := request.Noun
	notice := func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		out.Notices = append(out.Notices, strings.TrimSuffix(message, "\n"))
		if d.Notice != nil {
			d.Notice(message)
		}
	}
	refuse := func(reason, detail string) error {
		return d.Refuse(reason, fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", request.Spelling, target.Metadata.Name, detail, reason),
			personaRefusalCause(reason, request.Persona, detail))
	}

	// Validation. Provider first, because the rest only means something for a
	// Claude Agent.
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if provider == "" && target.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(target.Status.SessionRef.Provider)
	}
	if provider != "claude" {
		if provider == "codex" {
			return out, refuse(personaReasonCodexInstructionsImmutable, "cannot change its instructions: Codex fixes developer instructions when the thread starts, and resume keeps the original message; create a new Codex Agent with a prompt to use different instructions")
		}
		if noun == "instructions" {
			return out, refuse(persona.ReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; named instructions apply only to --provider %s", target.Spec.Provider, "claude"))
		}
		return out, refuse(persona.ReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; a persona applies only to --provider %s", target.Spec.Provider, "claude"))
	}
	restart := AgentRestartPlan{Target: target, Provider: provider, Interaction: d.Interaction}
	tokens := PersonaTokens(noun)
	if err := restart.CheckTarget(registry, tokens, refuse); err != nil {
		return out, err
	}
	var loaded persona.Persona
	if request.Action == "attach" {
		store, err := d.OpenStore()
		if err != nil {
			return out, fmt.Errorf("%s: %w; nothing was changed", request.Spelling, err)
		}
		loaded, err = store.Load(request.Persona)
		if err != nil {
			if persona.ReasonOf(err) != "" {
				return out, d.Refuse(persona.ReasonOf(err), fmt.Sprintf("%s: %v; nothing was changed", request.Spelling, PersonaRefusalNoun(err, noun)), err)
			}
			return out, fmt.Errorf("%s: %w; nothing was changed", request.Spelling, err)
		}
	}
	// The instructions this command gives are an override recorded with the
	// source attach, `none` for a detach.
	instructions := loaded.Name
	if err := d.Plan(&restart, AgentSettingsRequest{Instructions: &instructions, Source: coremetadata.SettingSourceAttach}, refuse); err != nil {
		return out, err
	}

	running, paneUID := restart.Running, restart.PaneUID
	current := coremetadata.PersonaAnnotationsOf(target)
	want := coremetadata.AgentPersonaAnnotations{SystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff, InstructionsSource: coremetadata.SettingSourceAttach}
	if request.Action == "attach" {
		want.Persona, want.PersonaDigest = loaded.Name, loaded.Digest
	}
	result := AgentPersonaResult{
		Action: request.Action, DryRun: request.DryRun,
		AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: provider,
		Phase: target.Status.Phase, Interaction: restart.Interaction, PaneUID: paneUID,
		CurrentPersona: current.Persona, CurrentPersonaDigest: current.PersonaDigest,
		NewPersona: want.Persona, NewPersonaDigest: want.PersonaDigest,
		SystemPromptSnapshot: want.SystemPromptSnapshot,
		Restart:              running,
		ConfirmationRequired: restart.ConfirmationRequired(),
	}
	// A Running Agent already launched with exactly this persona content and
	// the snapshot mode that honors it, whose other settings its layers
	// resolve to what it runs, has nothing to gain from a restart.
	if running && current.SameLaunch(want) && len(restart.Settings.Resolution.Reasons) == 0 {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = paneUID
		out = result
		return out, nil
	}
	if request.DryRun {
		result.Outcome = personaOutcomeWouldResume
		if running {
			result.Outcome = personaOutcomeWouldRestart
		}
		out = result
		return out, nil
	}
	out = result
	execution := d.Execution
	execution.Confirm, execution.Tokens, execution.Refuse = request.Confirm, tokens, refuse
	execution.BeforeStop = func() error {
		if d.Execution.BeforeStop != nil {
			if err := d.Execution.BeforeStop(); err != nil {
				return err
			}
		}
		if request.Action == "attach" {
			store, err := d.OpenStore()
			if err != nil {
				return fmt.Errorf("%s: %w; nothing was changed", request.Spelling, err)
			}
			if _, err := store.WriteSnapshot(loaded.Content); err != nil {
				return fmt.Errorf("%s: %w; nothing was changed", request.Spelling, err)
			}
		}
		return d.SetPersona(target, want)
	}
	execution.StopFailed = func(err error, liveness AgentRestartLiveness, observeErr error) error {
		if liveness == AgentRestartClosed {
			// The old session is gone, so the old annotations describe
			// nothing that runs. Keep the new ones and resume: restoring
			// them would bring the next resume back without the persona.
			notice("projmux: warning: closing agent/%s's managed pane %s reported an error, but that pane is already closed, so its new persona is kept and the Agent is resumed: %v\n",
				target.Metadata.Name, paneUID, err)
			return nil
		}
		// The Agent may still run its old provider session, so its
		// annotations go back to what that session was launched with:
		// leaving the new ones would make a re-run report `unchanged` for
		// a persona the provider never received.
		if restoreErr := d.SetPersona(target, current); restoreErr != nil {
			return fmt.Errorf("%s: closing agent/%s's managed pane failed: %w; restoring its persona annotations also failed: %v",
				request.Spelling, target.Metadata.Name, err, restoreErr)
		}
		if liveness == AgentRestartUnknown {
			out.Recovery = PersonaRerunCommand(registry, target, request)
			notice("projmux: could not observe whether agent/%s's managed pane %s is still alive (%v); its persona annotations were restored, and re-running the same command recovers: %s\n",
				target.Metadata.Name, paneUID, observeErr, PersonaRerunCommand(registry, target, request))
			return fmt.Errorf("%s: closing agent/%s's managed pane failed and whether it is still alive could not be observed, so its persona annotations were restored: %w",
				request.Spelling, target.Metadata.Name, err)
		}
		return fmt.Errorf("%s: closing agent/%s's managed pane failed, so its persona annotations were restored: %w",
			request.Spelling, target.Metadata.Name, err)
	}
	execution.ResumeFailed = func(err error) error {
		out.Recovery = PersonaRecoveryCommand(registry, target)
		notice("projmux: agent/%s records %s but did not resume: %v\n", target.Metadata.Name, DescribePersonaAnnotations(want, noun), err)
		notice("projmux: recover with: %s\n", PersonaRecoveryCommand(registry, target))
		return fmt.Errorf("%s: agent/%s is %s with its new persona annotations and needs `agent resume`: %w",
			request.Spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
	}
	newPane, err := restart.Run(execution)
	if err != nil {
		return out, err
	}
	out.Outcome = personaOutcomeResumed
	if running {
		out.Outcome = personaOutcomeRestarted
	}
	out.NewPaneUID = newPane
	return out, nil
}
func PersonaRefusalNoun(err error, noun string) error {
	if noun == persona.DeprecatedNoun {
		return persona.SpelledAs(err, persona.DeprecatedNoun)
	}
	return err
}

func PersonaTokens(noun string) AgentRestartTokens {
	return AgentRestartTokens{
		Busy: personaReasonAgentBusy, SelfTarget: personaReasonSelfTarget, NoConversation: personaReasonNoConversation,
		Phase: func(phase coremetadata.AgentPhase) string {
			if noun == "instructions" {
				return fmt.Sprintf("is %s; instructions are attached only to a %s, %s, or %s Agent",
					phase, coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed)
			}
			return fmt.Sprintf("is %s; a persona is attached only to a %s, %s, or %s Agent",
				phase, coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed)
		},
	}
}

func PersonaRecoveryCommand(registry coremetadata.Registry, agent coremetadata.Agent) string {
	return "projmux agent resume " + personaAgentRef(registry, agent)
}

// PersonaRerunCommand is the same `agent persona` invocation, for a run that
// restored the previous annotations without knowing whether the stop closed
// the Pane: a re-run observes the Agent afresh and finishes the change.
func PersonaRerunCommand(registry coremetadata.Registry, agent coremetadata.Agent, request AgentPersonaRequest) string {
	command := "projmux " + request.Spelling + " " + personaAgentRef(registry, agent)
	if request.Socket != "" {
		command += " --socket " + personaCommandWord(request.Socket)
	}
	if request.SocketPath != "" {
		command += " --socket-path " + personaCommandWord(request.SocketPath)
	}
	if request.Confirm {
		command += " --yes"
	}
	if request.Action == "attach" {
		command += " " + request.Persona
	}
	return command
}

// personaAgentRef names the Agent by uid inside its exact Window and Project.
func personaAgentRef(registry coremetadata.Registry, agent coremetadata.Agent) string {
	ref := selector.UIDPrefix + agent.Metadata.UID
	windowUID := agent.Metadata.OwnerUID()
	if window, ok := registry.Window(windowUID); ok {
		if projectUID := window.Metadata.OwnerUID(); projectUID != "" {
			ref += " --project " + selector.UIDPrefix + projectUID
		}
		ref += " --window " + selector.UIDPrefix + windowUID
	}
	return ref
}

// personaCommandWord quotes a flag value for a printed command only when the
// shell would split or expand it.
func personaCommandWord(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
		return value
	}
	return ShellQuote(value)
}

func DescribePersonaAnnotations(annotations coremetadata.AgentPersonaAnnotations, noun string) string {
	if annotations.Persona == "" {
		return "no " + noun
	}
	return fmt.Sprintf("%s %s (%s)", noun, annotations.Persona, annotations.PersonaDigest)
}

// SetPlannedAgentPersona rejects a stale persona plan inside the caller's locked mutation.
func SetPlannedAgentPersona(spelling string, reg *coremetadata.Registry, mut coremetadata.Mutator, planned coremetadata.Agent, want coremetadata.AgentPersonaAnnotations) error {
	agent, ok := reg.Agent(planned.Metadata.UID)
	if !ok || agent.Status.Phase != planned.Status.Phase || agent.Status.PaneRef != planned.Status.PaneRef {
		return fmt.Errorf("%s: agent/%s changed since this run planned it; nothing was changed, re-run it", spelling, planned.Metadata.Name)
	}
	_, err := mut.SetAgentPersona(reg, planned.Metadata.UID, want)
	return err
}
