package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseProjectConfigSupportedSections(t *testing.T) {
	t.Parallel()

	cfg, err := ParseProjectConfig(`
[startup]
run = "git status --short"

[hooks.pre-create]
run = "echo pre"

[hooks.post-create]
run = "echo post"

[hooks.pane-startup]
run = "echo pane-command"

[hooks.post-attach]
run = "echo attached"

[env]
FOO = "bar"
QUOTED = "a \"quoted\" value"

[kube]
context = "dev-cluster"
namespace = "tools"
`)
	if err != nil {
		t.Fatalf("ParseProjectConfig() error = %v", err)
	}
	if cfg.StartupRun != "git status --short" {
		t.Fatalf("StartupRun = %q", cfg.StartupRun)
	}
	if cfg.Hooks[EventPreCreate] != "echo pre" || cfg.Hooks[EventPostCreate] != "echo post" || cfg.Hooks[EventPaneStartup] != "echo pane-command" || cfg.Hooks[EventPostAttach] != "echo attached" {
		t.Fatalf("Hooks = %#v", cfg.Hooks)
	}
	if cfg.Env["FOO"] != "bar" || cfg.Env["QUOTED"] != `a "quoted" value` {
		t.Fatalf("Env = %#v", cfg.Env)
	}
	sessionEnv := cfg.SessionEnv()
	for _, key := range []string{"PROJMUX_KUBE_CONTEXT", "KUBE_CONTEXT"} {
		if sessionEnv[key] != "dev-cluster" {
			t.Fatalf("SessionEnv[%s] = %q", key, sessionEnv[key])
		}
	}
	for _, key := range []string{"PROJMUX_KUBE_NAMESPACE", "KUBE_NAMESPACE"} {
		if sessionEnv[key] != "tools" {
			t.Fatalf("SessionEnv[%s] = %q", key, sessionEnv[key])
		}
	}
}

func TestParseProjectConfigRejectsInternalTmuxHooks(t *testing.T) {
	t.Parallel()

	_, err := ParseProjectConfig(`
[hooks.after-select-pane]
run = "echo nope"
`)
	if err == nil {
		t.Fatal("expected unsupported hook event error")
	}
	if !strings.Contains(err.Error(), "unsupported section") {
		t.Fatalf("error = %v, want unsupported section", err)
	}
}

func TestUpdateProjectConfigPreservesHooksAndWritesEditableSections(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	path := writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo post"

[env]
ZED = "last"
`)

	_, err := UpdateProjectConfig(path, func(cfg *ProjectConfig) error {
		cfg.StartupRun = "codex"
		cfg.Env["ALPHA"] = "first"
		cfg.Kube.Context = "dev"
		cfg.Kube.Namespace = "tools"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateProjectConfig() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `[hooks.post-create]
run = "echo post"

[startup]
run = "codex"

[env]
ALPHA = "first"
ZED = "last"

[kube]
context = "dev"
namespace = "tools"
`
	if string(got) != want {
		t.Fatalf("config.toml =\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateProjectConfigRejectsInvalidEnvKey(t *testing.T) {
	t.Parallel()

	if err := ValidateProjectEnvKey("1BAD"); err == nil {
		t.Fatal("ValidateProjectEnvKey accepted invalid key")
	}
	path := filepath.Join(t.TempDir(), ".projmux", "config.toml")
	_, err := UpdateProjectConfig(path, func(cfg *ProjectConfig) error {
		cfg.Env["1BAD"] = "value"
		return nil
	})
	if err == nil {
		t.Fatal("UpdateProjectConfig accepted invalid env key")
	}
}

func TestRunnerPaneStartupUsesTrustedProjectConfigWithoutHookFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "git status --short"
`)

	promptCalls := 0
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(req ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			if req.RelativePath != projectConfigRelativePath {
				t.Fatalf("RelativePath = %q, want %q", req.RelativePath, projectConfigRelativePath)
			}
			return ProjectHookAllowOnce
		},
	}

	result, err := runner.Run(context.Background(), EventPaneStartup, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Stdout != "git status --short" {
		t.Fatalf("pane-startup stdout = %q, want config startup command", result.Stdout)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
}

func TestRunnerPaneStartupConfigPrecedenceStartupOverHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	dir := t.TempDir()
	globalConfigPath := filepath.Join(dir, "global", "config.toml")
	if err := os.MkdirAll(filepath.Dir(globalConfigPath), 0o755); err != nil {
		t.Fatalf("MkdirAll global: %v", err)
	}
	if err := os.WriteFile(globalConfigPath, []byte(`
[hooks.pane-startup]
run = "echo global-command"
`), 0o644); err != nil {
		t.Fatalf("WriteFile global: %v", err)
	}
	// Legacy script files in the historical layout must be silently ignored
	// by the runner; only declarative entries execute.
	cwd := filepath.Join(dir, "repo")
	writeHook(t, filepath.Join(cwd, ".projmux", "pane-startup"), "echo legacy-script-should-not-run\n", 0o755)
	writeProjectConfig(t, cwd, `
[hooks.pane-startup]
run = "echo config-hook-command"

[startup]
run = "startup-direct-command"
`)

	runner := &Runner{
		GlobalConfigPath:     globalConfigPath,
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
	}

	result, err := runner.Run(context.Background(), EventPaneStartup, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Stdout != "startup-direct-command" {
		t.Fatalf("pane-startup stdout = %q, want startup direct command to win", result.Stdout)
	}
}

func TestRunnerProjectConfigEnvAndKubeInjectedIntoConfigHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.post-create]
run = "echo env=$FOO ctx=$PROJMUX_KUBE_CONTEXT ns=$PROJMUX_KUBE_NAMESPACE"

