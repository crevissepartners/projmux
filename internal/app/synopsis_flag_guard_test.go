package app

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// synopsisSiteException excludes one whole public flag parse site whose
// FlagSet carries its parent's name: the flags are a dispatch verb's, not the
// route's. route is the route the site must still resolve to.
type synopsisSiteException struct {
	route, reason string
}

// synopsisFlagSiteExceptions is keyed by flagParseGuardSite.key(). It is
// empty: every dispatch verb is its own catalog child with a FlagSet named
// after it, so every site resolves to the route whose flags it parses. A row
// no site has, or whose site resolves elsewhere, fails.
var synopsisFlagSiteExceptions = map[string]synopsisSiteException{}

// synopsisFlagException admits one registered flag a route's Usage may omit.
// Two categories exist:
//
//   - (a) an ignored compatibility no-op flag: refusedBy is empty.
//   - (c) a flag the route's shared parser registers but the route refuses
//     with a usage error: refusedBy names the refusing function as
//     "file:symbol", and probe is the argv after `projmux` that runs the
//     route with the flag through a side-effect-free test seam
//     (TestSynopsisFlagRefusalRowsStillRefuse). A row whose refusal would
//     need side effects to observe leaves probe nil and is checked statically:
//     the named function must exist and its body must mention the flag.
type synopsisFlagException struct {
	reason    string
	refusedBy string
	probe     []string
}

// synopsisProviderShortcutReason explains the category (c) rows of the
// provider shortcuts: `create <provider>` shares the Agent create parser.
const synopsisProviderShortcutReason = "registered by the shared Agent create parser but refused on this provider shortcut before anything is read or written"

