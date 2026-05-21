package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type stubNotifyStore struct {
	pushed     []notify.PushInput
	pushResult notify.PushResult
	pushEntry  notify.Notification
	pushErr    error

	listEntries []notify.Notification
	listErr     error
	listCalls   int

	ackedID  string
	ackedIDs []string
	ackErr   error
	ackAll   int
	ackAllOK bool
}

type stubNotifyPicker struct {
	options intpickercompat.Options
	result  intpickercompat.Result
	err     error
}

func (p *stubNotifyPicker) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	p.options = options
	return p.result, p.err
}

type recordingNotifyPicker struct {
	options []intpickercompat.Options
	results []intpickercompat.Result
	err     error
}

func (p *recordingNotifyPicker) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	p.options = append(p.options, options)
	if p.err != nil {
		return intpickercompat.Result{}, p.err
	}
	if len(p.results) == 0 {
		return intpickercompat.Result{}, nil
	}
	result := p.results[0]
	p.results = p.results[1:]
	return result, nil
}

type notifyPickerFunc func(options intpickercompat.Options) (intpickercompat.Result, error)

func (f notifyPickerFunc) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	return f(options)
}

func (s *stubNotifyStore) Push(in notify.PushInput) (notify.Notification, notify.PushResult, error) {
	s.pushed = append(s.pushed, in)
	return s.pushEntry, s.pushResult, s.pushErr
}

func (s *stubNotifyStore) List() ([]notify.Notification, error) {
	s.listCalls++
	return append([]notify.Notification(nil), s.listEntries...), s.listErr
}

func (s *stubNotifyStore) Ack(id string) error {
	s.ackedID = id
	s.ackedIDs = append(s.ackedIDs, id)
	if s.ackErr == nil {
		next := s.listEntries[:0]
		for _, entry := range s.listEntries {
			if entry.ID != id {
				next = append(next, entry)
			}
		}
		s.listEntries = next
	}
	return s.ackErr
}

func (s *stubNotifyStore) AckAll() (int, error) {
	s.ackAllOK = true
	if s.ackErr == nil {
		s.listEntries = nil
	}
	return s.ackAll, s.ackErr
}

