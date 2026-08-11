package app

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestSessionStateStatusShowsToggleAndSnapshotReadModel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC)
	store := sessionstate.NewStore(t.TempDir())
	saveSessionStateTestSnapshot(t, store, now.Add(-2*time.Minute))
	configHome := t.TempDir()
	cmd := &sessionStateCommand{
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case sessionStateAutosaveEnv:
				return "off"
			default:
				return ""
			}
		},
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		now:          func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"Session State",
		"session:      workspace",
		"source:       autosave",
		"auto-save:    off (PROJMUX_SESSIONSTATE_AUTOSAVE env)",
		"snapshot:     saved",
		"2m ago",
		"window 0 editor (2 panes)",
		"pane 0.1 watcher startup make watch",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStateStatusShowsSnapshotSource(t *testing.T) {
	t.Parallel()

	store := sessionstate.NewStore(t.TempDir())
	saveSessionStateTestSnapshotSource(t, store, time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC), sessionstate.LayoutSource("team"))
	cmd := &sessionStateCommand{
		lookupEnv:    func(string) string { return "" },
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		now:          func() time.Time { return time.Date(2026, time.May, 12, 12, 1, 0, 0, time.UTC) },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"status", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output := stdout.String(); !strings.Contains(output, "source:       layout(team)") {
		t.Fatalf("status output = %q, want snapshot source", output)
	}
}

func TestSessionStateSaveCapturesCurrentSessionEvenWhenAutosaveDisabled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 3, 4, 5, 0, time.UTC)
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{session_name}": "workspace",
		},
		outputs: map[string]string{
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):   "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"): "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &sessionStateCommand{
		runner: runner,
		lookupEnv: func(name string) string {
			switch name {
			case "TMUX":
				return "/tmp/tmux,1,0"
			case sessionStateAutosaveEnv:
				return "off"
			default:
				return ""
			}
		},
		homeDir: func() (string, error) { return t.TempDir(), nil },
		now:     func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"save"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "saved session snapshot: workspace (1 window, 1 pane)") {
		t.Fatalf("stdout = %q, want saved summary", got)
	}
	loaded, err := sessionstate.NewStore(dir).Load("workspace")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Session != "workspace" || loaded.SavedAt != now.UTC() {
		t.Fatalf("loaded snapshot = %#v, want saved current session", loaded)
	}
	if got := loaded.Windows[0].Panes[0].Title; got != "shell" {
		t.Fatalf("loaded pane title = %q, want captured tmux pane title", got)
	}
	for _, call := range runner.calls {
		if reflect.DeepEqual(call.args, []string{"display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_autosave_at}"}) {
			t.Fatalf("manual save read autosave debounce gate; calls = %#v", runner.calls)
		}
	}
}

func TestSessionStateSaveFailsClearlyOutsideTmux(t *testing.T) {
	t.Parallel()

	cmd := &sessionStateCommand{
		runner:       &recordingTmuxRunner{},
		lookupEnv:    func(string) string { return "" },
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	err := cmd.Run([]string{"save"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "requires a current tmux session") {
		t.Fatalf("error = %v, want clear outside-tmux failure", err)
	}
}

func TestSessionStateDeleteRemovesExplicitSnapshotWithoutPrompt(t *testing.T) {
	t.Parallel()

	store := sessionstate.NewStore(t.TempDir())
	saveSessionStateTestSnapshot(t, store, time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC))
	cmd := &sessionStateCommand{
		lookupEnv:    func(string) string { return "" },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"delete", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "deleted session snapshot: workspace") {
		t.Fatalf("stdout = %q, want deleted message", got)
	}
	if _, err := store.Load("workspace"); !errors.Is(err, sessionstate.ErrNotFound) {
		t.Fatalf("Load() after delete error = %v, want %v", err, sessionstate.ErrNotFound)
	}
}

func TestSessionStateRestoreDryRunPrintsPreviewWithoutTmux(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC)
	store := sessionstate.NewStore(t.TempDir())
	saveSessionStateTestSnapshot(t, store, now.Add(-time.Minute))
	runner := &recordingTmuxRunner{}
	cmd := &sessionStateCommand{
		runner:       runner,
		lookupEnv:    func(string) string { return "" },
		now:          func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"restore", "--dry-run", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"Session State Restore Preview",
		"source:       autosave",
		"Dry run only; no tmux commands were executed.",
		"window 0 editor (2 panes)",
		"pane 0.1 watcher startup make watch",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run tmux calls = %#v, want none", runner.calls)
	}
}

