package app

import (
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// registryNavigationView is the hierarchy picker's pure row model.
//
// It is a value with no runner, no clock, and no environment, so the rows a
// test asserts are the rows the picker renders.
type registryNavigationView struct {
	locale i18n.Locale
	view   registryview.View
	rows   []registryview.Row
}

// registryNavigationRowValue is the selection token of one navigation row.
//
// It is the row's own stable id, which is derived from the resource uid. A
// refresh that observes a different machine produces the same token for the
// same resource, which is what makes a selection survive a runtime object
// appearing or disappearing underneath it.
func registryNavigationRowValue(row registryview.Row) string { return row.ID }

var registryNavigationColumns = []string{"KIND", "NAME", "STATUS", "ACTIONS", "RUNTIME", "UID"}

func registryNavigationRow(row registryview.Row) []string {
	return []string{
		runtimeCell(registryNavigationIndent(row) + string(row.Kind)),
		runtimeCell(registryNavigationName(row)),
		runtimeCell(string(row.Status)),
		runtimeCell(registryNavigationActionList(row)),
		runtimeCell(registryNavigationRuntimeCell(row)),
		runtimeCell(row.UID),
	}
}

// registryNavigationIndent renders the owner chain as leading depth.
//
// The tree is drawn in the KIND column rather than the NAME column because a
// name is what an operator types into the filter, and a filtered list has no
// tree left to draw: indenting the name would leave the surviving rows carrying
// whitespace that no longer means anything.
func registryNavigationIndent(row registryview.Row) string {
	if row.Depth <= 0 {
		return ""
	}
	return strings.Repeat("  ", row.Depth) + "└ "
}

func registryNavigationName(row registryview.Row) string {
	name := strings.TrimSpace(row.DisplayName)
	if name == "" {
		name = strings.TrimSpace(row.Name)
	}
	switch {
	case row.Kind == registryview.RowKindAgent && strings.TrimSpace(row.Provider) != "":
		return name + " (" + strings.TrimSpace(row.Provider) + " " + strings.TrimSpace(row.Phase) + ")"
	case row.Kind == registryview.RowKindPane && strings.TrimSpace(row.Role) != "":
		return name + " (" + strings.TrimSpace(row.Role) + ")"
	default:
		return name
	}
}

func registryNavigationActionList(row registryview.Row) string {
	parts := make([]string, 0, len(row.Actions))
	for _, action := range row.Actions {
		parts = append(parts, string(action))
	}
	return strings.Join(parts, ",")
}

func registryNavigationRuntimeCell(row registryview.Row) string {
	if row.Runtime == nil {
		return ""
	}
	if target := strings.TrimSpace(row.Runtime.Target); target != "" {
		return target
	}
	return strings.TrimSpace(row.Runtime.ID)
}

// entries renders the hierarchy list: one header stating which server answered,
// one row per unobservable scope, then the Registry rows in Registry order.
//
// The header is a selectable-but-inert row rather than chrome for the same
// reason the Runtime surface's is: it is what an operator types into the filter
// when they are checking whether an all-offline list means the resources are
// down or the observation could not be taken.
func (v registryNavigationView) entries() []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, len(v.rows)+len(v.view.Unavailable)+3)
	entries = append(entries, settingsBackEntryLocale(v.locale))
	entries = append(entries, intpickercompat.Entry{
		Label: registryNavigationHeaderLine(v.view),
		Value: settingsNoopValue,
	})
	for _, entry := range v.view.Unavailable {
		entries = append(entries, intpickercompat.Entry{
			Label: "unavailable " + string(entry.Scope) + ": " + entry.Reason,
			Value: settingsNoopValue,
		})
	}
	if len(v.rows) == 0 {
		return entries
	}

	table := make([][]string, 0, len(v.rows)+1)
	table = append(table, registryNavigationColumns)
	for _, row := range v.rows {
		table = append(table, registryNavigationRow(row))
	}
	widths := make([]int, len(registryNavigationColumns))
	for _, row := range table {
		for i, cell := range row {
			if width := resourceCellWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	entries = append(entries, intpickercompat.Entry{
		Label: resourceTableLine(table[0], widths),
		Value: settingsNoopValue,
	})
	for i, row := range v.rows {
		entries = append(entries, intpickercompat.Entry{
			Label:     resourceTableLine(table[i+1], widths),
			Value:     registryNavigationRowValue(row),
			SearchKey: registryNavigationSearchKey(row),
		})
	}
	return entries
}

func registryNavigationSearchKey(row registryview.Row) string {
	parts := []string{
		string(row.Kind), row.Name, row.DisplayName, string(row.Status),
		row.UID, row.Provider, row.Phase, row.Role, row.Root,
	}
	if row.Runtime != nil {
		parts = append(parts, row.Runtime.ID, row.Runtime.Target)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, " ")
}

func (v registryNavigationView) rowByValue(value string) (registryview.Row, bool) {
	for _, row := range v.rows {
		if registryNavigationRowValue(row) == value {
			return row, true
		}
	}
	return registryview.Row{}, false
}

// actionEntries renders the action menu of one row.
//
// An eligible action whose route this phase does not own is listed with the
// exact command that performs it rather than being dropped or wired to an
// improvised implementation. Both halves matter: the eligibility is what the
// resource state says, and the command is what an operator can actually run --
// and neither of them is a reason for a read surface to start writing.
func (v registryNavigationView) actionEntries(row registryview.Row, socket string, insideTmux bool) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntryLocale(v.locale)}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfoLocale(v.locale, "Resource", registryNavigationSummary(row), row.Reason),
		Value: settingsNoopValue,
	})

	for _, action := range row.Actions {
		entries = append(entries, v.actionEntry(row, action, socket, insideTmux))
	}
	return entries
}

