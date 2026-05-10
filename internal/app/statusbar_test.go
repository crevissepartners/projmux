package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// statusbarFakeRunner records every call so tests can assert on exec args
// without spawning real processes.
type statusbarFakeRunner struct {
	calls []statusbarFakeCall
	// respond is consulted before each call. Returning an error makes that
	// particular invocation appear to fail; nil leaves it as a success.
	respond func(name string, args []string) ([]byte, error)
}

type statusbarFakeCall struct {
	name string
	args []string
}

func (r *statusbarFakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, statusbarFakeCall{name: name, args: append([]string(nil), args...)})
	if r.respond != nil {
		return r.respond(name, args)
	}
	return nil, nil
}

func newStatusbarTestCommand(runner *statusbarFakeRunner, store notifyStore) *statusbarCommand {
	c := &statusbarCommand{
		runner:     runner,
		executable: func() (string, error) { return "/usr/local/bin/projmux", nil },
	}
	if store != nil {
		c.notifyStoreFn = func() (notifyStore, error) { return store, nil }
	}
	return c
}

func TestStatusbarDispatchTableCoversAllKnownRanges(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})
	table := cmd.dispatchTable()

	want := []statusbarRangeID{
		statusbarRangeSession,
		statusbarRangePwd,
		statusbarRangeKube,
		statusbarRangeGit,
		statusbarRangeUsage,
		statusbarRangeNotify,
		statusbarRangeSettings,
	}
	if got := len(table); got != len(want) {
		t.Fatalf("dispatch table size = %d, want %d", got, len(want))
	}
	for _, id := range want {
		if _, ok := table[id]; !ok {
			t.Fatalf("dispatch table missing range %q", id)
		}
	}
}

func TestStatusbarClickUnknownRangeWithoutMouseWindowIsNoop(t *testing.T) {
	t.Parallel()

	// Once we share `MouseDown1Status` with the window-list passthrough, an
	// unknown range id is no longer a user error — it just means the click
	// landed somewhere we don't manage. Returning nil keeps run-shell from
	// flashing a tmux error popup at the user.
	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "totally-bogus"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unknown range without mouse-window should not invoke runner, got %d calls", len(runner.calls))
	}
}

func TestStatusbarClickEmptyRangeIsNoop(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("empty range should not invoke runner, got %d calls", len(runner.calls))
	}
}

func TestStatusbarClickKnownRangeIgnoresMouseWindow(t *testing.T) {
	t.Parallel()

	// When the click lands on a projmux user-defined range, the range
	// handler always wins. The `--mouse-window` passthrough only applies
	// when the click landed outside any range.
	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: nil}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "--mouse-window", "3", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.name != "tmux" || len(call.args) < 1 {
			continue
		}
		if call.args[0] == "select-window" {
			t.Fatalf("known range must not trigger select-window passthrough; calls = %#v", runner.calls)
		}
	}
	if !sawTmuxDisplayMessage(runner.calls, "no notifications") {
		t.Fatalf("notify handler did not run; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickEmptyRangeWithMouseWindowSelectsWindow(t *testing.T) {
	t.Parallel()

	// Default tmux behavior: clicking on a window-list entry switches to
	// that window. Restored here via select-window with the `@` prefix.
	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window", "3", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@3"}) {
		t.Fatalf("missing select-window -t @3; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickEmptyRangeWithPrefixedMouseWindowDoesNotDoublePrefix(t *testing.T) {
	t.Parallel()

	// `#{mouse_window}` is normally numeric (e.g. "3") but if a future
	// tmux ever returns "@5" we must not produce "@@5".
	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window", "@5", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@5"}) {
		t.Fatalf("missing select-window -t @5; calls = %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 3 && call.args[0] == "select-window" && call.args[2] == "@@5" {
			t.Fatalf("select-window target was double-prefixed; calls = %#v", runner.calls)
		}
	}
}

func TestStatusbarClickEmptyRangeWithEmptyMouseWindowIsNoop(t *testing.T) {
	t.Parallel()

	// Click on row-1 whitespace between two ranges: tmux gives us an empty
	// `mouse_status_range` *and* an empty `mouse_window`. We must not call
	// select-window with a bare `@`.
	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window", "", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("empty range + empty mouse-window must be a noop, got %d calls", len(runner.calls))
	}
}

