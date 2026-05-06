package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

func newStatusNotifyCommand(store notifyStore) *statusCommand {
	cmd := testStatusCommand("/tmp")
	cmd.notifyStoreFn = func() (notifyStore, error) { return store, nil }
	return cmd
}

func TestStatusNotifyEmptyQueueIsEmptyOutput(t *testing.T) {
	t.Parallel()

	cmd := newStatusNotifyCommand(&stubNotifyStore{})
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

func TestStatusNotifyInfoEntryOmitsColorPrefix(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "hello", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "[I] hello") {
		t.Fatalf("stdout = %q, want prefix '[I] hello'", got)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("stdout = %q, want suffix '#[default]'", got)
	}
	if strings.HasPrefix(got, "#[fg=") {
		t.Fatalf("info severity should not emit a color prefix; got %q", got)
	}
	if !strings.Contains(got, "main") {
		t.Fatalf("stdout = %q, want session 'main'", got)
	}
}

func TestStatusNotifyWarnEntryEmitsYellow(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "warn-me", Severity: notify.SeverityWarn, Source: notify.SourceAI, Session: "ops"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[fg=yellow][W] warn-me") {
		t.Fatalf("stdout = %q, want yellow warn prefix", got)
	}
}

func TestStatusNotifyCriticalEntryEmitsRedBold(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "boom", Severity: notify.SeverityCritical, Source: notify.SourceAI, Session: "prod"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.HasPrefix(got, "#[fg=red,bold][!] boom") {
		t.Fatalf("stdout = %q, want red,bold critical prefix", got)
	}
}

func TestStatusNotifyMultipleEntriesAppendsCount(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "a", Text: "newest", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
		{ID: "b", Text: "older1", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
		{ID: "c", Text: "older2", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "+2") {
		t.Fatalf("stdout = %q, want suffix '+2'", got)
	}
	if !strings.Contains(got, "[I] newest") {
		t.Fatalf("stdout = %q, want '[I] newest'", got)
	}
}

func TestStatusNotifyMaxWidthTruncatesInnerTextOnly(t *testing.T) {
	t.Parallel()

	store := &stubNotifyStore{listEntries: []notify.Notification{
		{
			ID:       "a",
			Text:     "this text is intentionally pretty long",
			Severity: notify.SeverityInfo,
			Source:   notify.SourceAI,
			Session:  "main",
		},
		{ID: "b", Text: "x", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main"},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify", "--max-width", "30"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	// Strip the trailing reset directive and compute the visible-ish length.
	visible := strings.TrimSuffix(got, "#[default]")
	if len([]rune(visible)) > 30 {
		t.Fatalf("rendered length = %d, want <= 30 (got %q)", len([]rune(visible)), got)
	}
	if !strings.HasPrefix(got, "[I] ") {
		t.Fatalf("stdout = %q, want severity prefix kept", got)
	}
	if !strings.Contains(got, "+1") {
		t.Fatalf("stdout = %q, want '+N' suffix kept", got)
	}
	if !strings.Contains(got, "main") {
		t.Fatalf("stdout = %q, want session suffix kept", got)
	}
}

func TestStatusNotifyMissingStoreFactoryReturnsEmpty(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand("/tmp")
	cmd.notifyStoreFn = nil
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestStatusNotifyStableTimestampOrdering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	store := &stubNotifyStore{listEntries: []notify.Notification{
		{ID: "newer", Text: "newer", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", CreatedAt: now},
		{ID: "older", Text: "older", Severity: notify.SeverityInfo, Source: notify.SourceAI, Session: "main", CreatedAt: now.Add(-time.Minute)},
	}}
	cmd := newStatusNotifyCommand(store)
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"notify"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "[I] newer") {
		t.Fatalf("stdout = %q, want newest entry first", got)
	}
}
