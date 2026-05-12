package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// newHookTestCommand builds a hookCommand whose home / env / cwd are all
// rooted under a temp dir so the test can drive list / edit / validate /
// trust / untrust without touching the real XDG locations.
//
// projectCtx, when non-empty, is exposed via PROJMUX_CWD so
// resolveProjectContext picks it up without walking the real filesystem.
func newHookTestCommand(t *testing.T, home, projectCtx, stdin string) (*hookCommand, string, string) {
	t.Helper()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	mustMkdirAll(t, filepath.Join(configHome, "projmux"))
	mustMkdirAll(t, filepath.Join(stateHome, "projmux"))

	env := map[string]string{
		"XDG_CONFIG_HOME": configHome,
		"XDG_STATE_HOME":  stateHome,
	}
	if projectCtx != "" {
		env["PROJMUX_CWD"] = projectCtx
	}

	cmd := &hookCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(name string) string { return env[name] },
		getwd:     func() (string, error) { return home, nil },
		stdin:     strings.NewReader(stdin),
		editorRunner: func(string, []string, io.Writer, io.Writer) error {
			return errors.New("editor runner should not be called")
		},
	}
	globalPath := filepath.Join(configHome, "projmux", "config.toml")
	trustPath := filepath.Join(stateHome, "projmux", "trusted-projects.json")
	return cmd, globalPath, trustPath
}

func writeHookFile(t *testing.T, path, contents string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestHookCommandRequiresSubcommand confirms `projmux hook` with no verb
// returns a usage error so callers do not silently no-op.
func TestHookCommandRequiresSubcommand(t *testing.T) {
	t.Parallel()
	cmd, _, _ := newHookTestCommand(t, t.TempDir(), "", "")
	var stdout, stderr bytes.Buffer
	err := cmd.Run(nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run() with no args returned nil, want usage error")
	}
	if !strings.Contains(stderr.String(), "projmux hook list") {
		t.Fatalf("stderr missing usage banner: %q", stderr.String())
	}
}

// TestHookCommandRejectsUnknownVerb confirms unknown subcommands print
// usage + return a usage error rather than dispatching silently.
func TestHookCommandRejectsUnknownVerb(t *testing.T) {
	t.Parallel()
	cmd, _, _ := newHookTestCommand(t, t.TempDir(), "", "")
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"explode"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run(explode) returned nil, want usage error")
	}
	if !strings.Contains(err.Error(), "unknown hook subcommand") {
		t.Fatalf("err = %v, want unknown hook subcommand", err)
	}
}

