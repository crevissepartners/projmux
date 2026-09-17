package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	//
	// It says `Recreate Project` rather than the historical `Open fresh` because
	// the row is the one action on the startup screen that replaces identity,
	// and "open" is the word every other Project entry point uses for the
	// actions that deliberately do not. `Continue project` opens, `open project`
	// opens, `attach project` opens; only this one mints a new Project UID and
	// releases the old graph's addresses, and the label is where an operator
	// finds that out.
	projectStartupNewLabel = "Recreate Project"

	// projectStartupNewDescription names the replacement rather than the open.
	projectStartupNewDescription = "replace this Project identity with a new Project, Window, and shell"

	// projectStartupRecreateConfirmLabel is the confirmation row that authorizes
	// the replacement.
	projectStartupRecreateConfirmLabel = "Recreate Project"

	// projectStartupRecreateCancelLabel returns to the startup screen.
	projectStartupRecreateCancelLabel = "Keep this Project"

	// projectStartupRecreateConfirmValue is the picker/transport spelling of the
	// approval. It is deliberately different from projectStartupValueNew so a
	// stale token from either picker cannot be read as the other one's answer.
	projectStartupRecreateConfirmValue = "recreate-confirm"
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
// store lock and committed atomically; nothing outside the Registry is read or
// changed by this seam.
type registryProjectFreshStarter struct {
	resources *resourceStore
	runner    tmuxRunner
	target    tmuxTransport
	shell     string
}

func newRegistryProjectFreshStarter() *registryProjectFreshStarter {
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		panic(err)
	}
	return &registryProjectFreshStarter{
		resources: newResourceStore(), runner: inttmux.ExecRunner{}, target: target,
		shell: configuredShell(os.Getenv),
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
// shell atomically before runtime materialization. A root that is not a
// registered Project has no identity to continue: the call refuses with zero
// Registry writes and points to Recreate Project. Nothing outside the Registry
// is read here.
func (s *registryProjectFreshStarter) ContinueProject(_ context.Context, root, _ string) (openedProjectBootstrap, error) {
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
	if decision.State != coremetadata.ProjectLifecycleUnregistered || decision.Available || uid != "" {
		return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", "", "",
			fmt.Errorf("unregistered Continue did not fail closed: %+v", decision))
	}
	return openedProjectBootstrap{}, wrapProjectLifecycleError(coremetadata.ProjectLifecycleContinue, "state-table", "", "",
		fmt.Errorf("continue project unavailable: %s is not a registered Project; choose Recreate Project", root))
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
// Registry authority goes first. A rejected replacement must retain the
// Registry and tmux runtime; Open fresh writes no Project state outside the
// Registry.
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
	// The saved launch default runs last, on the other side of the handoff: an
	// Agent or a picker popup belongs to the Session the operator is now looking
	// at, and applying it earlier would open it behind a client that had not
	// been moved yet. It cannot fail the open -- see applyFreshLaunchDefault.
	c.applyFreshLaunchDefault(ctx, target)
	return nil
}

// applyFreshLaunchDefault opens the saved launch default on the shell Pane of
// the fresh Project's one Window, exactly as a UI Window create opens it on the
// Pane its create committed.
//
// Nothing here can fail the open, and nothing here is rolled back. The Session,
// its Window and its shell Pane are committed by the time this runs; a refusal
// costs the operator one line and leaves a shell they can work in, which is why
// this returns nothing and startProjectFresh still answers nil.
func (c *switchCommand) applyFreshLaunchDefault(ctx context.Context, root string) {
	if c.launchDefault == nil || c.freshOriginShellPane == nil {
		return
	}
	// The launch default attaches only to a gesture a human made. The sidebar
	// continuation carries the exact client that pressed the row through
	// withSidebarOpenClientEnv, and it is the client that will see whatever the
	// default does. A detached `start project`, a scripted `open project`, and
	// any other open without that client carry no such gesture, and there a
	// provider mode would open an Agent -- or a picker popup -- on whatever
	// client tmux happens to consider current. So with no exact client this does
	// nothing at all, in every mode: no Agent, no picker, no message. The client
	// is resolved the way launchSidebarOpenContinuation resolves it, so both
	// halves of the same re-exec agree on who pressed the row.
	client := firstNonEmpty(
		c.lookupEnvValue(inttmux.SwitchTargetClientEnv),
		c.lookupEnvValue(hookTrustPopupTargetClientEnv),
	)
	if strings.TrimSpace(client) == "" {
		return
	}
	origin, err := c.freshOriginShellPane(ctx, root)
	if err != nil {
		c.displayFreshLaunchDefaultLine(ctx, client, keptOriginShellLine(err.Error()))
		return
	}
	result := c.launchDefault(origin, strings.TrimSpace(client))
	if result.problem != "" {
		c.displayFreshLaunchDefaultLine(ctx, client, result.problem)
		return
	}
	// result.picker means the popup has already come and gone on this client and
	// owns whatever it reported, so nothing is written over it. result.notice --
	// the split start notice a committed Agent produces -- is deliberately
	// dropped: the success line it rides on for a Window create was reported
	// before the handoff here, so the notice could only arrive as a second,
	// unprompted status line on a Session the operator has just been moved into.
	// The one thing worth interrupting a fresh open for is a failure.
}

