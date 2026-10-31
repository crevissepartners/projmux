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
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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
type processDeleteError = mutationruntime.ProcessDeleteError

func processDeleteRefusal(token, runtime, format string, args ...any) *processDeleteError {
	return mutationruntime.ProcessDeleteRefusal(token, runtime, format, args...)
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
	store        *resourceStore
	alive        func(coremetadata.ProcessIdentity) bool
	readIdentity mutationruntime.ProcessIdentityReader
	stop         func(context.Context, coremetadata.Registry, coremetadata.Agent) error
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

// stopProcessAgentOwner sends deletion-specific Stop admission to the exact
// owner socket. Socket identity and kernel peer birth are verified by the
// shared caller; a stale or unreachable host is never rediscovered.
func stopProcessAgentOwner(ctx context.Context, reg coremetadata.Registry, agent coremetadata.Agent) error {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return err
	}
	admission, ok := ctx.Value(processDeleteAdmissionKey{}).(processDeleteAdmission)
	if !ok {
		return processhost.ErrStale
	}
	err = mutationruntime.CallProcessAgentDeleteStop(ctx, intmetadata.PathFor(paths.StateDir), reg, agent, admission)
	if errors.Is(err, processhost.ErrClosed) {
		return nil
	}
	return err
}

type processDeleteTarget struct {
	Agent, Pane, Window        string
	RootKind                   coremetadata.Kind
	RootUID, Runtime, Evidence string
	HostPID                    int
	binding                    coremetadata.ProcessBinding
	activation                 *coremetadata.ProcessActivation
	claim                      bool
	claimNonce                 string
	claimOwner                 coremetadata.ProcessIdentity
}

func processDeleteTargetFromShared(t mutationruntime.ProcessDeleteTarget) processDeleteTarget {
	return processDeleteTarget{Agent: t.Agent, Pane: t.Pane, Window: t.Window, RootKind: t.RootKind, RootUID: t.RootUID, Runtime: t.Runtime, Evidence: t.Evidence, HostPID: t.HostPID, binding: t.Binding, activation: t.Activation, claim: t.Claim, claimNonce: t.ClaimNonce, claimOwner: t.ClaimOwner}
}
func (t processDeleteTarget) shared() mutationruntime.ProcessDeleteTarget {
	return mutationruntime.ProcessDeleteTarget{Agent: t.Agent, Pane: t.Pane, Window: t.Window, RootKind: t.RootKind, RootUID: t.RootUID, Runtime: t.Runtime, Evidence: t.Evidence, HostPID: t.HostPID, Binding: t.binding, Activation: t.activation, Claim: t.claim, ClaimNonce: t.claimNonce, ClaimOwner: t.claimOwner}
}

type processDeleteOutcome struct {
	Targets  []processDeleteTarget
	Affected []DeletionAffected
	Actor    DeletionActor
	Notices  []string
}

//lint:ignore U1000 Retain the metadata predicate adapter for existing internal process-control consumers; the shared delete executor also verifies kernel retirement.
func processWaitRetired(r coremetadata.Registry, t processDeleteTarget) bool {
	return mutationruntime.ProcessWaitRetired(r, t.shared())
}
func (d *processAgentDeleter) stopAndWait(ctx context.Context, t processDeleteTarget) error {
	return d.shared().StopAndWait(ctx, t.shared())
}

func isProcessAgentPlan(r coremetadata.Registry, p deletePlan) bool {
	return mutationruntime.IsProcessAgentPlan(r, mutationruntime.DeletePlan(p))
}
func (d *processAgentDeleter) shared() *mutationruntime.ProcessAgentDeleter {
	path := ""
	if d.store.stateDir != nil {
		if root, err := d.store.stateDir(); err == nil {
			path = intmetadata.PathFor(root)
		}
	}
	return &mutationruntime.ProcessAgentDeleter{RegistryPath: path, MapError: MapMetadataError, Load: d.store.load, Mutate: d.store.mutate, Alive: d.alive, ReadIdentity: d.readIdentity, Stop: d.stop, WaitLimit: d.waitLimit, StopAdmission: d.stopAdmission, Poll: d.poll}
}

