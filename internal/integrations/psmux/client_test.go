package psmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	outputs map[string]string
	errors  map[string]error
	calls   []recordedCall
}

type recordedCall struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := recordedCall{name: name, args: append([]string(nil), args...)}
	r.calls = append(r.calls, call)
	key := callKey(call)
	if err, ok := r.errors[key]; ok {
		return nil, err
	}
	if out, ok := r.outputs[key]; ok {
		return []byte(out), nil
	}
	return nil, nil
}

func TestClientEnsureSessionCreatesDetachedSessionOnAppSocket(t *testing.T) {
	runner := &recordingRunner{
		errors: map[string]error{
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "has-session", "-t", "=workspace"}}): errors.New("can't find session: workspace"),
		},
	}
	client := NewClient(runner, WithSocketName("projmux"))

	if err := client.EnsureSession(context.Background(), "workspace", `C:\repo`); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}

	want := []recordedCall{
		{name: "psmux", args: []string{"-L", "projmux", "has-session", "-t", "=workspace"}},
		{name: "psmux", args: []string{"-L", "projmux", "new-session", "-d", "-s", "workspace", "-c", `C:\repo`}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestClientEnsureSessionDoesNotCreateExistingSession(t *testing.T) {
	runner := &recordingRunner{}
	client := NewClient(runner, WithSocketName("projmux"))

	if err := client.EnsureSession(context.Background(), "workspace", `C:\repo`); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}

	want := []recordedCall{
		{name: "psmux", args: []string{"-L", "projmux", "has-session", "-t", "=workspace"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestClientOpenSessionAttachesOutsideAndSwitchesInside(t *testing.T) {
	outsideRunner := &recordingRunner{}
	outside := NewClient(outsideRunner, WithSocketName("projmux"), WithEnv(func(string) string { return "" }))
	if err := outside.OpenSession(context.Background(), "workspace"); err != nil {
		t.Fatalf("outside OpenSession() error = %v", err)
	}

	insideRunner := &recordingRunner{}
	inside := NewClient(insideRunner, WithSocketName("projmux"), WithEnv(func(name string) string {
		if name == "TMUX" {
			return `C:\Users\Ada\AppData\Local\Temp\psmux,1,0`
		}
		return ""
	}))
	if err := inside.OpenSessionTarget(context.Background(), "workspace", "1", "2"); err != nil {
		t.Fatalf("inside OpenSessionTarget() error = %v", err)
	}

	wantOutside := []recordedCall{{name: "psmux", args: []string{"-L", "projmux", "attach-session", "-t", "=workspace"}}}
	if !reflect.DeepEqual(outsideRunner.calls, wantOutside) {
		t.Fatalf("outside calls = %#v, want %#v", outsideRunner.calls, wantOutside)
	}
	wantInside := []recordedCall{{name: "psmux", args: []string{"-L", "projmux", "switch-client", "-t", "=workspace:1.2"}}}
	if !reflect.DeepEqual(insideRunner.calls, wantInside) {
		t.Fatalf("inside calls = %#v, want %#v", insideRunner.calls, wantInside)
	}
}

func TestClientCreateEphemeralSessionMarksSessionOption(t *testing.T) {
	runner := &recordingRunner{
		errors: map[string]error{
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "has-session", "-t", "=scratch"}}): errors.New("can't find session: scratch"),
		},
	}
	client := NewClient(runner, WithSocketName("projmux"))

	if err := client.CreateEphemeralSession(context.Background(), "scratch", `C:\repo`); err != nil {
		t.Fatalf("CreateEphemeralSession() error = %v", err)
	}

	want := []recordedCall{
		{name: "psmux", args: []string{"-L", "projmux", "has-session", "-t", "=scratch"}},
		{name: "psmux", args: []string{"-L", "projmux", "new-session", "-d", "-s", "scratch", "-c", `C:\repo`}},
		{name: "psmux", args: []string{"-L", "projmux", "set-option", "-t", "scratch", "-q", "@projmux_ephemeral", "1"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestClientParsesMinimalInventory(t *testing.T) {
	runner := &recordingRunner{
		outputs: map[string]string{
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "list-sessions", "-F", "#{session_activity}" + fieldSep + "#{session_name}" + fieldSep + "#{session_attached}" + fieldSep + "#{session_windows}"}}):                                                                                                                                                                   "20\x1fwork\x1f1\x1f2\n10\x1fhome\x1f0\x1f1\n",
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "list-windows", "-t", "work", "-F", "#{window_index}" + fieldSep + "#{?window_active,1,0}" + fieldSep + "#{window_name}" + fieldSep + "#{window_panes}" + fieldSep + "#{pane_current_path}"}}):                                                                                                                        "0\x1f1\x1feditor\x1f2\x1fC:\\repo\n",
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "list-panes", "-a", "-F", "#{session_name}" + fieldSep + "#{pane_id}" + fieldSep + "#{window_index}" + fieldSep + "#{pane_index}" + fieldSep + "#{?pane_active,1,0}" + fieldSep + "#{pane_title}" + fieldSep + "#{@projmux_pane_label}" + fieldSep + "#{pane_current_command}" + fieldSep + "#{pane_current_path}"}}): "work\x1f%1\x1f0\x1f0\x1f1\x1fPowerShell\x1fuser shell\x1fpwsh\x1fC:\\repo\nwork\x1f%2\x1f0\x1f1\x1f0\x1fCodex\x1f\x1fcodex\x1fC:\\repo\n",
		},
	}
	client := NewClient(runner, WithSocketName("projmux"))

	sessions, err := client.RecentSessions(context.Background())
	if err != nil {
		t.Fatalf("RecentSessions() error = %v", err)
	}
	if want := []string{"work", "home"}; !reflect.DeepEqual(sessions, want) {
		t.Fatalf("RecentSessions() = %#v, want %#v", sessions, want)
	}

	summaries, err := client.RecentSessionSummaries(context.Background())
	if err != nil {
		t.Fatalf("RecentSessionSummaries() error = %v", err)
	}
	if len(summaries) != 2 || summaries[0].Name != "work" || summaries[0].PaneCount != 2 || summaries[0].Path != `C:\repo` {
		t.Fatalf("RecentSessionSummaries() = %#v, want work summary with panes/path", summaries)
	}

	windows, err := client.ListSessionWindows(context.Background(), "work")
	if err != nil {
		t.Fatalf("ListSessionWindows() error = %v", err)
	}
	if len(windows) != 1 || windows[0].Name != "editor" || windows[0].PaneCount != 2 || !windows[0].Active {
		t.Fatalf("ListSessionWindows() = %#v, want parsed window", windows)
	}

	panes, err := client.ListAllPanes(context.Background())
	if err != nil {
		t.Fatalf("ListAllPanes() error = %v", err)
	}
	if len(panes) != 2 || panes[0].ID != "%1" || panes[0].Label != "user shell" || panes[0].Command != "pwsh" || panes[1].Title != "Codex" {
		t.Fatalf("ListAllPanes() = %#v, want minimal pane inventory", panes)
	}
	if panes[0].AIState != "" || panes[0].AttentionState != "" {
		t.Fatalf("psmux pane metadata should be degraded/empty, got %#v", panes[0])
	}
}

