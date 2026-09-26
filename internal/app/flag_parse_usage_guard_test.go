package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// flagParseGuardModule is the module path the on-disk sources are loaded under.
const flagParseGuardModule = "github.com/crevissepartners/projmux"

// flagParseGuardHiddenReason explains why a hidden plumbing route keeps its
// historical flag-error exit code instead of the public usage exit 2.
const flagParseGuardHiddenReason = "hidden plumbing: its exit code is consumed by generated tmux config, provider hooks or supervisors, not scripts (docs/hooks.md: the question hook exits 0 on any failure; Claude Code reads hook exit 2 as blocking)"

// flagParseGuardException is one parse site that may return a non-usage error
// and so keep its historical exit 1: the parse error itself when its FlagSet
// discards its output, or flagParseReported(err) when the flag package has
// already printed the reason on stderr. Only hidden `projmux internal ...`
// routes may appear here; the guard asserts it.
type flagParseGuardException struct {
	route  string
	reason string
}

// flagParseGuardExceptions is keyed by flagParseGuardSite.key(): the
// repo-relative file, the enclosing function, and the FlagSet name (the first
// argument of flag.NewFlagSet).
var flagParseGuardExceptions = map[string]flagParseGuardException{
	`internal/app/ai.go (*aiCommand).runPicker ai picker`:                                                       {route: "internal agent-pane picker", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go (*aiCommand).runIngest internal agent-hook ingest antigravity-hook`:              {route: "internal agent-hook ingest antigravity-hook", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go parseAIHookPaneArgument "internal agent-hook ingest " + route`:                   {route: "internal agent-hook ingest codex-hook|claude-hook", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go (*aiCommand).runIngestBell internal agent-hook ingest bell`:                      {route: "internal agent-hook ingest bell", reason: flagParseGuardHiddenReason},
	`internal/app/claude_question_hook.go (claudeQuestionHook).run "internal " + claudeQuestionHookRoute`:       {route: "internal claude-question-hook", reason: flagParseGuardHiddenReason},
	`internal/app/claude_permission_hook.go (claudePermissionHook).run "internal " + claudePermissionHookRoute`: {route: "internal claude-permission-hook", reason: flagParseGuardHiddenReason},
	`internal/app/claude_question_popup.go runClaudeQuestionPicker "internal " + claudeQuestionPickerRoute`:     {route: "internal claude-question-picker", reason: flagParseGuardHiddenReason},
	`internal/app/focus.go parseFocusArgs focus`:                                                                {route: "internal focus", reason: flagParseGuardHiddenReason},
	`internal/app/hook_trust_popup.go (*tmuxCommand).runHookTrustPromptWithReader tmux hook-trust-prompt`:       {route: "internal tmux hook-trust-prompt", reason: flagParseGuardHiddenReason},
	`internal/app/key_broker.go (*keyBrokerCommand).Run key-broker`:                                             {route: "internal key-broker", reason: flagParseGuardHiddenReason},
	`internal/app/preview.go (*previewCommand).Run preview`:                                                     {route: "internal preview", reason: flagParseGuardHiddenReason},
	`internal/app/session_popup.go (*sessionPopupCommand).Run session-popup`:                                    {route: "internal session-popup", reason: flagParseGuardHiddenReason},
	`internal/app/split_selection_continuation.go parseSplitSelectionArgs spelling`:                             {route: "internal agent-pane launch-selection", reason: flagParseGuardHiddenReason},
	`internal/app/status.go (*statusCommand).runNotify status notify`:                                           {route: "internal status notify", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).Run tmux`:                                                              {route: "internal tmux", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runAutosaveSessionState tmux autosave-session-state`:                   {route: "internal tmux autosave-session-state", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runConverge internal tmux converge`:                                    {route: "internal tmux converge", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runDeleteConfirmIntent tmux delete-confirm`:                            {route: "internal tmux delete-confirm", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runInstall tmux install`:                                               {route: "internal tmux install", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runInstallApp tmux install-app`:                                        {route: "internal tmux install-app", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runPaneMenuAction tmux pane-menu`:                                      {route: "internal tmux pane-menu", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runWindowCreateIntent tmux window-create`:                              {route: "internal tmux window-create", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runWindowDeleteIntent tmux window-delete`:                              {route: "internal tmux window-delete", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go parseRenameIntentArgs "tmux " + route`:                                                {route: "internal tmux window-rename|pane-rename", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go parseTmuxPopupToggleArgs tmux popup-toggle`:                                           {route: "internal tmux popup-toggle", reason: flagParseGuardHiddenReason},
	`internal/app/usagecmd/usage.go (*Command).RunStatus status usage`:                                          {route: "internal status usage", reason: flagParseGuardHiddenReason},
}

// flagParseGuardVerdict classifies the non-help error path of one parse site.
type flagParseGuardVerdict int

const (
	// flagParseGuardMarked: every non-help return constructs a usage marker.
	flagParseGuardMarked flagParseGuardVerdict = iota
	// flagParseGuardBare: every non-help return passes the parse error through.
	flagParseGuardBare
	// flagParseGuardReported: every non-help return constructs an error that
	// says its reason is already on stderr but carries no usage marker.
	flagParseGuardReported
	// flagParseGuardOther: anything else (nil, wrapped, discarded, no return).
	flagParseGuardOther
)

func (v flagParseGuardVerdict) String() string {
	switch v {
	case flagParseGuardMarked:
		return "usage-marked"
	case flagParseGuardBare:
		return "bare"
	case flagParseGuardReported:
		return "reported, not usage-marked"
	default:
		return "not usage-marked"
	}
}

// flagParseGuardSite is one place a FlagSet's parse error reaches handler
// code: a direct FlagSet.Parse call, or a call to a parse wrapper.
type flagParseGuardSite struct {
	file, fn, flagSet string
	line              int
	via               string // "" for a direct Parse, else the wrapper name
	verdict           flagParseGuardVerdict
	detail            string
	helpWrapped       bool // the flag.ErrHelp branch constructs a usage marker
	helpBranch        bool
	// marked counts the non-help returns that construct a usage marker, and
	// reported those whose error says the reason is already on stderr, with a
	// usage marker (flagParseError) or without one (flagParseReported).
	marked, reported int
	// output is where the FlagSet writes its reason and usage: "stderr" (any
	// writer but io.Discard, or the flag default), "discard", or a problem
	// description when the analyzer cannot tell.
	output string
	// reprints lists the usage printers the non-help error path calls, here or
	// in the error branch of a caller that hands its FlagSet to this site.
	reprints []string
	// usage says what the FlagSet prints after the reason: "catalog" (a
	// catalog Usage setter), "custom" (its own Usage assignment), "default"
	// (the flag package's `Usage of <name>:` listing), or a mix across the
	// callers that hand it a FlagSet.
	usage string
	// selfReported counts the non-help returns that call a constructor
	// printing the reason and the catalog Usage itself (usageRefusal).
	selfReported int
}

func (s flagParseGuardSite) key() string { return s.file + " " + s.fn + " " + s.flagSet }

func (s flagParseGuardSite) describe() string {
	via := "FlagSet.Parse"
	if s.via != "" {
		via = "parse wrapper " + s.via
	}
	return s.file + ":" + strconv.Itoa(s.line) + ": func " + s.fn + ", FlagSet " + strconv.Quote(s.flagSet) + " (" + via + ")"
}

// flagParseGuardFile is one source file of a flagParseGuardPackage.
type flagParseGuardFile struct {
	name string // repo-relative, used in reports
	src  []byte
}

// flagParseGuardPackage is one package handed to the analyzer. Marker types
// and marker constructors are derived from every package; parse sites are only
// collected from packages with scan set.
type flagParseGuardPackage struct {
	importPath string
	scan       bool
	files      []flagParseGuardFile
}

// flagParseGuardReport is the analyzer's result. problems lists every place
// the analyzer could not classify; the guard treats each as a failure so the
// site set stays closed.
type flagParseGuardReport struct {
	sites   []flagParseGuardSite
	markers []string
	ctors   []string
	// reportedMarkers and reportedCtors are the types declaring
	// `FailureReported() bool { return true }` and their constructors, usage
	// markers or not.
	reportedMarkers []string
	reportedCtors   []string
	wrappers        []string
	problems        []string
}

type flagParseGuardParsedFile struct {
	name       string
	pkg        string
	file       *ast.File
	fset       *token.FileSet
	imports    map[string]string // local name -> import path
	flagName   string
	errorsName string
	// locals maps every identifier that names a function-local binding
	// (parameter, result, receiver, := or var/const/type inside a body) to
	// that binding; an identifier absent here is package-level or an import.
	locals map[*ast.Ident]*flagParseGuardBinding
	// declares marks the identifiers that introduce a local binding.
	declares map[*ast.Ident]bool
}

// flagParseGuardBinding is one lexical local binding.
type flagParseGuardBinding struct{ name string }

// flagParseGuardResolver is a small lexical scope tracker over function
// bodies: it binds each identifier use to its local declaration.
type flagParseGuardResolver struct {
	pf     *flagParseGuardParsedFile
	scopes []map[string]*flagParseGuardBinding
}

func flagParseGuardResolve(pf *flagParseGuardParsedFile) {
	pf.locals = map[*ast.Ident]*flagParseGuardBinding{}
	pf.declares = map[*ast.Ident]bool{}
	r := &flagParseGuardResolver{pf: pf}
	for _, decl := range pf.file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			r.push()
			r.fields(d.Recv, true)
			r.funcType(d.Type, true)
			if d.Body != nil {
				r.stmts(d.Body.List)
			}
			r.pop()
		case *ast.GenDecl:
			// Package-level names stay unresolved; only walk initializers so
			// function literals in them get their own locals.
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					r.push()
					for _, v := range vs.Values {
						r.node(v)
					}
					r.pop()
				}
			}
		}
	}
}

func (r *flagParseGuardResolver) push() {
	r.scopes = append(r.scopes, map[string]*flagParseGuardBinding{})
}

func (r *flagParseGuardResolver) pop() { r.scopes = r.scopes[:len(r.scopes)-1] }

func (r *flagParseGuardResolver) declare(id *ast.Ident) {
	if id == nil || id.Name == "_" {
		return
	}
	top := r.scopes[len(r.scopes)-1]
	b := top[id.Name]
	if b == nil {
		// A := redeclaration in the same scope reuses the binding.
		b = &flagParseGuardBinding{name: id.Name}
		top[id.Name] = b
	}
	r.pf.locals[id] = b
	r.pf.declares[id] = true
}

func (r *flagParseGuardResolver) use(id *ast.Ident) {
	for i := len(r.scopes) - 1; i >= 0; i-- {
		if b := r.scopes[i][id.Name]; b != nil {
			r.pf.locals[id] = b
			return
		}
	}
}

func (r *flagParseGuardResolver) fields(list *ast.FieldList, declare bool) {
	if list == nil {
		return
	}
	for _, f := range list.List {
		r.node(f.Type)
	}
	if declare {
		for _, f := range list.List {
			for _, name := range f.Names {
				r.declare(name)
			}
		}
	}
}

func (r *flagParseGuardResolver) funcType(ft *ast.FuncType, declare bool) {
	r.fields(ft.TypeParams, declare)
	r.fields(ft.Params, declare)
	r.fields(ft.Results, declare)
}

func (r *flagParseGuardResolver) stmts(list []ast.Stmt) {
	for _, stmt := range list {
		r.node(stmt)
	}
}

func (r *flagParseGuardResolver) exprs(list []ast.Expr) {
	for _, e := range list {
		r.node(e)
	}
}

func (r *flagParseGuardResolver) node(n ast.Node) {
	switch n := n.(type) {
	case nil:
	case *ast.Ident:
		r.use(n)
	case *ast.SelectorExpr:
		r.node(n.X)
	case *ast.KeyValueExpr:
		if _, isName := n.Key.(*ast.Ident); !isName {
			r.node(n.Key)
		}
		r.node(n.Value)
	case *ast.FuncLit:
		r.push()
		r.funcType(n.Type, true)
		r.stmts(n.Body.List)
		r.pop()
	case *ast.FuncType:
		r.funcType(n, false)
	case *ast.StructType:
		r.fields(n.Fields, false)
	case *ast.InterfaceType:
		r.fields(n.Methods, false)
	case *ast.BlockStmt:
		r.push()
		r.stmts(n.List)
		r.pop()
	case *ast.AssignStmt:
		r.exprs(n.Rhs)
		for _, lhs := range n.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && n.Tok == token.DEFINE {
				r.declare(id)
			} else {
				r.node(lhs)
			}
		}
	case *ast.DeclStmt:
		for _, spec := range n.Decl.(*ast.GenDecl).Specs {
			switch spec := spec.(type) {
			case *ast.ValueSpec:
				r.node(spec.Type)
				r.exprs(spec.Values)
				for _, name := range spec.Names {
					r.declare(name)
				}
			case *ast.TypeSpec:
				r.declare(spec.Name)
				r.fields(spec.TypeParams, false)
				r.node(spec.Type)
			}
		}
	case *ast.IfStmt:
		r.push()
		r.node(n.Init)
		r.node(n.Cond)
		r.node(n.Body)
		r.node(n.Else)
		r.pop()
	case *ast.ForStmt:
		r.push()
		r.node(n.Init)
		r.node(n.Cond)
		r.node(n.Post)
		r.node(n.Body)
		r.pop()
	case *ast.RangeStmt:
		r.node(n.X)
		r.push()
		for _, e := range []ast.Expr{n.Key, n.Value} {
			if id, ok := e.(*ast.Ident); ok && n.Tok == token.DEFINE {
				r.declare(id)
			} else {
				r.node(e)
			}
		}
		r.node(n.Body)
		r.pop()
	case *ast.SwitchStmt:
		r.push()
		r.node(n.Init)
		r.node(n.Tag)
		r.stmts(n.Body.List)
		r.pop()
	case *ast.TypeSwitchStmt:
		r.push()
		r.node(n.Init)
		var bound *ast.Ident
		switch assign := n.Assign.(type) {
		case *ast.AssignStmt:
			r.exprs(assign.Rhs)
			bound, _ = assign.Lhs[0].(*ast.Ident)
		default:
			r.node(assign)
		}
		for _, clause := range n.Body.List {
			cc := clause.(*ast.CaseClause)
			r.push()
			r.exprs(cc.List)
			if bound != nil {
				r.declare(bound)
			}
			r.stmts(cc.Body)
			r.pop()
		}
		r.pop()
	case *ast.CaseClause:
		r.push()
		r.exprs(n.List)
		r.stmts(n.Body)
		r.pop()
	case *ast.CommClause:
		r.push()
		r.node(n.Comm)
		r.stmts(n.Body)
		r.pop()
	case *ast.LabeledStmt:
		r.node(n.Stmt)
	case *ast.BranchStmt:
		// Labels live in their own namespace.
	default:
		// Every other node: resolve its immediate children in order.
		ast.Inspect(n, func(child ast.Node) bool {
			if child == n {
				return true
			}
			r.node(child)
			return false
		})
	}
}

func (f *flagParseGuardParsedFile) pos(p token.Pos) string {
	return f.name + ":" + strconv.Itoa(f.fset.Position(p).Line)
}

// flagParseGuardFlagSet describes one FlagSet-valued local or parameter.
type flagParseGuardFlagSet struct {
	name string
	// wrapperFunc/wrapperIdx are set for a parameter of a package-level
	// function: a bare parse on it makes that function a parse wrapper.
	wrapperFunc string
	wrapperIdx  int
	param       bool
	pf          *flagParseGuardParsedFile
	// outputs are the arguments of every SetOutput call on the FlagSet.
	outputs []ast.Expr
	// usageFuncs are the same-package functions its Usage literal calls.
	usageFuncs map[string]bool
	// catalogUsage is set when the FlagSet is handed to a catalog Usage
	// setter (flagParseGuardCatalogUsageSetters); customUsage when it assigns
	// its own Usage.
	catalogUsage, customUsage bool
}

// flagParseGuardCatalogUsageSetters are the functions that make a FlagSet
// print its catalog Usage after the reason of a parse failure, as the suffix
// of their importPath.Name (the synthetic controls load under another module).
var flagParseGuardCatalogUsageSetters = []string{"/internal/cli.SetRouteUsage", "/internal/app.setRouteUsage"}

// flagParseGuardSelfReporting are the usage constructors that print the reason
// and the catalog Usage themselves, for a FlagSet whose output is discarded,
// as the suffix of their importPath.Name.
var flagParseGuardSelfReporting = []string{"/internal/app.usageRefusal"}

// flagParseGuardHiddenName reports whether a FlagSet name is a literal route
// in the hidden `internal ...` namespace. Such a route has no catalog Usage,
// so its FlagSet keeps the flag package default output; a shared leaf whose
// name comes from its caller calls setRouteUsage, which leaves a hidden name
// alone (cli.SetRouteUsage).
func flagParseGuardHiddenName(name string) bool {
	return strings.HasPrefix(name, "internal ")
}

// flagParseGuardQualifiedIn reports whether the qualified name q ends in one
// of suffixes.
func flagParseGuardQualifiedIn(q string, suffixes []string) bool {
	return q != "" && slices.ContainsFunc(suffixes, func(suffix string) bool { return strings.HasSuffix(q, suffix) })
}

// flagParseGuardCall is a candidate site before classification.
type flagParseGuardCall struct {
	pf     *flagParseGuardParsedFile
	fn     string
	call   *ast.CallExpr
	stack  []ast.Node // ancestors of call, outermost first
	fs     *flagParseGuardFlagSet
	callee string // "" for a direct Parse
	argIdx int
}

type flagParseGuardFunc struct {
	fsParams map[int]bool
}

type flagParseGuardAnalyzer struct {
	fset    *token.FileSet
	files   []*flagParseGuardParsedFile
	markers map[string]bool // importPath.Type
	ctors   map[string]bool // importPath.func
	// reported and reportedCtors are the types (and constructors) that tell
	// the entrypoint the reason is already printed, usage markers or not.
	reported      map[string]bool
	reportedCtors map[string]bool
	report        flagParseGuardReport
	funcs         map[string]map[string][]flagParseGuardFunc // pkg -> lookup name -> decls
	direct        []flagParseGuardCall
	calls         []flagParseGuardCall
	wrappers      map[string]map[string]map[int]bool // pkg -> func -> param idx
}

func flagParseGuardAnalyze(pkgs []flagParseGuardPackage) flagParseGuardReport {
	a := &flagParseGuardAnalyzer{
		fset:     token.NewFileSet(),
		markers:  map[string]bool{},
		ctors:    map[string]bool{},
		funcs:    map[string]map[string][]flagParseGuardFunc{},
		wrappers: map[string]map[string]map[int]bool{},
	}
	var scanned []*flagParseGuardParsedFile
	for _, pkg := range pkgs {
		for _, f := range pkg.files {
			file, err := parser.ParseFile(a.fset, f.name, f.src, parser.SkipObjectResolution)
			if err != nil {
				a.report.problems = append(a.report.problems, "parse "+f.name+": "+err.Error())
				continue
			}
			pf := &flagParseGuardParsedFile{name: f.name, pkg: pkg.importPath, file: file, fset: a.fset, imports: map[string]string{}}
			for _, spec := range file.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				local := path[strings.LastIndex(path, "/")+1:]
				if spec.Name != nil {
					local = spec.Name.Name
				}
				pf.imports[local] = path
				switch path {
				case "flag":
					pf.flagName = local
				case "errors":
					pf.errorsName = local
				}
			}
			flagParseGuardResolve(pf)
			a.files = append(a.files, pf)
			if pkg.scan {
				scanned = append(scanned, pf)
			}
		}
	}
	a.markers, a.ctors = a.deriveMarkers("MetadataUsageError")
	a.reported, a.reportedCtors = a.deriveMarkers("FailureReported")
	for _, pf := range scanned {
		a.collectFuncs(pf)
	}
	for _, pf := range scanned {
		a.collectCalls(pf)
	}
	a.resolveSites()
	a.report.markers = flagParseGuardSortedKeys(a.markers)
	a.report.ctors = flagParseGuardSortedKeys(a.ctors)
	a.report.reportedMarkers = flagParseGuardSortedKeys(a.reported)
	a.report.reportedCtors = flagParseGuardSortedKeys(a.reportedCtors)
	for pkg, fns := range a.wrappers {
		for fn := range fns {
			a.report.wrappers = append(a.report.wrappers, pkg+"."+fn)
		}
	}
	sort.Strings(a.report.wrappers)
	sort.Slice(a.report.sites, func(i, j int) bool {
		si, sj := a.report.sites[i], a.report.sites[j]
		if si.file != sj.file {
			return si.file < sj.file
		}
		return si.line < sj.line
	})
	return a.report
}

func flagParseGuardSortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// deriveMarkers finds every type declaring `<method>() bool` that returns
// true, then every package-level function whose returns all construct such a
// marker (usageError and friends for MetadataUsageError), to a fixpoint.
func (a *flagParseGuardAnalyzer) deriveMarkers(method string) (markers, ctors map[string]bool) {
	markers, ctors = map[string]bool{}, map[string]bool{}
	for _, pf := range a.files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != method || fn.Body == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if len(fn.Body.List) != 1 {
				continue
			}
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 || types.ExprString(ret.Results[0]) != "true" {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok {
				markers[pf.pkg+"."+id.Name] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, pf := range a.files {
			for _, decl := range pf.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil || ctors[pf.pkg+"."+fn.Name.Name] {
					continue
				}
				returns := flagParseGuardReturns(fn.Body)
				if len(returns) == 0 {
					continue
				}
				all := true
				for _, ret := range returns {
					if len(ret.Results) == 0 || !a.constructs(pf, ret.Results[len(ret.Results)-1], markers, ctors) {
						all = false
						break
					}
				}
				if all {
					ctors[pf.pkg+"."+fn.Name.Name] = true
					changed = true
				}
			}
		}
	}
	return markers, ctors
}

// flagParseGuardReturns lists the return statements of body, excluding those
// of nested function literals.
func flagParseGuardReturns(body ast.Node) []*ast.ReturnStmt {
	var out []*ast.ReturnStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			out = append(out, n)
		}
		return true
	})
	return out
}

