package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func emptyTmuxOption() string { return "" }

func TestSwitchRepoRootPrefersProjdirEnv(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		if name == projdirEnvVar {
			return "/from/projdir"
		}
		return ""
	}
	tmuxOption := func() string { return "/from/tmux" }
	load := func(string) (string, error) { return "/from/saved", nil }
	saveCalls := 0
	save := func(string, string) error { saveCalls++; return nil }

	got := switchRepoRoot("/home/tester", lookup, tmuxOption, load, save)
	if got != "/from/projdir" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/projdir")
	}
	if saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", saveCalls)
	}
}

func TestSwitchRepoRootUsesTmuxOptionWhenProjdirEmpty(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	tmuxOption := func() string { return "/from/tmux" }
	load := func(string) (string, error) { return "/from/saved", nil }
	save := func(string, string) error {
		t.Fatalf("save should not be called for tmux option source")
		return nil
	}

	got := switchRepoRoot("/home/tester", lookup, tmuxOption, load, save)
	if got != "/from/tmux" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/tmux")
	}
}

func TestSwitchRepoRootTmuxOptionPreferredOverSavedFile(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	tmuxOption := func() string { return "/from/tmux" }
	load := func(string) (string, error) { return "/from/saved", nil }
	save := func(string, string) error {
		t.Fatalf("save should not be called when env unset")
		return nil
	}

	got := switchRepoRoot("/home/tester", lookup, tmuxOption, load, save)
	if got != "/from/tmux" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/tmux")
	}
}

func TestSwitchRepoRootUsesSavedFileWhenEnvUnset(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	load := func(string) (string, error) { return "/from/saved", nil }
	save := func(string, string) error {
		t.Fatalf("save should not be called when env unset")
		return nil
	}

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	if got != "/from/saved" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/saved")
	}
}

func TestSwitchRepoRootFallsBackToHomeDirWhenAllUnset(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	load := func(string) (string, error) { return "", nil }
	save := func(string, string) error { return nil }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	want := filepath.Clean(filepath.Join("/home/tester", "source", "repos"))
	if got != want {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, want)
	}
}

func TestSwitchRepoRootIgnoresLoadErrorAndUsesFallback(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	load := func(string) (string, error) { return "", errors.New("io error") }
	save := func(string, string) error { return nil }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	want := filepath.Clean(filepath.Join("/home/tester", "source", "repos"))
	if got != want {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, want)
	}
}

func TestSwitchRepoRootSkipsSaveWhenSavedMatches(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		if name == projdirEnvVar {
			return "/already/saved"
		}
		return ""
	}
	load := func(string) (string, error) { return "/already/saved", nil }
	saveCalls := 0
	save := func(string, string) error { saveCalls++; return nil }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	if got != "/already/saved" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/already/saved")
	}
	if saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", saveCalls)
	}
}

func TestSwitchRepoRootSwallowsSaveError(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		if name == projdirEnvVar {
			return "/from/projdir"
		}
		return ""
	}
	load := func(string) (string, error) { return "", nil }
	save := func(string, string) error { return errors.New("disk full") }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	if got != "/from/projdir" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/projdir")
	}
}

func TestSwitchRepoRootEmptyHomeWithEmptyEnvReturnsEmpty(t *testing.T) {
	t.Parallel()

	got := switchRepoRoot("", func(string) string { return "" }, emptyTmuxOption, nil, nil)
	if got != "" {
		t.Fatalf("switchRepoRoot() = %q, want empty", got)
	}
}

func TestSwitchRepoRootTmuxOptionDoesNotMemoize(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	tmuxOption := func() string { return "/from/tmux" }
	load := func(string) (string, error) { return "", nil }
	saveCalls := 0
	save := func(string, string) error { saveCalls++; return nil }

	got := switchRepoRoot("/home/tester", lookup, tmuxOption, load, save)
	if got != "/from/tmux" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/tmux")
	}
	if saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0 (tmux option must not memoize)", saveCalls)
	}
}

func TestSwitchRepoRootMultiPathReturnsFirstEntry(t *testing.T) {
	t.Parallel()

	multi := strings.Join([]string{"/main/repo", "/extra/one", "/extra/two"}, string(os.PathListSeparator))
	lookup := func(name string) string {
		if name == projdirEnvVar {
			return multi
		}
		return ""
	}
	load := func(string) (string, error) { return "", nil }
	var savedValue string
	save := func(_, value string) error { savedValue = value; return nil }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	if got != "/main/repo" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/main/repo")
	}
	if savedValue != "/main/repo" {
		t.Fatalf("savedValue = %q, want only primary path", savedValue)
	}
}

func TestSwitchRepoRootMultiPathSkipsEmptyLeadingEntries(t *testing.T) {
	t.Parallel()

	// Leading empty entries (e.g. PROJMUX_PROJDIR=":/main/repo") must
	// be skipped so the primary path is the first non-empty value.
	multi := strings.Join([]string{"", "/main/repo"}, string(os.PathListSeparator))
	lookup := func(name string) string {
		if name == projdirEnvVar {
			return multi
		}
		return ""
	}
	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, func(string) (string, error) { return "", nil }, func(string, string) error { return nil })
	if got != "/main/repo" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/main/repo")
	}
}

func TestExtraProjdirRootsReturnsTrailingPaths(t *testing.T) {
	t.Parallel()

	multi := strings.Join([]string{"/main/repo", "/extra/one", "", "/extra/two"}, string(os.PathListSeparator))
	lookup := func(name string) string {
		if name == projdirEnvVar {
			return multi
		}
		return ""
	}

	got := extraProjdirRoots(lookup)
	want := []string{"/extra/one", "/extra/two"}
	if len(got) != len(want) {
		t.Fatalf("extraProjdirRoots() = %#v, want %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("extraProjdirRoots()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtraProjdirRootsEmptyWhenSinglePath(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		if name == projdirEnvVar {
			return "/main/repo"
		}
		return ""
	}
	if got := extraProjdirRoots(lookup); len(got) != 0 {
		t.Fatalf("extraProjdirRoots() = %#v, want empty for single path", got)
	}
}

func TestExtraProjdirRootsEmptyWhenUnset(t *testing.T) {
	t.Parallel()

	lookup := func(string) string { return "" }
	if got := extraProjdirRoots(lookup); len(got) != 0 {
		t.Fatalf("extraProjdirRoots() = %#v, want empty when env unset", got)
	}
}

func TestFirstProjdirPathReturnsFirstNonEmpty(t *testing.T) {
	t.Parallel()

	multi := strings.Join([]string{"", "/first", "/second"}, string(os.PathListSeparator))
	if got := firstProjdirPath(multi); got != "/first" {
		t.Fatalf("firstProjdirPath() = %q, want %q", got, "/first")
	}
}

func TestFirstProjdirPathEmptyForBlankInput(t *testing.T) {
	t.Parallel()

	if got := firstProjdirPath(""); got != "" {
		t.Fatalf("firstProjdirPath(\"\") = %q, want empty", got)
	}
	blank := strings.Repeat(string(os.PathListSeparator), 3)
	if got := firstProjdirPath(blank); got != "" {
		t.Fatalf("firstProjdirPath(only-separators) = %q, want empty", got)
	}
}
