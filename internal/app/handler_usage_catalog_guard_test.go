package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// handlerUsageCatalogRow is one handler whose printed usage names a public
// route. route is the catalog path the printed line must match; print writes
// the handler's usage exactly as the handler does on a rejected call.
type handlerUsageCatalogRow struct {
	route string
	print func(io.Writer)
}

// handlerUsageCatalogRows are the handlers whose usage line once drifted from
// the catalog synopsis (`--ui=popup|sidebar`, `hook edit <event> [flags]`,
// the whole `ai` listing for `config edit`).
var handlerUsageCatalogRows = []handlerUsageCatalogRow{
	{route: "hook edit", print: printHookUsage},
	{route: "runtime sessions", print: printSessionsUsage},
	{route: "runtime diagnostics", print: printRuntimeDiagnosticsUsage},
	{route: "switch", print: printSwitchUsage},
	{route: "config edit", print: printConfigEditUsage},
}

// handlerUsageCatalogSynopsis returns the catalog Usage lines of route.
func handlerUsageCatalogSynopsis(t *testing.T, route string) []string {
	t.Helper()
	tokens := strings.Fields(route)
	path, resolved, ok := cli.Resolve(tokens)
	if !ok || !slices.Equal(path, tokens) {
		t.Fatalf("catalog does not resolve route %q (got path %v)", route, path)
	}
	if len(resolved.Usage) == 0 {
		t.Fatalf("catalog route %q declares no Usage", route)
	}
	return resolved.Usage
}

