package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The command-scoped controller kernel: the one seam that runs
// observe -> plan -> Registry commit -> tmux guard -> execute -> reobserve.
//
// Every stage of that sequence existed before this file, and none of them were
// in one place. The observation was a shadow tmux runner inside the planner, the
// policy was three unrelated predicates, the guard was a loop over the planner's
// own bookkeeping, and there was no reobservation at all -- "the repeat is a
// no-op" was a property tests asserted by running the command twice rather than
// one the command could report about itself. Collecting the sequence here is
// what lets the next producer inherit the order instead of reimplementing it.
//
// Three ordering decisions are contractual:
//
//  1. The Registry commit precedes the first tmux write, always. The Registry is
//     the durable desired state and tmux is a non-transactional overlay, so a
//     failure after the commit leaves a state the exact retry converges, while a
//     failure before it must leave the machine untouched. That is why a commit
//     error returns before the guards even run.
//  2. Guards are all-or-nothing and run before the first write, never
//     interleaved. A guard checked immediately before its own write would still
//     be racing; checking every guard first means a stale plan aborts having
//     changed nothing, which is the only failure mode a replan can describe
//     honestly.
//  3. The reobservation is a full replan against fresh bytes, not a diff of what
//     was written. Asking the machine again is the only way to notice that a
//     write succeeded and did not converge -- a hook that rewrote the option
//     back, a second client racing the same repair -- and that is exactly the
//     case a report claiming success must not hide.

// controllerReobservation is the post-execute evidence that the command
// converged.
type controllerReobservation struct {
	// Converged reports whether a repeat of this command would write nothing.
	Converged bool `json:"converged"`
	// Residual is the stable key of every action a repeat would still plan.
	Residual []string `json:"residual,omitempty"`
	// Unavailable states why the reobservation could not be taken, if it could
	// not. An unavailable reobservation never claims convergence.
	Unavailable string `json:"unavailable,omitempty"`
}

// resourceControllerKernel orchestrates one command against one exact server.
type resourceControllerKernel struct {
	target               explicitTmuxTarget
	runner               tmuxCommandRunner
	store                *resourceStore
	planner              resourceReconcilePlanner
	trigger              controller.RecoveryTrigger
	approvedOrphanImport bool
	// observe takes one bounded inventory of the exact server. It is injectable
	// so a test can state a machine state instead of scripting tmux output.
	observe func(ctx context.Context) resourcegraph.Inventory
	// socketPath reads the server's own `#{socket_path}`, which is the socket
	// guard. A server that is not running has no path and no writes to guard.
	socketPath func(ctx context.Context) (string, bool)
	// route is internal execution authority. It is deliberately absent from
	// controller.Plan so public reconcile JSON remains byte-compatible.
	route     *runtimeMutationRoute
	lookupEnv func(string) string
}

// controllerGuardFields binds the pure kernel's guard vocabulary to the option
// catalog. The kernel takes the spellings as input so it can stay free of any
// tmux dependency; this is the one place they are supplied.
var controllerGuardFields = controller.GuardFields{
	SessionUID: tmuxopts.ProjectUIDSession,
	WindowUID:  tmuxopts.WindowUID,
	PaneUID:    tmuxopts.PaneUID,
	SessionID:  "session_id",
	WindowID:   "window_id",
}

func newResourceControllerKernel(runner tmuxCommandRunner, store *resourceStore, planner resourceReconcilePlanner, target explicitTmuxTarget) *resourceControllerKernel {
	kernel := &resourceControllerKernel{
		target: target, runner: runner, store: store, planner: planner,
		trigger: controller.RecoveryExplicit, lookupEnv: os.Getenv,
	}
	kernel.observe = func(ctx context.Context) resourcegraph.Inventory {
		// A fresh observer per call is deliberate. The memoization inside one
		// observer is what keeps a single stage consistent; carrying it across
		// stages would make the reobservation report the pre-execute machine.
		readTarget := kernel.target
		if kernel.route != nil && kernel.route.expectedSocketPath != "" {
			readTarget = explicitTmuxTarget{flag: "-S", value: kernel.route.expectedSocketPath}
		}
		inventory := intmetadata.NewInventoryObserver(runner, controllerTransport(readTarget)).Observe(ctx)
		// The public plan continues to describe the operator's requested route;
		// the internal typed plan separately prints the physical generation.
		inventory.Transport = controllerTransport(kernel.target)
		return inventory
	}
	kernel.socketPath = func(ctx context.Context) (string, bool) {
		readTarget := kernel.target
		if kernel.route != nil && kernel.route.expectedSocketPath != "" {
			readTarget = explicitTmuxTarget{flag: "-S", value: kernel.route.expectedSocketPath}
		}
		routed := explicitTmuxRunner{runner: runner, target: readTarget}
		out, err := routed.Run(ctx, "tmux", "display-message", "-p", "#{socket_path}")
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	return kernel
}

func (k *resourceControllerKernel) bindRuntimeRoute(ctx context.Context) error {
	if k.route != nil {
		return guardResolvedRuntimeMutationRoute(ctx, k.runner, *k.route)
	}
	lookup := k.lookupEnv
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	route, err := resolveControllerRuntimeMutationRoute(ctx, k.runner, k.target, lookup)
	if err != nil {
		return err
	}
	k.route = &route
	return nil
}

// controllerTransport turns the command's exact routing into the typed
// transport the graph is resolved against. There is no branch for an absent
// target: this command refuses one before a kernel is built.
func controllerTransport(target explicitTmuxTarget) resourcegraph.Transport {
	if target.flag == "-S" {
		return resourcegraph.Transport{
			Kind: resourcegraph.TransportSocketPath, Value: target.value,
			Source: resourcegraph.TransportSourceSocketPath,
		}
	}
	return resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketName, Value: target.value,
		Source: resourcegraph.TransportSourceSocketName,
	}
}

