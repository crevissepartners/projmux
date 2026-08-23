package app

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

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
	tmuxArgs        []string
	refused         bool
	registry        bool
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
			state, badge, attention := agentTmuxProjection(interaction.Kind)
			manual := ""
			if topic != "" {
				manual = "on"
			}
			for _, desired := range []struct {
				field, value string
			}{
				{aiPaneTopicOption, topic},
				{aiPaneTopicManualOption, manual},
				{aiPaneStateOption, state},
				{aiPaneBadgeKindOption, badge},
				{attentionStateOption, attention},
			} {
				if err := planExactPaneOption(ctx, recorder, target, desired.field, desired.value); err != nil {
					return err
				}
			}
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
	exactTarget        explicitTmuxTarget
	// agents is the provider-launch seam the Agent half of a materialization
	// plan consumes. It is read-only at plan time: it builds argv and applies
	// the Settings gate, and creates nothing.
	agents               topologyAgentLauncher
	approvedOrphanImport bool
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
	reconciler.refuseForeign = true
	reconciler.targetLiveOnly = true
	reconciler.approvedOrphanImport = p.approvedOrphanImport
	projectSessions, err := observeResourceProjectSessions(ctx, recorder)
	if err != nil {
		if p.materializeProject == "" || !isMissingTmuxServer(err) {
			return resourceReconcilePlan{}, err
		}
		projectSessions = nil
	}
	topology, err := planRegistryTopology(ctx, p.reader, before, p.materializeProject, reconciler, projectSessions, p.exactTarget, p.agents)
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
	if err := reconciler.reconcile(ctx, &scopedAfter, p.store.mutator(), "reconcile-resources"); err != nil {
		return resourceReconcilePlan{}, err
	}
	after := mergeScopedResourceRegistry(before, scopedBefore, scopedAfter, projectSessions, reconciler)
	if err := planResourceBoundMirrorDrift(ctx, recorder, after, reconciler); err != nil {
		return resourceReconcilePlan{}, err
	}
	if err := planResourceAgentProjections(ctx, recorder, after, time.Now()); err != nil {
		return resourceReconcilePlan{}, err
	}
	if err := planResourceProjectMirrors(ctx, recorder, after, projectSessions, reconciler); err != nil {
		return resourceReconcilePlan{}, err
	}
	// Mutators may refresh UpdatedAt while finding no durable resource change.
	// Match resourceStore.converge so repeat plans remain byte-stable.
	updatedAt := after.UpdatedAt
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(after.Normalize(), before.Normalize()) {
		after.UpdatedAt = updatedAt
	}

	normalize := newPlanUIDNormalizer(before, after)
	items := registryReconcileItems(before, after, normalize)
	items = append(items, recorder.planItems(before, normalize)...)
	items = append(items, resourceProjectForeignItems(before, projectSessions, reconciler)...)
	items = append(items, recorder.foreignItems(before, reconciler)...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if err := validateResourcePlanItems(items); err != nil {
		return resourceReconcilePlan{}, err
	}
	return resourceReconcilePlan{registry: after, items: items, writes: slices.Clone(recorder.writes)}, nil
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
	out.Projects, out.Windows, out.Panes, out.Agents = retained.Projects, retained.Windows, retained.Panes, retained.Agents
	changed := resourceRegistryProjectGraph(scopedAfter, activeAfter)
	out.Projects = append(out.Projects, changed.Projects...)
	out.Windows = append(out.Windows, changed.Windows...)
	out.Panes = append(out.Panes, changed.Panes...)
	out.Agents = append(out.Agents, changed.Agents...)
	out.NameReservations = slices.Clone(scopedAfter.NameReservations)
	out.UpdatedAt = scopedAfter.UpdatedAt
	return retainReservedResourceNames(out.Normalize())
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
		if claim := resourceProjectSessionClaim(registry, session, reconciler); claim != "" && claims[claim] > 1 {
			refused[session.name] = true
			continue
		}
		if session.uid == "" {
			if session.root == "" && len(resourceSessionProjectClaims(registry, session.name, reconciler)) > 1 {
				refused[session.name] = true
			}
			continue
		}
		expected, ok := resourceProjectForSession(registry, session, reconciler)
		if !ok || session.uid != expected.Metadata.UID || counts[session.uid] > 1 {
			refused[session.name] = true
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
		reason := "live session carries a Project uid outside its exact Registry identity"
		if session.uid == "" && session.root == "" && len(resourceSessionProjectClaims(registry, session.name, reconciler)) > 1 {
			reason = "multiple Registry Projects claim the live session-name edge"
		} else if claim := resourceProjectSessionClaim(registry, session, reconciler); claim != "" && claims[claim] > 1 {
			reason = "multiple live sessions resolve to the same exact Registry Project"
		} else if counts[session.uid] > 1 {
			reason = "multiple live sessions claim the same Registry Project uid"
		} else if _, ok := registry.Project(session.uid); !ok {
			reason = "live session carries a Project uid absent from the authoritative Registry"
		}
		items = append(items, resourceReconcileItem{
			Key: "tmux:refuse:project:" + session.id, Drift: resourceDriftForeign, Surface: "tmux", Action: "refuse",
			Kind: string(coremetadata.KindProject), Target: session.id, Field: tmuxopts.ProjectUIDSession,
			Before: session.uid, Outcome: "refused", Reason: reason, refused: true,
			Divergence: func() resourcegraph.Divergence {
				if _, ok := registry.Project(session.uid); !ok && session.uid != "" {
					return resourcegraph.DivergenceOrphanMirror
				}
				return resourcegraph.DivergenceContaminated
			}(),
		})
	}
	return items
}

