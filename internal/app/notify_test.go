package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
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

type stubNotifyQueueEvents struct {
	publishCalls   int
	publishErr     error
	subscribeCalls int
	subscribeErr   error
	trigger        chan struct{}
}

func (e *stubNotifyQueueEvents) Publish() error {
	e.publishCalls++
	return e.publishErr
}

func (e *stubNotifyQueueEvents) Subscribe(context.Context) (<-chan struct{}, error) {
	e.subscribeCalls++
	if e.subscribeErr != nil {
		return nil, e.subscribeErr
	}
	if e.trigger == nil {
		e.trigger = make(chan struct{}, 1)
	}
	return e.trigger, nil
}

type recordingNotifyNativePicker struct {
	options []intpicker.Options
	steps   []notifyNativeActionStep
	updates [][]intpicker.Item
	result  intpicker.Result
	err     error
}

type notifyNativeActionStep struct {
	key           string
	value         string
	selectedIndex int
}

func (p *recordingNotifyNativePicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.options = append(p.options, options)
	if p.err != nil {
		return intpicker.Result{}, p.err
	}
	for _, step := range p.steps {
		action, ok := notifyNativeActionByKey(options.Actions, step.key)
		if !ok {
			return intpicker.Result{Key: step.key, Value: step.value}, nil
		}
		if action.Mutate == nil {
			return intpicker.Result{Key: step.key, Value: step.value}, nil
		}
		update, err := action.Mutate(intpicker.ActionContext{
			Key:           step.key,
			Value:         step.value,
			SelectedIndex: step.selectedIndex,
		})
		if err != nil {
			return intpicker.Result{}, err
		}
		if update.Result != nil {
			return *update.Result, nil
		}
		p.updates = append(p.updates, update.Items)
		options.Items = update.Items
	}
	return p.result, nil
}

