package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// appSourceFiles returns every non-test Go file under internal/app/**, the
// subpackages included, as a slash-separated path relative to internal/app
// ("tmux.go", "keybinding/keymap.go"). A test runs in its package directory,
// so each path opens as is and doubles as the file's key in an allowlist.
//
// A source sweep reads the package through this walk, never by listing a
// directory itself: a sweep that lists only the top level stops seeing code
// the moment that code moves into a subpackage, and passes silently.
// TestAppSourceSweepsUseTheSharedWalk refuses a direct listing.
func appSourceFiles(t testing.TB) []string {
	t.Helper()
	return appGoFiles(t, func(name string) bool { return !strings.HasSuffix(name, "_test.go") })
}

// appTestFiles returns every _test.go file under internal/app/**, in the same
// form as appSourceFiles.
func appTestFiles(t testing.TB) []string {
	t.Helper()
	return appGoFiles(t, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
}

// appGoFiles walks internal/app/** and keeps the .go files whose base name
// keep accepts. It skips what the go tool skips: testdata, and directories
// whose name starts with "." or "_".
func appGoFiles(t testing.TB, keep func(name string) bool) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != "." && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") && keep(name) {
			paths = append(paths, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/app: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("walk internal/app found no Go files")
	}
	slices.Sort(paths)
	return paths
}

// appSourceSweepExceptions are the test functions under internal/app/** that
// still list a directory themselves, each with the reason it does not read
// internal/app through appSourceFiles or appTestFiles. Keys are
// "<path relative to internal/app>:<function>".
var appSourceSweepExceptions = map[string]string{
	// Sweeps over more than internal/app: the repository, internal/ and cmd/,
	// or other packages. They walk recursively already.
	"agent_session_history_test.go:TestClaudeSessionRefWritersRecordHistory":             "repository-wide walk of internal/ and cmd/",
	"agent_session_history_test.go:TestSessionHistoryObservedRowWritersCarryAffiliation": "repository-wide walk of internal/ and cmd/",
	"ai_ingest_reason_vocabulary_test.go:TestNoTestExpectsALeakedReason":                 "repository-wide walk of internal/ tests and test/e2e scripts",
	"ai_integrate_codex_trust_test.go:TestProductionSourceNeverProducesCodexTrustState":  "repository-wide walk of internal/ and cmd/",
	"codex_controlplane_gate_test.go:TestControlPlaneTestsNeverExpectAnUncapturedReason": "repository-wide walk of internal/ tests and test/e2e scripts",
	"codex_controlplane_gate_test.go:declaredTestNames":                                  "repository-wide walk for declared Test names",
	"command_literal_guard_test.go:extractCommandLiterals":                               "repository-wide walk of the command literal sources",
	"converged_predicate_owners_test.go:declaredPredicateSites":                          "repository-wide walk of internal/ and cmd/",
	"create_creator_audit_test.go:TestNoArgvPathBuildsAnOperatorCreator":                 "repository-wide walk",
	"flag_parse_usage_guard_test.go:flagParseGuardLoadRepo":                              "walks all of internal/ and marks internal/app/** for collection",
	"git_call_lock_guard_test.go:TestProductionGitCallsCannotTakeTheIndexLock":           "repository-wide walk",
	"lifecycle_teardown_journal_test.go:coreTeardownReasons":                             "reads internal/core/metadata, not internal/app",
	"resource_reconcile_root_kind_test.go:scanRootSliceTraversals":                       "repository-wide walk",
	"runtime_mutation_plan_test.go:TestPlanOnlyMutationNegativeAuditHasZeroBypass":       "walks internal/app/** and four packages outside it",

	// Sweeps that already walk internal/app/** recursively; their rules are
	// out of this helper's scope.
	"ai_route_usage_guard_test.go:TestAIHandlersNameNoRetiredRoute":                              "recursive walk of internal/app/**",
	"handler_usage_synopsis_guard_test.go:loadHandlerSynopsisFiles":                              "recursive walk of internal/app/**",
	"run_shell_output_ledger_test.go:TestRunShellSourceSitesAreClosed":                           "recursive walk of internal/app/**",
	"runtime_mutation_plan_test.go:TestAIBellIntegrationOwnsRunnerLifecycleTransport":            "recursive walk of internal/app/**",
	"runtime_mutation_plan_test.go:TestGenericWindowPaneMirrorIsRecorderOnlyOutsideNativePhase2": "recursive walk of internal/app/**",
	"settings_layer_guard_test.go:TestAppNamesNoSettingPathInline":                               "recursive walk of internal/app/**",
	"settings_test.go:TestLiteralProjmuxFootersHaveCatalogMappings":                              "recursive walk of internal/app/**",

	// A subpackage test cannot import package app's helper, so it reads its
	// own package directory.
	"keybinding/keybinding_catalog_test.go:TestRetiredKeyBindingMetadataMapsStayAbsent": "subpackage sweep of its own package",
}

// appSourceSweepAPIs are the calls that list a directory, keyed by import path
// and then by name. Readdir and Readdirnames are *os.File methods and are
// matched by name on any receiver.
var appSourceSweepAPIs = map[string]map[string]bool{
	"os":            {"ReadDir": true, "DirFS": true},
	"io/ioutil":     {"ReadDir": true},
	"io/fs":         {"ReadDir": true, "WalkDir": true, "Glob": true},
	"path/filepath": {"Glob": true, "Walk": true, "WalkDir": true},
	"go/parser":     {"ParseDir": true},
}

