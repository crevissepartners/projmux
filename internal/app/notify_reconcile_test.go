package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/notify"
)

// reconcileTmuxRunner is a fake tmux runner that returns a fixed payload
// for `tmux list-panes -a -F <fmt>` and records every invocation. The
// fake intentionally ignores other tmux commands so it can be reused
// across subtests without surprising the cases that don't shell out to
// list-panes.
type reconcileTmuxRunner struct {
	output []byte
	err    error
	calls  int
}

// reconcilePaneRow renders one live pane row in the attention list-panes
// format the livePaneLister seam parses: session, window, pane, active,
// title, attention state, ai state, agent, topic, socket.
func reconcilePaneRow(session, window, pane, attention, ai, agent, topic, socket string) []byte {
	return notifyLivePaneRows([]string{session, window, pane, "0", "", attention, ai, agent, topic, socket})
}

func (r *reconcileTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "tmux" && len(args) >= 2 && args[0] == "list-panes" && args[1] == "-a" {
		r.calls++
		return r.output, r.err
	}
	return nil, nil
}

// memNotifyStore is an in-memory notify store with the same surface as the
// production *notify.Store. It is intentionally simpler than the disk
// store: every Push replaces by id, every Ack removes by id, every List
// returns a copy.
type memNotifyStore struct {
	entries []notify.Notification
	pushed  []notify.PushInput
	acks    []string
}

func (s *memNotifyStore) Push(in notify.PushInput) (notify.Notification, notify.PushResult, error) {
	s.pushed = append(s.pushed, in)
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entry := notify.Notification{
		ID:        in.ID,
		Text:      in.Text,
		Severity:  in.Severity,
		Source:    in.Source,
		Metadata:  in.Metadata,
		Socket:    in.Target.Socket,
		Session:   in.Target.Session,
		Window:    in.Target.Window,
		Pane:      in.Target.Pane,
		CreatedAt: now,
		ExpiresAt: now.Add(in.TTL),
	}
	for i, e := range s.entries {
		if e.ID == in.ID {
			s.entries[i] = entry
			return entry, notify.PushResult{ID: in.ID, QueueLen: len(s.entries), Replaced: true}, nil
		}
	}
	s.entries = append(s.entries, entry)
	return entry, notify.PushResult{ID: in.ID, QueueLen: len(s.entries)}, nil
}

func (s *memNotifyStore) List() ([]notify.Notification, error) {
	out := make([]notify.Notification, len(s.entries))
	copy(out, s.entries)
	return out, nil
}

func (s *memNotifyStore) Ack(id string) error {
	s.acks = append(s.acks, id)
	for i, e := range s.entries {
		if e.ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return nil
		}
	}
	return notify.ErrNotFound
}

func (s *memNotifyStore) AckAll() (int, error) {
	n := len(s.entries)
	s.entries = nil
	return n, nil
}

func (s *memNotifyStore) Reconcile(targetExists notify.TargetExistsFunc) (notify.ReconcileResult, error) {
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	result := notify.ReconcileResult{}
	kept := make([]notify.Notification, 0, len(s.entries))
	for _, entry := range s.entries {
		expired := !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now)
		if expired && targetExists != nil && !targetExists(entry) {
			result.ExpiredGone++
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) > notify.MaxQueueEntries {
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].CreatedAt.Equal(kept[j].CreatedAt) {
				return kept[i].ID > kept[j].ID
			}
			return kept[i].CreatedAt.After(kept[j].CreatedAt)
		})
		result.Overflow = len(kept) - notify.MaxQueueEntries
		kept = kept[:notify.MaxQueueEntries]
	}
	s.entries = kept
	result.QueueLen = len(kept)
	return result, nil
}

func newReconcileCmd(store notifyStore, runner tmuxRunner) *notifyCommand {
	return &notifyCommand{
		store:     store,
		now:       func() time.Time { return time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC) },
		runner:    runner,
		livePanes: newAttentionLivePaneLister(runner),
	}
}

