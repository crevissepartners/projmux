package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	iofs "io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// aiRouteUsageHelper is the one printer the ai handlers name their route
// through; the AST scan below keeps every call of it a string literal.
const aiRouteUsageHelper = "printRouteUsage"

// aiRouteUsageCallFloor is the number of printRouteUsage calls in internal/app
// when the retired `ai` listing was split per route. The set may grow; it must
// not silently shrink back into a shared listing.
const aiRouteUsageCallFloor = 36

// aiRouteUsageExit is the exit class a rejected call keeps: nil (help, exit
// 0), a usage error (exit 2), or any other error (exit 1).
type aiRouteUsageExit int

const (
	aiRouteUsageExitOK aiRouteUsageExit = iota
	aiRouteUsageExitUsage
	aiRouteUsageExitError
)

func (e aiRouteUsageExit) of(err error) aiRouteUsageExit {
	switch {
	case err == nil:
		return aiRouteUsageExitOK
	case IsUsageError(err):
		return aiRouteUsageExitUsage
	default:
		return aiRouteUsageExitError
	}
}

// aiRouteUsageRow is one rejected call an ai handler prints usage for. route
// is the catalog path the printed block must match; app rows are driven
// through App.Run, handler rows (the legacy aiCommand status/topic branches no
// route dispatches to) through aiCommand.Run. help rows print on stdout.
type aiRouteUsageRow struct {
	route  string
	app    []string
	ai     []string
	exit   aiRouteUsageExit
	reason string
	help   bool
}

