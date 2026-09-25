package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// forwardedRouteUsageRow drives one public route that forwards into another
// handler through forwardRawArgv. argv makes the reached leaf reject the call
// before it touches tmux, the Registry, or a picker.
type forwardedRouteUsageRow struct {
	route string
	argv  []string
}

// forwardedRouteUsageFlag is an unknown flag no parser defines.
const forwardedRouteUsageFlag = "--zz-forward-guard"

// forwardedRouteUsageRows holds one row per public forwardRawArgv label. The
// set is closed against the labels in the source. `attach project` forwards
// into `switch open` only after its own parser accepted exactly one operand,
// so the shared switch leaf never rejects a call that reached it through
// `attach project`; the row pins the attach parser that does.
var forwardedRouteUsageRows = []forwardedRouteUsageRow{
	{route: "agent integrate", argv: []string{"agent", "integrate", "codex", forwardedRouteUsageFlag}},
	{route: "agent usage", argv: []string{"agent", "usage", forwardedRouteUsageFlag}},
	{route: "attach project", argv: []string{"attach", "project", forwardedRouteUsageFlag}},
	{route: "config apply", argv: []string{"config", "apply", forwardedRouteUsageFlag}},
	{route: "config edit", argv: []string{"config", "edit", forwardedRouteUsageFlag}},
	{route: "config render app", argv: []string{"config", "render", "app", forwardedRouteUsageFlag}},
	{route: "config render standalone", argv: []string{"config", "render", "standalone", forwardedRouteUsageFlag}},
	{route: "create notification", argv: []string{"create", "notification", forwardedRouteUsageFlag}},
	{route: "delete notification", argv: []string{"delete", "notification", forwardedRouteUsageFlag}},
	{route: "diagnostics agent-hook", argv: []string{"diagnostics", "agent-hook", forwardedRouteUsageFlag}},
	{route: "get notifications", argv: []string{"get", "notifications", forwardedRouteUsageFlag}},
	{route: "notification ack", argv: []string{"notification", "ack", forwardedRouteUsageFlag}},
	{route: "notification reconcile", argv: []string{"notification", "reconcile", forwardedRouteUsageFlag}},
	{route: "runtime attach", argv: []string{"runtime", "attach", forwardedRouteUsageFlag}},
	{route: "runtime diagnostics", argv: []string{"runtime", "diagnostics", forwardedRouteUsageFlag}},
	{route: "runtime prune", argv: []string{"runtime", "prune", forwardedRouteUsageFlag}},
	{route: "runtime sessions", argv: []string{"runtime", "sessions", forwardedRouteUsageFlag}},
	{route: "runtime stop", argv: []string{"runtime", "stop", forwardedRouteUsageFlag}},
	{route: "runtime tag", argv: []string{"runtime", "tag", forwardedRouteUsageFlag}},
}

// forwardedRouteUsageHeader matches the `Usage of <name>:` header a FlagSet
// without a Usage override prints.
var forwardedRouteUsageHeader = regexp.MustCompile(`(?m)^Usage of (.+):$`)

// forwardedRouteUsageProblems checks what a rejected call on route printed:
// its own FlagSet header or its own catalog usage block, and no header or
// `projmux …` line of another route. Every problem names the route.
func forwardedRouteUsageProblems(route, stderr string) []string {
	var problems []string
	var block bytes.Buffer
	cli.WriteRouteUsage(&block, route)
	own := strings.Contains(stderr, "Usage of "+route+":\n") || (block.Len() > 0 && strings.Contains(stderr, block.String()))
	if !own {
		problems = append(problems, route+": printed neither `Usage of "+route+":` nor its catalog usage block: "+strconv.Quote(stderr))
	}
	for _, match := range forwardedRouteUsageHeader.FindAllStringSubmatch(stderr, -1) {
		if match[1] != route {
			problems = append(problems, route+": printed the FlagSet header of "+strconv.Quote(match[1]))
		}
	}
	want := strings.Fields(route)
	for line := range strings.SplitSeq(stderr, "\n") {
		fields := strings.Fields(line)
		// Flag help such as "projmux binary path ..." names no route.
		if len(fields) < 2 || fields[0] != "projmux" {
			continue
		}
		if _, ok := cli.LookupRoute(fields[1]); !ok {
			continue
		}
		if path, _, _ := cli.Resolve(fields[1:]); !slices.Equal(path, want) {
			problems = append(problems, route+": printed a usage line of another route: "+strconv.Quote(strings.TrimSpace(line)))
		}
	}
	return problems
}

// scanForwardedRouteLabels returns the public routes forwardRawArgv names in
// the package source: literal labels the catalog resolves exactly, outside
// the hidden internal namespace.
func scanForwardedRouteLabels(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != "forwardRawArgv" {
				return true
			}
			label := ""
			switch arg := call.Args[1].(type) {
			case *ast.BasicLit:
				label, _ = strconv.Unquote(arg.Value)
			case *ast.Ident:
				// attach project forwards under its local const spelling.
				label = forwardedRouteConst(file, arg.Name)
			}
			if label == "" {
				t.Errorf("%s: forwardRawArgv label is not a literal or a local constant", fset.Position(call.Pos()))
				return true
			}
			tokens := strings.Fields(label)
			if path, _, ok := cli.Resolve(tokens); ok && slices.Equal(path, tokens) && tokens[0] != "internal" {
				seen[label] = true
			}
			return true
		})
	}
	labels := make([]string, 0, len(seen))
	for label := range seen {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// forwardedRouteConst returns the string value of a constant named name
// declared anywhere in file, or "".
func forwardedRouteConst(file *ast.File, name string) string {
	value := ""
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, id := range spec.Names {
			if id.Name == name && i < len(spec.Values) {
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, _ = strconv.Unquote(lit.Value)
				}
			}
		}
		return value == ""
	})
	return value
}

