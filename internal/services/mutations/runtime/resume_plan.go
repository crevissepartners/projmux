package runtime

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// planAgentResume fixes one rebind from the read-only registry.
//
// Nothing here mutates, opens the store, or calls tmux, so every refusal it can
// produce leaves zero transactions, zero tmux objects, and zero bytes on stdout.
//
// # What resume does when several Agents point at one conversation
//
// Phase 0 deliberately did not enforce "one conversation <-> at most one live
// Agent": the ref is a best-effort observation rather than a declaration, and
// enforcing uniqueness at write time deadlocks, because an Offline Agent holds
// its conversation forever and a later Agent observing the same conversation
// could then never record it. The consequence is that several Agents may point
// at one conversation, and this Phase owes that state a rule.
//
// The rule is: **the conversation is never a selector.** `agent resume <ref>`
// rebinds exactly the Agent the reference resolves to, and nothing in this route
// ever searches the registry by conversation id to decide *which* Agent to
// rebind. Duplicates therefore neither redirect the rebind nor block it. That is
// total (defined for every duplicate count), deterministic (it depends on the
// operator's reference and the selector's exact-one cardinality, not on registry
// order, map iteration, or observedAt), and it is the only rule that does not
// re-create Phase 0's deadlock somewhere else: refusing a duplicate at resume
// time would make a state Phase 0 declared legal permanently unusable.
//
// Duplicates are not silent, though. They are disclosed on stderr in uid order,
// so an operator who is about to run two live panes on one provider conversation
// finds that out from projmux rather than from the provider.
//
// The ambiguity this resolves is "which conversation-sharing Agent gets the new
// Pane". The ambiguity it does *not* resolve is "which Agent does a bare
// reference mean" -- that one is already answered upstream by the selector
// engine's <resume, Agent> exact-one cell, which refuses rather than guesses.
//
// # What resume does with observedAt
//
// Nothing. It is deliberately not consulted by any gate on this path.
// `observedAt` records when projmux last *saw* the conversation; it is not a
// timestamp the provider supplied and it says nothing about whether the provider
// still holds the conversation. A wall-clock staleness heuristic built on it
// would be wrong in both directions: a conversation untouched for a month is
// perfectly resumable, and a conversation observed a minute ago may already have
// been deleted. The only authority on whether a conversation can be revived is
// the provider, and reading the provider's own store to ask is permanently out
// of scope. So projmux checks everything it can see -- a ref exists, it names a
// known provider, that provider is enabled, its conversation id is well formed,
// its binary is installed -- and hands the rest to the provider's resume argv.
func BuildAgentResumePlan(spelling string, registry coremetadata.Registry, agent *coremetadata.Agent, retiringProcess ...bool) (AgentResumePlan, error) {
	Name := agent.Metadata.Name

	// (d) An Agent whose provider hook never ran has nothing to revive. This is
	// the most important refusal in the route: the tempting behavior is to start
	// a fresh conversation "since there is nothing to resume", and that is
	// exactly the silent context loss `create` and `resume` are separate verbs
	// to prevent. The message names the other verb rather than performing it.
	Ref := agent.Status.SessionRef
	if Ref.Empty() {
		return AgentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has no provider session ref, so there is no conversation to resume; "+
				"projmux records one the first time that Agent's provider hook fires. "+
				"To start a new conversation instead, run `projmux create agent --provider <provider>`, which mints a new Agent",
			spelling, Name)
	}
	ConversationID := strings.TrimSpace(Ref.ConversationID())
	if ConversationID == "" {
		return AgentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a %s session ref that carries no conversation id; it cannot be resumed",
			spelling, Name, Ref.Provider)
	}
	Provider := strings.TrimSpace(Ref.Provider)
	if Provider == "" {
		return AgentResumePlan{}, fmt.Errorf(
			"%s: agent/%s has a session ref with no provider discriminator; it cannot be resumed", spelling, Name)
	}
	// spec.provider is cross-checked only when the Agent declares one. Phase 0
	// deliberately kept an Agent whose provider never normalized recordable, and
	// punishing it here would undo that leniency.
	if declared := strings.TrimSpace(agent.Spec.Provider); declared != "" && declared != Provider {
		return AgentResumePlan{}, fmt.Errorf(
			"%s: agent/%s is a %s Agent but its session ref is a %s conversation; refusing to resume a mismatched conversation",
			spelling, Name, declared, Provider)
	}

	// A resumable Agent has already given its Pane up. A surviving paneRef means
	// the registry disagrees with itself, and binding a second Pane would orphan
	// the first, so this refuses rather than guessing which one is real.
	if paneUID := strings.TrimSpace(agent.Status.PaneRef); paneUID != "" && !(len(retiringProcess) == 1 && retiringProcess[0] && registry.IsVirtualWindow(agent.Metadata.OwnerUID())) {
		if pane, ok := registry.Pane(paneUID); ok {
			return AgentResumePlan{}, fmt.Errorf(
				"%s: agent/%s is %s but still owns managed pane/%s; refusing to bind a second managed Pane",
				spelling, Name, agent.Status.Phase, pane.Metadata.Name)
		}
	}

	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return AgentResumePlan{}, fmt.Errorf("%s: agent/%s has no owning Window in the registry", spelling, Name)
	}
	Project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return AgentResumePlan{}, fmt.Errorf("%s: window/%s has no owning Project in the registry", spelling, window.Metadata.Name)
	}
	// The same rule `create` applies: a Project whose root has disappeared is
	// preserved and still resolves, but nothing may be materialized under a
	// directory tmux cannot enter. The check is restated with this route's own
	// spelling rather than borrowed, so the message names the verb the operator
	// actually ran.
	if condition, ok := Project.HasCondition(coremetadata.ConditionMissingRoot); ok && condition.Status == coremetadata.ConditionTrue {
		return AgentResumePlan{}, createUsage(fmt.Sprintf(
			"%s: project/%s carries a MissingRoot condition for %q; rebind it before resuming an Agent under it",
			spelling, Project.Metadata.Name, Project.Spec.Root))
	}
	// Resume uses the stable role-agnostic Window anchor. There is no fallback to
	// the active, last-used, or an alternate live Pane.
	AnchorUID := strings.TrimSpace(window.Spec.AnchorPaneRef)
	if AnchorUID == "" {
		return AgentResumePlan{}, createUsage(fmt.Sprintf(
			"%s: window/%s (project/%s) has no anchorPaneRef, so there is no anchor to split",
			spelling, window.Metadata.Name, Project.Metadata.Name))
	}
	if anchor, ok := registry.WindowAnchor(window.Metadata.UID); !ok || anchor.Metadata.UID != AnchorUID {
		return AgentResumePlan{}, createUsage(fmt.Sprintf(
			"%s: window/%s (project/%s) anchorPaneRef %q is dangling or cross-Window",
			spelling, window.Metadata.Name, Project.Metadata.Name, AnchorUID))
	}

	return AgentResumePlan{
		AgentUID:       agent.Metadata.UID,
		AgentName:      Name,
		Provider:       Provider,
		ConversationID: ConversationID,
		Ref:            Ref.Clone(),
		ProjectUID:     Project.Metadata.UID,
		ProjectRoot:    Project.Spec.Root,
		Project:        Project.Clone(),
		Workspace:      agent.Spec.Workspace,
		Topic:          agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic],
		WindowUID:      window.Metadata.UID,
		AnchorUID:      AnchorUID,
		Shared:         SharedConversationAgents(registry, agent.Metadata.UID, Ref),
		Annotations:    agent.Metadata.Annotations,
	}, nil
}