func TestNotifyReconcilePushesMissingEntryForReplyPane(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("main", "@4", "%16", "reply", "waiting", "claude", "notify wiring", "/tmp/tmux-1000/projmux"),
	}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run error = %v (stderr=%q)", err, stderr.String())
	}

	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	in := store.pushed[0]
	if in.ID != "ai:main:%16" {
		t.Fatalf("ID = %q, want ai:main:%%16", in.ID)
	}
	if in.Text != "notify wiring" {
		t.Fatalf("Text = %q", in.Text)
	}
	if in.Metadata["agent"] != "claude" || in.Metadata["category"] != "response_complete" || in.Metadata["topic"] != "notify wiring" {
		t.Fatalf("Metadata = %#v", in.Metadata)
	}
	if in.Source != notify.SourceAI || in.Severity != notify.SeverityInfo {
		t.Fatalf("Source/Severity = %q/%q", in.Source, in.Severity)
	}
	if in.TTL != attentionNotifyTTL {
		t.Fatalf("TTL = %s, want %s", in.TTL, attentionNotifyTTL)
	}
	if in.Target.Session != "main" || in.Target.Window != "@4" || in.Target.Pane != "%16" {
		t.Fatalf("Target = %+v", in.Target)
	}
	if in.Target.Socket != "/tmp/tmux-1000/projmux" {
		t.Fatalf("Socket = %q", in.Target.Socket)
	}
	if got := stdout.String(); got != "reconcile: pushed 1, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcilePublishesQueueRefreshBestEffort(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("main", "@4", "%16", "reply", "waiting", "claude", "notify wiring", "/tmp/tmux-1000/projmux"),
	}
	store := &memNotifyStore{}
	events := &stubNotifyQueueEvents{publishErr: errors.New("listener unavailable")}
	cmd := newReconcileCmd(store, runner)
	cmd.events = events

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want queue write to succeed", len(store.pushed))
	}
	if events.publishCalls != 1 {
		t.Fatalf("publish calls = %d, want 1", events.publishCalls)
	}
	if got := stdout.String(); got != "reconcile: pushed 1, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileNoOpWhenQueueAndPanesAlreadyAgree(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{output: []byte("")}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 0 || len(store.acks) != 0 {
		t.Fatalf("expected no-op, pushed=%d acks=%d", len(store.pushed), len(store.acks))
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileReportsStaleEntryWhenPaneNoLongerReply(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	runner := &reconcileTmuxRunner{
		// Pane %16 is still alive but no longer in reply state.
		output: reconcilePaneRow("main", "@4", "%16", "", "idle", "claude", "", "/tmp/tmux/default"),
	}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{
				ID:        "ai:main:%16",
				Text:      "notify wiring",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "main",
				Pane:      "%16",
				CreatedAt: now,
				ExpiresAt: now.Add(attentionNotifyTTL),
			},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.acks) != 0 {
		t.Fatalf("acks = %v, want none", store.acks)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %+v, want retained stale row", store.entries)
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 0, stale 1, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileReportsStaleEntryWhenPaneGone(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	// list-panes returns no panes at all (pane was killed).
	runner := &reconcileTmuxRunner{output: []byte("")}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{
				ID:        "ai:main:%99",
				Text:      "notify wiring",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "main",
				Pane:      "%99",
				CreatedAt: now,
				ExpiresAt: now.Add(attentionNotifyTTL),
			},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.acks) != 0 {
		t.Fatalf("acks = %v, want none", store.acks)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %+v, want retained stale row", store.entries)
	}
}

