package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// The Registry-first row source of the Projects picker.
//
// The picker used to be a filesystem scan with a tmux existence check stapled
// on: every row was a directory, and a Project existed exactly as long as a
// session did. That is the wrong authority in both directions. A Project whose
// session was closed vanished from the list whose purpose is reopening it, and
// a directory that had never been a Project was indistinguishable from one that
// had.
//
// Rows now come from the Registry and split into sections that mean different
// things:
//
//   - The Home row is chrome. It is the operator's own root, offered by
//     filesystem discovery and claimed by no Project, and it leads the list
//     because it is where the surface starts from rather than a member of what
//     the surface orders.
//   - Managed rows are Registry Projects. They are present whether or not a
//     tmux server is, they keep their identity across a refresh, and the runtime
//     contributes a status, an exact handle, and a presentation tier.
//   - Unregistered rows are discovered directories that no Project claims.
//     They keep exactly the behavior they have always had, because bootstrapping
//     a new Project out of a directory is what they are for.
//
// The Runtime link is the last thing before Settings and it is not a Project. It
// is what makes the Registry-first list safe to trust: an operator's own shell,
// the Home control session, a scratch session and anything on a guest server
// are correctly absent from the managed rows, and "correctly absent" has to be
// distinguishable from "lost".
//
// Membership is the Registry's and presentation is the sidebar's. Only the
// managed section is tiered, and a tier is a view of one exact host: it can move
// a row between refreshes, which is why the cursor is anchored to a Project uid
// rather than to a position.

// switchRuntimeSentinel is the selection token of the Runtime link row.
const switchRuntimeSentinel = "__projmux_runtime__"

// switchRegistryUIDPrefix marks a managed row whose selection is a resource uid
// rather than a filesystem path.
//
// A Project whose spec.root disappeared has no path a picker could hand to the
// open flow, and handing over the stale one would ask the session opener to cd
// into a directory that is not there. Its selection is its uid, and selecting
// it opens the resource surface, which is where rebind lives.
const switchRegistryUIDPrefix = "uid:"

// switchRegistrySelectionUID returns the resource uid a selection names, if any.
func switchRegistrySelectionUID(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, switchRegistryUIDPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, switchRegistryUIDPrefix))
}

// navigationView reads the Registry and one bounded observation of the exact
// host. It writes nothing.
func (c *switchCommand) navigationView(ctx context.Context) (registryview.View, error) {
	if c.navigation == nil || c.navigation.reader == nil {
		return registryview.View{}, nil
	}
	return c.navigation.reader.view(ctx, nil)
}