func (v registryNavigationView) actionEntry(row registryview.Row, action registryview.Action, socket string, insideTmux bool) intpickercompat.Entry {
	switch action {
	case registryview.ActionOpen:
		target := registryNavigationRuntimeCell(row)
		switch {
		case row.Runtime == nil || strings.TrimSpace(row.Runtime.Target) == "":
			return runtimeUnavailableAction(v.locale, "Open",
				"unavailable - the containing session was not observed, so there is no exact coordinate")
		case strings.TrimSpace(socket) == "":
			return runtimeUnavailableAction(v.locale, "Open",
				"unavailable - the exact socket path could not be read from the server")
		default:
			return intpickercompat.Entry{
				Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Open", target),
				Value: navActionOpen,
			}
		}
	case registryview.ActionStart:
		if row.Kind == registryview.RowKindProject {
			if insideTmux {
				return runtimeUnavailableAction(v.locale, "Start",
					"unavailable - already inside a tmux client; open the Project from the Projects list")
			}
			return intpickercompat.Entry{
				Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Start", row.Name),
				Value: navActionStart,
			}
		}
		if insideTmux {
			return runtimeUnavailableAction(v.locale, "Start",
				"unavailable - Window and Pane activation is owned by the Project; open the Project from the Projects list")
		}
		return intpickercompat.Entry{
			Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Start owning Project",
				"activation is owned by the Project, not by this row"),
			Value: navActionStartProject,
		}
	case registryview.ActionResume:
		return intpickercompat.Entry{
			Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Resume", row.Name),
			Value: navActionResume,
		}
	case registryview.ActionRebind:
		return runtimeUnavailableAction(v.locale, "Rebind",
			"run `projmux rebind project uid:"+registryNavigationProjectUID(v.view, row)+" --root <absolute-path>`")
	case registryview.ActionDelete:
		return runtimeUnavailableAction(v.locale, "Delete",
			"run `projmux delete "+string(row.Kind)+" uid:"+row.UID+"`")
	case registryview.ActionRuntime:
		return intpickercompat.Entry{
			Label: settingsLabelLocale(v.locale, settingsGlyphOpen, settingsColorType, "Runtime diagnostics", ""),
			Value: navActionRuntime,
		}
	default:
		return intpickercompat.Entry{Label: settingsLabelDimLocale(v.locale, string(action), ""), Value: settingsNoopValue}
	}
}

// registryNavigationSummary renders one row as `kind name (status)`.
func registryNavigationSummary(row registryview.Row) string {
	return string(row.Kind) + " " + registryNavigationName(row) + " (" + string(row.Status) + ")"
}

// registryNavigationHeaderLine states which server the status column came from.
//
// It is the same claim the Runtime surface makes and for the same reason: the
// rows below are the Registry's on every host, so the only thing that varies is
// the overlay, and an overlay is unreadable without naming the machine it was
// taken from.
func registryNavigationHeaderLine(view registryview.View) string {
	transport := "no tmux transport"
	switch view.Transport.Kind {
	case resourcegraph.TransportSocketName:
		transport = "tmux -L " + view.Transport.Value
	case resourcegraph.TransportSocketPath:
		transport = "tmux -S " + view.Transport.Value
	}
	line := "host " + runtimeCell(string(view.HostMode)) + "  transport " + transport +
		"  source " + runtimeCell(string(view.Transport.Source))
	if total := view.Runtime.Total(); total > 0 {
		line += "  runtime-only " + strconv.Itoa(total)
	}
	return line
}

// registryNavigationRuntimeLabel renders the Runtime link row of a primary
// picker: what the link leads to, tallied by class.
func registryNavigationRuntimeLabel(view registryview.View) string {
	counts := view.Runtime
	parts := make([]string, 0, 6)
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"control", counts.Control},
		{"ephemeral", counts.Ephemeral},
		{"unattributed", counts.Unattributed},
		{"foreign", counts.Foreign},
		{"recoverable", counts.Recoverable},
		{"conflict", counts.Conflict},
	} {
		if entry.count > 0 {
			parts = append(parts, entry.name+" "+strconv.Itoa(entry.count))
		}
	}
	if len(parts) == 0 {
		if !view.Observed() {
			return "Runtime - no tmux transport"
		}
		return "Runtime - nothing here that projmux does not manage"
	}
	return "Runtime - " + strings.Join(parts, ", ")
}

func registryNavigationFooter(locale i18n.Locale) string {
	return localizeText(locale, "picker.registry_nav.footer", "Enter: actions | Esc: back")
}

func registryNavigationActionFooter(locale i18n.Locale) string {
	return localizeText(locale, "picker.registry_nav.action_footer",
		"Registry rows are the source of truth; this surface never writes.")
}
