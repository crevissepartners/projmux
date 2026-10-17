package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// Drive the same execution and outcome callback as main: calling only the
// handler cannot prove the CLI's zero-write boundary on errors.
func TestGuidanceReadValidateEntrypointNoWrites(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, test := range []struct {
			name       string
			args       []string
			candidate  []byte
			unreadable bool
			wantCode   int
		}{
			{name: "read", args: []string{"read"}},
			{name: "validate", args: []string{"validate", "--file", "CANDIDATE"}, candidate: []byte("candidate")},
			{name: "invalid", args: []string{"validate", "--file", "CANDIDATE"}, candidate: []byte("bad\x00candidate"), wantCode: 1},
			{name: "missing candidate", args: []string{"validate", "--file", "MISSING"}, wantCode: 1},
			{name: "read usage", args: []string{"read", "--unknown"}, wantCode: 2},
			{name: "validate usage", args: []string{"validate"}, wantCode: 2},
			{name: "read unreadable", args: []string{"read"}, unreadable: true, wantCode: 1},
		} {
			t.Run(fmt.Sprintf("existing=%v/%s", existing, test.name), func(t *testing.T) {
				root := t.TempDir()
				for key, path := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache"} {
					t.Setenv(key, filepath.Join(root, path))
				}
				for _, key := range []string{"TMUX", "TMUX_PANE", "TMUX_TMPDIR", "__PROJMUX_RUNTIME_ANCHOR_PANE", "PROJMUX_CWD"} {
					t.Setenv(key, "")
				}
				candidate := filepath.Join(root, "candidate.md")
				if err := os.WriteFile(candidate, test.candidate, 0600); err != nil {
					t.Fatal(err)
				}
				path, err := diagnostics.DefaultPath(os.Getenv, os.UserHomeDir)
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					config := filepath.Join(root, "config", "projmux")
					if err := os.MkdirAll(filepath.Join(config, "agent-guidance.d"), 0700); err != nil {
						t.Fatal(err)
					}
					for name, content := range map[string]string{"agent-guidance.md": "user", "agent-guidance.json": "{\"disabled\":false,\"disabledFragments\":[]}", "agent-guidance.d/a.md": "fragment", "agent-guidance-migration.json": "preserved receipt"} {
						if err := os.WriteFile(filepath.Join(config, name), []byte(content), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("preserved journal\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if test.unreadable {
					user := filepath.Join(root, "config", "projmux", "agent-guidance.md")
					if err := os.Remove(user); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if err := os.MkdirAll(user, 0700); err != nil {
						t.Fatal(err)
					}
				}
				args := append([]string{"agent", "guidance"}, test.args...)
				for i, arg := range args {
					if arg == "CANDIDATE" {
						args[i] = candidate
					}
					if arg == "MISSING" {
						args[i] = filepath.Join(root, "missing.md")
					}
				}
				before := guidanceEntrypointTree(t, root)
				var stdout, stderr bytes.Buffer
				store := diagnostics.NewStore(path)
				code := executeCLI(func() error { return app.New().Run(args, &stdout, &stderr) }, func(err error) {
					if err := diagnostics.RecordOutcome(store, args, "guidance-test", "test", "tmux", time.Now(), err, app.IsUsageError(err), false); err != nil {
						t.Fatal(err)
					}
				}, &stderr)
				if code != test.wantCode {
					t.Fatalf("exit=%d want=%d stderr=%q", code, test.wantCode, stderr.String())
				}
				after := guidanceEntrypointTree(t, root)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("entrypoint changed isolated tree: before=%v after=%v", before, after)
				}
			})
		}
	}
}

func guidanceEntrypointTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fingerprint := fmt.Sprintf("%v:%d", info.Mode(), info.ModTime().UnixNano())
		if !entry.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fingerprint += fmt.Sprintf(":%x", sha256.Sum256(content))
		}
		out[relative] = fingerprint
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
