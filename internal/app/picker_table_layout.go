package app

import (
	"github.com/crevissepartners/projmux/internal/i18n"
)

// A fixed-viewport picker row is one pre-rendered label string. The renderer
// clips it at the popup width and the picker does not scroll horizontally, so a
// column pushed past that width is not merely cut off, it is gone -- and the
// layout has no viewport width to react with. `pickercompat.Options` carries no
// width or height, every `Entry.Label` is measured and padded before
// `Runner.Run` is ever called, and the repository has no terminal-size probe of
// any kind. The budget therefore has to be a declared constant rather than an
// observation.
//
// The constants below come from measurement, not taste. Schema v4 made every
// automatic `metadata.name` the resource's exact full UID, and an offline row
// has no invocation-scoped context to render in its place, so every cell that
// carries a Registry name grew by roughly 27 display cells. The consequence was
// not that one fixture overflowed: it was that the *client width at which the
// whole table is readable* went up, because the popup width is derived from the
// client (`popupSize(ClientWidth, 80, 120)`), so every operator on a 120-180
// column terminal silently lost the right-hand columns. Bounding the
// name-bearing cells returns each view's natural line to its pre-v4 width, and
// with it that threshold.
//
// Truncation is display-width based, so a multi-byte or wide-glyph context
// value can never be cut mid-rune or mis-measured, and it is marked with one
// ellipsis cell so a clipped value can never be read as a complete name.
// Identity is never what gets clipped: the navigation UID column keeps the
// whole UID, and every search key keeps the untruncated name, UID and context,
// because an operator filters on what the resource is rather than on what fit
// in the cell.

const (
	// fixedViewportPopupFloorCells is the narrowest popup the production sizing
	// rule produces once the client is at least this wide:
	// `popupSize(total, 80, 120)` clamps up to 120 before it clamps down to the
	// client. A fixed-viewport table whose natural line fits this floor is
	// therefore fully readable for every client from 120 columns up, which is
	// the threshold schema v4 must not raise.
	fixedViewportPopupFloorCells = 120

	// fixedViewportNarrowUsableCells is the content width left inside an
	// 80-column popup: 80 less the two border columns tmux draws and the two
	// pointer/gutter columns the picker reserves for its selection marker.
	// Columns beyond this are unreadable in a narrow client. `RUNTIME` and `UID`
	// have never fit here -- that predates schema v4 and is not this layout's to
	// repair -- but everything through `ACTIONS` must, because the action list is
	// what the navigation surface exists to offer.
	fixedViewportNarrowUsableCells = 76

	// registryNavigationNameCells bounds the navigation NAME cell.
	//
	// Measured against a realistic four-row hierarchy (Project, Window, shell
	// Pane, Agent) whose resources are all automatically named: the natural line
	// is 142 cells at an unbounded NAME of 48, against 117 cells for the same
	// hierarchy with pre-v4 names, whose widest NAME cell was 23
	// (`codex-1 (codex running)`). A 24-cell bound brings the v4 line to 118, so
	// it fits the popup floor above, restores the pre-v4 threshold to within one
	// cell, and is still wide enough that no pre-v4-era name is truncated at all.
	registryNavigationNameCells = 24

	// registryNavigationRuntimeCells bounds the navigation RUNTIME cell.
	//
	// This is not a schema v4 regression: the cell renders a tmux target such as
	// `work-projmux-roadmap:@3.%7`, which measured 26 cells both before and after
	// v4. It is bounded anyway because a target embeds an operator-chosen session
	// name and so has no natural ceiling, and one unbounded cell is enough to
	// undo the whole budget. The bound sits just above the measured live target
	// so realistic bindings render whole.
	registryNavigationRuntimeCells = 28

	// runtimeDiagnosticsResourceCells bounds the runtime diagnostics RESOURCE
	// cell, which is the one schema v4 widened here: `runtimeResourceCell`
	// renders `<kind>/<Registry name>`, so an automatic name turns
	// `pane/zsh` into `pane/pane-afcbuym6ghlpfo73nsjrhueqki`. Measured on a
	// realistic managed report the natural line went from 92 cells before v4 to
	// 120 after, entirely through this cell growing from 11 to 39. The bound
	// matches the navigation NAME budget: both are cells that may carry an exact
	// UID name, and one number for both keeps the two surfaces consistent.
	runtimeDiagnosticsResourceCells = 24

	// runtimeDiagnosticsNameCells bounds the runtime diagnostics NAME cell. Like
	// the navigation RUNTIME cell this is not a v4 regression -- it is what tmux
	// displays, a session name, window name or pane title -- but it is
	// operator-controlled free text with no ceiling of its own.
	runtimeDiagnosticsNameCells = 24

	// runtimeDiagnosticsReasonCells bounds the runtime diagnostics REASON cell.
	// The reason is the sentence that explains the class, so this is a ceiling
	// against pathological growth rather than a budget: the longest reason the
	// classifier ships measures under 70 cells and is left intact.
	runtimeDiagnosticsReasonCells = 72
)

// pickerColumnBound is one column's maximum display width in a fixed-viewport
// picker row, addressed by column index.
type pickerColumnBound struct {
	column int
	cells  int
}

// pickerColumnBoundsFor resolves bounds declared by column name against the
// view's own column contract, so reordering or renaming a column cannot
// silently move a budget onto the wrong cell.
func pickerColumnBoundsFor(columns []string, budgets map[string]int) []pickerColumnBound {
	bounds := make([]pickerColumnBound, 0, len(budgets))
	for i, column := range columns {
		if cells, ok := budgets[column]; ok {
			bounds = append(bounds, pickerColumnBound{column: i, cells: cells})
		}
	}
	return bounds
}

// boundPickerTableWidths clamps every bounded column's cells in place and
// returns the column widths the padded rows are laid out against.
//
// It replaces the plain max-over-rows measurement a fixed-viewport table used to
// share with the stdout tables. The stdout projections keep that unbounded
// measurement on purpose: a terminal wraps, the operator can widen it or pipe
// it, and clipping there would cost copy-pasteable identifiers for nothing.
//
// table[0] is the header and is never clipped. A clipped column name would make
// the table unreadable, and callers build the header by appending their
// package-level column contract, so the row aliases that slice -- writing
// through it here would edit the contract itself for the life of the process.
func boundPickerTableWidths(table [][]string, bounds []pickerColumnBound) []int {
	if len(table) == 0 {
		return nil
	}
	for _, bound := range bounds {
		if bound.cells <= 0 || bound.column < 0 {
			continue
		}
		for _, row := range table[1:] {
			if bound.column >= len(row) {
				continue
			}
			row[bound.column] = truncateDisplayCells(row[bound.column], bound.cells)
		}
	}
	widths := make([]int, len(table[0]))
	for _, row := range table {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if width := resourceCellWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	return widths
}

// truncateDisplayCells clips value to limit display cells, spending the last
// cell on an ellipsis so the cut is visible and the result can never be read as
// a complete name. Width-based rather than rune- or byte-based, so a CJK or
// otherwise wide-glyph value is measured and cut the way the terminal draws it.
func truncateDisplayCells(value string, limit int) string {
	if limit <= 0 || i18n.TerminalCellWidth(value) <= limit {
		return value
	}
	return i18n.TruncateTerminalCells(value, limit-1) + "…"
}
