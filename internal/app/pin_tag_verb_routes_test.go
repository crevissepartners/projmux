package app

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// pinVerbRoutes are the verbs `(*pinCommand).runLevel` dispatches at the
// `pin project` level, each a public catalog child of `pin project`.
var pinVerbRoutes = []string{"list", "add", "remove", "toggle", "clear", "migrate"}

// tagVerbRoutes are the verbs `(*tagCommand).Run` dispatches, each a public
// catalog child of `runtime tag`.
var tagVerbRoutes = []string{"list", "clear", "toggle"}

// TestPinAndTagVerbsAreCatalogChildren holds each dispatch set to the catalog:
// every verb the handler dispatches is a public child of its parent route,
// every public child is a dispatched verb, and each child is its own
// canonical spelling rather than an edge to an unrelated family.
func TestPinAndTagVerbsAreCatalogChildren(t *testing.T) {
	t.Parallel()
	for parent, verbs := range map[string][]string{"pin project": pinVerbRoutes, "runtime tag": tagVerbRoutes} {
		path, route, ok := cli.Resolve(strings.Fields(parent))
		if !ok || strings.Join(path, " ") != parent {
			t.Fatalf("catalog has no %s route", parent)
		}
		var children []string
		for _, child := range route.Children {
			if child.Hidden {
				t.Errorf("%s %s is hidden, want a public child", parent, child.Name)
			}
			spelling := parent + " " + child.Name
			if !slices.Equal(child.Canonical, []string{spelling}) {
				t.Errorf("%s canonical = %q, want only its own spelling", spelling, child.Canonical)
			}
			if !slices.Equal(child.Usage, []string{route.Usage[len(children)]}) {
				t.Errorf("%s usage = %q, want the parent's line %q", spelling, child.Usage, route.Usage[len(children)])
			}
			children = append(children, child.Name)
		}
		if !slices.Equal(children, verbs) {
			t.Fatalf("%s children = %q, want %q", parent, children, verbs)
		}
		if len(route.Usage) != len(children) {
			t.Errorf("%s usage = %q, want exactly one copied line per child", parent, route.Usage)
		}
	}
}

// pinVerbNotes is the pin-kind note block every pin rejection prints under
// its usage.
func pinVerbNotes() string {
	var notes bytes.Buffer
	printPinNotes(&notes)
	return notes.String()
}

// verbMisuseCheck asserts one rejected verb call: a usage error (exit 2)
// keeping reason want, stderr exactly the verb's own usage block and tail (or,
// for a flag error, one flag reason line and then only the usage block), and
// no parent synopsis line.
func verbMisuseCheck(t *testing.T, route, name, want, tail string, err error, stderr string) {
	t.Helper()
	if err == nil || !IsUsageError(err) {
		t.Fatalf("%s err = %v (%T), want a usage error (exit 2)", name, err, err)
	}
	var usage bytes.Buffer
	cli.WriteRouteUsage(&usage, route)
	if usage.Len() == 0 || !strings.Contains(usage.String(), "projmux "+route) {
		t.Fatalf("catalog usage of %q = %q, want its own line", route, usage.String())
	}
	block := usage.String() + tail
	if want != "" {
		if got := err.Error(); got != want {
			t.Errorf("%s reason = %q, want %q", name, got, want)
		}
		if stderr != block {
			t.Errorf("%s stderr = %q, want exactly the %s usage block %q", name, stderr, route, block)
		}
	} else {
		// A flag error prints the reason and then only the catalog Usage,
		// the shape every public flag parse failure shares.
		reason, rest, _ := strings.Cut(stderr, "\n")
		if !strings.HasPrefix(reason, "flag provided but not defined: ") || rest != usage.String() {
			t.Errorf("%s stderr = %q, want one flag reason line and then exactly the %s usage block %q", name, stderr, route, usage.String())
		}
	}
	for line := range strings.SplitSeq(stderr, "\n") {
		if synopsis, ok := strings.CutPrefix(line, "  projmux "); ok && synopsis != route && !strings.HasPrefix(synopsis, route+" ") {
			t.Errorf("%s stderr prints another route's line %q: %q", name, line, stderr)
		}
	}
}

