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

// flagParseGuardException is one parse site that may keep returning its parse
// error without a usage marker. Only hidden `projmux internal ...` routes may
// appear here; the guard asserts it.
type flagParseGuardException struct {
	route  string
	reason string
}

// flagParseGuardExceptions is keyed by flagParseGuardSite.key(): the
// repo-relative file, the enclosing function, and the FlagSet name (the first
// argument of flag.NewFlagSet).
var flagParseGuardExceptions = map[string]flagParseGuardException{
	`internal/app/ai.go (*aiCommand).runPicker ai picker`:                                                   {route: "internal agent-pane picker", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go (*aiCommand).runIngest internal agent-hook ingest antigravity-hook`:          {route: "internal agent-hook ingest antigravity-hook", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go parseAIHookPaneArgument "internal agent-hook ingest " + route`:               {route: "internal agent-hook ingest codex-hook|claude-hook", reason: flagParseGuardHiddenReason},
	`internal/app/ai_ingest.go (*aiCommand).runIngestBell internal agent-hook ingest bell`:                  {route: "internal agent-hook ingest bell", reason: flagParseGuardHiddenReason},
	`internal/app/claude_question_hook.go (claudeQuestionHook).run "internal " + claudeQuestionHookRoute`:   {route: "internal claude-question-hook", reason: flagParseGuardHiddenReason},
	`internal/app/claude_question_popup.go runClaudeQuestionPicker "internal " + claudeQuestionPickerRoute`: {route: "internal claude-question-picker", reason: flagParseGuardHiddenReason},
	`internal/app/focus.go parseFocusArgs focus`:                                                            {route: "internal focus", reason: flagParseGuardHiddenReason},
	`internal/app/hook_trust_popup.go (*tmuxCommand).runHookTrustPromptWithReader tmux hook-trust-prompt`:   {route: "internal tmux hook-trust-prompt", reason: flagParseGuardHiddenReason},
	`internal/app/key_broker.go (*keyBrokerCommand).Run key-broker`:                                         {route: "internal key-broker", reason: flagParseGuardHiddenReason},
	`internal/app/preview.go (*previewCommand).Run preview`:                                                 {route: "internal preview", reason: flagParseGuardHiddenReason},
	`internal/app/session_popup.go (*sessionPopupCommand).Run session-popup`:                                {route: "internal session-popup", reason: flagParseGuardHiddenReason},
	`internal/app/split_selection_continuation.go parseSplitSelectionArgs spelling`:                         {route: "internal agent-pane launch-selection", reason: flagParseGuardHiddenReason},
	`internal/app/status.go (*statusCommand).runNotify status notify`:                                       {route: "internal status notify", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).Run tmux`:                                                          {route: "internal tmux", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runAutosaveSessionState tmux autosave-session-state`:               {route: "internal tmux autosave-session-state", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runConverge internal tmux converge`:                                {route: "internal tmux converge", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runDeleteConfirmIntent tmux delete-confirm`:                        {route: "internal tmux delete-confirm", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runInstall tmux install`:                                           {route: "internal tmux install", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runInstallApp tmux install-app`:                                    {route: "internal tmux install-app", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runPaneMenuAction tmux pane-menu`:                                  {route: "internal tmux pane-menu", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runWindowCreateIntent tmux window-create`:                          {route: "internal tmux window-create", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go (*tmuxCommand).runWindowDeleteIntent tmux window-delete`:                          {route: "internal tmux window-delete", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go parseRenameIntentArgs "tmux " + route`:                                            {route: "internal tmux window-rename|pane-rename", reason: flagParseGuardHiddenReason},
	`internal/app/tmux.go parseTmuxPopupToggleArgs tmux popup-toggle`:                                       {route: "internal tmux popup-toggle", reason: flagParseGuardHiddenReason},
	`internal/app/usagecmd/usage.go (*Command).RunStatus status usage`:                                      {route: "internal status usage", reason: flagParseGuardHiddenReason},
}

// flagParseGuardVerdict classifies the non-help error path of one parse site.
type flagParseGuardVerdict int

const (
	// flagParseGuardMarked: every non-help return constructs a usage marker.
	flagParseGuardMarked flagParseGuardVerdict = iota
	// flagParseGuardBare: every non-help return passes the parse error through.
	flagParseGuardBare
	// flagParseGuardOther: anything else (nil, wrapped, discarded, no return).
	flagParseGuardOther
)

func (v flagParseGuardVerdict) String() string {
	switch v {
	case flagParseGuardMarked:
		return "usage-marked"
	case flagParseGuardBare:
		return "bare"
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
	sites    []flagParseGuardSite
	markers  []string
	ctors    []string
	wrappers []string
	problems []string
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
	fset     *token.FileSet
	files    []*flagParseGuardParsedFile
	markers  map[string]bool // importPath.Type
	ctors    map[string]bool // importPath.func
	report   flagParseGuardReport
	funcs    map[string]map[string][]flagParseGuardFunc // pkg -> lookup name -> decls
	direct   []flagParseGuardCall
	calls    []flagParseGuardCall
	wrappers map[string]map[string]map[int]bool // pkg -> func -> param idx
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
	a.deriveMarkers()
	for _, pf := range scanned {
		a.collectFuncs(pf)
	}
	for _, pf := range scanned {
		a.collectCalls(pf)
	}
	a.resolveSites()
	a.report.markers = flagParseGuardSortedKeys(a.markers)
	a.report.ctors = flagParseGuardSortedKeys(a.ctors)
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

// deriveMarkers finds every type declaring `MetadataUsageError() bool` that
// returns true, then every package-level function whose returns all construct
// such a marker (usageError and friends), to a fixpoint.
func (a *flagParseGuardAnalyzer) deriveMarkers() {
	for _, pf := range a.files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "MetadataUsageError" || fn.Body == nil || len(fn.Recv.List) != 1 {
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
				a.markers[pf.pkg+"."+id.Name] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, pf := range a.files {
			for _, decl := range pf.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil || a.ctors[pf.pkg+"."+fn.Name.Name] {
					continue
				}
				returns := flagParseGuardReturns(fn.Body)
				if len(returns) == 0 {
					continue
				}
				all := true
				for _, ret := range returns {
					if len(ret.Results) == 0 || !a.isMarker(pf, ret.Results[len(ret.Results)-1]) {
						all = false
						break
					}
				}
				if all {
					a.ctors[pf.pkg+"."+fn.Name.Name] = true
					changed = true
				}
			}
		}
	}
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
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return a.isMarker(pf, e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if lit, ok := e.X.(*ast.CompositeLit); ok {
				return a.isMarker(pf, lit)
			}
		}
	case *ast.CompositeLit:
		return a.markers[a.qualify(pf, e.Type)]
	case *ast.CallExpr:
		return a.ctors[a.qualify(pf, e.Fun)]
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
					info := &flagParseGuardFlagSet{name: "param " + name.Name + " of " + fnName, wrapperIdx: -1}
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
		flagSets[pf.locals[id]] = &flagParseGuardFlagSet{name: name, wrapperIdx: -1}
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
		if len(parent.Rhs) != 1 || len(parent.Lhs) == 0 {
			return unclassified("multi-value assignment")
		}
		id, ok := parent.Lhs[len(parent.Lhs)-1].(*ast.Ident)
		if !ok {
			return unclassified("the error is not assigned to a variable")
		}
		if id.Name == "_" {
			site.verdict = flagParseGuardOther
			site.detail = "discards the parse error"
			return site
		}
		errName = id.Name
		if len(c.stack) < 2 {
			return unclassified("no enclosing statement")
		}
		var cond ast.Expr
		switch gp := c.stack[len(c.stack)-2].(type) {
		case *ast.IfStmt:
			if gp.Init != parent {
				return unclassified("assignment is not the if initializer")
			}
			cond, body = gp.Cond, gp.Body
		default:
			list := flagParseGuardStmtList(gp)
			i := slices.IndexFunc(list, func(s ast.Stmt) bool { return s == parent })
			if i < 0 || i+1 >= len(list) {
				return unclassified("the assignment is not followed by an error check")
			}
			next, ok := list[i+1].(*ast.IfStmt)
			if !ok || next.Init != nil {
				return unclassified("the assignment is not followed by `if " + errName + " != nil`")
			}
			cond, body = next.Cond, next.Body
		}
		if !flagParseGuardChecksNonNil(cond, errName) {
			return unclassified("the error check does not test " + errName + " != nil")
		}
	default:
		return unclassified("unsupported statement shape")
	}

	var marked, bare, other int
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
	switch {
	case marked+bare+other == 0:
		site.verdict = flagParseGuardOther
		site.detail = "the error branch does not return"
	case bare == 0 && other == 0:
		site.verdict = flagParseGuardMarked
	case marked == 0 && other == 0:
		site.verdict = flagParseGuardBare
		site.detail = "returns " + errName + " unchanged"
	default:
		site.verdict = flagParseGuardOther
		site.detail = strings.Join(details, "; ")
		if bare > 0 {
			site.detail = strings.TrimPrefix(site.detail+"; returns "+errName+" unchanged", "; ")
		}
	}
	return site
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
// an exception row. It returns the failures and the exception keys it used.
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

// TestFlagParseUsageGuardPositiveControl runs the same analyzer and policy on
// synthetic source: bare returns, directly or through wrappers of wrappers,
// wrapped help, and unfollowable FlagSets must fail; the idiom must pass.
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
	failures, _ := flagParseGuardFindings(report, nil)
	joined := strings.Join(failures, "\n")
	t.Logf("positive control failures:\n%s", joined)

	for _, want := range []string{
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
	for _, clean := range []string{"idiom-direct", "idiom-wrapper", "idiom-literal", "func wrap,", "func wrapWrap,"} {
		if strings.Contains(joined, clean) {
			t.Errorf("positive control: %q must pass the guard", clean)
		}
	}
	if len(failures) != 5 {
		t.Errorf("positive control: got %d failures, want 5", len(failures))
	}
	if !slices.Equal(report.wrappers, []string{pkgPath + ".wrap", pkgPath + ".wrapWrap"}) {
		t.Errorf("positive control: wrappers = %v, want wrap and wrapWrap", report.wrappers)
	}
	var sites []string
	for _, site := range report.sites {
		sites = append(sites, site.flagSet+"="+site.verdict.String())
	}
	want := []string{"bare-direct=bare", "bare-wrapper=not usage-marked", "help-wrapped=usage-marked", "idiom-direct=usage-marked", "idiom-wrapper=usage-marked", "idiom-literal=usage-marked"}
	if !slices.Equal(sites, want) {
		t.Errorf("positive control: sites = %v, want the six parse sites (wrapper internals are not sites)", sites)
	}
}