// isMarker reports whether expr constructs a usage marker: a composite literal
// of a marker type, or a call to a marker constructor.
func (a *flagParseGuardAnalyzer) isMarker(pf *flagParseGuardParsedFile, expr ast.Expr) bool {
	return a.constructs(pf, expr, a.markers, a.ctors)
}

// isReported reports whether expr constructs an error that tells the
// entrypoint the reason is already printed, usage marker or not.
func (a *flagParseGuardAnalyzer) isReported(pf *flagParseGuardParsedFile, expr ast.Expr) bool {
	return a.constructs(pf, expr, a.reported, a.reportedCtors)
}

// constructs reports whether expr is a composite literal of one of markers or
// a call to one of ctors.
func (a *flagParseGuardAnalyzer) constructs(pf *flagParseGuardParsedFile, expr ast.Expr, markers, ctors map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return a.constructs(pf, e.X, markers, ctors)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if lit, ok := e.X.(*ast.CompositeLit); ok {
				return a.constructs(pf, lit, markers, ctors)
			}
		}
	case *ast.CompositeLit:
		return markers[a.qualify(pf, e.Type)]
	case *ast.CallExpr:
		return ctors[a.qualify(pf, e.Fun)]
	}
	return false
}

// qualify resolves a type or function name to importPath.Name.
func (a *flagParseGuardAnalyzer) qualify(pf *flagParseGuardParsedFile, expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return pf.pkg + "." + e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && pf.locals[x] == nil {
			if path, ok := pf.imports[x.Name]; ok {
				return path + "." + e.Sel.Name
			}
		}
	}
	return ""
}