func notifyNativeActionByKey(actions []intpicker.Action, key string) (intpicker.Action, bool) {
	for _, action := range actions {
		if action.Key == key {
			return action, true
		}
	}
	return intpicker.Action{}, false
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

func TestNotifyPushPublishesQueueRefreshBestEffort(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		pushResult: notify.PushResult{ID: "abc", QueueLen: 1},
		pushEntry:  notify.Notification{ID: "abc"},
	}
	events := &stubNotifyQueueEvents{publishErr: errors.New("listener unavailable")}
	cmd := newCmd(store)
	cmd.events = events

	if err := cmd.Run([]string{"push", "--text", "hi", "--target", "s"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if events.publishCalls != 1 {
		t.Fatalf("publish calls = %d, want 1", events.publishCalls)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want queue write to succeed", len(store.pushed))
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
	if !strings.Contains(out, "abc\t30s ago\twarn\tai\tmain:1.0\tdeploy ok") {
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
	if got, want := picker.options.Footer, "Right: unfold  |  Left: fold  |  Enter: focus/ack  |  a: ack  |  A: ack group  |  x: clear non-critical  |  Ctrl-X: clear all"; got != want {
		t.Fatalf("picker footer = %q, want %q", got, want)
	}
	if got, want := picker.options.ExpectKeys, []string{"enter", "a", "A", "x", "right", "left", "ctrl-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expect keys = %#v, want %#v", got, want)
	}
	groupValue := notifySidebarGroupValue("pane\x00projmux\x00main\x000")
	if len(picker.options.Entries) != 1 || picker.options.Entries[0].Value != groupValue {
		t.Fatalf("entries = %#v", picker.options.Entries)
	}
	entry := picker.options.Entries[0]
	labelLines := strings.Split(entry.Label, "\n")
	if len(labelLines) != 3 {
		t.Fatalf("sidebar label = %q, want three-line group card", entry.Label)
	}
	if got := stripANSI(labelLines[0]); !strings.Contains(got, "▸ Codex · worker loop") || !strings.Contains(got, "30s ago") || strings.Contains(got, "WARN") || strings.Contains(got, "notification") {
		t.Fatalf("sidebar first line = %q, want identity and age only", labelLines[0])
	}
	if got := stripANSI(labelLines[1]); !strings.Contains(got, "Codex · Response complete") || !strings.Contains(got, "WARN") || strings.Contains(got, "+0") || strings.Contains(got, "win 1") || strings.Contains(got, "pane 0") {
		t.Fatalf("sidebar second line = %q, want preview and aggregate metadata without target ids", labelLines[1])
	}
	if aux := labelLines[2]; !strings.Contains(aux, " main ") || strings.Contains(aux, "queued") || strings.Contains(aux, " ai ") {
		t.Fatalf("sidebar aux line = %q, want project context without queue internals", aux)
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

[bindings."NotifySidebar:AckGroup"]
keys = ["h", "A"]

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
	want := "Right: unfold  |  Left: fold  |  Enter: focus/ack  |  a: ack  |  A: ack group  |  c: clear non-critical  |  Ctrl-Y: clear all"
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
	if !strings.Contains(lines[1], "win 1") || !strings.Contains(lines[1], "pane 42") {
		t.Fatalf("metadata = %q, want readable window/pane labels", lines[1])
	}
}

func TestNotifySidebarLabelUsesKoreanFormatterOutput(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	label := notifySidebarLabelForLocale(notify.Notification{
		ID:        "stale",
		Text:      "deploy ok",
		Severity:  notify.SeverityInfo,
		Source:    notify.SourceExternal,
		Session:   "main",
		Window:    "1",
		Pane:      "%42",
		CreatedAt: now.Add(-36 * time.Second),
	}, now, notifyDisplayStale, i18n.Locale("ko-KR"))

	stripped := stripANSI(label)
	for _, want := range []string{"36초 전", "오래됨", "창 1", "페인 42"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("label = %q, want localized formatter output %q", stripped, want)
		}
	}
}

func TestNotifySidebarGroupedReadModelConstructsCollapsedPaneRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "new", Text: "tests failed", Severity: notify.SeverityWarn, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex", "topic": "worker loop"}, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "old", Text: "background update", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex", "topic": "worker loop"}, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: now.Add(-5 * time.Minute)},
	}

	model := buildNotifySidebarReadModel(entries, now, nil, i18n.FallbackLocale)
	if len(model.Groups) != 1 {
		t.Fatalf("groups = %#v, want one pane group", model.Groups)
	}
	group := model.Groups[0]
	if group.Key != "pane\x00sock\x00main\x00%2" {
		t.Fatalf("group key = %q, want pane precedence key", group.Key)
	}
	if group.Count != 2 || group.Worst != notify.SeverityWarn || group.Display != notifyDisplayLive || group.Latest.ID != "new" {
		t.Fatalf("group = %+v, want count/worst/latest live group", group)
	}
	groupValue := notifySidebarGroupValue(group.Key)
	collapsed := model.CollapsedEntries()
	if len(collapsed) != 1 || collapsed[0].Value != groupValue {
		t.Fatalf("collapsed entries = %#v, want group value %q", collapsed, groupValue)
	}
	label := stripANSI(collapsed[0].Label)
	lines := strings.Split(label, "\n")
	if len(lines) != 3 {
		t.Fatalf("group label = %q, want three-line card", label)
	}
	if !strings.Contains(lines[0], "▸ Codex · worker loop") || !strings.Contains(lines[0], "1m ago") || strings.Contains(lines[0], "WARN") || strings.Contains(lines[0], "+1") {
		t.Fatalf("group row 1 = %q, want identity and age only", lines[0])
	}
	if !strings.Contains(lines[1], "tests failed") || !strings.Contains(lines[1], "+1") || !strings.Contains(lines[1], "WARN") {
		t.Fatalf("group row 2 = %q, want preview and aggregate metadata", lines[1])
	}
	if !strings.Contains(lines[2], "main") {
		t.Fatalf("group row 3 = %q, want project context", lines[2])
	}
	for _, forbidden := range []string{"2 notifications", "win 1", "pane 2", "%2", "@1"} {
		if strings.Contains(label, forbidden) {
			t.Fatalf("group label = %q, did not expect collapsed technical/count text %q", label, forbidden)
		}
	}
	for _, want := range []string{"▸ Codex · worker loop", "WARN", "1m ago", "main", "tests failed"} {
		if !strings.Contains(label, want) {
			t.Fatalf("group label = %q, want %q", label, want)
		}
	}

	expanded := model.ExpandedEntries(map[string]bool{group.Key: true})
	if len(expanded) != 3 || expanded[0].Value != groupValue || expanded[1].Value != "new" || expanded[2].Value != "old" {
		t.Fatalf("expanded entries = %#v, want group then newest-first child rows", expanded)
	}
	if !strings.Contains(stripANSI(expanded[0].Label), "▾ Codex · worker loop") {
		t.Fatalf("expanded group label = %q, want unfolded marker", expanded[0].Label)
	}
	if !strings.Contains(expanded[1].Label, " WARN ") || !strings.Contains(expanded[2].Label, " INFO ") {
		t.Fatalf("child labels = %#v, want existing severity badges preserved", expanded)
	}
}

