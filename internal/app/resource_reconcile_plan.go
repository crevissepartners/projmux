package app

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type resourceDriftKind string

const (
	resourceDriftMissing resourceDriftKind = "missing"
	resourceDriftStale   resourceDriftKind = "stale"
	resourceDriftForeign resourceDriftKind = "foreign"
	resourceDriftOrphan  resourceDriftKind = "orphan"
)

type resourceReconcileItem struct {
	Key             string                   `json:"key"`
	Drift           resourceDriftKind        `json:"drift"`
	Surface         string                   `json:"surface"`
	Action          string                   `json:"action"`
	Kind            string                   `json:"kind,omitempty"`
	Target          string                   `json:"target"`
	Field           string                   `json:"field,omitempty"`
	Before          string                   `json:"before,omitempty"`
	After           string                   `json:"after,omitempty"`
	Outcome         string                   `json:"outcome"`
	Reason          string                   `json:"reason,omitempty"`
	Divergence      resourcegraph.Divergence `json:"divergence"`
	ApprovalCommand string                   `json:"approvalCommand,omitempty"`
	LossKind        string                   `json:"lossKind,omitempty"`
	LossCount       int                      `json:"lossCount,omitempty"`
	Authority       string                   `json:"authority,omitempty"`
	PromotionKind   string                   `json:"promotionKind,omitempty"`
	AllocationSlot  string                   `json:"allocationSlot,omitempty"`
	Transitions     []resourceRefTransition  `json:"transitions,omitempty"`
	Guards          []controller.Guard       `json:"guards,omitempty"`
	tmuxArgs        []string
	refused         bool
	registry        bool
}