// TestHookList_DefaultRendersBothScopes confirms the default `hook list`
// view prints both the global config table and the project config table —
// matching the Settings popup which always shows both axes in one view.
// This is the headless / TTY-independent behaviour the Phase 3 spec calls
// out.
func TestHookList_DefaultRendersBothScopes(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[hooks.post-attach]
run = "echo project-attach"
`)

	cmd, globalPath, _ := newHookTestCommand(t, home, project, "")
	writeHookFile(t, globalPath, `
[hooks.pane-startup]
run = "echo global-focus"
`)

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook list error = %v (stderr=%q)", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "global config:") {
		t.Fatalf("stdout missing global table header: %q", out)
	}
	if !strings.Contains(out, "project config:") {
		t.Fatalf("stdout missing project table header: %q", out)
	}
	if !strings.Contains(out, "echo project-attach") {
		t.Fatalf("stdout missing project hook value: %q", out)
	}
	if !strings.Contains(out, "echo global-focus") {
		t.Fatalf("stdout missing global hook value: %q", out)
	}
	if !strings.Contains(out, "pane-startup (deprecated)") {
		t.Fatalf("stdout missing pane-startup deprecation badge: %q", out)
	}
	if !strings.Contains(out, "send-noti") {
		t.Fatalf("stdout missing send-noti row: %q", out)
	}
}

// TestHookList_ProjectOnlyDegradesWithoutContext covers the headless
// path where the user runs `projmux hook list --project` outside any
// project tree — the CLI should print a friendly notice instead of
// crashing or failing.
func TestHookList_ProjectOnlyDegradesWithoutContext(t *testing.T) {
	t.Parallel()
	cmd, _, _ := newHookTestCommand(t, t.TempDir(), "", "")
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--project"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook list --project error = %v", err)
	}
	if !strings.Contains(stdout.String(), "no project context") {
		t.Fatalf("stdout missing no-project notice: %q", stdout.String())
	}
}

// TestHookList_ScopeFlagsMutuallyExclusive enforces the documented flag
// contract: at most one of --global / --project / --effective.
func TestHookList_ScopeFlagsMutuallyExclusive(t *testing.T) {
	t.Parallel()
	cmd, _, _ := newHookTestCommand(t, t.TempDir(), "", "")
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"list", "--global", "--project"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("Run(list --global --project) returned nil, want usage error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutually exclusive", err)
	}
}

// TestHookList_EffectiveUsesMergeEngine verifies the Phase 3 spec
// requirement: the `--effective` view delegates to hooks.MergeEffective
// so its source labels match the Settings popup. Project-defined hooks
// outrank global ones with the same event. After main #165 added the
// Hooks field to EffectiveConfig, the same engine drives [env] / [kube]
// / [startup] AND [hooks] — the CLI surfaces all four sections.
func TestHookList_EffectiveUsesMergeEngine(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[env]
DATABASE_URL = "postgres://project"

[hooks.pane-startup]
run = "echo project-focus"
`)
	cmd, globalPath, _ := newHookTestCommand(t, home, project, "")
	writeHookFile(t, globalPath, `
[env]
DATABASE_URL = "postgres://global"
EDITOR = "vim"

[hooks.pane-startup]
run = "echo global-focus"
[hooks.post-attach]
run = "echo global-attach"
`)

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--effective"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook list --effective error = %v (stderr=%q)", err, stderr.String())
	}
	out := stdout.String()
	// Hooks section appears with EVENT/SOURCE/RUN header.
	if !strings.Contains(out, "[hooks]") || !strings.Contains(out, "EVENT") {
		t.Fatalf("stdout missing hooks effective table: %q", out)
	}
	// pane-startup is project-wins.
	if !strings.Contains(out, "pane-startup (deprecated)") {
		t.Fatalf("deprecated pane-startup badge missing: %q", out)
	}
	if !strings.Contains(out, "echo project-focus") {
		t.Fatalf("project pane-startup value not surfaced: %q", out)
	}
	// post-attach is global-only — surfaced with global source.
	if !strings.Contains(out, "echo global-attach") {
		t.Fatalf("global post-attach value not surfaced: %q", out)
	}
	if !strings.Contains(out, "send-noti") {
		t.Fatalf("stdout missing send-noti row: %q", out)
	}
	// Conflict resolution check: project value rendered, global shadowed.
	if strings.Contains(out, "echo global-focus") {
		t.Fatalf("global pane-startup leaked into effective view (project should win): %q", out)
	}
	// env section still present.
	if !strings.Contains(out, "[env]") || !strings.Contains(out, "postgres://project") {
		t.Fatalf("env section missing or project did not win: %q", out)
	}
	// EDITOR only on global → labelled global.
	if !strings.Contains(out, "vim") {
		t.Fatalf("EDITOR row missing global value: %q", out)
	}
	// kube + startup sections appear even when unset (default).
	if !strings.Contains(out, "[kube]") || !strings.Contains(out, "[startup]") {
		t.Fatalf("kube/startup section headers missing: %q", out)
	}
}

// TestHookList_EffectiveRedactsEnvSecretsOnly covers the redaction
// boundary: sensitive env keys collapse to <redacted>, but a [startup]
// or [hooks] entry whose command happens to contain the substring
// "TOKEN" or "SECRET" must NOT be mangled — those are legitimate
// command lines, not credential values.
func TestHookList_EffectiveRedactsEnvSecretsOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[env]
GH_TOKEN = "ghp_xxx"
EDITOR = "vim"

[startup]
run = "echo MY_TOKEN literal"

[hooks.pane-startup]
run = "kubectl get secret"
`)
	cmd, _, _ := newHookTestCommand(t, home, project, "")
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--effective"}, &stdout, &stderr); err != nil {
		t.Fatalf("hook list --effective error = %v", err)
	}
	out := stdout.String()
	if strings.Contains(out, "ghp_xxx") {
		t.Fatalf("env GH_TOKEN value leaked: %q", out)
	}
	if !strings.Contains(out, hooks.SensitiveRedaction) {
		t.Fatalf("env redaction sentinel missing: %q", out)
	}
	if !strings.Contains(out, "echo MY_TOKEN literal") {
		t.Fatalf("startup run was redacted but should be verbatim: %q", out)
	}
	if !strings.Contains(out, "kubectl get secret") {
		t.Fatalf("hook run was redacted but should be verbatim: %q", out)
	}
}

// TestHookValidate_OK verifies validate exits 0 when all configs parse
// and only reference supported hook events. Both axes must be reported
// so CI logs can pinpoint which side failed when one does break.
func TestHookValidate_OK(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[hooks.post-attach]
run = "echo hi"
`)
	cmd, globalPath, _ := newHookTestCommand(t, home, project, "")
	writeHookFile(t, globalPath, `
[hooks.pane-startup]
run = "echo focus"
`)
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"validate"}, &stdout, &stderr); err != nil {
		t.Fatalf("validate err = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "global") || !strings.Contains(out, "project") {
		t.Fatalf("stdout missing axis labels: %q", out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("stdout missing OK marker: %q", out)
	}
}

// TestHookValidate_ReportsProjectParseError verifies a malformed TOML in
// the project config surfaces as a parse error with a non-default exit
// code so CI scripts can branch on it.
func TestHookValidate_ReportsProjectParseError(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), "[hooks.post-attach\nrun =")
	cmd, _, _ := newHookTestCommand(t, home, project, "")
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"validate"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("validate returned nil error on malformed config")
	}
	var hookErr *hookValidateError
	if !errors.As(err, &hookErr) {
		t.Fatalf("err = %T (%v), want *hookValidateError", err, err)
	}
	if hookErr.ExitCode() == 0 {
		t.Fatalf("ExitCode() = 0, want non-zero")
	}
	if !strings.Contains(stdout.String(), "PARSE ERROR") {
		t.Fatalf("stdout missing PARSE ERROR marker: %q", stdout.String())
	}
}

