package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAIIntegrateCodexInstallsManagedNotify(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	got := readCodexTestFile(t, path)
	if !strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, codexNotifyLine) || !strings.Contains(got, codexNotifyMarkerEnd) {
		t.Fatalf("config missing managed block:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "configured Codex legacy notify") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateCodexInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate codex error = %v", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate codex error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second install changed config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateCodexDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, codexNotifyLine) {
		t.Fatalf("stdout = %q, want dry-run managed block preview", out)
	}
}

func TestAIIntegrateCodexRefusesUnmanagedNotify(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `model = "gpt-5.1-codex"
notify = ["custom", "notify"]
`)

	err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate codex expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already configured outside a projmux-managed block") || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("error = %v, want conflict with dry-run guidance", err)
	}
	if got := readCodexTestFile(t, path); !strings.Contains(got, `notify = ["custom", "notify"]`) || strings.Contains(got, codexNotifyMarkerBegin) {
		t.Fatalf("config was modified unexpectedly:\n%s", got)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --dry-run with conflict error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would refuse") || !strings.Contains(stdout.String(), `notify = ["custom", "notify"]`) {
		t.Fatalf("stdout = %q, want conflict preview", stdout.String())
	}
}

func TestAIIntegrateCodexRemoveOnlyManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexNotifyBlock()+`model = "gpt-5.1-codex"
# keep this user setting
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) || !strings.Contains(got, "# keep this user setting") {
		t.Fatalf("config after remove =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks --dry-run error = %v", err)
	}

	path := filepath.Join(home, codexConfigRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, codexHooksMarkerBegin) || !strings.Contains(out, codexHookCommand) || !strings.Contains(out, "hooks = true") {
		t.Fatalf("stdout = %q, want hooks dry-run preview", out)
	}
}

func TestAIIntegrateCodexHooksInstallsManagedBlockAndPreservesConfig(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `model = "gpt-5.1-codex"
[features]
experimental_resume = true

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "echo keep"

# keep this user setting
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if !strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, "[features]") || !strings.Contains(got, "hooks = true") {
		t.Fatalf("config missing hooks feature block:\n%s", got)
	}
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
	if strings.Count(got, "[features]") != 1 {
		t.Fatalf("config duplicated [features]:\n%s", got)
	}
	codexHookEvents := defaultAIHookInstallEvents(aiHookProviderCodex)
	if len(codexHookEvents) != 8 {
		t.Fatalf("default Codex hook catalog has %d events, want 8", len(codexHookEvents))
	}
	for _, event := range codexHookEvents {
		assertCodexHookNestedHandler(t, got, event)
	}
	if strings.Count(got, codexHookCommand) != len(codexHookEvents) {
		t.Fatalf("config command count = %d, want %d:\n%s", strings.Count(got, codexHookCommand), len(codexHookEvents), got)
	}
	if !strings.Contains(got, `model = "gpt-5.1-codex"`) ||
		!strings.Contains(got, "experimental_resume = true") ||
		!strings.Contains(got, "echo keep") ||
		!strings.Contains(got, "# keep this user setting") {
		t.Fatalf("config did not preserve unmanaged content:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "configured Codex hooks") {
		t.Fatalf("stdout = %q, want configured hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksMigratesDeprecatedManagedFeature(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `[features]
experimental_resume = true
# projmux-managed:codex-hooks-feature:v1
codex_hooks = true
`)

	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
	if strings.Count(got, codexHooksFeatureMarker) != 1 || !strings.Contains(got, "hooks = true") {
		t.Fatalf("config did not migrate managed feature cleanly:\n%s", got)
	}
	if !strings.Contains(got, "experimental_resume = true") {
		t.Fatalf("config did not preserve existing feature:\n%s", got)
	}
}

func TestAIIntegrateCodexHooksReplacesOldManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, oldCodexHooksBlock()+`model = "gpt-5.1-codex"
`)

	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, `command = "projmux ai ingest codex-hook`) && !strings.Contains(got, "[[hooks.PermissionRequest.hooks]]") {
		t.Fatalf("config still appears to use flat hook command schema:\n%s", got)
	}
	codexHookEvents := defaultAIHookInstallEvents(aiHookProviderCodex)
	for _, event := range codexHookEvents {
		assertCodexHookNestedHandler(t, got, event)
	}
	if strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config kept deprecated codex_hooks feature:\n%s", got)
	}
}

func TestAIIntegrateCodexHooksInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "codex", "--mode=hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate codex --mode hooks error = %v", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--mode=hooks"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate codex --mode hooks error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second hooks install changed config:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateCodexHooksRemoveOnlySelectedManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexHooksBlock(true)+codexNotifyBlock()+`model = "gpt-5.1-codex"
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || !strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) {
		t.Fatalf("config after selected hooks remove =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want removed hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexRemoveWithoutModeRemovesManagedNotifyAndHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, codexHooksBlock(true)+codexNotifyBlock()+`model = "gpt-5.1-codex"
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || strings.Contains(got, codexNotifyMarkerBegin) || !strings.Contains(got, `model = "gpt-5.1-codex"`) {
		t.Fatalf("config after remove-all =\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex legacy notify and hooks") {
		t.Fatalf("stdout = %q, want removed both message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksRemoveCleansManagedFeatureEntry(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)
	writeCodexTestFile(t, path, `[features]
experimental_resume = true
`+"\n"+codexHooksBlock(false))

	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks --remove error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, codexHooksMarkerBegin) || strings.Contains(got, codexHooksFeatureMarker) || strings.Contains(got, "hooks = true") || strings.Contains(got, "codex_hooks = true") {
		t.Fatalf("config after hooks remove kept managed entries:\n%s", got)
	}
	if !strings.Contains(got, "experimental_resume = true") {
		t.Fatalf("config after hooks remove did not preserve feature table:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want removed hooks message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksDoesNotTouchClaudeSettings(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	claudePath := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, claudePath, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo claude"}]}]}}`+"\n")
	before := readCodexTestFile(t, claudePath)

	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}
	if got := readCodexTestFile(t, claudePath); got != before {
		t.Fatalf("Claude settings changed:\nbefore:%s\nafter:%s", before, got)
	}
}

func TestAIIntegrateClaudeInstallsManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit",
        "hooks": [
          {
            "type": "command",
            "command": "echo keep"
          }
        ]
      }
    ]
  }
}
`)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude error = %v", err)
	}

	settings := readClaudeSettingsTestFile(t, path)
	if got := settings["theme"]; got != "dark" {
		t.Fatalf("theme = %#v, want preserved dark", got)
	}
	claudeHookEvents := defaultAIHookInstallEvents(aiHookProviderClaude)
	if len(claudeHookEvents) != 29 {
		t.Fatalf("default Claude hook catalog has %d events, want 29", len(claudeHookEvents))
	}
	for _, event := range claudeHookEvents {
		if !claudeSettingsHasManagedCommand(t, settings, event) {
			t.Fatalf("settings missing managed command for %s:\n%s", event, readCodexTestFile(t, path))
		}
	}
	if !strings.Contains(readCodexTestFile(t, path), `"PostToolUse"`) || !strings.Contains(readCodexTestFile(t, path), "echo keep") {
		t.Fatalf("settings did not preserve user hook:\n%s", readCodexTestFile(t, path))
	}
	if !strings.Contains(stdout.String(), "configured Claude Code hooks") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateClaudeInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run integrate claude error = %v", err)
	}
	path := filepath.Join(home, claudeSettingsRelativePath)
	first := readCodexTestFile(t, path)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run integrate claude error = %v", err)
	}
	second := readCodexTestFile(t, path)
	if second != first {
		t.Fatalf("second install changed settings:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("stdout = %q, want no changes", stdout.String())
	}
}

func TestAIIntegrateClaudeDryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --dry-run error = %v", err)
	}

	path := filepath.Join(home, claudeSettingsRelativePath)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("settings stat err = %v, want missing file", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, claudeHookCommand) {
		t.Fatalf("stdout = %q, want dry-run managed hook preview", out)
	}
}