func (a *flagParseGuardAnalyzer) problem(pf *flagParseGuardParsedFile, p token.Pos, msg string) {
	a.report.problems = append(a.report.problems, pf.pos(p)+": "+msg)
}

func (pf *flagParseGuardParsedFile) isFlagSetPtr(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok || pf.flagName == "" {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pf.flagName && sel.Sel.Name == "FlagSet"
}

func flagParseGuardFuncName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

// collectFuncs records, per package and lookup name (function or method
// name), which parameter positions take a *flag.FlagSet.
func (a *flagParseGuardAnalyzer) collectFuncs(pf *flagParseGuardParsedFile) {
	if a.funcs[pf.pkg] == nil {
		a.funcs[pf.pkg] = map[string][]flagParseGuardFunc{}
	}
	for _, decl := range pf.file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		info := flagParseGuardFunc{fsParams: map[int]bool{}}
		idx := 0
		for _, field := range fn.Type.Params.List {
			n := max(len(field.Names), 1)
			if pf.isFlagSetPtr(field.Type) {
				for i := range n {
					info.fsParams[idx+i] = true
				}
			}
			idx += n
		}
		a.funcs[pf.pkg][fn.Name.Name] = append(a.funcs[pf.pkg][fn.Name.Name], info)
	}
}

func (a *flagParseGuardAnalyzer) isNewFlagSet(pf *flagParseGuardParsedFile, expr ast.Expr) (*ast.CallExpr, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || pf.flagName == "" {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	x, ok := sel.X.(*ast.Ident)
	return call, ok && pf.locals[x] == nil && x.Name == pf.flagName && sel.Sel.Name == "NewFlagSet"
}

// collectCalls finds every FlagSet value in pf, accounts for every use of it,
// and records direct Parse calls and calls that hand it to another function.
func (a *flagParseGuardAnalyzer) collectCalls(pf *flagParseGuardParsedFile) {
	// Closure over the FlagSet type: it may only be spelled as a
	// *flag.FlagSet function parameter.
	var stack []ast.Node
	accounted := map[*ast.CallExpr]bool{}
	ast.Inspect(pf.file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && pf.flagName != "" {
			if x, ok := sel.X.(*ast.Ident); ok && pf.locals[x] == nil && x.Name == pf.flagName {
				switch sel.Sel.Name {
				case "FlagSet":
					if !flagParseGuardIsParamType(stack) {
						a.problem(pf, sel.Pos(), "flag.FlagSet is spelled outside a *flag.FlagSet function parameter; the guard cannot follow it")
					}
				case "Parse", "CommandLine", "Args", "Arg", "NArg", "Parsed":
					a.problem(pf, sel.Pos(), "use of the global flag."+sel.Sel.Name+"; the guard only follows explicit FlagSets")
				}
			}
		}
		stack = append(stack, n)
		return true
	})

	for _, decl := range pf.file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				a.walkFunc(pf, d, accounted)
			}
		case *ast.GenDecl:
			// Package-level FlagSets are not followed; closure below reports them.
		}
	}
	ast.Inspect(pf.file, func(n ast.Node) bool {
		if call, ok := a.isNewFlagSet(pf, flagParseGuardAsExpr(n)); ok && !accounted[call] {
			a.problem(pf, call.Pos(), "flag.NewFlagSet result is not assigned to a local variable inside a function; the guard cannot follow it")
		}
		return true
	})
}

func flagParseGuardAsExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}

func flagParseGuardIsParamType(stack []ast.Node) bool {
	if len(stack) < 4 {
		return false
	}
	_, star := stack[len(stack)-1].(*ast.StarExpr)
	_, field := stack[len(stack)-2].(*ast.Field)
	list, isList := stack[len(stack)-3].(*ast.FieldList)
	ft, isFunc := stack[len(stack)-4].(*ast.FuncType)
	return star && field && isList && isFunc && ft.Params == list
}