// switchManagedRows renders the Registry Projects as picker candidates.
//
// Order is a presentation projection over Registry order, never a second source
// of membership. The Registry decides which Projects exist and in what order;
// the sidebar decides which of them an operator is reaching for, in three tiers
// -- pinned, then live, then everything else -- with Registry order as the
// tie-break inside each tier.
//
// The live half of that is an overlay of one exact host, so the same Registry
// does render in a different order on a second server, and that is the intended
// reading: the tier says what is open *here*. What must not move with it is the
// selection. It follows a Project uid rather than a row index or a path, so a
// tier change relocates the row and not the resource the cursor is on; see
// switchProjectRowFocusValue.
func (c *switchCommand) switchManagedRows(
	view registryview.View,
	ui string,
	mode switchRowRenderMode,
	selection pinSelection,
	attentionRanks map[string]int,
	aiBadgeKinds map[string]string,
	aiBadgeStyle string,
	homeDir string,
	repoRoot string,
) ([]intrender.SwitchCandidate, map[string]string, error) {
	projects := projectRowsOf(view)
	sortManagedProjectRows(projects, selection)
	rows := make([]intrender.SwitchCandidate, 0, len(projects))
	sessionNames := make(map[string]string, len(projects))
	for _, row := range projects {
		value, display := switchManagedRowValue(row, homeDir, repoRoot)
		sessionName := strings.TrimSpace(row.SessionName)
		if sessionName == "" && row.Root != "" && switchRowOpensByPath(row) && c.identity != nil {
			// The Registry has never projected a session name for this Project.
			// Deriving it from the root is the same derivation the opener
			// performs, so the row states the name the open would use rather
			// than leaving the column blank. A row whose root is gone is skipped
			// because there is no path to derive from.
			derived, err := c.identity.SessionIdentityForPath(row.Root)
			if err != nil {
				return nil, nil, fmt.Errorf("render switch rows: resolve session identity for Project %q: %w", row.Name, err)
			}
			sessionName = derived
		}
		if row.Root != "" {
			sessionNames[cleanOptionalPath(row.Root)] = sessionName
		}
		modeLabel := "new"
		if row.Live() {
			modeLabel = "existing"
		}
		gitBranch := ""
		var windowTabs []intrender.SwitchWindowTab
		if mode == switchRowRenderFull {
			if row.Root != "" && switchRowOpensByPath(row) {
				gitBranch = c.resolveGitBranch(row.Root)
			}
			windowTabs = switchRegistryWindowTabs(view, row, aiBadgeStyle)
		}
		rows = append(rows, intrender.SwitchCandidate{
			Path:          value,
			DisplayPath:   display,
			DisplayName:   switchManagedDisplayName(row),
			SessionName:   sessionName,
			ModeLabel:     modeLabel,
			GitBranch:     gitBranch,
			WindowTabs:    windowTabs,
			UI:            ui,
			AttentionRank: attentionRanks[sessionName],
			AIBadgeKind:   aiBadgeKinds[sessionName],
			AIBadgeStyle:  aiBadgeStyle,
			Pinned:        selection.pinnedProject(row.UID),
		})
	}
	return rows, sessionNames, nil
}

// projectRowsOf returns the Project rows of a view in view order.
func projectRowsOf(view registryview.View) []registryview.Row {
	var out []registryview.Row
	for _, row := range view.Section(registryview.SectionProjects) {
		if row.Kind == registryview.RowKindProject {
			out = append(out, row)
		}
	}
	return out
}

// switchManagedProjectTier is the sidebar presentation tier of one Registry
// Project.
//
// Pinned outranks live because pinning is a stated preference and liveness is an
// accident of the moment: a pinned Project that is offline is still the one the
// operator asked to keep at the top, and demoting it below whatever happens to
// be running would make the pin mean nothing.
type switchManagedProjectTier int

const (
	switchManagedTierPinned switchManagedProjectTier = iota
	switchManagedTierLive
	switchManagedTierOffline
)

// switchManagedProjectTierOf classifies one Project row.
//
// Pinning is keyed by the Project uid, which is what the typed pin store holds. A
// Project with no root, or one whose root has gone missing, is still pinnable and
// still pinned: the preference is about the resource, and a rebind, a rename or a
// vanished directory does not change which resource the operator asked to keep on
// top.
func switchManagedProjectTierOf(project registryview.Row, selection pinSelection) switchManagedProjectTier {
	switch {
	case selection.pinnedProject(project.UID):
		return switchManagedTierPinned
	case project.Live():
		return switchManagedTierLive
	default:
		return switchManagedTierOffline
	}
}

// sortManagedProjectRows partitions the Registry Projects into presentation
// tiers and preserves Registry order inside each one.
//
// The sort is stable and the comparator answers with nothing but the tier
// difference, so two Projects in the same tier keep the order the Registry gave
// them. That is what makes a refresh deterministic: only a tier change can move
// a row past a sibling, and a tier change is an observation the operator caused.
func sortManagedProjectRows(projects []registryview.Row, selection pinSelection) {
	slices.SortStableFunc(projects, func(a, b registryview.Row) int {
		return int(switchManagedProjectTierOf(a, selection)) - int(switchManagedProjectTierOf(b, selection))
	})
}