// TestPinVerbMisuseIsItsOwnRouteUsageError drives each `pin project` verb with
// argv its parser refuses (add, remove, and toggle parse no flags, so only
// list, clear, and migrate have an unknown-flag row), through the canonical spelling and the store and
// Registry fixtures: the error is a usage error (exit 2) that keeps its
// message, stderr is the verb's own usage block and the pin kinds (a flag
// error: the reason and the usage block only), and nothing is written or
// printed on stdout.
func TestPinVerbMisuseIsItsOwnRouteUsageError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		argv []string
		want string // the reason; empty for a flag error the flag package prints
	}{
		{argv: []string{"list", "x"}, want: "pin project list does not accept positional arguments"},
		{argv: []string{"list", "--zz"}},
		{argv: []string{"add"}, want: "pin project add requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"add", "/x/a", "/x/b"}, want: "pin project add requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"add", "--", "-foo"}, want: "pin project add requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"remove"}, want: "pin project remove requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"remove", "/x/a", "/x/b"}, want: "pin project remove requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"toggle"}, want: "pin project toggle requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"toggle", "/x/a", "/x/b"}, want: "pin project toggle requires exactly 1 <dir|uid:uid> argument"},
		{argv: []string{"clear", "x"}, want: "pin project clear does not accept positional arguments"},
		{argv: []string{"clear", "--zz"}},
		{argv: []string{"migrate", "x"}, want: "pin project migrate does not accept positional arguments"},
		{argv: []string{"migrate", "--zz"}},
	} {
		name := "pin project " + strings.Join(test.argv, " ")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newStubPinStore("proj-a")
			var stdout, stderr bytes.Buffer
			err := pinFixture(store).Run(append([]string{"project"}, test.argv...), &stdout, &stderr)
			verbMisuseCheck(t, "pin project "+test.argv[0], name, test.want, pinVerbNotes(), err, stderr.String())
			if stdout.Len() != 0 || store.writes != 0 {
				t.Errorf("%s ran past its parser: stdout=%q writes=%d", name, stdout.String(), store.writes)
			}
		})
	}

	// A bad --kind value is a flag value outside its declared set: a usage
	// error (exit 2) that keeps its reason and prints the list route's own
	// usage.
	store := newStubPinStore("proj-a")
	var stdout, stderr bytes.Buffer
	err := pinFixture(store).Run([]string{"project", "list", "--kind", "bogus"}, &stdout, &stderr)
	if err == nil || !IsUsageError(err) || err.Error() != `unknown pin kind "bogus": use project or candidate` {
		t.Fatalf("pin project list --kind bogus err = %v (usage error %v), want the exit 2 kind refusal", err, IsUsageError(err))
	}
	if store.writes != 0 {
		t.Errorf("pin project list --kind bogus wrote the pin store %d times", store.writes)
	}
	var usage bytes.Buffer
	cli.WriteRouteUsage(&usage, "pin project list")
	if want := usage.String() + pinVerbNotes(); stderr.String() != want || stdout.Len() != 0 {
		t.Errorf("pin project list --kind bogus stdout=%q stderr=%q, want only the list usage %q", stdout.String(), stderr.String(), want)
	}
}

// TestTagVerbMisuseIsItsOwnRouteUsageError drives each `runtime tag` verb with
// argv its parser refuses (toggle parses no flags, so a dash token is its
// operand and only list and clear have an unknown-flag row): a usage error (exit 2) with its message kept,
// stderr exactly the verb's own usage block, and the tag store untouched.
func TestTagVerbMisuseIsItsOwnRouteUsageError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		argv []string
		want string
	}{
		{argv: []string{"list", "x"}, want: "runtime tag list does not accept positional arguments"},
		{argv: []string{"list", "--zz"}},
		{argv: []string{"clear", "x"}, want: "runtime tag clear does not accept positional arguments"},
		{argv: []string{"clear", "--zz"}},
		{argv: []string{"toggle"}, want: "runtime tag toggle requires exactly 1 <name> argument"},
		{argv: []string{"toggle", "a", "b"}, want: "runtime tag toggle requires exactly 1 <name> argument"},
		{argv: []string{"toggle", "   "}, want: "runtime tag toggle requires a non-empty <name> argument"},
		{argv: []string{"toggle", "--", "-x"}, want: "runtime tag toggle requires exactly 1 <name> argument"},
	} {
		name := "runtime tag " + strings.Join(test.argv, " ")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &stubTagStore{list: []string{"alpha"}}
			var stdout, stderr bytes.Buffer
			err := (&tagCommand{store: store}).Run(test.argv, &stdout, &stderr)
			verbMisuseCheck(t, "runtime tag "+test.argv[0], name, test.want, "", err, stderr.String())
			if stdout.Len() != 0 || store.toggled != "" || store.cleared {
				t.Errorf("%s ran past its parser: stdout=%q toggled=%q cleared=%v", name, stdout.String(), store.toggled, store.cleared)
			}
		})
	}
}

