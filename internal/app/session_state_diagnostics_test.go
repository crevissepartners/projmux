package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func TestSessionStateDiagnosticsDirectPopupAndDeleteSurfaces(t *testing.T) {
	writer := &appLifecycleWriter{err: errors.New("diagnostics append failed")}
	recorder := diagnostics.NewLifecycleRecorder(writer, "surface-run", "0.10.0", "tmux")
	store := sessionstate.NewStore(t.TempDir())
	runner := autosaveCaptureRunner("workspace", "/seed/private/project")
	runner.formats = map[string]string{"#{session_name}": "workspace"}
	cmd := &sessionStateCommand{
		diagnostics: recorder.SessionState(), runner: runner,
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/seed/private/socket,1,0"
			}
			return ""
		},
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		now:          func() time.Time { return time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC) },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}
	if err := cmd.Run([]string{"save"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("workspace"); err != nil {
		t.Fatalf("append failure changed save result: %v", err)
	}
	if err := cmd.executePopupAction(sessionStatePopupSave, "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run([]string{"delete", "--session", "workspace"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 3 {
		t.Fatalf("events = %#v, want direct save, popup save, and delete", writer.events)
	}
	for i, event := range writer.events[:2] {
		if event.Operation != string(diagnostics.OperationSessionStateSave) || event.Source != string(diagnostics.SessionStateSourceManual) || event.WindowCount == nil || *event.WindowCount != 1 || event.PaneCount == nil || *event.PaneCount != 1 || event.ShellRecipeCount == nil || *event.ShellRecipeCount != 1 {
			t.Fatalf("save event %d = %#v", i, event)
		}
	}
	if event := writer.events[2]; event.Operation != string(diagnostics.OperationSessionStateDelete) || event.Source != string(diagnostics.SessionStateSourceManual) || event.ItemCount == nil || *event.ItemCount != 1 {
		t.Fatalf("delete event = %#v", event)
	}
}

func TestSessionStateDiagnosticCountsUseOnlyClosedRecipeKinds(t *testing.T) {
	snap := sessionstate.Snapshot{Windows: []sessionstate.Window{
		{Panes: []sessionstate.Pane{{Recipe: sessionstate.ShellRecipe()}, {Recipe: sessionstate.AgentRecipe("codex", "raw-session", "topic")}}},
		{Panes: []sessionstate.Pane{{Recipe: sessionstate.StartupRecipe("raw command")}, {Recipe: sessionstate.Recipe{Kind: "future-private-kind"}}}},
	}}
	got := sessionStateDiagnosticCounts(snap)
	want := diagnostics.SessionStateCounts{WindowCount: 2, PaneCount: 4, ShellRecipeCount: 1, AgentRecipeCount: 1, StartupRecipeCount: 1}
	if got != want {
		t.Fatalf("counts = %#v, want %#v", got, want)
	}
}

func TestSessionStateDiagnosticsPruneDeduplicatesAggregate(t *testing.T) {
	writer := &appLifecycleWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "prune-run", "0.10.0", "tmux")
	store := sessionstate.NewStore(t.TempDir())
	cmd := &pruneCommand{
		sessionStateDiagnostics: recorder.SessionState(),
		sessionStore:            func() (sessionstate.Store, error) { return store, nil },
		now:                     func() time.Time { return time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC) },
	}
	if err := cmd.Run([]string{"session-state", "delete", "one", "one", "two"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 1 || writer.events[0].ItemCount == nil || *writer.events[0].ItemCount != 2 || writer.events[0].Source != string(diagnostics.SessionStateSourcePrune) {
		t.Fatalf("events = %#v", writer.events)
	}
}

func TestSessionStateDiagnosticsSettingsFailureIsRecordedBeforeFeedbackSwallowsIt(t *testing.T) {
	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "settings-run", "0.10.0", "tmux")
	cmd := &settingsCommand{sessionStateDiagnostics: lifecycle.SessionState(), homeDir: func() (string, error) { return t.TempDir(), nil }, lookupEnv: func(string) string { return "" }}
	if err := cmd.executeWithFeedback(settingsProjectSessionStateSaveLatest, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("feedback wrapper error = %v", err)
	}
	if len(writer.events) != 1 || writer.events[0].Result != "error" || writer.events[0].Source != string(diagnostics.SessionStateSourceSettingsLatest) || !lifecycle.RecordedOutcome() {
		t.Fatalf("events = %#v, feedback = %#v", writer.events, cmd.feedback)
	}
}

func TestSessionStateDiagnosticsSettingsSurfaceTable(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		operation diagnostics.Operation
		source    diagnostics.SessionStateSource
	}{
		{"project latest save", "project-save-latest", diagnostics.OperationSessionStateSave, diagnostics.SessionStateSourceSettingsLatest},
		{"project named save", "project-save-named:team", diagnostics.OperationSessionStateSave, diagnostics.SessionStateSourceSettingsNamed},
		{"project delete", "project-delete", diagnostics.OperationSessionStateDelete, diagnostics.SessionStateSourceSettingsLatest},
		{"current delete", "delete", diagnostics.OperationSessionStateDelete, diagnostics.SessionStateSourceSettingsLatest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "settings-table", "0.10.0", "tmux")
			stateHome := t.TempDir()
			cmd := &settingsCommand{
				sessionStateDiagnostics: lifecycle.SessionState(), homeDir: func() (string, error) { return t.TempDir(), nil },
				lookupEnv: func(name string) string {
					if name == "XDG_STATE_HOME" {
						return stateHome
					}
					if name == "PROJMUX_SESSION" {
						return "workspace"
					}
					return ""
				},
			}
			_ = cmd.executeSessionStateAction(tt.action, &bytes.Buffer{}, &bytes.Buffer{})
			if len(writer.events) != 1 || writer.events[0].Operation != string(tt.operation) || writer.events[0].Source != string(tt.source) {
				t.Fatalf("events = %#v", writer.events)
			}
		})
	}
}

func TestSessionStateDiagnosticsSupportReportDropsSeededUnknownMetadata(t *testing.T) {
	stateHome := t.TempDir()
	path := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"at":"2026-08-14T00:00:00Z","level":"error","component":"session-state","event":"session-state.outcome","result":"error","duration_ms":1,"run_id":"safe-run","version":"0.10.0","mux_backend":"tmux","kind":"runtime","operation":"session-state.restore","code":"session-state.restore.failed","source":"startup-latest","snapshot_path":"/seed/private/snapshot","pane_command":"sleep 300","session_id":"raw-session-id"}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &diagnosticsCommand{
		lookupEnv: func(name string) string {
			if name == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		homeDir: func() (string, error) { return t.TempDir(), nil },
	}
	data, entry, err := cmd.supportOperationalErrors()
	if err != nil || entry.Status != "included" {
		t.Fatalf("supportOperationalErrors() = %s, %#v, %v", data, entry, err)
	}
	output := string(data)
	for _, forbidden := range []string{"/seed/private/snapshot", "sleep 300", "raw-session-id", "snapshot_path", "pane_command", "session_id"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("support output leaked %q: %s", forbidden, output)
		}
	}
	for _, want := range []string{"session-state.restore.failed", "startup-latest"} {
		if !strings.Contains(output, want) {
			t.Fatalf("support output missing %q: %s", want, output)
		}
	}
}
