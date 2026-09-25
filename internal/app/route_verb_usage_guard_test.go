package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// routeVerbDispatcher names the code that dispatches the verbs a catalog Usage
// line spells as an alternation (`a|b|c`, or a bracketed `[a … | b …]`) at one
// verb position. It takes one of three forms: a switch or `==` comparisons on
// tag, optionally widened by a loop over a ranged call, or a lookup table.
type routeVerbDispatcher struct {
	// file is repo-relative; fn is "Name" or "(*Recv).Name"; tag is the
	// printed expression whose unique switch, or failing that whose `==`
	// comparisons against string literals, names the verbs inside fn.
	file, fn, tag string
	// passThrough are case values that descend to a deeper verb position
	// rather than name a verb at this one (pin's `project`).
	passThrough []string
	// ranged is the printed range expression of a loop in fn that accepts
	// rangedVerbs() as further verbs (create's provider shortcuts).
	ranged      string
	rangedVerbs func() []string
	// table, when set, replaces tag: fn dispatches through lookups that accept
	// exactly table(), and must reference every printed expression in uses.
	table func() []string
	uses  []string
}

// routeVerbDispatchers is closed over the catalog: every qualified verb
// position must have exactly one entry here, and every entry must be one.
var routeVerbDispatchers = map[string]routeVerbDispatcher{
	"agent status":  {file: "internal/app/agent_interaction.go", fn: "(*agentCommand).runStatus", tag: "args[0]"},
	"agent topic":   {file: "internal/app/agent_interaction.go", fn: "(*agentCommand).runTopic", tag: "action"},
	"agent turn":    {file: "internal/app/agent_control.go", fn: "(*agentCommand).runTurn", tag: "args[0]"},
	"attention":     {file: "internal/app/attention.go", fn: "(*attentionCommand).Run", tag: "args[0]"},
	"config render": {file: "internal/app/config.go", fn: "(*configCommand).runRender", tag: "args[0]"},
	"create": {file: "internal/app/create.go", fn: "(*createCommand).Run", tag: "token",
		ranged: "cli.ProviderCreateShortcuts()", rangedVerbs: cli.ProviderCreateShortcuts},
	"get runtime": {file: "internal/app/get_runtime.go", fn: "(*getCommand).runRuntime",
		table: getRuntimeDispatchedKinds, uses: []string{"cli.CanonicalGrandchildToken", "runtimeKindTokens"}},
	"hook":        {file: "internal/app/hook.go", fn: "(*hookCommand).Run", tag: "fs.Arg(0)"},
	"pin project": {file: "internal/app/pin.go", fn: "(*pinCommand).Run", tag: "fs.Arg(0)", passThrough: []string{"project"}},
	"runtime tag": {file: "internal/app/tag.go", fn: "(*tagCommand).Run", tag: "fs.Arg(0)", passThrough: []string{"project"}},
	"update":      {file: "internal/app/update.go", fn: "(*updateCommand).Run", tag: "args[0]"},
	"window":      {file: "internal/app/recent_window.go", fn: "(*windowCommand).Run", tag: "fs.Arg(0)"},
}

// getRuntimeDispatchedKinds returns every token runRuntime's two lookups
// accept: the catalog must canonicalize it and runtimeKindTokens must map the
// canonical spelling.
func getRuntimeDispatchedKinds() []string {
	candidates := slices.Collect(maps.Keys(runtimeKindTokens))
	for _, spellings := range cli.GrandchildSpellings("get", "runtime") {
		candidates = append(candidates, strings.Split(spellings, "|")...)
	}
	var out []string
	for _, candidate := range candidates {
		canonical, ok := cli.CanonicalGrandchildToken("get", "runtime", candidate)
		if !ok {
			continue
		}
		if _, ok := runtimeKindTokens[canonical]; ok {
			out = append(out, candidate)
		}
	}
	return out
}

var routeVerbToken = regexp.MustCompile(`^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)*$`)

var routeVerbBranchLead = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var routeVerbHelpAliases = []string{"help", "--help", "-h"}

type routeVerbUsage struct {
	verbs   map[string]bool
	sources map[string]bool
}

type routeVerbUsageLine struct {
	run    []string
	source string
}

