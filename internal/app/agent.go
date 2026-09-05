package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// agentSubcommands is projected from the authoritative provider action catalog.
// capabilities is the metadata query itself, not a provider action cell.
var agentSubcommands = append(aiprovider.AgentGroups(), "capabilities")

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
// `status` and `topic` are Registry-owned exact-Agent workflows. `integrate`
// and `usage` forward to their existing handlers untouched; provider account
// quota is deliberately not an addressable `usage` resource: there is no
// `get usage`, only this read-only Agent-domain workflow.
//
// `resume` is the one route with logic of its own, because it is the only way
// an existing Agent is ever reused: `create agent` always mints a new uid.
type agentCommand struct {
	ai               rawArgvCommand
	usage            rawArgvCommand
	loadRegistry     func() (coremetadata.Registry, error)
	store            *resourceStore
	activeTarget     activeTargetLookup
	mirror           agentMutationMirror
	now              func() time.Time
	resolveWorkspace func(string, coremetadata.Registry, coremetadata.Project, string, string, []string) (coremetadata.AgentWorkspace, error)
	// rebind materializes the new managed Pane of a resumed Agent. It is the
	// only part of this namespace that mutates the registry, and it is held as
	// its own seam so the read-only resolution and phase gate above it stay
	// testable without a runtime.
	rebind         *agentRebinder
	reviews        agentReviewStarter
	reviewBinding  agentReviewBindingLookup
	reviewTimeout  time.Duration
	controlBinding agentControlBindingLookup
	controlRoute   func(context.Context) (runtimeMutationRoute, error)
	controlRunner  tmuxCommandRunner
	controlCall    agentControlCaller
	controlPaths   agentControlPathResolver
	controlPicker  agentControlPicker
	controlTimeout time.Duration
	focus          rawArgvCommand
	codexUpgrade   rawArgvCommand
	codexHandover  rawArgvCommand
	handover       codexDrainingHandoverRequester
}

type codexDrainingHandoverRequester interface {
	RequestHandover(context.Context, coremetadata.CodexEndpointRef) (string, bool, error)
}

func newAgentCommand() *agentCommand {
	return &agentCommand{
		loadRegistry:     loadResourceRegistry,
		store:            newResourceStore(),
		activeTarget:     defaultActiveTargetLookup(),
		mirror:           defaultAgentMutationMirror(),
		now:              time.Now,
		resolveWorkspace: resolveAgentWorkspaceFor,
		reviews:          defaultCodexReviewStarter{},
		reviewBinding:    defaultAgentReviewBindingLookup(),
		reviewTimeout:    25 * time.Second,
		controlRoute: func(ctx context.Context) (runtimeMutationRoute, error) {
			return resolveExactObjectRuntimeMutationRoute(ctx, inttmux.ExecRunner{}, os.Getenv)
		},
		controlRunner:  inttmux.ExecRunner{},
		controlCall:    callCodexControl,
		controlPaths:   config.DefaultPathsFromEnv,
		controlPicker:  intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		controlTimeout: 10 * time.Second,
	}
}

func (c *agentCommand) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}

