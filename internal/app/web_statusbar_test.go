package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/web"
)

func TestWebStatusbarFollowsSettingsVisibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	paths, err := configPaths(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock, _ := statusbarRowOneVisibilityPath(paths, statusbarRowOneClock)
	notifications, _ := statusbarHUDVisibilityPath(paths, statusbarHUDNotifications)
	for _, path := range []string{clock, notifications} {
		if err := config.SaveStatusbarVisibilityFile(path, config.StatusbarVisibilityOff); err != nil {
			t.Fatal(err)
		}
	}
	backend, _ := webFixtureBackend(t)
	code, body := webGet(t, web.New(backend, nil).Handler(), "/api/v1/web/statusbar")
	if code != 200 {
		t.Fatalf("statusbar = %d %v", code, body)
	}
	want := map[string]bool{
		"notifications": false, "usage": true, "project": true, "workingDirectory": true,
		"git": true, "resources": false, "clock": false,
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %v, want %v (%v)", key, body[key], value, body)
		}
	}
}

func TestWebPaneGitReadsTheRecordedDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "demo-repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q", "-b", "topic").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, _ := webFixtureBackend(t)
	registry := resourceFixtureRegistry(t)
	pane, ok := registry.Pane("pan-alpha-zsh")
	if !ok {
		t.Fatal("fixture has no pan-alpha-zsh")
	}
	pane.Spec.CWD = repo
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	handler := web.New(backend, nil).Handler()

	code, body := webGet(t, handler, "/api/v1/web/panes/pan-alpha-zsh/git")
	if code != 200 || body["cwd"] != repo || body["branch"] != "topic" || body["repo"] != "demo-repo" || body["dirty"] != true {
		t.Fatalf("git = %d %v", code, body)
	}
	if _, body := webGet(t, handler, "/api/v1/web/panes/pan-missing/git"); errorCode(body) != web.CodeNotFound {
		t.Fatalf("missing pane = %v", body)
	}
}
