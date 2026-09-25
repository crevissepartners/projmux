package liveguard

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath    = "github.com/crevissepartners/projmux"
	liveguardPath = modulePath + "/internal/testutil/liveguard"
)

// liveMachineRoots are the packages through which a test binary can reach the
// live machine. A test binary that links any of them must run behind the
// guard.
var liveMachineRoots = []string{
	// The Registry store: its default path is the live Registry.
	modulePath + "/internal/integrations/metadata",
	// The tmux runners: without an injected socket they reach the live tmux
	// server through TMUX or the default socket.
	modulePath + "/internal/integrations/mux",
	modulePath + "/internal/integrations/tmux",
	// Provider exec code: it resolves the real codex CLI through PATH and
	// starts or talks to its app-server daemon.
	modulePath + "/internal/integrations/agents/codexappserver",
	// The dispatcher: every command's default wiring, including the claude
	// provider, the Registry, and tmux.
	modulePath + "/internal/app",
	// The installed-provider qualification fixture: it runs the real codex.
	modulePath + "/internal/testutil/codexinstalled",
}

// TestEveryPackageThatCanReachTheLiveMachineRunsBehindTheGuard lists every
// test binary in the module and requires each one that links a live-machine
// root to have a TestMain that runs behind RunTests (or RunGuarded) and a test
// that calls RequireActive.
func TestEveryPackageThatCanReachTheLiveMachineRunsBehindTheGuard(t *testing.T) {
	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	moduleRoot := filepath.Dir(strings.TrimSpace(string(gomod)))
	list := exec.Command("go", "list", "-test", "-f",
		`{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .TestGoFiles ","}}{{"\t"}}{{join .XTestGoFiles ","}}{{"\t"}}{{join .Deps ","}}`,
		"./...")
	list.Dir = moduleRoot
	var stderr bytes.Buffer
	list.Stderr = &stderr
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}

	type pkg struct {
		dir   string
		files []string
	}
	packages := map[string]pkg{}
	links := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("unexpected go list line %q", line)
		}
		importPath := fields[0]
		if strings.Contains(importPath, " ") {
			continue // a test variant; the plain entry carries the same files.
		}
		if base, ok := strings.CutSuffix(importPath, ".test"); ok {
			for dep := range strings.SplitSeq(fields[4], ",") {
				dep, _, _ = strings.Cut(dep, " ")
				if slices.Contains(liveMachineRoots, dep) {
					links[base] = dep
					break
				}
			}
			continue
		}
		var files []string
		for _, list := range fields[2:4] {
			for name := range strings.SplitSeq(list, ",") {
				if name != "" {
					files = append(files, filepath.Join(fields[1], name))
				}
			}
		}
		packages[importPath] = pkg{dir: fields[1], files: files}
	}

	set := slices.Sorted(maps.Keys(links))
	t.Logf("%d test packages link a live-machine root: %s", len(set), strings.Join(set, " "))
	for _, want := range []string{modulePath + "/internal/app", modulePath + "/internal/integrations/metadata"} {
		if !slices.Contains(set, want) {
			t.Errorf("the set lacks %s; the listing is broken", want)
		}
	}

	for _, importPath := range set {
		runs, requires, err := guardCallsIn(packages[importPath].files, importPath == liveguardPath)
		if err != nil {
			t.Errorf("%s: %v", importPath, err)
			continue
		}
		if !runs {
			t.Errorf("%s links %s but does not run behind liveguard.RunTests; add a TestMain (see internal/testutil/liveguard)", importPath, links[importPath])
		}
		if !requires {
			t.Errorf("%s links %s but no test calls liveguard.RequireActive; add TestLiveMachineGuardHolds (see internal/testutil/liveguard)", importPath, links[importPath])
		}
	}
}

// guardCallsIn reports whether files hold a TestMain(m *testing.M) that calls
// liveguard.RunTests or liveguard.RunGuarded, and a Test function that calls
// liveguard.RequireActive. The selector is resolved through each file's own
// import of liveguard under any name; self is true for liveguard's own tests,
// which call the functions unqualified.
func guardCallsIn(files []string, self bool) (runs, requires bool, err error) {
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return false, false, err
		}
		local := ""
		for _, spec := range parsed.Imports {
			if path, _ := strconv.Unquote(spec.Path.Value); path == liveguardPath {
				local = "liveguard"
				if spec.Name != nil {
					local = spec.Name.Name
				}
			}
		}
		calls := func(body *ast.BlockStmt, names ...string) bool {
			found := false
			ast.Inspect(body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || found {
					return !found
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fun.X.(*ast.Ident); ok && local != "" && x.Name == local && slices.Contains(names, fun.Sel.Name) {
						found = true
					}
				case *ast.Ident:
					if self && slices.Contains(names, fun.Name) {
						found = true
					}
				}
				return !found
			})
			return found
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			switch {
			case fn.Name.Name == "TestMain" && isTestingMParam(fn.Type):
				runs = runs || calls(fn.Body, "RunTests", "RunGuarded")
			case strings.HasPrefix(fn.Name.Name, "Test"):
				requires = requires || calls(fn.Body, "RequireActive")
			}
		}
	}
	return runs, requires, nil
}

// isTestingMParam reports whether a function takes exactly one *testing.M.
func isTestingMParam(fn *ast.FuncType) bool {
	if fn.Params == nil || len(fn.Params.List) != 1 || len(fn.Params.List[0].Names) > 1 {
		return false
	}
	star, ok := fn.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "M"
}
