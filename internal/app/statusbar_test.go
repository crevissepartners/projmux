package app

import (
	"bytes"
	"context"
	"errors"
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

func TestStatusbarClickRejectsUnknownRange(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	err := cmd.Run([]string{"click", "totally-bogus"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unknown range id")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "unknown statusbar range id") {
		t.Fatalf("err = %v, want substring 'unknown statusbar range id'", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unknown range should not invoke runner, got %d calls", len(runner.calls))
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

func TestStatusbarClickSessionDisplaysMessage(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "session"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "session: #{session_name}") {
		t.Fatalf("missing display-message; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickPwdDisplaysPanePath(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "pwd"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawTmuxDisplayMessage(runner.calls, "#{pane_current_path}") {
		t.Fatalf("missing display-message; calls = %#v", runner.calls)
	}
}

func TestStatusbarClickKubeDisplaysTodoMessage(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "kube"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got, ok := lastDisplayMessage(runner.calls)
	if !ok {
		t.Fatalf("missing display-message; calls = %#v", runner.calls)
	}
	if !strings.Contains(got, "kube clicker") {
		t.Fatalf("display-message = %q, want substring 'kube clicker'", got)
	}
}

func TestStatusbarClickGitDisplaysTodoMessage(t *testing.T) {
	t.Parallel()

	runner := &statusbarFakeRunner{}
	cmd := newStatusbarTestCommand(runner, &stubNotifyStore{})

	if err := cmd.Run([]string{"click", "git"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got, ok := lastDisplayMessage(runner.calls)
	if !ok {
		t.Fatalf("missing display-message; calls = %#v", runner.calls)
	}
	if !strings.Contains(got, "git clicker") {
		t.Fatalf("display-message = %q, want substring 'git clicker'", got)
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
		for _, a := range c.args {
			if a == sub {
				return true
			}
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