// displayFreshLaunchDefaultLine shows one bounded line on the exact client that
// pressed the row.
//
// It addresses the app socket explicitly, for the reason openProjectSession
// does: the sidebar continuation is a detached `run-shell` job with no useful
// inherited $TMUX, so the plain `display-message` the startup notice sink uses
// would either be skipped or land on an unrelated server. A failed display is
// swallowed -- the Session is open and the Window is there, and a line that
// could not be shown is not a reason to call the open a failure.
func (c *switchCommand) displayFreshLaunchDefaultLine(ctx context.Context, client, line string) {
	line = strings.Join(strings.Fields(line), " ")
	client = strings.TrimSpace(client)
	if line == "" || client == "" || c.tmuxRunner == nil {
		return
	}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		return
	}
	exact := explicitTmuxRunner{runner: c.tmuxRunner, target: target}
	_, _ = exact.Run(ctx, "tmux", "display-message", "-c", client, "-d", "10000", tmuxLiteralMessage(line))
}

// freshOriginPaneLocator turns one Registry Pane uid into the exact live tmux
// Pane that mirrors it. Production passes the canonical metadata mirror; a unit
// test answers it without a tmux server.
type freshOriginPaneLocator func(ctx context.Context, paneUID string) (string, bool, error)

// freshProjectOriginShellPane resolves the exact `%N` of the one shell Pane a
// fresh open committed.
//
// The Registry is the authority for *which* Pane, not tmux Pane order: the
// launch default replaces exactly one Pane, and asking the live Window which
// Pane is first would answer with whatever tmux most recently made current. A
// fresh Project is one Window holding one shell Pane -- the shape
// projectFreshStartPlan.Empty() requires and verifyProjectFreshStartPruned
// enforces -- so any other shape is reported as a failure to apply rather than
// guessed at, because the guess would be a Pane somebody is working in.
func freshProjectOriginShellPane(
	ctx context.Context,
	snapshot func() (coremetadata.Registry, error),
	locate freshOriginPaneLocator,
	root string,
) (string, error) {
	if snapshot == nil {
		return "", errors.New("projmux could not read the Registry for the saved launch default")
	}
	registry, err := snapshot()
	if err != nil {
		return "", fmt.Errorf("projmux could not read the Registry for the saved launch default: %v", err)
	}
	project, ok := registry.ProjectByRoot(root)
	if !ok {
		return "", fmt.Errorf("projmux found no Project at %q for the saved launch default", root)
	}
	windows := registry.WindowsOf(project.Metadata.UID)
	if len(windows) != 1 {
		return "", fmt.Errorf("the fresh Project declares %d Windows, not the one a fresh open commits", len(windows))
	}
	window := windows[0]
	panes := registry.PanesOf(window.Metadata.UID)
	if len(panes) != 1 || len(registry.AgentsOf(window.Metadata.UID)) != 0 {
		return "", fmt.Errorf("the fresh Window declares %d shell Panes and %d Agents, not the single shell Pane a fresh open commits",
			len(panes), len(registry.AgentsOf(window.Metadata.UID)))
	}
	shell, ok := registry.WindowDefaultShell(window.Metadata.UID)
	if !ok {
		// A Window committed from its anchor carries the same Pane without a
		// separate defaultShellPaneRef, which is the fallback the topology
		// engine's own bootstrap selection makes. An anchor that is not this
		// Window's own shell is not a Pane this may replace.
		anchor, anchored := registry.WindowAnchor(window.Metadata.UID)
		if !anchored || anchor.Spec.Role != coremetadata.PaneRoleShell ||
			anchor.Metadata.OwnerUID() != window.Metadata.UID {
			return "", errors.New("the fresh Window declares no Window-owned shell Pane to open the saved launch default on")
		}
		shell = anchor
	}
	if shell.Metadata.UID != panes[0].Metadata.UID {
		return "", errors.New("the fresh Window's shell Pane is not the Pane it declares")
	}
	// The Registry names the Pane but does not carry its live handle: the first
	// Window's Pane arrives with the atomic new-session result and is mirrored
	// with its uid, while the activation generation that records a `%N` is
	// written only for the Panes created after it. So the uid is resolved
	// through the canonical metadata mirror, which reads the mirrored uid off
	// every live Pane and refuses a duplicate claim instead of guessing.
	if locate == nil {
		return "", errors.New("projmux has no route to the live Pane mirroring the fresh shell Pane")
	}
	target, found, err := locate(ctx, shell.Metadata.UID)
	if err != nil {
		return "", fmt.Errorf("projmux could not find the live Pane mirroring the fresh shell Pane %s: %v",
			shell.Metadata.UID, err)
	}
	origin := exactTmuxHandle(strings.TrimSpace(target), "%")
	if !found || origin == "" {
		return "", fmt.Errorf("no live Pane carries an exact %%N for the fresh shell Pane %s", shell.Metadata.UID)
	}
	return origin, nil
}

// liveShellPaneTarget is the production locator: the canonical metadata mirror,
// routed over the app's own socket the way every other exact read in this flow
// is. The sidebar continuation inherits no useful $TMUX, so the socket is named
// rather than resolved from the environment.
func (c *switchCommand) liveShellPaneTarget(ctx context.Context, paneUID string) (string, bool, error) {
	if c.tmuxRunner == nil {
		return "", false, errors.New("exact tmux runner is not configured")
	}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		return "", false, err
	}
	return intmetadata.NewMirror(explicitTmuxRunner{runner: c.tmuxRunner, target: target}).FindPaneTargetForUID(ctx, paneUID)
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
