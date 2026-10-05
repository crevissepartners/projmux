package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// A process runtime Agent has no tmux half. Its delete is decided by the exact
// Registry owner chain and kernel birth evidence alone, so it needs no tmux
// server, socket, or inherited TMUX receipt.
//
// The runtime vocabulary of one target:
//
//   - offline: the owner recorded an exact supervisor Wait and retired the
//     activation. Registry cleanup only.
//   - unknown: no activation was ever recorded, or both the recorded host and
//     child births are gone (the owner was killed before it could record a
//     Wait). Registry cleanup only.
//   - running: the recorded owner host birth is alive. The delete asks that
//     host to Stop through its existing control socket, waits for the exact
//     Wait receipt and activation retirement it records, and then deletes.
//     This is the process counterpart of tmux delete killing a live Pane.
//
// A child that outlived its host is refused: nothing here signals a provider
// child directly. Its supervisor ends it within the stop grace.
const (
	processDeleteRuntimeOffline = "offline"
	processDeleteRuntimeUnknown = "unknown"
	processDeleteRuntimeRunning = "running"
	processDeleteRuntimeStopped = "stopped"
)

// Refusal tokens. process-host-unavailable is the existing control token and
// is reused when a live owner cannot be reached.
const (
	processDeleteRefusedToken         = "process-delete-refused"
	processDeleteStopUnconfirmedToken = "process-delete-stop-unconfirmed"
	processDeleteHostUnavailableToken = "process-host-unavailable"
)

// processDeleteError is the typed refusal of a process Agent delete. Its text
// is exactly what the CLI prints; Token and Runtime let an in-process caller
// branch without parsing it. A refusal leaves the Registry unchanged.
type processDeleteError struct {
	Token   string
	Runtime string
	cause   error
}

func (e *processDeleteError) Error() string { return e.cause.Error() }
func (e *processDeleteError) Unwrap() error { return e.cause }

func processDeleteRefusal(token, runtime, format string, args ...any) *processDeleteError {
	return &processDeleteError{Token: token, Runtime: runtime, cause: fmt.Errorf(token+": "+format, args...)}
}

// processDeleteScope narrows the Agent reference like `--project`/`--window`.
type processDeleteScope struct{ Project, Window selector.Ref }

type processAgentDeleteOptions struct {
	Agent  selector.Ref
	Scope  processDeleteScope
	DryRun bool
}

type processAgentDeleteRequest struct {
	options processAgentDeleteOptions
	// The CLI route fills these; an in-process caller leaves them zero and
	// gets the production seams, no confirmation, and no deletion actor.
	deleter     *processAgentDeleter
	confirm     func(processDeleteTarget) error
	actor       func(*coremetadata.Registry) DeletionActor
	operationID func() (string, error)
	stderr      io.Writer
}

// processAgentDeleteResult reports one committed (or previewed) delete.
// Runtime is offline, unknown, or stopped; a dry run of a live owner reports
// running.
type processAgentDeleteResult struct {
	Agent    string
	Pane     string
	Runtime  string
	Evidence string
	DryRun   bool
	target   processDeleteTarget
}

// newProcessAgentDeleteRequest accepts exactly one uid: Agent reference, the
// same exact-uid rule Registry-only tmux deletes follow.
func newProcessAgentDeleteRequest(opts processAgentDeleteOptions) (processAgentDeleteRequest, error) {
	if !opts.Agent.IsUID() {
		return processAgentDeleteRequest{}, processDeleteRefusal(processDeleteRefusedToken, "",
			"delete agent requires one exact uid: Agent reference")
	}
	opts.Agent.Kind = coremetadata.KindAgent
	return processAgentDeleteRequest{options: opts}, nil
}

// deleteProcessAgent is the in-process entry point of `projmux delete agent`
// for one process runtime Agent. The CLI route calls it too.
func deleteProcessAgent(ctx context.Context, req processAgentDeleteRequest) (processAgentDeleteResult, error) {
	deleter := req.deleter
	if deleter == nil {
		deleter = newProcessAgentDeleter()
	}
	return deleter.deleteOne(ctx, req)
}

// processAgentDeleter carries the delete's seams. Production uses the
// resource store, kernel birth identity, and the owner host's control socket.
type processAgentDeleter struct {
	store *resourceStore
	alive func(coremetadata.ProcessIdentity) bool
	stop  func(context.Context, coremetadata.Registry, coremetadata.Agent) error
	// waitLimit bounds Stop -> Wait the same way create rollback bounds it.
	waitLimit time.Duration
	// stopAdmission bounds retries of a Stop the live host refused as stale:
	// its session authority can briefly trail the Registry while a Claude
	// session starts.
	stopAdmission time.Duration
	poll          time.Duration
	via           deletionVia
}

