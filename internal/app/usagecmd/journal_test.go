package usagecmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// journalHarness wires a Command to a private operations journal under a temp
// directory and lets the test read the rows back the way `projmux diagnostics
// log` does.
type journalHarness struct {
	t     *testing.T
	cmd   *Command
	store *diagnostics.Store
	path  string
}

func newJournalHarness(t *testing.T, adapters ...*stubAdapter) *journalHarness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "operations.jsonl")
	store := diagnostics.NewStore(path)

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	for _, adapter := range adapters {
		if err := registry.Replace(adapter); err != nil {
			t.Fatal(err)
		}
	}
	mgr := usage.NewManager(registry, usage.NewStore(filepath.Join(dir, "usage")), func() time.Time { return now })

	cmd := New(func() time.Time { return now })
	cmd.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }
	cmd.journalFn = func() *diagnostics.UsageRecorder {
		return diagnostics.NewUsageRecorder(store, "journaltestrun", "0.0.0-test", diagnostics.MuxBackend())
	}
	return &journalHarness{t: t, cmd: cmd, store: store, path: path}
}

// tail mirrors `projmux diagnostics log --component usage --tail n`.
func (h *journalHarness) tail(n int) []diagnostics.Event {
	h.t.Helper()
	events, err := h.store.Read()
	if err != nil {
		h.t.Fatalf("read journal: %v", err)
	}
	filtered := make([]diagnostics.Event, 0, len(events))
	for _, event := range events {
		if event.Component == "usage" {
			filtered = append(filtered, event)
		}
	}
	if len(filtered) > n {
		filtered = filtered[len(filtered)-n:]
	}
	return filtered
}

func (h *journalHarness) run(args ...string) {
	h.t.Helper()
	if err := h.cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		h.t.Fatalf("Run(%v): %v", args, err)
	}
}

// TestUsageCollectFailureLandsInJournal covers the whole-adapter failure: the
// row names the provider and a closed failure enum, and nothing else.
func TestUsageCollectFailureLandsInJournal(t *testing.T) {
	t.Parallel()

	h := newJournalHarness(t, &stubAdapter{name: "claude", err: errors.New("claude: credentials not found at /home/secret/.claude/.credentials.json")})
	h.run("--model", "claude")

	rows := h.tail(10)
	if len(rows) != 1 {
		t.Fatalf("journal rows = %#v, want exactly one", rows)
	}
	row := rows[0]
	if row.Event != "usage.collect.outcome" || row.Component != "usage" {
		t.Fatalf("row family = %q/%q", row.Component, row.Event)
	}
	if row.Provider != string(diagnostics.ProviderClaude) {
		t.Fatalf("row provider = %q, want claude", row.Provider)
	}
	if row.Failure != string(diagnostics.UsageFailureCollect) {
		t.Fatalf("row failure = %q, want collect-failed", row.Failure)
	}
	if row.Level != "error" || row.Result != "error" || row.Kind != "runtime" {
		t.Fatalf("hard failure must be an error row: %#v", row)
	}
	// Privacy: the adapter's own error text never enters the journal.
	if row.Message != "" {
		t.Fatalf("row carried a free-form message: %q", row.Message)
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("credentials")) {
		t.Fatalf("journal leaked adapter error text: %s", raw)
	}
}

// TestUsagePartialCollectLandsInJournalAsRowsSkipped covers the partial case:
// rows survived, so the record is an informational anomaly rather than an
// error, and it is still attributed to the right provider.
func TestUsagePartialCollectLandsInJournalAsRowsSkipped(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	h := newJournalHarness(t, &stubAdapter{
		name:  "claude",
		snaps: []usage.Snapshot{{Model: "claude", Window: usage.Window5h, Pct: 30, ResetsAt: now.Add(time.Hour), UpdatedAt: now}},
		err:   usage.RowSkipWarning([]string{"row 2: missing resets_at"}),
	})
	h.run("--model", "claude")

	rows := h.tail(10)
	if len(rows) != 1 {
		t.Fatalf("journal rows = %#v, want exactly one", rows)
	}
	row := rows[0]
	if row.Failure != string(diagnostics.UsageFailureRowsSkipped) {
		t.Fatalf("row failure = %q, want rows-skipped", row.Failure)
	}
	if row.Level != "info" || row.Result != "success" || row.Kind != "" {
		t.Fatalf("partial failure must stay informational: %#v", row)
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("resets_at")) {
		t.Fatalf("journal copied the adapter warning text: %s", raw)
	}
}

