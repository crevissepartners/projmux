package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// providersTestCommand is a config command whose policy file lives under an
// isolated home. XDG_CONFIG_HOME is unset, so the file is
// <home>/.config/projmux/ai-enabled-agents.
func providersTestCommand(t *testing.T) (*configCommand, string) {
	t.Helper()
	home := t.TempDir()
	return &configCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}, filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName)
}

func readPolicyFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content), true
}

// TestConfigProvidersRoundTripsThroughTheSettingsWriter is the route's
// round-trip contract: the list reports the policy the launch gate reads, a
// mutation prints the resulting line for the provider it touched, both are
// idempotent, disabling every provider persists as "none enabled" rather than
// falling back to the shipped default, and the Settings toggle writes the same
// file through the same helper.
func TestConfigProvidersRoundTripsThroughTheSettingsWriter(t *testing.T) {
	t.Parallel()

	cmd, path := providersTestCommand(t)
	run := func(args ...string) string {
		t.Helper()
		stdout, stderr, err := runRoute(t, cmd, append([]string{"providers"}, args...)...)
		if err != nil {
			t.Fatalf("config providers %v error = %v", args, err)
		}
		if stderr != "" {
			t.Fatalf("config providers %v stderr = %q, want none", args, stderr)
		}
		return stdout
	}

	// A fresh install has no file: every known provider is enabled, in the
	// order the Settings toggle list shows them.
	if got, want := run(), "claude enabled\ncodex enabled\nantigravity enabled\n"; got != want {
		t.Fatalf("fresh list = %q, want %q", got, want)
	}
	// Enabling an already enabled provider changes nothing, so it must not
	// freeze the shipped default into a file.
	if got, want := run("--enable", "codex"), "codex enabled\n"; got != want {
		t.Fatalf("idempotent enable = %q, want %q", got, want)
	}
	if _, exists := readPolicyFile(t, path); exists {
		t.Fatal("an idempotent --enable on a fresh install wrote the policy file")
	}

	if got, want := run("--disable", "codex"), "codex disabled\n"; got != want {
		t.Fatalf("disable = %q, want %q", got, want)
	}
	if got, _ := readPolicyFile(t, path); got != "claude,antigravity\n" {
		t.Fatalf("policy file = %q, want claude,antigravity", got)
	}
	if got, want := run("--disable=codex"), "codex disabled\n"; got != want {
		t.Fatalf("idempotent disable = %q, want %q", got, want)
	}
	if got, want := run(), "claude enabled\ncodex disabled\nantigravity enabled\n"; got != want {
		t.Fatalf("list after disable = %q, want %q", got, want)
	}
	// Case and surrounding space are normalized the way the policy file is.
	if got, want := run("--enable", " Codex "), "codex enabled\n"; got != want {
		t.Fatalf("enable = %q, want %q", got, want)
	}
	if got, _ := readPolicyFile(t, path); got != "claude,codex,antigravity\n" {
		t.Fatalf("policy file = %q, want known-provider order", got)
	}

	// Disabling every provider is a real policy: an empty, present file.
	for _, provider := range config.KnownAIAgentProviders() {
		run("--disable", string(provider))
	}
	if got, _ := readPolicyFile(t, path); got != "\n" {
		t.Fatalf("policy file after disabling everything = %q, want an empty list", got)
	}
	if got, want := run(), "claude disabled\ncodex disabled\nantigravity disabled\n"; got != want {
		t.Fatalf("list with none enabled = %q, want %q", got, want)
	}
	if got := aiEnabledAgents(cmd.homeDir, cmd.lookupEnv); len(got) != 0 {
		t.Fatalf("launch gate reads %v with none enabled, want none (not the default)", got)
	}

	// The Settings toggle and the route are one writer over one file.
	settings := &settingsCommand{homeDir: cmd.homeDir, lookupEnv: cmd.lookupEnv}
	if err := settings.toggleAIEnabledAgent("antigravity"); err != nil {
		t.Fatalf("Settings toggle error = %v", err)
	}
	if got, want := run(), "claude disabled\ncodex disabled\nantigravity enabled\n"; got != want {
		t.Fatalf("list after Settings toggle = %q, want %q", got, want)
	}
	if err := settings.toggleAIEnabledAgent("antigravity"); err != nil {
		t.Fatalf("Settings toggle error = %v", err)
	}
	if got, _ := readPolicyFile(t, path); got != "\n" {
		t.Fatalf("policy file after toggling back = %q, want an empty list", got)
	}
	if err := settings.toggleAIEnabledAgent("shell"); err == nil || err.Error() != "unknown AI agent provider: shell" {
		t.Fatalf("Settings toggle of a non-provider = %v, want the unchanged unknown-provider error", err)
	}
}

