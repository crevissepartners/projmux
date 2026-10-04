package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/updatecmd"
	"github.com/crevissepartners/projmux/internal/version"
)

// The fixtures below build `projmux update` from its exported edges for the
// app tests that drive it through the shell welcome, Settings, and the
// flag-parse tables. updatecmd's own tests keep theirs beside the unexported
// seams they also replace.

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testUpdateCommand(t *testing.T, now time.Time) (*updatecmd.Command, string) {
	t.Helper()
	cacheDir := t.TempDir()
	reads := 0
	cmd := &updatecmd.Command{
		Now:       func() time.Time { return now },
		Getenv:    func(string) string { return "" },
		CacheDir:  func() (string, error) { return cacheDir, nil },
		AppSocket: defaultAppSocket,
		Client: &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("unexpected update request to %s", req.URL.String())
		})},
		APIURL:     "https://example.invalid/latest",
		NPMURL:     "https://example.invalid/npm/projmux",
		Executable: func() (string, error) { return "/tmp/projmux", nil },
		// An apply driven from these surfaces describes an ordinary landed
		// upgrade, so the probe reports a higher version after publication.
		ProbeVersion: func(string) (string, error) {
			reads++
			if reads == 1 {
				return "projmux 0.13.0\n", nil
			}
			return "projmux 0.13.1\n", nil
		},
		LookPath: func(name string) (string, error) {
			if name != "projmux" {
				return "", fmt.Errorf("unexpected executable lookup %q", name)
			}
			return "/npm/bin/projmux", nil
		},
	}
	return cmd, cacheDir
}

func writeUpdateCacheFixture(t *testing.T, cacheDir string, cache updatecmd.Cache) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, updatecmd.CacheFileName), data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func testVersionTag(t *testing.T, patchDelta int) string {
	t.Helper()
	parts, ok := updatecmd.ParseVersion(version.String())
	if !ok {
		t.Fatalf("cannot parse current version %q", version.String())
	}
	parts[2] += patchDelta
	if parts[2] < 0 {
		t.Fatalf("invalid patch delta %d for current version %q", patchDelta, version.String())
	}
	return fmt.Sprintf("v%d.%d.%d", parts[0], parts[1], parts[2])
}

