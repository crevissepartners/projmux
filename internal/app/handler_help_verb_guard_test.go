package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// helpVerbRouteHelpPrinter is the one call a handler `help` branch makes: it
// renders the catalog help `projmux <route> --help` prints.
const helpVerbRouteHelpPrinter = "printRouteHelp"

// helpVerbSite is one route a handler `help` branch answers. argv is the full
// public spelling that reaches the branch, one the help boundary does not
// answer first.
type helpVerbSite struct {
	argv []string
}

// helpVerbSites is keyed by "<file>: <route>", one row per printRouteHelp call
// inside a help branch; the route's `--help` output is the reference its argv
// must reproduce. The set is closed against the source both ways: every help
// branch is reachable and listed with a spelling that reaches it, and a branch
// no argv reaches is deleted rather than listed. A public parent has no row:
// the help boundary answers its `help`, `--help`, and `-h`, and the same
// spellings after the bare `--` are payload it refuses as a usage error
// (TestPublicParentDashHelpIsAUsageError).
var helpVerbSites = map[string]helpVerbSite{
	"ai_ingest.go: internal agent-hook ingest": {argv: []string{"internal", "agent-hook", "ingest", "help"}},
	"ai_integrate.go: agent integrate":         {argv: []string{"agent", "integrate", "help"}},
	"preview.go: internal preview":             {argv: []string{"internal", "preview", "help"}},
	"session_popup.go: internal session-popup": {argv: []string{"internal", "session-popup", "help"}},
	"status.go: internal status":               {argv: []string{"internal", "status", "help"}},
	"statusbar.go: internal statusbar":         {argv: []string{"internal", "statusbar", "help"}},
	"tmux.go: internal tmux":                   {argv: []string{"internal", "tmux", "help"}},
}

// helpVerbExceptions are `help` comparisons that are not a route help verb,
// keyed by "<file>: <func>". Every row needs a reason, and a row no branch
// matches is stale.
var helpVerbExceptions = map[string]string{
	"interactive_run_shell.go: argvRequestsHelp": "detects a help request in the argv an interactive shell forwards (the word and the -h/--help flag names); it answers no route itself",
	"statusbar.go: parseStatusbarClickArgs":      "a `--help`/`-h` flag name inside the status bar click parser, refused as a usage error; not a help verb",
}

// helpVerbBranch is one `help` branch found in the source.
type helpVerbBranch struct {
	pos, file, fn string
	body          []ast.Stmt
}

// helpVerbScan is what the scan found in a set of files.
type helpVerbScan struct {
	// sites maps "<file>: <route>" to the position of its printRouteHelp call.
	sites map[string]string
	// excepted records the exception keys a branch matched.
	excepted map[string]bool
	problems []string
}

// helpVerbLiteral reports whether expr is the string literal "help".
func helpVerbLiteral(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	value, _ := strconv.Unquote(lit.Value)
	return value == "help"
}

// helpVerbCondition reports whether cond compares something with "help".
func helpVerbCondition(cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(node ast.Node) bool {
		if bin, ok := node.(*ast.BinaryExpr); ok && bin.Op == token.EQL && (helpVerbLiteral(bin.X) || helpVerbLiteral(bin.Y)) {
			found = true
		}
		return !found
	})
	return found
}

// helpVerbFuncName names a function declaration as "name" or "(*T).name".
func helpVerbFuncName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return decl.Name.Name
	}
	return "(" + types.ExprString(decl.Recv.List[0].Type) + ")." + decl.Name.Name
}

// helpVerbBranches returns every switch case listing "help" and every if
// statement comparing with "help", with the function that holds it.
func helpVerbBranches(fset *token.FileSet, name string, file *ast.File) []helpVerbBranch {
	var branches []helpVerbBranch
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CaseClause:
				if slices.ContainsFunc(node.List, helpVerbLiteral) {
					branches = append(branches, helpVerbBranch{pos: fset.Position(node.Pos()).String(), file: name, fn: helpVerbFuncName(fn), body: node.Body})
				}
			case *ast.IfStmt:
				if helpVerbCondition(node.Cond) {
					branches = append(branches, helpVerbBranch{pos: fset.Position(node.Pos()).String(), file: name, fn: helpVerbFuncName(fn), body: node.Body.List})
				}
			}
			return true
		})
	}
	return branches
}

// helpVerbForbiddenCall reports whether name writes help text other than
// through printRouteHelp: a handler printer, a catalog writer, or a raw write.
func helpVerbForbiddenCall(name string) bool {
	if name == helpVerbRouteHelpPrinter {
		return false
	}
	return strings.HasPrefix(name, "print") || strings.HasPrefix(name, "fmt.Fprint") ||
		strings.HasPrefix(name, "cli.Write") || strings.HasPrefix(name, "cli.Render") ||
		name == "io.WriteString" || strings.HasSuffix(name, ".Write") || strings.HasSuffix(name, ".WriteString")
}