// synopsisFlagExceptions is keyed "<route> <flag name>" (the flag name
// without dashes). A row whose flag is no longer registered on that route, is
// already in its Usage, or (category c) is no longer refused, is stale.
var synopsisFlagExceptions = map[string]synopsisFlagException{
	"create codex provider":                  {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/create_agent.go:(*createCommand).resolveCreateProvider", probe: []string{"create", "codex", "--provider", "codex"}},
	"create codex dialogue-reply-only":       {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_dialogue_profile.go:requireClaudeDialogueMode", probe: []string{"create", "codex", "--dialogue-reply-only"}},
	"create claude provider":                 {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/create_agent.go:(*createCommand).resolveCreateProvider", probe: []string{"create", "claude", "--provider", "claude"}},
	"create claude interactive-only":         {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/codex_native_thread.go:requireInteractiveOnlyProvider", probe: []string{"create", "claude", "--interactive-only"}},
	"create antigravity provider":            {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/create_agent.go:(*createCommand).resolveCreateProvider", probe: []string{"create", "antigravity", "--provider", "antigravity"}},
	"create antigravity model":               {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_launch_options.go:requireClaudeLaunchOptions", probe: []string{"create", "antigravity", "--model", "opus"}},
	"create antigravity effort":              {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_launch_options.go:requireClaudeLaunchOptions", probe: []string{"create", "antigravity", "--effort", "high"}},
	"create antigravity interactive-only":    {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/codex_native_thread.go:requireInteractiveOnlyProvider", probe: []string{"create", "antigravity", "--interactive-only"}},
	"create antigravity instructions":        {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_launch_options.go:requirePersonaLane", probe: []string{"create", "antigravity", "--instructions", "reviewer"}},
	"create antigravity persona":             {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_launch_options.go:requirePersonaLane", probe: []string{"create", "antigravity", "--persona", "reviewer"}},
	"create antigravity dialogue-reply-only": {reason: synopsisProviderShortcutReason, refusedBy: "internal/app/claude_dialogue_profile.go:requireClaudeDialogueMode", probe: []string{"create", "antigravity", "--dialogue-reply-only"}},
}

// synopsisFlagAliases is the closed table of flag spellings that count as one
// flag in a synopsis: either spelling in the Usage covers both. A pair only
// covers a route whose parser binds both names to the same variable; the guard
// derives that binding per route, so a pair the parser binds apart is a
// missing flag. A pair no route relies on is stale.
var synopsisFlagAliases = [][2]string{
	{"f", "force"},
	{"o", "output"},
}

// synopsisFlagRegistrars maps a FlagSet method that defines a flag to the
// position of its name argument.
var synopsisFlagRegistrars = map[string]int{
	"Bool": 0, "BoolVar": 1, "BoolFunc": 0,
	"Duration": 0, "DurationVar": 1,
	"Float64": 0, "Float64Var": 1,
	"Func": 0,
	"Int":  0, "IntVar": 1, "Int64": 0, "Int64Var": 1,
	"String": 0, "StringVar": 1,
	"TextVar": 1,
	"Uint":    0, "UintVar": 1, "Uint64": 0, "Uint64Var": 1,
	"Var": 1,
}

// synopsisFlagVarArg reports whether the registrar binds the flag to its first
// argument (the *Var forms) rather than to a pointer it returns.
func synopsisFlagVarArg(method string) bool { return synopsisFlagRegistrars[method] == 1 }

// synopsisFlagName spells a flag the way a synopsis prints it.
func synopsisFlagName(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// synopsisFlagToken matches a flag token in a synopsis line: a dash-led word
// at the start of the line or after a space, bracket, parenthesis or bar.
var synopsisFlagToken = regexp.MustCompile(`(?:^|[\s\[(|])(--?[A-Za-z][A-Za-z0-9-]*)`)

// synopsisFlagTokens is the set of flag names (without dashes) a route's Usage
// lines spell.
func synopsisFlagTokens(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, line := range lines {
		for _, m := range synopsisFlagToken.FindAllStringSubmatch(line, -1) {
			out[strings.TrimLeft(m[1], "-")] = true
		}
	}
	return out
}

// synopsisValueKind is the kind of an abstract value.
type synopsisValueKind int

const (
	synopsisUnknown synopsisValueKind = iota
	synopsisString
	synopsisSymbol // a constant of another package, compared by its spelling
	synopsisBool
	synopsisComposite // a composite literal: a struct or a map
)

// synopsisValue is what the evaluator knows about an expression.
type synopsisValue struct {
	kind synopsisValueKind
	s    string
	b    bool
	lit  *ast.CompositeLit
	fr   *synopsisFrame
}

// synopsisArg is the expression a parameter is bound to, in its caller frame.
type synopsisArg struct {
	expr ast.Expr
	fr   *synopsisFrame
}

// synopsisFrame is one evaluation scope: a function declaration entered from a
// known call (or from none), or a function literal called inside one.
type synopsisFrame struct {
	file   string
	decl   *ast.FuncDecl
	ftype  *ast.FuncType          // the parameters of this frame
	body   *ast.BlockStmt         // where this frame's locals are defined
	params map[string]synopsisArg // bound parameters (and receiver)
	binds  map[string]string      // printed expression -> value unified from a route
	fs     map[string]bool        // identifiers naming the tracked FlagSet
	lits   map[string]*ast.FuncLit
	parent *synopsisFrame // the enclosing frame of a function literal
	id     string
}

func (fr *synopsisFrame) bound(key string) (string, bool) {
	for f := fr; f != nil; f = f.parent {
		if v, ok := f.binds[key]; ok {
			return v, true
		}
	}
	return "", false
}

func (fr *synopsisFrame) isFlagSet(name string) bool {
	for f := fr; f != nil; f = f.parent {
		if f.fs[name] {
			return true
		}
		if _, ok := f.params[name]; ok {
			return false
		}
	}
	return false
}

func (fr *synopsisFrame) funcLit(name string) *ast.FuncLit {
	for f := fr; f != nil; f = f.parent {
		if lit, ok := f.lits[name]; ok {
			return lit
		}
	}
	return nil
}

// synopsisFlagEngine enumerates the flags a FlagSet registers, following
// same-package helpers that receive it and evaluating the conditions that
// guard a registration against what the route spelling determines.
type synopsisFlagEngine struct {
	eval    *flagSetNameEval
	imports map[string]map[string]bool // file -> import names
	vars    map[string]map[string]ast.Expr
	types   map[string]map[string]*ast.StructType
	frames  int
}

func newSynopsisFlagEngine(eval *flagSetNameEval) *synopsisFlagEngine {
	e := &synopsisFlagEngine{eval: eval, imports: map[string]map[string]bool{}, vars: map[string]map[string]ast.Expr{}, types: map[string]map[string]*ast.StructType{}}
	for name, file := range eval.files {
		pkg := eval.pkgOf[name]
		e.imports[name] = map[string]bool{}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			local := path[strings.LastIndex(path, "/")+1:]
			if spec.Name != nil {
				local = spec.Name.Name
			}
			e.imports[name][local] = true
		}
		if e.vars[pkg] == nil {
			e.vars[pkg] = map[string]ast.Expr{}
			e.types[pkg] = map[string]*ast.StructType{}
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					if gen.Tok != token.VAR {
						continue
					}
					for i, id := range s.Names {
						if i < len(s.Values) {
							e.vars[pkg][id.Name] = s.Values[i]
						}
					}
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						e.types[pkg][s.Name.Name] = st
					}
				}
			}
		}
	}
	return e
}

func (e *synopsisFlagEngine) newFrame(file string, decl *ast.FuncDecl, parent *synopsisFrame) *synopsisFrame {
	e.frames++
	fr := &synopsisFrame{file: file, decl: decl, params: map[string]synopsisArg{}, binds: map[string]string{}, fs: map[string]bool{}, lits: map[string]*ast.FuncLit{}, parent: parent, id: "f" + strconv.Itoa(e.frames)}
	if decl != nil {
		fr.ftype, fr.body = decl.Type, decl.Body
	}
	return fr
}

// declaresParam reports whether name is a parameter or the receiver of fr.
func (fr *synopsisFrame) declaresParam(name string) bool {
	lists := []*ast.FieldList{}
	if fr.ftype != nil {
		lists = append(lists, fr.ftype.Params)
	}
	if fr.parent == nil && fr.decl != nil && fr.decl.Recv != nil {
		lists = append(lists, fr.decl.Recv)
	}
	for _, list := range lists {
		for _, field := range list.List {
			for _, id := range field.Names {
				if id.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// defs lists the expressions assigned to the local name in fr's body, and
// whether one of them is a plain reassignment.
func (fr *synopsisFrame) defs(name string) ([]ast.Expr, bool) {
	if fr.body == nil {
		return nil, false
	}
	var out []ast.Expr
	reassigned := false
	ast.Inspect(fr.body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != name {
					continue
				}
				switch {
				case len(x.Lhs) == len(x.Rhs):
					out = append(out, x.Rhs[i])
				case len(x.Rhs) == 1 && i == 0:
					// v, ok := m[k] (or a call): the first result.
					out = append(out, x.Rhs[0])
				default:
					out = append(out, nil)
				}
				if x.Tok != token.DEFINE {
					reassigned = true
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				if id.Name == name && i < len(x.Values) {
					out = append(out, x.Values[i])
				}
			}
		case *ast.RangeStmt:
			for _, v := range []ast.Expr{x.Key, x.Value} {
				if id, ok := v.(*ast.Ident); ok && id.Name == name {
					out = append(out, nil)
				}
			}
		}
		return true
	})
	return out, reassigned
}

// fieldAssigns lists the expressions assigned to the printed selector sel in
// fr's body (`request.flags = ...`).
func (fr *synopsisFrame) fieldAssigns(sel string) []ast.Expr {
	if fr.body == nil {
		return nil
	}
	var out []ast.Expr
	ast.Inspect(fr.body, func(n ast.Node) bool {
		if x, ok := n.(*ast.AssignStmt); ok && len(x.Lhs) == len(x.Rhs) {
			for i, lhs := range x.Lhs {
				if types.ExprString(lhs) == sel {
					out = append(out, x.Rhs[i])
				}
			}
		}
		return true
	})
	return out
}

func synopsisKnown(v synopsisValue) bool { return v.kind != synopsisUnknown }

// value evaluates expr in fr as far as the source determines it.
func (e *synopsisFlagEngine) value(expr ast.Expr, fr *synopsisFrame, depth int) synopsisValue {
	if expr == nil || depth > 24 {
		return synopsisValue{}
	}
	if v, ok := fr.bound(types.ExprString(expr)); ok {
		return synopsisValue{kind: synopsisString, s: v}
	}
	switch x := expr.(type) {
	case *ast.ParenExpr:
		return e.value(x.X, fr, depth+1)
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			if err == nil {
				return synopsisValue{kind: synopsisString, s: s}
			}
		}
		return synopsisValue{}
	case *ast.CompositeLit:
		return synopsisValue{kind: synopsisComposite, lit: x, fr: fr}
	case *ast.Ident:
		return e.identValue(x.Name, fr, depth)
	case *ast.SelectorExpr:
		return e.selectorValue(x, fr, depth)
	case *ast.IndexExpr:
		m := e.value(x.X, fr, depth+1)
		key := e.value(x.Index, fr, depth+1)
		if m.kind != synopsisComposite || key.kind != synopsisString {
			return synopsisValue{}
		}
		if _, ok := m.lit.Type.(*ast.MapType); !ok {
			return synopsisValue{}
		}
		for _, elt := range m.lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return synopsisValue{}
			}
			if k := e.value(kv.Key, m.fr, depth+1); k.kind == synopsisString && k.s == key.s {
				return e.value(kv.Value, m.fr, depth+1)
			}
		}
		return synopsisValue{}
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			if v := e.value(x.X, fr, depth+1); v.kind == synopsisBool {
				return synopsisValue{kind: synopsisBool, b: !v.b}
			}
		}
		return synopsisValue{}
	case *ast.BinaryExpr:
		return e.binaryValue(x, fr, depth)
	}
	return synopsisValue{}
}

func (e *synopsisFlagEngine) binaryValue(x *ast.BinaryExpr, fr *synopsisFrame, depth int) synopsisValue {
	l := e.value(x.X, fr, depth+1)
	switch x.Op {
	case token.LAND, token.LOR:
		stop := x.Op == token.LOR // true || _ is true; false && _ is false
		if l.kind == synopsisBool && l.b == stop {
			return l
		}
		r := e.value(x.Y, fr, depth+1)
		if r.kind == synopsisBool && r.b == stop {
			return r
		}
		if l.kind == synopsisBool && r.kind == synopsisBool {
			return synopsisValue{kind: synopsisBool, b: !stop}
		}
		return synopsisValue{}
	}
	r := e.value(x.Y, fr, depth+1)
	switch x.Op {
	case token.ADD:
		if l.kind == synopsisString && r.kind == synopsisString {
			return synopsisValue{kind: synopsisString, s: l.s + r.s}
		}
	case token.EQL, token.NEQ:
		if l.kind == r.kind && (l.kind == synopsisString || l.kind == synopsisSymbol || l.kind == synopsisBool) {
			eq := l.s == r.s && l.b == r.b
			return synopsisValue{kind: synopsisBool, b: eq == (x.Op == token.EQL)}
		}
	}
	return synopsisValue{}
}

func (e *synopsisFlagEngine) identValue(name string, fr *synopsisFrame, depth int) synopsisValue {
	switch name {
	case "true", "false":
		return synopsisValue{kind: synopsisBool, b: name == "true"}
	}
	for f := fr; f != nil; f = f.parent {
		if arg, ok := f.params[name]; ok {
			if arg.fr == nil {
				return synopsisValue{}
			}
			return e.value(arg.expr, arg.fr, depth+1)
		}
		if f.declaresParam(name) {
			return synopsisValue{}
		}
		defs, reassigned := f.defs(name)
		if len(defs) > 0 {
			if reassigned || len(defs) > 1 {
				// Every assignment must agree.
				var first synopsisValue
				for i, def := range defs {
					v := e.value(def, f, depth+1)
					if !synopsisKnown(v) || (i > 0 && (v.kind != first.kind || v.s != first.s || v.b != first.b || v.lit != first.lit)) {
						return synopsisValue{}
					}
					first = v
				}
				return first
			}
			return e.value(defs[0], f, depth+1)
		}
	}
	pkg := e.eval.pkgOf[fr.file]
	if c, ok := e.eval.consts[pkg][name]; ok {
		return e.value(c, e.newFrame(fr.file, nil, nil), depth+1)
	}
	if v, ok := e.vars[pkg][name]; ok {
		return e.value(v, e.newFrame(fr.file, nil, nil), depth+1)
	}
	return synopsisValue{}
}

// isLocal reports whether name is bound in fr or an enclosing frame.
func (e *synopsisFlagEngine) isLocal(name string, fr *synopsisFrame) bool {
	for f := fr; f != nil; f = f.parent {
		if _, ok := f.params[name]; ok || f.declaresParam(name) {
			return true
		}
		if defs, _ := f.defs(name); len(defs) > 0 {
			return true
		}
	}
	return false
}

func (e *synopsisFlagEngine) selectorValue(x *ast.SelectorExpr, fr *synopsisFrame, depth int) synopsisValue {
	if id, ok := x.X.(*ast.Ident); ok && e.imports[fr.file][id.Name] && !e.isLocal(id.Name, fr) {
		return synopsisValue{kind: synopsisSymbol, s: types.ExprString(x)}
	}
	sel := types.ExprString(x)
	for f := fr; f != nil; f = f.parent {
		if assigns := f.fieldAssigns(sel); len(assigns) > 0 {
			if len(assigns) != 1 {
				return synopsisValue{}
			}
			return e.value(assigns[0], f, depth+1)
		}
	}
	base := e.value(x.X, fr, depth+1)
	if base.kind != synopsisComposite {
		if field, ok := e.receiverField(x, fr); ok {
			return e.value(field, e.newFrame(fr.file, nil, nil), depth+1)
		}
		return synopsisValue{}
	}
	for _, elt := range base.lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return synopsisValue{}
		}
		if k, ok := kv.Key.(*ast.Ident); ok && k.Name == x.Sel.Name {
			return e.value(kv.Value, base.fr, depth+1)
		}
	}
	// An omitted field is its zero value when the struct type says what it is.
	if id, ok := base.lit.Type.(*ast.Ident); ok {
		if st := e.types[e.eval.pkgOf[base.fr.file]][id.Name]; st != nil {
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name != x.Sel.Name {
						continue
					}
					switch types.ExprString(field.Type) {
					case "bool":
						return synopsisValue{kind: synopsisBool}
					case "string":
						return synopsisValue{kind: synopsisString}
					}
				}
			}
		}
	}
	return synopsisValue{}
}

// receiverField resolves recv.field for the unbound receiver of a method to
// the one value the package ever gives that field: a single keyed composite
// literal of the receiver type, and no assignment to the field anywhere.
func (e *synopsisFlagEngine) receiverField(x *ast.SelectorExpr, fr *synopsisFrame) (ast.Expr, bool) {
	root := fr
	for root.parent != nil {
		root = root.parent
	}
	id, ok := x.X.(*ast.Ident)
	if !ok || root.decl == nil || root.decl.Recv == nil || len(root.decl.Recv.List) != 1 {
		return nil, false
	}
	recv := root.decl.Recv.List[0]
	if len(recv.Names) != 1 || recv.Names[0].Name != id.Name {
		return nil, false
	}
	if _, bound := root.params[id.Name]; bound {
		return nil, false
	}
	typ := recv.Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	typeName := types.ExprString(typ)
	pkg := e.eval.pkgOf[fr.file]
	var values []ast.Expr
	assigned := false
	for name, file := range e.eval.files {
		if e.eval.pkgOf[name] != pkg {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CompositeLit:
				if types.ExprString(v.Type) != typeName {
					return true
				}
				for _, elt := range v.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok && types.ExprString(kv.Key) == x.Sel.Name {
						values = append(values, kv.Value)
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == x.Sel.Name {
						assigned = true
					}
				}
			}
			return true
		})
	}
	if assigned || len(values) != 1 {
		return nil, false
	}
	return values[0], true
}

