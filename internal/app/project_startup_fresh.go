package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const (
	// projectStartupKindNew is the fresh-start row of the closed-Project startup
	// screen. It is the only row that deletes anything; Continue project only
	// materializes current desired state.
	projectStartupKindNew = "fresh"

	// projectStartupValueNew is the picker/transport spelling of that row. It is
	// also the `switch sidebar-open --mode` token, because the sidebar open is a
	// re-exec and the operator's approved choice has to survive it.
	projectStartupValueNew = "fresh"

	// projectStartupNewLabel is the exact user-facing row name.
	projectStartupNewLabel = "Open fresh"

	// projectStartupNewDescription presents Fresh as an ordinary one-step open.
	projectStartupNewDescription = "reuse the canonical Project Window with one shell"
)

// newProjectStartupCandidate is the fresh-start row.
func newProjectStartupCandidate(locales ...i18n.Locale) projectStartupCandidate {
	locale := settingsLocale()
	if len(locales) > 0 {
		locale = locales[0]
	}
	return projectStartupCandidate{
		Kind:        projectStartupKindNew,
		Label:       localizeUIText(locale, projectStartupNewLabel),
		Description: localizeUIText(locale, projectStartupNewDescription),
	}
}

// projectFreshStartPlan is the preflighted same-root replacement for Open fresh.
//
// It is a plan rather than a direct mutation because picker selection and the
// detached continuation run in different processes. The mutation re-derives
// the target under the store lock and refuses a changed Project identity.
type projectFreshStartPlan struct {
	// ProjectUID is empty when the exact root declares no Registry Project. That
	// is the ordinary first-open case, not a failure: there is nothing to prune.
	ProjectUID string
	Windows    int
	Panes      int
	Agents     int
	// AgentSessionRefs remains diagnostic plan evidence; it is never rendered as
	// a danger count or confirmation in the neutral Fresh flow.
	AgentSessionRefs int
	SessionName      string
	// signature summarizes the exact preflighted graph for tests and diagnostics.
	signature string
}

// Empty reports that the plan removes no Registry record at all.
func (p projectFreshStartPlan) Empty() bool {
	return p.Windows == 0 && p.Panes == 0 && p.Agents == 0
}

// Counts renders exact per-kind plan diagnostics. It is not user-facing Fresh
// picker or confirmation text.
func (p projectFreshStartPlan) Counts() string {
	return fmt.Sprintf("Window %d / Pane %d / Agent %d", p.Windows, p.Panes, p.Agents)
}

// ResultMessage is emitted after materialization and before the final client
// handoff, so switch-client remains the last observable startup action.
func (p projectFreshStartPlan) ResultMessage(sessionName string) string {
	return p.ResultMessageLocale(i18n.FallbackLocale, sessionName)
}

func (p projectFreshStartPlan) ResultMessageLocale(locale i18n.Locale, sessionName string) string {
	format := localizeUIText(locale, "projmux: opened %s fresh with its canonical Project Window and shell")
	return fmt.Sprintf(format, sessionName)
}

// switchProjectFreshStarter is the Open fresh projection seam.
//
// Planning and replacement are separate calls because the sidebar picker and
// its detached continuation run in different processes.
type switchProjectFreshStarter interface {
	PlanProjectFreshStart(root string) (projectFreshStartPlan, error)
	PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) error
}

// registryProjectFreshStarter projects one closed Project to its canonical
// schema-v2 Window/shell anchor. The desired Registry is re-derived under the
// store lock and committed atomically; snapshot storage is never consulted or
// changed by this seam.
type registryProjectFreshStarter struct {
	resources    *resourceStore
	runner       tmuxRunner
	target       explicitTmuxTarget
	shell        string
	loadSnapshot func(string) (sessionstate.Snapshot, error)
}

func newRegistryProjectFreshStarter() *registryProjectFreshStarter {
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		panic(err)
	}
	return &registryProjectFreshStarter{
		resources: newResourceStore(), runner: inttmux.ExecRunner{}, target: target,
		shell: configuredShell(os.Getenv),
		loadSnapshot: func(session string) (sessionstate.Snapshot, error) {
			store, err := sessionstate.NewDefaultStoreFromEnv()
			if err != nil {
				return sessionstate.Snapshot{}, err
			}
			return store.LoadReadOnly(session)
		},
	}
}