// switchManagedRowValue decides what a managed row's selection carries.
//
// The rule is the row's own eligibility rather than a second reading of the
// Registry: a row that offers a rebind is a row whose root cannot be opened, and
// handing the stale path to the session opener would ask it to cd into a
// directory that is not there.
func switchManagedRowValue(row registryview.Row, homeDir, repoRoot string) (value, display string) {
	switch {
	case row.Root != "" && switchRowOpensByPath(row):
		return row.Root, intrender.PrettyPath(row.Root, homeDir, repoRoot)
	case row.Root != "":
		return switchRegistryUIDPrefix + row.UID, intrender.PrettyPath(row.Root, homeDir, repoRoot) + " (missing root)"
	default:
		return switchRegistryUIDPrefix + row.UID, "(no root)"
	}
}

// switchRowOpensByPath reports whether this row's spec.root is usable.
func switchRowOpensByPath(row registryview.Row) bool {
	return !row.Allows(registryview.ActionRebind)
}

func switchManagedDisplayName(row registryview.Row) string {
	if name := strings.TrimSpace(row.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(row.Name)
}

// switchRegistryWindowTabs renders a Project card's window tabs from the
// Registry rather than from tmux.
//
// The tabs used to be `list-windows` output, which meant a Project with no live
// session showed none at all -- the card said nothing about a topology the
// Registry knows exactly. They are the Registry's Windows now, and the runtime
// decides only which of them is marked live.
func switchRegistryWindowTabs(view registryview.View, project registryview.Row, badgeStyle string) []intrender.SwitchWindowTab {
	children := view.Children(project.ID)
	tabs := make([]intrender.SwitchWindowTab, 0, len(children))
	for _, child := range children {
		if child.Kind != registryview.RowKindWindow {
			continue
		}
		name := strings.TrimSpace(child.DisplayName)
		if name == "" {
			name = strings.TrimSpace(child.Name)
		}
		if name == "" {
			continue
		}
		tabs = append(tabs, intrender.SwitchWindowTab{
			Name:         name,
			AIBadgeStyle: badgeStyle,
			Active:       child.Live(),
		})
	}
	if len(tabs) == 0 {
		return nil
	}
	return tabs
}

// switchUnregisteredPaths drops the discovered paths a Registry Project already
// claims, so a managed Project is never offered twice with two action sets.
func switchUnregisteredPaths(view registryview.View, candidatePaths []string) []string {
	roots := map[string]bool{}
	for _, row := range projectRowsOf(view) {
		if root := cleanOptionalPath(row.Root); root != "" {
			roots[root] = true
		}
	}
	out := make([]string, 0, len(candidatePaths))
	for _, path := range candidatePaths {
		if path != switchSettingsSentinel && roots[cleanOptionalPath(path)] {
			continue
		}
		out = append(out, path)
	}
	return out
}

// switchRuntimeRow renders the Runtime link as the last row of the list.
func switchRuntimeRow(view registryview.View, ui string) intrender.SwitchCandidate {
	return intrender.SwitchCandidate{
		Path:        switchRuntimeSentinel,
		DisplayPath: registryNavigationRuntimeLabel(view),
		DisplayName: "Runtime",
		UI:          ui,
	}
}

// openRegistryHierarchy opens the read-only Registry resource surface of one
// Project uid.
func (c *switchCommand) openRegistryHierarchy(ctx context.Context, ui, projectUID string, stdout io.Writer) error {
	if c.navigation == nil {
		return errors.New("switch registry navigation handler is not configured")
	}
	return c.navigation.runProject(ctx, ui, projectUID, stdout, io.Discard)
}

// registryProjectUIDForSelection resolves a picker selection to the Registry
// Project it belongs to.
//
// A selection is either a uid, which is already the answer, or a path, which is
// matched against spec.root. The path match is a lookup of an exact stored root
// and not a heuristic: an unregistered candidate simply has no Project, and the
// surface says so by declining rather than by picking the nearest one.
func (c *switchCommand) registryProjectUIDForSelection(ctx context.Context, selection string) (string, error) {
	if uid := switchRegistrySelectionUID(selection); uid != "" {
		return uid, nil
	}
	selection = cleanOptionalPath(selection)
	if selection == "" || selection == switchSettingsSentinel || selection == switchRuntimeSentinel {
		return "", nil
	}
	view, err := c.navigationView(ctx)
	if err != nil {
		return "", err
	}
	for _, row := range projectRowsOf(view) {
		if cleanOptionalPath(row.Root) == selection {
			return row.UID, nil
		}
	}
	return "", nil
}

// switchProjectRowFocusValue returns the row value the Project behind a
// pre-refresh selection renders as now, or empty when the selection was not a
// Project.
//
// The presentation tiers make a refresh able to move a Project past its
// siblings, and a Project whose spec.root has since gone missing changes what
// its own row's selection carries. Neither is a reason for the cursor to end up
// on a different resource, so the anchor is the Project uid: the old selection
// is resolved to the Project it belonged to, and that Project is resolved back
// to whatever row it renders as after the refresh. An index would follow the
// position and a path would be lost by a rebind; a uid is the only handle that
// survives both.
//
// Home, Settings, the Runtime link and an unregistered directory belong to no
// Project. They answer empty, which leaves the picker's shipped
// preserve-the-previous-value behavior exactly as it was.
func (c *switchCommand) switchProjectRowFocusValue(ctx context.Context, selection string) (string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" || selection == switchSettingsSentinel || selection == switchRuntimeSentinel {
		return "", nil
	}
	view, err := c.navigationView(ctx)
	if err != nil {
		return "", err
	}
	projects := projectRowsOf(view)
	uid := switchRegistrySelectionUID(selection)
	if uid == "" {
		// An exact stored-root lookup, the same one the hierarchy key uses. An
		// unregistered directory simply has no Project and gets no anchor rather
		// than the nearest match.
		path := cleanOptionalPath(selection)
		for _, project := range projects {
			if cleanOptionalPath(project.Root) == path {
				uid = project.UID
				break
			}
		}
	}
	if uid == "" {
		return "", nil
	}
	homeDir, err := c.resolveHomeDir()
	if err != nil {
		return "", err
	}
	repoRoot := c.switchRepoRoot(homeDir)
	for _, project := range projects {
		if project.UID != uid {
			continue
		}
		value, _ := switchManagedRowValue(project, homeDir, repoRoot)
		return value, nil
	}
	// The Project left the Registry between the render and this lookup. An
	// absent anchor is safer than a stale one: the picker clamps rather than
	// jumping to a resource the operator never selected.
	return "", nil
}

