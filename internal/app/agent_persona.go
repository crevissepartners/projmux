package app

import (
	"context"
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
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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

// agentPersonaRefusal is the typed cause an `agent persona` usage error carries
// for a reason token this package owns. It changes nothing the CLI prints: the
// message is the UsageError's. It lets an in-process caller
// read the token with errors.As instead of from the text, as *persona.Error
// does for the persona package's own tokens.
type agentPersonaRefusal struct {
	Reason string
}

func (e *agentPersonaRefusal) Error() string { return e.Reason }

// agentPersonaRefusalCause is the typed cause of a refusal with reason: the
// persona package's own *persona.Error for its tokens, agentPersonaRefusal
// for this package's.
func agentPersonaRefusalCause(reason, name, detail string) error {
	switch reason {
	case personaReasonAgentBusy, personaReasonSelfTarget, personaReasonNoConversation:
		return &agentPersonaRefusal{Reason: reason}
	}
	return &persona.Error{Reason: reason, Name: name, Detail: detail}
}

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
type agentPersonaResult = mutationruntime.AgentPersonaResult

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
// It is `agent relaunch --instructions <name>` (detach: `--instructions
// none`), recorded with the source attach, with its own output: the same
// agentRestart plans and runs it, so the settings come from the same layers
// and the stop and the resume are the same. A Running Agent's managed Pane is
// closed through `delete pane`, which leaves the Agent Offline, and the Agent
// is brought back through the `agent resume` rebinder, which keeps its uid and
// its conversation. What differs is when the instructions are recorded:
// between the checks and the stop the command writes the persona snapshot and
// changes the Agent's persona annotations in one Registry mutation, so the
// resume seam finds the new persona the same way every later resume will.
//
// Every refusal happens before the snapshot is written and leaves no trace.
// A resume that fails after the stop leaves the Agent Offline with the new
// annotations and prints the `agent resume` command that finishes the job.
//
// A stop that reports an error is not taken at its word, because `delete
// pane` can fail after the Pane is already gone: the Registry and the exact
// tmux server are observed again. A Pane still alive keeps running the old
// session, so the previous annotations are restored. A Pane already closed
// keeps the new annotations and the run goes on to the resume, with a warning.
// When liveness cannot be observed the previous annotations are restored and
// the command to re-run is printed.
func (c *agentCommand) runPersona(args []string, noun string, stdout, stderr io.Writer) error {
	request, err := parseAgentPersonaArgs(args, noun, stderr)
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
	forward := stdout
	if request.json {
		forward = io.Discard
	}
	var restart *agentRestart
	result, err := mutationruntime.RunAgentPersona(mutationruntime.AgentPersonaDependencies{
		Request:  mutationruntime.AgentPersonaRequest{Action: request.action, Spelling: request.spelling, Noun: noun, Persona: request.persona, DryRun: request.dryRun, Confirm: request.yes, Socket: request.socket.socket, SocketPath: request.socket.socketPath},
		Registry: registry, Target: target, Interaction: target.EffectiveInteraction(c.clock()).Kind,
		OpenStore: c.openPersonaStore,
		Refuse: func(reason, message string, cause error) error {
			if cause == nil {
				cause = agentPersonaRefusalCause(reason, request.persona, message)
			}
			return usageErrorFor(message, cause)
		},
		Plan: func(p *mutationruntime.AgentRestartPlan, q mutationruntime.AgentSettingsRequest, refuse func(string, string) error) error {
			restart = c.newAgentRestart(request.spelling, registry, target, p.Provider, personaTokens(noun), refuse)
			restart.running, restart.paneUID, restart.paneName = p.Running, p.PaneUID, p.PaneName
			err := c.plan(restart, localAgentSettingsRequest(q), request.socket)
			*p = restart.sharedRestart()
			return err
		},
		SetPersona: func(a coremetadata.Agent, want coremetadata.AgentPersonaAnnotations) error {
			return c.setAgentPersona(request.spelling, a, want)
		},
		Execution: mutationruntime.AgentRestartExecution{
			ValidateTransport: func() error { _, err := resolveDeleteTarget(request.spelling, request.socket, c.lookupEnv); return err },
			Stop:              func() error { return c.stopAgentPane(request.socket, restart.paneUID, forward, stderr) },
			Observe: func() (mutationruntime.AgentRestartLiveness, error) {
				v, e := c.observeStoppedAgentPane(request.spelling, request.socket, target.Metadata.UID, restart.paneUID)
				return mutationruntime.AgentRestartLiveness(v), e
			},
			Resume: func(p mutationruntime.StoppedAgentPane) error {
				return c.resumeStoppedAgent(target.Metadata.UID, stoppedAgentPane{uid: p.UID, name: p.Name}, agentSettingsRequest{}, forward, stderr)
			},
			NewPane: func() string {
				if reg, e := c.loadRegistry(); e == nil {
					if a, ok := reg.Agent(target.Metadata.UID); ok {
						return a.Status.PaneRef
					}
				}
				return ""
			},
		},
	})
	for _, notice := range result.Notices {
		fmt.Fprintln(stderr, notice)
	}
	if err != nil {
		return err
	}
	return writeAgentPersonaResult(stdout, request, result)
}

// personaRefusalNoun keeps the old noun in a persona refusal reached through
// `agent persona`, the way the rest of that spelling's text does.
func personaRefusalNoun(err error, noun string) error {
	if noun == persona.DeprecatedNoun {
		return persona.SpelledAs(err, persona.DeprecatedNoun)
	}
	return err
}

// personaTokens are the refusal tokens of `agent instructions|persona
// attach|detach`.
func personaTokens(noun string) agentRestartTokens {
	v := mutationruntime.PersonaTokens(noun)
	return agentRestartTokens{busy: v.Busy, selfTarget: v.SelfTarget, noConversation: v.NoConversation, phase: v.Phase}
}

// parseAgentPersonaArgs parses `attach <agent-ref> <persona>` and
// `detach <agent-ref>` with their flags. The Agent reference is required:
// this is a restart command, so it never falls back to the active Pane.
func parseAgentPersonaArgs(args []string, noun string, stderr io.Writer) (agentPersonaRequest, error) {
	if len(args) == 0 || (args[0] != "attach" && args[0] != "detach") {
		return agentPersonaRequest{}, usageError("agent " + noun + " requires attach or detach")
	}
	request := agentPersonaRequest{action: args[0], spelling: "agent " + noun + " " + args[0]}
	fs := flag.NewFlagSet(request.spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	request.flags = resourceQueryFlags{kind: coremetadata.KindAgent}
	request.flags.register(fs)
	fs.BoolVar(&request.yes, "yes", false, "restart the Agent even when its interaction shows a turn in progress or unknown")
	fs.BoolVar(&request.dryRun, "dry-run", false, "report the target, its interaction, and the "+noun+" change without changing anything")
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
		return agentPersonaRequest{}, flagParseError(err)
	}
	want := 2
	shape := "<agent-ref> <persona>"
	if noun == "instructions" {
		shape = "<agent-ref> <name>"
	}
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
			return agentPersonaRequest{}, usageErrorFor(fmt.Sprintf("%s: %v; nothing was changed", request.spelling, personaRefusalNoun(err, noun)), err)
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

// predictStoppedAgentResume plans the final resume of a restart against the
// registry the stop would leave: the Running Agent's managed Pane deleted by
// the same mutator `delete pane` runs. Nothing is written.
func (c *agentCommand) predictStoppedAgentResume(registry coremetadata.Registry, agent coremetadata.Agent, paneUID string) (agentResumePlan, error) {
	predicted := registry.Clone()
	if paneUID != "" {
		if c.store == nil || c.store.mutator == nil {
			return agentResumePlan{}, errors.New("the registry mutator is not configured")
		}
		if err := c.store.mutator().DeletePane(&predicted, paneUID); err != nil {
			return agentResumePlan{}, err
		}
	}
	stopped, ok := predicted.Agent(agent.Metadata.UID)
	if !ok {
		return agentResumePlan{}, fmt.Errorf("agent %q disappeared", agent.Metadata.UID)
	}
	return c.prepareResume("agent resume", predicted, stopped)
}

// setAgentPersona writes want onto the Agent in one Registry mutation, and
// only while the Agent is still in the phase and Pane this run planned for.
func (c *agentCommand) setAgentPersona(spelling string, planned coremetadata.Agent, want coremetadata.AgentPersonaAnnotations) error {
	return c.mutateAgent(planned.Metadata.UID, func(reg *coremetadata.Registry, mut coremetadata.Mutator) error {
		return mutationruntime.SetPlannedAgentPersona(spelling, reg, mut, planned, want)
	})
}

// stopAgentPane closes the Agent's managed Pane through `delete pane`, which
// leaves the Agent Offline. The socket flags are passed through unchanged, so
// the live half addresses exactly the server `delete pane` would.
func (c *agentCommand) stopAgentPane(socket deleteSocketFlags, paneUID string, stdout, stderr io.Writer) error {
	if c.paneDelete == nil {
		return errors.New("the managed Pane delete route is not configured")
	}
	args := []string{"pane", selector.UIDPrefix + paneUID, "--yes"}
	if socket.socket != "" {
		args = append(args, "--socket", socket.socket)
	}
	if socket.socketPath != "" {
		args = append(args, "--socket-path", socket.socketPath)
	}
	return c.paneDelete.Run(args, stdout, stderr)
}

// personaPaneLiveness is what a failed stop left of the managed Pane.
type personaPaneLiveness int

const (
	// personaPaneAlive: the Pane still runs the old provider session.
	personaPaneAlive personaPaneLiveness = iota
	// personaPaneClosed: the stop closed the Pane before it failed.
	personaPaneClosed
	// personaPaneUnknown: neither could be observed.
	personaPaneUnknown
)

// observeStoppedAgentPane decides whether the managed Pane a failed stop
// targeted is still alive. The Registry answers first: an Agent no longer
// Running on paneUID, or a Pane uid gone from the Registry, means the stop's
// Registry half committed. Otherwise the exact server `delete pane` addressed
// answers, because the live half can close the Pane before the Registry
// commit fails. The error explains a personaPaneUnknown answer.
func (c *agentCommand) observeStoppedAgentPane(spelling string, socket deleteSocketFlags, agentUID, paneUID string) (personaPaneLiveness, error) {
	registry, err := c.loadRegistry()
	if err != nil {
		return personaPaneUnknown, err
	}
	agent, ok := registry.Agent(agentUID)
	if !ok {
		return personaPaneUnknown, fmt.Errorf("agent %q is no longer in the registry", agentUID)
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID {
		return personaPaneClosed, nil
	}
	if _, ok := registry.Pane(paneUID); !ok {
		return personaPaneClosed, nil
	}
	target, err := resolveDeleteTarget(spelling, socket, c.lookupEnv)
	if err != nil {
		return personaPaneUnknown, err
	}
	live := c.managedPaneLive
	if live == nil {
		live = observeManagedPaneLive
	}
	alive, err := live(target, paneUID)
	switch {
	case err != nil:
		return personaPaneUnknown, err
	case alive:
		return personaPaneAlive, nil
	default:
		return personaPaneClosed, nil
	}
}

// observeManagedPaneLive is the production managedPaneLive: a fresh `delete
// pane` runtime bound to target, read-only.
func observeManagedPaneLive(target tmuxTransport, paneUID string) (bool, error) {
	runtime := newTmuxPaneDeleteRuntime()
	runtime.UseExactTarget(target)
	return managedPaneMirrorLive(context.Background(), runtime, paneUID)
}

// managedPaneMirrorLive reports whether runtime's exact server holds a live
// tmux Pane mirroring paneUID, from the inventory `delete pane` plans its
// kills from. An empty inventory proves nothing about one Pane, the same way
// it authorizes nothing in `delete pane`, so it is an error.
func managedPaneMirrorLive(ctx context.Context, runtime *tmuxPaneDeleteRuntime, paneUID string) (bool, error) {
	rows, err := runtime.Inventory(ctx)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, fmt.Errorf("exact tmux inventory on %s was empty", runtime.Target.Label())
	}
	return slices.ContainsFunc(rows, func(row LivePaneDeleteRow) bool { return row.PaneUID == paneUID }), nil
}

// resumeStoppedAgent brings the stopped Agent back through the `agent resume`
// rebinder, planned from the registry as it is now, with what request asks of
// its layers: the zero request is a plain resume, byte for byte. The model
// and effort are `agent resume --model/--effort` overrides recorded under
// request.source (empty means coremetadata.SettingSourceResume). stopped is
// the managed Pane the restart closed, whose name the new Pane carries.
func (c *agentCommand) resumeStoppedAgent(agentUID string, stopped stoppedAgentPane, request agentSettingsRequest, stdout, stderr io.Writer) error {
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
	plan.modelOverride, plan.effortOverride, plan.overrideSource = request.model, request.effort, request.source
	plan.layerChanges = agentSettingsRequest{profile: request.profile, instructions: request.instructions, reset: request.reset}
	plan.stoppedPane = stopped
	return c.rebind.rebind("agent resume", plan, stdout, stderr)
}

// personaRecoveryCommand is the `agent resume` invocation that finishes an
// attach or detach whose resume failed. It names the Agent by uid inside its
// exact Window and Project, so it resolves to the same Agent from anywhere.
func personaRecoveryCommand(registry coremetadata.Registry, agent coremetadata.Agent) string {
	return mutationruntime.PersonaRecoveryCommand(registry, agent)
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
	return shellQuote(value)
}

func describePersonaAnnotations(annotations coremetadata.AgentPersonaAnnotations, noun string) string {
	if annotations.Persona == "" {
		return "no " + noun
	}
	return fmt.Sprintf("%s %s (%s)", noun, annotations.Persona, annotations.PersonaDigest)
}

// writeAgentPersonaResult prints one result as JSON or as one line.
func writeAgentPersonaResult(stdout io.Writer, request agentPersonaRequest, result agentPersonaResult) error {
	if request.json {
		return json.NewEncoder(stdout).Encode(result)
	}
	noun := "persona"
	if strings.HasPrefix(request.spelling, "agent instructions ") {
		noun = "instructions"
	}
	change := describePersonaAnnotations(coremetadata.AgentPersonaAnnotations{Persona: result.NewPersona, PersonaDigest: result.NewPersonaDigest}, noun)
	switch result.Outcome {
	case personaOutcomeUnchanged:
		_, err := fmt.Fprintf(stdout, "agent/%s unchanged: already running with %s and --system-prompt-snapshot off\n", result.AgentName, change)
		return err
	case personaOutcomeWouldRestart, personaOutcomeWouldResume:
		currentPersona := describePersonaAnnotations(coremetadata.AgentPersonaAnnotations{Persona: result.CurrentPersona, PersonaDigest: result.CurrentPersonaDigest}, noun)
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
		_, err := fmt.Fprintf(stdout, "agent/%s %s %s: now %s; %s on the same conversation\n", result.AgentName, noun, verb, change, result.Outcome)
		return err
	}
}
