package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// pruneAgentObservation takes one read-only observation of the exact tmux
// server this invocation can name. It is called at most once per selection
// pass, and only when a selected Agent still records a managed Pane.
type pruneAgentObservation func(ctx context.Context) resourcegraph.Inventory

// pruneAgentCommand implements the canonical `prune agent` route.
//
// It is the bounded, explicit sibling of `prune project`. It only ever
// considers Offline and Failed Agents, it refuses to run without both an age
// bound and at least one evidence selector, it lists a bounded candidate set by
// default, and it deletes nothing until `--yes` is passed. Each deletion is
// exactly Mutator.DeleteAgent: there is no live half, so no tmux object is ever
// killed and nothing outside the Registry is written.
type pruneAgentCommand struct {
	store   *resourceStore
	now     func() time.Time
	observe pruneAgentObservation
}

func newPruneAgentCommand() *pruneAgentCommand {
	return &pruneAgentCommand{
		store:   newResourceStore(),
		now:     time.Now,
		observe: runtimeReaderObservation(newRuntimeDiagnosticsReader(nil)),
	}
}

// runtimeReaderObservation reuses the read half `get` and `describe` already
// observe through: the runtime diagnostics reader's transport resolution
// (inherited $TMUX only, because this route has no socket flags) and its
// bounded inventory observer. Nothing here writes to tmux.
func runtimeReaderObservation(reader *runtimeDiagnosticsReader) pruneAgentObservation {
	return func(ctx context.Context) resourcegraph.Inventory {
		if reader == nil || reader.observe == nil {
			return unavailablePruneAgentInventory("the live tmux observer is not configured")
		}
		transport, err := reader.transport(runtimeTransportRequest{})
		if err != nil {
			return unavailablePruneAgentInventory("the exact tmux server could not be resolved: " + err.Error())
		}
		// An absent transport is handed to the observer too: it answers with
		// zero tmux calls and every scope unavailable, so the classification of
		// every outcome stays the shared inventory's own.
		return reader.observe(ctx, transport)
	}
}

func unavailablePruneAgentInventory(reason string) resourcegraph.Inventory {
	inventory := resourcegraph.Inventory{HostMode: resourcegraph.HostModeUnknown}
	for _, scope := range resourcegraph.Scopes() {
		inventory = inventory.MarkUnavailable(scope, reason)
	}
	return inventory
}

// pruneAgentCriteria is the complete selector one invocation spelled out.
type pruneAgentCriteria struct {
	age          time.Duration
	noSessionRef bool
	noPane       bool
	excluded     map[string]bool
}

// selector renders the evidence flags for the "no match" line.
func (c pruneAgentCriteria) selector() string {
	parts := []string{"--older-than " + c.age.String()}
	if c.noSessionRef {
		parts = append(parts, "--no-session-ref")
	}
	if c.noPane {
		parts = append(parts, "--no-pane")
	}
	return strings.Join(parts, " ")
}

// pruneAgentCandidate is one listed Agent row.
type pruneAgentCandidate struct {
	UID              string
	Name             string
	Provider         string
	Phase            coremetadata.AgentPhase
	Reason           string
	LastTransitionAt time.Time
	SessionRef       bool
	Panes            int
	// Continue is the verdict of decideTopologyAgentContinueEligibility, read
	// only. It is shown so an operator sees which Agents an ordinary Project
	// Continue would still restore before choosing to delete them.
	Continue bool
}

func (c pruneAgentCandidate) render() string {
	return fmt.Sprintf("agent/%s uid=%s provider=%s phase=%s reason=%s lastTransitionAt=%s sessionRef=%s panes=%d continue=%s",
		c.Name, c.UID, pruneAgentField(c.Provider), c.Phase, pruneAgentField(c.Reason),
		c.LastTransitionAt.Format("2006-01-02T15:04:05Z"), yesNo(c.SessionRef), c.Panes, yesNo(c.Continue))
}

