package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// commandLiteral is one backtick-quoted `projmux ...` command found in a
// string literal of product code.
type commandLiteral struct {
	pos  string // repo-relative file:line
	text string // the span between the backticks; holeMarker marks a non-literal operand
}

// holeMarker stands for a non-literal operand of a `+` concatenation, such as
// the agentUID in "`projmux agent resume uid:" + agentUID + "`". It is filled
// like a format verb.
const holeMarker = "\x00"

// commandLiteralRoots are the trees whose non-test Go files may print a
// command for an operator to run.
var commandLiteralRoots = []string{"internal", "cmd"}

// extractCommandLiterals parses every non-test .go file under dirs and returns
// each backtick-delimited span that starts with "projmux " inside a string
// literal. A `+` chain that contains a string literal is read as one string so a
// command split around a variable is still seen whole; its literals are not
// visited a second time on their own.
func extractCommandLiterals(t *testing.T, repoRoot string, dirs []string) []commandLiteral {
	t.Helper()
	var out []commandLiteral
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			consumed := map[*ast.BasicLit]bool{}
			ast.Inspect(file, func(node ast.Node) bool {
				var text string
				switch node := node.(type) {
				case *ast.BinaryExpr:
					if node.Op != token.ADD {
						return true
					}
					var b strings.Builder
					sawLiteral := false
					var flatten func(ast.Expr)
					flatten = func(expr ast.Expr) {
						switch expr := expr.(type) {
						case *ast.BinaryExpr:
							if expr.Op == token.ADD {
								flatten(expr.X)
								flatten(expr.Y)
								return
							}
						case *ast.ParenExpr:
							flatten(expr.X)
							return
						case *ast.BasicLit:
							if expr.Kind == token.STRING {
								value, unquoteErr := strconv.Unquote(expr.Value)
								if unquoteErr != nil {
									t.Fatalf("%s: unquote %s: %v", fset.Position(expr.Pos()), expr.Value, unquoteErr)
								}
								b.WriteString(value)
								consumed[expr] = true
								sawLiteral = true
								return
							}
						}
						b.WriteString(holeMarker)
					}
					flatten(node)
					if !sawLiteral {
						return true
					}
					text = b.String()
				case *ast.BasicLit:
					if node.Kind != token.STRING || consumed[node] {
						return true
					}
					value, unquoteErr := strconv.Unquote(node.Value)
					if unquoteErr != nil {
						t.Fatalf("%s: unquote %s: %v", fset.Position(node.Pos()), node.Value, unquoteErr)
					}
					text = value
				default:
					return true
				}
				// Odd-indexed parts sit between a pair of backticks.
				parts := strings.Split(text, "`")
				for i := 1; i < len(parts)-1; i += 2 {
					if strings.HasPrefix(parts[i], "projmux ") {
						out = append(out, commandLiteral{
							pos:  fmt.Sprintf("%s:%d", filepath.ToSlash(rel), fset.Position(node.Pos()).Line),
							text: parts[i],
						})
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return out
}

// filledToken is one argv token after placeholder filling. placeholder records
// that the token's text was invented by the fill rules, so a route check never
// blames the literal for a word the test chose.
type filledToken struct {
	text        string
	placeholder bool
}

var (
	// commandPlaceholder matches a printf verb, a concatenation hole, or an
	// angle-bracket placeholder such as <uid> or <absolute-path>.
	commandPlaceholder = regexp.MustCompile(`%[sqdv]|\x00|<[a-z][a-z-]*>`)
	// commandEllipsis is "more arguments follow".
	commandEllipsis = map[string]bool{"...": true, "…": true}
	// selectorNoMatch is the no-match cardinality detail that
	// internal/core/selector (cardinalityDetail) renders when a selector
	// resolved nothing. The app flattens that typed SelectorError into a
	// message-only *UsageError, so its ErrNotFound unwrap does not survive and
	// the phrase its one formatter owns is the only signal left.
	selectorNoMatch = regexp.MustCompile(` matched no [a-z]+s, want (exactly one|at least one)$`)
)

// contextRejections are the exact refusal phrases of a route that needs a
// runtime context the hermetic run cannot give. Such a refusal is a
// Non-Guarantee of C-1, not a refused spelling. Each entry is a phrase, never
// a literal: the same literal refused for its argument shape still fails,
// because argument parsing runs first and reports its own message.
var contextRejections = []struct{ phrase, reason string }{
	{
		phrase: "no --project <ref> was given and this invocation is not inside a tmux client",
		reason: "create routes default their Project to the active tmux client, which the isolated run never has (create_resource.go requireExplicitProject)",
	},
	{
		phrase: "explicit Window/Pane selector matched no Project owner",
		reason: "with no --project, create infers the Project from the named Window/Pane, which the empty isolated Registry does not hold (create_resource.go)",
	},
}

// contextRejection returns the reason of the context phrase err carries, or "".
func contextRejection(err error) string {
	if err == nil {
		return ""
	}
	for _, entry := range contextRejections {
		if strings.Contains(err.Error(), entry.phrase) {
			return entry.reason
		}
	}
	return ""
}

// endsWithEllipsis reports whether the raw literal ends in `...` or `…`, the
// notation for "this command, with arguments left out". It reads the literal
// before fillCommandLiteral drops the ellipsis.
func endsWithEllipsis(literal string) bool {
	fields := strings.Fields(literal)
	return len(fields) > 0 && commandEllipsis[fields[len(fields)-1]]
}

// fillCommandLiteral turns one literal into argv. The rules are the whole
// notation the product strings use, and they are deliberately small:
//
//   - `...` and `…` mean "more arguments" and are dropped.
//   - `[x]` marks an optional segment and is dropped.
//   - `a|b|c` lists alternatives; the first is taken.
//   - A placeholder (%s, %q, %d, %v, a concatenation hole, or <name>) gets a
//     representative value chosen by what it stands for: the flag before it
//     (--root wants an absolute path, --provider a real provider), its own
//     angle name (<provider>, <absolute-path>), %d a number, and anything else
//     -- a name, a `uid:` selector body, a payload -- a plain word.
func fillCommandLiteral(literal, absolutePath string) []filledToken {
	provider := cli.AgentProviders()[0]
	var out []filledToken
	for field := range strings.FieldsSeq(strings.TrimPrefix(literal, "projmux ")) {
		if commandEllipsis[field] {
			continue
		}
		if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") {
			continue
		}
		if !strings.HasPrefix(field, "-") && strings.Contains(field, "|") {
			field, _, _ = strings.Cut(field, "|")
		}
		previous := ""
		if len(out) > 0 {
			previous = out[len(out)-1].text
		}
		filled := false
		field = commandPlaceholder.ReplaceAllStringFunc(field, func(match string) string {
			filled = true
			switch {
			case match == "<provider>" || previous == "--provider":
				return provider
			case match == "<absolute-path>" || previous == "--root":
				return absolutePath
			case match == "%d":
				return "1"
			default:
				return "guard"
			}
		})
		out = append(out, filledToken{text: unquoteShellWord(field), placeholder: filled})
	}
	return out
}

// unquoteShellWord strips one level of matching shell quotes.
func unquoteShellWord(word string) string {
	if len(word) >= 2 && (word[0] == '"' || word[0] == '\'') && word[len(word)-1] == word[0] {
		return word[1 : len(word)-1]
	}
	return word
}

// commandRouteVerdict resolves argv through the catalog. It reports template
// when a placeholder occupies a route position (`projmux %s`, `projmux delete
// <kind> ...`): the route is chosen at run time, so the literal names no route
// to judge. Otherwise it reports a problem when the first token is unknown or
// when a literal bare word after a node that has children was not one of them
// -- the catalog then fell back to a shorter prefix than the literal spells.
//
// routeLen is the number of argv tokens the catalog consumed as the route.
func commandRouteVerdict(argv []filledToken) (routeLen int, template bool, problem string) {
	tokens := make([]string, len(argv))
	for i, tok := range argv {
		tokens[i] = tok.text
	}
	if len(argv) == 0 {
		return 0, false, "no route after `projmux`"
	}
	path, route, ok := cli.Resolve(tokens)
	if !ok {
		if argv[0].placeholder {
			return 0, true, ""
		}
		return 0, false, fmt.Sprintf("unknown top-level route %q", tokens[0])
	}
	if len(path) >= len(argv) || len(route.Children) == 0 {
		return len(path), false, ""
	}
	next := argv[len(path)]
	if next.placeholder {
		return len(path), true, ""
	}
	if strings.HasPrefix(next.text, "-") {
		return len(path), false, ""
	}
	return len(path), false, fmt.Sprintf("resolves only to `%s`: %q is not a child of it", strings.Join(path, " "), next.text)
}

// TestProductCommandLiteralsAreAcceptedByTheCLI is contract C-1: every
// `projmux ...` command that product code prints in backticks resolves to the
// exact catalog route it spells and is not refused as a usage error.
//
// The seam is the settings layer guard's in-process harness
// (newSettingsLayerGuardEnv, settingsLayerGuardEnv.run), reused as is on top of
// the package's liveguard TestMain: a temp HOME and XDG homes (so an empty
// private Registry answers every selector and every write lands there), TMUX,
// TMUX_PANE and the Pane anchor unset, PATH an empty directory (tmux, git and
// both provider CLIs do not resolve), a private TMUX_TMPDIR, `update` on a
// refusing HTTP transport behind an unreachable proxy, stdin at /dev/null, a
// self-exec trap that ends any re-executed test binary with exit 3, panic
// recovery, and a per-command timeout. Each turns a command that would mutate
// live state or block into a fast non-usage failure, which this guard accepts:
// only a usage rejection (IsUsageError, exit code 2) counts against a literal,
// and a selector that matched nothing (selectorNoMatch, also exit 2 today) is
// a missing target rather than a refused spelling. A refusal that carries one
// of the exact contextRejections phrases is a Non-Guarantee (the route needs a
// tmux context the run cannot give). A name reference (no argument, flag or
// placeholder after the route, or a trailing ellipsis) is judged on route
// resolution only.
//
// No t.Parallel: the harness swaps process environment, working directory and
// stdin.
func TestProductCommandLiteralsAreAcceptedByTheCLI(t *testing.T) {
	// Extract first: the harness below changes the working directory.
	literals := extractCommandLiterals(t, filepath.Join("..", ".."), commandLiteralRoots)
	unique := map[string]bool{}
	for _, literal := range literals {
		unique[literal.text] = true
	}
	t.Logf("extracted %d `projmux ...` command literals (%d unique)", len(literals), len(unique))
	if len(literals) == 0 {
		t.Fatalf("extracted no `projmux ...` command literals from %v; the extractor is broken", commandLiteralRoots)
	}
	// A stronger positive control: the activation diagnostic's cleanup step
	// must be among what the extractor sees.
	const knownPos, knownText = "internal/app/create_agent.go", "projmux delete agent uid:%s --yes"
	found := false
	for _, literal := range literals {
		if strings.HasPrefix(literal.pos, knownPos+":") && literal.text == knownText {
			found = true
		}
	}
	if !found {
		t.Fatalf("extractor missed the known literal `%s` in %s", knownText, knownPos)
	}

	env := newSettingsLayerGuardEnv(t)
	recorder := &frontReadRecorder{}
	var violations, templates, nameRefs []string
	checked := 0
	for _, literal := range literals {
		display := strings.ReplaceAll(literal.text, holeMarker, "<expr>")
		argv := fillCommandLiteral(literal.text, env.root)
		tokens := make([]string, len(argv))
		for i, tok := range argv {
			tokens[i] = tok.text
		}
		filled := "projmux " + strings.Join(tokens, " ")
		routeLen, template, problem := commandRouteVerdict(argv)
		if template {
			templates = append(templates, fmt.Sprintf("%s `%s`", literal.pos, display))
			continue
		}
		if problem != "" {
			violations = append(violations, fmt.Sprintf("%s\n\tliteral: `%s`\n\tfilled:  `%s`\n\troute:   %s", literal.pos, display, filled, problem))
			continue
		}
		// A name reference -- nothing after the route, or a trailing ellipsis
		// that says the arguments were left out -- names a command rather than
		// spelling an invocation, so it is judged on its route only.
		if routeLen == len(argv) || endsWithEllipsis(literal.text) {
			nameRefs = append(nameRefs, fmt.Sprintf("%s `%s`", literal.pos, display))
			continue
		}
		checked++
		result := env.run(recorder, tokens)
		switch {
		case result.timedOut:
			violations = append(violations, fmt.Sprintf("%s\n\tliteral: `%s`\n\tfilled:  `%s`\n\tseam:    did not return within %s", literal.pos, display, filled, settingsLayerGuardRouteTimeout))
		case result.panicked != nil:
			violations = append(violations, fmt.Sprintf("%s\n\tliteral: `%s`\n\tfilled:  `%s`\n\tpanic:   %v", literal.pos, display, filled, result.panicked))
		case result.err != nil && selectorNoMatch.MatchString(result.err.Error()):
			// Exit 2 today, but the representative uid or name resolved nothing
			// in the empty private Registry: a missing target, not a refused
			// spelling.
		case contextRejection(result.err) != "":
			if testing.Verbose() {
				t.Logf("%s `%s`: context refusal allowed (%s): %v", literal.pos, filled, contextRejection(result.err), result.err)
			}
		case IsUsageError(result.err):
			violations = append(violations, fmt.Sprintf("%s\n\tliteral: `%s`\n\tfilled:  `%s`\n\tusage rejection: %v", literal.pos, display, filled, result.err))
		case testing.Verbose():
			t.Logf("%s `%s`: accepted (err=%v)", literal.pos, filled, result.err)
		}
	}
	sort.Strings(templates)
	sort.Strings(nameRefs)
	t.Logf("counts: extracted=%d unique=%d templates=%d name-refs(route-only)=%d fully-judged=%d",
		len(literals), len(unique), len(templates), len(nameRefs), checked)
	t.Logf("templates (route assembled at run time, not judged):\n\t%s", strings.Join(templates, "\n\t"))
	t.Logf("name references (judged on route resolution only):\n\t%s", strings.Join(nameRefs, "\n\t"))
	if len(violations) > 0 {
		t.Fatalf("%d `projmux ...` command literal(s) are not accepted by the CLI:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
