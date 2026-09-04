package app

import (
	"context"
	"slices"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// The Registry-first half of the recent-window surface.
//
// A recent window used to be whatever tmux window an operator had last focused,
// which put a managed Window and a window someone opened by hand inside a
// managed session onto the same list with the same affordances. Under the
// Registry-first model the Main surfaces list managed resources: this one keeps
// its recency ordering, because that is what it is for, and takes its identity
// from the Registry.
//
// Pairing is exact. A recent-window snapshot already stores the tmux `@N`
// window id, and the resolved graph classifies observed windows by that same
// id, so nothing here joins on a window name.

// recentWindowRuntimeValue is the selection token of the Runtime link row.
const recentWindowRuntimeValue = "__projmux_recent_window_runtime__"

// recentWindowAttribution is the partition of the recent-window candidates.
type recentWindowAttribution struct {
	// managed holds the candidates bound to a Registry Window, in recency order.
	managed []recentwindows.Candidate
	// presentations maps a managed window's `@N` id to the stable Registry
	// address plus the invocation-scoped context derived from the same graph.
	presentations map[string]recentWindowPresentation
	// withheld counts the recent windows that are not managed rows, by class.
	withheld registryview.RuntimeCounts
	// resolved reports whether a graph was available to attribute with.
	resolved bool
}

type recentWindowPresentation struct {
	name    string
	context registryview.Context
}

// attributeRecentWindows partitions the candidates by what the resolved graph
// says each window is.
//
// The current window is never withheld. An operator standing in a window that
// projmux does not manage still needs the "stay here" row, and removing it would
// make the picker's own current-selection semantics unrepresentable.
func (c *recentWindowCommand) attributeRecentWindows(ctx context.Context, candidates []recentwindows.Candidate) recentWindowAttribution {
	out := recentWindowAttribution{presentations: map[string]recentWindowPresentation{}}
	if c.navigation == nil {
		out.managed = candidates
		return out
	}
	graph, err := c.navigation.graph(ctx)
	if err != nil {
		out.managed = candidates
		return out
	}
	out.resolved = true
	projector := registryview.NewObservedContextProjector(graph)
	windowPresentations := map[string]recentWindowPresentation{}
	for _, window := range graph.Windows {
		uid := window.Window.Metadata.UID
		windowPresentations[uid] = recentWindowPresentation{
			name:    window.Window.Metadata.Name,
			context: projector.For(coremetadata.KindWindow, uid),
		}
	}
	observed := map[string]resourcegraph.RuntimeNode{}
	for _, node := range graph.Runtime {
		if node.Ref.Kind == resourcegraph.ObjectWindow {
			observed[node.Ref.ID] = node
		}
	}
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.WindowID)
		node, ok := observed[id]
		switch {
		case candidate.IsCurrent, id == "", !ok:
			// The current window always stays. An unobserved one stays too: the
			// recent list is allowed to remember a window the current server no
			// longer has, and calling that "not managed" would be a claim the
			// observation did not support.
			out.managed = append(out.managed, candidate)
		case node.Class != resourcegraph.ClassManaged || strings.TrimSpace(node.ResourceUID) == "":
			out.withheld = addWithheldClass(out.withheld, node.Class)
		default:
			out.presentations[id] = windowPresentations[node.ResourceUID]
			out.managed = append(out.managed, candidate)
		}
	}
	return out
}

// runtimeLinkItem renders the picker row that leads to the withheld windows.
func (a recentWindowAttribution) runtimeLinkItem() (intpicker.Item, bool) {
	if !a.resolved || a.withheld.Total() == 0 {
		return intpicker.Item{}, false
	}
	parts := make([]string, 0, 6)
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"control", a.withheld.Control},
		{"ephemeral", a.withheld.Ephemeral},
		{"unattributed", a.withheld.Unattributed},
		{"foreign", a.withheld.Foreign},
		{"recoverable", a.withheld.Recoverable},
		{"conflict", a.withheld.Conflict},
	} {
		if entry.count > 0 {
			parts = append(parts, entry.name+" "+strconv.Itoa(entry.count))
		}
	}
	return intpicker.Item{
		Title:      "Runtime - " + strings.Join(parts, ", ") + " not managed here",
		Value:      recentWindowRuntimeValue,
		SearchText: "runtime diagnostics unmanaged " + strings.Join(parts, " "),
	}, true
}

// resourcePresentation returns the durable name and ephemeral context of one
// exactly managed candidate.
func (a recentWindowAttribution) resourcePresentation(candidate recentwindows.Candidate) recentWindowPresentation {
	return a.presentations[strings.TrimSpace(candidate.WindowID)]
}

// annotate adds the Registry Window name to each managed row's badges.
//
// It is a badge rather than a replacement of the title on purpose: the title is
// the pane content an operator recognizes the window by, and the Registry name
// is what says the window is a managed resource. Losing either one would make
// the row worse.
func (a recentWindowAttribution) annotate(items []intpicker.Item, candidates []recentwindows.Candidate) []intpicker.Item {
	if len(items) != len(candidates) {
		return items
	}
	for i := range items {
		presentation := a.resourcePresentation(candidates[i])
		for _, badge := range []string{presentation.context.Value, presentation.name} {
			badge = strings.TrimSpace(badge)
			if badge == "" || slices.Contains(items[i].Badges, badge) {
				continue
			}
			items[i].Badges = append(items[i].Badges, badge)
		}
		items[i].SearchText = strings.TrimSpace(strings.Join([]string{
			items[i].EffectiveSearchText(), presentation.context.Value,
			string(presentation.context.Source), presentation.name,
		}, " "))
	}
	return items
}
