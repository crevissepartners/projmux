package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestProjectStartupRetiredPathsNegativeAudit(t *testing.T) {
	t.Parallel()
	files := []string{"project_startup.go", "project_startup_fresh.go", "session_state.go"}
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(source), "RestoreSessionSnapshot(") {
			t.Fatalf("%s retains a direct snapshot replay callsite", path)
		}
	}

	for _, candidate := range (&switchCommand{}).projectStartupCandidates("ignored", "/ignored") {
		rendered := strings.ToLower(candidate.Label + " " + candidate.Description + " " + projectStartupPickerLabel(candidate))
		for _, retired := range []string{"latest snapshot", "named snapshot", "project topology", "reconcile"} {
			if strings.Contains(rendered, retired) {
				t.Fatalf("startup action %+v exposes retired vocabulary %q", candidate, retired)
			}
		}
	}

	// Restore and fresh may commit their scoped Registry plans, but must never
	// call a snapshot persistence mutation. Parsing only these execution
	// functions avoids conflating the separate explicit save/delete commands.
	audited := map[string]bool{
		"commitSnapshotProjection": false,
		"startProjectFresh":        false,
		"PruneProjectFreshStart":   false,
	}
	for _, path := range []string{"session_state.go", "project_startup_fresh.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, ok := audited[fn.Name.Name]; !ok {
				continue
			}
			audited[fn.Name.Name] = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if assignment, ok := node.(*ast.AssignStmt); ok && fn.Name.Name == "commitSnapshotProjection" {
					for i, left := range assignment.Lhs {
						star, ok := left.(*ast.StarExpr)
						if !ok {
							continue
						}
						owner, ownerOK := star.X.(*ast.Ident)
						if !ownerOK || owner.Name != "working" || i >= len(assignment.Rhs) {
							continue
						}
						right, ok := assignment.Rhs[i].(*ast.SelectorExpr)
						base, baseOK := right.X.(*ast.Ident)
						if !ok || !baseOK || base.Name != "plan" || right.Sel.Name != "Desired" {
							t.Errorf("%s.%s replaces the Registry from a source other than the scoped projection plan", path, fn.Name.Name)
						}
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "Save" || selector.Sel.Name == "Delete" || selector.Sel.Name == "RestoreSessionSnapshot") {
					t.Errorf("%s.%s contains forbidden persistence/replay call %s", path, fn.Name.Name, selector.Sel.Name)
				}
				return true
			})
		}
	}
	for name, found := range audited {
		if !found {
			t.Fatalf("negative audit did not find execution function %s", name)
		}
	}
}
