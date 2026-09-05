package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A discarded write error is the strongest form of a surface lying.
//
// Everything else this track chased at least reported something. `disconnected`
// meant nothing was recorded, `no matching pane` named the wrong step — both
// wrong, both visible. A reflection path that runs a tmux write and throws the
// error away reports **success**: the ingest log records `result:"state"` for an
// event whose Pane never moved, and no diagnosis built on that log can see it,
// because the log is the thing that is lying.
//
// It is why one hook event appeared to work and another appeared to fail. They
// failed alike; only one of them said so.

// aiBestEffortWriteMarker admits one discarded write.
//
// It carries the same rule as the uncaptured-reason sweep: an exception is
// admitted by a stated reason, never by a count. That distinction is the whole
// difference between this gate and a baseline. A baseline says "there are N
// today, so N passes", which defers on volume and re-freezes the current state
// as the passing condition — the exact shape of the E2E assertion that made a
// silent control plane the condition for green. A marker says "this one write
// is a different kind of thing, and here is why", which is a judgment someone
// made and a reviewer can dispute. New writes with no reason stay red.
const aiBestEffortWriteMarker = "best-effort-write:"

// TestHookReflectionWritesNeverDiscardTheirErrorSilently holds the gate, and it
// owns the whole package.
//
// The scan reads every file in the package rather than a named list, so a
// discarded write appearing somewhere new is caught rather than skipped for
// being somewhere nobody thought to look. That breadth is the point, and it is
// worth stating because a narrower audit lives alongside it: the phase that
// repaired the reflection writes checks only the files it touched. Its green is
// evidence about those files, never about the package. This test is where the
// package-wide claim is made.
//
// It reads the syntax tree rather than the text. A textual scan counts the
// pattern wherever it appears, including inside the comment that explains why a
// helper replaced it — so the gate would redden on the very sentence describing
// its own fix. A gate that cannot tell code from prose about code is a gate
// people route around.
func TestHookReflectionWritesNeverDiscardTheirErrorSilently(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var unjustified []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		payload, readErr := os.ReadFile(path) // #nosec G304 -- package source under test.
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		lines := strings.Split(string(payload), "\n")
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, payload, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, position := range discardedTmuxWrites(fileSet, parsed) {
			if bestEffortWriteJustified(lines, position.Line-1) {
				continue
			}
			unjustified = append(unjustified, name+":"+strconv.Itoa(position.Line))
		}
	}
	sort.Strings(unjustified)
	if len(unjustified) > 0 {
		t.Fatalf("%d tmux write(s) throw their error away with no reason given:\n  %s\n\n"+
			"Each one lets a reflection path report success for a write that never landed, which no diagnosis "+
			"built on the ingest log can detect. Check the error and report a bounded reason, or state on the "+
			"line above why this write is best-effort with a `%s <reason>` comment.",
			len(unjustified), strings.Join(unjustified, "\n  "), aiBestEffortWriteMarker)
	}
}

// discardedTmuxWrites finds every tmux invocation in one file whose error the
// call site throws away.
//
// Both spellings count, and the second is prevention rather than discovery.
// Every discarded write in the tree today is an assignment to blank
// identifiers; a bare call statement, which discards the error without even
// naming it, currently appears nowhere. It is covered so that the obvious way
// to quiet this gate -- deleting the assignment rather than checking the error
// -- does not work.
func discardedTmuxWrites(fileSet *token.FileSet, file *ast.File) []token.Position {
	var found []token.Position
	ast.Inspect(file, func(node ast.Node) bool {
		var call *ast.CallExpr
		switch statement := node.(type) {
		case *ast.AssignStmt:
			if statement.Tok != token.ASSIGN || len(statement.Rhs) != 1 {
				return true
			}
			for _, target := range statement.Lhs {
				ident, ok := target.(*ast.Ident)
				if !ok || ident.Name != "_" {
					return true
				}
			}
			candidate, ok := statement.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			call = candidate
		case *ast.ExprStmt:
			candidate, ok := statement.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			call = candidate
		default:
			return true
		}
		if isTmuxRunCall(call) {
			found = append(found, fileSet.Position(call.Pos()))
		}
		return true
	})
	return found
}

// isTmuxRunCall reports whether one call runs tmux through the command runner.
func isTmuxRunCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "run" || len(call.Args) == 0 {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == "tmux"
}

// bestEffortWriteJustified reports whether a discarded write carries its reason
// on its own line or in the comment block directly above it.
func bestEffortWriteJustified(lines []string, index int) bool {
	if index < 0 || index >= len(lines) {
		return false
	}
	if markerCarriesBestEffortReason(lines[index]) {
		return true
	}
	for above := index - 1; above >= 0 && strings.HasPrefix(strings.TrimSpace(lines[above]), "//"); above-- {
		if markerCarriesBestEffortReason(lines[above]) {
			return true
		}
	}
	return false
}

// markerCarriesBestEffortReason reports whether one line states a reason after
// the marker. An empty marker admits nothing.
func markerCarriesBestEffortReason(line string) bool {
	_, after, ok := strings.Cut(line, aiBestEffortWriteMarker)
	if !ok {
		return false
	}
	return strings.TrimSpace(after) != ""
}

// TestDiscardedWriteScanNeverCountsProseAboutCode is the gate checking itself.
//
// The first version of this scan matched text, and it reddened on the comment
// that explains why a checked helper replaced the discarded call — the sentence
// describing the fix counted as the defect. A gate that cannot tell code from
// prose about code gets routed around, and being routed around is how the
// defects this whole section exists to catch survived in the first place.
func TestDiscardedWriteScanNeverCountsProseAboutCode(t *testing.T) {
	const source = `package app

// recordAIPaneOption is the honest spelling of what ` + "`_ = c.run(\"tmux\", ...)`" + ` used
// to be: the failure is kept instead of dropped.
func sample(c *thing) {
	documentation := "_ = c.run(\"tmux\", \"set-option\")"
	_ = documentation
	_, _ = c.run("tmux", "select-pane")
	c.run("tmux", "display-message")
	if err := c.run("tmux", "set-option"); err != nil {
		return
	}
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "sample.go", source, 0)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	found := discardedTmuxWrites(fileSet, parsed)
	if len(found) != 2 {
		t.Fatalf("found %d discarded write(s), want exactly the assignment and the bare call\n%+v", len(found), found)
	}
	// A checked call is not a discarded one, and neither the comment nor the
	// string literal quoting the pattern may enter the count.
	for _, position := range found {
		if position.Line < 8 || position.Line > 9 {
			t.Fatalf("counted something at line %d that is not one of the two discarded calls", position.Line)
		}
	}
}
