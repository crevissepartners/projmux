package hooks

import (
	"bytes"
	"context"
	"io"
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

func TestPostCreateRunnerProjectLocalOnlyDiscoversShortPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo local-short\n", 0o755)

	var logger bytes.Buffer
	runner := testProjectHookRunner(t, &logger, ProjectHookAllowOnce)
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "[post-create] local-short") {
		t.Fatalf("logger output missing project hook output:\n%s", got)
	}
}

func TestPostCreateRunnerProjectLocalDiscoversHooksSubdirFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo nonexec\n", 0o644)
	writeHook(t, filepath.Join(cwd, ".projmux", "hooks", "post-create"), "echo local-hooks-dir\n", 0o755)

	var logger bytes.Buffer
	runner := testProjectHookRunner(t, &logger, ProjectHookAllowOnce)
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if strings.Contains(got, "nonexec") {
		t.Fatalf("non-executable project hook should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "[post-create] local-hooks-dir") {
		t.Fatalf("logger output missing hooks-dir fallback output:\n%s", got)
	}
}

func TestPostCreateRunnerGlobalAndProjectHooksRunInOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global-post-create")
	cwd := filepath.Join(dir, "repo")
	writeHook(t, globalPath, "echo global\n", 0o755)
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo project\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath:             globalPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	globalIdx := strings.Index(got, "[post-create] global")
	projectIdx := strings.Index(got, "[post-create] project")
	if globalIdx < 0 || projectIdx < 0 {
		t.Fatalf("logger output missing hook output:\n%s", got)
	}
	if globalIdx > projectIdx {
		t.Fatalf("hooks ran out of order:\n%s", got)
	}
}

func TestPostCreateRunnerMissingProjectHookIsNoOp(t *testing.T) {
	t.Parallel()

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		Logger:               &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         t.TempDir(),
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerNonExecutableProjectHookIsNoOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo skipped\n", 0o644)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		Logger:               &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerGlobalFailureDoesNotSkipProjectHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global-post-create")
	cwd := filepath.Join(dir, "repo")
	writeHook(t, globalPath, "echo global-failed\nexit 7\n", 0o755)
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo project-still-ran\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath:             globalPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "exited with status 7") {
		t.Fatalf("expected global failure warning, got:\n%s", got)
	}
	if !strings.Contains(got, "[post-create] project-still-ran") {
		t.Fatalf("project hook did not run after global failure:\n%s", got)
	}
}

func TestPostCreateRunnerProjectHookTimeoutWarnsAndReturns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "sleep 5\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
		Timeout:              200 * time.Millisecond,
	}

	start := time.Now()
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
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

func TestPostCreateRunnerProjectHookRequiresTrustInNonInteractiveContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo should-not-run\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		PromptReader:         strings.NewReader(""),
		Logger:               &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if strings.Contains(got, "should-not-run") {
		t.Fatalf("untrusted project hook ran:\n%s", got)
	}
	if !strings.Contains(got, "requires trust") || !strings.Contains(got, "non-interactive") {
		t.Fatalf("logger output missing trust warning:\n%s", got)
	}
}

func TestPostCreateRunnerProjectHookAllowAlwaysPersistsAndHashMatchSkipsPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo trusted\n", 0o755)
	trustPath := testTrustStorePath(t)
	promptCalls := 0

	var firstLogger bytes.Buffer
	first := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			if req.RepoPath == "" || req.RelativePath != ".projmux/post-create" || req.SHA256 == "" {
				t.Fatalf("prompt request = %#v", req)
			}
			return ProjectHookAllowAlways
		},
		Logger: &firstLogger,
	}
	first.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})
	if !strings.Contains(firstLogger.String(), "[post-create] trusted") {
		t.Fatalf("first run logger output missing hook output:\n%s", firstLogger.String())
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls after first run = %d, want 1", promptCalls)
	}

	var secondLogger bytes.Buffer
	second := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("prompt should not be called when hash matches trust store")
			return ProjectHookDeny
		},
		Logger: &secondLogger,
	}
	second.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})
	if !strings.Contains(secondLogger.String(), "[post-create] trusted") {
		t.Fatalf("second run logger output missing hook output:\n%s", secondLogger.String())
	}
}

