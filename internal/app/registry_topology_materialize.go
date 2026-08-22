package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// registryTopologyPlan is the immutable Project-scoped desired-topology plan
// shared by dry-run and execute. It contains persistent Project, Window,
// Window-owned shell Pane, and Agent work. A stored Pane *command* still has no
// representation here, which makes replaying one impossible by design; an
// Agent's launch, by contrast, is fixed at plan time from the provider seams
// `create agent` already owns, so nothing here can invent a third way to start
// a provider either.
type registryTopologyPlan struct {
	project     coremetadata.Project
	sessionName string
	sessionLive bool
	windows     []registryTopologyWindowPlan
	items       []resourceReconcileItem
	// notices are the operator-facing disclosures this plan owes: which stored
	// Agents will not rejoin their recorded conversation, and why. They are
	// deliberately not plan items -- an Agent that comes back on a new
	// conversation is converged work, and an Agent that cannot come back at all
	// is not work this pass will do, so counting either as drift would make a
	// stable topology report churn forever.
	notices []string
}

type registryTopologyWindowPlan struct {
	window  coremetadata.Window
	liveID  string
	create  bool
	primary coremetadata.Pane
	panes   []registryTopologyPanePlan
	agents  []registryTopologyAgentPlan
}

type registryTopologyPanePlan struct {
	pane   coremetadata.Pane
	liveID string
	create bool
}

type observedTopologyWindow struct {
	uid, id, name string
}

type observedTopologyPane struct {
	uid, id string
}