// controllerPass is one observe-and-plan cycle: the resolved graph, the
// Registry-side desired state, and the authorized plan over both.
type controllerPass struct {
	graph    resourcegraph.Graph
	registry resourceReconcilePlan
	plan     controller.Plan
}

// plan runs the observe and plan stages against the supplied Registry snapshot.
//
// The Registry-side desired state still comes from the existing reconciler:
// deciding what a Project row should say is Registry business, and it is not the
// thing that was ambiguous. What changes is that its runtime writes leave here
// as candidates rather than as commands, so every one of them is answered by the
// policy table before anything can run it.
func (k *resourceControllerKernel) plan(ctx context.Context, registry coremetadata.Registry) (controllerPass, error) {
	if !k.trigger.Valid() {
		return controllerPass{}, fmt.Errorf("resource controller recovery trigger %q is outside the closed table", k.trigger)
	}
	if k.trigger == controller.RecoveryExplicit {
		if err := requireAutomaticRecoveryPaths("explicit-mirror-repair", "explicit-status-projection", "explicit-orphan-mirror-discard"); err != nil {
			return controllerPass{}, err
		}
	}
	inventory := k.observe(ctx)
	graph := resourcegraph.Resolve(registry, inventory)
	approvedImport, err := k.authorizeApprovedOrphanImport(graph)
	if err != nil {
		return controllerPass{}, err
	}
	registryPlan, err := k.planner.build(ctx, registry)
	if err != nil {
		return controllerPass{}, err
	}
	if approvedImport != nil {
		markApprovedD3Import(&registryPlan, graph, *approvedImport)
	}
	plan := k.authorize(graph, &registryPlan)
	return controllerPass{graph: graph, registry: registryPlan, plan: plan}, nil
}

func (k *resourceControllerKernel) authorizeApprovedOrphanImport(graph resourcegraph.Graph) (*controller.RecoveryVerdict, error) {
	if !k.approvedOrphanImport {
		return nil, nil
	}
	count := len(graph.RuntimeOfClass(resourcegraph.ClassRecoverable))
	verdict := controller.AuthorizeRecovery(
		resourcegraph.DivergenceOrphanMirror,
		controller.RecoveryExplicit,
		controller.RecoveryImport,
		true,
		count,
	)
	if verdict.Decision != controller.RecoveryAllowApproved {
		return nil, fmt.Errorf("approved orphan import is outside the recovery authority table: %s", verdict.Reason)
	}
	return &verdict, nil
}

func markApprovedD3Import(plan *resourceReconcilePlan, graph resourcegraph.Graph, verdict controller.RecoveryVerdict) {
	recoverable := map[string]bool{}
	for _, node := range graph.RuntimeOfClass(resourcegraph.ClassRecoverable) {
		recoverable[node.Ref.ID] = true
	}
	writeKeys := map[string]bool{}
	for _, write := range plan.writes {
		if recoverable[write.target] {
			writeKeys[write.itemKey()] = true
		}
	}
	for i := range plan.items {
		item := &plan.items[i]
		createdIdentity := item.Surface == "registry" && item.Action == "create" &&
			(item.Kind == string(coremetadata.KindWindow) || item.Kind == string(coremetadata.KindPane) || item.Kind == string(coremetadata.KindAgent))
		if !createdIdentity && !writeKeys[item.Key] {
			continue
		}
		item.Divergence = resourcegraph.DivergenceOrphanMirror
		item.LossKind, item.LossCount = verdict.LossKind, verdict.LossCount
		item.Reason = verdict.Reason
	}
	plan.items = slices.DeleteFunc(plan.items, func(item resourceReconcileItem) bool {
		return item.Action == "refuse" && item.Divergence == resourcegraph.DivergenceOrphanMirror && recoverable[item.Target]
	})
}

