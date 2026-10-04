package hooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Runner declarative behaviour -----------------------------------------

func TestProcessPostCreateContractRejectsInheritedAndConfiguredPane(t *testing.T) {
	t.Setenv("PROJMUX_PANE", "%inherited")
	t.Setenv("PROJMUX_RUNTIME", "inherited")
	t.Setenv("TMUX", "/tmp/inherited-server,42,1")
	t.Setenv("TMUX_PANE", "%inherited-tmux")
	cwd := t.TempDir()
	global := filepath.Join(cwd, "global.toml")
	writeFileEnsuringDir(t, global, `
[env]
PROJMUX_PANE = "%configured"
PROJMUX_RUNTIME = "configured"
[hooks.post-create]
runtime = "process"
run = "printf \"runtime=%s pane=%s session=%s:%s kind=%s:%s cwd=%s socket=%s tmux=%s tmux-pane=%s\\n\" \"$PROJMUX_RUNTIME\" \"${PROJMUX_PANE+x}\" \"${PROJMUX_SESSION+x}\" \"$PROJMUX_SESSION\" \"${PROJMUX_SESSION_KIND+x}\" \"$PROJMUX_SESSION_KIND\" \"$PROJMUX_CWD\" \"$PROJMUX_SOCKET\" \"${TMUX+x}\" \"${TMUX_PANE+x}\""
`)
	var output bytes.Buffer
	runner := &Runner{GlobalConfigPath: global, Logger: &output}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{Runtime: RuntimeProcess, CWD: cwd, Socket: "projmux"})
	if err != nil {
		t.Fatal(err)
	}
	want := "runtime=process pane= session=x: kind=x: cwd=" + cwd + " socket=projmux tmux= tmux-pane="
	if !strings.Contains(output.String(), want) {
		t.Fatalf("process hook environment: %q; want %q", output.String(), want)
	}
}

func TestTmuxHookPreservesInheritedRoutingEnvironment(t *testing.T) {
	t.Setenv("TMUX", "/tmp/inherited-server,42,1")
	t.Setenv("TMUX_PANE", "%inherited-tmux")
	env := buildHookEnv(Context{SessionName: "created", Kind: "persistent", PaneID: "%created"}, "test")
	for _, want := range []string{"TMUX=/tmp/inherited-server,42,1", "TMUX_PANE=%inherited-tmux", "PROJMUX_SESSION=created", "PROJMUX_SESSION_KIND=persistent", "PROJMUX_PANE=%created"} {
		found := slices.Contains(env, want)
		if !found {
			t.Fatalf("tmux hook lost %q", want)
		}
	}
}

func TestProcessPostCreateFailureIsReturnedWithoutChangingTmux(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	global := filepath.Join(cwd, "global.toml")
	writeFileEnsuringDir(t, global, "[hooks.post-create]\nruntime = \"process\"\nrun = \"exit 7\"\n")
	for _, host := range []string{RuntimeProcess, ""} {
		runner := &Runner{GlobalConfigPath: global, Logger: io.Discard}
		_, err := runner.Run(context.Background(), EventPostCreate, Context{Runtime: host, CWD: cwd})
		if host == RuntimeProcess {
			if err == nil || !strings.Contains(err.Error(), "status 7") {
				t.Fatalf("process hook failure: %v", err)
			}
		} else if err != nil {
			t.Fatalf("tmux hook failure became fatal: %v", err)
		}
	}
}

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

func TestBuildHookEnvOmitsEmptySocketAndPane(t *testing.T) {
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
	if strings.Contains(joined, "PROJMUX_PANE") {
		t.Fatalf("env should not include PROJMUX_PANE when empty:\n%s", joined)
	}
}

func TestDisplayEventNameReturnsEventName(t *testing.T) {
	t.Parallel()

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

func TestProcessPostCreateRequiresOptInBeforeTrustOrExecution(t *testing.T) {
	cwd := t.TempDir()
	global := filepath.Join(cwd, "global.toml")
	command := "printf ran"
	writeFileEnsuringDir(t, global, "[hooks.post-create]\nrun = "+strconv.Quote(command)+"\n")
	writeFileEnsuringDir(t, filepath.Join(cwd, ".projmux", "config.toml"), "[hooks.post-create]\nrun = "+strconv.Quote(command)+"\n")
	var output bytes.Buffer
	prompts := 0
	runner := &Runner{GlobalConfigPath: global, DiscoverProjectHooks: true, Logger: &output, ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision { prompts++; return ProjectHookDeny }}
	if _, err := runner.Run(context.Background(), EventPostCreate, Context{Runtime: RuntimeProcess, CWD: cwd}); err != nil {
		t.Fatal(err)
	}
	if prompts != 0 || strings.Contains(output.String(), "ran") {
		t.Fatalf("undeclared hook prompted/executed: prompts=%d output=%q", prompts, output.String())
	}
	output.Reset()
	runner.DiscoverProjectHooks = false
	if _, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "ran") {
		t.Fatal("legacy tmux hook did not run")
	}
}

