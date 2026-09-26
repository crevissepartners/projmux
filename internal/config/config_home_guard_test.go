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

// xdgHomeVars maps each XDG base directory variable the guard covers to the
// Homes field that passes it through unresolved.
var xdgHomeVars = map[string]string{
	"XDG_CONFIG_HOME": "ConfigHome",
	"XDG_STATE_HOME":  "StateHome",
	"XDG_DATA_HOME":   "",
	"XDG_CACHE_HOME":  "",
}

// xdgResolvers are the functions that apply the one XDG base directory rule.
var xdgResolvers = map[string]bool{
	"ResolveConfigHome": true,
	"ResolveStateHome":  true,
	"ResolveDataHome":   true,
	"ResolveCacheHome":  true,
}

// xdgGuardExemptFiles are the files, relative to the repository root, that may
// read an XDG base directory variable by hand. liveguard imports only the
// standard library so any package's internal tests can use it without an
// import cycle; its read checks the private root it set itself, not a product
// path.
var xdgGuardExemptFiles = map[string]bool{
	"internal/testutil/liveguard/liveguard.go": true,
}

// TestXDGHomesAreResolvedInOnePlace keeps every read of XDG_CONFIG_HOME,
// XDG_STATE_HOME, XDG_DATA_HOME, and XDG_CACHE_HOME going through the
// Resolve*Home functions, directly or through the Homes pass-through, so no
// site can resolve a base directory by hand and drift from the others.
func TestXDGHomesAreResolvedInOnePlace(t *testing.T) {
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
			if xdgGuardExemptFiles[filepath.ToSlash(strings.TrimPrefix(path, "../../"))] {
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
	for _, violation := range xdgHomeReadsOutsideResolver(fset, files) {
		t.Errorf("%s: %s is read outside the resolver; pass it to config.Resolve*Home (or config.Homes{ConfigHome: ..., StateHome: ...})", violation.pos, violation.name)
	}
}

type xdgHomeRead struct {
	pos  token.Position
	name string
}

// xdgHomeReadsOutsideResolver returns every call that takes one of the
// xdgHomeVars string literals as an argument, unless that call is the matching
// Homes field value of a composite literal or a direct argument of a
// Resolve*Home function. Literals outside calls, such as env pass-through
// lists, are not reads and are ignored.
func xdgHomeReadsOutsideResolver(fset *token.FileSet, files []*ast.File) []xdgHomeRead {
	var violations []xdgHomeRead
	for _, file := range files {
		allowed := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && (key.Name == "ConfigHome" || key.Name == "StateHome") {
					if call, ok := n.Value.(*ast.CallExpr); ok && xdgHomeVars[xdgHomeLiteral(call)] == key.Name {
						allowed[call] = true
					}
				}
			case *ast.CallExpr:
				if isXDGResolver(n.Fun) {
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
			if name := xdgHomeLiteral(call); name != "" {
				violations = append(violations, xdgHomeRead{pos: fset.Position(call.Pos()), name: name})
			}
			return true
		})
	}
	return violations
}

// xdgHomeLiteral returns the first xdgHomeVars name call takes as a string
// literal argument, or "" when it takes none.
func xdgHomeLiteral(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if value, err := strconv.Unquote(lit.Value); err == nil {
				if _, ok := xdgHomeVars[value]; ok {
					return value
				}
			}
		}
	}
	return ""
}

func isXDGResolver(fun ast.Expr) bool {
	switch fun := fun.(type) {
	case *ast.Ident:
		return xdgResolvers[fun.Name]
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		return ok && pkg.Name == "config" && xdgResolvers[fun.Sel.Name]
	}
	return false
}