// authorize applies the policy to one Registry-side plan and folds the verdicts
// back into it, returning the kernel plan the executor and the report share.
//
// It is called twice per execute -- once for the pre-lock observation and once
// for the plan rebuilt under the Registry lock -- against the same graph. The
// graph is not re-observed in between on purpose: an authorization taken from a
// second, later observation would be a different plan than the one the guards
// were built from, and the guards are the only thing standing between a stale
// plan and somebody else's pane.
func (k *resourceControllerKernel) authorize(graph resourcegraph.Graph, registryPlan *resourceReconcilePlan) controller.Plan {
	handles := controller.IndexHandles(graph)
	// `reconcile resources` cannot run without one exact server: an explicit
	// socket flag, or the socket the operator's own client is attached to. That
	// selection is the grant, and it is the only reason an unmarked object on a
	// standalone host is repairable here.
	grant := controller.Grant{OperatorTargeted: true}
	candidates := controllerCandidates(*registryPlan, k.approvedOrphanImport)
	if !k.approvedOrphanImport {
		candidates = append(candidates, controllerRecoveryCandidates(graph, k.trigger)...)
	}
	actions, policy := controller.Authorize(handles, controllerGuardFields, grant, candidates)
	plannedByKey := map[string]plannedTmuxWrite{}
	for _, write := range registryPlan.writes {
		plannedByKey[write.itemKey()] = write
	}
	for index := range actions {
		write, ok := plannedByKey[actions[index].Key]
		if !ok || !actions[index].Allowed() {
			continue
		}
		if actions[index].Scope == resourcegraph.ObjectPane && strings.TrimSpace(write.guardSessionID) != "" {
			actions[index].Guards = append(actions[index].Guards, controller.Guard{Field: "session_id", Expect: write.guardSessionID})
		}
		actions[index].Guards = append(actions[index].Guards, slices.Clone(write.semanticGuards)...)
	}
	policy = append(policy, controller.Exercised(handles, grant, graphHasOfflineRow(graph))...)
	plan := controller.NewPlan(graph.Transport, graph.HostMode, actions, policy)
	projectRecoveryActions(registryPlan, plan.Actions, graph)
	if k.trigger == controller.RecoveryExplicit && !k.approvedOrphanImport {
		addApprovedD3ImportCommand(registryPlan, graph, k.target)
	}
	applyPolicy(registryPlan, plan.Actions)
	return plan
}

func addApprovedD3ImportCommand(plan *resourceReconcilePlan, graph resourcegraph.Graph, target explicitTmuxTarget) {
	count := len(graph.RuntimeOfClass(resourcegraph.ClassRecoverable))
	if count == 0 {
		return
	}
	flag := "--socket"
	if target.flag == "-S" {
		flag = "--socket-path"
	}
	command := "projmux reconcile resources " + flag + " " + shellQuote(target.value) + " --import-orphan-mirrors --yes"
	verdict := controller.AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, controller.RecoveryExplicit, controller.RecoveryImport, false, count)
	plan.items = append(plan.items, resourceReconcileItem{
		Key: "recovery:approved:d3-import", Drift: resourceDriftOrphan, Surface: "registry",
		Action: string(controller.RecoveryImport), Kind: "Recovery", Target: fmt.Sprintf("%d exact D3 object(s)", count),
		Outcome: "approval-required", Reason: verdict.Reason, Divergence: resourcegraph.DivergenceOrphanMirror,
		ApprovalCommand: command, LossKind: verdict.LossKind, LossCount: verdict.LossCount,
	})
	sort.SliceStable(plan.items, func(i, j int) bool { return plan.items[i].Key < plan.items[j].Key })
}

