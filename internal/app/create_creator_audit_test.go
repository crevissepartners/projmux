package app

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

const coreMetadataImportPath = "github.com/crevissepartners/projmux/internal/core/metadata"

// operatorCreatorProducers lists the non-test files that may build an operator
// creator record, keyed by repository-relative path, with what each one may
// use. create_creator.go defines the seam and the record; create_intent.go is
// the UI, which records the client "ui". No file may call the in-process seam
// recordOperatorCreator yet: an operator client layered on projmux adds itself
// here when it lands. Every entry is in process: no argv spelling reaches one.
var operatorCreatorProducers = map[string]map[string]bool{
	"internal/app/create_creator.go": {"OperatorCreatorAnnotations": true, "CreatorBasisOperator": true, "newOperatorCreator": true},
	"internal/app/create_intent.go":  {"newOperatorCreator": true},
}

// TestNoArgvPathBuildsAnOperatorCreator is the negative audit for the operator
// creator basis. Outside internal/core/metadata, a non-test Go file that names
// metadata.OperatorCreatorAnnotations or metadata.CreatorBasisOperator, or
// calls newOperatorCreator or recordOperatorCreator, is a producer, and every
// producer must be listed with that name in operatorCreatorProducers.
func TestNoArgvPathBuildsAnOperatorCreator(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	self := filepath.Join("internal", "core", "metadata")
	scanned, definitionSeen := 0, false
	seen := map[string]map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			if skipNestedCheckout(root, path, entry) {
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
		relative = filepath.ToSlash(relative)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		if filepath.Dir(filepath.FromSlash(relative)) == self {
			for _, declaration := range file.Decls {
				if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "OperatorCreatorAnnotations" {
					definitionSeen = true
				}
			}
			return nil
		}
		local := ""
		for _, imported := range file.Imports {
			if value, _ := strconv.Unquote(imported.Path.Value); value == coreMetadataImportPath {
				local = "metadata"
				if imported.Name != nil {
					local = imported.Name.Name
				}
			}
		}
		report := func(used string) {
			if seen[relative] == nil {
				seen[relative] = map[string]bool{}
			}
			seen[relative][used] = true
			if !operatorCreatorProducers[relative][used] {
				t.Errorf("%s builds an operator creator (%s) but is not a listed producer of it", relative, used)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok && local != "" && pkg.Name == local &&
					(node.Sel.Name == "OperatorCreatorAnnotations" || node.Sel.Name == "CreatorBasisOperator") {
					report(node.Sel.Name)
				}
				if node.Sel.Name == "recordOperatorCreator" {
					report(node.Sel.Name)
				}
			case *ast.CallExpr:
				if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "newOperatorCreator" {
					report(ident.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the walk saw the constructor's definition and every
	// listed use, so an empty result means no unlisted producer rather than a
	// scan that looked nowhere.
	if !definitionSeen || scanned < 100 {
		t.Fatalf("audit scanned %d files and saw the constructor definition: %t, so it proves nothing", scanned, definitionSeen)
	}
	for file, uses := range operatorCreatorProducers {
		for used := range uses {
			if !seen[file][used] {
				t.Errorf("listed producer %s no longer uses %s; the audit list is stale", file, used)
			}
		}
	}
}
