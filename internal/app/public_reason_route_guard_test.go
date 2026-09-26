package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// publicReasonRetiredRoots are the removed top-level tokens of the retirement
// ledger (internal/cli TestRetirementCatalogMatchesTheRemainingLedger). A
// reason or hint that leads with one of them, or with a catalog top-level
// route, names a route; the guard asserts that no row here is a live root.
var publicReasonRetiredRoots = []string{
	"ai", "current", "kill", "notify", "sessions", "session-state", "tag", "upgrade", "usage",
	"tmux", "status", "statusbar", "preview", "session-popup", "key-broker", "popup-wait-key",
}

// publicReasonUnknown stands in for the part of a reason the evaluator cannot
// read statically. It is a word of its own in the route-shape patterns, so a
// route spelling built from an unknown value is caught rather than skipped.
const publicReasonUnknown = "\x00"

// publicReasonWord is one argv token of a route spelling, or the unknown part.
const publicReasonWord = `(?:[a-z][a-z0-9-]*|\x00)`

// publicReasonShapes are the rejection shapes whose leading words name the
// route that refused: `unknown <route> subcommand`, `<route>: unknown flag`
// (splitOperands), and `<route> requires|does not accept|accepts|must|cannot`
// or `<route> --flag ...`. The first capture group is the route spelling.
var publicReasonShapes = []*regexp.Regexp{
	regexp.MustCompile(`^unknown ((?:` + publicReasonWord + ` )+)subcommand\b`),
	regexp.MustCompile(`^(` + publicReasonWord + `(?: ` + publicReasonWord + `)*): unknown flag\b`),
	regexp.MustCompile(`^(` + publicReasonWord + `(?: ` + publicReasonWord + `)*?) (?:requires\b|does not accept\b|accepts\b|must\b|cannot\b|--)`),
}

// publicReasonToken is one plain argv token.
var publicReasonToken = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// publicReasonHintSpan is one backtick-quoted command in a string literal, the
// way handlers point an operator at another route on stderr.
var publicReasonHintSpan = regexp.MustCompile("`([^`]+)`")

// publicReasonException is one route spelling in one file that a reason or
// hint may carry although it names no catalog route (a hidden `internal ...`
// handler's historical spelling, a machine-emitted sub-verb of a public token,
// or text that is not a route at all), or one reason whose route spelling the
// evaluator cannot read statically. routes lists the catalog paths such a
// dynamic spelling takes at run time; each must resolve exactly.
type publicReasonException struct {
	routes []string
	reason string
}

const (
	publicReasonHiddenReason  = "hidden plumbing: only a hidden `internal ...` spelling reaches this handler, and its reasons keep their historical spelling byte for byte"
	publicReasonNotARoute     = "an internal invariant or a non-projmux command, not an argv rejection naming a route"
	publicReasonDynamicReason = "the spelling is the canonical route built from the dispatched kind or provider token, so it takes one catalog path per token"
)

