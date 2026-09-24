package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAINewWindowModeFilePath(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{DefaultPaths("/x/config", "/x/state").AINewWindowModeFile(), "/x/config/projmux/ai-new-window-mode"},
		{DefaultPaths(".config", "").AINewWindowModeFile(), ".config/projmux/ai-new-window-mode"},
	} {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestLoadAINewWindowModeFileUnsetWhenMissingOrEmptyPath(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), AINewWindowModeFileName)} {
		mode, saved, err := LoadAINewWindowModeFile(path)
		if err != nil || saved || mode != "" {
			t.Fatalf("LoadAINewWindowModeFile(%q) = %q, %v, %v; want unset", path, mode, saved, err)
		}
	}
}

func TestSaveAINewWindowModeFileRoundTripsEveryMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projmux", AINewWindowModeFileName)
	for _, want := range AINewWindowModes {
		if err := SaveAINewWindowModeFile(path, want); err != nil {
			t.Fatalf("SaveAINewWindowModeFile(%q) error = %v", want, err)
		}
		got, saved, err := LoadAINewWindowModeFile(path)
		if err != nil || !saved || got != want {
			t.Fatalf("load after saving %q = %q, %v, %v", want, got, saved, err)
		}
	}
}

func TestSaveAINewWindowModeFileRefusesInvalidModes(t *testing.T) {
	for _, value := range []string{"", "bogus", "Claude"} {
		path := filepath.Join(t.TempDir(), AINewWindowModeFileName)
		if err := SaveAINewWindowModeFile(path, value); err == nil {
			t.Fatalf("SaveAINewWindowModeFile(%q) error = nil, want refusal", value)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SaveAINewWindowModeFile(%q) created the file: %v", value, err)
		}

		if err := SaveAINewWindowModeFile(path, "codex"); err != nil {
			t.Fatal(err)
		}
		if err := SaveAINewWindowModeFile(path, value); err == nil {
			t.Fatalf("SaveAINewWindowModeFile(%q) over a saved mode error = nil", value)
		}
		if mode, saved, err := LoadAINewWindowModeFile(path); err != nil || !saved || mode != "codex" {
			t.Fatalf("saved mode after refusing %q = %q, %v, %v; want codex kept", value, mode, saved, err)
		}
	}
}

func TestSaveAINewWindowModeFileRequiresAPath(t *testing.T) {
	if err := SaveAINewWindowModeFile("", "codex"); !errors.Is(err, ErrHomeDirRequired) {
		t.Fatalf("SaveAINewWindowModeFile(\"\") error = %v, want ErrHomeDirRequired", err)
	}
}

func TestLoadAINewWindowModeFileReadsInvalidContentAsUnset(t *testing.T) {
	for _, content := range []string{"", "\n", "bogus\n", "Claude\n", "codex shell\n"} {
		path := filepath.Join(t.TempDir(), AINewWindowModeFileName)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		mode, saved, err := LoadAINewWindowModeFile(path)
		if err != nil || saved || mode != "" {
			t.Fatalf("load of %q = %q, %v, %v; want unset", content, mode, saved, err)
		}
	}
}

func TestLoadAINewWindowModeFileTrimsWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), AINewWindowModeFileName)
	if err := os.WriteFile(path, []byte("  shell \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode, saved, err := LoadAINewWindowModeFile(path); err != nil || !saved || mode != "shell" {
		t.Fatalf("load = %q, %v, %v; want shell", mode, saved, err)
	}
}