// TestSettingsToggleOnAFreshInstallStillDisablesOneProvider pins the Settings
// toggle's pre-refactor behavior on a missing file: the first toggle turns one
// provider off and writes the remaining default set.
func TestSettingsToggleOnAFreshInstallStillDisablesOneProvider(t *testing.T) {
	t.Parallel()

	cmd, path := providersTestCommand(t)
	settings := &settingsCommand{homeDir: cmd.homeDir, lookupEnv: cmd.lookupEnv}
	if err := settings.toggleAIEnabledAgent("claude"); err != nil {
		t.Fatalf("toggle error = %v", err)
	}
	if got, _ := readPolicyFile(t, path); got != "codex,antigravity\n" {
		t.Fatalf("policy file = %q, want codex,antigravity", got)
	}
}

// TestConfigProvidersRejectsBadArgvAsUsageErrors pins exit code 2 for every
// malformed invocation, and that none of them writes the policy or prints.
func TestConfigProvidersRejectsBadArgvAsUsageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown provider", args: []string{"--enable", "gemini"}, want: `config providers --enable: unknown provider "gemini"; known providers: claude, codex, antigravity`},
		{name: "unknown provider on disable", args: []string{"--disable", "shell"}, want: `config providers --disable: unknown provider "shell"`},
		{name: "both flags", args: []string{"--enable", "claude", "--disable", "codex"}, want: "config providers accepts only one of --enable or --disable"},
		{name: "missing value", args: []string{"--enable"}, want: "config providers: flag needs an argument: -enable"},
		{name: "empty value", args: []string{"--disable="}, want: "config providers --disable requires a provider id: claude, codex, antigravity"},
		{name: "extra positional", args: []string{"--enable", "claude", "codex"}, want: "config providers does not accept positional arguments: codex"},
		{name: "bare positional", args: []string{"claude"}, want: "config providers does not accept positional arguments: claude"},
		{name: "unknown flag", args: []string{"--toggle", "claude"}, want: "config providers: flag provided but not defined: -toggle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, path := providersTestCommand(t)
			stdout, stderr, err := runRoute(t, cmd, append([]string{"providers"}, test.args...)...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("config providers %v error = %v, want a usage error (exit 2)", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("config providers %v error = %q, want it to contain %q", test.args, err, test.want)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("config providers %v printed stdout=%q stderr=%q; the usage error carries the message", test.args, stdout, stderr)
			}
			if _, exists := readPolicyFile(t, path); exists {
				t.Fatalf("config providers %v wrote the policy file", test.args)
			}
		})
	}
}

// TestDisabledProviderRefusalNamesTheConfigProvidersCommand drives the real
// launch gate: a provider disabled through `config providers` is refused by
// the gate `create agent`, `create window --provider`, and `agent resume`
// consume, the refusal names the exact command that re-enables it, and running
// that command clears the refusal.
func TestDisabledProviderRefusalNamesTheConfigProvidersCommand(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	ai := testAICommand(home)
	cmd := &configCommand{homeDir: ai.homeDir, lookupEnv: ai.lookupEnv}
	if _, _, err := runRoute(t, cmd, "providers", "--disable", "codex"); err != nil {
		t.Fatalf("config providers --disable codex error = %v", err)
	}

	err := ai.RequireAgentEnabled("codex")
	if err == nil {
		t.Fatal("a provider disabled through config providers passed the launch gate")
	}
	if IsUsageError(err) {
		t.Fatalf("the refusal changed classification to a usage error: %v", err)
	}
	if got, want := err.Error(), "AI agent codex is disabled; enable it with: projmux config providers --enable codex"; got != want {
		t.Fatalf("refusal = %q, want %q", got, want)
	}

	for path, want := range map[aiSplitLaunchPath]string{
		aiSplitLaunchDefault:   "AI split default codex is disabled; choose another default, use --agent shell, or enable it with: projmux config providers --enable codex",
		aiSplitLaunchPicker:    "AI agent codex is disabled; enable it with: projmux config providers --enable codex",
		aiSplitLaunchCanonical: "AI agent codex is disabled; enable it with: projmux config providers --enable codex",
		"direct":               "AI agent codex is disabled; pass --force-agent for this direct launch, or enable it with: projmux config providers --enable codex",
	} {
		got, disabled := ai.aiAgentDisabledLaunchMessage("codex", path)
		if !disabled || got != want {
			t.Fatalf("%s refusal = %q (disabled=%v), want %q", path, got, disabled, want)
		}
		if strings.Contains(got, "Settings > AI Settings") {
			t.Fatalf("%s refusal still names a Settings path: %q", path, got)
		}
	}

	if _, _, err := runRoute(t, cmd, "providers", "--enable", "codex"); err != nil {
		t.Fatalf("config providers --enable codex error = %v", err)
	}
	if err := ai.RequireAgentEnabled("codex"); err != nil {
		t.Fatalf("the command the refusal names did not clear it: %v", err)
	}
}