// TestUsageSuccessfulCollectProducesNoJournalRows keeps the healthy steady
// state silent — the journal records anomalies, not every refresh.
func TestUsageSuccessfulCollectProducesNoJournalRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	h := newJournalHarness(t, &stubAdapter{
		name:  "claude",
		snaps: []usage.Snapshot{{Model: "claude", Window: usage.Window5h, Pct: 30, ResetsAt: now.Add(time.Hour), UpdatedAt: now}},
	})
	h.run("--model", "claude")
	h.run("--model", "claude")

	if rows := h.tail(10); len(rows) != 0 {
		t.Fatalf("healthy collect wrote %#v, want zero journal rows", rows)
	}
	if _, err := os.Stat(h.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("healthy collect touched the journal file: %v", err)
	}
}

func TestCodexFallbackAndLastKnownGoodDiagnosticsExposeClosedSourceReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	fallback := usage.Snapshot{
		Model: "codex", Window: usage.Window5h, Pct: 19, UpdatedAt: now,
		Source: usage.SourceRollout, FallbackReason: usage.ReasonAppServerUnsupported,
	}
	if label, _ := compactModelDisplayLabels(fallback); label != "Codex [fallback]" {
		t.Fatalf("fallback compact identity = %q", label)
	}
	h := newJournalHarness(t, &stubAdapter{name: "codex", snaps: []usage.Snapshot{fallback}})
	h.run("--model", "codex")
	rows := h.tail(10)
	if len(rows) != 1 || rows[0].Source != string(diagnostics.UsageSourceRollout) ||
		rows[0].Failure != string(diagnostics.UsageFailureAppServerUnsupported) {
		t.Fatalf("fallback diagnostics = %#v", rows)
	}

	stale := fallback
	stale.StaleReason = usage.ReasonAppServerDisconnected
	if label, _ := compactModelDisplayLabels(stale); label != "Codex [stale]" {
		t.Fatalf("last-known-good compact identity = %q", label)
	}
	h.cmd.recordCollectDiagnostics(
		&usage.AdapterError{Model: "codex", Err: &usage.StaleReasonError{
			Reason: usage.ReasonAppServerDisconnected,
			Err:    errors.New("private transport detail"),
		}},
		[]usage.Snapshot{stale}, now,
	)
	rows = h.tail(10)
	if len(rows) != 2 || rows[1].Source != string(diagnostics.UsageSourceLastKnownGood) ||
		rows[1].Failure != string(diagnostics.UsageFailureAppServerDisconnected) ||
		rows[1].Level != "error" {
		t.Fatalf("last-known-good diagnostics = %#v", rows)
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private")) || bytes.Contains(raw, []byte("transport detail")) {
		t.Fatalf("diagnostics leaked error text: %s", raw)
	}
}

// TestUsageRepeatedIdenticalFailuresAreSuppressed proves the bounded journal
// cannot be flooded by a persistent failure inside one process run, while a
// genuinely different (provider, failure) tuple still gets its own row.
func TestUsageRepeatedIdenticalFailuresAreSuppressed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	claude := &stubAdapter{name: "claude", err: errors.New("claude: usage endpoint returned status 500")}
	codex := &stubAdapter{
		name:  "codex",
		snaps: []usage.Snapshot{{Model: "codex", Window: usage.Window5h, Pct: 12, ResetsAt: now.Add(time.Hour), UpdatedAt: now}},
		err:   usage.RowSkipWarning([]string{"primary: missing used_percent"}),
	}
	h := newJournalHarness(t, claude, codex)

	for range 4 {
		h.run("--model", "all")
	}
	if claude.collectCalls != 4 {
		t.Fatalf("collect calls = %d, want 4 (the failure really did repeat)", claude.collectCalls)
	}

	rows := h.tail(10)
	if len(rows) != 2 {
		t.Fatalf("journal rows = %#v, want one per distinct (provider, failure) tuple", rows)
	}
	seen := map[string]string{}
	for _, row := range rows {
		seen[row.Provider] = row.Failure
	}
	if seen[string(diagnostics.ProviderClaude)] != string(diagnostics.UsageFailureCollect) {
		t.Fatalf("claude row = %q, want collect-failed", seen[string(diagnostics.ProviderClaude)])
	}
	if seen[string(diagnostics.ProviderCodex)] != string(diagnostics.UsageFailureRowsSkipped) {
		t.Fatalf("codex row = %q, want rows-skipped", seen[string(diagnostics.ProviderCodex)])
	}
}