type resourceRefTransition struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type resourceAllocationSlot struct {
	Slot          string `json:"slot"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	PromotionKind string `json:"promotionKind,omitempty"`
}

type resourceAuthorshipPromotion struct {
	PaneUID, PaneTarget, WindowUID, ProjectUID string
	SessionID, WindowID                        string
	ObservedProvider, AgentProvider            string
	AgentUID, AgentName                        string
	LinkKind                                   coremetadata.AgentLinkKind
	AnchorBefore, AnchorAfter                  string
	DefaultBefore, DefaultAfter                string
}

// planResourceAgentProjections compares Registry Agent authority with the exact
// live managed Pane. It only emits writes; tmux values are never imported into
// Agent status or annotations. Offline/stale state projects unknown, which
// clears response-complete and attention options instead of preserving them.
func planResourceAgentProjections(ctx context.Context, recorder *resourcePlanTmuxRunner, registry coremetadata.Registry, now time.Time) error {
	lookup := intmetadata.NewMirror(recorder)
	for _, agent := range registry.Agents {
		paneUIDs := []string{}
		if agent.Status.PaneRef != "" {
			paneUIDs = append(paneUIDs, agent.Status.PaneRef)
		} else {
			for _, pane := range registry.PanesOf(agent.Metadata.UID) {
				paneUIDs = append(paneUIDs, pane.Metadata.UID)
			}
		}
		for _, paneUID := range paneUIDs {
			target, found, err := lookup.FindPaneTargetForUID(ctx, paneUID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			topic := ""
			interaction := coremetadata.AgentInteraction{Kind: coremetadata.InteractionUnknown}
			if agent.Status.Phase == coremetadata.PhaseRunning && paneUID == agent.Status.PaneRef {
				topic = strings.TrimSpace(agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic])
				interaction = agent.EffectiveInteraction(now)
			}
			manual := ""
			if topic != "" {
				manual = "on"
			}
			for _, desired := range []struct {
				field, value string
			}{
				{aiPaneTopicOption, topic},
				{aiPaneTopicManualOption, manual},
			} {
				if err := planExactPaneOption(ctx, recorder, target, desired.field, desired.value); err != nil {
					return err
				}
			}
			lifecycleInput, durableLifecycle, authorizedLifecycle := resourceAgentLifecycleProjectionInput(registry, agent, paneUID, target, interaction.Kind)
			// A durable lifecycle declaration is fail-closed. If its exact
			// Agent/Pane/endpoint/activation fence is no longer current, this
			// reconcile does not replace it with a legacy tuple. A later
			// authoritative producer or binding owns that transition.
			if durableLifecycle && !authorizedLifecycle {
				continue
			}
			if err := planAgentLifecycleProjection(ctx, recorder, target, lifecycleInput); err != nil {
				return err
			}
		}
	}
	return nil
}

// resourceAgentLifecycleProjectionInput joins the durable generation state and
// operation to the exact live activation fence. It reads no tmux presentation,
// process, exit, executable, or version evidence. The target is only the
// resolved route named by the Registry Pane uid.
func resourceAgentLifecycleProjectionInput(registry coremetadata.Registry, agent coremetadata.Agent, paneUID, target string, interaction coremetadata.AgentInteractionKind) (codexgeneration.LifecycleProjectionInput, bool, bool) {
	legacy := codexgeneration.LifecycleProjectionInput{Interaction: interaction}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Lifecycle == nil {
		return legacy, false, true
	}
	codex := agent.Status.SessionRef.Codex
	lifecycle := codex.Lifecycle
	input := codexgeneration.LifecycleProjectionInput{
		Interaction: interaction, Endpoint: codex.Endpoint,
		GenerationState: lifecycle.State, Operation: lifecycle.Operation,
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != paneUID ||
		agent.Spec.Provider != aiModeCodex || !input.Authoritative() {
		return input, true, false
	}
	pane, ok := registry.Pane(paneUID)
	if !ok || pane.Metadata.OwnerUID() != agent.Metadata.UID ||
		pane.Status.Activation.AgentUID != agent.Metadata.UID ||
		pane.Status.Activation.RuntimeID != target || pane.Status.Activation.Codex == nil ||
		pane.Status.Activation.Codex.ThreadID != codex.ThreadID ||
		pane.Status.Activation.Codex.Authority == nil {
		return input, true, false
	}
	authority := pane.Status.Activation.Codex.Authority
	decision := codexgeneration.DecideRuntimeMutation(codexgeneration.RuntimeMutationInput{
		DurableEndpoint: codex.Endpoint,
		StoredAuthority: authority, PresentedAuthority: authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: target,
	})
	return input, true, decision.Class.Effect == codexgeneration.MutationSemanticEffect
}

func planAgentLifecycleProjection(ctx context.Context, recorder *resourcePlanTmuxRunner, target string, input codexgeneration.LifecycleProjectionInput) error {
	projection := codexgeneration.ProjectLifecycle(input)
	for _, desired := range []struct{ field, value string }{
		{aiPaneStateOption, projection.State},
		{aiPaneBadgeKindOption, projection.Badge},
		{attentionStateOption, projection.Attention},
	} {
		if err := planExactPaneOption(ctx, recorder, target, desired.field, desired.value); err != nil {
			return err
		}
	}
	return nil
}

func planExactPaneOption(ctx context.Context, recorder *resourcePlanTmuxRunner, target, field, desired string) error {
	out, err := recorder.Run(ctx, "tmux", "display-message", "-p", "-t", target, "-F", "#{"+field+"}")
	if err != nil {
		return fmt.Errorf("inspect Agent projection %s on %s: %w", field, target, err)
	}
	before := strings.TrimSpace(string(out))
	if before == desired {
		return nil
	}
	args := []string{"set-option", "-p", "-t", target, field, desired}
	if desired == "" {
		args = []string{"set-option", "-p", "-u", "-t", target, field}
	}
	if _, err := recorder.Run(ctx, "tmux", args...); err != nil {
		return err
	}
	recorder.setLastWriteBefore(before)
	return nil
}

type resourceReconcilePlan struct {
	registry        coremetadata.Registry
	items           []resourceReconcileItem
	writes          []plannedTmuxWrite
	allocations     []resourceAllocationSlot
	promotions      []resourceAuthorshipPromotion
	materialization *registryTopologyPlan
}

func (p resourceReconcilePlan) safeItems() int {
	count := 0
	for _, item := range p.items {
		if !item.refused {
			count++
		}
	}
	return count
}

func (p resourceReconcilePlan) refusedItems() int {
	count := 0
	for _, item := range p.items {
		if item.refused {
			count++
		}
	}
	return count
}

type resourceReconcilePlanner struct {
	reader        tmuxCommandRunner
	store         *resourceStore
	newReconciler func(tmuxCommandRunner, sessionLister) *registryReconciler
	// materializeProject names the one exact Project whose desired topology this
	// plan materializes. Empty means the ordinary identity/mirror reconcile.
	materializeProject string
	// materializeSession is the exact session name the activation is opening.
	// Only an activation that already owns a session identity sets it; the
	// public reconcile route leaves it empty and accepts whatever session the
	// Registry projects.
	materializeSession string
	exactTarget        tmuxTransport
	// agents is the provider-launch seam the Agent half of a materialization
	// plan consumes. It is read-only at plan time: it builds argv and applies
	// the Settings gate, and creates nothing.
	agents               topologyAgentLauncher
	agentReplayAuthority topologyAgentReplayAuthority
	approvedOrphanImport bool
	// symbolicAllocations makes a preview use stable typed slots without
	// invoking the opaque UID allocator. Execute leaves it false and binds the
	// same slots under the Registry lock.
	symbolicAllocations bool
}

func (p resourceReconcilePlanner) build(ctx context.Context, before coremetadata.Registry) (resourceReconcilePlan, error) {
	if p.reader == nil || p.store == nil || p.store.mutator == nil {
		return resourceReconcilePlan{}, fmt.Errorf("resource reconciliation planner is not configured")
	}
	recorder := newResourcePlanTmuxRunner(p.reader)
	sessions := inttmux.NewClient(recorder)
	newReconciler := p.newReconciler
	if newReconciler == nil {
		newReconciler = newRegistryReconciler
	}
	reconciler := newReconciler(recorder, sessions)
	reconciler.initializeRefusalBookkeeping()
	reconciler.refuseForeign = true
	reconciler.targetLiveOnly = true
	reconciler.approvedOrphanImport = p.approvedOrphanImport
	reconciler.atomicAuthorshipPromotion = true
	projectSessions, err := observeResourceProjectSessions(ctx, recorder)
	if err != nil {
		if p.materializeProject == "" || !isMissingTmuxServer(err) {
			return resourceReconcilePlan{}, err
		}
		projectSessions = nil
	}
	topology, err := planRegistryTopology(ctx, p.reader, before, p.materializeProject, reconciler, projectSessions, p.exactTarget, p.agents, p.agentReplayAuthority)
	if err != nil {
		return resourceReconcilePlan{}, err
	}
	// Materialization is a pure selected-Project graph plan. In particular it
	// never runs the default reconciler's blank adoption, orphan minting, or
	// Agent phase observation as an incidental side effect.
	if p.materializeProject != "" {
		requireMaterializeSession(topology, p.materializeSession)
		items := slices.Clone(topology.items)
		if err := validateResourcePlanItems(items); err != nil {
			return resourceReconcilePlan{}, err
		}
		return resourceReconcilePlan{registry: before.Clone(), items: items, materialization: topology}, nil
	}
	reconciler.refusedSessions = refusedResourceProjectSessions(before, projectSessions, reconciler)
	reconciler.exactProjects = map[string]string{}
	for _, session := range projectSessions {
		if reconciler.refusedSessions[session.name] || session.uid == "" {
			continue
		}
		if _, ok := before.Project(session.uid); ok {
			reconciler.exactProjects[session.name] = session.uid
		}
	}
	scopedBefore := scopeResourceRegistry(before, projectSessions, reconciler)
	scopedAfter := scopedBefore.Clone()
	mutator := p.store.mutator()
	allocateUID := mutator.NewUID
	if allocateUID == nil {
		allocateUID = coremetadata.NewUID
	}
	if p.symbolicAllocations {
		counts := map[coremetadata.Kind]int{}
		allocateUID = func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-plan-slot-%d", strings.ToLower(string(kind)), counts[kind]), nil
		}
	}
	var allocationOrder []resourcePlanUIDAllocation
	mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
		uid, err := allocateUID(kind)
		if err == nil {
			allocationOrder = append(allocationOrder, resourcePlanUIDAllocation{kind: kind, uid: uid})
		}
		return uid, err
	}
	if err := reconciler.reconcile(ctx, &scopedAfter, mutator, "reconcile-resources"); err != nil {
		return resourceReconcilePlan{}, err
	}
	after := mergeScopedResourceRegistry(before, scopedBefore, scopedAfter, projectSessions, reconciler)
	promotions := detectAuthorshipPromotions(before, after, recorder.objects)
	if len(promotions) != 0 {
		targetWindows := map[string]bool{}
		for _, promotion := range promotions {
			targetWindows[promotion.WindowUID] = true
		}
		// Promotion is a Window-local composite transaction. Sibling Project D5
		// remains visible to a later ordinary pass and is never absorbed into the
		// promotion's commit/rollback domain.
		after = mergeExactWindowGraphs(before, after, targetWindows)
		recorder.writes = nil
		if err := planAuthorshipPromotionOptions(ctx, recorder, after, promotions); err != nil {
			return resourceReconcilePlan{}, err
		}
	} else {
		if err := planResourceBoundMirrorDrift(ctx, recorder, after, reconciler); err != nil {
			return resourceReconcilePlan{}, err
		}
		if err := planResourceAgentProjections(ctx, recorder, after, time.Now()); err != nil {
			return resourceReconcilePlan{}, err
		}
		if err := planResourceProjectMirrors(ctx, recorder, after, projectSessions, reconciler); err != nil {
			return resourceReconcilePlan{}, err
		}
	}
	// A malformed or conflicting authorship receipt freezes the exact Pane
	// target for this pass. This suppresses both identity and presentation
	// writes to that target without consuming unrelated-target #759 repairs.
	refusedTargets := refusedAuthorshipTargets(before, recorder.objects)
	recorder.writes = slices.DeleteFunc(recorder.writes, func(write plannedTmuxWrite) bool {
		return refusedTargets[write.target] != ""
	})
	// Mutators may refresh UpdatedAt while finding no durable resource change.
	// Match resourceStore.converge so repeat plans remain byte-stable.
	updatedAt := after.UpdatedAt
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(after.Normalize(), before.Normalize()) {
		after.UpdatedAt = updatedAt
	}

	normalize := newPlanUIDNormalizerWithAllocations(before, after, allocationOrder)
	items := registryReconcileItems(before, after, normalize)
	items = append(items, recorder.planItems(before, normalize)...)
	items = append(items, promotionPlanItems(promotions, normalize)...)
	items = append(items, resourceProjectForeignItems(before, projectSessions, reconciler)...)
	items = append(items, recorder.foreignItems(before, reconciler)...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if err := validateResourcePlanItems(items); err != nil {
		return resourceReconcilePlan{}, err
	}
	return resourceReconcilePlan{
		registry: after, items: items, writes: slices.Clone(recorder.writes),
		allocations: normalize.slots(after, promotions), promotions: promotions,
	}, nil
}

func refusedAuthorshipTargets(registry coremetadata.Registry, objects []observedPlanObject) map[string]string {
	refused := map[string]string{}
	known := registryUIDSet(registry)
	counts := map[string]int{}
	for _, object := range objects {
		if object.kind == coremetadata.KindPane && strings.TrimSpace(object.uid) != "" {
			counts[object.uid]++
		}
	}
	for _, object := range objects {
		uid := strings.TrimSpace(object.uid)
		if object.kind != coremetadata.KindPane || !known[uid] || counts[uid] != 1 {
			continue
		}
		windowUID := observedWindowUIDForPane(object, objects)
		reason := coremetadata.AgentPanePromotionRefusal(&registry, windowUID, uid, coremetadata.LegacyPane{
			Provider: object.agentProvider, LaunchAuthorship: object.agentLaunchAuthorship,
			Topic: object.agentTopic, SessionID: object.agentSessionID, ThreadID: object.agentThreadID,
		})
		if reason != "" {
			refused[object.target] = reason
		}
	}
	return refused
}

func validateResourcePlanItems(items []resourceReconcileItem) error {
	for _, item := range items {
		if !item.Divergence.Valid() {
			return fmt.Errorf("resource plan item %q has no divergence classification", item.Key)
		}
		if strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("resource plan item %q has no reason", item.Key)
		}
	}
	return nil
}

// scopeResourceRegistry isolates the authoritative graphs safely attributable
// to sessions observed on the exact target server. Absence from one socket is
// not evidence that a Project owned by another socket is offline.
func scopeResourceRegistry(registry coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) coremetadata.Registry {
	projects := resourceProjectUIDsForSessions(registry, sessions, reconciler)
	out := resourceRegistryProjectGraph(registry, projects)
	// Global reservations remain allocator input even though unrelated resource
	// bodies are absent from the mutation scope.
	out.NameReservations = slices.Clone(registry.NameReservations)
	return out
}

func resourceProjectUIDsForSessions(registry coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) map[string]bool {
	projects := map[string]bool{}
	for _, session := range sessions {
		if reconciler.refusedSessions[session.name] {
			continue
		}
		if project, ok := resourceProjectForSession(registry, session, reconciler); ok {
			projects[project.Metadata.UID] = true
		}
	}
	return projects
}

// resourceRegistryProjectGraph is the graph of the named Projects: their
// Windows, those Windows' Agents, and the Panes of both, plus exactly the name
// reservations of the resources it kept.
//
// ControlSessions are the one root it carries whole. Clone() brings them along
// and nothing here can select them, so their reservations are claimed
// unconditionally -- otherwise the projection holds a root that
// Registry.Validate rejects from the other direction, "ControlSession %q has no
// name reservation in scope", which is the same class of defect as a
// reservation with no resource. Their Windows and Panes are still out of the
// Project graph and stay out, along with the reservations naming them.
func resourceRegistryProjectGraph(registry coremetadata.Registry, projects map[string]bool) coremetadata.Registry {
	out := registry.Clone()
	out.Projects = nil
	out.Windows = nil
	out.Panes = nil
	out.Agents = nil
	out.NameReservations = nil
	includedUIDs := map[string]bool{}
	windows := map[string]bool{}
	agents := map[string]bool{}
	for _, control := range registry.ControlSessions {
		includedUIDs[control.Metadata.UID] = true
	}
	for _, project := range registry.Projects {
		if projects[project.Metadata.UID] {
			out.Projects = append(out.Projects, project.Clone())
			includedUIDs[project.Metadata.UID] = true
		}
	}
	for _, window := range registry.Windows {
		if projects[window.Metadata.OwnerUID()] {
			out.Windows = append(out.Windows, window.Clone())
			windows[window.Metadata.UID] = true
			includedUIDs[window.Metadata.UID] = true
		}
	}
	for _, agent := range registry.Agents {
		if windows[agent.Metadata.OwnerUID()] {
			out.Agents = append(out.Agents, agent.Clone())
			agents[agent.Metadata.UID] = true
			includedUIDs[agent.Metadata.UID] = true
		}
	}
	for _, pane := range registry.Panes {
		owner := pane.Metadata.OwnerUID()
		if windows[owner] || agents[owner] {
			out.Panes = append(out.Panes, pane.Clone())
			includedUIDs[pane.Metadata.UID] = true
		}
	}
	for _, reservation := range registry.NameReservations {
		if includedUIDs[reservation.UID] {
			out.NameReservations = append(out.NameReservations, reservation)
		}
	}
	return out
}

func mergeScopedResourceRegistry(before, scopedBefore, scopedAfter coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) coremetadata.Registry {
	removeProjects := map[string]bool{}
	for _, project := range scopedBefore.Projects {
		removeProjects[project.Metadata.UID] = true
	}
	// A Window/Pane foreign binding discovered during reconciliation turns its
	// entire session diagnostic-only. Do not merge even incidental root/session
	// status changes for that graph.
	activeAfter := resourceProjectUIDsForSessions(scopedAfter, sessions, reconciler)
	for _, session := range sessions {
		if !reconciler.refusedSessions[session.name] {
			continue
		}
		// Every refused session is diagnostic-only, so preserve its entire
		// authoritative Registry graph. In the D3 descendant case the quarantine
		// is not a second D4 divergence; the exact runtime items own reporting and
		// L8 recovery.
		if project, ok := resourceProjectForSession(scopedBefore, session, reconciler); ok {
			delete(removeProjects, project.Metadata.UID)
		}
	}

	out := before.Clone()
	retained := resourceRegistryWithoutProjectGraphs(before, removeProjects)
	changed := resourceRegistryProjectGraph(scopedAfter, activeAfter)
	// Replace scoped resources at their existing global positions. Building the
	// result as `retained + changed` made a no-op pass rotate every selected
	// Project behind unrelated/other-host roots, producing a Registry byte diff
	// even though the exact-socket plan had zero actions. UID replacement keeps
	// source ordering fixed and appends only genuinely new scoped resources.
	out.Projects = mergeScopedResources(before.Projects, retained.Projects, changed.Projects, func(value coremetadata.Project) string { return value.Metadata.UID })
	out.Windows = mergeScopedResources(before.Windows, retained.Windows, changed.Windows, func(value coremetadata.Window) string { return value.Metadata.UID })
	out.Panes = mergeScopedResources(before.Panes, retained.Panes, changed.Panes, func(value coremetadata.Pane) string { return value.Metadata.UID })
	out.Agents = mergeScopedResources(before.Agents, retained.Agents, changed.Agents, func(value coremetadata.Agent) string { return value.Metadata.UID })
	out.NameReservations = slices.Clone(scopedAfter.NameReservations)
	out.UpdatedAt = scopedAfter.UpdatedAt
	return retainReservedResourceNames(out.Normalize())
}

func mergeScopedResources[T any](before, retained, changed []T, uid func(T) string) []T {
	retainedByUID := make(map[string]T, len(retained))
	changedByUID := make(map[string]T, len(changed))
	for _, value := range retained {
		retainedByUID[uid(value)] = value
	}
	for _, value := range changed {
		changedByUID[uid(value)] = value
	}
	out := make([]T, 0, len(retained)+len(changed))
	seen := make(map[string]bool, cap(out))
	for _, value := range before {
		key := uid(value)
		if replacement, ok := changedByUID[key]; ok {
			out = append(out, replacement)
			seen[key] = true
			continue
		}
		if replacement, ok := retainedByUID[key]; ok {
			out = append(out, replacement)
			seen[key] = true
		}
	}
	for _, values := range [][]T{retained, changed} {
		for _, value := range values {
			key := uid(value)
			if seen[key] {
				continue
			}
			out = append(out, value)
			seen[key] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resourceRegistryWithoutProjectGraphs removes the whole graphs of the named
// Projects and carries everything else through untouched.
//
// It replaced "the graph of every Project except these", which is a different
// set the moment a resource is rooted anywhere but a Project. A Window owned by
// a ControlSession belongs to no Project graph at all, so the inclusion
// projection dropped it from the retained half while the merge put every
// reservation back -- the dangling `Window name reservation "window" refers to
// unknown uid` that aborted every `reconcile resources` commit once the Home
// session became a Registry owner.
//
// Removal follows the ownership chain the same way inclusion does, so over a
// well-formed registry the two are exact complements and a Project-only
// registry projects byte-for-byte as before. Where they differ is a resource
// the removal set does not reach: this keeps it, which is the safe direction.
func resourceRegistryWithoutProjectGraphs(registry coremetadata.Registry, removed map[string]bool) coremetadata.Registry {
	out := registry.Clone()
	out.Projects = nil
	out.Windows = nil
	out.Panes = nil
	out.Agents = nil
	droppedWindows := map[string]bool{}
	droppedAgents := map[string]bool{}
	for _, project := range registry.Projects {
		if !removed[project.Metadata.UID] {
			out.Projects = append(out.Projects, project.Clone())
		}
	}
	for _, window := range registry.Windows {
		if removed[window.Metadata.OwnerUID()] {
			droppedWindows[window.Metadata.UID] = true
			continue
		}
		out.Windows = append(out.Windows, window.Clone())
	}
	for _, agent := range registry.Agents {
		if droppedWindows[agent.Metadata.OwnerUID()] {
			droppedAgents[agent.Metadata.UID] = true
			continue
		}
		out.Agents = append(out.Agents, agent.Clone())
	}
	for _, pane := range registry.Panes {
		owner := pane.Metadata.OwnerUID()
		if droppedWindows[owner] || droppedAgents[owner] {
			continue
		}
		out.Panes = append(out.Panes, pane.Clone())
	}
	return out
}

// retainReservedResourceNames drops every reservation whose uid names no
// resource in the projection.
//
// Registry.Validate requires the reservation table and the resource set to
// agree in both directions, so a reservation left behind by a resource this
// projection did not carry is not allocator input -- it is a row that makes the
// whole registry unwritable, and the commit that would have written it aborts
// with nothing repaired. The merge above no longer produces one for a control
// graph; this is the total guarantee for any other input, including a scoped
// pass whose Project stopped resolving between the two halves.
//
// It only ever removes rows. A reservation is never invented here: a resource
// with no reservation is a different defect and stays a Validate failure rather
// than being papered over with a row the allocator never issued.
func retainReservedResourceNames(registry coremetadata.Registry) coremetadata.Registry {
	known := map[string]bool{}
	for _, project := range registry.Projects {
		known[project.Metadata.UID] = true
	}
	for _, control := range registry.ControlSessions {
		known[control.Metadata.UID] = true
	}
	for _, window := range registry.Windows {
		known[window.Metadata.UID] = true
	}
	for _, pane := range registry.Panes {
		known[pane.Metadata.UID] = true
	}
	for _, agent := range registry.Agents {
		known[agent.Metadata.UID] = true
	}
	registry.NameReservations = slices.DeleteFunc(slices.Clone(registry.NameReservations),
		func(reservation coremetadata.NameReservation) bool { return !known[reservation.UID] })
	return registry
}

type observedResourceProjectSession struct {
	id   string
	name string
	uid  string
	meta string
	root string
}

func observeResourceProjectSessions(ctx context.Context, runner tmuxCommandRunner) ([]observedResourceProjectSession, error) {
	out, err := runner.Run(ctx, "tmux", "list-sessions", "-F", strings.Join([]string{
		"#{session_id}", "#{session_name}", "#{" + tmuxopts.ProjectUIDSession + "}", "#{" + tmuxopts.ProjectNameSession + "}", "#{" + tmuxopts.ProjectPathSession + "}",
	}, "\\037"))
	if err != nil {
		return nil, fmt.Errorf("observe Project session mirrors: %w", err)
	}
	var sessions []observedResourceProjectSession
	for _, fields := range splitResourcePlanRows(string(out)) {
		if len(fields) != 5 {
			continue
		}
		sessions = append(sessions, observedResourceProjectSession{
			id: fields[0], name: fields[1], uid: strings.TrimSpace(fields[2]), meta: fields[3], root: strings.TrimSpace(fields[4]),
		})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].name < sessions[j].name })
	return sessions, nil
}