func TestExecRunnerTreatsSocketPrefixedAttachAsInteractive(t *testing.T) {
	if !psmuxInteractiveCommand([]string{"-L", "projmux", "attach-session", "-t", "=home"}) {
		t.Fatal("attach-session with socket prefix should be interactive")
	}
	if !psmuxInteractiveCommand([]string{"-L", "projmux", "switch-client", "-t", "=home"}) {
		t.Fatal("switch-client with socket prefix should be interactive")
	}
	if psmuxInteractiveCommand([]string{"-L", "projmux", "list-sessions"}) {
		t.Fatal("list-sessions should not be interactive")
	}
}

func TestClientExistingSessionsReturnsSessionNameSet(t *testing.T) {
	runner := &recordingRunner{
		outputs: map[string]string{
			callKey(recordedCall{name: "psmux", args: []string{"-L", "projmux", "list-sessions", "-F", "#{session_name}"}}): "home\nwork\n",
		},
	}
	client := NewClient(runner, WithSocketName("projmux"))

	sessions, err := client.ExistingSessions(context.Background())
	if err != nil {
		t.Fatalf("ExistingSessions() error = %v", err)
	}
	if got, want := sessions, map[string]bool{"home": true, "work": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExistingSessions() = %#v, want %#v", got, want)
	}
	wantCalls := []recordedCall{{name: "psmux", args: []string{"-L", "projmux", "list-sessions", "-F", "#{session_name}"}}}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func callKey(call recordedCall) string {
	return strings.Join(append([]string{call.name}, call.args...), "\x00")
}