func (s *registryProjectFreshStarter) ProjectRegistered(root string) (bool, error) {
	if s == nil || s.resources == nil || s.resources.snapshot == nil {
		return false, errors.New("project startup: read-only Registry is not configured")
	}
	registry, err := s.resources.snapshot()
	if err != nil {
		return false, MapMetadataError(err)
	}
	_, ok := registry.ProjectByRoot(cleanOptionalPath(root))
	return ok, nil
}

// ContinueProject refuses to invent desired topology. An existing Project is
// reused; a deleted Project can only be recreated from its exact usable
// snapshot, projected under freshly minted resource identities in one commit.
func (s *registryProjectFreshStarter) ContinueProject(_ context.Context, root, sessionName string) (openedProjectBootstrap, error) {
	if s == nil || s.resources == nil {
		return openedProjectBootstrap{}, errors.New("continue project: resource registry store is not configured")
	}
	root = cleanOptionalPath(root)
	registry, err := s.resources.snapshot()
	if err != nil {
		return openedProjectBootstrap{}, MapMetadataError(err)
	}
	if project, ok := registry.ProjectByRoot(root); ok {
		return openedProjectBootstrap{project: project.Clone()}, nil
	}
	if s.loadSnapshot == nil {
		return openedProjectBootstrap{}, errors.New("continue project unavailable: no read-only snapshot source is configured; choose Open fresh")
	}
	snap, err := s.loadSnapshot(sessionName)
	if err != nil {
		return openedProjectBootstrap{}, fmt.Errorf("continue project unavailable: no usable snapshot for %q; choose Open fresh: %w", sessionName, err)
	}
	if candidates.MatchKey(snap.DefaultCWD) != candidates.MatchKey(root) {
		return openedProjectBootstrap{}, fmt.Errorf("continue project unavailable: snapshot root %q does not match %q; choose Open fresh", snap.DefaultCWD, root)
	}
	stripSnapshotResourceMetadata(&snap)
	var opened coremetadata.Project
	_, err = s.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if _, exists := working.ProjectByRoot(root); exists {
			return errors.New("continue project: the root was registered after snapshot preflight; retry")
		}
		registered, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{
			Root: root, SessionName: sessionName, DefaultShell: s.shell,
		})
		if err != nil {
			return err
		}
		newUID := mutator.NewUID
		if newUID == nil {
			newUID = coremetadata.NewUID
		}
		projection, err := coremetadata.PlanSnapshotProjection(*working, registered.Project.Metadata.UID, snap, time.Now().UTC(), newUID)
		if err != nil {
			return err
		}
		*working = projection.Desired
		stored, _ := working.Project(registered.Project.Metadata.UID)
		opened = stored.Clone()
		return nil
	})
	if err != nil {
		return openedProjectBootstrap{}, MapMetadataError(err)
	}
	return openedProjectBootstrap{project: opened, bootstrapped: true, materializeTopology: true}, nil
}

func stripSnapshotResourceMetadata(snap *sessionstate.Snapshot) {
	if snap == nil {
		return
	}
	snap.Metadata = nil
	for wi := range snap.Windows {
		snap.Windows[wi].Metadata = nil
		for pi := range snap.Windows[wi].Panes {
			snap.Windows[wi].Panes[pi].Metadata = nil
		}
	}
}

// PlanProjectFreshStart resolves the exact prune for one Project root.
//
// The read is the zero-write snapshot read, like desiredTopologyRef: showing the
// startup screen for a directory that was never registered must not create
// <state>/projmux/metadata/.
func (s *registryProjectFreshStarter) PlanProjectFreshStart(root string) (projectFreshStartPlan, error) {
	root = strings.TrimSpace(root)
	if s == nil || s.resources == nil || root == "" {
		return projectFreshStartPlan{}, nil
	}
	read := s.resources.snapshot
	if read == nil {
		read = s.resources.load
	}
	if read == nil {
		return projectFreshStartPlan{}, nil
	}
	registry, err := read()
	if err != nil {
		return projectFreshStartPlan{}, MapMetadataError(err)
	}
	project, ok := registry.ProjectByRoot(root)
	if !ok {
		return projectFreshStartPlan{}, nil
	}
	if _, err := coremetadata.PlanOpenFresh(registry, project.Metadata.UID, time.Now()); err != nil {
		return projectFreshStartPlan{}, MapMetadataError(err)
	}
	return projectFreshStartPlanFor(registry, project.Metadata.UID), nil
}

