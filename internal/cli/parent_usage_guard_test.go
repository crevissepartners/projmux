package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// parentUsageSummary declares one parent Usage line that names a child but is
// deliberately not a copy of any child line: it condenses several child forms
// into one row of the parent's synopsis. Every row is an exception to the
// parent-child line guard, so it must say why the summary reads better than
// the child's own lines.
type parentUsageSummary struct {
	parent string
	line   string
	reason string
}

// maxParentUsageSummaries caps the exception table. A new summary has to
// displace an existing one instead of growing the list, which keeps copying
// the child line the default.
const maxParentUsageSummaries = 6

// parentUsageSummaries are the only parent lines allowed to differ from the
// child they name.
var parentUsageSummaries = []parentUsageSummary{
	{
		parent: "config",
		line:   "projmux config providers [--enable <id>|--disable <id>]",
		reason: "Folds the child's separate enable and disable lines into one alternative.",
	},
	{
		parent: "config",
		line:   "projmux config locale [--set <value>]",
		reason: "Folds the child's read and set lines into one optional flag.",
	},
	{
		parent: "config",
		line:   "projmux config agent-questions [--answering <claude|projmux>] [--window <seconds|unlimited>]",
		reason: "Folds the child's read and per-setting write lines into one line of optional flags.",
	},
	{
		parent: "config",
		line:   "projmux config render standalone|app [--bin <path>]",
		reason: "Folds the child's one line per render target into a target alternative.",
	},
	{
		parent: "get",
		line:   "projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o wide|json|none]",
		reason: "Folds the three runtime kinds, which share one flag set, into one kind alternative.",
	},
	{
		parent: "get",
		line:   "projmux get pane --current -o cwd",
		reason: "Names the one projection a caller reaches for, without the rest of the child's flags.",
	},
}

// publicRouteNode is one non-hidden route outside the internal namespace,
// with its full argv path.
type publicRouteNode struct {
	path  []string
	route Route
}

// publicRouteNodes walks the public tree. A hidden route hides its whole
// subtree, and the internal namespace is never public.
func publicRouteNodes(nodes []Route) []publicRouteNode {
	var out []publicRouteNode
	var walk func(prefix []string, nodes []Route)
	walk = func(prefix []string, nodes []Route) {
		for _, node := range nodes {
			if node.Hidden || (len(prefix) == 0 && node.Name == "internal") {
				continue
			}
			path := append(append([]string{}, prefix...), node.Name)
			out = append(out, publicRouteNode{path: path, route: node})
			walk(path, node.Children)
		}
	}
	walk(nil, nodes)
	return out
}

// effectiveUsage is what help prints: the declared lines, or the synthesized
// `projmux <path>` line when none are declared.
func effectiveUsage(path []string, route Route) []string {
	if len(route.Usage) == 0 {
		return []string{"projmux " + strings.Join(path, " ")}
	}
	return route.Usage
}

// namedUsage is one child a parent Usage line names, with the line as it
// reads for that child alone.
type namedUsage struct {
	child Route
	line  string
}

// namedChildren returns the non-hidden children a parent Usage line names:
// the line starts with `projmux <parent path> <token>` followed by a space or
// the end of the line, and token is a child's Name or one of its Aliases, or
// an alternation `a|b|...` of them. An alternation names every child it
// spells, each with the line expanded to `projmux <parent path> <a> <rest>`.
//
// An alternation with nothing after it is a verb menu: it promises no flags or
// operands, and its verb set is already held to the dispatchers by
// internal/app TestCatalogRouteVerbAlternationsMatchDispatchers, so it names
// no child here. An alternation with any non-child alternative is a leaf's
// own verb or flag choice, which that guard judges too.
func namedChildren(parentPath []string, parent Route, line string) ([]namedUsage, bool) {
	prefix := "projmux " + strings.Join(parentPath, " ") + " "
	rest, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return nil, false
	}
	token, tail, more := strings.Cut(rest, " ")
	alternatives := strings.Split(token, "|")
	if len(alternatives) > 1 && !more {
		return nil, false
	}
	var named []namedUsage
	for _, alternative := range alternatives {
		child, ok := publicChild(parent, alternative)
		if !ok {
			return nil, false
		}
		expanded := line
		if len(alternatives) > 1 {
			expanded = prefix + alternative + " " + tail
		}
		named = append(named, namedUsage{child: child, line: expanded})
	}
	return named, true
}

// publicChild returns the non-hidden child whose Name or one of whose Aliases
// is token.
func publicChild(parent Route, token string) (Route, bool) {
	for _, child := range parent.Children {
		if child.Hidden {
			continue
		}
		if child.Name == token || slices.Contains(child.Aliases, token) {
			return child, true
		}
	}
	return Route{}, false
}

