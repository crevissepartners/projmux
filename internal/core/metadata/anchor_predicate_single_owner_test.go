package metadata

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestWindowAnchorShellOwnedByAgentIsNotARepresentableRegistry records why the
// input the two former derivations disagreed on is unreachable rather than
// latent-but-producible: a shell Pane whose ownerRef names an Agent is refused
// by the Pane role switch, which runs before the Window anchor clause. No
// Registry that Validate accepts can carry the shape, so resolving the anchor
// predicate onto the refusing answer cannot change any accepted Registry.
func TestWindowAnchorShellOwnedByAgentIsNotARepresentableRegistry(t *testing.T) {
	t.Parallel()
	fixture := newAnchorSchemaFixture(t)
	reg := fixture.registry.Clone()
	shell, ok := reg.Pane(fixture.shellUID)
	if !ok {
		t.Fatalf("fixture Pane %q missing", fixture.shellUID)
	}
	if shell.Spec.Role != PaneRoleShell {
		t.Fatalf("fixture Pane %q role = %q, want %q", fixture.shellUID, shell.Spec.Role, PaneRoleShell)
	}
	shell.Metadata.OwnerRef = &OwnerRef{Kind: KindAgent, UID: fixture.agentUID}

	// The input really is the divergent one: the owner chain reaches the
	// Window, so membership alone would admit it.
	ownerWindowUID, owned := paneWindowOwnerUID(reg, *shell)
	if !owned || ownerWindowUID != fixture.windowUID {
		t.Fatalf("paneWindowOwnerUID = (%q, %t), want (%q, true)", ownerWindowUID, owned, fixture.windowUID)
	}

	err := reg.Validate()
	if err == nil {
		t.Fatal("Validate accepted a shell Pane owned by an Agent")
	}
	if !strings.Contains(err.Error(), "ownerRef kind") {
		t.Fatalf("Validate refused for an unexpected reason: %v", err)
	}
}

// TestWindowAnchorPredicateHasOneOwner is the retired-source negative audit for
// C-2 Failure: reviving an inline derivation of the anchor rule inside
// Registry.WindowAnchor fails here even when the revived copy happens to agree,
// because agreement of two copies is exactly what stops being observable once
// they are written out twice.
func TestWindowAnchorPredicateHasOneOwner(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	definitions := 0
	for _, entry := range entries {
		path := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "windowAnchorEligibility" {
				definitions++
			}
		}
	}
	if definitions != 1 {
		t.Fatalf("windowAnchorEligibility is declared %d times, want exactly 1", definitions)
	}

	const path = "model.go"
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.Name == "WindowAnchor" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s no longer declares Registry.WindowAnchor", path)
	}

	// Every clause of the rule the retired inline copy spelled out. The
	// resolver may name the Window and the referenced Pane; it may not decide
	// role, ownership, or managed-Pane binding for itself.
	retired := map[string]string{
		"PaneRoleShell":      "role clause",
		"PaneRoleAgent":      "role clause",
		"paneWindowOwnerUID": "membership clause",
		"OwnerRef":           "ownership clause",
		"OwnerUID":           "ownership clause",
		"PaneRef":            "managed-Pane clause",
		"KindWindow":         "ownership clause",
	}
	delegates := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if clause, ok := retired[typed.Name]; ok {
				t.Errorf("Registry.WindowAnchor re-derives the anchor %s through %q; windowAnchorEligibility owns it", clause, typed.Name)
			}
			if typed.Name == "windowAnchorEligibility" {
				delegates = true
			}
		case *ast.SelectorExpr:
			if clause, ok := retired[typed.Sel.Name]; ok {
				t.Errorf("Registry.WindowAnchor re-derives the anchor %s through %q; windowAnchorEligibility owns it", clause, typed.Sel.Name)
			}
		}
		return true
	})
	if !delegates {
		t.Error("Registry.WindowAnchor does not call windowAnchorEligibility")
	}
}