func newProcessAgentDeleter() *processAgentDeleter {
	limits := processhost.DefaultLimits()
	return &processAgentDeleter{
		store:         newResourceStore(),
		alive:         processBirthAlive,
		stop:          stopProcessAgentOwner,
		waitLimit:     3*limits.Grace + 2*limits.Write,
		stopAdmission: 2 * time.Second,
		poll:          25 * time.Millisecond,
		via:           deletionViaCLI,
	}
}

func processBirthAlive(birth coremetadata.ProcessIdentity) bool {
	if !birth.Valid() {
		return false
	}
	current, _, err := localipc.Process(birth.PID)
	return err == nil && current == birth
}

// stopProcessAgentOwner sends the existing host control "stop" to the exact
// owner socket. Socket identity and kernel peer birth are verified by the
// shared caller; a stale or unreachable host is never rediscovered.
func stopProcessAgentOwner(_ context.Context, reg coremetadata.Registry, agent coremetadata.Agent) error {
	control := &agentCommand{controlPaths: config.DefaultPathsFromEnv}
	var err error
	switch agent.Spec.Provider {
	case aiModeClaude, aiModeCodex:
		_, err = control.callProcessTurn(reg, agent, agent.Spec.Provider, "stop", "")
	default:
		err = processhost.ErrStale
	}
	if errors.Is(err, processhost.ErrClosed) {
		// The owner is already shutting down; its Wait is what we wait for.
		return nil
	}
	return err
}

// processDeleteTarget is one classified Agent.
type processDeleteTarget struct {
	Agent, Pane, Window string
	RootKind            coremetadata.Kind
	RootUID             string
	Runtime             string
	Evidence            string
	HostPID             int
	binding             coremetadata.ProcessBinding
}

// isProcessAgentPlan reports whether every target is an Agent whose managed
// Panes are all process runtime Panes. A plan that touches any tmux Pane, or
// an Agent with no managed Pane, keeps the tmux delete path unchanged.
func isProcessAgentPlan(registry coremetadata.Registry, plan deletePlan) bool {
	if plan.Kind != coremetadata.KindAgent || len(plan.Targets) == 0 {
		return false
	}
	for _, target := range plan.Targets {
		panes := 0
		for _, descendant := range target.Descendants {
			if descendant.Kind != coremetadata.KindPane {
				continue
			}
			pane, ok := registry.Pane(descendant.UID)
			if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
				return false
			}
			panes++
		}
		if panes == 0 {
			return false
		}
	}
	return true
}

// classify judges one Agent from the Registry and kernel birth evidence.
// It never probes under the Registry lock; callers revalidate the result with
// processDeleteSignature inside the transaction.
func (d *processAgentDeleter) classify(registry coremetadata.Registry, agentUID string) (processDeleteTarget, error) {
	agent, ok := registry.Agent(agentUID)
	if !ok {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, "", "registry Agent uid %q disappeared; nothing was changed", agentUID)
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, "", "registry Agent uid %q has no exact owning Window; nothing was changed", agentUID)
	}
	root, err := deleteRootForWindow(registry, *window)
	if err != nil {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, "", "%v; nothing was changed", err)
	}
	panes := registry.PanesOf(agentUID)
	if len(panes) != 1 || panes[0].Metadata.UID != agent.Status.PaneRef || panes[0].Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, "",
			"registry Agent uid %q does not own exactly its current process Pane; nothing was changed", agentUID)
	}
	pane := panes[0]
	target := processDeleteTarget{Agent: agentUID, Pane: pane.Metadata.UID, Window: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}
	activation := pane.Status.Activation
	if activation.IsZero() {
		record := pane.Status.ProcessSession
		receipt := pane.Status.LastTermination
		if agent.Status.Phase == coremetadata.PhaseOffline && record != nil &&
			coremetadata.MatchesProcessWait(record.Binding, receipt) && coremetadata.SameProcessWait(receipt, agent.Status.LastTermination) {
			target.Runtime, target.binding = processDeleteRuntimeOffline, record.Binding
			target.Evidence = processWaitEvidence(*receipt)
			return target, nil
		}
		target.Runtime, target.Evidence = processDeleteRuntimeUnknown, "activation-absent"
		return target, nil
	}
	process := activation.Process
	if activation.Kind != coremetadata.RuntimeProcess || process == nil {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, "",
			"process Pane uid %q carries a non-process activation; nothing was changed", pane.Metadata.UID)
	}
	target.binding = process.Binding
	if d.alive(process.HostProcess) {
		target.Runtime, target.HostPID = processDeleteRuntimeRunning, process.HostProcess.PID
		target.Evidence = fmt.Sprintf("owner-host-pid=%d", process.HostProcess.PID)
		return target, nil
	}
	if d.alive(process.Child) {
		return processDeleteTarget{}, processDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeUnknown,
			"process Agent uid %q lost its owner host but child pid %d is still alive; its supervisor ends it within the stop grace, retry then; nothing was changed",
			agentUID, process.Child.PID)
	}
	target.Runtime, target.Evidence = processDeleteRuntimeUnknown, "owner-host-and-child-absent"
	return target, nil
}