// projectRecoveryActions makes automatic L8 visible without counting its
// three Pane option unsets as three divergence objects. The uid action is the
// one item; its reason states that the two routing indexes are part of the same
// object-scoped discard.
func projectRecoveryActions(registryPlan *resourceReconcilePlan, actions []controller.Action, _ resourcegraph.Graph) {
	recovered := map[string]bool{}
	for _, action := range actions {
		if action.RecoveryLevel == controller.RecoveryDiscardMirror && action.Divergence == resourcegraph.DivergenceOrphanMirror {
			recovered[action.Target] = true
		}
	}
	registryPlan.items = slices.DeleteFunc(registryPlan.items, func(item resourceReconcileItem) bool {
		if item.Divergence == resourcegraph.DivergenceOrphanMirror && recovered[item.Target] {
			return true
		}
		return false
	})
	for _, action := range actions {
		identityField := action.Field == tmuxopts.ProjectUIDSession || action.Field == tmuxopts.WindowUID || action.Field == tmuxopts.PaneUID
		if action.RecoveryLevel != controller.RecoveryDiscardMirror || !identityField {
			continue
		}
		item := resourceReconcileItem{
			Key: action.Key, Drift: resourceDriftOrphan, Surface: "tmux", Action: string(controller.RecoveryDiscardMirror),
			Kind: action.Kind, Target: action.Target, Field: action.Field, Before: action.Before,
			Outcome: "planned", Reason: "discard orphan uid mirror and any live conversation routing indexes",
			Divergence: action.Divergence, LossKind: action.LossKind, LossCount: action.LossCount,
		}
		if action.Refused() {
			item.Outcome, item.Reason, item.refused = "refused", action.Reason, true
		}
		registryPlan.items = append(registryPlan.items, item)
	}
	sort.SliceStable(registryPlan.items, func(i, j int) bool { return registryPlan.items[i].Key < registryPlan.items[j].Key })
}

// controllerRecoveryCandidates is the only automatic recovery-action producer.
// It emits an action only after the exact D3 classifier has labelled the live
// object recoverable, and every action carries its trigger and L8 cell into the
// controller's closed authority gate.
func controllerRecoveryCandidates(graph resourcegraph.Graph, trigger controller.RecoveryTrigger) []controller.Candidate {
	orphans := graph.RuntimeOfClass(resourcegraph.ClassRecoverable)
	out := make([]controller.Candidate, 0, len(orphans)*3)
	for _, node := range orphans {
		var fields []string
		scopeArgs := []string{}
		switch node.Ref.Kind {
		case resourcegraph.ObjectSession:
			fields = []string{tmuxopts.ProjectUIDSession}
		case resourcegraph.ObjectWindow:
			fields = []string{tmuxopts.WindowUID}
			scopeArgs = []string{"-w"}
		case resourcegraph.ObjectPane:
			// Provider and topic are user-visible runtime metadata. L8 removes
			// only the Pane uid and the two live conversation routing indexes;
			// Registry Agent status.sessionRef is not a tmux option and is never
			// touched here.
			fields = []string{tmuxopts.PaneUID, tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane}
			scopeArgs = []string{"-p"}
		default:
			continue
		}
		for _, field := range fields {
			before := node.UID
			switch field {
			case tmuxopts.AgentSessionIDPane:
				before = node.AgentSessionID
			case tmuxopts.AgentThreadIDPane:
				before = node.AgentThreadID
			}
			if field != tmuxopts.ProjectUIDSession && field != tmuxopts.WindowUID && field != tmuxopts.PaneUID && before == "" {
				continue
			}
			args := []string{"set-option"}
			args = append(args, scopeArgs...)
			args = append(args, "-u", "-t", node.Ref.ID, field)
			out = append(out, controller.Candidate{
				Key:    fmt.Sprintf("recovery/%s/%s/%s", node.Ref.Kind, node.Ref.ID, field),
				Intent: controller.IntentDiscardMirror, Kind: string(node.Ref.Kind), Target: node.Ref.ID,
				Field: field, Before: before, Args: args,
				Divergence: resourcegraph.DivergenceOrphanMirror, Trigger: trigger,
				Level: controller.RecoveryDiscardMirror, LossCount: len(orphans),
			})
		}
	}
	return out
}

// graphHasOfflineRow reports whether any Registry row resolved without a live
// runtime object. It is what makes the start refusal appear in the policy
// projection only when there was actually something the kernel could have
// started.
func graphHasOfflineRow(graph resourcegraph.Graph) bool {
	for _, node := range graph.Projects {
		if node.Runtime == nil {
			return true
		}
	}
	for _, node := range graph.ControlSessions {
		if node.Runtime == nil {
			return true
		}
	}
	for _, node := range graph.Windows {
		if node.Runtime == nil {
			return true
		}
	}
	for _, node := range graph.Panes {
		if node.Runtime == nil {
			return true
		}
	}
	for _, node := range graph.Agents {
		if node.Runtime == nil {
			return true
		}
	}
	return false
}

