package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusGitPrintsBranchForPath(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}) {
			return []byte("true\n"), nil
		}
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}) {
			return []byte("main\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/repo"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), " | git:main"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitUsesCurrentPanePathInsideTmux(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{pane_current_path}"}):
			return []byte("/repo\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return nil, errors.New("detached")
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--short", "HEAD"}):
			return []byte("abc1234\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), " | git:abc1234"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusKubePrintsCachedFreshSegment(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testStatusCommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cacheFile := filepath.Join(home, ".cache", "tmux", "kube-segment-dev.txt")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(999, 0)
	if err := os.Chtimes(cacheFile, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "kubectl"}) {
			return []byte("/usr/bin/kubectl\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"kube", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "cached"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusKubeRefreshesContextAndNamespace(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runtimeDir := filepath.Join(home, "run")
	cacheHome := filepath.Join(home, "cache")
	kubeConfig := filepath.Join(runtimeDir, "kube-sessions", "dev.yaml")
	if err := os.MkdirAll(filepath.Dir(kubeConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeConfig, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := testStatusCommand(home)
	cmd.now = func() time.Time { return time.Unix(2000, 0) }
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		case "XDG_CACHE_HOME":
			return cacheHome
		case "TMUX_KUBE_TIMEOUT":
			return "0.4"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "command" && reflect.DeepEqual(args, []string{"-v", "kubectl"}):
			return []byte("/usr/bin/kubectl\n"), nil
		case name == "command" && reflect.DeepEqual(args, []string{"-v", "timeout"}):
			return []byte("/usr/bin/timeout\n"), nil
		case name == "env" && reflect.DeepEqual(args, []string{"KUBECONFIG=" + kubeConfig, "timeout", "0.400", "kubectl", "config", "current-context"}):
			return []byte("kind-dev\n"), nil
		case name == "env" && reflect.DeepEqual(args, []string{"KUBECONFIG=" + kubeConfig, "timeout", "0.400", "kubectl", "config", "view", "--minify", "--output", "jsonpath={..namespace}"}):
			return []byte("apps\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"kube", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "k8s:#[fg=red]kind-dev#[default]/#[fg=blue]apps#[default]"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(readTextFile(filepath.Join(cacheHome, "tmux", "kube-segment-dev.txt"))); got != want {
		t.Fatalf("cache = %q, want %q", got, want)
	}
}

func TestStatusRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: nil, want: "status requires a subcommand"},
		{name: "unknown", args: []string{"bad"}, want: "unknown status subcommand"},
		{name: "git args", args: []string{"git", "a", "b"}, want: "status git accepts at most 1"},
		{name: "kube args", args: []string{"kube", "a", "b"}, want: "status kube accepts at most 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			err := testStatusCommand(t.TempDir()).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func testStatusCommand(home string) *statusCommand {
	return &statusCommand{
		lookupEnv: func(string) string { return "" },
		homeDir:   func() (string, error) { return home, nil },
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		now: func() time.Time { return time.Now() },
	}
}
