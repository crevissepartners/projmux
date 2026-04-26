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
		return []byte("workspace\n"), nil
	}))

	sessionName, err := client.CurrentSessionName(context.Background())
	if err != nil {
		t.Fatalf("CurrentSessionName returned error: %v", err)
	}
	if sessionName != "workspace" {
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
		return []byte("10\tstale\t0\t1\n35\tfresh\t1\t3\n35\ttie-kept-order\t0\t2\n"), nil
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
		return []byte("oops\tworkspace\t1\t2"), nil
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

func TestClientListEphemeralSessionsTreatsMultipleAttachedClientsAsAttached(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("shared\t2\t42\t1\n"), nil
	}))

	got, err := client.ListEphemeralSessions(context.Background())
	if err != nil {
		t.Fatalf("ListEphemeralSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEphemeralSessions() len = %d, want 1", len(got))
	}
	if !got[0].Attached {
		t.Fatalf("session = %#v, want attached when client count is greater than zero", got[0])
	}
}

func TestClientListEphemeralSessionsTreatsMissingEphemeralFlagAsFalse(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("home\t0\t42\t\n"), nil
	}))

	got, err := client.ListEphemeralSessions(context.Background())
	if err != nil {
		t.Fatalf("ListEphemeralSessions() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEphemeralSessions() len = %d, want 1", len(got))
	}
	if got[0].Ephemeral {
		t.Fatalf("session = %#v, want non-ephemeral when flag is unset", got[0])
	}
}

func TestClientListEphemeralSessionsTreatsNoServerAsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "no server running",
			err:  errors.New("tmux list-sessions: exit status 1: no server running on /tmp/tmux-1000/default"),
		},
		{
			name: "socket missing",
			err:  errors.New("tmux list-sessions: exit status 1: error connecting to /tmp/tmux-1000/default (No such file or directory)"),
		},
		{
			name: "generic failed to connect",
			err:  errors.New("tmux list-sessions: exit status 1: failed to connect to server"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
				return nil, tt.err
			}))

			got, err := client.ListEphemeralSessions(context.Background())
			if err != nil {
				t.Fatalf("ListEphemeralSessions() error = %v", err)
			}
			if got != nil {
				t.Fatalf("ListEphemeralSessions() = %#v, want nil", got)
			}
		})
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
			row:    "ephemeral\tnope\t42\t1",
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
		return []byte("10\t \t1\t2\n"), nil
	}))

	_, err := client.RecentSessions(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errSessionNameRequired) {
		t.Fatalf("RecentSessions error = %v, want %v", err, errSessionNameRequired)
	}
}

func TestClientRecentSessionSummariesIncludeAttachedPaneCountAndPath(t *testing.T) {
	t.Parallel()

	call := 0
	client := NewClient(staticRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		call++
		switch call {
		case 1:
			if got, want := args, []string{"list-sessions", "-F", "#{session_activity}\t#{session_name}\t#{session_attached}\t#{session_windows}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("list-sessions args = %#v, want %#v", got, want)
			}
			return []byte("10\tstale\t0\t1\n35\tfresh\t1\t3\n"), nil
		case 2:
			if got, want := args, []string{"list-panes", "-a", "-F", "#{session_name}\t#{pane_id}\t#{window_index}\t#{pane_index}\t#{?pane_active,1,0}\t#{pane_title}\t#{@projmux_attention_state}\t#{@projmux_ai_state}\t#{@projmux_ai_agent}\t#{@projmux_ai_topic}\t#{@projmux_attention_ack}\t#{@projmux_attention_focus_armed}\t#{pane_current_command}\t#{pane_current_path}"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("list-panes args = %#v, want %#v", got, want)
			}
			return []byte(
				"fresh\t%1\t0\t0\t0\tshell\t\t\t\t\t\t\tzsh\t/tmp/fresh-first\n" +
					"fresh\t%2\t0\t1\t1\teditor\t\t\t\t\t\t\tnvim\t/tmp/fresh-active\n" +
					"stale\t%3\t0\t0\t0\tshell\t\t\t\t\t\t\tzsh\t/tmp/stale\n",
			), nil
		default:
			t.Fatalf("unexpected call %d", call)
			return nil, nil
		}
	}))

	summaries, err := client.RecentSessionSummaries(context.Background())
	if err != nil {
		t.Fatalf("RecentSessionSummaries returned error: %v", err)
	}

	want := []RecentSessionSummary{
		{Name: "fresh", Attached: true, WindowCount: 3, PaneCount: 2, Path: "/tmp/fresh-active", Activity: 35},
		{Name: "stale", Attached: false, WindowCount: 1, PaneCount: 1, Path: "/tmp/stale", Activity: 10},
	}
	if !reflect.DeepEqual(summaries, want) {
		t.Fatalf("RecentSessionSummaries = %#v, want %#v", summaries, want)
	}
}