func TestStatusbarClickUnknownRangeWithMouseWindowSelectsWindow(t *testing.T) {
	t.Parallel()

	// Some custom right-hand `range=user|foo` from a third-party plugin
	// could deliver an unfamiliar range id. We still want the click to
	// switch to the window underneath the cursor when one is available.
	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window", "7", "totally-bogus"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@7"}) {
		t.Fatalf("missing select-window -t @7; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickSessionOpensProjectSidebar(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "session"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawProjmuxArgs(runner.calls, []string{"tmux", "popup-toggle", "sessionizer-sidebar"}) {
		t.Fatalf("missing sessionizer-sidebar popup-toggle; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickPwdOpensPathPopupWithoutCopy(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, args []string) ([]byte, error) {
			if name == "tmux" && equalStringSlices(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}) {
				return []byte("/home/es5h/source/repos/projmux\n"), nil
			}
			if name == "git" && equalStringSlices(args, []string{"-C", "/home/es5h/source/repos/projmux", "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD"}) {
				return []byte("/home/es5h/source/repos/projmux\nship/statusbar-cwd-popup-phase2\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "pwd"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxSubcommand(runner.calls, "display-popup") {
		t.Fatalf("missing path display-popup; calls = %#v", runner.calls)
	}
	if !sawTmuxArgsContainInOrder(runner.calls, []string{"display-popup", "-T", "Current path"}) {
		t.Fatalf("missing path popup title; calls = %#v", runner.calls)
	}
	if !sawTmuxPopupCommandContaining(runner.calls, "/home/es5h/source/repos/projmux") {
		t.Fatalf("missing full path in popup; calls = %#v", runner.calls)
	}
	if !sawTmuxPopupCommandContaining(runner.calls, "project") || !sawTmuxPopupCommandContaining(runner.calls, "projmux") {
		t.Fatalf("missing project metadata; calls = %#v", runner.calls)
	}
	if !sawTmuxPopupCommandContaining(runner.calls, "git") || !sawTmuxPopupCommandContaining(runner.calls, "ship/statusbar-cwd-popup-phase2") {
		t.Fatalf("missing git metadata; calls = %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name != "tmux" {
			if call.name == "clip.exe" || call.name == "pbcopy" || call.name == "wl-copy" || call.name == "xclip" || call.name == "xsel" {
				t.Fatalf("pwd click must not invoke system clipboard tool; calls = %#v", runner.calls)
			}
			continue
		}
		if len(call.args) > 0 && (call.args[0] == "set-buffer" || call.args[0] == "load-buffer") {
			t.Fatalf("pwd click must not write tmux buffer; calls = %#v", runner.calls)
		}
	}
	command, ok := firstTmuxPopupCommand(runner.calls)
	if !ok {
		t.Fatalf("missing popup command; calls = %#v", runner.calls)
	}
	if !strings.HasPrefix(command, "printf %s ") {
		t.Fatalf("popup command = %q, want single-payload printf %%s", command)
	}
	if !strings.Contains(command, "; IFS= read -r _") {
		t.Fatalf("popup command = %q, want simple Enter read", command)
	}
	if strings.Contains(command, "printf '%s\\n'") || strings.Contains(command, "read -n1") {
		t.Fatalf("popup command uses brittle output/read shape: %q", command)
	}
}

func TestStatusbarPathMetadataPreservesGitRootWithSpaces(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, args []string) ([]byte, error) {
			if name == "git" && equalStringSlices(args, []string{"-C", "/tmp/work tree/proj", "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD"}) {
				return []byte("/tmp/work tree/proj\nfeature/cwd-popup\n"), nil
			}
			return nil, nil
		},
	}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	metadata := cmd.statusbarPathMetadata(context.Background(), "/tmp/work tree/proj")
	if metadata.Project != "proj" {
		t.Fatalf("Project = %q, want proj", metadata.Project)
	}
	if metadata.Git != "feature/cwd-popup" {
		t.Fatalf("Git = %q, want feature/cwd-popup", metadata.Git)
	}
}

