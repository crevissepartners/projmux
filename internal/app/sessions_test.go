package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestAppRunSessionsDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotOptions intpickercompat.Options
	app := &App{
		sessions: &sessionsCommand{
			recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
				return []inttmux.RecentSessionSummary{
					{Name: "repo-b", Attached: true, WindowCount: 3, PaneCount: 4, Path: "/tmp/repo-b"},
					{Name: "home", Attached: false, WindowCount: 1, PaneCount: 1, Path: "/home/tester"},
				}, nil
			}),
			store: &recordingSessionsStore{
				selections: map[string]corepreview.Selection{
					"repo-b": {
						SessionName: "repo-b",
						WindowIndex: "3",
						PaneIndex:   "1",
					},
				},
			},
			runner: sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
				gotOptions = options
				return intpickercompat.Result{Value: "repo-b"}, nil
			}),
			native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
				gotOptions = options
				return intpickercompat.Result{Value: "repo-b"}, nil
			})),
			executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			opener:     &recordingSessionsOpener{},
		},
	}

	opener := app.sessions.opener.(*recordingSessionsOpener)
	if err := app.Run([]string{"sessions"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotOptions.UI, switchUIPopup; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotOptions.Footer, "Preview follows the focused target.\nSession state opens read-only; destructive actions keep the current confirmation policy."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotOptions.ExpectKeys, []string{sessionsKillExpectKey, sessionsStateExpectKey}; !equalStrings(got, want) {
		t.Fatalf("runner expect keys = %q, want %q", got, want)
	}
	if got, want := gotOptions.Entries, []intpickercompat.Entry{
		{Label: "[ ]  \x1b[32m[Attached]\x1b[0m  \x1b[34m3 Windows\x1b[0m  repo-b", Value: "repo-b"},
		{Label: "[ ]  \x1b[33m[Detached]\x1b[0m  home", Value: "home"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := gotOptions.PreviewCommand, "exec '/tmp/proj mux/bin/projmux' 'session-popup' 'preview' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"alt-2:abort",
		"alt-3:abort",
		"left:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/proj mux/bin/projmux' 'session-popup' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := opener.openSessionName, "repo-b"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got, want := opener.windowIndex, "3"; got != want {
		t.Fatalf("open window = %q, want %q", got, want)
	}
	if got, want := opener.paneIndex, "1"; got != want {
		t.Fatalf("open pane = %q, want %q", got, want)
	}
}

func TestSessionsCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotOptions intpickercompat.Options
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
		}),
		runner: sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			gotOptions = options
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			gotOptions = options
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     &recordingSessionsOpener{},
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := gotOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotOptions.Footer, "Preview follows the focused target.\nSession state opens read-only; destructive actions keep the current confirmation policy."; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
}

