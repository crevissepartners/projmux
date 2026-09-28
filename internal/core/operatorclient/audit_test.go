package operatorclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// namedClientsNeverInProduct are operator client names no non-test Go file may
// spell. The operator client is a name value under this package's rule, so a
// build that knew one client by name would judge, route, or word operator input
// for that client alone. Records naming one stay readable because the rule, not
// the name, decides.
var namedClientsNeverInProduct = []string{"web"}

// clientWord matches name as a whole word of a string literal: "name",
// "--via name", "the projmux name client", "<popup|cli|name>". Letters,
// digits, '_' and '-' extend a word, so "name-search", "nameSearch", and
// "names" are other words.
func clientWord(name string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_-])`)
}

// literalsNaming returns every string literal in file that spells name as a
// word, as "line: value". Comments are not literals and are not reported.
func literalsNaming(fset *token.FileSet, file *ast.File, name string) []string {
	word := clientWord(name)
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			value = literal.Value
		}
		if word.MatchString(value) {
			found = append(found, strconv.Itoa(fset.Position(literal.Pos()).Line)+": "+strconv.Quote(value))
		}
		return true
	})
	return found
}

// productLiteralsNaming scans every non-test Go file of the checkout at root
// and returns the literals that spell name, keyed by repository-relative path,
// with the number of files scanned.
func productLiteralsNaming(t *testing.T, root, name string) (map[string][]string, int) {
	t.Helper()
	found, scanned := map[string][]string{}, 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		base := entry.Name()
		if entry.IsDir() {
			// Worktrees, VCS data, and vendored or generated trees are not this
			// checkout's product source; neither is another checkout nested in it.
			if path != root && (strings.HasPrefix(base, ".") || base == "node_modules" || base == "testdata" || base == "vendor") {
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		if hits := literalsNaming(fset, file, name); len(hits) > 0 {
			found[filepath.ToSlash(relative)] = hits
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found, scanned
}

// TestNoProductStringNamesAnOperatorClient is C-2's negative audit: no
// non-test Go file spells a client in namedClientsNeverInProduct as a word of
// a string literal. The positive controls first prove the matcher finds a
// client name in each shape it took before (a bare value, a flag argument, a
// notice, a usage choice) without taking a longer word for it, and that the
// walk reaches the approval store, whose own channel names it must see.
func TestNoProductStringNamesAnOperatorClient(t *testing.T) {
	t.Parallel()
	const probe = `package probe

// console is a comment and not a literal.
const bare = "console"

var (
	flag    = []string{"--via", "console"}
	usage   = "requires explicit --via console (caller-reported source)"
	notice  = "Operator input that arrived through the projmux console client."
	choices = "[--via <popup|cli|console>]"
	other   = []string{"console-search", "consoleSearch", "consoles", "console_x", "webconsole"}
)
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", probe, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`4: "console"`, `7: "console"`, `8: "requires explicit --via console (caller-reported source)"`,
		`9: "Operator input that arrived through the projmux console client."`, `10: "[--via <popup|cli|console>]"`}
	if got := literalsNaming(fset, file, "console"); !slices.Equal(got, want) {
		t.Fatalf("probe literals naming console =\n%q\nwant\n%q", got, want)
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	channels, scanned := productLiteralsNaming(t, root, "popup")
	if scanned < 100 || len(channels["internal/integrations/agents/agentapproval/store.go"]) == 0 {
		t.Fatalf("audit scanned %d files and did not see the approval store's popup channel (%v), so it proves nothing", scanned, channels)
	}
	for _, name := range namedClientsNeverInProduct {
		if !Valid(name) {
			t.Fatalf("%q is not a client name under the rule; auditing it proves nothing", name)
		}
		found, _ := productLiteralsNaming(t, root, name)
		for path, hits := range found {
			t.Errorf("%s names the operator client %q; pass the client name as a value instead:\n\t%s", path, name, strings.Join(hits, "\n\t"))
		}
	}
}