func pruneAgentField(value string) string {
	switch {
	case strings.TrimSpace(value) == "":
		return "-"
	case strings.ContainsAny(value, " \t\n"):
		return fmt.Sprintf("%q", value)
	default:
		return value
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// pruneAgentPlan is one selection pass.
type pruneAgentPlan struct {
	candidates []pruneAgentCandidate
	// unknown are Agents that match every Registry criterion but keep managed
	// Panes whose liveness could not be observed. They are never deleted, and
	// any of them makes `--yes` refuse the whole run.
	unknown     []pruneAgentCandidate
	unavailable string
}

// signature renders the whole plan as a comparable string. The locked pass
// refuses on any difference, including a changed column, not only on a changed
// uid set.
func (p pruneAgentPlan) signature() string {
	var b strings.Builder
	for _, candidate := range p.candidates {
		b.WriteString(candidate.render())
		b.WriteString("\n")
	}
	for _, candidate := range p.unknown {
		b.WriteString("unknown ")
		b.WriteString(candidate.render())
		b.WriteString("\n")
	}
	return b.String()
}

func (p pruneAgentPlan) observationRefusal(spelling string) error {
	return fmt.Errorf("%s: live tmux observation is unavailable (%s) for %d Agent%s with remaining managed Panes; --yes refuses the whole run and nothing was deleted",
		spelling, p.unavailable, len(p.unknown), plural(len(p.unknown)))
}

func (c *pruneAgentCommand) Run(args []string, stdout, stderr io.Writer) error {
	const spelling = "prune agent"

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	olderThan := fs.String("older-than", "", "minimum age of status.lastTransitionAt, for example 720h")
	noSessionRef := fs.Bool("no-session-ref", false, "select Agents with no recorded status.sessionRef")
	noPane := fs.Bool("no-pane", false, "select Agents with no remaining managed Pane in the Registry")
	var excludes repeatedFlag
	fs.Var(&excludes, "exclude", "repeatable exact Agent reference to keep: <name> or uid:<uid>")
	yes := fs.Bool("yes", false, "actually delete the listed Agents")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}
	// The evidence selector is mandatory. Age alone must never be able to mean
	// "every stopped Agent": what makes an Agent stale has to be spelled out.
	if !*noSessionRef && !*noPane {
		return usageError(spelling + " requires --no-session-ref, --no-pane, or both; it prunes only Agents whose session ref or managed Panes are gone")
	}
	if strings.TrimSpace(*olderThan) == "" {
		return usageError(spelling + " requires --older-than <duration>, for example --older-than 720h")
	}
	age, err := time.ParseDuration(*olderThan)
	if err != nil {
		return usageError(fmt.Sprintf("%s --older-than %q is not a duration: %v", spelling, *olderThan, err))
	}
	if age < 0 {
		return usageError(spelling + " --older-than must not be negative")
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	excluded, err := resolvePruneAgentExcludes(spelling, registry, excludes)
	if err != nil {
		return err
	}
	criteria := pruneAgentCriteria{age: age, noSessionRef: *noSessionRef, noPane: *noPane, excluded: excluded}
	if len(registry.Agents) == 0 {
		_, err := fmt.Fprintf(stdout, "%s: no Agents are registered\n", spelling)
		return err
	}

	// Selection only reads: the Registry value is the loaded copy and the tmux
	// observation is read-only, so the default listing opens no transaction.
	plan := selectPruneAgents(registry, c.now().UTC(), criteria, c.observe)
	if !*yes {
		return writePruneAgentPlan(stdout, spelling, criteria, plan, true)
	}
	if len(plan.unknown) > 0 {
		return plan.observationRefusal(spelling)
	}
	if len(plan.candidates) == 0 {
		return writePruneAgentPlan(stdout, spelling, criteria, plan, false)
	}

	approved := plan.signature()
	// No uid precheck is passed to mutate: the locked re-selection below compares
	// the complete plan, which already refuses a candidate that disappeared, and
	// it does so with the one refusal this route promises.
	if err := c.store.mutate(coremetadata.KindAgent, nil,
		func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
			// Re-observe and re-select inside the lock, exactly like `prune
			// project`: a Pane that came back live, an Agent that resumed, a
			// session ref that was recorded, or a clock that moved an Agent across
			// the age bound changes the plan, and the whole run is refused rather
			// than executed on stale evidence.
			current := selectPruneAgents(*working, c.now().UTC(), criteria, c.observe)
			if len(current.unknown) > 0 {
				return current.observationRefusal(spelling)
			}
			if current.signature() != approved {
				return fmt.Errorf("%s: the candidate set changed between the listing and the locked re-selection; nothing was deleted", spelling)
			}
			for _, candidate := range plan.candidates {
				if err := mutator.DeleteAgent(working, candidate.UID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	return writePruneAgentPlan(stdout, spelling, criteria, plan, false)
}

// resolvePruneAgentExcludes resolves every --exclude reference through the
// shared Agent selector pipeline, registry-wide. Every reference must address
// exactly one Agent: a typo that silently kept nothing would turn an intended
// exclusion into a deletion.
func resolvePruneAgentExcludes(spelling string, registry coremetadata.Registry, refs []string) (map[string]bool, error) {
	excluded := make(map[string]bool, len(refs))
	for _, raw := range refs {
		flags := resourceQueryFlags{kind: coremetadata.KindAgent, agents: []string{raw}}
		query, err := flags.query()
		if err != nil {
			return nil, usageError(fmt.Sprintf("%s --exclude %q is not an Agent reference: %v", spelling, raw, err))
		}
		resolution, err := flags.resolveQuery(registry, query)
		if err != nil {
			return nil, usageError(fmt.Sprintf("%s --exclude %q could not be resolved: %v", spelling, raw, err))
		}
		switch len(resolution.Matches) {
		case 0:
			return nil, usageError(fmt.Sprintf("%s --exclude %q matched no Agent; every excluded reference must resolve, and nothing was deleted", spelling, raw))
		case 1:
			excluded[resolution.Matches[0].UID] = true
		default:
			return nil, usageError(fmt.Sprintf("%s --exclude %q matched %d Agents; pass the exact uid:<uid> reference of the Agent to keep", spelling, raw, len(resolution.Matches)))
		}
	}
	return excluded, nil
}

// selectPruneAgents runs one selection pass over registry.
//
// The Registry criteria are judged first and cost nothing. Only an Agent that
// passes all of them and still records a managed Pane needs the live
// observation, so a run whose matches hold no Pane never calls tmux at all.
func selectPruneAgents(registry coremetadata.Registry, now time.Time, criteria pruneAgentCriteria, observe pruneAgentObservation) pruneAgentPlan {
	var plan pruneAgentPlan
	var liveness *pruneAgentPaneLiveness
	for _, agent := range registry.Agents {
		if !pruneAgentMatchesRegistry(registry, agent, now, criteria) {
			continue
		}
		panes := registry.PanesOf(agent.Metadata.UID)
		row := newPruneAgentCandidate(registry, agent, len(panes))
		if len(panes) == 0 {
			plan.candidates = append(plan.candidates, row)
			continue
		}
		if liveness == nil {
			observed := observePruneAgentPanes(registry, observe)
			liveness = &observed
		}
		switch liveness.judge(panes) {
		case pruneAgentPanesLive:
			continue
		case pruneAgentPanesUnknown:
			plan.unknown = append(plan.unknown, row)
			plan.unavailable = liveness.reason
		default:
			plan.candidates = append(plan.candidates, row)
		}
	}
	return plan
}

// pruneAgentMatchesRegistry applies every criterion that the Registry alone
// can answer.
func pruneAgentMatchesRegistry(registry coremetadata.Registry, agent coremetadata.Agent, now time.Time, criteria pruneAgentCriteria) bool {
	switch agent.Status.Phase {
	case coremetadata.PhaseOffline, coremetadata.PhaseFailed:
	default:
		// Running, Pending, and any phase added later are never stale by age.
		return false
	}
	if criteria.excluded[agent.Metadata.UID] {
		return false
	}
	last := agent.Status.LastTransitionAt
	if last.IsZero() || now.Sub(last.UTC()) < criteria.age {
		// An Agent with no recorded transition has no age to measure, so it is
		// never old enough.
		return false
	}
	if criteria.noSessionRef && !agent.Status.SessionRef.Empty() {
		return false
	}
	if criteria.noPane && len(registry.PanesOf(agent.Metadata.UID)) != 0 {
		return false
	}
	return true
}

func newPruneAgentCandidate(registry coremetadata.Registry, agent coremetadata.Agent, panes int) pruneAgentCandidate {
	eligible, _, _ := decideTopologyAgentContinueEligibility(registry, agent.Clone())
	return pruneAgentCandidate{
		UID:              agent.Metadata.UID,
		Name:             agent.Metadata.Name,
		Provider:         agent.Spec.Provider,
		Phase:            agent.Status.Phase,
		Reason:           agent.Status.Reason,
		LastTransitionAt: agent.Status.LastTransitionAt.UTC(),
		SessionRef:       !agent.Status.SessionRef.Empty(),
		Panes:            panes,
		Continue:         eligible,
	}
}

type pruneAgentPanesVerdict uint8

const (
	pruneAgentPanesOffline pruneAgentPanesVerdict = iota
	pruneAgentPanesLive
	pruneAgentPanesUnknown
)

// pruneAgentPaneLiveness is the verdict source for managed Pane liveness.
type pruneAgentPaneLiveness struct {
	// live holds every Registry Pane uid a live tmux object mirrors, including
	// a contradicted claim: a uid that two live objects carry, or that sits
	// under the wrong owner, is still evidence that something live holds it.
	live      map[string]bool
	available bool
	reason    string
}

// observePruneAgentPanes joins one observation to the Registry through the
// shared resource graph, so a Pane is judged by exactly the claim rules every
// read verb uses.
//
// "No live Pane" is a destructive verdict here, so it is admitted only from an
// actual read of a server confirmed to be a projmux app host: an exact
// transport, an observed host mode of HostModeAppOwned, and a readable Pane
// scope. The shared inventory deliberately reports a server with no
// @projmux_app marker as standalone with the Pane scope available, and a socket
// with no server behind it as available and empty with only host ownership
// unavailable. Both are right for the views that consume it and both are wrong
// evidence for deletion: a $TMUX that names someone else's server or a dead
// socket would read every live projmux Pane as offline. So standalone,
// unknown, server-absent, a Pane list failure, and a Registry-only snapshot
// with no transport are all "cannot observe" on this side only; the inventory
// itself is untouched.
func observePruneAgentPanes(registry coremetadata.Registry, observe pruneAgentObservation) pruneAgentPaneLiveness {
	if observe == nil {
		return pruneAgentPaneLiveness{reason: "the live tmux observer is not configured"}
	}
	inventory := observe(context.Background())
	out := pruneAgentPaneLiveness{live: map[string]bool{}}
	out.available, out.reason = pruneAgentObservationAuthority(inventory)
	for _, node := range resourcegraph.Resolve(registry, inventory).Panes {
		if node.Runtime != nil || node.Class == resourcegraph.ClassConflict {
			out.live[node.Pane.Metadata.UID] = true
		}
	}
	return out
}

// noTransportObserverReason is the prefix of the shared observer's reason for
// an absent transport. That reason advises socket flags this route does not
// accept, so it is restated in terms of what the invocation lacks; any other
// wording is shown verbatim.
const noTransportObserverReason = "no exact tmux transport"

// pruneAgentObservationAuthority reports whether inventory may answer "no live
// Pane", and why not when it may not.
func pruneAgentObservationAuthority(inventory resourcegraph.Inventory) (bool, string) {
	if !inventory.Transport.Present() {
		if unavailable, ok := inventory.Unavailability(resourcegraph.ScopePanes); ok && !strings.HasPrefix(unavailable.Reason, noTransportObserverReason) {
			return false, unavailable.Reason
		}
		return false, "this invocation names no exact tmux server ($TMUX carries no absolute socket path)"
	}
	if unavailable, ok := inventory.Unavailability(resourcegraph.ScopeHostMode); ok {
		// Includes a socket with no server behind it: host ownership cannot be
		// observed, so its empty object scopes prove nothing.
		return false, unavailable.Reason
	}
	switch inventory.HostMode {
	case resourcegraph.HostModeAppOwned:
	case resourcegraph.HostModeStandalone:
		return false, "the observed tmux server on " + inventory.Transport.String() + " is not projmux app-owned (no @projmux_app marker)"
	default:
		return false, "host ownership of the observed tmux server on " + inventory.Transport.String() + " is " + string(inventory.HostMode)
	}
	if unavailable, ok := inventory.Unavailability(resourcegraph.ScopePanes); ok {
		return false, unavailable.Reason
	}
	return true, ""
}

func (l pruneAgentPaneLiveness) judge(panes []coremetadata.Pane) pruneAgentPanesVerdict {
	for _, pane := range panes {
		if l.live[pane.Metadata.UID] {
			return pruneAgentPanesLive
		}
	}
	if !l.available {
		return pruneAgentPanesUnknown
	}
	return pruneAgentPanesOffline
}

// writePruneAgentPlan renders the bounded candidate listing, capped at the
// same bound `prune project` and the selector ambiguity output use.
func writePruneAgentPlan(stdout io.Writer, spelling string, criteria pruneAgentCriteria, plan pruneAgentPlan, dryRun bool) error {
	var b strings.Builder
	if len(plan.candidates) == 0 && len(plan.unknown) == 0 {
		fmt.Fprintf(&b, "%s: no Agent matches %s\n", spelling, criteria.selector())
		_, err := io.WriteString(stdout, b.String())
		return err
	}
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(&b, "%s: %s %d agent%s\n", spelling, verb, len(plan.candidates), plural(len(plan.candidates)))
	rows := make([]string, 0, len(plan.candidates)+len(plan.unknown))
	for _, candidate := range plan.candidates {
		rows = append(rows, "  "+candidate.render())
	}
	for _, candidate := range plan.unknown {
		rows = append(rows, "  unknown "+candidate.render()+" live=unknown")
	}
	omitted := 0
	if len(rows) > selector.MaxCandidates {
		omitted = len(rows) - selector.MaxCandidates
		rows = rows[:selector.MaxCandidates]
	}
	for _, row := range rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "  ... %d more omitted\n", omitted)
	}
	if len(plan.unknown) > 0 {
		fmt.Fprintf(&b, "live tmux observation unavailable: %s; %d Agent%s with remaining managed Panes cannot be judged and will not be deleted, and --yes is refused\n",
			plan.unavailable, len(plan.unknown), plural(len(plan.unknown)))
	}
	if dryRun {
		b.WriteString("dry-run: nothing was deleted; re-run with --yes to delete\n")
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}
