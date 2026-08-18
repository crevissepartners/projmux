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

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// registryTopologyPlan is the immutable Project-scoped desired-topology plan
// shared by dry-run and execute. It contains only persistent Project, Window,
// and Window-owned shell Pane work. Agent-owned panes and stored commands have
// no representation here, which makes launching either impossible by design.
type registryTopologyPlan struct {
	project     coremetadata.Project
	sessionName string
	sessionLive bool
	windows     []registryTopologyWindowPlan
	items       []resourceReconcileItem
}

type registryTopologyWindowPlan struct {
	window  coremetadata.Window
	liveID  string
	create  bool
	primary coremetadata.Pane
	panes   []registryTopologyPanePlan
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
		plan.refuse(coremetadata.KindProject, project.Metadata.Name, "status.session.name and the configured persistent session name are both empty")
		return plan, nil
	}
	if reason := validateMaterializeDirectory(project.Spec.Root, "Project root"); reason != "" {
		plan.refuse(coremetadata.KindProject, project.Metadata.Name, reason)
		return plan, nil
	}
	if condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot); ok && condition.Status == coremetadata.ConditionTrue {
		plan.refuse(coremetadata.KindProject, project.Metadata.Name, "Project carries a MissingRoot condition")
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
			plan.refuse(coremetadata.KindProject, project.Metadata.Name, "multiple live sessions claim the selected Project")
			return plan, nil
		}
		if session.uid != "" && session.uid != project.Metadata.UID {
			plan.refuse(coremetadata.KindProject, project.Metadata.Name, "the expected session name carries a foreign Project uid")
			return plan, nil
		}
		if session.uid == "" {
			plan.refuse(coremetadata.KindProject, project.Metadata.Name, "live session lacks the selected Project's exact uid binding")
			return plan, nil
		}
		if claimsUID && session.name != plan.sessionName {
			plan.refuse(coremetadata.KindProject, project.Metadata.Name, "the Project uid is live under a different session name")
			return plan, nil
		}
		if session.root != "" && filepath.Clean(session.root) != filepath.Clean(project.Spec.Root) {
			plan.refuse(coremetadata.KindProject, project.Metadata.Name, "the live session carries a foreign Project root")
			return plan, nil
		}
		target = &session
	}
	if target == nil {
		plan.addItem(0, coremetadata.KindProject, project.Metadata.Name, project.Metadata.UID, "materialize")
		if exactTarget.flag == "-S" {
			plan.refuse(coremetadata.KindProject, project.Metadata.Name,
				"offline Project session creation via --socket-path cannot preserve exact name-only PROJMUX_SOCKET hook re-entry")
		}
	} else {
		plan.sessionLive = true
	}

	windows := registry.WindowsOf(project.Metadata.UID)
	if len(windows) == 0 {
		plan.refuse(coremetadata.KindProject, project.Metadata.Name, "selected Project has no Registry Window topology")
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
			plan.refuse(coremetadata.KindWindow, live.id, "uid-less live Window cannot be heuristically adopted")
			continue
		}
		windowClaims[live.uid]++
		if _, ok := knownWindows[live.uid]; !ok {
			plan.refuse(coremetadata.KindWindow, live.id, "live Window uid is not owned by the selected Project")
		}
	}
	for uid, count := range windowClaims {
		if count > 1 {
			plan.refuse(coremetadata.KindWindow, uid, "multiple live Windows claim one Registry uid")
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
			plan.refuse(coremetadata.KindWindow, window.Metadata.Name, "spec.primaryPaneRef must resolve to a Window-owned shell Pane")
			continue
		}
		work.primary = *primary
		eligible := registry.PanesOf(window.Metadata.UID)
		for _, pane := range eligible {
			if pane.Spec.Role != coremetadata.PaneRoleShell {
				continue
			}
			if reason := validateMaterializeDirectory(materializePaneCWD(project, pane), "Pane cwd"); reason != "" {
				plan.refuse(coremetadata.KindPane, pane.Metadata.Name, reason)
			}
		}
		if work.liveID == "" {
			work.create = true
			plan.addItem(wi+1, coremetadata.KindWindow, window.Metadata.Name, window.Metadata.UID, "materialize")
			for pi, pane := range eligible {
				if pane.Spec.Role != coremetadata.PaneRoleShell {
					continue
				}
				work.panes = append(work.panes, registryTopologyPanePlan{pane: pane, create: true})
				plan.addItem((wi+1)*1000+pi, coremetadata.KindPane, window.Metadata.Name+"/"+pane.Metadata.Name, pane.Metadata.UID, "materialize")
			}
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
				plan.refuse(coremetadata.KindPane, live.id, "live Pane uid has the wrong owner or is absent from the selected Window graph")
			}
		}
		for uid, count := range claims {
			if count > 1 {
				plan.refuse(coremetadata.KindPane, uid, "multiple live Panes claim one Registry uid")
			}
		}
		for pi, pane := range eligible {
			if pane.Spec.Role != coremetadata.PaneRoleShell {
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
			plan.refuse(coremetadata.KindPane, work.liveID, "pre-existing uid-less live Pane cannot be heuristically adopted")
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
			plan.refuse(coremetadata.KindWindow, window.Metadata.Name, "missing primary Pane has no exact-bound Window-owned shell anchor")
		}
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
	})
}

