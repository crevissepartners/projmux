package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	// projectStartupKindNew is the fresh-start row of the closed-Project startup
	// screen. It is the only row that deletes anything: `Project topology` means
	// "bring back what was saved", so a row that starts clean has to be a
	// different row with a different, observably different outcome.
	projectStartupKindNew = "new"

	// projectStartupValueNew is the picker/transport spelling of that row. It is
	// also the `switch sidebar-open --mode` token, because the sidebar open is a
	// re-exec and the operator's approved choice has to survive it.
	projectStartupValueNew = "new"

	// projectStartupNewLabel is the row name. It matches the value on purpose:
	// the row is referred to as the `new` row everywhere else.
	projectStartupNewLabel = "New"

	// projectStartupNewDescription is the row description. The destructive-action
	// contract requires the row itself to say what it discards, before the
	// operator has committed to anything.
	projectStartupNewDescription = "discard the latest snapshot and every saved Window, Pane, and Agent, then start one fresh shell Window"

	projectStartupNewConfirmTitle  = "Start new: discard saved state?"
	projectStartupNewConfirmPrompt = "Start new > "
	projectStartupNewConfirmFooter = "Enter: discard and start  |  Esc: cancel"
	projectStartupNewConfirmRow    = "Yes, discard and start new"
	projectStartupNewCancelRow     = "Cancel"
	projectStartupNewCancelHelp    = "keep the saved state; nothing is deleted"

	projectStartupNewConfirmValue = "project-startup-new:confirm"
	projectStartupNewCancelValue  = "project-startup-new:cancel"

	projectStartupNewCanceledMessage = "projmux: fresh start canceled; nothing was deleted"
)

// newProjectStartupCandidate is the fresh-start row.
func newProjectStartupCandidate() projectStartupCandidate {
	return projectStartupCandidate{
		Kind:        projectStartupKindNew,
		Label:       projectStartupNewLabel,
		Description: projectStartupNewDescription,
	}
}

// projectFreshStartPlan is the preflighted prune of one `new` start.
//
// It is a plan rather than a direct mutation for the same reason `delete` has
// one: the counts the operator approves and the records the transaction removes
// have to be the same set, and the only way to say that is to name the set once
// and re-derive it under the store lock.
type projectFreshStartPlan struct {
	// ProjectUID is empty when the exact root declares no Registry Project. That
	// is the ordinary first-open case, not a failure: there is nothing to prune.
	ProjectUID string
	// WindowUIDs are the delete targets. Panes and Agents are never targets: they
	// are removed as the canonical Window cascade's descendants.
	WindowUIDs []string
	Windows    int
	Panes      int
	Agents     int
	// AgentSessionRefs counts the Agents whose Registry record carries the
	// durable conversation pointer status.sessionRef. Deleting the Agent is what
	// destroys that pointer, so the confirmation names the number explicitly.
	AgentSessionRefs int
	// LatestSnapshot reports whether an auto-saved snapshot exists for the target
	// session, so the confirmation can say whether one is being discarded.
	LatestSnapshot bool
	// signature pins the exact cascade the operator approved.
	signature string
}

// Empty reports that the plan removes no Registry record at all.
func (p projectFreshStartPlan) Empty() bool {
	return len(p.WindowUIDs) == 0
}

// Counts renders the exact per-kind deletion counts. Nothing here is rounded or
// lumped: the destructive-action contract is that the operator sees the three
// numbers that will actually be removed.
func (p projectFreshStartPlan) Counts() string {
	return fmt.Sprintf("Window %d / Pane %d / Agent %d", p.Windows, p.Panes, p.Agents)
}

// ConfirmHeader is the always-visible line of the confirmation step. It states
// the prune, the snapshot, and -- because a destructive prompt that only lists
// losses invites the operator to assume the worst -- what survives.
func (p projectFreshStartPlan) ConfirmHeader() string {
	snapshot := "there is no latest snapshot to discard"
	if p.LatestSnapshot {
		snapshot = "discards the latest snapshot"
	}
	return fmt.Sprintf("deletes %s and %s; Named snapshots, the Project registration, its managed root, and its trust decision are kept",
		p.Counts(), snapshot)
}

