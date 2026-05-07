package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPostCreateRunnerEmptyHookPathIsNoOp(t *testing.T) {
	t.Parallel()

	var logger bytes.Buffer
	runner := &PostCreateRunner{Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerNilReceiverIsNoOp(t *testing.T) {
	t.Parallel()

	var runner *PostCreateRunner
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})
}

func TestPostCreateRunnerMissingHookIsNoOp(t *testing.T) {
	t.Parallel()

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath: filepath.Join(t.TempDir(), "missing"),
		Logger:   &logger,
	}
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerNonExecutableHookIsNoOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "hook")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var logger bytes.Buffer
	runner := &PostCreateRunner{HookPath: path, Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty (non-executable should be silent)", logger.String())
	}
}

func TestPostCreateRunnerDirectoryAtHookPathIsNoOp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var logger bytes.Buffer
	runner := &PostCreateRunner{HookPath: dir, Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerHappyPathInjectsEnvAndPrefixesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	hookPath := absFixture(t, "echo-env.sh")
	cwd := t.TempDir()
	var logger bytes.Buffer
	runner := &PostCreateRunner{HookPath: hookPath, Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Socket:      "projmux",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	want := []string{
		"[post-create] session=workspace",
		"[post-create] cwd=" + cwd,
		"[post-create] kind=persistent",
		"[post-create] socket=projmux",
		"[post-create] version=0.0.0-test",
		"[post-create] stderr-line",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Fatalf("logger output missing %q\nfull output:\n%s", line, got)
		}
	}
	if strings.Contains(got, "projmux: post-create hook:") {
		t.Fatalf("happy path should not emit warning: %q", got)
	}
}

func TestPostCreateRunnerRunsProjectLocalHookOnlyWhenEnabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: actual.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(repo, "service")
	if err := os.MkdirAll(filepath.Join(projectDir, ".keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	hookDir := filepath.Join(repo, ".projmux")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hookDir, "post-create")
	if err := os.WriteFile(hookPath, []byte("#!/usr/bin/env bash\necho project-local:$PROJMUX_SESSION:$PWD\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var disabled bytes.Buffer
	(&PostCreateRunner{ProjectHooksEnabled: false, Logger: &disabled}).Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         projectDir,
		Version:     "0.0.0-test",
	})
	if disabled.Len() != 0 {
		t.Fatalf("disabled logger output = %q, want empty", disabled.String())
	}

	var enabled bytes.Buffer
	(&PostCreateRunner{ProjectHooksEnabled: true, Logger: &enabled}).Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         projectDir,
		Version:     "0.0.0-test",
	})
	if got := enabled.String(); !strings.Contains(got, "[post-create] project-local:workspace:"+projectDir) {
		t.Fatalf("enabled logger output = %q, want project-local hook output", got)
	}
}

func TestPostCreateRunnerRunsGlobalThenFirstExecutableProjectHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	globalHook := filepath.Join(repo, "global")
	if err := os.WriteFile(globalHook, []byte("#!/usr/bin/env bash\necho global\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".projmux", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".projmux", "post-create"), []byte("#!/usr/bin/env bash\necho non-exec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".projmux", "hooks", "post-create"), []byte("#!/usr/bin/env bash\necho project\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var logger bytes.Buffer
	(&PostCreateRunner{
		HookPath:            globalHook,
		ProjectHooksEnabled: true,
		Logger:              &logger,
		Version:             "0.0.0-test",
	}).Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         repo,
	})

	got := logger.String()
	globalIdx := strings.Index(got, "[post-create] global")
	projectIdx := strings.Index(got, "[post-create] project")
	if globalIdx < 0 || projectIdx < 0 || globalIdx > projectIdx {
		t.Fatalf("logger output = %q, want global then project hook", got)
	}
	if strings.Contains(got, "non-exec") {
		t.Fatalf("logger output = %q, non-executable project hook should be skipped", got)
	}
}

func TestProjectLocalPostCreateHooksEnabledFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "yes", want: true},
		{value: "on", want: true},
		{value: "", want: false},
		{value: "0", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			got := ProjectLocalPostCreateHooksEnabledFromEnv(func(string) string { return tt.value })
			if got != tt.want {
				t.Fatalf("enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPostCreateRunnerOmitsSocketWhenEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	hookPath := absFixture(t, "echo-env.sh")
	var logger bytes.Buffer
	runner := &PostCreateRunner{HookPath: hookPath, Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         t.TempDir(),
		Kind:        "ephemeral",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "[post-create] socket=unset") {
		t.Fatalf("expected socket env to be unset, got:\n%s", got)
	}
}

func TestPostCreateRunnerNonZeroExitWritesWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	hookPath := absFixture(t, "fail.sh")
	var logger bytes.Buffer
	runner := &PostCreateRunner{HookPath: hookPath, Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         t.TempDir(),
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "projmux: post-create hook:") {
		t.Fatalf("expected warning line, got:\n%s", got)
	}
	if !strings.Contains(got, "exited with status 7") {
		t.Fatalf("expected exit status 7 in warning, got:\n%s", got)
	}
}

func TestPostCreateRunnerTimeoutKillsHookAndWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	hookPath := absFixture(t, "slow.sh")
	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath: hookPath,
		Logger:   &logger,
		Timeout:  200 * time.Millisecond,
	}

	start := time.Now()
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         t.TempDir(),
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fire in time: elapsed=%s", elapsed)
	}
	got := logger.String()
	if !strings.Contains(got, "timed out") {
		t.Fatalf("expected timeout warning, got:\n%s", got)
	}
}

func absFixture(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat fixture %s: %v", abs, err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		// Repo restore did not preserve the execute bit; restore it for this run
		// so we still exercise the happy path locally. CI checks that the bit is
		// committed via git update-index --chmod=+x.
		if err := os.Chmod(abs, 0o755); err != nil {
			t.Fatalf("chmod fixture %s: %v", abs, err)
		}
	}
	return abs
}
