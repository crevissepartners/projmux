package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type sessionPopupOpenerFunc func(context.Context, string, string, string) error

func (f sessionPopupOpenerFunc) OpenSessionTarget(ctx context.Context, sessionName, windowIndex, paneIndex string) error {
	return f(ctx, sessionName, windowIndex, paneIndex)
}

type pruneInventoryFunc func(context.Context) ([]lifecycle.SessionInventory, error)

func (f pruneInventoryFunc) ListEphemeralSessions(ctx context.Context) ([]lifecycle.SessionInventory, error) {
	return f(ctx)
}

type pruneKillerFunc func(context.Context, string) error

func (f pruneKillerFunc) KillSession(ctx context.Context, sessionName string) error {
	return f(ctx, sessionName)
}

type lifecycleTmuxRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f lifecycleTmuxRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestLifecycleMutationSurfaceInventoryUsesSharedRecorder(t *testing.T) {
	t.Parallel()
	recorder := diagnostics.NewLifecycleRecorder(&appLifecycleWriter{}, "inventory-run", "0.10.0", "tmux")
	application := NewWithLifecycleDiagnostics(recorder)

	wantInventory := []string{
		"runtime attach", "runtime stop", "runtime sessions open/kill",
		"switch create/restore/open/kill", "internal tmux apply", "internal session-popup open",
		"window recent", "runtime prune", "internal focus switch-client", "shell open-app",
		"snapshot replay create", "popup-toggle cancel restore",
	}
	if !reflect.DeepEqual(lifecycleMutationSurfaceInventory, wantInventory) {
		t.Fatalf("production inventory = %#v, want %#v", lifecycleMutationSurfaceInventory, wantInventory)
	}
	// Constructor assertions are paired with action-level tests in this file,
	// tmux/client_test.go (baseline + replay), and tmux_test.go (apply + popup
	// restore), so fields alone cannot satisfy the maintained contract.
	surfaces := map[string]*diagnostics.LifecycleRecorder{
		"runtime attach":     application.attach.diagnostics,
		"runtime stop":       application.kill.diagnostics,
		"runtime sessions":   application.sessions.diagnostics,
		"switch mutations":   application.switcher.diagnostics,
		"tmux apply/restore": application.tmux.diagnostics,
		"session-popup open": application.sessionPopup.diagnostics,
		"window recent":      application.window.recent.diagnostics,
		"prune ephemeral":    application.prune.diagnostics,
		"focus switch":       application.focus.diagnostics,
		"shell new-session":  application.shell.diagnostics,
	}
	for name, got := range surfaces {
		if got != recorder {
			t.Errorf("%s recorder = %p, want shared %p", name, got, recorder)
		}
	}
}