// forwardedRouteSetProblems compares the source labels with the table rows,
// both ways.
func forwardedRouteSetProblems(labels []string, rows []forwardedRouteUsageRow) []string {
	var problems []string
	driven := map[string]bool{}
	for _, row := range rows {
		driven[row.route] = true
		if !slices.Contains(labels, row.route) {
			problems = append(problems, row.route+": forwardedRouteUsageRows drives a route no forwardRawArgv call names")
		}
	}
	for _, label := range labels {
		if !driven[label] {
			problems = append(problems, label+": forwardRawArgv forwards this public route but forwardedRouteUsageRows has no row for it")
		}
	}
	sort.Strings(problems)
	return problems
}

// TestForwardedRoutesPrintTheirOwnUsage drives every public route that
// forwards into a shared handler with an argv the reached leaf rejects, and
// pins the FlagSet header and usage block to that route: a leaf reached by
// several routes (`notification ack` and `delete notification`, `config
// apply` and `internal tmux apply`) must name whichever route reached it.
func TestForwardedRoutesPrintTheirOwnUsage(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	for _, problem := range forwardedRouteSetProblems(scanForwardedRouteLabels(t), forwardedRouteUsageRows) {
		t.Error(problem)
	}
	for _, row := range forwardedRouteUsageRows {
		t.Run(row.route, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := New().Run(row.argv, &stdout, &stderr)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("%s: err = %v, want a usage error (stderr=%q)", row.route, err, stderr.String())
			}
			for _, problem := range forwardedRouteUsageProblems(row.route, stderr.String()) {
				t.Error(problem)
			}
		})
	}
}

// TestDeleteNotificationPrintsItsOwnUsage pins the ack leaf `delete
// notification` shares with `notification ack`: a flag error and a missing
// operand both name `delete notification`, never `notification ack`.
func TestDeleteNotificationPrintsItsOwnUsage(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)
	var block bytes.Buffer
	cli.WriteRouteUsage(&block, "delete notification")
	for _, argv := range [][]string{{"delete", "notification", "--zz"}, {"delete", "notification"}} {
		var stdout, stderr bytes.Buffer
		err := New().Run(argv, &stdout, &stderr)
		if err == nil || !IsUsageError(err) {
			t.Fatalf("%v: err = %v, want a usage error", argv, err)
		}
		printed := stderr.String()
		if len(argv) == 3 && !strings.Contains(printed, "Usage of delete notification:\n") {
			t.Errorf("%v: stderr = %q, want `Usage of delete notification:`", argv, printed)
		}
		if len(argv) == 2 && !strings.Contains(printed, block.String()) {
			t.Errorf("%v: stderr = %q, want the delete notification block %q", argv, printed, block.String())
		}
		if n := strings.Count(printed, "notification ack"); n != 0 {
			t.Errorf("%v: stderr names `notification ack` %d times, want 0: %q", argv, n, printed)
		}
	}
}

// TestForwardedRouteUsageGuardDetectsDrift is the negative control: the shared
// ack leaf printing its sibling's header or block for `delete notification`,
// and a label without a row or a row without a label, are each reported by
// route.
func TestForwardedRouteUsageGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	var ackBlock bytes.Buffer
	cli.WriteRouteUsage(&ackBlock, "notification ack")
	for _, printed := range []string{
		"flag provided but not defined: -zz\nUsage of notification ack:\n  -all\n    \tremove every queued entry\n",
		ackBlock.String(),
	} {
		problems := forwardedRouteUsageProblems("delete notification", printed)
		if len(problems) == 0 {
			t.Errorf("checker accepted %q for delete notification", printed)
		}
		for _, problem := range problems {
			if !strings.HasPrefix(problem, "delete notification: ") {
				t.Errorf("problem %q does not name the reaching route", problem)
			}
		}
	}
	headerProblems := strings.Join(forwardedRouteUsageProblems("delete notification", "Usage of notification ack:\n"), "\n")
	if !strings.Contains(headerProblems, `printed the FlagSet header of "notification ack"`) {
		t.Errorf("header drift problems = %q, want the sibling header named", headerProblems)
	}
	set := forwardedRouteSetProblems([]string{"delete notification", "notification ack"}, []forwardedRouteUsageRow{{route: "notification ack"}, {route: "notify ack"}})
	for _, want := range []string{
		"delete notification: forwardRawArgv forwards this public route but forwardedRouteUsageRows has no row for it",
		"notify ack: forwardedRouteUsageRows drives a route no forwardRawArgv call names",
	} {
		if !slices.Contains(set, want) {
			t.Errorf("set problems = %q, want %q", set, want)
		}
	}
}