func processWaitEvidence(receipt coremetadata.TerminationEvidence) string {
	evidence := "wait:" + string(receipt.Classification)
	if receipt.ExitCode != nil {
		evidence += fmt.Sprintf(" exit=%d", *receipt.ExitCode)
	}
	if receipt.Signal != "" {
		evidence += " signal=" + receipt.Signal
	}
	return evidence
}

// processDeleteSignature signs the Registry facts a classification read. The
// locked transaction must reproduce it byte for byte before deleting.
func processDeleteSignature(registry coremetadata.Registry, plan deletePlan) string {
	var b strings.Builder
	b.WriteString(plan.signature())
	for _, target := range plan.Targets {
		agent, ok := registry.Agent(target.Match.UID)
		if !ok {
			fmt.Fprintf(&b, "|agent=%q:absent", target.Match.UID)
			continue
		}
		fmt.Fprintf(&b, "|agent=%q,owner=%q,phase=%q,pane-ref=%q", agent.Metadata.UID, agent.Metadata.OwnerUID(), agent.Status.Phase, agent.Status.PaneRef)
		for _, pane := range registry.PanesOf(agent.Metadata.UID) {
			fmt.Fprintf(&b, ",pane=%q,runtime=%q,activation=%+v,termination=%s", pane.Metadata.UID, pane.Spec.Runtime.EffectiveKind(),
				pane.Status.Activation.Process, processTerminationSignature(pane.Status.LastTermination))
			if pane.Status.ProcessSession != nil {
				fmt.Fprintf(&b, ",binding=%+v", pane.Status.ProcessSession.Binding)
			}
		}
	}
	return b.String()
}