// appSourceSweeps returns the "<path>:<function>" key of every top-level
// function in the given test files that lists a directory to read Go source.
// A function counts when it calls a listing API and either names a ".go"
// pattern or suffix in a string literal or lists "." itself.
func appSourceSweeps(fset *token.FileSet, files map[string]*ast.File) []string {
	var sweeps []string
	for path, file := range files {
		imports := map[string]string{}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			local := importPath[strings.LastIndex(importPath, "/")+1:]
			if spec.Name != nil {
				local = spec.Name.Name
			}
			imports[local] = importPath
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			lists, namesGo := false, false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.BasicLit:
					if node.Kind == token.STRING {
						if value, err := strconv.Unquote(node.Value); err == nil && strings.HasSuffix(value, ".go") {
							namesGo = true
						}
					}
				case *ast.CallExpr:
					selector, ok := node.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					listing := selector.Sel.Name == "Readdir" || selector.Sel.Name == "Readdirnames"
					if pkg, ok := selector.X.(*ast.Ident); ok && appSourceSweepAPIs[imports[pkg.Name]][selector.Sel.Name] {
						listing = true
					}
					if !listing {
						return true
					}
					lists = true
					if len(node.Args) > 0 {
						if literal, ok := node.Args[0].(*ast.BasicLit); ok && (literal.Value == `"."` || literal.Value == `""`) {
							namesGo = true
						}
					}
				}
				return true
			})
			if !lists || !namesGo {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				receiver := fn.Recv.List[0].Type
				if star, ok := receiver.(*ast.StarExpr); ok {
					receiver = star.X
				}
				if ident, ok := receiver.(*ast.Ident); ok {
					name = ident.Name + "." + name
				}
			}
			sweeps = append(sweeps, path+":"+name)
		}
	}
	slices.Sort(sweeps)
	return sweeps
}

// appSourceSweepProblems compares the sweeps found with the exceptions: a
// sweep that is neither the shared walk nor an exception lists a directory
// directly, and an exception no sweep matches is stale.
func appSourceSweepProblems(sweeps []string, exceptions map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, sweep := range sweeps {
		seen[sweep] = true
		if sweep == "app_source_walk_test.go:appGoFiles" {
			continue
		}
		if _, ok := exceptions[sweep]; !ok {
			problems = append(problems, sweep+" lists a directory to read Go source; read internal/app through appSourceFiles or appTestFiles")
		}
	}
	for key := range exceptions {
		if !seen[key] {
			problems = append(problems, key+" is an appSourceSweepExceptions row no sweep matches; drop it")
		}
	}
	slices.Sort(problems)
	return problems
}

// TestAppSourceSweepsUseTheSharedWalk is the meta-guard over every test under
// internal/app/**: a source sweep reads the package through appGoFiles, or it
// is a reasoned row of appSourceSweepExceptions.
func TestAppSourceSweepsUseTheSharedWalk(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, path := range appTestFiles(t) {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = file
	}
	sweeps := appSourceSweeps(fset, files)
	if !slices.Contains(sweeps, "app_source_walk_test.go:appGoFiles") {
		t.Fatalf("the meta-guard did not see the shared walk itself; sweeps = %v", sweeps)
	}
	for _, problem := range appSourceSweepProblems(sweeps, appSourceSweepExceptions) {
		t.Error(problem)
	}
}

// TestAppSourceSweepsPositiveControl proves the meta-guard catches each
// listing shape a sweep has used, and leaves a runtime directory read alone.
func TestAppSourceSweepsPositiveControl(t *testing.T) {
	t.Parallel()
	const source = `package app

import (
	"os"
	iofs "io/fs"
	"path/filepath"
)

func readDot() { os.ReadDir(".") }

func globGo() { filepath.Glob("*.go") }

func globInstalled() { filepath.Glob("*_installed_test.go") }

func getwdList() {
	dir, _ := os.Getwd()
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		_ = filepath.Ext(entry.Name()) == ".go"
	}
}

func aliasedWalk(root string) {
	iofs.WalkDir(os.DirFS(root), ".", nil)
	_ = "x_test.go"
}

func runtimeRead(home string) { os.ReadDir(home) }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe_test.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	sweeps := appSourceSweeps(fset, map[string]*ast.File{"probe_test.go": file})
	want := []string{
		"probe_test.go:aliasedWalk",
		"probe_test.go:getwdList",
		"probe_test.go:globGo",
		"probe_test.go:globInstalled",
		"probe_test.go:readDot",
	}
	if !slices.Equal(sweeps, want) {
		t.Fatalf("sweeps = %v, want %v", sweeps, want)
	}
	problems := appSourceSweepProblems(sweeps, map[string]string{"probe_test.go:globGo": "reason", "gone_test.go:f": "reason"})
	wantProblems := []string{
		"gone_test.go:f is an appSourceSweepExceptions row no sweep matches; drop it",
		"probe_test.go:aliasedWalk lists a directory to read Go source; read internal/app through appSourceFiles or appTestFiles",
		"probe_test.go:getwdList lists a directory to read Go source; read internal/app through appSourceFiles or appTestFiles",
		"probe_test.go:globInstalled lists a directory to read Go source; read internal/app through appSourceFiles or appTestFiles",
		"probe_test.go:readDot lists a directory to read Go source; read internal/app through appSourceFiles or appTestFiles",
	}
	if !slices.Equal(problems, wantProblems) {
		t.Fatalf("problems = %q, want %q", problems, wantProblems)
	}
}