// synopsisPart is one piece of a flattened FlagSet name: a literal, or a hole
// that a route token fills.
type synopsisPart struct {
	lit  string
	hole string // printed expression, "" for a literal
	fr   *synopsisFrame
}

// patterns flattens a FlagSet name expression into its alternative spellings.
func (e *synopsisFlagEngine) patterns(expr ast.Expr, fr *synopsisFrame, depth int) [][]synopsisPart {
	if v := e.value(expr, fr, 0); v.kind == synopsisString {
		return [][]synopsisPart{{{lit: v.s}}}
	}
	hole := [][]synopsisPart{{{hole: types.ExprString(expr), fr: fr}}}
	if depth > 12 {
		return hole
	}
	switch x := expr.(type) {
	case *ast.ParenExpr:
		return e.patterns(x.X, fr, depth+1)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return hole
		}
		var out [][]synopsisPart
		for _, l := range e.patterns(x.X, fr, depth+1) {
			for _, r := range e.patterns(x.Y, fr, depth+1) {
				out = append(out, append(slices.Clone(l), r...))
			}
		}
		return out
	case *ast.Ident:
		for f := fr; f != nil; f = f.parent {
			if arg, ok := f.params[x.Name]; ok {
				if arg.fr == nil {
					return [][]synopsisPart{{{hole: x.Name, fr: f}}}
				}
				return e.patterns(arg.expr, arg.fr, depth+1)
			}
			if f.declaresParam(x.Name) {
				return [][]synopsisPart{{{hole: x.Name, fr: f}}}
			}
			if defs, _ := f.defs(x.Name); len(defs) > 0 {
				var out [][]synopsisPart
				for _, def := range defs {
					if def == nil {
						return hole
					}
					out = append(out, e.patterns(def, f, depth+1)...)
				}
				return out
			}
		}
	case *ast.SelectorExpr:
		sel := types.ExprString(x)
		for f := fr; f != nil; f = f.parent {
			if assigns := f.fieldAssigns(sel); len(assigns) == 1 {
				return e.patterns(assigns[0], f, depth+1)
			}
		}
		if base := e.value(x.X, fr, 0); base.kind == synopsisComposite {
			for _, elt := range base.lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == x.Sel.Name {
						return e.patterns(kv.Value, base.fr, depth+1)
					}
				}
			}
		}
	}
	return hole
}

