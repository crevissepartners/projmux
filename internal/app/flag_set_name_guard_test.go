package app

import (
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

// flagSetNameException is one public flag parse site whose FlagSet name the
// guard cannot read statically, or one hidden site whose route has no catalog
// node. routes lists the catalog paths a dynamic name takes at run time; each
// must resolve exactly. An uncatalogued hidden site leaves routes empty.
type flagSetNameException struct {
	routes []string
	reason string
}

// flagSetNameDynamicReason explains the rows whose name is built from the
// dispatched resource kind or noun token.
const flagSetNameDynamicReason = "the name is the canonical spelling built from the dispatched kind or noun token, so it takes one catalog path per token"

// flagSetNameExceptions is keyed by flagParseGuardSite.key(). It is closed: a
// row no site uses is stale.
var flagSetNameExceptions = map[string]flagSetNameException{
	`internal/app/agent_message.go (*agentCommand).runMessageSend spelling`:                           {routes: []string{"agent message send"}, reason: "the name comes from the spelling argument runMessageSend is handed"},
	`internal/app/agent_message.go (*agentCommand).runMessageStatus spelling`:                         {routes: []string{"agent message status"}, reason: "the name comes from the spelling argument runMessageStatus is handed"},
	`internal/app/agent_message.go (*agentCommand).runWait spelling`:                                  {routes: []string{"agent wait"}, reason: "the name comes from the spelling argument runWait is handed"},
	`internal/app/agent_persona.go parseAgentPersonaArgs request.spelling`:                            {routes: []string{"agent persona attach", "agent persona detach", "agent instructions attach", "agent instructions detach"}, reason: flagSetNameDynamicReason},
	`internal/app/agent_question.go parseAgentQuestionArgs request.spelling`:                          {routes: []string{"agent question enable", "agent question disable", "agent question list", "agent question answer"}, reason: flagSetNameDynamicReason},
	`internal/app/codex_broker_runtime.go (*codexBrokerCommand).runServe internal codex-broker serve`: {reason: "hidden broker runtime: the catalog lists it only in the internal Usage, with no route node"},
	`internal/app/codex_broker_runtime.go (*codexBrokerCommand).runProbe internal codex-broker probe`: {reason: "hidden broker runtime: the catalog lists it only in the internal Usage, with no route node"},
	`internal/app/create_resource.go parseResourceCreateFlags spelling`:                               {routes: []string{"create window", "create pane", "create agent", "create codex", "create claude", "create antigravity"}, reason: flagSetNameDynamicReason},
	`internal/app/delete.go (*deleteCommand).runKind spelling`:                                        {routes: []string{"delete project", "delete window", "delete pane", "delete agent", "unregister project"}, reason: flagSetNameDynamicReason},
	`internal/app/describe.go (*describeCommand).runKind spelling`:                                    {routes: []string{"describe project", "describe window", "describe pane", "describe agent"}, reason: flagSetNameDynamicReason},
	`internal/app/focus.go parseCanonicalFocusArgs spelling`:                                          {routes: []string{"focus project", "focus window", "focus pane"}, reason: flagSetNameDynamicReason},
	`internal/app/get.go (*getCommand).runList spelling`:                                              {routes: []string{"get projects", "get windows", "get panes", "get agents"}, reason: flagSetNameDynamicReason},
	`internal/app/get_runtime.go (*getCommand).runRuntime spelling`:                                   {routes: []string{"get runtime sessions", "get runtime windows", "get runtime panes"}, reason: flagSetNameDynamicReason},
	`internal/app/label.go (*labelCommand).runKind spelling`:                                          {routes: []string{"label project", "label window", "label pane", "label agent"}, reason: flagSetNameDynamicReason},
	`internal/app/persona.go parsePersonaArgs param fs of parsePersonaArgs`:                           {routes: []string{"persona list", "persona show", "persona edit", "persona set", "persona delete", "instructions list", "instructions show", "instructions edit", "instructions set", "instructions delete", "profile list", "profile show", "profile set", "profile delete"}, reason: "parsePersonaArgs parses a FlagSet its callers name `<noun> <verb>`"},
	`internal/app/project_lifecycle_verbs.go (*projectLifecycleCommand).runProject spelling`:          {routes: []string{"open project", "start project", "stop project"}, reason: flagSetNameDynamicReason},
	`internal/app/rename.go (*renameCommand).runKind spelling`:                                        {routes: []string{"rename project", "rename window", "rename pane", "rename agent"}, reason: flagSetNameDynamicReason},
}

// flagSetNameEval evaluates the string a FlagSet name expression takes. It
// follows string literals, `+` concatenation, local and package constants, a
// local assigned once, and a function parameter through every same-package
// call of that function; anything else is not statically known.
type flagSetNameEval struct {
	fset   *token.FileSet
	files  map[string]*ast.File // repo-relative name -> file
	pkgOf  map[string]string    // repo-relative name -> import path
	consts map[string]map[string]ast.Expr
	funcs  map[string][]flagSetNameFunc // import path -> every function declaration
}

type flagSetNameFunc struct {
	file string
	decl *ast.FuncDecl
}

func newFlagSetNameEval(pkgs []flagParseGuardPackage) (*flagSetNameEval, []string) {
	fset := token.NewFileSet()
	e := &flagSetNameEval{fset: fset, files: map[string]*ast.File{}, pkgOf: map[string]string{}, consts: map[string]map[string]ast.Expr{}, funcs: map[string][]flagSetNameFunc{}}
	var problems []string
	for _, pkg := range pkgs {
		if !pkg.scan {
			continue
		}
		e.consts[pkg.importPath] = map[string]ast.Expr{}
		for _, f := range pkg.files {
			file, err := parser.ParseFile(fset, f.name, f.src, parser.SkipObjectResolution)
			if err != nil {
				problems = append(problems, "parse "+f.name+": "+err.Error())
				continue
			}
			e.files[f.name] = file
			e.pkgOf[f.name] = pkg.importPath
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					if d.Tok != token.CONST {
						continue
					}
					for _, spec := range d.Specs {
						vs := spec.(*ast.ValueSpec)
						for i, name := range vs.Names {
							if i < len(vs.Values) {
								e.consts[pkg.importPath][name.Name] = vs.Values[i]
							}
						}
					}
				case *ast.FuncDecl:
					e.funcs[pkg.importPath] = append(e.funcs[pkg.importPath], flagSetNameFunc{file: f.name, decl: d})
				}
			}
		}
	}
	return e, problems
}