func (a *flagParseGuardAnalyzer) walkFunc(pf *flagParseGuardParsedFile, fn *ast.FuncDecl, accounted map[*ast.CallExpr]bool) {
	fnName := flagParseGuardFuncName(fn)
	flagSets := map[*flagParseGuardBinding]*flagParseGuardFlagSet{}
	registerParams := func(ft *ast.FuncType, wrapperFunc string) {
		idx := 0
		for _, field := range ft.Params.List {
			n := max(len(field.Names), 1)
			if pf.isFlagSetPtr(field.Type) {
				for i, name := range field.Names {
					if pf.locals[name] == nil {
						continue
					}
					info := &flagParseGuardFlagSet{name: "param " + name.Name + " of " + fnName, wrapperIdx: -1, param: true, pf: pf}
					if wrapperFunc != "" {
						info.wrapperFunc, info.wrapperIdx = wrapperFunc, idx+i
					}
					flagSets[pf.locals[name]] = info
				}
			}
			idx += n
		}
	}
	// Methods are resolved by name like functions.
	registerParams(fn.Type, fn.Name.Name)

	registerNew := func(lhs ast.Expr, rhs ast.Expr) {
		call, ok := a.isNewFlagSet(pf, rhs)
		if !ok {
			return
		}
		id, ok := lhs.(*ast.Ident)
		if !ok || pf.locals[id] == nil {
			return
		}
		name := "?"
		if len(call.Args) > 0 {
			name = types.ExprString(call.Args[0])
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				name, _ = strconv.Unquote(lit.Value)
			}
		}
		accounted[call] = true
		flagSets[pf.locals[id]] = &flagParseGuardFlagSet{name: name, wrapperIdx: -1, pf: pf}
	}

	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			registerParams(node.Type, "")
		case *ast.AssignStmt:
			if len(node.Lhs) == len(node.Rhs) {
				for i := range node.Rhs {
					registerNew(node.Lhs[i], node.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(node.Names) == len(node.Values) {
				for i := range node.Values {
					registerNew(node.Names[i], node.Values[i])
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Parse" {
				x, isIdent := sel.X.(*ast.Ident)
				switch {
				case isIdent && pf.locals[x] != nil && flagSets[pf.locals[x]] != nil:
					// A FlagSet Parse; recorded from the ident use below.
				case isIdent && pf.locals[x] == nil && pf.imports[x.Name] != "" && x.Name != pf.flagName:
					// A package function such as url.Parse or time.Parse.
				default:
					a.problem(pf, node.Pos(), "unclassified "+types.ExprString(node.Fun)+" call in "+fnName+"; the guard cannot tell whether it parses a FlagSet")
				}
			}
		case *ast.Ident:
			binding := pf.locals[node]
			info := flagSets[binding]
			if binding == nil || info == nil || pf.declares[node] {
				break
			}
			a.classifyUse(pf, fnName, node, info, stack)
		}
		stack = append(stack, n)
		return true
	})
}

// classifyUse accounts for one use of a FlagSet value. Every use must be a
// method call on it or an argument to a known function taking *flag.FlagSet.
func (a *flagParseGuardAnalyzer) classifyUse(pf *flagParseGuardParsedFile, fnName string, id *ast.Ident, info *flagParseGuardFlagSet, stack []ast.Node) {
	parent := stack[len(stack)-1]
	switch p := parent.(type) {
	case *ast.SelectorExpr:
		if p.X != id {
			break
		}
		if p.Sel.Name != "Parse" {
			a.recordSetting(pf, info, p, stack)
			return
		}
		if len(stack) >= 2 {
			if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == p {
				a.direct = append(a.direct, flagParseGuardCall{pf: pf, fn: fnName, call: call, stack: slices.Clone(stack[:len(stack)-2]), fs: info})
				return
			}
		}
		a.problem(pf, id.Pos(), "FlagSet "+id.Name+".Parse used as a value in "+fnName)
		return
	case *ast.CallExpr:
		idx := slices.IndexFunc(p.Args, func(e ast.Expr) bool { return e == id })
		if idx < 0 {
			break
		}
		if flagParseGuardQualifiedIn(a.qualify(pf, p.Fun), flagParseGuardCatalogUsageSetters) {
			info.catalogUsage = true
			return
		}
		callee := ""
		switch fun := p.Fun.(type) {
		case *ast.Ident:
			callee = fun.Name
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); !ok || pf.locals[x] != nil || pf.imports[x.Name] == "" {
				callee = fun.Sel.Name
			}
		}
		takes := false
		for _, decl := range a.funcs[pf.pkg][callee] {
			takes = takes || decl.fsParams[idx]
		}
		if callee == "" || !takes {
			a.problem(pf, id.Pos(), "FlagSet "+id.Name+" is passed to "+types.ExprString(p.Fun)+" in "+fnName+", which is not a same-package function taking a *flag.FlagSet at that position")
			return
		}
		a.calls = append(a.calls, flagParseGuardCall{pf: pf, fn: fnName, call: p, stack: slices.Clone(stack[:len(stack)-1]), fs: info, callee: callee, argIdx: idx})
		return
	}
	a.problem(pf, id.Pos(), "FlagSet "+id.Name+" escapes in "+fnName+" (used in a "+strings.TrimPrefix(reflect.TypeOf(parent).String(), "*ast.")+"); the guard cannot follow it")
}

// resolveSites classifies every direct Parse, then promotes functions that
// return a FlagSet parameter's parse error bare to parse wrappers, to a
// fixpoint, so calls to wrappers (and wrappers of wrappers) become sites.
func (a *flagParseGuardAnalyzer) resolveSites() {
	type classified struct {
		call flagParseGuardCall
		site flagParseGuardSite
	}
	var sites []classified
	for _, c := range a.direct {
		sites = append(sites, classified{c, a.classify(c)})
	}
	promoted := map[int]bool{}
	isWrapper := func(pkg, fn string, idx int) bool { return a.wrappers[pkg][fn][idx] }
	for changed := true; changed; {
		changed = false
		for _, s := range sites {
			fs := s.call.fs
			if s.site.verdict != flagParseGuardBare || fs.wrapperFunc == "" || isWrapper(s.call.pf.pkg, fs.wrapperFunc, fs.wrapperIdx) {
				continue
			}
			pkg := s.call.pf.pkg
			if a.wrappers[pkg] == nil {
				a.wrappers[pkg] = map[string]map[int]bool{}
			}
			if a.wrappers[pkg][fs.wrapperFunc] == nil {
				a.wrappers[pkg][fs.wrapperFunc] = map[int]bool{}
			}
			a.wrappers[pkg][fs.wrapperFunc][fs.wrapperIdx] = true
			changed = true
		}
		for i, c := range a.calls {
			if !promoted[i] && isWrapper(c.pf.pkg, c.callee, c.argIdx) {
				promoted[i] = true
				sites = append(sites, classified{c, a.classify(c)})
				changed = true
			}
		}
	}
	for _, s := range sites {
		fs := s.call.fs
		if s.site.verdict == flagParseGuardBare && fs.wrapperFunc != "" && isWrapper(s.call.pf.pkg, fs.wrapperFunc, fs.wrapperIdx) {
			// The wrapper's own parse is not a site; its call sites are.
			continue
		}
		a.resolveOutput(s.call, &s.site)
		a.report.sites = append(a.report.sites, s.site)
	}
}

// classify finds how the error of one parse call is handled.
func (a *flagParseGuardAnalyzer) classify(c flagParseGuardCall) flagParseGuardSite {
	pf := c.pf
	site := flagParseGuardSite{file: pf.name, fn: c.fn, flagSet: c.fs.name, line: pf.fset.Position(c.call.Pos()).Line, via: c.callee}
	unclassified := func(why string) flagParseGuardSite {
		site.verdict = flagParseGuardOther
		site.detail = why
		a.problem(pf, c.call.Pos(), "cannot classify the error handling of "+types.ExprString(c.call.Fun)+" in "+c.fn+": "+why)
		return site
	}
	if len(c.stack) == 0 {
		return unclassified("no enclosing statement")
	}
	var errName string
	var body *ast.BlockStmt
	switch parent := c.stack[len(c.stack)-1].(type) {
	case *ast.ReturnStmt:
		if len(parent.Results) != 1 {
			return unclassified("the call is one of several return values")
		}
		site.verdict = flagParseGuardBare
		site.detail = "returns the parse result directly"
		return site
	case *ast.ExprStmt:
		site.verdict = flagParseGuardOther
		site.detail = "discards the parse error"
		return site
	case *ast.AssignStmt:
		var why string
		errName, body, why = flagParseGuardErrorBranch(c.stack)
		switch {
		case why == "discards the parse error":
			site.verdict = flagParseGuardOther
			site.detail = why
			return site
		case why != "":
			return unclassified(why)
		}
	default:
		return unclassified("unsupported statement shape")
	}

	var marked, bare, reportedOnly, other int
	var details []string
	var walk func(n ast.Node, help bool)
	walk = func(n ast.Node, help bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.IfStmt:
				if n != body && a.isHelpCond(pf, node.Cond, errName) {
					site.helpBranch = true
					walk(node.Body, true)
					if node.Else != nil {
						walk(node.Else, help)
					}
					return false
				}
			case *ast.ReturnStmt:
				var last ast.Expr
				if len(node.Results) > 0 {
					last = node.Results[len(node.Results)-1]
				}
				isMarker := last != nil && a.isMarker(pf, last)
				if help {
					if isMarker {
						site.helpWrapped = true
					}
					return true
				}
				switch {
				case isMarker:
					marked++
					if a.isReported(pf, last) {
						site.reported++
					}
					if call, ok := last.(*ast.CallExpr); ok && flagParseGuardQualifiedIn(a.qualify(pf, call.Fun), flagParseGuardSelfReporting) {
						site.selfReported++
					}
				case last != nil && a.isReported(pf, last):
					reportedOnly++
					site.reported++
					details = append(details, "returns "+types.ExprString(last))
				case last != nil && flagParseGuardIsIdent(last, errName):
					bare++
				default:
					other++
					if last == nil {
						details = append(details, "returns without an error value")
					} else {
						details = append(details, "returns "+types.ExprString(last))
					}
				}
			}
			return true
		})
	}
	walk(body, false)
	site.marked = marked
	site.reprints = a.reprintsIn(pf, body, errName, c.fs)
	switch {
	case marked+bare+reportedOnly+other == 0:
		site.verdict = flagParseGuardOther
		site.detail = "the error branch does not return"
	case bare == 0 && reportedOnly == 0 && other == 0:
		site.verdict = flagParseGuardMarked
	case marked == 0 && reportedOnly == 0 && other == 0:
		site.verdict = flagParseGuardBare
		site.detail = "returns " + errName + " unchanged"
	case marked == 0 && bare == 0 && other == 0:
		site.verdict = flagParseGuardReported
		site.detail = strings.Join(slices.Compact(details), "; ")
	default:
		site.verdict = flagParseGuardOther
		site.detail = strings.Join(details, "; ")
		if bare > 0 {
			site.detail = strings.TrimPrefix(site.detail+"; returns "+errName+" unchanged", "; ")
		}
	}
	return site
}