// Run dispatches one `agent <subcommand>` invocation.
func (c *agentCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("agent requires a subcommand: %s", strings.Join(agentSubcommands, ", ")))
	}
	rest := args[1:]
	switch args[0] {
	case "status":
		return c.runStatus(rest, stdout, stderr)
	case "topic":
		return c.runTopic(rest, stdout, stderr)
	case "integrate":
		return forwardRawArgv(c.ai, "agent integrate", "ai", []string{"integrate"}, rest, stdout, stderr)
	case "usage":
		// No prefix: the canonical spelling accepts exactly the argv the
		// current `usage` route accepts, which is what keeps the two spellings
		// byte-identical.
		return forwardRawArgv(c.usage, "agent usage", "usage", nil, rest, stdout, stderr)
	case "resume":
		return c.runResume(rest, stdout, stderr)
	case "turn":
		return c.runTurn(rest, stdout, stderr)
	case "approval":
		return c.runApproval(rest, stdout, stderr)
	case "review":
		return c.runReview(rest, stdout, stderr)
	case "app-server":
		if len(rest) == 0 {
			return usageError("agent app-server requires upgrade or handover")
		}
		switch rest[0] {
		case "upgrade":
			return forwardRawArgv(c.codexUpgrade, "agent app-server upgrade", "agent app-server upgrade", nil, rest[1:], stdout, stderr)
		case "handover":
			return forwardRawArgv(c.codexHandover, "agent app-server handover", "agent app-server handover", nil, rest[1:], stdout, stderr)
		default:
			return usageError("agent app-server requires upgrade or handover")
		}
	case "capabilities":
		return c.runCapabilities(rest, stdout, stderr)
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
// The rebind itself reuses the existing Agent. `create agent` always mints a new
// uid; this route never calls CreateAgent at all, so a resumed Agent keeps its
// uid and its metadata.name by construction rather than by care. The two verbs
// are deliberately not merged: a single verb that mints an identity or reuses
// one depending on runtime state has an unpredictable cardinality, and its worst
// failure mode -- a resume that cannot find its conversation quietly becoming a
// fresh conversation -- is exactly the context loss the separation prevents.
//
// Every refusal below happens against a read-only registry snapshot, so a failed
// resume opens no transaction, creates no tmux object, and starts no
// conversation of any kind.
func (c *agentCommand) runResume(args []string, stdout, stderr io.Writer) error {
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
	if operationRef, endpoint, draining := drainingCodexResumeRequest(agent); draining {
		// A Draining/HandoverPending generation cannot take the normal rebind
		// path. That would silently resume on the old endpoint and bypass the
		// generation-wide Phase 5 handover gate. An unconfigured requester is
		// therefore a fail-closed refusal with zero provider/Registry/tmux
		// effects, not permission to reattach.
		if c.handover == nil {
			return fmt.Errorf("%s: %s operation=%s: handover requester is not configured", spelling, codexNativeReasonHandoverRequired, operationRef)
		}
		ctx, cancel := context.WithTimeout(context.Background(), codexNativeThreadTimeout)
		defer cancel()
		requested, _, requestErr := c.handover.RequestHandover(ctx, endpoint)
		if requestErr != nil {
			return requestErr
		}
		if requested == "" {
			requested = operationRef
		}
		return fmt.Errorf("%s: %s operation=%s", spelling, codexNativeReasonHandoverRequired, requested)
	}
	plan, err := planAgentResume(spelling, registry, agent)
	if err != nil {
		return err
	}
	project, ok := registry.Project(plan.projectUID)
	if !ok {
		return fmt.Errorf("%s: owning Project %q disappeared", spelling, plan.projectUID)
	}
	resolver := c.resolveWorkspace
	if resolver == nil {
		resolver = resolveAgentWorkspaceFor
	}
	plan.workspace, err = resolver(spelling, registry, *project, plan.provider, plan.workspace.CWD, plan.workspace.AdditionalWritableRoots)
	if err != nil {
		return err
	}
	return c.rebind.rebind(spelling, plan, stdout, stderr)
}

func drainingCodexResumeRequest(agent *coremetadata.Agent) (string, coremetadata.CodexEndpointRef, bool) {
	if agent == nil || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Valid() ||
		agent.Status.SessionRef.Codex.Lifecycle == nil {
		return "", coremetadata.CodexEndpointRef{}, false
	}
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	lifecycle := agent.Status.SessionRef.Codex.Lifecycle
	if !lifecycle.ValidFor(&endpoint) || (lifecycle.State != coremetadata.CodexGenerationDraining && lifecycle.State != coremetadata.CodexGenerationHandoverPending) {
		return "", coremetadata.CodexEndpointRef{}, false
	}
	return lifecycle.Operation.ID, endpoint, true
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
