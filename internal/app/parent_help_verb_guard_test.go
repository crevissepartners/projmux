package app

import (
	"bytes"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// minPublicParentRoutes is a lower bound on the canonical public parents the
// catalog walk collects, so a walk that silently finds fewer fails.
//
// `pin project` is not among them: the catalog declares it a leaf whose
// handler dispatches its own verbs, so the boundary leaves its `help` alone
// and TestHandlerHelpVerbMatchesHelpFlag holds that handler branch instead.
const minPublicParentRoutes = 37

// publicParentLowerBound is a cross-check on the catalog walk, not its source:
// each of these parents must be in the collected set.
var publicParentLowerBound = []string{
	"agent", "agent approval", "agent message", "attach", "config", "config render",
	"create", "get", "get runtime", "pin", "runtime", "window",
}

// parentHelpSpelling is one argv spelling of a public parent route: canonical
// is the catalog path, argv the tokens a user types (names or aliases).
type parentHelpSpelling struct {
	canonical string
	argv      []string
}

// publicParentSpellings walks routes and returns every non-hidden route with
// children, spelled by every combination of name and alias along its path.
// A hidden route hides its whole subtree.
func publicParentSpellings(routes []cli.Route) []parentHelpSpelling {
	var out []parentHelpSpelling
	var walk func(canonical []string, spellings [][]string, nodes []cli.Route)
	walk = func(canonical []string, spellings [][]string, nodes []cli.Route) {
		for _, node := range nodes {
			if node.Hidden {
				continue
			}
			path := append(slices.Clone(canonical), node.Name)
			var next [][]string
			for _, prefix := range spellings {
				for _, token := range append([]string{node.Name}, node.Aliases...) {
					next = append(next, append(slices.Clone(prefix), token))
				}
			}
			if len(node.Children) > 0 {
				for _, argv := range next {
					out = append(out, parentHelpSpelling{canonical: strings.Join(path, " "), argv: argv})
				}
			}
			walk(path, next, node.Children)
		}
	}
	walk(nil, [][]string{nil}, routes)
	return out
}

// publicLeafPaths returns the canonical path of every non-hidden route with no
// children.
func publicLeafPaths(routes []cli.Route) [][]string {
	var out [][]string
	var walk func(prefix []string, nodes []cli.Route)
	walk = func(prefix []string, nodes []cli.Route) {
		for _, node := range nodes {
			if node.Hidden {
				continue
			}
			path := append(slices.Clone(prefix), node.Name)
			if len(node.Children) == 0 {
				out = append(out, path)
			}
			walk(path, node.Children)
		}
	}
	walk(nil, routes)
	return out
}

// helpVerbRun runs one argv through an entrypoint and returns what it wrote.
type helpVerbRun func(argv []string) (stdout, stderr string, err error)

// parentHelpVerbProblems drives every spelling through run and the help
// boundary predicate: `<parent> help` must be a help request and write the
// bytes `<parent> --help` writes, on stdout, with no error and no stderr.
func parentHelpVerbProblems(spellings []parentHelpSpelling, run helpVerbRun, helpRequested func([]string) bool) []string {
	var problems []string
	for _, spelling := range spellings {
		route := strings.Join(spelling.argv, " ")
		verbArgv := append(slices.Clone(spelling.argv), "help")
		if !helpRequested(verbArgv) {
			problems = append(problems, route+" help: the help boundary does not answer it, so migrations and tmux may run")
		}
		flagOut, _, flagErr := run(append(slices.Clone(spelling.argv), "--help"))
		if flagErr != nil {
			problems = append(problems, route+" --help: err = "+flagErr.Error())
			continue
		}
		verbOut, verbStderr, verbErr := run(verbArgv)
		if verbErr != nil {
			problems = append(problems, route+" help: err = "+verbErr.Error()+", want nil")
		}
		if verbStderr != "" {
			problems = append(problems, route+" help: stderr = "+verbStderr+", want empty")
		}
		if verbOut != flagOut {
			problems = append(problems, route+" help: stdout differs from "+route+" --help")
		}
	}
	return problems
}

// parentHelpChildProblems reports every public parent with a child spelled
// `help`. The boundary answers `<parent> help` itself, so such a child would
// become unreachable; adding one means revisiting the help verb design.
func parentHelpChildProblems(spellings []parentHelpSpelling, routes []cli.Route) []string {
	var problems []string
	seen := map[string]bool{}
	for _, spelling := range spellings {
		if seen[spelling.canonical] {
			continue
		}
		seen[spelling.canonical] = true
		parent, ok := routeAtPath(routes, strings.Fields(spelling.canonical))
		if !ok {
			problems = append(problems, spelling.canonical+": the walk produced a path the tree does not resolve")
			continue
		}
		for _, child := range parent.Children {
			if child.Name == "help" || slices.Contains(child.Aliases, "help") {
				problems = append(problems, spelling.canonical+": child "+child.Name+" is spelled `help`, which the help verb shadows")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// routeAtPath finds the node at a canonical path in routes.
func routeAtPath(routes []cli.Route, path []string) (cli.Route, bool) {
	nodes := routes
	var current cli.Route
	for _, token := range path {
		i := slices.IndexFunc(nodes, func(route cli.Route) bool { return route.Name == token })
		if i < 0 {
			return cli.Route{}, false
		}
		current = nodes[i]
		nodes = current.Children
	}
	return current, true
}

// appHelpVerbRun runs argv through the real app entrypoint.
func appHelpVerbRun(argv []string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := New().Run(argv, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestPublicParentHelpVerbMatchesHelpFlag is the closed catalog-wide guard:
// every public parent route, under every alias spelling of its path, answers
// `<parent> help` with the bytes `<parent> --help` writes, on stdout, exit 0,
// nothing on stderr, and through the shared help boundary.
func TestPublicParentHelpVerbMatchesHelpFlag(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	spellings := publicParentSpellings(cli.Routes())
	canonical := map[string]bool{}
	for _, spelling := range spellings {
		canonical[spelling.canonical] = true
	}
	names := make([]string, 0, len(canonical))
	for name := range canonical {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("public parent routes: %d canonical, %d spellings with aliases: %s", len(names), len(spellings), strings.Join(names, ", "))
	if len(names) < minPublicParentRoutes {
		t.Fatalf("collected %d public parent routes, want at least %d", len(names), minPublicParentRoutes)
	}
	for _, want := range publicParentLowerBound {
		if !canonical[want] {
			t.Errorf("public parent %q is missing from the catalog walk", want)
		}
	}
	for _, problem := range parentHelpVerbProblems(spellings, appHelpVerbRun, cli.HelpRequested) {
		t.Error(problem)
	}
}

// TestPublicParentsHaveNoChildSpelledHelp holds the condition the help verb
// rests on: no public parent has a child named or aliased `help`.
func TestPublicParentsHaveNoChildSpelledHelp(t *testing.T) {
	t.Parallel()
	routes := cli.Routes()
	for _, problem := range parentHelpChildProblems(publicParentSpellings(routes), routes) {
		t.Error(problem)
	}
}

// TestHelpVerbIsOnlyAParentVerb keeps the boundary narrow: a leaf's `help`
// can be an operand, a `help` followed by more tokens is not a verb, and a
// `help` after the bare `--` is payload.
func TestHelpVerbIsOnlyAParentVerb(t *testing.T) {
	t.Parallel()
	leaves := publicLeafPaths(cli.Routes())
	if len(leaves) == 0 {
		t.Fatal("the catalog walk found no public leaf routes")
	}
	for _, leaf := range leaves {
		if argv := append(slices.Clone(leaf), "help"); cli.HelpRequested(argv) {
			t.Errorf("cli.HelpRequested(%q) = true; a leaf's help word can be an operand", argv)
		}
	}
	for _, spelling := range publicParentSpellings(cli.Routes()) {
		for _, argv := range [][]string{
			append(slices.Clone(spelling.argv), "help", "extra"),
			append(slices.Clone(spelling.argv), "--", "help"),
		} {
			if cli.HelpRequested(argv) {
				t.Errorf("cli.HelpRequested(%q) = true, want false", argv)
			}
		}
	}
}

// TestPublicParentHelpVerbGuardDetectsDrift is the negative control: a
// boundary that ignores the bare help word, a handler that rejects it, and a
// parent with a child spelled `help` are each reported with the route.
func TestPublicParentHelpVerbGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	spellings := []parentHelpSpelling{
		{canonical: "agent approval", argv: []string{"agent", "approval"}},
		{canonical: "get", argv: []string{"get"}},
	}
	// The pre-boundary behavior: flags render help, the bare word is refused.
	rejectingRun := func(argv []string) (string, string, error) {
		if argv[len(argv)-1] == "help" {
			return "", "unknown subcommand: help\n", errors.New("usage")
		}
		return "projmux " + strings.Join(argv[:len(argv)-1], " ") + "\n", "", nil
	}
	ignoresVerb := func([]string) bool { return false }
	problems := strings.Join(parentHelpVerbProblems(spellings, rejectingRun, ignoresVerb), "\n")
	for _, want := range []string{
		"agent approval help: the help boundary does not answer it",
		"agent approval help: err = usage, want nil",
		"agent approval help: stderr = unknown subcommand: help",
		"agent approval help: stdout differs from agent approval --help",
		"get help: the help boundary does not answer it",
		"get help: stdout differs from get --help",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("guard problems do not report %q:\n%s", want, problems)
		}
	}

	tree := []cli.Route{{Name: "fake", Children: []cli.Route{
		{Name: "help"},
		{Name: "nested", Children: []cli.Route{{Name: "list", Aliases: []string{"help"}}}},
	}}}
	childProblems := strings.Join(parentHelpChildProblems(publicParentSpellings(tree), tree), "\n")
	for _, want := range []string{
		"fake: child help is spelled `help`",
		"fake nested: child list is spelled `help`",
	} {
		if !strings.Contains(childProblems, want) {
			t.Errorf("child guard problems do not report %q:\n%s", want, childProblems)
		}
	}
}
