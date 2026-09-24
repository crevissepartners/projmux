package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// centralSettingsTestCommand is a config command whose central settings live
// under an isolated home and XDG_CONFIG_HOME.
func centralSettingsTestCommand(t *testing.T) *configCommand {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	return &configCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
	}
}

func centralSettingsFiles(t *testing.T, cmd *configCommand) (configToml string, paths config.Paths) {
	t.Helper()
	configToml, err := hooks.GlobalConfigPath(cmd.lookupEnv, cmd.homeDir)
	if err != nil {
		t.Fatalf("GlobalConfigPath error = %v", err)
	}
	paths, err = configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatalf("configPaths error = %v", err)
	}
	return configToml, paths
}

func runConfigRoute(t *testing.T, cmd *configCommand, args ...string) string {
	t.Helper()
	stdout, stderr, err := runRoute(t, cmd, args...)
	if err != nil {
		t.Fatalf("config %v error = %v", args, err)
	}
	if stderr != "" {
		t.Fatalf("config %v stderr = %q, want none", args, stderr)
	}
	return stdout
}

// TestConfigAgentQuestionsSetThenShowMatchesHookLoaders stores both values in
// one call, then proves the bare read and the question hook's own loaders see
// the same values from the same files.
func TestConfigAgentQuestionsSetThenShowMatchesHookLoaders(t *testing.T) {
	t.Parallel()

	cmd := centralSettingsTestCommand(t)
	_, paths := centralSettingsFiles(t, cmd)

	if got, want := runConfigRoute(t, cmd, "agent-questions"), "answering claude window 900\n"; got != want {
		t.Fatalf("fresh agent-questions = %q, want %q", got, want)
	}
	if got, want := runConfigRoute(t, cmd, "agent-questions", "--answering", "projmux", "--window", "unlimited"), "answering projmux window unlimited\n"; got != want {
		t.Fatalf("agent-questions set = %q, want %q", got, want)
	}
	if got, want := runConfigRoute(t, cmd, "agent-questions"), "answering projmux window unlimited\n"; got != want {
		t.Fatalf("agent-questions after set = %q, want %q", got, want)
	}

	if got := claudeQuestionAnsweringFromPaths(paths); got != config.AgentQuestionAnsweringProjmux {
		t.Fatalf("hook answering = %q, want projmux", got)
	}
	seconds, err := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	if err != nil || seconds != config.UnlimitedAgentQuestionWindowSeconds {
		t.Fatalf("hook window = %d, %v, want unlimited (%d)", seconds, err, config.UnlimitedAgentQuestionWindowSeconds)
	}

	// Either flag alone changes only its own value.
	if got, want := runConfigRoute(t, cmd, "agent-questions", "--window", "120"), "answering projmux window 120\n"; got != want {
		t.Fatalf("agent-questions --window = %q, want %q", got, want)
	}
	if got, want := runConfigRoute(t, cmd, "agent-questions", "--answering=Claude"), "answering claude window 120\n"; got != want {
		t.Fatalf("agent-questions --answering = %q, want %q", got, want)
	}
	if got := claudeQuestionAnsweringFromPaths(paths); got != config.AgentQuestionAnsweringClaude {
		t.Fatalf("hook answering = %q, want claude", got)
	}
	if seconds, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile()); seconds != 120 {
		t.Fatalf("hook window = %d, want 120", seconds)
	}
}

// TestConfigAgentQuestionsWindowReachesTheNextQuestionWithoutIntegrate is the
// no-re-integrate guarantee: a window stored with `config agent-questions` is
// what the hook's own resolver reads for the next question, Unlimited
// included, and that question's record deadline is created with it. Nothing
// integrates between the store and the question.
func TestConfigAgentQuestionsWindowReachesTheNextQuestionWithoutIntegrate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		window string
		want   time.Duration
	}{
		{window: "120", want: 120 * time.Second},
		{window: "3600", want: time.Hour},
		{window: config.AgentQuestionWindowUnlimitedWord, want: 604785 * time.Second},
	} {
		t.Run(test.window, func(t *testing.T) {
			t.Parallel()
			cmd := centralSettingsTestCommand(t)
			_, paths := centralSettingsFiles(t, cmd)
			if got := claudeQuestionWindowFromPaths(paths); got != 900*time.Second {
				t.Fatalf("window before the store = %s, want the default", got)
			}
			runConfigRoute(t, cmd, "agent-questions", "--window", test.window)
			if got := claudeQuestionWindowFromPaths(paths); got != test.want {
				t.Fatalf("hook window after the store = %s, want %s", got, test.want)
			}

			fixture := newQuestionFixture(t, true)
			hook := fixture.hook(0)
			hook.window = func() time.Duration { return claudeQuestionWindowFromPaths(paths) }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan string, 1)
			go func() {
				var stdout bytes.Buffer
				hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
				done <- stdout.String()
			}()
			var id string
			for deadline := time.Now().Add(5 * time.Second); id == ""; time.Sleep(5 * time.Millisecond) {
				if records, err := fixture.store.List(questionTestAgent); err == nil && len(records) == 1 {
					id = records[0].ID
				}
				if time.Now().After(deadline) {
					t.Fatal("the hook never recorded its question")
				}
			}
			cancel()
			waitHookOutput(t, done)
			record, _, _ := fixture.store.Get(id)
			if got := record.Deadline.Sub(record.CreatedAt); got != test.want {
				t.Fatalf("record window = %s, want %s", got, test.want)
			}
		})
	}
}