func TestClientRecentSessionSummariesPropagatePaneListingErrors(t *testing.T) {
	t.Parallel()

	call := 0
	client := NewClient(staticRunner(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		call++
		if call == 1 {
			return []byte("35\tfresh\t1\t3\n"), nil
		}
		return nil, errors.New("tmux failed")
	}))

	_, err := client.RecentSessionSummaries(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list tmux panes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientListSessionWindowsParsesRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("0\t1\tshell\t1\t/home/tester\n2\t0\tdev\t2\t/home/tester/source/repos/dev\n"), nil
	}))

	windows, err := client.ListSessionWindows(context.Background(), "workspace")
	if err != nil {
		t.Fatalf("ListSessionWindows returned error: %v", err)
	}

	want := []Window{
		{Index: 0, Name: "shell", PaneCount: 1, Path: "/home/tester", Active: true},
		{Index: 2, Name: "dev", PaneCount: 2, Path: "/home/tester/source/repos/dev", Active: false},
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

	_, err := client.ListSessionWindows(context.Background(), "workspace")
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
		return []byte("oops\t1\tshell\t1\t/home/tester"), nil
	}))

	_, err := client.ListSessionWindows(context.Background(), "workspace")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errWindowIndexInvalid) {
		t.Fatalf("ListSessionWindows error = %v, want %v", err, errWindowIndexInvalid)
	}
}

func TestClientListSessionWindowsRejectsInvalidWindowPaneCount(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("0\t1\tshell\toops\t/home/tester"), nil
	}))

	_, err := client.ListSessionWindows(context.Background(), "workspace")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errWindowPaneCountInvalid) {
		t.Fatalf("ListSessionWindows error = %v, want %v", err, errWindowPaneCountInvalid)
	}
}

func TestClientListAllPanesParsesRows(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("workspace\t%1\t0\t1\t1\tserver\tbusy\tthinking\tcodex\tapproval needed\t\t1\tgo\t/home/tester/source/repos/workspace\nhome\t%2\t2\t0\t0\tshell\t\t\t\t\t\t\tzsh\t/home/tester\n"), nil
	}))

	panes, err := client.ListAllPanes(context.Background())
	if err != nil {
		t.Fatalf("ListAllPanes returned error: %v", err)
	}

	want := []Pane{
		{ID: "%1", SessionName: "workspace", WindowIndex: 0, PaneIndex: 1, Title: "server", AttentionState: "busy", AIState: "thinking", AIAgent: "codex", AITopic: "approval needed", AttentionFocusArmed: "1", Command: "go", Path: "/home/tester/source/repos/workspace", Active: true},
		{ID: "%2", SessionName: "home", WindowIndex: 2, PaneIndex: 0, Title: "shell", Command: "zsh", Path: "/home/tester", Active: false},
	}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("ListAllPanes = %#v, want %#v", panes, want)
	}
}

func TestClientListAllPanesRejectsEmptySessionNames(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(" \t%1\t0\t1\t1\tshell\tzsh\t/home/tester"), nil
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
		return []byte("workspace\t%1\t0\toops\t1\tserver\tgo\t/repo"), nil
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
		return []byte("workspace\t%1\t0\t1\tmaybe\tserver\tgo\t/repo"), nil
	}))

	_, err := client.ListAllPanes(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errActiveFlagInvalid) {
		t.Fatalf("ListAllPanes error = %v, want %v", err, errActiveFlagInvalid)
	}
}

func TestClientCapturePaneUsesPaneTargetAndStartLine(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if got, want := args, []string{"capture-pane", "-p", "-t", "%8", "-S", "-80"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("capture-pane args = %#v, want %#v", got, want)
		}
		return []byte("line one\nline two\n"), nil
	}))

	got, err := client.CapturePane(context.Background(), "%8", -80)
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if want := "line one\nline two"; got != want {
		t.Fatalf("CapturePane() = %q, want %q", got, want)
	}
}

func TestClientListWindowPanesParsesRows(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t:     t,
		steps: []scriptedStep{{output: []byte("0\t1\n3\t0\n")}},
	}
	client := NewClient(runner)

	panes, err := client.ListWindowPanes(context.Background(), "workspace", 2)
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
		{name: "tmux", args: []string{"list-panes", "-t", "workspace:2", "-F", "#{pane_index}\t#{?pane_active,1,0}"}},
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

	_, err := client.ListWindowPanes(context.Background(), "workspace", -1)
	if !errors.Is(err, errWindowIndexInvalid) {
		t.Fatalf("ListWindowPanes error = %v, want %v", err, errWindowIndexInvalid)
	}
}

