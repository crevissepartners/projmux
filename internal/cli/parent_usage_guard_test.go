package cli

import (
	"fmt"
	"regexp"
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

// parentChildCoverage counts what parentChildCoverageViolations checked.
type parentChildCoverage struct {
	parents, copied, summarized int
}

// parentChildCoverageViolations reports every public child that no line of
// its public parent's Usage names with a verbatim copy of one of the child's
// own lines. A line a summary row excuses covers every child it names, since
// the row already says why the parent condenses that child.
func parentChildCoverageViolations(routes []Route, summaries []parentUsageSummary) ([]string, parentChildCoverage) {
	excused := map[string]bool{}
	for _, row := range summaries {
		excused[row.parent+"\x00"+row.line] = true
	}
	var violations []string
	var counts parentChildCoverage
	for _, node := range publicRouteNodes(routes) {
		var children []string
		for _, child := range node.route.Children {
			if !child.Hidden {
				children = append(children, child.Name)
			}
		}
		if len(children) == 0 {
			continue
		}
		counts.parents++
		parent := strings.Join(node.path, " ")
		copied, summarized := map[string]bool{}, map[string]bool{}
		for _, line := range node.route.Usage {
			named, ok := namedChildren(node.path, node.route, line)
			if !ok {
				continue
			}
			for _, n := range named {
				childPath := append(append([]string{}, node.path...), n.child.Name)
				switch {
				case slices.Contains(effectiveUsage(childPath, n.child), n.line):
					copied[n.child.Name] = true
				case excused[parent+"\x00"+line]:
					summarized[n.child.Name] = true
				}
			}
		}
		for _, name := range children {
			switch {
			case copied[name]:
				counts.copied++
			case summarized[name]:
				counts.summarized++
			default:
				violations = append(violations, fmt.Sprintf("%s usage names no copy of any %s usage line", parent, parent+" "+name))
			}
		}
	}
	return violations, counts
}

// parentVerbMenu declares one public Usage line that stays a bare verb menu,
// `projmux <path> a|b|c` with nothing after the alternation.
type parentVerbMenu struct {
	route  string
	line   string
	reason string
}

// parentVerbMenus are the only bare verb menus a public route may print. A
// menu promises no flags or operands, so a route whose verbs are children
// prints each child's line instead.
var parentVerbMenus = []parentVerbMenu{
	{
		route:  "pin project",
		line:   "projmux pin project list|add|remove|toggle|clear|migrate",
		reason: "Its verbs have no catalog child nodes yet, so there is no child line to copy; remove this row when the verbs become children.",
	},
	{
		route:  "runtime tag",
		line:   "projmux runtime tag list|clear",
		reason: "Its verbs have no catalog child nodes yet, so there is no child line to copy; remove this row when the verbs become children.",
	},
}

// verbMenu matches an alternation of two or more bare verbs, so a bracketed
// flag choice such as `[--yes|--force]` is not one.
var verbMenu = regexp.MustCompile(`^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)+$`)

// isVerbMenu reports whether line is a bare verb menu at path: the rest of the
// line after `projmux <path> ` is one verb alternation and nothing else, the
// case namedChildren leaves to the verb guard.
func isVerbMenu(path []string, line string) bool {
	rest, ok := strings.CutPrefix(line, "projmux "+strings.Join(path, " ")+" ")
	return ok && verbMenu.MatchString(rest)
}

// verbMenuViolations reports every bare verb menu a public route prints
// without a row, and every row that no longer matches the tree it excuses.
func verbMenuViolations(routes []Route, menus []parentVerbMenu) []string {
	excused := map[string]bool{}
	for _, row := range menus {
		excused[row.route+"\x00"+row.line] = true
	}
	var violations []string
	byPath := map[string]publicRouteNode{}
	for _, node := range publicRouteNodes(routes) {
		path := strings.Join(node.path, " ")
		byPath[path] = node
		for _, line := range node.route.Usage {
			if isVerbMenu(node.path, line) && !excused[path+"\x00"+line] {
				violations = append(violations, fmt.Sprintf("%s usage %q is a bare verb menu; print each child's usage line instead", path, line))
			}
		}
	}
	for _, row := range menus {
		if strings.TrimSpace(row.reason) == "" {
			violations = append(violations, fmt.Sprintf("verb menu row %q under %q has no reason", row.line, row.route))
		}
		node, ok := byPath[row.route]
		if !ok {
			violations = append(violations, fmt.Sprintf("stale verb menu row %q: route %q is not a public route", row.line, row.route))
			continue
		}
		if !slices.Contains(node.route.Usage, row.line) {
			violations = append(violations, fmt.Sprintf("stale verb menu row %q: not a %s usage line", row.line, row.route))
			continue
		}
		if !isVerbMenu(node.path, row.line) {
			violations = append(violations, fmt.Sprintf("stale verb menu row %q: not a bare verb menu of %s", row.line, row.route))
			continue
		}
		for _, child := range node.route.Children {
			if !child.Hidden {
				violations = append(violations, fmt.Sprintf("stale verb menu row %q: %s has public children now, so its usage must copy their lines", row.line, row.route))
				break
			}
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

// TestParentUsageCoversEveryPublicChild makes every public parent's help list
// each public child with that child's own line, so a reader of the parent
// sees every child's flags and operands without opening the child's help.
func TestParentUsageCoversEveryPublicChild(t *testing.T) {
	t.Parallel()

	violations, counts := parentChildCoverageViolations(Routes(), parentUsageSummaries)
	for _, violation := range violations {
		t.Error(violation)
	}
	t.Logf("checked %d public parents: %d children covered by a copied line, %d by a summary row", counts.parents, counts.copied, counts.summarized)
}

// TestPublicUsageHasNoBareVerbMenu keeps a bare `a|b|c` verb menu out of
// public help except where the verbs have no child line to copy yet.
func TestPublicUsageHasNoBareVerbMenu(t *testing.T) {
	t.Parallel()

	for _, violation := range verbMenuViolations(Routes(), parentVerbMenus) {
		t.Error(violation)
	}
	t.Logf("%d verb menu rows", len(parentVerbMenus))
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

// withTopLevelUsage returns Routes() with the named top-level route's Usage
// replaced by edit's result. Routes shares Usage arrays with the package
// catalog, so edit receives a clone and the slice is replaced, never written
// through.
func withTopLevelUsage(t *testing.T, name string, edit func([]string) []string) []Route {
	t.Helper()
	routes := Routes()
	for i := range routes {
		if routes[i].Name == name {
			routes[i].Usage = edit(slices.Clone(routes[i].Usage))
			return routes
		}
	}
	t.Fatalf("no top-level route %q", name)
	return nil
}

func TestParentCoverageGuardReportsAnUncoveredChild(t *testing.T) {
	t.Parallel()

	// The synthetic tree is covered; its hidden child and the internal
	// namespace name no line and are not reported.
	if got, _ := parentChildCoverageViolations(guardTestTree(), nil); len(got) != 0 {
		t.Fatalf("coverage guard rejected a covered tree: %q", got)
	}

	tree := guardTestTree()
	tree[0].Children = append(slices.Clone(tree[0].Children), Route{Name: "other", Usage: []string{"projmux verb other [--x]"}})
	got, _ := parentChildCoverageViolations(tree, nil)
	if len(got) != 1 || !strings.Contains(got[0], "verb usage names no copy of any verb other usage line") {
		t.Errorf("uncovered child: want one violation naming verb and verb other, got %q", got)
	}

	// A differing line covers its child only through a summary row.
	summary := parentUsageSummary{parent: "verb", line: "projmux verb thing ...", reason: "x"}
	tree = guardTestTree()
	tree[0].Usage = []string{summary.line}
	if got, _ := parentChildCoverageViolations(tree, nil); len(got) != 1 || !strings.Contains(got[0], "verb thing") {
		t.Errorf("differing line without a row: want one violation naming verb thing, got %q", got)
	}
	got, counts := parentChildCoverageViolations(tree, []parentUsageSummary{summary})
	if len(got) != 0 || counts != (parentChildCoverage{parents: 1, summarized: 1}) {
		t.Errorf("summary-covered child: want no violation and one summarized child, got %q %+v", got, counts)
	}

	// The same omission on the real catalog, one parent at a time.
	for _, tc := range []struct {
		parent, child string
		drop          func(string) bool
	}{
		{"attention", "attention toggle", func(line string) bool { return strings.HasPrefix(line, "projmux attention toggle ") }},
		{"get", "get notifications", func(line string) bool { return strings.HasPrefix(line, "projmux get notifications ") }},
		{"agent", "agent instructions", func(line string) bool { return strings.HasPrefix(line, "projmux agent instructions ") }},
	} {
		routes := withTopLevelUsage(t, tc.parent, func(usage []string) []string {
			before := len(usage)
			kept := slices.DeleteFunc(usage, tc.drop)
			if len(kept) == before {
				t.Fatalf("%s has no %s usage line to drop", tc.parent, tc.child)
			}
			return kept
		})
		want := tc.parent + " usage names no copy of any " + tc.child + " usage line"
		if got, _ := parentChildCoverageViolations(routes, parentUsageSummaries); len(got) != 1 || got[0] != want {
			t.Errorf("dropped %s line: want exactly %q, got %q", tc.child, want, got)
		}
	}
}

func TestVerbMenuGuardReportsMenusAndStaleRows(t *testing.T) {
	t.Parallel()

	menuTree := func() []Route {
		tree := guardTestTree()
		tree[1].Usage = []string{"projmux leaf a|b"}
		return tree
	}
	row := parentVerbMenu{route: "leaf", line: "projmux leaf a|b", reason: "x"}

	if got := verbMenuViolations(guardTestTree(), nil); len(got) != 0 {
		t.Fatalf("menu guard rejected a tree without menus: %q", got)
	}
	if got := verbMenuViolations(menuTree(), nil); len(got) != 1 || !strings.Contains(got[0], `leaf usage "projmux leaf a|b" is a bare verb menu`) {
		t.Errorf("unexcused menu: want one violation, got %q", got)
	}
	if got := verbMenuViolations(menuTree(), []parentVerbMenu{row}); len(got) != 0 {
		t.Errorf("excused menu: want no violation, got %q", got)
	}

	// A flag choice is not a verb menu, and hidden or internal routes are out
	// of scope.
	tree := guardTestTree()
	tree[1].Usage = []string{"projmux leaf [--yes|--force]"}
	tree[0].Children[1].Usage = []string{"projmux verb secret a|b"}
	tree[2].Usage = []string{"projmux internal a|b"}
	if got := verbMenuViolations(tree, nil); len(got) != 0 {
		t.Errorf("flag choice, hidden, or internal menu reported: %q", got)
	}

	cases := map[string]struct {
		mutate func([]Route)
		row    parentVerbMenu
		want   string
	}{
		"route not public": {
			row:  parentVerbMenu{route: "nope", line: "projmux nope a|b", reason: "x"},
			want: `route "nope" is not a public route`,
		},
		"line absent from route": {
			row:  parentVerbMenu{route: "leaf", line: "projmux leaf c|d", reason: "x"},
			want: "not a leaf usage line",
		},
		"line not a menu": {
			row:  parentVerbMenu{route: "leaf", line: "projmux leaf [--json]", reason: "x"},
			want: "not a bare verb menu of leaf",
		},
		"reason missing": {
			mutate: func(tree []Route) { tree[1].Usage = []string{"projmux leaf a|b"} },
			row:    parentVerbMenu{route: "leaf", line: "projmux leaf a|b", reason: " "},
			want:   "has no reason",
		},
		"route has public children": {
			mutate: func(tree []Route) { tree[0].Usage = append(slices.Clone(tree[0].Usage), "projmux verb thing|things") },
			row:    parentVerbMenu{route: "verb", line: "projmux verb thing|things", reason: "x"},
			want:   "verb has public children now",
		},
	}
	for name, tc := range cases {
		tree := guardTestTree()
		if tc.mutate != nil {
			tc.mutate(tree)
		}
		got := verbMenuViolations(tree, []parentVerbMenu{tc.row})
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: want one violation containing %q, got %q", name, tc.want, got)
		}
	}

	// The menu update used to print must fail against the real catalog.
	const menu = "projmux update status|check|apply"
	routes := withTopLevelUsage(t, "update", func(usage []string) []string { return append([]string{menu}, usage...) })
	got := verbMenuViolations(routes, parentVerbMenus)
	if len(got) != 1 || !strings.Contains(got[0], `update usage "`+menu+`" is a bare verb menu`) {
		t.Fatalf("re-inserted update menu: want one violation, got %q", got)
	}
}