func (p *registryTopologyPlan) refuse(kind coremetadata.Kind, target, reason string) {
	p.items = append(p.items, resourceReconcileItem{
		Key:   "tmux:materialize:refuse:" + strings.ToLower(string(kind)) + ":" + target,
		Drift: resourceDriftForeign, Surface: "tmux", Action: "refuse", Kind: string(kind), Target: target,
		Outcome: "refused", Reason: reason, refused: true,
	})
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

func (c *resourceReconcileCommand) runMaterializeExecute(
	ctx context.Context,
	planner resourceReconcilePlanner,
	target explicitTmuxTarget,
	reportTarget resourceReconcileTarget,
	opts resourceReconcileOptions,
	stdout, stderr io.Writer,
) error {
	if c.resources.updateConvergent == nil {
		return errors.New("resource topology materialization write store is not configured")
	}
	newID := c.newOperationID
	if newID == nil {
		newID = newCreateOperationID
	}
	operationID, err := newID()
	if err != nil {
		return err
	}
	ledger := newRuntimeLedger(operationID)
	routed := explicitTmuxRunner{runner: c.runner, target: target}
	newRuntime := c.newMaterializer
	if newRuntime == nil {
		newRuntime = func(runner tmuxCommandRunner, warn io.Writer) *materializer {
			opts := []inttmux.ClientOption{}
			if target.flag == "-L" {
				opts = append(opts, inttmux.WithSocketName(target.value))
			}
			if hooks := defaultLifecycleHookRunner(); hooks != nil {
				opts = append(opts, inttmux.WithLifecycleHookRunner(hooks))
			}
			return &materializer{runner: runner, mirror: intmetadata.NewMirror(runner), sessions: inttmux.NewClient(runner, opts...), warn: warn}
		}
	}
	runtime := newRuntime(routed, stderr)
	var plan resourceReconcilePlan
	var completed []string
	failedStage := ""
	_, registryChanged, updateErr := c.resources.updateConvergent(func(working *coremetadata.Registry) error {
		current, buildErr := planner.build(ctx, working.Clone())
		if buildErr != nil {
			failedStage = "locked plan"
			return buildErr
		}
		plan = current
		completed = append(completed, "plan rechecked under Registry lock")
		if plan.refusedItems() != 0 {
			failedStage = "topology prevalidation"
			return fmt.Errorf("selected Project topology contains %d refused item(s)", plan.refusedItems())
		}
		if err := validateResourcePlanWrites(ctx, routed, plan.writes); err != nil {
			failedStage = "tmux prevalidation"
			return err
		}
		completed = append(completed, "tmux targets prevalidated")
		for _, write := range plan.writes {
			if _, err := routed.Run(ctx, "tmux", write.args...); err != nil {
				failedStage = write.itemKey()
				return err
			}
			completed = append(completed, write.itemKey())
		}
		*working = plan.registry.Clone()
		if err := executeRegistryTopology(ctx, runtime, working, c.resources.mutator(), plan.materialization, ledger); err != nil {
			failedStage = "topology materialization"
			return err
		}
		return nil
	})
	if updateErr != nil {
		runtime.rollback(ctx, ledger)
		runtime.clearCreateOperations(ctx, ledger)
		remaining, replanErr := c.replanAfterFailure(ctx, planner)
		report := reportForFailure(plan, remaining, reportTarget, completed, failedStage, retryResourceReconcileProject(reportTarget, planner.materializeProject), updateErr, replanErr)
		if writeErr := writeResourceReconcileReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("reconcile resources failed at %s: %w", failedStage, MapMetadataError(updateErr))
	}
	runtime.clearCreateOperations(ctx, ledger)
	stage := "Registry commit"
	if !registryChanged {
		stage += " (no-op)"
	}
	completed = append(completed, stage)
	report := reportForExecute(plan, reportTarget, completed, retryResourceReconcileProject(reportTarget, planner.materializeProject))
	return writeResourceReconcileReport(stdout, opts.output, report)
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
		return nil, fmt.Errorf("inventory tmux Window owners: %w", err)
	}
	panesOut, err := runtime.read(ctx, "list-panes", "-a", "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
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
	return runtime.mirror.MirrorPane(ctx, paneID, pane)
}

func executeRegistryTopology(
	ctx context.Context,
	runtime *materializer,
	registry *coremetadata.Registry,
	mutator coremetadata.Mutator,
	plan *registryTopologyPlan,
	ledger *runtimeLedger,
) error {
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
		if err := runtime.mirror.MirrorWindow(ctx, created.WindowID, first.window); err != nil {
			return err
		}
		if err := runtime.claimRuntimeUIDForRollback(ctx, runtimePane, created.PaneID, first.primary.Metadata.UID, ledger); err != nil {
			return err
		}
		if err := runtime.mirror.MirrorPane(ctx, created.PaneID, first.primary); err != nil {
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
	// One inventory proves every planned live handle's exact parent before the
	// first mutation of this pass. Handles this pass creates prove their own
	// parent at creation time instead.
	guard, err := newTopologyOwnerGuard(ctx, runtime)
	if err != nil {
		return err
	}
	for wi := range plan.windows {
		work := &plan.windows[wi]
		windowLabel := work.window.Metadata.Name
		if err := guard.requireSoleWindowUID(work.window.Metadata.UID, work.liveID, windowLabel); err != nil {
			return err
		}
		if work.liveID != "" {
			if runtime.option(ctx, work.liveID, "#{"+tmuxopts.WindowUID+"}") != work.window.Metadata.UID {
				return fmt.Errorf("window %s uid changed after planning", windowLabel)
			}
			if err := guard.requireWindowOf(created.SessionID, work.liveID, windowLabel); err != nil {
				return err
			}
		}
		for pi := range work.panes {
			paneWork := &work.panes[pi]
			paneLabel := windowLabel + "/" + paneWork.pane.Metadata.Name
			if err := guard.requireSolePaneUID(paneWork.pane.Metadata.UID, paneWork.liveID, paneLabel); err != nil {
				return err
			}
			if paneWork.liveID == "" {
				continue
			}
			if err := guard.requirePaneOf(created.SessionID, work.liveID, paneWork.liveID, paneLabel); err != nil {
				return err
			}
		}
	}
	for wi := range plan.windows {
		work := &plan.windows[wi]
		if work.liveID == "" {
			// newWindow can report a synchronous post-mutation hook failure while
			// still returning the exact attributed @N/%N pair. Claiming and
			// ledgering that pair before surfacing the original error is what lets
			// the ownership-checked rollback remove it.
			result, createErr := runtime.newWindow(ctx, created.SessionID, work.window.Metadata.Name, materializePaneCWD(plan.project, work.primary), nil)
			if result.WindowID != "" {
				if claimErr := runtime.claimRuntimeUIDForRollback(ctx, runtimeWindow, result.WindowID, work.window.Metadata.UID, ledger); claimErr != nil {
					return errors.Join(createErr, claimErr)
				}
				if mirrorErr := runtime.mirror.MirrorWindow(ctx, result.WindowID, work.window); mirrorErr != nil {
					return errors.Join(createErr, mirrorErr)
				}
				if result.PaneID != "" {
					// Attribution already proved this Pane belongs to the exact new
					// Window under the selected Session.
					if claimErr := runtime.claimRuntimeUIDForRollback(ctx, runtimePane, result.PaneID, work.primary.Metadata.UID, ledger); claimErr != nil {
						return errors.Join(createErr, claimErr)
					}
					if mirrorErr := runtime.mirror.MirrorPane(ctx, result.PaneID, work.primary); mirrorErr != nil {
						return errors.Join(createErr, mirrorErr)
					}
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
			paneID, splitErr := runtime.splitPane(ctx, fallbackID, defaultPlacement, materializePaneCWD(plan.project, primaryWork.pane), nil)
			if paneID != "" {
				if adoptErr := adoptCreatedPane(ctx, runtime, paneID, created.SessionID, windowID, primaryWork.pane, ledger); adoptErr != nil {
					return errors.Join(splitErr, adoptErr)
				}
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
			paneID, splitErr := runtime.splitPane(ctx, anchorID, defaultPlacement, materializePaneCWD(plan.project, paneWork.pane), nil)
			if paneID != "" {
				if adoptErr := adoptCreatedPane(ctx, runtime, paneID, created.SessionID, windowID, paneWork.pane, ledger); adoptErr != nil {
					return errors.Join(splitErr, adoptErr)
				}
			}
			if splitErr != nil {
				return splitErr
			}
			paneWork.create, paneWork.liveID = false, paneID
			runtime.equalizeSplitLayout(ctx, anchorID, defaultPlacement)
		}
	}
	if _, err := mutator.BindProjectSession(registry, plan.project.Metadata.UID, plan.sessionName, true); err != nil {
		return MapMetadataError(err)
	}
	// Startup runs only after every created object carries its exact uid, so a
	// synchronous startup mutation cannot escape the transaction unnoticed.
	return runtime.finalizeSessionStartup(ctx, created, plan.sessionName, plan.project.Spec.Root, ledger)
}