func TestPostCreateRunnerProjectHookHashMismatchPromptsWithOldAndNewHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	hookPath := filepath.Join(cwd, ".projmux", "post-create")
	writeHook(t, hookPath, "echo old\n", 0o755)
	oldHash, _, err := hashHookFile(hookPath)
	if err != nil {
		t.Fatalf("hashHookFile old: %v", err)
	}
	trustPath := testTrustStorePath(t)
	store := trustedProjects{}
	store.trust(cwd, ".projmux/post-create", oldHash, time.Unix(1, 0).UTC())
	if err := store.save(trustPath); err != nil {
		t.Fatalf("save trust store: %v", err)
	}

	writeHook(t, hookPath, "echo new\n", 0o755)
	newHash, _, err := hashHookFile(hookPath)
	if err != nil {
		t.Fatalf("hashHookFile new: %v", err)
	}

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			if req.PreviousSHA256 != oldHash {
				t.Fatalf("PreviousSHA256 = %q, want %q", req.PreviousSHA256, oldHash)
			}
			if req.SHA256 != newHash {
				t.Fatalf("SHA256 = %q, want %q", req.SHA256, newHash)
			}
			if !strings.Contains(req.Preview, "echo new") {
				t.Fatalf("Preview = %q, want new hook content", req.Preview)
			}
			return ProjectHookAllowAlways
		},
		Logger: &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	if !strings.Contains(logger.String(), "[post-create] new") {
		t.Fatalf("logger output missing updated hook output:\n%s", logger.String())
	}
	reloaded, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects: %v", err)
	}
	file, ok := reloaded.trustedFile(cwd, ".projmux/post-create")
	if !ok {
		t.Fatalf("trusted file missing after mismatch approval: %#v", reloaded)
	}
	if file.SHA256 != newHash {
		t.Fatalf("stored sha256 = %q, want %q", file.SHA256, newHash)
	}
}

func TestPostCreateRunnerProjectHooksKillSwitchKeepsGlobalHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global-post-create")
	cwd := filepath.Join(dir, "repo")
	writeHook(t, globalPath, "echo global\n", 0o755)
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo project\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath:             globalPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("project hook prompt should not be called when kill switch is off")
			return ProjectHookDeny
		},
		Logger: &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "[post-create] global") {
		t.Fatalf("global hook did not run with kill switch:\n%s", got)
	}
	if strings.Contains(got, "[post-create] project") {
		t.Fatalf("project hook ran with kill switch:\n%s", got)
	}
}

func TestPostCreateRunnerProjectHooksSettingsOffKeepsGlobalHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	if err := os.MkdirAll(filepath.Join(configHome, "projmux"), 0o755); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "projmux", "project-hooks"), []byte("off\n"), 0o644); err != nil {
		t.Fatalf("WriteFile project-hooks: %v", err)
	}

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global-post-create")
	cwd := filepath.Join(dir, "repo")
	writeHook(t, globalPath, "echo global\n", 0o755)
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo project\n", 0o755)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
		HookPath:             globalPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: filepath.Join(configHome, "projmux", "project-hooks"),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("project hook prompt should not be called when settings toggle is off")
			return ProjectHookDeny
		},
		Logger: &logger,
	}
	runner.Run(context.Background(), PostCreateContext{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})

	got := logger.String()
	if !strings.Contains(got, "[post-create] global") {
		t.Fatalf("global hook did not run with settings toggle off:\n%s", got)
	}
	if strings.Contains(got, "[post-create] project") {
		t.Fatalf("project hook ran with settings toggle off:\n%s", got)
	}
}

func TestTrustedProjectsStoreRoundTrip(t *testing.T) {
	t.Parallel()

	path := testTrustStorePath(t)
	store := trustedProjects{}
	at := time.Date(2026, 5, 10, 1, 2, 3, 0, time.UTC)
	store.trust("/repo", ".projmux/post-create", "abc123", at)
	if err := store.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadTrustedProjects(path)
	if err != nil {
		t.Fatalf("loadTrustedProjects: %v", err)
	}
	file, ok := got.trustedFile("/repo", ".projmux/post-create")
	if !ok {
		t.Fatalf("trusted file missing: %#v", got)
	}
	if file.SHA256 != "abc123" {
		t.Fatalf("SHA256 = %q, want abc123", file.SHA256)
	}
	if !file.TrustedAt.Equal(at) {
		t.Fatalf("TrustedAt = %s, want %s", file.TrustedAt, at)
	}
	if project := got["/repo"]; !project.TrustedAt.Equal(at) {
		t.Fatalf("project TrustedAt = %s, want %s", project.TrustedAt, at)
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

func writeHook(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := []byte("#!/usr/bin/env bash\nset -euo pipefail\n" + body)
	if err := os.WriteFile(path, content, perm); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func testProjectHookRunner(t *testing.T, logger io.Writer, decision ProjectHookDecision) *PostCreateRunner {
	t.Helper()
	return &PostCreateRunner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return decision },
		Logger:               logger,
	}
}

func testProjectHooksFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "project-hooks")
}

func testTrustStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "trusted-projects.json")
}
