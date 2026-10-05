package app

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
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
//
// CurrentSettings and NewSettings are the Agent's layered settings as it
// recorded them and as the relaunch runs them (agentsettings.Resolve), and
// RelaunchReasons are why the two differ, empty when they do not. They come
// after every older field, which keep their names and meaning.
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
	CurrentSettings      agentsettings.Settings            `json:"currentSettings"`
	NewSettings          agentsettings.Settings            `json:"newSettings"`
	CurrentHost          string                            `json:"currentHost"`
	TargetHost           string                            `json:"targetHost"`
	RelaunchReasons      []string                          `json:"relaunchReasons"`
}

// agentRelaunchRequest is one parsed `agent relaunch` argv.
type agentRelaunchRequest struct {
	agentRef string
	host     string
	prompt   []string
	model    string
	effort   string
	// profile and instructions are nil when not given; a pointer to "" is
	// `none`.
	profile      *string
	instructions *string
	reset        []string
	flags        resourceQueryFlags
	yes          bool
	dryRun       bool
	json         bool
	socket       deleteSocketFlags
}

// settings is what the relaunch asks of the Agent's layers.
func (request agentRelaunchRequest) settings() agentSettingsRequest {
	return agentSettingsRequest{
		model: request.model, effort: request.effort, instructions: request.instructions,
		profile: request.profile, reset: request.reset, source: coremetadata.SettingSourceRelaunch,
	}
}

// relaunchTokens are the refusal tokens of `agent relaunch`.
var relaunchTokens = agentRestartTokens{
	busy: relaunchReasonAgentBusy, selfTarget: relaunchReasonSelfTarget, noConversation: relaunchReasonNoConversation,
	phase: func(phase coremetadata.AgentPhase) string {
		return fmt.Sprintf("is %s; only a %s, %s, or %s Agent is relaunched",
			phase, coremetadata.PhaseRunning, coremetadata.PhaseOffline, coremetadata.PhaseFailed)
	},
}

// runRelaunch restarts one existing Claude or Codex Agent on the same uid and
// the same provider conversation with other settings: another profile
// (--profile, or none), other instructions (--instructions, or none), another
// model or effort, or an override removed so the item follows the profile
// again (--reset). Without any of them it restarts the Agent with the
// settings its layers resolve to now: its overrides, then its profile as it
// is now. A profile switch clears every override; only the overrides given
// with it are overrides again. All of it is one restart.
//
// A Running Agent whose layers resolve to what it runs already, whose agent
// guidance and Project label link rules are the ones it recorded, and that is
// not asked to change them, is left alone (`unchanged`); otherwise the
// relaunch restarts it and says why (relaunchReasons). A --model always
// restarts.
//
// It is the agentRestart every restarting command shares: a Running Agent's
// managed Pane is closed through `delete pane`, which leaves the Agent
// Offline, and the Agent is brought back through the `agent resume` rebinder,
// which keeps its uid and its conversation. An Offline or Failed Agent is only
// resumed.
//
// Nothing is written before the stop. The settings are recorded by the rebind
// transaction, the way `agent resume --model/--effort` records them, so a
// launch that fails records none of them. Every refusal happens before any
// change and leaves no trace.
//
// A stop that reports an error is not taken at its word, because `delete
// pane` can fail after the Pane is already gone: the Registry and the exact
// tmux server are observed again. A Pane still alive keeps running the old
// session and nothing changed, so the run fails. A Pane already closed goes on
// to the resume, with a warning. When liveness cannot be observed the run
// fails and prints the command to re-run. A resume that fails after the stop
// leaves the Agent Offline with its previous settings and prints the command,
// overrides included, that finishes the job.
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
	return c.dispatchRelaunch(registry, target, request, stdout, stderr)
}