// parentChildUsageViolations reports every parent Usage line that names a
// child without being byte-identical to one of that child's lines (for an
// alternation, every expansion must be), and every summary row that no longer
// matches the tree it excuses.
func parentChildUsageViolations(routes []Route, summaries []parentUsageSummary) []string {
	var violations []string
	if len(summaries) > maxParentUsageSummaries {
		violations = append(violations, fmt.Sprintf("summary table has %d rows, cap is %d", len(summaries), maxParentUsageSummaries))
	}
	excused := map[string]bool{}
	for _, row := range summaries {
		if strings.TrimSpace(row.reason) == "" {
			violations = append(violations, fmt.Sprintf("summary row %q under %q has no reason", row.line, row.parent))
		}
		excused[row.parent+"\x00"+row.line] = true
	}

	nodes := publicRouteNodes(routes)
	byPath := map[string]publicRouteNode{}
	for _, node := range nodes {
		byPath[strings.Join(node.path, " ")] = node
	}

	for _, node := range nodes {
		parent := strings.Join(node.path, " ")
		for _, line := range node.route.Usage {
			named, ok := namedChildren(node.path, node.route, line)
			if !ok || excused[parent+"\x00"+line] {
				continue
			}
			for _, n := range named {
				childPath := append(append([]string{}, node.path...), n.child.Name)
				if slices.Contains(effectiveUsage(childPath, n.child), n.line) {
					continue
				}
				if n.line == line {
					violations = append(violations, fmt.Sprintf("%s usage %q differs from every %s usage line %q",
						parent, line, strings.Join(childPath, " "), effectiveUsage(childPath, n.child)))
					continue
				}
				violations = append(violations, fmt.Sprintf("%s usage %q expands to %q, which differs from every %s usage line %q",
					parent, line, n.line, strings.Join(childPath, " "), effectiveUsage(childPath, n.child)))
			}
		}
	}

	for _, row := range summaries {
		node, ok := byPath[row.parent]
		if !ok {
			violations = append(violations, fmt.Sprintf("stale summary row %q: parent %q is not a public route", row.line, row.parent))
			continue
		}
		if !slices.Contains(node.route.Usage, row.line) {
			violations = append(violations, fmt.Sprintf("stale summary row %q: not a %s usage line", row.line, row.parent))
			continue
		}
		named, ok := namedChildren(node.path, node.route, row.line)
		if !ok {
			violations = append(violations, fmt.Sprintf("stale summary row %q: names no public child of %s", row.line, row.parent))
			continue
		}
		// A row excuses the whole line, so it is stale once every child it
		// names already carries its expansion verbatim.
		var childPaths []string
		copied := true
		for _, n := range named {
			childPath := append(append([]string{}, node.path...), n.child.Name)
			childPaths = append(childPaths, strings.Join(childPath, " "))
			copied = copied && slices.Contains(effectiveUsage(childPath, n.child), n.line)
		}
		if copied {
			violations = append(violations, fmt.Sprintf("stale summary row %q: already equals a %s usage line", row.line, strings.Join(childPaths, " / ")))
		}
	}
	return violations
}

// emptyLeafUsageViolations reports every public leaf that declares no Usage,
// so help would fall back to a bare `projmux <path>` that hides its flags.
func emptyLeafUsageViolations(routes []Route) []string {
	var violations []string
	for _, node := range publicRouteNodes(routes) {
		if len(node.route.Children) == 0 && len(node.route.Usage) == 0 {
			violations = append(violations, fmt.Sprintf("public leaf %q declares no usage", strings.Join(node.path, " ")))
		}
	}
	return violations
}

// TestParentUsageLinesCopyTheChildLine pins every parent synopsis line that
// names a child to a verbatim child line, so a parent can never promise fewer
// (or different) flags than the child's own help.
func TestParentUsageLinesCopyTheChildLine(t *testing.T) {
	t.Parallel()

	for _, violation := range parentChildUsageViolations(Routes(), parentUsageSummaries) {
		t.Error(violation)
	}
}

// TestPublicLeafRoutesDeclareUsage keeps every public leaf's flags visible in
// help instead of relying on the synthesized bare path.
func TestPublicLeafRoutesDeclareUsage(t *testing.T) {
	t.Parallel()

	for _, violation := range emptyLeafUsageViolations(Routes()) {
		t.Error(violation)
	}
}

// guardTestTree is a small synthetic tree for the negative controls: one
// parent with an aliased child and one public leaf, plus a hidden child and
// the internal namespace that the guards must ignore.
func guardTestTree() []Route {
	return []Route{
		{
			Name:  "verb",
			Usage: []string{"projmux verb thing [--flag <value>]"},
			Children: []Route{
				{Name: "thing", Aliases: []string{"things"}, Usage: []string{"projmux verb thing [--flag <value>]"}},
				{Name: "secret", Hidden: true},
			},
		},
		{Name: "leaf", Usage: []string{"projmux leaf [--json]"}},
		{Name: "internal", Hidden: true, Children: []Route{{Name: "payload"}}},
	}
}