// TestHookEdit_GlobalInlineWritesAndPreserves drives the inline edit
// flow for the global config. Phase 2.6 is declarative-only so the
// edit always writes a [hooks.<event>] run line — there is no script
// branch to choose.
func TestHookEdit_GlobalInlineWritesAndPreserves(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// stdin: newline-terminated new run command.
	cmd, globalPath, _ := newHookTestCommand(t, home, "", "echo new-global\n")
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"edit", "--global", "post-attach"}, &stdout, &stderr); err != nil {
		t.Fatalf("edit --global err = %v (stderr=%q)", err, stderr.String())
	}
	cfg, err := hooks.LoadGlobalConfig(globalPath)
	if err != nil {
		t.Fatalf("LoadGlobalConfig() err = %v", err)
	}
	got := cfg.Hooks[hooks.Event("post-attach")]
	if got != "echo new-global" {
		t.Fatalf("post-attach run = %q, want echo new-global", got)
	}
}

// TestHookEdit_ProjectInlineTrustsAfterWrite verifies the project edit
// path always re-trusts the config so the runner does not lock the user
// out of the entry they just authored. This mirrors the Settings popup's
// "write + trust" combo.
func TestHookEdit_ProjectInlineTrustsAfterWrite(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	cmd, _, trustPath := newHookTestCommand(t, home, project, "echo focus-cmd\n")
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"edit", "pane-startup"}, &stdout, &stderr); err != nil {
		t.Fatalf("edit err = %v (stderr=%q)", err, stderr.String())
	}
	projectPath := filepath.Join(project, ".projmux", "config.toml")
	cfg, err := hooks.LoadProjectConfigFile(projectPath)
	if err != nil {
		t.Fatalf("LoadProjectConfigFile() err = %v", err)
	}
	if got := cfg.Hooks[hooks.Event("pane-startup")]; got != "echo focus-cmd" {
		t.Fatalf("pane-startup run = %q, want echo focus-cmd", got)
	}
	trusted, _, err := hooks.IsProjectConfigTrusted(project, trustPath)
	if err != nil {
		t.Fatalf("IsProjectConfigTrusted() err = %v", err)
	}
	if !trusted {
		t.Fatalf("project config not trusted after edit, expected auto-trust")
	}
}

// TestHookEdit_RejectsUnsupportedEvent confirms the CLI refuses events
// not listed in hooks.SupportedEvents — the same allow-list the parser
// uses, so the surfaces stay consistent.
func TestHookEdit_RejectsUnsupportedEvent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cmd, _, _ := newHookTestCommand(t, home, "", "echo never\n")
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"edit", "--global", "not-a-real-event"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("edit unknown event returned nil, want usage error")
	}
	if !strings.Contains(err.Error(), "unsupported hook event") {
		t.Fatalf("err = %v, want unsupported hook event", err)
	}
}

// TestHookEdit_EditorRunnerInvoked verifies --editor delegates to the
// injected editor runner — the headless test stub captures the command
// + path so we can assert the contract without spawning $EDITOR.
func TestHookEdit_EditorRunnerInvoked(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))

	cmd, _, _ := newHookTestCommand(t, home, project, "")
	cmd.lookupEnv = wrapLookupEnv(cmd.lookupEnv, map[string]string{"EDITOR": "stub-editor"})

	var captured struct {
		command string
		args    []string
	}
	cmd.editorRunner = func(command string, args []string, _, _ io.Writer) error {
		captured.command = command
		captured.args = append([]string(nil), args...)
		// Simulate editor writing a minimal valid config.
		if len(args) == 0 {
			return errors.New("editor invoked without path")
		}
		return os.WriteFile(args[len(args)-1], []byte("[hooks.post-attach]\nrun = \"echo edited\"\n"), 0o644)
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"edit", "--editor", "post-attach"}, &stdout, &stderr); err != nil {
		t.Fatalf("edit --editor err = %v (stderr=%q)", err, stderr.String())
	}
	if captured.command != "stub-editor" {
		t.Fatalf("editor command = %q, want stub-editor", captured.command)
	}
	wantSuffix := filepath.Join(".projmux", "config.toml")
	if len(captured.args) == 0 || !strings.HasSuffix(captured.args[len(captured.args)-1], wantSuffix) {
		t.Fatalf("editor args = %v, want last arg ending in %s", captured.args, wantSuffix)
	}
}