func resourceProjectForSession(registry coremetadata.Registry, session observedResourceProjectSession, reconciler *registryReconciler) (*coremetadata.Project, bool) {
	// An exact ControlSession declaration owns this physical session before any
	// Project evidence is interpreted. A Project uid/root observed on the same
	// session is D4 contamination and is classified by the refusal planner; it
	// is never allowed to override the root kind here.
	if _, ok := registry.ControlSessionBySession(session.name); ok {
		return nil, false
	}
	// A known mirrored UID is the authoritative identity edge. In particular,
	// rebind intentionally makes the old path anchor stale while preserving this
	// UID, so preferring the anchor would make the public retry route refuse the
	// exact drift it is responsible for repairing. An unknown UID never falls
	// through to path/name heuristics: foreign claims remain fail-closed.
	if session.uid != "" {
		return registry.Project(session.uid)
	}
	if session.root != "" {
		if project, ok := registry.ProjectByRoot(session.root); ok {
			return project, true
		}
		// A present anchor is the exact identity edge. Falling through to the
		// session-name projection would turn a mismatched root into the heuristic
		// merge this command must refuse.
		return nil, false
	}
	uid := reconciler.projectsBySessionName(registry)[session.name]
	if uid == "" {
		return nil, false
	}
	return registry.Project(uid)
}