func planRegistryTopology(
	ctx context.Context,
	runner tmuxCommandRunner,
	registry coremetadata.Registry,
	projectRef string,
	reconciler *registryReconciler,
	sessions []observedResourceProjectSession,
	exactTarget explicitTmuxTarget,
	launcher topologyAgentLauncher,
) (*registryTopologyPlan, error) {
	if strings.TrimSpace(projectRef) == "" {
		return nil, nil
	}
	project, err := resolveMaterializeProject(registry, projectRef)
	if err != nil {
		return nil, err
	}
	plan := &registryTopologyPlan{project: project}
	plan.sessionName = materializeSessionName(project, reconciler)
	if plan.sessionName == "" {
		plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindProject, project.Metadata.Name, "status.session.name and the configured persistent session name are both empty")
		return plan, nil
	}
	if reason := validateMaterializeDirectory(project.Spec.Root, "Project root"); reason != "" {
		plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindProject, project.Metadata.Name, reason)
		return plan, nil
	}
	if condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot); ok && condition.Status == coremetadata.ConditionTrue {
		plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindProject, project.Metadata.Name, "Project carries a MissingRoot condition")
		return plan, nil
	}

	var target *observedResourceProjectSession
	for i := range sessions {
		session := sessions[i]
		claimsUID := session.uid == project.Metadata.UID
		claimsName := session.name == plan.sessionName
		if !claimsUID && !claimsName {
			continue
		}
		if target != nil {
			plan.refuse(resourcegraph.DivergenceContaminated, coremetadata.KindProject, project.Metadata.Name, "multiple live sessions claim the selected Project")
			return plan, nil
		}
		if session.uid != "" && session.uid != project.Metadata.UID {
			plan.refuse(resourcegraph.DivergenceContaminated, coremetadata.KindProject, project.Metadata.Name, "the expected session name carries a foreign Project uid")
			return plan, nil
		}
		if session.uid == "" {
			plan.refuse(resourcegraph.DivergenceUnattributed, coremetadata.KindProject, project.Metadata.Name, "live session lacks the selected Project's exact uid binding")
			return plan, nil
		}
		if claimsUID && session.name != plan.sessionName {
			plan.refuse(resourcegraph.DivergenceDrifted, coremetadata.KindProject, project.Metadata.Name, "the Project uid is live under a different session name")
			return plan, nil
		}
		if session.root != "" && filepath.Clean(session.root) != filepath.Clean(project.Spec.Root) {
			plan.refuse(resourcegraph.DivergenceDrifted, coremetadata.KindProject, project.Metadata.Name, "the live session carries a foreign Project root")
			return plan, nil
		}
		target = &session
	}
	if target == nil {
		plan.addItem(0, coremetadata.KindProject, project.Metadata.Name, project.Metadata.UID, "materialize")
		if exactTarget.flag == "-S" {
			plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindProject, project.Metadata.Name,
				"offline Project session creation via --socket-path cannot preserve exact name-only PROJMUX_SOCKET hook re-entry")
		}
	} else {
		plan.sessionLive = true
	}

	windows := registry.WindowsOf(project.Metadata.UID)
	if len(windows) == 0 {
		plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindProject, project.Metadata.Name, "selected Project has no Registry Window topology")
		return plan, nil
	}
	var liveWindows []observedTopologyWindow
	if target != nil {
		out, readErr := runner.Run(ctx, "tmux", "list-windows", "-t", target.name, "-F", tmuxRowFormat("#{"+tmuxopts.WindowUID+"}", "#{window_id}", "#{window_name}"))
		if readErr != nil {
			return nil, fmt.Errorf("inspect selected Project windows: %w", readErr)
		}
		for _, row := range splitTmuxRows(string(out), 3) {
			liveWindows = append(liveWindows, observedTopologyWindow{uid: strings.TrimSpace(row[0]), id: row[1], name: row[2]})
		}
	}
	knownWindows := map[string]coremetadata.Window{}
	for _, window := range windows {
		knownWindows[window.Metadata.UID] = window
	}
	windowClaims := map[string]int{}
	for _, live := range liveWindows {
		if live.uid == "" {
			plan.refuse(resourcegraph.DivergenceUnattributed, coremetadata.KindWindow, live.id, "uid-less live Window cannot be heuristically adopted")
			continue
		}
		windowClaims[live.uid]++
		if _, ok := knownWindows[live.uid]; !ok {
			divergence := resourcegraph.DivergenceOrphanMirror
			if _, exists := registry.Window(live.uid); exists {
				divergence = resourcegraph.DivergenceContaminated
			}
			plan.refuse(divergence, coremetadata.KindWindow, live.id, "live Window uid is not owned by the selected Project")
		}
	}
	for uid, count := range windowClaims {
		if count > 1 {
			plan.refuse(resourcegraph.DivergenceContaminated, coremetadata.KindWindow, uid, "multiple live Windows claim one Registry uid")
		}
	}
	if plan.hasRefusal() {
		return plan, nil
	}

	for wi, window := range windows {
		work := registryTopologyWindowPlan{window: window}
		for _, live := range liveWindows {
			if live.uid == window.Metadata.UID {
				work.liveID = live.id
				break
			}
		}
		primary, ok := registry.Pane(strings.TrimSpace(window.Spec.PrimaryPaneRef))
		if !ok || primary.Spec.Role != coremetadata.PaneRoleShell || primary.Metadata.OwnerUID() != window.Metadata.UID {
			plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindWindow, window.Metadata.Name, "spec.primaryPaneRef must resolve to a Window-owned shell Pane")
			continue
		}
		work.primary = *primary
		eligible := registry.PanesOf(window.Metadata.UID)
		// A stored Pane whose cwd is gone is one item of the desired topology, not
		// a verdict on the Project. It is refused here and left out of the plan, so
		// the tmux pass never tries to open a directory that does not exist and the
		// Window still comes back with the Panes that can be built. A refused
		// *primary* is different: the Window is created from its primary Pane's
		// cwd, so that one takes the Window with it.
		refusedPanes := map[string]bool{}
		for _, pane := range eligible {
			if pane.Spec.Role != coremetadata.PaneRoleShell {
				continue
			}
			if reason := validateMaterializeDirectory(materializePaneCWD(project, pane), "Pane cwd"); reason != "" {
				plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindPane, pane.Metadata.Name, reason)
				refusedPanes[pane.Metadata.UID] = true
			}
		}
		if refusedPanes[work.primary.Metadata.UID] {
			plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindWindow, window.Metadata.Name,
				"the Window's primary Pane cwd cannot be materialized, so the Window has no cwd to be created from")
			continue
		}
		if work.liveID == "" {
			work.create = true
			plan.addItem(wi+1, coremetadata.KindWindow, window.Metadata.Name, window.Metadata.UID, "materialize")
			for pi, pane := range eligible {
				if pane.Spec.Role != coremetadata.PaneRoleShell || refusedPanes[pane.Metadata.UID] {
					continue
				}
				work.panes = append(work.panes, registryTopologyPanePlan{pane: pane, create: true})
				plan.addItem((wi+1)*1000+pi, coremetadata.KindPane, window.Metadata.Name+"/"+pane.Metadata.Name, pane.Metadata.UID, "materialize")
			}
			work.agents = planTopologyWindowAgents(plan, registry, project, window, wi+1, nil, launcher)
			plan.windows = append(plan.windows, work)
			continue
		}

		livePanes, readErr := observeTopologyPanes(ctx, runner, work.liveID)
		if readErr != nil {
			return nil, readErr
		}
		knownPanes := map[string]coremetadata.Pane{}
		for _, pane := range eligible {
			if pane.Spec.Role == coremetadata.PaneRoleShell {
				knownPanes[pane.Metadata.UID] = pane
			}
		}
		for _, agent := range registry.AgentsOf(window.Metadata.UID) {
			for _, pane := range registry.PanesOf(agent.Metadata.UID) {
				knownPanes[pane.Metadata.UID] = pane // existing Agent panes are valid but never planned.
			}
		}
		claims := map[string]int{}
		var unclaimed []observedTopologyPane
		for _, live := range livePanes {
			if live.uid == "" {
				unclaimed = append(unclaimed, live)
				continue
			}
			claims[live.uid]++
			if _, ok := knownPanes[live.uid]; !ok {
				divergence := resourcegraph.DivergenceOrphanMirror
				if _, exists := registry.Pane(live.uid); exists {
					divergence = resourcegraph.DivergenceContaminated
				}
				plan.refuse(divergence, coremetadata.KindPane, live.id, "live Pane uid has the wrong owner or is absent from the selected Window graph")
			}
		}
		for uid, count := range claims {
			if count > 1 {
				plan.refuse(resourcegraph.DivergenceContaminated, coremetadata.KindPane, uid, "multiple live Panes claim one Registry uid")
			}
		}
		for pi, pane := range eligible {
			if pane.Spec.Role != coremetadata.PaneRoleShell || refusedPanes[pane.Metadata.UID] {
				continue
			}
			paneWork := registryTopologyPanePlan{pane: pane}
			for _, live := range livePanes {
				if live.uid == pane.Metadata.UID {
					paneWork.liveID = live.id
					break
				}
			}
			if paneWork.liveID == "" {
				paneWork.create = true
				plan.addItem((wi+1)*1000+pi, coremetadata.KindPane, window.Metadata.Name+"/"+pane.Metadata.Name, pane.Metadata.UID, "materialize")
			}
			work.panes = append(work.panes, paneWork)
		}
		if len(unclaimed) != 0 {
			plan.refuse(resourcegraph.DivergenceUnattributed, coremetadata.KindPane, work.liveID, "pre-existing uid-less live Pane cannot be heuristically adopted")
		}
		primaryLive := false
		shellAnchorLive := false
		for _, paneWork := range work.panes {
			if paneWork.liveID == "" {
				continue
			}
			shellAnchorLive = true
			if paneWork.pane.Metadata.UID == work.primary.Metadata.UID {
				primaryLive = true
			}
		}
		if !primaryLive && !shellAnchorLive {
			plan.refuse(resourcegraph.DivergenceUnrealized, coremetadata.KindWindow, window.Metadata.Name, "missing primary Pane has no exact-bound Window-owned shell anchor")
		}
		work.agents = planTopologyWindowAgents(plan, registry, project, window, wi+1, livePanes, launcher)
		plan.windows = append(plan.windows, work)
	}
	return plan, nil
}

func resolveMaterializeProject(registry coremetadata.Registry, raw string) (coremetadata.Project, error) {
	ref, err := selector.ParseRef(coremetadata.KindProject, raw)
	if err != nil {
		return coremetadata.Project{}, MapMetadataError(err)
	}
	resolution, err := selector.New(registry).ResolveProjects(selector.Query{Project: &ref})
	if err != nil {
		return coremetadata.Project{}, MapMetadataError(err)
	}
	if len(resolution.Matches) != 1 {
		return coremetadata.Project{}, usageError(fmt.Sprintf("reconcile resources --materialize-project %q must resolve exactly one Project", raw))
	}
	project, ok := registry.Project(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Project{}, fmt.Errorf("selected Project disappeared during planning")
	}
	return *project, nil
}

