package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

type stubNotifyStore struct {
	pushed     []notify.PushInput
	pushResult notify.PushResult
	pushEntry  notify.Notification
	pushErr    error

	listEntries []notify.Notification
	listErr     error

	ackedID  string
	ackErr   error
	ackAll   int
	ackAllOK bool
}

func (s *stubNotifyStore) Push(in notify.PushInput) (notify.Notification, notify.PushResult, error) {
	s.pushed = append(s.pushed, in)
	return s.pushEntry, s.pushResult, s.pushErr
}

func (s *stubNotifyStore) List() ([]notify.Notification, error) {
	return append([]notify.Notification(nil), s.listEntries...), s.listErr
}

func (s *stubNotifyStore) Ack(id string) error {
	s.ackedID = id
	return s.ackErr
}

func (s *stubNotifyStore) AckAll() (int, error) {
	s.ackAllOK = true
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
}
