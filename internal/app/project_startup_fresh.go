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
	projectStartupNewDescription = "replace this Project identity with a new Project, Window, and shell"
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
	// NewProjectUID is populated after the atomic replacement commits. It is
	// empty during picker preflight because UIDs are allocated under the lock.
	NewProjectUID string
	State         coremetadata.ProjectLifecycleState
	Windows       int
	Panes         int
	Agents        int
	// AgentSessionRefs remains diagnostic plan evidence; it is never rendered as
	// a danger count or confirmation in the neutral Fresh flow.
	AgentSessionRefs int
	SessionName      string
	// signature summarizes the exact preflighted graph for tests and diagnostics.
	signature string
}

// Empty reports that the current graph is already the minimum canonical
// topology. Fresh still replaces its identity; this predicate only verifies
// the post-commit topology shape.
func (p projectFreshStartPlan) Empty() bool {
	return p.ProjectUID != "" && p.Windows == 1 && p.Panes == 1 && p.Agents == 0
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
	oldUID := p.ProjectUID
	if strings.TrimSpace(oldUID) == "" {
		oldUID = absentProjectLifecycleUID
	}
	newUID := p.NewProjectUID
	if strings.TrimSpace(newUID) == "" {
		newUID = absentProjectLifecycleUID
	}
	format := localizeUIText(locale, "projmux: opened %s fresh; old Project UID %s -> new Project UID %s; stage=materialized")
	return fmt.Sprintf(format, sessionName, oldUID, newUID)
}

// switchProjectFreshStarter is the Open fresh projection seam.
//
// Planning and replacement are separate calls because the sidebar picker and
// its detached continuation run in different processes.
type switchProjectFreshStarter interface {
	PlanProjectFreshStart(root string) (projectFreshStartPlan, error)
	PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) (projectFreshStartCommit, error)
}

// projectFreshStartCommit reports the identity allocated while building the
// atomic desired Registry. It is returned even when the store commit fails so
// the failure can name both the retained old preimage and the attempted new
// Project UID.
type projectFreshStartCommit struct {
	NewProjectUID string
}

