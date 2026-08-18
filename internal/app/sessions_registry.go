package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Registry-first half of the recent-session surface.
//
// The picker listed every tmux session on the server, which meant the Home
// control session, an auto-attach scratch session, and anything on a guest tmux
// were rows in the same list as a managed Project -- indistinguishable from one
// and offering the same actions. That is the mixing the Registry-first model
// exists to end: a Main surface lists managed resources, and everything else is
// reached through the Runtime escape hatch.
//
// Attribution comes from the resolved graph and from the exact `$N` session id,
// never from a session name. The name is what an operator reads and what the
// action adapter targets; the id is what says whose session it is.

// sessionsRuntimeSentinel is the selection token of the Runtime link row.
const sessionsRuntimeSentinel = "__projmux_sessions_runtime__"

// managedSessionAttribution is one resolved answer about one observed session.
type managedSessionAttribution struct {
	// ResourceUID is the Registry Project uid this session projects.
	ResourceUID string
	// ResourceName is that Project's stable name.
	ResourceName string
}

// sessionsAttribution is the partition of one observation of the exact host.
type sessionsAttribution struct {
	// managed holds the summaries bound to a Registry Project, in the order the
	// caller supplied -- which is recency, and is what this surface is for.
	managed []inttmux.RecentSessionSummary
	// byID maps a managed session's `$N` id to its resource identity.
	byID map[string]managedSessionAttribution
	// withheld counts the observed sessions that are not managed rows, by class.
	withheld registryview.RuntimeCounts
	// resolved reports whether a graph was available at all. When it is false
	// the surface has nothing to attribute with and lists what it observed,
	// because a failed read is not a reason to show an operator an empty list.
	resolved bool
}

// attributeSessions partitions the recent-session summaries by what the
// resolved graph says each observed session is.
//
// A summary with no `$N` id is kept rather than dropped: the id was added to
// this read for exactly this pairing, and a build whose tmux predates it would
// otherwise silently lose every row.
func (c *sessionsCommand) attributeSessions(ctx context.Context, summaries []inttmux.RecentSessionSummary) sessionsAttribution {
	out := sessionsAttribution{byID: map[string]managedSessionAttribution{}}
	if c.navigation == nil {
		out.managed = summaries
		return out
	}
	graph, err := c.navigation.graph(ctx)
	if err != nil {
		out.managed = summaries
		return out
	}
	out.resolved = true
	names := map[string]string{}
	for _, project := range graph.Projects {
		names[project.Project.Metadata.UID] = project.Project.Metadata.Name
	}
	class := map[string]resourcegraph.RuntimeNode{}
	for _, node := range graph.Runtime {
		if node.Ref.Kind == resourcegraph.ObjectSession {
			class[node.Ref.ID] = node
		}
	}
	for _, summary := range summaries {
		id := strings.TrimSpace(summary.ID)
		node, ok := class[id]
		if id == "" || !ok {
			// Either the observation could not name this session or this build
			// read no id for it. Refusing to classify is not a reason to hide a
			// row an operator can see on their own screen.
			out.managed = append(out.managed, summary)
			continue
		}
		if node.Class != resourcegraph.ClassManaged || strings.TrimSpace(node.ResourceUID) == "" {
			out.withheld = addWithheldClass(out.withheld, node.Class)
			continue
		}
		out.byID[id] = managedSessionAttribution{
			ResourceUID:  node.ResourceUID,
			ResourceName: names[node.ResourceUID],
		}
		out.managed = append(out.managed, summary)
	}
	return out
}

func addWithheldClass(counts registryview.RuntimeCounts, class resourcegraph.Class) registryview.RuntimeCounts {
	switch class {
	case resourcegraph.ClassControl:
		counts.Control++
	case resourcegraph.ClassEphemeral:
		counts.Ephemeral++
	case resourcegraph.ClassUnattributed:
		counts.Unattributed++
	case resourcegraph.ClassForeign:
		counts.Foreign++
	case resourcegraph.ClassRecoverable:
		counts.Recoverable++
	case resourcegraph.ClassConflict:
		counts.Conflict++
	}
	return counts
}

// runtimeLinkEntry renders the row that leads to the withheld sessions.
//
// It is present whenever anything was withheld, and it names the classes rather
// than a total. "control 1, ephemeral 2" is the answer to "where is my Home
// session"; a bare "Runtime" would leave an operator to guess whether their
// session is missing or merely elsewhere.
func (a sessionsAttribution) runtimeLinkEntry() (intpickercompat.Entry, bool) {
	if !a.resolved || a.withheld.Total() == 0 {
		return intpickercompat.Entry{}, false
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
	return intpickercompat.Entry{
		Label:     "Runtime - " + strings.Join(parts, ", ") + " not managed here",
		Value:     sessionsRuntimeSentinel,
		SearchKey: "runtime diagnostics unmanaged " + strings.Join(parts, " "),
	}, true
}