// controllerCandidates turns the Registry planner's runtime writes into policy
// candidates.
//
// The intent split is by field, not by caller. A write that sets a uid is a
// binding repair whatever produced it, and a write that sets a name, a root, or
// an Agent projection is a mirror repair -- so a future producer cannot obtain a
// weaker verdict by describing the same write differently.
func controllerCandidates(plan resourceReconcilePlan, approved bool) []controller.Candidate {
	items := map[string]resourceReconcileItem{}
	for _, item := range plan.items {
		items[item.Key] = item
	}
	out := make([]controller.Candidate, 0, len(plan.writes))
	for _, write := range plan.writes {
		intent := controller.IntentRepairMirror
		switch write.field {
		case tmuxopts.ProjectUIDSession, tmuxopts.WindowUID, tmuxopts.PaneUID:
			intent = controller.IntentRepairBinding
		}
		candidate := controller.Candidate{
			Key: write.itemKey(), Intent: intent, Kind: plannedWriteKind(write),
			Target: write.target, Field: write.field, Before: write.before, After: write.after,
			Args: write.args,
		}
		if item := items[candidate.Key]; approved && item.Divergence == resourcegraph.DivergenceOrphanMirror {
			candidate.Divergence = resourcegraph.DivergenceOrphanMirror
			candidate.Trigger = controller.RecoveryExplicit
			candidate.Level = controller.RecoveryImport
			candidate.LossCount = item.LossCount
			candidate.Approved = true
		}
		out = append(out, candidate)
	}
	return out
}

func plannedWriteKind(write plannedTmuxWrite) string {
	switch {
	case len(write.args) > 0 && write.args[0] == "rename-window":
		return "Window"
	case slices.Contains(write.args, "-w"):
		return "Window"
	case slices.Contains(write.args, "-p"):
		return "Pane"
	default:
		return "Project"
	}
}

// applyPolicy folds the policy verdicts back into the report items and returns
// the writes that survived.
//
// A denied candidate does not vanish: its plan item becomes refused drift with
// the policy's own reason. An operator reading the report sees the same item
// they would have seen if the drift were unrepairable for any other cause, which
// is the point -- "the controller was not allowed to" is a drift outcome, not an
// absence.
func applyPolicy(registryPlan *resourceReconcilePlan, actions []controller.Action) []controller.Action {
	plan := controller.Plan{Actions: actions}
	refused := map[string]controller.Action{}
	actionByKey := map[string]controller.Action{}
	for _, action := range actions {
		actionByKey[action.Key] = action
	}
	for index := range registryPlan.items {
		if action, ok := actionByKey[registryPlan.items[index].Key]; ok {
			registryPlan.items[index].Guards = slices.Clone(action.Guards)
		}
		if registryPlan.items[index].Action == "promote-authorship" {
			seen := map[string]bool{}
			for _, guard := range registryPlan.items[index].Guards {
				seen[guard.Field+"\x00"+guard.Expect] = true
			}
			for _, action := range actions {
				if action.Target != registryPlan.items[index].Target {
					continue
				}
				for _, guard := range action.Guards {
					key := guard.Field + "\x00" + guard.Expect
					if !seen[key] {
						seen[key] = true
						registryPlan.items[index].Guards = append(registryPlan.items[index].Guards, guard)
					}
				}
			}
		}
	}
	for _, action := range plan.Refusals() {
		refused[action.Key] = action
	}
	if len(refused) == 0 {
		return plan.Writes()
	}
	for index := range registryPlan.items {
		action, denied := refused[registryPlan.items[index].Key]
		if !denied || registryPlan.items[index].refused {
			continue
		}
		registryPlan.items[index].refused = true
		registryPlan.items[index].Outcome = "refused"
		registryPlan.items[index].Drift = resourceDriftForeign
		registryPlan.items[index].Reason = action.Reason
	}
	registryPlan.writes = slices.DeleteFunc(registryPlan.writes, func(write plannedTmuxWrite) bool {
		_, denied := refused[write.itemKey()]
		return denied
	})
	return plan.Writes()
}

// guardPlan re-proves every guard of every authorized write before the first
// one runs.
//
// It reads through the same routed runner the writes will use, so the socket the
// guard proved is the socket the write lands on. Two different guards would be
// two different servers.
func (k *resourceControllerKernel) guardPlan(ctx context.Context, socket string, writes []controller.Action) error {
	if len(writes) == 0 {
		return nil
	}
	if k.route == nil {
		return errors.New("controller runtime route authority is not bound")
	}
	if err := guardResolvedRuntimeMutationRoute(ctx, k.runner, *k.route); err != nil {
		return fmt.Errorf("exact tmux socket changed before repair: %w", err)
	}
	routed := explicitTmuxRunner{runner: k.runner, target: explicitTmuxTarget{flag: "-S", value: k.route.expectedSocketPath}}
	if socket != "" {
		current, ok := k.socketPath(ctx)
		if !ok {
			return fmt.Errorf("exact tmux server %s became unreachable before repair", k.target.value)
		}
		if current != socket {
			return fmt.Errorf("exact tmux socket changed before repair: #{socket_path} is %q, planned against %q", current, socket)
		}
	}
	for _, write := range writes {
		for _, guard := range write.Guards {
			out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", write.Target, "-F", "#{"+guard.Field+"}")
			if err != nil {
				return fmt.Errorf("revalidate exact tmux target %s: %w", write.Target, err)
			}
			if observed := strings.TrimSpace(string(out)); observed != strings.TrimSpace(guard.Expect) {
				return fmt.Errorf("exact tmux target %s changed before repair: %s is %q, planned from %q",
					write.Target, guard.Field, observed, guard.Expect)
			}
		}
	}
	return nil
}

