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

// --- PostCreateRunner shim coverage ---------------------------------------

func TestPostCreateRunnerNilReceiverIsNoOp(t *testing.T) {
	t.Parallel()

	var runner *PostCreateRunner
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})
}

func TestPostCreateRunnerEmptyConfigIsNoOp(t *testing.T) {
	t.Parallel()

	var logger bytes.Buffer
	runner := &PostCreateRunner{Logger: &logger}
	runner.Run(context.Background(), PostCreateContext{SessionName: "workspace"})

	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestPostCreateRunnerProjectConfigRunsWithTrust(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo from-project"
`)

	var logger bytes.Buffer
	runner := &PostCreateRunner{
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
	if !strings.Contains(got, "from-project") {
		t.Fatalf("logger output missing declarative project hook stdout:\n%s", got)
	}
}

// --- Runner declarative behaviour -----------------------------------------

func TestRunnerNoConfigIsNoOp(t *testing.T) {
	t.Parallel()

	var logger bytes.Buffer
	runner := &Runner{Logger: &logger}
	result, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Stdout != "" {
		t.Fatalf("result.Stdout = %q, want empty", result.Stdout)
	}
	if logger.Len() != 0 {
		t.Fatalf("logger output = %q, want empty", logger.String())
	}
}

func TestRunnerGlobalAndProjectDeclarativeBothRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	dir := t.TempDir()
	globalConfigPath := filepath.Join(dir, "global", "config.toml")
	writeFileEnsuringDir(t, globalConfigPath, `
[hooks.post-create]
run = "echo global"
`)
	cwd := filepath.Join(dir, "repo")
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo project"
`)

	var logger bytes.Buffer
	runner := &Runner{
		GlobalConfigPath:     globalConfigPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := logger.String()
	globalIdx := strings.Index(got, "global")
	projectIdx := strings.Index(got, "project")
	if globalIdx < 0 || projectIdx < 0 {
		t.Fatalf("logger output missing hook stdout:\n%s", got)
	}
	if globalIdx > projectIdx {
		t.Fatalf("hooks ran out of order:\n%s", got)
	}
}

func TestRunnerDoesNotExecuteScriptFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeHook(t, filepath.Join(cwd, ".projmux", "post-create"), "echo legacy-script\n", 0o755)
	writeHook(t, filepath.Join(cwd, ".projmux", "hooks", "post-create"), "echo legacy-script-hooks-dir\n", 0o755)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("script files should never trigger a trust prompt")
			return ProjectHookDeny
		},
		Logger: &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
		Version:     "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(logger.String(), "legacy-script") {
		t.Fatalf("runner executed legacy script file:\n%s", logger.String())
	}
}

func TestRunnerPreCreateNonZeroExitAborts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.pre-create]
run = "echo before-abort; exit 9"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}

	_, err := runner.Run(context.Background(), EventPreCreate, Context{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
	})
	if err == nil {
		t.Fatal("expected pre-create error")
	}
	if !strings.Contains(err.Error(), "exited with status 9") {
		t.Fatalf("pre-create error = %v, want exit status 9", err)
	}
	if !strings.Contains(logger.String(), "before-abort") {
		t.Fatalf("pre-create output missing from logger:\n%s", logger.String())
	}
}

func TestRunnerPostCreateFailureIsLoggedNotReturned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "exit 7"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{
		SessionName: "workspace",
		CWD:         cwd,
		Kind:        "persistent",
	})
	if err != nil {
		t.Fatalf("post-create error should be swallowed, got %v", err)
	}
	if !strings.Contains(logger.String(), "exited with status 7") {
		t.Fatalf("logger should record failure warning, got:\n%s", logger.String())
	}
}

func TestRunnerTimeoutKillsHookAndWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "sleep 5"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
		Timeout:              200 * time.Millisecond,
	}

	start := time.Now()
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("timeout did not fire in time: elapsed=%s", elapsed)
	}
	if !strings.Contains(logger.String(), "timed out") {
		t.Fatalf("expected timeout warning, got:\n%s", logger.String())
	}
}

func TestRunnerSendNotiPassesStdinAndNotifyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.send-noti]
run = "printf '%s|%s|%s\n' \"$PROJMUX_NOTIFY_ID\" \"$PROJMUX_NOTIFY_TYPE\" \"$PROJMUX_NOTIFY_MESSAGE\"; cat"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventSendNoti, Context{
		CWD: cwd,
		Env: map[string]string{
			"PROJMUX_NOTIFY_ID":      "n_123",
			"PROJMUX_NOTIFY_TYPE":    "ai-reply-ready",
			"PROJMUX_NOTIFY_MESSAGE": "claude: reply ready",
		},
		Stdin: []byte(`{"event":"send-noti"}`),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := logger.String()
	if !strings.Contains(got, "n_123|ai-reply-ready|claude: reply ready") {
		t.Fatalf("logger missing notify env output:\n%s", got)
	}
	if !strings.Contains(got, `{"event":"send-noti"}`) {
		t.Fatalf("logger missing stdin payload:\n%s", got)
	}
}