func TestPostCreateOptInRunsForProcessAndTmux(t *testing.T) {
	for _, host := range []string{RuntimeProcess, ""} {
		t.Run("host="+host, func(t *testing.T) {
			cwd := t.TempDir()
			global := filepath.Join(cwd, "global.toml")
			writeFileEnsuringDir(t, global, "[hooks.post-create]\nruntime = \"process\"\nrun = \"printf opted\"\n")
			var output bytes.Buffer
			r := &Runner{GlobalConfigPath: global, Logger: &output}
			if _, err := r.Run(context.Background(), EventPostCreate, Context{Runtime: host, CWD: cwd}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "opted") {
				t.Fatalf("opt-in must preserve tmux and allow process: %s", output.String())
			}
		})
	}
}

func TestProcessUndeclaredHookPreservesMostRecentTmuxSession(t *testing.T) {
	// The CI Unit Tests job explicitly opts into isolated real-tmux tests.
	// Pure hook tests keep running without this integration dependency.
	if os.Getenv("PROJMUX_REAL_TMUX_STRICT") != "1" {
		t.Skip("set PROJMUX_REAL_TMUX_STRICT=1 to run the isolated real-tmux regression")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal("strict real tmux test requires tmux")
	}
	root := t.TempDir()
	// Follow isolatedTmuxSmokeRoot's short /tmp allocation: TMPDIR may be
	// longer than the Unix socket bound. HOME/config remain under TempDir.
	socketRoot, err := os.MkdirTemp("/tmp", "phk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketRoot); err != nil {
			t.Errorf("remove owned socket root: %v", err)
		}
	})
	socket := filepath.Join(socketRoot, "s")
	call := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		cmd.Env = []string{"HOME=" + root, "PATH=/usr/bin:/bin", "TERM=dumb"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated tmux %v: %v %s", args, err, out)
		}
		return string(out)
	}
	call("new-session", "-d", "-s", "recent", "exec sleep 60")
	t.Cleanup(func() { call("kill-server") })
	call("set-environment", "-t", "recent", "probe", "original")
	global := filepath.Join(root, "config.toml")
	command := `"$PROBE_TMUX" -S "$PROBE_SOCKET" set-environment -t "$PROJMUX_SESSION" probe changed`
	writeFileEnsuringDir(t, global, "[hooks.post-create]\nrun = "+strconv.Quote(command)+"\n")
	runner := &Runner{GlobalConfigPath: global, Logger: io.Discard}
	env := map[string]string{"PROBE_TMUX": binary, "PROBE_SOCKET": socket}
	// The legacy unguarded hook really mutates the recent session when its
	// target is empty. This control prevents a nonworking hook masking failure.
	if _, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: root, Env: env}); err != nil {
		t.Fatal(err)
	}
	if got := call("show-environment", "-t", "recent", "probe"); got != "probe=changed\n" {
		t.Fatalf("negative control did not reach recent session: %q", got)
	}
	call("set-environment", "-t", "recent", "probe", "original")
	if _, err := runner.Run(context.Background(), EventPostCreate, Context{Runtime: RuntimeProcess, CWD: root, Env: env}); err != nil {
		t.Fatal(err)
	}
	if got := call("show-environment", "-t", "recent", "probe"); got != "probe=original\n" {
		t.Fatalf("process hook mutated recent session: %q", got)
	}
}

func TestProcessPostCreateOptInIsIndependentByConfigTier(t *testing.T) {
	for _, tier := range []string{"global", "project"} {
		t.Run(tier, func(t *testing.T) {
			root := t.TempDir()
			global := filepath.Join(root, "global.toml")
			project := filepath.Join(root, ".projmux", "config.toml")
			declared := "[hooks.post-create]\nruntime = \"process\"\nrun = \"printf opted-%s\"\n"
			legacy := "[hooks.post-create]\nrun = \"printf legacy-%s\"\n"
			g, p := legacy, declared
			if tier == "global" {
				g, p = declared, legacy
			}
			writeFileEnsuringDir(t, global, fmt.Sprintf(g, "global"))
			writeFileEnsuringDir(t, project, fmt.Sprintf(p, "project"))
			var output bytes.Buffer
			prompts := 0
			r := &Runner{GlobalConfigPath: global, DiscoverProjectHooks: true, Logger: &output, ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision { prompts++; return ProjectHookAllowOnce }}
			if _, err := r.Run(context.Background(), EventPostCreate, Context{Runtime: RuntimeProcess, CWD: root}); err != nil {
				t.Fatal(err)
			}
			wantPrompts := 0
			if tier == "project" {
				wantPrompts = 1
			}
			if prompts != wantPrompts || strings.Contains(output.String(), "legacy-") || !strings.Contains(output.String(), "opted-"+tier) {
				t.Fatalf("tier filter: prompts=%d output=%q", prompts, output.String())
			}
		})
	}
}