// funcNamed finds the declaration flagParseGuardFuncName prints as name.
func (e *flagSetNameEval) funcNamed(file, name string) *ast.FuncDecl {
	for _, fn := range e.funcs[e.pkgOf[file]] {
		if fn.file == file && flagParseGuardFuncName(fn.decl) == name {
			return fn.decl
		}
	}
	return nil
}

// flagSetNameArg returns the name argument of a flag.NewFlagSet call, or nil.
func flagSetNameArg(node ast.Node) ast.Expr {
	call, ok := node.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewFlagSet" {
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "flag" {
			return call.Args[0]
		}
	}
	return nil
}

// flagSetNameSpelling is the name the flag parse analyzer records for expr.
func flagSetNameSpelling(expr ast.Expr) string {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		name, _ := strconv.Unquote(lit.Value)
		return name
	}
	return types.ExprString(expr)
}

// siteValues evaluates every NewFlagSet name in site's function that the
// analyzer recorded as site.flagSet.
func (e *flagSetNameEval) siteValues(site flagParseGuardSite) ([]string, bool) {
	fn := e.funcNamed(site.file, site.fn)
	if fn == nil {
		return nil, false
	}
	var values []string
	found, ok := false, true
	ast.Inspect(fn, func(node ast.Node) bool {
		arg := flagSetNameArg(node)
		if arg == nil || flagSetNameSpelling(arg) != site.flagSet {
			return true
		}
		found = true
		got, known := e.eval(site.file, fn, arg, 0)
		ok = ok && known
		values = append(values, got...)
		return true
	})
	return values, found && ok
}

func (e *flagSetNameEval) eval(file string, fn *ast.FuncDecl, expr ast.Expr, depth int) ([]string, bool) {
	if depth > 4 {
		return nil, false
	}
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return nil, false
		}
		value, err := strconv.Unquote(x.Value)
		return []string{value}, err == nil
	case *ast.ParenExpr:
		return e.eval(file, fn, x.X, depth)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, false
		}
		left, ok := e.eval(file, fn, x.X, depth)
		if !ok {
			return nil, false
		}
		right, ok := e.eval(file, fn, x.Y, depth)
		if !ok {
			return nil, false
		}
		var out []string
		for _, l := range left {
			for _, r := range right {
				out = append(out, l+r)
			}
		}
		return out, true
	case *ast.Ident:
		return e.evalIdent(file, fn, x.Name, depth)
	}
	return nil, false
}

