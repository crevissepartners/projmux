package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
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

	// projectStartupNewDescription is the row description. The destructive-action
	// contract requires the row itself to say what it discards, before the
	// operator has committed to anything.
	projectStartupNewDescription = "keep the canonical Project shell and remove every other saved Window, Pane, and Agent"

	projectStartupNewConfirmTitle  = "Open fresh: prune saved topology?"
	projectStartupNewConfirmPrompt = "Open fresh > "
	projectStartupNewConfirmFooter = "Enter: discard and start  |  Esc: cancel"
	projectStartupNewConfirmRow    = "Yes, prune and open fresh"
	projectStartupNewCancelRow     = "Cancel"
	projectStartupNewCancelHelp    = "keep the saved state; nothing is deleted"

	projectStartupNewConfirmValue = "project-startup-new:confirm"
	projectStartupNewCancelValue  = "project-startup-new:cancel"

	projectStartupNewCanceledMessage = "projmux: Open fresh canceled; nothing was changed"
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

// projectFreshStartPlan is the preflighted prune for Open fresh.
//
// It is a plan rather than a direct mutation for the same reason `delete` has
// one: the counts the operator approves and the records the transaction removes
// have to be the same set, and the only way to say that is to name the set once
// and re-derive it under the store lock.
type projectFreshStartPlan struct {
	// ProjectUID is empty when the exact root declares no Registry Project. That
	// is the ordinary first-open case, not a failure: there is nothing to prune.
	ProjectUID string
	Windows    int
	Panes      int
	Agents     int
	// AgentSessionRefs counts the Agents whose Registry record carries the
	// durable conversation pointer status.sessionRef. Deleting the Agent is what
	// destroys that pointer, so the confirmation names the number explicitly.
	AgentSessionRefs int
	// signature pins the exact cascade the operator approved.
	signature string
}

// Empty reports that the plan removes no Registry record at all.
func (p projectFreshStartPlan) Empty() bool {
	return p.Windows == 0 && p.Panes == 0 && p.Agents == 0
}

// Counts renders the exact per-kind deletion counts. Nothing here is rounded or
// lumped: the destructive-action contract is that the operator sees the three
// numbers that will actually be removed.
func (p projectFreshStartPlan) Counts() string {
	return fmt.Sprintf("Window %d / Pane %d / Agent %d", p.Windows, p.Panes, p.Agents)
}

// ConfirmHeader is the always-visible line of the confirmation step. It states
// both the exact prune and the identities and snapshot storage that survive.
func (p projectFreshStartPlan) ConfirmHeader() string {
	return p.ConfirmHeaderLocale(i18n.FallbackLocale)
}

func (p projectFreshStartPlan) ConfirmHeaderLocale(locale i18n.Locale) string {
	format := localizeUIText(locale, "deletes %s; the canonical Project Window and shell Pane, snapshots, Project registration, managed root, and trust decision are kept")
	return fmt.Sprintf(format, p.Counts())
}

// ConfirmRowHelp is the description of the row that performs the deletion. It
// repeats the counts so the numbers are attached to the action itself, and names
// the conversation pointer the Agent records take with them.
func (p projectFreshStartPlan) ConfirmRowHelp() string {
	return p.ConfirmRowHelpLocale(i18n.FallbackLocale)
}

func (p projectFreshStartPlan) ConfirmRowHelpLocale(locale i18n.Locale) string {
	if p.Agents == 0 {
		format := localizeUIText(locale, "deletes %s; no Agent record remains, so no Agent conversation pointer status.sessionRef is lost")
		return fmt.Sprintf(format, p.Counts())
	}
	format := localizeUIText(locale, "deletes %s; the Agents' conversation pointer status.sessionRef (%d recorded) is deleted with them and cannot be recovered")
	return fmt.Sprintf(format,
		p.Counts(), p.AgentSessionRefs)
}

// ResultMessage is emitted after materialization and before the final client
// handoff, so switch-client remains the last observable startup action.
func (p projectFreshStartPlan) ResultMessage(sessionName string) string {
	return p.ResultMessageLocale(i18n.FallbackLocale, sessionName)
}

func (p projectFreshStartPlan) ResultMessageLocale(locale i18n.Locale, sessionName string) string {
	format := localizeUIText(locale, "projmux: opened %s fresh: deleted %s; the canonical Project shell identity was preserved")
	return fmt.Sprintf(format,
		sessionName, p.Counts())
}

// switchProjectFreshStarter is the Open fresh projection seam.
//
// Planning and pruning are separate calls because they happen at different
// moments and, on the sidebar route, in different processes: the confirmation
// runs in the picker popup, and the start runs in the re-exec that popup
// launches. Handing the second half a plan it re-derives and compares is what
// keeps the approved counts and the deleted records the same set.
type switchProjectFreshStarter interface {
	PlanProjectFreshStart(root string) (projectFreshStartPlan, error)
	PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) error
}