// usageVerbRun returns the leading run of bare verb tokens of one Usage line.
// A bracket group right after the run whose two or more top-level `|` branches
// all open with a bare verb adds one synthetic token of those verbs (`get|set`).
func usageVerbRun(usage string) []string {
	rest := strings.TrimPrefix(usage, "projmux ")
	var run []string
	for rest != "" {
		tok, after, _ := strings.Cut(rest, " ")
		if !routeVerbToken.MatchString(tok) {
			break
		}
		run = append(run, tok)
		rest = after
	}
	if !strings.HasPrefix(rest, "[") {
		return run
	}
	depth, end := 0, -1
	for i, r := range rest {
		switch r {
		case '[', '<', '{':
			depth++
		case ']', '>', '}':
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return run
	}
	var branches []string
	depth, start := 0, 1
	inner := rest[:end]
	for i := 1; i < len(inner); i++ {
		switch inner[i] {
		case '[', '<', '{':
			depth++
		case ']', '>', '}':
			depth--
		case '|':
			if depth == 0 {
				branches = append(branches, inner[start:i])
				start = i + 1
			}
		}
	}
	branches = append(branches, inner[start:])
	if len(branches) < 2 {
		return run
	}
	var leads []string
	for _, branch := range branches {
		fields := strings.Fields(branch)
		if len(fields) == 0 || !routeVerbBranchLead.MatchString(fields[0]) {
			return run
		}
		leads = append(leads, fields[0])
	}
	return append(run, strings.Join(leads, "|"))
}

// routeVerbPinnedPositions are verb positions that stay qualified although no
// line spells them as an alternation. create's Usage copies each child's line
// verbatim (internal/cli TestParentUsageLinesCopyTheChildLine), so its kinds
// and provider shortcuts are only ever spelled one per line.
var routeVerbPinnedPositions = []string{"create"}

// routeVerbPositions returns, per qualified verb position, the verbs the lines
// spell there and the routes those lines belong to.
func routeVerbPositions(lines []routeVerbUsageLine, pinned ...string) map[string]*routeVerbUsage {
	// A position is qualified by any alternation token in a run, or by a pin.
	positions := map[string]*routeVerbUsage{}
	for _, position := range pinned {
		positions[position] = &routeVerbUsage{verbs: map[string]bool{}, sources: map[string]bool{}}
	}
	for _, line := range lines {
		for i, tok := range line.run {
			if strings.Contains(tok, "|") {
				positions[strings.Join(line.run[:i], " ")] = &routeVerbUsage{verbs: map[string]bool{}, sources: map[string]bool{}}
			}
		}
	}
	// Its verb set is the union of the next run token over every line that
	// reaches past it, alternation or not.
	for position, usage := range positions {
		prefix := strings.Fields(position)
		for _, line := range lines {
			if len(line.run) <= len(prefix) || !slices.Equal(line.run[:len(prefix)], prefix) {
				continue
			}
			for verb := range strings.SplitSeq(line.run[len(prefix)], "|") {
				usage.verbs[verb] = true
			}
			usage.sources[line.source] = true
		}
	}
	return positions
}

// catalogRouteVerbUsage walks every public route (the `internal` subtree is
// skipped) into Usage lines and returns their qualified verb positions.
func catalogRouteVerbUsage(t *testing.T) map[string]*routeVerbUsage {
	t.Helper()
	var lines []routeVerbUsageLine
	var walk func(nodes []cli.Route, prefix []string)
	walk = func(nodes []cli.Route, prefix []string) {
		for _, node := range nodes {
			path := append(append([]string{}, prefix...), node.Name)
			if path[0] == "internal" {
				continue
			}
			for _, usage := range node.Usage {
				lines = append(lines, routeVerbUsageLine{run: usageVerbRun(usage), source: strings.Join(path, " ")})
			}
			walk(node.Children, path)
		}
	}
	walk(cli.Routes(), nil)
	return routeVerbPositions(lines, routeVerbPinnedPositions...)
}

// dispatcherVerbs returns the verbs d dispatches: table() for a lookup, else
// the string literals of the unique switch on d.tag (or, with no such switch,
// of `==` comparisons with d.tag) plus rangedVerbs(), minus help aliases and
// pass-through tokens.
func dispatcherVerbs(t *testing.T, repoRoot string, d routeVerbDispatcher) (map[string]bool, string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot, d.file), nil, 0)
	if err != nil {
		return nil, err.Error()
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		name := f.Name.Name
		if f.Recv != nil && len(f.Recv.List) == 1 {
			name = "(" + types.ExprString(f.Recv.List[0].Type) + ")." + name
		}
		if name == d.fn {
			fn = f
		}
	}
	if fn == nil {
		return nil, "func " + d.fn + " not found in " + d.file
	}
	verbs := map[string]bool{}
	literal := func(expr ast.Expr) (string, bool, string) {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false, ""
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return "", false, err.Error()
		}
		return value, true, ""
	}

	if d.table != nil {
		referenced := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr, *ast.Ident:
				referenced[types.ExprString(e.(ast.Expr))] = true
			}
			return true
		})
		for _, use := range d.uses {
			if !referenced[use] {
				return nil, d.fn + " does not reference " + use
			}
		}
		for _, verb := range d.table() {
			verbs[verb] = true
		}
		return verbs, ""
	}

	var switches []*ast.SwitchStmt
	var compared []string
	rangedFound := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			if s.Tag != nil && types.ExprString(s.Tag) == d.tag {
				switches = append(switches, s)
			}
		case *ast.BinaryExpr:
			if s.Op != token.EQL {
				break
			}
			for _, pair := range [][2]ast.Expr{{s.X, s.Y}, {s.Y, s.X}} {
				if types.ExprString(pair[0]) != d.tag {
					continue
				}
				if value, ok, _ := literal(pair[1]); ok {
					compared = append(compared, value)
				}
			}
		case *ast.RangeStmt:
			if d.ranged != "" && types.ExprString(s.X) == d.ranged {
				rangedFound = true
			}
		}
		return true
	})
	switch {
	case len(switches) > 1:
		return nil, "found " + strconv.Itoa(len(switches)) + " switches on " + d.tag + " in " + d.fn + ", want at most 1"
	case len(switches) == 1:
		for _, stmt := range switches[0].Body.List {
			for _, expr := range stmt.(*ast.CaseClause).List {
				value, ok, problem := literal(expr)
				if problem != "" {
					return nil, problem
				}
				if ok {
					verbs[value] = true
				}
			}
		}
	case len(compared) > 0:
		for _, value := range compared {
			verbs[value] = true
		}
	default:
		return nil, "found no switch on or == comparison with " + d.tag + " in " + d.fn
	}
	if d.ranged != "" {
		if !rangedFound {
			return nil, "found no range over " + d.ranged + " in " + d.fn
		}
		for _, verb := range d.rangedVerbs() {
			verbs[verb] = true
		}
	}
	for _, skip := range append(append([]string{}, routeVerbHelpAliases...), d.passThrough...) {
		delete(verbs, skip)
	}
	return verbs, ""
}