// registryProjectFreshStarter projects one closed Project to its canonical
// schema-v2 Window/shell anchor. The desired Registry is re-derived under the
// store lock and committed atomically; snapshot storage is never consulted or
// changed by this seam.
type registryProjectFreshStarter struct {
	resources    *resourceStore
	runner       tmuxRunner
	target       tmuxTransport
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

// ContinueProject preserves an existing Project identity. Retained topology is
// reused exactly; a zero-Window Project receives one new canonical Window and
// shell atomically before runtime materialization. A deleted Project can only be
// recreated from its exact usable snapshot, projected under freshly minted
// resource identities in one commit.
func (s *registryProjectFreshStarter) ContinueProject(_ context.Context, root, sessionName string) (openedProjectBootstrap, error) {
	if s == nil || s.resources == nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "registry-read", "", "",
			errors.New("continue project: resource registry store is not configured"))
	}
	root = cleanOptionalPath(root)
	registry, err := s.resources.snapshot()
	if err != nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "registry-read", "", "", MapMetadataError(err))
	}
	decision, uid := projectLifecycleDecisionFor(registry, root, coremetadata.ProjectLifecycleContinue, coremetadata.ProjectLifecyclePreconditions{})
	if project, ok := registry.ProjectByRoot(root); ok {
		state := decision.State
		var decisionErr error
		switch state {
		case coremetadata.ProjectLifecycleRetainedWindows:
			decisionErr = requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationContinue,
				coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsPreserved,
				coremetadata.ProjectStartupWriteMaterializeRegistry)
		case coremetadata.ProjectLifecycleZeroWindows:
			decisionErr = requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationContinue,
				coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsCreated,
				coremetadata.ProjectStartupWriteCreateCanonicalWindow, coremetadata.ProjectStartupWriteCreateCanonicalShell)
		default:
			decisionErr = fmt.Errorf("continue classified registered Project as %q", state)
		}
		if decisionErr != nil || uid != project.Metadata.UID {
			if decisionErr == nil {
				decisionErr = errors.New("continue state-table Project UID disagrees with Registry")
			}
			return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", project.Metadata.UID, project.Metadata.UID,
				decisionErr)
		}
		if state == coremetadata.ProjectLifecycleZeroWindows {
			var continued coremetadata.Project
			_, err := s.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
				current, currentUID := projectLifecycleDecisionFor(*working, root, coremetadata.ProjectLifecycleContinue, coremetadata.ProjectLifecyclePreconditions{})
				if currentUID != project.Metadata.UID {
					return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "canonical-allocation", project.Metadata.UID, project.Metadata.UID,
						errors.New("project topology changed after Continue preflight; retry"))
				}
				if err := requireProjectLifecyclePlan(current, coremetadata.ProjectLifecycleOperationContinue,
					coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsCreated,
					coremetadata.ProjectStartupWriteCreateCanonicalWindow, coremetadata.ProjectStartupWriteCreateCanonicalShell); err != nil {
					return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "canonical-allocation", project.Metadata.UID, project.Metadata.UID, err)
				}
				window, _, err := mutator.AddWindow(working, currentUID, coremetadata.BootstrapWindow{}, s.shell, "")
				if err != nil {
					return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "canonical-allocation", project.Metadata.UID, project.Metadata.UID, err)
				}
				stored, _ := working.Project(currentUID)
				stored.Spec.PrimaryWindowRef = window.Metadata.UID
				continued = stored.Clone()
				return nil
			})
			if err != nil {
				return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "registry-commit",
					project.Metadata.UID, project.Metadata.UID, MapMetadataError(err))
			}
			return openedProjectBootstrap{project: continued, bootstrapped: true}, nil
		}
		return openedProjectBootstrap{project: project.Clone()}, nil
	}
	if decision.State != coremetadata.ProjectLifecycleDeleted || decision.Available || decision.Reason != "no-usable-snapshot" {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", "", "",
			fmt.Errorf("deleted Continue did not fail closed before snapshot evidence: %+v", decision))
	}
	if s.loadSnapshot == nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-preflight", "", "",
			errors.New("continue project unavailable: no read-only snapshot source is configured; choose Open fresh"))
	}
	snap, err := s.loadSnapshot(sessionName)
	if err != nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-preflight", "", "",
			fmt.Errorf("continue project unavailable: no usable snapshot for %q; choose Open fresh: %w", sessionName, err))
	}
	if candidates.MatchKey(snap.DefaultCWD) != candidates.MatchKey(root) {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-preflight", "", "",
			fmt.Errorf("continue project unavailable: snapshot root %q does not match %q; choose Open fresh", snap.DefaultCWD, root))
	}
	decision, uid = projectLifecycleDecisionFor(registry, root, coremetadata.ProjectLifecycleContinue,
		coremetadata.ProjectLifecyclePreconditions{UsableSnapshot: true})
	if uid != "" {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", uid, uid,
			errors.New("deleted Continue unexpectedly resolved a registered Project UID"))
	}
	if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationContinue,
		coremetadata.ProjectUIDCreated, coremetadata.ProjectDescendantUIDsCreated,
		coremetadata.ProjectStartupWriteCreateProject, coremetadata.ProjectStartupWriteRestoreSnapshotGraph); err != nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", "", "", err)
	}
	stripSnapshotResourceMetadata(&snap)
	var opened coremetadata.Project
	_, err = s.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		current, currentUID := projectLifecycleDecisionFor(*working, root, coremetadata.ProjectLifecycleContinue,
			coremetadata.ProjectLifecyclePreconditions{UsableSnapshot: true})
		if currentUID != "" {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-commit", currentUID, currentUID,
				errors.New("continue project: the root was registered after snapshot preflight; retry"))
		}
		if err := requireProjectLifecyclePlan(current, coremetadata.ProjectLifecycleOperationContinue,
			coremetadata.ProjectUIDCreated, coremetadata.ProjectDescendantUIDsCreated,
			coremetadata.ProjectStartupWriteCreateProject, coremetadata.ProjectStartupWriteRestoreSnapshotGraph); err != nil {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-commit", "", "", err)
		}
		registered, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{
			Root: root, SessionName: sessionName, DefaultShell: s.shell,
		})
		if err != nil {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-commit", "", "", err)
		}
		opened = registered.Project.Clone()
		newUID := mutator.NewUID
		if newUID == nil {
			newUID = coremetadata.NewUID
		}
		projection, err := coremetadata.PlanSnapshotProjection(*working, registered.Project.Metadata.UID, snap, time.Now().UTC(), newUID)
		if err != nil {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "snapshot-projection", "", registered.Project.Metadata.UID, err)
		}
		*working = projection.Desired
		stored, _ := working.Project(registered.Project.Metadata.UID)
		opened = stored.Clone()
		return nil
	})
	if err != nil {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "registry-commit", "", opened.Metadata.UID, MapMetadataError(err))
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
		decision, _ := projectLifecycleDecisionFor(registry, root, coremetadata.ProjectLifecycleFresh, coremetadata.ProjectLifecyclePreconditions{})
		if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationFresh,
			coremetadata.ProjectUIDCreated, coremetadata.ProjectDescendantUIDsCreated,
			coremetadata.ProjectStartupWriteCreateProject, coremetadata.ProjectStartupWriteCreateCanonicalWindow,
			coremetadata.ProjectStartupWriteCreateCanonicalShell); err != nil {
			return projectFreshStartPlan{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "state-table", "", "", err)
		}
		return projectFreshStartPlan{State: decision.State}, nil
	}
	decision, _ := projectLifecycleDecisionFor(registry, root, coremetadata.ProjectLifecycleFresh, coremetadata.ProjectLifecyclePreconditions{})
	state := decision.State
	descendants := coremetadata.ProjectDescendantUIDsReplaced
	if state == coremetadata.ProjectLifecycleZeroWindows {
		descendants = coremetadata.ProjectDescendantUIDsCreated
	}
	if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationFresh,
		coremetadata.ProjectUIDReplaced, descendants,
		coremetadata.ProjectStartupWriteDeleteProjectGraph, coremetadata.ProjectStartupWriteCreateProject,
		coremetadata.ProjectStartupWriteCreateCanonicalWindow, coremetadata.ProjectStartupWriteCreateCanonicalShell); err != nil {
		return projectFreshStartPlan{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "state-table", project.Metadata.UID, "", err)
	}
	plan := projectFreshStartPlanFor(registry, project.Metadata.UID)
	plan.State = state
	return plan, nil
}