func TestAIIntegrateClaudeRefusesUnmanagedProjmuxHook(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true"
          }
        ]
      }
    ]
  }
}
`)

	err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate claude expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "unmanaged projmux ingest command") || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("error = %v, want conflict with dry-run guidance", err)
	}
	if got := readCodexTestFile(t, path); strings.Contains(got, claudeHookManagedMarker) {
		t.Fatalf("settings was modified unexpectedly:\n%s", got)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --dry-run with conflict error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would refuse") || !strings.Contains(stdout.String(), "unmanaged projmux ingest command") {
		t.Fatalf("stdout = %q, want conflict preview", stdout.String())
	}
}

func TestAIIntegrateClaudeRemoveOnlyManagedHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude setup error = %v", err)
	}
	settings := readClaudeSettingsTestFile(t, path)
	hooks := settings["hooks"].(map[string]any)
	hooks["Notification"] = append(hooks["Notification"].([]any), map[string]any{
		"matcher": "idle_prompt",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": "echo user-notify",
			},
		},
	})
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, path, string(data)+"\n")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --remove error = %v", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, claudeHookManagedMarker) || !strings.Contains(got, "echo user-notify") {
		t.Fatalf("settings after remove =\n%s", got)
	}
	claudeHookEvents := defaultAIHookInstallEvents(aiHookProviderClaude)
	for _, event := range claudeHookEvents {
		if event == "Notification" {
			continue
		}
		if strings.Contains(got, `"`+event+`"`) {
			t.Fatalf("settings retained empty managed-only event %s:\n%s", event, got)
		}
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func TestAIIntegrateCodexHooksUsesCatalogOverride(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".xdg-config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "codex.json"), `{
  "provider": "codex",
  "observed_version": "codex-test",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
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

	if err := cmd.Run([]string{"integrate", "codex", "--mode", "hooks"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex --mode hooks error = %v", err)
	}

	got := readCodexTestFile(t, filepath.Join(home, codexConfigRelativePath))
	if strings.Contains(got, "[[hooks.Stop]]") {
		t.Fatalf("config installed catalog-disabled Stop event:\n%s", got)
	}
	assertCodexHookNestedHandler(t, got, "ExperimentalEvent")
}

func TestAIIntegrateClaudeUsesCatalogOverride(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".xdg-config")
	writeCodexTestFile(t, filepath.Join(configHome, "projmux", "ai-hooks.d", "claude.json"), `{
  "provider": "claude",
  "events": [
    { "name": "Stop", "install": false, "action": "notify" },
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

	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude error = %v", err)
	}

	settings := readClaudeSettingsTestFile(t, filepath.Join(home, claudeSettingsRelativePath))
	if claudeSettingsHasManagedCommand(t, settings, "Stop") {
		t.Fatalf("settings installed catalog-disabled Stop event:\n%s", readCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath)))
	}
	if !claudeSettingsHasManagedCommand(t, settings, "ExperimentalEvent") {
		t.Fatalf("settings missing override event:\n%s", readCodexTestFile(t, filepath.Join(home, claudeSettingsRelativePath)))
	}
}

func TestAIIntegrateClaudeRemoveScansManagedMarkersOutsideCatalog(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "OldRemovedEvent": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1"
          }
        ]
      }
    ],
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo keep"
          }
        ]
      }
    ]
  }
}
`)

	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate claude --remove error = %v", err)
	}

	got := readCodexTestFile(t, path)
	if strings.Contains(got, "OldRemovedEvent") || strings.Contains(got, claudeHookManagedMarker) {
		t.Fatalf("settings retained stale managed marker outside catalog:\n%s", got)
	}
	if !strings.Contains(got, "echo keep") {
		t.Fatalf("settings removed user hook:\n%s", got)
	}
}

