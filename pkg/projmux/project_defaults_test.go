package projmux

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProjectDefaultsCapturedAndExplicit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("SHELL", "  /bin/zsh \t")
	captured, err := NewClient(Options{})
	if err != nil {
		t.Fatal(err)
	}
	explicitHome := filepath.Join(root, "explicit-home")
	if err := os.Mkdir(explicitHome, 0700); err != nil {
		t.Fatal(err)
	}
	explicit, err := NewClient(Options{HomeDir: explicitHome, DefaultShell: " /bin/bash ", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "later"))
	t.Setenv("SHELL", "/bin/other")
	if captured.projectShell != "/bin/zsh" || captured.projectSessionName(root) != "home" {
		t.Fatal("construction defaults changed", captured.projectShell, captured.projectSessionName(root))
	}
	if explicit.projectShell != "/bin/bash" || explicit.projectSessionName(explicitHome) != "home" || explicit.projectSessionName(root) == "home" {
		t.Fatal("explicit defaults ignored", explicit.projectShell)
	}
	result, err := explicit.CreateProject(context.Background(), core.CreateProjectRequest{Root: explicitHome})
	if err != nil {
		t.Fatal(err)
	}
	if result.Project.Status.Session == nil || result.Project.Status.Session.Name != "home" {
		t.Fatalf("session name: %+v", result.Project.Status)
	}
	if os.Getenv("SHELL") != "/bin/other" || os.Getenv("HOME") != filepath.Join(root, "later") {
		t.Fatal("client changed environment")
	}
	if _, err := NewClient(Options{HomeDir: "relative"}); err == nil || !strings.Contains(err.Error(), "HomeDir") {
		t.Fatal("relative HomeDir accepted", err)
	}
}

func sdkEnvironmentReferences(filename string, source any) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, source, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if path != "os" {
			continue
		}
		name := "os"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		aliases[name] = true
	}
	forbidden := map[string]bool{"Getenv": true, "LookupEnv": true, "UserHomeDir": true, "Environ": true}
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if ident, ok := n.X.(*ast.Ident); ok && aliases[ident.Name] && forbidden[n.Sel.Name] {
				found = append(found, ident.Name+"."+n.Sel.Name)
			}
		case *ast.Ident:
			if aliases["."] && forbidden[n.Name] {
				found = append(found, n.Name)
			}
		}
		return true
	})
	return found, nil
}

func TestSDKDoesNotReadProcessEnvironment(t *testing.T) {
	for _, source := range []string{
		`package sample; import "os"; var _ = os.Getenv("SHELL")`,
		`package sample; import environment "os"; var _ = environment.LookupEnv`,
		`package sample; import "os"; var _ = os.UserHomeDir`,
		`package sample; import . "os"; var _ = Environ`,
	} {
		found, err := sdkEnvironmentReferences("positive.go", source)
		if err != nil || len(found) != 1 {
			t.Fatalf("positive control: %v %v", found, err)
		}
	}
	if err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, err := sdkEnvironmentReferences(path, nil)
		if err != nil {
			return err
		}
		if len(found) != 0 {
			t.Errorf("%s reads process environment: %v", path, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
