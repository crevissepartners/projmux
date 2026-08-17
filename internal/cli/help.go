package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// helpFlagNames are the flag names that request help.
//
// The standard library `flag` package strips one or two leading dashes and
// splits on the first `=` before looking a flag name up; an undefined `h`/`help`
// name then returns flag.ErrHelp regardless of any value. So every dash prefix
// and every `=value` suffix of these two names already *is* a help request at
// the parser level, and the shared boundary must intercept all of them or the
// uncovered spellings fall through to the leaf failure path — non-zero exit,
// stderr output, and an operational error row.
//
// This is why matching is name-level: the boundary aligns with what the parser
// already does and never interprets a flag value, which keeps the Phase 0 bridge
// dumb. Leaf flag semantics stay with the leaf parsers.
//
// The invariant that makes this safe is that no leaf parser defines a real
// `help` or `h` flag; TestNoLeafParserDefinesAHelpFlag guards it.
var helpFlagNames = []string{"help", "h"}

// isHelpFlag reports whether arg requests help. It matches the flag NAME only:
// the part after the leading dashes and before the first `=`.
//
// Bare words are never help flags. The top-level `help` route owns that
// spelling, and a nested `help` word (`projmux pin help`) stays with the
// handler that already implements it.
func isHelpFlag(arg string) bool {
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	name, _, _ = strings.Cut(name, "=")
	return slices.Contains(helpFlagNames, name)
}

// argumentTerminator ends option scanning. Anything after the first bare `--`
// is payload that projmux forwards untouched, so a `--help` there is data.
const argumentTerminator = "--"

// nameColumnWidth is the primary help name column. Names shorter than the
// column are padded to it; names at or past the column get exactly two
// trailing spaces. This reproduces the historical hand-written listing.
const nameColumnWidth = 10

// HelpTarget describes an intercepted help invocation.
type HelpTarget struct {
	// Path is the resolved route path, nil for root help.
	Path []string
	// Route is the deepest matched route. It is the zero Route for root help.
	Route Route
	// Root reports whether root help was requested.
	Root bool
}

// RequestedHelp reports whether args is a help invocation and, if so, which
// manifest node owns the rendered help.
//
// Detection rules:
//   - Only tokens before the first bare `--` are considered; a help flag after
//     the terminator is payload and must reach the handler untouched.
//   - An empty argv is root help (the historical bare `projmux` behavior).
//   - A leading help flag is root help.
//   - Otherwise the first token must resolve to a known route. An unknown
//     leading token is not treated as help so `projmux nosuchcmd --help` keeps
//     its unknown-command error instead of silently succeeding.
func RequestedHelp(args []string) (HelpTarget, bool) {
	if len(args) == 0 {
		return HelpTarget{Root: true}, true
	}
	index := helpFlagIndex(args)
	if index < 0 {
		return HelpTarget{}, false
	}
	lead := args[:index]
	if len(lead) == 0 {
		return HelpTarget{Root: true}, true
	}
	path, route, ok := Resolve(lead)
	if !ok {
		return HelpTarget{}, false
	}
	// Retired compatibility bridges are error tombstones, not help surfaces.
	// Let their handlers return exit 2 and the migration replacement even when
	// the removed argv contains a help flag.
	if route.Retired {
		return HelpTarget{}, false
	}
	return HelpTarget{Path: path, Route: route}, true
}

// HelpRequested reports whether args is a help invocation. Help invocations
// must not run lifecycle migrations, touch tmux, or reach a handler.
func HelpRequested(args []string) bool {
	_, ok := RequestedHelp(args)
	return ok
}

// helpFlagIndex returns the index of the first help flag that appears before
// the first bare `--`, or -1 when there is none.
func helpFlagIndex(args []string) int {
	for i, arg := range args {
		if arg == argumentTerminator {
			return -1
		}
		if isHelpFlag(arg) {
			return i
		}
	}
	return -1
}

// RenderHelp writes the help owned by target. Every help rendering path is
// side-effect free: it only writes to w.
func RenderHelp(w io.Writer, target HelpTarget) error {
	if target.Root || len(target.Path) == 0 {
		return RenderRootHelp(w)
	}
	return RenderRouteHelp(w, target.Path, target.Route)
}