func materializeSessionName(project coremetadata.Project, reconciler *registryReconciler) string {
	if project.Status.Session != nil && strings.TrimSpace(project.Status.Session.Name) != "" {
		return strings.TrimSpace(project.Status.Session.Name)
	}
	if reconciler != nil && reconciler.sessionNameFor != nil {
		return strings.TrimSpace(reconciler.sessionNameFor(project.Spec.Root))
	}
	return ""
}

func materializePaneCWD(project coremetadata.Project, pane coremetadata.Pane) string {
	if cwd := strings.TrimSpace(pane.Spec.CWD); cwd != "" {
		return cwd
	}
	return project.Spec.Root
}

func validateMaterializeDirectory(path, label string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Sprintf("%s %q must be a clean absolute directory", label, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("%s %q is not an existing directory", label, path)
	}
	return ""
}

func observeTopologyPanes(ctx context.Context, runner tmuxCommandRunner, windowID string) ([]observedTopologyPane, error) {
	out, err := runner.Run(ctx, "tmux", "list-panes", "-t", windowID, "-F", tmuxRowFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("inspect selected Registry Window panes: %w", err)
	}
	var panes []observedTopologyPane
	for _, row := range splitTmuxRows(string(out), 2) {
		panes = append(panes, observedTopologyPane{uid: strings.TrimSpace(row[0]), id: row[1]})
	}
	return panes, nil
}

func (p *registryTopologyPlan) addItem(order int, kind coremetadata.Kind, target, uid, action string) {
	p.items = append(p.items, resourceReconcileItem{
		Key:   fmt.Sprintf("tmux:materialize:%06d:%s:%s", order, strings.ToLower(string(kind)), uid),
		Drift: resourceDriftMissing, Surface: "tmux", Action: action, Kind: string(kind), Target: target,
		After: "uid:" + uid, Outcome: "planned",
		Reason:     "Registry resource has no materialized runtime object",
		Divergence: resourcegraph.DivergenceUnrealized,
	})
}

func (p *registryTopologyPlan) refuse(divergence resourcegraph.Divergence, kind coremetadata.Kind, target, reason string) {
	p.items = append(p.items, resourceReconcileItem{
		Key:   "tmux:materialize:refuse:" + strings.ToLower(string(kind)) + ":" + target,
		Drift: resourceDriftForeign, Surface: "tmux", Action: "refuse", Kind: string(kind), Target: target,
		Outcome: "refused", Reason: reason, refused: true,
		Divergence: divergence,
	})
}

// noteAgent records that one stored Agent will not be brought back at all.
func (p *registryTopologyPlan) noteAgent(label, reason string) {
	p.notices = append(p.notices, fmt.Sprintf("projmux: agent/%s was not restored: %s", label, reason))
}

// noteNewConversation records that one stored Agent comes back on a *new*
// provider conversation rather than the one it recorded.
func (p *registryTopologyPlan) noteNewConversation(label, reason string) {
	p.notices = append(p.notices,
		fmt.Sprintf("projmux: agent/%s starts a new conversation instead of resuming: %s", label, reason))
}

// writeNotices discloses the Agent decisions of one plan. A write failure is
// never fatal: the disclosure is a diagnostic, and losing it must not turn a
// converged topology into a failed open.
func (p *registryTopologyPlan) writeNotices(w io.Writer) {
	if p == nil || w == nil {
		return
	}
	for _, notice := range p.notices {
		fmt.Fprintln(w, notice)
	}
	for _, notice := range p.skipNotices() {
		fmt.Fprintln(w, notice)
	}
}

// refusalScope splits one plan's refusals into the ones that end the pass and the
// ones the pass can skip.
//
// A refused Project is the pass. There is no partial answer to "this Project's
// identity, root, or session projection is wrong", and every item below it was
// planned against that answer.
//
// A refused Window or Pane is one item of the stored topology. Ending the whole
// activation because one stored item cannot be built is what made a single
// deleted worktree directory, or one Window that had only ever hosted Agents,
// fatal to everything else the Project had. The kernel this plan feeds already
// states the rule the other way round: a refusal is a recorded outcome with a
// stated reason, not an error.
//
// The split is restricted to an offline activation on purpose. Once a live
// session claims the Project, a refusal is a statement about runtime objects the
// operator is using right now, and the ownership guards are the only thing
// keeping this pass out of somebody else's window. That case keeps the
// all-or-nothing contract it shipped with.
func (p *registryTopologyPlan) refusalScope() (fatal, skipped []resourceReconcileItem) {
	if p == nil {
		return nil, nil
	}
	for _, item := range p.items {
		if !item.refused {
			continue
		}
		if p.sessionLive || item.Kind == string(coremetadata.KindProject) {
			fatal = append(fatal, item)
			continue
		}
		skipped = append(skipped, item)
	}
	return fatal, skipped
}

// skipNotices renders the operator disclosure for every stored item this pass
// leaves out.
//
// It is derived from the plan rather than accumulated while planning, so a
// refusal cannot be recorded as a plan item and then quietly fail to be
// disclosed: the two come from one source. An activation that dropped part of the
// stored topology in silence would be indistinguishable from one that restored
// all of it.
func (p *registryTopologyPlan) skipNotices() []string {
	_, skipped := p.refusalScope()
	out := make([]string, 0, len(skipped))
	for _, item := range skipped {
		out = append(out, fmt.Sprintf("projmux: %s/%s was not restored: %s",
			strings.ToLower(item.Kind), item.Target, item.Reason))
	}
	return out
}

func (p *registryTopologyPlan) hasRefusal() bool {
	return slices.ContainsFunc(p.items, func(item resourceReconcileItem) bool { return item.refused })
}

