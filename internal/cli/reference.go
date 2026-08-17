package cli

import (
	"fmt"
	"io"
	"strings"
)

// The generated CLI reference.
//
// The compatibility contract makes the command manifest the common source of
// runtime help and the published reference. The manifest that runtime help
// actually renders is `routes` -- the Cobra command tree in catalog.go -- so
// this renderer walks that tree and nothing else.
//
// It deliberately does not read `canonicalRoutes`. That manifest audits
// executable canonical spellings and their source namespaces, while `routes`
// owns user-visible summaries, usage, disposition, and help structure. Keeping
// the renderer on `routes` prevents internal spellings and audit-only metadata
// from leaking into public help; tests enforce the boundary. See the header
// comment in canonical.go.
//
// Every byte this renderer emits is a pure function of the manifest: no
// timestamp, no version string, no host path, no map iteration. Regenerating on
// a clean tree is a no-op.

// ReferencePath is the repository-relative path of the generated reference.
const ReferencePath = "docs/cli.md"

// ReferenceRegenCommand is the exact command that regenerates the reference. It
// is rendered into the DO NOT EDIT header and into the drift-test failure
// message, so the two can never disagree.
const ReferenceRegenCommand = "make docs"

// referenceGuidePath is the hand-maintained companion, linked from the
// generated page so the split surface stays reachable from either half.
const referenceGuidePath = "cli-guide.md"

// referenceHeading is the markdown heading prefix for a route at depth.
// Top-level routes are `##` so the page keeps a single `#` title.
func referenceHeading(depth int) string {
	level := min(depth+2, 6)
	return strings.Repeat("#", level)
}

// referenceAnchor reproduces the GitHub heading slug for a route path so the
// index table links resolve inside the rendered page.
func referenceAnchor(path []string) string {
	return "#projmux-" + strings.Join(path, "-")
}

// referenceCell escapes a manifest string for a markdown table cell.
func referenceCell(text string) string {
	return strings.ReplaceAll(text, "|", "\\|")
}

// referenceSpelling renders one route path as an inline code command.
func referenceSpelling(path []string) string {
	return "`projmux " + strings.Join(path, " ") + "`"
}

// publicRoutes returns the top-level routes the reference publishes: every
// non-hidden node, in manifest order.
//
// Hidden routes are excluded outright. The internal isolation Phase took the
// plumbing namespace out of the primary help listing, and the published
// reference holds the same line: a route a user is never meant to type is not
// part of the public surface just because a document could enumerate it.
func publicRoutes() []Route {
	out := make([]Route, 0, len(routes))
	for _, route := range routes {
		if route.Hidden {
			continue
		}
		out = append(out, route)
	}
	return out
}

// executableCanonicalSpellings filters a route's canonical spellings down to the
// ones argv can actually reach today, dropping the spelling identical to the
// route being documented.
//
// The filter is what keeps acceptance honest. A canonical spelling such as
// `config apply` is a contract commitment with no route behind it: printing it
// as a "canonical route" in a published reference would advertise a command
// that answers `unknown command`. Runtime help carries the migration hint for
// the operator who is already at a prompt; the reference documents what the
// binary does.
func executableCanonicalSpellings(path []string, route Route) []string {
	current := strings.Join(path, " ")
	var out []string
	for _, spelling := range route.Canonical {
		if spelling == current {
			continue
		}
		if !isExecutableSpelling(spelling) {
			continue
		}
		out = append(out, spelling)
	}
	return out
}

// isExecutableSpelling reports whether every token of spelling resolves against
// the command tree and lands inside the public surface, so `projmux <spelling>`
// reaches a real node a user is meant to type.
//
// The hidden half of the test matters as much as the resolvable half: an
// internal route can resolve perfectly well while remaining plumbing.
// Publishing it as a canonical spelling would put an internal route back into
// the public reference through the side door the internal isolation Phase
// closed in the primary listing.
func isExecutableSpelling(spelling string) bool {
	tokens := strings.Fields(spelling)
	if len(tokens) == 0 {
		return false
	}
	top, ok := LookupRoute(tokens[0])
	if !ok || top.Hidden {
		return false
	}
	resolved, _, ok := Resolve(tokens)
	return ok && len(resolved) == len(tokens)
}

