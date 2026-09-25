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

// helpVerbSite is one route a handler `help` branch answers. argv is the
// public spelling that reaches the branch with `help` appended; a site no
// spelling reaches carries the reason instead, so it stays in the closed set
// rather than dropping out of it.
type helpVerbSite struct {
	argv        []string
	unreachable string
	// wordOnly marks a branch that must list only the `help` word: the help
	// boundary answers `<argv> --help` and `<argv> -h` before the handler runs,
	// and a leading `--` makes args[0] `--`, so a flag label there is dead.
	wordOnly bool
}

// helpVerbSites is keyed by "<file>: <route>", one row per printRouteHelp call
// inside a help branch. The set is closed against the source both ways.
var helpVerbSites = map[string]helpVerbSite{
	"ai.go: agent status":                      {unreachable: "`agent status` dispatches to agentCommand; no route forwards `status` into the ai handler"},
	"ai.go: agent topic":                       {unreachable: "`agent topic` dispatches to agentCommand; no route forwards `topic` into the ai handler"},
	"ai_ingest.go: internal agent-hook ingest": {argv: []string{"internal", "agent-hook", "ingest"}},
	"ai_integrate.go: agent integrate":         {argv: []string{"agent", "integrate"}},
	"attach.go: attach":                        {unreachable: "the legacy `attach` gate admits only `project`, and `runtime attach` forwards with the `auto` prefix"},
	"attention.go: attention":                  {argv: []string{"attention"}, wordOnly: true},
	"diagnostics.go: diagnostics":              {argv: []string{"diagnostics"}, wordOnly: true},
	"hook.go: hook":                            {argv: []string{"hook"}},
	"kill.go: runtime stop":                    {unreachable: "`runtime stop` forwards with the `tagged` prefix"},
	"notify.go: notification":                  {unreachable: "`notification` dispatches its children itself; no route forwards a bare verb into the notify handler"},
	"persona.go: instructions":                 {argv: []string{"instructions"}, wordOnly: true},
	"persona.go: persona":                      {argv: []string{"persona"}, wordOnly: true},
	"pin.go: pin":                              {unreachable: "the legacy `pin` gate admits only `project`"},
	"pin.go: pin project":                      {argv: []string{"pin", "project"}},
	"preview.go: internal preview":             {argv: []string{"internal", "preview"}},
	"profile.go: profile":                      {argv: []string{"profile"}, wordOnly: true},
	"prune.go: prune":                          {unreachable: "the legacy `prune` gate admits only `agent` and `project`, and `runtime prune` forwards with the `ephemeral` prefix"},
	"recent_window.go: window":                 {argv: []string{"window"}},
	"session_popup.go: internal session-popup": {argv: []string{"internal", "session-popup"}},
	"status.go: internal status":               {argv: []string{"internal", "status"}},
	"statusbar.go: internal statusbar":         {argv: []string{"internal", "statusbar"}},
	"tag.go: runtime tag":                      {argv: []string{"runtime", "tag"}},
	"tmux.go: internal tmux":                   {argv: []string{"internal", "tmux"}},
	"update.go: update":                        {argv: []string{"update"}, wordOnly: true},
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
	// flags are the help flag literals ("--help", "-h") the same case clause
	// lists beside "help"; an if statement carries none.
	flags []string
}