// isMissingTmuxServer recognizes an absent tmux server, which is the ordinary
// state of an offline Project rather than a planning failure. The typed
// CommandFailure seam is authoritative; the narrow text fallback only covers
// runners that lose the typed carrier while wrapping.
func isMissingTmuxServer(err error) bool {
	if inttmux.IsNoServerFailure(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no server running on ") ||
		strings.Contains(message, "failed to connect to server: connection refused")
}

// topologyMaterializeRun is one exact-socket Project materialization
// transaction, deliberately separate from any reporting surface.
//
// The public `reconcile resources --materialize-project` route and
// closed-Project startup activation are two different user-visible results over
// exactly one engine. Keeping the Registry lock, the prevalidation order, the
// tmux write pass, and the ownership-checked rollback here is what stops the
// startup path from growing a second, weaker copy of that contract.
type topologyMaterializeRun struct {
	resources          *resourceStore
	runner             tmuxCommandRunner
	target             explicitTmuxTarget
	expectedSocketPath string
	diagnostics        *diagnostics.LifecycleRecorder
	newOperationID     func() (string, error)
	newGeneration      func() (string, error)
	newMaterializer    func(tmuxCommandRunner, io.Writer) *materializer
	// agents is the provider-launch seam of the Agent half. It is the exact
	// object `create agent` and `agent resume` consume, so a replayed Agent's
	// argv is built by the shipped launch builders rather than a second copy.
	agents topologyAgentLauncher
	// notices receives the plan's operator-facing Agent disclosures. It is a
	// separate writer from the rollback `warn` stream because the two have
	// different audiences: startup discards rollback diagnostics and must still
	// tell the operator which Agents did not rejoin their conversation.
	notices io.Writer
	// recoveryTrigger is the closed authority-table trigger. The public
	// materialize flag is an explicitly approved reconcile; startup is the
	// distinct Project-open automatic cell.
	recoveryTrigger  controller.RecoveryTrigger
	recoveryApproved bool
}

// topologyMaterializeOutcome is the bookkeeping a caller needs to report. The
// plan it carries is the one rechecked under the Registry lock, never the
// caller's earlier preview.
type topologyMaterializeOutcome struct {
	plan            resourceReconcilePlan
	completed       []string
	failedStage     string
	registryChanged bool
}

// defaultTopologyMaterializer builds the runtime for one exact target. The tmux
// client is the existing one, so the public pre/post-create and startup hook
// contract has exactly one implementation.
func defaultTopologyMaterializer(target explicitTmuxTarget, recorder *diagnostics.LifecycleRecorder) func(tmuxCommandRunner, io.Writer) *materializer {
	return func(runner tmuxCommandRunner, warn io.Writer) *materializer {
		opts := []inttmux.ClientOption{}
		if target.flag == "-L" {
			opts = append(opts, inttmux.WithSocketName(target.value))
		}
		if recorder != nil {
			opts = append(opts, inttmux.WithLifecycleDiagnostics(recorder))
		}
		if hooks := defaultLifecycleHookRunner(); hooks != nil {
			opts = append(opts, inttmux.WithLifecycleHookRunner(hooks))
		}
		return &materializer{runner: runner, mirror: intmetadata.NewMirror(runner), sessions: inttmux.NewClient(runner, opts...), target: target, warn: warn}
	}
}

func (r topologyMaterializeRun) execute(ctx context.Context, planner resourceReconcilePlanner, warn io.Writer) (topologyMaterializeOutcome, error) {
	var outcome topologyMaterializeOutcome
	if r.resources == nil || r.resources.updateConvergent == nil {
		return outcome, errors.New("resource topology materialization write store is not configured")
	}
	newID := r.newOperationID
	if newID == nil {
		newID = newCreateOperationID
	}
	operationID, err := newID()
	if err != nil {
		return outcome, err
	}
	ledger := newRuntimeLedger(operationID)
	routed := explicitTmuxRunner{runner: r.runner, target: r.target}
	newRuntime := r.newMaterializer
	if newRuntime == nil {
		newRuntime = defaultTopologyMaterializer(r.target, r.diagnostics)
	}
	runtime := newRuntime(routed, warn)
	if runtime != nil && runtime.expectedSocketPath == "" {
		runtime.expectedSocketPath = r.expectedSocketPath
	}
	_, registryChanged, updateErr := r.resources.updateConvergent(func(working *coremetadata.Registry) error {
		current, buildErr := planner.build(ctx, working.Clone())
		if buildErr != nil {
			outcome.failedStage = "locked plan"
			return buildErr
		}
		outcome.plan = current
		outcome.completed = append(outcome.completed, "plan rechecked under Registry lock")
		if err := r.authorizeRecovery(&current); err != nil {
			outcome.failedStage = "recovery authority"
			return err
		}
		fatal, skipped := current.materialization.refusalScope()
		if current.materialization == nil && current.refusedItems() != 0 {
			outcome.failedStage = "topology prevalidation"
			return fmt.Errorf("selected Project topology contains %d refused item(s)", current.refusedItems())
		}
		if len(fatal) != 0 {
			outcome.failedStage = "topology prevalidation"
			return fmt.Errorf("selected Project topology contains %d refused item(s) that end the activation: %s",
				len(fatal), describeRefusedItems(fatal))
		}
		_ = skipped
		// The Agent disclosures describe what this pass is about to do, so they
		// are written only once the pass is going to happen. A refused plan
		// materializes nothing, and "this Agent starts a new conversation" would
		// be false about it.
		current.materialization.writeNotices(r.notices)
		if err := validateResourcePlanWrites(ctx, routed, current.writes); err != nil {
			outcome.failedStage = "tmux prevalidation"
			return err
		}
		outcome.completed = append(outcome.completed, "tmux targets prevalidated")
		for _, write := range current.writes {
			if _, err := routed.Run(ctx, "tmux", write.args...); err != nil {
				outcome.failedStage = write.itemKey()
				return err
			}
			outcome.completed = append(outcome.completed, write.itemKey())
		}
		*working = current.registry.Clone()
		if err := executeRegistryTopology(ctx, runtime, working, r.resources.mutator(), current.materialization, ledger, r.newGeneration, operationID, r.agents); err != nil {
			outcome.failedStage = "topology materialization"
			return err
		}
		return nil
	})
	outcome.registryChanged = registryChanged
	if updateErr != nil {
		runtime.rollback(ctx, ledger)
		runtime.clearCreateOperations(ctx, ledger)
		return outcome, updateErr
	}
	runtime.clearCreateOperations(ctx, ledger)
	return outcome, nil
}

// authorizeRecovery is the single production gate for topology actions. It
// evaluates the actual locked plan, so neither a stale preview nor a caller's
// label can grant authority to objects that are about to be materialized or
// skipped.
func (r topologyMaterializeRun) authorizeRecovery(plan *resourceReconcilePlan) error {
	if plan == nil || plan.materialization == nil || !r.recoveryTrigger.Valid() {
		return fmt.Errorf("topology recovery trigger %q is outside the closed authority table", r.recoveryTrigger)
	}
	type request struct {
		divergence resourcegraph.Divergence
		level      controller.RecoveryLevel
		count      int
	}
	requests := map[string]*request{}
	add := func(divergence resourcegraph.Divergence, level controller.RecoveryLevel) {
		key := string(divergence) + "\x00" + string(level)
		if requests[key] == nil {
			requests[key] = &request{divergence: divergence, level: level}
		}
		requests[key].count++
	}
	for _, item := range plan.materialization.items {
		if !item.refused && item.Action == "materialize" {
			add(item.Divergence, controller.RecoveryMaterialize)
		}
	}
	_, skipped := plan.materialization.refusalScope()
	for _, item := range skipped {
		add(item.Divergence, controller.RecoverySkipItem)
	}
	for _, request := range requests {
		verdict := controller.AuthorizeRecovery(request.divergence, r.recoveryTrigger, request.level, r.recoveryApproved, request.count)
		allowed := verdict.Decision == controller.RecoveryAllowAutomatic ||
			(r.recoveryApproved && verdict.Decision == controller.RecoveryAllowApproved)
		if !allowed {
			return fmt.Errorf("%s %s recovery refused: %s", request.divergence, request.level, verdict.Reason)
		}
		for i := range plan.items {
			item := &plan.items[i]
			if item.Divergence != request.divergence {
				continue
			}
			matchesMaterialize := request.level == controller.RecoveryMaterialize && !item.refused && item.Action == "materialize"
			matchesSkip := request.level == controller.RecoverySkipItem && item.refused
			if matchesMaterialize || matchesSkip {
				item.LossKind, item.LossCount = verdict.LossKind, verdict.LossCount
			}
		}
	}
	return nil
}

// requireMaterializeSession pins one activation's exact session name onto the
// plan.
//
// Closed-Project startup materializes into the single session the client is
// about to be moved to. If the Registry projects a different session name for
// that Project, populating it would leave the open pointing at a session the
// topology never reached, so the plan is refused before the first mutation
// rather than silently building the wrong runtime.
func requireMaterializeSession(plan *registryTopologyPlan, sessionName string) {
	sessionName = strings.TrimSpace(sessionName)
	if plan == nil || sessionName == "" || plan.sessionName == sessionName {
		return
	}
	plan.refuse(resourcegraph.DivergenceDrifted, coremetadata.KindProject, plan.project.Metadata.Name,
		fmt.Sprintf("Registry projects session %q but this activation opens session %q", plan.sessionName, sessionName))
}

func (c *resourceReconcileCommand) runMaterializeExecute(
	ctx context.Context,
	planner resourceReconcilePlanner,
	target explicitTmuxTarget,
	reportTarget resourceReconcileTarget,
	opts resourceReconcileOptions,
	stdout, stderr io.Writer,
) error {
	if c.resources == nil || c.resources.updateConvergent == nil {
		return errors.New("resource topology materialization write store is not configured")
	}
	expectedSocketPath, err := bindExplicitMaterializeSocket(ctx, c.runner, target)
	if err != nil {
		return err
	}
	run := topologyMaterializeRun{
		resources:          c.resources,
		runner:             c.runner,
		target:             target,
		expectedSocketPath: expectedSocketPath,
		newOperationID:     c.newOperationID,
		newGeneration:      c.newGeneration,
		newMaterializer:    c.newMaterializer,
		agents:             c.agents,
		notices:            stderr,
		recoveryTrigger:    controller.RecoveryExplicit,
		recoveryApproved:   true,
	}
	retry := retryResourceReconcileProject(reportTarget, planner.materializeProject)
	outcome, runErr := run.execute(ctx, planner, stderr)
	if runErr != nil {
		remaining, replanErr := c.replanAfterFailure(ctx, planner)
		report := reportForFailure(outcome.plan, remaining, reportTarget, outcome.completed, outcome.failedStage, retry, runErr, replanErr)
		if writeErr := writeResourceReconcileReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("reconcile resources failed at %s: %w", outcome.failedStage, MapMetadataError(runErr))
	}
	stage := "Registry commit"
	if !outcome.registryChanged {
		stage += " (no-op)"
	}
	completed := append(outcome.completed, stage)
	report := reportForExecute(outcome.plan, reportTarget, completed, retry)
	return writeResourceReconcileReport(stdout, opts.output, report)
}

func bindExplicitMaterializeSocket(ctx context.Context, runner tmuxCommandRunner, target explicitTmuxTarget) (string, error) {
	if runner == nil {
		return "", errors.New("resource materialization requires a tmux runner")
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			// A fresh logical route has no physical identity until new-session
			// returns and the materializer binds #{socket_path}. An explicit -S
			// declaration already names the immutable path the create must use.
			if target.flag == "-L" {
				return "", nil
			}
			if target.flag == "-S" {
				path := filepath.Clean(strings.TrimSpace(target.value))
				if filepath.IsAbs(path) && path == target.value {
					return path, nil
				}
			}
		}
		return "", fmt.Errorf("resource materialization bind exact socket: %w", err)
	}
	path := filepath.Clean(strings.TrimSpace(string(out)))
	if path == "." || !filepath.IsAbs(path) {
		return "", errors.New("resource materialization observed no absolute socket identity")
	}
	if target.flag == "-S" && path != filepath.Clean(target.value) {
		return "", errors.New("resource materialization physical socket drifted")
	}
	if err := guardRuntimeMutationServerOwnership(ctx, routed, target); err != nil {
		return "", err
	}
	return path, nil
}