func refusedResourceProjectSessions(registry coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) map[string]bool {
	reconciler.initializeRefusalBookkeeping()
	counts := map[string]int{}
	claims := map[string]int{}
	for _, session := range sessions {
		if session.uid != "" {
			counts[session.uid]++
		}
		if claim := resourceProjectSessionClaim(registry, session, reconciler); claim != "" {
			claims[claim]++
		}
	}
	refused := map[string]bool{}
	for _, session := range sessions {
		claim := reconciler.resolveRegistrySessionClaim(registry, session.name)
		if claim.Kind == registrySessionClaimControl {
			if session.uid != "" || session.root != "" {
				refused[session.name] = true
				reconciler.refuseSession(session.name, resourcegraph.DivergenceContaminated,
					"exact ControlSession carries a foreign Project identity claim")
			}
			continue
		}
		if claim := resourceProjectSessionClaim(registry, session, reconciler); claim != "" && claims[claim] > 1 {
			refused[session.name] = true
			reconciler.refuseSession(session.name, resourcegraph.DivergenceContaminated,
				"multiple live sessions resolve to the same exact Registry Project")
			continue
		}
		if session.uid == "" {
			if session.root == "" && claim.Kind == registrySessionClaimRefused {
				refused[session.name] = true
				reconciler.refuseSession(session.name, claim.Divergence, claim.Reason)
			}
			continue
		}
		expected, ok := resourceProjectForSession(registry, session, reconciler)
		if !ok || session.uid != expected.Metadata.UID || counts[session.uid] > 1 {
			refused[session.name] = true
			if _, known := registry.Project(session.uid); !known {
				reconciler.refuseSession(session.name, resourcegraph.DivergenceOrphanMirror,
					"live session carries a Project uid absent from the authoritative Registry")
			} else {
				reconciler.refuseSession(session.name, resourcegraph.DivergenceContaminated,
					"live session carries a Project uid outside its exact Registry identity")
			}
		}
	}
	return refused
}

// resourceProjectSessionClaim is the exact Project identity a live session
// would acquire. Counting the claim independently from the current uid catches
// two UID-less sessions anchored to the same root before either one can mirror
// the same authoritative Project uid.
func resourceProjectSessionClaim(registry coremetadata.Registry, session observedResourceProjectSession, reconciler *registryReconciler) string {
	if project, ok := resourceProjectForSession(registry, session, reconciler); ok {
		return "uid:" + project.Metadata.UID
	}
	if root := strings.TrimSpace(session.root); root != "" {
		return "root:" + root
	}
	return ""
}

func resourceSessionProjectClaims(registry coremetadata.Registry, sessionName string, reconciler *registryReconciler) []string {
	var claims []string
	for _, project := range registry.Projects {
		name := ""
		if project.Status.Session != nil {
			name = strings.TrimSpace(project.Status.Session.Name)
		}
		if name == "" && reconciler.sessionNameFor != nil {
			name = strings.TrimSpace(reconciler.sessionNameFor(project.Spec.Root))
		}
		if name == sessionName {
			claims = append(claims, project.Metadata.UID)
		}
	}
	slices.Sort(claims)
	return claims
}

func planResourceProjectMirrors(ctx context.Context, recorder *resourcePlanTmuxRunner, registry coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) error {
	for _, session := range sessions {
		if reconciler.refusedSessions[session.name] {
			continue
		}
		project, ok := resourceProjectForSession(registry, session, reconciler)
		if !ok {
			continue
		}
		switch {
		case session.uid == "":
			if err := reconciler.mirror.MirrorProject(ctx, session.id, *project); err != nil {
				return err
			}
		case session.meta != project.Metadata.Name:
			if err := reconciler.mirror.RenameProject(ctx, session.id, project.Metadata.Name); err != nil {
				return err
			}
		}
		if session.root != project.Spec.Root {
			if err := reconciler.mirror.RebindProject(ctx, session.id, project.Spec.Root); err != nil {
				return err
			}
		}
	}
	return nil
}

func planResourceBoundMirrorDrift(ctx context.Context, recorder *resourcePlanTmuxRunner, registry coremetadata.Registry, reconciler *registryReconciler) error {
	for _, object := range recorder.objects {
		if object.uid == "" || reconciler.refusedSessions[object.session] {
			continue
		}
		switch object.kind {
		case coremetadata.KindWindow:
			window, ok := registry.Window(object.uid)
			if !ok {
				continue
			}
			if resourceTmuxTruthy(object.automatic) {
				if err := reconciler.mirror.DisableAutomaticRename(ctx, object.target); err != nil {
					return err
				}
				recorder.setLastWriteBefore(object.automatic)
			}
			out, err := recorder.Run(ctx, "tmux", "display-message", "-p", "-t", object.target, "-F", "#{"+tmuxopts.WindowName+"}")
			if err != nil {
				return fmt.Errorf("metadata: read window name mirror: %w", err)
			}
			if strings.TrimSpace(string(out)) != window.Metadata.Name {
				if _, err := recorder.Run(ctx, "tmux", "set-option", "-w", "-t", object.target, "-q", tmuxopts.WindowName, window.Metadata.Name); err != nil {
					return fmt.Errorf("metadata: mirror window name: %w", err)
				}
				recorder.setLastWriteBefore(strings.TrimSpace(string(out)))
			}
		case coremetadata.KindPane:
			pane, ok := registry.Pane(object.uid)
			if !ok || object.nameMirror == pane.Metadata.Name {
				continue
			}
			if err := reconciler.mirror.RenamePane(ctx, object.target, pane.Metadata.Name); err != nil {
				return err
			}
			recorder.setLastWriteBefore(object.nameMirror)
		}
	}
	return nil
}

func resourceTmuxTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "1", "yes", "true":
		return true
	default:
		return false
	}
}

func resourceProjectForeignItems(registry coremetadata.Registry, sessions []observedResourceProjectSession, reconciler *registryReconciler) []resourceReconcileItem {
	counts := map[string]int{}
	claims := map[string]int{}
	for _, session := range sessions {
		if session.uid != "" {
			counts[session.uid]++
		}
		if claim := resourceProjectSessionClaim(registry, session, reconciler); claim != "" {
			claims[claim]++
		}
	}
	var items []resourceReconcileItem
	for _, session := range sessions {
		if !reconciler.refusedSessions[session.name] {
			continue
		}
		if reconciler.refusedSessionDivergence[session.name] == resourcegraph.DivergenceOrphanMirror {
			continue
		}
		reason := strings.TrimSpace(reconciler.refusedSessionReasons[session.name])
		if reason == "" {
			reason = "live session carries a Project uid outside its exact Registry identity"
		}
		if reason == "live session carries a Project uid outside its exact Registry identity" && session.uid == "" && session.root == "" && len(resourceSessionProjectClaims(registry, session.name, reconciler)) > 1 {
			reason = "multiple Registry Projects claim the live session-name edge"
		} else if reason == "live session carries a Project uid outside its exact Registry identity" && func() bool {
			claim := resourceProjectSessionClaim(registry, session, reconciler)
			return claim != "" && claims[claim] > 1
		}() {
			reason = "multiple live sessions resolve to the same exact Registry Project"
		} else if reason == "live session carries a Project uid outside its exact Registry identity" && counts[session.uid] > 1 {
			reason = "multiple live sessions claim the same Registry Project uid"
		} else if reason == "live session carries a Project uid outside its exact Registry identity" {
			if _, ok := registry.Project(session.uid); !ok {
				reason = "live session carries a Project uid absent from the authoritative Registry"
			}
		}
		divergence := reconciler.refusedSessionDivergence[session.name]
		if !divergence.Valid() {
			if _, ok := registry.Project(session.uid); !ok && session.uid != "" {
				divergence = resourcegraph.DivergenceOrphanMirror
			} else {
				divergence = resourcegraph.DivergenceContaminated
			}
		}
		items = append(items, resourceReconcileItem{
			Key: "tmux:refuse:project:" + session.id, Drift: resourceDriftForeign, Surface: "tmux", Action: "refuse",
			Kind: string(coremetadata.KindProject), Target: session.id, Field: tmuxopts.ProjectUIDSession,
			Before: session.uid, Outcome: "refused", Reason: reason, refused: true,
			Divergence: divergence,
		})
	}
	return items
}

type resourcePlanUIDNormalizer struct {
	created map[string]string
	ordered []resourcePlanUIDAllocation
}

type resourcePlanUIDAllocation struct {
	kind coremetadata.Kind
	uid  string
}

func newPlanUIDNormalizerWithAllocations(before, after coremetadata.Registry, allocations []resourcePlanUIDAllocation) resourcePlanUIDNormalizer {
	known := registryUIDSet(before)
	created := map[string]string{}
	counts := map[coremetadata.Kind]int{}
	ordered := make([]resourcePlanUIDAllocation, 0, len(allocations))
	for _, allocation := range allocations {
		if known[allocation.uid] || created[allocation.uid] != "" {
			continue
		}
		counts[allocation.kind]++
		created[allocation.uid] = fmt.Sprintf("<allocated-%s-%d>", strings.ToLower(string(allocation.kind)), counts[allocation.kind])
		ordered = append(ordered, allocation)
	}
	for _, record := range registryResourceRecords(after) {
		if known[record.uid] || created[record.uid] != "" {
			continue
		}
		counts[record.kind]++
		created[record.uid] = fmt.Sprintf("<allocated-%s-%d>", strings.ToLower(string(record.kind)), counts[record.kind])
		ordered = append(ordered, resourcePlanUIDAllocation{kind: record.kind, uid: record.uid})
	}
	return resourcePlanUIDNormalizer{created: created, ordered: ordered}
}

func (n resourcePlanUIDNormalizer) value(value string) string {
	if normalized := n.created[value]; normalized != "" {
		return normalized
	}
	return value
}