func TestNotifySidebarGroupedReadModelTitleUsesOlderRowMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "latest", Text: "new sparse update", Severity: notify.SeverityInfo, Source: notify.SourceAI, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: now},
		{ID: "older", Text: "older rich update", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex", "topic": "worker loop"}, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: now.Add(-2 * time.Minute)},
	}

	model := buildNotifySidebarReadModel(entries, now, nil, i18n.FallbackLocale)
	collapsed := model.CollapsedEntries()
	if len(collapsed) != 1 || collapsed[0].Value != notifySidebarGroupValue(model.Groups[0].Key) {
		t.Fatalf("collapsed entries = %#v, want group value", collapsed)
	}
	label := stripANSI(collapsed[0].Label)
	if !strings.Contains(label, "▸ Codex · worker loop") {
		t.Fatalf("group label = %q, want agent/topic metadata from older same-pane row", label)
	}
}

func TestNotifySidebarGroupedReadModelReducesDuplicateLabelPreview(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "latest", Text: "worker loop", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex", "topic": "worker loop"}, Session: "main", Pane: "%2", CreatedAt: now},
	}

	model := buildNotifySidebarReadModel(entries, now, nil, i18n.FallbackLocale)
	collapsed := model.CollapsedEntries()
	if len(collapsed) != 1 {
		t.Fatalf("collapsed entries = %#v, want one group row", collapsed)
	}
	label := stripANSI(collapsed[0].Label)
	lines := strings.Split(label, "\n")
	if len(lines) != 3 {
		t.Fatalf("group label = %q, want three-line card", label)
	}
	if !strings.Contains(lines[0], "Codex · worker loop") {
		t.Fatalf("group row 1 = %q, want topic identity", lines[0])
	}
	if strings.Contains(lines[1], "worker loop") || !strings.Contains(lines[1], "Ready") {
		t.Fatalf("group row 2 = %q, want reduced non-duplicate preview", lines[1])
	}
}

func TestNotifySidebarGroupedReadModelUsesWorstSeverity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "latest", Text: "minor update", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", Pane: "%2", CreatedAt: now},
		{ID: "critical", Text: "approval required", Severity: notify.SeverityCritical, Source: notify.SourceExternal, Session: "main", Pane: "%2", CreatedAt: now.Add(-1 * time.Minute)},
	}
	model := buildNotifySidebarReadModel(entries, now, nil, i18n.FallbackLocale)
	if got := model.Groups[0].Worst; got != notify.SeverityCritical {
		t.Fatalf("worst severity = %q, want critical", got)
	}
	if label := model.CollapsedEntries()[0].Label; !strings.Contains(label, " CRIT ") {
		t.Fatalf("group label = %q, want CRIT badge", label)
	}
}

