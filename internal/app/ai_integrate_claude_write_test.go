package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/services/claudesettings"
)

// atomicClaudeAICommand is testAICommand writing Claude settings the way the
// CLI does in production: no writeFile seam, so through
// claudesettings.Replace.
func atomicClaudeAICommand(home string) *aiCommand {
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	cmd.writeFile = nil
	return cmd
}

func claudeSettingsInode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func requireNoClaudeSettingsTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".projmux-") {
			t.Fatalf("temporary file %s left in %s", entry.Name(), dir)
		}
	}
}

func TestWriteClaudeSettingsReplacesAtomically(t *testing.T) {
	const userSettings = "{\n  \"theme\": \"dark\"\n}\n"

	t.Run("existing file keeps its mode and is replaced", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		writeCodexTestFile(t, path, userSettings)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, "hard-link.json")
		if err := os.Link(path, link); err != nil {
			t.Fatal(err)
		}
		before := claudeSettingsInode(t, path)

		if err := atomicClaudeAICommand(home).Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want the file's own 0600", info.Mode().Perm())
		}
		if claudeSettingsInode(t, path) == before {
			t.Fatal("settings file kept its inode, want it replaced")
		}
		if !strings.Contains(readCodexTestFile(t, path), claudeHookManagedMarker) {
			t.Fatalf("settings missing managed hooks:\n%s", readCodexTestFile(t, path))
		}
		if got := readCodexTestFile(t, link); got != userSettings {
			t.Fatalf("hard link = %q, want the replaced file's old bytes", got)
		}
		requireNoClaudeSettingsTemp(t, filepath.Dir(path))
	})

	t.Run("symbolic link stays and its target is replaced", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		target := filepath.Join(home, "dotfiles", "claude-settings.json")
		writeCodexTestFile(t, target, userSettings)
		if err := os.Chmod(target, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../dotfiles/claude-settings.json", path); err != nil {
			t.Fatal(err)
		}

		if err := atomicClaudeAICommand(home).Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("settings path mode = %v, want the symbolic link kept", info.Mode())
		}
		targetInfo, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if targetInfo.Mode().Perm() != 0o640 {
			t.Fatalf("target mode = %v, want its own 0640", targetInfo.Mode().Perm())
		}
		if !strings.Contains(readCodexTestFile(t, target), claudeHookManagedMarker) {
			t.Fatalf("link target missing managed hooks:\n%s", readCodexTestFile(t, target))
		}
		requireNoClaudeSettingsTemp(t, filepath.Dir(target))
		requireNoClaudeSettingsTemp(t, filepath.Dir(path))
	})

	t.Run("dangling symbolic link creates its target", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		target := filepath.Join(home, "dotfiles", "claude-settings.json")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}

		if err := atomicClaudeAICommand(home).Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("settings path = %v, %v; want the symbolic link kept", info, err)
		}
		if !strings.Contains(readCodexTestFile(t, target), claudeHookManagedMarker) {
			t.Fatalf("link target missing managed hooks:\n%s", readCodexTestFile(t, target))
		}
	})

	t.Run("new file is created as os.WriteFile creates it", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		reference := filepath.Join(t.TempDir(), "reference")
		if err := os.WriteFile(reference, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		referenceDir := filepath.Join(t.TempDir(), "reference-dir", "child")
		if err := os.MkdirAll(referenceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		wantFile, err := os.Stat(reference)
		if err != nil {
			t.Fatal(err)
		}
		wantDir, err := os.Stat(referenceDir)
		if err != nil {
			t.Fatal(err)
		}

		if err := atomicClaudeAICommand(home).Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != wantFile.Mode().Perm() {
			t.Fatalf("new file mode = %v, want %v (0644 less umask)", info.Mode().Perm(), wantFile.Mode().Perm())
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != wantDir.Mode().Perm() {
			t.Fatalf("new directory mode = %v, want %v (0755 less umask)", dirInfo.Mode().Perm(), wantDir.Mode().Perm())
		}
		requireNoClaudeSettingsTemp(t, filepath.Dir(path))
	})

	t.Run("target that is not a regular file is refused", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := atomicClaudeAICommand(home)
		cmd.readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }

		err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "write Claude settings "+path) || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("error = %v, want a refused non-regular target", err)
		}
		requireNoClaudeSettingsTemp(t, filepath.Dir(path))
	})
}