// reobserve replans against fresh bytes and reports whether a repeat would
// write anything.
func (k *resourceControllerKernel) reobserve(ctx context.Context) controllerReobservation {
	registry, err := k.loadRegistry()
	if err != nil {
		return controllerReobservation{Unavailable: "registry could not be re-read: " + err.Error()}
	}
	pass, err := k.plan(ctx, registry)
	if err != nil {
		return controllerReobservation{Unavailable: "plan could not be rebuilt: " + err.Error()}
	}
	if pass.plan.Converged() {
		return controllerReobservation{Converged: true}
	}
	pending := pass.plan.Pending()
	residual := make([]string, 0, len(pending))
	for _, action := range pending {
		residual = append(residual, action.Key)
	}
	slices.Sort(residual)
	return controllerReobservation{Residual: residual}
}

func (k *resourceControllerKernel) loadRegistry() (coremetadata.Registry, error) {
	if k.store == nil {
		return coremetadata.Registry{}, errors.New("resource reconciliation read store is not configured")
	}
	load := k.store.snapshot
	if load == nil {
		load = k.store.load
	}
	if load == nil {
		return coremetadata.Registry{}, errors.New("resource reconciliation read store is not configured")
	}
	return load()
}

// The trigger half of the kernel: one producer, one sequence, one report.
//
// Everything above this point is the kernel's vocabulary -- observe, plan,
// authorize, guard, reobserve. What was missing is a single place that runs them
// in the contractual order, because the order lived inside one command's
// reporting body and every other producer of the same drift had to reimplement
// some of it. `internal tmux reconcile-bindings` ran the Registry half and wrote
// through the reconciler's own mirror; the pane-exit sweep ran the lifecycle
// projection alone; `config apply` ran the first and not the second. Three
// producers, three sequences, one machine.
//
// converge below is that sequence, and it is the only body that mutates on a
// trigger's behalf. A producer supplies a target and a reason; it does not get
// to choose which stages run or in what order.

// controllerRun is what one convergence pass did, in enough detail for a report
// to be rendered from it without re-deriving anything.
type controllerRun struct {
	graph  resourcegraph.Graph
	socket string
	// plan is the plan as rebuilt and committed under the Registry lock. On a
	// failure before the lock it is the zero plan, which is what makes "nothing
	// was planned" distinguishable from "nothing was found".
	plan       resourceReconcilePlan
	authorized controller.Plan
	// completed names the stages that finished, in order.
	completed []string
	// registryChanged reports whether the Registry commit wrote bytes.
	registryChanged bool
	// executed counts the authorized tmux writes that ran.
	executed int
	// reobserved is the post-execute replan. It is nil when the pass changed
	// nothing, because a reobservation could then only restate the observation
	// the pass already took.
	reobserved *controllerReobservation
}

// changed reports whether this pass wrote anything at all.
func (r controllerRun) changed() bool {
	return r.registryChanged || r.executed > 0
}

// controllerRunError tags a convergence failure with the stage that produced it
// and carries the partial run, so a caller can report exactly how far the
// sequence got instead of guessing from the error text.
type controllerRunError struct {
	stage string
	err   error
	run   controllerRun
}

func (e *controllerRunError) Error() string {
	return "controller convergence failed at " + e.stage + ": " + e.err.Error()
}

func (e *controllerRunError) Unwrap() error { return e.err }

type promotionRegistryReceipt struct {
	before     coremetadata.Registry
	committed  coremetadata.Registry
	promotions []resourceAuthorshipPromotion
}

