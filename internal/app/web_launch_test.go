package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/web"
)

func TestWebLaunchOptionsFollowTheLauncherSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	mode := filepath.Join(home, ".config", "projmux", "tmux-ai-split-mode")
	if err := os.MkdirAll(filepath.Dir(mode), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mode, []byte("claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, _ := webFixtureBackend(t)
	code, body := webGet(t, web.New(backend, nil).Handler(), "/api/v1/web/launch")
	if code != 200 || body["defaultMode"] != "claude" {
		t.Fatalf("launch = %d %v", code, body)
	}
	providers, _ := body["providers"].([]any)
	if len(providers) == 0 {
		t.Fatalf("no providers in %v", body)
	}
	for _, raw := range providers {
		row := raw.(map[string]any)
		switch row["id"] {
		case "claude", "codex", "antigravity":
		default:
			t.Errorf("unexpected provider row %v", row)
		}
	}
	if efforts, _ := body["claudeEfforts"].([]any); len(efforts) != len(claudeEffortLevels) {
		t.Fatalf("claudeEfforts = %v", body["claudeEfforts"])
	}
}

func TestWebCreatePaneSplitsAShellBesideTheAnchor(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	recorder.reply = func([]string) (string, error) {
		return `{"items":[{"metadata":{"uid":"pan-alpha-log"}}]}`, nil
	}
	if code, _ := webSend(t, handler, "POST", webWindowAlpha+"/panes", `{"anchorPane":"pan-alpha-log"}`); code == 201 {
		t.Fatal("a create without confirm ran")
	}
	code, body := webSend(t, handler, "POST", webWindowAlpha+"/panes", `{"anchorPane":"pan-alpha-log","confirm":true}`)
	if code != 201 || body["pane"] == nil {
		t.Fatalf("create pane = %d %v", code, body)
	}
	// Nothing configured: the terminal launcher's default, the Project root.
	want := "create pane --project uid:prj-alpha --window uid:win-alpha-main --pane uid:pan-alpha-log --placement right --cwd-from project -o json"
	if len(recorder.calls) != 1 || recorder.calls[0] != want {
		t.Fatalf("calls = %q", recorder.calls)
	}
	if code, body := webSend(t, handler, "POST", webWindowAlpha+"/panes", `{"anchorPane":"pan-missing","confirm":true}`); code != 404 {
		t.Fatalf("unknown anchor = %d %v", code, body)
	}
	if strings.Contains(strings.Join(recorder.calls, "|"), "pan-missing") {
		t.Fatal("an unknown anchor reached the CLI")
	}
}

func TestWebSplitsFollowTheSavedStartDirectory(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	home := t.TempDir()
	backend.home = func() (string, error) { return home, nil }
	recorder := &webCLIRecorder{reply: func([]string) (string, error) {
		return `{"items":[{"metadata":{"uid":"pan-alpha-log"}}]}`, nil
	}}
	backend.runCLI = recorder.run
	handler := web.New(backend, nil).Handler()
	settings := &settingsCommand{homeDir: backend.home, lookupEnv: backend.env}
	if err := settings.setSplitCWDFrom(splitCWDFromPane, io.Discard); err != nil {
		t.Fatal(err)
	}
	if code, body := webSend(t, handler, "POST", webWindowAlpha+"/panes", `{"anchorPane":"pan-alpha-log","confirm":true}`); code != 201 {
		t.Fatalf("create pane = %d %v", code, body)
	}
	if code, body := webSend(t, handler, "POST", webWindowAlpha+"/panes", `{"anchorPane":"pan-alpha-log","cwdFrom":"project","confirm":true}`); code != 201 {
		t.Fatalf("create pane = %d %v", code, body)
	}
	if !strings.Contains(recorder.calls[0], "--cwd-from pane") || !strings.Contains(recorder.calls[1], "--cwd-from project") {
		t.Fatalf("calls = %q", recorder.calls)
	}
}
