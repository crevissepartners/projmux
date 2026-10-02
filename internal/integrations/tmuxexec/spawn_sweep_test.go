package tmuxexec

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nonTmuxSpawnSites is the closed set of functions in tmux-capable packages
// (see TestTmuxSpawnSitesAreClosed) that start a process without going through this
// package, keyed by "<package dir>.<Receiver>.<Function>" (or
// "<package dir>.<Function>"). Each one names a program that is never tmux.
// Anything else in those packages must use Command or CommandContext, so a
// tmux command client always gets the UTF-8 client flag.
var nonTmuxSpawnSites = map[string]string{
	// projmux's own executable or its helper routes
	"internal/app.launchAgentMessageRelease":              "own executable: internal message release route",
	"internal/app.startCodexLifecycleObserverProcess":     "own executable: Codex lifecycle observer route",
	"internal/app.runClaudeDialogueExec":                  "own executable observer and the Claude dialogue argv",
	"internal/app.startClaudeEndpointHelper":              "own executable: Claude endpoint helper route",
	"internal/app.runClaudeReplyTool":                     "own executable: Claude reply tool argv",
	"internal/app.startCodexBrokerRuntimeProcessForRoute": "own executable: Codex broker runtime route",
	"internal/app.restartKeyBrokerProcess":                "own executable: macOS key broker restart",
	// provider and Pane programs
	"internal/app.execCommittedActivation": "the committed provider activation argv that becomes the Pane program",
	"internal/app.startSupervisedChild":    "the supervised Pane program argv; its output belongs to the Pane",
	// terminal and other fixed tools
	"internal/app.popupSttyApply":      "stty",
	"internal/app.popupSttyStdinGet":   "stty",
	"internal/app.defaultPopupRawMode": "stty",
	"internal/app.runSttyOn":           "stty",
	"internal/app.runSwitchGitCommand": "git: detectGitBranchWithRunner passes git only",
	// user-supplied command lines
	"internal/app.defaultEditorRunner": "the user's editor",
}

// spawnFuncs are the standard-library entry points that start a process,
// keyed by import path.
var spawnFuncs = map[string][]string{
	"os/exec":               {"Command", "CommandContext"},
	"syscall":               {"Exec", "ForkExec", "StartProcess"},
	"golang.org/x/sys/unix": {"Exec"},
	"os":                    {"StartProcess"},
}

// sweepRoots are the source trees built into projmux. internal/testutil holds
// test-only support that the binary never links.
var (
	sweepRoots   = []string{"cmd", "internal"}
	sweepSkipped = []string{"internal/testutil", "internal/integrations/tmuxexec"}
)

type spawnSite struct {
	pkgDir   string
	symbol   string
	call     string
	position string
	program  string // the literal program name, or "" when it is not a literal
}

// TestTmuxSpawnSitesAreClosed is the closed-set check behind the package
// guarantee. It inspects the tmux-capable packages: a package whose non-test
// source holds the string literal "tmux" (or a path ending in /tmux) or that
// imports a tmux integration package. Every process spawn in those packages
// sits in a function listed in nonTmuxSpawnSites, and every listed function
// still spawns there. In any package, a spawn whose program is the literal
// "tmux" fails.
//
// Non-Guarantee: a package that runs tmux with neither the literal nor such an
// import (a program name read from settings, say) is not inspected.
func TestTmuxSpawnSitesAreClosed(t *testing.T) {
	sweep := sweepSpawnSites(t, repoRoot(t))
	if len(sweep.sites) == 0 || len(sweep.capable) == 0 {
		t.Fatal("found no process spawns or tmux-capable packages; the sweep is not reading the source tree")
	}
	seen := map[string]bool{}
	for _, site := range sweep.sites {
		if site.program == "tmux" {
			t.Errorf("%s: %s starts %s(\"tmux\", ...) outside tmuxexec; use tmuxexec.Command or tmuxexec.CommandContext", site.position, site.symbol, site.call)
			continue
		}
		if !sweep.capable[site.pkgDir] {
			continue
		}
		seen[site.symbol] = true
		if _, ok := nonTmuxSpawnSites[site.symbol]; !ok {
			t.Errorf("%s: %s starts a process with %s outside tmuxexec in tmux-capable package %s; start it with tmuxexec.Command or tmuxexec.CommandContext, or, if it can never run tmux, add %q to nonTmuxSpawnSites with the reason", site.position, site.symbol, site.call, site.pkgDir, site.symbol)
		}
	}
	for symbol := range nonTmuxSpawnSites {
		if !seen[symbol] {
			t.Errorf("nonTmuxSpawnSites lists %s, which no longer starts a process in a tmux-capable package; remove it", symbol)
		}
	}
}