// ConfirmRowHelp is the description of the row that performs the deletion. It
// repeats the counts so the numbers are attached to the action itself, and names
// the conversation pointer the Agent records take with them.
func (p projectFreshStartPlan) ConfirmRowHelp() string {
	if p.Agents == 0 {
		return fmt.Sprintf("deletes %s; no Agent record remains, so no Agent conversation pointer status.sessionRef is lost", p.Counts())
	}
	return fmt.Sprintf("deletes %s; the Agents' conversation pointer status.sessionRef (%d recorded) is deleted with them and cannot be recovered",
		p.Counts(), p.AgentSessionRefs)
}

// ResultMessage is what the operator is told once the start has happened.
//
// It states the "nothing was resumed" outcome as a result rather than leaving it
// as silence. After the prune no Agent record exists, so Phase 0's replay has
// nothing to replay -- which looks exactly like a replay that failed quietly
// unless someone says which of the two it was.
func (p projectFreshStartPlan) ResultMessage(sessionName string) string {
	snapshot := "there was no latest snapshot to discard"
	if p.LatestSnapshot {
		snapshot = "discarded the latest snapshot"
	}
	return fmt.Sprintf("projmux: started %s fresh: deleted %s and %s; no Agent record remained, so nothing was resumed",
		sessionName, p.Counts(), snapshot)
}

// switchProjectFreshStarter is the `new` row's prune seam.
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

// registryProjectFreshStarter prunes through the canonical delete cascade.
//
// It does not own a deletion routine. The target set is expanded by cascadeOf
// and buildDeletePlan, every removal goes through deleteResource -> the
// coremetadata Mutator's DeleteWindow cascade, and the plan is re-derived inside
// resourceStore.mutate and compared against the approved signature -- the exact
// discipline documented on deleteCommand.
//
// What it deliberately omits is `delete`'s live tmux half. This route runs only
// on a closed Project: openProjectTarget reaches the startup picker only when
// switchSessionExists reported false, so there is no live Window or Pane to
// kill. Preflighting a live cascade that is empty by construction would add a
// tmux round trip whose only possible answer is "nothing", and would make the
// cancel path's zero-tmux-writes claim harder to prove rather than easier.
type registryProjectFreshStarter struct {
	resources *resourceStore
}

func newRegistryProjectFreshStarter() *registryProjectFreshStarter {
	return &registryProjectFreshStarter{resources: newResourceStore()}
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
	return projectFreshStartPlanFor(registry, project.Metadata.UID), nil
}

// projectFreshStartPlanFor expands one Project's Windows into the canonical
// cascade plan and counts it per kind.
func projectFreshStartPlanFor(registry coremetadata.Registry, projectUID string) projectFreshStartPlan {
	resolution := projectFreshStartResolution(registry, projectUID)
	deletion := buildDeletePlan(registry, coremetadata.KindWindow, resolution)
	plan := projectFreshStartPlan{
		ProjectUID: projectUID,
		WindowUIDs: resolution.UIDs(),
		Windows:    len(deletion.Targets),
		signature:  deletion.signature(),
	}
	for _, target := range deletion.Targets {
		for _, descendant := range target.Descendants {
			switch descendant.Kind {
			case coremetadata.KindPane:
				plan.Panes++
			case coremetadata.KindAgent:
				plan.Agents++
				if agent, ok := registry.Agent(descendant.UID); ok && agent.Status.SessionRef != nil {
					plan.AgentSessionRefs++
				}
			}
		}
	}
	return plan
}

// projectFreshStartResolution names every Window of one Project as an explicit
// delete target, in registry order.
//
// This is the "force-prune" scope the owner fixed: not the offline ones, not the
// ones whose runtime is gone, all of them. A selective variant is a different
// row and is out of scope here.
func projectFreshStartResolution(registry coremetadata.Registry, projectUID string) selector.Resolution {
	resolution := selector.Resolution{Kind: coremetadata.KindWindow}
	for _, window := range registry.WindowsOf(projectUID) {
		resolution.Matches = append(resolution.Matches, selector.Match{
			Kind: coremetadata.KindWindow,
			UID:  window.Metadata.UID,
			Name: window.Metadata.Name,
		})
	}
	return resolution
}