// aiRouteUsageRows reaches every printRouteUsage call site at least once.
var aiRouteUsageRows = []aiRouteUsageRow{
	{route: "agent integrate", app: []string{"agent", "integrate"}, exit: aiRouteUsageExitUsage, reason: "agent integrate requires <agent-kind>"},
	{route: "agent integrate", app: []string{"agent", "integrate", "help"}, exit: aiRouteUsageExitOK, help: true},
	{route: "agent integrate", app: []string{"agent", "integrate", "bogus"}, exit: aiRouteUsageExitUsage, reason: "unknown agent integrate agent-kind: bogus"},
	{route: "agent integrate", app: []string{"agent", "integrate", "tmux-bell", "x"}, exit: aiRouteUsageExitUsage, reason: "agent integrate tmux-bell does not accept positional arguments"},
	{route: "agent integrate", app: []string{"agent", "integrate", "claude", "x"}, exit: aiRouteUsageExitUsage, reason: "agent integrate claude does not accept positional arguments"},
	{route: "agent integrate", app: []string{"agent", "integrate", "codex", "x"}, exit: aiRouteUsageExitUsage, reason: "agent integrate codex does not accept positional arguments"},
	{route: "agent integrate", app: []string{"agent", "integrate", "antigravity", "x"}, exit: aiRouteUsageExitUsage, reason: "agent integrate antigravity does not accept positional arguments"},
	{route: "diagnostics agent-hook", app: []string{"diagnostics", "agent-hook", "x"}, exit: aiRouteUsageExitUsage, reason: "diagnostics agent-hook does not accept positional arguments"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest requires <agent-kind>"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "bogus"}, exit: aiRouteUsageExitError, reason: "unknown internal agent-hook ingest source: bogus"},
	{route: "internal agent-hook ingest", ai: []string{"ingest", "help"}, exit: aiRouteUsageExitOK},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "codex-hook", "x"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest codex-hook reads JSON from stdin and accepts no payload arguments"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "claude-hook", "x"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest claude-hook reads JSON from stdin and accepts no payload arguments"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "antigravity-hook", "x"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest antigravity-hook does not accept positional payload arguments"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "bell", "x"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest bell does not accept positional arguments"},
	{route: "internal agent-hook ingest", app: []string{"internal", "agent-hook", "ingest", "bell"}, exit: aiRouteUsageExitError, reason: "internal agent-hook ingest bell requires --pane <pane_id>"},
	{route: "internal agent-hook watch-title", app: []string{"internal", "agent-hook", "watch-title", "a", "b"}, exit: aiRouteUsageExitError, reason: "internal agent-hook watch-title accepts at most 1 [pane] argument"},
	{route: "internal agent-pane launch-default", app: []string{"internal", "agent-pane", "launch-default", "sideways"}, exit: aiRouteUsageExitError, reason: "internal agent-pane launch-default direction must be right or down"},
	{route: "internal agent-pane picker", app: []string{"internal", "agent-pane", "picker", "right", "down"}, exit: aiRouteUsageExitError, reason: "ai picker accepts at most 1 [right|down] argument"},
	{route: "internal agent-pane picker", app: []string{"internal", "agent-pane", "picker", "--shell", "--resume"}, exit: aiRouteUsageExitError, reason: "ai picker cannot combine --shell and --resume"},
	{route: "internal agent-pane launch-selection", app: []string{"internal", "agent-pane", "launch-selection"}, exit: aiRouteUsageExitUsage, reason: "internal agent-pane launch-selection requires exactly 1 <right|down> argument"},
	{route: "internal agent-pane launch-selection", app: []string{"internal", "agent-pane", "launch-selection", "sideways"}, exit: aiRouteUsageExitError, reason: "internal agent-pane launch-selection direction must be right or down"},
	{route: "agent status", ai: []string{"status"}, exit: aiRouteUsageExitError, reason: "ai status requires a subcommand"},
	{route: "agent status", ai: []string{"status", "set"}, exit: aiRouteUsageExitError, reason: "ai status set requires <thinking|waiting|idle> [pane]"},
	{route: "agent status", ai: []string{"status", "help"}, exit: aiRouteUsageExitOK},
	{route: "agent status", ai: []string{"status", "bogus"}, exit: aiRouteUsageExitError, reason: "unknown ai status subcommand: bogus"},
	{route: "agent topic", ai: []string{"topic"}, exit: aiRouteUsageExitError, reason: "ai topic requires a subcommand"},
	{route: "agent topic", ai: []string{"topic", "set", "--pane"}, exit: aiRouteUsageExitError, reason: "--pane requires a value"},
	{route: "agent topic", ai: []string{"topic", "set", "--pane", "%1"}, exit: aiRouteUsageExitError, reason: "ai topic set requires <text>"},
	{route: "agent topic", ai: []string{"topic", "set", "a", "b", "--pane", "%1"}, exit: aiRouteUsageExitError, reason: "ai topic set accepts a single <text> argument"},
	{route: "agent topic", ai: []string{"topic", "set", " ", "--pane", "%1"}, exit: aiRouteUsageExitError, reason: "ai topic set requires non-empty <text>"},
	{route: "agent topic", ai: []string{"topic", "clear", "--pane"}, exit: aiRouteUsageExitError, reason: "--pane requires a value"},
	{route: "agent topic", ai: []string{"topic", "clear", "x"}, exit: aiRouteUsageExitError, reason: "ai topic clear takes no positional arguments"},
	{route: "agent topic", ai: []string{"topic", "get", "--pane"}, exit: aiRouteUsageExitError, reason: "--pane requires a value"},
	{route: "agent topic", ai: []string{"topic", "get", "x"}, exit: aiRouteUsageExitError, reason: "ai topic get takes no positional arguments"},
	{route: "agent topic", ai: []string{"topic", "help"}, exit: aiRouteUsageExitOK, help: true},
	{route: "agent topic", ai: []string{"topic", "bogus"}, exit: aiRouteUsageExitError, reason: "unknown ai topic subcommand: bogus"},
}

// aiRouteUsageReasonOnlyRows are ai handler rejections that serve no catalog
// route (the retired `ai` root and `ai notify`, and the launch-provider and
// launch-shell bridges the catalog does not list): the reason stands alone.
var aiRouteUsageReasonOnlyRows = []aiRouteUsageRow{
	{app: []string{"internal", "agent-pane", "launch-provider", "claude"}, exit: aiRouteUsageExitUsage, reason: "internal agent-pane launch-provider requires <provider> <right|down>"},
	{app: []string{"internal", "agent-pane", "launch-provider", "claude", "sideways"}, exit: aiRouteUsageExitError, reason: "internal agent-pane launch-provider direction must be right or down"},
	{app: []string{"internal", "agent-pane", "launch-shell", "right", "down"}, exit: aiRouteUsageExitError, reason: "internal agent-pane launch-shell accepts at most 1 [right|down] argument"},
	{ai: []string{}, exit: aiRouteUsageExitError, reason: "ai requires a subcommand"},
	{ai: []string{"bogus"}, exit: aiRouteUsageExitError, reason: "unknown ai subcommand: bogus"},
	{ai: []string{"help"}, exit: aiRouteUsageExitOK},
	{ai: []string{"notify", "a", "b", "c"}, exit: aiRouteUsageExitError, reason: "ai notify accepts [notify|reset] [pane]"},
	{ai: []string{"notify", "bogus", "%1"}, exit: aiRouteUsageExitError, reason: "unknown ai notify action: bogus"},
}

