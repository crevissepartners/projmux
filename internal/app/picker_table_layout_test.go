package app

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
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

// pickerTableEntries drops the leading chrome and retains the table in order.
func pickerTableEntries(entries []intpickercompat.Entry, columns []string) []intpickercompat.Entry {
	for i, entry := range entries {
		if strings.HasPrefix(entry.Label, columns[0]) && strings.Contains(entry.Label, columns[len(columns)-1]) {
			return entries[i:]
		}
	}
	return nil
}

func TestPickerCompactFullUIDAndHangulFits80Columns(t *testing.T) {
	rows := automaticNavigationRows()
	for i := range rows {
		rows[i].Actions = []registryview.Action{registryview.ActionStart, registryview.ActionDelete}
		if rows[i].Kind == registryview.RowKindAgent {
			rows[i].Actions[0] = registryview.ActionResume
		}
	}
	rows = append(rows, registryview.Row{Kind: registryview.RowKindPane, ID: "hangul", UID: layoutPaneUID,
		Name: layoutPaneUID, Context: registryview.Context{Value: "한글 작업 이름"}, Depth: 2,
		Status: resourcegraph.Status("offline"), Actions: []registryview.Action{registryview.ActionStart, registryview.ActionDelete}})
	view := registryNavigationView{rows: rows, locale: i18n.FallbackLocale}
	compact := pickerTableEntries(view.entries(), registryNavigationColumns(columnCompact))
	if len(compact) != len(rows)+1 {
		t.Fatalf("compact entries: %v", compact)
	}
	for _, entry := range compact {
		if width := resourceCellWidth(entry.Label); width > fixedViewportNarrowUsableCells {
			t.Fatalf("compact width %d > %d: %s", width, fixedViewportNarrowUsableCells, entry.Label)
		}
	}
	for i, row := range rows {
		if !strings.Contains(compact[i+1].Label, registryNavigationBaseName(row)) {
			t.Fatalf("full NAME missing: %s", compact[i+1].Label)
		}
	}
	view.profile = columnWide
	wide := pickerTableEntries(view.entries(), registryNavigationColumns(columnWide))
	maxWidth := func(entries []intpickercompat.Entry) int {
		n := 0
		for _, entry := range entries {
			n = max(n, resourceCellWidth(entry.Label))
		}
		return n
	}
	t.Logf("actual-action full UID/Hangul compact=%d wide=%d cells", maxWidth(compact), maxWidth(wide))
	for i, row := range rows {
		if wide[i+1].Value != compact[i+1].Value || wide[i+1].SearchKey != compact[i+1].SearchKey {
			t.Fatal("profile changed identity/search")
		}
		if !strings.Contains(wide[i+1].Label, registryNavigationName(row)) || !strings.Contains(wide[i+1].Label, row.UID) {
			t.Fatalf("wide dropped full value: %s", wide[i+1].Label)
		}
	}
}

func TestPickerWideRetainsUnboundedValuesAndDetailRecovery(t *testing.T) {
	const target = "work-a-very-long-operator-chosen-session-name:@31.%214"
	rows := automaticNavigationRows()
	rows[2].Runtime = &resourcegraph.RuntimeRef{ID: "%214", Target: target}
	view := registryNavigationView{rows: rows, locale: i18n.FallbackLocale, profile: columnWide}
	entries := pickerTableEntries(view.entries(), registryNavigationColumns(columnWide))
	for _, want := range []string{layoutPaneUID + " (shell)", target, layoutPaneUID} {
		if !strings.Contains(entries[3].Label, want) {
			t.Fatalf("wide dropped %q: %s", want, entries[3].Label)
		}
	}
	if !strings.Contains(entries[3].SearchKey, target) {
		t.Fatal("full raw target missing from search")
	}
	details := view.actionEntries(rows[2], "/socket", true)
	for _, want := range []string{"UID: " + layoutPaneUID, "RUNTIME: " + target, "NAME: " + layoutPaneUID + " (shell)"} {
		found := false
		for _, entry := range details {
			found = found || entry.Label == want
		}
		if !found {
			t.Fatalf("missing detail %q", want)
		}
	}
	runtimeRow := runtimediag.Row{Kind: "pane", ID: "%214", ContainerID: "@31", Name: "한글 아주 긴 이름을 가진 창 제목과 원문 보존", Target: target,
		Class: "managed", UID: layoutPaneUID, Resource: &runtimediag.Resource{Kind: "pane", Name: layoutPaneUID, UID: layoutPaneUID}, Reason: strings.Repeat("reason ", 20)}
	runtimeView := runtimeDiagnosticsView{rows: []runtimediag.Row{runtimeRow}, profile: columnWide}
	runtimeEntries := pickerTableEntries(runtimeView.entries(), runtimeViewColumns(columnWide))
	for _, want := range []string{runtimeRow.Name, "pane/" + layoutPaneUID, strings.TrimSpace(runtimeRow.Reason)} {
		if !strings.Contains(runtimeEntries[1].Label, want) {
			t.Fatalf("runtime wide dropped %q", want)
		}
	}
	for _, field := range columnsFor(columnRuntimePicker, "", columnWide) {
		if field.field == columnKind {
			continue
		}
		if got, want := runtimeColumnValue(field.field, runtimeRow, true), runtimeColumnValue(field.field, runtimeRow, false); got != want {
			t.Fatalf("runtime CLI parity %s: %q != %q", field.field, got, want)
		}
	}
	before := runtimeView.rows
	runtimeView.profile = columnCompact
	if !reflect.DeepEqual(before, runtimeView.rows) {
		t.Fatal("profile mutated source")
	}
}

