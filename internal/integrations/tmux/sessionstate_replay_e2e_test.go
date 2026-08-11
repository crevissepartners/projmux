package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func TestRealTmuxSessionStateSaveDestroyReplayFieldFidelity(t *testing.T) {
	if os.Getenv("PROJMUX_REAL_TMUX_TEST") == "" {
		t.Skip("set PROJMUX_REAL_TMUX_TEST=1 to run disposable real-tmux replay coverage")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("real tmux test requires tmux: %v", err)
	}

	ctx := context.Background()
	socket := fmt.Sprintf("projmux-phase1-%d", os.Getpid())
	runner := realSocketTmuxRunner{socket: socket}
	run := func(args ...string) string {
		t.Helper()
		output, err := runner.Run(ctx, "tmux", args...)
		if err != nil {
			t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(output))
	}
	t.Cleanup(func() { _, _ = runner.Run(ctx, "tmux", "kill-server") })

	cwd := t.TempDir()
	run("new-session", "-d", "-s", "phase1-keeper", "-c", cwd)
	run("new-session", "-d", "-s", "phase1-fidelity", "-c", cwd)
	shellPane := run("display-message", "-p", "-t", "phase1-fidelity:0.0", "#{pane_id}")
	startupPane := run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", shellPane, "-c", cwd)
	agentPane := run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", startupPane, "-c", cwd)

	seedPaneFieldMatrix(t, run, shellPane, startupPane, agentPane)
	client := NewClient(runner)
	store := sessionstate.NewStore(filepath.Join(t.TempDir(), "sessions"))
	saved, err := client.SaveSessionSnapshot(ctx, store, "phase1-fidelity", time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveSessionSnapshot() error = %v", err)
	}
	assertCapturedPhaseOneMatrix(t, saved)

	run("kill-session", "-t", "phase1-fidelity")
	// Deliberately change the pane index base before replay. Metadata replay must
	// target the %pane_id returned by creation rather than saved/index-derived
	// session:window.pane guesses.
	run("set-option", "-g", "pane-base-index", "5")

	agentBin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(agentBin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(agentBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SHELL", "/bin/sh")
	loaded, err := store.Load("phase1-fidelity")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := client.RestoreSessionSnapshot(ctx, loaded, cwd, sessionstate.SourceAutosave); err != nil {
		t.Fatalf("RestoreSessionSnapshot() error = %v", err)
	}
	assertLivePhaseOneMatrix(t, run, "phase1-fidelity")

	var old sessionstate.Snapshot
	oldJSON := fmt.Sprintf(`{
  "version": 1,
  "session": "phase1-old",
  "default_cwd": %q,
  "saved_at": "2026-08-12T12:00:00Z",
  "windows": [{
    "index": 0,
    "name": "old",
    "active_pane_index": 0,
    "panes": [{
      "index": 0,
      "title": "equal legacy identity",
      "cwd": %q,
      "recipe": {
        "kind": "agent",
        "agent": "codex",
        "resume_id": "01973f21-legacy",
        "topic": "equal legacy identity"
      }
    }]
  }]
}`, cwd, cwd)
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatalf("decode old snapshot: %v", err)
	}
	if err := client.RestoreSessionSnapshot(ctx, old, cwd, sessionstate.SourceAutosave); err != nil {
		t.Fatalf("RestoreSessionSnapshot(old snapshot) error = %v", err)
	}
	oldRow := run("list-panes", "-t", "phase1-old", "-F", replayFieldFormat())
	oldFields := splitReplayFields(oldRow)
	if len(oldFields) != 13 {
		t.Fatalf("old replay fields = %#v, want 13", oldFields)
	}
	if oldFields[1] != "" || oldFields[2] != "equal legacy identity" || oldFields[3] != "" || oldFields[6] != "1" || oldFields[8] != "equal legacy identity" || oldFields[12] != "equal legacy identity" {
		t.Fatalf("old replay fields = %#v, want absent label/ownership with independent preserved title/topic", oldFields)
	}
}

type realSocketTmuxRunner struct {
	socket string
}

func (r realSocketTmuxRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, fmt.Errorf("unexpected command %q", name)
	}
	command := exec.CommandContext(ctx, name, append([]string{"-L", r.socket}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func seedPaneFieldMatrix(t *testing.T, run func(...string) string, shellPane, startupPane, agentPane string) {
	t.Helper()
	set := func(target, option, value string) { run("set-option", "-p", "-t", target, option, value) }
	run("select-pane", "-T", "shell raw title", "-t", shellPane)
	set(shellPane, "@projmux_pane_label", "shell user label")
	run("select-pane", "-T", "startup raw title", "-t", startupPane)
	set(startupPane, "@projmux_recipe_kind", "startup")
	set(startupPane, "@projmux_startup_command", "sleep 30")
	run("select-pane", "-T", "agent raw title", "-t", agentPane)
	set(agentPane, "@projmux_pane_label", "agent user label")
	set(agentPane, "@projmux_ai_managed", "1")
	set(agentPane, "@projmux_ai_agent", "codex")
	set(agentPane, "@projmux_ai_topic", "agent saved topic")
	set(agentPane, "@projmux_ai_topic_manual", "on")
	set(agentPane, "@projmux_ai_resume_id", "01973f21-phase1")
	set(agentPane, "@projmux_ai_resume_source", "session-id")
	set(agentPane, "@projmux_ai_resume_updated_at", "2026-08-12T12:00:00Z")
}

func assertCapturedPhaseOneMatrix(t *testing.T, snap sessionstate.Snapshot) {
	t.Helper()
	if len(snap.Windows) != 1 || len(snap.Windows[0].Panes) != 3 {
		t.Fatalf("captured snapshot = %#v, want one window with three panes", snap)
	}
	panes := snap.Windows[0].Panes
	if panes[0].Label != "shell user label" || panes[0].Title != "shell raw title" || panes[0].Recipe.Kind != sessionstate.RecipeKindShell {
		t.Fatalf("captured shell pane = %#v", panes[0])
	}
	if panes[1].Label != "" || panes[1].Title != "startup raw title" || panes[1].Recipe.Command != "sleep 30" {
		t.Fatalf("captured startup pane = %#v", panes[1])
	}
	if panes[2].Label != "agent user label" || panes[2].Title != "agent raw title" || panes[2].Recipe.Agent != "codex" || panes[2].Recipe.Topic != "agent saved topic" || !panes[2].Recipe.TopicManual {
		t.Fatalf("captured agent pane = %#v", panes[2])
	}
}

func assertLivePhaseOneMatrix(t *testing.T, run func(...string) string, session string) {
	t.Helper()
	rows := strings.Split(run("list-panes", "-s", "-t", session, "-F", replayFieldFormat()), "\n")
	if len(rows) != 3 {
		t.Fatalf("restored rows = %#v, want three panes", rows)
	}
	byKind := make(map[string][]string)
	for _, row := range rows {
		fields := splitReplayFields(row)
		if len(fields) != 13 {
			t.Fatalf("restored fields = %#v, want 13", fields)
		}
		key := "shell"
		if fields[4] == "startup" {
			key = "startup"
		} else if fields[7] != "" {
			key = "agent"
		}
		byKind[key] = fields
	}
	want := map[string][]string{
		"shell":   {"shell user label", "shell raw title", "", "", "", "", "", "", "", "", "", "shell user label"},
		"startup": {"", "startup raw title", "", "startup", "sleep 30", "", "", "", "", "", "", "startup raw title"},
		"agent":   {"agent user label", "agent raw title", "on", "", "", "1", "codex", "agent saved topic", "01973f21-phase1", "session-id", "2026-08-12T12:00:00Z", "agent user label"},
	}
	for kind, expected := range want {
		fields := byKind[kind]
		if len(fields) != 13 {
			t.Fatalf("restored %s fields = %#v", kind, fields)
		}
		// Ignore pane id (field 0); compare label through visible resolver.
		if got := fields[1:]; !reflect.DeepEqual(got, expected) {
			t.Fatalf("restored %s fields = %#v, want %#v", kind, got, expected)
		}
	}
}

func replayFieldFormat() string {
	visible := "#{?#{!=:#{@projmux_pane_label},},#{@projmux_pane_label},#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic},#{?#{||:#{==:#{pane_current_command},sh},#{==:#{pane_current_command},bash}},#{pane_current_command},#{pane_title}}}}"
	return strings.Join([]string{
		"#{pane_id}",
		"#{@projmux_pane_label}",
		"#{pane_title}",
		"#{@projmux_ai_topic_manual}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
		visible,
	}, "\x1f")
}

func splitReplayFields(row string) []string {
	for _, separator := range []string{"\x1f", `\037`, "\t"} {
		fields := strings.Split(row, separator)
		if len(fields) > 1 {
			return fields
		}
	}
	return []string{row}
}