func TestParentUsageGuardAcceptsTheSyntheticTree(t *testing.T) {
	t.Parallel()

	if got := parentChildUsageViolations(guardTestTree(), nil); len(got) != 0 {
		t.Fatalf("parent-child guard rejected a consistent tree: %q", got)
	}
	if got := emptyLeafUsageViolations(guardTestTree()); len(got) != 0 {
		t.Fatalf("empty-usage guard rejected a consistent tree: %q", got)
	}
}

func TestParentUsageGuardReportsADifferingParentLine(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"projmux verb thing", "projmux verb things [--flag <value>]"} {
		tree := guardTestTree()
		tree[0].Usage = []string{line}
		got := parentChildUsageViolations(tree, nil)
		if len(got) != 1 || !strings.Contains(got[0], line) {
			t.Errorf("parent line %q: want one violation naming it, got %q", line, got)
		}
	}

	// The same mutation on the real catalog must fail too, so the guard is
	// proven against the tree it actually protects. Routes clones the nodes
	// but shares their Usage arrays, so the mutation replaces the slice
	// instead of writing through it into the package catalog.
	routes := Routes()
	mutated := false
	for i := range routes {
		if routes[i].Name != "create" {
			continue
		}
		usage := slices.Clone(routes[i].Usage)
		for j, line := range usage {
			if strings.HasPrefix(line, "projmux create pane ") {
				usage[j] = "projmux create pane"
				mutated = true
			}
		}
		routes[i].Usage = usage
	}
	if !mutated {
		t.Fatal("create has no `create pane` usage line to mutate")
	}
	got := parentChildUsageViolations(routes, parentUsageSummaries)
	if len(got) != 1 || !strings.Contains(got[0], `"projmux create pane"`) {
		t.Fatalf("mutated create pane line: want one violation, got %q", got)
	}
}

func TestParentUsageGuardReportsStaleSummaryRows(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func([]Route)
		row    parentUsageSummary
		want   string
	}{
		"line absent from parent": {
			row:  parentUsageSummary{parent: "verb", line: "projmux verb thing --gone", reason: "x"},
			want: "not a verb usage line",
		},
		"line equals a child line": {
			row:  parentUsageSummary{parent: "verb", line: "projmux verb thing [--flag <value>]", reason: "x"},
			want: "already equals",
		},
		"child removed": {
			mutate: func(tree []Route) {
				tree[0].Usage = append(tree[0].Usage, "projmux verb gone [--x]")
			},
			row:  parentUsageSummary{parent: "verb", line: "projmux verb gone [--x]", reason: "x"},
			want: "names no public child",
		},
		"parent removed": {
			row:  parentUsageSummary{parent: "nope", line: "projmux nope thing", reason: "x"},
			want: "is not a public route",
		},
		"reason missing": {
			mutate: func(tree []Route) {
				tree[0].Usage = append(tree[0].Usage, "projmux verb thing ...")
			},
			row:  parentUsageSummary{parent: "verb", line: "projmux verb thing ...", reason: " "},
			want: "has no reason",
		},
	}
	for name, tc := range cases {
		tree := guardTestTree()
		if tc.mutate != nil {
			tc.mutate(tree)
		}
		got := parentChildUsageViolations(tree, []parentUsageSummary{tc.row})
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: want one violation containing %q, got %q", name, tc.want, got)
		}
	}

	rows := make([]parentUsageSummary, maxParentUsageSummaries+1)
	for i := range rows {
		rows[i] = parentUsageSummary{parent: "verb", line: "projmux verb thing ...", reason: "x"}
	}
	tree := guardTestTree()
	tree[0].Usage = append(tree[0].Usage, "projmux verb thing ...")
	if got := parentChildUsageViolations(tree, rows); len(got) == 0 || !strings.Contains(got[0], "cap is") {
		t.Errorf("oversized summary table: want the cap violation first, got %q", got)
	}
}

// alternationGuardTestTree is a synthetic parent whose children b and c
// disagree on flags, so an alternation line can be consistent for one pair and
// narrower than a child for another.
func alternationGuardTestTree(line string) []Route {
	return []Route{{
		Name:  "pick",
		Usage: []string{line},
		Children: []Route{
			{Name: "a", Usage: []string{"projmux pick a [--x]"}},
			{Name: "b", Usage: []string{"projmux pick b [--x] [--y]"}},
			{Name: "c", Usage: []string{"projmux pick c [--x]"}},
		},
	}}
}