func (e *flagSetNameEval) evalIdent(file string, fn *ast.FuncDecl, name string, depth int) ([]string, bool) {
	if fn != nil {
		if idx := flagSetNameParamIndex(fn, name); idx >= 0 {
			return e.evalParam(file, fn, idx, depth)
		}
		var defs []ast.Expr
		reassigned := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name != name {
						continue
					}
					if n.Tok != token.DEFINE || len(n.Lhs) != len(n.Rhs) {
						reassigned = true
						continue
					}
					defs = append(defs, n.Rhs[i])
				}
			case *ast.ValueSpec:
				for i, id := range n.Names {
					if id.Name == name && i < len(n.Values) {
						defs = append(defs, n.Values[i])
					}
				}
			}
			return true
		})
		if reassigned || len(defs) > 1 {
			return nil, false
		}
		if len(defs) == 1 {
			return e.eval(file, fn, defs[0], depth+1)
		}
	}
	if value, ok := e.consts[e.pkgOf[file]][name]; ok {
		return e.eval(file, nil, value, depth+1)
	}
	return nil, false
}

// flagSetNameParamIndex is the position of parameter name in fn, or -1.
func flagSetNameParamIndex(fn *ast.FuncDecl, name string) int {
	idx := 0
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			idx++
			continue
		}
		for _, id := range field.Names {
			if id.Name == name {
				return idx
			}
			idx++
		}
	}
	return -1
}

// evalParam evaluates parameter idx of fn at every same-package call of a
// function or method with fn's name and arity.
func (e *flagSetNameEval) evalParam(file string, fn *ast.FuncDecl, idx, depth int) ([]string, bool) {
	var values []string
	calls, ok := 0, true
	params := 0
	for _, field := range fn.Type.Params.List {
		params += max(len(field.Names), 1)
	}
	for _, caller := range e.funcs[e.pkgOf[file]] {
		ast.Inspect(caller.decl, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			callee := ""
			switch f := call.Fun.(type) {
			case *ast.Ident:
				callee = f.Name
			case *ast.SelectorExpr:
				callee = f.Sel.Name
			}
			// A same-named function or method with another arity is a
			// different declaration (updateCommand.runApply next to
			// tmuxCommand.runApply).
			if callee != fn.Name.Name || len(call.Args) != params {
				return true
			}
			calls++
			got, known := e.eval(caller.file, caller.decl, call.Args[idx], depth+1)
			ok = ok && known
			values = append(values, got...)
			return true
		})
	}
	return values, ok && calls > 0
}

// flagSetNameProblems checks every public flag parse site: the FlagSet name
// must resolve exactly to a catalog route path, because it is the `Usage of
// <name>:` header a flag error prints. Sites in the hidden exception table of
// the exit-code guard keep their historical names. A site whose name is not
// statically known, or a hidden site without a catalog node, needs a row in
// exceptions. It returns the problems and the exception keys used.
func flagSetNameProblems(eval *flagSetNameEval, report flagParseGuardReport, hidden map[string]flagParseGuardException, exceptions map[string]flagSetNameException) ([]string, map[string]bool) {
	var problems []string
	used := map[string]bool{}
	for _, site := range report.sites {
		if _, ok := hidden[site.key()]; ok {
			continue
		}
		if row, ok := exceptions[site.key()]; ok {
			used[site.key()] = true
			for _, route := range row.routes {
				tokens := strings.Fields(route)
				if path, _, ok := cli.Resolve(tokens); !ok || !slices.Equal(path, tokens) {
					problems = append(problems, site.describe()+": exception route "+strconv.Quote(route)+" is not a catalog route path")
				}
			}
			continue
		}
		values, known := eval.siteValues(site)
		if !known {
			problems = append(problems, site.describe()+": the FlagSet name is not statically known; name it after its catalog route, or add key "+strconv.Quote(site.key())+" to flagSetNameExceptions with the routes it takes")
			continue
		}
		for _, name := range values {
			tokens := strings.Fields(name)
			path, _, ok := cli.Resolve(tokens)
			if !ok || !slices.Equal(path, tokens) || name != strings.Join(tokens, " ") {
				route := strings.Join(path, " ")
				if route == "" {
					route = "(none)"
				}
				problems = append(problems, site.describe()+": FlagSet name "+strconv.Quote(name)+" is not a catalog route path (the catalog resolves route "+strconv.Quote(route)+"); a flag error prints `Usage of "+name+":`")
			}
		}
	}
	sort.Strings(problems)
	return problems, used
}