func processTerminationSignature(receipt *coremetadata.TerminationEvidence) string {
	if receipt == nil {
		return "none"
	}
	code := "nil"
	if receipt.ExitCode != nil {
		code = fmt.Sprint(*receipt.ExitCode)
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", receipt.Source, receipt.Classification, receipt.Generation, code, receipt.Signal, receipt.ObservedAt.UTC().Format(time.RFC3339Nano))
}

func (d *processAgentDeleter) classifyPlan(registry coremetadata.Registry, plan deletePlan) ([]processDeleteTarget, error) {
	targets := make([]processDeleteTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		classified, err := d.classify(registry, target.Match.UID)
		if err != nil {
			return nil, err
		}
		targets = append(targets, classified)
	}
	return targets, nil
}

// processDeleteOutcome is a committed delete.
type processDeleteOutcome struct {
	targets  []processDeleteTarget
	affected []DeletionAffected
	actor    DeletionActor
}

// execute stops every running target through its owner, waits for the exact
// retirement, reclassifies outside the lock, and deletes the whole plan in one
// transaction that must reproduce the reclassified Registry evidence.
func (d *processAgentDeleter) execute(ctx context.Context, plan deletePlan, targets []processDeleteTarget, actor func(*coremetadata.Registry) DeletionActor) (processDeleteOutcome, error) {
	stopped := map[string]bool{}
	for _, target := range targets {
		if target.Runtime != processDeleteRuntimeRunning {
			continue
		}
		if err := d.stopAndWait(ctx, target); err != nil {
			return processDeleteOutcome{}, err
		}
		stopped[target.Agent] = true
	}
	registry, err := d.store.load()
	if err != nil {
		return processDeleteOutcome{}, MapMetadataError(err)
	}
	if current := buildDeletePlan(registry, plan.Kind, resolutionOf(plan)).signature(); current != plan.signature() {
		return processDeleteOutcome{}, processDeleteRefusal(processDeleteRefusedToken, "", "delete agent: the cascade plan changed before deletion; nothing was deleted")
	}
	final, err := d.classifyPlan(registry, plan)
	if err != nil {
		return processDeleteOutcome{}, err
	}
	for i := range final {
		if final[i].Runtime == processDeleteRuntimeRunning {
			return processDeleteOutcome{}, processDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeRunning,
				"process Agent uid %q became live under owner host pid %d before deletion; nothing was deleted", final[i].Agent, final[i].HostPID)
		}
		if stopped[final[i].Agent] {
			if final[i].Runtime != processDeleteRuntimeOffline {
				return processDeleteOutcome{}, processDeleteRefusal(processDeleteStopUnconfirmedToken, final[i].Runtime,
					"process Agent uid %q did not keep its exact Wait receipt after Stop; nothing was deleted", final[i].Agent)
			}
			final[i].Runtime = processDeleteRuntimeStopped
		}
	}
	approved := processDeleteSignature(registry, plan)
	outcome := processDeleteOutcome{targets: final}
	err = d.store.mutate(plan.Kind, plan.uids(), func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if processDeleteSignature(*working, plan) != approved {
			return processDeleteRefusal(processDeleteRefusedToken, "", "delete agent: process Agent evidence changed between preflight and deletion; nothing was deleted")
		}
		if actor != nil {
			outcome.actor = actor(working)
		}
		candidate := working.Clone()
		for _, uid := range plan.uids() {
			if err := deleteResource(&candidate, mutator, plan.Kind, uid); err != nil {
				return err
			}
		}
		if err := candidate.Validate(); err != nil {
			return err
		}
		outcome.affected = deletionAffectedBetween(*working, candidate)
		*working = candidate
		return nil
	})
	if err != nil {
		var refusal *processDeleteError
		if errors.As(err, &refusal) {
			return processDeleteOutcome{}, refusal
		}
		return processDeleteOutcome{}, err
	}
	return outcome, nil
}

// stopAndWait asks the owner to Stop and waits, by condition, for the exact
// Wait receipt of the same binding and the retired activation.
func (d *processAgentDeleter) stopAndWait(ctx context.Context, target processDeleteTarget) error {
	ctx, cancel := context.WithTimeout(ctx, d.waitLimit)
	defer cancel()
	admission := time.Now().Add(d.stopAdmission)
	var err error
	for {
		registry, loadErr := d.store.load()
		if loadErr != nil {
			return MapMetadataError(loadErr)
		}
		agent, ok := registry.Agent(target.Agent)
		if !ok {
			return processDeleteRefusal(processDeleteRefusedToken, processDeleteRuntimeRunning, "registry Agent uid %q disappeared before Stop; nothing was changed", target.Agent)
		}
		err = d.stop(ctx, registry, *agent)
		if err == nil || !errors.Is(err, processhost.ErrStale) || !time.Now().Before(admission) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.poll):
		}
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), processDeleteHostUnavailableToken+":") {
			return &processDeleteError{Token: processDeleteHostUnavailableToken, Runtime: processDeleteRuntimeRunning, cause: err}
		}
		return processDeleteRefusal(processDeleteHostUnavailableToken, processDeleteRuntimeRunning,
			"owner host pid %d of process Agent uid %q did not accept Stop: %v; nothing was changed", target.HostPID, target.Agent, err)
	}
	ticker := time.NewTicker(d.poll)
	defer ticker.Stop()
	for {
		registry, err := d.store.load()
		if err == nil && processWaitRetired(registry, target) {
			return nil
		}
		select {
		case <-ctx.Done():
			return processDeleteRefusal(processDeleteStopUnconfirmedToken, processDeleteRuntimeRunning,
				"owner host pid %d accepted Stop for process Agent uid %q but no exact Wait receipt was recorded within %s; the Registry is unchanged, retry once the owner exits",
				target.HostPID, target.Agent, d.waitLimit)
		case <-ticker.C:
		}
	}
}

func processWaitRetired(registry coremetadata.Registry, target processDeleteTarget) bool {
	pane, ok := registry.Pane(target.Pane)
	agent, agentOK := registry.Agent(target.Agent)
	if !ok || !agentOK || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != target.binding {
		return false
	}
	return agent.Status.Phase == coremetadata.PhaseOffline &&
		coremetadata.MatchesProcessWait(target.binding, pane.Status.LastTermination) &&
		coremetadata.SameProcessWait(pane.Status.LastTermination, agent.Status.LastTermination)
}