// routeVerbDrift returns "" when usage spells exactly the dispatched verbs at
// position, else the message naming the missing and extra verbs.
func routeVerbDrift(position string, usage *routeVerbUsage, d routeVerbDispatcher, dispatched map[string]bool) string {
	var missing, extra []string
	for verb := range dispatched {
		if !usage.verbs[verb] {
			missing = append(missing, verb)
		}
	}
	for verb := range usage.verbs {
		if !dispatched[verb] {
			extra = append(extra, verb)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Sprintf("verb position %q (sources %v, dispatcher %s %s): missing from Usage %v; extra in Usage %v",
		position, slices.Sorted(maps.Keys(usage.sources)), d.file, d.fn, missing, extra)
}

// TestCatalogRouteVerbAlternationsMatchDispatchers holds every verb
// alternation a public Usage line prints, bare (`pin project list|add|...`) or
// bracketed (`agent status [get … | set …]`), to the code that dispatches that
// position: a switch or `==` comparisons (plus a ranged call's values), or a
// lookup table. Its verbs, minus help aliases and pass-through tokens, must
// equal the Usage verbs, so help can neither omit an accepted verb nor
// advertise an unhandled one.
func TestCatalogRouteVerbAlternationsMatchDispatchers(t *testing.T) {
	t.Parallel()
	repoRoot := filepath.Join("..", "..")
	positions := catalogRouteVerbUsage(t)
	t.Logf("found %d qualified verb positions in public route Usage", len(positions))

	for _, position := range slices.Sorted(maps.Keys(positions)) {
		if _, ok := routeVerbDispatchers[position]; !ok {
			t.Errorf("verb position %q (sources %v) has no entry in routeVerbDispatchers", position, slices.Sorted(maps.Keys(positions[position].sources)))
		}
	}
	for _, position := range slices.Sorted(maps.Keys(routeVerbDispatchers)) {
		if _, ok := positions[position]; !ok {
			t.Errorf("routeVerbDispatchers entry %q is not a qualified verb position in the catalog", position)
		}
	}

	for _, position := range slices.Sorted(maps.Keys(positions)) {
		d, ok := routeVerbDispatchers[position]
		if !ok {
			continue
		}
		dispatched, problem := dispatcherVerbs(t, repoRoot, d)
		if problem != "" {
			t.Errorf("verb position %q: %s", position, problem)
			continue
		}
		if drift := routeVerbDrift(position, positions[position], d, dispatched); drift != "" {
			t.Error(drift)
		}
	}
}

func TestUsageVerbRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		usage string
		want  []string
	}{
		{"projmux pin project list|add|remove", []string{"pin", "project", "list|add|remove"}},
		{"projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]", []string{"agent", "status", "get|set"}},
		{"projmux attention toggle [pane]", []string{"attention", "toggle"}},
		{"projmux setup terminal [terminal] [--apply]", []string{"setup", "terminal"}},
		{"projmux create window [--project <ref> | -p <ref>]", []string{"create", "window"}},
		{"projmux agent capabilities [<agent-ref> | --provider <codex|claude>]", []string{"agent", "capabilities"}},
		{"projmux quit [--yes|--force]", []string{"quit"}},
		{"projmux instructions set <name> [--file <path> | -]", []string{"instructions", "set"}},
		{"projmux welcome [--popup [--force]]", []string{"welcome"}},
		{"projmux x [a | b", []string{"x"}},
		{"projmux x [a | <b>]", []string{"x"}},
		{"projmux x [a]", []string{"x"}},
	} {
		if got := usageVerbRun(tc.usage); !slices.Equal(got, tc.want) {
			t.Errorf("usageVerbRun(%q) = %q, want %q", tc.usage, got, tc.want)
		}
	}
}