func TestPickerProfilesUseExactCatalogFieldOrder(t *testing.T) {
	row := automaticNavigationRows()[3]
	for _, profile := range []columnProfile{columnCompact, columnWide} {
		columns := columnsFor(columnRegistryPicker, "", profile)
		cells := registryNavigationRowAt(row, i18n.FallbackLocale, time.Time{}, profile)
		if len(cells) != len(columns) {
			t.Fatal("Registry field count mismatch")
		}
		for i, field := range columns {
			switch field.field {
			case columnKind:
				if cells[i] != runtimeCell(registryNavigationIndent(row)+string(row.Kind)) {
					t.Fatal("kind drift")
				}
			case columnUID:
				if cells[i] != row.UID {
					t.Fatal("UID drift")
				}
			case columnName:
				want := registryNavigationBaseName(row)
				if profile == columnWide {
					want = registryNavigationName(row)
				}
				if cells[i] != want {
					t.Fatal("NAME drift")
				}
			}
		}
	}
}

// Natural widths are evidence about these rows, not the renderer's available
// viewport. Attached tmux smoke observes clipping against real frame geometry.
func TestPickerWideNaturalWidthWitnesses(t *testing.T) {
	for _, test := range []struct {
		actions []registryview.Action
		want    int
	}{
		{nil, 142},
		{[]registryview.Action{registryview.ActionResume, registryview.ActionDelete}, 148},
	} {
		rows := automaticNavigationRows()
		for i := range rows {
			rows[i].Actions = test.actions
		}
		view := registryNavigationView{rows: rows, profile: columnWide}
		widest := 0
		for _, entry := range pickerTableEntries(view.entries(), registryNavigationColumns(columnWide)) {
			widest = max(widest, resourceCellWidth(entry.Label))
		}
		if widest != test.want {
			t.Fatalf("natural width=%d want%d", widest, test.want)
		}
	}
}

// This uses the same exported row/frame pipeline as the native picker, including
// its pointer and always-reserved scrollbar gutter. No guessed usable width.
func columnRowNativeFrame(label string, width, height int, scrollbar bool) string {
	renderer := projmuxpicker.DefaultRenderer()
	layout := projmuxpicker.Layout{Cols: width, Rows: height}
	content := renderer.ContentLayoutWithTitle(layout, "Columns")
	lines := projmuxpicker.InteractiveListLines([]projmuxpicker.Row{{Label: label}}, 0, 1, 0, false)
	total := 1
	if scrollbar {
		total = 10
	}
	lines = projmuxpicker.ListLinesWithScrollbarRows(lines, total, 0, 1, content.Cols, 1)
	var frame strings.Builder
	renderer.RenderFrameWithTitle(&frame, strings.Join(lines, "\n"), "Columns", layout)
	return stripANSI(frame.String())
}

func TestPickerCompactNativeFramePreserves76CellRegression(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		rows := automaticNavigationRows()
		for i := range rows {
			rows[i].Actions = []registryview.Action{registryview.ActionStart, registryview.ActionDelete}
		}
		rows[3].Actions[0] = registryview.ActionResume
		if boundary {
			rows[0].Context = registryview.Context{Value: "columnar-한글-" + strings.Repeat("x", 28)}
		}
		table := [][]string{registryNavigationColumns(columnCompact)}
		for _, row := range rows {
			table = append(table, registryNavigationRowAt(row, i18n.FallbackLocale, time.Time{}, columnCompact))
		}
		widths := pickerTableWidths(table)
		old := resourceTableLine(table[4], widths)
		oldWidth := 66
		if boundary {
			oldWidth = 76
		}
		if got := resourceCellWidth(old); got != oldWidth {
			t.Fatalf("regression fixture=%d want%d", got, oldWidth)
		}
		fixed := pickerTableLine(table[4], widths, columnCompact)
		if got := resourceCellWidth(fixed); got != oldWidth-3 {
			t.Fatalf("compact=%d want%d", got, oldWidth-3)
		}
		for _, scrollbar := range []bool{false, true} {
			before := columnRowNativeFrame(old, 80, 24, scrollbar)
			if boundary && strings.Contains(before, old) {
				t.Fatal("old76-cell regression must reproduce native clipping")
			}
			for _, cells := range table {
				label := pickerTableLine(cells, widths, columnCompact)
				frame := columnRowNativeFrame(label, 80, 24, scrollbar)
				if !strings.Contains(frame, label) {
					t.Fatalf("compact lost full row in actual frame:\n%s\nwant%s", frame, label)
				}
			}
		}
	}
}

func TestPickerWideNativeFrameViewportMatrix(t *testing.T) {
	view := registryNavigationView{rows: automaticNavigationRows(), profile: columnWide}
	widest := ""
	for _, entry := range pickerTableEntries(view.entries(), registryNavigationColumns(columnWide)) {
		if resourceCellWidth(entry.Label) > resourceCellWidth(widest) {
			widest = entry.Label
		}
	}
	if resourceCellWidth(widest) != 142 {
		t.Fatal("historical witness must remain142 cells")
	}
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 30}, {180, 40}, {184, 40}} {
		popup, err := strconv.Atoi(popupSize(size.width, 80, 120))
		if err != nil {
			t.Fatal(err)
		}
		for _, scrollbar := range []bool{false, true} {
			frame := columnRowNativeFrame(widest, popup, size.height, scrollbar)
			if full := strings.Contains(frame, widest); full != (size.width >= 184) {
				t.Fatalf("client%d popup%d full=%v:\n%s", size.width, popup, full, frame)
			}
		}
	}
}
