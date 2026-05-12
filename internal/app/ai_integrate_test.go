package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	for _, event := range []string{"Stop", "UserPromptSubmit", "PermissionRequest", "StopFailure", "SubagentStop", "TeammateIdle"} {
		if strings.Contains(got, `"`+event+`"`) {
			t.Fatalf("settings retained empty managed-only event %s:\n%s", event, got)
		}
	}
	if !strings.Contains(stdout.String(), "removed projmux-managed") {
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