func TestCorrectiveLifecycleSurfacesSuccessAndFailure(t *testing.T) {
	t.Parallel()
	forced := errors.New("closed failure fixture")
	tests := []struct {
		name      string
		operation diagnostics.Operation
		run       func(*diagnostics.LifecycleRecorder, error) error
	}{
		{
			name:      "session-popup open",
			operation: diagnostics.OperationSessionSwitch,
			run: func(recorder *diagnostics.LifecycleRecorder, wantErr error) error {
				cmd := &sessionPopupCommand{
					diagnostics:   recorder,
					openOperation: func() diagnostics.Operation { return diagnostics.OperationSessionSwitch },
					store:         &stubPreviewStore{},
					opener: sessionPopupOpenerFunc(func(context.Context, string, string, string) error {
						return wantErr
					}),
				}
				return cmd.Run([]string{"open", "raw-session-never-recorded"}, io.Discard, io.Discard)
			},
		},
		{
			name:      "window recent",
			operation: diagnostics.OperationSessionSwitch,
			run: func(recorder *diagnostics.LifecycleRecorder, wantErr error) error {
				cmd := &recentWindowCommand{
					diagnostics:   recorder,
					openOperation: func() diagnostics.Operation { return diagnostics.OperationSessionSwitch },
					opener: sessionPopupOpenerFunc(func(context.Context, string, string, string) error {
						return wantErr
					}),
				}
				return cmd.openRecentWindow(context.Background(), recentwindows.Candidate{Snapshot: recentwindows.Snapshot{Session: "raw-session", WindowID: "@raw"}})
			},
		},
		{
			name:      "prune ephemeral",
			operation: diagnostics.OperationSessionKill,
			run: func(recorder *diagnostics.LifecycleRecorder, wantErr error) error {
				cmd := &pruneCommand{
					diagnostics: recorder,
					inventory: pruneInventoryFunc(func(context.Context) ([]lifecycle.SessionInventory, error) {
						return []lifecycle.SessionInventory{{Name: "raw-old", Ephemeral: true, LastAttached: 1}}, nil
					}),
					killer: pruneKillerFunc(func(context.Context, string) error { return wantErr }),
				}
				return cmd.Run([]string{"ephemeral", "--keep=0"}, io.Discard, io.Discard)
			},
		},
		{
			name:      "focus switch",
			operation: diagnostics.OperationSessionSwitch,
			run: func(recorder *diagnostics.LifecycleRecorder, wantErr error) error {
				cmd := &focusCommand{
					diagnostics: recorder,
					runner: lifecycleTmuxRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
						return nil, wantErr
					}),
				}
				return cmd.switchClient(context.Background(), "/raw/socket", "/raw/client", "raw-session")
			},
		},
		{
			name:      "shell opens application session",
			operation: diagnostics.OperationSessionAttach,
			run: func(recorder *diagnostics.LifecycleRecorder, wantErr error) error {
				cmd := &shellCommand{
					diagnostics: recorder,
					runCommand:  func(context.Context, []string, string, ...string) error { return wantErr },
				}
				return cmd.executeShellSession(context.Background(), "raw-socket", "raw-session", "tmux", "new-session", "-A")
			},
		},
	}

	for _, tt := range tests {
		for _, fail := range []bool{false, true} {
			name := tt.name + map[bool]string{false: "/success", true: "/failure"}[fail]
			t.Run(name, func(t *testing.T) {
				writer := &appLifecycleWriter{}
				recorder := diagnostics.NewLifecycleRecorder(writer, "surface-run", "0.10.0", "tmux")
				finish := recorder.BeginCommand()
				var injected error
				if fail {
					injected = forced
				}
				err := tt.run(recorder, injected)
				finish(err)
				if fail && err == nil || !fail && err != nil {
					t.Fatalf("run error = %v, fail=%v", err, fail)
				}
				if len(writer.events) != 2 {
					t.Fatalf("events = %#v, want exactly one lifecycle pair", writer.events)
				}
				start, outcome := writer.events[0], writer.events[1]
				if start.RunID != outcome.RunID || start.RunID != "surface-run" || start.Operation != string(tt.operation) || outcome.Operation != string(tt.operation) {
					t.Fatalf("pair = %#v, want correlated %s", writer.events, tt.operation)
				}
				if outcome.Message != "" {
					t.Fatalf("outcome leaked raw detail: %#v", outcome)
				}
			})
		}
	}
}

func TestShellNewSessionAtomicOpenUsesAttachOwnership(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
	}{
		{name: "existing session"},
		{name: "absent session provisioned by new-session A"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			recorder := diagnostics.NewLifecycleRecorder(writer, "shell-outer", "0.10.0", "tmux")
			finish := recorder.BeginCommand()
			cmd := &shellCommand{
				diagnostics: recorder,
				runCommand:  func(context.Context, []string, string, ...string) error { return nil },
			}
			err := cmd.executeShellSession(context.Background(), "raw-socket", "raw-session", "tmux", "new-session", "-A")
			finish(err)
			if err != nil {
				t.Fatal(err)
			}
			if len(writer.events) != 2 || writer.events[0].Operation != string(diagnostics.OperationSessionAttach) || writer.events[1].Operation != string(diagnostics.OperationSessionAttach) {
				t.Fatalf("events = %#v, want one session.attach open-application pair", writer.events)
			}
		})
	}
}