// converge runs the six contractual stages against one exact server.
//
// The observation is taken once, before the Registry lock, and the plan is
// authorized against it twice: once as observed and once as rebuilt under the
// lock. Re-observing between those two would produce a plan whose guards
// describe a machine nobody looked at, and the guards are the only reason a
// recycled handle cannot redirect a write.
func (k *resourceControllerKernel) converge(ctx context.Context) (controllerRun, error) {
	if k == nil {
		return controllerRun{}, errors.New("resource controller kernel is not configured")
	}
	if k.store == nil || k.store.updateConvergent == nil {
		return controllerRun{}, errors.New("resource controller write store is not configured")
	}
	var run controllerRun
	if err := k.bindRuntimeRoute(ctx); err != nil {
		return run, &controllerRunError{stage: "runtime authority", err: err, run: run}
	}
	snapshot, err := k.loadRegistry()
	if err != nil {
		return run, &controllerRunError{stage: "registry read", err: MapMetadataError(err), run: run}
	}
	inventory := k.observe(ctx)
	run.graph = resourcegraph.Resolve(snapshot, inventory)
	run.socket, _ = k.socketPath(ctx)
	run.completed = []string{"exact socket observed: " + run.graph.Transport.String()}

	failedStage := ""
	var promotionReceipt promotionRegistryReceipt
	_, registryChanged, updateErr := k.store.updateConvergent(func(working *coremetadata.Registry) error {
		lockedBefore := working.Clone()
		// Resolve the same bounded runtime observation against the Registry bytes
		// protected by this lock. If another writer claimed an orphan between the
		// preview and this transaction, the locked graph is Managed and neither
		// automatic L8 nor approved L7 may act on stale D3 evidence.
		lockedGraph := resourcegraph.Resolve(working.Clone(), inventory)
		approvedImport, authorityErr := k.authorizeApprovedOrphanImport(lockedGraph)
		if authorityErr != nil {
			failedStage = "recovery authority"
			return authorityErr
		}
		currentPlan, err := k.planner.build(ctx, working.Clone())
		if err != nil {
			failedStage = "locked plan"
			return err
		}
		if approvedImport != nil {
			markApprovedD3Import(&currentPlan, lockedGraph, *approvedImport)
		}
		run.graph = lockedGraph
		run.authorized = k.authorize(lockedGraph, &currentPlan)
		run.plan = currentPlan
		if len(currentPlan.promotions) != 0 {
			promotionReceipt = promotionRegistryReceipt{
				before: lockedBefore, committed: currentPlan.registry.Clone(),
				promotions: slices.Clone(currentPlan.promotions),
			}
		}
		run.completed = append(run.completed, "plan rechecked under Registry lock")
		*working = run.plan.registry.Clone()
		return nil
	})
	if updateErr != nil {
		if failedStage == "" {
			failedStage = "registry commit"
		}
		return run, &controllerRunError{stage: failedStage, err: MapMetadataError(updateErr), run: run}
	}
	run.registryChanged = registryChanged
	registryStage := "Registry commit"
	if !registryChanged {
		registryStage += " (no-op)"
	}
	run.completed = append(run.completed, registryStage)
	for _, item := range run.plan.items {
		if item.registry {
			run.completed = append(run.completed, item.Key)
		}
	}

	writes := run.authorized.Writes()
	if err := k.guardPlan(ctx, run.socket, writes); err != nil {
		if len(run.plan.promotions) != 0 {
			if rollbackErr := k.rollbackPromotionRegistry(promotionReceipt); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("atomic promotion Registry rollback failed: %w", rollbackErr))
			} else {
				run.registryChanged = false
				run.completed = append(run.completed, "atomic promotion Registry preimage restored")
			}
		}
		return run, &controllerRunError{stage: "tmux prevalidation", err: err, run: run}
	}
	run.completed = append(run.completed, "tmux targets prevalidated")
	if err := executeControllerRuntimeMutations(ctx, k.runner, *k.route, writes); err != nil {
		if len(run.plan.promotions) != 0 {
			if rollbackErr := k.rollbackPromotionRegistry(promotionReceipt); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("atomic promotion Registry rollback failed: %w", rollbackErr))
			} else {
				run.registryChanged = false
				run.completed = append(run.completed, "atomic promotion Registry preimage restored")
			}
		}
		return run, &controllerRunError{stage: "controller.identity", err: err, run: run}
	}
	for _, write := range writes {
		run.executed++
		run.completed = append(run.completed, write.Key)
	}
	if run.changed() {
		reobserved := k.reobserve(ctx)
		run.reobserved = &reobserved
		run.completed = append(run.completed, controllerReobserveStage(reobserved))
	}
	return run, nil
}

// rollbackPromotionRegistry restores the exact preimage only when the current
// Registry still equals this transaction's committed postimage. It therefore
// cannot erase a concurrent writer while closing the promotion's runtime
// failure boundary back to a pure before state.
func (k *resourceControllerKernel) rollbackPromotionRegistry(receipt promotionRegistryReceipt) error {
	if k == nil || k.store == nil || k.store.updateConvergent == nil {
		return errors.New("promotion rollback store is not configured")
	}
	_, _, err := k.store.updateConvergent(func(current *coremetadata.Registry) error {
		return rollbackPromotionScope(current, receipt)
	})
	return err
}

