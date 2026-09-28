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
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// agentRelaunchSpelling is the one route spelling of `agent relaunch`.
const agentRelaunchSpelling = "agent relaunch"

// Refusal reason tokens of `agent relaunch`. They are stable strings, carried
// verbatim in the error text, like the `agent persona` tokens they mirror.
const (
	// relaunchReasonAgentBusy refuses to cut a turn the operator did not
	// confirm: the Agent is Running and its interaction is not idle or
	// response_complete (unknown included).
	relaunchReasonAgentBusy = "relaunch-agent-busy"
	// relaunchReasonSelfTarget refuses to restart the Agent that owns the
	// Pane the command runs in: closing that Pane would end the command
	// before the resume.
	relaunchReasonSelfTarget = "relaunch-self-target"
	// relaunchReasonNoConversation refuses an Agent with no provider
	// conversation to resume, or one whose resume would be refused.
	relaunchReasonNoConversation = "relaunch-no-conversation"
	// relaunchReasonProviderUnsupported refuses an Agent whose provider takes
	// no --model or --effort: only Claude and Codex do.
	relaunchReasonProviderUnsupported = "relaunch-provider-unsupported"
)

// agentRelaunchResult is the stable projection of one `agent relaunch` run.
// `--dry-run -o json` is what a confirmation dialog reads, so the fields
// describe the target and the change before anything happens.
type agentRelaunchResult struct {
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
	CurrentEffort        string                            `json:"currentEffort,omitempty"`
	CurrentModel         string                            `json:"currentModel,omitempty"`
	NewEffort            string                            `json:"newEffort,omitempty"`
	NewModel             string                            `json:"newModel,omitempty"`
	Restart              bool                              `json:"restart"`
	ConfirmationRequired bool                              `json:"confirmationRequired"`
	Unchanged            bool                              `json:"unchanged"`
}

// agentRelaunchRequest is one parsed `agent relaunch` argv.
type agentRelaunchRequest struct {
	agentRef string
	model    string
	effort   string
	flags    resourceQueryFlags
	yes      bool
	dryRun   bool
	json     bool
	socket   deleteSocketFlags
}