// handlerUsageRouteLines returns the trimmed printed lines that spell route
// itself: `projmux <route>` followed by nothing or by a flag/operand slot. A
// line that continues with a child verb (`projmux switch toggle-tag`) names a
// different route and is left to that route.
func handlerUsageRouteLines(route, printed string) []string {
	prefix := "projmux " + route
	var lines []string
	for line := range strings.SplitSeq(printed, "\n") {
		line = strings.TrimSpace(line)
		if line != prefix && !strings.HasPrefix(line, prefix+" ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if rest == "" || strings.ContainsRune("[<-{", rune(rest[0])) {
			lines = append(lines, line)
		}
	}
	return lines
}

// handlerUsageCatalogProblems compares a handler's printed usage for route
// with the catalog synopsis. Every problem names the route.
func handlerUsageCatalogProblems(route, printed string, synopsis []string) []string {
	lines := handlerUsageRouteLines(route, printed)
	if len(lines) == 0 {
		return []string{route + ": handler usage prints no `projmux " + route + "` line; want one of " + strings.Join(synopsis, " | ")}
	}
	var problems []string
	for _, line := range lines {
		if !slices.Contains(synopsis, line) {
			problems = append(problems, route+": handler usage line "+`"`+line+`"`+" is not a catalog synopsis; want one of "+`"`+strings.Join(synopsis, `" | "`)+`"`)
		}
	}
	return problems
}

// handlerUsageForeignLines returns the printed `projmux …` lines that do not
// resolve to route in the catalog: a usage for one route must not list others.
func handlerUsageForeignLines(route, printed string) []string {
	want := strings.Fields(route)
	var foreign []string
	for line := range strings.SplitSeq(printed, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "projmux" {
			continue
		}
		path, _, ok := cli.Resolve(fields[1:])
		if !ok || !slices.Equal(path, want) {
			foreign = append(foreign, strings.TrimSpace(line))
		}
	}
	return foreign
}

// hookEditHintPattern extracts the command a hook edit refusal tells the user
// to run.
var hookEditHintPattern = regexp.MustCompile(`'(projmux hook edit [^']*)'`)

// hookEditHintProblem extracts the `projmux hook edit …` hint from errText
// and feeds its argv, typed literally, to run (the hook handler). It reports a
// problem naming the route when the hint is missing or the handler rejects it
// as a usage error (flag parsing or the one-<event> check).
func hookEditHintProblem(errText string, run func(args []string) error) string {
	match := hookEditHintPattern.FindStringSubmatch(errText)
	if match == nil {
		return "hook edit: refusal " + `"` + errText + `"` + " names no 'projmux hook edit …' command"
	}
	argv := strings.Fields(match[1])
	if err := run(argv[2:]); err != nil && IsUsageError(err) {
		return "hook edit: hint " + `"` + match[1] + `"` + " is rejected when typed literally: " + err.Error()
	}
	return ""
}

// newHookEditHintFixture builds a hook command whose post-create hook is
// defined only in the global config, inside an isolated temp HOME/XDG.
func newHookEditHintFixture(t *testing.T) (*hookCommand, string) {
	t.Helper()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	cmd, globalPath, _ := newHookTestCommand(t, home, project, "echo hint-override\n")
	writeHookFile(t, globalPath, `
[hooks.post-create]
run = "echo global-post-create"
`)
	return cmd, project
}

// TestHandlerUsageMatchesCatalogSynopsis pins each handler-printed usage line
// to its route's catalog synopsis, so the line a user copies parses.
func TestHandlerUsageMatchesCatalogSynopsis(t *testing.T) {
	t.Parallel()
	for _, row := range handlerUsageCatalogRows {
		var buf bytes.Buffer
		row.print(&buf)
		for _, problem := range handlerUsageCatalogProblems(row.route, buf.String(), handlerUsageCatalogSynopsis(t, row.route)) {
			t.Error(problem)
		}
	}
}

// TestHookEditGlobalRefusalHintParses drives the refusal for a globally
// defined hook, then types its `run '…'` hint back into the handler: the hint
// must pass flag parsing and the one-<event> check and reach the edit path.
func TestHookEditGlobalRefusalHintParses(t *testing.T) {
	t.Parallel()
	cmd, project := newHookEditHintFixture(t)

	var stdout, stderr bytes.Buffer
	refusal := cmd.Run([]string{"edit", "post-create"}, &stdout, &stderr)
	if refusal == nil || IsUsageError(refusal) {
		t.Fatalf("hook edit post-create err = %v, want the global-source refusal", refusal)
	}

	var runErr error
	problem := hookEditHintProblem(refusal.Error(), func(args []string) error {
		runErr = cmd.Run(args, &stdout, &stderr)
		return runErr
	})
	if problem != "" {
		t.Fatal(problem)
	}
	if !strings.Contains(refusal.Error(), "'projmux hook edit --project post-create'") {
		t.Errorf("refusal = %q, want the hint 'projmux hook edit --project post-create'", refusal.Error())
	}
	if runErr != nil {
		t.Fatalf("hint run err = %v (stderr=%q), want the project override written", runErr, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".projmux", "config.toml")); err != nil {
		t.Fatalf("hint did not reach the edit path: project config stat err = %v", err)
	}
}

// TestConfigEditOperandPrintsOnlyItsUsage pins `config edit <operand>`: still a
// usage error (exit 2), a reason that names `config edit`, and a usage block
// that lists `config edit` alone instead of the retired `ai` route listing.
func TestConfigEditOperandPrintsOnlyItsUsage(t *testing.T) {
	t.Parallel()
	fixture := newConfigForwarderFixture(t)
	var stdout, stderr bytes.Buffer
	err := fixture.app.Run([]string{"config", "edit", "x"}, &stdout, &stderr)
	if err == nil || !IsUsageError(err) {
		t.Fatalf("config edit x err = %v, want a usage error", err)
	}
	if got, want := err.Error(), "config edit does not accept positional arguments"; got != want {
		t.Errorf("config edit x reason = %q, want %q", got, want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("config edit x wrote stdout %q, want none", stdout.String())
	}
	printed := stderr.String()
	for _, problem := range handlerUsageCatalogProblems("config edit", printed, handlerUsageCatalogSynopsis(t, "config edit")) {
		t.Error(problem)
	}
	if foreign := handlerUsageForeignLines("config edit", printed); len(foreign) != 0 {
		t.Errorf("config edit x usage lists other routes: %q", foreign)
	}
	for _, banned := range []string{"create agent", "internal agent-hook", "agent topic", "ai settings"} {
		if strings.Contains(printed, banned) || strings.Contains(err.Error(), banned) {
			t.Errorf("config edit x output mentions %q: reason=%q stderr=%q", banned, err.Error(), printed)
		}
	}
}

// TestHandlerUsageCatalogGuardDetectsDrift is the negative control: the
// checkers must name the route when fed the strings this guard replaced.
func TestHandlerUsageCatalogGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ route, printed string }{
		{"runtime sessions", "Usage:\n  projmux runtime sessions [--ui=popup|sidebar]\n"},
		{"runtime diagnostics", "Usage:\n  projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]\n"},
		{"switch", "Usage:\n  projmux switch [--ui=popup|sidebar]\n  projmux switch toggle-tag [path]\n"},
		{"hook edit", "Usage:\n  projmux hook edit <event> [--global|--project] [--editor]\n"},
		{"config edit", "Usage:\n  projmux create agent --provider <p>\n"},
	} {
		problems := handlerUsageCatalogProblems(test.route, test.printed, handlerUsageCatalogSynopsis(t, test.route))
		if len(problems) == 0 {
			t.Errorf("%s: checker accepted drifted usage %q", test.route, test.printed)
		}
		for _, problem := range problems {
			if !strings.HasPrefix(problem, test.route+": ") {
				t.Errorf("problem %q does not name route %q", problem, test.route)
			}
		}
	}

	var aiUsage bytes.Buffer
	printAIUsage(&aiUsage)
	if foreign := handlerUsageForeignLines("config edit", aiUsage.String()); len(foreign) == 0 {
		t.Error("config edit: foreign-line check accepted the full ai usage listing")
	}

	cmd, _ := newHookEditHintFixture(t)
	var stdout, stderr bytes.Buffer
	old := "hook \"post-create\" is defined at /x/config.toml; edit that file directly or run 'projmux hook edit post-create --project' to create a project override"
	problem := hookEditHintProblem(old, func(args []string) error { return cmd.Run(args, &stdout, &stderr) })
	if !strings.HasPrefix(problem, "hook edit: ") || !strings.Contains(problem, "requires exactly one <event> argument") {
		t.Errorf("old hook edit hint problem = %q, want a hook edit rejection naming the one-<event> check", problem)
	}
	if problem := hookEditHintProblem("no hint here", func([]string) error { return errors.New("unreachable") }); !strings.HasPrefix(problem, "hook edit: ") {
		t.Errorf("missing hint problem = %q, want it to name hook edit", problem)
	}
}
