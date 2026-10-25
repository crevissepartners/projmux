package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// claudeHooksFixtures are the settings files the SDK must plan and apply as
// `projmux agent integrate claude` does; "" with exists false is no file.
var claudeHooksFixtures = []struct {
	name, content string
	exists        bool
}{
	{"none", "", false},
	{"empty", "", true},
	{"existing-hooks", `{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      { "matcher": "Edit", "hooks": [ { "type": "command", "command": "echo keep" } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1" } ] }
    ]
  }
}
`, true},
	{"conflict", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"projmux ai ingest claude-hook >/dev/null 2>&1 || true"}]}]}}` + "\n", true},
}

// claudeHooksHome seeds a home with fixture's settings and a Claude hook
// catalog override, and returns the CLI command and SDK client that read it.
func claudeHooksHome(t *testing.T, content string, exists, installed bool) (*aiCommand, *projmux.Client, string) {
	t.Helper()
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "claude.json"), `{
  "provider": "claude",
  "events": [
    { "name": "Notification", "install": false, "action": "notify" },
    { "name": "ExperimentalEvent", "install": true, "action": "quiet" }
  ]
}
`)
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return configHome
		default:
			return ""
		}
	}
	cmd.readFile = os.ReadFile
	// The command's own writer, not the test seam.
	cmd.writeFile = nil
	path := filepath.Join(home, claudeSettingsRelativePath)
	if exists {
		writeCodexTestFile(t, path, content)
	}
	if installed {
		if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	client, err := projmux.NewClient(projmux.Options{HomeDir: home, ConfigDir: filepath.Join(configHome, "projmux"), StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	return cmd, client, home
}

// TestPreviewClaudeHooksMatchesCLIPlan is the comparison the preview
// promises: on the same home and catalog override, its plan is the plan
// `projmux agent integrate claude` (and --remove) makes, field for field.
func TestPreviewClaudeHooksMatchesCLIPlan(t *testing.T) {
	for _, fixture := range claudeHooksFixtures {
		for _, installed := range []bool{false, true} {
			if installed && fixture.name == "conflict" {
				continue
			}
			for _, remove := range []bool{false, true} {
				name := fixture.name
				if installed {
					name += "-installed"
				}
				if remove {
					name += "-remove"
				}
				t.Run(name, func(t *testing.T) {
					cmd, client, _ := claudeHooksHome(t, fixture.content, fixture.exists, installed)
					want, err := cmd.planClaudeHookIntegration(remove)
					if err != nil {
						t.Fatal(err)
					}
					got, err := client.PreviewClaudeHooks(context.Background(), core.ClaudeHooksRequest{Remove: remove})
					if err != nil {
						t.Fatal(err)
					}
					if got.Path != want.path || got.Exists != want.exists || got.Current != want.current || got.Next != want.next ||
						got.Changed != want.changed || got.Conflict != want.conflict || got.Action != want.action {
						t.Fatalf("SDK %+v\nCLI %+v", got, want)
					}
					// The fixtures must exercise the conflict, the override and a removal.
					switch {
					case fixture.name == "conflict":
						if got.Conflict == "" {
							t.Fatalf("no conflict: %+v", got)
						}
					case !remove && !installed:
						if !got.Changed || !strings.Contains(got.Next, `"ExperimentalEvent"`) || strings.Contains(got.Next, `"Notification"`) {
							t.Fatalf("override not planned: %+v", got)
						}
					case remove && installed:
						if !got.Changed || strings.Contains(got.Next, claudeHookManagedMarker) {
							t.Fatalf("removal not planned: %+v", got)
						}
					}
				})
			}
		}
	}
}

// TestApplyClaudeHooksMatchesCLIInstall applies each previewed plan through
// the SDK in one home and runs `projmux agent integrate claude` (and
// --remove) in a twin: they leave the same bytes and mode, report the same
// action, and refuse the same conflict.
func TestApplyClaudeHooksMatchesCLIInstall(t *testing.T) {
	for _, fixture := range claudeHooksFixtures {
		for _, remove := range []bool{false, true} {
			name := fixture.name
			if remove {
				name += "-remove"
			}
			t.Run(name, func(t *testing.T) {
				installed := remove && fixture.name != "conflict"
				cmd, _, cliHome := claudeHooksHome(t, fixture.content, fixture.exists, installed)
				_, client, sdkHome := claudeHooksHome(t, fixture.content, fixture.exists, installed)
				args := []string{"integrate", "claude"}
				if remove {
					args = append(args, "--remove")
				}
				var stdout bytes.Buffer
				cliErr := cmd.Run(args, &stdout, &bytes.Buffer{})

				ctx := context.Background()
				plan, err := client.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{Remove: remove})
				if err != nil {
					t.Fatal(err)
				}
				result, sdkErr := client.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: remove, Base: plan.Base})
				if plan.Conflict != "" {
					var refusal *projmux.Refusal
					if cliErr == nil || !strings.Contains(cliErr.Error(), plan.Conflict) || !errors.As(sdkErr, &refusal) || refusal.Code != projmux.CodeClaudeHooksConflict {
						t.Fatalf("conflict: CLI %v, SDK %v", cliErr, sdkErr)
					}
				} else {
					if cliErr != nil || sdkErr != nil {
						t.Fatalf("CLI %v, SDK %v", cliErr, sdkErr)
					}
					if got, want := strings.ReplaceAll(result.Action, sdkHome, "HOME"), strings.ReplaceAll(strings.TrimSuffix(stdout.String(), "\n"), cliHome, "HOME"); got != want {
						t.Fatalf("action SDK %q, CLI %q", got, want)
					}
					if result.Changed != plan.Changed || result.Path != plan.Path {
						t.Fatalf("result %+v, plan %+v", result, plan)
					}
				}
				cliPath, sdkPath := filepath.Join(cliHome, claudeSettingsRelativePath), filepath.Join(sdkHome, claudeSettingsRelativePath)
				cliInfo, cliStatErr := os.Stat(cliPath)
				sdkInfo, sdkStatErr := os.Stat(sdkPath)
				if (cliStatErr == nil) != (sdkStatErr == nil) {
					t.Fatalf("file presence CLI %v, SDK %v", cliStatErr, sdkStatErr)
				}
				if cliStatErr != nil {
					return
				}
				cliBytes, _ := os.ReadFile(cliPath)
				sdkBytes, _ := os.ReadFile(sdkPath)
				if !bytes.Equal(cliBytes, sdkBytes) || cliInfo.Mode() != sdkInfo.Mode() {
					t.Fatalf("CLI %v %q\nSDK %v %q", cliInfo.Mode(), cliBytes, sdkInfo.Mode(), sdkBytes)
				}
			})
		}
	}
}

// The command's settings names the private web server also reads stay
// callable with their signatures and are the shared claudesettings code.
func TestClaudeSettingsNamesDelegate(t *testing.T) {
	settings, err := parseClaudeSettings(`{"hooks":{"Stop":[]}}`, "settings.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := claudeSettingsHooks(settings, false)
	if err != nil || hooks["Stop"] == nil {
		t.Fatalf("%v %v", hooks, err)
	}
	if _, err := parseClaudeSettings("[]", "settings.json"); err == nil {
		t.Fatal("non-object settings parsed")
	}
	hooks["PreToolUse"] = []any{claudeQuestionManagedEntry()}
	hooks["PermissionRequest"] = []any{claudePermissionManagedEntry()}
	encoded, err := encodeClaudeSettings(settings)
	if err != nil || !strings.Contains(encoded, claudeQuestionManagedMarker) || !strings.Contains(encoded, claudePermissionManagedMarker) {
		t.Fatalf("%s %v", encoded, err)
	}
}