func resolutionOf(plan deletePlan) selector.Resolution {
	resolution := selector.Resolution{Kind: plan.Kind}
	for _, target := range plan.Targets {
		resolution.Matches = append(resolution.Matches, target.Match)
	}
	return resolution
}

func (p deletePlan) uids() []string {
	out := make([]string, 0, len(p.Targets))
	for _, target := range p.Targets {
		out = append(out, target.Match.UID)
	}
	return out
}

func (d *processAgentDeleter) record(outcome processDeleteOutcome, plan deletePlan, operationID string, stderr io.Writer) {
	recordDeletion(d.store, newDeletionRecord(deletionOperationDeleteAgent, operationID, d.via, outcome.actor,
		deletionTargetsOf(plan), outcome.affected), stderr)
}

func (d *processAgentDeleter) deleteOne(ctx context.Context, req processAgentDeleteRequest) (processAgentDeleteResult, error) {
	opts := req.options
	if !opts.Agent.IsUID() {
		return processAgentDeleteResult{}, processDeleteRefusal(processDeleteRefusedToken, "", "delete agent requires one exact uid: Agent reference")
	}
	registry, err := d.store.load()
	if err != nil {
		return processAgentDeleteResult{}, MapMetadataError(err)
	}
	query := selector.Query{Agents: []selector.Ref{opts.Agent}}
	if opts.Scope.Project != (selector.Ref{}) {
		project := opts.Scope.Project
		query.Project = &project
	}
	if opts.Scope.Window != (selector.Ref{}) {
		query.Windows = []selector.Ref{opts.Scope.Window}
	}
	resolution, err := selector.New(registry).ResolveAgents(query)
	if err != nil {
		return processAgentDeleteResult{}, MapMetadataError(err)
	}
	if len(resolution.Matches) != 1 {
		return processAgentDeleteResult{}, processDeleteRefusal(processDeleteRefusedToken, "",
			"delete agent: %s matched %d Agents, want exactly one; nothing was changed", opts.Agent.Raw, len(resolution.Matches))
	}
	plan := buildDeletePlan(registry, coremetadata.KindAgent, resolution)
	plan.ExactUID = true
	if !isProcessAgentPlan(registry, plan) {
		return processAgentDeleteResult{}, processDeleteRefusal(processDeleteRefusedToken, "",
			"delete agent: Agent uid %q is not a process runtime Agent; nothing was changed", resolution.Matches[0].UID)
	}
	targets, err := d.classifyPlan(registry, plan)
	if err != nil {
		return processAgentDeleteResult{}, err
	}
	if opts.DryRun {
		return targets[0].result(true), nil
	}
	if req.confirm != nil {
		if err := req.confirm(targets[0]); err != nil {
			return processAgentDeleteResult{}, err
		}
	}
	mint := req.operationID
	if mint == nil {
		mint = newCreateOperationID
	}
	operationID, err := mint()
	if err != nil {
		return processAgentDeleteResult{}, err
	}
	outcome, err := d.execute(ctx, plan, targets, req.actor)
	if err != nil {
		return processAgentDeleteResult{}, err
	}
	d.record(outcome, plan, operationID, req.stderr)
	return outcome.targets[0].result(false), nil
}

func (t processDeleteTarget) result(dryRun bool) processAgentDeleteResult {
	return processAgentDeleteResult{Agent: t.Agent, Pane: t.Pane, Runtime: t.Runtime, Evidence: t.Evidence, DryRun: dryRun, target: t}
}

// validateDeleteSocketFlags keeps the socket flags' syntax contract on a delete
// that has no tmux half: conflicting or relative flags are still usage errors,
// and a well-formed one is simply not consulted.
func validateDeleteSocketFlags(spelling string, flags deleteSocketFlags) error {
	_, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketName: flags.socket, SocketPath: flags.socketPath})
	if err == nil {
		return nil
	}
	if errors.Is(err, resourcegraph.ErrTransportConflict) {
		return usageError(spelling + " accepts only one of --socket and --socket-path")
	}
	return usageError(spelling + ": --socket-path must be absolute")
}