// sharedConversationAgents lists the other Agents recording the same
// conversation, in uid order.
//
// The order is the point: it makes the disclosure byte-identical regardless of
// the order the Agents happen to sit in the registry file, which is what a
// determinism assertion can pin.
func SharedConversationAgents(registry coremetadata.Registry, selfUID string, Ref *coremetadata.AgentSessionRef) []string {
	type row struct{ uid, Name string }
	var rows []row
	for i := range registry.Agents {
		other := registry.Agents[i]
		if other.Metadata.UID == selfUID || !other.Status.SessionRef.SameConversation(Ref) {
			continue
		}
		rows = append(rows, row{uid: other.Metadata.UID, Name: other.Metadata.Name})
	}
	slices.SortStableFunc(rows, func(a, b row) int { return cmp.Compare(a.uid, b.uid) })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("agent/%s (uid:%s)", r.Name, r.uid))
	}
	return out
}

// SharedConversationNotices preserves the plan's UID order and discloses that
// this rebind selects one Agent from a conversation shared by several Agents.
func (p AgentResumePlan) SharedConversationNotices(spelling string) []string {
	var notices []string
	for _, other := range p.Shared {
		notices = append(notices, fmt.Sprintf(
			"projmux: conversation %s:%s is also recorded on %s; %s rebinds only agent/%s",
			p.Provider, p.ConversationID, other, spelling, p.AgentName))
	}
	return notices
}