func TestSessionStateRestoreDryRunShowsResumeHealth(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC)
	store := sessionstate.NewStore(t.TempDir())
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		DefaultCWD: "/tmp/workspace",
		SavedAt:    now,
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "agents",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{
				{Index: 0, Title: "codex", CWD: "/tmp/workspace", Recipe: sessionstate.AgentRecipeWithResumeMetadata("codex", "codex-session", "topic", "session-id", now.Format(time.RFC3339))},
				{Index: 1, Title: "stale claude", CWD: "/tmp/workspace", Recipe: sessionstate.AgentRecipeWithResumeMetadata("claude", "claude-session", "topic", "claude-transcript", now.Add(-48*time.Hour).Format(time.RFC3339))},
				{Index: 2, Title: "missing codex", CWD: "/tmp/workspace", Recipe: sessionstate.AgentRecipe("codex", "", "topic")},
				{Index: 3, Title: "antigravity", CWD: "/tmp/workspace", Recipe: sessionstate.AgentRecipeWithResumeMetadata("antigravity", "123e4567-e89b-12d3-a456-426614174000", "topic", "hook", now.Format(time.RFC3339))},
				{Index: 4, Title: "missing antigravity", CWD: "/tmp/workspace", Recipe: sessionstate.AgentRecipe("antigravity", "", "topic")},
			},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cmd := &sessionStateCommand{
		lookupEnv:    func(string) string { return "" },
		now:          func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"restore", "--dry-run", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"status available confidence high",
		"status stale confidence medium",
		"status unavailable confidence none source unknown",
		"pane 0.3 antigravity agent antigravity resume 123e4567-e89b-12d3-a456-426614174000",
		"antigravity status unavailable confidence none source unknown",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStateRestoreRejectsExecutionWithoutDryRun(t *testing.T) {
	t.Parallel()

	cmd := &sessionStateCommand{}
	err := cmd.Run([]string{"restore", "--session", "workspace"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "only supports --dry-run") {
		t.Fatalf("error = %v, want dry-run gate", err)
	}
}

func TestSessionStatePopupPreviewUsesDryRunReadModel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 12, 0, 0, 0, time.UTC)
	store := sessionstate.NewStore(t.TempDir())
	saveSessionStateTestSnapshot(t, store, now.Add(-time.Minute))
	var sawPopup bool
	cmd := &sessionStateCommand{
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			sawPopup = true
			if got, want := options.UI, "sessionstate-popup"; got != want {
				t.Fatalf("popup UI = %q, want %q", got, want)
			}
			for _, forbidden := range []string{
				"sessionstate-popup:delete",
				"sessionstate-popup:toggle-autosave",
				"sessionstate-popup:toggle-autorestore",
			} {
				if hasEntryValue(options.Entries, forbidden) {
					t.Fatalf("popup entries include deferred action %q: %#v", forbidden, options.Entries)
				}
			}
			if !hasEntryValue(options.Entries, sessionStatePopupSave) || !hasEntryValue(options.Entries, sessionStatePopupPreviewRestore) {
				t.Fatalf("popup entries = %#v, want save and preview actions", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: sessionStatePopupPreviewRestore}, nil
		})),
		lookupEnv:    func(string) string { return "" },
		now:          func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) { return store, nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"popup", "--session", "workspace"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawPopup {
		t.Fatal("popup picker was not opened")
	}
	output := stdout.String()
	for _, want := range []string{
		"Session State Restore Preview",
		"source:       autosave",
		"Dry run only; no tmux commands were executed.",
		"pane 0.1 watcher startup make watch",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("popup preview output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStatePopupSaveNowCapturesCurrentSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 12, 3, 4, 5, 0, time.UTC)
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{session_name}": "workspace",
		},
		outputs: map[string]string{
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):   "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"): "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	var calls int
	cmd := &sessionStateCommand{
		runner: runner,
		nativePicker: nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			calls++
			switch calls {
			case 1:
				if !hasEntryValue(options.Entries, sessionStatePopupSave) {
					t.Fatalf("popup entries = %#v, want save action", options.Entries)
				}
				return intpickercompat.Result{Key: "enter", Value: sessionStatePopupSave}, nil
			case 2:
				return intpickercompat.Result{Key: "enter", Value: sessionStatePopupClose}, nil
			default:
				t.Fatalf("unexpected popup call %d", calls)
				return intpickercompat.Result{}, nil
			}
		})),
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux,1,0"
			}
			return ""
		},
		homeDir: func() (string, error) { return t.TempDir(), nil },
		now:     func() time.Time { return now },
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"popup"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "saved session snapshot: workspace (1 window, 1 pane)") {
		t.Fatalf("stdout = %q, want saved summary", got)
	}
	loaded, err := sessionstate.NewStore(dir).Load("workspace")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Session != "workspace" || loaded.SavedAt != now.UTC() {
		t.Fatalf("loaded snapshot = %#v, want saved current session", loaded)
	}
	if got := loaded.Windows[0].Panes[0].Title; got != "shell" {
		t.Fatalf("loaded pane title = %q, want captured tmux pane title", got)
	}
}

func saveSessionStateTestSnapshot(t *testing.T, store sessionstate.Store, savedAt time.Time) {
	t.Helper()
	saveSessionStateTestSnapshotSource(t, store, savedAt, "")
}

func saveSessionStateTestSnapshotSource(t *testing.T, store sessionstate.Store, savedAt time.Time, source string) {
	t.Helper()
	if err := store.Save(sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "workspace",
		Source:     source,
		DefaultCWD: "/tmp/workspace",
		SavedAt:    savedAt,
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "editor",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{
				{Index: 0, CWD: "/tmp/workspace", Recipe: sessionstate.ShellRecipe()},
				{Index: 1, Title: "watcher", CWD: "/tmp/workspace", Recipe: sessionstate.StartupRecipe("make watch")},
			},
		}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}
