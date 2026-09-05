package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

// TestClaudeEnvelopeExpiryDiscardsRowsTheScannerAlreadyFound pins the defect,
// it does not justify it.
//
// Contract violated: C-1 ("an enabled provider with matching sessions does not
// disappear from the list because of its own scan cost"). The Claude lane has
// no cancellation path - interactive.go's claude arm calls discoverClaude
// without a ctx - so when the provider envelope and the handoff window that
// follows it both expire, the scanner is still running and settlement returns
// an empty result flagged envelopeExpired. resumeProviderProjection then maps
// count==0 plus envelopeExpired to search_failed, and populateOnce publishes
// rows only for providers projected as count. Every row the scanner had
// already found is thrown away, and the footer says "search failed" rather
// than "0 found", so the user cannot tell a truncated scan from an empty one.
//
// Phase 1 (`fix(resume): bound the claude resume scan to the displayed limit`)
// inverts this: with the scan bounded to the displayed limit, this same
// scenario must project count and publish the rows.
//
// Determinism: the scanner is held past settlement, so the handoff branch is
// the only branch that can ever fire - the outcome does not depend on which
// goroutine wins a race, on load, or on any wall-clock reading.
func TestClaudeEnvelopeExpiryDiscardsRowsTheScannerAlreadyFound(t *testing.T) {
	const cwd = "/work"
	const fixtureFiles = 24
	projectsDir := writeClaudeScanFixture(t, cwd, fixtureFiles)

	clock := newVirtualResumeBudgetClock()
	cmd := testAICommand(t.TempDir())
	var scanned atomic.Int64
	found := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, scanCWD string, opts aisessions.ResumeSummaryOptions, limit int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeClaude {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		// The real Claude lane, against a real directory: the rows below are
		// found by production discovery, not fabricated by the test.
		opts.DiscoverOptions.ClaudeProjectsDir = projectsDir
		discovery, err := aisessions.DiscoverResumeSummariesContext(ctx, provider, scanCWD, opts, limit)
		scanned.Store(int64(len(discovery.Summaries)))
		close(found)
		// Production keeps scanning here because nothing cancels it. Holding the
		// return models exactly that: work completed, delivery too late.
		<-release
		close(returned)
		return discovery, err
	}

	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	// Only the Claude lane is under test; the sibling providers would otherwise
	// settle on their own clocks and add nothing to the assertion.
	controller.providerEnabled = map[string]bool{aiModeClaude: true}
	// The handoff window is the one stage settleProviderResult measures with a
	// real timer rather than the injected clock, so the declared 35ms bound is
	// asserted here and driven by a compressed stand-in below. The stand-in
	// cannot change the outcome: the scanner is held past any handoff window,
	// so the timer branch always wins. This is not a budget increase - the
	// declared constant is unchanged and pinned on the next line.
	if got := defaultAIResumeBudgets().stage(aiResumeStageHandoff); got != 35*time.Millisecond {
		t.Fatalf("declared handoff budget = %s want 35ms", got)
	}
	budgets := controller.budgets
	budgets.handoff = time.Nanosecond
	controller.budgets = budgets
	defer controller.close()

	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	// The scanner has matching rows in hand before either window expires.
	<-found
	if got := scanned.Load(); got != fixtureFiles {
		t.Fatalf("claude scan found %d rows, want %d before the envelope expired", got, fixtureFiles)
	}

	// Both windows expire: the provider envelope on the injected clock, and the
	// handoff that follows it on its own timer.
	clock.advance(aiResumeSummaryPopulationBudget)
	settled := <-entries

	// The picker's own "[+ New Session]" affordance is always present; a
	// published session row is any other entry.
	sessionRows := 0
	for _, entry := range settled {
		if entry.Value != aiResumeNewValue {
			sessionRows++
		}
	}
	if sessionRows != 0 {
		t.Fatalf("published %d session rows, want 0: expiry discards every row the scan already found", sessionRows)
	}
	claude := settledProviderState(controller, aiModeClaude)
	if claude.state != aiResumeProviderSearchFailed || claude.count != 0 {
		t.Fatalf("claude projection = %+v, want search_failed with no rows", claude)
	}

	// The late result is discarded rather than appended: the frame settled and
	// stays settled.
	close(release)
	<-returned
	controller.mu.Lock()
	published := len(controller.summaries)
	controller.mu.Unlock()
	if published != 0 {
		t.Fatalf("late claude result mutated the settled frame: %d rows", published)
	}
	if got := settledProviderState(controller, aiModeClaude); got.state != aiResumeProviderSearchFailed {
		t.Fatalf("late claude result changed the projection: %+v", got)
	}
}