// publicReasonExceptions is keyed by publicReasonSite.key(): the repo-relative
// file and the quoted spelling up to its first token the catalog does not
// know, or, for a dynamic spelling, the file, the function, and the
// expression the spelling comes from. A dynamic reason whose expression is
// already a flagSetNameExceptions row reuses that row's routes. It is closed:
// a row no site uses is stale.
var publicReasonExceptions = map[string]publicReasonException{
	// Dynamic spellings: the canonical `<verb> <kind|provider>` spelling the
	// dispatch built, handed to a shared helper.
	`internal/app/agent_profile.go (*createCommand).prepareProfileSettings spelling`:              {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/agent_profile.go (*createCommand).selectCreateProfile spelling`:                 {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/agent_profile.go requireProfileLane spelling`:                                   {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/claude_launch_options.go (*createCommand).preparePersonaLaunch spelling`:        {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/claude_launch_options.go requireClaudeLaunchOptions spelling`:                   {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/claude_launch_options.go requirePersonaLane spelling`:                           {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/codex_native_thread.go requireInteractiveOnlyProvider spelling`:                 {routes: publicReasonCreateAgentRoutes, reason: publicReasonDynamicReason},
	`internal/app/create.go requireCanonicalProvider spelling`:                                    {routes: publicReasonCreateResourceRoutes, reason: publicReasonDynamicReason},
	`internal/app/create_resource.go (resourceCreateFlags).refuseConflictingWindowScope spelling`: {routes: publicReasonCreateResourceRoutes, reason: publicReasonDynamicReason},
	`internal/app/create_resource.go requireExplicitProject spelling`:                             {routes: publicReasonCreateResourceRoutes, reason: publicReasonDynamicReason},
	`internal/app/create_resource.go splitPayload spelling`:                                       {routes: publicReasonCreateResourceRoutes, reason: publicReasonDynamicReason},
	`internal/app/delete_transport.go resolveDeleteTarget spelling`:                               {routes: []string{"delete project", "delete window", "delete pane", "delete agent", "unregister project"}, reason: publicReasonDynamicReason},
	`internal/app/persona.go (*personaCommand).Run c.spelling()`:                                  {routes: []string{"persona", "instructions"}, reason: "the noun is the persona or instructions spelling the handler was built for"},
	`internal/app/persona.go (*personaCommand).runDelete c.spelling()`:                            {routes: []string{"persona delete", "instructions delete"}, reason: "the noun is the persona or instructions spelling the handler was built for"},
	`internal/app/persona.go (*personaCommand).runList c.spelling()`:                              {routes: []string{"persona list", "instructions list"}, reason: "the noun is the persona or instructions spelling the handler was built for"},
	`internal/app/persona.go (*personaCommand).runSet c.spelling()`:                               {routes: []string{"persona set", "instructions set"}, reason: "the noun is the persona or instructions spelling the handler was built for"},
	`internal/app/persona.go personaNameOperand spelling`:                                         {routes: []string{"persona show", "persona edit", "instructions show", "instructions edit"}, reason: "personaNameOperand is handed the `<noun> <verb>` spelling of its caller"},
	`internal/app/project_lifecycle_verbs.go (*projectLifecycleCommand).Run verb`:                 {routes: []string{"open", "start", "stop"}, reason: "the verb is the lifecycle root the handler was built for"},
	`internal/app/project_lifecycle_verbs.go (*projectLifecycleCommand).resolveProject spelling`:  {routes: []string{"open project", "start project", "stop project"}, reason: publicReasonDynamicReason},

	// Hidden plumbing.
	`internal/app/agent_message_hold.go "internal agent-message-release"`: {reason: publicReasonHiddenReason},
	`internal/app/ai.go "ai"`: {reason: publicReasonHiddenReason + " (public routes forward into the ai handler with a fixed leaf, so its `ai ...` dispatch reasons are unreachable from them)"},
	`internal/app/ai.go "internal agent-pane launch-default direction"`:       {reason: publicReasonHiddenReason},
	`internal/app/ai.go "internal agent-pane launch-provider"`:                {reason: publicReasonHiddenReason},
	`internal/app/ai.go "internal agent-pane launch-selection direction"`:     {reason: publicReasonHiddenReason},
	`internal/app/ai.go "internal agent-pane launch-shell"`:                   {reason: publicReasonHiddenReason},
	`internal/app/claude_question_popup.go "internal claude-question-picker"`: {reason: publicReasonHiddenReason},
	`internal/app/codex_broker_runtime.go "internal codex-broker probe"`:      {reason: publicReasonHiddenReason},
	`internal/app/codex_broker_runtime.go "internal codex-broker serve"`:      {reason: publicReasonHiddenReason},
	`internal/app/hook_trust_popup.go "tmux"`:                                 {reason: publicReasonHiddenReason},
	`internal/app/key_broker.go "key-broker"`:                                 {reason: publicReasonHiddenReason},
	`internal/app/preview.go "preview"`:                                       {reason: publicReasonHiddenReason},
	`internal/app/preview_select.go "preview"`:                                {reason: publicReasonHiddenReason},
	`internal/app/session_popup.go "session-popup"`:                           {reason: publicReasonHiddenReason},
	`internal/app/status.go "status"`:                                         {reason: publicReasonHiddenReason},
	`internal/app/statusbar.go "statusbar"`:                                   {reason: publicReasonHiddenReason},
	`internal/app/tmux.go "internal tmux converge"`:                           {reason: publicReasonHiddenReason},
	`internal/app/tmux.go "tmux"`:                                             {reason: publicReasonHiddenReason + " (the public `config apply` and `config render ...` spellings name themselves through tmuxReasonRoute)"},
	`internal/app/usagecmd/usage.go "status"`:                                 {reason: publicReasonHiddenReason},

	// Text that is not a route.
	`internal/app/agent_activation_binding.go "tmux"`:                 {reason: publicReasonNotARoute + ": the tmux binary's capture-pane command"},
	`internal/app/attach.go "runtime attach fallback"`:                {reason: "the route `runtime attach` followed by the name of its --fallback flag"},
	`internal/app/config_central_settings.go "agent question window"`: {reason: publicReasonNotARoute + ": the agent question window setting"},
	`internal/app/create.go "agent pane"`:                             {reason: publicReasonNotARoute + ": the agent pane intent invariant"},
	`internal/app/create_agent.go "tmux"`:                             {reason: publicReasonNotARoute + ": the tmux binary's capture-pane command"},
	`internal/app/registry_recovery.go "reconcile registry checksum"`: {reason: "the route `reconcile registry` followed by English (`checksum guards require --source`)"},
	`internal/app/runtime_mutation_route.go "runtime mutation"`:       {reason: publicReasonNotARoute + ": the runtime mutation authority invariant"},
	`internal/app/supervise.go "agent activation"`:                    {reason: publicReasonNotARoute + ": the agent activation invariant"},
	`internal/app/supervise_child_unix.go "agent activation"`:         {reason: publicReasonNotARoute + ": the agent activation invariant"},
	`internal/app/switch.go "settings discovery"`:                     {reason: publicReasonNotARoute + ": a settings picker label its caller passes with no operands, so the refusal never prints"},
	`internal/app/switch.go "settings project"`:                       {reason: publicReasonNotARoute + ": a settings picker label its caller passes with no operands, so the refusal never prints"},
	`internal/app/switch.go "switch command"`:                         {reason: publicReasonNotARoute + ": the switch plan invariant"},
	`internal/app/tmux.go "config apply route"`:                       {reason: publicReasonNotARoute + ": the config apply runtime route invariant"},
}

// publicReasonCreateAgentRoutes are the spellings the Agent create helpers
// are handed.
var publicReasonCreateAgentRoutes = []string{"create agent", "create codex", "create claude", "create antigravity"}

// publicReasonCreateResourceRoutes are the spellings the resource create
// helpers are handed (the parseResourceCreateFlags FlagSet routes).
var publicReasonCreateResourceRoutes = []string{"create window", "create pane", "create agent", "create codex", "create claude", "create antigravity"}

// publicReasonSite is one route spelling a reason or hint names.
type publicReasonSite struct {
	file, fn string
	line     int
	kind     string // "reason" or "hint"
	words    []string
	text     string
	// lead is the source of the expression a dynamic spelling comes from.
	lead string
}

func (s publicReasonSite) dynamic() bool { return slices.Contains(s.words, publicReasonUnknown) }

func (s publicReasonSite) key() string {
	if s.dynamic() {
		return s.file + " " + s.fn + " " + s.lead
	}
	return s.file + " " + strconv.Quote(strings.Join(s.words[:publicReasonFailIndex(s.words)+1], " "))
}

// publicReasonFailIndex is the index of the first token of words that is
// neither on the catalog path nor the one Usage verb after it.
func publicReasonFailIndex(words []string) int {
	path, route, ok := cli.Resolve(words)
	if !ok {
		return 0
	}
	for i := range path {
		if path[i] != words[i] {
			return i
		}
	}
	if len(words) > len(path) && slices.Contains(publicReasonUsageVerbs(path, route), words[len(path)]) {
		return min(len(path)+1, len(words)-1)
	}
	return min(len(path), len(words)-1)
}

// publicReasonLead is the source of the first non-literal operand of a
// reason expression, where a dynamic route spelling comes from: the first
// such operand of a concatenation, or the first argument of a literal format.
func publicReasonLead(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.ParenExpr:
		return publicReasonLead(x.X)
	case *ast.BinaryExpr:
		if lit, ok := x.X.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return publicReasonLead(x.Y)
		}
		return publicReasonLead(x.X)
	case *ast.CallExpr:
		switch publicReasonCallee(x) {
		case "fmt.Sprintf", "fmt.Errorf":
			if len(x.Args) == 0 {
				break
			}
			if _, ok := x.Args[0].(*ast.BasicLit); !ok {
				return publicReasonLead(x.Args[0])
			}
			if len(x.Args) > 1 {
				return types.ExprString(x.Args[1])
			}
		}
	}
	return types.ExprString(expr)
}

func (s publicReasonSite) describe() string {
	fn := s.fn
	if fn == "" {
		fn = "(package scope)"
	}
	return s.file + ":" + strconv.Itoa(s.line) + ": func " + fn + ", " + s.kind + " " + strconv.Quote(strings.ReplaceAll(s.text, publicReasonUnknown, "<?>"))
}

// publicReasonEval evaluates the text a reason expression takes, one value per
// statically reachable spelling. It reuses flagSetNameEval's parsed package
// (functions, constants) and its parameter rule, indexed and memoized, and
// adds what reasons are built from: fmt.Sprintf/fmt.Errorf formats,
// strings.TrimPrefix, and same-package single-return helpers. A part it
// cannot read becomes publicReasonUnknown instead of failing the value.
type publicReasonEval struct {
	*flagSetNameEval
	// calls indexes every same-package call by package and callee name, so
	// a parameter resolves without rescanning the package per lookup.
	calls map[string]map[string][]publicReasonCall
	// params memoizes a parameter's values; a nil entry marks one in
	// progress, which a recursive lookup reads as unknown.
	params map[string][]string
}

type publicReasonCall struct {
	file   string
	caller *ast.FuncDecl
	call   *ast.CallExpr
}

func newPublicReasonEval(eval *flagSetNameEval) *publicReasonEval {
	e := &publicReasonEval{flagSetNameEval: eval, calls: map[string]map[string][]publicReasonCall{}, params: map[string][]string{}}
	for pkg, funcs := range eval.funcs {
		index := map[string][]publicReasonCall{}
		for _, fn := range funcs {
			ast.Inspect(fn.decl, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					name = f.Name
				case *ast.SelectorExpr:
					name = f.Sel.Name
				}
				if name != "" {
					index[name] = append(index[name], publicReasonCall{file: fn.file, caller: fn.decl, call: call})
				}
				return true
			})
		}
		e.calls[pkg] = index
	}
	return e
}

