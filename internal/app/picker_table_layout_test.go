package app

import (
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The uids below are exact-length receipts: schema v4 makes every automatic
// `metadata.name` the resource's own full uid, so a fixture that shortened them
// would measure a table no operator ever sees.
const (
	layoutProjectUID = "proj-nzuydtl35c3zf4h6gpreznzbmu"
	layoutWindowUID  = "win-jkscnvb5glleebh5fqra65ccua"
	layoutPaneUID    = "pane-afcbuym6ghlpfo73nsjrhueqki"
	layoutAgentUID   = "agent-3btqoq35sm4xcrh3adg63sj52m"
)

// automaticNavigationRows is the realistic worst case schema v4 introduced: a
// whole hierarchy whose every resource carries its automatic name, observed
// offline.
//
// Offline is the widest state on purpose, and it is the state v4 changed. A
// live row renders its invocation-scoped `Context.Value` in NAME, which is a
// pane title or command and was never a uid; it is only the offline fallback to
// `row.Name` that now yields the exact uid. The Pane carries a role suffix,
// which makes its NAME the widest cell this view can produce.
func automaticNavigationRows() []registryview.Row {
	return []registryview.Row{
		{
			Kind: registryview.RowKindProject, ID: "row-project", UID: layoutProjectUID,
			Name: layoutProjectUID, Depth: 0, Root: "/src/projmux",
			Status: resourcegraph.Status("offline"),
		},
		{
			Kind: registryview.RowKindWindow, ID: "row-window", UID: layoutWindowUID,
			Name: layoutWindowUID, Depth: 1, Status: resourcegraph.Status("offline"),
		},
		{
			Kind: registryview.RowKindPane, ID: "row-pane", UID: layoutPaneUID,
			Name: layoutPaneUID, Depth: 2, Role: "shell",
			Status: resourcegraph.Status("offline"),
		},
		{
			Kind: registryview.RowKindAgent, ID: "row-agent", UID: layoutAgentUID,
			Name: layoutAgentUID, Depth: 2, Provider: "codex", Phase: "running",
			Status: resourcegraph.Status("offline"),
		},
	}
}

func navigationTableEntries(t *testing.T, rows []registryview.Row) []intpickercompat.Entry {
	t.Helper()
	view := registryNavigationView{
		locale: i18n.FallbackLocale,
		view:   registryview.View{},
		rows:   rows,
		now:    time.Time{},
	}
	table := pickerTableEntries(view.entries(), registryNavigationColumns)
	if len(table) != len(rows)+1 {
		t.Fatalf("navigation table has %d lines, want a header plus %d rows", len(table), len(rows))
	}
	return table
}

// pickerTableEntries drops the leading chrome -- Back, the host header, any
// unavailability rows -- and returns the header line followed by the row lines,
// which is the part of the list this layout owns.
func pickerTableEntries(entries []intpickercompat.Entry, columns []string) []intpickercompat.Entry {
	for i, entry := range entries {
		if strings.HasPrefix(entry.Label, columns[0]) &&
			strings.Contains(entry.Label, columns[len(columns)-1]) {
			return entries[i:]
		}
	}
	return nil
}

// navigationCells re-runs the exact production layout -- the same row
// projection and the same shared bounding helper the view calls -- and hands
// back the clipped cells. Asserting on cells rather than on offsets carved out
// of a padded line keeps the assertions honest: a rendered line mixes
// multi-byte glyphs with padding, so any byte offset taken from the ASCII
// header would silently address the wrong cell.
func navigationCells(rows []registryview.Row) ([][]string, []int) {
	table := make([][]string, 0, len(rows)+1)
	table = append(table, registryNavigationColumns)
	for _, row := range rows {
		table = append(table, registryNavigationRowAt(row, i18n.FallbackLocale, time.Time{}))
	}
	return table, boundPickerTableWidths(table, registryNavigationColumnBounds())
}

func runtimeCells(rows []runtimediag.Row) ([][]string, []int) {
	table := make([][]string, 0, len(rows)+1)
	table = append(table, runtimeViewColumns)
	for _, row := range rows {
		table = append(table, runtimeViewRow(row))
	}
	return table, boundPickerTableWidths(table, runtimeViewColumnBounds())
}

// cellAt reads one named column out of a projected row.
func cellAt(t *testing.T, columns, row []string, name string) string {
	t.Helper()
	for i, column := range columns {
		if column == name {
			if i >= len(row) {
				t.Fatalf("row %v has no %s cell", row, name)
			}
			return row[i]
		}
	}
	t.Fatalf("no %s column in %v", name, columns)
	return ""
}

// cellsThroughColumn measures the rendered prefix up to and including one named
// column, from the measured widths rather than from a hardcoded offset -- which
// is what makes the assertion survive a column growing or a budget changing.
func cellsThroughColumn(t *testing.T, columns []string, widths []int, name string) int {
	t.Helper()
	total := 0
	for i, column := range columns {
		total += widths[i]
		if column == name {
			return total
		}
		total += resourceTableGap
	}
	t.Fatalf("no %s column in %v", name, columns)
	return 0
}

// TestFixedViewportNavigationRowKeepsEveryColumnReadableUnderAutomaticUIDNames
// is the threshold property schema v4 must not move.
//
// The popup this view renders into is sized from the client
// (`popupSize(ClientWidth, 80, 120)`), so a wider natural line does not merely
// clip one fixture: it raises the client width at which the table is readable at
// all. Measured on this exact hierarchy the line was 117 cells with pre-v4
// names and 142 cells once every automatic name became its exact uid, which
// moved the full-table threshold from roughly 120 columns to roughly 183 and
// silently cost every operator on a half-split or 1080p terminal the right-hand
// columns. The assertion is therefore that the line fits the popup floor, not
// that NAME is any particular number of characters -- a per-column cap that
// happens to fit today would not survive another column growing.
func TestFixedViewportNavigationRowKeepsEveryColumnReadableUnderAutomaticUIDNames(t *testing.T) {
	t.Parallel()

	rows := automaticNavigationRows()
	table, widths := navigationCells(rows)

	// The threshold: the whole line fits the popup floor, so every client from
	// 120 columns up shows every column.
	for _, row := range table {
		if width := resourceCellWidth(resourceTableLine(row, widths)); width > fixedViewportPopupFloorCells {
			t.Fatalf("navigation line is %d cells, want at most the %d-cell popup floor:\n%s",
				width, fixedViewportPopupFloorCells, resourceTableLine(row, widths))
		}
	}

	// Every declared column is still rendered. A budget that closed the overflow
	// by dropping a column would pass a width assertion and lose the surface.
	if len(table[0]) != len(registryNavigationColumns) {
		t.Fatalf("the navigation header carries %d columns, want %d", len(table[0]), len(registryNavigationColumns))
	}
	for i, column := range registryNavigationColumns {
		if table[0][i] != column {
			t.Fatalf("navigation column %d = %q, want %q", i, table[0][i], column)
		}
	}

	// The narrow case L14 exercises: everything through ACTIONS has to sit inside
	// an 80-column popup's usable width, because the action list is what this
	// surface exists to offer. RUNTIME and UID are deliberately not asserted
	// here -- they have never fit 80 columns, which predates schema v4.
	if through := cellsThroughColumn(t, registryNavigationColumns, widths, "ACTIONS"); through > fixedViewportNarrowUsableCells {
		t.Fatalf("navigation prefix through ACTIONS is %d cells, want at most %d",
			through, fixedViewportNarrowUsableCells)
	}

	// Identity is never what gets clipped: the UID column carries the whole uid.
	for i, row := range rows {
		if got := cellAt(t, registryNavigationColumns, table[i+1], "UID"); got != row.UID {
			t.Fatalf("row %d UID cell = %q, want the complete %q", i, got, row.UID)
		}
	}

	// The widest NAME cell is clipped, the clip is marked, and what survives can
	// never be read as a complete name.
	paneIndex := -1
	for i, row := range rows {
		if row.Kind == registryview.RowKindPane {
			paneIndex = i
		}
	}
	if paneIndex < 0 {
		t.Fatal("the fixture has no Pane row")
	}
	name := cellAt(t, registryNavigationColumns, table[paneIndex+1], "NAME")
	if !strings.HasSuffix(name, "…") {
		t.Fatalf("the widest NAME cell was not visibly truncated: %q", name)
	}
	if name == layoutPaneUID+" (shell)" || !strings.HasPrefix(layoutPaneUID, strings.TrimSuffix(name, "…")) {
		t.Fatalf("NAME cell %q is not a marked prefix of the durable name", name)
	}

	// The search key keeps the untruncated name, uid and context, because an
	// operator filters on what the resource is rather than on what fit in a cell.
	entries := navigationTableEntries(t, automaticNavigationRows())
	pane := entries[paneIndex+1]
	for _, want := range []string{layoutPaneUID, "shell"} {
		if !strings.Contains(pane.SearchKey, want) {
			t.Fatalf("the navigation search key dropped %q: %q", want, pane.SearchKey)
		}
	}
	if strings.Contains(pane.SearchKey, "…") {
		t.Fatalf("the navigation search key was routed through a truncated cell: %q", pane.SearchKey)
	}
}

// TestFixedViewportNavigationBoundsAnUnboundedRuntimeTarget covers the column
// that is not a v4 regression but has no ceiling of its own: a RUNTIME cell
// renders a tmux target, and a target embeds an operator-chosen session name.
func TestFixedViewportNavigationBoundsAnUnboundedRuntimeTarget(t *testing.T) {
	t.Parallel()

	const target = "work-a-very-long-operator-chosen-session-name:@31.%214"
	rows := automaticNavigationRows()
	rows[2].Runtime = &resourcegraph.RuntimeRef{ID: "%214", Target: target}

	table, _ := navigationCells(rows)
	cell := cellAt(t, registryNavigationColumns, table[3], "RUNTIME")
	if resourceCellWidth(cell) > registryNavigationRuntimeCells {
		t.Fatalf("RUNTIME cell %q is %d cells, over its %d-cell bound",
			cell, resourceCellWidth(cell), registryNavigationRuntimeCells)
	}
	if !strings.HasSuffix(cell, "…") {
		t.Fatalf("an over-budget RUNTIME cell was not visibly truncated: %q", cell)
	}

	live := automaticNavigationRows()
	live[2].Runtime = &resourcegraph.RuntimeRef{ID: "%214", Target: target}
	entries := navigationTableEntries(t, live)
	if !strings.Contains(entries[3].SearchKey, target) {
		t.Fatalf("the search key dropped the exact target %q: %q", target, entries[3].SearchKey)
	}
}

// TestFixedViewportRuntimeDiagnosticsRowKeepsEveryColumnReadable is the sibling
// surface. `runtimeResourceCell` renders `<kind>/<Registry name>`, so v4's
// automatic names widened it exactly the way the navigation NAME cell widened:
// measured on this report the natural line went from 92 cells to 120, all of it
// in RESOURCE growing from 11 cells to 39.
func TestFixedViewportRuntimeDiagnosticsRowKeepsEveryColumnReadable(t *testing.T) {
	t.Parallel()

	const reason = "runtime object is not bound to a Registry resource"
	rows := []runtimediag.Row{
		{
			Kind: "session", ID: "$0", Target: "work-projmux", Name: "work-projmux",
			Class: "managed", UID: layoutProjectUID,
			Resource: &runtimediag.Resource{
				Kind: "project", Name: layoutProjectUID, UID: layoutProjectUID,
			},
			Reason: reason,
		},
		{
			Kind: "pane", ID: "%7", Target: "work-projmux:@3.%7", Name: "~/src/projmux",
			Class: "managed", UID: layoutPaneUID, ContainerID: "@3",
			Resource: &runtimediag.Resource{
				Kind: "pane", Name: layoutPaneUID, UID: layoutPaneUID,
			},
			Reason: reason,
		},
	}
	table, widths := runtimeCells(rows)

	for _, row := range table {
		if width := resourceCellWidth(resourceTableLine(row, widths)); width > fixedViewportPopupFloorCells {
			t.Fatalf("runtime line is %d cells, want at most the %d-cell popup floor:\n%s",
				width, fixedViewportPopupFloorCells, resourceTableLine(row, widths))
		}
	}
	if len(table[0]) != len(runtimeViewColumns) {
		t.Fatalf("the runtime header carries %d columns, want %d", len(table[0]), len(runtimeViewColumns))
	}
	for i, column := range runtimeViewColumns {
		if table[0][i] != column {
			t.Fatalf("runtime column %d = %q, want %q", i, table[0][i], column)
		}
	}
	// The exact tmux coordinates are never clipped: they are what an operator
	// retypes into `tmux -S <socket>`.
	for i, row := range rows {
		if got := cellAt(t, runtimeViewColumns, table[i+1], "ID"); got != row.ID {
			t.Fatalf("runtime row %d ID cell = %q, want the exact %q", i, got, row.ID)
		}
	}
	resource := cellAt(t, runtimeViewColumns, table[2], "RESOURCE")
	if resourceCellWidth(resource) > runtimeDiagnosticsResourceCells {
		t.Fatalf("RESOURCE cell %q is %d cells, over its %d-cell bound",
			resource, resourceCellWidth(resource), runtimeDiagnosticsResourceCells)
	}
	if !strings.HasSuffix(resource, "…") {
		t.Fatalf("the widest RESOURCE cell was not visibly truncated: %q", resource)
	}

	view := runtimeDiagnosticsView{
		locale:    i18n.FallbackLocale,
		hostMode:  "app-owned",
		transport: resourcegraph.Transport{},
		rows:      rows,
	}
	entries := pickerTableEntries(view.entries(), runtimeViewColumns)
	if len(entries) != len(rows)+1 {
		t.Fatalf("runtime entries = %d, want a header plus %d rows", len(entries), len(rows))
	}
	pane := entries[2]
	for _, want := range []string{layoutPaneUID, "%7", "work-projmux:@3.%7"} {
		if !strings.Contains(pane.SearchKey, want) {
			t.Fatalf("the runtime search key dropped %q: %q", want, pane.SearchKey)
		}
	}
	if strings.Contains(pane.SearchKey, "…") {
		t.Fatalf("the runtime search key was routed through a truncated cell: %q", pane.SearchKey)
	}
}

// TestTruncateDisplayCellsCutsByWidthAndMarksTheCut pins the two properties the
// whole layout rests on: a cut is visible, and it is measured the way the
// terminal draws the value rather than by bytes or runes.
func TestTruncateDisplayCellsCutsByWidthAndMarksTheCut(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "a value inside the budget is untouched", value: "zsh (shell)", limit: 24, want: "zsh (shell)"},
		{name: "a value exactly at the budget is untouched", value: "0123456789", limit: 10, want: "0123456789"},
		{name: "an exact-uid name is cut and marked", value: layoutPaneUID, limit: 12, want: "pane-afcbuy…"},
		{name: "a zero budget disables the bound", value: layoutPaneUID, limit: 0, want: layoutPaneUID},
		// Two cells per glyph, so a five-cell budget admits two glyphs plus the
		// marker rather than five glyphs.
		{name: "wide glyphs are measured as cells", value: "가나다라마", limit: 5, want: "가나…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := truncateDisplayCells(test.value, test.limit)
			if got != test.want {
				t.Fatalf("truncateDisplayCells(%q, %d) = %q, want %q", test.value, test.limit, got, test.want)
			}
			if test.limit > 0 && i18n.TerminalCellWidth(got) > test.limit {
				t.Fatalf("%q measures %d cells, over the %d-cell budget",
					got, i18n.TerminalCellWidth(got), test.limit)
			}
		})
	}
}

// TestPickerColumnBoundsResolveByColumnName proves a budget cannot drift onto
// the wrong cell if a column contract is reordered.
func TestPickerColumnBoundsResolveByColumnName(t *testing.T) {
	t.Parallel()

	bounds := pickerColumnBoundsFor([]string{"UID", "NAME", "RUNTIME"}, map[string]int{
		"NAME":    registryNavigationNameCells,
		"RUNTIME": registryNavigationRuntimeCells,
	})
	if len(bounds) != 2 {
		t.Fatalf("bounds = %+v, want one per named column", bounds)
	}
	if bounds[0].column != 1 || bounds[0].cells != registryNavigationNameCells {
		t.Fatalf("NAME bound = %+v, want column 1", bounds[0])
	}
	if bounds[1].column != 2 || bounds[1].cells != registryNavigationRuntimeCells {
		t.Fatalf("RUNTIME bound = %+v, want column 2", bounds[1])
	}
	if unknown := pickerColumnBoundsFor([]string{"KIND"}, map[string]int{"NAME": 8}); len(unknown) != 0 {
		t.Fatalf("a budget naming an absent column produced %+v", unknown)
	}
}
