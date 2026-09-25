package app

import (
	"fmt"
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

// Product git calls run under deadlines and can be SIGKILLed mid-flight. A
// killed git that held the index lock leaves .git/index.lock behind and breaks
// the operator's next commit, so every git subcommand product code runs must
// be one that never takes that lock.

// gitLockFreeSubcommands is the closed allow set. A subcommand maps to the
// literal flag it additionally requires, or "" when it is lock-free as is.
var gitLockFreeSubcommands = map[string]string{
	"rev-parse":    "",
	"symbolic-ref": "",
	"config":       "--get",               // a config write takes config.lock
	"status":       "--no-optional-locks", // plain status refreshes the index under index.lock
}

// gitLiteralNonExecCallees name calls that take a "git" literal without
// running git. A package call is keyed by its qualified pkg.Name, so another
// package's LookPath is not excluded; a method or local func by its bare name.
// The table is closed: an entry that matches nothing in the tree fails the guard.
var gitLiteralNonExecCallees = map[string]string{
	"exec.LookPath":               "resolves git on PATH; it does not run it",
	"handlePopupToggleWithClient": "\"git\" is the popup label",
	"statusbarFieldLines":         "\"git\" is the status bar field label",
	"stringSet":                   "\"git\" is a CLI subcommand name in the diagnostics set",
}

// gitCallFinding is one call that passes a "git" literal as an argument.
type gitCallFinding struct {
	pos        token.Position
	callee     string
	subcommand string // "" when no literal subcommand could be read
	allowed    bool
	reason     string // why the call is not allowed; empty when allowed
	excluded   string // the gitLiteralNonExecCallees key that matched; no verdict then
}

// gitCallee returns a readable name for a call's function and the
// gitLiteralNonExecCallees key it matches, if any. For x.Name the qualified
// "x.Name" is looked up first, then the bare "Name"; for an identifier, its name.
func gitCallee(expr ast.Expr) (callee, excluded string) {
	switch fn := expr.(type) {
	case *ast.SelectorExpr:
		callee = fn.Sel.Name
		if x, ok := fn.X.(*ast.Ident); ok {
			callee = x.Name + "." + fn.Sel.Name
			if _, ok := gitLiteralNonExecCallees[callee]; ok {
				return callee, callee
			}
		}
		if _, ok := gitLiteralNonExecCallees[fn.Sel.Name]; ok {
			return callee, fn.Sel.Name
		}
	case *ast.Ident:
		callee = fn.Name
		if _, ok := gitLiteralNonExecCallees[callee]; ok {
			return callee, callee
		}
	}
	return callee, ""
}

// gitStringLit returns the unquoted value of a string literal argument.
func gitStringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

// scanGitCalls returns every call in file that passes a "git" literal. A named
// non-execution callee is returned with excluded set and no verdict; every
// other call gets a verdict against the allow set.
func scanGitCalls(fset *token.FileSet, file *ast.File) []gitCallFinding {
	var out []gitCallFinding
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		gitAt := -1
		for i, arg := range call.Args {
			if value, ok := gitStringLit(arg); ok && value == "git" {
				gitAt = i
				break
			}
		}
		if gitAt < 0 {
			return true
		}
		finding := gitCallFinding{pos: fset.Position(call.Pos())}
		finding.callee, finding.excluded = gitCallee(call.Fun)
		if finding.excluded != "" {
			out = append(out, finding)
			return true
		}
		rest := call.Args[gitAt+1:]
		flags := map[string]bool{}
		for _, arg := range rest {
			if value, ok := gitStringLit(arg); ok {
				flags[value] = true
			}
		}
		// Skip global flags; -C and -c consume the next argument as their value.
		for i := 0; i < len(rest); i++ {
			value, ok := gitStringLit(rest[i])
			if !ok {
				break
			}
			if strings.HasPrefix(value, "-") {
				if value == "-C" || value == "-c" {
					i++
				}
				continue
			}
			finding.subcommand = value
			break
		}
		required, known := gitLockFreeSubcommands[finding.subcommand]
		switch {
		case finding.subcommand == "":
			finding.reason = "no literal subcommand, so the guard cannot prove it is lock-free"
		case !known:
			finding.reason = fmt.Sprintf("subcommand %q is not in the lock-free allow set", finding.subcommand)
		case required != "" && !flags[required]:
			finding.reason = fmt.Sprintf("subcommand %q is lock-free only with %s", finding.subcommand, required)
		default:
			finding.allowed = true
		}
		out = append(out, finding)
		return true
	})
	return out
}