// paramValues evaluates parameter idx of fn at every same-package call of a
// function or method with fn's name and arity (flagSetNameEval.evalParam's
// rule, indexed and memoized).
func (e *publicReasonEval) paramValues(file string, fn *ast.FuncDecl, idx, depth int) []string {
	arity := 0
	for _, field := range fn.Type.Params.List {
		arity += max(len(field.Names), 1)
	}
	key := e.pkgOf[file] + " " + fn.Name.Name + " " + strconv.Itoa(arity) + " " + strconv.Itoa(idx)
	if got, ok := e.params[key]; ok {
		if got == nil {
			return []string{publicReasonUnknown}
		}
		return got
	}
	e.params[key] = nil
	var out []string
	for _, site := range e.calls[e.pkgOf[file]][fn.Name.Name] {
		if len(site.call.Args) != arity || site.call.Ellipsis.IsValid() {
			continue
		}
		out = append(out, e.values(site.file, site.caller, site.call.Args[idx], depth+1)...)
	}
	if len(out) == 0 {
		out = []string{publicReasonUnknown}
	}
	out = publicReasonCap(out)
	e.params[key] = out
	return out
}

const publicReasonMaxValues = 64

func (e *publicReasonEval) values(file string, fn *ast.FuncDecl, expr ast.Expr, depth int) []string {
	unknown := []string{publicReasonUnknown}
	if depth > 12 {
		return unknown
	}
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return unknown
		}
		value, err := strconv.Unquote(x.Value)
		if err != nil {
			return unknown
		}
		return []string{value}
	case *ast.ParenExpr:
		return e.values(file, fn, x.X, depth)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return unknown
		}
		return publicReasonProduct(e.values(file, fn, x.X, depth), e.values(file, fn, x.Y, depth))
	case *ast.Ident:
		if fn != nil {
			if idx := flagSetNameParamIndex(fn, x.Name); idx >= 0 {
				return e.paramValues(file, fn, idx, depth)
			}
			if def := publicReasonSingleDef(fn, x.Name); def != nil {
				return e.values(file, fn, def, depth+1)
			}
		}
		if value, ok := e.consts[e.pkgOf[file]][x.Name]; ok {
			return e.values(file, nil, value, depth+1)
		}
		return unknown
	case *ast.CallExpr:
		return e.callValues(file, fn, x, depth)
	}
	return unknown
}

