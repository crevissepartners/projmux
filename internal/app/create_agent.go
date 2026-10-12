package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// canonicalCreateAgent is the spelling the provider shortcuts normalize onto.
const canonicalCreateAgent = "create agent"



// agentPaneNameSuffix is the launcher convention for the name of the Pane an
// Agent owns. It used to live only in the launcher documentation as a
// mandatory follow-up `rename pane`, which meant a caller that had not read
// that page left the managed Pane addressed by a raw UID.
const agentPaneNameSuffix = "-pane"





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

	shape := resourceCreateShape{split: true, provider: true}
	var flags resourceCreateFlags
	var err error
	if shortcutProvider == "" {
		flags, err = parseResourceCreateFlags("create agent", args, stderr, resourceCreateShape{split: true, provider: true, host: true})
	} else {
		flags, err = parseResourceCreateFlags(spelling, args, stderr, shape)
	}
	if err != nil {
		return err
	}
	if flags.host != "" && flags.host != "tmux" && flags.host != "process" {
		return usageError(fmt.Sprintf("create agent --host %q requires tmux or process", flags.host))
	}
	if flags.host == "process" {
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









func (c *createCommand) confirmAgentActivations(targets []agentActivationTarget) error {
	var diagnostics []error
	for _, target := range targets {
		acknowledged, _, err := c.agents.AwaitAgentActivation(context.Background(), c.runtime.Runner, target.paneID,
			agentActivationStartupDeadline, agentActivationAcknowledgementDeadline)
		source := string(coremetadata.InteractionSourceProviderHook)
		state := coremetadata.ActivationAcknowledged
		reason := ""
		if err != nil || !acknowledged {
			state = coremetadata.ActivationUnconfirmed
			if err != nil {
				reason = coremetadata.ActivationReasonFailed
			} else {
				reason = coremetadata.ActivationReasonTimedOut
			}
		}
		var committed coremetadata.Agent
		if _, updateErr := c.store.update(func(registry *coremetadata.Registry) error {
			check := checkAgentActivationBinding(*registry, target.paneUID, target.paneID,
				&agentActivationBindingExpectation{AgentUID: target.agentUID, Generation: target.generation})
			if !check.bound() {
				return &agentActivationBindingError{
					command: "create agent", stage: agentBindingStageBeforeRecording,
					paneUID: target.paneUID, paneID: target.paneID, check: check,
				}
			}
			updated, setErr := c.store.mutator().SetAgentActivation(registry, target.agentUID, state, source, reason)
			committed = updated.Clone()
			return setErr
		}); updateErr != nil {
			diagnostics = append(diagnostics, MapMetadataError(updateErr))
			continue
		}
		// A provider hook may commit after Await's final read but before the
		// timeout writer takes the Registry lock. SetAgentActivation is monotonic,
		// so inspect the committed authority instead of the stale local decision.
		if committed.Status.Activation.State == coremetadata.ActivationUnconfirmed {
			diagnostics = append(diagnostics, errors.New(activationUnconfirmedDiagnostic(target, reason)))
		}
	}
	return errors.Join(diagnostics...)
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

// planAgentPaneLaunch builds the provider launch of one canonical Agent create.
//
// The two branches are two different launches, not two spellings of one. A fresh
// create appends the operator's payload as the provider's initial task; a resume
// joins a conversation the provider already has and takes no payload at all --
// the conversation id is the provider's own resume option, not an operand. There
// is deliberately no fallback from the second to the first: a resume that
// silently started a new conversation would lose the context the operator picked
// the row for.
func (c *createCommand) planAgentPaneLaunch(provider string, workspace coremetadata.AgentWorkspace, flags resourceCreateFlags) (string, []string, error) {
	title, argv, _, err := c.planAgentPaneLaunchWithResume(provider, workspace, flags)
	return title, argv, err
}



// bindAgentPane applies the managed-agent pane options.
//
// A resumed Pane additionally carries the conversation id in the live routing
// index from the moment the pane exists, which is what lets the provider's first
// hook event be attributed to this pane instead of having to wait for the
// provider to report the conversation itself.
func (c *createCommand) bindAgentPane(ctx context.Context, paneID, provider, contextDir, title, declared string, flags resourceCreateFlags) error {
	if conversation := strings.TrimSpace(flags.resumeConversation); conversation != "" && c.resumes != nil {
		if source := strings.TrimSpace(flags.resumeSource); source != "" {
			return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
				PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
				ConversationID: conversation, ResumeSource: source, CodexNativeDeclared: declared,
			})
		}
		return c.resumes.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
			PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title,
			ConversationID: conversation, CodexNativeDeclared: declared,
		})
	}
	return c.agents.BindAgentPaneOnRoute(ctx, c.runtime.Runner, agentPaneBinding{
		PaneID: paneID, Provider: provider, ContextDir: contextDir, Title: title, CodexNativeDeclared: declared,
	})
}

// declaredPlainCodexLane names why one managed Codex Agent is being created on
// the plain CLI lane, from the closed declared vocabulary.
//
// Only the two by-design lanes are declared. Every other route to the plain
// lane is either a refusal that creates nothing, or a genuine loss of native
// authority that must stay visible as an unexplained native fallback.
func declaredPlainCodexLane(provider string, flags resourceCreateFlags, prompt string) string {
	if provider != aiModeCodex {
		return ""
	}
	if flags.interactiveOnly {
		return codexNativeDeclaredInteractiveOnly
	}
	if len(flags.payload) == 0 && strings.TrimSpace(flags.resumeConversation) == "" {
		return codexNativeDeclaredPayloadFreeFallback
	}
	if strings.TrimSpace(flags.resumeConversation) != "" && strings.TrimSpace(flags.resumeSource) == aisessions.SourceCodexRollout {
		return codexNativeDeclaredRolloutCatalogResume
	}
	return ""
}
