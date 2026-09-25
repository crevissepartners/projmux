package app

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appConfigPathTestEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// Apply writes the generated app tmux config that create later hands to
// `tmux -f`. Both sides have to agree on the path, and create resolves it
// through the XDG-aware config paths, so apply must too.
func TestTmuxApplyWritesAppConfigWhereCreateReadsItUnderXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdgConfigHome := t.TempDir()
	// The materializer resolves home through os.UserHomeDir.
	t.Setenv("HOME", home)
	env := appConfigPathTestEnv(map[string]string{"HOME": home, "XDG_CONFIG_HOME": xdgConfigHome})
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  env,
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--no-reload"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v; stderr = %q", err, stderr.String())
	}

	want := filepath.Join(xdgConfigHome, "projmux", "tmux.conf")
	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read app config at XDG path: %v; stdout = %q", err, stdout.String())
	}
	if !strings.Contains(string(content), "set -g @projmux_app 1") {
		t.Fatalf("config = %q, want app marker", string(content))
	}
	if !strings.Contains(stdout.String(), "wrote "+want) {
		t.Fatalf("stdout = %q, want write summary for %s", stdout.String(), want)
	}
	legacy := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if _, err := os.Stat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %s error = %v, want not exist", legacy, err)
	}

	read, err := (&materializer{lookupEnv: env}).generatedAppConfigPath()
	if err != nil {
		t.Fatalf("generatedAppConfigPath() error = %v", err)
	}
	if read != want {
		t.Fatalf("create reads %q, apply wrote %q", read, want)
	}
}

func TestTmuxInstallAppWritesAppConfigUnderHomeConfigWithoutXDGConfigHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  appConfigPathTestEnv(map[string]string{"HOME": home}),
		writeFile:  os.WriteFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install-app"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
	if !strings.Contains(stdout.String(), "wrote "+want) {
		t.Fatalf("stdout = %q, want write summary for %s", stdout.String(), want)
	}
}

func TestTmuxInstallAppExplicitConfigIgnoresXDGConfigHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdgConfigHome := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "custom", "app.conf")
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  appConfigPathTestEnv(map[string]string{"HOME": home, "XDG_CONFIG_HOME": xdgConfigHome}),
		writeFile:  os.WriteFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install-app", "--config", explicit}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(explicit); err != nil {
		t.Fatalf("stat %s: %v", explicit, err)
	}
	if !strings.Contains(stdout.String(), "wrote "+explicit) {
		t.Fatalf("stdout = %q, want write summary for %s", stdout.String(), explicit)
	}
	for _, unexpected := range []string{
		filepath.Join(xdgConfigHome, "projmux", "tmux.conf"),
		filepath.Join(home, ".config", "projmux", "tmux.conf"),
	} {
		if _, err := os.Stat(unexpected); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat %s error = %v, want not exist", unexpected, err)
		}
	}
}