func (e *publicReasonEval) callValues(file string, fn *ast.FuncDecl, call *ast.CallExpr, depth int) []string {
	unknown := []string{publicReasonUnknown}
	switch publicReasonCallee(call) {
	case "fmt.Sprintf", "fmt.Errorf":
		if len(call.Args) == 0 {
			return unknown
		}
		var out []string
		for _, format := range e.values(file, fn, call.Args[0], depth+1) {
			out = append(out, e.formatValues(file, fn, format, call.Args[1:], depth)...)
		}
		return publicReasonCap(out)
	case "strings.TrimPrefix":
		if len(call.Args) != 2 {
			return unknown
		}
		prefixes := e.values(file, fn, call.Args[1], depth+1)
		if len(prefixes) != 1 || strings.Contains(prefixes[0], publicReasonUnknown) {
			return unknown
		}
		var out []string
		for _, value := range e.values(file, fn, call.Args[0], depth+1) {
			out = append(out, strings.TrimPrefix(value, prefixes[0]))
		}
		return out
	}
	// A same-package function whose body is one return statement, such as
	// tmuxReasonRoute: evaluate that expression in the callee, whose
	// parameters resolve through every same-package call.
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return unknown
	}
	for _, candidate := range e.funcs[e.pkgOf[file]] {
		decl := candidate.decl
		if decl.Recv != nil || decl.Name.Name != id.Name || decl.Body == nil || len(decl.Body.List) != 1 {
			continue
		}
		ret, ok := decl.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return unknown
		}
		return e.values(candidate.file, decl, ret.Results[0], depth+1)
	}
	return unknown
}