func TestParentUsageGuardExpandsChildAlternations(t *testing.T) {
	t.Parallel()

	got := parentChildUsageViolations(alternationGuardTestTree("projmux pick a|b [--x]"), nil)
	if len(got) != 1 || !strings.Contains(got[0], `"projmux pick a|b [--x]" expands to "projmux pick b [--x]"`) {
		t.Errorf("narrow alternation: want one violation naming the b expansion, got %q", got)
	}

	for _, line := range []string{
		"projmux pick a|c [--x]",    // every expansion is a child line
		"projmux pick a|b|c",        // verb menu: no flags or operands promised
		"projmux pick a|nope [--x]", // not all children: the verb guard's line
	} {
		if got := parentChildUsageViolations(alternationGuardTestTree(line), nil); len(got) != 0 {
			t.Errorf("parent line %q: want no violation, got %q", line, got)
		}
	}

	// A summary row excuses the alternation line as a whole, and goes stale
	// once every expansion is a child line.
	excuse := parentUsageSummary{parent: "pick", line: "projmux pick a|b [--x]", reason: "x"}
	if got := parentChildUsageViolations(alternationGuardTestTree(excuse.line), []parentUsageSummary{excuse}); len(got) != 0 {
		t.Errorf("excused alternation: want no violation, got %q", got)
	}
	stale := parentUsageSummary{parent: "pick", line: "projmux pick a|c [--x]", reason: "x"}
	got = parentChildUsageViolations(alternationGuardTestTree(stale.line), []parentUsageSummary{stale})
	if len(got) != 1 || !strings.Contains(got[0], "already equals a pick a / pick c usage line") {
		t.Errorf("stale alternation row: want one violation, got %q", got)
	}
	menu := parentUsageSummary{parent: "pick", line: "projmux pick a|b", reason: "x"}
	got = parentChildUsageViolations(alternationGuardTestTree(menu.line), []parentUsageSummary{menu})
	if len(got) != 1 || !strings.Contains(got[0], "names no public child") {
		t.Errorf("menu row: want one stale violation, got %q", got)
	}

	// The narrow shortcut line create used to print must fail against the
	// real catalog for both providers. The mutation replaces the Usage slice
	// instead of writing through it into the package catalog.
	const narrow = "projmux create claude|antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]"
	routes := Routes()
	mutated := false
	for i := range routes {
		if routes[i].Name != "create" {
			continue
		}
		var usage []string
		for _, line := range routes[i].Usage {
			switch {
			case strings.HasPrefix(line, "projmux create claude "):
				usage = append(usage, narrow)
				mutated = true
			case strings.HasPrefix(line, "projmux create antigravity "):
			default:
				usage = append(usage, line)
			}
		}
		routes[i].Usage = usage
	}
	if !mutated {
		t.Fatal("create has no `create claude` usage line to replace")
	}
	got = parentChildUsageViolations(routes, parentUsageSummaries)
	if len(got) != 2 ||
		!strings.Contains(got[0], `expands to "projmux create claude [--project`) ||
		!strings.Contains(got[1], `expands to "projmux create antigravity [--project`) {
		t.Fatalf("narrow create shortcut line: want one violation per provider, got %q", got)
	}

	// agent turn's alternation carries operands and matches both children.
	for _, node := range publicRouteNodes(Routes()) {
		if strings.Join(node.path, " ") != "agent turn" {
			continue
		}
		const line = "projmux agent turn start|steer <agent-ref> -- <text>"
		if !slices.Contains(node.route.Usage, line) {
			t.Fatalf("agent turn usage lacks %q: %q", line, node.route.Usage)
		}
		named, ok := namedChildren(node.path, node.route, line)
		if !ok || len(named) != 2 {
			t.Fatalf("agent turn line names %d children, want 2", len(named))
		}
		for _, n := range named {
			childPath := append(append([]string{}, node.path...), n.child.Name)
			if !slices.Contains(effectiveUsage(childPath, n.child), n.line) {
				t.Errorf("agent turn expansion %q is not a %s usage line", n.line, strings.Join(childPath, " "))
			}
		}
		return
	}
	t.Fatal("agent turn is not a public route")
}

func TestEmptyUsageGuardReportsAPublicLeafWithoutUsage(t *testing.T) {
	t.Parallel()

	tree := guardTestTree()
	tree[1].Usage = nil
	got := emptyLeafUsageViolations(tree)
	if len(got) != 1 || !strings.Contains(got[0], `"leaf"`) {
		t.Fatalf("blank leaf usage: want one violation naming leaf, got %q", got)
	}

	// Hidden leaves and the internal namespace stay out of scope.
	tree = guardTestTree()
	tree[0].Children[1].Usage = nil
	tree[2].Children[0].Usage = nil
	if got := emptyLeafUsageViolations(tree); len(got) != 0 {
		t.Fatalf("hidden or internal leaves were reported: %q", got)
	}
}