// runRelaunch restarts one existing Claude or Codex Agent on the same uid and
// the same provider conversation with another model or effort.
//
// It is the `agent persona` restart without the persona: a Running Agent's
// managed Pane is closed through `delete pane`, which leaves the Agent
// Offline, and the Agent is brought back through the `agent resume` rebinder
// with --model and --effort, which keeps its uid and its conversation. An
// Offline or Failed Agent is only resumed.
//
// Nothing is written before the stop. The model and effort are recorded by
// the rebind transaction, the way `agent resume --model/--effort` records
// them, so a launch that fails records neither. Every refusal happens before
// any change and leaves no trace.
//
// A stop that reports an error is not taken at its word, because `delete
// pane` can fail after the Pane is already gone: the Registry and the exact
// tmux server are observed again. A Pane still alive keeps running the old
// session and nothing changed, so the run fails. A Pane already closed goes on
// to the resume, with a warning. When liveness cannot be observed the run
// fails and prints the command to re-run. A resume that fails after the stop
// leaves the Agent Offline with its previous effort and prints the `agent
// resume` command, overrides included, that finishes the job.
func (c *agentCommand) runRelaunch(args []string, stdout, stderr io.Writer) error {
	const spelling = agentRelaunchSpelling
	request, err := parseAgentRelaunchArgs(args, stderr)
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
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, resolution.Matches[0].UID)
	}
	target := agent.Clone()
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", spelling, target.Metadata.Name, detail, reason))
	}

	// Validation. Provider first, because a model or an effort only means
	// something for a Claude or Codex Agent.
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if provider == "" && target.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(target.Status.SessionRef.Provider)
	}
	if provider != aiModeClaude && provider != aiModeCodex {
		return refuse(relaunchReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; --model and --effort apply only to --provider %s or %s", target.Spec.Provider, aiModeClaude, aiModeCodex))
	}
	if err := requireLaunchOptions(spelling, provider, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if target.Status.SessionRef.Empty() || strings.TrimSpace(target.Status.SessionRef.ConversationID()) == "" {
		return refuse(relaunchReasonNoConversation, "has no provider conversation to resume; projmux records one the first time its provider hook fires")
	}
	running := target.Status.Phase == coremetadata.PhaseRunning
	if !running && !slices.Contains(resumableAgentPhases, target.Status.Phase) {
		return refuse(relaunchReasonNoConversation, fmt.Sprintf("is %s; only a %s, %s, or %s Agent is relaunched",
			target.Status.Phase, coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
	}
	var paneUID string
	if running {
		paneUID = strings.TrimSpace(target.Status.PaneRef)
		if _, ok := registry.Pane(paneUID); !ok {
			return refuse(relaunchReasonNoConversation, fmt.Sprintf("is %s but its managed pane %q is not in the registry", target.Status.Phase, paneUID))
		}
	}
	// The resume this command ends with is planned now, against the registry
	// the stop will leave behind, so a resume `agent resume` would refuse is
	// refused here while nothing has changed.
	plan, err := c.predictStoppedAgentResume(registry, target, paneUID)
	if err == nil && provider == aiModeCodex {
		err = c.preflightCodexRelaunch(plan)
	}
	if err != nil {
		return refuse(relaunchReasonNoConversation, "cannot be resumed: "+err.Error())
	}
	if running && c.invokedFromAgentPane(registry, target.Metadata.UID) {
		return refuse(relaunchReasonSelfTarget, "owns the Pane this command runs in; closing that Pane would end the command before the resume. Run it from another Pane")
	}

	currentEffort := target.Metadata.Annotations[coremetadata.AnnotationAgentEffort]
	interaction := target.EffectiveInteraction(c.clock()).Kind
	result := agentRelaunchResult{
		Action: "relaunch", DryRun: request.dryRun,
		AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: provider,
		Phase: target.Status.Phase, Interaction: interaction, PaneUID: paneUID,
		CurrentEffort: currentEffort, CurrentModel: target.Metadata.Annotations[coremetadata.AnnotationAgentModel],
		NewEffort: request.effort, NewModel: request.model,
		Restart:              running,
		ConfirmationRequired: running && interaction != coremetadata.InteractionIdle && interaction != coremetadata.InteractionResponseComplete,
	}
	// A Running Agent already launched with exactly this effort has nothing
	// to gain from a restart. A --model always restarts: the recorded model is
	// the last one requested, not necessarily the one the provider runs now (a
	// `/model` switch inside the session is not observed), so matching the
	// record cannot prove nothing would change.
	if running && request.model == "" && request.effort == currentEffort {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = paneUID
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if request.dryRun {
		result.Outcome = personaOutcomeWouldResume
		if running {
			result.Outcome = personaOutcomeWouldRestart
		}
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if result.ConfirmationRequired && !request.yes {
		return refuse(relaunchReasonAgentBusy, fmt.Sprintf("is %s with interaction %s; restarting it would cut that turn. Re-run with --yes to restart it anyway, or with --dry-run to review",
			target.Status.Phase, interaction))
	}
	if running {
		// The stop runs through `delete pane`, which refuses outside tmux
		// without an exact socket. Refuse that here, before anything changes.
		if _, err := resolveDeleteTarget(spelling, request.socket, c.lookupEnv); err != nil {
			return err
		}
	}

	forward := stdout
	if request.json {
		forward = io.Discard
	}
	if running {
		if err := c.stopAgentPane(request.socket, paneUID, forward, stderr); err != nil {
			// `delete pane` can fail after its live half already closed the
			// Pane, so the error alone does not say whether the old provider
			// session still runs. Nothing was written before the stop, so
			// there is nothing to restore either way.
			liveness, observeErr := c.observeStoppedAgentPane(spelling, request.socket, target.Metadata.UID, paneUID)
			switch liveness {
			case personaPaneClosed:
				fmt.Fprintf(stderr, "projmux: warning: closing agent/%s's managed pane %s reported an error, but that pane is already closed, so the Agent is resumed with the new launch options: %v\n",
					target.Metadata.Name, paneUID, err)
			case personaPaneUnknown:
				fmt.Fprintf(stderr, "projmux: could not observe whether agent/%s's managed pane %s is still alive (%v); nothing was recorded, and re-running the same command recovers: %s\n",
					target.Metadata.Name, paneUID, observeErr, relaunchRerunCommand(registry, target, request))
				return fmt.Errorf("%s: closing agent/%s's managed pane failed and whether it is still alive could not be observed: %w",
					spelling, target.Metadata.Name, err)
			default:
				return fmt.Errorf("%s: closing agent/%s's managed pane failed, so it keeps running with its previous launch options: %w",
					spelling, target.Metadata.Name, err)
			}
		}
	}
	if err := c.resumeStoppedAgent(target.Metadata.UID, request.model, request.effort, forward, stderr); err != nil {
		fmt.Fprintf(stderr, "projmux: agent/%s did not resume with %s: %v\n", target.Metadata.Name, describeRelaunchTarget(result), err)
		fmt.Fprintf(stderr, "projmux: recover with: %s\n", relaunchRecoveryCommand(registry, target, request))
		return fmt.Errorf("%s: agent/%s is %s with its previous effort and needs `agent resume`: %w",
			spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
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
	return writeAgentRelaunchResult(stdout, request, result)
}

// preflightCodexRelaunch runs the read-only checks the Codex rebind makes
// before it asks the daemon anything: the recorded profile is still valid,
// and the stored thread has a durable endpoint a native resume can use. A
// Running Codex Agent is stopped only when these pass. The daemon itself is
// asked only by the resume, so a daemon down after the stop still leaves the
// Agent Offline with the recovery command.
func (c *agentCommand) preflightCodexRelaunch(plan agentResumePlan) error {
	if c.rebind == nil || c.rebind.create == nil {
		return errors.New("the resume materialization seam is not configured")
	}
	if _, _, err := c.rebind.create.codexResumeProfile(plan.annotations); err != nil {
		return err
	}
	return validateStoredCodexNativeResumeRoute(plan.ref)
}

// parseAgentRelaunchArgs parses `relaunch <agent-ref>` with its flags. The
// Agent reference is required: this is a restart command, so it never falls
// back to the active Pane.
func parseAgentRelaunchArgs(args []string, stderr io.Writer) (agentRelaunchRequest, error) {
	const spelling = agentRelaunchSpelling
	var request agentRelaunchRequest
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	request.flags = resourceQueryFlags{kind: coremetadata.KindAgent}
	request.flags.register(fs)
	fs.StringVar(&request.model, "model", "", "claude or codex: model name the relaunch runs; recorded on the Agent")
	fs.StringVar(&request.effort, "effort", "", "claude or codex: effort level, recorded on the Agent: "+strings.Join(claudeEffortLevels, "|"))
	fs.BoolVar(&request.yes, "yes", false, "restart the Agent even when its interaction shows a turn in progress or unknown")
	fs.BoolVar(&request.dryRun, "dry-run", false, "report the target, its interaction, and the model and effort change without changing anything")
	fs.StringVar(&request.socket.socket, "socket", "", "exact tmux socket name (tmux -L) the managed Pane of a Running Agent is closed on")
	fs.StringVar(&request.socket.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S) the managed Pane of a Running Agent is closed on")
	var output string
	fs.StringVar(&output, "output", "", "result projection: json")
	fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	positionals, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return agentRelaunchRequest{}, err
		}
		return agentRelaunchRequest{}, flagParseError(err)
	}
	if len(positionals) != 1 {
		return agentRelaunchRequest{}, usageError(spelling + " requires <agent-ref>; it restarts that exact Agent, so the Agent is never taken from the active Pane")
	}
	if output != "" && output != "json" {
		return agentRelaunchRequest{}, usageError(fmt.Sprintf("%s: unsupported output %q; want json", spelling, output))
	}
	if request.model == "" && request.effort == "" {
		return agentRelaunchRequest{}, usageError(spelling + " requires --model, --effort, or both; nothing was changed")
	}
	request.json = output == "json"
	request.agentRef = positionals[0]
	request.flags.addPositionalRef(request.agentRef)
	return request, nil
}

// relaunchOverrideFlags spells the overrides the request was given, in the
// order the usage line declares them.
func relaunchOverrideFlags(request agentRelaunchRequest) string {
	var flags string
	if request.model != "" {
		flags += " --model " + personaCommandWord(request.model)
	}
	if request.effort != "" {
		flags += " --effort " + personaCommandWord(request.effort)
	}
	return flags
}

// relaunchRecoveryCommand is the `agent resume` invocation, overrides
// included, that finishes a relaunch whose resume failed.
func relaunchRecoveryCommand(registry coremetadata.Registry, agent coremetadata.Agent, request agentRelaunchRequest) string {
	return personaRecoveryCommand(registry, agent) + relaunchOverrideFlags(request)
}

// relaunchRerunCommand is the same `agent relaunch` invocation, for a stop
// whose outcome could not be observed: a re-run observes the Agent afresh.
func relaunchRerunCommand(registry coremetadata.Registry, agent coremetadata.Agent, request agentRelaunchRequest) string {
	command := "projmux " + agentRelaunchSpelling + " " + personaAgentRef(registry, agent)
	if request.socket.socket != "" {
		command += " --socket " + personaCommandWord(request.socket.socket)
	}
	if request.socket.socketPath != "" {
		command += " --socket-path " + personaCommandWord(request.socket.socketPath)
	}
	if request.yes {
		command += " --yes"
	}
	return command + relaunchOverrideFlags(request)
}

// relaunchEffortWord is a recorded effort, or "unset" for none.
func relaunchEffortWord(effort string) string {
	if effort == "" {
		return "unset"
	}
	return effort
}

// describeRelaunchTarget is the launch options a relaunch ends with: the
// requested effort, or the recorded one it carries on, and the model when one
// was given.
func describeRelaunchTarget(result agentRelaunchResult) string {
	effort := result.NewEffort
	if effort == "" {
		effort = result.CurrentEffort
	}
	description := "effort=" + relaunchEffortWord(effort)
	if result.NewModel != "" {
		description += " model=" + result.NewModel
	}
	return description
}

// writeAgentRelaunchResult prints one result as JSON or as one line.
func writeAgentRelaunchResult(stdout io.Writer, request agentRelaunchRequest, result agentRelaunchResult) error {
	if request.json {
		return json.NewEncoder(stdout).Encode(result)
	}
	current := "effort=" + relaunchEffortWord(result.CurrentEffort)
	switch result.Outcome {
	case personaOutcomeUnchanged:
		_, err := fmt.Fprintf(stdout, "agent/%s unchanged: already running with %s\n", result.AgentName, current)
		return err
	case personaOutcomeWouldRestart, personaOutcomeWouldResume:
		_, err := fmt.Fprintf(stdout,
			"%s: agent/%s uid=%s phase=%s interaction=%s from %s to %s; would %s it on the same conversation; confirmation-required=%t\ndry-run: nothing was changed\n",
			agentRelaunchSpelling, result.AgentName, result.AgentUID, result.Phase, result.Interaction, current, describeRelaunchTarget(result),
			strings.TrimPrefix(result.Outcome, "would-"), result.ConfirmationRequired)
		return err
	default:
		_, err := fmt.Fprintf(stdout, "agent/%s relaunched from %s to %s; %s on the same conversation\n",
			result.AgentName, current, describeRelaunchTarget(result), result.Outcome)
		return err
	}
}