// flagParseGuardErrorBranch finds the error branch of a parse call whose
// innermost ancestor (the last of stack) assigns its error: the body of the
// `if err != nil` that is the assignment's if statement or the statement right
// after it. why is non-empty when the shape is anything else.
func flagParseGuardErrorBranch(stack []ast.Node) (errName string, body *ast.BlockStmt, why string) {
	if len(stack) < 2 {
		return "", nil, "no enclosing statement"
	}
	parent, ok := stack[len(stack)-1].(*ast.AssignStmt)
	if !ok {
		return "", nil, "unsupported statement shape"
	}
	if len(parent.Rhs) != 1 || len(parent.Lhs) == 0 {
		return "", nil, "multi-value assignment"
	}
	id, ok := parent.Lhs[len(parent.Lhs)-1].(*ast.Ident)
	if !ok {
		return "", nil, "the error is not assigned to a variable"
	}
	if id.Name == "_" {
		return "", nil, "discards the parse error"
	}
	errName = id.Name
	var cond ast.Expr
	switch gp := stack[len(stack)-2].(type) {
	case *ast.IfStmt:
		if gp.Init != parent {
			return "", nil, "assignment is not the if initializer"
		}
		cond, body = gp.Cond, gp.Body
	default:
		list := flagParseGuardStmtList(gp)
		i := slices.IndexFunc(list, func(s ast.Stmt) bool { return s == parent })
		if i < 0 || i+1 >= len(list) {
			return "", nil, "the assignment is not followed by an error check"
		}
		next, ok := list[i+1].(*ast.IfStmt)
		if !ok || next.Init != nil {
			return "", nil, "the assignment is not followed by `if " + errName + " != nil`"
		}
		cond, body = next.Cond, next.Body
	}
	if !flagParseGuardChecksNonNil(cond, errName) {
		return "", nil, "the error check does not test " + errName + " != nil"
	}
	return errName, body, ""
}

// recordSetting records what a FlagSet method call configures: the output of
// SetOutput, and the same-package functions a Usage literal calls.
func (a *flagParseGuardAnalyzer) recordSetting(pf *flagParseGuardParsedFile, info *flagParseGuardFlagSet, sel *ast.SelectorExpr, stack []ast.Node) {
	if len(stack) < 2 {
		return
	}
	switch sel.Sel.Name {
	case "SetOutput":
		if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && call.Fun == sel && len(call.Args) == 1 {
			info.outputs = append(info.outputs, call.Args[0])
		}
	case "Usage":
		assign, ok := stack[len(stack)-2].(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || assign.Lhs[0] != sel || len(assign.Rhs) != 1 {
			return
		}
		info.customUsage = true
		if info.usageFuncs == nil {
			info.usageFuncs = map[string]bool{}
		}
		ast.Inspect(assign.Rhs[0], func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if name := flagParseGuardLocalCallee(pf, call); name != "" {
					info.usageFuncs[name] = true
				}
			}
			return true
		})
		if id, ok := assign.Rhs[0].(*ast.Ident); ok && pf.locals[id] == nil {
			info.usageFuncs[id.Name] = true
		}
	}
}

// flagParseGuardLocalCallee names the same-package function or method call
// calls, or returns "" for a call into an imported package or a local value.
func flagParseGuardLocalCallee(pf *flagParseGuardParsedFile, call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if pf.locals[fun] == nil {
			return fun.Name
		}
	case *ast.SelectorExpr:
		if x, ok := fun.X.(*ast.Ident); ok && pf.locals[x] == nil && pf.imports[x.Name] != "" {
			return ""
		}
		return fun.Sel.Name
	}
	return ""
}

// reprintsIn lists the usage printers called on the non-help path of an error
// branch: the FlagSet's Usage or PrintDefaults, a function its Usage literal
// calls, or any same-package function whose name says it prints a usage.
func (a *flagParseGuardAnalyzer) reprintsIn(pf *flagParseGuardParsedFile, body *ast.BlockStmt, errName string, fs *flagParseGuardFlagSet) []string {
	if body == nil {
		return nil
	}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt:
			if node != nil && errName != "" && a.isHelpCond(pf, node.Cond, errName) {
				if node.Else != nil {
					out = append(out, a.reprintsIn(pf, &ast.BlockStmt{List: []ast.Stmt{node.Else}}, errName, fs)...)
				}
				return false
			}
		case *ast.CallExpr:
			name := flagParseGuardLocalCallee(pf, node)
			_, method := node.Fun.(*ast.SelectorExpr)
			switch {
			case name == "":
			case method && (name == "Usage" || name == "PrintDefaults"),
				fs != nil && fs.usageFuncs[name],
				strings.Contains(name, "Usage") && !a.ctors[a.qualify(pf, node.Fun)]:
				out = append(out, types.ExprString(node.Fun))
			}
		}
		return true
	})
	return out
}

// outputOf says where fs writes its reason and usage: "stderr", "discard", or
// a description of why the analyzer cannot tell.
func flagParseGuardOutputOf(fs *flagParseGuardFlagSet) string {
	if len(fs.outputs) == 0 {
		if fs.param {
			return ""
		}
		// flag.NewFlagSet writes to os.Stderr until SetOutput is called.
		return "stderr"
	}
	kind := ""
	for _, out := range fs.outputs {
		k := "stderr"
		if sel, ok := out.(*ast.SelectorExpr); ok && sel.Sel.Name == "Discard" {
			if x, ok := sel.X.(*ast.Ident); ok && fs.pf.locals[x] == nil && fs.pf.imports[x.Name] == "io" {
				k = "discard"
			}
		}
		if kind != "" && kind != k {
			return "mixed SetOutput calls"
		}
		kind = k
	}
	return kind
}

// flagParseGuardUsageOf says what fs prints after the reason of a parse
// failure: "catalog", "custom", or "default".
func flagParseGuardUsageOf(fs *flagParseGuardFlagSet) string {
	switch {
	case fs.customUsage:
		return "custom"
	case fs.catalogUsage:
		return "catalog"
	default:
		return "default"
	}
}

// resolveOutput decides a site's output. A FlagSet parameter without its own
// SetOutput takes the output of every caller that hands a FlagSet to it, and
// the usage printers in each caller's error branch join the site's.
func (a *flagParseGuardAnalyzer) resolveOutput(c flagParseGuardCall, site *flagParseGuardSite) {
	site.output = flagParseGuardOutputOf(c.fs)
	site.usage = flagParseGuardUsageOf(c.fs)
	if site.output != "" {
		return
	}
	site.usage = ""
	if c.fs.wrapperFunc == "" {
		site.output = "unresolved: a FlagSet parameter of a function literal without SetOutput"
		return
	}
	for _, caller := range a.calls {
		if caller.pf.pkg != c.pf.pkg || caller.callee != c.fs.wrapperFunc || caller.argIdx != c.fs.wrapperIdx {
			continue
		}
		out := flagParseGuardOutputOf(caller.fs)
		if out == "" {
			out = "unresolved: caller " + caller.fn + " passes a FlagSet parameter without SetOutput"
		}
		if site.output != "" && site.output != out {
			site.output = "mixed: callers disagree on the FlagSet output"
			return
		}
		site.output = out
		switch usage := flagParseGuardUsageOf(caller.fs); {
		case site.usage == "":
			site.usage = usage
		case site.usage != usage:
			site.usage = "mixed: callers disagree on the FlagSet usage"
		}
		if errName, body, why := flagParseGuardErrorBranch(caller.stack); why == "" {
			for _, reprint := range a.reprintsIn(caller.pf, body, errName, caller.fs) {
				site.reprints = append(site.reprints, reprint+" in caller "+caller.fn)
			}
		}
	}
	if site.output == "" {
		site.output = "unresolved: no caller hands a FlagSet to " + c.fs.wrapperFunc
	}
}