func TestProductionGitCallsCannotTakeTheIndexLock(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	var violations []string
	executions := 0
	matchedExclusions := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(repoRoot, "internal", "testutil")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.ToSlash(rel), src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, finding := range scanGitCalls(fset, file) {
				where := fmt.Sprintf("%s:%d", finding.pos.Filename, finding.pos.Line)
				if finding.excluded != "" {
					matchedExclusions[finding.excluded] = true
					t.Logf("%s excluded %s: %s", where, finding.callee, gitLiteralNonExecCallees[finding.excluded])
					continue
				}
				executions++
				if finding.allowed {
					t.Logf("%s git %s", where, finding.subcommand)
					continue
				}
				subcommand := finding.subcommand
				if subcommand == "" {
					subcommand = "no literal subcommand"
				}
				violations = append(violations, fmt.Sprintf("%s: %s(... \"git\" ...) runs %s: %s", where, finding.callee, subcommand, finding.reason))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	for key := range gitLiteralNonExecCallees {
		if !matchedExclusions[key] {
			t.Errorf("stale exclusion %q matched no call under internal/ and cmd/; remove it from gitLiteralNonExecCallees", key)
		}
	}
	if executions == 0 {
		t.Fatal("found no git execution calls under internal/ and cmd/; the scanner or its root is wrong")
	}
	if len(violations) > 0 {
		t.Fatalf("product git calls that can take the git index lock (a deadline SIGKILL would leave .git/index.lock behind):\n  %s\n"+
			"Fix: pass --no-optional-locks (status) or --get (config), spell the subcommand as a literal, "+
			"or justify adding it to gitLockFreeSubcommands in review.",
			strings.Join(violations, "\n  "))
	}
}

func TestScanGitCallsVerdicts(t *testing.T) {
	cases := []struct {
		name       string
		call       string
		subcommand string
		allowed    bool
		excluded   string // the exclusion key expected to match; such a call gets no verdict
	}{
		{name: "rev-parse", call: `c.read("git", "-C", path, "rev-parse", "--is-inside-work-tree")`, subcommand: "rev-parse", allowed: true},
		{name: "symbolic-ref", call: `runner(ctx, "git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD")`, subcommand: "symbolic-ref", allowed: true},
		{name: "config --get", call: `c.readTrimmed("git", "-C", path, "config", "--get", "remote.origin.url")`, subcommand: "config", allowed: true},
		{name: "status without optional locks", call: `c.readTrimmed("git", "--no-optional-locks", "-C", path, "status", "--porcelain=v1")`, subcommand: "status", allowed: true},
		{name: "-c value before subcommand", call: `c.run("git", "-c", "core.quotepath=off", "rev-parse", "HEAD")`, subcommand: "rev-parse", allowed: true},
		{name: "flagless status", call: `c.readTrimmed("git", "-C", path, "status", "--porcelain")`, subcommand: "status"},
		{name: "diff", call: `c.readTrimmed("git", "-C", path, "diff", "--stat")`, subcommand: "diff"},
		{name: "config write", call: `c.run("git", "-C", path, "config", "user.name", name)`, subcommand: "config"},
		{name: "variadic args", call: `run(ctx, "git", args...)`},
		{name: "non-literal subcommand", call: `run(ctx, "git", "-C", path, sub)`},
		{name: "path lookup", call: `exec.LookPath("git")`, excluded: "exec.LookPath"},
		{name: "another package's LookPath is not excluded", call: `foo.LookPath("git")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package sample\n\nfunc f() {\n\t" + tc.call + "\n}\n"
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "sample.go", src, 0)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.call, err)
			}
			findings := scanGitCalls(fset, file)
			if len(findings) != 1 {
				t.Fatalf("findings = %+v, want exactly one", findings)
			}
			got := findings[0]
			if got.excluded != tc.excluded {
				t.Fatalf("excluded = %q (callee %q), want %q", got.excluded, got.callee, tc.excluded)
			}
			if tc.excluded != "" {
				if got.allowed || got.reason != "" {
					t.Fatalf("excluded call got a verdict: allowed=%v reason=%q", got.allowed, got.reason)
				}
				return
			}
			if got.subcommand != tc.subcommand || got.allowed != tc.allowed {
				t.Fatalf("subcommand=%q allowed=%v (reason %q), want subcommand=%q allowed=%v",
					got.subcommand, got.allowed, got.reason, tc.subcommand, tc.allowed)
			}
		})
	}
}