// scanHelpVerbs parses files (name → source; a nil source reads the file) and
// checks every help branch: it prints only through printRouteHelp, names its
// route as a literal the catalog resolves exactly, and writes nothing else.
func scanHelpVerbs(files map[string][]byte) helpVerbScan {
	scan := helpVerbScan{sites: map[string]string{}, excepted: map[string]bool{}}
	fset := token.NewFileSet()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var src any
		if files[name] != nil {
			src = files[name]
		}
		file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			scan.problems = append(scan.problems, "parse "+name+": "+err.Error())
			continue
		}
		for _, branch := range helpVerbBranches(fset, name, file) {
			if _, ok := helpVerbExceptions[branch.file+": "+branch.fn]; ok {
				scan.excepted[branch.file+": "+branch.fn] = true
				continue
			}
			printed := 0
			for _, stmt := range branch.body {
				ast.Inspect(stmt, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					callee := types.ExprString(call.Fun)
					if helpVerbForbiddenCall(callee) {
						scan.problems = append(scan.problems, branch.pos+": help branch in "+branch.fn+" writes through "+callee+"; it must print the catalog help through "+helpVerbRouteHelpPrinter)
						return true
					}
					if callee != helpVerbRouteHelpPrinter {
						return true
					}
					printed++
					if len(call.Args) != 2 {
						scan.problems = append(scan.problems, branch.pos+": "+helpVerbRouteHelpPrinter+" takes (w, route)")
						return true
					}
					if w := types.ExprString(call.Args[0]); w != "stdout" {
						scan.problems = append(scan.problems, branch.pos+": help branch in "+branch.fn+" prints to "+w+", want stdout")
					}
					lit, ok := call.Args[1].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						scan.problems = append(scan.problems, branch.pos+": "+helpVerbRouteHelpPrinter+" route "+types.ExprString(call.Args[1])+" is not a string literal")
						return true
					}
					route, _ := strconv.Unquote(lit.Value)
					tokens := strings.Fields(route)
					if path, _, ok := cli.Resolve(tokens); !ok || !slices.Equal(path, tokens) {
						scan.problems = append(scan.problems, branch.pos+": "+helpVerbRouteHelpPrinter+" names "+strconv.Quote(route)+", which the catalog does not resolve exactly")
						return true
					}
					scan.sites[filepath.Base(branch.file)+": "+route] = branch.pos
					return true
				})
			}
			if printed == 0 {
				scan.problems = append(scan.problems, branch.pos+": help branch in "+branch.fn+" does not print the catalog help through "+helpVerbRouteHelpPrinter)
			}
		}
	}
	return scan
}

// helpVerbSetProblems compares the scan with the site and exception tables,
// both ways.
func helpVerbSetProblems(scan helpVerbScan, sites map[string]helpVerbSite, exceptions map[string]string) []string {
	problems := slices.Clone(scan.problems)
	for key, pos := range scan.sites {
		if _, ok := sites[key]; !ok {
			problems = append(problems, pos+": help branch for "+key+" has no helpVerbSites row")
		}
	}
	for key, site := range sites {
		if _, ok := scan.sites[key]; !ok {
			problems = append(problems, key+": helpVerbSites row matches no help branch (stale)")
		}
		if len(site.argv) == 0 {
			problems = append(problems, key+": helpVerbSites row has no argv; a help branch no argv reaches should be deleted, not listed")
		}
		if len(site.argv) > 0 && cli.HelpRequested(site.argv) {
			problems = append(problems, key+": helpVerbSites row drives "+strconv.Quote(strings.Join(site.argv, " "))+", which the help boundary answers before the handler runs; drive a spelling that reaches the branch")
		}
	}
	for key, reason := range exceptions {
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, key+": helpVerbExceptions row has no reason")
		}
		if !scan.excepted[key] {
			problems = append(problems, key+": helpVerbExceptions row matches no help comparison (stale)")
		}
	}
	sort.Strings(problems)
	return problems
}

// TestHandlerHelpVerbsRenderTheCatalogHelp is the closed set: every `help`
// branch in internal/app prints only the catalog help of a literal route on
// stdout, and every branch is a row of helpVerbSites or helpVerbExceptions.
func TestHandlerHelpVerbsRenderTheCatalogHelp(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			files[name] = nil
		}
	}
	for _, problem := range helpVerbSetProblems(scanHelpVerbs(files), helpVerbSites, helpVerbExceptions) {
		t.Error(problem)
	}
}

