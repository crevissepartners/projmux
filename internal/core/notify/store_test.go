package notify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "notify.json"))
}

func TestPushRoundTrip(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	in := PushInput{
		Text:     "deploy finished",
		Severity: SeverityInfo,
		Source:   SourceAI,
		TTL:      time.Hour,
		Target:   Target{Session: "s", Window: "1", Pane: "0"},
	}

	entry, result, err := store.Push(in)
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected auto-generated id")
	}
	if result.QueueLen != 1 {
		t.Fatalf("QueueLen = %d, want 1", result.QueueLen)
	}
	if result.Replaced {
		t.Fatal("did not expect Replaced")
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Text != in.Text {
		t.Fatalf("Text = %q, want %q", got.Text, in.Text)
	}
	if got.Session != "s" || got.Window != "1" || got.Pane != "0" {
		t.Fatalf("target = %+v", got)
	}
	if got.Source != SourceAI {
		t.Fatalf("Source = %q", got.Source)
	}
	if !got.ExpiresAt.After(got.CreatedAt) {
		t.Fatal("expected ExpiresAt after CreatedAt")
	}
	assertNotifyMode(t, filepath.Dir(store.Path()), 0o700)
	assertNotifyMode(t, store.Path(), 0o600)
}

func assertNotifyMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}

func TestPushRoundTripMetadataSanitizesAndCopies(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	metadata := map[string]string{
		" agent ": " codex ",
		"empty":   "",
		"":        "ignored",
	}
	if _, _, err := store.Push(PushInput{
		Text:     "Codex ready",
		Source:   SourceAI,
		Metadata: metadata,
		Target:   Target{Session: "s"},
	}); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	metadata[" agent "] = "mutated"

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := entries[0].Metadata; len(got) != 1 || got["agent"] != "codex" {
		t.Fatalf("Metadata = %#v, want sanitized copy", got)
	}
}

func TestPushDefaults(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	_, _, err := store.Push(PushInput{
		Text:   "hi",
		Target: Target{Session: "s"},
	})
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Severity != SeverityInfo {
		t.Fatalf("Severity = %q, want info", got.Severity)
	}
	if got.Source != SourceExternal {
		t.Fatalf("Source = %q, want external", got.Source)
	}
	if delta := got.ExpiresAt.Sub(got.CreatedAt); delta != DefaultTTL {
		t.Fatalf("default ttl delta = %s, want %s", delta, DefaultTTL)
	}
}

func TestPushDedupeByID(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	in := PushInput{
		ID:     "fixed-id",
		Text:   "first",
		Target: Target{Session: "s"},
		TTL:    time.Hour,
	}
	if _, _, err := store.Push(in); err != nil {
		t.Fatalf("first push: %v", err)
	}

	in.Text = "second"
	_, result, err := store.Push(in)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if result.QueueLen != 1 {
		t.Fatalf("QueueLen = %d, want 1", result.QueueLen)
	}
	if !result.Replaced {
		t.Fatal("expected Replaced=true")
	}
	if result.WasExpired {
		t.Fatal("expected WasExpired=false (entry was still live)")
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Text != "second" {
		t.Fatalf("Text = %q, want second", entries[0].Text)
	}
}

func TestPushDedupeRefreshesExpiredEntry(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	now := time.Now()
	store.SetClock(func() time.Time { return now })

	in := PushInput{
		ID:     "old",
		Text:   "first",
		Target: Target{Session: "s"},
		TTL:    time.Second,
	}
	if _, _, err := store.Push(in); err != nil {
		t.Fatalf("first push: %v", err)
	}

	now = now.Add(2 * time.Second) // expire the first entry

	in.Text = "second"
	in.TTL = time.Hour
	_, result, err := store.Push(in)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if result.QueueLen != 1 {
		t.Fatalf("QueueLen = %d, want 1", result.QueueLen)
	}
	if result.Replaced {
		t.Fatal("expected Replaced=false (entry had expired)")
	}
}

func TestListKeepsExpiredEntriesUntilAck(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	now := time.Now()
	store.SetClock(func() time.Time { return now })

	if _, _, err := store.Push(PushInput{Text: "old", Target: Target{Session: "s"}, TTL: time.Second}); err != nil {
		t.Fatalf("push old: %v", err)
	}

	now = now.Add(500 * time.Millisecond)
	if _, _, err := store.Push(PushInput{Text: "new", Target: Target{Session: "s"}, TTL: time.Hour}); err != nil {
		t.Fatalf("push new: %v", err)
	}

	now = now.Add(2 * time.Second) // first entry now expired

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].Text != "new" || entries[1].Text != "old" {
		t.Fatalf("order = %v, want new then old", []string{entries[0].Text, entries[1].Text})
	}
}