// RenderReference writes the complete generated CLI reference.
func RenderReference(w io.Writer) error {
	var b strings.Builder

	fmt.Fprintf(&b, "<!-- Code generated from the projmux command manifest (internal/cli/catalog.go). DO NOT EDIT. -->\n")
	fmt.Fprintf(&b, "<!-- Regenerate with: %s -->\n\n", ReferenceRegenCommand)

	b.WriteString("# CLI Reference\n\n")
	b.WriteString("This page is generated from the projmux command manifest, the same source the\n")
	b.WriteString("binary renders `projmux help` and `projmux <route> --help` from. Editing it by\n")
	fmt.Fprintf(&b, "hand is pointless: run `%s` instead, and a drift test fails the build when the\n", ReferenceRegenCommand)
	b.WriteString("checked-in page and the manifest disagree.\n\n")
	b.WriteString("It documents the routes that exist in this build and nothing else. Contract\n")
	b.WriteString("spellings a later release will introduce are absent on purpose, as is the\n")
	b.WriteString("hidden internal plumbing namespace, which is not part of the public surface.\n\n")
	fmt.Fprintf(&b, "Prose that a manifest cannot hold -- the help boundary contract, exit codes,\n")
	fmt.Fprintf(&b, "per-flag behavior, and task-oriented walkthroughs -- lives in the\n")
	fmt.Fprintf(&b, "[CLI Task Guide](%s).\n\n", referenceGuidePath)

	writeReferenceIndex(&b)

	for _, route := range publicRoutes() {
		writeReferenceRoute(&b, []string{route.Name}, route)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// writeReferenceIndex writes the top-level command table. The disposition
// column is what makes the coverage claim checkable by eye: every canonical and
// shortcut route the contract inventoried has a row here.
func writeReferenceIndex(b *strings.Builder) {
	b.WriteString("## Commands\n\n")
	b.WriteString("```\nprojmux <command> [args...]\n```\n\n")
	b.WriteString("| Command | Kind | Summary |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, route := range publicRoutes() {
		path := []string{route.Name}
		fmt.Fprintf(b, "| [%s](%s) | %s | %s |\n",
			referenceSpelling(path), referenceAnchor(path),
			referenceCell(string(route.Disposition)), referenceCell(route.Summary))
	}
	b.WriteString("\n")
}

// writeReferenceRoute writes one route section plus every descendant, depth
// first in manifest order.
func writeReferenceRoute(b *strings.Builder, path []string, route Route) {
	fmt.Fprintf(b, "%s %s\n\n", referenceHeading(len(path)-1), referenceSpelling(path))
	if route.Summary != "" {
		b.WriteString(route.Summary)
		b.WriteString("\n\n")
	}

	usage := route.Usage
	if len(usage) == 0 {
		usage = []string{"projmux " + strings.Join(path, " ")}
	}
	b.WriteString("```\n")
	for _, line := range usage {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")

	// Aliases are extra spellings of this same section, so they are stated here
	// rather than given sections of their own: a second heading would put a
	// second entry in the index for one route and imply two behaviors to
	// document.
	if len(route.Aliases) > 0 {
		b.WriteString("Aliases:")
		for i, alias := range route.Aliases {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " `%s`", alias)
		}
		b.WriteString("\n\n")
	}

	if len(route.Children) > 0 {
		writeReferenceChildGroup(b, path, "Subcommands", route.Children, false)
		writeReferenceChildGroup(b, path, "Provider shortcuts", route.Children, true)
	}

	if len(route.Outputs) > 0 {
		b.WriteString("Output modes (`-o`):")
		for i, mode := range route.Outputs {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " `%s`", mode)
		}
		b.WriteString("\n\n")
	}
	if len(route.Fields) > 0 {
		b.WriteString("Field projections (`-o`):")
		for i, field := range route.Fields {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " `%s`", field)
		}
		b.WriteString("\n\n")
	}

	if canonical := executableCanonicalSpellings(path, route); len(canonical) > 0 {
		b.WriteString("Canonical spelling:")
		for i, spelling := range canonical {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " `projmux %s`", spelling)
		}
		b.WriteString("\n\n")
	}

	for _, child := range route.Children {
		writeReferenceRoute(b, append(append([]string{}, path...), child.Name), child)
	}
}

// writeReferenceChildGroup writes one titled child table. Provider shortcuts are
// spellings of `create agent --provider <id>` rather than resource kinds, so
// they keep the separate group the help renderer gives them and are never
// counted among the kinds.
func writeReferenceChildGroup(b *strings.Builder, path []string, title string, children []Route, shortcuts bool) {
	var group []Route
	for _, child := range children {
		if child.ProviderShortcut == shortcuts {
			group = append(group, child)
		}
	}
	if len(group) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n\n", title)
	b.WriteString("| Route | Summary |\n")
	b.WriteString("| --- | --- |\n")
	for _, child := range group {
		childPath := append(append([]string{}, path...), child.Name)
		fmt.Fprintf(b, "| [%s](%s) | %s |\n",
			referenceSpelling(childPath), referenceAnchor(childPath), referenceCell(child.Summary))
	}
	b.WriteString("\n")
}