func (c *agentCommand) dispatchRelaunch(registry coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	// An offline source still has a tmux recipe. Its pending host-transfer
	// journal must take precedence over the ordinary same-host relaunch route.
	if target.Spec.Provider == aiModeCodex && c.store != nil && c.store.stateDir != nil {
		path, err := c.codexHostTransferPath(target.Metadata.UID)
		if err != nil {
			return err
		}
		pending, err := readCodexHostTransfer(path)
		if err != nil {
			return err
		}
		if pending != nil {
			return c.runCodexHostRelaunch(registry, target, request, stdout, stderr)
		}
	}
	current := relaunchCurrentHost(registry, target)
	if request.host != "" && request.host != current {
		return c.runHostRelaunch(registry, target, request, stdout, stderr)
	}
	if pane, ambiguous := processResumePane(registry, target.Metadata.UID); pane != nil && !ambiguous && (target.Spec.Provider == aiModeClaude || target.Spec.Provider == aiModeCodex) {
		return c.runProcessRelaunch(registry, target, *pane, request, stdout, stderr)
	}
	if _, handled, err := c.processRuntime.admit(registry, target.Status.PaneRef, resourcegraph.ProcessRelaunch); handled {
		return err
	}
	if len(request.prompt) > 0 {
		return usageError("agent relaunch: a first prompt applies only to process agents; nothing was changed")
	}
	return c.runTmuxRelaunch(registry, target, request, stdout, stderr)
}