// helpVerbScan is what the scan found in a set of files.
type helpVerbScan struct {
	// sites maps "<file>: <route>" to the position of its printRouteHelp call.
	sites map[string]string
	// flags maps "<file>: <route>" to the help flag labels its branch lists.
	flags map[string][]string
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

// helpVerbFlagLabel returns the help flag a case label spells, or "".
func helpVerbFlagLabel(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	value, _ := strconv.Unquote(lit.Value)
	if value == "--help" || value == "-h" {
		return value
	}
	return ""
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
					var flags []string
					for _, label := range node.List {
						if flag := helpVerbFlagLabel(label); flag != "" {
							flags = append(flags, flag)
						}
					}
					branches = append(branches, helpVerbBranch{pos: fset.Position(node.Pos()).String(), file: name, fn: helpVerbFuncName(fn), body: node.Body, flags: flags})
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
	scan := helpVerbScan{sites: map[string]string{}, flags: map[string][]string{}, excepted: map[string]bool{}}
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
					scan.flags[filepath.Base(branch.file)+": "+route] = branch.flags
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
		if (len(site.argv) == 0) == (site.unreachable == "") {
			problems = append(problems, key+": helpVerbSites row needs exactly one of argv or an unreachable reason")
		}
		if site.wordOnly && site.unreachable != "" {
			problems = append(problems, key+": helpVerbSites row is wordOnly but unreachable; only a reachable branch can be narrowed to the help word")
		}
		if pos, ok := scan.sites[key]; ok && site.wordOnly {
			for _, label := range scan.flags[key] {
				problems = append(problems, pos+": help branch for "+key+" lists "+label+", which the help boundary answers before the handler runs")
			}
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

// TestHandlerHelpVerbMatchesHelpFlag drives every reachable row: `<route>
// help` must write the bytes `<route> --help` writes, on stdout, exit 0, and
// nothing on stderr.
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
		route := strings.Join(site.argv, " ")
		t.Run(route, func(t *testing.T) {
			var flagOut, flagErr bytes.Buffer
			if err := New().Run(append(slices.Clone(site.argv), "--help"), &flagOut, &flagErr); err != nil {
				t.Fatalf("%s --help: err = %v", route, err)
			}
			var verbOut, verbErr bytes.Buffer
			if err := New().Run(append(slices.Clone(site.argv), "help"), &verbOut, &verbErr); err != nil {
				t.Fatalf("%s help: err = %v, want nil (stderr=%q)", route, err, verbErr.String())
			}
			if verbErr.Len() != 0 {
				t.Errorf("%s help: stderr = %q, want empty", route, verbErr.String())
			}
			if verbOut.String() != flagOut.String() {
				t.Errorf("%s help: stdout differs from %s --help\nhelp:\n%s\n--help:\n%s", route, route, verbOut.String(), flagOut.String())
			}
		})
	}
}

// TestHandlerHelpVerbGuardDetectsDrift is the negative control: a help branch
// that goes back to a handler copy, prints on stderr, names a route the
// catalog lacks, or has no row, a wordOnly branch that lists a help flag, a
// wordOnly row with no reachable spelling, and a stale row, are each reported.
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
	case "help", "--help", "-h":
		return printRouteHelp(stdout, "profile")
	}
	return nil
}
`)
	scan := scanHelpVerbs(map[string][]byte{"recent_window.go": src})
	problems := strings.Join(helpVerbSetProblems(scan, map[string]helpVerbSite{
		"recent_window.go: update":  {argv: []string{"update"}},
		"recent_window.go: window":  {argv: []string{"window"}},
		"recent_window.go: profile": {argv: []string{"profile"}, wordOnly: true},
		"recent_window.go: gone":    {unreachable: "no route forwards here", wordOnly: true},
	}, map[string]string{"recent_window.go: gone": "stale"}), "\n")
	for _, want := range []string{
		"help branch in (*windowCommand).Run writes through printRouteUsage",
		"help branch in (*windowCommand).Run prints to stderr, want stdout",
		`printRouteHelp names "no such route", which the catalog does not resolve exactly`,
		"help branch for recent_window.go: attention has no helpVerbSites row",
		"recent_window.go: window: helpVerbSites row matches no help branch (stale)",
		"recent_window.go: gone: helpVerbExceptions row matches no help comparison (stale)",
		"help branch for recent_window.go: profile lists --help, which the help boundary answers before the handler runs",
		"help branch for recent_window.go: profile lists -h, which the help boundary answers before the handler runs",
		"recent_window.go: gone: helpVerbSites row is wordOnly but unreachable",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("guard problems do not report %q:\n%s", want, problems)
		}
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