// runProcessAgentDelete is the `delete agent` route for process runtime
// Agents. It performs no tmux call and runs through deleteProcessAgent, one
// exact Agent per invocation: each running Agent is stopped by its own owner,
// which no single transaction can make atomic across several owners.
func (c *deleteCommand) runProcessAgentDelete(spelling string, plan deletePlan, flags deleteSocketFlags, dryRun, yes bool, stdout, stderr io.Writer) error {
	if err := validateDeleteSocketFlags(spelling, flags); err != nil {
		return err
	}
	commands := make([]string, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		commands = append(commands, "projmux delete agent uid:"+target.Match.UID)
	}
	if !plan.ExactUID || len(plan.Targets) != 1 {
		return processDeleteRefusal(processDeleteRefusedToken, "",
			"%s: process runtime Agents are deleted one exact uid: selector at a time; run `%s --dry-run`, then `--yes`; nothing was changed",
			spelling, strings.Join(commands, "`, `"))
	}
	deleter := c.processDeleter
	if deleter == nil {
		deleter = newProcessAgentDeleter()
	}
	deleter.store, deleter.via = c.store, c.deletionVia()
	uid := plan.Targets[0].Match.UID
	req, err := newProcessAgentDeleteRequest(processAgentDeleteOptions{Agent: selector.Ref{UID: uid, Raw: "uid:" + uid}, DryRun: dryRun})
	if err != nil {
		return err
	}
	req.deleter = deleter
	req.confirm = func(target processDeleteTarget) error {
		stops := 0
		if target.Runtime == processDeleteRuntimeRunning {
			stops = 1
		}
		prompt := fmt.Sprintf("%s will remove 1 agent and %d descendant resources, and stop %d running process Agent through its owner host",
			spelling, plan.Cascades(), stops)
		refusal := fmt.Sprintf("%s needs confirmation: 1 targets and %d descendant resources. Re-run with --yes, or with --dry-run to review the plan first.",
			spelling, plan.Cascades())
		return c.confirm.confirm(yes, prompt, refusal, stdout)
	}
	req.actor = func(working *coremetadata.Registry) DeletionActor {
		return c.observeDeletionActor(tmuxTransport{}, working)
	}
	req.operationID, req.stderr = c.mintOperationID, stderr
	result, err := deleteProcessAgent(context.Background(), req)
	if err != nil {
		return err
	}
	if err := writeProcessDeletePlan(stdout, spelling, plan, result.target, dryRun); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	receipt := childDeleteReceipt(coremetadata.KindAgent, plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, false)
	if result.Runtime == processDeleteRuntimeStopped {
		receipt.Effects.Runtime = cli.RuntimeStopped
	}
	if err := receipt.WriteHuman(stdout); err != nil {
		return err
	}
	return flushDeleteResult(stdout)
}

func writeProcessDeletePlan(stdout io.Writer, spelling string, plan deletePlan, classified processDeleteTarget, dryRun bool) error {
	var b strings.Builder
	verb := "deleting"
	if dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(&b, "%s: %s %d %s and %d descendant resource%s\n",
		spelling, verb, len(plan.Targets), "agent"+plural(len(plan.Targets)), plan.Cascades(), plural(plan.Cascades()))
	for _, target := range plan.Targets {
		fmt.Fprintf(&b, "%s uid=%s", resourceRef(target.Match), target.Match.UID)
		if owner := target.Match.Owner.String(); owner != "" {
			fmt.Fprintf(&b, " owner=%s", owner)
		}
		b.WriteString("\n")
		for _, descendant := range target.Descendants {
			fmt.Fprintf(&b, "  cascade %s/%s uid=%s\n", strings.ToLower(string(descendant.Kind)), descendant.Name, descendant.UID)
		}
		if target.Match.UID != classified.Agent {
			continue
		}
		action := "deleted"
		if dryRun {
			action = "would delete"
		}
		switch classified.Runtime {
		case processDeleteRuntimeRunning:
			fmt.Fprintf(&b, "  process-host would stop through owner host pid=%d, wait for its exact Wait receipt, then delete this Agent; runtime=running",
				classified.HostPID)
		case processDeleteRuntimeStopped:
			fmt.Fprintf(&b, "  process-host stopped through its owner host and deleted this Agent; runtime=stopped")
		default:
			fmt.Fprintf(&b, "  process-host registry-only %s this Agent; no process was signaled and no tmux server was used; runtime=%s", action, classified.Runtime)
		}
		fmt.Fprintf(&b, " evidence=%s owner-window=%s root=%s/%s\n", classified.Evidence, classified.Window,
			strings.ToLower(string(classified.RootKind)), classified.RootUID)
	}
	if dryRun {
		b.WriteString("dry-run: nothing was deleted\n")
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}