// topologyOwnerGuard is one execute pass's exact parent and uid-claim proof.
//
// UID strings on the planned handles are not enough. A live Window can be
// relinked to another session and a live Pane can be join-paned into another
// Window without either uid changing, so a split could otherwise land in the
// wrong parent. Worse, a Window relinked out of the selected session is invisible
// to the selected-session plan, which would happily create a second live Window
// carrying the same Registry uid. Both are proven here against one server-wide
// inventory, before the first mutation.
type topologyOwnerGuard struct {
	windowOwner map[string]string
	paneOwner   map[string]runtimeOwner
	windowUIDs  map[string][]string
	paneUIDs    map[string][]string
}

func newTopologyOwnerGuard(ctx context.Context, runtime *materializer) (*topologyOwnerGuard, error) {
	windowsOut, err := runtime.read(ctx, "list-windows", "-a", "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		// No server is the ordinary state of an offline Project on a fresh
		// socket, and it is also the strongest possible answer to "is this uid
		// claimed anywhere": nothing is live, so the inventory is empty.
		if isMissingTmuxServer(err) {
			return emptyTopologyOwnerGuard(), nil
		}
		return nil, fmt.Errorf("inventory tmux Window owners: %w", err)
	}
	panesOut, err := runtime.read(ctx, "list-panes", "-a", "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
		if isMissingTmuxServer(err) {
			return emptyTopologyOwnerGuard(), nil
		}
		return nil, fmt.Errorf("inventory tmux Pane owners: %w", err)
	}
	guard := &topologyOwnerGuard{
		windowOwner: map[string]string{},
		paneOwner:   map[string]runtimeOwner{},
		windowUIDs:  map[string][]string{},
		paneUIDs:    map[string][]string{},
	}
	windowRows, err := strictTmuxRows(windowsOut, 3)
	if err != nil {
		return nil, fmt.Errorf("malformed tmux Window owner inventory: %w", err)
	}
	for _, row := range windowRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" {
			return nil, fmt.Errorf("malformed tmux Window owner inventory")
		}
		if existing, seen := guard.windowOwner[row[1]]; seen && existing != row[0] {
			return nil, fmt.Errorf("tmux window %s reports two owning sessions", row[1])
		}
		guard.windowOwner[row[1]] = row[0]
		if uid := strings.TrimSpace(row[2]); uid != "" && !slices.Contains(guard.windowUIDs[uid], row[1]) {
			guard.windowUIDs[uid] = append(guard.windowUIDs[uid], row[1])
		}
	}
	paneRows, err := strictTmuxRows(panesOut, 4)
	if err != nil {
		return nil, fmt.Errorf("malformed tmux Pane owner inventory: %w", err)
	}
	for _, row := range paneRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" || exactTmuxHandle(row[2], "%") == "" {
			return nil, fmt.Errorf("malformed tmux Pane owner inventory")
		}
		owner := runtimeOwner{SessionID: row[0], WindowID: row[1]}
		if existing, seen := guard.paneOwner[row[2]]; seen && existing != owner {
			return nil, fmt.Errorf("tmux pane %s reports two owning windows", row[2])
		}
		guard.paneOwner[row[2]] = owner
		if uid := strings.TrimSpace(row[3]); uid != "" && !slices.Contains(guard.paneUIDs[uid], row[2]) {
			guard.paneUIDs[uid] = append(guard.paneUIDs[uid], row[2])
		}
	}
	return guard, nil
}

