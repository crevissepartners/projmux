package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// declaredPredicateSites walks every tracked production Go file under internal/
// and cmd/ and returns, for each audited name, the files that declare it as a
// package-level func or type. Methods are reported as "<recv>.<name>" so a
// projection that keeps the old method name is distinguishable from a second
// free-standing derivation.
func declaredPredicateSites(t *testing.T, names map[string]bool) map[string][]string {
	t.Helper()
	root := repoRootForTest(t)
	sites := map[string][]string{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				// .wt/ worktree copies and .git live outside these two trees,
				// but testdata carries fixtures that are not production source.
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				switch typed := declaration.(type) {
				case *ast.FuncDecl:
					key := typed.Name.Name
					if typed.Recv != nil {
						key = "method:" + typed.Name.Name
					}
					if names[key] {
						sites[key] = append(sites[key], rel)
					}
				case *ast.GenDecl:
					for _, spec := range typed.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if ok && names[ts.Name.Name] {
							sites[ts.Name.Name] = append(sites[ts.Name.Name], rel)
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sites
}

// TestConvergedPredicatesHaveOneOwner is the retired-source negative audit for
// C-2 Failure. Each row is a concept that used to be derived in more than one
// package; the canonical owner must declare it exactly once and every retired
// spelling must be gone. Behavioural tests cannot catch a revived copy that
// happens to agree today — agreement of two copies is exactly what stops being
// observable once the rule is written out twice — so ownership is enforced
// against the source tree instead.
func TestConvergedPredicatesHaveOneOwner(t *testing.T) {
	t.Parallel()

	cases := []struct {
		concept  string
		owner    string // "" means the canonical owner is outside this repo
		declares string
		// projections are files allowed to re-export the canonical name. Each
		// one is held to delegation by TestProjectedPredicatesDelegateInsteadOfRederiving.
		projections []string
		retired     []string
	}{
		{
			concept:  "WSL environment",
			owner:    "internal/app/initcmd/init_windows_terminal.go",
			declares: "IsWSL",
			retired:  []string{"isWSL"},
		},
		{
			concept:     "usage-error classification",
			owner:       "internal/core/metadata/errors.go",
			declares:    "IsUsageError",
			projections: []string{"internal/app/app.go"},
		},
		{
			concept:  "Registry ref validity",
			owner:    "internal/core/agentmessage/message.go",
			declares: "ValidRef",
			retired:  []string{"validRef"},
		},
		{
			concept:  "Braille spinner prefix",
			owner:    "internal/ui/render/switch_preview.go",
			declares: "HasBraillePrefix",
			retired:  []string{"hasBraillePrefix"},
		},
		{
			concept: "slice membership",
			owner:   "", // stdlib slices.Contains owns it; no repo symbol remains.
			retired: []string{"containsString"},
		},
		{
			concept: "compat picker runner",
			owner:   "internal/ui/pickercompat/types.go",
			// The interface is named Runner in several packages, so only the
			// retired local redeclarations are audited by name here.
			retired: []string{"aiCommandRunner", "sessionsRunner", "switchRunner"},
		},
		{
			concept: "tmux lifecycle hook inspection",
			owner:   "",
			retired: []string{"lifecycleHookInspector"},
		},
	}

	names := map[string]bool{}
	for _, c := range cases {
		if c.declares != "" {
			names[c.declares] = true
		}
		for _, r := range c.retired {
			names[r] = true
			names["method:"+r] = true
		}
	}
	sites := declaredPredicateSites(t, names)

	for _, c := range cases {
		if c.declares != "" {
			want := append([]string{c.owner}, c.projections...)
			got := sites[c.declares]
			if !sameSites(got, want) {
				t.Errorf("%s: %q is declared at %v, want exactly %v (owner first, then declared projections)",
					c.concept, c.declares, got, want)
			}
		}
		for _, retired := range c.retired {
			if got := sites[retired]; len(got) != 0 {
				t.Errorf("%s: retired derivation %q reappeared at %v; %s owns it",
					c.concept, retired, got, ownerLabel(c.owner))
			}
		}
	}

	// mux exposes its tmux command surface through Runner methods. The retired
	// free-function mirror delegated to DefaultRunner(), which is precisely the
	// second entry point the convergence removed.
	if got := sites["ShowOption"]; len(got) != 0 {
		t.Errorf("mux free function ShowOption reappeared at %v; Runner.ShowOption owns the surface", got)
	}
}

func sameSites(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	remaining := map[string]int{}
	for _, w := range want {
		remaining[w]++
	}
	for _, g := range got {
		if remaining[g] == 0 {
			return false
		}
		remaining[g]--
	}
	return true
}

func ownerLabel(owner string) string {
	if owner == "" {
		return "the standard library"
	}
	return owner
}

// TestProjectedPredicatesDelegateInsteadOfRederiving audits the two surfaces
// that keep their old name as a projection. A projection is only a projection
// while it calls the canonical owner; the moment it spells the rule out again
// the concept has two owners under one name, which is the failure mode the
// convergence was for.
func TestProjectedPredicatesDelegateInsteadOfRederiving(t *testing.T) {
	t.Parallel()

	root := repoRootForTest(t)
	cases := []struct {
		path     string
		receiver bool
		function string
		delegate string
		retired  map[string]string
	}{
		{
			path:     "internal/app/ai.go",
			receiver: true,
			function: "isWSL",
			delegate: "IsWSL",
			retired: map[string]string{
				"WSL_DISTRO_NAME":            "env marker clause",
				"WSL_INTEROP":                "env marker clause",
				"/proc/sys/kernel/osrelease": "kernel osrelease clause",
				"microsoft":                  "kernel osrelease clause",
			},
		},
		{
			path:     "internal/app/app.go",
			function: "IsUsageError",
			delegate: "IsUsageError",
			retired: map[string]string{
				"As": "marker protocol clause",
			},
		},
	}

	for _, c := range cases {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, c.path), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var body *ast.BlockStmt
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Name.Name != c.function {
				continue
			}
			if (fn.Recv != nil) == c.receiver {
				body = fn.Body
			}
		}
		if body == nil {
			t.Errorf("%s no longer declares %s", c.path, c.function)
			continue
		}
		delegates := false
		ast.Inspect(body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.SelectorExpr:
				if typed.Sel.Name == c.delegate {
					delegates = true
				}
				if clause, ok := c.retired[typed.Sel.Name]; ok {
					t.Errorf("%s: %s re-derives the %s through %q", c.path, c.function, clause, typed.Sel.Name)
				}
			case *ast.BasicLit:
				for literal, clause := range c.retired {
					if strings.Contains(strings.ToLower(typed.Value), strings.ToLower(literal)) {
						t.Errorf("%s: %s re-derives the %s through literal %s", c.path, c.function, clause, typed.Value)
					}
				}
			}
			return true
		})
		if !delegates {
			t.Errorf("%s: %s does not call the canonical %s", c.path, c.function, c.delegate)
		}
	}
}