func TestListSortedRecencyDesc(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	now := time.Now()
	store.SetClock(func() time.Time { return now })

	for i, text := range []string{"a", "b", "c"} {
		now = now.Add(time.Second)
		if _, _, err := store.Push(PushInput{Text: text, Target: Target{Session: "s"}, TTL: time.Hour}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len = %d", len(entries))
	}
	if entries[0].Text != "c" || entries[1].Text != "b" || entries[2].Text != "a" {
		t.Fatalf("order = %v", []string{entries[0].Text, entries[1].Text, entries[2].Text})
	}
}

func TestAckRemovesEntry(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	entry, _, err := store.Push(PushInput{Text: "x", Target: Target{Session: "s"}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if err := store.Ack(entry.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len = %d, want 0", len(entries))
	}
}

func TestAckUnknownReturnsNotFound(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	err := store.Ack("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Ack err = %v, want ErrNotFound", err)
	}
}

func TestAckAllClearsQueue(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	for range 3 {
		if _, _, err := store.Push(PushInput{Text: "x", Target: Target{Session: "s"}, TTL: time.Hour}); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	removed, err := store.AckAll()
	if err != nil {
		t.Fatalf("AckAll: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len = %d", len(entries))
	}
}

func TestReconcileEvictionPolicy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	entry := func(id, session string, age, ttl time.Duration) Notification {
		createdAt := now.Add(-age)
		return Notification{
			ID:        id,
			Text:      id,
			Session:   session,
			CreatedAt: createdAt,
			ExpiresAt: createdAt.Add(ttl),
		}
	}

	tests := []struct {
		name         string
		entries      []Notification
		liveSessions map[string]bool
		inventory    bool
		maxEntries   int
		wantIDs      []string
		wantExpired  int
		wantOverflow int
	}{
		{
			name: "expired dead target removed",
			entries: []Notification{
				entry("dead-expired", "dead", 20*time.Minute, DefaultTTL),
				entry("live-expired", "live", 20*time.Minute, DefaultTTL),
			},
			liveSessions: map[string]bool{"live": true},
			inventory:    true,
			maxEntries:   10,
			wantIDs:      []string{"live-expired"},
			wantExpired:  1,
		},
		{
			name: "expired live and unexpired dead retained",
			entries: []Notification{
				entry("live-expired", "live", 20*time.Minute, DefaultTTL),
				entry("dead-fresh", "dead", time.Minute, DefaultTTL),
			},
			liveSessions: map[string]bool{"live": true},
			inventory:    true,
			maxEntries:   10,
			wantIDs:      []string{"live-expired", "dead-fresh"},
		},
		{
			name: "missing inventory skips ttl eviction",
			entries: []Notification{
				entry("dead-expired", "dead", 20*time.Minute, DefaultTTL),
			},
			maxEntries: 10,
			wantIDs:    []string{"dead-expired"},
		},
		{
			name: "hard cap removes only oldest excess",
			entries: []Notification{
				entry("oldest", "live", 4*time.Minute, time.Hour),
				entry("newest", "live", time.Minute, time.Hour),
				entry("middle-new", "live", 2*time.Minute, time.Hour),
				entry("middle-old", "live", 3*time.Minute, time.Hour),
			},
			liveSessions: map[string]bool{"live": true},
			inventory:    true,
			maxEntries:   2,
			wantIDs:      []string{"newest", "middle-new"},
			wantOverflow: 2,
		},
		{
			name: "ttl eviction runs before hard cap",
			entries: []Notification{
				entry("dead-expired", "dead", 20*time.Minute, DefaultTTL),
				entry("old-live", "live", 4*time.Minute, time.Hour),
				entry("new-live", "live", time.Minute, time.Hour),
			},
			liveSessions: map[string]bool{"live": true},
			inventory:    true,
			maxEntries:   2,
			wantIDs:      []string{"old-live", "new-live"},
			wantExpired:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var targetExists TargetExistsFunc
			if tt.inventory {
				targetExists = func(entry Notification) bool {
					return tt.liveSessions[entry.Session]
				}
			}
			got, result := reconcileEntries(tt.entries, now, targetExists, tt.maxEntries)
			gotIDs := make([]string, 0, len(got))
			for _, item := range got {
				gotIDs = append(gotIDs, item.ID)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tt.wantIDs, ",") {
				t.Fatalf("ids = %v, want %v", gotIDs, tt.wantIDs)
			}
			if result.ExpiredGone != tt.wantExpired || result.Overflow != tt.wantOverflow || result.QueueLen != len(tt.wantIDs) {
				t.Fatalf("result = %+v, want expired=%d overflow=%d len=%d", result, tt.wantExpired, tt.wantOverflow, len(tt.wantIDs))
			}
		})
	}
}

func TestStoreReconcilePersistsBoundedQueue(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	entries := make([]Notification, 0, MaxQueueEntries+2)
	for i := range MaxQueueEntries + 2 {
		createdAt := now.Add(time.Duration(i) * time.Second)
		entries = append(entries, Notification{
			ID:        fmt.Sprintf("entry-%03d", i),
			Text:      "pending",
			Session:   "live",
			CreatedAt: createdAt,
			ExpiresAt: createdAt.Add(time.Hour),
		})
	}
	if err := store.withLock(func() error { return store.write(entries) }); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	store.SetClock(func() time.Time { return now })

	result, err := store.Reconcile(func(Notification) bool { return true })
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.ExpiredGone != 0 || result.Overflow != 2 || result.QueueLen != MaxQueueEntries {
		t.Fatalf("result = %+v", result)
	}
	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != MaxQueueEntries {
		t.Fatalf("queue length = %d, want %d", len(got), MaxQueueEntries)
	}
	if got[len(got)-1].ID != "entry-002" {
		t.Fatalf("oldest retained id = %q, want entry-002", got[len(got)-1].ID)
	}
}

func TestPushTruncatesText(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	long := strings.Repeat("a", MaxTextLength+25)
	entry, _, err := store.Push(PushInput{Text: long, Target: Target{Session: "s"}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len([]rune(entry.Text)) != MaxTextLength {
		t.Fatalf("text len = %d, want %d", len([]rune(entry.Text)), MaxTextLength)
	}
}

func TestPushRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   PushInput
		want error
	}{
		{"empty text", PushInput{Text: "  ", Target: Target{Session: "s"}}, ErrInvalidText},
		{"missing session", PushInput{Text: "x", Target: Target{}}, ErrInvalidTarget},
		{"bad severity", PushInput{Text: "x", Target: Target{Session: "s"}, Severity: "loud"}, ErrInvalidSeverity},
		{"bad source", PushInput{Text: "x", Target: Target{Session: "s"}, Source: "weird"}, ErrInvalidSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			_, _, err := store.Push(tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParseTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want Target
		err  bool
	}{
		{"s", Target{Session: "s"}, false},
		{"s:1", Target{Session: "s", Window: "1"}, false},
		{"s:1.0", Target{Session: "s", Window: "1", Pane: "0"}, false},
		{"s:@5.%7", Target{Session: "s", Window: "@5", Pane: "%7"}, false},
		{"", Target{}, true},
		{":1", Target{}, true},
		{"s:", Target{}, true},
		{"s:1.", Target{}, true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.in)
		if c.err {
			if err == nil {
				t.Fatalf("ParseTarget(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseTarget(%q) error = %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseTarget(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestConcurrentPushFileLockContention(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var failures int32

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			_, _, err := store.Push(PushInput{
				Text:   "concurrent",
				Target: Target{Session: "s"},
				TTL:    time.Hour,
			})
			if err != nil {
				atomic.AddInt32(&failures, 1)
			}
		}(i)
	}
	wg.Wait()

	if failures != 0 {
		t.Fatalf("concurrent pushes had %d failures", failures)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(entries) != goroutines {
		t.Fatalf("queue length = %d, want %d", len(entries), goroutines)
	}
}

func TestStoreUsesDefaultStateDirPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	paths := struct {
		ConfigDir string
		StateDir  string
	}{StateDir: dir}
	store := NewStore(filepath.Join(paths.StateDir, NotifyFileName))
	if got, want := filepath.Base(store.Path()), NotifyFileName; got != want {
		t.Fatalf("Path basename = %q, want %q", got, want)
	}
}