// unify matches route against pattern. Every hole takes one route token (no
// spaces) and is bound in its frame, so conditions on that expression see the
// route's value. It reports whether the route matches.
func synopsisUnify(route string, pattern []synopsisPart) bool {
	var re strings.Builder
	re.WriteString("^")
	hole := "([^ ]+)"
	if len(pattern) == 1 {
		// A name that is one unknown expression takes the whole route.
		hole = "(.+)"
	}
	for _, part := range pattern {
		if part.hole == "" {
			re.WriteString(regexp.QuoteMeta(part.lit))
		} else {
			re.WriteString(hole)
		}
	}
	re.WriteString("$")
	m := regexp.MustCompile(re.String()).FindStringSubmatch(route)
	if m == nil {
		return false
	}
	i := 1
	for _, part := range pattern {
		if part.hole == "" {
			continue
		}
		if prev, ok := part.fr.binds[part.hole]; ok && prev != m[i] {
			return false
		}
		part.fr.binds[part.hole] = m[i]
		i++
	}
	return true
}

func synopsisResetBinds(pattern []synopsisPart) {
	for _, p := range pattern {
		if p.fr != nil {
			p.fr.binds = map[string]string{}
		}
	}
}

// synopsisReg is one flag definition reached from a FlagSet.
type synopsisReg struct {
	name string
	// variable identifies the storage the flag is bound to; two names bound
	// to one variable are spellings of one flag.
	variable string
	pos      string
}

// synopsisWalk collects the registrations of one FlagSet in one frame.
type synopsisWalk struct {
	e        *synopsisFlagEngine
	regs     []synopsisReg
	problems []string
	depth    int
	inLit    map[*ast.FuncLit]bool
	results  map[*ast.CallExpr]string // registration call -> variable its result is assigned to
}

func (w *synopsisWalk) pos(fr *synopsisFrame, p token.Pos) string {
	return fr.file + ":" + strconv.Itoa(w.e.eval.fset.Position(p).Line)
}

func (w *synopsisWalk) problem(fr *synopsisFrame, p token.Pos, msg string) {
	w.problems = append(w.problems, w.pos(fr, p)+": "+msg)
}

// variableKey names the storage expr denotes, following parameters to the
// argument their caller passed.
func (w *synopsisWalk) variableKey(expr ast.Expr, fr *synopsisFrame, depth int) string {
	for {
		switch x := expr.(type) {
		case *ast.ParenExpr:
			expr = x.X
			continue
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				expr = x.X
				continue
			}
		}
		break
	}
	if depth < 12 {
		switch x := expr.(type) {
		case *ast.Ident:
			for f := fr; f != nil; f = f.parent {
				if arg, ok := f.params[x.Name]; ok && arg.fr != nil {
					return w.variableKey(arg.expr, arg.fr, depth+1)
				}
				if f.declaresParam(x.Name) {
					break
				}
			}
		case *ast.SelectorExpr:
			return w.variableKey(x.X, fr, depth+1) + "." + x.Sel.Name
		}
	}
	root := fr
	for root.parent != nil {
		root = root.parent
	}
	return root.id + ":" + types.ExprString(expr)
}

// mentionsFlagSet reports whether n registers a flag on, or hands on, the
// tracked FlagSet (or calls a local function literal that may).
func (w *synopsisWalk) mentionsFlagSet(n ast.Node, fr *synopsisFrame) bool {
	found := false
	if n == nil {
		return false
	}
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && fr.isFlagSet(id.Name) {
				if _, reg := synopsisFlagRegistrars[sel.Sel.Name]; reg {
					found = true
				}
			}
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			if lit := fr.funcLit(id.Name); lit != nil && lit.Body != n && !w.inLit[lit] {
				w.inLit[lit] = true
				found = w.mentionsFlagSet(lit.Body, fr)
				delete(w.inLit, lit)
			}
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && fr.isFlagSet(id.Name) {
				found = true
			}
		}
		return !found
	})
	return found
}

// walk visits n in fr, taking only the branches the evaluator decides.
func (w *synopsisWalk) walk(n ast.Node, fr *synopsisFrame) {
	if n == nil {
		return
	}
	ast.Inspect(n, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.IfStmt:
			w.walk(x.Init, fr)
			cond := w.e.value(x.Cond, fr, 0)
			switch {
			case cond.kind == synopsisBool && cond.b:
				w.walk(x.Body, fr)
			case cond.kind == synopsisBool:
				w.walk(x.Else, fr)
			default:
				if w.mentionsFlagSet(x.Body, fr) || w.mentionsFlagSet(x.Else, fr) {
					w.problem(fr, x.Pos(), "the condition `"+types.ExprString(x.Cond)+"` guards a flag registration and is not statically known for this route")
				}
				w.walk(x.Body, fr)
				w.walk(x.Else, fr)
			}
			return false
		case *ast.SwitchStmt:
			w.walk(x.Init, fr)
			w.switchStmt(x, fr)
			return false
		case *ast.TypeSwitchStmt:
			if w.mentionsFlagSet(x.Body, fr) {
				w.problem(fr, x.Pos(), "a type switch guards a flag registration; the guard cannot decide it")
			}
			return true
		case *ast.AssignStmt:
			// `v := fs.Bool(...)` binds the flag to v, the pointer a later
			// `fs.BoolVar(v, ...)` may share.
			for i, rhs := range x.Rhs {
				if call, ok := rhs.(*ast.CallExpr); ok && i < len(x.Lhs) && len(x.Lhs) == len(x.Rhs) {
					w.results[call] = w.variableKey(x.Lhs[i], fr, 0)
				}
			}
			if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
				if lit, ok := x.Rhs[0].(*ast.FuncLit); ok {
					if id, ok := x.Lhs[0].(*ast.Ident); ok {
						fr.lits[id.Name] = lit
						return false
					}
				}
			}
			return true
		case *ast.FuncLit:
			// A literal not assigned to a local runs where the call site
			// decides; walk it as part of this frame.
			inner := w.e.newFrame(fr.file, fr.decl, fr)
			inner.ftype, inner.body = x.Type, x.Body
			w.walk(x.Body, inner)
			return false
		case *ast.CallExpr:
			return w.call(x, fr)
		}
		return true
	})
}