func TestStatusbarClickPwdFallsBackToToastWhenPopupFails(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, args []string) ([]byte, error) {
			switch {
			case name == "tmux" && equalStringSlices(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}):
				return []byte("/tmp/project\n"), nil
			case name == "tmux" && len(args) > 0 && args[0] == "display-popup":
				return nil, errors.New("popup unavailable")
			default:
				return nil, nil
			}
		},
	}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "pwd"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "path: /tmp/project") {
		t.Fatalf("missing fallback path toast; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickKubeOpensProjectSwitcher(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "kube"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawProjmuxArgs(runner.calls, []string{"tmux", "popup-toggle", "sessionizer"}) {
		t.Fatalf("missing project switcher popup-toggle; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickGitOpensProjectSwitcher(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "git"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawProjmuxArgs(runner.calls, []string{"tmux", "popup-toggle", "sessionizer"}) {
		t.Fatalf("missing project switcher popup-toggle; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickSettingsOpensSettingsPopupForClient(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "settings", "--client", "/dev/pts/7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawProjmuxArgs(runner.calls, []string{"tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-settings"}) {
		t.Fatalf("missing settings popup-toggle for client; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickPopupActionFailuresShowToast(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rangeID string
		want    string
	}{
		{name: "session", rangeID: "session", want: "statusbar session: popup failed"},
		{name: "kube", rangeID: "kube", want: "statusbar kube: popup failed"},
		{name: "git", rangeID: "git", want: "statusbar git: popup failed"},
		{name: "settings", rangeID: "settings", want: "statusbar settings: popup failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := &statusbarFakeRunner{
				respond: func(name string, _ []string) ([]byte, error) {
					if name == "/usr/local/bin/projmux" {
						return nil, errors.New("popup unavailable")
					}
					return nil, nil
				},
			}
			cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

			if err := cmd.Run([]string{"click", tt.rangeID}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}
			if !sawTmuxDisplayMessage(runner.calls, tt.want) {
				t.Fatalf("missing fallback display-message %q; calls = %#v", tt.want, runner.calls)
			}
		})
	}
}

func TestStatusbarClickPopupActionBinaryResolutionFailureShowsToast(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})
	cmd.executable = func() (string, error) { return "", errors.New("missing binary") }

	if err := cmd.Run([]string{"click", "session"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "statusbar session: cannot resolve projmux binary") {
		t.Fatalf("missing binary-resolution fallback display-message; calls = %#v", runner.calls)
	}
	if sawProjmuxArgs(runner.calls, []string{"tmux", "popup-toggle", "session-popup"}) {
		t.Fatalf("binary-resolution failure must not invoke popup-toggle; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickUsageOpensPopup(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "usage"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxSubcommand(runner.calls, "display-popup") {
		t.Fatalf("missing display-popup; calls = %#v", runner.calls)
	}
	if !sawTmuxPopupCommandContaining(runner.calls, "Press Enter to close.") {
		t.Fatalf("missing enter-to-close prompt; calls = %#v", runner.calls)
	}
	if sawTmuxPopupCommandContaining(runner.calls, "read -n1 -s") {
		t.Fatalf("usage popup should wait for Enter, not any key; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickNotifyEmptyQueueDisplaysMessage(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: nil}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "no notifications") {
		t.Fatalf("missing 'no notifications' display-message; calls = %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if call.name == "/usr/local/bin/projmux" {
			t.Fatalf("notify with empty queue must not exec focus, got %#v", call)
		}
	}
}

func TestStatusbarClickNotifyExecsFocusForNewestEntry(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:        "abc",
			Text:      "deploy ok",
			Severity:  notify.SeverityWarn,
			Source:    notify.SourceAI,
			Socket:    "projmux",
			Session:   "main",
			Window:    "1",
			Pane:      "0",
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		},
		{
			ID:        "older",
			Text:      "earlier",
			Severity:  notify.SeverityInfo,
			Source:    notify.SourceAI,
			Session:   "side",
			CreatedAt: now.Add(-time.Hour),
			ExpiresAt: now.Add(time.Hour),
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var focusCall *statusbarFakeCall
	for i := range runner.calls {
		if runner.calls[i].name == "/usr/local/bin/projmux" {
			focusCall = &runner.calls[i]
			break
		}
	}
	if focusCall == nil {
		t.Fatalf("expected projmux focus invocation; calls = %#v", runner.calls)
	}
	wantArgs := []string{
		"focus", "--target", "main:1.0", "--source", "status-bar", "--kind", "segment-click",
		"--socket", "projmux",
	}
	if !equalStringSlices(focusCall.args, wantArgs) {
		t.Fatalf("focus args = %#v, want %#v", focusCall.args, wantArgs)
	}
	if store.ackedID != "abc" {
		t.Fatalf("store.ackedID = %q, want abc", store.ackedID)
	}
}

func TestStatusbarClickNotifyAcksEntryAfterSuccessfulFocus(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:      "abc",
			Text:    "deploy ok",
			Source:  notify.SourceAI,
			Socket:  "projmux",
			Session: "main",
			Window:  "1",
			Pane:    "0",
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if store.ackedID != "abc" {
		t.Fatalf("store.ackedID = %q, want abc", store.ackedID)
	}
}

func TestStatusbarClickNotifyTransientFocusFailureSurfacesToastAndKeepsEntry(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, _ []string) ([]byte, error) {
			if name == "/usr/local/bin/projmux" {
				return nil, &fakeExitError{code: 1, msg: "focus boom"}
			}
			return nil, nil
		},
	}
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:      "abc",
			Text:    "deploy ok",
			Source:  notify.SourceAI,
			Socket:  "projmux",
			Session: "main",
			Window:  "1",
			Pane:    "0",
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil (transient failure must not surface as tmux error popup)", err)
	}
	if store.ackedID != "" {
		t.Fatalf("store.ackedID = %q, want empty (transient focus failure must keep entry for retry)", store.ackedID)
	}
	got, ok := lastDisplayMessage(runner.calls)
	if !ok {
		t.Fatalf("missing display-message; calls = %#v", runner.calls)
	}
	if !strings.HasPrefix(got, "focus failed:") {
		t.Fatalf("display-message = %q, want prefix 'focus failed:'", got)
	}
}

