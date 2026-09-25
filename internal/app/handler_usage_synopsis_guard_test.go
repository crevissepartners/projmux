package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// handlerSynopsisHeader matches the `Usage:` header line of a usage block.
var handlerSynopsisHeader = regexp.MustCompile(`^\s*Usage:`)

// handlerSynopsisLine matches a `projmux …` line printed as a synopsis: after
// the two-space usage indent, or after a `Word:` label such as `usage:` or a
// localized `%s:`. The captured token is the first route word; a literal that
// ends right after `projmux ` is the head of a concatenated synopsis.
var handlerSynopsisLine = regexp.MustCompile(`^(?:\s{2,}|\s*[^\s:]+:\s*)projmux(?:\s+(\S+)|\s*$)`)

// handlerSynopsisException is one literal line that may keep a hand-written
// synopsis shape. Every row needs a reason, and a row no literal matches is
// stale.
type handlerSynopsisException struct {
	reason string
}

// handlerSynopsisExceptions is keyed by "<repo-relative file>: <line>", the
// line being one line of the literal's value. It is empty: every handler
// usage renders the catalog through printRouteUsage (or cli.WriteRouteUsage
// outside package app). A parent verb menu that cannot render from the
// catalog would be the one reason to add a row ("parent verb menu —
// parent_usage_guard_test.go").
var handlerSynopsisExceptions = map[string]handlerSynopsisException{}

// handlerRouteUsagePrinters are the calls that render a catalog route's Usage.
// Each call must name its route as a string literal that resolves exactly.
var handlerRouteUsagePrinters = []string{"printRouteUsage", "cli.WriteRouteUsage"}

// handlerRouteUsageCallFloor is the number of route usage calls in
// internal/app/** when every handler printer moved onto the catalog and the
// `help` verbs moved to printRouteHelp (handler_help_verb_guard_test.go). The
// set may grow; a drop means a handler went back to printing its own text.
const handlerRouteUsageCallFloor = 217

// handlerSynopsisFile is one non-test source file of internal/app/**.
type handlerSynopsisFile struct {
	name string // repo-relative
	src  []byte
}

// handlerSynopsisFinding is one hand-written synopsis literal line.
type handlerSynopsisFinding struct {
	pos, line, kind string
}

func (f handlerSynopsisFinding) key() string {
	file, _, _ := strings.Cut(f.pos, ":")
	return file + ": " + f.line
}

// handlerSynopsisScan is what the scan found in a set of files.
type handlerSynopsisScan struct {
	findings []handlerSynopsisFinding
	// calls counts the route usage calls; problems lists every call whose
	// route is not a literal the catalog resolves exactly.
	calls    int
	problems []string
}

// loadHandlerSynopsisFiles reads every non-test Go file under internal/app/**.
func loadHandlerSynopsisFiles(t *testing.T, repoRoot string) []handlerSynopsisFile {
	t.Helper()
	var files []handlerSynopsisFile
	root := filepath.Join(repoRoot, "internal", "app")
	err := filepath.WalkDir(root, func(path string, entry iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
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
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, handlerSynopsisFile{name: filepath.ToSlash(rel), src: src})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

// handlerSynopsisLiteralKind classifies one line of a string literal: "usage
// header", "synopsis line", or "" when it is not a hand-written synopsis.
func handlerSynopsisLiteralKind(line string) string {
	if handlerSynopsisHeader.MatchString(line) {
		return "usage header"
	}
	match := handlerSynopsisLine.FindStringSubmatch(line)
	if match == nil {
		return ""
	}
	if match[1] == "" {
		return "synopsis line"
	}
	if _, ok := cli.LookupRoute(match[1]); ok {
		return "synopsis line"
	}
	return ""
}

// scanHandlerSynopsis parses files and returns their hand-written synopsis
// literal lines and the route usage calls.
func scanHandlerSynopsis(files []handlerSynopsisFile) handlerSynopsisScan {
	var scan handlerSynopsisScan
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f.name, f.src, parser.SkipObjectResolution)
		if err != nil {
			scan.problems = append(scan.problems, "parse "+f.name+": "+err.Error())
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				// The helper itself forwards its route parameter.
				return node.Recv != nil || node.Name.Name != handlerRouteUsagePrinters[0]
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(node.Value)
				if err != nil {
					return true
				}
				for line := range strings.SplitSeq(value, "\n") {
					if kind := handlerSynopsisLiteralKind(line); kind != "" {
						scan.findings = append(scan.findings, handlerSynopsisFinding{pos: fset.Position(node.Pos()).String(), line: line, kind: kind})
					}
				}
			case *ast.CallExpr:
				name := types.ExprString(node.Fun)
				if !slices.Contains(handlerRouteUsagePrinters, name) {
					return true
				}
				scan.calls++
				pos := fset.Position(node.Pos()).String()
				if len(node.Args) != 2 {
					scan.problems = append(scan.problems, pos+": "+name+" call takes "+strconv.Itoa(len(node.Args))+" arguments, want (w, route)")
					return true
				}
				lit, ok := node.Args[1].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					scan.problems = append(scan.problems, pos+": "+name+" route "+types.ExprString(node.Args[1])+" is not a string literal")
					return true
				}
				route, _ := strconv.Unquote(lit.Value)
				tokens := strings.Fields(route)
				if path, _, ok := cli.Resolve(tokens); !ok || !slices.Equal(path, tokens) {
					scan.problems = append(scan.problems, pos+": "+name+" names "+strconv.Quote(route)+", which the catalog does not resolve exactly (got "+strings.Join(path, " ")+")")
				}
			}
			return true
		})
	}
	return scan
}