func (n resourcePlanUIDNormalizer) slots(registry coremetadata.Registry, promotions []resourceAuthorshipPromotion) []resourceAllocationSlot {
	var slots []resourceAllocationSlot
	byUID := map[string]registryResourceRecord{}
	for _, record := range registryResourceRecords(registry) {
		byUID[record.uid] = record
	}
	promotionKind := map[string]string{}
	for _, promotion := range promotions {
		promotionKind[promotion.AgentUID] = string(promotion.LinkKind)
	}
	for _, allocation := range n.ordered {
		record, ok := byUID[allocation.uid]
		slot := n.created[allocation.uid]
		if !ok || slot == "" {
			continue
		}
		slots = append(slots, resourceAllocationSlot{
			Slot:          slot,
			Kind:          string(record.kind),
			Name:          n.value(record.name),
			PromotionKind: promotionKind[record.uid],
		})
	}
	return slots
}

func detectAuthorshipPromotions(before, after coremetadata.Registry, objects []observedPlanObject) []resourceAuthorshipPromotion {
	byPane := map[string]observedPlanObject{}
	for _, object := range objects {
		if object.kind != coremetadata.KindPane || strings.TrimSpace(object.uid) == "" {
			continue
		}
		byPane[object.uid] = object
	}
	var promotions []resourceAuthorshipPromotion
	for _, paneAfter := range after.Panes {
		paneBefore, ok := before.Pane(paneAfter.Metadata.UID)
		if !ok || paneBefore.Metadata.OwnerRef == nil || paneAfter.Metadata.OwnerRef == nil ||
			paneBefore.Metadata.OwnerRef.Kind != coremetadata.KindWindow || paneAfter.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
			paneBefore.Spec.Role != coremetadata.PaneRoleShell || paneAfter.Spec.Role != coremetadata.PaneRoleAgent {
			continue
		}
		windowUID := paneBefore.Metadata.OwnerUID()
		agent, ok := after.Agent(paneAfter.Metadata.OwnerUID())
		if !ok || agent.Metadata.OwnerUID() != windowUID || agent.Status.PaneRef != paneAfter.Metadata.UID {
			continue
		}
		windowBefore, beforeOK := before.Window(windowUID)
		windowAfter, afterOK := after.Window(windowUID)
		projectUID, projectOK := projectUIDForWindow(after, windowUID)
		object, liveOK := byPane[paneAfter.Metadata.UID]
		if !beforeOK || !afterOK || !projectOK || !liveOK ||
			coremetadata.ResolveAgentPaneAuthority(coremetadata.LegacyPane{
				Provider: object.agentProvider, LaunchAuthorship: object.agentLaunchAuthorship,
			}) != coremetadata.AgentPaneAuthorityLaunch {
			continue
		}
		linkKind := coremetadata.AgentLinkAttached
		if _, existed := before.Agent(agent.Metadata.UID); !existed {
			linkKind = coremetadata.AgentLinkMinted
		}
		promotions = append(promotions, resourceAuthorshipPromotion{
			PaneUID: paneAfter.Metadata.UID, PaneTarget: object.target,
			WindowUID: windowUID, ProjectUID: projectUID,
			SessionID: object.sessionID, WindowID: object.windowID,
			ObservedProvider: object.agentProvider, AgentProvider: agent.Spec.Provider,
			AgentUID: agent.Metadata.UID, AgentName: agent.Metadata.Name, LinkKind: linkKind,
			AnchorBefore: windowBefore.Spec.AnchorPaneRef, AnchorAfter: windowAfter.Spec.AnchorPaneRef,
			DefaultBefore: windowBefore.Spec.DefaultShellPaneRef, DefaultAfter: windowAfter.Spec.DefaultShellPaneRef,
		})
	}
	sort.Slice(promotions, func(i, j int) bool { return promotions[i].PaneUID < promotions[j].PaneUID })
	return promotions
}

