package app

import (
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// runtimeDiagnosticsView is the picker's pure row model.
//
// It is a value with no runner, no clock, and no environment so the rows a
// test asserts are the rows the picker renders. The command above owns the
// loop and the handoffs; everything an operator reads is decided here.
type runtimeDiagnosticsView struct {
	locale      i18n.Locale
	hostMode    string
	transport   resourcegraph.Transport
	unavailable []runtimediag.Unavailability
	rows        []runtimediag.Row
}

// runtimeRowValue is the selection token of one runtime object.
//
// It carries the kind and the stable tmux id and nothing else. A label carries
// display text that changes between observations; an id does not, so a
// selection resolved through this token always names the object the operator
// was looking at.
func runtimeRowValue(row runtimediag.Row) string {
	return "runtime:" + row.Kind + ":" + row.ID
}

// runtimeViewColumns is the one column contract of the picker list. Unlike the
// CLI, which projects one kind per invocation and can afford kind-specific
// columns, the picker shows all three kinds in one list, so KIND is a column
// and every row shares the rest.
var runtimeViewColumns = []string{"KIND", "ID", "IN", "NAME", "CLASS", "RESOURCE", "REASON"}

// runtimeViewColumnBounds is this view's half of the same fixed-viewport
// budget. RESOURCE is the cell schema v4 widened, because it renders
// `<kind>/<Registry name>`; NAME and REASON are bounded as ceilings over free
// text rather than as v4 repairs. The exact tmux handles -- ID and IN -- are
// never clipped: they are the coordinates an operator retypes.
func runtimeViewColumnBounds() []pickerColumnBound {
	return pickerColumnBoundsFor(runtimeViewColumns, map[string]int{
		"NAME":     runtimeDiagnosticsNameCells,
		"RESOURCE": runtimeDiagnosticsResourceCells,
		"REASON":   runtimeDiagnosticsReasonCells,
	})
}

func runtimeViewRow(row runtimediag.Row) []string {
	return []string{
		runtimeCell(row.Kind), runtimeCell(row.ID), runtimeCell(row.ContainerID),
		runtimeCell(row.Name), runtimeCell(row.Class), runtimeResourceCell(row),
		runtimeCell(row.Reason),
	}
}

// entries renders the picker list: one header info row, one row per
// unobservable scope, then every observed object in containment order.
//
// The header and the unavailable rows are selectable-but-inert rather than
// chrome, because chrome is not searchable and these are exactly what an
// operator types into the filter when the list is long: the transport when they
// are checking which server answered, the scope name when they are checking
// whether an empty list means empty or unreadable.
func (v runtimeDiagnosticsView) entries() []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, len(v.rows)+len(v.unavailable)+2)
	entries = append(entries, intpickercompat.Entry{
		Label: runtimeHeaderLine(runtimediag.Report{
			HostMode: v.hostMode,
			Transport: runtimediag.Transport{
				Kind:   string(v.transport.Kind),
				Value:  v.transport.Value,
				Source: string(v.transport.Source),
			},
		}),
		Value: settingsNoopValue,
	})
	if summary := runtimeClassSummary(v.rows); summary != "" {
		entries = append(entries, intpickercompat.Entry{Label: summary, Value: settingsNoopValue})
	}
	for _, entry := range v.unavailable {
		entries = append(entries, intpickercompat.Entry{
			Label: "unavailable " + entry.Scope + ": " + entry.Reason,
			Value: settingsNoopValue,
		})
	}
	if len(v.rows) == 0 {
		return entries
	}

	table := make([][]string, 0, len(v.rows)+1)
	table = append(table, runtimeViewColumns)
	for _, row := range v.rows {
		table = append(table, runtimeViewRow(row))
	}
	widths := boundPickerTableWidths(table, runtimeViewColumnBounds())
	entries = append(entries, intpickercompat.Entry{
		Label: resourceTableLine(table[0], widths),
		Value: settingsNoopValue,
	})
	for i, row := range v.rows {
		entries = append(entries, intpickercompat.Entry{
			Label:     resourceTableLine(table[i+1], widths),
			Value:     runtimeRowValue(row),
			SearchKey: runtimeRowSearchKey(row),
		})
	}
	return entries
}