func TestAIIntegrateClaudeConflictScansUnmanagedCommandsOutsideCatalog(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{
  "hooks": {
    "OldRemovedEvent": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "projmux ai ingest claude-hook >/dev/null 2>&1 || true"
          }
        ]
      }
    ]
  }
}
`)

	err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("Run integrate claude expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "OldRemovedEvent") || !strings.Contains(err.Error(), "unmanaged projmux ingest command") {
		t.Fatalf("error = %v, want stale event unmanaged command conflict", err)
	}
	got := readCodexTestFile(t, path)
	if strings.Contains(got, claudeHookManagedMarker) {
		t.Fatalf("settings was modified unexpectedly:\n%s", got)
	}
}

func TestAIIntegrateTmuxBellDryRunPlansInstallCommands(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-hooks", "-g", tmuxBellHookName}) {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell", "--dry-run"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell --dry-run error = %v", err)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want no writes on dry-run", cmdRecorder(cmd).commands)
	}
	out := stdout.String()
	for _, want := range []string{
		"set-option -g allow-passthrough on",
		"set-option -g monitor-bell on",
		"set-option -g bell-action other",
		"set-hook -ag alert-bell",
		"#{hook_pane}",
		tmuxBellManagedMarker,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestAIIntegrateTmuxBellInstallAppendsManagedHookAndPreservesExisting(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-hooks", "-g", tmuxBellHookName}) {
			return []byte("alert-bell[0] run-shell -b 'echo user-hook'\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell error = %v", err)
	}

	wantCommands := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-g", "allow-passthrough", "on"}},
		{name: "tmux", args: []string{"set-option", "-g", "monitor-bell", "on"}},
		{name: "tmux", args: []string{"set-option", "-g", "bell-action", "other"}},
		{name: "tmux", args: []string{"set-hook", "-ag", tmuxBellHookName, tmuxBellHookCommand}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, wantCommands)
	}
	if !strings.Contains(stdout.String(), "configured tmux bell fallback") {
		t.Fatalf("stdout = %q, want configured message", stdout.String())
	}
}

func TestAIIntegrateTmuxBellInstallSkipsDuplicateManagedHook(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-hooks", "-g", tmuxBellHookName}) {
			return []byte("alert-bell[1] " + tmuxBellHookCommand + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"integrate", "tmux-bell"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell error = %v", err)
	}
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "tmux" && len(command.args) >= 2 && command.args[0] == "set-hook" && command.args[1] == "-ag" {
			t.Fatalf("commands = %#v, did not want duplicate managed hook append", cmdRecorder(cmd).commands)
		}
	}
}

func TestAIIntegrateTmuxBellRemoveOnlyManagedHook(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"show-hooks", "-g", tmuxBellHookName}) {
			return []byte(strings.Join([]string{
				"alert-bell[0] run-shell -b 'echo user-hook'",
				"alert-bell[2] " + tmuxBellHookCommand,
			}, "\n") + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "tmux-bell", "--remove"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate tmux-bell --remove error = %v", err)
	}
	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-hook", "-gu", "alert-bell[2]"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed tmux bell hook") {
		t.Fatalf("stdout = %q, want removed message", stdout.String())
	}
}

func readCodexTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeCodexTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readClaudeSettingsTestFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal([]byte(readCodexTestFile(t, path)), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func claudeSettingsHasManagedCommand(t *testing.T, settings map[string]any, event string) bool {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	entries, ok := hooks[event].([]any)
	if !ok {
		return false
	}
	for _, entryValue := range entries {
		entry, ok := entryValue.(map[string]any)
		if !ok {
			continue
		}
		hookValues, _ := entry["hooks"].([]any)
		for _, hookValue := range hookValues {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				continue
			}
			if hook["type"] == "command" && hook["command"] == claudeHookCommand {
				return true
			}
		}
	}
	return false
}

func assertCodexHookNestedHandler(t *testing.T, config, event string) {
	t.Helper()
	for _, want := range []string{
		"[[hooks." + event + "]]",
		`matcher = "*"`,
		"[[hooks." + event + ".hooks]]",
		`type = "command"`,
		`command = "` + codexHookCommand + `"`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("config missing %q for %s:\n%s", want, event, config)
		}
	}
}

func oldCodexHooksBlock() string {
	lines := []string{
		codexHooksMarkerBegin,
		"[features]",
		"codex_hooks = true",
		"",
	}
	for _, event := range defaultAIHookInstallEvents(aiHookProviderCodex) {
		lines = append(lines,
			"[[hooks."+event+"]]",
			`command = "`+codexHookCommand+`"`,
			"",
		)
	}
	lines = append(lines, codexHooksMarkerEnd)
	return strings.Join(lines, "\n") + "\n"
}