func TestSessionsStateOverviewShowsReadModelWithoutImmediateMutation(t *testing.T) {
	t.Parallel()

	project := filepath.Join(t.TempDir(), "repo")
	store := sessionstate.NewStore(t.TempDir())
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "repo",
		DefaultCWD: project,
		SavedAt:    time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC),
		Windows: []sessionstate.Window{{
			Index:           0,
			Name:            "dev",
			ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{{
				Index:  0,
				Title:  "editor",
				CWD:    project,
				Recipe: sessionstate.AgentRecipeWithResumeMetadata("codex", "codex-session", "topic", "session-id", "2026-05-12T03:04:05Z"),
			}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	if err := corelayout.NewStore(project).Save("team", corelayout.Preset{
		SchemaVersion: corelayout.SchemaVersion,
		Windows: []corelayout.Window{{
			Index: 0,
			Panes: []corelayout.Pane{{Index: 0, CWD: "${PROJMUX_CWD}", Recipe: sessionstate.ShellRecipe()}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	cmd := &sessionsCommand{
		stateStore: func() (sessionstate.Store, error) { return store, nil },
	}

	entries := cmd.sessionStateOverviewEntries("repo", []inttmux.RecentSessionSummary{{Name: "repo", Path: project}})
	for _, want := range []string{"Latest snapshot", "saved", "Named snapshots", "Named snapshot", "team", "Window", "dev", "Pane", "editor", "Pane cwd", project, "Pane recipe", "agent codex", "resume available"} {
		if !hasEntryLabelContaining(entries, want) {
			t.Fatalf("state overview entries = %#v, want %q", entries, want)
		}
	}
	for _, entry := range entries {
		if entry.Value != settingsNoopValue && entry.Value != settingsBackValue {
			t.Fatalf("state overview entry = %#v, want read-only/no immediate mutation", entry)
		}
		for _, forbidden := range []string{"Save", "Delete", "Restore", "Preview"} {
			if strings.Contains(entry.Label, forbidden) {
				t.Fatalf("state overview entry = %#v, want no immediate mutation label %q", entry, forbidden)
			}
		}
	}
}

func TestSessionsCommandNativeBackendDoesNotCallCompatRunner(t *testing.T) {
	t.Parallel()

	var compatCalled bool
	opener := &recordingSessionsOpener{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
		}),
		runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			compatCalled = true
			return intpickercompat.Result{}, nil
		}),
		native: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			if options.UI != switchUIPopup {
				t.Fatalf("native UI = %q, want %q", options.UI, switchUIPopup)
			}
			if len(options.Items) != 1 || options.Items[0].Value != "repo-b" {
				t.Fatalf("native items = %#v, want repo-b", options.Items)
			}
			return intpicker.Result{Key: "enter", Value: "repo-b"}, nil
		}),
		lookupEnv: func(name string) string {
			if name == intpicker.BackendEnv {
				return string(intpicker.BackendNative)
			}
			return ""
		},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat runner was called for native sessions backend")
	}
	if opener.openSessionName != "repo-b" {
		t.Fatalf("open session = %q, want repo-b", opener.openSessionName)
	}
}

func TestSessionsCommandCtrlXKillsSelectedSessionAndReopensPicker(t *testing.T) {
	t.Parallel()

	recentCalls := 0
	runnerCalls := 0
	opener := &recordingSessionsOpener{}
	killer := &recordingSessionsKiller{}
	var gotOptions []intpickercompat.Options
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			recentCalls++
			if recentCalls == 1 {
				return []inttmux.RecentSessionSummary{
					{Name: "repo-b", Attached: false},
					{Name: "home", Attached: true},
				}, nil
			}
			return []inttmux.RecentSessionSummary{{Name: "home", Attached: true}}, nil
		}),
		runner: sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			gotOptions = append(gotOptions, options)
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			gotOptions = append(gotOptions, options)
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
		killer:     killer,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(gotOptions), 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	for i, options := range gotOptions {
		if got, want := options.ExpectKeys, []string{sessionsKillExpectKey, sessionsStateExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
	}
	if got, want := killer.killSessionName, "repo-b"; got != want {
		t.Fatalf("kill session = %q, want %q", got, want)
	}
	if got := opener.openSessionName; got != "" {
		t.Fatalf("open session called unexpectedly: %q", got)
	}
}

func TestSessionsCommandCtrlXSwitchesToFallbackBeforeKillingAttachedSession(t *testing.T) {
	t.Parallel()

	recentCalls := 0
	runnerCalls := 0
	opener := &recordingSessionsOpener{}
	killer := &recordingSessionsKiller{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			recentCalls++
			if recentCalls == 1 {
				return []inttmux.RecentSessionSummary{
					{Name: "repo-b", Attached: true},
					{Name: "home", Attached: false},
				}, nil
			}
			return []inttmux.RecentSessionSummary{{Name: "home", Attached: true}}, nil
		}),
		runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
		killer:     killer,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := opener.openSessionName, "home"; got != want {
		t.Fatalf("fallback open session = %q, want %q", got, want)
	}
	if got, want := killer.killSessionName, "repo-b"; got != want {
		t.Fatalf("kill session = %q, want %q", got, want)
	}
}

func TestSessionsCommandCtrlXBlocksAttachedSessionKillWithoutFallback(t *testing.T) {
	t.Parallel()

	runnerCalls := 0
	opener := &recordingSessionsOpener{}
	killer := &recordingSessionsKiller{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b", Attached: true}}, nil
		}),
		runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intpickercompat.Result{Key: sessionsKillExpectKey, Value: "repo-b"}, nil
			}
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
		killer:     killer,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := killer.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
	if got := opener.openSessionName; got != "" {
		t.Fatalf("open session called unexpectedly: %q", got)
	}
	if got, want := runnerCalls, 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
}

func TestSessionsCommandAllowsEmptySelection(t *testing.T) {
	t.Parallel()

	opener := &recordingSessionsOpener{}
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
		}),
		runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     opener,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := opener.openSessionName; got != "" {
		t.Fatalf("OpenSession called unexpectedly: %q", got)
	}
}

func TestSessionsCommandReturnsWithoutPickerWhenRecentListIsEmpty(t *testing.T) {
	t.Parallel()

	called := false
	cmd := &sessionsCommand{
		recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
			return nil, nil
		}),
		runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			called = true
			return intpickercompat.Result{}, nil
		}),
		native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
			called = true
			return intpickercompat.Result{}, nil
		})),
		executable: func() (string, error) { return "/tmp/projmux", nil },
		opener:     &recordingSessionsOpener{},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("runner called unexpectedly")
	}
}

func TestFormatStoredTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		window     string
		pane       string
		wantTarget string
	}{
		{name: "window and pane", window: "3", pane: "1", wantTarget: "w3.p1"},
		{name: "window only", window: "3", pane: "", wantTarget: "w3"},
		{name: "missing window", window: "", pane: "1", wantTarget: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatStoredTarget(tt.window, tt.pane); got != tt.wantTarget {
				t.Fatalf("formatStoredTarget(%q, %q) = %q, want %q", tt.window, tt.pane, got, tt.wantTarget)
			}
		})
	}
}

func TestSessionsCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "invalid ui", args: []string{"--ui=dialog"}, want: "invalid --ui value"},
		{name: "positional args", args: []string{"extra"}, want: "sessions does not accept positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&sessionsCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestSessionsCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *sessionsCommand
		want string
	}{
		{name: "recent resolver", cmd: &sessionsCommand{}, want: "recent tmux session resolver is not configured"},
		{
			name: "recent sessions",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return nil, errors.New("tmux failed")
				}),
			},
			want: "resolve recent tmux sessions",
		},
		{
			name: "executable resolver",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{}, nil
				}),
				native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{}, nil
				})),
			},
			want: "sessions executable resolver is not configured",
		},
		{
			name: "resolve executable",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner:     sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) { return intpickercompat.Result{}, nil }),
				native:     nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) { return intpickercompat.Result{}, nil })),
				executable: func() (string, error) { return "", errors.New("not found") },
			},
			want: "resolve sessions executable",
		},
		{
			name: "runner",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{}, errors.New("picker failed")
				}),
				native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{}, errors.New("picker failed")
				})),
				executable: func() (string, error) { return "/tmp/projmux", nil },
			},
			want: "run sessions picker",
		},
		{
			name: "missing opener",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				}),
				native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				})),
				executable: func() (string, error) { return "/tmp/projmux", nil },
			},
			want: "sessions opener is not configured",
		},
		{
			name: "load selection",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				store: &recordingSessionsStore{err: errors.New("state failed")},
				runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				}),
				native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				})),
				executable: func() (string, error) { return "/tmp/projmux", nil },
				opener:     &recordingSessionsOpener{},
			},
			want: "load sessions preview selection",
		},
		{
			name: "open session",
			cmd: &sessionsCommand{
				recent: sessionsRecentFunc(func(context.Context) ([]inttmux.RecentSessionSummary, error) {
					return []inttmux.RecentSessionSummary{{Name: "repo-b"}}, nil
				}),
				runner: sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				}),
				native: nativePickerFromCompatRunner(sessionsRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Value: "repo-b"}, nil
				})),
				executable: func() (string, error) { return "/tmp/projmux", nil },
				opener:     &recordingSessionsOpener{openErr: errors.New("attach failed")},
			},
			want: "open tmux session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

type sessionsRecentFunc func(context.Context) ([]inttmux.RecentSessionSummary, error)

func (f sessionsRecentFunc) RecentSessionSummaries(ctx context.Context) ([]inttmux.RecentSessionSummary, error) {
	return f(ctx)
}

type sessionsRunnerFunc func(options intpickercompat.Options) (intpickercompat.Result, error)

func (f sessionsRunnerFunc) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	return f(options)
}

type recordingSessionsOpener struct {
	openSessionName string
	windowIndex     string
	paneIndex       string
	openErr         error
}

func (o *recordingSessionsOpener) OpenSessionTarget(_ context.Context, sessionName, windowIndex, paneIndex string) error {
	o.openSessionName = sessionName
	o.windowIndex = windowIndex
	o.paneIndex = paneIndex
	return o.openErr
}

type recordingSessionsKiller struct {
	killSessionName string
	killErr         error
}

func (k *recordingSessionsKiller) KillSession(_ context.Context, sessionName string) error {
	k.killSessionName = sessionName
	return k.killErr
}

type recordingSessionsStore struct {
	selections map[string]corepreview.Selection
	found      bool
	err        error
}

func (s *recordingSessionsStore) ReadSelection(sessionName string) (corepreview.Selection, bool, error) {
	if s.err != nil {
		return corepreview.Selection{}, false, s.err
	}
	if s.selections != nil {
		selection, ok := s.selections[sessionName]
		return selection, ok, nil
	}
	return corepreview.Selection{}, s.found, nil
}