func newCmd(store notifyStore) *notifyCommand {
	return &notifyCommand{
		store: store,
		now:   func() time.Time { return time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func TestNotifyPushHappyPath(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		pushResult: notify.PushResult{ID: "abc", QueueLen: 1},
		pushEntry:  notify.Notification{ID: "abc"},
	}
	cmd := newCmd(store)

	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{
		"push",
		"--text", "deploy ok",
		"--target", "main:1.0",
		"--severity", "warn",
		"--source", "ai",
		"--ttl", "60",
		"--id", "fixed",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error = %v (stderr=%q)", err, stderr.String())
	}

	if len(store.pushed) != 1 {
		t.Fatalf("push call count = %d", len(store.pushed))
	}
	in := store.pushed[0]
	if in.Text != "deploy ok" || in.Severity != "warn" || in.Source != "ai" || in.ID != "fixed" {
		t.Fatalf("push input = %+v", in)
	}
	if in.Target.Session != "main" || in.Target.Window != "1" || in.Target.Pane != "0" {
		t.Fatalf("target = %+v", in.Target)
	}
	if in.TTL != 60*time.Second {
		t.Fatalf("ttl = %s", in.TTL)
	}

	var out struct {
		ID     string `json:"id"`
		Queued int    `json:"queued"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if out.ID != "abc" || out.Queued != 1 {
		t.Fatalf("decoded = %+v", out)
	}
}

func TestNotifyPushDefaults(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	cmd := newCmd(store)

	if err := cmd.Run([]string{"push", "--text", "hi", "--target", "s"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d", len(store.pushed))
	}
	in := store.pushed[0]
	if in.Severity != notify.SeverityInfo {
		t.Fatalf("Severity = %q", in.Severity)
	}
	if in.Source != notify.SourceExternal {
		t.Fatalf("Source = %q", in.Source)
	}
	if in.TTL != notify.DefaultTTL {
		t.Fatalf("TTL = %s", in.TTL)
	}
}

func TestNotifyPushUsageErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing text", []string{"push", "--target", "s"}, "requires --text"},
		{"missing target", []string{"push", "--text", "hi"}, "requires --target"},
		{"bad severity", []string{"push", "--text", "hi", "--target", "s", "--severity", "loud"}, "invalid severity"},
		{"bad source", []string{"push", "--text", "hi", "--target", "s", "--source", "weird"}, "invalid source"},
		{"bad target", []string{"push", "--text", "hi", "--target", ":1"}, "invalid target"},
		{"non-positive ttl", []string{"push", "--text", "hi", "--target", "s", "--ttl", "0"}, "positive --ttl"},
		{"positional arg", []string{"push", "--text", "hi", "--target", "s", "extra"}, "does not accept positional"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store := &stubNotifyStore{}
			cmd := newCmd(store)
			err := cmd.Run(c.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsUsageError(err) {
				t.Fatalf("expected UsageError, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want substring %q", err, c.want)
			}
		})
	}
}

func TestNotifyListJSON(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "a", Text: "x", Severity: "info", Source: "ai", Session: "s", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var got []notify.Notification
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, stdout.String())
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestNotifyListTable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "abc",
				Text:      "deploy ok",
				Severity:  "warn",
				Source:    "ai",
				Session:   "main",
				Window:    "1",
				Pane:      "0",
				CreatedAt: now.Add(-30 * time.Second),
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "ID\tAGE\tSEV\tSRC\tTARGET\tTEXT") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "abc\t30s\twarn\tai\tmain:1.0\tdeploy ok") {
		t.Fatalf("missing row: %q", out)
	}
}

func TestNotifyListSidebarFocusesAndAcksSelectedRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "abc",
				Text:      "Ready",
				Severity:  notify.SeverityWarn,
				Source:    notify.SourceAI,
				Metadata:  map[string]string{"agent": "codex", "category": "response_complete", "state": "need", "topic": "worker loop"},
				Socket:    "projmux",
				Session:   "main",
				Window:    "1",
				Pane:      "0",
				CreatedAt: now.Add(-30 * time.Second),
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	picker := &stubNotifyPicker{result: intpickercompat.Result{Value: "abc"}}
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = runner
	cmd.executable = func() (string, error) { return "/usr/local/bin/projmux", nil }

	if err := cmd.Run([]string{"list", "--ui=sidebar", "--client", "/dev/pts/7"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if picker.options.UI != "notify-sidebar" {
		t.Fatalf("picker UI = %q, want notify-sidebar", picker.options.UI)
	}
	if !picker.options.Read0 {
		t.Fatal("picker Read0 = false, want true")
	}
	if !picker.options.DisableSearch {
		t.Fatal("picker DisableSearch = false, want true")
	}
	if got, want := picker.options.Prompt, "Notify > "; got != want {
		t.Fatalf("picker prompt = %q, want %q", got, want)
	}
	if got, want := picker.options.Title, "\x1b[1;38;5;220mPending Notifications\x1b[0m"; got != want {
		t.Fatalf("picker title = %q, want %q", got, want)
	}
	if got, want := picker.options.Header, "Newest first"; got != want {
		t.Fatalf("picker header = %q, want %q", got, want)
	}
	if got, want := picker.options.Footer, "Enter: focus/ack  |  A: ack  |  X: clear non-critical  |  Ctrl-X: clear all"; got != want {
		t.Fatalf("picker footer = %q, want %q", got, want)
	}
	if got, want := picker.options.ExpectKeys, []string{"a", "x", "ctrl-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expect keys = %#v, want %#v", got, want)
	}
	if len(picker.options.Entries) != 1 || picker.options.Entries[0].Value != "abc" {
		t.Fatalf("entries = %#v", picker.options.Entries)
	}
	entry := picker.options.Entries[0]
	labelLines := strings.Split(entry.Label, "\n")
	if len(labelLines) != 2 {
		t.Fatalf("sidebar label = %q, want two-line card", entry.Label)
	}
	if got := stripANSI(labelLines[0]); got != " main  Ready" {
		t.Fatalf("sidebar first line = %q, want project badge before notification text", labelLines[0])
	}
	if !strings.Contains(labelLines[1], theme.ANSIChipActiveStart+" worker loop ") {
		t.Fatalf("sidebar metadata = %q, want prominent topic badge", labelLines[1])
	}
	metaText := stripANSI(labelLines[1])
	ageIndex := strings.Index(metaText, "age 30s")
	topicIndex := strings.Index(metaText, "worker loop")
	statusIndex := strings.Index(metaText, "WARN")
	agentIndex := strings.Index(metaText, "codex")
	if !(ageIndex >= 0 && ageIndex < topicIndex && topicIndex < statusIndex && statusIndex < agentIndex) {
		t.Fatalf("sidebar metadata = %q, want age/topic/status/agent order", labelLines[1])
	}
	if meta := labelLines[1]; !strings.Contains(meta, " age 30s ") || strings.Contains(meta, " main ") || !strings.Contains(meta, " codex ") || !strings.Contains(meta, " WARN ") || strings.Contains(meta, "window 1") || strings.Contains(meta, "pane 0") || strings.Contains(meta, " queued ") || strings.Contains(meta, " ai ") {
		t.Fatalf("sidebar metadata = %q, want age/topic/status/agent without project/target/source", meta)
	}
	if strings.Contains(entry.Label, "abc") {
		t.Fatalf("sidebar label = %q, want hidden queue id", entry.Label)
	}
	if got, want := picker.options.Bindings, []string{"esc:abort", "alt-2:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings = %#v, want %#v", got, want)
	}
	if store.ackedID != "abc" {
		t.Fatalf("ackedID = %q, want abc", store.ackedID)
	}
	focusCalls := filterFocusCalls(runner.calls)
	if len(focusCalls) != 1 || focusCalls[0].name != "/usr/local/bin/projmux" {
		t.Fatalf("focus calls = %#v", focusCalls)
	}
	wantArgs := []string{"focus", "--target", "main:1.0", "--source", "notify-sidebar", "--kind", "row-select", "--socket", "projmux", "--client", "/dev/pts/7"}
	if !equalStringSlices(focusCalls[0].args, wantArgs) {
		t.Fatalf("focus args = %#v, want %#v", focusCalls[0].args, wantArgs)
	}
}

func TestNotifyListSidebarFooterReadsKeymapGuide(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings."NotifySidebar:FocusAndAck"]
keys = ["o", "Enter"]

[bindings."NotifySidebar:Ack"]
keys = ["b", "a"]

[bindings."NotifySidebar:ClearNonCritical"]
keys = ["c"]

[bindings."NotifySidebar:ClearAll"]
keys = ["C-y"]
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &stubNotifyStore{
		listEntries: []notify.Notification{{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"}},
	}
	picker := &stubNotifyPicker{result: intpickercompat.Result{}}
	cmd := newCmd(store)
	cmd.homeDir = func() (string, error) { return home, nil }
	cmd.lookupEnv = func(string) string { return "" }
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	want := "Enter: focus/ack  |  A: ack  |  C: clear non-critical  |  Ctrl-Y: clear all"
	if got := picker.options.Footer; got != want {
		t.Fatalf("picker footer = %q, want %q", got, want)
	}
}

func TestNotifySidebarProjectBadgeMatchesStatusbarProjectPalette(t *testing.T) {
	t.Parallel()

	sidebar := notifySidebarProjectBadge("main")
	for _, want := range []string{"\x1b[1;", "38;5;231", "48;5;90"} {
		if !strings.Contains(sidebar, want) {
			t.Fatalf("sidebar project badge = %q, want ANSI token %q", sidebar, want)
		}
	}

	statusbar := renderNotifyProjectBadge("main")
	for _, want := range []string{"bg=" + theme.TmuxAttentionProjectBg, "fg=" + theme.TmuxPrimaryFg, "bold"} {
		if !strings.Contains(statusbar, want) {
			t.Fatalf("statusbar project badge = %q, want tmux token %q", statusbar, want)
		}
	}
}

func TestNotifyListSidebarTitleDecoration(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		decoration string
		wantTitle  string
	}{
		{name: "off", decoration: "off", wantTitle: "\x1b[1;38;5;220mPending Notifications\x1b[0m"},
		{name: "symbol", decoration: "symbol", wantTitle: "\x1b[1;38;5;220m Pending Notifications\x1b[0m"},
		{name: "emoji", decoration: "emoji", wantTitle: "\x1b[1;38;5;220m🔔 Pending Notifications\x1b[0m"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &stubNotifyStore{}
			picker := &stubNotifyPicker{}
			runner := &focusFakeRunner{respond: func(args []string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"show-options", "-gqv", statusbarDecorationNotifyTmuxOption}) {
					return []byte(tt.decoration + "\n"), nil
				}
				return nil, errors.New("unexpected runner call")
			}}
			cmd := newCmd(store)
			cmd.lookupEnv = func(name string) string {
				if name == "TMUX" {
					return "/tmp/tmux"
				}
				return ""
			}
			cmd.picker = picker
			cmd.native = nativePickerFromCompatRunner(picker)
			cmd.runner = runner

			if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run error = %v", err)
			}
			if got := picker.options.Title; got != tt.wantTitle {
				t.Fatalf("picker title = %q, want %q", got, tt.wantTitle)
			}
			if got, want := picker.options.Header, "Newest first"; got != want {
				t.Fatalf("picker header = %q, want %q", got, want)
			}
		})
	}
}

func TestNotifySidebarLabelDoesNotExposeRawPaneID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	label := notifySidebarLabel(notify.Notification{
		ID:        "abc",
		Text:      "deploy\nok",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceExternal,
		Session:   "main",
		Window:    "1",
		Pane:      "%42",
		CreatedAt: now.Add(-2 * time.Minute),
	}, now)

	lines := strings.Split(label, "\n")
	if len(lines) != 2 {
		t.Fatalf("sidebar label = %q, want two lines", label)
	}
	if lines[0] != "deploy ok" {
		t.Fatalf("first line = %q, want sanitized text", lines[0])
	}
	if strings.Contains(label, "%42") {
		t.Fatalf("sidebar label = %q, want raw pane id hidden", label)
	}
	if !strings.Contains(lines[1], "window 1") || !strings.Contains(lines[1], "pane 42") {
		t.Fatalf("metadata = %q, want readable window/pane labels", lines[1])
	}
}

func TestNotifySidebarNativeBackendDoesNotCallCompatRunner(t *testing.T) {
	store := &stubNotifyStore{
		listEntries: []notify.Notification{{
			ID:        "abc",
			Text:      "deploy ok",
			Severity:  notify.SeverityWarn,
			Source:    notify.SourceAI,
			Metadata:  map[string]string{"agent": "codex"},
			Socket:    "projmux",
			Session:   "main",
			Window:    "1",
			Pane:      "0",
			CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC),
			ExpiresAt: time.Date(2026, time.May, 6, 13, 0, 0, 0, time.UTC),
		}},
	}
	var compatCalled bool
	var nativeCalled bool
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.native = pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		nativeCalled = true
		if options.UI != "notify-sidebar" {
			t.Fatalf("native UI = %q, want notify-sidebar", options.UI)
		}
		if len(options.Items) != 1 || options.Items[0].Value != "abc" {
			t.Fatalf("native items = %#v, want abc", options.Items)
		}
		return intpicker.Result{Value: "abc"}, nil
	})
	cmd.lookupEnv = func(name string) string {
		if name == intpicker.BackendEnv {
			return string(intpicker.BackendNative)
		}
		return ""
	}
	cmd.picker = notifyPickerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
		compatCalled = true
		return intpickercompat.Result{}, nil
	})
	cmd.runner = runner
	cmd.executable = func() (string, error) { return "/usr/local/bin/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_NOTIFY_ORIGIN_CLIENT" {
			return "/dev/pts/9"
		}
		return ""
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if compatCalled {
		t.Fatal("compat picker was called for native notify backend")
	}
	if !nativeCalled {
		t.Fatal("native picker was not called")
	}
	if store.ackedID != "abc" {
		t.Fatalf("ackedID = %q, want abc", store.ackedID)
	}
	focusCalls := filterFocusCalls(runner.calls)
	if len(focusCalls) != 1 || focusCalls[0].name != "/usr/local/bin/projmux" {
		t.Fatalf("focus calls = %#v, want one focus call", focusCalls)
	}
	wantArgs := []string{"focus", "--target", "main:1.0", "--source", "notify-sidebar", "--kind", "row-select", "--socket", "projmux", "--client", "/dev/pts/9"}
	if !equalStringSlices(focusCalls[0].args, wantArgs) {
		t.Fatalf("focus args = %#v, want %#v", focusCalls[0].args, wantArgs)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no output for focus + ack path", stdout.String())
	}
}

func TestNotifyListSidebarDoesNotAckWhenFocusFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "abc",
				Text:      "deploy ok",
				Severity:  notify.SeverityWarn,
				Source:    notify.SourceAI,
				Session:   "main",
				Window:    "1",
				Pane:      "0",
				CreatedAt: now.Add(-30 * time.Second),
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	picker := &stubNotifyPicker{result: intpickercompat.Result{Value: "abc"}}
	runner := &focusFakeRunner{respond: func([]string) ([]byte, error) {
		return nil, errors.New("focus failed")
	}}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = runner
	cmd.executable = func() (string, error) { return "/usr/local/bin/projmux", nil }

	err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "focus failed") {
		t.Fatalf("Run error = %v, want focus failure", err)
	}
	if store.ackedID != "" {
		t.Fatalf("ackedID = %q, want empty", store.ackedID)
	}
}

func TestNotifyListSidebarTargetGoneAcksSelectedRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "abc",
				Text:      "deploy ok",
				Severity:  notify.SeverityWarn,
				Source:    notify.SourceAI,
				Session:   "__gone",
				Window:    "1",
				Pane:      "0",
				CreatedAt: now.Add(-30 * time.Second),
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	picker := &stubNotifyPicker{result: intpickercompat.Result{Value: "abc"}}
	runner := &focusFakeRunner{respond: func([]string) ([]byte, error) {
		return nil, &fakeExitError{code: focusExitNotResolved, msg: "target unresolved"}
	}}
	cmd := newCmd(store)
	cmd.now = func() time.Time { return now }
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = runner
	cmd.executable = func() (string, error) { return "/usr/local/bin/projmux", nil }

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if store.ackedID != "abc" {
		t.Fatalf("ackedID = %q, want abc", store.ackedID)
	}
}

func TestNotifyListSidebarAAcksSelectedRowAndRefreshes(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
			{ID: "def", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
			{ID: "ghi", Text: "blocked", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main"},
		},
	}
	picker := &recordingNotifyPicker{
		results: []intpickercompat.Result{
			{Key: "a", Value: "def"},
			{Key: "a", Value: "abc"},
			{},
		},
	}
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = runner

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if store.ackedID != "abc" {
		t.Fatalf("ackedID = %q, want abc", store.ackedID)
	}
	if got, want := store.ackedIDs, []string{"def", "abc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if store.listCalls != 3 {
		t.Fatalf("List calls = %d, want 3", store.listCalls)
	}
	if len(picker.options) != 3 {
		t.Fatalf("picker runs = %d, want 3", len(picker.options))
	}
	first := picker.options[0].Entries
	if len(first) != 3 || first[0].Value != "abc" || first[1].Value != "def" || first[2].Value != "ghi" {
		t.Fatalf("first picker entries = %#v, want abc then def then ghi", first)
	}
	if got, want := picker.options[0].Bindings, []string{"esc:abort", "alt-2:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first picker bindings = %#v, want %#v", got, want)
	}
	second := picker.options[1].Entries
	if len(second) != 2 || second[0].Value != "abc" || second[1].Value != "ghi" {
		t.Fatalf("second picker entries = %#v, want abc then ghi", second)
	}
	if got, want := picker.options[1].Bindings, []string{"esc:abort", "alt-2:abort", "start:pos(2)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second picker bindings = %#v, want %#v", got, want)
	}
	third := picker.options[2].Entries
	if len(third) != 1 || third[0].Value != "ghi" {
		t.Fatalf("third picker entries = %#v, want ghi", third)
	}
	if got, want := picker.options[2].Bindings, []string{"esc:abort", "alt-2:abort", "start:pos(1)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("third picker bindings = %#v, want %#v", got, want)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no sidebar ack output", stdout.String())
	}
	if focusCalls := filterFocusCalls(runner.calls); len(focusCalls) != 0 {
		t.Fatalf("focus calls = %#v, want none", focusCalls)
	}
}

func TestNotifyListSidebarXClearsNonCriticalAndPreservesCritical(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
			{ID: "def", Text: "blocked", Severity: notify.SeverityCritical, Source: notify.SourceAI, Session: "main"},
			{ID: "ghi", Text: "warn", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main"},
		},
	}
	picker := &recordingNotifyPicker{
		results: []intpickercompat.Result{
			{Key: "x", Value: "def"},
			{},
		},
	}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = &focusFakeRunner{}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"abc", "ghi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 2 {
		t.Fatalf("picker runs = %d, want 2", len(picker.options))
	}
	second := picker.options[1].Entries
	if len(second) != 1 || second[0].Value != "def" {
		t.Fatalf("second picker entries = %#v, want critical def preserved", second)
	}
	if second[0].Value == "abc" || second[0].Value == "ghi" {
		t.Fatalf("second picker entries = %#v, want non-critical rows removed", second)
	}
	if got, want := picker.options[1].Bindings, []string{"esc:abort", "alt-2:abort", "start:pos(1)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second picker bindings = %#v, want %#v", got, want)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no sidebar clear output", stdout.String())
	}
	if focusCalls := filterFocusCalls(cmd.runner.(*focusFakeRunner).calls); len(focusCalls) != 0 {
		t.Fatalf("focus calls = %#v, want none", focusCalls)
	}
}

func TestNotifyListSidebarXClearNonCriticalRendersEmptyState(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
			{ID: "def", Text: "warn", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main"},
		},
	}
	picker := &recordingNotifyPicker{
		results: []intpickercompat.Result{
			{Key: "x", Value: "abc"},
			{},
		},
	}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"abc", "def"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 2 {
		t.Fatalf("picker runs = %d, want 2", len(picker.options))
	}
	second := picker.options[1].Entries
	if len(second) != 1 || second[0].Value != notifySidebarEmptyValue {
		t.Fatalf("second picker entries = %#v, want empty state", second)
	}
	if second[0].Label != "No pending notifications" {
		t.Fatalf("empty label = %q", second[0].Label)
	}
	if got, want := picker.options[1].Bindings, []string{"esc:abort", "alt-2:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty picker bindings = %#v, want %#v", got, want)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no sidebar clear output", stdout.String())
	}
}

func TestNotifyListSidebarAAckLastRowStartsAtPreviousRow(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
			{ID: "def", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
		},
	}
	picker := &recordingNotifyPicker{
		results: []intpickercompat.Result{
			{Key: "a", Value: "def"},
			{},
		},
	}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"def"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 2 {
		t.Fatalf("picker runs = %d, want 2", len(picker.options))
	}
	second := picker.options[1].Entries
	if len(second) != 1 || second[0].Value != "abc" {
		t.Fatalf("second picker entries = %#v, want only abc", second)
	}
	if got, want := picker.options[1].Bindings, []string{"esc:abort", "alt-2:abort", "start:pos(1)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second picker bindings = %#v, want %#v", got, want)
	}
}

func TestNotifyListSidebarAAckRefreshesLiveStateEachLoop(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "ai:main:%2", Text: "first", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex"}, Session: "main", Window: "@1", Pane: "%2", CreatedAt: now},
			{ID: "ai:main:%3", Text: "second", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex"}, Session: "main", Window: "@1", Pane: "%3", CreatedAt: now},
		},
	}
	picker := &recordingNotifyPicker{
		results: []intpickercompat.Result{
			{Key: "a", Value: "ai:main:%2"},
			{},
		},
	}
	var liveCalls int
	runner := &focusFakeRunner{respond: func(args []string) ([]byte, error) {
		if containsArg(args, "list-panes") {
			liveCalls++
			if liveCalls == 1 {
				return notifyLivePaneRows(
					[]string{"main", "@1", "%2", "0", "codex", "reply", "waiting", "codex", "first", "/tmp/tmux/default"},
					[]string{"main", "@1", "%3", "0", "codex", "reply", "waiting", "codex", "second", "/tmp/tmux/default"},
				), nil
			}
			return []byte{}, nil
		}
		return nil, errors.New("unexpected runner call")
	}}
	cmd := newCmd(store)
	cmd.now = func() time.Time { return now }
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)
	cmd.runner = runner

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if liveCalls != 2 {
		t.Fatalf("live list calls = %d, want 2", liveCalls)
	}
	if len(picker.options) != 2 {
		t.Fatalf("picker runs = %d, want 2", len(picker.options))
	}
	second := picker.options[1].Entries
	if len(second) != 1 || second[0].Value != "ai:main:%3" {
		t.Fatalf("second picker entries = %#v, want only ai:main:%%3", second)
	}
	if !strings.Contains(second[0].Label, "STALE") {
		t.Fatalf("second label = %q, want refreshed stale state", second[0].Label)
	}
}

func TestNotifyListSidebarClearAll(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"}},
		ackAll:      1,
	}
	picker := &stubNotifyPicker{result: intpickercompat.Result{Key: "ctrl-x", Value: "abc"}}
	cmd := newCmd(store)
	cmd.picker = picker
	cmd.native = nativePickerFromCompatRunner(picker)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if !store.ackAllOK {
		t.Fatal("AckAll was not called")
	}
	if !strings.Contains(stdout.String(), "cleared 1 notification") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestNotifyListSidebarRejectsJSONAndLive(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"list", "--ui=sidebar", "--json"},
		{"list", "--ui=sidebar", "--live"},
	} {
		cmd := newCmd(&stubNotifyStore{})
		err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !IsUsageError(err) {
			t.Fatalf("Run(%v) err = %v, want usage error", args, err)
		}
	}
}

func TestNotifyListLiveHumanExplainsManualReplyWithoutQueue(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			if containsArg(args, "list-panes") {
				return notifyLivePaneRows(
					[]string{"main", "@1", "%2", "0", "✳ shell", "reply", "idle", "", "", "/tmp/tmux/default"},
				), nil
			}
			return nil, nil
		},
	}
	cmd := newCmd(store)
	cmd.runner = runner

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--live"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "STATE\tTARGET\tID\tEXPLANATION\tTEXT") {
		t.Fatalf("missing live header: %q", out)
	}
	if !strings.Contains(out, "live-manual-reply") {
		t.Fatalf("missing manual reply explanation row: %q", out)
	}
	if !strings.Contains(out, "manual attention panes do not create notify queue entries") {
		t.Fatalf("missing queue-empty explanation: %q", out)
	}
}

func TestNotifyListLiveHumanExplainsTitlePrefixWithoutQueue(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			if containsArg(args, "list-panes") {
				return notifyLivePaneRows(
					[]string{"main", "@1", "%2", "0", "✳ stale title", "", "idle", "", "", "/tmp/tmux/default"},
				), nil
			}
			return nil, nil
		},
	}
	cmd := newCmd(store)
	cmd.runner = runner

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--live"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "live-title-attention") {
		t.Fatalf("missing title attention explanation row: %q", out)
	}
	if !strings.Contains(out, "title-only/manual attention does not create notify queue entries") {
		t.Fatalf("missing title-only queue explanation: %q", out)
	}
}

func TestNotifyListLiveJSONExplainsQueueAndLiveStates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{
				ID:        "ai:main:%2",
				Text:      "topic",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Metadata:  map[string]string{"agent": "codex", "category": "response_complete", "state": "need"},
				Session:   "main",
				Window:    "@1",
				Pane:      "%2",
				CreatedAt: now,
				ExpiresAt: now.Add(time.Hour),
			},
			{
				ID:        "ai:gone:%9",
				Text:      "stale",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "gone",
				Pane:      "%9",
				CreatedAt: now,
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			if containsArg(args, "list-panes") {
				return notifyLivePaneRows(
					[]string{"main", "@1", "%2", "0", "codex", "reply", "waiting", "codex", "topic", "/tmp/tmux/default"},
					[]string{"feat", "@2", "%3", "0", "claude", "reply", "waiting", "claude", "help", "/tmp/tmux/default"},
					[]string{"shell", "@3", "%4", "0", "✳ shell", "reply", "idle", "", "", "/tmp/tmux/default"},
					[]string{"title", "@4", "%5", "0", "✳ title-only", "", "idle", "codex", "topic", "/tmp/tmux/default"},
				), nil
			}
			return nil, nil
		},
	}
	cmd := newCmd(store)
	cmd.runner = runner

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--live", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var report notifyLiveReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, stdout.String())
	}
	states := map[string]bool{}
	for _, row := range report.Rows {
		states[row.State] = true
	}
	for _, state := range []string{
		"live-ai-reply-queued",
		"live-ai-reply-missing-queue",
		"live-manual-reply",
		"live-title-attention",
		"queue-stale",
	} {
		if !states[state] {
			t.Fatalf("missing state %q in rows: %+v", state, report.Rows)
		}
	}
	if len(report.Queue) != 2 {
		t.Fatalf("queue len = %d, want 2", len(report.Queue))
	}
	if len(report.Live) != 4 {
		t.Fatalf("live len = %d, want 4", len(report.Live))
	}
}

func TestNotifyListFiltersBySeverity(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "a", Severity: "info", Source: "ai", Session: "s"},
			{ID: "b", Severity: "warn", Source: "git", Session: "s"},
			{ID: "c", Severity: "critical", Source: "ai", Session: "s"},
		},
	}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--severity", "warn", "--severity", "critical", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var got []notify.Notification
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("filtered = %+v", got)
	}
}

func TestNotifyListLimit(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "a", Severity: "info", Source: "ai", Session: "s"},
			{ID: "b", Severity: "info", Source: "ai", Session: "s"},
			{ID: "c", Severity: "info", Source: "ai", Session: "s"},
		},
	}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--limit", "2", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var got []notify.Notification
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestNotifyAckByID(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ack", "abc"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if store.ackedID != "abc" {
		t.Fatalf("acked id = %q", store.ackedID)
	}
	if !strings.Contains(stdout.String(), "ack abc") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestNotifyAckAll(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{ackAll: 3}
	cmd := newCmd(store)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"ack", "--all"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if !store.ackAllOK {
		t.Fatal("expected AckAll to be called")
	}
	if !strings.Contains(stdout.String(), "ack 3 notification") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestNotifyAckMissingArgs(t *testing.T) {
	t.Parallel()

	cmd := newCmd(&stubNotifyStore{})
	err := cmd.Run([]string{"ack"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %v", err)
	}
}

func TestNotifyAckNotFoundIsNotUsageError(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{ackErr: notify.ErrNotFound}
	cmd := newCmd(store)
	err := cmd.Run([]string{"ack", "missing"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if IsUsageError(err) {
		t.Fatalf("did not expect UsageError")
	}
	if !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNotifyUnknownSubcommandIsUsageError(t *testing.T) {
	t.Parallel()

	cmd := newCmd(&stubNotifyStore{})
	err := cmd.Run([]string{"oops"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %v", err)
	}
}

func TestNotifyHelpPrintsUsage(t *testing.T) {
	t.Parallel()

	cmd := newCmd(&stubNotifyStore{})
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Pending AI notify queue") {
		t.Fatalf("stdout = %q, want pending queue boundary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "notify reconcile") {
		t.Fatalf("stdout = %q, want reconcile recovery path", stdout.String())
	}
}

func TestNotifyListHelpDescribesPendingQueueBoundary(t *testing.T) {
	t.Parallel()

	cmd := newCmd(&stubNotifyStore{})
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"list", "--help"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Pending AI notify queue entries only") {
		t.Fatalf("stderr = %q, want pending queue boundary", stderr.String())
	}
	if !strings.Contains(stderr.String(), "projmux attention list") {
		t.Fatalf("stderr = %q, want live attention pointer", stderr.String())
	}
}

func notifyLivePaneRows(rows ...[]string) []byte {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, strings.Join(row, attentionListSeparator))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