func flagParseGuardStmtList(n ast.Node) []ast.Stmt {
	switch b := n.(type) {
	case *ast.BlockStmt:
		return b.List
	case *ast.CaseClause:
		return b.Body
	case *ast.CommClause:
		return b.Body
	}
	return nil
}

func flagParseGuardIsIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func flagParseGuardChecksNonNil(cond ast.Expr, errName string) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.NEQ && flagParseGuardIsIdent(b.X, errName) && flagParseGuardIsIdent(b.Y, "nil") {
			found = true
		}
		return !found
	})
	return found
}

// isHelpCond matches errors.Is(err, flag.ErrHelp) and err == flag.ErrHelp.
func (a *flagParseGuardAnalyzer) isHelpCond(pf *flagParseGuardParsedFile, cond ast.Expr, errName string) bool {
	isErrHelp := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == pf.flagName && sel.Sel.Name == "ErrHelp"
	}
	switch c := cond.(type) {
	case *ast.CallExpr:
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || len(c.Args) != 2 {
			return false
		}
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == pf.errorsName && sel.Sel.Name == "Is" && flagParseGuardIsIdent(c.Args[0], errName) && isErrHelp(c.Args[1])
	case *ast.BinaryExpr:
		return c.Op == token.EQL && flagParseGuardIsIdent(c.X, errName) && isErrHelp(c.Y)
	}
	return false
}

// flagParseGuardLoadRepo loads every non-test Go file under internal/**, and
// marks the packages under internal/app/** (minus testdata and assets) for
// parse-site collection.
func flagParseGuardLoadRepo(t *testing.T, repoRoot string) []flagParseGuardPackage {
	t.Helper()
	byDir := map[string]*flagParseGuardPackage{}
	root := filepath.Join(repoRoot, "internal")
	err := filepath.WalkDir(root, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir := rel[:strings.LastIndex(rel, "/")]
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkg := byDir[dir]
		if pkg == nil {
			scan := dir == "internal/app" || strings.HasPrefix(dir, "internal/app/")
			if strings.Contains("/"+dir+"/", "/assets/") {
				scan = false
			}
			pkg = &flagParseGuardPackage{importPath: flagParseGuardModule + "/" + dir, scan: scan}
			byDir[dir] = pkg
		}
		pkg.files = append(pkg.files, flagParseGuardFile{name: rel, src: src})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	var pkgs []flagParseGuardPackage
	for _, dir := range slices.Sorted(func(yield func(string) bool) {
		for dir := range byDir {
			if !yield(dir) {
				return
			}
		}
	}) {
		pkgs = append(pkgs, *byDir[dir])
	}
	return pkgs
}

// flagParseGuardFindings applies the guard policy to a report: every site must
// be usage-marked on its non-help path, must pass flag.ErrHelp through, or be
// an exception row. An exception row admits any non-usage verdict (bare,
// reported, or other); how such a site prints is flagParseOutputFindings'
// rule. It returns the failures and the exception keys it used, so a row whose
// site became usage-marked or disappeared is stale.
func flagParseGuardFindings(report flagParseGuardReport, exceptions map[string]flagParseGuardException) (failures []string, used map[string]bool) {
	used = map[string]bool{}
	failures = append(failures, report.problems...)
	for _, site := range report.sites {
		if site.helpWrapped {
			failures = append(failures, site.describe()+": the flag.ErrHelp branch returns a usage error; help must pass through unchanged")
		}
		if site.verdict == flagParseGuardMarked {
			continue
		}
		if _, ok := exceptions[site.key()]; ok {
			used[site.key()] = true
			continue
		}
		failures = append(failures, site.describe()+": the parse error path is "+site.verdict.String()+" ("+site.detail+"); a flag error must exit 2: return usageError(err.Error()) after passing flag.ErrHelp through, or, for a hidden `internal ...` route only, add key "+strconv.Quote(site.key())+" to flagParseGuardExceptions")
	}
	return failures, used
}

// TestFlagParseUsageGuardEveryParserMarksUsage holds every flag parse site in
// internal/app/** to the exit-code contract of docs/cli-guide.md: a flag error
// is a usage error (exit 2). Hidden `internal ...` plumbing is the only
// allowed exception, row by row.
func TestFlagParseUsageGuardEveryParserMarksUsage(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := flagParseGuardAnalyze(flagParseGuardLoadRepo(t, filepath.Join("..", "..")))

	failures, used := flagParseGuardFindings(report, flagParseGuardExceptions)
	for _, failure := range failures {
		t.Error(failure)
	}
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range flagParseGuardExceptions {
			if !yield(key) {
				return
			}
		}
	}) {
		row := flagParseGuardExceptions[key]
		if !strings.HasPrefix(row.route, "internal ") {
			t.Errorf("exception %q: route %q is not a hidden `internal ...` route; public routes must return a usage error", key, row.route)
		}
		if strings.TrimSpace(row.reason) == "" {
			t.Errorf("exception %q: missing reason", key)
		}
		if !used[key] {
			t.Errorf("exception %q (route %q) is stale: no parse site with that key returns a non-usage error", key, row.route)
		}
	}

	// Vacuity checks: the markers, constructors and wrappers the policy
	// depends on must be discovered, not assumed.
	for _, want := range []string{flagParseGuardModule + "/internal/app.UsageError", flagParseGuardModule + "/internal/core/metadata.InputError"} {
		if !slices.Contains(report.markers, want) {
			t.Errorf("usage marker %s not derived from a MetadataUsageError declaration; markers = %v", want, report.markers)
		}
	}
	if !slices.Contains(report.ctors, flagParseGuardModule+"/internal/app.usageError") {
		t.Errorf("usageError not derived as a usage marker constructor; ctors = %v", report.ctors)
	}
	if !slices.Contains(report.wrappers, flagParseGuardModule+"/internal/app.parseWithPositionals") {
		t.Errorf("parseWithPositionals not discovered as a parse wrapper; wrappers = %v", report.wrappers)
	}
	marked, helpless := 0, 0
	for _, site := range report.sites {
		if site.verdict == flagParseGuardMarked {
			marked++
			if !site.helpBranch {
				// Such a site also marks flag.ErrHelp; the root help boundary
				// intercepts every help spelling before it reaches the leaf.
				helpless++
			}
		}
	}
	if len(report.sites) < 100 {
		t.Errorf("found only %d parse sites; the site collection has regressed", len(report.sites))
	}
	t.Logf("flag parse guard: %d sites (%d usage-marked, %d of them without a flag.ErrHelp branch; %d exception rows), wrappers %v, markers %v, in %s",
		len(report.sites), marked, helpless, len(used), report.wrappers, report.markers, time.Since(start).Round(time.Millisecond))
}

// flagParseOutputFindings applies the output rule to a report. On a public
// route a flag parse failure puts its reason on stderr exactly once, first,
// and then the route's catalog Usage exactly once, never the flag package's
// `Usage of <name>:` default listing. A public FlagSet writing to stderr gets
// both from the flag package: it hands the FlagSet to setRouteUsage (and
// assigns no Usage of its own), returns an error that says the reason is
// printed (flagParseError, so the entrypoint stays silent), and prints no
// usage of its own. A public FlagSet with a discarded output prints nothing,
// so every non-help return calls usageRefusal, which prints the reason and
// the catalog Usage itself. An exception row keeps exit 1 and the flag
// package default output, so it
// returns flagParseReported or, with a discarded output, a non-reported error;
// its exit-code verdict stays the exit-code guard's. A site that is neither is
// left to that guard. examined counts the exception sites checked here.
func flagParseOutputFindings(report flagParseGuardReport, exceptions map[string]flagParseGuardException) (failures []string, examined int) {
	for _, site := range report.sites {
		_, exception := exceptions[site.key()]
		if !exception && site.verdict != flagParseGuardMarked {
			continue
		}
		if exception {
			examined++
		}
		switch site.output {
		case "stderr":
			switch {
			case exception && site.verdict != flagParseGuardReported:
				failures = append(failures, site.describe()+": the FlagSet writes to stderr, so the flag package has already printed the reason and the usage; this hidden route returns "+site.verdict.String()+" ("+site.detail+"), so the entrypoint prints the reason a second time; return flagParseReported(err), which keeps exit 1")
			case !exception && site.reported != site.marked:
				failures = append(failures, site.describe()+": the FlagSet writes to stderr, so the flag package has already printed the reason and the usage; return flagParseError(err) so the entrypoint does not print the reason a second time")
			}
			if !exception && !flagParseGuardHiddenName(site.flagSet) && site.usage != "catalog" {
				failures = append(failures, site.describe()+": the public FlagSet does not print its catalog Usage after the reason (usage: "+site.usage+"); call setRouteUsage(fs) and assign no fs.Usage of its own")
			}
			if len(site.reprints) > 0 {
				failures = append(failures, site.describe()+": the parse error path prints the usage again ("+strings.Join(site.reprints, ", ")+") after the flag package printed it; print it only in the flag.ErrHelp branch, if at all")
			}
		case "discard":
			switch {
			case exception && site.reported > 0:
				failures = append(failures, site.describe()+": the FlagSet discards its output, so nothing printed the reason; return the parse error itself so the entrypoint prints it once")
			case !exception && site.reported != site.selfReported:
				failures = append(failures, site.describe()+": the FlagSet discards its output, so nothing printed the reason; return usageRefusal(stderr, route, reason) so the reason and the catalog Usage are printed once")
			case !exception && site.selfReported != site.marked:
				failures = append(failures, site.describe()+": the public FlagSet discards its output, so no catalog Usage follows the reason; return usageRefusal(stderr, route, reason), or write to stderr with setRouteUsage(fs)")
			}
		default:
			failures = append(failures, site.describe()+": cannot tell where the FlagSet writes its reason ("+site.output+")")
		}
	}
	return failures, examined
}

