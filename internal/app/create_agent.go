package app

import (
	"errors"
	"fmt"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// canonicalCreateAgent is the spelling the provider shortcuts normalize onto.
const canonicalCreateAgent = "create agent"

// Provider startup and initial-task acknowledgement are separate bounded
// stages. Exact SessionStart evidence opens the acknowledgement window; it does
// not itself acknowledge the task. Keeping them separate lets a provider spend
// startup time before a delayed hook without turning one larger timeout into
// the contract.
//
// Both bounds were five seconds, which is inside the cost of starting a real
// provider on a busy machine, so a create that had worked would still exit
// nonzero and offer to delete the live Agent. Measured on a 14-core machine
// under `while :; do :; done` at 1x and 3x nproc (observed loadavg 37-72, real
// `claude`, n=20, the product's own stage timestamps):
//
//	stage           p50     p90     observed max
//	startup         9.5s    13.9s   24.4s  (loadavg 69.8)
//	acknowledgement 4.2s     5.2s    8.6s  (loadavg 63.3)
//
// Each bound covers its own peak plus one more step of the observed tail slope,
// max + (max - p90). Twenty samples make a max a weak tail estimate, and this
// tail is heavy: startup jumps 1.75x from p90 to max. Sizing to the sample max
// alone would leave the bound one unlucky run from the behavior this change
// exists to remove.
//
// The asymmetry is the reason to spend the headroom rather than save it. A
// bound that is too low deletes live work; a bound that is too high only delays
// reporting a provider that really is dead, once, on the failure path -- worst
// case 47s for one target and 47s x targets for a fan-out create, which waits
// per target in confirmAgentActivations. The same load that produced these
// peaks also put 19-61s of Registry and tmux work *before* this wait: those
// creates took 26-90s end to end.
//
// The headroom is also what makes the exit code honest. Exceeding a bound is
// reported as a failure only because it is meant to mean "the task never
// arrived" rather than "this run was slower than the sample" -- so the claim
// depends on the margin, not just on the timeout existing.
//
// These are not a sufficiency proof. They cover the measured envelope up to
// loadavg ~72 on this machine class; heavier load is explicitly outside it.
const (
	agentActivationStartupDeadline         = mutationruntime.AgentActivationStartupDeadline
	agentActivationAcknowledgementDeadline = mutationruntime.AgentActivationAcknowledgementDeadline
)

// agentWork is one allocated Agent plus its managed Pane, waiting for the
// runtime phase to give the Pane a live tmux binding.
type agentWork struct {
	target     paneTarget
	windowName string
	agent      coremetadata.Agent
	pane       coremetadata.Pane
	// activation is the generation this Agent launch was issued.
	activation superviseSpec
}

// runResourceAgent answers the canonical resource-backed `create agent` and the
// three provider shortcuts.
//
// It is `create pane` with two substitutions, which is the point: the scope
// resolution, the Window fan-out, the split anchor, the Window ensure, the
// operation ledger and the rollback are the shared ones, not a second
// implementation.
// What differs is the metadata it allocates -- a Window-owned Agent plus the
// Agent-owned managed Pane, rather than a Window-owned shell Pane -- and the
// command the detached split runs, which is the provider launch instead of the
// raw payload.
//
// Three properties are load bearing and are asserted rather than assumed:
//
//   - The Agent is always new. There is no lookup of an existing Agent of the
//     same provider anywhere on this path; rebinding an existing conversation is
//     `agent resume`, which is a different verb with a different cardinality.
//   - The automatic name is the exact minted Agent UID. Only an explicit
//     `--name` can replace it; provider, topic, prompt, and payload cannot.
//   - Nothing moves the client. The split goes through the materializer, which
//     owns `-d`; the focus-following legacy split is not on this path at all.
//
// shortcutProvider is empty for the canonical spelling and carries the provider
// for `create codex|claude|antigravity`.
func (c *createCommand) runResourceAgent(shortcutProvider string, args []string, stdout, stderr io.Writer) error {
	spelling := canonicalCreateAgent
	if shortcutProvider != "" {
		spelling = "create " + shortcutProvider
	}

	shape := resourceCreateShape{split: true, provider: true, host: true}
	flags, err := parseResourceCreateFlags(spelling, args, stderr, shape)
	if err != nil {
		return err
	}
	// Validate the shortcut's provider override before selecting any runtime.
	if shortcutProvider != "" {
		provider, err := c.resolveCreateProvider(spelling, shortcutProvider, flags)
		if err != nil {
			return err
		}
		flags.provider = provider
	}
	if err := c.resolveNewAgentHost(spelling, &flags); err != nil {
		return err
	}
	if flags.host == "process" {
		// The typed process request accepts canonical IDs, not argv spellings.
		if flags.providerSet {
			flags.provider, err = requireCanonicalProvider(spelling, flags.provider)
			if err != nil {
				return err
			}
		}
		return c.runProcessAgentCLI(flags, stdout, stderr)
	}
	provider, err := c.resolveCreateProvider(spelling, shortcutProvider, flags)
	if err != nil {
		return err
	}
	// An argv-only refusal, so it lands before the Settings gate, the scope
	// derivation, and the transaction: `--interactive-only` names a Codex-only
	// lane, and silently ignoring it on another provider would let an operator
	// believe they had opted out of something that was never there.
	if err := requireInteractiveOnlyProvider(spelling, provider, flags); err != nil {
		return err
	}
	if err := requireClaudeLaunchOptions(spelling, provider, flags); err != nil {
		return err
	}
	if err := requirePersonaLane(spelling, provider, flags); err != nil {
		return err
	}
	if err := requireProfileLane(spelling, flags); err != nil {
		return err
	}
	if err := requireClaudeDialogueMode(provider, flags.dialogueReplyOnly, flags.payload); err != nil {
		return err
	}
	// A --cwd or --add-dir value no state can rescue is refused here, before
	// the Registry lock, under the route's own Usage block; the resolver inside
	// the transaction keeps the checks that need the Registry or the filesystem.
	if err := refuseStatelessAgentWorkspace(provider, flags.cwd, flags.addDirs); err != nil {
		printCreateAgentUsage(stderr, shortcutProvider)
		return err
	}
	if flags.dialogueReplyOnly {
		if _, ok := c.agents.(claudeDialogueLauncher); !ok {
			return errors.New("claude reply-only launcher is unavailable")
		}
	}
	// The profile is resolved after every argv-only refusal, since it reads
	// the profile files. (Only a canonical create that omits --provider has
	// read it already, in resolveCreateProvider, because there the profile
	// names the provider those refusals judge.) What it merges into the flags
	// is then held to the same lane checks an explicit flag is.
	if err := c.resolveCreateProfile(spelling, provider, &flags); err != nil {
		return err
	}
	if flags.profileLaunch.active() {
		if err := requireClaudeLaunchOptions(spelling, provider, flags); err != nil {
			return err
		}
		if err := requirePersonaLane(spelling, provider, flags); err != nil {
			return err
		}
	}
	return c.createAgent(spelling, provider, flags, shape, stdout, stderr)
}

// printCreateAgentUsage prints the catalog Usage of the create spelling a
// refusal reached. Each route is spelled as a literal so the usage guards can
// resolve it against the catalog.
func printCreateAgentUsage(stderr io.Writer, shortcutProvider string) {
	switch shortcutProvider {
	case "":
		printRouteUsage(stderr, "create agent")
	case aiModeCodex:
		printRouteUsage(stderr, "create codex")
	case aiModeClaude:
		printRouteUsage(stderr, "create claude")
	case aiModeAntigravity:
		printRouteUsage(stderr, "create antigravity")
	}
}

type agentActivationTarget struct {
	agentUID   string
	agentName  string
	paneUID    string
	paneID     string
	generation string
}

// Registration arrives on the provider's own SessionStart hook, which is not
// ordered against the activation acknowledgement this create already waited
// for. A short grace therefore separates "the hook is a moment behind" from
// "the hook never ran", and only the second is worth a warning. The create
// itself never fails on this: registration is the messaging lane, and a Pane
// that works for its operator is not a failed create.
// They are variables, not constants, so a test can run the whole loop without
// spending its wall clock; nothing outside tests reassigns them.
var (
	claudeRegistrationCreateGrace        = 2 * time.Second
	claudeRegistrationCreatePollInterval = 100 * time.Millisecond
	claudeRegistrationCreateSleep        = time.Sleep
)

// warnUnregisteredClaudeActivations reports every acknowledged Claude
// activation that finished with no registration lease.
//
// Before this existed the state was silent: the Agent came up, looked healthy
// in every projection, and only refused when someone finally sent it a message
// -- which for an unattended worker could be hours later, or never. Saying it
// at create time is the cheapest place to say it.
func (c *createCommand) warnUnregisteredClaudeActivations(targets []agentActivationTarget, stderr io.Writer) error {
	if len(targets) == 0 || c.store == nil || c.store.load == nil {
		return nil
	}
	remaining := targets
	var warnings []string
	for deadline := time.Now().Add(claudeRegistrationCreateGrace); ; {
		registry, err := c.store.load()
		if err != nil {
			// The resources exist and the create succeeded; a Registry read
			// that fails here is not a reason to fail it retroactively.
			return nil
		}
		var pending []agentActivationTarget
		warnings = warnings[:0]
		for _, target := range remaining {
			agent, ok := registry.Agent(target.agentUID)
			if !ok || agent.Spec.Provider != string(aiprovider.Claude) ||
				agent.Status.PaneRef != target.paneUID {
				continue
			}
			shape := classifyAgentClaudeRegistration(registry, *agent)
			if shape == coremetadata.ClaudeRegistrationReady {
				continue
			}
			pending = append(pending, target)
			warnings = append(warnings, unregisteredClaudeActivationWarning(target, *agent, shape))
		}
		remaining = pending
		if len(remaining) == 0 || !time.Now().Before(deadline) {
			break
		}
		claudeRegistrationCreateSleep(claudeRegistrationCreatePollInterval)
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintln(stderr, warning); err != nil {
			return err
		}
	}
	return nil
}

// unregisteredClaudeActivationWarning states what is live, what is missing,
// what it costs, and what fixes it -- in that order, so an operator who reads
// only the first clause still learns that nothing was rolled back.
func unregisteredClaudeActivationWarning(target agentActivationTarget, agent coremetadata.Agent, shape coremetadata.ClaudeRegistrationShape) string {
	return fmt.Sprintf(
		"create agent: warning: agent/%s uid:%s is live in Pane %s but activation finished with no Claude registration lease, so `projmux agent message send` to it will be refused; %s",
		target.agentName, target.agentUID, target.paneID,
		shape.Diagnosis()+"; "+claudeRegistrationNextAction(shape, agent.Metadata.UID))
}

type agentLaunchOutcomeRow struct {
	Outcome    string
	RC         string
	Stdout     string
	Resources  string
	Activation string
	Diagnostic string
}

// agentLaunchOutcomeTable is the closed command-result contract. String values
// make empty output and absent diagnostics printable rather than ambiguous
// blank cells in docs, tests, or support output.
var agentLaunchOutcomeTable = []agentLaunchOutcomeRow{
	{Outcome: "pre-runtime failure", RC: "nonzero", Stdout: "empty", Resources: "none", Activation: "not created", Diagnostic: "bounded refusal/failure"},
	{Outcome: "created+acknowledged", RC: "0", Stdout: "exact %N one line", Resources: "preserved", Activation: string(coremetadata.ActivationAcknowledged), Diagnostic: "none"},
	{Outcome: "created+unconfirmed", RC: "nonzero", Stdout: "empty", Resources: "preserved", Activation: string(coremetadata.ActivationUnconfirmed), Diagnostic: "exact Agent UID, live Pane, recheck-first remediation ending in conditional delete"},
	{Outcome: "delayed acknowledgement", RC: "0", Stdout: "exact %N one line", Resources: "preserved", Activation: string(coremetadata.ActivationAcknowledged), Diagnostic: "none"},
}

// resolveCreateProvider fixes the provider of one canonical Agent create.
//
// The canonical spelling takes an explicit `--provider`, which always wins and
// is checked against the argv alone. Without it, the profile the create selects
// (selectCreateProfile: --profile, or a `role` creation label) decides the
// provider when it names one; a profile that cannot be selected or resolved is
// refused here with the refusal resolveCreateProfile would give. With neither,
// the create is refused as requiring --provider. The saved split mode is
// deliberately not consulted, because a canonical route whose result depends
// on hidden state is not canonical. A shortcut already names its provider, so
// respelling it is a usage error rather than a silent winner.
func (c *createCommand) resolveCreateProvider(spelling, shortcutProvider string, flags resourceCreateFlags) (string, error) {
	if shortcutProvider == "" {
		if flags.providerSet {
			return requireCanonicalProvider(spelling, flags.provider)
		}
		selected, err := c.selectCreateProfile(spelling, flags)
		if err != nil {
			return "", err
		}
		return requireCanonicalProvider(spelling, selected.spec.Provider)
	}
	if flags.providerSet {
		return "", usageError(fmt.Sprintf(
			"%s already names the provider; drop --provider or use `projmux create agent --provider %s`",
			spelling, strings.TrimSpace(flags.provider)))
	}
	return shortcutProvider, nil
}
