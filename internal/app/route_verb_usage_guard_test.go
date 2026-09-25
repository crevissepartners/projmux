package app

import (
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

// routeVerbDispatcher names the switch that dispatches the verbs a catalog
// Usage line spells as an alternation (`a|b|c`) at one verb position.
type routeVerbDispatcher struct {
	// file is repo-relative; fn is "Name" or "(*Recv).Name"; tag is the
	// printed switch tag expression that selects the switch inside fn.
	file, fn, tag string
	// passThrough are case values that descend to a deeper verb position
	// rather than name a verb at this one (pin's `project`).
	passThrough []string
	// exception, when set, skips the equality check with a reason; the key
	// still counts for closure.
	exception string
}

// routeVerbDispatchers is closed over the catalog: every qualified verb
// position must have exactly one entry here, and every entry must be one.
var routeVerbDispatchers = map[string]routeVerbDispatcher{
	"agent topic":   {file: "internal/app/agent_interaction.go", fn: "(*agentCommand).runTopic", tag: "action"},
	"agent turn":    {file: "internal/app/agent_control.go", fn: "(*agentCommand).runTurn", tag: "args[0]"},
	"attention":     {file: "internal/app/attention.go", fn: "(*attentionCommand).Run", tag: "args[0]"},
	"config render": {file: "internal/app/config.go", fn: "(*configCommand).runRender", tag: "args[0]"},
	"create":        {exception: "kinds dispatch through a switch but provider shortcuts through a loop over cli.ProviderCreateShortcuts"},
	"get runtime":   {exception: "dispatch is table-driven by cli.CanonicalGrandchildToken and runtimeKindTokens, not a switch"},
	"hook":          {file: "internal/app/hook.go", fn: "(*hookCommand).Run", tag: "fs.Arg(0)"},
	"pin project":   {file: "internal/app/pin.go", fn: "(*pinCommand).Run", tag: "fs.Arg(0)", passThrough: []string{"project"}},
	"runtime tag":   {file: "internal/app/tag.go", fn: "(*tagCommand).Run", tag: "fs.Arg(0)", passThrough: []string{"project"}},
	"update":        {file: "internal/app/update.go", fn: "(*updateCommand).Run", tag: "args[0]"},
	"window":        {file: "internal/app/recent_window.go", fn: "(*windowCommand).Run", tag: "fs.Arg(0)"},
}

var routeVerbToken = regexp.MustCompile(`^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)*$`)

var routeVerbHelpAliases = []string{"help", "--help", "-h"}

type routeVerbUsage struct {
	verbs   map[string]bool
	sources map[string]bool
}

// catalogRouteVerbUsage walks every public route (the `internal` subtree is
// skipped) and returns, per qualified verb position, the verbs its Usage lines
// spell and the routes those lines belong to.
func catalogRouteVerbUsage(t *testing.T) map[string]*routeVerbUsage {
	t.Helper()
	type usageLine struct {
		run    []string
		source string
	}
	var lines []usageLine
	var walk func(nodes []cli.Route, prefix []string)
	walk = func(nodes []cli.Route, prefix []string) {
		for _, node := range nodes {
			path := append(append([]string{}, prefix...), node.Name)
			if path[0] == "internal" {
				continue
			}
			for _, usage := range node.Usage {
				var run []string
				for tok := range strings.SplitSeq(strings.TrimPrefix(usage, "projmux "), " ") {
					if !routeVerbToken.MatchString(tok) {
						break
					}
					run = append(run, tok)
				}
				lines = append(lines, usageLine{run: run, source: strings.Join(path, " ")})
			}
			walk(node.Children, path)
		}
	}
	walk(cli.Routes(), nil)

	// A position is qualified by any alternation token in a leading run.
	positions := map[string]*routeVerbUsage{}
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

// dispatcherSwitchVerbs returns the string-literal case values of the unique
// switch on d.tag inside d.fn.
func dispatcherSwitchVerbs(t *testing.T, repoRoot string, d routeVerbDispatcher) (map[string]bool, string) {
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
	var switches []*ast.SwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if s, ok := n.(*ast.SwitchStmt); ok && s.Tag != nil && types.ExprString(s.Tag) == d.tag {
			switches = append(switches, s)
		}
		return true
	})
	if len(switches) != 1 {
		return nil, "found " + strconv.Itoa(len(switches)) + " switches on " + d.tag + " in " + d.fn + ", want exactly 1"
	}
	verbs := map[string]bool{}
	for _, stmt := range switches[0].Body.List {
		for _, expr := range stmt.(*ast.CaseClause).List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return nil, err.Error()
			}
			verbs[value] = true
		}
	}
	for _, skip := range append(append([]string{}, routeVerbHelpAliases...), d.passThrough...) {
		delete(verbs, skip)
	}
	return verbs, ""
}

// TestCatalogRouteVerbAlternationsMatchDispatchers holds every verb
// alternation a public Usage line prints (`pin project list|add|...`) to the
// switch that dispatches that position: the switch's string cases, minus help
// aliases and pass-through tokens, must equal the Usage verbs, so help can
// neither omit an accepted verb nor advertise an unhandled one.
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
		if !ok || d.exception != "" {
			continue
		}
		usage := positions[position]
		dispatched, problem := dispatcherSwitchVerbs(t, repoRoot, d)
		if problem != "" {
			t.Errorf("verb position %q: %s", position, problem)
			continue
		}
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
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("verb position %q (sources %v, dispatcher %s %s): missing from Usage %v; extra in Usage %v",
				position, slices.Sorted(maps.Keys(usage.sources)), d.file, d.fn, missing, extra)
		}
	}
}
