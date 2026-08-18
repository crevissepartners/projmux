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
// Rows now come from the Registry, in Registry order, and split into two
// sections that mean different things:
//
//   - Managed rows are Registry Projects. They are present whether or not a
//     tmux server is, they keep their identity and their position across a
//     refresh, and the runtime contributes a status and an exact handle.
//   - Unregistered rows are discovered directories that no Project claims.
//     They keep exactly the behavior they have always had, because bootstrapping
//     a new Project out of a directory is what they are for.
//
// The Runtime link is the third thing on the list and it is not a Project. It
// is what makes the Registry-first list safe to trust: an operator's own shell,
// the Home control session, a scratch session and anything on a guest server
// are correctly absent from the managed rows, and "correctly absent" has to be
// distinguishable from "lost".

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
// Order is the Registry's own slice order, which is insertion order, with
// pinned Projects lifted to the front. Nothing observed contributes to it. That
// is the whole ordering contract: pinning is a stored preference and is the
// same on every machine, while liveness and attention are properties of one
// exact host, and letting either of them move a row would mean the same
// Registry rendered in a different order on a second server -- and a selection
// that jumped when a session opened somewhere.
func (c *switchCommand) switchManagedRows(
	view registryview.View,
	ui string,
	mode switchRowRenderMode,
	pinnedSet map[string]bool,
	attentionRanks map[string]int,
	aiBadgeKinds map[string]string,
	aiBadgeStyle string,
	homeDir string,
	repoRoot string,
) ([]intrender.SwitchCandidate, map[string]string, error) {
	projects := projectRowsOf(view)
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
			Pinned:        row.Root != "" && pinnedSet[cleanOptionalPath(row.Root)],
		})
	}
	sortManagedRows(rows)
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

// sortManagedRows lifts pinned Projects to the front and preserves Registry
// order everywhere else.
func sortManagedRows(rows []intrender.SwitchCandidate) {
	slices.SortStableFunc(rows, func(a, b intrender.SwitchCandidate) int {
		switch {
		case a.Pinned && !b.Pinned:
			return -1
		case b.Pinned && !a.Pinned:
			return 1
		default:
			return 0
		}
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
