package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeQuestionEntries returns the PreToolUse matcher entries that carry the
// managed question command.
func claudeQuestionEntries(t *testing.T, settings map[string]any) []map[string]any {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	var out []map[string]any
	for _, value := range entries {
		entry := value.(map[string]any)
		for _, hookValue := range entry["hooks"].([]any) {
			if command, _ := hookValue.(map[string]any)["command"].(string); strings.Contains(command, claudeQuestionManagedMarker) {
				out = append(out, entry)
			}
		}
	}
	return out
}

func TestAIIntegrateClaudeInstallsOneQuestionEntryAndRemoveRestoresTheFileBytes(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	original, err := encodeClaudeSettings(map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo keep-bash"}}}},
			"Stop":       []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo keep-stop"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeCodexTestFile(t, path, original)

	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	installed := readCodexTestFile(t, path)
	entries := claudeQuestionEntries(t, readClaudeSettingsTestFile(t, path))
	if len(entries) != 1 {
		t.Fatalf("question entries = %d, want 1:\n%s", len(entries), installed)
	}
	entry := entries[0]
	hook := entry["hooks"].([]any)[0].(map[string]any)
	if entry["matcher"] != "AskUserQuestion" || len(entry["hooks"].([]any)) != 1 || hook["type"] != "command" ||
		hook["timeout"] != float64(315) || hook["statusMessage"] != claudeQuestionStatusMessage ||
		hook["command"] != "exec projmux internal claude-question-hook --pane=${PMX_INTERNAL_ACTIVATION_PANE_UID:-} 2>/dev/null # projmux-managed:claude-question:v1" {
		t.Fatalf("question entry = %#v", entry)
	}
	if strings.Contains(hook["command"].(string), " >/dev/null") || strings.Contains(hook["command"].(string), "1>") {
		t.Fatalf("question command discards stdout: %q", hook["command"])
	}
	if !strings.Contains(installed, "echo keep-bash") || !strings.Contains(installed, "echo keep-stop") {
		t.Fatalf("user hooks lost:\n%s", installed)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if readCodexTestFile(t, path) != installed || !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("second integrate changed settings; stdout=%q", stdout.String())
	}

	automatic, err := cmd.planClaudeHookMigration()
	if err != nil || automatic.changed || automatic.next != installed {
		t.Fatalf("automatic migration planned a change: changed=%t err=%v", automatic.changed, err)
	}

	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readCodexTestFile(t, path); got != original {
		t.Fatalf("remove did not restore the original bytes:\n--- got ---\n%s--- want ---\n%s", got, original)
	}
}

func TestAIIntegrateClaudeRefusesAnUnmanagedQuestionHook(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{"hooks":{"PreToolUse":[{"matcher":"AskUserQuestion","hooks":[{"type":"command","command":"projmux internal claude-question-hook"}]}]}}`+"\n")
	before := readCodexTestFile(t, path)
	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unmanaged projmux ingest command") {
		t.Fatalf("err = %v, want unmanaged conflict", err)
	}
	if readCodexTestFile(t, path) != before {
		t.Fatal("conflict changed settings")
	}
}

func TestClaudeDialogueProfileCarriesNoQuestionHook(t *testing.T) {
	t.Parallel()

	files, err := claudeDialogueProfile{Version: 1, AgentUID: "agt-1", PaneUID: "pan-1", Generation: "gen-1", Candidate: "/usr/local/bin/projmux"}.files()
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(files["settings.json"], &settings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["settings.json"]), claudeQuestionHookRoute) || strings.Contains(string(files["settings.json"]), "AskUserQuestion") {
		t.Fatalf("dialogue profile carries the question hook: %s", files["settings.json"])
	}
}
