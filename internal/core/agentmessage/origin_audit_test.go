package agentmessage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const agentMessageImportPath = "github.com/crevissepartners/projmux/internal/core/agentmessage"

// operatorOriginProducers lists the non-test files outside this package that
// may build an operator origin, keyed by repository-relative path. It is empty
// on purpose: no product path writes operator input yet. The web sender adds
// itself here when it lands.
var operatorOriginProducers = map[string]bool{}

// TestNoProductPathBuildsAnOperatorOrigin is the negative audit for operator
// input. Outside this package, a non-test Go file that calls OperatorWebOrigin
// or writes a non-empty Origin literal is a producer, and every producer must
// be listed in operatorOriginProducers.
func TestNoProductPathBuildsAnOperatorOrigin(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	self := filepath.Join("internal", "core", "agentmessage")
	scanned, definitionSeen := 0, false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			// Worktrees, VCS data, and vendored or generated trees are not this
			// checkout's product source.
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		if filepath.Dir(relative) == self {
			for _, declaration := range file.Decls {
				if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "OperatorWebOrigin" {
					definitionSeen = true
				}
			}
			return nil
		}
		local := ""
		for _, imported := range file.Imports {
			if value, _ := strconv.Unquote(imported.Path.Value); value == agentMessageImportPath {
				local = "agentmessage"
				if imported.Name != nil {
					local = imported.Name.Name
				}
			}
		}
		if local == "" {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var selector *ast.SelectorExpr
			switch node := node.(type) {
			case *ast.CallExpr:
				if candidate, ok := node.Fun.(*ast.SelectorExpr); ok && candidate.Sel.Name == "OperatorWebOrigin" {
					selector = candidate
				}
			case *ast.CompositeLit:
				// The empty literal is the Agent origin, used in comparisons.
				if candidate, ok := node.Type.(*ast.SelectorExpr); ok && candidate.Sel.Name == "Origin" && len(node.Elts) > 0 {
					selector = candidate
				}
			}
			if selector == nil {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == local && !operatorOriginProducers[filepath.ToSlash(relative)] {
				t.Errorf("%s builds an operator origin (%s.%s) but is not a listed producer", relative, local, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !definitionSeen || scanned < 100 {
		t.Fatalf("audit scanned %d files and saw the constructor definition: %t, so it proves nothing", scanned, definitionSeen)
	}
}