func (w *synopsisWalk) switchStmt(x *ast.SwitchStmt, fr *synopsisFrame) {
	var tag synopsisValue
	if x.Tag != nil {
		tag = w.e.value(x.Tag, fr, 0)
	} else {
		tag = synopsisValue{kind: synopsisBool, b: true}
	}
	var chosen, deflt *ast.CaseClause
	known := synopsisKnown(tag)
	for _, stmt := range x.Body.List {
		clause := stmt.(*ast.CaseClause)
		if clause.List == nil {
			deflt = clause
			continue
		}
		if !known || chosen != nil {
			continue
		}
		for _, c := range clause.List {
			v := w.e.value(c, fr, 0)
			if !synopsisKnown(v) {
				known = false
				break
			}
			if v.kind == tag.kind && v.s == tag.s && v.b == tag.b {
				chosen = clause
				break
			}
		}
	}
	if !known {
		if w.mentionsFlagSet(x.Body, fr) {
			w.problem(fr, x.Pos(), "the switch on `"+types.ExprString(x.Tag)+"` guards a flag registration and is not statically known for this route")
		}
		for _, stmt := range x.Body.List {
			for _, s := range stmt.(*ast.CaseClause).Body {
				w.walk(s, fr)
			}
		}
		return
	}
	if chosen == nil {
		chosen = deflt
	}
	if chosen != nil {
		for _, s := range chosen.Body {
			w.walk(s, fr)
		}
	}
}

// call records a registration, enters a helper that receives the FlagSet or a
// local function literal, and otherwise lets the walk descend.
func (w *synopsisWalk) call(call *ast.CallExpr, fr *synopsisFrame) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && fr.isFlagSet(id.Name) {
			idx, reg := synopsisFlagRegistrars[sel.Sel.Name]
			if !reg {
				return true
			}
			if len(call.Args) <= idx {
				w.problem(fr, call.Pos(), "flag registration "+types.ExprString(call.Fun)+" without a name argument")
				return false
			}
			name := w.e.value(call.Args[idx], fr, 0)
			if name.kind != synopsisString {
				w.problem(fr, call.Pos(), "the flag name `"+types.ExprString(call.Args[idx])+"` is not statically known")
				return false
			}
			variable, assigned := w.results[call]
			if !assigned {
				variable = w.pos(fr, call.Pos()) + "#" + name.s
			}
			if synopsisFlagVarArg(sel.Sel.Name) {
				variable = w.variableKey(call.Args[0], fr, 0)
			}
			w.regs = append(w.regs, synopsisReg{name: name.s, variable: variable, pos: w.pos(fr, call.Pos())})
			return false
		}
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		if lit := fr.funcLit(id.Name); lit != nil {
			inner := w.e.newFrame(fr.file, fr.decl, fr)
			inner.ftype, inner.body = lit.Type, lit.Body
			w.bindParams(inner, lit.Type, call.Args, fr)
			w.walk(lit.Body, inner)
			for _, arg := range call.Args {
				w.walk(arg, fr)
			}
			return false
		}
	}
	idx := slices.IndexFunc(call.Args, func(arg ast.Expr) bool {
		id, ok := arg.(*ast.Ident)
		return ok && fr.isFlagSet(id.Name)
	})
	if idx < 0 {
		return true
	}
	callee, recv := "", ast.Expr(nil)
	switch f := call.Fun.(type) {
	case *ast.Ident:
		callee = f.Name
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok && w.e.imports[fr.file][id.Name] && !w.e.isLocal(id.Name, fr) {
			w.problem(fr, call.Pos(), "the FlagSet is handed to "+types.ExprString(call.Fun)+" in another package; the guard cannot follow its registrations")
			return false
		}
		callee, recv = f.Sel.Name, f.X
	}
	var decls []flagSetNameFunc
	for _, fn := range w.e.eval.funcs[w.e.eval.pkgOf[fr.file]] {
		if fn.decl.Name.Name != callee || fn.decl.Body == nil || (recv == nil) != (fn.decl.Recv == nil) {
			continue
		}
		if synopsisParamType(fn.decl.Type, idx) == "*flag.FlagSet" && synopsisArity(fn.decl.Type) == len(call.Args) {
			decls = append(decls, fn)
		}
	}
	if len(decls) == 0 {
		w.problem(fr, call.Pos(), "the FlagSet is handed to "+types.ExprString(call.Fun)+", which is not a same-package function taking a *flag.FlagSet there")
		return false
	}
	if w.depth > 8 {
		w.problem(fr, call.Pos(), "helper nesting too deep at "+types.ExprString(call.Fun))
		return false
	}
	for _, fn := range decls {
		inner := w.e.newFrame(fn.file, fn.decl, nil)
		w.bindParams(inner, fn.decl.Type, call.Args, fr)
		if recv != nil && fn.decl.Recv != nil && len(fn.decl.Recv.List) == 1 && len(fn.decl.Recv.List[0].Names) == 1 {
			inner.params[fn.decl.Recv.List[0].Names[0].Name] = synopsisArg{expr: recv, fr: fr}
		}
		inner.fs[synopsisParamName(fn.decl.Type, idx)] = true
		w.depth++
		w.walk(fn.decl.Body, inner)
		w.depth--
	}
	return false
}

func (w *synopsisWalk) bindParams(inner *synopsisFrame, ft *ast.FuncType, args []ast.Expr, caller *synopsisFrame) {
	i := 0
	for _, field := range ft.Params.List {
		for _, id := range field.Names {
			if i < len(args) {
				inner.params[id.Name] = synopsisArg{expr: args[i], fr: caller}
			}
			i++
		}
		if len(field.Names) == 0 {
			i++
		}
	}
}

func synopsisArity(ft *ast.FuncType) int {
	n := 0
	for _, field := range ft.Params.List {
		n += max(len(field.Names), 1)
	}
	return n
}

func synopsisParamType(ft *ast.FuncType, idx int) string {
	i := 0
	for _, field := range ft.Params.List {
		n := max(len(field.Names), 1)
		if idx < i+n {
			return types.ExprString(field.Type)
		}
		i += n
	}
	return ""
}

func synopsisParamName(ft *ast.FuncType, idx int) string {
	i := 0
	for _, field := range ft.Params.List {
		for _, id := range field.Names {
			if i == idx {
				return id.Name
			}
			i++
		}
		if len(field.Names) == 0 {
			i++
		}
	}
	return ""
}

// synopsisOrigin is where a site's FlagSet is created: the function, the
// local it is assigned to, and its name expression.
type synopsisOrigin struct {
	file string
	decl *ast.FuncDecl
	fs   string
	name ast.Expr
}

// newFlagSetAssigns lists the locals of decl assigned flag.NewFlagSet(...)
// with their name argument.
func synopsisNewFlagSets(decl *ast.FuncDecl) map[string][]ast.Expr {
	out := map[string][]ast.Expr{}
	ast.Inspect(decl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) == len(x.Rhs) {
				for i, rhs := range x.Rhs {
					if arg := flagSetNameArg(rhs); arg != nil {
						if id, ok := x.Lhs[i].(*ast.Ident); ok {
							out[id.Name] = append(out[id.Name], arg)
						}
					}
				}
			}
		case *ast.ValueSpec:
			for i, v := range x.Values {
				if arg := flagSetNameArg(v); arg != nil && i < len(x.Names) {
					out[x.Names[i].Name] = append(out[x.Names[i].Name], arg)
				}
			}
		}
		return true
	})
	return out
}