// aiRouteUsageTestApp wires the ai handler behind every route that forwards
// into it. Every row fails before a leaf reaches tmux, a picker, or a provider.
func aiRouteUsageTestApp(ai *aiCommand) *App {
	app := leftoverArgsTestApp()
	app.ai = ai
	app.agent.ai = ai
	app.diagnostics = &diagnosticsCommand{ai: ai}
	return app
}

// runAIRouteUsageRow drives row and returns its error, stdout, and stderr.
func runAIRouteUsageRow(t *testing.T, row aiRouteUsageRow) (error, string, string) {
	t.Helper()
	ai := testAICommand(t.TempDir())
	ai.stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	var err error
	if row.app != nil {
		err = aiRouteUsageTestApp(ai).Run(row.app, &stdout, &stderr)
	} else {
		err = ai.Run(row.ai, &stdout, &stderr)
	}
	return err, stdout.String(), stderr.String()
}

func (row aiRouteUsageRow) name() string {
	if row.app != nil {
		return strings.Join(row.app, " ")
	}
	return "aiCommand " + strings.Join(row.ai, " ")
}

// aiRouteCatalogUsage returns the catalog Usage of route, failing the test when
// route is not a catalog path.
func aiRouteCatalogUsage(t *testing.T, route string) []string {
	t.Helper()
	tokens := strings.Fields(route)
	path, resolved, ok := cli.Resolve(tokens)
	if !ok || !slices.Equal(path, tokens) {
		t.Fatalf("%s: catalog does not resolve the route (got path %v)", route, path)
	}
	return resolved.Usage
}