func TestWriteClaudeSettingsRefusesChangedFile(t *testing.T) {
	const userSettings = "{\n  \"theme\": \"dark\"\n}\n"
	const concurrent = "{\n  \"theme\": \"light\"\n}\n"

	for _, tc := range []struct {
		name    string
		initial *string
		change  func(t *testing.T, path string)
		want    *string
	}{
		{
			name:    "file rewritten after the plan read it",
			initial: ptr(userSettings),
			change:  func(t *testing.T, path string) { writeCodexTestFile(t, path, concurrent) },
			want:    ptr(concurrent),
		},
		{
			name: "file created after the plan read none",
			change: func(t *testing.T, path string) {
				writeCodexTestFile(t, path, concurrent)
			},
			want: ptr(concurrent),
		},
		{
			name:    "empty file removed after the plan read it",
			initial: ptr(""),
			change: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file emptied after the plan read none",
			change: func(t *testing.T, path string) {
				writeCodexTestFile(t, path, "")
			},
			want: ptr(""),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, claudeSettingsRelativePath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.initial != nil {
				writeCodexTestFile(t, path, *tc.initial)
			}
			cmd := atomicClaudeAICommand(home)
			changed := false
			cmd.readFile = func(name string) ([]byte, error) {
				data, err := os.ReadFile(name)
				if name == path && !changed {
					changed = true
					tc.change(t, path)
				}
				return data, err
			}
			var stdout bytes.Buffer

			err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{})
			var refusal *claudesettings.ChangedError
			if !errors.As(err, &refusal) || !strings.HasPrefix(err.Error(), claudesettings.CodeChanged+": ") || !strings.Contains(err.Error(), path) {
				t.Fatalf("error = %v, want %s for %s", err, claudesettings.CodeChanged, path)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing reported as written", stdout.String())
			}
			data, readErr := os.ReadFile(path)
			switch {
			case tc.want == nil && !errors.Is(readErr, os.ErrNotExist):
				t.Fatalf("settings read = %q, %v; want the file left absent", data, readErr)
			case tc.want != nil && (readErr != nil || string(data) != *tc.want):
				t.Fatalf("settings = %q, %v; want the concurrent bytes %q kept", data, readErr, *tc.want)
			}
			requireNoClaudeSettingsTemp(t, filepath.Dir(path))
		})
	}
}

func TestIntegrateClaudeOutputUnchanged(t *testing.T) {
	const installed = "installed"
	fixtures := []struct {
		name    string
		initial *string
	}{
		{name: "no settings file"},
		{name: "empty settings file", initial: ptr("")},
		{name: "user hooks", initial: ptr(`{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit",
        "hooks": [
          {
            "type": "command",
            "command": "echo keep"
          }
        ]
      }
    ]
  }
}
`)},
		{name: "already installed", initial: ptr(installed)},
		{name: "unmanaged projmux hook", initial: ptr(`{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true"
          }
        ]
      }
    ]
  }
}
`)},
		{name: "unreadable settings", initial: ptr("{not json\n")},
	}
	commands := [][]string{
		{"integrate", "claude"},
		{"integrate", "claude", "--dry-run"},
		{"integrate", "claude", "--remove"},
		{"integrate", "claude", "--remove", "--dry-run"},
	}
	type outcome struct {
		stdout, stderr, err, settings string
		exists                        bool
	}
	run := func(t *testing.T, seam bool, initial *string, args []string) outcome {
		t.Helper()
		home := t.TempDir()
		path := filepath.Join(home, claudeSettingsRelativePath)
		cmd := atomicClaudeAICommand(home)
		if seam {
			// The in-place write every release before the atomic replace made.
			cmd.writeFile = os.WriteFile
		}
		if initial != nil {
			if *initial == installed {
				if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			} else {
				writeCodexTestFile(t, path, *initial)
			}
		}
		var stdout, stderr bytes.Buffer
		err := cmd.Run(args, &stdout, &stderr)
		got := outcome{
			stdout: strings.ReplaceAll(stdout.String(), home, "$HOME"),
			stderr: strings.ReplaceAll(stderr.String(), home, "$HOME"),
		}
		if err != nil {
			got.err = strings.ReplaceAll(err.Error(), home, "$HOME")
		}
		if data, readErr := os.ReadFile(path); readErr == nil {
			got.exists, got.settings = true, string(data)
		}
		return got
	}
	for _, fixture := range fixtures {
		for _, args := range commands {
			t.Run(fixture.name+"/"+strings.Join(args[1:], " "), func(t *testing.T) {
				before := run(t, true, fixture.initial, args)
				after := run(t, false, fixture.initial, args)
				if after != before {
					t.Fatalf("atomic write changed the command's result:\nbefore: %#v\nafter:  %#v", before, after)
				}
			})
		}
	}
}
