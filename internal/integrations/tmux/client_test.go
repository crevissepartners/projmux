package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestClientCurrentPanePathTrimsOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("/tmp/projmux\n"), nil
	}))

	path, err := client.CurrentPanePath(context.Background())
	if err != nil {
		t.Fatalf("CurrentPanePath returned error: %v", err)
	}
	if path != "/tmp/projmux" {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestClientCurrentPanePathRejectsEmptyOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	}))

	_, err := client.CurrentPanePath(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errCurrentPanePathUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestClientCurrentPanePathWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.CurrentPanePath(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve current tmux pane path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientCurrentSessionNameTrimsOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("dotfiles\n"), nil
	}))

	sessionName, err := client.CurrentSessionName(context.Background())
	if err != nil {
		t.Fatalf("CurrentSessionName returned error: %v", err)
	}
	if sessionName != "dotfiles" {
		t.Fatalf("unexpected session name %q", sessionName)
	}
}

func TestClientCurrentSessionNameRejectsEmptyOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \n"), nil
	}))

	_, err := client.CurrentSessionName(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errCurrentSessionUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestClientCurrentSessionNameWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.CurrentSessionName(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "resolve current tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsSortsByActivityDescending(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("10\tstale\n35\tfresh\n35\ttie-kept-order\n"), nil
	}))

	sessions, err := client.RecentSessions(context.Background())
	if err != nil {
		t.Fatalf("RecentSessions returned error: %v", err)
	}

	want := []string{"fresh", "tie-kept-order", "stale"}
	if !reflect.DeepEqual(sessions, want) {
		t.Fatalf("RecentSessions = %#v, want %#v", sessions, want)
	}
}

func TestClientRecentSessionsReturnsEmptyListForNoOutput(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(""), nil
	}))

	sessions, err := client.RecentSessions(context.Background())
	if err != nil {
		t.Fatalf("RecentSessions returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("RecentSessions = %#v, want empty", sessions)
	}
}

func TestClientRecentSessionsWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list recent tmux sessions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsRejectsMalformedRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("missing-tab-row"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "malformed row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRecentSessionsRejectsInvalidActivity(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("oops\tdotfiles"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionActivityInvalid) {
		t.Fatalf("RecentSessions error = %v, want %v", err, errSessionActivityInvalid)
	}
}

func TestClientListEphemeralSessionsParsesRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("ephemeral\t0\t42\t1\nhome\t1\t99\t0\n"), nil
	}))

	got, err := client.ListEphemeralSessions(context.Background())
	if err != nil {
		t.Fatalf("ListEphemeralSessions() error = %v", err)
	}

	if want := []string{"ephemeral", "home"}; !reflect.DeepEqual([]string{got[0].Name, got[1].Name}, want) {
		t.Fatalf("ListEphemeralSessions() names = %#v, want %#v", []string{got[0].Name, got[1].Name}, want)
	}
	if !got[0].Ephemeral || got[0].Attached {
		t.Fatalf("first session = %#v, want unattached ephemeral", got[0])
	}
	if got[1].Ephemeral || !got[1].Attached {
		t.Fatalf("second session = %#v, want attached non-ephemeral", got[1])
	}
}

func TestClientListEphemeralSessionsRejectsMalformedRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("broken"), nil
	}))

	_, err := client.ListEphemeralSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "malformed row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientListEphemeralSessionsRejectsInvalidFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		row    string
		target error
	}{
		{
			name:   "attached",
			row:    "ephemeral\tyes\t42\t1",
			target: errSessionAttachedInvalid,
		},
		{
			name:   "ephemeral",
			row:    "ephemeral\t0\t42\tyes",
			target: errSessionEphemeralInvalid,
		},
		{
			name:   "activity",
			row:    "ephemeral\t0\toops\t1",
			target: errSessionActivityInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tt.row), nil
			}))

			_, err := client.ListEphemeralSessions(context.Background())
			if !errors.Is(err, tt.target) {
				t.Fatalf("ListEphemeralSessions() error = %v, want %v", err, tt.target)
			}
		})
	}
}

