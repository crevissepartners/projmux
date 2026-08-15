package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// agentSubcommands lists the Agent domain routes, in help order.
var agentSubcommands = []string{"status", "topic", "resume", "integrate", "usage"}

// resumableAgentPhases is the closed set of phases `agent resume` accepts.
var resumableAgentPhases = []coremetadata.AgentPhase{
	coremetadata.PhaseOffline,
	coremetadata.PhaseFailed,
}

// agentCommand owns the Agent domain namespace.
//
// The split from the shared verbs is the contract's: creating, reading, and
// deleting an Agent are resource operations and stay with `create`, `get`,
// `describe`, and `delete`, while the state, topic, resume, integration, and
// account-usage workflows are Agent-domain verbs that no CRUD shape describes.
//
// `status`, `topic`, and `integrate` forward raw argv to the handler that owns
// them today, and `usage` forwards to the existing usage command untouched, so
// each is a parity alias rather than a second implementation. Provider account
// quota is deliberately not an addressable `usage` resource: there is no
// `get usage`, only this read-only Agent-domain workflow.
//
// `resume` is the one route with logic of its own, because it is the only way
// an existing Agent is ever reused: `create agent` always mints a new uid.
type agentCommand struct {
	ai           rawArgvCommand
	usage        rawArgvCommand
	loadRegistry func() (coremetadata.Registry, error)
}

func newAgentCommand() *agentCommand {
	return &agentCommand{loadRegistry: loadResourceRegistry}
}

// Run dispatches one `agent <subcommand>` invocation.
func (c *agentCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("agent requires a subcommand: %s", strings.Join(agentSubcommands, ", ")))
	}
	rest := args[1:]
	switch args[0] {
	case "status":
		return forwardRawArgv(c.ai, "agent status", "ai", []string{"status"}, rest, stdout, stderr)
	case "topic":
		return forwardRawArgv(c.ai, "agent topic", "ai", []string{"topic"}, rest, stdout, stderr)
	case "integrate":
		return forwardRawArgv(c.ai, "agent integrate", "ai", []string{"integrate"}, rest, stdout, stderr)
	case "usage":
		// No prefix: the canonical spelling accepts exactly the argv the
		// current `usage` route accepts, which is what keeps the two spellings
		// byte-identical.
		return forwardRawArgv(c.usage, "agent usage", "usage", nil, rest, stdout, stderr)
	case "resume":
		return c.runResume(rest, stdout, stderr)
	default:
		return usageError(fmt.Sprintf("agent %s is not available; this release implements: %s",
			args[0], strings.Join(agentSubcommands, ", ")))
	}
}

// runResume resolves and gates one Agent resume.
//
// Two properties are the point of this route:
//
//   - It targets exactly one *existing* Agent through the shared selector
//     engine and the declared <resume, Agent> cardinality cell. An ambiguous or
//     unresolvable reference is refused rather than guessed, because rebinding
//     the wrong conversation is worse than doing nothing.
//   - A Running Agent is refused. Resume is not a navigation verb: it never
//     degrades into focusing the Agent's live pane, so an operator who wanted
//     to look at a running Agent gets an error naming `focus pane` instead of a
//     silent client move.
//
// The rebind itself needs a materialized managed Pane, which the runtime
// materialization Phase owns. Until then a phase-eligible target stops with a
// runtime error and zero mutations: a half-applied Pending transition with no
// way to reach Running would be worse than not starting.
func (c *agentCommand) runResume(args []string, _, stderr io.Writer) error {
	const spelling = "agent resume"

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: coremetadata.KindAgent}
	flags.register(fs)
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if len(refs) == 0 {
		return usageError(spelling + " requires one Agent reference: <name> or uid:<uid>")
	}
	if len(refs) > 1 {
		return usageError(fmt.Sprintf("%s accepts at most one Agent reference; got %q", spelling, refs[1]))
	}
	flags.addPositionalRef(refs[0])

	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbResume, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]
	agent, ok := registry.Agent(match.UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, match.UID)
	}
	if err := requireResumablePhase(spelling, agent); err != nil {
		return err
	}
	return fmt.Errorf("%s %s: binding a new managed Pane to an %s Agent needs runtime materialization, which is not wired yet",
		spelling, agent.Metadata.Name, agent.Status.Phase)
}

// requireResumablePhase enforces the Agent lifecycle gate of resume.
func requireResumablePhase(spelling string, agent *coremetadata.Agent) error {
	if slices.Contains(resumableAgentPhases, agent.Status.Phase) {
		return nil
	}
	if agent.Status.Phase == coremetadata.PhaseRunning {
		return usageError(fmt.Sprintf(
			"%s: agent/%s is %s and already owns a managed Pane; resume only rebinds an %s or %s Agent. To look at it, run `projmux focus pane`",
			spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
	}
	return usageError(fmt.Sprintf("%s: agent/%s is %s; resume only rebinds an %s or %s Agent",
		spelling, agent.Metadata.Name, agent.Status.Phase, coremetadata.PhaseOffline, coremetadata.PhaseFailed))
}
