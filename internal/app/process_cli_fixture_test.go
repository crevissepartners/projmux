package app

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

// copiedCLI returns the copied product binary named by env and skips without
// it. Callers pass the variable name literally so the process CLI runner still
// finds each consuming file.
func copiedCLI(t testing.TB, env string) string {
	t.Helper()
	product := os.Getenv(env)
	if product == "" {
		t.Skipf("set %s to a copied product binary", env)
	}
	return product
}

// patchFixture replaces the first anchor in a fake provider source and fails
// the test when the anchor is absent, so a drifted fixture cannot silently
// keep its old behavior.
func patchFixture(t testing.TB, source, anchor, replacement string) string {
	t.Helper()
	if !strings.Contains(source, anchor) {
		t.Fatalf("fixture patch anchor not found: %q", anchor)
	}
	return strings.Replace(source, anchor, replacement, 1)
}

// patchFixtureAll replaces every anchor and fails when there is none.
func patchFixtureAll(t testing.TB, source, anchor, replacement string) string {
	t.Helper()
	if !strings.Contains(source, anchor) {
		t.Fatalf("fixture patch anchor not found: %q", anchor)
	}
	return strings.ReplaceAll(source, anchor, replacement)
}

// mustPatchFixture is patchFixture for constant fixture builders that have no
// test in scope; a missing anchor panics and so still fails the package.
func mustPatchFixture(source, anchor, replacement string) string {
	if !strings.Contains(source, anchor) {
		panic(fmt.Sprintf("fixture patch anchor not found: %q", anchor))
	}
	return strings.Replace(source, anchor, replacement, 1)
}

type fixturePatchRecorder struct {
	testing.TB
	failure string
}

func (r *fixturePatchRecorder) Helper() {}

func (r *fixturePatchRecorder) Fatalf(format string, args ...any) {
	r.failure = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestProcessFixturePatchRequiresAnchor(t *testing.T) {
	for name, patch := range map[string]func(testing.TB, string, string, string) string{
		"first": patchFixture,
		"all":   patchFixtureAll,
	} {
		t.Run(name, func(t *testing.T) {
			if got := patch(t, "a=1;a=1", "a=1", "b=2"); got != map[string]string{"first": "b=2;a=1", "all": "b=2;b=2"}[name] {
				t.Fatalf("anchored patch = %q", got)
			}
			recorder := &fixturePatchRecorder{TB: t}
			done := make(chan string)
			returned := false
			go func() {
				defer func() { done <- recorder.failure }()
				patch(recorder, "a=1", "missing", "b=2")
				returned = true
			}()
			if failure := <-done; !strings.Contains(failure, `"missing"`) || returned {
				t.Fatalf("missing anchor failure = %q, returned = %v", failure, returned)
			}
		})
	}
	t.Run("constant", func(t *testing.T) {
		if got := mustPatchFixture("a=1;a=1", "a=1", "b=2"); got != "b=2;a=1" {
			t.Fatalf("anchored patch = %q", got)
		}
		defer func() {
			if failure := fmt.Sprint(recover()); !strings.Contains(failure, `"missing"`) {
				t.Fatalf("missing anchor panic = %q", failure)
			}
		}()
		mustPatchFixture("a=1", "missing", "b=2")
		t.Fatal("missing anchor returned")
	})
}