// writeRegistrySelectionPreview renders the preview of a selection that is not
// a filesystem path.
//
// It reports handled=false for an ordinary path so the caller falls through to
// the shipped filesystem preview unchanged.
func (c *switchCommand) writeRegistrySelectionPreview(ctx context.Context, stdout io.Writer, selection string) (bool, error) {
	selection = strings.TrimSpace(selection)
	uid := switchRegistrySelectionUID(selection)
	if selection != switchRuntimeSentinel && uid == "" {
		return false, nil
	}
	view, err := c.navigationView(ctx)
	if err != nil {
		return true, err
	}
	if _, err := io.WriteString(stdout, registryNavigationHeaderLine(view)+"\n"); err != nil {
		return true, err
	}
	if selection == switchRuntimeSentinel {
		_, err := io.WriteString(stdout, registryNavigationRuntimeLabel(view)+"\n")
		return true, err
	}
	rows := view.Descendants(registryview.ProjectID(uid))
	if len(rows) == 0 {
		_, err := io.WriteString(stdout, "no Registry Project carries uid "+uid+"\n")
		return true, err
	}
	for _, row := range rows {
		line := registryNavigationIndent(row) + string(row.Kind) + " " + registryNavigationName(row) +
			"  " + string(row.Status) + "  " + registryNavigationActionList(row) + "\n"
		if _, err := io.WriteString(stdout, line); err != nil {
			return true, err
		}
	}
	return true, nil
}