// formatValues expands the verbs of one format string: %s takes its argument's
// values, %% is a literal percent, and every other verb is unknown.
func (e *publicReasonEval) formatValues(file string, fn *ast.FuncDecl, format string, args []ast.Expr, depth int) []string {
	out := []string{""}
	next := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 == len(format) {
			out = publicReasonProduct(out, []string{format[i : i+1]})
			continue
		}
		i++
		if format[i] == '%' {
			out = publicReasonProduct(out, []string{"%"})
			continue
		}
		piece := []string{publicReasonUnknown}
		if format[i] == 's' && next < len(args) {
			piece = e.values(file, fn, args[next], depth+1)
		}
		next++
		out = publicReasonProduct(out, piece)
	}
	return out
}

func publicReasonProduct(left, right []string) []string {
	var out []string
	for _, l := range left {
		for _, r := range right {
			out = append(out, l+r)
		}
	}
	return publicReasonCap(out)
}

func publicReasonCap(values []string) []string {
	slices.Sort(values)
	values = slices.Compact(values)
	if len(values) > publicReasonMaxValues {
		return []string{publicReasonUnknown}
	}
	return values
}

// publicReasonSingleDef is the one `name := expr` or `var name = expr` in fn,
// or nil when name is assigned more than once or not at all.
func publicReasonSingleDef(fn *ast.FuncDecl, name string) ast.Expr {
	if fn == nil || fn.Body == nil {
		return nil
	}
	var defs []ast.Expr
	reassigned := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					if n.Tok != token.DEFINE || len(n.Lhs) != len(n.Rhs) {
						reassigned = true
						continue
					}
					defs = append(defs, n.Rhs[i])
				}
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
	if reassigned || len(defs) != 1 {
		return nil
	}
	return defs[0]
}

// publicReasonCallee is `pkg.Name` for a selector call and `Name` otherwise.
func publicReasonCallee(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// publicReasonArg is the reason expression of a rejection constructor:
// usageError(reason), errors.New(reason), fmt.Errorf(format, ...), and the
// Detail of a metadata InputError (usagecmd). Anything else returns nil.
func publicReasonArg(node ast.Node) ast.Expr {
	switch n := node.(type) {
	case *ast.CallExpr:
		switch publicReasonCallee(n) {
		case "usageError", "errors.New":
			if len(n.Args) == 1 {
				return n.Args[0]
			}
		case "fmt.Errorf":
			return n
		}
	case *ast.CompositeLit:
		typ := n.Type
		if sel, ok := typ.(*ast.SelectorExpr); ok {
			typ = sel.Sel
		}
		if id, ok := typ.(*ast.Ident); !ok || id.Name != "InputError" {
			return nil
		}
		for _, elt := range n.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Detail" {
					return kv.Value
				}
			}
		}
	}
	return nil
}

// publicReasonRouteWords returns the route spelling a reason leads with, or
// nil when the reason has no route shape or does not lead with a root token.
func publicReasonRouteWords(text string, roots map[string]bool) []string {
	for _, shape := range publicReasonShapes {
		match := shape.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		words := strings.Fields(match[1])
		// A value the evaluator cannot read after the leading route words is
		// an operand (`profile delete <name> requires --yes`) or a verb
		// token; only the words before it are the route spelling.
		if i := slices.Index(words, publicReasonUnknown); i > 0 {
			words = words[:i]
		}
		if len(words) > 0 && (roots[words[0]] || words[0] == publicReasonUnknown) {
			return words
		}
		return nil
	}
	return nil
}

// publicReasonHintWords returns the route spelling one backtick span leads
// with (after an optional `projmux `), or nil when it leads with no root.
func publicReasonHintWords(span string, roots map[string]bool) []string {
	var words []string
	for field := range strings.FieldsSeq(strings.TrimPrefix(span, "projmux ")) {
		if !publicReasonToken.MatchString(field) {
			break
		}
		words = append(words, field)
	}
	if len(words) == 0 || !roots[words[0]] {
		return nil
	}
	return words
}