func (c *agentCommand) runTmuxRelaunch(registry coremetadata.Registry, target coremetadata.Agent, request agentRelaunchRequest, stdout, stderr io.Writer) error {
	const spelling = agentRelaunchSpelling
	refuse := func(reason, detail string) error {
		return usageError(fmt.Sprintf("%s: agent/%s %s (%s); nothing was changed", spelling, target.Metadata.Name, detail, reason))
	}

	// Validation. Provider first, because a model or an effort only means
	// something for a Claude or Codex Agent.
	provider := coremetadata.NormalizeProvider(target.Spec.Provider)
	if provider == "" && target.Status.SessionRef != nil {
		provider = coremetadata.NormalizeProvider(target.Status.SessionRef.Provider)
	}
	restart := c.newAgentRestart(spelling, registry, target, provider, relaunchTokens, refuse)
	restart.comparesPromptParts = true
	if provider != aiModeClaude && provider != aiModeCodex {
		return refuse(relaunchReasonProviderUnsupported, fmt.Sprintf("is a %q Agent; --model and --effort apply only to --provider %s or %s", target.Spec.Provider, aiModeClaude, aiModeCodex))
	}
	if err := requireLaunchOptions(spelling, provider, request.model, request.effort, false, "nothing was changed"); err != nil {
		return err
	}
	if err := restart.checkTarget(); err != nil {
		return err
	}
	if err := c.plan(restart, request.settings(), request.socket); err != nil {
		return err
	}
	settings := restart.settings.resolution
	running := restart.running
	result := agentRelaunchResult{
		Action: "relaunch", DryRun: request.dryRun, CurrentHost: "tmux", TargetHost: "tmux",
		AgentUID: target.Metadata.UID, AgentName: target.Metadata.Name, Provider: provider,
		Phase: target.Status.Phase, Interaction: restart.interaction, PaneUID: restart.paneUID,
		CurrentEffort: target.Metadata.Annotations[coremetadata.AnnotationAgentEffort], CurrentModel: target.Metadata.Annotations[coremetadata.AnnotationAgentModel],
		NewEffort: request.effort, NewModel: request.model,
		Restart:              running,
		ConfirmationRequired: restart.confirmationRequired(),
		CurrentSettings:      settings.Current,
		NewSettings:          settings.New,
		RelaunchReasons:      append([]string{}, settings.Reasons...),
	}
	// A Running Agent whose layers resolve to exactly what it was launched
	// with, and that is not asked to change them, has nothing to gain from a
	// restart. A --model always restarts: the recorded model is the last one
	// requested, not necessarily the one the provider runs now (a `/model`
	// switch inside the session is not observed), so matching the record
	// cannot prove nothing would change.
	changesLayers := settings.ProfileSwitched || request.instructions != nil || len(request.reset) > 0
	if running && request.model == "" && len(settings.Reasons) == 0 && !(changesLayers && settings.LayersChanged()) {
		result.Outcome, result.Unchanged, result.Restart, result.ConfirmationRequired = personaOutcomeUnchanged, true, false, false
		result.NewPaneUID = restart.paneUID
		return writeAgentRelaunchResult(stdout, request, result)
	}
	if request.dryRun {
		result.Outcome = personaOutcomeWouldResume
		if running {
			result.Outcome = personaOutcomeWouldRestart
		}
		return writeAgentRelaunchResult(stdout, request, result)
	}
	var beforeStop func() error
	if running && provider == aiModeCodex {
		beforeStop = func() error { return c.refuseCodexSettingsUnsupported(restart) }
	}
	newPane, err := c.run(restart, agentRestartSteps{
		yes: request.yes, socket: request.socket, json: request.json,
		beforeStop: beforeStop,
		stopFailed: func(err error, liveness personaPaneLiveness, observeErr error) error {
			// Nothing was written before the stop, so there is nothing to
			// restore either way.
			switch liveness {
			case personaPaneClosed:
				fmt.Fprintf(stderr, "projmux: warning: closing agent/%s's managed pane %s reported an error, but that pane is already closed, so the Agent is resumed with the new launch options: %v\n",
					target.Metadata.Name, restart.paneUID, err)
				return nil
			case personaPaneUnknown:
				fmt.Fprintf(stderr, "projmux: could not observe whether agent/%s's managed pane %s is still alive (%v); nothing was recorded, and re-running the same command recovers: %s\n",
					target.Metadata.Name, restart.paneUID, observeErr, relaunchRerunCommand(registry, target, request))
				return fmt.Errorf("%s: closing agent/%s's managed pane failed and whether it is still alive could not be observed: %w",
					spelling, target.Metadata.Name, err)
			default:
				return fmt.Errorf("%s: closing agent/%s's managed pane failed, so it keeps running with its previous launch options: %w",
					spelling, target.Metadata.Name, err)
			}
		},
		resume: request.settings(),
		resumeFailed: func(err error) error {
			fmt.Fprintf(stderr, "projmux: agent/%s did not resume with %s: %v\n", target.Metadata.Name, describeRelaunchTarget(result), err)
			if codexThreadSettingsNotApplied(err) {
				// The same settings would fail the same way, so the recovery
				// is a resume with the previous ones.
				fmt.Fprintf(stderr, "projmux: recover with: %s\n", personaRecoveryCommand(registry, target))
				return fmt.Errorf("%s: agent/%s is %s with its previous settings because its Codex thread did not take the new ones, and needs `agent resume`: %w",
					spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
			}
			fmt.Fprintf(stderr, "projmux: recover with: %s\n", relaunchRecoveryCommand(registry, target, request))
			if request.changesLayers() {
				return fmt.Errorf("%s: agent/%s is %s with its previous settings and needs `agent relaunch`: %w",
					spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
			}
			return fmt.Errorf("%s: agent/%s is %s with its previous effort and needs `agent resume`: %w",
				spelling, target.Metadata.Name, coremetadata.PhaseOffline, err)
		},
	}, stdout, stderr)
	if err != nil {
		return err
	}
	result.Outcome = personaOutcomeResumed
	if running {
		result.Outcome = personaOutcomeRestarted
	}
	result.NewPaneUID = newPane
	return writeAgentRelaunchResult(stdout, request, result)
}

// changesLayers reports a request with --profile, --instructions or --reset,
// which `agent resume` cannot carry.
func (request agentRelaunchRequest) changesLayers() bool {
	return request.settings().changesLayers()
}

// preflightCodexRelaunch runs the read-only checks the Codex rebind makes
// before it asks the daemon anything: the recorded profile is still valid,
// and the stored thread has a durable endpoint a native resume can use. A
// Running Codex Agent is stopped only when these pass. The daemon itself is
// asked only by the resume, so a daemon down after the stop still leaves the
// Agent Offline with the recovery command.
//
// annotations are the ones the resume launches with: a profile switch names
// the new profile there.
func (c *agentCommand) preflightCodexRelaunch(plan agentResumePlan, annotations map[string]string) error {
	if c.rebind == nil || c.rebind.create == nil {
		return errors.New("the resume materialization seam is not configured")
	}
	if _, _, err := c.rebind.create.codexResumeProfile(annotations); err != nil {
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
	fs.Func("profile", "profile the Agent runs with from now on, or none; switching clears every override but the ones given here", func(value string) error {
		name, err := relaunchNameFlag("--profile", value, profile.ValidateName)
		request.profile = &name
		return err
	})
	fs.Func("instructions", "named instructions the Agent runs with from now on, or none (claude)", func(value string) error {
		name, err := relaunchNameFlag("--instructions", value, persona.ValidateName)
		request.instructions = &name
		return err
	})
	fs.Func("host", "execution host: tmux or process; omitted keeps the current host", func(value string) error {
		if value != "tmux" && value != "process" {
			return fmt.Errorf("--host must be tmux or process; nothing was changed")
		}
		request.host = value
		return nil
	})
	fs.StringVar(&request.model, "model", "", "claude or codex: model name the relaunch runs; recorded on the Agent")
	fs.StringVar(&request.effort, "effort", "", "claude or codex: effort level, recorded on the Agent: "+strings.Join(claudeEffortLevels, "|"))
	fs.Func("reset", "remove the override of these items so they follow the profile again: "+strings.Join(agentsettings.Items(), ",")+", or all", func(value string) error {
		items, err := parseRelaunchReset(value)
		request.reset = items
		return err
	})
	fs.BoolVar(&request.yes, "yes", false, "restart the Agent even when its interaction shows a turn in progress or unknown")
	fs.BoolVar(&request.dryRun, "dry-run", false, "report the target, its interaction, and the model and effort change without changing anything")
	fs.StringVar(&request.socket.socket, "socket", "", "exact tmux socket name (tmux -L) the managed Pane of a Running Agent is closed on")
	fs.StringVar(&request.socket.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S) the managed Pane of a Running Agent is closed on")
	var output string
	fs.StringVar(&output, "output", "", "result projection: json")
	fs.StringVar(&output, "o", "", "result projection: json (alias of --output)")
	for i, arg := range args {
		if arg == "--" && processResumeHasReference(fs, args[:i]) {
			request.prompt = slices.Clone(args[i+1:])
			args = args[:i]
			break
		}
	}
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
	given := []bool{request.instructions != nil, request.model != "", request.effort != ""}
	for i, item := range agentsettings.Items() {
		if given[i] && slices.Contains(request.reset, item) {
			return agentRelaunchRequest{}, usageError(fmt.Sprintf("%s: --%s sets the %s and --reset removes its override; give one of them; nothing was changed", spelling, item, item))
		}
	}
	request.json = output == "json"
	request.agentRef = positionals[0]
	request.flags.addPositionalRef(request.agentRef)
	return request, nil
}

// relaunchNameFlag reads a --profile or --instructions value: a name, or
// `none` for none ("").
func relaunchNameFlag(flagName, value string, validate func(string) error) (string, error) {
	if value == relaunchNone {
		return "", nil
	}
	if err := validate(value); err != nil {
		return "", fmt.Errorf("%s: %v; nothing was changed", flagName, err)
	}
	return value, nil
}

// relaunchNone is the --profile and --instructions value for none.
const relaunchNone = "none"

// parseRelaunchReset reads a --reset value: a comma-separated list of items,
// or `all`, in the order agentsettings.Items declares them.
func parseRelaunchReset(value string) ([]string, error) {
	if value == "all" {
		return agentsettings.Items(), nil
	}
	var items []string
	for item := range strings.SplitSeq(value, ",") {
		item = strings.TrimSpace(item)
		if !slices.Contains(agentsettings.Items(), item) {
			return nil, fmt.Errorf("--reset %q is not an item; want %s, or all; nothing was changed", item, strings.Join(agentsettings.Items(), ","))
		}
		items = append(items, item)
	}
	return slices.DeleteFunc(agentsettings.Items(), func(item string) bool { return !slices.Contains(items, item) }), nil
}

// relaunchOverrideFlags spells the settings the request was given, in the
// order the usage line declares them.
func relaunchOverrideFlags(request agentRelaunchRequest) string {
	var flags string
	if request.host != "" {
		flags += " --host " + request.host
	}
	if request.profile != nil {
		flags += " --profile " + personaCommandWord(cmp.Or(*request.profile, relaunchNone))
	}
	if request.instructions != nil {
		flags += " --instructions " + personaCommandWord(cmp.Or(*request.instructions, relaunchNone))
	}
	if request.model != "" {
		flags += " --model " + personaCommandWord(request.model)
	}
	if request.effort != "" {
		flags += " --effort " + personaCommandWord(request.effort)
	}
	if len(request.reset) > 0 {
		flags += " --reset " + strings.Join(request.reset, ",")
	}
	return flags
}

// relaunchRecoveryCommand is the invocation, overrides included, that
// finishes a relaunch whose resume failed: `agent resume` for a model or an
// effort, and the same `agent relaunch` for a change `agent resume` cannot
// carry (the Agent is Offline, so it only resumes).
func relaunchRecoveryCommand(registry coremetadata.Registry, agent coremetadata.Agent, request agentRelaunchRequest) string {
	if request.changesLayers() {
		return "projmux " + agentRelaunchSpelling + " " + personaAgentRef(registry, agent) + relaunchOverrideFlags(request)
	}
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
// requested effort, or the one its layers resolve to, and the model when one
// was given or its layers pass one.
func describeRelaunchTarget(result agentRelaunchResult) string {
	effort := cmp.Or(result.NewEffort, result.NewSettings.Effort.Value)
	if result.NewEffort == "" && !slices.Contains(result.RelaunchReasons, agentsettings.ReasonEffortChanged) {
		effort = result.CurrentEffort
	}
	description := "effort=" + relaunchEffortWord(effort)
	if model := result.NewModel; model != "" {
		description += " model=" + model
	} else if slices.Contains(result.RelaunchReasons, agentsettings.ReasonModelChanged) {
		description += " model=" + result.NewSettings.Model.Value
	}
	return description + describeRelaunchNames(result, result.NewSettings)
}

// describeRelaunchNames names the profile and the instructions on one side of
// a relaunch that changes their names, "" when it changes neither.
func describeRelaunchNames(result agentRelaunchResult, side agentsettings.Settings) string {
	var names string
	if result.NewSettings.Profile.Name != result.CurrentSettings.Profile.Name {
		names += " profile=" + cmp.Or(side.Profile.Name, relaunchNone)
	}
	if result.NewSettings.Instructions.Value != result.CurrentSettings.Instructions.Value {
		names += " instructions=" + cmp.Or(side.Instructions.Value, relaunchNone)
	}
	return names
}

// writeAgentRelaunchResult prints one result as JSON or as one line.
func writeAgentRelaunchResult(stdout io.Writer, request agentRelaunchRequest, result agentRelaunchResult) error {
	if request.json {
		return json.NewEncoder(stdout).Encode(result)
	}
	current := "effort=" + relaunchEffortWord(result.CurrentEffort) + describeRelaunchNames(result, result.CurrentSettings)
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