func TestCorrectiveLifecycleOwnershipSuppressesGenericFallback(t *testing.T) {
	t.Parallel()
	writer := &appLifecycleWriter{err: errors.New("writer unavailable"), drop: true}
	recorder := diagnostics.NewLifecycleRecorder(writer, "best-effort-run", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	recorder.Mark(diagnostics.OperationSessionSwitch)
	original := errors.New("original switch failure")
	finish(original)
	if !recorder.RecordedOutcome() {
		t.Fatal("logical ownership lost after writer failure")
	}
	store := diagnostics.NewStore(t.TempDir() + "/operations.jsonl")
	if err := diagnostics.RecordOutcome(store, []string{"focus", "raw-target"}, "best-effort-run", "0.10.0", "tmux", time.Now(), original, false, recorder.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read()
	if err != nil || len(events) != 0 {
		t.Fatalf("generic fallback events = %#v err=%v, want none", events, err)
	}
}

func TestAttachAutoExecutionPruneThenOpenFailureUsesActualStage(t *testing.T) {
	t.Setenv("TMUX", "")
	writer := &appLifecycleWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "auto-composite", "0.10.0", "tmux")
	calls := 0
	runner := lifecycleTmuxRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 && len(args) > 0 && args[0] == "kill-session" {
			return nil, nil
		}
		if calls == 2 && len(args) > 0 && args[0] == "attach-session" {
			return nil, errors.New("private attach target")
		}
		return nil, errors.New("unexpected mutation call")
	})
	client := inttmux.NewClient(runner, inttmux.WithLifecycleDiagnostics(recorder))
	cmd := &attachCommand{sessions: client, killer: client}
	finish := recorder.BeginCommand()
	err := cmd.executeAutoAttachPlan(context.Background(), lifecycle.AutoAttachPlan{
		PruneTargets: []string{"raw-old"},
		AttachTarget: "raw-next",
	}, "home", "/private/home")
	finish(err)
	if err == nil || calls != 2 {
		t.Fatalf("execution error=%v calls=%d, want kill success then attach failure", err, calls)
	}
	if len(writer.events) != 2 || writer.events[0].Operation != string(diagnostics.OperationSessionKill) || writer.events[1].Operation != string(diagnostics.OperationSessionKill) || writer.events[1].Code != string(diagnostics.CodeSessionAttachFailed) || writer.events[0].RunID != writer.events[1].RunID {
		t.Fatalf("events = %#v, want one correlated kill -> attach.failed pair", writer.events)
	}
	store := diagnostics.NewStore(t.TempDir() + "/fallback.jsonl")
	if recordErr := diagnostics.RecordOutcome(store, []string{"attach", "auto"}, "auto-composite", "0.10.0", "tmux", time.Now(), err, false, recorder.RecordedOutcome()); recordErr != nil {
		t.Fatal(recordErr)
	}
	events, readErr := store.Read()
	if readErr != nil || len(events) != 0 {
		t.Fatalf("generic fallback events=%#v err=%v", events, readErr)
	}
}

func TestFocusLifecycleSealsAtActualSwitchBoundary(t *testing.T) {
	t.Parallel()
	for _, after := range []string{"select-window", "select-pane", "output"} {
		t.Run(after, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			recorder := diagnostics.NewLifecycleRecorder(writer, "focus-sealed", "0.10.0", "tmux")
			finish := recorder.BeginCommand()
			runner := lifecycleTmuxRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if slices.Contains(args, after) {
					return nil, errors.New("post-switch private failure")
				}
				return nil, nil
			})
			cmd := &focusCommand{diagnostics: recorder, runner: runner}
			if err := cmd.switchClient(context.Background(), "/raw/socket", "/raw/client", "raw-session"); err != nil {
				t.Fatal(err)
			}
			var laterErr error
			switch after {
			case "select-window":
				laterErr = cmd.selectWindow(context.Background(), "/raw/socket", "raw-session:@raw")
			case "select-pane":
				laterErr = cmd.selectPane(context.Background(), "/raw/socket", "raw-session:@raw.%raw")
			case "output":
				laterErr = errors.New("stdout rejected")
			}
			if laterErr == nil {
				t.Fatal("post-switch fixture did not fail")
			}
			finish(laterErr)
			if len(writer.events) != 2 || writer.events[1].Result != "success" || writer.events[1].Operation != string(diagnostics.OperationSessionSwitch) || writer.events[1].Code != "" {
				t.Fatalf("events = %#v, want sealed switch success despite later failure", writer.events)
			}
		})
	}
}

func TestFocusLifecycleSealWriterFailureIsBestEffort(t *testing.T) {
	t.Parallel()
	writer := &appLifecycleWriter{err: errors.New("journal unavailable"), drop: true}
	recorder := diagnostics.NewLifecycleRecorder(writer, "focus-writer", "0.10.0", "tmux")
	finish := recorder.BeginCommand()
	cmd := &focusCommand{diagnostics: recorder, runner: lifecycleTmuxRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, nil
	})}
	if err := cmd.switchClient(context.Background(), "/raw/socket", "/raw/client", "raw-session"); err != nil {
		t.Fatal(err)
	}
	finish(nil)
	if !recorder.RecordedOutcome() {
		t.Fatal("sealed switch lost logical ownership after writer failure")
	}
}