// origins finds where the FlagSet of site is created: in the site function,
// or, for a site that parses a FlagSet parameter, at every same-package call
// that hands it a FlagSet created there.
func (e *synopsisFlagEngine) origins(site flagParseGuardSite) ([]synopsisOrigin, []string) {
	fn := e.eval.funcNamed(site.file, site.fn)
	if fn == nil {
		return nil, []string{site.describe() + ": the site function is not found"}
	}
	if param, ok := strings.CutPrefix(site.flagSet, "param "); ok {
		name, _, _ := strings.Cut(param, " of ")
		idx := flagSetNameParamIndex(fn, name)
		var out []synopsisOrigin
		var problems []string
		for _, caller := range e.eval.funcs[e.eval.pkgOf[site.file]] {
			ast.Inspect(caller.decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != synopsisArity(fn.Type) {
					return true
				}
				callee := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					callee = f.Name
				case *ast.SelectorExpr:
					callee = f.Sel.Name
				}
				if callee != fn.Name.Name {
					return true
				}
				if idx < 0 {
					return false
				}
				id, ok := call.Args[idx].(*ast.Ident)
				names := synopsisNewFlagSets(caller.decl)
				if !ok || len(names[id.Name]) != 1 {
					problems = append(problems, site.describe()+": a call in "+flagParseGuardFuncName(caller.decl)+" hands it a FlagSet not created there by one flag.NewFlagSet")
					return true
				}
				out = append(out, synopsisOrigin{file: caller.file, decl: caller.decl, fs: id.Name, name: names[id.Name][0]})
				return true
			})
		}
		if len(out) == 0 && len(problems) == 0 {
			problems = append(problems, site.describe()+": no call hands the parsed FlagSet parameter a FlagSet")
		}
		return out, problems
	}
	var out []synopsisOrigin
	for local, args := range synopsisNewFlagSets(fn) {
		for _, arg := range args {
			if flagSetNameSpelling(arg) == site.flagSet {
				out = append(out, synopsisOrigin{file: site.file, decl: fn, fs: local, name: arg})
			}
		}
	}
	if len(out) != 1 {
		return nil, []string{site.describe() + ": want exactly one flag.NewFlagSet local with that name, got " + strconv.Itoa(len(out))}
	}
	return out, nil
}

// contexts lists the frames origin's function is entered in: one per
// same-package call, with the caller's arguments bound, or one frame with
// unknown parameters when nothing calls it.
func (e *synopsisFlagEngine) contexts(origin synopsisOrigin) []*synopsisFrame {
	var out []*synopsisFrame
	arity := synopsisArity(origin.decl.Type)
	for _, caller := range e.eval.funcs[e.eval.pkgOf[origin.file]] {
		ast.Inspect(caller.decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != arity {
				return true
			}
			var recv ast.Expr
			switch f := call.Fun.(type) {
			case *ast.Ident:
				if f.Name != origin.decl.Name.Name || origin.decl.Recv != nil {
					return true
				}
			case *ast.SelectorExpr:
				if f.Sel.Name != origin.decl.Name.Name || origin.decl.Recv == nil {
					return true
				}
				recv = f.X
			default:
				return true
			}
			callerFrame := e.newFrame(caller.file, caller.decl, nil)
			fr := e.newFrame(origin.file, origin.decl, nil)
			(&synopsisWalk{e: e, inLit: map[*ast.FuncLit]bool{}, results: map[*ast.CallExpr]string{}}).bindParams(fr, origin.decl.Type, call.Args, callerFrame)
			if recv != nil && len(origin.decl.Recv.List) == 1 && len(origin.decl.Recv.List[0].Names) == 1 {
				fr.params[origin.decl.Recv.List[0].Names[0].Name] = synopsisArg{expr: recv, fr: callerFrame}
			}
			out = append(out, fr)
			return true
		})
	}
	if len(out) == 0 {
		out = append(out, e.newFrame(origin.file, origin.decl, nil))
	}
	return out
}

// synopsisRouteFlags is what one route's parsers register: flag name ->
// variables it is bound to, and the sites that register it.
type synopsisRouteFlags struct {
	vars  map[string]map[string]bool
	sites map[string]map[string]bool
}

// registrations enumerates the flags origin's FlagSet registers when the
// FlagSet is named route. ok is false when no context of the origin can take
// that name.
func (e *synopsisFlagEngine) registrations(origin synopsisOrigin, route string) (regs []synopsisReg, problems []string, ok bool) {
	// A spelling that takes the route with fewer holes is more specific: a
	// route one context spells literally belongs to that context, and a hole
	// only takes the routes no context spells more exactly.
	type match struct {
		ctx     *synopsisFrame
		pattern []synopsisPart
		holes   int
	}
	var matches []match
	for _, ctx := range e.contexts(origin) {
		for _, pattern := range e.patterns(origin.name, ctx, 0) {
			holes := 0
			for _, p := range pattern {
				if p.hole != "" {
					holes++
				}
			}
			matches = append(matches, match{ctx, pattern, holes})
		}
	}
	best := -1
	for _, m := range matches {
		synopsisResetBinds(m.pattern)
		if synopsisUnify(route, m.pattern) && (best < 0 || m.holes < best) {
			best = m.holes
		}
		synopsisResetBinds(m.pattern)
	}
	for _, m := range matches {
		if m.holes != best {
			continue
		}
		// Bindings belong to one route and one spelling at a time.
		synopsisResetBinds(m.pattern)
		if !synopsisUnify(route, m.pattern) {
			continue
		}
		ok = true
		m.ctx.fs = map[string]bool{origin.fs: true}
		m.ctx.lits = map[string]*ast.FuncLit{}
		w := &synopsisWalk{e: e, inLit: map[*ast.FuncLit]bool{}, results: map[*ast.CallExpr]string{}}
		w.walk(origin.decl.Body, m.ctx)
		regs = append(regs, w.regs...)
		problems = append(problems, w.problems...)
		synopsisResetBinds(m.pattern)
	}
	return regs, problems, ok
}

// synopsisFlagInputs are the tables and catalog view the guard is run
// against; the negative control substitutes its own.
type synopsisFlagInputs struct {
	hidden         map[string]flagParseGuardException
	names          map[string]flagSetNameException
	siteExceptions map[string]synopsisSiteException
	flagExceptions map[string]synopsisFlagException
	aliases        [][2]string
	// usage returns a public route's Usage lines; ok is false for a route
	// that is not public (hidden, or under `internal`) or not in the catalog.
	usage func(route string) (lines []string, public bool, ok bool)
}

// synopsisFlagCounts reports how much the guard examined.
type synopsisFlagCounts struct {
	sites, routes, flags int
	routeNames           []string
}