[env]
FOO = "bar"

[kube]
context = "dev"
namespace = "tools"
`)

	var logger bytes.Buffer
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
		Logger:               &logger,
	}
	_, err := runner.Run(context.Background(), EventPostCreate, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := logger.String()
	if !strings.Contains(got, "[post-create] env=bar ctx=dev ns=tools") {
		t.Fatalf("logger output missing config env:\n%s", got)
	}
}

func TestRunnerProjectConfigPreCreateAbort(t *testing.T) {
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
	_, err := runner.Run(context.Background(), EventPreCreate, Context{CWD: cwd})
	if err == nil {
		t.Fatal("expected pre-create config error")
	}
	if !strings.Contains(err.Error(), "exited with status 9") {
		t.Fatalf("pre-create error = %v, want exit status", err)
	}
	if !strings.Contains(logger.String(), "[pre-create] before-abort") {
		t.Fatalf("logger output missing config hook stdout:\n%s", logger.String())
	}
}

func TestRunnerProjectConfigAllowAlwaysPersistsHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	configPath := writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       trustPath,
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowAlways },
	}
	_, err := runner.Run(context.Background(), EventPaneStartup, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	sum, _, err := hashHookFile(configPath)
	if err != nil {
		t.Fatalf("hashHookFile: %v", err)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects: %v", err)
	}
	file, ok := store.trustedFile(cwd, projectConfigRelativePath)
	if !ok {
		t.Fatalf("trusted config missing: %#v", store)
	}
	if file.SHA256 != sum {
		t.Fatalf("stored sha256 = %q, want %q", file.SHA256, sum)
	}
}

func TestTrustProjectConfigPersistsHash(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	configPath := writeProjectConfig(t, cwd, `
[startup]
run = "echo ready"
`)
	trustPath := testTrustStorePath(t)

	sum, err := TrustProjectConfig(cwd, trustPath)
	if err != nil {
		t.Fatalf("TrustProjectConfig() error = %v", err)
	}
	wantSum, _, err := hashHookFile(configPath)
	if err != nil {
		t.Fatalf("hashHookFile() error = %v", err)
	}
	if sum != wantSum {
		t.Fatalf("TrustProjectConfig hash = %q, want %q", sum, wantSum)
	}
	store, err := loadTrustedProjects(trustPath)
	if err != nil {
		t.Fatalf("loadTrustedProjects() error = %v", err)
	}
	file, ok := store.trustedFile(cwd, projectConfigRelativePath)
	if !ok {
		t.Fatalf("trusted config missing: %#v", store)
	}
	if file.SHA256 != wantSum {
		t.Fatalf("stored sha256 = %q, want %q", file.SHA256, wantSum)
	}
}

func TestRunnerProjectConfigKillSwitchDisablesConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}

	t.Setenv("PROJMUX_PROJECT_HOOKS", "off")
	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[startup]
run = "should-not-run"
`)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			t.Fatal("project config prompt should not be called when kill switch is off")
			return ProjectHookDeny
		},
	}

	result, err := runner.Run(context.Background(), EventPaneStartup, Context{CWD: cwd})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Stdout != "" {
		t.Fatalf("pane-startup stdout = %q, want empty", result.Stdout)
	}
}

func TestRunnerProjectSessionEnvUsesTrustedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[env]
FOO = "bar"

[kube]
context = "dev"
namespace = "tools"
`)
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt:    func(ProjectHookPromptRequest) ProjectHookDecision { return ProjectHookAllowOnce },
	}

	env := runner.ProjectSessionEnv(cwd)
	want := map[string]string{
		"FOO":                    "bar",
		"PROJMUX_KUBE_CONTEXT":   "dev",
		"KUBE_CONTEXT":           "dev",
		"PROJMUX_KUBE_NAMESPACE": "tools",
		"KUBE_NAMESPACE":         "tools",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("ProjectSessionEnv()[%s] = %q, want %q; env=%#v", key, env[key], value, env)
		}
	}
}

func TestRunnerProjectConfigTrustCacheSharedByHooksAndSessionEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fixtures require POSIX")
	}
	t.Parallel()

	cwd := t.TempDir()
	writeProjectConfig(t, cwd, `
[hooks.pre-create]
run = "true"

[env]
FOO = "bar"
`)
	promptCalls := 0
	runner := &Runner{
		DiscoverProjectHooks: true,
		ProjectHooksFilePath: testProjectHooksFilePath(t),
		TrustStorePath:       testTrustStorePath(t),
		ProjectHookPrompt: func(ProjectHookPromptRequest) ProjectHookDecision {
			promptCalls++
			return ProjectHookAllowOnce
		},
	}

	if _, err := runner.Run(context.Background(), EventPreCreate, Context{CWD: cwd, SessionName: "workspace"}); err != nil {
		t.Fatalf("Run(pre-create) error = %v", err)
	}
	env := runner.ProjectSessionEnv(cwd)
	if env["FOO"] != "bar" {
		t.Fatalf("ProjectSessionEnv()[FOO] = %q, want bar; env=%#v", env["FOO"], env)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
}

func writeProjectConfig(t *testing.T, cwd, body string) string {
	t.Helper()
	path := filepath.Join(cwd, projectConfigRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