// Acceptance 1, 2, 4, 5, 7: actual CLI and two compiled candidates, isolated
// HOME/config/state. B1-2 permits only the diagnostics journal to change.
func TestUpdateCLIFixtureSchemaPlanAndRefusalPreserveInstallStateConfig(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "projmux")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/projmux")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	candidates := map[int]string{}
	for _, schema := range []int{4, 5, 6, 0} {
		source := filepath.Join(root, fmt.Sprintf("candidate-%d.go", schema))
		code := fmt.Sprintf(`package main
import("fmt";"os")
func main(){if len(os.Args)>2 && os.Args[1]=="version" {fmt.Print("{\"version\":\"0.16.1\",\"commit\":\"fixture-%d\",\"schema_version\":%d}");return};fmt.Println("projmux 0.16.1")}`, schema, schema)
		if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := filepath.Join(root, fmt.Sprintf("candidate-%d", schema))
		compile := exec.Command("go", "build", "-o", candidate, source)
		if out, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("build candidate: %v\n%s", err, out)
		}
		candidates[schema] = candidate
	}
	for _, tc := range []struct {
		name   string
		schema int
		dry    bool
		change string
	}{{"same-version-bump", 6, false, "bump"}, {"downgrade", 4, false, "downgrade"}, {"unknown", 0, false, "unknown"}, {"dry-bump", 6, true, "bump"}, {"dry-same", 5, true, "same"}} {
		t.Run(tc.name, func(t *testing.T) {
			caseRoot := t.TempDir()
			home := filepath.Join(caseRoot, "home")
			configHome := filepath.Join(home, "config")
			stateHome := filepath.Join(home, "state")
			repo := filepath.Join(home, "project")
			for path, data := range map[string]string{
				filepath.Join(configHome, "projmux", "hooks", "post-create"):         "#!/bin/sh\necho legacy-global\n",
				filepath.Join(repo, ".projmux", "post-create"):                       "#!/bin/sh\necho legacy-project\n",
				filepath.Join(configHome, "projmux", "config.toml"):                  "# untouched fixture config\n",
				filepath.Join(stateHome, "projmux", "registry.json"):                 "{\"schemaVersion\":5}\n",
				filepath.Join(stateHome, "projmux", "sentinel"):                      "state fixture\n",
				filepath.Join(stateHome, "projmux", "logs", "operations.jsonl.lock"): "",
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(caseRoot, "installed-projmux")
			raw, err := os.ReadFile(candidates[5])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, raw, 0o700); err != nil {
				t.Fatal(err)
			}
			before := updateFixtureTreeHashes(t, home)
			targetBefore := sha256.Sum256(raw)
			args := []string{"update", "apply", "--from", candidates[tc.schema], "--target", target}
			if tc.dry {
				args = append(args, "--dry-run")
			}
			command := exec.Command(binary, args...)
			command.Dir = repo
			command.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + configHome, "XDG_STATE_HOME=" + stateHome, "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "PROJMUX_CWD=" + repo, "PATH=" + root + ":/usr/bin:/bin"}
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err = command.Run()
			if tc.dry && err != nil {
				t.Fatalf("dry-run: %v\n%s", err, stderr.String())
			}
			if !tc.dry && (err == nil || !strings.Contains(stderr.String(), "update-schema-"+tc.change) || !strings.Contains(stderr.String(), "docs/registry.md")) {
				t.Fatalf("refusal: %v\n%s", err, stderr.String())
			}
			for _, want := range []string{"digest: sha256:", target, "schema: " + tc.change, "(different)", "stop → backup → install → resume", "not automated yet"} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("missing %q: %s", want, stdout.String())
				}
			}
			after := updateFixtureTreeHashes(t, home)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("state/config changed:\nbefore=%v\nafter=%v", before, after)
			}
			installed, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(installed) != targetBefore {
				t.Fatal("installed binary changed")
			}
			journal, err := os.ReadFile(filepath.Join(stateHome, "projmux", "logs", "operations.jsonl"))
			if err != nil && !tc.dry {
				t.Fatal(err)
			}
			if !tc.dry && !bytes.Contains(journal, []byte(`"result":"error"`)) {
				t.Fatalf("missing refusal diagnostic: %s", journal)
			}
		})
	}

	t.Run("same-schema-success-migrates-hooks-at-config-stage", func(t *testing.T) {
		caseRoot := t.TempDir()
		home := filepath.Join(caseRoot, "home")
		configHome := filepath.Join(home, "config")
		repo := filepath.Join(home, "project")
		global := filepath.Join(configHome, "projmux", "hooks", "post-create")
		project := filepath.Join(repo, ".projmux", "post-create")
		for _, path := range []string{global, project} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\necho legacy\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		binDir := filepath.Join(caseRoot, "bin")
		if err := os.Mkdir(binDir, 0o700); err != nil {
			t.Fatal(err)
		}
		tmuxDir := filepath.Join(caseRoot, "tmux")
		if err := os.Mkdir(tmuxDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(tmuxDir); err != nil || !info.IsDir() {
			t.Fatal("TMUX_TMPDIR was not created")
		}
		tmuxLog := filepath.Join(caseRoot, "tmux-invocations")
		tmuxStub := "#!/bin/sh\nprintf invoked >> '" + tmuxLog + "'\nexit 99\n"
		if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(tmuxStub), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(binDir, "projmux")
		old := "#!/bin/sh\nif [ \"$#\" -gt 1 ]; then printf '%s' '{\"version\":\"0.16.0\",\"commit\":\"fixture\",\"schema_version\":5}'; else printf 'projmux 0.16.0\\n'; fi\n"
		if err := os.WriteFile(target, []byte(old), 0o700); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "update", "apply", "--from", binary, "--target", target, "--no-apply")
		command.Dir = repo
		command.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + configHome, "XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "PROJMUX_CWD=" + repo, "PATH=" + binDir + ":/usr/bin:/bin", "TMUX_TMPDIR=" + tmuxDir}
		output, err := command.CombinedOutput()
		if _, statErr := os.Stat(tmuxLog); !os.IsNotExist(statErr) {
			t.Fatalf("config-only apply invoked tmux: %v", statErr)
		}
		if err != nil {
			t.Fatalf("same-schema apply: %v\n%s", err, output)
		}
		for _, path := range []string{global, project} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("legacy hook not migrated %s: %v\n%s", path, err, output)
			}
		}
		if !bytes.Contains(output, []byte("verified: projmux "+version.String())) {
			t.Fatalf("not verified: %s", output)
		}
	})
	t.Run("metadata-public-surface", func(t *testing.T) {
		home := t.TempDir()
		for _, spelling := range []string{"version", "--version", "-version"} {
			command := exec.Command(binary, spelling, "-o", "json")
			command.Env = []string{"HOME=" + home, "XDG_STATE_HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=/usr/bin:/bin"}
			out, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			var metadata updatecmd.CandidateMetadata
			if err := json.Unmarshal(out, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Version != version.String() || metadata.SchemaVersion != 5 || metadata.Commit != "unknown" {
				t.Fatalf("metadata=%v", metadata)
			}
		}
		if entries := updateFixtureTreeHashes(t, home); len(entries) != 0 {
			t.Fatalf("metadata wrote HOME: %v", entries)
		}
	})
}
func updateFixtureTreeHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == "state/projmux/logs/operations.jsonl" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[relative] = sha256.Sum256(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

// B1-3: deferred hook migration still belongs to the normal config apply stage.
func TestUpdateApplyDefersLegacyHooksUntilConfigApply(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"version", "-o", "json"}, {"--version"}, {"-version"}, {"update", "apply"}, {"update", "apply", "--dry-run"}} {
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("predispatch writes for %v", args)
		}
	}
	if !shouldRunLegacyHookMigrations([]string{"config", "apply"}) {
		t.Fatal("config apply no longer migrates legacy hooks")
	}
}