// TestPinAndTagVerbHelpIsTheVerbsOwnHelp runs `<verb> --help` through the
// shared help boundary with an isolated HOME: stdout is the verb's own node
// help, exit 0, nothing on stderr.
func TestPinAndTagVerbHelpIsTheVerbsOwnHelp(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	var routes []string
	for _, verb := range pinVerbRoutes {
		routes = append(routes, "pin project "+verb)
	}
	for _, verb := range tagVerbRoutes {
		routes = append(routes, "runtime tag "+verb)
	}
	for _, route := range routes {
		var want bytes.Buffer
		if err := cli.WriteRouteHelp(&want, route); err != nil {
			t.Fatalf("catalog help of %q: %v", route, err)
		}
		if !strings.HasPrefix(want.String(), "projmux "+route+"\n") {
			t.Fatalf("catalog help of %q = %q, want the node's own help", route, want.String())
		}
		stdout, stderr, err := appHelpVerbRun(append(strings.Fields(route), "--help"))
		if err != nil || stderr != "" || stdout != want.String() {
			t.Errorf("projmux %s --help = stdout %q stderr %q err %v, want its own help %q", route, stdout, stderr, err, want.String())
		}
	}
}

// TestPinAndTagOperandVerbsTakeDashOperandsVerbatim pins the unchanged normal
// path of the four operand verbs: they parse no flags, so a token with a
// leading dash, `--zz` included, is the operand itself.
func TestPinAndTagOperandVerbsTakeDashOperandsVerbatim(t *testing.T) {
	t.Parallel()
	store := newStubPinStore()
	cmd := pinFixture(store)
	for _, test := range []struct {
		argv []string
		want string
	}{
		{argv: []string{"project", "add", "-foo"}, want: "pinned: candidate -foo\n"},
		{argv: []string{"project", "toggle", "-foo"}, want: "unpinned: candidate -foo\n"},
		{argv: []string{"project", "toggle", "-foo"}, want: "pinned: candidate -foo\n"},
		{argv: []string{"project", "remove", "-foo"}, want: "unpinned: candidate -foo\n"},
		{argv: []string{"project", "add", "--zz"}, want: "pinned: candidate --zz\n"},
	} {
		var stdout, stderr bytes.Buffer
		if err := cmd.Run(test.argv, &stdout, &stderr); err != nil {
			t.Fatalf("pin %q err = %v (stderr %q), want success", test.argv, err, stderr.String())
		}
		if stdout.String() != test.want || stderr.Len() != 0 {
			t.Errorf("pin %q stdout = %q stderr = %q, want %q and no stderr", test.argv, stdout.String(), stderr.String(), test.want)
		}
	}
	if store.writes != 5 {
		t.Errorf("pin store writes = %d, want 5", store.writes)
	}

	tags := &stubTagStore{toggleResult: true}
	var stdout, stderr bytes.Buffer
	if err := (&tagCommand{store: tags}).Run([]string{"toggle", "-x"}, &stdout, &stderr); err != nil {
		t.Fatalf("runtime tag toggle -x err = %v (stderr %q), want success", err, stderr.String())
	}
	if stdout.String() != "tagged: -x\n" || stderr.Len() != 0 || tags.toggled != "-x" {
		t.Errorf("runtime tag toggle -x stdout = %q stderr = %q toggled = %q, want tagged: -x", stdout.String(), stderr.String(), tags.toggled)
	}
}