// TestConfigLocaleSetPreservesOtherKeys stores `[ui] locale` into a
// config.toml that already holds other keys and proves they survive.
func TestConfigLocaleSetPreservesOtherKeys(t *testing.T) {
	t.Parallel()

	cmd := centralSettingsTestCommand(t)
	configToml, _ := centralSettingsFiles(t, cmd)
	if err := os.MkdirAll(filepath.Dir(configToml), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configToml, []byte("[env]\nKEEP_ME = \"yes\"\n\n[ui]\nnative_keys = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, want := runConfigRoute(t, cmd, "locale"), "locale auto from "+configToml+"\n"; got != want {
		t.Fatalf("locale = %q, want %q", got, want)
	}
	if got, want := runConfigRoute(t, cmd, "locale", "--set", "ko-KR"), "locale ko-KR\n"; got != want {
		t.Fatalf("locale --set = %q, want %q", got, want)
	}
	if got, want := runConfigRoute(t, cmd, "locale"), "locale ko-KR from "+configToml+"\n"; got != want {
		t.Fatalf("locale after set = %q, want %q", got, want)
	}

	cfg, err := hooks.LoadGlobalConfig(configToml)
	if err != nil {
		t.Fatalf("LoadGlobalConfig error = %v", err)
	}
	if cfg.UI.Locale != "ko-KR" {
		t.Fatalf("[ui] locale = %q, want ko-KR", cfg.UI.Locale)
	}
	if cfg.Env["KEEP_ME"] != "yes" {
		t.Fatalf("[env] KEEP_ME = %q, want the seeded value kept", cfg.Env["KEEP_ME"])
	}
	if cfg.UI.NativeKeys == nil || *cfg.UI.NativeKeys {
		t.Fatalf("[ui] native_keys = %v, want the seeded false kept", cfg.UI.NativeKeys)
	}
}

// TestConfigCentralSettingsRejectInvalidWithoutWriting pins a non-zero exit for
// every invalid value, and that none of them changes a central settings file:
// absent files stay absent, and present files stay byte-for-byte the same. A
// bad --window next to a good --answering writes neither.
func TestConfigCentralSettingsRejectInvalidWithoutWriting(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		args  []string
		usage bool
		want  string
	}{
		{name: "unsupported locale", args: []string{"locale", "--set", "fr-FR"}, want: "unsupported locale setting: fr-FR"},
		{name: "empty locale", args: []string{"locale", "--set="}, usage: true, want: "config locale --set requires a locale setting"},
		{name: "locale positional", args: []string{"locale", "ko-KR"}, usage: true, want: "config locale does not accept positional arguments: ko-KR"},
		{name: "unknown answering way", args: []string{"agent-questions", "--answering", "codex"}, usage: true, want: `config agent-questions --answering: unknown way "codex"; known ways: claude, projmux`},
		{name: "empty answering way", args: []string{"agent-questions", "--answering="}, usage: true, want: "config agent-questions --answering requires a way: claude, projmux"},
		{name: "window below range", args: []string{"agent-questions", "--window", "59"}, usage: true, want: `config agent-questions --window: invalid window "59"; want 60..3600 seconds or unlimited`},
		{name: "window above range", args: []string{"agent-questions", "--window", "3601"}, usage: true, want: `invalid window "3601"`},
		{name: "window unlimited seconds", args: []string{"agent-questions", "--window", "604785"}, usage: true, want: `invalid window "604785"`},
		{name: "window not numeric", args: []string{"agent-questions", "--window", "10m"}, usage: true, want: `invalid window "10m"`},
		{name: "bad window with good answering", args: []string{"agent-questions", "--answering", "projmux", "--window", "0"}, usage: true, want: `invalid window "0"`},
		{name: "good window with bad answering", args: []string{"agent-questions", "--window", "unlimited", "--answering", "native"}, usage: true, want: `unknown way "native"`},
		{name: "agent-questions positional", args: []string{"agent-questions", "projmux"}, usage: true, want: "config agent-questions does not accept positional arguments: projmux"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, seeded := range []bool{false, true} {
				cmd := centralSettingsTestCommand(t)
				configToml, paths := centralSettingsFiles(t, cmd)
				files := []string{configToml, paths.AgentQuestionAnsweringFile(), paths.AgentQuestionWindowSecondsFile()}
				if seeded {
					seedCentralSettingsFile(t, configToml, "[ui]\nlocale = \"en-US\"\n")
					seedCentralSettingsFile(t, paths.AgentQuestionAnsweringFile(), "claude\n")
					seedCentralSettingsFile(t, paths.AgentQuestionWindowSecondsFile(), "300\n")
				}
				before := snapshotCentralSettingsFiles(t, files)

				stdout, stderr, err := runRoute(t, cmd, test.args...)
				if err == nil {
					t.Fatalf("config %v succeeded, want an error", test.args)
				}
				if IsUsageError(err) != test.usage {
					t.Fatalf("config %v usage error = %v, want %v (err %v)", test.args, IsUsageError(err), test.usage, err)
				}
				if !strings.Contains(err.Error(), test.want) {
					t.Fatalf("config %v error = %q, want it to contain %q", test.args, err, test.want)
				}
				if stdout != "" || stderr != "" {
					t.Fatalf("config %v printed stdout=%q stderr=%q", test.args, stdout, stderr)
				}
				after := snapshotCentralSettingsFiles(t, files)
				for _, path := range files {
					if before[path] != after[path] {
						t.Fatalf("config %v (seeded=%v) changed %s: %q -> %q", test.args, seeded, path, before[path], after[path])
					}
				}
			}
		})
	}
}

func seedCentralSettingsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshotCentralSettingsFiles records each file's bytes, with a sentinel for
// an absent file.
func snapshotCentralSettingsFiles(t *testing.T, files []string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, path := range files {
		content, exists := readPolicyFile(t, path)
		if !exists {
			content = "<absent>"
		}
		snapshot[path] = content
	}
	return snapshot
}