func projectUIDForWindow(registry coremetadata.Registry, windowUID string) (string, bool) {
	window, ok := registry.Window(windowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject {
		return "", false
	}
	_, ok = registry.Project(window.Metadata.OwnerUID())
	return window.Metadata.OwnerUID(), ok
}

func exactWindowGraphUIDSet(registry coremetadata.Registry, windows map[string]bool) map[string]bool {
	uids := map[string]bool{}
	for _, window := range registry.Windows {
		if windows[window.Metadata.UID] {
			uids[window.Metadata.UID] = true
		}
	}
	for _, agent := range registry.Agents {
		if agent.Metadata.OwnerRef != nil && agent.Metadata.OwnerRef.Kind == coremetadata.KindWindow && windows[agent.Metadata.OwnerUID()] {
			uids[agent.Metadata.UID] = true
		}
	}
	for _, pane := range registry.Panes {
		if pane.Metadata.OwnerRef == nil {
			continue
		}
		if pane.Metadata.OwnerRef.Kind == coremetadata.KindWindow && windows[pane.Metadata.OwnerUID()] ||
			pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent && uids[pane.Metadata.OwnerUID()] {
			uids[pane.Metadata.UID] = true
		}
	}
	return uids
}

// mergeExactWindowGraphs keeps the composite promotion's durable write-set at
// the promoted Window ancestry. A second Window in the same Project is no more
// part of this transaction than a Window on another socket.
func mergeExactWindowGraphs(before, after coremetadata.Registry, windows map[string]bool) coremetadata.Registry {
	oldUIDs := exactWindowGraphUIDSet(before, windows)
	newUIDs := exactWindowGraphUIDSet(after, windows)
	out := before.Clone()
	out.Windows = slices.DeleteFunc(out.Windows, func(value coremetadata.Window) bool { return oldUIDs[value.Metadata.UID] })
	out.Panes = slices.DeleteFunc(out.Panes, func(value coremetadata.Pane) bool { return oldUIDs[value.Metadata.UID] })
	out.Agents = slices.DeleteFunc(out.Agents, func(value coremetadata.Agent) bool { return oldUIDs[value.Metadata.UID] })
	out.NameReservations = slices.DeleteFunc(out.NameReservations, func(value coremetadata.NameReservation) bool { return oldUIDs[value.UID] })
	for _, window := range after.Windows {
		if newUIDs[window.Metadata.UID] {
			out.Windows = append(out.Windows, window.Clone())
		}
	}
	for _, pane := range after.Panes {
		if newUIDs[pane.Metadata.UID] {
			out.Panes = append(out.Panes, pane.Clone())
		}
	}
	for _, agent := range after.Agents {
		if newUIDs[agent.Metadata.UID] {
			out.Agents = append(out.Agents, agent.Clone())
		}
	}
	for _, reservation := range after.NameReservations {
		if newUIDs[reservation.UID] {
			out.NameReservations = append(out.NameReservations, reservation)
		}
	}
	out.UpdatedAt = after.UpdatedAt
	return out.Normalize()
}

func planAuthorshipPromotionOptions(ctx context.Context, recorder *resourcePlanTmuxRunner, registry coremetadata.Registry, promotions []resourceAuthorshipPromotion) error {
	for _, promotion := range promotions {
		pane, ok := registry.Pane(promotion.PaneUID)
		if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Spec.Role != coremetadata.PaneRoleAgent {
			return fmt.Errorf("authorship promotion %s has no valid Registry Pane post-state", promotion.PaneUID)
		}
		agent, ok := registry.Agent(pane.Metadata.OwnerUID())
		if !ok || agent.Metadata.UID != promotion.AgentUID || agent.Status.PaneRef != promotion.PaneUID {
			return fmt.Errorf("authorship promotion %s has no exact Agent reverse reference", promotion.PaneUID)
		}
		firstWrite := len(recorder.writes)
		for _, desired := range []struct{ field, value string }{
			{tmuxopts.PaneOwnerKind, string(coremetadata.KindAgent)},
			{tmuxopts.PaneOwnerUID, promotion.AgentUID},
			{tmuxopts.PaneRole, string(coremetadata.PaneRoleAgent)},
			{tmuxopts.AgentUIDPane, promotion.AgentUID},
			{tmuxopts.AgentProviderPane, promotion.ObservedProvider},
		} {
			if err := planExactManagedPaneOption(ctx, recorder, promotion.PaneTarget, desired.field, desired.value); err != nil {
				return err
			}
		}
		for index := firstWrite; index < len(recorder.writes); index++ {
			recorder.writes[index].semanticGuards = []controller.Guard{
				{Field: tmuxopts.AgentLaunchAuthorshipPane, Expect: "1"},
				{Field: tmuxopts.AgentProviderPane, Expect: promotion.ObservedProvider},
			}
		}
	}
	return nil
}

func planExactManagedPaneOption(ctx context.Context, recorder *resourcePlanTmuxRunner, target, field, desired string) error {
	out, err := recorder.Run(ctx, "tmux", "display-message", "-p", "-t", target, "-F", "#{"+field+"}")
	if err != nil {
		return fmt.Errorf("inspect promotion option %s on %s: %w", field, target, err)
	}
	before := strings.TrimSpace(string(out))
	if before == desired {
		return nil
	}
	if _, err := recorder.Run(ctx, "tmux", "set-option", "-p", "-t", target, "-q", field, desired); err != nil {
		return err
	}
	recorder.setLastWriteBefore(before)
	return nil
}

func promotionPlanItems(promotions []resourceAuthorshipPromotion, normalize resourcePlanUIDNormalizer) []resourceReconcileItem {
	items := make([]resourceReconcileItem, 0, len(promotions))
	for _, promotion := range promotions {
		agentUID := normalize.value(promotion.AgentUID)
		agentName := normalize.value(promotion.AgentName)
		allocationSlot := ""
		agentBefore := agentName + " (" + agentUID + ")"
		if promotion.LinkKind == coremetadata.AgentLinkMinted {
			allocationSlot = agentUID
			agentBefore = "<none>"
		}
		defaultBefore, defaultAfter := promotion.DefaultBefore, promotion.DefaultAfter
		if defaultBefore == "" {
			defaultBefore = "<none>"
		}
		if defaultAfter == "" {
			defaultAfter = "<none>"
		}
		guards := []controller.Guard{
			{Field: tmuxopts.PaneUID, Expect: promotion.PaneUID},
			{Field: tmuxopts.AgentLaunchAuthorshipPane, Expect: "1"},
			{Field: tmuxopts.AgentProviderPane, Expect: promotion.ObservedProvider},
		}
		if promotion.SessionID != "" {
			guards = append(guards, controller.Guard{Field: "session_id", Expect: promotion.SessionID})
		}
		if promotion.WindowID != "" {
			guards = append(guards, controller.Guard{Field: "window_id", Expect: promotion.WindowID})
		}
		items = append(items, resourceReconcileItem{
			Key:   "registry:promote-authorship:pane:" + promotion.PaneUID,
			Drift: resourceDriftStale, Surface: "registry", Action: "promote-authorship", Kind: string(coremetadata.KindPane),
			Target: promotion.PaneUID, After: "agent:" + agentUID, Outcome: "planned",
			Reason:     "canonical Projmux launch authorship atomically promotes the exact Pane and its Window refs",
			Divergence: resourcegraph.DivergenceDrifted, Authority: string(coremetadata.AgentPaneAuthorityLaunch),
			PromotionKind: string(promotion.LinkKind), AllocationSlot: allocationSlot,
			Transitions: []resourceRefTransition{
				{Field: "Agent", Before: agentBefore, After: agentName + " (" + agentUID + ")"},
				{Field: "Pane.metadata.ownerRef", Before: "Window/" + promotion.WindowUID, After: "Agent/" + agentUID},
				{Field: "Pane.spec.role", Before: string(coremetadata.PaneRoleShell), After: string(coremetadata.PaneRoleAgent)},
				{Field: "Agent.status.paneRef", Before: "<none>", After: promotion.PaneUID},
				{Field: "Window.spec.anchorPaneRef", Before: promotion.AnchorBefore, After: promotion.AnchorAfter},
				{Field: "Window.spec.defaultShellPaneRef", Before: defaultBefore, After: defaultAfter},
				{Field: "tmux." + tmuxopts.PaneUID, Before: promotion.PaneUID, After: promotion.PaneUID},
				{Field: "tmux." + tmuxopts.PaneOwnerKind, Before: "<unset>", After: string(coremetadata.KindAgent)},
				{Field: "tmux." + tmuxopts.PaneOwnerUID, Before: "<unset>", After: agentUID},
				{Field: "tmux." + tmuxopts.PaneRole, Before: "<unset>", After: string(coremetadata.PaneRoleAgent)},
				{Field: "tmux." + tmuxopts.AgentUIDPane, Before: "<unset>", After: agentUID},
				{Field: "Agent.spec.provider", Before: promotion.AgentProvider, After: promotion.AgentProvider},
				{Field: "tmux." + tmuxopts.AgentProviderPane, Before: promotion.ObservedProvider, After: promotion.ObservedProvider},
			},
			Guards: guards, registry: true,
		})
	}
	return items
}

type registryResourceRecord struct {
	kind  coremetadata.Kind
	uid   string
	owner string
	name  string
	value any
}

// registryResourceRecords is the flat record projection of a whole Registry:
// every resource of every kind, root kinds included.
//
// Both root kinds are here because both of its consumers ask a whole-Registry
// question. registryUIDSet asks "is this uid a resource projmux owns", and a
// ControlSession uid that answered "no" would make its own live objects read as
// foreign drift. registryReconcileItems asks "what changed between these two
// registries", and a root the projection cannot see is a root whose appearance
// or change the plan silently reports as nothing at all. Leaving one kind out
// is the C-5 defect in its exact general form -- a projection that reads the
// Registry as if Project were the only root.
func registryResourceRecords(registry coremetadata.Registry) []registryResourceRecord {
	out := make([]registryResourceRecord, 0,
		len(registry.Projects)+len(registry.ControlSessions)+len(registry.Windows)+len(registry.Panes)+len(registry.Agents))
	for _, resource := range registry.Projects {
		out = append(out, registryResourceRecord{kind: resource.Kind, uid: resource.Metadata.UID, name: resource.Metadata.Name, value: resource})
	}
	for _, resource := range registry.ControlSessions {
		out = append(out, registryResourceRecord{kind: resource.Kind, uid: resource.Metadata.UID, name: resource.Metadata.Name, value: resource})
	}
	for _, resource := range registry.Windows {
		out = append(out, registryResourceRecord{kind: resource.Kind, uid: resource.Metadata.UID, owner: resource.Metadata.OwnerUID(), name: resource.Metadata.Name, value: resource})
	}
	for _, resource := range registry.Panes {
		out = append(out, registryResourceRecord{kind: resource.Kind, uid: resource.Metadata.UID, owner: resource.Metadata.OwnerUID(), name: resource.Metadata.Name, value: resource})
	}
	for _, resource := range registry.Agents {
		out = append(out, registryResourceRecord{kind: resource.Kind, uid: resource.Metadata.UID, owner: resource.Metadata.OwnerUID(), name: resource.Metadata.Name, value: resource})
	}
	return out
}

func registryUIDSet(registry coremetadata.Registry) map[string]bool {
	out := map[string]bool{}
	for _, record := range registryResourceRecords(registry) {
		out[record.uid] = true
	}
	return out
}

func registryReconcileItems(before, after coremetadata.Registry, normalize resourcePlanUIDNormalizer) []resourceReconcileItem {
	old := map[string]registryResourceRecord{}
	for _, record := range registryResourceRecords(before) {
		old[record.uid] = record
	}
	var items []resourceReconcileItem
	for _, record := range registryResourceRecords(after) {
		previous, existed := old[record.uid]
		if existed && reflect.DeepEqual(previous.value, record.value) {
			continue
		}
		drift, action := resourceDriftStale, "update"
		if !existed {
			drift, action = resourceDriftMissing, "create"
		} else if resourceBecameOrphan(previous.value, record.value) {
			drift = resourceDriftOrphan
		}
		owner := normalize.value(record.owner)
		target := strings.ToLower(string(record.kind)) + "/" + normalize.value(record.name)
		if owner != "" {
			target = owner + "/" + target
		}
		uid := normalize.value(record.uid)
		items = append(items, resourceReconcileItem{
			Key:     "registry:" + action + ":" + strings.ToLower(string(record.kind)) + ":" + target,
			Drift:   drift,
			Surface: "registry",
			Action:  action,
			Kind:    string(record.kind),
			Target:  target,
			After:   "uid:" + uid,
			Outcome: "planned",
			Reason:  "Registry reconciliation changes stored state to match the exact observed object",
			Divergence: func() resourcegraph.Divergence {
				if !existed {
					return resourcegraph.DivergenceUnattributed
				}
				if resourceBecameOrphan(previous.value, record.value) {
					return resourcegraph.DivergenceUnrealized
				}
				return resourcegraph.DivergenceDrifted
			}(),
			registry: true,
		})
	}
	return items
}

func resourceBecameOrphan(before, after any) bool {
	switch previous := before.(type) {
	case coremetadata.Window:
		current := after.(coremetadata.Window)
		return !hasRuntimeCondition(previous.Status.Conditions) && hasRuntimeCondition(current.Status.Conditions)
	case coremetadata.Pane:
		current := after.(coremetadata.Pane)
		return !hasRuntimeCondition(previous.Status.Conditions) && hasRuntimeCondition(current.Status.Conditions)
	case coremetadata.Agent:
		current := after.(coremetadata.Agent)
		return previous.Status.Phase == coremetadata.PhaseRunning && current.Status.Phase == coremetadata.PhaseOffline
	default:
		return false
	}
}

func hasRuntimeCondition(conditions []coremetadata.Condition) bool {
	for _, condition := range conditions {
		if condition.Type == coremetadata.ConditionMissingRuntime {
			return true
		}
	}
	return false
}

type observedPlanObject struct {
	kind                  coremetadata.Kind
	target                string
	uid                   string
	session               string
	sessionID             string
	windowID              string
	windowIndex           string
	nameMirror            string
	automatic             string
	agentProvider         string
	agentLaunchAuthorship string
	agentTopic            string
	agentSessionID        string
	agentThreadID         string
}

type plannedTmuxWrite struct {
	args           []string
	target         string
	field          string
	before         string
	after          string
	guardField     string
	guardBefore    string
	guardSessionID string
	guardWindowID  string
	semanticGuards []controller.Guard
}

func (w plannedTmuxWrite) itemKey() string {
	action := "set-option"
	if len(w.args) > 0 && w.args[0] == "rename-window" {
		action = "rename-window"
	}
	kind := "Project"
	if action == "rename-window" || slices.Contains(w.args, "-w") {
		kind = "Window"
	} else if slices.Contains(w.args, "-p") {
		kind = "Pane"
	}
	return "tmux:" + action + ":" + strings.ToLower(kind) + ":" + w.target + ":" + w.field
}

// resourcePlanTmuxRunner is a transactional shadow of the exact tmux server.
// Reads reach the server; mirror writes are recorded and applied only to the
// UID overlay used by the reconciler's final inventory read.
type resourcePlanTmuxRunner struct {
	reader            tmuxCommandRunner
	writes            []plannedTmuxWrite
	windowUID         map[string]string
	paneUID           map[string]string
	windowAlias       map[string]string
	objects           []observedPlanObject
	sessionRoots      map[string]string
	seenObject        map[string]bool
	sessionOpts       map[string]map[string]string
	initialWindowUID  map[string]string
	initialWindowName map[string]string
	windowAutomatic   map[string]string
	initialPaneUID    map[string]string
	initialSessionUID map[string]string
	sessionIDByName   map[string]string
	windowIDByCoord   map[string]string
	windowSessionID   map[string]string
	paneWindowID      map[string]string
	paneSessionID     map[string]string
	optionValues      map[string]map[string]string
}

func newResourcePlanTmuxRunner(reader tmuxCommandRunner) *resourcePlanTmuxRunner {
	return &resourcePlanTmuxRunner{
		reader: reader, windowUID: map[string]string{}, paneUID: map[string]string{}, windowAlias: map[string]string{},
		sessionRoots: map[string]string{}, seenObject: map[string]bool{}, sessionOpts: map[string]map[string]string{},
		initialWindowUID: map[string]string{}, initialWindowName: map[string]string{}, windowAutomatic: map[string]string{},
		initialPaneUID: map[string]string{}, initialSessionUID: map[string]string{},
		sessionIDByName: map[string]string{}, windowIDByCoord: map[string]string{},
		windowSessionID: map[string]string{}, paneWindowID: map[string]string{}, paneSessionID: map[string]string{},
		optionValues: map[string]map[string]string{},
	}
}

func (r *resourcePlanTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) == 0 {
		return nil, fmt.Errorf("resource reconciliation planner expected tmux command, got %s %v", name, args)
	}
	if args[0] == "set-option" || args[0] == "rename-window" {
		return r.recordWrite(ctx, args)
	}
	out, err := r.reader.Run(ctx, name, args...)
	if err != nil {
		return out, err
	}
	out = r.applyOverlay(args, out)
	r.observeRead(args, out)
	return out, nil
}