// handlerSynopsisFailures applies the policy: every finding is a failure
// unless an exception row with a reason covers it, and every row must cover
// one. It returns the failures, sorted.
func handlerSynopsisFailures(scan handlerSynopsisScan, exceptions map[string]handlerSynopsisException) []string {
	failures := slices.Clone(scan.problems)
	used := map[string]bool{}
	for _, finding := range scan.findings {
		if _, ok := exceptions[finding.key()]; ok {
			used[finding.key()] = true
			continue
		}
		failures = append(failures, finding.pos+": string literal line "+strconv.Quote(finding.line)+" is a hand-written "+finding.kind+"; print the reached route's catalog Usage with printRouteUsage")
	}
	for key, row := range exceptions {
		if strings.TrimSpace(row.reason) == "" {
			failures = append(failures, "exception "+strconv.Quote(key)+": missing reason")
		}
		if !used[key] {
			failures = append(failures, "exception "+strconv.Quote(key)+" is stale: no literal line matches it")
		}
	}
	sort.Strings(failures)
	return failures
}

// TestHandlerUsageHasNoHandWrittenSynopsis keeps internal/app/** free of
// hand-written `Usage:` blocks and `projmux …` synopsis lines: a handler
// prints the reached route's catalog Usage through printRouteUsage, whose
// route is a literal the catalog resolves exactly.
func TestHandlerUsageHasNoHandWrittenSynopsis(t *testing.T) {
	t.Parallel()
	files := loadHandlerSynopsisFiles(t, filepath.Join("..", ".."))
	if len(files) < 250 {
		t.Fatalf("scanned %d files under internal/app, want at least 250; the file walk has regressed", len(files))
	}
	scan := scanHandlerSynopsis(files)
	for _, failure := range handlerSynopsisFailures(scan, handlerSynopsisExceptions) {
		t.Error(failure)
	}
	if scan.calls < handlerRouteUsageCallFloor {
		t.Errorf("found %d route usage calls, want at least %d", scan.calls, handlerRouteUsageCallFloor)
	}
}

// TestHandlerUsageSynopsisGuardDetectsDrift is the negative control: a new
// usage header, a new indented synopsis line, a concatenated synopsis head, a
// non-literal or unresolvable route, a row without a reason, and a stale row
// are each reported, with file and line.
func TestHandlerUsageSynopsisGuardDetectsDrift(t *testing.T) {
	t.Parallel()
	const src = `package app

import (
	"fmt"
	"io"
)

func printDriftUsage(w io.Writer, noun string) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux hook edit <event>")
	fmt.Fprintln(w, "  projmux "+noun+" list")
	fmt.Fprintln(w, "usage: projmux reconcile registry [--dry-run]")
	fmt.Fprintln(w, "%s: projmux does not apply a model")
	fmt.Fprintln(w, "projmux hook: config parse error")
	printRouteUsage(w, noun)
	printRouteUsage(w, "hook bogus")
	printRouteUsage(w, "hook edit")
}
`
	scan := scanHandlerSynopsis([]handlerSynopsisFile{{name: "synthetic/drift.go", src: []byte(src)}})
	failures := handlerSynopsisFailures(scan, map[string]handlerSynopsisException{
		"synthetic/drift.go:   projmux hook list": {reason: "no such literal"},
		"synthetic/drift.go: Usage:":              {},
	})
	joined := strings.Join(failures, "\n")
	for _, want := range []string{
		`synthetic/drift.go:10:18: string literal line "  projmux hook edit <event>" is a hand-written synopsis line`,
		`synthetic/drift.go:11:18: string literal line "  projmux " is a hand-written synopsis line`,
		`synthetic/drift.go:12:18: string literal line "usage: projmux reconcile registry [--dry-run]" is a hand-written synopsis line`,
		`synthetic/drift.go:15:2: printRouteUsage route noun is not a string literal`,
		`synthetic/drift.go:16:2: printRouteUsage names "hook bogus", which the catalog does not resolve exactly (got hook)`,
		`exception "synthetic/drift.go: Usage:": missing reason`,
		`exception "synthetic/drift.go:   projmux hook list" is stale: no literal line matches it`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("negative control: missing failure %q in:\n%s", want, joined)
		}
	}
	for _, clean := range []string{"does not apply a model", "config parse error", `"hook edit"`} {
		if strings.Contains(joined, clean) {
			t.Errorf("negative control: %q must not be reported:\n%s", clean, joined)
		}
	}
	if len(failures) != 7 {
		t.Errorf("negative control: got %d failures, want 7:\n%s", len(failures), joined)
	}
	if scan.calls != 3 {
		t.Errorf("negative control: counted %d route usage calls, want 3", scan.calls)
	}
}