// TestFlagParseOutputGuardPrintsReasonThenCatalogUsage holds every
// usage-marked flag parse site and every exception row in internal/app/** to
// the stderr shape of a flag error: the reason exactly once, then, on a
// public route, the catalog Usage exactly once and never the flag package
// `Usage of <name>:` listing (a hidden exception row keeps the flag package
// default). It walks the same closed site set as
// TestFlagParseUsageGuardEveryParserMarksUsage; the runtime shape of every
// public route is cmd/projmux TestPublicFlagParseErrorsPrintReasonThenCatalogUsage.
func TestFlagParseOutputGuardPrintsReasonThenCatalogUsage(t *testing.T) {
	t.Parallel()
	report := flagParseGuardAnalyze(flagParseGuardLoadRepo(t, filepath.Join("..", "..")))
	findings, examined := flagParseOutputFindings(report, flagParseGuardExceptions)
	for _, failure := range append(report.problems, findings...) {
		t.Error(failure)
	}
	for _, want := range []string{flagParseGuardModule + "/internal/cli.flagParseError", flagParseGuardModule + "/internal/cli.flagParseReported"} {
		if !slices.Contains(report.reportedMarkers, want) {
			t.Errorf("%s not derived from a FailureReported declaration; reported markers = %v", want, report.reportedMarkers)
		}
	}
	for _, want := range []string{flagParseGuardModule + "/internal/cli.FlagParseError", flagParseGuardModule + "/internal/app.flagParseError", flagParseGuardModule + "/internal/cli.FlagParseReported", flagParseGuardModule + "/internal/app.flagParseReported"} {
		if !slices.Contains(report.reportedCtors, want) {
			t.Errorf("%s not derived as a reported constructor; reported ctors = %v", want, report.reportedCtors)
		}
	}
	// The hidden-route constructor must stay out of the usage markers, or
	// its sites would exit 2.
	if slices.Contains(report.markers, flagParseGuardModule+"/internal/cli.flagParseReported") {
		t.Errorf("cli.flagParseReported is derived as a usage marker; markers = %v", report.markers)
	}
	for _, ctor := range []string{flagParseGuardModule + "/internal/cli.FlagParseReported", flagParseGuardModule + "/internal/app.flagParseReported"} {
		if slices.Contains(report.ctors, ctor) {
			t.Errorf("%s is derived as a usage marker constructor; ctors = %v", ctor, report.ctors)
		}
	}
	outputs, exceptionOutputs := map[string]int{}, map[string]int{}
	exceptionKeys := map[string]bool{}
	for _, site := range report.sites {
		if _, ok := flagParseGuardExceptions[site.key()]; ok {
			exceptionOutputs[site.output]++
			exceptionKeys[site.key()] = true
		} else if site.verdict == flagParseGuardMarked {
			outputs[site.output]++
		}
	}
	if outputs["stderr"] < 80 || outputs["discard"] < 3 {
		t.Errorf("site outputs = %v; the output resolution has regressed", outputs)
	}
	// Every exception row is checked, not only the usage-marked sites.
	if examined != len(flagParseGuardExceptions) || len(exceptionKeys) != len(flagParseGuardExceptions) {
		t.Errorf("output guard examined %d exception sites under %d keys, want one per row of the %d flagParseGuardExceptions", examined, len(exceptionKeys), len(flagParseGuardExceptions))
	}
	if exceptionOutputs["stderr"] == 0 || exceptionOutputs["discard"] == 0 {
		t.Errorf("exception site outputs = %v; want both a stderr and a discarded FlagSet among the rows", exceptionOutputs)
	}
	t.Logf("flag parse output guard: %v usage-marked sites by output; %d exception sites checked, by output %v", outputs, examined, exceptionOutputs)
}

// TestFlagParseOutputGuardPositiveControl runs the output rule on synthetic
// source: a plain usage error or a usage reprint on a stderr FlagSet, a
// public stderr FlagSet without the catalog Usage setter (the flag package
// default listing) or with its own Usage, a reported error or a plain usage
// error on a discarded public FlagSet, and helpers whose callers disagree on
// the output or the usage must fail; so must an exception row that returns the
// bare parse error or a usage marker on a stderr FlagSet, reprints the usage,
// or reports on a discarded FlagSet. The idioms, and a FlagSet named after a
// hidden `internal ...` route that keeps the flag package default, must pass.
func TestFlagParseOutputGuardPositiveControl(t *testing.T) {
	t.Parallel()
	const pkgPath = "example.test/guard/internal/app"
	const src = `package app

import (
	"errors"
	"flag"
	"io"
)

type UsageError struct{ Message string }

func (e *UsageError) Error() string            { return e.Message }
func (e *UsageError) MetadataUsageError() bool { return true }

type flagParseUsageError struct{ message string }

func (e *flagParseUsageError) Error() string            { return e.message }
func (e *flagParseUsageError) MetadataUsageError() bool { return true }
func (e *flagParseUsageError) FailureReported() bool    { return true }

func usageError(message string) error { return &UsageError{Message: message} }

func flagParseError(err error) error { return &flagParseUsageError{message: err.Error()} }

type flagParseReportedError struct{ cause error }

func (e *flagParseReportedError) Error() string         { return e.cause.Error() }
func (e *flagParseReportedError) FailureReported() bool { return true }

func flagParseReported(err error) error { return &flagParseReportedError{cause: err} }

func printFooUsage(w io.Writer) {}

func showHelp(w io.Writer) {}

func setRouteUsage(fs *flag.FlagSet) {}

func usageRefusal(stderr io.Writer, route, reason string) error {
	printFooUsage(stderr)
	return flagParseError(errors.New(reason))
}

func stderrPlain(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("stderr-plain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	return nil
}

func defaultPlain(args []string) error {
	fs := flag.NewFlagSet("default-plain", flag.ContinueOnError)
	setRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func stderrReprint(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("stderr-reprint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		printFooUsage(stderr)
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	return nil
}

func stderrUsageLiteral(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("stderr-usage-literal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { showHelp(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		showHelp(stderr)
		return flagParseError(err)
	}
	return nil
}

func discardReported(args []string) error {
	fs := flag.NewFlagSet("discard-reported", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func parseHelper(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, flagParseError(err)
	}
	return fs.Args(), nil
}

func helperCallerReprint(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("helper-caller", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	_, err := parseHelper(fs, args)
	if err != nil {
		printFooUsage(stderr)
		return err
	}
	return nil
}

func mixedHelper(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func mixedStderr(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("mixed-stderr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	return mixedHelper(fs, args)
}

func mixedDiscard(args []string) error {
	fs := flag.NewFlagSet("mixed-discard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return mixedHelper(fs, args)
}

func idiomStderr(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("idiom-stderr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printFooUsage(stderr)
			return err
		}
		return flagParseError(err)
	}
	return nil
}

func idiomDiscard(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("idiom-discard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, "idiom-discard", "idiom-discard: "+err.Error())
	}
	return nil
}

func idiomHelperCaller(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("idiom-helper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	if _, err := parseHelper(fs, args); err != nil {
		return err
	}
	return nil
}

func stderrDefaultUsage(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("stderr-default-usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func stderrCustomUsage(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("stderr-custom-usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	fs.Usage = func() { printFooUsage(stderr) }
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func discardPlainUsage(args []string) error {
	fs := flag.NewFlagSet("discard-plain-usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageError("discard-plain-usage: " + err.Error())
	}
	return nil
}

func internalDefaultUsage(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("internal default-usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func usageHelper(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func usageHelperCatalog(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage-helper-catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	return usageHelper(fs, args)
}

func usageHelperDefault(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage-helper-default", flag.ContinueOnError)
	fs.SetOutput(stderr)
	return usageHelper(fs, args)
}

func hiddenBare(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("hidden-bare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return nil
}

func hiddenUsage(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("hidden-usage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return flagParseError(err)
	}
	return nil
}

func hiddenReprint(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("hidden-reprint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		printFooUsage(stderr)
		return flagParseReported(err)
	}
	return nil
}

func hiddenDiscardReported(args []string) error {
	fs := flag.NewFlagSet("hidden-discard-reported", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return flagParseReported(err)
	}
	return nil
}

func hiddenIdiomStderr(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("hidden-idiom-stderr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return flagParseReported(err)
	}
	return nil
}

func hiddenIdiomDiscard(args []string) error {
	fs := flag.NewFlagSet("hidden-idiom-discard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return nil
}
`
	report := flagParseGuardAnalyze([]flagParseGuardPackage{{
		importPath: pkgPath,
		scan:       true,
		files:      []flagParseGuardFile{{name: "synthetic/app.go", src: []byte(src)}},
	}})
	if len(report.problems) != 0 {
		t.Fatalf("positive control: analyzer problems: %v", report.problems)
	}
	exceptions := map[string]flagParseGuardException{}
	for _, key := range []string{
		"synthetic/app.go hiddenBare hidden-bare",
		"synthetic/app.go hiddenUsage hidden-usage",
		"synthetic/app.go hiddenReprint hidden-reprint",
		"synthetic/app.go hiddenDiscardReported hidden-discard-reported",
		"synthetic/app.go hiddenIdiomStderr hidden-idiom-stderr",
		"synthetic/app.go hiddenIdiomDiscard hidden-idiom-discard",
	} {
		exceptions[key] = flagParseGuardException{route: "internal synthetic", reason: flagParseGuardHiddenReason}
	}
	failures, examined := flagParseOutputFindings(report, exceptions)
	joined := strings.Join(failures, "\n")
	t.Logf("positive control failures:\n%s", joined)
	if examined != len(exceptions) {
		t.Errorf("positive control: examined %d exception sites, want %d", examined, len(exceptions))
	}
	for _, want := range []string{
		`func hiddenBare, FlagSet "hidden-bare" (FlagSet.Parse): the FlagSet writes to stderr, so the flag package has already printed the reason and the usage; this hidden route returns bare`,
		`func hiddenUsage, FlagSet "hidden-usage" (FlagSet.Parse): the FlagSet writes to stderr, so the flag package has already printed the reason and the usage; this hidden route returns usage-marked`,
		`func hiddenReprint, FlagSet "hidden-reprint" (FlagSet.Parse): the parse error path prints the usage again (printFooUsage)`,
		`func hiddenDiscardReported, FlagSet "hidden-discard-reported" (FlagSet.Parse): the FlagSet discards its output, so nothing printed the reason; return the parse error itself`,
		`func stderrPlain, FlagSet "stderr-plain" (FlagSet.Parse): the FlagSet writes to stderr`,
		`func defaultPlain, FlagSet "default-plain" (FlagSet.Parse): the FlagSet writes to stderr`,
		`func stderrReprint, FlagSet "stderr-reprint" (FlagSet.Parse): the parse error path prints the usage again (printFooUsage)`,
		`func stderrUsageLiteral, FlagSet "stderr-usage-literal" (FlagSet.Parse): the parse error path prints the usage again (showHelp)`,
		`func discardReported, FlagSet "discard-reported" (FlagSet.Parse): the FlagSet discards its output`,
		`FlagSet "param fs of parseHelper" (FlagSet.Parse): the parse error path prints the usage again (printFooUsage in caller helperCallerReprint)`,
		`FlagSet "param fs of mixedHelper" (FlagSet.Parse): cannot tell where the FlagSet writes its reason (mixed: callers disagree on the FlagSet output)`,
		`func stderrUsageLiteral, FlagSet "stderr-usage-literal" (FlagSet.Parse): the public FlagSet does not print its catalog Usage after the reason (usage: custom)`,
		`func stderrDefaultUsage, FlagSet "stderr-default-usage" (FlagSet.Parse): the public FlagSet does not print its catalog Usage after the reason (usage: default)`,
		`func stderrCustomUsage, FlagSet "stderr-custom-usage" (FlagSet.Parse): the public FlagSet does not print its catalog Usage after the reason (usage: custom)`,
		`func discardPlainUsage, FlagSet "discard-plain-usage" (FlagSet.Parse): the public FlagSet discards its output, so no catalog Usage follows the reason`,
		`FlagSet "param fs of usageHelper" (FlagSet.Parse): the public FlagSet does not print its catalog Usage after the reason (usage: mixed: callers disagree on the FlagSet usage)`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("positive control: missing failure containing %q", want)
		}
	}
	for _, clean := range []string{`"idiom-stderr"`, `"idiom-discard"`, `"idiom-helper"`, `"hidden-idiom-stderr"`, `"hidden-idiom-discard"`, `"internal default-usage"`} {
		if strings.Contains(joined, clean) {
			t.Errorf("positive control: %s must pass the output guard", clean)
		}
	}
	if len(failures) != 16 {
		t.Errorf("positive control: got %d failures, want 16", len(failures))
	}
}

