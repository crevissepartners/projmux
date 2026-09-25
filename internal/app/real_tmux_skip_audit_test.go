package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// realTmuxSkipAudit is what one pass of the source audit saw: every
// exec.LookPath("tmux") call, the function each one sits in, and the places
// where a missing tmux leads to a skip outside requireRealTmux.
type realTmuxSkipAudit struct {
	lookups    int
	lookupFunc map[string]int
	violations []string
}

// auditRealTmuxSkips flags a tmux lookup whose missing-tmux branch skips: an if
// statement with exec.LookPath("tmux") in its init or condition, or an if
// statement right after an assignment from it, whose body calls Skip, Skipf,
// or SkipNow. A failing branch is allowed; only requireRealTmux may skip.
func auditRealTmuxSkips(fset *token.FileSet, file *ast.File, audit *realTmuxSkipAudit) {
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && isTmuxLookPath(call) {
				audit.lookups++
				audit.lookupFunc[fn.Name.Name]++
			}
			if fn.Name.Name == "requireRealTmux" {
				return true
			}
			var statements []ast.Stmt
			switch block := node.(type) {
			case *ast.BlockStmt:
				statements = block.List
			case *ast.CaseClause:
				statements = block.Body
			case *ast.CommClause:
				statements = block.Body
			default:
				return true
			}
			for i, statement := range statements {
				branch, ok := statement.(*ast.IfStmt)
				if !ok {
					continue
				}
				looked := containsTmuxLookPath(branch.Init) || containsTmuxLookPath(branch.Cond)
				if i > 0 {
					if assign, ok := statements[i-1].(*ast.AssignStmt); ok && containsTmuxLookPath(assign) {
						looked = true
					}
				}
				if looked && callsSkip(branch.Body) {
					audit.violations = append(audit.violations, fmt.Sprintf("%s: %s skips on a missing tmux; call requireRealTmux", fset.Position(branch.Pos()), fn.Name.Name))
				}
			}
			return true
		})
	}
}

func isTmuxLookPath(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "LookPath" || len(call.Args) != 1 {
		return false
	}
	if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != "exec" {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == "tmux"
}

func containsTmuxLookPath(node ast.Node) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(child ast.Node) bool {
		if call, ok := child.(*ast.CallExpr); ok && isTmuxLookPath(call) {
			found = true
		}
		return !found
	})
	return found
}

func callsSkip(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch selector.Sel.Name {
			case "Skip", "Skipf", "SkipNow":
				found = true
			}
		}
		return !found
	})
	return found
}

func auditRealTmuxSkipSource(t *testing.T, source string) realTmuxSkipAudit {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "control.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	audit := realTmuxSkipAudit{lookupFunc: map[string]int{}}
	auditRealTmuxSkips(fset, file, &audit)
	return audit
}

// TestRealTmuxSkipIsOwnedByOneHelper keeps every real-tmux test in this package
// on requireRealTmux, so PROJMUX_REAL_TMUX_STRICT=1 reaches all of them: a
// test that skips on its own lookup would still pass silently without tmux.
func TestRealTmuxSkipIsOwnedByOneHelper(t *testing.T) {
	t.Parallel()

	// The detector must see both skip shapes and leave a failing one alone.
	for name, source := range map[string]string{
		"if init": `package app
func TestControl(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("x")
	}
}`,
		"assignment then if": `package app
func controlHelper(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("no tmux: %v", err)
	}
	_ = binary
}`,
	} {
		audit := auditRealTmuxSkipSource(t, source)
		if len(audit.violations) != 1 || !strings.HasPrefix(audit.violations[0], "control.go:") {
			t.Fatalf("%s: violations=%q, want exactly one in control.go", name, audit.violations)
		}
	}
	negative := auditRealTmuxSkipSource(t, `package app
func controlFatal(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("tmux is required: %v", err)
	}
	_ = binary
}`)
	if len(negative.violations) != 0 || negative.lookups != 2 {
		t.Fatalf("fatal shape: violations=%q lookups=%d, want none and 2", negative.violations, negative.lookups)
	}

	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	audit := realTmuxSkipAudit{lookupFunc: map[string]int{}}
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		auditRealTmuxSkips(fset, file, &audit)
	}
	for _, violation := range audit.violations {
		t.Error(violation)
	}
	// A scan that parsed nothing would also report nothing. The helper and the
	// tests that fail on a missing tmux prove the lookups were actually seen.
	if len(paths) == 0 || audit.lookups < 4 || audit.lookupFunc["requireRealTmux"] != 1 {
		t.Fatalf("scanned %d files and %d tmux lookups (requireRealTmux: %d); the audit did not see the package", len(paths), audit.lookups, audit.lookupFunc["requireRealTmux"])
	}
}