// projectFreshStartPlanFor expands one Project's Windows into the canonical
// cascade plan and counts it per kind.
func projectFreshStartPlanFor(registry coremetadata.Registry, projectUID string) projectFreshStartPlan {
	project, ok := registry.Project(projectUID)
	if !ok {
		return projectFreshStartPlan{}
	}
	anchorWindow, ok := registry.Window(project.Spec.PrimaryWindowRef)
	if !ok {
		return projectFreshStartPlan{ProjectUID: projectUID, signature: "invalid-anchor"}
	}
	anchorPane := ""
	if shell, ok := registry.WindowDefaultShell(anchorWindow.Metadata.UID); ok {
		anchorPane = shell.Metadata.UID
	} else if anchor, ok := registry.WindowAnchor(anchorWindow.Metadata.UID); ok &&
		anchor.Spec.Role == coremetadata.PaneRoleShell && anchor.Metadata.OwnerUID() == anchorWindow.Metadata.UID {
		anchorPane = anchor.Metadata.UID
	} else {
		for _, pane := range registry.PanesOf(anchorWindow.Metadata.UID) {
			if pane.Spec.Role == coremetadata.PaneRoleShell {
				anchorPane = pane.Metadata.UID
				break
			}
		}
	}
	plan := projectFreshStartPlan{
		ProjectUID: projectUID,
		signature:  "keep:" + anchorWindow.Metadata.UID + "," + anchorPane + ";",
	}
	for _, window := range registry.WindowsOf(projectUID) {
		if window.Metadata.UID != anchorWindow.Metadata.UID {
			plan.Windows++
		}
		for _, pane := range registry.PanesOf(window.Metadata.UID) {
			if pane.Metadata.UID != anchorPane {
				plan.Panes++
				plan.signature += "pane:" + pane.Metadata.UID + ";"
			}
		}
		for _, agent := range registry.AgentsOf(window.Metadata.UID) {
			plan.Agents++
			plan.signature += "agent:" + agent.Metadata.UID + ";"
			if agent.Status.SessionRef != nil {
				plan.AgentSessionRefs++
			}
			for _, pane := range registry.PanesOf(agent.Metadata.UID) {
				plan.Panes++
				plan.signature += "pane:" + pane.Metadata.UID + ";"
			}
		}
	}
	return plan
}

// PruneProjectFreshStart atomically projects any exact same-root graph onto its
// canonical Project Window and one minimum direct shell.
func (s *registryProjectFreshStarter) PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) error {
	if s == nil || s.resources == nil {
		return errors.New("project fresh start: resource registry store is not configured")
	}
	root = strings.TrimSpace(root)
	if plan.ProjectUID != "" {
		if err := s.requireClosedProject(ctx, root, plan.ProjectUID); err != nil {
			return err
		}
	}
	_, err := s.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current, ok := working.ProjectByRoot(root); ok {
			if current.Metadata.UID != plan.ProjectUID {
				return fmt.Errorf("project fresh start: %q now declares a different Project; retry", root)
			}
			uidSource := mutator.NewUID
			if uidSource == nil {
				uidSource = coremetadata.NewUID
			}
			now := time.Now
			if mutator.Now != nil {
				now = mutator.Now
			}
			projection, err := coremetadata.PlanOpenFresh(*working, current.Metadata.UID, now().UTC(), uidSource)
			if err != nil {
				return err
			}
			*working = projection.Desired
			return nil
		} else if plan.ProjectUID != "" {
			return fmt.Errorf("project fresh start: %q no longer declares Project %s", root, plan.ProjectUID)
		}
		_, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{
			Root: root, SessionName: plan.SessionName, DefaultShell: s.shell,
		})
		return err
	})
	return err
}