// TestFlagParseUsageGuardPositiveControl runs the same analyzer and policy on
// synthetic source: bare returns, directly or through wrappers of wrappers,
// wrapped help, a reported error without a usage marker outside the exception
// rows, and unfollowable FlagSets must fail; the idiom and a reported hidden
// exception row must pass.
func TestFlagParseUsageGuardPositiveControl(t *testing.T) {
	t.Parallel()
	const pkgPath = "example.test/guard/internal/app"
	const src = `package app

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
)

type UsageError struct{ Message string }

func (e *UsageError) Error() string            { return e.Message }
func (e *UsageError) MetadataUsageError() bool { return true }

func usageError(message string) error { return &UsageError{Message: message} }

func usagef(format string, args ...any) error { return usageError(fmt.Sprintf(format, args...)) }

type reportedError struct{ cause error }

func (e *reportedError) Error() string         { return e.cause.Error() }
func (e *reportedError) FailureReported() bool { return true }

func flagParseReported(err error) error { return &reportedError{cause: err} }

func wrap(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func wrapWrap(fs *flag.FlagSet, args []string) ([]string, error) {
	return wrap(fs, args)
}

func register(fs *flag.FlagSet) { fs.Bool("x", false, "") }

func bareDirect(args []string) error {
	fs := flag.NewFlagSet("bare-direct", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return nil
}

func bareViaWrapper(args []string) error {
	fs := flag.NewFlagSet("bare-wrapper", flag.ContinueOnError)
	register(fs)
	_, err := wrapWrap(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

func helpWrapped(args []string) error {
	fs := flag.NewFlagSet("help-wrapped", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageError("help")
		}
		return usageError(err.Error())
	}
	return nil
}

func idiomDirect(args []string) error {
	fs := flag.NewFlagSet("idiom-direct", flag.ContinueOnError)
	if _, err := url.Parse("x"); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	return nil
}

func idiomViaWrapper(args []string) error {
	fs := flag.NewFlagSet("idiom-wrapper", flag.ContinueOnError)
	_, err := wrapWrap(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usagef("parse: %v", err)
	}
	return nil
}

func idiomLiteral(args []string) error {
	fs := flag.NewFlagSet("idiom-literal", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &UsageError{Message: err.Error()}
	}
	return nil
}

func reportedPublic(args []string) error {
	fs := flag.NewFlagSet("reported-public", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return flagParseReported(err)
	}
	return nil
}

func hiddenReported(args []string) error {
	fs := flag.NewFlagSet("hidden-reported", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return flagParseReported(err)
	}
	return nil
}

type holder struct{ fs *flag.FlagSet }

func escapes() *holder {
	fs := flag.NewFlagSet("escapes", flag.ContinueOnError)
	return &holder{fs: fs}
}
`
	report := flagParseGuardAnalyze([]flagParseGuardPackage{{
		importPath: pkgPath,
		scan:       true,
		files:      []flagParseGuardFile{{name: "synthetic/app.go", src: []byte(src)}},
	}})
	const hiddenKey = "synthetic/app.go hiddenReported hidden-reported"
	failures, used := flagParseGuardFindings(report, map[string]flagParseGuardException{
		hiddenKey: {route: "internal synthetic", reason: flagParseGuardHiddenReason},
	})
	joined := strings.Join(failures, "\n")
	t.Logf("positive control failures:\n%s", joined)
	if !used[hiddenKey] {
		t.Errorf("positive control: the reported hidden exception row was not used")
	}

	for _, want := range []string{
		`func reportedPublic, FlagSet "reported-public" (FlagSet.Parse): the parse error path is reported, not usage-marked`,
		`func bareDirect, FlagSet "bare-direct" (FlagSet.Parse): the parse error path is bare`,
		`func bareViaWrapper, FlagSet "bare-wrapper" (parse wrapper wrapWrap): the parse error path is not usage-marked`,
		`func helpWrapped, FlagSet "help-wrapped" (FlagSet.Parse): the flag.ErrHelp branch returns a usage error`,
		`flag.FlagSet is spelled outside a *flag.FlagSet function parameter`,
		`FlagSet fs escapes in escapes`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("positive control: missing failure containing %q", want)
		}
	}
	for _, clean := range []string{"idiom-direct", "idiom-wrapper", "idiom-literal", "hidden-reported", "func wrap,", "func wrapWrap,"} {
		if strings.Contains(joined, clean) {
			t.Errorf("positive control: %q must pass the guard", clean)
		}
	}
	if len(failures) != 6 {
		t.Errorf("positive control: got %d failures, want 6", len(failures))
	}
	if !slices.Equal(report.wrappers, []string{pkgPath + ".wrap", pkgPath + ".wrapWrap"}) {
		t.Errorf("positive control: wrappers = %v, want wrap and wrapWrap", report.wrappers)
	}
	var sites []string
	for _, site := range report.sites {
		sites = append(sites, site.flagSet+"="+site.verdict.String())
	}
	want := []string{"bare-direct=bare", "bare-wrapper=not usage-marked", "help-wrapped=usage-marked", "idiom-direct=usage-marked", "idiom-wrapper=usage-marked", "idiom-literal=usage-marked", "reported-public=reported, not usage-marked", "hidden-reported=reported, not usage-marked"}
	if !slices.Equal(sites, want) {
		t.Errorf("positive control: sites = %v, want the eight parse sites (wrapper internals are not sites)", sites)
	}
}
