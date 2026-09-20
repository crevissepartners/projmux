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

// bareRefusalReturns lists the repository-relative non-test files still
// allowed to return ErrInvalidEnvelope without naming which rule broke. It is
// empty on purpose: one string served about twenty causes, and a reader could
// not tell them apart. A new entry needs a reason a caller can act on instead.
var bareRefusalReturns = map[string]bool{}

// TestNoProductPathReturnsAnUnnamedEnvelopeRefusal is the negative audit for
// the diagnostic contract. A non-test Go file that returns the bare
// ErrInvalidEnvelope value hands a caller a refusal with no cause, so every
// such return must be listed above. Wrapping it through EnvelopeRefusal, or
// naming one of the exported sentinels built from it, is not a bare return.
func TestNoProductPathReturnsAnUnnamedEnvelopeRefusal(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	self := filepath.Join("internal", "core", "agentmessage")
	scanned, helperSeen := 0, false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
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
		own := filepath.Dir(relative) == self
		local := ""
		if own {
			for _, declaration := range file.Decls {
				if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "EnvelopeRefusal" {
					helperSeen = true
				}
			}
		} else {
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
		}
		ast.Inspect(file, func(node ast.Node) bool {
			statement, ok := node.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, result := range statement.Results {
				if !bareEnvelopeRefusal(result, own, local) || bareRefusalReturns[filepath.ToSlash(relative)] {
					continue
				}
				t.Errorf("%s returns ErrInvalidEnvelope with no reason; wrap it with EnvelopeRefusal", relative)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !helperSeen || scanned < 100 {
		t.Fatalf("audit scanned %d files and saw the helper definition: %t, so it proves nothing", scanned, helperSeen)
	}
}

// bareEnvelopeRefusal reports whether a returned expression is the sentinel
// value itself rather than a refusal built from it.
func bareEnvelopeRefusal(result ast.Expr, own bool, local string) bool {
	if own {
		identifier, ok := result.(*ast.Ident)
		return ok && identifier.Name == "ErrInvalidEnvelope"
	}
	selector, ok := result.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "ErrInvalidEnvelope" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == local
}