// runtimeRowSearchKey widens the filter to the fields an operator searches by
// but the row does not spell out: the mirrored uid, the bound resource uid, and
// the full exact coordinate.
func runtimeRowSearchKey(row runtimediag.Row) string {
	parts := []string{row.Kind, row.ID, row.Target, row.Name, row.Class, row.UID, row.SessionID, row.ContainerID}
	if row.Resource != nil {
		parts = append(parts, row.Resource.Kind, row.Resource.Name, row.Resource.UID)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, " ")
}

// rowByValue resolves one selection token back to its row.
func (v runtimeDiagnosticsView) rowByValue(value string) (runtimediag.Row, bool) {
	for _, row := range v.rows {
		if runtimeRowValue(row) == value {
			return row, true
		}
	}
	return runtimediag.Row{}, false
}

// actionEntries renders the safe-action menu of one object.
//
// An action that does not apply is listed as an inert row stating why rather
// than being dropped. "Attach is not offered because nothing in the Registry
// claims this session" is the diagnostic; a menu that silently shrank would
// leave an operator guessing whether the action is missing or the object is.
func (v runtimeDiagnosticsView) actionEntries(row runtimediag.Row, socket string, insideTmux bool) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntryLocale(v.locale)}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfoLocale(v.locale, "Runtime object", runtimeObjectSummary(row), row.Reason),
		Value: settingsNoopValue,
	})
	for _, conflict := range row.Conflicts {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(v.locale, "Conflict", conflict.Reason, conflict.Detail),
			Value: settingsNoopValue,
		})
	}

	switch {
	case strings.TrimSpace(row.Target) == "":
		entries = append(entries, runtimeUnavailableAction(v.locale, "Focus",
			"unavailable - the containing session was not observed, so there is no exact coordinate"))
	case strings.TrimSpace(socket) == "":
		entries = append(entries, runtimeUnavailableAction(v.locale, "Focus",
			"unavailable - the exact socket path could not be read from the server"))
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Focus", row.Target),
			Value: runtimeActionFocus,
		})
	}

	switch {
	case row.Kind != string(resourcegraph.ObjectSession):
		entries = append(entries, runtimeUnavailableAction(v.locale, "Attach",
			"unavailable - only a session projects a Project runtime"))
	case !row.Managed():
		entries = append(entries, runtimeUnavailableAction(v.locale, "Attach",
			"unavailable - no Registry Project claims this session; diagnostics never adopts one"))
	case insideTmux:
		entries = append(entries, runtimeUnavailableAction(v.locale, "Attach",
			"unavailable - already inside a tmux client; use Focus instead"))
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Attach", row.Resource.Name),
			Value: runtimeActionAttach,
		})
	}

	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Open Resource Inspector", ""),
		Value: runtimeActionInspect,
	})
	return entries
}

// runtimeUnavailableAction renders one action that does not apply to this row.
//
// It is dim rather than absent, and it states the reason rather than the
// action, because the reason is the diagnostic: "no Registry Project claims
// this session" is the answer an operator came for, and a menu that simply
// omitted Attach would have withheld it.
func runtimeUnavailableAction(locale i18n.Locale, name, reason string) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabelDimLocale(locale, name, reason),
		Value: settingsNoopValue,
	}
}

// runtimeObjectSummary renders one object as `kind id (class)`.
func runtimeObjectSummary(row runtimediag.Row) string {
	summary := row.Kind + " " + row.ID
	if name := strings.TrimSpace(row.Name); name != "" {
		summary += " " + name
	}
	return summary + " (" + row.Class + ")"
}

// runtimeClassSummary renders the attribution tally of the whole server.
//
// It answers the question the list answers row by row -- how much of this
// machine does projmux actually own -- in one line, which is what an operator
// scans first on a server with fifty panes on it. Classes with nothing in them
// are absent rather than zero, so the line names only what is there.
func runtimeClassSummary(rows []runtimediag.Row) string {
	counts := runtimediag.Counts(rows)
	if len(counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(counts))
	for _, entry := range counts {
		parts = append(parts, entry.Class+" "+strconv.Itoa(entry.Count))
	}
	return strings.Join(parts, "  ")
}