func TestRunnerPaneStartupWarnsDeprecated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.pane-startup]
run = "echo pane"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPaneStartup, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(logger.String(), "deprecated; move [hooks.pane-startup] run to [startup] run before the next breaking release") {
		t.Fatalf("logger missing deprecation warning:\n%s", logger.String())
	}
}

func TestRunnerHooksKillSwitchDisablesProjectConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")
	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo should-not-run"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("kill switch should suppress trust prompts")
			return ProjectHookDeny
		},
		Logger: &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(logger.String(), "should-not-run") {
		t.Fatalf("project hook ran with kill switch:\n%s", logger.String())
	}
}

func TestRunnerHooksKillSwitchKeepsGlobalConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")
	dir := t.TempDir()
	globalConfigPath := filepath.Join(dir, "global", "config.toml")
	writeFileEnsuringDir(t, globalConfigPath, `
[hooks.post-create]
run = "echo global"
`)
	cwd := filepath.Join(dir, "repo")
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo project-should-not-run"
`)

	var logger bytes.Buffer
	runner := &Runner{
		GlobalConfigPath:     globalConfigPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := logger.String()
	if !strings.Contains(got, "global") {
		t.Fatalf("global hook missing:\n%s", got)
	}
	if strings.Contains(got, "project-should-not-run") {
		t.Fatalf("project hook ran with kill switch:\n%s", got)
	}
}

func TestRunnerHasHooksReadsGlobalAndProject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	globalConfigPath := filepath.Join(dir, "global", "config.toml")
	writeFileEnsuringDir(t, globalConfigPath, `
[hooks.post-attach]
run = "echo on-attach"
`)
	cwd := filepath.Join(dir, "repo")
	writeProjectConfig(t, cwd, `
[hooks.pre-create]
run = "true"
`)

	runner := &Runner{
		GlobalConfigPath:     globalConfigPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
	}
	if !runner.HasHooks(EventPostAttach, cwd) {
		t.Fatal("HasHooks(post-attach) = false, want true (global)")
	}
	if !runner.HasHooks(EventPreCreate, cwd) {
		t.Fatal("HasHooks(pre-create) = false, want true (project)")
	}
	if runner.HasHooks(EventPostCreate, cwd) {
		t.Fatal("HasHooks(post-create) = true, want false")
	}
}

// --- buildHookEnv coverage -------------------------------------------------

func TestBuildHookEnvIncludesProjmuxVars(t *testing.T) {
	t.Parallel()

	env := buildHookEnv(Context{
		SessionName: "workspace",
		CWD:         "/tmp/repo",
		Kind:        "persistent",
		Socket:      "projmux",
		PaneID:      "%7",
		Env:         map[string]string{"FOO": "bar"},
	}, "0.0.0-test")

	want := []string{
		"FOO=bar",
		"PROJMUX_SESSION=workspace",
		"PROJMUX_CWD=/tmp/repo",
		"PROJMUX_SESSION_KIND=persistent",
		"PROJMUX_VERSION=0.0.0-test",
		"PROJMUX_SOCKET=projmux",
		"PROJMUX_PANE=%7",
	}
	joined := strings.Join(env, "\n")
	for _, line := range want {
		if !strings.Contains(joined, line) {
			t.Fatalf("env missing %q in:\n%s", line, joined)
		}
	}
}

func TestBuildHookEnvOmitsSocketWhenEmpty(t *testing.T) {
	t.Parallel()

	env := buildHookEnv(Context{
		SessionName: "workspace",
		CWD:         "/tmp/repo",
		Kind:        "ephemeral",
	}, "0.0.0-test")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "PROJMUX_SOCKET") {
		t.Fatalf("env should not include PROJMUX_SOCKET when empty:\n%s", joined)
	}
}

func TestDisplayEventNameMarksDeprecatedEvents(t *testing.T) {
	t.Parallel()

	if got := DisplayEventName(EventPaneStartup); got != "pane-startup (deprecated)" {
		t.Fatalf("DisplayEventName(pane-startup) = %q", got)
	}
	if got := DisplayEventName(EventSendNoti); got != "send-noti" {
		t.Fatalf("DisplayEventName(send-noti) = %q", got)
	}
}

// --- trust store roundtrip -------------------------------------------------

func TestTrustedProjectsStoreRoundTrip(t *testing.T) {
	t.Parallel()

	path := testTrustStorePath(t)
	store := trustedProjects{}
	at := time.Date(2026, 5, 10, 1, 2, 3, 0, time.UTC)
	store.trust("/repo", ".projmux/config.toml", "abc123", at)
	if err := store.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadTrustedProjects(path)
	if err != nil {
		t.Fatalf("loadTrustedProjects: %v", err)
	}
	file, ok := got.trustedFile("/repo", ".projmux/config.toml")
	if !ok {
		t.Fatalf("trusted file missing: %#v", got)
	}
	if file.SHA256 != "abc123" {
		t.Fatalf("SHA256 = %q, want abc123", file.SHA256)
	}
	if !file.TrustedAt.Equal(at) {
		t.Fatalf("TrustedAt = %s, want %s", file.TrustedAt, at)
	}
}

// --- helpers ---------------------------------------------------------------

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

func writeFileEnsuringDir(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
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

// Silence unused warning when running selective tests.
var _ = io.Discard
