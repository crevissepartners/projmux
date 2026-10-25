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
// use. create_operator_creator.go holds the record and the operator precedence
// the CLI and the SDK share; create_creator.go defines the CLI seam over them;
// create_intent.go is the UI, which records the client "ui"; create_agent.go is
// the SDK's CreateAgentRequest.Operator, which reaches a process create's owner
// route only as the typed OperatorCreatorClient field that
// process_detached_create.go hands to the seam. Every entry is in process: no
// argv spelling reaches one.
var operatorCreatorProducers = map[string]map[string]bool{
	"internal/services/mutations/runtime/create_operator_creator.go": {"OperatorCreatorAnnotations": true, "OperatorAgentCreator": true},
	"internal/app/create_creator.go":                                 {"CreatorBasisOperator": true, "newOperatorCreator": true, "OperatorAgentCreator": true, "DecideOperatorCreator": true},
	"internal/app/create_intent.go":                                  {"newOperatorCreator": true},
	"internal/app/process_detached_create.go":                        {"recordOperatorCreator": true},
	"pkg/projmux/create_agent.go":                                    {"DecideOperatorCreator": true, "OperatorCreatorClient": true},
}

// operatorCreatorSeams are the shared runtime entrypoints that build an
// operator creator record, whether called by selector or, inside their own
// package, by name.
var operatorCreatorSeams = map[string]bool{"OperatorAgentCreator": true, "DecideOperatorCreator": true}

// TestNoArgvPathBuildsAnOperatorCreator is the negative audit for the operator
// creator basis. Outside internal/core/metadata, a non-test Go file that names
// metadata.OperatorCreatorAnnotations or metadata.CreatorBasisOperator, calls
// newOperatorCreator, recordOperatorCreator or an operatorCreatorSeams entry,
// or sets the process request's OperatorCreatorClient field, is a producer, and
// every producer must be listed with that name in operatorCreatorProducers.
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
				if node.Sel.Name == "recordOperatorCreator" || operatorCreatorSeams[node.Sel.Name] {
					report(node.Sel.Name)
				}
			case *ast.CallExpr:
				if ident, ok := node.Fun.(*ast.Ident); ok && (ident.Name == "newOperatorCreator" || operatorCreatorSeams[ident.Name]) {
					report(ident.Name)
				}
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && key.Name == "OperatorCreatorClient" {
					report(key.Name)
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if field, ok := lhs.(*ast.SelectorExpr); ok && field.Sel.Name == "OperatorCreatorClient" {
						report(field.Sel.Name)
					}
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
