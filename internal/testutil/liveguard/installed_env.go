package liveguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// InstalledTestEnvReads parses files and returns every variable name they
// pass to one of calls, which maps a callee to the argument positions that
// hold a variable name. A callee is "pkg.Func" for a qualified call (for
// example "os.Getenv") or "Func" for a package-local one. A string literal
// argument yields its value; a qualified constant argument such as
// codexinstalled.DefaultSmokeRootEnv yields its source text, which the caller
// resolves. A package's provider opt-in test uses it to keep ProviderOptIn
// closed over the variables its installed tests read.
func InstalledTestEnvReads(files []string, calls map[string][]int) (map[string]bool, error) {
	read := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := ""
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fun.X.(*ast.Ident); ok {
					callee = pkg.Name + "." + fun.Sel.Name
				}
			case *ast.Ident:
				callee = fun.Name
			}
			for _, arg := range calls[callee] {
				if arg >= len(call.Args) {
					continue
				}
				switch value := call.Args[arg].(type) {
				case *ast.BasicLit:
					if value.Kind != token.STRING {
						continue
					}
					if name, err := strconv.Unquote(value.Value); err == nil {
						read[name] = true
					}
				case *ast.SelectorExpr:
					if pkg, ok := value.X.(*ast.Ident); ok {
						read[pkg.Name+"."+value.Sel.Name] = true
					}
				}
			}
			return true
		})
	}
	return read, nil
}