// pinTagVerbFakeRoutes returns a copy of the catalog with the child verb of
// the parent at path removed, and, when dropLine is set, its parent line too.
func pinTagVerbFakeRoutes(parent, verb string, dropLine bool) []cli.Route {
	tokens := strings.Fields(parent)
	var prune func(nodes []cli.Route, depth int) []cli.Route
	prune = func(nodes []cli.Route, depth int) []cli.Route {
		nodes = slices.Clone(nodes)
		for i, node := range nodes {
			if node.Name != tokens[depth] {
				continue
			}
			if depth == len(tokens)-1 {
				node.Children = slices.DeleteFunc(slices.Clone(node.Children), func(child cli.Route) bool { return child.Name == verb })
			} else {
				node.Children = prune(node.Children, depth+1)
			}
			if dropLine {
				line := "projmux " + parent + " " + verb
				node.Usage = slices.DeleteFunc(slices.Clone(node.Usage), func(l string) bool {
					return l == line || strings.HasPrefix(l, line+" ")
				})
			}
			nodes[i] = node
		}
		return nodes
	}
	return prune(cli.Routes(), 0)
}

// TestPinAndTagVerbGuardsCatchAMissingCatalogNode is the negative control: a
// catalog without the `pin project migrate` node fails the synopsis flag guard
// (the FlagSet `pin project migrate` names no catalog route), and, with the
// parent lines gone too, a catalog without `runtime tag clear` fails the route
// verb guard (the dispatcher's `clear` is missing from the runtime tag Usage).
// Each failure names the route.
func TestPinAndTagVerbGuardsCatchAMissingCatalogNode(t *testing.T) {
	t.Parallel()

	pkgs := flagParseGuardLoadRepo(t, filepath.Join("..", ".."))
	report := flagParseGuardAnalyze(pkgs)
	eval, problems := newFlagSetNameEval(pkgs)
	if len(problems) != 0 {
		t.Fatalf("flag set name evaluator: %q", problems)
	}
	checked, _ := synopsisFlagProblems(newSynopsisFlagEngine(eval), report, synopsisFlagInputs{
		hidden:         flagParseGuardExceptions,
		names:          flagSetNameExceptions,
		siteExceptions: synopsisFlagSiteExceptions,
		flagExceptions: synopsisFlagExceptions,
		aliases:        synopsisFlagAliases,
		usage:          switchVerbFakeUsage(pinTagVerbFakeRoutes("pin project", "migrate", false)),
	})
	want := `route "pin project migrate" is not a catalog route`
	if !slices.ContainsFunc(checked, func(p string) bool { return strings.Contains(p, "runMigrate") && strings.Contains(p, want) }) {
		t.Errorf("synopsis flag guard over a catalog without pin project migrate: missing %q in:\n%s", want, strings.Join(checked, "\n"))
	}

	var lines []routeVerbUsageLine
	var walk func(nodes []cli.Route, prefix []string)
	walk = func(nodes []cli.Route, prefix []string) {
		for _, node := range nodes {
			path := append(slices.Clone(prefix), node.Name)
			if path[0] == "internal" {
				continue
			}
			for _, usage := range node.Usage {
				lines = append(lines, routeVerbUsageLine{run: usageVerbRun(usage), source: strings.Join(path, " ")})
			}
			walk(node.Children, path)
		}
	}
	walk(pinTagVerbFakeRoutes("runtime tag", "clear", true), nil)
	positions := routeVerbPositions(lines, routeVerbPinnedPositions...)
	d := routeVerbDispatchers["runtime tag"]
	dispatched, problem := dispatcherVerbs(t, filepath.Join("..", ".."), d)
	if problem != "" {
		t.Fatal(problem)
	}
	drift := routeVerbDrift("runtime tag", positions["runtime tag"], d, dispatched)
	if !strings.Contains(drift, `verb position "runtime tag"`) || !strings.Contains(drift, "missing from Usage [clear]") {
		t.Errorf("route verb guard over a catalog without runtime tag clear: drift = %q, want the runtime tag position missing clear", drift)
	}
	for _, position := range []string{"pin project", "runtime tag"} {
		d := routeVerbDispatchers[position]
		dispatched, problem := dispatcherVerbs(t, filepath.Join("..", ".."), d)
		if problem != "" {
			t.Fatal(problem)
		}
		if clean := routeVerbDrift(position, catalogRouteVerbUsage(t)[position], d, dispatched); clean != "" {
			t.Errorf("route verb guard over the real catalog: %s", clean)
		}
	}
}