func (r *resourcePlanTmuxRunner) recordWrite(ctx context.Context, args []string) ([]byte, error) {
	target := flagArg(args, "-t")
	write := plannedTmuxWrite{args: slices.Clone(args), target: target}
	if args[0] == "rename-window" {
		write.field = "window_name"
		write.before = r.initialWindowName[target]
		if len(args) > 0 {
			write.after = args[len(args)-1]
		}
		write.guardField, write.guardBefore = tmuxopts.WindowUID, r.initialWindowUID[target]
	} else if len(args) >= 3 {
		if slices.Contains(args, "-u") {
			write.field = args[len(args)-1]
			write.after = ""
		} else {
			write.field = args[len(args)-2]
			write.after = args[len(args)-1]
		}
		switch write.field {
		case tmuxopts.WindowUID:
			write.before = r.windowUID[target]
			write.guardField, write.guardBefore = tmuxopts.WindowUID, r.initialWindowUID[target]
			r.windowUID[target] = write.after
			if alias := r.windowAlias[target]; alias != "" {
				r.windowUID[alias] = write.after
			}
		case tmuxopts.PaneUID:
			write.before = r.paneUID[target]
			write.guardField, write.guardBefore = tmuxopts.PaneUID, r.initialPaneUID[target]
			r.paneUID[target] = write.after
		case tmuxopts.AutomaticRenameWindow:
			before, observed := r.windowAutomatic[target]
			if !observed {
				return nil, fmt.Errorf("resource reconciliation planner has no exact automatic-rename receipt for %s", target)
			}
			write.before = before
			write.guardField, write.guardBefore = tmuxopts.WindowUID, r.initialWindowUID[target]
			r.windowAutomatic[target] = write.after
			if alias := r.windowAlias[target]; alias != "" {
				r.windowAutomatic[alias] = write.after
			}
			for index := range r.objects {
				if r.objects[index].kind == coremetadata.KindWindow && (r.objects[index].target == target || r.windowAlias[r.objects[index].target] == target) {
					r.objects[index].automatic = write.after
				}
			}
		default:
			before, err := r.observeOptionBefore(ctx, target, write.field)
			if err != nil {
				return nil, err
			}
			write.before = before
			switch {
			case slices.Contains(args, "-w"):
				write.guardField, write.guardBefore = tmuxopts.WindowUID, r.initialWindowUID[target]
			case slices.Contains(args, "-p"):
				write.guardField, write.guardBefore = tmuxopts.PaneUID, r.initialPaneUID[target]
			default:
				write.guardField, write.guardBefore = tmuxopts.ProjectUIDSession, r.initialSessionUID[target]
			}
			r.optionValues[target][write.field] = write.after
			if values := r.sessionOpts[target]; values != nil {
				values[write.field] = write.after
			}
		}
	}
	write.guardSessionID = r.windowSessionID[target]
	write.guardWindowID = r.paneWindowID[target]
	if write.guardSessionID == "" {
		write.guardSessionID = r.paneSessionID[target]
	}
	r.writes = append(r.writes, write)
	return nil, nil
}

// observeOptionBefore binds every ordinary planned set-option to the exact
// value visible on its exact target before the first write. UID claims,
// automatic-rename, and rename-window have dedicated inventory receipts above;
// all other fields must be read here rather than inferred from the session-only
// overlay. Later writes to the same target/field consume the shadowed value so
// their Before remains the total-order predecessor without planning a mutation.
func (r *resourcePlanTmuxRunner) observeOptionBefore(ctx context.Context, target, field string) (string, error) {
	if values := r.optionValues[target]; values != nil {
		if value, observed := values[field]; observed {
			return value, nil
		}
	}
	out, err := r.reader.Run(ctx, "tmux", "display-message", "-p", "-t", target, "-F", "#{"+field+"}")
	if err != nil {
		return "", fmt.Errorf("resource reconciliation planner cannot observe %s on exact target %s: %w", field, target, err)
	}
	if r.optionValues[target] == nil {
		r.optionValues[target] = map[string]string{}
	}
	value := strings.TrimSpace(string(out))
	r.optionValues[target][field] = value
	return value, nil
}

func (r *resourcePlanTmuxRunner) setLastWriteBefore(value string) {
	if len(r.writes) != 0 {
		r.writes[len(r.writes)-1].before = value
	}
}

func (r *resourcePlanTmuxRunner) observeRead(args []string, out []byte) {
	if args[0] == "display-message" && flagArg(args, "-F") == "#{"+tmuxopts.ProjectPathSession+"}" {
		r.sessionRoots[flagArg(args, "-t")] = strings.TrimSpace(string(out))
		return
	}
	rows := splitResourcePlanRows(string(out))
	switch {
	case args[0] == "list-sessions" && strings.Contains(flagArg(args, "-F"), tmuxopts.ProjectUIDSession):
		for _, row := range rows {
			if len(row) != 5 {
				continue
			}
			r.sessionOpts[row[0]] = map[string]string{
				tmuxopts.ProjectUIDSession: row[2], tmuxopts.ProjectNameSession: row[3], tmuxopts.ProjectPathSession: row[4],
			}
			r.sessionIDByName[row[1]] = row[0]
			if _, observed := r.initialSessionUID[row[0]]; !observed {
				r.initialSessionUID[row[0]] = strings.TrimSpace(row[2])
			}
		}
	case args[0] == "list-windows" && slices.Contains(args, "-a"):
		for _, row := range rows {
			if len(row) != 4 {
				continue
			}
			id, target := row[1], row[2]+":"+row[3]
			uid := strings.TrimSpace(row[0])
			r.windowAlias[id] = target
			r.windowUID[id], r.windowUID[target] = uid, uid
			if _, observed := r.initialWindowUID[id]; !observed {
				r.initialWindowUID[id] = uid
			}
			if _, observed := r.initialWindowUID[target]; !observed {
				r.initialWindowUID[target] = uid
			}
		}
	case args[0] == "list-panes" && slices.Contains(args, "-a"):
		for _, row := range rows {
			if len(row) != 2 {
				continue
			}
			r.paneUID[row[1]] = strings.TrimSpace(row[0])
			if _, observed := r.initialPaneUID[row[1]]; !observed {
				r.initialPaneUID[row[1]] = strings.TrimSpace(row[0])
			}
		}
	case args[0] == "list-windows" && flagArg(args, "-t") != "":
		session := flagArg(args, "-t")
		for _, row := range rows {
			idIndex, uidIndex := 3, 4
			if len(row) == 6 {
				idIndex, uidIndex = 4, 5
			} else if len(row) != 5 {
				continue
			}
			id, uid := row[idIndex], strings.TrimSpace(row[uidIndex])
			coord := session + ":" + row[0]
			sessionID := r.sessionIDByName[session]
			if exactTmuxHandle(session, "$") != "" {
				sessionID = session
			}
			r.windowAlias[id] = coord
			r.windowIDByCoord[coord] = id
			r.windowSessionID[id], r.windowSessionID[coord] = sessionID, sessionID
			r.windowUID[id], r.windowUID[coord] = uid, uid
			if _, observed := r.initialWindowUID[id]; !observed {
				r.initialWindowUID[id] = uid
			}
			if _, observed := r.initialWindowUID[coord]; !observed {
				r.initialWindowUID[coord] = uid
			}
			if _, observed := r.initialWindowName[id]; !observed {
				r.initialWindowName[id] = row[1]
			}
			if _, observed := r.initialWindowName[coord]; !observed {
				r.initialWindowName[coord] = row[1]
			}
			if _, observed := r.windowAutomatic[id]; !observed {
				r.windowAutomatic[id] = row[2]
			}
			if _, observed := r.windowAutomatic[coord]; !observed {
				r.windowAutomatic[coord] = row[2]
			}
			r.addObject(observedPlanObject{kind: coremetadata.KindWindow, target: id, uid: uid, session: session, sessionID: sessionID, windowIndex: row[0], automatic: row[2]})
		}
	case args[0] == "list-panes" && slices.Contains(args, "-s"):
		session := flagArg(args, "-t")
		for _, row := range rows {
			if len(row) != 12 {
				continue
			}
			id, uid := row[8], strings.TrimSpace(row[9])
			sessionID := r.sessionIDByName[session]
			if exactTmuxHandle(session, "$") != "" {
				sessionID = session
			}
			windowID := r.windowIDByCoord[session+":"+row[0]]
			r.paneSessionID[id], r.paneWindowID[id] = sessionID, windowID
			r.paneUID[id] = uid
			if _, observed := r.initialPaneUID[id]; !observed {
				r.initialPaneUID[id] = uid
			}
			r.addObject(observedPlanObject{
				kind: coremetadata.KindPane, target: id, uid: uid, session: session, sessionID: sessionID, windowID: windowID,
				windowIndex: row[0], nameMirror: row[1],
				agentProvider: row[2], agentLaunchAuthorship: strings.TrimSpace(row[3]), agentTopic: row[4],
				agentSessionID: strings.TrimSpace(row[10]), agentThreadID: strings.TrimSpace(row[11]),
			})
		}
	}
}