// aiRouteUsageBlock renders synopsis in the `Usage:` block shape.
func aiRouteUsageBlock(synopsis []string) string {
	if len(synopsis) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Usage:\n")
	for _, line := range synopsis {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// aiRouteUsageProblems checks printed, the usage a rejected call on route
// wrote, against the catalog: exactly the route's Usage block (nothing for a
// route without Usage) and no line of another route. Every problem names the
// route. The line-level synopsis check runs where it can read the synopsis:
// it skips a line whose route is followed by an operand word (`agent topic
// get|clear …`, `internal agent-hook ingest bell …`), and the exact block
// comparison covers those.
func aiRouteUsageProblems(t *testing.T, route, printed string) []string {
	t.Helper()
	synopsis := aiRouteCatalogUsage(t, route)
	var problems []string
	if len(handlerUsageRouteLines(route, strings.Join(synopsis, "\n"))) > 0 {
		problems = append(problems, handlerUsageCatalogProblems(route, printed, handlerUsageCatalogSynopsis(t, route))...)
	}
	if foreign := handlerUsageForeignLines(route, printed); len(foreign) != 0 {
		problems = append(problems, route+": usage lists other routes: "+strconv.Quote(strings.Join(foreign, " | ")))
	}
	if want := aiRouteUsageBlock(synopsis); printed != want {
		problems = append(problems, route+": printed usage "+strconv.Quote(printed)+", want the catalog block "+strconv.Quote(want))
	}
	return problems
}

// TestAIHandlersPrintOnlyTheirRouteUsage drives every rejection an ai handler
// prints usage for and pins the block to that route's catalog Usage, the
// reason, and the exit class.
func TestAIHandlersPrintOnlyTheirRouteUsage(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	for _, row := range aiRouteUsageRows {
		t.Run(row.name(), func(t *testing.T) {
			err, stdout, stderr := runAIRouteUsageRow(t, row)
			if got := row.exit.of(err); got != row.exit {
				t.Fatalf("%s: exit class = %d (err %v), want %d", row.route, got, err, row.exit)
			}
			if err != nil && err.Error() != row.reason {
				t.Errorf("%s: reason = %q, want %q", row.route, err.Error(), row.reason)
			}
			printed, other := stderr, stdout
			if row.help {
				printed, other = stdout, stderr
			}
			if other != "" {
				t.Errorf("%s: wrote %q to the other stream, want nothing", row.route, other)
			}
			for _, problem := range aiRouteUsageProblems(t, row.route, printed) {
				t.Error(problem)
			}
		})
	}
}

// TestAIHandlerRejectionsWithoutARoutePrintNoUsage pins the ai handler
// rejections that serve no catalog route: reason and exit class unchanged, and
// no usage block at all.
func TestAIHandlerRejectionsWithoutARoutePrintNoUsage(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	for _, row := range aiRouteUsageReasonOnlyRows {
		t.Run(row.name(), func(t *testing.T) {
			err, stdout, stderr := runAIRouteUsageRow(t, row)
			if got := row.exit.of(err); got != row.exit {
				t.Fatalf("%s: exit class = %d (err %v), want %d", row.name(), got, err, row.exit)
			}
			if err != nil && err.Error() != row.reason {
				t.Errorf("%s: reason = %q, want %q", row.name(), err.Error(), row.reason)
			}
			if stdout != "" || stderr != "" {
				t.Errorf("%s: printed stdout=%q stderr=%q, want the reason alone", row.name(), stdout, stderr)
			}
		})
	}
}

// aiRouteUsageCall is one printRouteUsage call found in the package source.
type aiRouteUsageCall struct {
	pos   string
	route string
}

// scanAIRouteUsageCalls parses every non-test Go file of this package and
// returns its printRouteUsage calls. A call whose route is not a string
// literal is reported as a problem: the set must stay closed.
func scanAIRouteUsageCalls(t *testing.T) ([]aiRouteUsageCall, []string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var calls []aiRouteUsageCall
	var problems []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != aiRouteUsageHelper {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			if len(call.Args) != 2 {
				problems = append(problems, pos+": "+aiRouteUsageHelper+" call takes "+strconv.Itoa(len(call.Args))+" arguments, want (w, route)")
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				problems = append(problems, pos+": "+aiRouteUsageHelper+" route is not a string literal")
				return true
			}
			route, err := strconv.Unquote(lit.Value)
			if err != nil {
				problems = append(problems, pos+": "+err.Error())
				return true
			}
			calls = append(calls, aiRouteUsageCall{pos: pos, route: route})
			return true
		})
	}
	return calls, problems
}

// aiRouteUsageSetProblems compares the routes the call sites name with the
// routes the table drives, both ways, and checks each resolves in the catalog.
// Every problem names the route.
func aiRouteUsageSetProblems(calls []aiRouteUsageCall, rows []aiRouteUsageRow) []string {
	called := map[string][]string{}
	for _, call := range calls {
		called[call.route] = append(called[call.route], call.pos)
	}
	driven := map[string]bool{}
	for _, row := range rows {
		driven[row.route] = true
	}
	var problems []string
	for route, positions := range called {
		tokens := strings.Fields(route)
		if path, _, ok := cli.Resolve(tokens); !ok || !slices.Equal(path, tokens) {
			problems = append(problems, route+": "+aiRouteUsageHelper+" names a route the catalog does not resolve ("+strings.Join(positions, ", ")+")")
		}
		if !driven[route] {
			problems = append(problems, route+": "+aiRouteUsageHelper+" call has no row in aiRouteUsageRows ("+strings.Join(positions, ", ")+")")
		}
	}
	for route := range driven {
		if _, ok := called[route]; !ok {
			problems = append(problems, route+": aiRouteUsageRows drives a route no "+aiRouteUsageHelper+" call names")
		}
	}
	sort.Strings(problems)
	return problems
}

// TestAIRouteUsageCallSetIsClosed keeps the printRouteUsage call sites and the
// table above in lockstep: every call names a literal catalog route the table
// drives, every table route has a call, and the call count cannot shrink.
func TestAIRouteUsageCallSetIsClosed(t *testing.T) {
	t.Parallel()
	calls, problems := scanAIRouteUsageCalls(t)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(calls) < aiRouteUsageCallFloor {
		t.Errorf("found %d %s calls, want at least %d", len(calls), aiRouteUsageHelper, aiRouteUsageCallFloor)
	}
	for _, problem := range aiRouteUsageSetProblems(calls, aiRouteUsageRows) {
		t.Error(problem)
	}
}

