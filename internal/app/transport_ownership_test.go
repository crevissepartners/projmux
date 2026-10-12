package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rawTransportNoopRunner struct{}

func (rawTransportNoopRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

// TestRawTmuxTransportHasOneProductionOwner is the retired-secondary-source
// audit for the raw routing stage. Commands may translate their own usage
// vocabulary, but they must pass name/path/inherited inputs to the
// resourcegraph resolver and carry its Transport without rebuilding it.
func TestRawTmuxTransportHasOneProductionOwner(t *testing.T) {
	t.Parallel()

	forbidden := map[string]bool{
		"explicitTmuxTarget":  true,
		"controllerTransport": true,
	}
	fset := token.NewFileSet()
	allowedContainmentReaders := map[string]bool{
		"delete_pane_runtime.go":                                 true,
		"../services/mutations/runtime/delete_window_runtime.go": true,
	}
	for _, name := range append(appSourceFiles(t), mutationRuntimeSourceFiles(t)...) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && forbidden[ident.Name] {
				t.Errorf("%s retains retired raw transport owner %s", name, ident.Name)
			}
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Cut" {
				return true
			}
			readsTMUX := false
			ast.Inspect(call.Args[0], func(arg ast.Node) bool {
				literal, ok := arg.(*ast.BasicLit)
				if ok && literal.Value == `"TMUX"` {
					readsTMUX = true
				}
				return true
			})
			if readsTMUX && !allowedContainmentReaders[name] {
				t.Errorf("%s manually splits inherited TMUX instead of using resourcegraph.ResolveTransport", name)
			}
			return true
		})
	}

	for _, owner := range []struct{ name, resolver string }{
		{"agent_interaction.go", "resourcegraph.ResolveTransport("},
		{"agent_review.go", "resourcegraph.ResolveTransport("},
		{"../services/mutations/runtime/binding_convergence.go", "resourcegraph.ResolveTransport("},
		{"../services/mutations/runtime/delete_transport.go", "resourcegraph.ResolveTransport("},
		{"delete_transport.go", "mutationruntime.ResolveDeleteTarget("},
		{"resource_mutation_mirror.go", "resourcegraph.ResolveTransport("},
		{"resource_reconcile.go", "resourcegraph.ResolveTransport("},
		{"registry_recovery.go", "resourcegraph.ResolveTransport("},
		{"runtime_diagnostics.go", "runtimeservice.ResolveTransport("},
		{"../services/runtime/observe.go", "resourcegraph.ResolveTransport("},
	} {
		source, err := os.ReadFile(filepath.Clean(owner.name))
		if err != nil {
			t.Fatalf("read %s: %v", owner.name, err)
		}
		if !strings.Contains(string(source), owner.resolver) {
			t.Errorf("%s does not project raw routing through %s", owner.name, owner.resolver)
		}
	}

}

func TestInheritedRawTransportAdaptersPreserveNoneBehavior(t *testing.T) {
	t.Parallel()
	runner := rawTransportNoopRunner{}
	adapters := map[string]func(func(string) string, tmuxCommandRunner) bool{
		"resource mutation mirror": func(lookup func(string) string, runner tmuxCommandRunner) bool {
			return inheritedResourceMutationMirror(lookup, runner) != nil
		},
		"agent mutation mirror": func(lookup func(string) string, runner tmuxCommandRunner) bool {
			return inheritedAgentMutationMirror(lookup, runner) != nil
		},
		"agent review binding": func(lookup func(string) string, runner tmuxCommandRunner) bool {
			return inheritedAgentReviewBindingLookup(lookup, runner) != nil
		},
	}
	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if adapter(nil, runner) || adapter(func(string) string { return "/tmp/projmux,1,0" }, nil) {
				t.Fatal("nil dependency enabled inherited routing")
			}
			for _, inherited := range []string{"", "relative.sock,1,0", "malformed"} {
				if adapter(func(key string) string {
					if key == "TMUX" {
						return inherited
					}
					return ""
				}, runner) {
					t.Fatalf("TMUX=%q enabled an inherited route", inherited)
				}
			}
			if !adapter(func(key string) string {
				if key == "TMUX" {
					return "/tmp/projmux,1,0"
				}
				return ""
			}, runner) {
				t.Fatal("absolute inherited TMUX did not enable its exact route")
			}
		})
	}
}