// requireClosedProject re-observes the exact app socket immediately before the
// destructive Registry transaction. A same-UID or same-root session under a
// different name is live Project identity too; the declared name alone is not
// sufficient proof that Open fresh is safe.
func (s *registryProjectFreshStarter) requireClosedProject(ctx context.Context, root, projectUID string) error {
	if s.runner == nil {
		return errors.New("project fresh start: exact tmux runner is not configured; nothing was deleted")
	}
	registry, err := s.resources.snapshot()
	if err != nil {
		return MapMetadataError(err)
	}
	project, ok := registry.ProjectByRoot(root)
	if !ok || project.Metadata.UID != projectUID {
		return fmt.Errorf("project fresh start: %q no longer declares Project %s; nothing was deleted", root, projectUID)
	}
	sessionName := ""
	if project.Status.Session != nil {
		sessionName = strings.TrimSpace(project.Status.Session.Name)
	}
	if sessionName == "" {
		return nil
	}
	target := s.target
	if target.flag == "" || target.value == "" {
		target, err = tmuxSocketNameTarget(defaultAppSocket)
		if err != nil {
			return fmt.Errorf("project fresh start: exact tmux target: %w", err)
		}
	}
	exactRunner := explicitTmuxRunner{runner: s.runner, target: target}
	if _, found, err := (&materializer{runner: exactRunner}).preflightSessionOwnership(ctx, *project, sessionName); err != nil {
		return fmt.Errorf("project fresh start: target Project must be exactly closed before Open fresh; nothing was deleted: %w", err)
	} else if found {
		return fmt.Errorf("project fresh start: target Project session %q is live; close it before Open fresh; nothing was deleted", sessionName)
	}
	return nil
}

// planProjectFreshStart resolves the exact same-root replacement scope.
//
// A missing seam answers with an empty plan rather than failing: the row is a
// start action first, and a Registry that cannot be read is a reason to prune
// nothing, not a reason to refuse to open the Project.
func (c *switchCommand) planProjectFreshStart(sessionName, target string) (projectFreshStartPlan, error) {
	var plan projectFreshStartPlan
	if c.projectFreshStart != nil {
		var err error
		plan, err = c.projectFreshStart.PlanProjectFreshStart(target)
		if err != nil {
			return projectFreshStartPlan{}, err
		}
	}
	plan.SessionName = sessionName
	return plan, nil
}

// startProjectFresh executes Open fresh: commit the canonical projection,
// verify it, materialize through the ordinary path, report, then switch client.
//
// Registry authority goes first. A rejected replacement must retain snapshots as
// well as the Registry and tmux runtime; Open fresh never writes snapshot
// storage.
// The mirror decision stays in the one place that owns Project registration.
func (c *switchCommand) startProjectFresh(ctx context.Context, sessionName, target string, opened openedProjectBootstrap) error {
	plan, err := c.planProjectFreshStart(sessionName, target)
	if err != nil {
		return err
	}
	if c.projectFreshStart != nil {
		if err := c.projectFreshStart.PruneProjectFreshStart(ctx, target, plan); err != nil {
			return err
		}
	}
	registered, err := c.registerOpenedProjectRoot(ctx, target)
	if err != nil {
		return err
	}
	if registered.project.Metadata.UID != "" {
		if plan.ProjectUID == "" || registered.project.Metadata.UID != plan.ProjectUID {
			registered.bootstrapped = true
		}
		opened = registered
	}
	// Fresh always owns one exact canonical Registry graph. Force the topology
	// engine so a reused or newly allocated minimum shell is live before client
	// handoff.
	opened.materializeTopology = true
	if err := c.verifyProjectFreshStartPruned(target); err != nil {
		return err
	}
	if err := c.materializeProjectTopology(ctx, sessionName, target, opened); err != nil {
		return err
	}
	c.reportProjectStartup(plan.ResultMessageLocale(appLocale(c.homeDir, c.lookupEnv), sessionName))
	return c.openProjectSession(ctx, sessionName)
}

// verifyProjectFreshStartPruned re-reads the Registry and refuses to continue
// while any non-canonical target descendant remains after the atomic projection.
func (c *switchCommand) verifyProjectFreshStartPruned(target string) error {
	if c.projectFreshStart == nil {
		return nil
	}
	plan, err := c.projectFreshStart.PlanProjectFreshStart(target)
	if err != nil {
		return err
	}
	if !plan.Empty() {
		return fmt.Errorf("project fresh start: %q still declares %s after the prune; the Project was not started fresh",
			target, plan.Counts())
	}
	return nil
}

// reportProjectStartup routes one operator-facing startup line to the shared
// report surface. See projectStartupNoticeSink for why that surface is a
// stderr/display-message tee.
func (c *switchCommand) reportProjectStartup(message string) {
	if c.startupNotices == nil {
		return
	}
	c.startupNotices.Report(message)
}

// projectStartupReporter is the operator-facing report seam of the startup
// flow. It exists so a test can observe exactly what the operator is told
// without a tmux server.
type projectStartupReporter interface {
	Report(message string)
}