//lint:ignore U1000 Retain the thin classification adapter for existing internal process-control consumers.
func (d *processAgentDeleter) classify(r coremetadata.Registry, uid string) (processDeleteTarget, error) {
	t, err := d.shared().Classify(r, uid)
	return processDeleteTargetFromShared(t), err
}
func (d *processAgentDeleter) classifyPlan(r coremetadata.Registry, p deletePlan) ([]processDeleteTarget, error) {
	targets, err := d.shared().ClassifyPlan(r, mutationruntime.DeletePlan(p))
	out := make([]processDeleteTarget, len(targets))
	for i, t := range targets {
		out[i] = processDeleteTargetFromShared(t)
	}
	return out, err
}
func (d *processAgentDeleter) execute(ctx context.Context, p deletePlan, targets []processDeleteTarget, actor func(*coremetadata.Registry) DeletionActor) (processDeleteOutcome, error) {
	shared := make([]mutationruntime.ProcessDeleteTarget, len(targets))
	for i, t := range targets {
		shared[i] = t.shared()
	}
	result, err := d.shared().Execute(ctx, mutationruntime.DeletePlan(p), shared, actor)
	out := processDeleteOutcome{Affected: result.Affected, Actor: result.Actor, Notices: result.Notices}
	for _, t := range result.Targets {
		out.Targets = append(out.Targets, processDeleteTargetFromShared(t))
	}
	return out, err
}
func (d *processAgentDeleter) record(outcome processDeleteOutcome, plan deletePlan, operationID string, stderr io.Writer) {
	recordDeletion(d.store, newDeletionRecord(childDeletionOperation(plan.Kind), operationID, d.via, outcome.Actor,
		deletionTargetsOf(plan), outcome.Affected), stderr)
	for _, notice := range outcome.Notices {
		_, _ = fmt.Fprintln(stderr, notice)
	}
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
		return processDeleteResult(targets[0], true), nil
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
	return processDeleteResult(outcome.Targets[0], false), nil
}

func processDeleteResult(t processDeleteTarget, dryRun bool) processAgentDeleteResult {
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

func isVirtualDeletePlan(reg coremetadata.Registry, plan deletePlan) bool {
	return mutationruntime.IsVirtualProcessPlan(reg, mutationruntime.DeletePlan(plan))
}

func (c *deleteCommand) runVirtualDelete(spelling string, plan deletePlan, flags deleteSocketFlags, dryRun, yes bool, stdout, stderr io.Writer) error {
	if err := validateDeleteSocketFlags(spelling, flags); err != nil {
		return err
	}
	d := c.processDeleter
	if d == nil {
		d = newProcessAgentDeleter()
	}
	d.store, d.via = c.store, c.deletionVia()
	reg, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	targets, err := d.classifyPlan(reg, plan)
	if err != nil {
		return err
	}
	if dryRun {
		return writeVirtualDeletePlan(stdout, spelling, plan, targets, true)
	}
	if plan.NeedsConfirmation() {
		stops := 0
		for _, target := range targets {
			if target.Runtime == processDeleteRuntimeRunning {
				stops++
			}
		}
		prompt := fmt.Sprintf("%s will remove %d targets and %d cascade resources, stop %d running process Agents through their owner hosts, and wait for their exact Wait receipts", spelling, len(plan.Targets), plan.Cascades(), stops)
		if err := c.confirm.confirm(yes, prompt, prompt+"; needs confirmation; re-run with --yes or --dry-run", stdout); err != nil {
			return err
		}
	}
	op, err := c.mintOperationID()
	if err != nil {
		return err
	}
	outcome, err := d.execute(context.Background(), plan, targets, func(working *coremetadata.Registry) DeletionActor {
		return c.observeDeletionActor(tmuxTransport{}, working)
	})
	if err != nil {
		return err
	}
	d.record(outcome, plan, op, stderr)
	if err := writeVirtualDeletePlan(stdout, spelling, plan, outcome.Targets, false); err != nil {
		return err
	}
	receipt := childDeleteReceipt(plan.Kind, plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, false)
	for _, target := range outcome.Targets {
		if target.Runtime == processDeleteRuntimeStopped {
			receipt.Effects.Runtime = cli.RuntimeStopped
			break
		}
	}
	if err := receipt.WriteHuman(stdout); err != nil {
		return err
	}
	return flushDeleteResult(stdout)
}

func writeVirtualDeletePlan(stdout io.Writer, spelling string, plan deletePlan, targets []processDeleteTarget, dryRun bool) error {
	if err := writeDeletePlan(stdout, spelling, plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, tmuxTransport{}, dryRun, false); err != nil {
		return err
	}
	for _, target := range targets {
		var action string
		switch target.Runtime {
		case processDeleteRuntimeRunning:
			action = fmt.Sprintf("would stop through owner host pid=%d and wait for its exact Wait receipt", target.HostPID)
		case processDeleteRuntimeStopped:
			action = "stopped through its owner host and confirmed its exact Wait receipt"
		default:
			action = "registry-only; no process was signaled and no tmux server was used"
		}
		if _, err := fmt.Fprintf(stdout, "  process-host agent uid=%s %s; runtime=%s evidence=%s owner-window=%s root=%s/%s\n", target.Agent, action, target.Runtime, target.Evidence, target.Window, strings.ToLower(string(target.RootKind)), target.RootUID); err != nil {
			return err
		}
	}
	return nil
}