func TestStatusbarClickNotifyTargetGoneKeepsEntryAndShowsToast(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, _ []string) ([]byte, error) {
			if name == "/usr/local/bin/projmux" {
				return nil, &fakeExitError{code: focusExitNotResolved, msg: "target unresolved"}
			}
			return nil, nil
		},
	}
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:      "abc",
			Text:    "deploy ok",
			Source:  notify.SourceAI,
			Socket:  "projmux",
			Session: "__nope",
			Window:  "1",
			Pane:    "0",
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil (target-gone must not surface as tmux error popup)", err)
	}
	if store.ackedID != "" {
		t.Fatalf("store.ackedID = %q, want empty (target-gone must keep entry until explicit ack)", store.ackedID)
	}
	if !sawTmuxDisplayMessage(runner.calls, "notify target gone; ack to clear") {
		t.Fatalf("missing 'notify target gone' display-message; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickNotifyUsageErrorTreatedAsTargetGone(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{
		respond: func(name string, _ []string) ([]byte, error) {
			if name == "/usr/local/bin/projmux" {
				// Simulate the focus subprocess exiting with a wrapped
				// app.UsageError. main.go maps UsageError to exit code 2,
				// which we equate with target-unresolved here.
				return nil, &UsageError{Message: "focus --target invalid"}
			}
			return nil, nil
		},
	}
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:      "abc",
			Text:    "deploy ok",
			Source:  notify.SourceAI,
			Session: "main",
			Window:  "1",
			Pane:    "0",
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if store.ackedID != "" {
		t.Fatalf("store.ackedID = %q, want empty (UsageError should keep entry like target-gone)", store.ackedID)
	}
	if !sawTmuxDisplayMessage(runner.calls, "notify target gone; ack to clear") {
		t.Fatalf("missing target-gone display-message; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickNotifyExplicitSocketOverridesEntry(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:      "abc",
			Text:    "x",
			Source:  notify.SourceAI,
			Socket:  "embedded-socket",
			Session: "main",
		},
	}}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "--socket", "explicit-socket", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var focusCall *statusbarFakeCall
	for i := range runner.calls {
		if runner.calls[i].name == "/usr/local/bin/projmux" {
			focusCall = &runner.calls[i]
			break
		}
	}
	if focusCall == nil {
		t.Fatalf("expected projmux focus invocation; calls = %#v", runner.calls)
	}
	if !sliceContainsPair(focusCall.args, "--socket", "explicit-socket") {
		t.Fatalf("focus args = %#v, want --socket explicit-socket", focusCall.args)
	}
	if sliceContainsPair(focusCall.args, "--socket", "embedded-socket") {
		t.Fatalf("focus args = %#v, embedded socket leaked", focusCall.args)
	}
}