func TestClientListWindowPanesRejectsInvalidPaneIndex(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("oops\t1"), nil
	}))

	_, err := client.ListWindowPanes(context.Background(), "workspace", 2)
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

	if err := client.EnsureSession(context.Background(), "workspace", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "workspace"}},
		{name: "tmux", args: []string{"new-session", "-d", "-s", "workspace", "-c", "/tmp/projmux"}},
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

	if err := client.EnsureSession(context.Background(), "workspace", "/tmp/projmux"); err != nil {
		t.Fatalf("EnsureSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"has-session", "-t", "workspace"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestClientCreateEphemeralSessionCreatesAndMarksSession(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{},
			{},
		},
	}
	client := NewClient(runner)

	if err := client.CreateEphemeralSession(context.Background(), "scratch-20260423-123456", "/tmp/projmux"); err != nil {
		t.Fatalf("CreateEphemeralSession() error = %v", err)
	}

	wantCalls := []commandCall{
		{name: "tmux", args: []string{"new-session", "-d", "-s", "scratch-20260423-123456", "-c", "/tmp/projmux"}},
		{name: "tmux", args: []string{"set-option", "-t", "scratch-20260423-123456", "-q", "@projmux_ephemeral", "1"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("CreateEphemeralSession() calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestClientCreateEphemeralSessionIgnoresMarkerFailure(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		t: t,
		steps: []scriptedStep{
			{},
			{err: errors.New("set-option failed")},
		},
	}
	client := NewClient(runner)

	if err := client.CreateEphemeralSession(context.Background(), "scratch", "/tmp/projmux"); err != nil {
		t.Fatalf("CreateEphemeralSession() error = %v", err)
	}
}

func TestClientEnsureSessionWrapsLookupError(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}))

	err := client.EnsureSession(context.Background(), "workspace", "/tmp/projmux")
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

	exists, err := client.SessionExists(context.Background(), "workspace")
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

	exists, err := client.SessionExists(context.Background(), "workspace")
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

	if err := client.OpenSession(context.Background(), "workspace"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "workspace"}},
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

	if err := client.OpenSession(context.Background(), "workspace"); err != nil {
		t.Fatalf("OpenSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"attach-session", "-t", "workspace"}},
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

	if err := client.SwitchClient(context.Background(), "workspace"); err != nil {
		t.Fatalf("SwitchClient returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "workspace"}},
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

	err := client.SwitchClient(context.Background(), "workspace")
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

	if err := client.KillSession(context.Background(), "workspace"); err != nil {
		t.Fatalf("KillSession returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"kill-session", "-t", "workspace"}},
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

	err := client.KillSession(context.Background(), "workspace")
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

func TestBuildDisplayPopupArgsMapsStandalonePopupOptions(t *testing.T) {
	t.Parallel()

	args, err := BuildDisplayPopupArgs("printf hello", PopupOptions{
		Target: "%1",
		Cwd:    "/tmp/work",
		Env: map[string]string{
			"TMUX_SPLIT_TARGET_PANE": "%1",
			"TMUX_SPLIT_CONTEXT_DIR": "/tmp/work",
		},
		X:             "0",
		Y:             "0",
		Width:         "40",
		Height:        "20",
		Title:         "proj popup",
		CloseBehavior: PopupCloseOnExit,
	})
	if err != nil {
		t.Fatalf("BuildDisplayPopupArgs returned error: %v", err)
	}

	want := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-d", "/tmp/work",
		"-e", "TMUX_SPLIT_CONTEXT_DIR=/tmp/work",
		"-e", "TMUX_SPLIT_TARGET_PANE=%1",
		"-x", "0",
		"-y", "0",
		"-w", "40",
		"-h", "20",
		"-T", "proj popup",
		"printf hello",
	}
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

	if err := client.OpenSessionTarget(context.Background(), "workspace", "3", "8"); err != nil {
		t.Fatalf("OpenSessionTarget returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"switch-client", "-t", "workspace:3.8"}},
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

	if err := client.OpenSessionTarget(context.Background(), "workspace", "3", "8"); err != nil {
		t.Fatalf("OpenSessionTarget returned error: %v", err)
	}

	want := []commandCall{
		{name: "tmux", args: []string{"attach-session", "-t", "workspace:3"}},
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

	err := client.OpenSessionTarget(context.Background(), "workspace", "", "8")
	if !errors.Is(err, errWindowIndexRequired) {
		t.Fatalf("OpenSessionTarget error = %v, want %v", err, errWindowIndexRequired)
	}
}

func TestClientOpenSessionTargetWrapsRunnerError(t *testing.T) {
	t.Parallel()

	client := newClientWithEnv(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("tmux failed")
	}), func(string) string { return "/tmp/tmux-sock" })

	err := client.OpenSessionTarget(context.Background(), "workspace", "3", "8")
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

func TestBuildPopupSessionsCommandQuotesBinaryPath(t *testing.T) {
	t.Parallel()

	command, err := BuildPopupSessionsCommand("/tmp/projmux's bin")
	if err != nil {
		t.Fatalf("BuildPopupSessionsCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'sessions' '--ui=popup'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildPopupSessionsCommandRequiresBinaryPath(t *testing.T) {
	t.Parallel()

	if _, err := BuildPopupSessionsCommand(" "); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}
}

func TestBuildSessionPopupPreviewCommandQuotesBinaryPath(t *testing.T) {
	t.Parallel()

	command, err := BuildSessionPopupPreviewCommand("/tmp/projmux's bin")
	if err != nil {
		t.Fatalf("BuildSessionPopupPreviewCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'session-popup' 'preview' {2}"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildSessionPopupPreviewCommandRequiresBinaryPath(t *testing.T) {
	t.Parallel()

	if _, err := BuildSessionPopupPreviewCommand(" "); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}
}

func TestBuildSessionPopupCycleCommandQuotesInputs(t *testing.T) {
	t.Parallel()

	command, err := BuildSessionPopupCycleCommand("/tmp/projmux's bin", "cycle-window", "next")
	if err != nil {
		t.Fatalf("BuildSessionPopupCycleCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'session-popup' 'cycle-window' {2} 'next'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildSessionPopupCycleCommandRequiresInputs(t *testing.T) {
	t.Parallel()

	if _, err := BuildSessionPopupCycleCommand(" ", "cycle-window", "next"); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}
	if _, err := BuildSessionPopupCycleCommand("/tmp/projmux", " ", "next"); err == nil || !strings.Contains(err.Error(), "subcommand is required") {
		t.Fatalf("unexpected error for subcommand: %v", err)
	}
	if _, err := BuildSessionPopupCycleCommand("/tmp/projmux", "cycle-window", " "); err == nil || !strings.Contains(err.Error(), "direction is required") {
		t.Fatalf("unexpected error for direction: %v", err)
	}
}

func TestBuildSwitchPreviewCommandQuotesBinaryPath(t *testing.T) {
	t.Parallel()

	command, err := BuildSwitchPreviewCommand("/tmp/projmux's bin", "sidebar")
	if err != nil {
		t.Fatalf("BuildSwitchPreviewCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'switch' 'preview' '--ui=sidebar' {2}"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildSwitchPreviewCommandRequiresBinaryPath(t *testing.T) {
	t.Parallel()

	if _, err := BuildSwitchPreviewCommand(" ", "popup"); err == nil || !strings.Contains(err.Error(), "binary path is required") {
		t.Fatalf("unexpected error for binary path: %v", err)
	}
}

func TestBuildSwitchCycleWindowCommandQuotesInputs(t *testing.T) {
	t.Parallel()

	command, err := BuildSwitchCycleWindowCommand("/tmp/projmux's bin", "prev")
	if err != nil {
		t.Fatalf("BuildSwitchCycleWindowCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'switch' 'cycle-window' {2} 'prev'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildSwitchCyclePaneCommandQuotesInputs(t *testing.T) {
	t.Parallel()

	command, err := BuildSwitchCyclePaneCommand("/tmp/projmux's bin", "next")
	if err != nil {
		t.Fatalf("BuildSwitchCyclePaneCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'switch' 'cycle-pane' {2} 'next'"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestBuildSwitchSidebarFocusCommandQuotesBinaryPath(t *testing.T) {
	t.Parallel()

	command, err := BuildSwitchSidebarFocusCommand("/tmp/projmux's bin")
	if err != nil {
		t.Fatalf("BuildSwitchSidebarFocusCommand returned error: %v", err)
	}

	const want = "exec '/tmp/projmux'\\''s bin' 'switch' 'sidebar-focus' {2}"
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
}

func TestClientEnsureSessionRequiresCWD(t *testing.T) {
	t.Parallel()

	client := NewClient(staticRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("runner should not be called")
		return nil, nil
	}))

	err := client.EnsureSession(context.Background(), "workspace", "")
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