// TestUsageStatusRefreshFailureLandsInJournal covers the other collection
// entry point: `projmux status usage` stays silent on stdout/stderr by
// contract, so the journal is the only surface where its failure shows up.
func TestUsageStatusRefreshFailureLandsInJournal(t *testing.T) {
	t.Parallel()

	h := newJournalHarness(t, &stubAdapter{name: "claude", err: errors.New("network down")})
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := h.cmd.RunStatus([]string{"--force"}, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("status path must stay silent, stderr = %q", stderr.String())
	}
	rows := h.tail(10)
	if len(rows) != 1 || rows[0].Provider != string(diagnostics.ProviderClaude) {
		t.Fatalf("journal rows = %#v, want one claude failure", rows)
	}
}

// TestUsageJournalWriteFailureKeepsCommandResult pins the lifecycle rule that a
// failing journal must not change the original command result.
func TestUsageJournalWriteFailureKeepsCommandResult(t *testing.T) {
	t.Parallel()

	h := newJournalHarness(t, &stubAdapter{name: "claude", err: errors.New("boom")})
	// Point the recorder at a path whose parent is a regular file, so every
	// append fails at the mkdir step.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.cmd.journal = nil
	h.cmd.journalFn = func() *diagnostics.UsageRecorder {
		return diagnostics.NewUsageRecorder(
			diagnostics.NewStore(filepath.Join(blocked, "logs", "operations.jsonl")),
			"journaltestrun", "0.0.0-test", diagnostics.MuxBackend())
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := h.cmd.Run([]string{"--model", "claude"}, stdout, stderr); err != nil {
		t.Fatalf("journal failure changed the command result: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("journal failure suppressed the usage table")
	}
}

// TestUsageJournalNilRecorderIsSafe covers the degraded path where the journal
// location cannot be resolved at all.
func TestUsageJournalNilRecorderIsSafe(t *testing.T) {
	t.Parallel()

	h := newJournalHarness(t, &stubAdapter{name: "claude", err: errors.New("boom")})
	h.cmd.journal = nil
	h.cmd.journalFn = func() *diagnostics.UsageRecorder { return nil }
	if err := h.cmd.Run([]string{"--model", "claude"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run with no journal: %v", err)
	}
}

// TestUsageDiagnosticsProviderProjection keeps adapter names out of the
// journal's free-form space: anything unrecognised collapses to `other`.
func TestUsageDiagnosticsProviderProjection(t *testing.T) {
	t.Parallel()

	cases := map[string]diagnostics.Provider{
		"claude":            diagnostics.ProviderClaude,
		"Codex":             diagnostics.ProviderCodex,
		" antigravity ":     diagnostics.ProviderAntigravity,
		"future-provider-x": diagnostics.ProviderOther,
		"":                  diagnostics.ProviderOther,
	}
	for model, want := range cases {
		if got := usageDiagnosticsProvider(model); got != want {
			t.Fatalf("usageDiagnosticsProvider(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestAdapterErrorsFlattensJoinedCollectResult proves the journal can attribute
// every adapter failure in one cycle, not just the first.
func TestAdapterErrorsFlattensJoinedCollectResult(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	for _, adapter := range []*stubAdapter{
		{name: "antigravity", err: errors.New("read quota: broken")},
		{name: "claude", err: errors.New("401")},
		{name: "codex", snaps: []usage.Snapshot{{Model: "codex", Window: usage.Window5h, Pct: 1, UpdatedAt: now}}},
	} {
		if err := registry.Replace(adapter); err != nil {
			t.Fatal(err)
		}
	}
	mgr := usage.NewManager(registry, usage.NewStore(t.TempDir()), func() time.Time { return now })
	_, collectErr := mgr.Collect(context.Background())
	adapterErrs := usage.AdapterErrors(collectErr)
	if len(adapterErrs) != 2 {
		t.Fatalf("adapter errors = %#v, want both failures", adapterErrs)
	}
	if adapterErrs[0].Model != "antigravity" || adapterErrs[1].Model != "claude" {
		t.Fatalf("adapter attribution lost order/identity: %#v", adapterErrs)
	}
	for _, adapterErr := range adapterErrs {
		if adapterErr.Partial() {
			t.Fatalf("%#v classified as partial, want a hard failure", adapterErr)
		}
	}
}