func TestNotifySidebarGroupedReadModelDisplaysStaleAndGoneGroups(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "ai:main:%2", Text: "Ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Metadata: map[string]string{"agent": "codex", "category": "response_complete", "state": "need"}, Session: "main", Pane: "%2", CreatedAt: now},
		{ID: "external", Text: "orphaned", Severity: notify.SeverityWarn, Source: notify.SourceExternal, Session: "", CreatedAt: now.Add(-1 * time.Minute)},
	}
	model := buildNotifySidebarReadModel(entries, now, map[string]notifyLivePane{}, i18n.FallbackLocale)
	if len(model.Groups) != 2 {
		t.Fatalf("groups = %#v, want stale and gone groups", model.Groups)
	}
	staleLines := strings.Split(stripANSI(model.Groups[0].Label), "\n")
	if model.Groups[0].Display != notifyDisplayStale || len(staleLines) < 2 || strings.Contains(staleLines[0], "STALE") || !strings.Contains(staleLines[1], "STALE") {
		t.Fatalf("stale group = %+v, want STALE display", model.Groups[0])
	}
	goneLines := strings.Split(stripANSI(model.Groups[1].Label), "\n")
	if model.Groups[1].Display != notifyDisplayGone || len(goneLines) < 2 || strings.Contains(goneLines[0], "GONE") || !strings.Contains(goneLines[1], "GONE") {
		t.Fatalf("gone group = %+v, want GONE display", model.Groups[1])
	}
}

func TestNotifySidebarGroupedReadModelPaneLessFallbackKeys(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := []notify.Notification{
		{ID: "window", Text: "window scoped", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Socket: "sock", Session: "main", Window: "@1", CreatedAt: now},
		{ID: "session", Text: "session scoped", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Socket: "sock", Session: "main", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "external", Text: "external scoped", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Socket: "sock", CreatedAt: now.Add(-2 * time.Minute)},
	}
	model := buildNotifySidebarReadModel(entries, now, nil, i18n.FallbackLocale)
	got := []string{model.Groups[0].Key, model.Groups[1].Key, model.Groups[2].Key}
	want := []string{"window\x00sock\x00main\x00@1", "session\x00sock\x00main", "external\x00sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("group keys = %#v, want %#v", got, want)
	}
	labels := []string{stripANSI(model.Groups[0].Label), stripANSI(model.Groups[1].Label), stripANSI(model.Groups[2].Label)}
	for i, wantLabel := range []string{"main", "main", "external"} {
		if !strings.Contains(labels[i], wantLabel) {
			t.Fatalf("label[%d] = %q, want fallback %q", i, labels[i], wantLabel)
		}
	}
	for i, label := range labels {
		for _, forbidden := range []string{"win 1", "pane 1", "@1", "%1"} {
			if strings.Contains(label, forbidden) {
				t.Fatalf("label[%d] = %q, did not expect collapsed technical id %q", i, label, forbidden)
			}
		}
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
		groupValue := notifySidebarGroupValue("pane\x00projmux\x00main\x000")
		if len(options.Items) != 1 || options.Items[0].Value != groupValue {
			t.Fatalf("native items = %#v, want group value %q", options.Items, groupValue)
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
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1"},
			{ID: "def", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%2"},
			{ID: "ghi", Text: "blocked", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%3"},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{
			{key: "a", value: "def", selectedIndex: 1},
			{key: "a", value: "abc", selectedIndex: 0},
		},
	}
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.native = picker
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
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	if len(picker.updates) != 2 {
		t.Fatalf("picker updates = %d, want 2", len(picker.updates))
	}
	abcGroup := notifySidebarGroupValue("pane\x00\x00main\x00%1")
	defGroup := notifySidebarGroupValue("pane\x00\x00main\x00%2")
	ghiGroup := notifySidebarGroupValue("pane\x00\x00main\x00%3")
	first := picker.options[0].Items
	if len(first) != 3 || first[0].Value != abcGroup || first[1].Value != defGroup || first[2].Value != ghiGroup {
		t.Fatalf("first picker entries = %#v, want abc then def then ghi", first)
	}
	compatOptions := intpickercompat.OptionsFromPicker(picker.options[0])
	if got, want := compatOptions.Bindings, []string{"esc:abort", "alt-2:abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("picker bindings = %#v, want %#v", got, want)
	}
	if got, want := compatOptions.ExpectKeys, []string{"enter", "a", "A", "x", "right", "left", "ctrl-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expect keys = %#v, want %#v", got, want)
	}
	second := picker.updates[0]
	if len(second) != 2 || second[0].Value != abcGroup || second[1].Value != ghiGroup {
		t.Fatalf("second picker entries = %#v, want abc then ghi", second)
	}
	third := picker.updates[1]
	if len(third) != 1 || third[0].Value != ghiGroup {
		t.Fatalf("third picker entries = %#v, want ghi", third)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no sidebar ack output", stdout.String())
	}
	if focusCalls := filterFocusCalls(runner.calls); len(focusCalls) != 0 {
		t.Fatalf("focus calls = %#v, want none", focusCalls)
	}
}

func TestNotifyListSidebarRightLeftFoldNavigation(t *testing.T) {
	t.Parallel()

	groupKey := "pane\x00\x00main\x00%1"
	groupValue := notifySidebarGroupValue(groupKey)
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "new", Text: "reply ready", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1", CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)},
			{ID: "old", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1", CreatedAt: time.Date(2026, time.May, 6, 11, 59, 0, 0, time.UTC)},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{
			{key: "right", value: groupValue, selectedIndex: 0},
			{key: "left", value: "old", selectedIndex: 2},
		},
	}
	cmd := newCmd(store)
	cmd.native = picker

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := len(picker.updates), 2; got != want {
		t.Fatalf("picker updates = %d, want %d", got, want)
	}
	expanded := picker.updates[0]
	if len(expanded) != 3 || expanded[0].Value != groupValue || expanded[1].Value != "new" || expanded[2].Value != "old" {
		t.Fatalf("expanded items = %#v, want group plus child rows", expanded)
	}
	if !strings.Contains(stripANSI(expanded[0].Label), "▾ ") {
		t.Fatalf("expanded group label = %q, want unfolded marker", expanded[0].Label)
	}
	folded := picker.updates[1]
	if len(folded) != 1 || folded[0].Value != groupValue {
		t.Fatalf("folded items = %#v, want only group row", folded)
	}
	if !strings.Contains(stripANSI(folded[0].Label), "▸ ") {
		t.Fatalf("folded group label = %q, want folded marker", folded[0].Label)
	}
}