func TestClientRecentSessionsRejectsEmptySessionNames(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("10\t \n"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("RecentSessions error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientListSessionWindowsParsesRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("0\t1\n2\t0\n"), nil
	}))

	windows, err := client.ListSessionWindows(context.Background(), "dotfiles")
	if err != nil {
		t.Fatalf("ListSessionWindows returned error: %v", err)
	}

	want := []Window{
		{Index: 0, Active: true},
		{Index: 2, Active: false},
	}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("ListSessionWindows = %#v, want %#v", windows, want)
	}
}

func TestClientListSessionWindowsRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	_, err := client.ListSessionWindows(context.Background(), " ")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("ListSessionWindows error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientListSessionWindowsRejectsMalformedRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("0"), nil
	}))

	_, err := client.ListSessionWindows(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "malformed row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientListSessionWindowsRejectsInvalidWindowIndex(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("oops\t1"), nil
	}))

	_, err := client.ListSessionWindows(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errWindowIndexInvalid) {
		t.Fatalf("ListSessionWindows error = %v, want %v", err, errWindowIndexInvalid)
	}
}

func TestClientListAllPanesParsesRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("dotfiles\t0\t1\t1\nhome\t2\t0\t0\n"), nil
	}))

	panes, err := client.ListAllPanes(context.Background())
	if err != nil {
		t.Fatalf("ListAllPanes returned error: %v", err)
	}

	want := []Pane{
		{SessionName: "dotfiles", WindowIndex: 0, PaneIndex: 1, Active: true},
		{SessionName: "home", WindowIndex: 2, PaneIndex: 0, Active: false},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListAllPanes = %#v, want %#v", panes, want)
	}
}

func TestClientListAllPanesRejectsEmptySessionNames(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \t0\t1\t1"), nil
	}))

	_, err := client.ListAllPanes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("ListAllPanes error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientListAllPanesRejectsInvalidPaneIndex(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("dotfiles\t0\toops\t1"), nil
	}))

	_, err := client.ListAllPanes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errPaneIndexInvalid) {
		t.Fatalf("ListAllPanes error = %v, want %v", err, errPaneIndexInvalid)
	}
}

func TestClientListAllPanesRejectsInvalidActiveFlag(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("dotfiles\t0\t1\tmaybe"), nil
	}))

	_, err := client.ListAllPanes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errActiveFlagInvalid) {
		t.Fatalf("ListAllPanes error = %v, want %v", err, errActiveFlagInvalid)
	}
}

func TestClientListWindowPanesParsesRows(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{output: []byte("0\t1\n3\t0\n")}},
	}
	client := NewClient(runner)

	panes, err := client.ListWindowPanes(context.Background(), "dotfiles", 2)
	if err != nil {
		t.Fatalf("ListWindowPanes returned error: %v", err)
	}

	want := []WindowPane{
		{Index: 0, Active: true},
		{Index: 3, Active: false},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListWindowPanes = %#v, want %#v", panes, want)
	}

	wantCalls := []commandCall{
		{name: "tmux", args: []string{"list-panes", "-t", "dotfiles:2", "-F", "#{pane_index}\t#{?pane_active,1,0}"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientListWindowPanesRejectsInvalidWindowIndexArgument(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	_, err := client.ListWindowPanes(context.Background(), "dotfiles", -1)
	if !errors.Is(err, errWindowIndexInvalid) {
		t.Fatalf("ListWindowPanes error = %v, want %v", err, errWindowIndexInvalid)
	}
}

func TestClientListWindowPanesRejectsInvalidPaneIndex(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("oops\t1"), nil
	}))

	_, err := client.ListWindowPanes(context.Background(), "dotfiles", 2)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errPaneIndexInvalid) {
		t.Fatalf("ListWindowPanes error = %v, want %v", err, errPaneIndexInvalid)
	}
}