// TestRouteVerbGuardReportsDrift feeds synthetic Usage lines through the real
// dispatchers, so the guard is proven to fail on each kind of drift.
func TestRouteVerbGuardReportsDrift(t *testing.T) {
	t.Parallel()
	repoRoot := filepath.Join("..", "..")
	for _, tc := range []struct {
		position, usage string
		want            []string
	}{
		{"agent status", "projmux agent status [get <a> | set <b> | bogus <c>]", []string{"missing from Usage []", "extra in Usage [bogus]"}},
		{"agent status", "projmux agent status [get <a> | bogus <c>]", []string{"missing from Usage [set]", "extra in Usage [bogus]"}},
		{"create", "projmux create project|window|pane|agent|notification|codex|claude", []string{"missing from Usage [antigravity]"}},
		{"get runtime", "projmux get runtime sessions|windows|panes|nodes", []string{"extra in Usage [nodes]"}},
		{"agent status", "projmux agent status [get [<agent-ref>] | set <idle> [<agent-ref>]] [--agent <ref>]", nil},
	} {
		positions := routeVerbPositions([]routeVerbUsageLine{{run: usageVerbRun(tc.usage), source: tc.position}})
		usage, ok := positions[tc.position]
		if !ok {
			t.Errorf("%q: no verb position %q in %v", tc.usage, tc.position, slices.Sorted(maps.Keys(positions)))
			continue
		}
		d := routeVerbDispatchers[tc.position]
		dispatched, problem := dispatcherVerbs(t, repoRoot, d)
		if problem != "" {
			t.Errorf("%q: %s", tc.usage, problem)
			continue
		}
		drift := routeVerbDrift(tc.position, usage, d, dispatched)
		if tc.want == nil {
			if drift != "" {
				t.Errorf("%q: unexpected drift: %s", tc.usage, drift)
			}
			continue
		}
		for _, fragment := range tc.want {
			if !strings.Contains(drift, fragment) {
				t.Errorf("%q: drift %q lacks %q", tc.usage, drift, fragment)
			}
		}
	}
}