func TestNotifyListSidebarEnterOnGroupUnfoldsAndChildEnterFocuses(t *testing.T) {
	t.Parallel()

	groupValue := notifySidebarGroupValue("pane\x00sock\x00main\x00%2")
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "new", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)},
			{ID: "old", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Socket: "sock", Session: "main", Window: "@1", Pane: "%2", CreatedAt: time.Date(2026, time.May, 6, 11, 59, 0, 0, time.UTC)},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{
			{key: "enter", value: groupValue, selectedIndex: 0},
			{key: "enter", value: "new", selectedIndex: 1},
		},
	}
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.native = picker
	cmd.runner = runner
	cmd.executable = func() (string, error) { return "/usr/local/bin/projmux", nil }

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"new", "old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want focused AI bulk ack %#v", got, want)
	}
	if len(picker.updates) != 1 || len(picker.updates[0]) != 3 {
		t.Fatalf("enter group updates = %#v, want one expanded update", picker.updates)
	}
	focusCalls := filterFocusCalls(runner.calls)
	if len(focusCalls) != 1 {
		t.Fatalf("focus calls = %#v, want one child focus call", focusCalls)
	}
}

func TestNotifyListSidebarEnterOnGroupDoesNotAckWhenNotFollowedByChild(t *testing.T) {
	t.Parallel()

	groupValue := notifySidebarGroupValue("pane\x00\x00main\x00%2")
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "new", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%2"},
			{ID: "old", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%2"},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "enter", value: groupValue, selectedIndex: 0}},
	}
	runner := &focusFakeRunner{}
	cmd := newCmd(store)
	cmd.native = picker
	cmd.runner = runner

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.ackedIDs) != 0 {
		t.Fatalf("ackedIDs = %#v, want none for group Enter unfold", store.ackedIDs)
	}
	if focusCalls := filterFocusCalls(runner.calls); len(focusCalls) != 0 {
		t.Fatalf("focus calls = %#v, want none for group Enter unfold", focusCalls)
	}
	if len(picker.updates) != 1 || len(picker.updates[0]) != 3 {
		t.Fatalf("updates = %#v, want expanded group", picker.updates)
	}
}