func TestNotifyReconcileEvictsOnlyExpiredGoneTargets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	fresh := now.Add(time.Minute)
	runner := &reconcileTmuxRunner{
		// The pane exists but is inactive; its expired row must remain pending.
		output: reconcilePaneRow("live", "@4", "%16", "", "idle", "claude", "", "/tmp/tmux/default"),
	}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{ID: "ai:live:%16", Text: "live expired", Source: notify.SourceAI, Session: "live", Pane: "%16", CreatedAt: now.Add(-time.Hour), ExpiresAt: expired},
			{ID: "ai:dead:%99", Text: "dead expired", Source: notify.SourceAI, Session: "dead", Pane: "%99", CreatedAt: now.Add(-time.Hour), ExpiresAt: expired},
			{ID: "ai:dead:%98", Text: "dead fresh", Source: notify.SourceAI, Session: "dead", Pane: "%98", CreatedAt: now, ExpiresAt: fresh},
			{ID: "external:dead", Text: "session expired", Source: notify.SourceExternal, Session: "dead", CreatedAt: now.Add(-time.Hour), ExpiresAt: expired},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	gotIDs := make([]string, 0, len(store.entries))
	for _, entry := range store.entries {
		gotIDs = append(gotIDs, entry.ID)
	}
	if got, want := strings.Join(gotIDs, ","), "ai:live:%16,ai:dead:%98"; got != want {
		t.Fatalf("remaining ids = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 0, stale 2, evicted 2\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileCapsQueueAfterBackfillPush(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	entries := make([]notify.Notification, 0, notify.MaxQueueEntries)
	for i := range notify.MaxQueueEntries {
		createdAt := now.Add(-time.Duration(notify.MaxQueueEntries-i) * time.Minute)
		entries = append(entries, notify.Notification{
			ID:        fmt.Sprintf("external:%03d", i),
			Text:      "pending",
			Source:    notify.SourceExternal,
			Session:   "live",
			CreatedAt: createdAt,
			ExpiresAt: now.Add(time.Hour),
		})
	}
	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("live", "@4", "%16", "reply", "waiting", "codex", "ready", "/tmp/tmux/default"),
	}
	store := &memNotifyStore{entries: entries}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.entries) != notify.MaxQueueEntries {
		t.Fatalf("queue length = %d, want %d", len(store.entries), notify.MaxQueueEntries)
	}
	if len(store.pushed) != 1 || store.pushed[0].ID != "ai:live:%16" {
		t.Fatalf("pushed = %+v", store.pushed)
	}
	foundBackfill := false
	foundOldest := false
	for _, entry := range store.entries {
		foundBackfill = foundBackfill || entry.ID == "ai:live:%16"
		foundOldest = foundOldest || entry.ID == "external:000"
	}
	if !foundBackfill || foundOldest {
		t.Fatalf("backfill retained=%v oldest retained=%v", foundBackfill, foundOldest)
	}
	if got := stdout.String(); got != "reconcile: pushed 1, acked 0, kept 0, stale 0, evicted 1\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileKeepsMatchingEntryWithoutDuplicatePush(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("main", "@4", "%16", "reply", "waiting", "claude", "notify wiring", "/tmp/tmux/default"),
	}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{
				ID:        "ai:main:%16",
				Text:      "notify wiring",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Metadata:  map[string]string{"agent": "claude", "category": "response_complete", "state": "need", "topic": "notify wiring"},
				Session:   "main",
				Pane:      "%16",
				CreatedAt: now,
				ExpiresAt: now.Add(attentionNotifyTTL),
			},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0 (entry already matches)", len(store.pushed))
	}
	if len(store.acks) != 0 {
		t.Fatalf("acks = %v, want none", store.acks)
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 1, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileRefreshesEntryWithStaleText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	runner := &reconcileTmuxRunner{
		// Topic on pane is "new topic" but queued entry still has old text.
		output: reconcilePaneRow("main", "@4", "%16", "reply", "", "claude", "new topic", ""),
	}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{
				ID:        "ai:main:%16",
				Text:      "old topic",
				Severity:  notify.SeverityInfo,
				Source:    notify.SourceAI,
				Session:   "main",
				Pane:      "%16",
				CreatedAt: now,
				ExpiresAt: now.Add(attentionNotifyTTL),
			},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count = %d, want 1", len(store.pushed))
	}
	if got := store.pushed[0].Text; got != "new topic" {
		t.Fatalf("Text = %q", got)
	}
	if got := store.pushed[0].Metadata["topic"]; got != "new topic" {
		t.Fatalf("topic metadata = %q", got)
	}
	if got := stdout.String(); got != "reconcile: pushed 1, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileSkipsPaneWithoutAgent(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{
		// Pane is in reply state but has no AI agent (manual attention toggle
		// on a shell pane). Producer skips it; reconcile mirrors that.
		output: reconcilePaneRow("main", "@4", "%5", "reply", "", "", "shell topic", ""),
	}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.pushed) != 0 {
		t.Fatalf("push count = %d, want 0", len(store.pushed))
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileLeavesNonAIQueueEntriesUntouched(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	runner := &reconcileTmuxRunner{output: []byte("")}
	store := &memNotifyStore{
		entries: []notify.Notification{
			{
				ID:        "k8s:cluster:pod-foo",
				Text:      "pod restarted",
				Severity:  notify.SeverityWarn,
				Source:    notify.SourceK8s,
				Session:   "main",
				CreatedAt: now,
				ExpiresAt: now.Add(time.Hour),
			},
		},
	}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if len(store.acks) != 0 {
		t.Fatalf("acks = %v, want none (non-AI entry must be left alone)", store.acks)
	}
	if got := stdout.String(); got != "reconcile: pushed 0, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestNotifyReconcileIdempotent(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("main", "@4", "%16", "reply", "waiting", "claude", "notify wiring", "/tmp/tmux/default"),
	}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var first bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &first, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Run error = %v", err)
	}
	if got := first.String(); got != "reconcile: pushed 1, acked 0, kept 0, stale 0, evicted 0\n" {
		t.Fatalf("first stdout = %q", got)
	}

	var second bytes.Buffer
	if err := cmd.Run([]string{"reconcile"}, &second, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run error = %v", err)
	}
	if got := second.String(); got != "reconcile: pushed 0, acked 0, kept 1, stale 0, evicted 0\n" {
		t.Fatalf("second stdout = %q", got)
	}
	if len(store.pushed) != 1 {
		t.Fatalf("push count after re-run = %d, want 1", len(store.pushed))
	}
}