func emptyTopologyOwnerGuard() *topologyOwnerGuard {
	return &topologyOwnerGuard{
		windowOwner: map[string]string{},
		paneOwner:   map[string]runtimeOwner{},
		windowUIDs:  map[string][]string{},
		paneUIDs:    map[string][]string{},
	}
}

// requireSoleUIDClaims proves every planned Window and Pane uid is live on
// exactly its planned handle, or nowhere at all when this pass intends to create
// it. It needs no session id, so it runs as a preflight before the selected
// Project session is created -- creating that session runs the public
// pre/post-create hooks, whose effects are outside the rollback guarantee, so a
// foreign claim on a desired uid must be refused before them.
func (g *topologyOwnerGuard) requireSoleUIDClaims(plan *registryTopologyPlan) error {
	for wi := range plan.windows {
		work := &plan.windows[wi]
		windowLabel := work.window.Metadata.Name
		if err := g.requireSoleWindowUID(work.window.Metadata.UID, work.liveID, windowLabel); err != nil {
			return err
		}
		if err := g.requireSolePaneUID(work.primary.Metadata.UID, primaryPaneHandle(work), windowLabel+"/"+work.primary.Metadata.Name); err != nil {
			return err
		}
		for pi := range work.panes {
			paneWork := &work.panes[pi]
			if paneWork.pane.Metadata.UID == work.primary.Metadata.UID {
				continue
			}
			if err := g.requireSolePaneUID(paneWork.pane.Metadata.UID, paneWork.liveID, windowLabel+"/"+paneWork.pane.Metadata.Name); err != nil {
				return err
			}
		}
		// A stale managed Pane row of a replayed Agent is about to be released.
		// Releasing it deletes a Registry row, so the empty handle is asserted
		// for its uid first: if that uid is live anywhere on this socket, the
		// row is not stale and the pass refuses instead of orphaning a pane.
		for ai := range work.agents {
			replay := &work.agents[ai]
			for _, paneUID := range replay.releaseUIDs {
				if err := g.requireSolePaneUID(paneUID, "", windowLabel+"/"+replay.agent.Metadata.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// requirePlannedParents proves every planned live handle's exact parent under
// the now-known session id, and that no planned live handle's uid drifted.
func (g *topologyOwnerGuard) requirePlannedParents(ctx context.Context, runtime *materializer, plan *registryTopologyPlan, sessionID string) error {
	for wi := range plan.windows {
		work := &plan.windows[wi]
		windowLabel := work.window.Metadata.Name
		if work.liveID != "" {
			if runtime.option(ctx, work.liveID, "#{"+tmuxopts.WindowUID+"}") != work.window.Metadata.UID {
				return fmt.Errorf("window %s uid changed after planning", windowLabel)
			}
			if err := g.requireWindowOf(sessionID, work.liveID, windowLabel); err != nil {
				return err
			}
		}
		for pi := range work.panes {
			paneWork := &work.panes[pi]
			if paneWork.liveID == "" {
				continue
			}
			if err := g.requirePaneOf(sessionID, work.liveID, paneWork.liveID, windowLabel+"/"+paneWork.pane.Metadata.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// primaryPaneHandle returns the planned live handle of a Window's primary Pane.
// The primary is tracked in the pane plan, so a Window whose plan predates its
// own creation reports the empty handle.
func primaryPaneHandle(work *registryTopologyWindowPlan) string {
	for pi := range work.panes {
		if work.panes[pi].pane.Metadata.UID == work.primary.Metadata.UID {
			return work.panes[pi].liveID
		}
	}
	return ""
}

// requireWindowOf proves windowID is present and owned by exactly sessionID.
func (g *topologyOwnerGuard) requireWindowOf(sessionID, windowID, label string) error {
	if owner, present := g.windowOwner[windowID]; !present || owner != sessionID {
		return fmt.Errorf("window %s (%s) is not owned by session %s: owner=%q",
			label, windowID, sessionID, owner)
	}
	return nil
}

// requirePaneOf proves paneID is present and owned by exactly sessionID/windowID.
func (g *topologyOwnerGuard) requirePaneOf(sessionID, windowID, paneID, label string) error {
	want := runtimeOwner{SessionID: sessionID, WindowID: windowID}
	if owner, present := g.paneOwner[paneID]; !present || owner != want {
		return fmt.Errorf("pane %s (%s) is not owned by session %s window %s: owner=%q/%q",
			label, paneID, sessionID, windowID, owner.SessionID, owner.WindowID)
	}
	return nil
}

// requireSoleWindowUID proves uid is live on exactly the expected handle across
// the whole socket. An empty handle asserts the uid is live nowhere, which is
// what makes creating a Window for it safe.
func (g *topologyOwnerGuard) requireSoleWindowUID(uid, handle, label string) error {
	return requireSoleUIDClaim("window", label, uid, handle, g.windowUIDs[uid])
}

// requireSolePaneUID is requireSoleWindowUID for a Pane uid.
func (g *topologyOwnerGuard) requireSolePaneUID(uid, handle, label string) error {
	return requireSoleUIDClaim("pane", label, uid, handle, g.paneUIDs[uid])
}

func requireSoleUIDClaim(kind, label, uid, handle string, claims []string) error {
	if handle == "" {
		if len(claims) != 0 {
			return fmt.Errorf("%s %s uid %s is already live on %v elsewhere on this socket", kind, label, uid, claims)
		}
		return nil
	}
	if len(claims) != 1 || claims[0] != handle {
		return fmt.Errorf("%s %s uid %s is claimed by %v, want exactly %s", kind, label, uid, claims, handle)
	}
	return nil
}

// requireCreatedPaneParent re-reads one freshly created Pane's exact parent
// tuple. A split is not attributed by a before/after owner diff the way
// new-window is, so the returned handle's parent is proven with its own exact
// observation before any uid claim or mirror is written to it.
func requireCreatedPaneParent(ctx context.Context, runtime *materializer, paneID, sessionID, windowID string) error {
	out, err := runtime.read(ctx, "display-message", "-p", "-t", paneID, "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return fmt.Errorf("inspect created tmux pane %s parent: %w", paneID, err)
	}
	rows := splitTmuxRows(out, 2)
	if len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || exactTmuxHandle(rows[0][1], "@") == "" {
		return fmt.Errorf("inspect created tmux pane %s parent: malformed row %q", paneID, strings.TrimSpace(out))
	}
	if rows[0][0] != sessionID || rows[0][1] != windowID {
		return fmt.Errorf("created tmux pane %s landed in %s/%s, want %s/%s",
			paneID, rows[0][0], rows[0][1], sessionID, windowID)
	}
	return nil
}

// adoptCreatedPane claims, ledgers, and mirrors one Pane this operation created
// after proving its parent. A Pane whose parent cannot be proven is left
// unclaimed and reported as an exact residual instead of being given this
// Project's identity in the wrong topology.
func adoptCreatedPane(
	ctx context.Context,
	runtime *materializer,
	paneID, sessionID, windowID string,
	pane coremetadata.Pane,
	ledger *runtimeLedger,
) error {
	if err := requireCreatedPaneParent(ctx, runtime, paneID, sessionID, windowID); err != nil {
		runtime.warnUnclaimedHandle(runtimePane, paneID)
		return err
	}
	if err := runtime.claimRuntimeUIDForRollback(ctx, runtimePane, paneID, pane.Metadata.UID, ledger); err != nil {
		return err
	}
	return runtime.mirrorPane(ctx, paneID, pane)
}

func executeRegistryTopology(
	ctx context.Context,
	runtime *materializer,
	registry *coremetadata.Registry,
	mutator coremetadata.Mutator,
	plan *registryTopologyPlan,
	ledger *runtimeLedger,
	newGeneration func() (string, error),
	operationID string,
	launcher topologyAgentLauncher,
) error {
	// Every Pane this pass launches gets its own generation, exactly like an
	// interactive create. Materializing a stored topology is a launch, not an
	// adoption: the process that ends up in the pane is one this call started.
	activate := func(paneUID string) (superviseSpec, error) {
		return issuePaneActivation(newGeneration, registry, mutator, paneUID, "", operationID)
	}
	if plan == nil || len(plan.items) == 0 {
		return nil
	}
	if plan.hasRefusal() {
		return fmt.Errorf("refused desired topology")
	}
	exists, err := runtime.sessions.SessionExists(ctx, plan.sessionName)
	if err != nil {
		return fmt.Errorf("revalidate selected Project session: %w", err)
	}
	if exists != plan.sessionLive {
		return fmt.Errorf("selected Project session liveness changed after planning")
	}
	if exists && runtime.option(ctx, plan.sessionName, "#{"+tmuxopts.ProjectUIDSession+"}") != plan.project.Metadata.UID {
		return fmt.Errorf("selected Project session uid changed after planning")
	}
	// Creating the selected Project session runs the public pre/post-create
	// hooks, whose side effects no rollback can undo. So a foreign live claim on
	// any desired Window or Pane uid is refused here, before that happens.
	preflight, err := newTopologyOwnerGuard(ctx, runtime)
	if err != nil {
		return err
	}
	if err := preflight.requireSoleUIDClaims(plan); err != nil {
		return err
	}
	first := &plan.windows[0]
	// The session's own first Window and Pane arrive with the atomic
	// new-session result. Adopting those exact ids, rather than re-listing the
	// session, is what keeps a concurrently created sibling from being claimed.
	created, err := runtime.ensureSessionAt(ctx, plan.project, plan.sessionName, materializePaneCWD(plan.project, first.primary), ledger)
	if err != nil {
		return err
	}
	if created.Created {
		if err := runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, created.WindowID, first.window.Metadata.UID, ledger); err != nil {
			return err
		}
		if err := runtime.mirrorWindow(ctx, created.WindowID, first.window); err != nil {
			return err
		}
		if err := runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, first.primary.Metadata.UID, ledger); err != nil {
			return err
		}
		if err := runtime.mirrorPane(ctx, created.PaneID, first.primary); err != nil {
			return err
		}
		first.liveID, first.create = created.WindowID, false
		for pi := range first.panes {
			paneWork := &first.panes[pi]
			if paneWork.pane.Metadata.UID == first.primary.Metadata.UID {
				paneWork.liveID, paneWork.create = created.PaneID, false
			}
		}
	}
	// The inventory is refreshed after the session exists so the new tuple is
	// included and any race since the preflight is caught. Both passes run
	// before the first Window or Pane mutation of this pass.
	guard, err := newTopologyOwnerGuard(ctx, runtime)
	if err != nil {
		return err
	}
	if err := guard.requireSoleUIDClaims(plan); err != nil {
		return err
	}
	if err := guard.requirePlannedParents(ctx, runtime, plan, created.SessionID); err != nil {
		return err
	}
	for wi := range plan.windows {
		work := &plan.windows[wi]
		if work.liveID == "" {
			// newWindow can report a synchronous post-mutation hook failure while
			// still returning the exact attributed @N/%N pair. Claiming and
			// ledgering that pair before surfacing the original error is what lets
			// the ownership-checked rollback remove it.
			primaryActivation, activationErr := activate(work.primary.Metadata.UID)
			if activationErr != nil {
				return activationErr
			}
			result, createErr := runtime.newWindow(ctx, created.SessionID, work.window.Metadata.Name,
				materializePaneCWD(plan.project, work.primary),
				runtime.supervisedLaunch(ctx, primaryActivation, nil))
			if result.WindowID != "" {
				if claimErr := runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, result.WindowID, work.window.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(createErr, claimErr)
				}
				if mirrorErr := runtime.mirrorWindow(ctx, result.WindowID, work.window); mirrorErr != nil {
					return errors.Join(createErr, mirrorErr)
				}
				if result.PaneID != "" {
					// Attribution already proved this Pane belongs to the exact new
					// Window under the selected Session.
					if claimErr := runtime.claimRuntimeUIDForRollback(ctx, runtimePane, result.PaneID, work.primary.Metadata.UID, ledger); claimErr != nil {
						return errors.Join(createErr, claimErr)
					}
					if mirrorErr := runtime.mirrorPane(ctx, result.PaneID, work.primary); mirrorErr != nil {
						return errors.Join(createErr, mirrorErr)
					}
					observeActivationRuntime(registry, mutator, primaryActivation, result.PaneID, runtime.warn)
				}
			}
			if createErr != nil {
				return createErr
			}
			work.liveID = result.WindowID
			for pi := range work.panes {
				paneWork := &work.panes[pi]
				if paneWork.pane.Metadata.UID == work.primary.Metadata.UID {
					paneWork.liveID, paneWork.create = result.PaneID, false
				}
			}
		}
		windowID := work.liveID
		anchorID := ""
		for pi := range work.panes {
			paneWork := &work.panes[pi]
			if paneWork.pane.Metadata.UID == work.primary.Metadata.UID && paneWork.liveID != "" {
				anchorID = paneWork.liveID
			}
		}
		if anchorID == "" {
			// The Window is live but its stored primary Pane is not. Split it
			// back in from an exact-bound sibling shell Pane of the same Window;
			// planning already refused a Window with no such anchor.
			var primaryWork *registryTopologyPanePlan
			fallbackID := ""
			for pi := range work.panes {
				paneWork := &work.panes[pi]
				if paneWork.pane.Metadata.UID == work.primary.Metadata.UID {
					primaryWork = paneWork
					continue
				}
				if fallbackID == "" && paneWork.liveID != "" {
					fallbackID = paneWork.liveID
				}
			}
			if primaryWork == nil || !primaryWork.create || fallbackID == "" {
				return fmt.Errorf("window %s has no exact primary Pane binding", work.window.Metadata.Name)
			}
			primaryActivation, activationErr := activate(primaryWork.pane.Metadata.UID)
			if activationErr != nil {
				return activationErr
			}
			paneID, splitErr := runtime.splitPane(ctx, fallbackID, defaultPlacement,
				materializePaneCWD(plan.project, primaryWork.pane),
				runtime.supervisedLaunch(ctx, primaryActivation, nil))
			if paneID != "" {
				if adoptErr := adoptCreatedPane(ctx, runtime, paneID, created.SessionID, windowID, primaryWork.pane, ledger); adoptErr != nil {
					return errors.Join(splitErr, adoptErr)
				}
				observeActivationRuntime(registry, mutator, primaryActivation, paneID, runtime.warn)
			}
			if splitErr != nil {
				return splitErr
			}
			primaryWork.create, primaryWork.liveID = false, paneID
			anchorID = paneID
			runtime.equalizeSplitLayout(ctx, fallbackID, defaultPlacement)
		}
		if runtime.option(ctx, anchorID, "#{"+tmuxopts.PaneUID+"}") != work.primary.Metadata.UID {
			return fmt.Errorf("window %s primary Pane uid changed after planning", work.window.Metadata.Name)
		}
		for pi := range work.panes {
			paneWork := &work.panes[pi]
			if !paneWork.create || paneWork.pane.Metadata.UID == work.primary.Metadata.UID {
				continue
			}
			paneActivation, activationErr := activate(paneWork.pane.Metadata.UID)
			if activationErr != nil {
				return activationErr
			}
			paneID, splitErr := runtime.splitPane(ctx, anchorID, defaultPlacement,
				materializePaneCWD(plan.project, paneWork.pane),
				runtime.supervisedLaunch(ctx, paneActivation, nil))
			if paneID != "" {
				if adoptErr := adoptCreatedPane(ctx, runtime, paneID, created.SessionID, windowID, paneWork.pane, ledger); adoptErr != nil {
					return errors.Join(splitErr, adoptErr)
				}
				observeActivationRuntime(registry, mutator, paneActivation, paneID, runtime.warn)
			}
			if splitErr != nil {
				return splitErr
			}
			paneWork.create, paneWork.liveID = false, paneID
			runtime.equalizeSplitLayout(ctx, anchorID, defaultPlacement)
		}
		// Agents come last inside their Window, on the very anchor the shell
		// half just proved. Their launch argv was fixed at plan time, so the
		// only work left here is the ordinary managed-Pane materialization.
		if err := replayTopologyWindowAgents(ctx, runtime, registry, mutator, launcher, work,
			anchorID, created.SessionID, windowID, ledger, newGeneration, operationID); err != nil {
			return err
		}
	}
	if _, err := mutator.BindProjectSession(registry, plan.project.Metadata.UID, plan.sessionName, true); err != nil {
		return MapMetadataError(err)
	}
	// Startup runs only after every created object carries its exact uid, so a
	// synchronous startup mutation cannot escape the transaction unnoticed.
	return runtime.finalizeSessionStartup(ctx, created, plan.sessionName, plan.project.Spec.Root, ledger)
}

// describeRefusedItems renders refused items for one error line. The reasons are
// the plan's own, so the message an operator sees names the exact stored item and
// why it was refused rather than a count they then have to go and look up.
func describeRefusedItems(items []resourceReconcileItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", strings.ToLower(item.Kind), item.Target, item.Reason))
	}
	return strings.Join(parts, "; ")
}
