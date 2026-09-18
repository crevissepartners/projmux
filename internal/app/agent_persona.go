package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// Refusal reason tokens of `agent persona attach|detach` that the persona
// package does not own. Like the persona package's tokens they are stable
// strings, carried verbatim in the error text.
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

// Outcomes of one `agent persona` run, as the result line and the JSON
// "outcome" field spell them.
const (
	personaOutcomeUnchanged    = "unchanged"
	personaOutcomeRestarted    = "restarted"
	personaOutcomeResumed      = "resumed"
	personaOutcomeWouldRestart = "would-restart"
	personaOutcomeWouldResume  = "would-resume"
)

// agentPersonaResult is the stable projection of one `agent persona attach`
// or `detach` run. `--dry-run -o json` is what a confirmation dialog reads, so
// the fields describe the target and the change before anything happens.
type agentPersonaResult struct {
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
}

// agentPersonaRequest is one parsed `agent persona attach|detach` argv.
type agentPersonaRequest struct {
	action   string
	spelling string
	agentRef string
	persona  string
	flags    resourceQueryFlags
	yes      bool
	dryRun   bool
	json     bool
	socket   deleteSocketFlags
}

// runPersona gives one existing Claude Agent a persona, or takes it away, and
// relaunches that Agent's provider on the same conversation.
//
// It is the two shipped routes run back to back, not a third way to restart a
// provider: a Running Agent's managed Pane is closed through `delete pane`,
// which leaves the Agent Offline, and the Agent is brought back through the
// `agent resume` rebinder, which keeps its uid and its conversation. Between
// the checks and the stop the command writes the persona snapshot and changes
// the Agent's persona annotations in one Registry mutation, so the resume
// seam finds the new persona the same way every later resume will.
//
// Every refusal happens before the snapshot is written and leaves no trace.
// A resume that fails after the stop leaves the Agent Offline with the new
// annotations and prints the `agent resume` command that finishes the job.
func (c *agentCommand) runPersona(args []string, stdout, stderr io.Writer) error {
	request, err := parseAgentPersonaArgs(args, stderr)
	if err != nil {
		return err
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := request.flags.resolve(selector.VerbResume, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	agent, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", request.spelling, resolution.Matches[0].UID)
	}
	target := agent.Clone()
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", request.spelling, target.Metadata.Name, detail, reason))
	}

	// Validation. Provider first, because the rest only means something for a
	// Claude Agent.
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if provider == "" && target.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(target.Status.SessionRef.Provider)
	}
	if provider != aiModeClaude {
		return refuse(persona.ReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; a persona applies only to --provider %s", target.Spec.Provider, aiModeClaude))
	}
	if target.Status.SessionRef.Empty() || strings.TrimSpace(target.Status.SessionRef.ConversationID()) == "" {
		return refuse(personaReasonNoConversation, "has no provider conversation to resume; projmux records one the first time its provider hook fires")
	}
	running := target.Status.Phase == coremetadata.PhaseRunning
	if !running && !slices.Contains(resumableAgentPhases, target.Status.Phase) {
		return refuse(personaReasonNoConversation, fmt.Sprintf("is %s; a persona is attached only to a %s, %s, or %s Agent",
			target.Status.Phase, coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
	}
	var paneUID string
	if running {
		paneUID = strings.TrimSpace(target.Status.PaneRef)
		if _, ok := registry.Pane(paneUID); !ok {
			return refuse(personaReasonNoConversation, fmt.Sprintf("is %s but its managed pane %q is not in the registry", target.Status.Phase, paneUID))
		}
	}
	var loaded persona.Persona
	if request.action == "attach" {
		store, err := c.openPersonaStore()
		if err != nil {
			return fmt.Errorf("%s: %w; nothing was changed", request.spelling, err)
		}
		loaded, err = store.Load(request.persona)
		if err != nil {
			if persona.ReasonOf(err) != "" {
				return usageError(fmt.Sprintf("%s: %v; nothing was changed", request.spelling, err))
			}
			return fmt.Errorf("%s: %w; nothing was changed", request.spelling, err)
		}
	}
	// The resume this command ends with is planned now, against the registry
	// the stop will leave behind, so a resume `agent resume` would refuse is
	// refused here while nothing has changed.
	if err := c.preflightPersonaResume(registry, target, paneUID); err != nil {
		return refuse(personaReasonNoConversation, "cannot be resumed: "+err.Error())
	}
	if running && c.invokedFromAgentPane(registry, target.Metadata.UID) {
		return refuse(personaReasonSelfTarget, "owns the Pane this command runs in; closing that Pane would end the command before the resume. Run it from another Pane")
	}

	current := coremetadata.PersonaAnnotationsOf(target)
	want := coremetadata.AgentPersonaAnnotations{SystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	if request.action == "attach" {
		want.Persona, want.PersonaDigest = loaded.Name, loaded.Digest
	}
	interaction := target.EffectiveInteraction(c.clock()).Kind
	result := agentPersonaResult{
		Action: request.action, DryRun: request.dryRun,
		AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: provider,
		Phase: target.Status.Phase, Interaction: interaction, PaneUID: paneUID,
		CurrentPersona: current.Persona, CurrentPersonaDigest: current.PersonaDigest,
		NewPersona: want.Persona, NewPersonaDigest: want.PersonaDigest,
		SystemPromptSnapshot: want.SystemPromptSnapshot,
		Restart:              running,
		ConfirmationRequired: running && interaction != coremetadata.InteractionIdle && interaction != coremetadata.InteractionResponseComplete,
	}
	// A Running Agent already launched with exactly this persona content and
	// the snapshot mode that honors it has nothing to gain from a restart.
	if running && current == want {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = paneUID
		return writeAgentPersonaResult(stdout, request, result)
	}
	if request.dryRun {
		result.Outcome = personaOutcomeWouldResume
		if running {
			result.Outcome = personaOutcomeWouldRestart
		}
		return writeAgentPersonaResult(stdout, request, result)
	}
	if result.ConfirmationRequired && !request.yes {
		return refuse(personaReasonAgentBusy, fmt.Sprintf("is %s with interaction %s; restarting it would cut that turn. Re-run with --yes to restart it anyway, or with --dry-run to review",
			target.Status.Phase, interaction))
	}
	if running {
		// The stop runs through `delete pane`, which refuses outside tmux
		// without an exact socket. Refuse that here, before anything changes.
		if _, err := resolveDeleteTarget(request.spelling, request.socket, c.lookupEnv); err != nil {
			return err
		}
	}

	if request.action == "attach" {
		store, err := c.openPersonaStore()
		if err != nil {
			return fmt.Errorf("%s: %w; nothing was changed", request.spelling, err)
		}
		if _, err := store.WriteSnapshot(loaded.Content); err != nil {
			return fmt.Errorf("%s: %w; nothing was changed", request.spelling, err)
		}
	}
	if err := c.setAgentPersona(request.spelling, target, want); err != nil {
		return err
	}

	forward := stdout
	if request.json {
		forward = io.Discard
	}
	if running {
		if err := c.stopAgentPane(request, paneUID, forward, stderr); err != nil {
			// The Agent still runs its old provider session, so its annotations
			// go back to what that session was launched with: leaving the new
			// ones would make a re-run report `unchanged` for a persona the
			// provider never received.
			if restoreErr := c.setAgentPersona(request.spelling, target, current); restoreErr != nil {
				return fmt.Errorf("%s: closing agent/%s's managed pane failed: %w; restoring its persona annotations also failed: %v",
					request.spelling, target.Metadata.Name, err, restoreErr)
			}
			return fmt.Errorf("%s: closing agent/%s's managed pane failed, so its persona annotations were restored: %w",
				request.spelling, target.Metadata.Name, err)
		}
	}
	if err := c.resumePersonaAgent(request.spelling, target.Metadata.UID, forward, stderr); err != nil {
		fmt.Fprintf(stderr, "projmux: agent/%s records %s but did not resume: %v\n", target.Metadata.Name, describePersonaAnnotations(want), err)
		fmt.Fprintf(stderr, "projmux: recover with: %s\n", personaRecoveryCommand(registry, target))
		return fmt.Errorf("%s: agent/%s is %s with its new persona annotations and needs `agent resume`: %w",
			request.spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
	}
	result.Outcome = personaOutcomeResumed
	if running {
		result.Outcome = personaOutcomeRestarted
	}
	if after, err := c.loadRegistry(); err == nil {
		if resumed, ok := after.Agent(target.Metadata.UID); ok {
			result.NewPaneUID = resumed.Status.PaneRef
		}
	}
	return writeAgentPersonaResult(stdout, request, result)
}

// parseAgentPersonaArgs parses `attach <agent-ref> <persona>` and
// `detach <agent-ref>` with their flags. The Agent reference is required:
// this is a restart command, so it never falls back to the active Pane.
func parseAgentPersonaArgs(args []string, stderr io.Writer) (agentPersonaRequest, error) {
	if len(args) == 0 || (args[0] != "attach" && args[0] != "detach") {
		return agentPersonaRequest{}, usageError("agent persona requires attach or detach")
	}
	request := agentPersonaRequest{action: args[0], spelling: "agent persona " + args[0]}
	fs := flag.NewFlagSet(request.spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	request.flags = resourceQueryFlags{kind: coremetadata.KindAgent}
	request.flags.register(fs)
	fs.BoolVar(&request.yes, "yes", false, "restart the Agent even when its interaction shows a turn in progress or unknown")
	fs.BoolVar(&request.dryRun, "dry-run", false, "report the target, its interaction, and the persona change without changing anything")
	fs.StringVar(&request.socket.socket, "socket", "", "exact tmux socket name (tmux -L) the managed Pane of a Running Agent is closed on")
	fs.StringVar(&request.socket.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S) the managed Pane of a Running Agent is closed on")
	var output string
	fs.StringVar(&output, "output", "", "result projection: json")
	fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	positionals, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return agentPersonaRequest{}, err
		}
		return agentPersonaRequest{}, usageError(err.Error())
	}
	want := 2
	shape := "<agent-ref> <persona>"
	if request.action == "detach" {
		want, shape = 1, "<agent-ref>"
	}
	if len(positionals) != want {
		return agentPersonaRequest{}, usageError(fmt.Sprintf("%s requires %s; it restarts that exact Agent, so the Agent is never taken from the active Pane", request.spelling, shape))
	}
	if output != "" && output != "json" {
		return agentPersonaRequest{}, usageError(fmt.Sprintf("%s: unsupported output %q; want json", request.spelling, output))
	}
	request.json = output == "json"
	request.agentRef = positionals[0]
	request.flags.addPositionalRef(request.agentRef)
	if request.action == "attach" {
		request.persona = positionals[1]
		if err := persona.ValidateName(request.persona); err != nil {
			return agentPersonaRequest{}, usageError(fmt.Sprintf("%s: %v; nothing was changed", request.spelling, err))
		}
	}
	return request, nil
}

// openPersonaStore returns the persona store the resume seam reads snapshots
// from.
func (c *agentCommand) openPersonaStore() (persona.Store, error) {
	if c.personaStore != nil {
		return c.personaStore()
	}
	paths, err := configPaths(nil, c.lookupEnv)
	if err != nil {
		return persona.Store{}, err
	}
	return persona.NewDefaultStore(paths), nil
}

// preflightPersonaResume plans the final resume against the registry the stop
// would leave: the Running Agent's managed Pane deleted by the same mutator
// `delete pane` runs. Nothing is written.
func (c *agentCommand) preflightPersonaResume(registry coremetadata.Registry, agent coremetadata.Agent, paneUID string) error {
	predicted := registry.Clone()
	if paneUID != "" {
		if c.store == nil || c.store.mutator == nil {
			return errors.New("the registry mutator is not configured")
		}
		if err := c.store.mutator().DeletePane(&predicted, paneUID); err != nil {
			return err
		}
	}
	stopped, ok := predicted.Agent(agent.Metadata.UID)
	if !ok {
		return fmt.Errorf("agent %q disappeared", agent.Metadata.UID)
	}
	_, err := c.prepareResume("agent resume", predicted, stopped)
	return err
}

// invokedFromAgentPane reports whether this process runs in the managed Pane
// of agentUID. The caller's Pane is found runtime-first, the way create finds
// its creator: the ambient tmux Pane id, the one Registry Pane whose activation
// carries that runtime id, and the Agent that Pane belongs to. Outside tmux
// there is no caller Pane, so nothing is a self target.
func (c *agentCommand) invokedFromAgentPane(registry coremetadata.Registry, agentUID string) bool {
	paneID, err := resolveRuntimeMutationAnchorPane(c.lookupEnv, "")
	if err != nil || paneID == "" {
		return false
	}
	owner, _, skip := registryCreatorPane(&registry, paneID)
	return skip == "" && owner == agentUID
}

// setAgentPersona writes want onto the Agent in one Registry mutation, and
// only while the Agent is still in the phase and Pane this run planned for.
func (c *agentCommand) setAgentPersona(spelling string, planned coremetadata.Agent, want coremetadata.AgentPersonaAnnotations) error {
	return c.mutateAgent(planned.Metadata.UID, func(reg *coremetadata.Registry, mut coremetadata.Mutator) error {
		agent, ok := reg.Agent(planned.Metadata.UID)
		if !ok || agent.Status.Phase != planned.Status.Phase || agent.Status.PaneRef != planned.Status.PaneRef {
			return fmt.Errorf("%s: agent/%s changed since this run planned it; nothing was changed, re-run it", spelling, planned.Metadata.Name)
		}
		_, err := mut.SetAgentPersona(reg, planned.Metadata.UID, want)
		return err
	})
}

// stopAgentPane closes the Agent's managed Pane through `delete pane`, which
// leaves the Agent Offline. The socket flags are passed through unchanged, so
// the live half addresses exactly the server `delete pane` would.
func (c *agentCommand) stopAgentPane(request agentPersonaRequest, paneUID string, stdout, stderr io.Writer) error {
	if c.paneDelete == nil {
		return errors.New("the managed Pane delete route is not configured")
	}
	args := []string{"pane", selector.UIDPrefix + paneUID, "--yes"}
	if request.socket.socket != "" {
		args = append(args, "--socket", request.socket.socket)
	}
	if request.socket.socketPath != "" {
		args = append(args, "--socket-path", request.socket.socketPath)
	}
	return c.paneDelete.Run(args, stdout, stderr)
}

// resumePersonaAgent brings the stopped Agent back through the `agent resume`
// rebinder, planned from the registry as it is now.
func (c *agentCommand) resumePersonaAgent(spelling, agentUID string, stdout, stderr io.Writer) error {
	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	agent, ok := registry.Agent(agentUID)
	if !ok {
		return fmt.Errorf("agent %q is no longer in the registry", agentUID)
	}
	if err := requireResumablePhase("agent resume", agent); err != nil {
		return err
	}
	plan, err := c.prepareResume("agent resume", registry, agent)
	if err != nil {
		return err
	}
	return c.rebind.rebind("agent resume", plan, stdout, stderr)
}

// personaRecoveryCommand is the `agent resume` invocation that finishes an
// attach or detach whose resume failed. It names the Agent by uid inside its
// exact Window and Project, so it resolves to the same Agent from anywhere.
func personaRecoveryCommand(registry coremetadata.Registry, agent coremetadata.Agent) string {
	command := "projmux agent resume " + selector.UIDPrefix + agent.Metadata.UID
	windowUID := agent.Metadata.OwnerUID()
	if window, ok := registry.Window(windowUID); ok {
		if projectUID := window.Metadata.OwnerUID(); projectUID != "" {
			command += " --project " + selector.UIDPrefix + projectUID
		}
		command += " --window " + selector.UIDPrefix + windowUID
	}
	return command
}

func describePersonaAnnotations(annotations coremetadata.AgentPersonaAnnotations) string {
	if annotations.Persona == "" {
		return "no persona"
	}
	return fmt.Sprintf("persona %s (%s)", annotations.Persona, annotations.PersonaDigest)
}

// writeAgentPersonaResult prints one result as JSON or as one line.
func writeAgentPersonaResult(stdout io.Writer, request agentPersonaRequest, result agentPersonaResult) error {
	if request.json {
		return json.NewEncoder(stdout).Encode(result)
	}
	change := describePersonaAnnotations(coremetadata.AgentPersonaAnnotations{Persona: result.NewPersona, PersonaDigest: result.NewPersonaDigest})
	switch result.Outcome {
	case personaOutcomeUnchanged:
		_, err := fmt.Fprintf(stdout, "agent/%s unchanged: already running with %s and --system-prompt-snapshot off\n", result.AgentName, change)
		return err
	case personaOutcomeWouldRestart, personaOutcomeWouldResume:
		currentPersona := describePersonaAnnotations(coremetadata.AgentPersonaAnnotations{Persona: result.CurrentPersona, PersonaDigest: result.CurrentPersonaDigest})
		_, err := fmt.Fprintf(stdout,
			"%s: agent/%s uid=%s phase=%s interaction=%s from %s to %s; would %s it on the same conversation; confirmation-required=%t\ndry-run: nothing was changed\n",
			request.spelling, result.AgentName, result.AgentUID, result.Phase, result.Interaction, currentPersona, change,
			strings.TrimPrefix(result.Outcome, "would-"), result.ConfirmationRequired)
		return err
	default:
		verb := "attached"
		if request.action == "detach" {
			verb = "detached"
		}
		_, err := fmt.Fprintf(stdout, "agent/%s persona %s: now %s; %s on the same conversation\n", result.AgentName, verb, change, result.Outcome)
		return err
	}
}