// publicReasonRoots is every catalog top-level route plus the retired roots.
func publicReasonRoots() map[string]bool {
	roots := map[string]bool{}
	for _, route := range cli.Routes() {
		roots[route.Name] = true
	}
	for _, token := range publicReasonRetiredRoots {
		roots[token] = true
	}
	return roots
}

// publicReasonUsageVerbs are the literal argv tokens a node's Usage synopsis
// offers right after its path: `list|clear` in `projmux runtime tag
// list|clear`, or `codex|claude|...` in `<codex|claude|...>`. A single-word
// placeholder such as `<name>` is not a verb.
func publicReasonUsageVerbs(path []string, route cli.Route) []string {
	prefix := "projmux " + strings.Join(path, " ") + " "
	var verbs []string
	for _, line := range route.Usage {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		for token := range strings.FieldsSeq(strings.TrimPrefix(line, prefix)) {
			token = strings.Trim(token, "[]")
			if strings.HasPrefix(token, "<") && !strings.Contains(token, "|") {
				continue
			}
			for piece := range strings.SplitSeq(strings.Trim(token, "<>"), "|") {
				if publicReasonToken.MatchString(piece) {
					verbs = append(verbs, piece)
				}
			}
		}
	}
	return verbs
}

// publicReasonRouteProblem is empty when words is a catalog route path,
// optionally followed by one verb of that node's Usage synopsis.
func publicReasonRouteProblem(words []string) string {
	if slices.Contains(words, publicReasonUnknown) {
		return "the route spelling is not statically known"
	}
	path, route, ok := cli.Resolve(words)
	if ok && slices.Equal(path, words[:len(path)]) {
		rest := words[len(path):]
		if len(rest) == 0 || (len(rest) == 1 && slices.Contains(publicReasonUsageVerbs(path, route), rest[0])) {
			return ""
		}
	}
	resolved := strings.Join(path, " ")
	if resolved == "" {
		resolved = "(none)"
	}
	return strconv.Quote(strings.Join(words, " ")) + " is not a catalog route (the catalog resolves route " + strconv.Quote(resolved) + ")"
}

// publicReasonCollect parses every file of the scanned packages and returns
// each route spelling a rejection reason or a backtick hint names, with the
// problems it met parsing.
func publicReasonCollect(pkgs []flagParseGuardPackage, eval *flagSetNameEval) ([]publicReasonSite, []string) {
	e := newPublicReasonEval(eval)
	roots := publicReasonRoots()
	fset := token.NewFileSet()
	var sites []publicReasonSite
	var problems []string
	for _, pkg := range pkgs {
		if !pkg.scan {
			continue
		}
		for _, f := range pkg.files {
			file, err := parser.ParseFile(fset, f.name, f.src, parser.SkipObjectResolution)
			if err != nil {
				problems = append(problems, "parse "+f.name+": "+err.Error())
				continue
			}
			var fn *ast.FuncDecl
			var visit func(node ast.Node) bool
			visit = func(node ast.Node) bool {
				if node == nil {
					return false
				}
				if decl, ok := node.(*ast.FuncDecl); ok {
					if decl.Body != nil {
						outer := fn
						fn = decl
						ast.Inspect(decl.Body, visit)
						fn = outer
					}
					return false
				}
				fnName := ""
				if fn != nil {
					fnName = flagParseGuardFuncName(fn)
				}
				line := fset.Position(node.Pos()).Line
				if arg := publicReasonArg(node); arg != nil {
					for _, text := range e.values(f.name, fn, arg, 0) {
						if words := publicReasonRouteWords(text, roots); words != nil {
							sites = append(sites, publicReasonSite{file: f.name, fn: fnName, line: line, kind: "reason", words: words, text: text, lead: publicReasonLead(arg)})
						}
					}
				}
				if lit, ok := node.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.HasPrefix(lit.Value, `"`) {
					value, err := strconv.Unquote(lit.Value)
					if err == nil {
						for _, match := range publicReasonHintSpan.FindAllStringSubmatch(value, -1) {
							if words := publicReasonHintWords(match[1], roots); words != nil {
								sites = append(sites, publicReasonSite{file: f.name, fn: fnName, line: line, kind: "hint", words: words, text: match[0]})
							}
						}
					}
				}
				return true
			}
			ast.Inspect(file, visit)
		}
	}
	return sites, problems
}