func TestNotifyListSidebarAckGroupRemovesVisibleMixedSeverityIncludingCritical(t *testing.T) {
	t.Parallel()

	groupValue := notifySidebarGroupValue("pane\x00\x00main\x00%9")
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "info", Text: "info", Severity: notify.SeverityInfo, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%9"},
			{ID: "critical", Text: "critical", Severity: notify.SeverityCritical, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%9"},
			{ID: "warn", Text: "warn", Severity: notify.SeverityWarn, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%9"},
			{ID: "other", Text: "other", Severity: notify.SeverityCritical, Source: notify.SourceExternal, Session: "main", Window: "@1", Pane: "%10"},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "A", value: groupValue, selectedIndex: 0}},
	}
	cmd := newCmd(store)
	cmd.native = picker

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"info", "critical", "warn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want group ack including critical %#v", got, want)
	}
	otherGroup := notifySidebarGroupValue("pane\x00\x00main\x00%10")
	if len(picker.updates) != 1 || len(picker.updates[0]) != 1 || picker.updates[0][0].Value != otherGroup {
		t.Fatalf("updates = %#v, want only other critical group remaining", picker.updates)
	}
}

func TestNotifyListSidebarXClearsNonCriticalAndPreservesCritical(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1"},
			{ID: "def", Text: "blocked", Severity: notify.SeverityCritical, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%2"},
			{ID: "ghi", Text: "warn", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%3"},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "x", value: "def", selectedIndex: 1}},
	}
	cmd := newCmd(store)
	cmd.native = picker
	cmd.runner = &focusFakeRunner{}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"abc", "ghi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	if len(picker.updates) != 1 {
		t.Fatalf("picker updates = %d, want 1", len(picker.updates))
	}
	second := picker.updates[0]
	defGroup := notifySidebarGroupValue("pane\x00\x00main\x00%2")
	if len(second) != 1 || second[0].Value != defGroup {
		t.Fatalf("second picker entries = %#v, want critical def preserved", second)
	}
	if second[0].Value == notifySidebarGroupValue("pane\x00\x00main\x00%1") || second[0].Value == notifySidebarGroupValue("pane\x00\x00main\x00%3") {
		t.Fatalf("second picker entries = %#v, want non-critical rows removed", second)
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
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "x", value: "abc", selectedIndex: 0}},
	}
	cmd := newCmd(store)
	cmd.native = picker

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"abc", "def"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	if len(picker.updates) != 1 {
		t.Fatalf("picker updates = %d, want 1", len(picker.updates))
	}
	second := picker.updates[0]
	if len(second) != 1 || second[0].Value != notifySidebarEmptyValue {
		t.Fatalf("second picker entries = %#v, want empty state", second)
	}
	if second[0].Label != "No pending notifications" {
		t.Fatalf("empty label = %q", second[0].Label)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want no sidebar clear output", stdout.String())
	}
}

