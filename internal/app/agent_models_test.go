package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// runAgentModels runs `agent models` on a command whose only dependency fails
// the test, so a pass proves the read is static.
func runAgentModels(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) {
		t.Fatal("agent models must not read the Registry")
		return coremetadata.Registry{}, nil
	}}
	var stdout, stderr bytes.Buffer
	err := cmd.Run(append([]string{"models"}, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestAgentModelsPrintsTheCoreClaudeListOnePerLine(t *testing.T) {
	t.Parallel()
	want := strings.Join(profile.ClaudeModels(), "\n") + "\n"
	for name, args := range map[string][]string{
		"default provider":  nil,
		"explicit provider": {"--provider", "claude"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runAgentModels(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != want || stderr != "" {
				t.Fatalf("stdout = %q stderr = %q, want %q", stdout, stderr, want)
			}
		})
	}
}

func TestAgentModelsJSONNamesProviderOrderAndUnlistedAcceptance(t *testing.T) {
	t.Parallel()
	stdout, stderr, err := runAgentModels(t, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	want := "{\n  \"provider\": \"claude\",\n  \"models\": [\n    \"fable\",\n    \"opus\",\n    \"sonnet\",\n    \"haiku\"\n  ],\n  \"acceptsUnlisted\": true\n}\n"
	if stdout != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	var projection agentModelProjection
	if err := json.Unmarshal([]byte(stdout), &projection); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(projection.Models, profile.ClaudeModels()) {
		t.Fatalf("models = %v, want the core list %v", projection.Models, profile.ClaudeModels())
	}
}

func TestAgentModelsRefusesWhatItCannotAnswerAsUsageErrors(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"antigravity":      {[]string{"--provider", "antigravity"}, "model listing is unavailable for provider antigravity"},
		"unknown provider": {[]string{"--provider", "gpt"}, `unsupported provider "gpt"`},
		"bad flag":         {[]string{"--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		"missing -o value": {[]string{"-o"}, "flag needs an argument: -o"},
		"bad output mode":  {[]string{"-o", "yaml"}, `unsupported output mode "yaml"`},
		"positional":       {[]string{"opus"}, `accepts no positional arguments; got "opus"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := runAgentModels(t, test.args...)
			if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 {
				t.Fatalf("err = %v (exit %d), want a usage error", err, exitCodeOf(err))
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %q, want it to mention %q", err, test.want)
			}
			if stdout != "" {
				t.Fatalf("a refused read printed %q; a refusal is not an empty list", stdout)
			}
		})
	}
}

func TestAgentModelsCodexUsesAppServerForTextAndJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, output string
		args         []string
	}{
		{"text", "gpt-6\ngpt-6-mini\n", []string{"--provider", "codex"}},
		{"json", "{\n  \"provider\": \"codex\",\n  \"models\": [\n    \"gpt-6\",\n    \"gpt-6-mini\"\n  ],\n  \"acceptsUnlisted\": true\n}\n", []string{"--provider", "codex", "-o", "json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := 0
			cmd := &agentCommand{listCodexModels: func(context.Context) ([]string, error) {
				called++
				return []string{"gpt-6", "gpt-6-mini"}, nil
			}}
			var stdout, stderr bytes.Buffer
			if err := cmd.Run(append([]string{"models"}, test.args...), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if called != 1 || stdout.String() != test.output || stderr.Len() != 0 {
				t.Fatalf("calls = %d stdout = %q stderr = %q", called, stdout.String(), stderr.String())
			}
		})
	}
}

func TestAgentModelsCodexUnavailableIsRuntimeFailureWithoutEmptyList(t *testing.T) {
	t.Parallel()
	cmd := &agentCommand{listCodexModels: func(context.Context) ([]string, error) {
		return nil, errors.New("endpoint-not-ready")
	}}
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"models", "--provider", "codex", "-o", "json"}, &stdout, &stderr)
	if err == nil || IsUsageError(err) || exitCodeOf(err) == 0 ||
		!strings.Contains(err.Error(), "Codex app-server model/list failed: endpoint-not-ready") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}

// TestAgentModelsThroughAppWritesNothing runs the full App wiring against an
// isolated home: the read creates no config, state, cache, or Registry file.
func TestAgentModelsThroughAppWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("PROJMUX_CWD", filepath.Join(home, "project"))
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	app := New()
	for _, argv := range [][]string{{"agent", "models"}, {"agent", "models", "--provider", "claude", "-o", "json"}} {
		var stdout, stderr bytes.Buffer
		if err := app.Run(argv, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%q) error = %v", argv, err)
		}
		if !strings.Contains(stdout.String(), "opus") {
			t.Fatalf("Run(%q) stdout = %q", argv, stdout.String())
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("agent models wrote into the isolated home: %v", names)
	}
}

func TestAgentModelsSkipsAutomaticHookMigration(t *testing.T) {
	t.Parallel()
	if shouldRunLegacyHookMigrations([]string{"agent", "models"}) {
		t.Fatal("shouldRunLegacyHookMigrations(agent models) = true")
	}
}