// publicReasonProblems checks every collected site. A reason must name a
// catalog route path, optionally followed by one Usage verb; a hint only
// needs its leading words to reach a catalog route, because what follows is
// an operand (`agent integrate claude`). A site the rule rejects needs a row
// in exceptions. It returns the problems and the exception keys used.
func publicReasonProblems(sites []publicReasonSite, exceptions map[string]publicReasonException) ([]string, map[string]bool) {
	var problems []string
	used := map[string]bool{}
	for _, site := range sites {
		problem := publicReasonRouteProblem(site.words)
		if site.kind == "hint" && problem != "" && !slices.Contains(site.words, publicReasonUnknown) {
			if path, _, ok := cli.Resolve(site.words); ok && slices.Equal(path, site.words[:len(path)]) {
				problem = ""
			}
		}
		if problem == "" {
			continue
		}
		if row, ok := flagSetNameExceptions[site.key()]; ok && site.dynamic() && len(row.routes) > 0 {
			continue
		}
		if _, ok := exceptions[site.key()]; ok {
			used[site.key()] = true
			continue
		}
		problems = append(problems, site.describe()+": "+problem+"; name the route that ran, or add key "+strconv.Quote(site.key())+" to publicReasonExceptions")
	}
	sort.Strings(problems)
	return problems, used
}

// TestPublicRouteReasonsNameCatalogRoutes holds every rejection reason in
// internal/app/** (usageError, errors.New, fmt.Errorf, InputError.Detail) and
// every backtick hint in a string literal to the catalog: when the text leads
// with a route spelling, that spelling must be a catalog route path, so stderr
// never names `notify push` for `create notification` or `prune ephemeral` for
// `runtime prune`. Hidden plumbing and non-route text are closed exception
// rows.
func TestPublicRouteReasonsNameCatalogRoutes(t *testing.T) {
	t.Parallel()
	pkgs := flagParseGuardLoadRepo(t, filepath.Join("..", ".."))
	eval, problems := newFlagSetNameEval(pkgs)
	sites, collectProblems := publicReasonCollect(pkgs, eval)
	checked, used := publicReasonProblems(sites, publicReasonExceptions)
	for _, problem := range append(append(problems, collectProblems...), checked...) {
		t.Error(problem)
	}
	for _, token := range publicReasonRetiredRoots {
		if _, ok := cli.LookupRoute(token); ok {
			t.Errorf("retired root %q is a live catalog route; drop it from publicReasonRetiredRoots", token)
		}
	}
	for _, key := range slices.Sorted(func(yield func(string) bool) {
		for key := range publicReasonExceptions {
			if !yield(key) {
				return
			}
		}
	}) {
		row := publicReasonExceptions[key]
		if strings.TrimSpace(row.reason) == "" {
			t.Errorf("exception %q: missing reason", key)
		}
		for _, route := range row.routes {
			tokens := strings.Fields(route)
			if path, _, ok := cli.Resolve(tokens); !ok || !slices.Equal(path, tokens) {
				t.Errorf("exception %q: route %q is not a catalog route path", key, route)
			}
		}
		if !used[key] {
			t.Errorf("exception %q is stale: no reason or hint names that spelling in that file any more", key)
		}
	}
	reasons, hints := 0, 0
	for _, site := range sites {
		if site.kind == "reason" {
			reasons++
		} else {
			hints++
		}
	}
	if reasons < 450 || hints < 55 {
		t.Errorf("examined %d route-shaped reasons and %d hints, want at least 450 and 55; the site collection has regressed", reasons, hints)
	}
}