// tmuxIntegrationImports are the packages whose importers are tmux-capable.
var tmuxIntegrationImports = []string{
	"github.com/crevissepartners/projmux/internal/integrations/tmux",
	"github.com/crevissepartners/projmux/internal/integrations/mux",
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

type spawnSweep struct {
	sites   []spawnSite
	capable map[string]bool // package dir -> tmux-capable
}

func sweepSpawnSites(t *testing.T, root string) spawnSweep {
	t.Helper()
	sweep := spawnSweep{capable: map[string]bool{}}
	fset := token.NewFileSet()
	for _, sweepRoot := range sweepRoots {
		err := filepath.WalkDir(filepath.Join(root, sweepRoot), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if entry.IsDir() {
				if slices.Contains(sweepSkipped, rel) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			pkgDir := filepath.ToSlash(filepath.Dir(rel))
			if tmuxCapableFile(file) {
				sweep.capable[pkgDir] = true
			}
			sweep.sites = append(sweep.sites, fileSpawnSites(fset, file, pkgDir)...)
			return nil
		})
		if err != nil {
			t.Fatalf("sweep %s: %v", sweepRoot, err)
		}
	}
	return sweep
}

// tmuxCapableFile reports whether a file holds the string literal "tmux" (or a
// path ending in /tmux) or imports a tmux integration package.
func tmuxCapableFile(file *ast.File) bool {
	for _, spec := range file.Imports {
		if path, err := strconv.Unquote(spec.Path.Value); err == nil && slices.Contains(tmuxIntegrationImports, path) {
			return true
		}
	}
	capable := false
	ast.Inspect(file, func(node ast.Node) bool {
		if capable {
			return false
		}
		if _, ok := node.(*ast.ImportSpec); ok {
			return false
		}
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(literal.Value); err == nil && (value == "tmux" || strings.HasSuffix(value, "/tmux")) {
			capable = true
		}
		return true
	})
	return capable
}

func fileSpawnSites(fset *token.FileSet, file *ast.File, pkgDir string) []spawnSite {
	// local import name -> spawn functions reachable through it
	locals := map[string][]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		funcs, ok := spawnFuncs[path]
		if !ok {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		locals[name] = append(locals[name], funcs...)
	}
	if len(locals) == 0 {
		return nil
	}
	var sites []spawnSite
	for _, declaration := range file.Decls {
		symbol := pkgDir + "." + declarationName(declaration)
		ast.Inspect(declaration, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CallExpr:
				if call, ok := spawnSelector(n.Fun, locals); ok {
					sites = append(sites, spawnSite{pkgDir: pkgDir, symbol: symbol, call: call, position: fset.Position(n.Pos()).String(), program: programLiteral(call, n.Args)})
					return false
				}
			case *ast.SelectorExpr:
				// A spawn function taken as a value, e.g. var run = exec.Command.
				if call, ok := spawnSelector(n, locals); ok {
					sites = append(sites, spawnSite{pkgDir: pkgDir, symbol: symbol, call: call, position: fset.Position(n.Pos()).String()})
				}
			case *ast.CompositeLit:
				// exec.Cmd{Path: ...} starts a process without exec.Command.
				if selector, ok := n.Type.(*ast.SelectorExpr); ok && selector.Sel.Name == "Cmd" {
					if pkg, ok := selector.X.(*ast.Ident); ok && slices.Contains(locals[pkg.Name], "CommandContext") {
						sites = append(sites, spawnSite{pkgDir: pkgDir, symbol: symbol, call: pkg.Name + ".Cmd{}", position: fset.Position(n.Pos()).String()})
					}
				}
			}
			return true
		})
	}
	return sites
}

func spawnSelector(expr ast.Expr, locals map[string][]string) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || !slices.Contains(locals[pkg.Name], selector.Sel.Name) {
		return "", false
	}
	return pkg.Name + "." + selector.Sel.Name, true
}

// programLiteral returns the program a spawn call names when it is a string
// literal: the first argument, or the one after the context.
func programLiteral(call string, args []ast.Expr) string {
	index := 0
	if strings.HasSuffix(call, ".CommandContext") {
		index = 1
	}
	if index >= len(args) {
		return ""
	}
	literal, ok := args[index].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return filepath.Base(value)
}

func declarationName(declaration ast.Decl) string {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil || len(d.Recv.List) == 0 {
			return d.Name.Name
		}
		return receiverName(d.Recv.List[0].Type) + "." + d.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, spec := range d.Specs {
			if value, ok := spec.(*ast.ValueSpec); ok {
				for _, name := range value.Names {
					names = append(names, name.Name)
				}
			}
		}
		return fmt.Sprintf("%s(%s)", d.Tok, strings.Join(names, ","))
	}
	return "?"
}

func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverName(e.X)
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return "?"
}