func TestNotifyReconcileJSONOutput(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{
		output: reconcilePaneRow("main", "@4", "%16", "reply", "waiting", "claude", "", ""),
	}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var got reconcileResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode json: %v (raw=%q)", err, stdout.String())
	}
	if got.Pushed != 1 || got.Acked != 0 || got.Kept != 0 {
		t.Fatalf("decoded = %+v", got)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("Errors = %v", got.Errors)
	}
}

func TestNotifyReconcileTmuxFailureSurfacesAsSoftError(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{err: errFakeTmux}
	store := &memNotifyStore{}
	cmd := newReconcileCmd(store, runner)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"reconcile", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	var got reconcileResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(got.Errors) == 0 {
		t.Fatalf("expected error in summary, got %+v", got)
	}
	if !strings.Contains(got.Errors[0], "tmux list-panes") {
		t.Fatalf("Errors[0] = %q", got.Errors[0])
	}
	if got.Pushed != 0 || got.Acked != 0 {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestNotifyReconcileRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	runner := &reconcileTmuxRunner{}
	cmd := newReconcileCmd(&memNotifyStore{}, runner)
	err := cmd.Run([]string{"reconcile", "extra"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsUsageError(err) {
		t.Fatalf("expected UsageError, got %T: %v", err, err)
	}
}

func TestNotifyReconcileHelpDescribesRecoveryPath(t *testing.T) {
	t.Parallel()

	cmd := newReconcileCmd(&memNotifyStore{}, &reconcileTmuxRunner{})
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"reconcile", "--help"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Repair the pending AI notify queue") {
		t.Fatalf("stderr = %q, want recovery path", stderr.String())
	}
}

// errFakeTmux is a sentinel for the reconcile soft-error path.
var errFakeTmux = errFakeTmuxImpl{}

type errFakeTmuxImpl struct{}

func (errFakeTmuxImpl) Error() string { return "fake tmux failure" }