// PruneProjectFreshStart remains fail-closed until the separately planned
// Project-start projection can replace the deleted graph with its intended
// startup topology. Canonical Window delete now preserves the v2 anchor by
// adding a minimum replacement, which is correct for delete but is not
// authorization to turn this legacy prune-to-zero path into Phase 15.
//
// An empty plan opens no transaction at all, which is what keeps `new` on an
// unregistered directory a pure start rather than a Registry write.
func (s *registryProjectFreshStarter) PruneProjectFreshStart(_ context.Context, root string, plan projectFreshStartPlan) error {
	if plan.Empty() {
		return nil
	}
	if s == nil || s.resources == nil {
		return errors.New("project fresh start: resource registry store is not configured")
	}
	root = strings.TrimSpace(root)
	return s.resources.mutate(coremetadata.KindWindow, plan.WindowUIDs, func(working *coremetadata.Registry, _ coremetadata.Mutator) error {
		project, ok := working.ProjectByRoot(root)
		if !ok || project.Metadata.UID != plan.ProjectUID {
			return fmt.Errorf("project fresh start: %q no longer declares Project %s; nothing was deleted", root, plan.ProjectUID)
		}
		current := projectFreshStartPlanFor(*working, plan.ProjectUID)
		if current.signature != plan.signature {
			return errors.New("project fresh start: the cascade plan changed between the confirmation and execution; nothing was deleted")
		}
		return errors.New("project fresh start: schema v2 requires Project.spec.primaryWindowRef; canonical Project-start projection is not available until Phase 15; nothing was deleted")
	})
}

// confirmProjectFreshStart is the destructive-action gate of the `new` row.
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
	options := intpickercompat.Options{
		UI:            "project-startup-new-confirm",
		Title:         settingsCatalogText(projectStartupNewConfirmTitle),
		Prompt:        settingsCatalogText(projectStartupNewConfirmPrompt),
		Header:        plan.ConfirmHeader(),
		Footer:        projmuxFooter(projectStartupNewConfirmFooter),
		Bindings:      settingsCloseBindings(),
		DisableSearch: true,
		Entries: []intpickercompat.Entry{
			{
				Label:     settingsLabel(settingsGlyphBack, settingsColorBack, projectStartupNewCancelRow, projectStartupNewCancelHelp),
				Value:     projectStartupNewCancelValue,
				SearchKey: projectStartupNewCancelRow,
			},
			{
				Label:     settingsLabel(settingsGlyphRemove, settingsColorRemove, projectStartupNewConfirmRow, plan.ConfirmRowHelp()),
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
	// The snapshot half is decided by exactly the predicate that decides whether
	// the `Latest snapshot` row is offered, so `new` never claims to discard a
	// snapshot the screen above it did not offer to restore.
	if store, err := c.projectStartupSessionStateStore(); err == nil {
		if summary, err := store.Summary(sessionName); err == nil && summary.Source != sessionstate.SourceFresh {
			plan.LatestSnapshot = true
		}
	}
	return plan, nil
}

// startProjectFresh is the `new` row's execution: preflight and prune the
// Registry topology, discard the latest snapshot, verify the prune, then start.
//
// Registry authority goes first because schema v2 can reject the legacy
// prune-to-zero transaction until Phase 15 supplies a replacement Project-start
// projection. A rejected prune must retain the latest snapshot as well as the
// Registry and tmux runtime; deleting it before that verdict would turn a
// fail-closed open into partial data loss. This is C-7 failure atomicity for
// the current rejection, not a claim that the future multi-store success path
// is atomic; Phase 15 owns that design.
// The bootstrap value travels through untouched. The `new` row prunes a Project
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
	if plan.LatestSnapshot {
		store, err := c.projectStartupSessionStateStore()
		if err != nil {
			return err
		}
		if err := store.Delete(sessionName); err != nil {
			return err
		}
	}
	if err := c.verifyProjectFreshStartPruned(target); err != nil {
		return err
	}
	if err := c.materializeAndOpenProjectTopology(ctx, sessionName, target, opened); err != nil {
		return err
	}
	c.reportProjectStartup(plan.ResultMessage(sessionName))
	return nil
}

// verifyProjectFreshStartPruned re-reads the Registry and refuses to continue
// while the Project still declares topology.
//
// Acceptance 3 -- "the Project comes up as a single fresh Window and shell Pane"
// -- is not something this route performs; it is something it *causes* by leaving
// desiredTopologyRef with no Window to find, so that materializeAndOpenProjectTopology
// falls through to the shipped ensureAndOpenProjectSession bootstrap. That is a
// consequence of state, so it is checked as state rather than assumed. A leftover
// Window here would silently restore the topology the operator just discarded.
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