func rollbackPromotionScope(current *coremetadata.Registry, receipt promotionRegistryReceipt) error {
	if current == nil || len(receipt.promotions) == 0 {
		return errors.New("promotion rollback has no exact transaction receipt")
	}
	affectedReservations := map[string]bool{}
	for _, promotion := range receipt.promotions {
		beforePane, beforePaneOK := receipt.before.Pane(promotion.PaneUID)
		committedPane, committedPaneOK := receipt.committed.Pane(promotion.PaneUID)
		currentPane, currentPaneOK := current.Pane(promotion.PaneUID)
		beforeWindow, beforeWindowOK := receipt.before.Window(promotion.WindowUID)
		committedWindow, committedWindowOK := receipt.committed.Window(promotion.WindowUID)
		currentWindow, currentWindowOK := current.Window(promotion.WindowUID)
		if !beforePaneOK || !committedPaneOK || !currentPaneOK || !beforeWindowOK || !committedWindowOK || !currentWindowOK {
			return fmt.Errorf("promotion rollback scope %s is incomplete", promotion.PaneUID)
		}
		if !reflect.DeepEqual(currentPane.Metadata.OwnerRef, committedPane.Metadata.OwnerRef) || currentPane.Spec.Role != committedPane.Spec.Role ||
			currentWindow.Spec.AnchorPaneRef != committedWindow.Spec.AnchorPaneRef ||
			currentWindow.Spec.DefaultShellPaneRef != committedWindow.Spec.DefaultShellPaneRef {
			return fmt.Errorf("promotion rollback CAS changed inside exact Window scope %s", promotion.WindowUID)
		}
		beforePaneClone := beforePane.Clone()
		currentPane.Metadata.OwnerRef = beforePaneClone.Metadata.OwnerRef
		currentPane.Spec.Role = beforePane.Spec.Role
		currentWindow.Spec.AnchorPaneRef = beforeWindow.Spec.AnchorPaneRef
		currentWindow.Spec.DefaultShellPaneRef = beforeWindow.Spec.DefaultShellPaneRef
		affectedReservations[promotion.PaneUID] = true
		affectedReservations[promotion.AgentUID] = true

		beforeAgent, existedBefore := receipt.before.Agent(promotion.AgentUID)
		committedAgent, committedAgentOK := receipt.committed.Agent(promotion.AgentUID)
		currentAgent, currentAgentOK := current.Agent(promotion.AgentUID)
		if !committedAgentOK || !currentAgentOK {
			return fmt.Errorf("promotion rollback Agent %s is absent", promotion.AgentUID)
		}
		if existedBefore {
			if !reflect.DeepEqual(currentAgent.Status, committedAgent.Status) {
				return fmt.Errorf("promotion rollback Agent status CAS changed for %s", promotion.AgentUID)
			}
			currentAgent.Status = beforeAgent.Clone().Status
		} else {
			if !reflect.DeepEqual(currentAgent.Clone(), committedAgent.Clone()) {
				return fmt.Errorf("promotion rollback minted Agent CAS changed for %s", promotion.AgentUID)
			}
			current.Agents = slices.DeleteFunc(current.Agents, func(agent coremetadata.Agent) bool {
				return agent.Metadata.UID == promotion.AgentUID
			})
		}
	}
	committedAffected := slices.DeleteFunc(slices.Clone(receipt.committed.NameReservations), func(value coremetadata.NameReservation) bool {
		return !affectedReservations[value.UID]
	})
	currentAffected := slices.DeleteFunc(slices.Clone(current.NameReservations), func(value coremetadata.NameReservation) bool {
		return !affectedReservations[value.UID]
	})
	if !reflect.DeepEqual(currentAffected, committedAffected) {
		return errors.New("promotion rollback reservation CAS changed inside exact scope")
	}
	// Restore affected reservations in the locked preimage order while keeping
	// every unrelated current reservation/value, including concurrent inserts.
	currentByUID := map[string]coremetadata.NameReservation{}
	for _, reservation := range current.NameReservations {
		if !affectedReservations[reservation.UID] {
			currentByUID[reservation.UID] = reservation
		}
	}
	var restored []coremetadata.NameReservation
	used := map[string]bool{}
	for _, reservation := range receipt.before.NameReservations {
		if affectedReservations[reservation.UID] {
			restored = append(restored, reservation)
			continue
		}
		if currentReservation, ok := currentByUID[reservation.UID]; ok {
			restored = append(restored, currentReservation)
			used[reservation.UID] = true
		}
	}
	for _, reservation := range current.NameReservations {
		if !affectedReservations[reservation.UID] && !used[reservation.UID] {
			restored = append(restored, reservation)
			used[reservation.UID] = true
		}
	}
	current.NameReservations = restored
	if err := current.Validate(); err != nil {
		return fmt.Errorf("promotion rollback post-state is invalid: %w", err)
	}
	return nil
}
