package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestAISplitModeFallbacks(t *testing.T) {
	for _, tc := range []struct{ tui, central, mode, source string }{
		{"codex", "claude", "codex", "tui"}, {"  resume\n", "shell", "resume", "tui"},
		{"", "claude", "claude", "central"}, {"invalid", "shell", "shell", "central"},
		{"directory", "resume", "resume", "central"}, {"absent", "codex", "codex", "central"},
		{"absent", "directory", "selective", "default"}, {"invalid", "invalid", "selective", "default"},
		{"absent", "absent", "selective", "default"},
	} {
		t.Run(tc.tui+"/"+tc.central, func(t *testing.T) {
			p := config.Paths{ConfigDir: t.TempDir()}
			for path, value := range map[string]string{p.TmuxAISplitModeFile(): tc.tui, p.AINewWindowModeFile(): tc.central} {
				switch value {
				case "absent":
				case "directory":
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := (AISplitMode{Paths: func() (config.Paths, error) { return p, nil }}).Get()
			if got != (AISplitModeState{tc.mode, tc.source}) {
				t.Fatal(got)
			}
		})
	}
	sentinel := errors.New("path unavailable")
	if got := (AISplitMode{Paths: func() (config.Paths, error) { return config.Paths{}, sentinel }}).Get(); got != (AISplitModeState{"selective", "default"}) {
		t.Fatal(got)
	}
	// A failed TUI path lookup still permits the independent central fallback.
	p := config.Paths{ConfigDir: t.TempDir()}
	calls := 0
	if err := os.WriteFile(p.AINewWindowModeFile(), []byte("shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := (AISplitMode{Paths: func() (config.Paths, error) {
		calls++
		if calls == 1 {
			return config.Paths{}, sentinel
		}
		return p, nil
	}}).Get()
	if got != (AISplitModeState{"shell", "central"}) {
		t.Fatal(got)
	}
}

func TestAISplitModePlainWriteAndDisplayOrder(t *testing.T) {
	p := config.Paths{ConfigDir: filepath.Join(t.TempDir(), "config")}
	s := AISplitMode{Paths: func() (config.Paths, error) { return p, nil }}
	displays := 0
	s.Display = func(message string) error {
		displays++
		b, err := os.ReadFile(p.TmuxAISplitModeFile())
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "selective\n" || message != "ai split default: selective" {
			t.Fatalf("%q %q", b, message)
		}
		return errors.New("display failure is ignored")
	}
	if v, err := s.Set("unknown"); err != nil || v != (AISplitModeState{"selective", "tui"}) {
		t.Fatalf("%+v %v", v, err)
	}
	// Compare to a reference creation with the same 0644 argument under this umask.
	ref := filepath.Join(t.TempDir(), "reference")
	if err := os.WriteFile(ref, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(p.TmuxAISplitModeFile())
	b, _ := os.Stat(ref)
	if a.Mode().Perm() != b.Mode().Perm() {
		t.Fatal(a.Mode(), b.Mode())
	}
	if err := os.Chmod(p.TmuxAISplitModeFile(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("bad"); err != nil {
		t.Fatal(err)
	}
	a, _ = os.Stat(p.TmuxAISplitModeFile())
	if a.Mode().Perm() != 0o600 || displays != 2 {
		t.Fatal(a.Mode(), displays)
	}
	if err := os.Remove(p.TmuxAISplitModeFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p.TmuxAISplitModeFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("codex"); err == nil || displays != 2 {
		t.Fatal(err, displays)
	}
	sentinel := errors.New("missing home")
	s.Paths = func() (config.Paths, error) { return config.Paths{}, sentinel }
	if _, err := s.Set("shell"); !errors.Is(err, sentinel) || err.Error() != "save AI split default: missing home" {
		t.Fatal(err)
	}
	// Parent creation failure also never displays.
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Paths = func() (config.Paths, error) { return config.Paths{ConfigDir: filepath.Join(block, "child")}, nil }
	if _, err := s.Set("codex"); err == nil || displays != 2 {
		t.Fatal(err, displays)
	}
}

func TestAISplitModeDisplayReadsEnvironmentAfterSave(t *testing.T) {
	p := config.Paths{ConfigDir: t.TempDir()}
	saved := false
	var keys, args []string
	s := AISplitMode{Paths: func() (config.Paths, error) { return p, nil }, Display: func(message string) error {
		b, err := os.ReadFile(p.TmuxAISplitModeFile())
		if err != nil || string(b) != "codex\n" {
			t.Fatal(string(b), err)
		}
		saved = true
		return DisplayAISplitModeMessage(message, func(key string) string {
			if !saved {
				t.Fatal("lookup before save")
			}
			keys = append(keys, key)
			return "inside"
		}, func(name string, a ...string) error { args = append([]string{name}, a...); return nil })
	}}
	if _, err := s.Set("codex"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"TMUX"}) || !reflect.DeepEqual(args, []string{"tmux", "display-message", "ai split default: codex"}) {
		t.Fatal(keys, args)
	}
}