// TestPublicRouteReasonGuardDetectsDrift is the negative control: a reason
// naming a retired root, one naming a retired child of a live root, one whose
// route comes from a parameter that takes a retired spelling, one whose route
// is not statically known, and a hint naming a retired root are each reported
// with the site; catalog spellings reached through literals, parameters,
// formats, and Usage verbs pass.
func TestPublicRouteReasonGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	const pkgPath = "example.test/guard/internal/app"
	const src = `package app

import (
	"errors"
	"fmt"
)

type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }

func usageError(message string) error { return &UsageError{Message: message} }

const queueHint = "Use ` + "`notify reconcile`" + ` to repair; ` + "`projmux get notifications --live`" + ` explains drift."

func retiredRoot() error { return usageError("notify push requires --text") }

func retiredChild() error {
	return usageError("prune ephemeral does not accept positional arguments")
}

func level(route string) error { return usageError(route + " requires a subcommand") }

func unknownVerb(route, verb string) error {
	return usageError(fmt.Sprintf("unknown %s subcommand: %s", route, verb))
}

func callers() {
	_ = level("pin project")
	_ = level("tag")
	_ = unknownVerb("pin project", "x")
}

func dynamic(command string) error { return fmt.Errorf("%s requires exactly 1 <name> argument", command) }

func canonical() error {
	_ = errors.New("runtime prune does not accept positional arguments")
	_ = usageError("pin project list does not accept positional arguments")
	_ = errors.New("typed metadata mirror requires a tmux runner")
	return usageError("get notifications --limit must be >= 0")
}

func tabled() error { return errors.New("tmux install does not accept positional arguments") }
`
	pkgs := []flagParseGuardPackage{{importPath: pkgPath, scan: true, files: []flagParseGuardFile{{name: "synthetic/app.go", src: []byte(src)}}}}
	eval, problems := newFlagSetNameEval(pkgs)
	if len(problems) != 0 {
		t.Fatalf("negative control: evaluator problems: %v", problems)
	}
	sites, problems := publicReasonCollect(pkgs, eval)
	if len(problems) != 0 {
		t.Fatalf("negative control: collection problems: %v", problems)
	}
	failures, used := publicReasonProblems(sites, map[string]publicReasonException{
		`synthetic/app.go "tmux"`: {reason: publicReasonHiddenReason},
		`synthetic/app.go "kill"`: {reason: "stale"},
	})
	joined := strings.Join(failures, "\n")
	for _, want := range []string{
		`synthetic/app.go:16: func retiredRoot, reason "notify push requires --text": "notify push" is not a catalog route (the catalog resolves route "(none)")`,
		`synthetic/app.go:19: func retiredChild, reason "prune ephemeral does not accept positional arguments": "prune ephemeral" is not a catalog route (the catalog resolves route "prune")`,
		`synthetic/app.go:22: func level, reason "tag requires a subcommand": "tag" is not a catalog route`,
		`synthetic/app.go:34: func dynamic, reason "<?> requires exactly 1 <name> argument": the route spelling is not statically known`,
		"synthetic/app.go:14: func (package scope), hint \"`notify reconcile`\": \"notify reconcile\" is not a catalog route",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("negative control: missing failure %q in:\n%s", want, joined)
		}
	}
	for _, clean := range []string{"pin project", "runtime prune", "get notifications", "typed metadata", "func tabled"} {
		if strings.Contains(joined, clean) {
			t.Errorf("negative control: %q must pass:\n%s", clean, joined)
		}
	}
	if len(failures) != 5 {
		t.Errorf("negative control: got %d failures, want 5:\n%s", len(failures), joined)
	}
	if !used[`synthetic/app.go "tmux"`] || used[`synthetic/app.go "kill"`] {
		t.Errorf("negative control: exception use = %v, want only the tmux row", used)
	}
}

// TestNotifyRoutesNameTheirRouteInRefusals pins the routes that forward into
// the notify handler end to end: each refusal names the route that ran, and
// the queue hint printed with it points at `get notifications --live` and
// `notification reconcile`, never at the retired `notify ...` spellings.
func TestNotifyRoutesNameTheirRouteInRefusals(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	for _, row := range []struct {
		argv []string
		want string
	}{
		{argv: []string{"create", "notification"}, want: "create notification requires --text"},
		{argv: []string{"create", "notification", "--text", "hi"}, want: "create notification requires --target"},
		{argv: []string{"create", "notification", "--text", "hi", "--target", "s", "x"}, want: "create notification does not accept positional arguments"},
		{argv: []string{"get", "notifications", "x"}, want: "get notifications does not accept positional arguments"},
		{argv: []string{"get", "notifications", "--limit", "-1"}, want: "get notifications --limit must be >= 0"},
		{argv: []string{"notification", "ack"}, want: "notification ack requires exactly 1 <id> argument or --all"},
		{argv: []string{"notification", "ack", "--all", "x"}, want: "notification ack --all does not accept positional arguments"},
		{argv: []string{"delete", "notification"}, want: "delete notification requires exactly 1 <id> argument or --all"},
		{argv: []string{"delete", "notification", "--all", "x"}, want: "delete notification --all does not accept positional arguments"},
		{argv: []string{"notification", "reconcile", "x"}, want: "notification reconcile does not accept positional arguments"},
	} {
		var stdout, stderr bytes.Buffer
		err := New().Run(row.argv, &stdout, &stderr)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("%v: err = %v, want a usage error", row.argv, err)
		}
		if got := err.Error(); got != row.want {
			t.Errorf("%v: error = %q, want %q", row.argv, got, row.want)
		}
		if strings.Contains(stderr.String(), "`notify ") {
			t.Errorf("%v: stderr names a retired `notify ...` route: %q", row.argv, stderr.String())
		}
	}
}