func TestNotifyListSidebarSubscribesToQueueRefreshEvents(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{
		listEntries: []notify.Notification{{ID: "abc", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"}},
	}
	picker := &recordingNotifyNativePicker{}
	events := &stubNotifyQueueEvents{trigger: make(chan struct{}, 1)}
	cmd := newCmd(store)
	cmd.native = picker
	cmd.events = events

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if events.subscribeCalls != 1 {
		t.Fatalf("subscribe calls = %d, want 1", events.subscribeCalls)
	}
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	options := picker.options[0]
	if options.DeferredUpdate == nil {
		t.Fatal("DeferredUpdate is nil, want event-backed refresh path")
	}
	if options.DeferredUpdateTrigger != events.trigger {
		t.Fatalf("DeferredUpdateTrigger = %#v, want injected trigger", options.DeferredUpdateTrigger)
	}

	store.listEntries = append([]notify.Notification{
		{ID: "def", Text: "reply ready", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
	}, store.listEntries...)
	update, err := options.DeferredUpdate()
	if err != nil {
		t.Fatalf("DeferredUpdate() error = %v", err)
	}
	groupValue := notifySidebarGroupValue("session\x00\x00main")
	if len(update.Items) != 1 || update.Items[0].Value != groupValue || !strings.Contains(update.Items[0].Label, "+1") {
		t.Fatalf("update items = %#v, want one refreshed grouped row with def latest and compact extra count", update.Items)
	}
}

func TestNotifyListSidebarDeferredRefreshKeepsExpandedGroupAndPrunesGoneGroup(t *testing.T) {
	t.Parallel()

	groupValue := notifySidebarGroupValue("pane\x00\x00main\x00%1")
	store := &stubNotifyStore{
		listEntries: []notify.Notification{
			{ID: "new", Text: "reply ready", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1", CreatedAt: time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)},
			{ID: "old", Text: "deploy ok", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1", CreatedAt: time.Date(2026, time.May, 6, 11, 59, 0, 0, time.UTC)},
		},
	}
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "right", value: groupValue, selectedIndex: 0}},
	}
	events := &stubNotifyQueueEvents{trigger: make(chan struct{}, 1)}
	cmd := newCmd(store)
	cmd.native = picker
	cmd.events = events

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	options := picker.options[0]
	if options.DeferredUpdate == nil {
		t.Fatal("DeferredUpdate is nil, want event-backed refresh")
	}
	store.listEntries = append([]notify.Notification{
		{ID: "newer", Text: "newer", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%1", CreatedAt: time.Date(2026, time.May, 6, 12, 1, 0, 0, time.UTC)},
	}, store.listEntries...)
	update, err := options.DeferredUpdate()
	if err != nil {
		t.Fatalf("DeferredUpdate() error = %v", err)
	}
	if len(update.Items) != 4 || update.Items[0].Value != groupValue || update.Items[1].Value != "newer" {
		t.Fatalf("expanded deferred items = %#v, want expanded group with newer child", update.Items)
	}

	store.listEntries = []notify.Notification{{ID: "other", Text: "other", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", Window: "@1", Pane: "%2"}}
	update, err = options.DeferredUpdate()
	if err != nil {
		t.Fatalf("DeferredUpdate() after prune error = %v", err)
	}
	otherGroup := notifySidebarGroupValue("pane\x00\x00main\x00%2")
	if len(update.Items) != 1 || update.Items[0].Value != otherGroup {
		t.Fatalf("pruned deferred items = %#v, want only folded other group", update.Items)
	}
	if strings.Contains(stripANSI(update.Items[0].Label), "▾ ") {
		t.Fatalf("pruned group label = %q, did not expect stale expanded marker", update.Items[0].Label)
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
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "a", value: "def", selectedIndex: 1}},
	}
	cmd := newCmd(store)
	cmd.native = picker

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if got, want := store.ackedIDs, []string{"def"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ackedIDs = %#v, want %#v", got, want)
	}
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	if len(picker.updates) != 1 {
		t.Fatalf("picker updates = %d, want 1", len(picker.updates))
	}
	second := picker.updates[0]
	groupValue := notifySidebarGroupValue("session\x00\x00main")
	if len(second) != 1 || second[0].Value != groupValue {
		t.Fatalf("second picker entries = %#v, want only abc", second)
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
	picker := &recordingNotifyNativePicker{
		steps: []notifyNativeActionStep{{key: "a", value: "ai:main:%2", selectedIndex: 0}},
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
	cmd.native = picker
	cmd.runner = runner

	if err := cmd.Run([]string{"list", "--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if liveCalls != 2 {
		t.Fatalf("live list calls = %d, want 2", liveCalls)
	}
	if len(picker.options) != 1 {
		t.Fatalf("picker runs = %d, want 1", len(picker.options))
	}
	if len(picker.updates) != 1 {
		t.Fatalf("picker updates = %d, want 1", len(picker.updates))
	}
	second := picker.updates[0]
	groupValue := notifySidebarGroupValue("pane\x00\x00main\x00%3")
	if len(second) != 1 || second[0].Value != groupValue {
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