func (r *resourcePlanTmuxRunner) addObject(object observedPlanObject) {
	key := strings.ToLower(string(object.kind)) + "\x00" + object.target
	if r.seenObject[key] {
		return
	}
	r.seenObject[key] = true
	r.objects = append(r.objects, object)
}

func (r *resourcePlanTmuxRunner) applyOverlay(args []string, out []byte) []byte {
	if !(slices.Contains(args, "-a") && (args[0] == "list-windows" || args[0] == "list-panes")) {
		return out
	}
	rows := splitResourcePlanRows(string(out))
	changed := false
	for _, row := range rows {
		switch {
		case args[0] == "list-windows" && len(row) == 4:
			if uid, ok := r.windowUID[row[1]]; ok && row[0] != uid {
				row[0], changed = uid, true
			}
		case args[0] == "list-panes" && len(row) == 2:
			if uid, ok := r.paneUID[row[1]]; ok && row[0] != uid {
				row[0], changed = uid, true
			}
		}
	}
	if !changed {
		return out
	}
	return []byte(joinResourcePlanRows(rows, strings.Contains(string(out), "\\037")))
}

func (r *resourcePlanTmuxRunner) planItems(before coremetadata.Registry, normalize resourcePlanUIDNormalizer) []resourceReconcileItem {
	known := registryUIDSet(before)
	targetDrift := map[string]resourceDriftKind{}
	for _, write := range r.writes {
		if write.field != tmuxopts.ProjectUIDSession && write.field != tmuxopts.WindowUID && write.field != tmuxopts.PaneUID {
			continue
		}
		drift := resourceDriftStale
		if strings.TrimSpace(write.before) == "" {
			drift = resourceDriftMissing
		} else if !known[strings.TrimSpace(write.before)] {
			drift = resourceDriftForeign
		}
		targetDrift[write.target] = drift
	}
	items := make([]resourceReconcileItem, 0, len(r.writes))
	for _, write := range r.writes {
		drift := targetDrift[write.target]
		if drift == "" {
			drift = resourceDriftStale
		}
		action := "set-option"
		if len(write.args) > 0 && write.args[0] == "rename-window" {
			action = "rename-window"
		}
		kind := "Project"
		if action == "rename-window" || slices.Contains(write.args, "-w") {
			kind = "Window"
		} else if slices.Contains(write.args, "-p") {
			kind = "Pane"
		}
		items = append(items, resourceReconcileItem{
			Key:     write.itemKey(),
			Drift:   drift,
			Surface: "tmux",
			Action:  action,
			Kind:    kind,
			Target:  write.target,
			Field:   write.field,
			Before:  normalize.value(write.before),
			After:   normalize.value(write.after),
			Outcome: "planned",
			Reason:  "Registry authority differs from the live tmux projection",
			Divergence: func() resourcegraph.Divergence {
				switch drift {
				case resourceDriftMissing:
					// The Registry row already exists and the planner is
					// projecting that authority back to its exact runtime
					// object. A missing mirror is D5 drift, not D2 runtime
					// identity with no Registry attribution.
					return resourcegraph.DivergenceDrifted
				case resourceDriftForeign:
					return resourcegraph.DivergenceOrphanMirror
				default:
					return resourcegraph.DivergenceDrifted
				}
			}(),
			tmuxArgs: slices.Clone(write.args),
		})
	}
	return items
}

func (r *resourcePlanTmuxRunner) foreignItems(registry coremetadata.Registry, reconciler *registryReconciler) []resourceReconcileItem {
	known := registryUIDSet(registry)
	bySession := reconciler.projectsBySessionName(registry)
	for session, root := range r.sessionRoots {
		if project, ok := registry.ProjectByRoot(root); ok {
			bySession[session] = project.Metadata.UID
		}
	}
	counts := map[string]int{}
	for _, object := range r.objects {
		if object.uid != "" {
			counts[strings.ToLower(string(object.kind))+"\x00"+object.uid]++
		}
	}
	var items []resourceReconcileItem
	for _, object := range r.objects {
		uid := strings.TrimSpace(object.uid)
		if uid == "" || strings.HasPrefix(uid, coremetadata.DeletedPaneMirrorPrefix) {
			continue
		}
		if object.kind == coremetadata.KindPane && known[uid] && counts[strings.ToLower(string(object.kind))+"\x00"+uid] == 1 {
			windowUID := observedWindowUIDForPane(object, r.objects)
			refusal := coremetadata.AgentPanePromotionRefusal(&registry, windowUID, uid, coremetadata.LegacyPane{
				Provider: object.agentProvider, LaunchAuthorship: object.agentLaunchAuthorship,
				Topic: object.agentTopic, SessionID: object.agentSessionID, ThreadID: object.agentThreadID,
			})
			if refusal != "" {
				items = append(items, resourceReconcileItem{
					Key: "tmux:refuse:agent-marker:" + object.target, Drift: resourceDriftForeign, Surface: "tmux", Action: "refuse",
					Kind: string(coremetadata.KindAgent), Target: object.target, Field: tmuxopts.AgentProviderPane,
					Before: object.agentProvider, Outcome: "refused", Reason: refusal,
					Divergence: resourcegraph.DivergenceUnattributed, refused: true,
				})
				continue
			}
		}
		reason := ""
		if !known[uid] {
			reason = "live object carries a uid absent from the authoritative Registry"
		} else if counts[strings.ToLower(string(object.kind))+"\x00"+uid] > 1 {
			reason = "multiple live objects claim the same Registry uid"
		} else if object.kind == coremetadata.KindWindow {
			window, _ := registry.Window(uid)
			owner := window.Metadata.OwnerUID()
			// A ControlSession owner is not Project drift and must not be
			// reported as though it were: the Registry says an app-owned session
			// owns this Window, so "outside the exact Project owner scope" would
			// name a scope the Window was never in. Sitting in the session its
			// own ControlSession is bound to is the converged state, and a
			// converged object is not drift at all -- so it is not listed. Only
			// a control-owned Window observed somewhere else is, with the owner
			// scope it actually has.
			if control, ok := registry.ControlSession(owner); ok {
				if strings.TrimSpace(control.Spec.Session) != object.session {
					reason = "live Window uid is outside the exact ControlSession owner scope"
				}
			} else if projectUID := bySession[object.session]; projectUID == "" || owner != projectUID {
				reason = "live Window uid is outside the exact Project owner scope"
			}
		} else if object.kind == coremetadata.KindPane && !paneOwnedByObservedWindow(registry, object, r.objects) {
			reason = "live Pane uid is outside the exact Window owner scope"
		}
		if reason == "" {
			continue
		}
		items = append(items, resourceReconcileItem{
			Key:     "tmux:refuse:" + strings.ToLower(string(object.kind)) + ":" + object.target,
			Drift:   resourceDriftForeign,
			Surface: "tmux",
			Action:  "refuse",
			Kind:    string(object.kind),
			Target:  object.target,
			Before:  uid,
			Outcome: "refused",
			Reason:  reason,
			Divergence: func() resourcegraph.Divergence {
				if !known[uid] {
					return resourcegraph.DivergenceOrphanMirror
				}
				return resourcegraph.DivergenceContaminated
			}(),
			refused: true,
		})
	}
	return items
}

func paneOwnedByObservedWindow(registry coremetadata.Registry, pane observedPlanObject, objects []observedPlanObject) bool {
	resource, ok := registry.Pane(pane.uid)
	if !ok {
		return false
	}
	windowUID := observedWindowUIDForPane(pane, objects)
	if windowUID == "" {
		return false
	}
	owner := resource.Metadata.OwnerUID()
	if owner == windowUID {
		return true
	}
	agent, ok := registry.Agent(owner)
	return ok && agent.Metadata.OwnerUID() == windowUID
}

func observedWindowUIDForPane(pane observedPlanObject, objects []observedPlanObject) string {
	for _, object := range objects {
		if object.kind == coremetadata.KindWindow && object.session == pane.session && object.windowIndex == pane.windowIndex {
			return strings.TrimSpace(object.uid)
		}
	}
	return ""
}

func flagArg(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func splitResourcePlanRows(output string) [][]string {
	output = strings.ReplaceAll(output, "\\037", "\x1f")
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\x1f"))
	}
	return rows
}

func joinResourcePlanRows(rows [][]string, escaped bool) string {
	separator := "\x1f"
	if escaped {
		separator = "\\037"
	}
	var b strings.Builder
	for _, row := range rows {
		b.WriteString(strings.Join(row, separator))
		b.WriteByte('\n')
	}
	return b.String()
}

var _ intmetadata.Runner = (*resourcePlanTmuxRunner)(nil)
