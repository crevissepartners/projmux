package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// writeClaudeScanFixture writes count Claude transcripts that all record cwd,
// each with its own mtime one second apart, and returns the projects dir. The
// fixture is fully declared here: no host state, no clock read, no sampling.
func writeClaudeScanFixture(t *testing.T, cwd string, count int) string {
	t.Helper()
	projectsDir := filepath.Join(t.TempDir(), "claude", "projects")
	dir := filepath.Join(projectsDir, aisessions.EncodeClaudeProjectPath(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for i := range count {
		id := fmt.Sprintf("11111111-2222-4333-8444-%012d", i)
		path := filepath.Join(dir, fmt.Sprintf("session-%04d.jsonl", i))
		content := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"gitBranch":"main","message":{"content":"Session %04d"}}`+"\n", id, cwd, i)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		at := base.Add(-time.Duration(i) * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return projectsDir
}

// Phase 1 reverses Phase 0's C-1 discard assertion and pins criterion ⑥c:
// envelope expiry publishes a confirmed partial with count, never search_failed
// plus zero rows. The old harness held a completed discovery past settlement,
// which models a non-cooperating provider rather than cancellation in the scan.
//
// This controller boundary test obtains a real descending-mtime prefix using
// partialLimit, then delivers it only after envelope expiry AND handoff entry.
// partialLimit is below the invocation's limit, so this is not a completed
// displayed page. This harness does not claim to cancel midway through parsing:
// TestClaudeCanceledScanReturnsOnlyFullyParsedRows independently proves that
// production scan cancellation returns only complete earlier rows and marks the
// result incomplete. Together they cover the producer and settlement boundary.
//
// The injected envelope clock and handoff channel establish every ordering;
// there is no sleep, real timer, budget increase, or wall-clock assertion.
func TestClaudeEnvelopeExpiryPublishesParsedPartialRows(t *testing.T) {
	const cwd = "/work"
	const fixtureFiles, invocationLimit, partialLimit = 24, 20, 4
	projectsDir := writeClaudeScanFixture(t, cwd, fixtureFiles)

	clock := newVirtualResumeBudgetClock()
	cmd := testAICommand(t.TempDir())
	type scanResult struct {
		discovery aisessions.ResumeSummaryDiscovery
		err       error
		ctx       context.Context
		limit     int
	}
	found := make(chan scanResult, 1)
	release := make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, scanCWD string, opts aisessions.ResumeSummaryOptions, limit int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeClaude {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		opts.DiscoverOptions.ClaudeProjectsDir = projectsDir
		discovery, err := aisessions.DiscoverResumeSummariesContext(ctx, provider, scanCWD, opts, partialLimit)
		found <- scanResult{discovery: discovery, err: err, ctx: ctx, limit: limit}
		<-ctx.Done()
		// Only the test's witnessed handoff entry can release this result.
		<-release
		return discovery, err
	}

	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, invocationLimit)
	controller.providerEnabled = map[string]bool{aiModeClaude: true}
	handoffEntered := make(chan time.Duration, 1)
	handoffExpiry := make(chan time.Time)
	handoffStopped := make(chan struct{})
	controller.handoffTimer = func(budget time.Duration) (<-chan time.Time, func()) {
		handoffEntered <- budget
		return handoffExpiry, func() { close(handoffStopped) }
	}
	defer controller.close()

	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	partial := <-found
	if partial.err != nil {
		t.Fatal(partial.err)
	}
	if partial.ctx.Err() != nil || partial.limit != invocationLimit {
		t.Fatalf("scan start: error=%v invocation limit=%d", partial.ctx.Err(), partial.limit)
	}
	if len(partial.discovery.Summaries) != partialLimit || !partial.discovery.MoreNotLoaded {
		t.Fatalf("scan prefix rows/more=%d/%t, want %d/true", len(partial.discovery.Summaries), partial.discovery.MoreNotLoaded, partialLimit)
	}
	for i, row := range partial.discovery.Summaries {
		wantID := fmt.Sprintf("11111111-2222-4333-8444-%012d", i)
		if row.ResumeID != wantID || row.Branch != "main" || row.Source != aisessions.SourceClaudeTranscript {
			t.Fatalf("prefix row %d is incomplete or out of order: %#v", i, row)
		}
	}

	clock.advance(aiResumeSummaryPopulationBudget)
	if budget := <-handoffEntered; budget != 35*time.Millisecond {
		t.Fatalf("handoff budget=%s, want unchanged 35ms", budget)
	}
	if !errors.Is(context.Cause(partial.ctx), context.DeadlineExceeded) {
		t.Fatalf("handoff entered without envelope expiry: %v", context.Cause(partial.ctx))
	}
	// The scanner result was unavailable during the envelope select. The
	// handoff result branch is now the only branch that can become ready.
	release <- struct{}{}
	settled := <-entries
	<-handoffStopped

	sessionRows := 0
	for _, entry := range settled {
		if entry.Value != aiResumeNewValue {
			sessionRows++
		}
	}
	if sessionRows != partialLimit || sessionRows >= invocationLimit {
		t.Fatalf("published %d session rows, want partial %d below invocation limit %d", sessionRows, partialLimit, invocationLimit)
	}
	claude := settledProviderState(controller, aiModeClaude)
	if claude.state != aiResumeProviderCount || claude.count != partialLimit {
		t.Fatalf("claude projection=%+v, want count/%d after envelope expiry", claude, partialLimit)
	}
	if got := controller.snapshotSummaries(); !reflect.DeepEqual(got, partial.discovery.Summaries) {
		t.Fatalf("published partial changed: got %#v want %#v", got, partial.discovery.Summaries)
	}
	controller.mu.Lock()
	more, elapsed, detailRefs := controller.moreNotLoaded, controller.providerElapsed[aiModeClaude], len(controller.detailRefs)
	controller.mu.Unlock()
	if !more || elapsed != aiResumeSummaryPopulationBudget || detailRefs != partialLimit {
		t.Fatalf("settled more/elapsed/detail refs=%t/%s/%d, want true/%s/%d", more, elapsed, detailRefs, aiResumeSummaryPopulationBudget, partialLimit)
	}
}

// A provider that ignores cancellation and misses the handoff still cannot
// mutate a settled frame. Preserve this negative case from Phase 0 separately
// from the cooperative partial delivery required by C-1/⑥c above.
func TestClaudeHandoffExpiryKeepsLateResultOutsideSettlement(t *testing.T) {
	const cwd = "/work"
	projectsDir := writeClaudeScanFixture(t, cwd, 4)
	discovery, err := aisessions.DiscoverResumeSummariesContext(context.Background(), aiModeClaude, cwd,
		aisessions.ResumeSummaryOptions{DiscoverOptions: aisessions.DiscoverOptions{ClaudeProjectsDir: projectsDir}}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Summaries) != 4 {
		t.Fatalf("fixture returned %d rows, want 4", len(discovery.Summaries))
	}
	clock := newVirtualResumeBudgetClock()
	controller := virtualBudgetController(testAICommand(t.TempDir()), clock, t.TempDir(), 0, 20)
	defer controller.close()
	handoffEntered := make(chan time.Duration, 1)
	handoffExpiry := make(chan time.Time)
	controller.handoffTimer = func(budget time.Duration) (<-chan time.Time, func()) {
		handoffEntered <- budget
		return handoffExpiry, func() {}
	}
	envelope, stop := clock.withTimeout(controller.ctx, aiResumeSummaryPopulationBudget)
	defer stop()
	startedAt := clock.instant()
	returned := make(chan aiResumeProviderResult, 1)
	settled := make(chan aiResumeProviderResult, 1)
	go func() {
		settled <- controller.settleProviderResult(aiModeClaude, envelope, startedAt, returned)
	}()
	clock.advance(aiResumeSummaryPopulationBudget)
	if budget := <-handoffEntered; budget != 35*time.Millisecond {
		t.Fatalf("handoff budget=%s, want unchanged 35ms", budget)
	}
	clock.advance(aiResumeHandoffBudget)
	handoffExpiry <- clock.instant()
	result := <-settled
	if !result.envelopeExpired || len(result.discovery.Summaries) != 0 || result.elapsed != aiResumeSummaryPopulationBudget+aiResumeHandoffBudget {
		t.Fatalf("expired handoff result=%+v", result)
	}
	// The completed settlement has no receiver left. A late provider can send
	// into its buffer, but cannot replace the already selected terminal result.
	returned <- aiResumeProviderResult{provider: aiModeClaude, discovery: discovery}
	if len(returned) != 1 {
		t.Fatal("late result was consumed after settlement")
	}
	projection := resumeProviderProjection(result, true)
	if projection.state != aiResumeProviderScanUnfinished || projection.count != 0 {
		t.Fatalf("late result changed the settled projection: %+v", projection)
	}
}
