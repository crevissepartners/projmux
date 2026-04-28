package app

import (
	"errors"
	"path/filepath"
	"testing"
)

func emptyTmuxOption() string { return "" }

func TestSwitchRepoRootPrefersProjdirEnv(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		switch name {
		case projdirEnvVar:
			return "/from/projdir"
		case repoRootEnvVar:
			return "/from/rp"
		default:
			return ""
		}
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

	lookup := func(name string) string {
		if name == repoRootEnvVar {
			return "/from/rp"
		}
		return ""
	}
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

func TestSwitchRepoRootFallsBackToRPEnvWhenProjdirAndTmuxEmpty(t *testing.T) {
	t.Parallel()

	lookup := func(name string) string {
		switch name {
		case projdirEnvVar:
			return ""
		case repoRootEnvVar:
			return "/from/rp"
		default:
			return ""
		}
	}
	load := func(string) (string, error) { return "/from/saved", nil }
	var savedValue string
	save := func(_, value string) error { savedValue = value; return nil }

	got := switchRepoRoot("/home/tester", lookup, emptyTmuxOption, load, save)
	if got != "/from/rp" {
		t.Fatalf("switchRepoRoot() = %q, want %q", got, "/from/rp")
	}
	if savedValue != "/from/rp" {
		t.Fatalf("savedValue = %q, want %q", savedValue, "/from/rp")
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