// RenderRootHelp writes the primary command listing from the manifest. The
// bytes are pinned by a golden fixture so moving the source of truth into the
// manifest cannot change the historical output.
func RenderRootHelp(w io.Writer) error {
	var b strings.Builder
	b.WriteString("projmux\n")
	b.WriteString("\n")
	b.WriteString("Commands:\n")
	for _, route := range routes {
		if route.Hidden {
			continue
		}
		b.WriteString("  ")
		b.WriteString(padName(route.Name))
		b.WriteString(route.Summary)
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// RenderRouteHelp writes the manifest-owned help for one route path.
func RenderRouteHelp(w io.Writer, path []string, route Route) error {
	var b strings.Builder
	fmt.Fprintf(&b, "projmux %s\n", strings.Join(path, " "))
	if route.Summary != "" {
		b.WriteString("\n")
		b.WriteString(route.Summary)
		b.WriteString("\n")
	}
	usage := route.Usage
	if len(usage) == 0 {
		usage = []string{"projmux " + strings.Join(path, " ")}
	}
	b.WriteString("\nUsage:\n")
	for _, line := range usage {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	// Aliases sit directly under Usage because they are spellings of the very
	// line above, not a separate capability. Every one of them reaches this same
	// node, so nothing else in this rendering changes between them.
	if len(route.Aliases) > 0 {
		b.WriteString("\nAliases:\n")
		for _, alias := range route.Aliases {
			fmt.Fprintf(&b, "  %s\n", alias)
		}
	}
	if len(route.Children) > 0 {
		width := nameColumnWidth
		for _, child := range route.Children {
			if len(childListingName(child))+2 > width {
				width = len(childListingName(child)) + 2
			}
		}
		// Provider shortcuts are spellings of `create agent --provider <id>`, not
		// resource kinds, so they get their own group rather than sitting in the
		// kind listing. Both groups share one column width so the two blocks line
		// up as a single table.
		writeChildGroup(&b, "Subcommands", route.Children, width, false)
		writeChildGroup(&b, "Provider shortcuts", route.Children, width, true)
	}
	if len(route.Outputs) > 0 {
		b.WriteString("\nOutput modes:\n")
		for _, mode := range route.Outputs {
			fmt.Fprintf(&b, "  %s\n", mode)
		}
	}
	if len(route.Fields) > 0 {
		b.WriteString("\nField projections:\n")
		for _, field := range route.Fields {
			fmt.Fprintf(&b, "  %s\n", field)
		}
	}
	// Migration guidance belongs in help, not in normal execution output. A
	// canonical spelling identical to the current path adds nothing.
	current := strings.Join(path, " ")
	var canonical []string
	for _, spelling := range route.Canonical {
		if spelling != current {
			canonical = append(canonical, spelling)
		}
	}
	if len(canonical) > 0 {
		b.WriteString("\nCanonical route:\n")
		for _, spelling := range canonical {
			fmt.Fprintf(&b, "  projmux %s\n", spelling)
		}
	}
	b.WriteString("\nRun 'projmux help' for the full command list.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// writeChildGroup writes one titled child listing, skipping the group entirely
// when no child belongs to it.
func writeChildGroup(b *strings.Builder, title string, children []Route, width int, shortcuts bool) {
	var group []Route
	for _, child := range children {
		if child.ProviderShortcut == shortcuts {
			group = append(group, child)
		}
	}
	if len(group) == 0 {
		return
	}
	b.WriteString("\n")
	b.WriteString(title)
	b.WriteString(":\n")
	for _, child := range group {
		name := childListingName(child)
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(strings.Repeat(" ", width-len(name)))
		b.WriteString(child.Summary)
		b.WriteString("\n")
	}
}

// childListingName renders one child's accepted spellings for the subcommand
// listing: the canonical name, then its aliases, joined with `|`.
//
// The listing is where an operator finds out a spelling exists at all, so an
// accepted one that only appears in an error message is effectively hidden. The
// summary column stays a single line per node because an alias is the same
// node -- `pane|panes` is one route described once, not two rows that would
// imply two behaviors.
func childListingName(child Route) string {
	if len(child.Aliases) == 0 {
		return child.Name
	}
	return strings.Join(append([]string{child.Name}, child.Aliases...), "|")
}

// padName pads a listing name to the shared help name column.
func padName(name string) string {
	if len(name) >= nameColumnWidth {
		return name + "  "
	}
	return name + strings.Repeat(" ", nameColumnWidth-len(name))
}