func TestStatusbarClickNotifyStoreErrorFallsBackToMessage(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listErr: errors.New("disk full")}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "no notifications") {
		t.Fatalf("missing 'no notifications' display-message; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickWindowRangeWithEmptyMouseWindowIsNoop is the direct
// regression for the parser bug where stdlib `flag.Parse` stopped at the first
// non-flag token. With args `["window", "--mouse-window", ""]` the package
// previously produced 3 positionals and rejected the click as a UsageError,
// which made tmux flash the run-shell error popup on every window-list click.
func TestStatusbarClickWindowRangeWithEmptyMouseWindowIsNoop(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "window", "--mouse-window", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unknown range + empty mouse-window must be a noop, got %d calls", len(runner.calls))
	}
}

// TestStatusbarClickPositionalThenFlagSelectsWindow verifies that the natural
// tmux invocation order — the positional range id first, then the
// `--mouse-window` flag — actually triggers the window-list passthrough rather
// than failing parser checks.
func TestStatusbarClickPositionalThenFlagSelectsWindow(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "window", "--mouse-window", "3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@3"}) {
		t.Fatalf("missing select-window -t @3; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickFlagThenPositionalSelectsWindow verifies the symmetric
// "flags first, positional last" order also works.
func TestStatusbarClickFlagThenPositionalSelectsWindow(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window", "3", "window"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@3"}) {
		t.Fatalf("missing select-window -t @3; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickEqualsFormSelectsWindow verifies the `--flag=value` form
// is accepted in addition to the `--flag value` form.
func TestStatusbarClickEqualsFormSelectsWindow(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "--mouse-window=3", "window"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@3"}) {
		t.Fatalf("missing select-window -t @3; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickEmptyPositionalAndEmptyMouseWindowIsNoop covers the
// `click "" --mouse-window ""` shape (positional first then flag) which the
// previous parser also mishandled when the flag value was empty.
func TestStatusbarClickEmptyPositionalAndEmptyMouseWindowIsNoop(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "", "--mouse-window", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("empty positional + empty mouse-window must be a noop, got %d calls", len(runner.calls))
	}
}

// TestStatusbarClickNotifyWithMouseWindowIgnoresFlag checks that when a known
// projmux range is clicked, the `--mouse-window` flag is ignored regardless of
// argument order — the range handler always wins.
func TestStatusbarClickNotifyWithMouseWindowIgnoresFlag(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	store := &stubNotifyStore{listEntries: nil}
	cmd := newStatusbarTestCommand(runner, store)

	if err := cmd.Run([]string{"click", "notify", "--mouse-window", "3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) >= 1 && call.args[0] == "select-window" {
			t.Fatalf("known range must not trigger select-window passthrough; calls = %#v", runner.calls)
		}
	}
	if !sawTmuxDisplayMessage(runner.calls, "no notifications") {
		t.Fatalf("notify handler did not run; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickWindowRangeTokenWithMouseWindowSelectsWindow covers the
// real tmux shape for a window-list click: `#{mouse_status_range}` evaluates
// to `window|<idx>` (the built-in `STYLE_RANGE_WINDOW`) rather than an empty
// string. When `#{mouse_window}` is also populated we should use it (it's the
// unique window id) and address with the `@<id>` syntax.
func TestStatusbarClickWindowRangeTokenWithMouseWindowSelectsWindow(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "window|3", "--mouse-window", "5"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", "@5"}) {
		t.Fatalf("missing select-window -t @5; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickWindowRangeTokenWithoutMouseWindowFallsBackToIndex covers
// the tmux configurations where `#{mouse_window}` is empty for a window-range
// click but the range token still carries the winlink index (`window|<idx>`).
// We fall back to addressing by index (`:<idx>`) so the click still switches
// tabs reliably — this is the regression the user reported.
func TestStatusbarClickWindowRangeTokenWithoutMouseWindowFallsBackToIndex(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "window|3", "--mouse-window", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxArgs(runner.calls, []string{"select-window", "-t", ":3"}) {
		t.Fatalf("missing select-window -t :3; calls = %#v", runner.calls)
	}
}

// TestStatusbarClickBareWindowRangeWithoutIndexIsNoop covers the (rare) case
// where `#{mouse_status_range}` is the bare `window` token with no index
// attached and no `mouse_window` either. We have nothing to switch to, so
// the click must be a noop rather than a tmux error popup.
func TestStatusbarClickBareWindowRangeWithoutIndexIsNoop(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "window", "--mouse-window", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("bare 'window' range with no mouse-window must be a noop, got %d calls", len(runner.calls))
	}
}

// TestStatusbarClickRejectsMultiplePositionals ensures a stray second
// positional still fails fast rather than getting silently dropped.
func TestStatusbarClickRejectsMultiplePositionals(t *testing.T) {
	t.Parallel()

	cmd := newStatusbarTestCommand(&statusbarFakeRunner{}, &stubNotifyStore{})

	err := cmd.Run([]string{"click", "a", "b"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %T: %v", err, err)
	}
}

func TestStatusbarRunRejectsMissingSubcommand(t *testing.T) {
	t.Parallel()

	cmd := newStatusbarTestCommand(&statusbarFakeRunner{}, &stubNotifyStore{})
	err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %T: %v", err, err)
	}
}

func TestStatusbarRunRejectsUnknownSubcommand(t *testing.T) {
	t.Parallel()

	cmd := newStatusbarTestCommand(&statusbarFakeRunner{}, &stubNotifyStore{})
	err := cmd.Run([]string{"oops"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %T: %v", err, err)
	}
}

// --- helpers -----------------------------------------------------------------

func sawTmuxSubcommand(calls []statusbarFakeCall, sub string) bool {
	for _, c := range calls {
		if c.name != "tmux" {
			continue
		}
		if slices.Contains(c.args, sub) {
			return true
		}
	}
	return false
}

func sawTmuxArgs(calls []statusbarFakeCall, want []string) bool {
	for _, c := range calls {
		if c.name != "tmux" {
			continue
		}
		if equalStringSlices(c.args, want) {
			return true
		}
	}
	return false
}

func sawTmuxArgsContainInOrder(calls []statusbarFakeCall, want []string) bool {
	for _, c := range calls {
		if c.name != "tmux" {
			continue
		}
		next := 0
		for _, arg := range c.args {
			if next < len(want) && arg == want[next] {
				next++
			}
		}
		if next == len(want) {
			return true
		}
	}
	return false
}

func sawProjmuxArgs(calls []statusbarFakeCall, want []string) bool {
	for _, c := range calls {
		if c.name != "/usr/local/bin/projmux" {
			continue
		}
		if equalStringSlices(c.args, want) {
			return true
		}
	}
	return false
}

func sawTmuxDisplayMessage(calls []statusbarFakeCall, want string) bool {
	for _, c := range calls {
		if c.name != "tmux" || len(c.args) < 2 || c.args[0] != "display-message" {
			continue
		}
		if c.args[1] == want {
			return true
		}
	}
	return false
}

func sawTmuxPopupCommandContaining(calls []statusbarFakeCall, want string) bool {
	for _, c := range calls {
		if c.name != "tmux" || len(c.args) < 2 || c.args[0] != "display-popup" {
			continue
		}
		if strings.Contains(c.args[len(c.args)-1], want) {
			return true
		}
	}
	return false
}

func firstTmuxPopupCommand(calls []statusbarFakeCall) (string, bool) {
	for _, c := range calls {
		if c.name != "tmux" || len(c.args) < 2 || c.args[0] != "display-popup" {
			continue
		}
		return c.args[len(c.args)-1], true
	}
	return "", false
}

func lastDisplayMessage(calls []statusbarFakeCall) (string, bool) {
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.name == "tmux" && len(c.args) >= 2 && c.args[0] == "display-message" {
			return c.args[1], true
		}
	}
	return "", false
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sliceContainsPair(values []string, key, value string) bool {
	for i := 0; i < len(values)-1; i++ {
		if values[i] == key && values[i+1] == value {
			return true
		}
	}
	return false
}

// fakeExitError simulates the shape of a subprocess exit error: it carries a
// numeric exit code that the production code can extract via an
// `interface{ ExitCode() int }` assertion, mirroring how *exec.ExitError
// (via *os.ProcessState) exposes its exit code in real runs.
type fakeExitError struct {
	code int
	msg  string
}

func (e *fakeExitError) Error() string { return e.msg }
func (e *fakeExitError) ExitCode() int { return e.code }