func TestClientEnsureSessionCreatesMissingSession(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{err: exitError(t, 1)},
			{},
		},
	}
	client := NewClient(runner)

	if err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "dotfiles"}},
		{name: "tmux", args: []string{"new-session", "-d", "-s", "dotfiles", "-c", "/tmp/projmux"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientEnsureSessionSkipsCreateWhenSessionExists(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientEnsureSessionWrapsLookupError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.EnsureSession(context.Background(), "dotfiles", "/tmp/projmux")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "check tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientSessionExistsReturnsTrueWhenSessionExists(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	exists, err := client.SessionExists(context.Background(), "dotfiles")
	if err != nil {
		t.Fatalf("SessionExists returned error: %v", err)
	}
	if !exists {
		t.Fatal("SessionExists = false, want true")
	}
}

func TestClientSessionExistsReturnsFalseWhenSessionIsMissing(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{err: exitError(t, 1)}},
	}
	client := NewClient(runner)

	exists, err := client.SessionExists(context.Background(), "dotfiles")
	if err != nil {
		t.Fatalf("SessionExists returned error: %v", err)
	}
	if exists {
		t.Fatal("SessionExists = true, want false")
	}
}

func TestClientOpenSessionSwitchesInsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "/tmp/tmux-sock" })

	if err := client.OpenSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionAttachesOutsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "" })

	if err := client.OpenSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"attach-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.OpenSession(context.Background(), "  ")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("OpenSession error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientSwitchClientRunsTmuxSwitch(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.SwitchClient(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("SwitchClient returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientSwitchClientRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.SwitchClient(context.Background(), "")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("SwitchClient error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientSwitchClientWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.SwitchClient(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "switch tmux client") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientKillSessionRunsTmuxKill(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.KillSession(context.Background(), "dotfiles"); err != nil {
		t.Fatalf("KillSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"kill-session", "-t", "dotfiles"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientKillSessionRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.KillSession(context.Background(), "  ")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("KillSession error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientKillSessionWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.KillSession(context.Background(), "dotfiles")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kill tmux session") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientDisplayPopupRunsTmuxDisplayPopup(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	if err := client.DisplayPopup(context.Background(), "exec 'projmux' 'session-popup' 'preview' 'dev'"); err != nil {
		t.Fatalf("DisplayPopup returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"display-popup", "-E", "-w", "80%", "-h", "80%", "exec 'projmux' 'session-popup' 'preview' 'dev'"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestBuildDisplayPopupArgsAppliesDefaults(t *testing.T) {
	t.Parallel()

	args, err := BuildDisplayPopupArgs("printf hello", PopupOptions{})
	if err != nil {
		t.Fatalf("BuildDisplayPopupArgs returned error: %v", err)
	}

	want := []string{"display-popup", "-E", "-w", "80%", "-h", "80%", "printf hello"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildDisplayPopupArgs = %#v, want %#v", args, want)
	}
}

func TestBuildDisplayPopupArgsMapsExplicitOptions(t *testing.T) {
	t.Parallel()

	args, err := BuildDisplayPopupArgs("printf hello", PopupOptions{
		Width:         "70%",
		Height:        "20",
		Title:         "proj popup",
		CloseBehavior: PopupKeepOpen,
	})
	if err != nil {
		t.Fatalf("BuildDisplayPopupArgs returned error: %v", err)
	}

	want := []string{"display-popup", "-w", "70%", "-h", "20", "-T", "proj popup", "printf hello"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildDisplayPopupArgs = %#v, want %#v", args, want)
	}
}

func TestBuildDisplayPopupArgsRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		options PopupOptions
		want    error
	}{
		{name: "missing command", command: " ", want: errPopupCommandRequired},
		{name: "invalid close behavior", command: "printf hi", options: PopupOptions{CloseBehavior: PopupCloseBehavior("later")}, want: errPopupCloseBehaviorInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildDisplayPopupArgs(tt.command, tt.options)
			if !errors.Is(err, tt.want) {
				t.Fatalf("BuildDisplayPopupArgs error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestClientDisplayPopupWithOptionsRunsTmuxDisplayPopup(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := NewClient(runner)

	err := client.DisplayPopupWithOptions(context.Background(), "printf hello", PopupOptions{
		Width:         "60%",
		Height:        "18",
		Title:         "proj popup",
		CloseBehavior: PopupKeepOpen,
	})
	if err != nil {
		t.Fatalf("DisplayPopupWithOptions returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"display-popup", "-w", "60%", "-h", "18", "-T", "proj popup", "printf hello"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientDisplayPopupRequiresCommand(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.DisplayPopup(context.Background(), "  ")
	if !errors.Is(err, errPopupCommandRequired) {
		t.Fatalf("DisplayPopup error = %v, want %v", err, errPopupCommandRequired)
	}
}

func TestClientDisplayPopupWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.DisplayPopup(context.Background(), "exec 'projmux' 'session-popup' 'preview' 'dev'")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "display tmux popup") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientOpenSessionTargetSwitchesToPaneInsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "/tmp/tmux-sock" })

	if err := client.OpenSessionTarget(context.Background(), "dotfiles", "3", "8"); err != nil {
		t.Fatalf("OpenSessionTarget returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "dotfiles:3.8"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionTargetAttachesToWindowOutsideTmux(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{}},
	}
	client := newClientWithEnv(runner, func(string) string { return "" })

	if err := client.OpenSessionTarget(context.Background(), "dotfiles", "3", "8"); err != nil {
		t.Fatalf("OpenSessionTarget returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"attach-session", "-t", "dotfiles:3"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientOpenSessionTargetRequiresSessionName(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.OpenSessionTarget(context.Background(), "", "3", "8")
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("OpenSessionTarget error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientOpenSessionTargetRejectsPaneWithoutWindow(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.OpenSessionTarget(context.Background(), "dotfiles", "", "8")
	if !errors.Is(err, errWindowIndexRequired) {
		t.Fatalf("OpenSessionTarget error = %v, want %v", err, errWindowIndexRequired)
	}
}

func TestClientOpenSessionTargetWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := newClientWithEnv(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}), func(string) string { return "/tmp/tmux-sock" })

	err := client.OpenSessionTarget(context.Background(), "dotfiles", "3", "8")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "switch tmux target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPopupPreviewCommandQuotesBinaryPathAndSession(t *testing.T) {
	t.Parallel()

	command, err := BuildPopupPreviewCommand("/tmp/projmux's bin", "team's/dev")
	if err != nil {
		t.Fatalf("BuildPopupPreviewCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'session-popup' 'preview' 'team'\\''s/dev'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildPopupPreviewCommandRequiresInputs(t *testing.T) {
	t.Parallel()

	if _, err := BuildPopupPreviewCommand(" ", "dev"); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}

	if _, err := BuildPopupPreviewCommand("/tmp/projmux", " "); !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("unexpected error for session name: %v", err)
	}
}

func TestBuildPopupSwitchCommandQuotesBinaryPathAndWorkingDirectory(t *testing.T) {
	t.Parallel()

	command, err := BuildPopupSwitchCommand("/tmp/projmux's bin", "/tmp/work tree")
	if err != nil {
		t.Fatalf("BuildPopupSwitchCommand returned error: %v", err)
	}

	const want = "cd -- '/tmp/work tree' && exec '/tmp/projmux'\\''s bin' 'switch' '--ui=popup'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildPopupSwitchCommandRequiresInputs(t *testing.T) {
	t.Parallel()

	if _, err := BuildPopupSwitchCommand(" ", "/tmp/work"); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}

	if _, err := BuildPopupSwitchCommand("/tmp/projmux", " "); err == nil || !strings.Contains(err.Error(), "working directory is required") {
		t.Fatalf("unexpected error for working directory: %v", err)
	}
}

func TestClientEnsureSessionRequiresCWD(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.EnsureSession(context.Background(), "dotfiles", "")
	if !errors.Is(err, errSessionCWDRequired) {
		t.Fatalf("EnsureSession error = %v, want %v", err, errSessionCWDRequired)
	}
}

type staticRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func (fn staticRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return fn(ctx, name, args...)
}

type commandCall struct {
	name string
	args []string
}

type scriptedStep struct {
	output []byte
	err    error
}

type scriptedRunner struct {
	t     *testing.T
	steps []scriptedStep
	calls []commandCall
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if len(r.steps) == 0 {
		r.t.Fatalf("unexpected command %s %v", name, args)
	}

	step := r.steps[0]
	r.steps = r.steps[1:]
	return step.output, step.err
}

func exitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("expected exit error for status %d", code)
	}

	return err
}