// synopsisFlagProblems checks every public flag parse site: each flag its
// FlagSet registers must be spelled in the Usage of every route the site's
// FlagSet name takes, by name or through an alias pair bound to the same
// variable, or be an exception row. It also reports stale rows.
func synopsisFlagProblems(e *synopsisFlagEngine, report flagParseGuardReport, in synopsisFlagInputs) ([]string, synopsisFlagCounts) {
	var problems []string
	var counts synopsisFlagCounts
	byRoute := map[string]*synopsisRouteFlags{}
	usedSites := map[string]bool{}
	for _, site := range report.sites {
		if _, ok := in.hidden[site.key()]; ok {
			continue
		}
		var routes []string
		if row, ok := in.names[site.key()]; ok {
			routes = row.routes
		} else {
			values, known := e.eval.siteValues(site)
			if !known {
				problems = append(problems, site.describe()+": the FlagSet name is not statically known and has no flagSetNameExceptions row")
				continue
			}
			routes = values
		}
		var public []string
		for _, route := range routes {
			_, isPublic, ok := in.usage(route)
			if !ok {
				problems = append(problems, site.describe()+": route "+strconv.Quote(route)+" is not a catalog route")
				continue
			}
			if isPublic {
				public = append(public, route)
			}
		}
		if len(public) == 0 {
			continue
		}
		if row, ok := in.siteExceptions[site.key()]; ok {
			usedSites[site.key()] = true
			if !slices.Equal(public, []string{row.route}) {
				problems = append(problems, site.describe()+": site exception row expects route "+strconv.Quote(row.route)+", but this site resolves to "+strings.Join(public, ", "))
			}
			continue
		}
		counts.sites++
		origins, originProblems := e.origins(site)
		problems = append(problems, originProblems...)
		for _, route := range public {
			reached := false
			for _, origin := range origins {
				regs, walkProblems, ok := e.registrations(origin, route)
				for _, p := range walkProblems {
					problems = append(problems, "route "+strconv.Quote(route)+" ("+site.describe()+"): "+p)
				}
				if !ok {
					continue
				}
				reached = true
				rf := byRoute[route]
				if rf == nil {
					rf = &synopsisRouteFlags{vars: map[string]map[string]bool{}, sites: map[string]map[string]bool{}}
					byRoute[route] = rf
				}
				for _, reg := range regs {
					if rf.vars[reg.name] == nil {
						rf.vars[reg.name] = map[string]bool{}
						rf.sites[reg.name] = map[string]bool{}
					}
					rf.vars[reg.name][reg.variable] = true
					rf.sites[reg.name][site.describe()] = true
				}
			}
			if !reached && len(origins) > 0 {
				problems = append(problems, site.describe()+": no FlagSet name spelling of the site takes route "+strconv.Quote(route))
			}
		}
	}
	usedAliases := map[[2]string]bool{}
	usedFlags := map[string]bool{}
	for _, route := range slices.Sorted(func(yield func(string) bool) {
		for r := range byRoute {
			if !yield(r) {
				return
			}
		}
	}) {
		rf := byRoute[route]
		lines, _, _ := in.usage(route)
		tokens := synopsisFlagTokens(lines)
		counts.routes++
		counts.routeNames = append(counts.routeNames, route)
		for _, name := range slices.Sorted(func(yield func(string) bool) {
			for n := range rf.vars {
				if !yield(n) {
					return
				}
			}
		}) {
			counts.flags++
			if tokens[name] {
				continue
			}
			covered := false
			for _, pair := range in.aliases {
				other := ""
				switch name {
				case pair[0]:
					other = pair[1]
				case pair[1]:
					other = pair[0]
				default:
					continue
				}
				if !tokens[other] || rf.vars[other] == nil {
					continue
				}
				for v := range rf.vars[name] {
					if rf.vars[other][v] {
						covered = true
					}
				}
				if covered {
					usedAliases[pair] = true
					break
				}
			}
			if covered {
				continue
			}
			if _, ok := in.flagExceptions[route+" "+name]; ok {
				usedFlags[route+" "+name] = true
				continue
			}
			problems = append(problems, fmt.Sprintf("route %q: flag %s is registered by its parser (%s) but missing from its Usage %q", route, synopsisFlagName(name), strings.Join(slices.Sorted(func(yield func(string) bool) {
				for s := range rf.sites[name] {
					if !yield(s) {
						return
					}
				}
			}), "; "), lines))
		}
	}
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for k := range in.siteExceptions {
			if !yield(k) {
				return
			}
		}
	}) {
		if strings.TrimSpace(in.siteExceptions[key].reason) == "" {
			problems = append(problems, "site exception "+strconv.Quote(key)+": missing reason")
		}
		if !usedSites[key] {
			problems = append(problems, "site exception "+strconv.Quote(key)+" is stale: no public flag parse site with that key resolves to a public route")
		}
	}
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for k := range in.flagExceptions {
			if !yield(k) {
				return
			}
		}
	}) {
		cut := strings.LastIndex(key, " ")
		route, name := key[:max(cut, 0)], key[cut+1:]
		row := in.flagExceptions[key]
		if strings.TrimSpace(row.reason) == "" {
			problems = append(problems, fmt.Sprintf("flag exception route %q flag %s: missing reason", route, synopsisFlagName(name)))
		}
		if row.refusedBy != "" {
			problems = append(problems, synopsisRefusalStaticProblems(e, route, name, row)...)
		}
		if !usedFlags[key] {
			problems = append(problems, fmt.Sprintf("flag exception route %q flag %s is stale: that route's parser does not register it outside its Usage", route, synopsisFlagName(name)))
		}
	}
	for _, pair := range in.aliases {
		if !usedAliases[pair] {
			problems = append(problems, fmt.Sprintf("alias pair %s/%s is stale: no route's Usage relies on it", synopsisFlagName(pair[0]), synopsisFlagName(pair[1])))
		}
	}
	sort.Strings(problems)
	return slices.Compact(problems), counts
}

// synopsisRefusalStaticProblems checks the refusing function a category (c)
// row names: it must exist, and a row without a behavioural probe must name a
// function whose body mentions the flag.
func synopsisRefusalStaticProblems(e *synopsisFlagEngine, route, name string, row synopsisFlagException) []string {
	file, symbol, ok := strings.Cut(row.refusedBy, ":")
	var fn *ast.FuncDecl
	if ok {
		fn = e.eval.funcNamed(file, symbol)
	}
	if fn == nil {
		return []string{fmt.Sprintf("flag exception route %q flag %s: refusing function %s is not declared there", route, synopsisFlagName(name), row.refusedBy)}
	}
	if row.probe != nil {
		return nil
	}
	var body strings.Builder
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			body.WriteString(x.Value + " ")
		case *ast.Ident:
			body.WriteString(x.Name + " ")
		}
		return true
	})
	squash := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }
	if !strings.Contains(squash(body.String()), squash(name)) {
		return []string{fmt.Sprintf("flag exception route %q flag %s is stale: refusing function %s no longer mentions the flag", route, synopsisFlagName(name), row.refusedBy)}
	}
	return nil
}

// synopsisCatalogUsage is the real catalog view: a route resolves exactly,
// and is public unless it or its top-level route is hidden.
func synopsisCatalogUsage(route string) ([]string, bool, bool) {
	tokens := strings.Fields(route)
	path, node, ok := cli.Resolve(tokens)
	if !ok || !slices.Equal(path, tokens) {
		return nil, false, false
	}
	top, _ := cli.LookupRoute(tokens[0])
	return node.Usage, !node.Hidden && !top.Hidden, true
}

// TestPublicRouteSynopsisListsEveryParsedFlag holds every public flag parse
// site in internal/app/** (the closed site set of the exit-code guard, minus
// its hidden exceptions, resolved to routes like the FlagSet name guard) to a
// catalog Usage that spells every flag its FlagSet registers, including the
// flags a helper registers on it.
func TestPublicRouteSynopsisListsEveryParsedFlag(t *testing.T) {
	t.Parallel()
	pkgs := flagParseGuardLoadRepo(t, filepath.Join("..", ".."))
	report := flagParseGuardAnalyze(pkgs)
	eval, problems := newFlagSetNameEval(pkgs)
	problems = append(problems, report.problems...)
	checked, counts := synopsisFlagProblems(newSynopsisFlagEngine(eval), report, synopsisFlagInputs{
		hidden:         flagParseGuardExceptions,
		names:          flagSetNameExceptions,
		siteExceptions: synopsisFlagSiteExceptions,
		flagExceptions: synopsisFlagExceptions,
		aliases:        synopsisFlagAliases,
		usage:          synopsisCatalogUsage,
	})
	for _, problem := range append(problems, checked...) {
		t.Error(problem)
	}
	if counts.sites < 92 || counts.routes < 138 {
		t.Errorf("checked %d public flag parse sites over %d routes, want at least 92 sites and 138 routes; the site collection has regressed", counts.sites, counts.routes)
	}
	t.Logf("synopsis flag guard: %d public sites, %d routes, %d route flags", counts.sites, counts.routes, counts.flags)
}