type resourcePlanUIDNormalizer struct {
	created map[string]string
}

func newPlanUIDNormalizer(before, after coremetadata.Registry) resourcePlanUIDNormalizer {
	known := registryUIDSet(before)
	created := map[string]string{}
	counts := map[coremetadata.Kind]int{}
	for _, record := range registryResourceRecords(after) {
		if known[record.uid] {
			continue
		}
		counts[record.kind]++
		created[record.uid] = fmt.Sprintf("<allocated-%s-%d>", strings.ToLower(string(record.kind)), counts[record.kind])
	}
	return resourcePlanUIDNormalizer{created: created}
}

func (n resourcePlanUIDNormalizer) value(value string) string {
	if normalized := n.created[value]; normalized != "" {
		return normalized
	}
	return value
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
		target := strings.ToLower(string(record.kind)) + "/" + record.name
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
	kind        coremetadata.Kind
	target      string
	uid         string
	session     string
	windowIndex string
	nameMirror  string
	automatic   string
}

type plannedTmuxWrite struct {
	args        []string
	target      string
	field       string
	before      string
	after       string
	guardField  string
	guardBefore string
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
	initialPaneUID    map[string]string
	initialSessionUID map[string]string
}

func newResourcePlanTmuxRunner(reader tmuxCommandRunner) *resourcePlanTmuxRunner {
	return &resourcePlanTmuxRunner{
		reader: reader, windowUID: map[string]string{}, paneUID: map[string]string{}, windowAlias: map[string]string{},
		sessionRoots: map[string]string{}, seenObject: map[string]bool{}, sessionOpts: map[string]map[string]string{},
		initialWindowUID: map[string]string{}, initialPaneUID: map[string]string{}, initialSessionUID: map[string]string{},
	}
}

func (r *resourcePlanTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) == 0 {
		return nil, fmt.Errorf("resource reconciliation planner expected tmux command, got %s %v", name, args)
	}
	if args[0] == "set-option" || args[0] == "rename-window" {
		return r.recordWrite(args)
	}
	out, err := r.reader.Run(ctx, name, args...)
	if err != nil {
		return out, err
	}
	out = r.applyOverlay(args, out)
	r.observeRead(args, out)
	return out, nil
}

func (r *resourcePlanTmuxRunner) recordWrite(args []string) ([]byte, error) {
	target := flagArg(args, "-t")
	write := plannedTmuxWrite{args: slices.Clone(args), target: target}
	if args[0] == "rename-window" {
		write.field = "window_name"
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
		default:
			switch {
			case slices.Contains(args, "-w"):
				write.guardField, write.guardBefore = tmuxopts.WindowUID, r.initialWindowUID[target]
			case slices.Contains(args, "-p"):
				write.guardField, write.guardBefore = tmuxopts.PaneUID, r.initialPaneUID[target]
			default:
				write.guardField, write.guardBefore = tmuxopts.ProjectUIDSession, r.initialSessionUID[target]
			}
			if values := r.sessionOpts[target]; values != nil {
				write.before = values[write.field]
				values[write.field] = write.after
			}
		}
	}
	r.writes = append(r.writes, write)
	return nil, nil
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
			r.windowAlias[id] = coord
			r.windowUID[id], r.windowUID[coord] = uid, uid
			if _, observed := r.initialWindowUID[id]; !observed {
				r.initialWindowUID[id] = uid
			}
			if _, observed := r.initialWindowUID[coord]; !observed {
				r.initialWindowUID[coord] = uid
			}
			r.addObject(observedPlanObject{kind: coremetadata.KindWindow, target: id, uid: uid, session: session, windowIndex: row[0], automatic: row[2]})
		}
	case args[0] == "list-panes" && slices.Contains(args, "-s"):
		session := flagArg(args, "-t")
		for _, row := range rows {
			if len(row) != 11 {
				continue
			}
			id, uid := row[7], strings.TrimSpace(row[8])
			r.paneUID[id] = uid
			if _, observed := r.initialPaneUID[id]; !observed {
				r.initialPaneUID[id] = uid
			}
			r.addObject(observedPlanObject{kind: coremetadata.KindPane, target: id, uid: uid, session: session, windowIndex: row[0], nameMirror: row[1]})
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
	windowUID := ""
	for _, object := range objects {
		if object.kind == coremetadata.KindWindow && object.session == pane.session && object.windowIndex == pane.windowIndex {
			windowUID = object.uid
			break
		}
	}
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