// TestHookTrustUntrust_Roundtrip drives the full trust -> untrust cycle
// headlessly: the trust store entry must materialise after `hook trust`
// and disappear after `hook untrust`, with the second untrust call
// reporting "no trust entry" rather than failing.
func TestHookTrustUntrust_Roundtrip(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[hooks.post-attach]
run = "echo hi"
`)

	cmd, _, trustPath := newHookTestCommand(t, home, project, "")

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"trust"}, &stdout, &stderr); err != nil {
		t.Fatalf("trust err = %v (stderr=%q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "trusted") {
		t.Fatalf("trust stdout missing marker: %q", stdout.String())
	}
	trusted, _, err := hooks.IsProjectConfigTrusted(project, trustPath)
	if err != nil {
		t.Fatalf("IsProjectConfigTrusted() err = %v", err)
	}
	if !trusted {
		t.Fatalf("trust store entry not created")
	}

	stdout.Reset()
	stderr.Reset()
	if err := cmd.Run([]string{"untrust"}, &stdout, &stderr); err != nil {
		t.Fatalf("untrust err = %v", err)
	}
	if !strings.Contains(stdout.String(), "untrusted") {
		t.Fatalf("untrust stdout missing marker: %q", stdout.String())
	}
	trustedAfter, _, err := hooks.IsProjectConfigTrusted(project, trustPath)
	if err != nil {
		t.Fatalf("IsProjectConfigTrusted() second err = %v", err)
	}
	if trustedAfter {
		t.Fatalf("trust entry still present after untrust")
	}

	// Idempotent untrust — second call reports no entry but does not error.
	stdout.Reset()
	if err := cmd.Run([]string{"untrust"}, &stdout, &stderr); err != nil {
		t.Fatalf("untrust (idempotent) err = %v", err)
	}
	if !strings.Contains(stdout.String(), "no trust entry") {
		t.Fatalf("idempotent untrust stdout = %q, want no trust entry", stdout.String())
	}
}

// TestHookTrust_AcceptsExplicitProjectArg verifies the documented form
// `projmux hook trust <project>` resolves against the supplied path,
// not the resolved project context. This is the form CI scripts use
// when they iterate over a workspace from outside any project tree.
func TestHookTrust_AcceptsExplicitProjectArg(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	project := filepath.Join(home, "repo")
	mustMkdirAll(t, filepath.Join(project, ".projmux"))
	writeHookFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[hooks.pane-startup]
run = "echo hi"
`)

	// Run from outside the project context — no PROJMUX_CWD, getwd
	// returns the home dir which is NOT a project root.
	cmd, _, trustPath := newHookTestCommand(t, home, "", "")

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"trust", project}, &stdout, &stderr); err != nil {
		t.Fatalf("trust err = %v (stderr=%q)", err, stderr.String())
	}
	trusted, _, err := hooks.IsProjectConfigTrusted(project, trustPath)
	if err != nil {
		t.Fatalf("IsProjectConfigTrusted() err = %v", err)
	}
	if !trusted {
		t.Fatalf("explicit-path trust did not persist")
	}
}

// TestHookTrust_RejectsWhenContextMissing covers the error path: if
// `hook trust` is invoked without an explicit path AND no project
// context can be resolved, the CLI must refuse rather than silently
// trust the cwd.
func TestHookTrust_RejectsWhenContextMissing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cmd, _, _ := newHookTestCommand(t, home, "", "")
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"trust"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("trust without context returned nil, want usage error")
	}
	if !strings.Contains(err.Error(), "project context") {
		t.Fatalf("err = %v, want project context message", err)
	}
}

// --- small helpers --------------------------------------------------------

// wrapLookupEnv returns a lookup function that overrides values for the
// supplied keys while delegating everything else to base. Used to inject
// $EDITOR for editor-related tests without rebuilding the full env map.
func wrapLookupEnv(base func(string) string, overrides map[string]string) func(string) string {
	return func(name string) string {
		if v, ok := overrides[name]; ok {
			return v
		}
		if base == nil {
			return ""
		}
		return base(name)
	}
}
