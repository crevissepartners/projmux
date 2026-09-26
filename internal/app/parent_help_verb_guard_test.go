package app

import (
	"bytes"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// minPublicParentRoutes is a lower bound on the canonical public parents the
// catalog walk collects, so a walk that silently finds fewer fails.
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

// parentHelpVerbTails are the verb spellings appended to every public parent:
// the bare `help` and `help` followed by tokens, a flag, or a bare `--`. The
// boundary ignores whatever follows the `help`.
var parentHelpVerbTails = [][]string{
	{"help"},
	{"help", "x"},
	{"help", "--json"},
	{"help", "x", "y"},
	{"help", "--", "x"},
}

// parentHelpVerbProblems drives every spelling and verb tail through run and
// the help boundary predicate: `<parent> help ...` must be a help request and
// write the bytes `<parent> --help` writes, on stdout, with no error and no
// stderr. Each problem names the route and the verb spelling.
func parentHelpVerbProblems(spellings []parentHelpSpelling, run helpVerbRun, helpRequested func([]string) bool) []string {
	var problems []string
	for _, spelling := range spellings {
		route := strings.Join(spelling.argv, " ")
		flagOut, _, flagErr := run(append(slices.Clone(spelling.argv), "--help"))
		if flagErr != nil {
			problems = append(problems, route+" --help: err = "+flagErr.Error())
			continue
		}
		for _, tail := range parentHelpVerbTails {
			verbArgv := append(slices.Clone(spelling.argv), tail...)
			verb := strings.Join(verbArgv, " ")
			if !helpRequested(verbArgv) {
				problems = append(problems, verb+": the help boundary does not answer it, so migrations and tmux may run")
			}
			verbOut, verbStderr, verbErr := run(verbArgv)
			if verbErr != nil {
				problems = append(problems, verb+": err = "+verbErr.Error()+", want nil")
			}
			if verbStderr != "" {
				problems = append(problems, verb+": stderr = "+verbStderr+", want empty")
			}
			if verbOut != flagOut {
				problems = append(problems, verb+": stdout differs from "+route+" --help")
			}
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

// lastTokenHelpRequested is the help verb rule before trailing tokens were
// ignored: `help` must be the last token before the first bare `--`, and every
// token before it must resolve to a public parent. It is kept only as the
// negative control of TestPublicParentHelpVerbGuardDetectsDrift.
func lastTokenHelpRequested(args []string) bool {
	lead := args
	if i := slices.Index(args, "--"); i >= 0 {
		lead = args[:i]
	}
	if len(lead) < 2 || lead[len(lead)-1] != "help" {
		return false
	}
	current, ok := cli.LookupRoute(lead[0])
	if !ok || current.Hidden {
		return false
	}
	for _, token := range lead[1 : len(lead)-1] {
		i := slices.IndexFunc(current.Children, func(child cli.Route) bool { return child.Name == token })
		if i < 0 {
			i = slices.IndexFunc(current.Children, func(child cli.Route) bool { return slices.Contains(child.Aliases, token) })
		}
		if i < 0 || current.Children[i].Hidden {
			return false
		}
		current = current.Children[i]
	}
	return len(current.Children) > 0
}

// appHelpVerbRun runs argv through the real app entrypoint.
func appHelpVerbRun(argv []string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := New().Run(argv, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestPublicParentHelpVerbMatchesHelpFlag is the closed catalog-wide guard:
// every public parent route, under every alias spelling of its path, answers
// `<parent> help` and `<parent> help <anything>` with the bytes `<parent>
// --help` writes, on stdout, exit 0, nothing on stderr, and through the shared
// help boundary.
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

// TestHelpVerbIsOnlyAParentVerb keeps the boundary narrow: a leaf's `help`,
// alone or followed by more tokens, can be an operand, and a `help` after the
// bare `--` is payload. Only a public parent's `help` is a verb, whatever
// follows it (TestPublicParentHelpVerbMatchesHelpFlag).
func TestHelpVerbIsOnlyAParentVerb(t *testing.T) {
	t.Parallel()
	leaves := publicLeafPaths(cli.Routes())
	if len(leaves) == 0 {
		t.Fatal("the catalog walk found no public leaf routes")
	}
	for _, leaf := range leaves {
		for _, argv := range [][]string{
			append(slices.Clone(leaf), "help"),
			append(slices.Clone(leaf), "help", "extra"),
		} {
			if cli.HelpRequested(argv) {
				t.Errorf("cli.HelpRequested(%q) = true; a leaf's help word can be an operand", argv)
			}
		}
	}
	for _, spelling := range publicParentSpellings(cli.Routes()) {
		for _, argv := range [][]string{
			append(slices.Clone(spelling.argv), "--", "help"),
			append(slices.Clone(spelling.argv), "--", "help", "x"),
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
		if slices.Contains(argv, "help") {
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

	// The last-token rule this guard replaced: the bare `help` word renders
	// help, and a `help` with tokens after it reaches the handler, which
	// refuses them. The guard must report every trailing-token spelling.
	lastTokenRun := func(argv []string) (string, string, error) {
		lead := argv
		if i := slices.Index(argv, "--"); i >= 0 {
			lead = argv[:i]
		}
		if i := slices.Index(lead, "help"); i >= 0 && i != len(lead)-1 {
			return "", "unexpected arguments after help\n", errors.New("usage")
		}
		if i := slices.Index(argv, "help"); i >= 0 {
			argv = append(slices.Clone(argv[:i]), "--help")
		}
		return "projmux " + strings.Join(argv[:len(argv)-1], " ") + "\n", "", nil
	}
	oldProblems := parentHelpVerbProblems(spellings, lastTokenRun, lastTokenHelpRequested)
	joined := strings.Join(oldProblems, "\n")
	for _, want := range []string{
		"agent approval help x: the help boundary does not answer it",
		"agent approval help x: err = usage, want nil",
		"agent approval help x: stderr = unexpected arguments after help",
		"agent approval help x: stdout differs from agent approval --help",
		"get help --json: the help boundary does not answer it",
		"get help x y: the help boundary does not answer it",
		"get help x y: stdout differs from get --help",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("guard problems under the last-token rule do not report %q:\n%s", want, joined)
		}
	}
	for _, problem := range oldProblems {
		for _, answered := range []string{"agent approval help:", "get help:", "agent approval help -- x:", "get help -- x:"} {
			if strings.HasPrefix(problem, answered) {
				t.Errorf("the last-token rule answers %q, but the guard reports %q", strings.TrimSuffix(answered, ":"), problem)
			}
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

// parentDashHelpTails are the help spellings placed after the bare `--`. On a
// public parent they are payload, not a help request, so the parent refuses
// them as a usage error instead of answering with its help.
var parentDashHelpTails = [][]string{
	{"--", "help"},
	{"--", "--help"},
	{"--", "-h"},
}

// parentDashHelpProblems runs every spelling with every parentDashHelpTails
// tail and reports, naming the full argv, each run that is not a usage error
// refused on stderr alone: no error, a non-usage error, anything on stdout, or
// no refusal text. The refusal text is what the handler wrote to stderr plus
// the error message, which the binary prints to stderr (executeCLI), so a
// handler that only returns its usage error still refuses on stderr.
func parentDashHelpProblems(spellings []parentHelpSpelling, run helpVerbRun, isUsage func(error) bool) []string {
	var problems []string
	for _, spelling := range spellings {
		for _, tail := range parentDashHelpTails {
			argv := append(slices.Clone(spelling.argv), tail...)
			name := strings.Join(argv, " ")
			stdout, stderr, err := run(slices.Clone(argv))
			switch {
			case err == nil:
				problems = append(problems, name+": err = nil, want a usage error")
			case !isUsage(err):
				problems = append(problems, name+": err = "+err.Error()+", want a usage error")
			}
			if stdout != "" {
				problems = append(problems, name+": stdout = "+dashHelpSnippet(stdout)+", want empty")
			}
			if stderr == "" && (err == nil || err.Error() == "") {
				problems = append(problems, name+": stderr is empty and the error has no message, want the refusal")
			}
		}
	}
	return problems
}

// dashHelpSnippet quotes s for a problem line, truncated so a whole help page
// does not flood the report.
func dashHelpSnippet(s string) string {
	const limit = 80
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return strconv.Quote(s)
}

// TestPublicParentDashHelpIsAUsageError pins the rule that a `help`, `--help`,
// or `-h` after the bare `--` is payload on every public parent, under every
// alias spelling of its path: the help boundary does not read past `--`, and
// the parent refuses the payload as a usage error (exit 2) with its refusal on
// stderr and nothing on stdout, instead of answering with its help.
func TestPublicParentDashHelpIsAUsageError(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	spellings := publicParentSpellings(cli.Routes())
	canonical := map[string]bool{}
	for _, spelling := range spellings {
		canonical[spelling.canonical] = true
	}
	if len(canonical) < minPublicParentRoutes {
		t.Fatalf("collected %d public parent routes, want at least %d", len(canonical), minPublicParentRoutes)
	}
	for _, want := range publicParentLowerBound {
		if !canonical[want] {
			t.Errorf("public parent %q is missing from the catalog walk", want)
		}
	}
	for _, problem := range parentDashHelpProblems(spellings, appHelpVerbRun, IsUsageError) {
		t.Error(problem)
	}
}

// TestPublicParentDashHelpGuardDetectsDrift is the negative control: a parent
// that answers `-- help` with its help and exit 0 (the old hook, window, pin
// project, and runtime tag branches), a non-usage error, a usage error that
// still writes stdout, and a usage error with nothing on stderr and no message
// are each reported with the full argv; a proper refusal, on stderr or only as
// the usage error's message, is not.
func TestPublicParentDashHelpGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	usage := errors.New("usage")
	emptyUsage := errors.New("")
	isUsage := func(err error) bool { return err == usage || err == emptyUsage }
	spellings := []parentHelpSpelling{
		{canonical: "hook", argv: []string{"hook"}},
		{canonical: "window", argv: []string{"window"}},
		{canonical: "get", argv: []string{"get"}},
	}
	run := func(argv []string) (string, string, error) {
		last := argv[len(argv)-1]
		switch argv[0] {
		case "hook":
			return "projmux hook\n", "", nil
		case "window":
			if last == "help" {
				return "", "window failed\n", errors.New("runtime failure")
			}
			return "projmux window\n", "usage: projmux window\n", usage
		default:
			switch last {
			case "-h":
				return "", "", emptyUsage
			case "--help":
				return "", "", usage
			}
			return "", "usage: projmux get\n", usage
		}
	}
	problems := parentDashHelpProblems(spellings, run, isUsage)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		"hook -- help: err = nil, want a usage error",
		"hook -- --help: err = nil, want a usage error",
		"hook -- -h: err = nil, want a usage error",
		`hook -- help: stdout = "projmux hook\n", want empty`,
		"hook -- help: stderr is empty and the error has no message, want the refusal",
		"window -- help: err = runtime failure, want a usage error",
		`window -- --help: stdout = "projmux window\n", want empty`,
		"get -- -h: stderr is empty and the error has no message, want the refusal",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("guard problems do not report %q:\n%s", want, joined)
		}
	}
	for _, problem := range problems {
		for _, clean := range []string{"get -- help:", "get -- --help:", "window -- help: stdout", "window -- help: stderr"} {
			if strings.HasPrefix(problem, clean) {
				t.Errorf("guard reports a proper refusal: %q", problem)
			}
		}
	}
}
