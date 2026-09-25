package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestConfigHomeIsResolvedInOnePlace keeps every XDG_CONFIG_HOME read going
// through ResolveConfigHome, directly or through the Homes pass-through, so no
// site can resolve the config home by hand and drift from the others.
func TestConfigHomeIsResolvedInOnePlace(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	var files []*ast.File
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			files = append(files, file)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	for _, violation := range configHomeReadsOutsideResolver(fset, files) {
		t.Errorf("%s: XDG_CONFIG_HOME is read outside the resolver; pass it to config.ResolveConfigHome (or config.Homes{ConfigHome: ...})", violation)
	}
}

// configHomeReadsOutsideResolver returns the position of every call that takes
// the "XDG_CONFIG_HOME" string literal as an argument, unless that call is the
// ConfigHome value of a composite literal or a direct argument of
// ResolveConfigHome. Literals outside calls, such as env pass-through lists,
// are not reads and are ignored.
func configHomeReadsOutsideResolver(fset *token.FileSet, files []*ast.File) []token.Position {
	var violations []token.Position
	for _, file := range files {
		allowed := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "ConfigHome" {
					if call, ok := n.Value.(*ast.CallExpr); ok {
						allowed[call] = true
					}
				}
			case *ast.CallExpr:
				if isResolveConfigHome(n.Fun) {
					for _, arg := range n.Args {
						if call, ok := arg.(*ast.CallExpr); ok {
							allowed[call] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || allowed[call] {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if value, err := strconv.Unquote(lit.Value); err == nil && value == "XDG_CONFIG_HOME" {
						violations = append(violations, fset.Position(call.Pos()))
					}
				}
			}
			return true
		})
	}
	return violations
}

func isResolveConfigHome(fun ast.Expr) bool {
	switch fun := fun.(type) {
	case *ast.Ident:
		return fun.Name == "ResolveConfigHome"
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		return ok && pkg.Name == "config" && fun.Sel.Name == "ResolveConfigHome"
	}
	return false
}
