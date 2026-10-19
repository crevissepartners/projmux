package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func appWalkFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{
		"z.go", "a_test.go", "alpha.go", "note.txt", "skip.go",
		"nested/b.go", "nested/deeper/c.go", "nested/_private/no.go",
		".hidden/hidden.go", "_private/private.go", "testdata/data.go",
		"alpha/a.go", "omega/z.go", "omega/a_test.go", "omega/note.txt",
		"omega/.hidden/h.go", "omega/_private/p.go", "omega/testdata/d.go",
		"omega/nested/n.go",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAppGoFilesDefaultRoots(t *testing.T) {
	t.Chdir(appWalkFixture(t))
	for _, tc := range []struct {
		name string
		keep func(string) bool
		want []string
	}{
		{"all", func(string) bool { return true }, []string{
			"a_test.go", "alpha.go", "alpha/a.go", "nested/b.go", "nested/deeper/c.go",
			"omega/a_test.go", "omega/nested/n.go", "omega/z.go", "skip.go", "z.go",
		}},
		{"source", func(name string) bool { return !strings.HasSuffix(name, "_test.go") && name != "skip.go" }, []string{
			"alpha.go", "alpha/a.go", "nested/b.go", "nested/deeper/c.go", "omega/nested/n.go", "omega/z.go", "z.go",
		}},
		{"tests", func(name string) bool { return strings.HasSuffix(name, "_test.go") }, []string{"a_test.go", "omega/a_test.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appGoFiles(t, tc.keep); !slices.Equal(got, tc.want) {
				t.Fatalf("default roots (%s): got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestAppGoFilesSelectedRoots(t *testing.T) {
	root := appWalkFixture(t)
	t.Chdir(root)
	source := func(name string) bool { return !strings.HasSuffix(name, "_test.go") && name != "skip.go" }
	for _, tc := range []struct {
		name  string
		roots []string
		keep  func(string) bool
		want  []string
	}{
		{"reverse roots", []string{"omega", "alpha"}, source, []string{
			"omega/.hidden/h.go", "omega/_private/p.go", "omega/nested/n.go", "omega/testdata/d.go", "omega/z.go", "alpha/a.go",
		}},
		{"mixed paths", []string{filepath.Join(root, "omega"), "alpha"}, source, []string{
			filepath.ToSlash(filepath.Join(root, "omega/.hidden/h.go")),
			filepath.ToSlash(filepath.Join(root, "omega/_private/p.go")),
			filepath.ToSlash(filepath.Join(root, "omega/nested/n.go")),
			filepath.ToSlash(filepath.Join(root, "omega/testdata/d.go")),
			filepath.ToSlash(filepath.Join(root, "omega/z.go")), "alpha/a.go",
		}},
		{"explicit dot", []string{"."}, source, []string{
			".hidden/hidden.go", "_private/private.go", "alpha/a.go", "alpha.go", "nested/_private/no.go", "nested/b.go", "nested/deeper/c.go",
			"omega/.hidden/h.go", "omega/_private/p.go", "omega/nested/n.go", "omega/testdata/d.go", "omega/z.go", "testdata/data.go", "z.go",
		}},
		{"test filter", []string{"omega", "alpha"}, func(name string) bool { return strings.HasSuffix(name, "_test.go") }, []string{"omega/a_test.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appGoFiles(t, tc.keep, tc.roots...); !slices.Equal(got, tc.want) {
				t.Fatalf("explicit roots %q (%s): got %q, want %q", tc.roots, tc.name, got, tc.want)
			}
		})
	}
}

func TestAppGoFilesFailures(t *testing.T) {
	// Run fatal paths in a child test process so a real testing.T records the
	// diagnostic and termination without failing the parent test.
	const childKey = "APP_GO_FILES_FAILURE_CASE"
	if scenario := os.Getenv(childKey); scenario != "" {
		root := t.TempDir()
		t.Chdir(root)
		keep := func(string) bool { return true }
		switch scenario {
		case "default empty":
			appGoFiles(t, keep)
		case "default error":
			if err := os.Chmod(root, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
			appGoFiles(t, keep)
		case "explicit error":
			appGoFiles(t, keep, "missing-root")
		case "explicit empty":
			appGoFiles(t, keep, ".", root)
		case "explicit rejected":
			if err := os.WriteFile("rejected.go", []byte("package fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			appGoFiles(t, func(string) bool { return false }, ".")
		}
		t.Fatal("walker unexpectedly returned")
	}
	for _, tc := range []struct{ name, diagnostic string }{
		{"default empty", "walk internal/app found no Go files"},
		{"default error", "walk internal/app: "},
		{"explicit error", "walk Go source root missing-root: "},
		{"explicit empty", "walk explicit source roots found no Go files"},
		{"explicit rejected", "walk explicit source roots found no Go files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "default error" && os.Geteuid() == 0 {
				t.Skip("root bypasses directory read permissions")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestAppGoFilesFailures$", "-test.count=1")
			cmd.Env = append(os.Environ(), childKey+"="+tc.name)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.diagnostic) || strings.Contains(string(output), "walker unexpectedly returned") {
				t.Fatalf("%s: error %v, want diagnostic %q and fatal termination; output:\n%s", tc.name, err, tc.diagnostic, output)
			}
		})
	}
}