// projectFreshStartPlanFor expands one Project's Windows into the canonical
// cascade plan and counts it per kind.
func projectFreshStartPlanFor(registry coremetadata.Registry, projectUID string) projectFreshStartPlan {
	_, ok := registry.Project(projectUID)
	if !ok {
		return projectFreshStartPlan{}
	}
	plan := projectFreshStartPlan{
		ProjectUID: projectUID,
		signature:  "project:" + projectUID + ";",
	}
	for _, window := range registry.WindowsOf(projectUID) {
		plan.Windows++
		plan.signature += "window:" + window.Metadata.UID + ";"
		for _, pane := range registry.PanesOf(window.Metadata.UID) {
			plan.Panes++
			plan.signature += "pane:" + pane.Metadata.UID + ";"
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

// PruneProjectFreshStart atomically replaces any exact same-root Project graph
// with a new Project UID and a new canonical Window/shell graph.
func (s *registryProjectFreshStarter) PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) (result projectFreshStartCommit, err error) {
	if s == nil || s.resources == nil {
		return result, wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "registry-commit", plan.ProjectUID, "",
			errors.New("project fresh start: resource registry store is not configured"))
	}
	root = strings.TrimSpace(root)
	if plan.ProjectUID != "" {
		if err := s.requireClosedProject(ctx, root, plan.ProjectUID); err != nil {
			return result, wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "closed-precondition", plan.ProjectUID, "", err)
		}
	}
	_, err = s.resources.converge(func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current, ok := working.ProjectByRoot(root); ok {
			if current.Metadata.UID != plan.ProjectUID {
				return fmt.Errorf("project fresh start: %q now declares a different Project; retry", root)
			}
			if currentPlan := projectFreshStartPlanFor(*working, current.Metadata.UID); currentPlan.signature != plan.signature {
				return fmt.Errorf("project fresh start: graph drifted after preflight; old_uid=%s; retry", plan.ProjectUID)
			}
			decision, _ := projectLifecycleDecisionFor(*working, root, coremetadata.ProjectLifecycleFresh, coremetadata.ProjectLifecyclePreconditions{})
			descendants := coremetadata.ProjectDescendantUIDsReplaced
			if decision.State == coremetadata.ProjectLifecycleZeroWindows {
				descendants = coremetadata.ProjectDescendantUIDsCreated
			}
			if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationFresh,
				coremetadata.ProjectUIDReplaced, descendants,
				coremetadata.ProjectStartupWriteDeleteProjectGraph, coremetadata.ProjectStartupWriteCreateProject,
				coremetadata.ProjectStartupWriteCreateCanonicalWindow, coremetadata.ProjectStartupWriteCreateCanonicalShell); err != nil {
				return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "state-table", current.Metadata.UID, "", err)
			}
			replacement, err := coremetadata.PlanProjectFreshReplacement(*working, current.Metadata.UID,
				coremetadata.RegisterProjectOptions{SessionName: plan.SessionName, DefaultShell: s.shell}, mutator)
			result.NewProjectUID = replacement.NewProjectUID
			if err != nil {
				return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "registry-replacement", current.Metadata.UID, replacement.NewProjectUID, err)
			}
			*working = replacement.Desired
			return nil
		} else if plan.ProjectUID != "" {
			return fmt.Errorf("project fresh start: %q no longer declares Project %s", root, plan.ProjectUID)
		}
		decision, _ := projectLifecycleDecisionFor(*working, root, coremetadata.ProjectLifecycleFresh, coremetadata.ProjectLifecyclePreconditions{})
		if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationFresh,
			coremetadata.ProjectUIDCreated, coremetadata.ProjectDescendantUIDsCreated,
			coremetadata.ProjectStartupWriteCreateProject, coremetadata.ProjectStartupWriteCreateCanonicalWindow,
			coremetadata.ProjectStartupWriteCreateCanonicalShell); err != nil {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "state-table", "", "", err)
		}
		registered, err := mutator.RegisterProject(working, coremetadata.RegisterProjectOptions{
			Root: root, SessionName: plan.SessionName, DefaultShell: s.shell,
		})
		result.NewProjectUID = registered.Project.Metadata.UID
		return err
	})
	if err != nil {
		return result, wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "registry-commit", plan.ProjectUID, result.NewProjectUID, err)
	}
	return result, nil
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
	if target.Flag() == "" || target.Value == "" {
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
func (c *switchCommand) startProjectFresh(ctx context.Context, sessionName, target string, opened openedProjectBootstrap, anchor string) error {
	anchor = strings.TrimSpace(anchor)
	plan, err := c.planProjectFreshStart(sessionName, target)
	if err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "preflight", plan.ProjectUID, "", err)
	}
	if c.projectFreshStart != nil {
		commit, err := c.projectFreshStart.PruneProjectFreshStart(ctx, target, plan)
		plan.NewProjectUID = commit.NewProjectUID
		if err != nil {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "registry-replacement", plan.ProjectUID, plan.NewProjectUID, err)
		}
	}
	registered, err := c.registerOpenedProjectRoot(ctx, target)
	if err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "replacement-readback", plan.ProjectUID, plan.NewProjectUID, err)
	}
	if registered.project.Metadata.UID != "" {
		plan.NewProjectUID = registered.project.Metadata.UID
		if plan.ProjectUID != "" && registered.project.Metadata.UID == plan.ProjectUID {
			return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "replacement-readback", plan.ProjectUID, registered.project.Metadata.UID,
				errors.New("fresh reused the old Project UID"))
		}
		registered.bootstrapped = true
		opened = registered
	}
	// Fresh always owns one exact canonical Registry graph. Force the topology
	// engine so a reused or newly allocated minimum shell is live before client
	// handoff.
	opened.materializeTopology = true
	if err := c.verifyProjectFreshStartPruned(target); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "replacement-verification", plan.ProjectUID, plan.NewProjectUID, err)
	}
	if err := c.materializeProjectTopology(ctx, projectTopologyMaterializeRequest{
		Root: target, SessionName: sessionName, Anchor: anchor,
	}, opened); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "topology-materialization", plan.ProjectUID, plan.NewProjectUID, err)
	}
	c.reportProjectStartup(plan.ResultMessageLocale(appLocale(c.homeDir, c.lookupEnv), sessionName))
	if err := c.openProjectSession(ctx, sessionName); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleFresh, "client-handoff", plan.ProjectUID, plan.NewProjectUID, err)
	}
	return nil
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
	// Test and alternate startup seams may not expose a Registry UID. Production
	// replacements do, and validate their canonical shape below.
	if plan.ProjectUID == "" {
		return nil
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