// TestPublicFlagSetNamesAreCatalogRoutes holds every public flag parse site in
// internal/app/** (the closed site set of the exit-code guard, minus its
// hidden exceptions) to a FlagSet named after its catalog route path, so the
// `Usage of <name>:` header of a flag error names a real route.
func TestPublicFlagSetNamesAreCatalogRoutes(t *testing.T) {
	t.Parallel()
	pkgs := flagParseGuardLoadRepo(t, filepath.Join("..", ".."))
	report := flagParseGuardAnalyze(pkgs)
	eval, problems := newFlagSetNameEval(pkgs)
	problems = append(problems, report.problems...)
	checked, used := flagSetNameProblems(eval, report, flagParseGuardExceptions, flagSetNameExceptions)
	for _, problem := range append(problems, checked...) {
		t.Error(problem)
	}
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range flagSetNameExceptions {
			if !yield(key) {
				return
			}
		}
	}) {
		row := flagSetNameExceptions[key]
		if strings.TrimSpace(row.reason) == "" {
			t.Errorf("exception %q: missing reason", key)
		}
		if len(row.routes) == 0 && !strings.Contains(key, " internal ") {
			t.Errorf("exception %q: a row without routes must be a hidden `internal ...` FlagSet", key)
		}
		if !used[key] {
			t.Errorf("exception %q is stale: no public flag parse site has that key", key)
		}
	}
	public := 0
	for _, site := range report.sites {
		if _, ok := flagParseGuardExceptions[site.key()]; !ok {
			public++
		}
	}
	if public < 85 {
		t.Errorf("checked %d public flag parse sites, want at least 85; the site collection has regressed", public)
	}
}

// TestPublicFlagSetNameGuardDetectsDrift is the negative control: a FlagSet
// named after a retired spelling, one whose name is not statically known,
// and a stale-route exception are each reported with the route and name;
// names reached through constants and parameters pass.
func TestPublicFlagSetNameGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	const pkgPath = "example.test/guard/internal/app"
	const src = `package app

import (
	"errors"
	"flag"
)

type UsageError struct{ Message string }

func (e *UsageError) Error() string            { return e.Message }
func (e *UsageError) MetadataUsageError() bool { return true }

func usageError(message string) error { return &UsageError{Message: message} }

const canonicalPush = "create notification"

func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func retired(args []string) error {
	fs := flag.NewFlagSet("notify push", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func dynamic(token string, args []string) error {
	fs := flag.NewFlagSet("get "+token, flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func viaConst(args []string) error {
	const spelling = canonicalPush
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func viaParam(route string, args []string) error {
	fs := flag.NewFlagSet(route, flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func callers(args []string) {
	_ = viaParam("config render standalone", args)
	_ = viaParam("config render app", args)
}

func tabled(args []string) error {
	fs := flag.NewFlagSet("create notification", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageError(err.Error())
	}
	return nil
}
`
	pkgs := []flagParseGuardPackage{{importPath: pkgPath, scan: true, files: []flagParseGuardFile{{name: "synthetic/app.go", src: []byte(src)}}}}
	report := flagParseGuardAnalyze(pkgs)
	if len(report.problems) != 0 {
		t.Fatalf("negative control: analyzer problems: %v", report.problems)
	}
	eval, problems := newFlagSetNameEval(pkgs)
	if len(problems) != 0 {
		t.Fatalf("negative control: evaluator problems: %v", problems)
	}
	failures, used := flagSetNameProblems(eval, report, nil, map[string]flagSetNameException{
		"synthetic/app.go tabled create notification": {routes: []string{"notify push"}, reason: "retired spelling"},
	})
	joined := strings.Join(failures, "\n")
	for _, want := range []string{
		`func retired, FlagSet "notify push" (parse wrapper parse): FlagSet name "notify push" is not a catalog route path (the catalog resolves route "(none)")`,
		`func dynamic, FlagSet "\"get \" + token" (parse wrapper parse): the FlagSet name is not statically known`,
		`func tabled, FlagSet "create notification" (parse wrapper parse): exception route "notify push" is not a catalog route path`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("negative control: missing failure %q in:\n%s", want, joined)
		}
	}
	for _, clean := range []string{"func viaConst,", "func viaParam,"} {
		if strings.Contains(joined, clean) {
			t.Errorf("negative control: %q must pass:\n%s", clean, joined)
		}
	}
	if len(failures) != 3 {
		t.Errorf("negative control: got %d failures, want 3:\n%s", len(failures), joined)
	}
	if !used["synthetic/app.go tabled create notification"] {
		t.Error("negative control: the tabled site did not use its exception row")
	}
}
