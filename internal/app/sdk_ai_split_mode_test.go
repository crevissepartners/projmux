package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

// These oracles pin the pre-extraction app algorithm independently of the
// shared service, so forwarding both fronts to a changed policy cannot pass.
func legacyAISplitModeRead(path, central string) string {
	if b, err := os.ReadFile(filepath.Clean(path)); err == nil {
		if mode, ok := config.ValidAINewWindowMode(string(b)); ok {
			return mode
		}
	}
	if mode, saved, _ := config.LoadAINewWindowModeFile(central); saved {
		return mode
	}
	return "selective"
}
func legacyAISplitModeWrite(path, mode string, display func(string) error) error {
	if valid, ok := config.ValidAINewWindowMode(mode); ok {
		mode = valid
	} else {
		mode = "selective"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(mode+"\n"), 0o644); err != nil {
		return err
	}
	_ = display("ai split default: " + mode)
	return nil
}

func TestAISplitModeAppSDKLegacyFileParity(t *testing.T) {
	t.Setenv("TMUX", "")
	for _, tc := range []struct{ tui, central string }{
		{"codex", "shell"}, {"", "resume"}, {"bad", "claude"}, {"directory", "codex"}, {"absent", "directory"}, {"absent", "bad"}, {"absent", "absent"},
	} {
		t.Run(tc.tui+"/"+tc.central, func(t *testing.T) {
			root := t.TempDir()
			cmd := testAICommand(root)
			path, err := cmd.configFile()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			central := filepath.Join(dir, config.AINewWindowModeFileName)
			for path, value := range map[string]string{path: tc.tui, central: tc.central} {
				switch value {
				case "absent":
				case "directory":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
			if err != nil {
				t.Fatal(err)
			}
			want := legacyAISplitModeRead(path, central)
			if got := cmd.getMode(); got != want {
				t.Fatal(got, want)
			}
			if got := c.GetAISplitMode(); got.Mode != want {
				t.Fatal(got, want)
			}
		})
	}
	for _, mode := range []string{"  codex\n", "resume", "claude", "antigravity", "shell", "selective", "unknown"} {
		t.Run("save/"+mode, func(t *testing.T) {
			root := t.TempDir()
			cmd := testAICommand(root)
			path, err := cmd.configFile()
			if err != nil {
				t.Fatal(err)
			}
			cmd.lookupEnv = func(key string) string {
				if key == "TMUX" {
					return "fixture"
				}
				return ""
			}
			displays := 0
			cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
				displays++
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(append([]string{name}, args...), []string{"tmux", "display-message", "ai split default: " + normalizeAIMode(mode)}) || string(b) != normalizeAIMode(mode)+"\n" {
					t.Fatal(name, args, string(b))
				}
				return errors.New("best effort")
			}
			ref := filepath.Join(root, "reference")
			if err := legacyAISplitModeWrite(ref, mode, func(string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if err := cmd.setMode(mode); err != nil {
				t.Fatal(err)
			}
			a, _ := os.ReadFile(ref)
			b, _ := os.ReadFile(path)
			if string(a) != string(b) || displays != 1 {
				t.Fatal(string(a), string(b), displays)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := cmd.setMode(mode); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o600 {
				t.Fatal(info.Mode())
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := cmd.setMode(mode); err == nil || displays != 2 {
				t.Fatal(err, displays)
			}
		})
	}
}

func TestAISplitModeAppMissingPathParity(t *testing.T) {
	cmd := &aiCommand{homeDir: func() (string, error) { return "", errors.New("no home") }, lookupEnv: func(string) string { return "" }}
	if got := cmd.getMode(); got != "selective" {
		t.Fatal(got)
	}
	_, pathErr := cmd.configFile()
	err := cmd.setMode("codex")
	var missing *config.MissingHomeError
	if !errors.As(err, &missing) || err.Error() != fmt.Sprintf("save AI split default: %v", pathErr) {
		t.Fatal(err, pathErr)
	}
}

func TestAISplitModeSDKPreservesSamePathLegacySaveErrors(t *testing.T) {
	t.Setenv("TMUX", "")
	for _, kind := range []string{"mkdir", "write"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "config")
			path := filepath.Join(dir, config.TmuxAISplitModeFileName)
			if kind == "mkdir" {
				if err := os.WriteFile(dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			displays := 0
			legacy := legacyAISplitModeWrite(path, "codex", func(string) error { displays++; return nil })
			c, err := projmux.NewClient(projmux.Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.SetAISplitMode("codex")
			var oldPath, newPath *os.PathError
			var refusal *projmux.Refusal
			if legacy == nil || err == nil || !errors.As(legacy, &oldPath) || !errors.As(err, &newPath) || !errors.As(err, &refusal) || refusal.Code != projmux.CodeAISplitModeSaveFailed {
				t.Fatalf("legacy=%v SDK=%v", legacy, err)
			}
			if err.Error() != legacy.Error() || oldPath.Op != newPath.Op || oldPath.Path != newPath.Path || !errors.Is(err, oldPath.Err) || displays != 0 {
				t.Fatalf("legacy=%v SDK=%v displays=%d", legacy, err, displays)
			}
		})
	}
}