// registryProjectFreshStarter projects one closed Project to its canonical
// schema-v2 Window/shell anchor. The desired Registry is re-derived under the
// store lock and committed atomically; snapshot storage is never consulted or
// changed by this seam.
type registryProjectFreshStarter struct {
	resources *resourceStore
	runner    tmuxRunner
	target    explicitTmuxTarget
}

func newRegistryProjectFreshStarter() *registryProjectFreshStarter {
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		panic(err)
	}
	return &registryProjectFreshStarter{resources: newResourceStore(), runner: inttmux.ExecRunner{}, target: target}
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
	anchorPane := anchorWindow.Spec.PrimaryPaneRef
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

// PruneProjectFreshStart commits the canonical fresh projection. An empty plan
// opens no transaction, which guarantees a second Open fresh is a Registry
// zero-diff and keeps first-use bootstrap free of an unnecessary Registry write.
func (s *registryProjectFreshStarter) PruneProjectFreshStart(ctx context.Context, root string, plan projectFreshStartPlan) error {
	if plan.Empty() {
		return nil
	}
	if s == nil || s.resources == nil {
		return errors.New("project fresh start: resource registry store is not configured")
	}
	root = strings.TrimSpace(root)
	if err := s.requireClosedProject(ctx, root, plan.ProjectUID); err != nil {
		return err
	}
	_, err := s.resources.converge(func(working *coremetadata.Registry, _ coremetadata.Mutator) error {
		project, ok := working.ProjectByRoot(root)
		if !ok || project.Metadata.UID != plan.ProjectUID {
			return fmt.Errorf("project fresh start: %q no longer declares Project %s; nothing was deleted", root, plan.ProjectUID)
		}
		current := projectFreshStartPlanFor(*working, plan.ProjectUID)
		if current.signature != plan.signature {
			return errors.New("project fresh start: the cascade plan changed between the confirmation and execution; nothing was deleted")
		}
		fresh, err := coremetadata.PlanOpenFresh(*working, plan.ProjectUID, time.Now())
		if err != nil {
			return err
		}
		*working = fresh.Desired
		return nil
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
		return errors.New("project fresh start: target Project has no declared session projection; nothing was deleted")
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

// confirmProjectFreshStart is the destructive-action gate for Open fresh.
//
// It is a second native picker rather than a yes/no line for the same reason
// confirmNotifySidebarClearAll is: the operator is already inside a popup picker,
// and a confirmation that renders somewhere else is a confirmation they can miss.
// Approval requires BOTH the Enter key and the exact confirm value, so a stray
// key or a picker that closes without a selection is a cancel.
//
// It runs at pick time, in the picker's own process, on purpose. The sidebar open
// finishes in a detached `run-shell -b` re-exec that has no controlling terminal,
// so a native picker there would have nowhere to draw.
func (c *switchCommand) confirmProjectFreshStart(plan projectFreshStartPlan) (bool, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	options := intpickercompat.Options{
		UI:            "project-startup-new-confirm",
		Locale:        locale,
		Title:         localizeUIText(locale, projectStartupNewConfirmTitle),
		Prompt:        localizeUIText(locale, projectStartupNewConfirmPrompt),
		Header:        plan.ConfirmHeaderLocale(locale),
		Footer:        localizeUIText(locale, projectStartupNewConfirmFooter),
		Bindings:      settingsCloseBindings(),
		DisableSearch: true,
		Entries: []intpickercompat.Entry{
			{
				Label:     settingsLabel(settingsGlyphBack, settingsColorBack, localizeUIText(locale, projectStartupNewCancelRow), localizeUIText(locale, projectStartupNewCancelHelp)),
				Value:     projectStartupNewCancelValue,
				SearchKey: projectStartupNewCancelRow,
			},
			{
				Label:     settingsLabel(settingsGlyphRemove, settingsColorRemove, localizeUIText(locale, projectStartupNewConfirmRow), plan.ConfirmRowHelpLocale(locale)),
				Value:     projectStartupNewConfirmValue,
				SearchKey: projectStartupNewConfirmRow,
			},
		},
	}
	result, err := runNativePickerOption(c.homeDir, c.lookupEnv, c.nativePicker, options)
	if err != nil {
		return false, fmt.Errorf("run project fresh start confirmation: %w", err)
	}
	if result.Key != "enter" || strings.TrimSpace(result.Value) != projectStartupNewConfirmValue {
		return false, nil
	}
	return true, nil
}

// planProjectFreshStart resolves the confirmation's counts.
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
	return plan, nil
}

// startProjectFresh executes Open fresh: commit the canonical projection,
// verify it, materialize through the ordinary path, report, then switch client.
//
// Registry authority goes first. A rejected prune must retain snapshots as
// well as the Registry and tmux runtime; Open fresh never writes snapshot
// storage.
// The bootstrap value travels through untouched. Open fresh prunes a Project
// the Registry already declares topology for, so in practice it is never the open
// that minted the Project -- but the mirror decision stays in the one place that
// owns it rather than being re-decided here.
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
