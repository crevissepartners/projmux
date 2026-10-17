package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestProjectGuidanceReadValidateEntrypointNoWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		candidate []byte
		want      int
	}{
		{"read", []string{"read", "--project", "sample"}, nil, 0},
		{"validate", []string{"validate", "--project", "sample", "--file", "CANDIDATE"}, []byte("valid"), 0},
		{"invalid", []string{"validate", "--project", "sample", "--file", "CANDIDATE"}, []byte("bad\x00"), 1},
		{"missing", []string{"read", "--project", "missing"}, nil, 2},
		{"empty", []string{"read", "--project="}, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for key, dir := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache"} {
				t.Setenv(key, filepath.Join(root, dir))
			}
			for _, key := range []string{"TMUX", "TMUX_PANE", "TMUX_TMPDIR", "__PROJMUX_RUNTIME_ANCHOR_PANE", "PROJMUX_CWD"} {
				t.Setenv(key, "")
			}
			paths := config.Paths{ConfigDir: filepath.Join(root, "config", "projmux"), StateDir: filepath.Join(root, "state", "projmux")}
			registry := metadata.NewRegistry()
			registry.Projects = []metadata.Project{{APIVersion: metadata.APIVersion, Kind: metadata.KindProject, Metadata: metadata.ObjectMeta{UID: "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "sample"}, Spec: metadata.ProjectSpec{Root: root}}}
			b, err := json.Marshal(registry)
			if err != nil {
				t.Fatal(err)
			}
			path := storemetadata.NewDefaultStore(paths).Path()
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			candidate := filepath.Join(root, "candidate.md")
			if err = os.WriteFile(candidate, tc.candidate, 0600); err != nil {
				t.Fatal(err)
			}
			for name, text := range map[string]string{"agent-guidance.md": "user", "agent-guidance.json": `{"disabled":true}`, "agent-guidance.d/a.md": "fragment", "project-guidance/proj-aaaaaaaaaaaaaaaaaaaaaaaaaa.md": " \t\r\n"} {
				path := filepath.Join(paths.ConfigDir, name)
				if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"agent", "guidance"}, tc.args...)
			for i, arg := range args {
				if arg == "CANDIDATE" {
					args[i] = candidate
				}
			}
			journal, err := diagnostics.DefaultPath(os.Getenv, os.UserHomeDir)
			if err != nil {
				t.Fatal(err)
			}
			before := guidanceEntrypointTree(t, root)
			var out, stderr bytes.Buffer
			code := executeCLI(func() error { return app.New().Run(args, &out, &stderr) }, func(err error) {
				if err := diagnostics.RecordOutcome(diagnostics.NewStore(journal), args, "guidance-test", "test", "tmux", time.Now(), err, app.IsUsageError(err), false); err != nil {
					t.Fatal(err)
				}
			}, &stderr)
			if code != tc.want {
				t.Fatalf("exit %d want %d: %s", code, tc.want, stderr.String())
			}
			if !reflect.DeepEqual(before, guidanceEntrypointTree(t, root)) {
				t.Fatal("read/validate entrypoint wrote state")
			}
		})
	}
}