// TestAIRouteUsageCallSetDetectsDrift is the negative control for the set
// comparison: a call the table misses, a table row no call names, and a route
// the catalog does not resolve are each reported by route.
func TestAIRouteUsageCallSetDetectsDrift(t *testing.T) {
	t.Parallel()
	calls := []aiRouteUsageCall{{pos: "x.go:1", route: "agent integrate"}, {pos: "x.go:2", route: "ai integrate"}}
	rows := []aiRouteUsageRow{{route: "agent integrate"}, {route: "agent topic"}}
	problems := aiRouteUsageSetProblems(calls, rows)
	for _, want := range []string{
		"agent topic: aiRouteUsageRows drives a route no printRouteUsage call names",
		"ai integrate: printRouteUsage call has no row in aiRouteUsageRows (x.go:2)",
		"ai integrate: printRouteUsage names a route the catalog does not resolve (x.go:2)",
	} {
		if !slices.Contains(problems, want) {
			t.Errorf("set check problems = %q, want %q", problems, want)
		}
	}
}

// aiRouteUsageRetiredSpellings are the retired `ai` route spellings no
// user-visible string in internal/app may carry any more.
var aiRouteUsageRetiredSpellings = []string{"ai integrate", "ai watch-title", "ai settings"}

// TestAIHandlersNameNoRetiredRoute keeps the retired listing and spellings
// out of internal/app: printAIUsage is gone and no non-test string literal
// names `ai integrate`, `ai watch-title`, or `ai settings`.
func TestAIHandlersNameNoRetiredRoute(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if node.Name.Name == "printAIUsage" {
					t.Errorf("%s: printAIUsage still exists; print the route's catalog usage with %s", fset.Position(node.Pos()), aiRouteUsageHelper)
				}
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				for _, retired := range aiRouteUsageRetiredSpellings {
					if strings.Contains(node.Value, retired) {
						t.Errorf("%s: string literal %s names the retired route %q", fset.Position(node.Pos()), node.Value, retired)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// aiRouteUsageRetiredListing is the 15-line listing printAIUsage printed from
// every ai handler before each call site named its own route.
const aiRouteUsageRetiredListing = `Usage:
  projmux create agent --provider <claude|codex|antigravity> [--project <ref>] [--window <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>] [-- <extra-arg>...]
  projmux create pane [--project <ref>] [--window <ref>]... [--create-window] [--all-windows | --primary-window] [--placement right|down] [-o <mode>]
  projmux config edit [--get|--set <mode>]
  projmux agent status set <thinking|waiting|idle> [pane]
  projmux create notification [flags]
  projmux internal agent-hook watch-title [pane]
  projmux internal agent-hook ingest codex-hook [--pane <pane_uid|pane_id>] < payload.json
  projmux internal agent-hook ingest claude-hook [--pane <pane_uid|pane_id>] < payload.json
  projmux internal agent-hook ingest antigravity-hook [--event <PreInvocation|PostInvocation|PostToolUse|Stop>] [--pane <pane_uid|pane_id>] < payload.json
  projmux internal agent-hook ingest bell --pane <pane_id>
  projmux diagnostics agent-hook [--tail N] [--json] [--path]
  projmux agent integrate <codex|claude|antigravity|tmux-bell> [--dry-run] [--remove]
  projmux agent topic set <text> [--pane <id>]
  projmux agent topic clear [--pane <id>]
  projmux agent topic get [--pane <id>]
`

// TestAIRouteUsageCheckerRejectsRetiredListing is the negative control for the
// per-route checker: the old shared listing is rejected for every route it was
// printed for, and each problem names the route.
func TestAIRouteUsageCheckerRejectsRetiredListing(t *testing.T) {
	t.Parallel()
	routes := map[string]bool{}
	for _, row := range aiRouteUsageRows {
		routes[row.route] = true
	}
	for route := range routes {
		problems := aiRouteUsageProblems(t, route, aiRouteUsageRetiredListing)
		if len(problems) == 0 {
			t.Errorf("%s: checker accepted the retired ai listing", route)
		}
		for _, problem := range problems {
			if !strings.HasPrefix(problem, route+": ") {
				t.Errorf("problem %q does not name route %q", problem, route)
			}
		}
	}
}
