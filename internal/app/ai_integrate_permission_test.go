package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// claudePermissionRequestHooks returns every hook command object installed on
// PermissionRequest, with the entry that carries it.
func claudePermissionRequestHooks(t *testing.T, settings map[string]any) (entries []map[string]any, hooks []map[string]any) {
	t.Helper()
	all, _ := settings["hooks"].(map[string]any)
	values, _ := all["PermissionRequest"].([]any)
	for _, value := range values {
		entry := value.(map[string]any)
		for _, hookValue := range entry["hooks"].([]any) {
			entries = append(entries, entry)
			hooks = append(hooks, hookValue.(map[string]any))
		}
	}
	return entries, hooks
}

// TestAIIntegrateClaudeInstallsASeparatePermissionEntry holds the integrate
// contract of the permission hook: its own PermissionRequest entry with the
// fixed timeout and its marker, stdout not discarded, the existing ingest entry
// on the same event byte-for-byte as before, idempotent re-runs, and a
// --remove that restores the original file.
func TestAIIntegrateClaudeInstallsASeparatePermissionEntry(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	original, err := encodeClaudeSettings(map[string]any{
		"hooks": map[string]any{
			"PermissionRequest": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo keep-permission"}}}},
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
	entries, hooks := claudePermissionRequestHooks(t, readClaudeSettingsTestFile(t, path))
	var ingest, permission []int
	for i, hook := range hooks {
		command, _ := hook["command"].(string)
		switch {
		case strings.Contains(command, claudePermissionManagedMarker):
			permission = append(permission, i)
		case command == claudeHookCommand:
			ingest = append(ingest, i)
		}
	}
	if len(permission) != 1 || len(ingest) != 1 {
		t.Fatalf("permission hooks = %d, ingest hooks = %d, want one each:\n%s", len(permission), len(ingest), installed)
	}
	entry, hook := entries[permission[0]], hooks[permission[0]]
	if len(entry) != 1 || len(entry["hooks"].([]any)) != 1 || hook["type"] != "command" ||
		hook["timeout"] != float64(config.AgentApprovalHookTimeoutSeconds) ||
		hook["command"] != "exec projmux internal claude-permission-hook --pane=${PMX_INTERNAL_ACTIVATION_PANE_UID:-} 2>/dev/null # projmux-managed:claude-permission:v1" {
		t.Fatalf("permission entry = %#v", entry)
	}
	if strings.Contains(hook["command"].(string), ">/dev/null 2>&1") || strings.Contains(hook["command"].(string), "1>") {
		t.Fatalf("permission command discards stdout: %q", hook["command"])
	}
	ingestEntry := entries[ingest[0]]
	if entries[ingest[0]]["matcher"] != nil || len(ingestEntry) != 1 || len(ingestEntry["hooks"].([]any)) != 1 ||
		hooks[ingest[0]]["command"] != canonicalClaudeHookRoute+aiHookPaneArgument+" >/dev/null 2>&1 || true # "+claudeHookManagedMarker {
		t.Fatalf("ingest entry changed: %#v", ingestEntry)
	}
	if !strings.Contains(installed, "echo keep-permission") {
		t.Fatalf("user hook lost:\n%s", installed)
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"integrate", "claude"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if readCodexTestFile(t, path) != installed || !strings.Contains(stdout.String(), "no changes") {
		t.Fatalf("second integrate changed settings; stdout=%q", stdout.String())
	}

	if err := cmd.Run([]string{"integrate", "claude", "--remove"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := readCodexTestFile(t, path); got != original {
		t.Fatalf("remove did not restore the original bytes:\n--- got ---\n%s--- want ---\n%s", got, original)
	}
}

// TestAIIntegrateClaudeRewritesAnOlderPermissionEntryByItsMarker holds that an
// entry carrying the marker, whatever its timeout, is replaced by the current
// one rather than duplicated.
func TestAIIntegrateClaudeRewritesAnOlderPermissionEntryByItsMarker(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"exec projmux internal claude-permission-hook 2>/dev/null # `+claudePermissionManagedMarker+`","timeout":60}]}]}}`+"\n")
	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	_, hooks := claudePermissionRequestHooks(t, readClaudeSettingsTestFile(t, path))
	found := 0
	for _, hook := range hooks {
		if command, _ := hook["command"].(string); strings.Contains(command, claudePermissionManagedMarker) {
			found++
			if hook["timeout"] != float64(config.AgentApprovalHookTimeoutSeconds) || command != claudePermissionHookCommand {
				t.Fatalf("rewritten hook = %#v", hook)
			}
		}
	}
	if found != 1 {
		t.Fatalf("permission hooks = %d, want 1", found)
	}
}

func TestAIIntegrateClaudeRefusesAnUnmanagedPermissionHook(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, claudeSettingsRelativePath)
	writeCodexTestFile(t, path, `{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"projmux internal claude-permission-hook"}]}]}}`+"\n")
	before := readCodexTestFile(t, path)
	if err := cmd.Run([]string{"integrate", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unmanaged projmux ingest command") {
		t.Fatalf("err = %v, want unmanaged conflict", err)
	}
	if readCodexTestFile(t, path) != before {
		t.Fatal("conflict changed settings")
	}
}

func TestClaudeDialogueProfileCarriesNoPermissionHook(t *testing.T) {
	t.Parallel()

	files, err := claudeDialogueProfile{Version: 1, AgentUID: "agt-1", PaneUID: "pan-1", Generation: "gen-1", Candidate: "/usr/local/bin/projmux"}.files()
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(files["settings.json"], &settings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["settings.json"]), claudePermissionHookRoute) {
		t.Fatalf("dialogue profile carries the permission hook: %s", files["settings.json"])
	}
}