// TestHandlerHelpVerbMatchesHelpFlag drives every reachable row through its
// argv, a spelling the help boundary does not answer, so the handler branch
// itself runs: it must write the bytes `<route> --help` writes, on stdout,
// exit 0, and nothing on stderr.
func TestHandlerHelpVerbMatchesHelpFlag(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	keys := make([]string, 0, len(helpVerbSites))
	for key := range helpVerbSites {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		site := helpVerbSites[key]
		if len(site.argv) == 0 {
			continue
		}
		_, routeText, _ := strings.Cut(key, ": ")
		route := strings.Fields(routeText)
		spelling := strings.Join(site.argv, " ")
		t.Run(spelling, func(t *testing.T) {
			var flagOut, flagErr bytes.Buffer
			if err := New().Run(append(route, "--help"), &flagOut, &flagErr); err != nil {
				t.Fatalf("%s --help: err = %v", routeText, err)
			}
			var verbOut, verbErr bytes.Buffer
			if err := New().Run(slices.Clone(site.argv), &verbOut, &verbErr); err != nil {
				t.Fatalf("%s: err = %v, want nil (stderr=%q)", spelling, err, verbErr.String())
			}
			if verbErr.Len() != 0 {
				t.Errorf("%s: stderr = %q, want empty", spelling, verbErr.String())
			}
			if verbOut.String() != flagOut.String() {
				t.Errorf("%s: stdout differs from %s --help\n%s:\n%s\n--help:\n%s", spelling, routeText, spelling, verbOut.String(), flagOut.String())
			}
		})
	}
}

// TestHandlerHelpVerbGuardDetectsDrift is the negative control: a help branch
// that goes back to a handler copy, prints on stderr, names a route the
// catalog lacks, or has no row, a row whose argv the help boundary answers
// before the handler runs, a row with no argv, and a stale row or exception,
// are each reported; a row driving a leaf's bare `help` operand, which the
// help boundary leaves to the handler, is not.
func TestHandlerHelpVerbGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	src := []byte(`package app

func (c *windowCommand) Run(args []string, stdout, stderr io.Writer) error {
	switch args[0] {
	case "help", "--help", "-h":
		printRouteUsage(stdout, "window")
		return nil
	}
	switch args[0] {
	case "help":
		return printRouteHelp(stderr, "update")
	}
	if args[0] == "help" {
		return printRouteHelp(stdout, "no such route")
	}
	switch args[0] {
	case "help":
		return printRouteHelp(stdout, "attention")
	}
	switch args[0] {
	case "help":
		return printRouteHelp(stdout, "agent integrate")
	}
	return nil
}
`)
	scan := scanHelpVerbs(map[string][]byte{"recent_window.go": src})
	problems := strings.Join(helpVerbSetProblems(scan, map[string]helpVerbSite{
		"recent_window.go: update":          {argv: []string{"update", "help"}},
		"recent_window.go: window":          {argv: []string{"window", "--", "help"}},
		"recent_window.go: agent integrate": {argv: []string{"agent", "integrate", "help"}},
		"recent_window.go: gone":            {},
	}, map[string]string{"recent_window.go: gone": "stale"}), "\n")
	for _, want := range []string{
		"help branch in (*windowCommand).Run writes through printRouteUsage",
		"help branch in (*windowCommand).Run prints to stderr, want stdout",
		`printRouteHelp names "no such route", which the catalog does not resolve exactly`,
		"help branch for recent_window.go: attention has no helpVerbSites row",
		"recent_window.go: window: helpVerbSites row matches no help branch (stale)",
		"recent_window.go: gone: helpVerbExceptions row matches no help comparison (stale)",
		"recent_window.go: gone: helpVerbSites row has no argv; a help branch no argv reaches should be deleted, not listed",
		`recent_window.go: update: helpVerbSites row drives "update help", which the help boundary answers before the handler runs`,
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("guard problems do not report %q:\n%s", want, problems)
		}
	}
	if key := "recent_window.go: agent integrate"; strings.Contains(problems, key+": helpVerbSites row drives") {
		t.Errorf("guard reports the leaf help-operand row %s as answered by the help boundary:\n%s", key, problems)
	}
}

// TestHookEventsNoteListsSupportedEvents pins the catalog `hook` note, which
// `hook --help`, `hook help`, and every hook refusal print, to the events the
// hook runner supports.
func TestHookEventsNoteListsSupportedEvents(t *testing.T) {
	t.Parallel()
	want := "Events:\n  " + supportedHookEventList()
	if notes := cli.RouteNotes("hook"); !slices.Contains(notes, want) {
		t.Errorf("catalog hook notes = %q, want a note %q", notes, want)
	}
}