// synopsisRefusalProblem judges one probe of a category (c) row: the route run
// with the flag must fail with a usage error that names the flag. Anything
// else means the refusal is gone and the row is stale.
func synopsisRefusalProblem(key string, row synopsisFlagException, err error) string {
	cut := strings.LastIndex(key, " ")
	route, name := key[:max(cut, 0)], key[cut+1:]
	flagName := synopsisFlagName(name)
	if err != nil && IsUsageError(err) && strings.Contains(err.Error(), flagName) {
		return ""
	}
	got := "no error"
	if err != nil {
		got = fmt.Sprintf("%q (usage error: %v)", err.Error(), IsUsageError(err))
	}
	return fmt.Sprintf("flag exception route %q flag %s is stale: `projmux %s` no longer fails with a usage error naming %s (refused by %s); got %s",
		route, flagName, strings.Join(row.probe, " "), flagName, row.refusedBy, got)
}

// TestSynopsisFlagRefusalRowsStillRefuse is the behavioural stale check of the
// category (c) rows: each probe runs its route through the create command on
// fakes and must be refused before the command reads or writes anything.
func TestSynopsisFlagRefusalRowsStillRefuse(t *testing.T) {
	t.Parallel()
	behavioural, static := 0, 0
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for k := range synopsisFlagExceptions {
			if !yield(k) {
				return
			}
		}
	}) {
		row := synopsisFlagExceptions[key]
		if row.refusedBy == "" {
			continue
		}
		if row.probe == nil {
			static++
			continue
		}
		behavioural++
		cut := strings.LastIndex(key, " ")
		if len(row.probe) < 2 || row.probe[0] != "create" || strings.Join(row.probe[:2], " ") != key[:cut] || !slices.Contains(row.probe, synopsisFlagName(key[cut+1:])) {
			t.Errorf("flag exception %q: probe %q must run `create <route>` with the row's flag", key, row.probe)
			continue
		}
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestAgentCreateCommand(t, store, tmux)
		err := create.Run(row.probe[1:], io.Discard, io.Discard)
		if problem := synopsisRefusalProblem(key, row, err); problem != "" {
			t.Error(problem)
			continue
		}
		if store.transactions != 0 || store.writes != 0 || len(tmux.calls) != 0 {
			t.Errorf("flag exception %q: the refusal of %q is not side-effect free: %d transactions, %d writes, %d tmux calls",
				key, row.probe, store.transactions, store.writes, len(tmux.calls))
		}
	}
	if behavioural == 0 {
		t.Error("no category (c) row was probed; the refusal check has regressed")
	}
	t.Logf("category (c) rows: %d behavioural, %d static", behavioural, static)
}

// TestSynopsisFlagGuardDetectsDrift is the negative control: on synthetic
// source, a flag missing from its route's Usage, a flag only a helper
// registers, a stale exception row whose refusing function is gone, a stale
// alias pair, and a (c) row whose refusal disappeared are each reported by
// route and flag; an alias bound to the same variable passes.
func TestSynopsisFlagGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	const pkgPath = "example.test/guard/internal/app"
	const src = `package app

import "flag"

type UsageError struct{ Message string }

func (e *UsageError) Error() string            { return e.Message }
func (e *UsageError) MetadataUsageError() bool { return true }

func usageError(message string) error { return &UsageError{Message: message} }

func sharedFlags(fs *flag.FlagSet, gamma *string) {
	fs.StringVar(gamma, "gamma", "", "registered only by this helper")
}

func run(args []string) error {
	fs := flag.NewFlagSet("demo run", flag.ContinueOnError)
	var alpha, beta, gamma string
	var quiet bool
	fs.StringVar(&alpha, "alpha", "", "in the Usage")
	fs.StringVar(&beta, "beta", "", "missing from the Usage")
	fs.BoolVar(&quiet, "quiet", false, "in the Usage")
	fs.BoolVar(&quiet, "q", false, "alias of --quiet")
	sharedFlags(fs, &gamma)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	return nil
}

func refuseDelta() error { return usageError("--delta is refused") }
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
	failures, counts := synopsisFlagProblems(newSynopsisFlagEngine(eval), report, synopsisFlagInputs{
		flagExceptions: map[string]synopsisFlagException{
			"demo run delta": {reason: "refused on this route", refusedBy: "synthetic/app.go:refuseGone", probe: []string{"demo", "run", "--delta"}},
		},
		aliases: [][2]string{{"q", "quiet"}, {"x", "xray"}},
		usage: func(route string) ([]string, bool, bool) {
			if route == "demo run" {
				return []string{"projmux demo run [--alpha <a>] [--quiet]"}, true, true
			}
			return nil, false, false
		},
	})
	joined := strings.Join(failures, "\n")
	for _, want := range []string{
		`route "demo run": flag --beta is registered by its parser (synthetic/app.go:25: func run, FlagSet "demo run" (FlagSet.Parse)) but missing from its Usage`,
		`route "demo run": flag --gamma is registered by its parser (synthetic/app.go:25: func run, FlagSet "demo run" (FlagSet.Parse)) but missing from its Usage`,
		`flag exception route "demo run" flag --delta is stale: that route's parser does not register it outside its Usage`,
		`flag exception route "demo run" flag --delta: refusing function synthetic/app.go:refuseGone is not declared there`,
		`alias pair -x/--xray is stale: no route's Usage relies on it`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("negative control: missing failure %q in:\n%s", want, joined)
		}
	}
	for _, clean := range []string{"flag -q", "flag --quiet", "flag --alpha", "-q/--quiet"} {
		if strings.Contains(joined, clean) {
			t.Errorf("negative control: %q must pass:\n%s", clean, joined)
		}
	}
	if len(failures) != 5 {
		t.Errorf("negative control: got %d failures, want 5:\n%s", len(failures), joined)
	}
	if counts.sites != 1 || counts.routes != 1 {
		t.Errorf("negative control: checked %d sites and %d routes, want 1 and 1", counts.sites, counts.routes)
	}
	row := synopsisFlagException{reason: "refused", refusedBy: "internal/app/create_agent.go:(*createCommand).resolveCreateProvider", probe: []string{"create", "codex", "--provider", "codex"}}
	want := "flag exception route \"create codex\" flag --provider is stale: `projmux create codex --provider codex` no longer fails with a usage error naming --provider"
	if got := synopsisRefusalProblem("create codex provider", row, nil); !strings.HasPrefix(got, want) {
		t.Errorf("negative control: a refusal that disappeared must be stale: got %q, want prefix %q", got, want)
	}
	if got := synopsisRefusalProblem("create codex provider", row, usageError("create codex already names the provider; drop --provider")); got != "" {
		t.Errorf("negative control: a live refusal must pass: %q", got)
	}
}
