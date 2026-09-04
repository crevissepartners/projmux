package aisessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestResumeSummaryProjectionContainsOnlyListFieldsAndSeparateDetailRef(t *testing.T) {
	session := SessionMeta{
		Agent: AgentClaude, ResumeID: "exact-id", Title: "  compact   label ", LastModified: time.Unix(7, 0), UpdatedAt: time.Unix(8, 0),
		Context: SessionContext{CWD: "/work/app/child", Branch: "main"}, Source: SourceClaudeTranscript,
		Confidence: ConfidenceMedium, Reason: "private reason", RuntimeStatus: "active", Turns: 99, sourcePath: "/opaque/transcript.jsonl",
	}
	summary, detailRef := projectResumeSummary(session, "/work/app", 1)
	if summary.Provider != AgentClaude || summary.ResumeID != "exact-id" || summary.Label != "compact label" || summary.RelativeCWD != "./child" || summary.Source != SourceClaudeTranscript {
		t.Fatalf("summary projection = %#v", summary)
	}
	typ := reflect.TypeFor[ResumeSummary]()
	for _, forbidden := range []string{"Turns", "Confidence", "Reason", "RuntimeStatus", "Preview", "Transcript", "sourcePath", "previewSource"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Fatalf("ResumeSummary gained detail/transcript field %q", forbidden)
		}
	}
	if detailRef.transcriptPath != session.sourcePath || detailRef.ResumeID != summary.ResumeID || detailRef.Source != summary.Source ||
		detailRef.Turns != 99 || detailRef.RuntimeStatus != "active" || detailRef.Confidence != ConfidenceMedium || detailRef.Reason != "private reason" {
		t.Fatalf("separate detail ref = %#v", detailRef)
	}
}

func TestReadResumeDetailCountsTurnsAndKeepsMetadataWithBoundedPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	writeFile(t, path, `{"type":"event_msg","payload":{"type":"user_message","message":"first question"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"first answer"}}
{"type":"event_msg","payload":{"type":"user_message","message":"second question"}}
`)
	ref := ResumeDetailRef{
		Provider: AgentClaude, ResumeID: "exact", Source: SourceClaudeTranscript,
		Confidence: ConfidenceMedium, Reason: "filesystem fallback", RuntimeStatus: "idle", transcriptPath: path,
	}
	detail, err := ReadResumeDetail(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("ReadResumeDetail() error = %v", err)
	}
	if detail.Turns != 2 || detail.Source != ref.Source || detail.Confidence != ref.Confidence || detail.Reason != ref.Reason || detail.RuntimeStatus != ref.RuntimeStatus {
		t.Fatalf("detail metadata = %#v", detail)
	}
	if detail.Preview.User != "second question" || detail.Preview.Assistant != "first answer" {
		t.Fatalf("detail preview = %#v", detail.Preview)
	}
}

func TestResumeSummaryNativeReadsOnePageAndPreservesExactIDSource(t *testing.T) {
	next := "opaque-page-2"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{{
			ID: "019f0000-0000-7000-8000-000000000041", CWD: "/work/app", Name: "Native summary", Branch: "main",
			RecencyAt: time.Unix(20, 0), UpdatedAt: time.Unix(21, 0), RuntimeStatus: "active",
		}}, NextCursor: &next},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000042", CWD: "/work/app"}}},
	}}
	discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{HomeDir: t.TempDir(), CodexSessionsDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(fake)},
		NativeBudget:    time.Second,
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.closed != 1 || !discovery.MoreNotLoaded {
		t.Fatalf("native summary calls=%d closed=%d more=%v", len(fake.calls), fake.closed, discovery.MoreNotLoaded)
	}
	if len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != "019f0000-0000-7000-8000-000000000041" || discovery.Summaries[0].Source != SourceCodexAppServer {
		t.Fatalf("native summary = %#v", discovery.Summaries)
	}
	if discovery.Codex.Source != CatalogSourceNative || len(discovery.DetailRefs) != 1 || discovery.DetailRefs[0].ResumeID != discovery.Summaries[0].ResumeID {
		t.Fatalf("native authority/detail ref = %#v", discovery)
	}
}

type delayedSummaryCatalog struct {
	delay    time.Duration
	returned chan struct{}

	mu         sync.Mutex
	listCalls  int
	readCalls  int
	closeCalls int
}

func (c *delayedSummaryCatalog) List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	if c.delay > 0 {
		time.Sleep(c.delay) // exercise late-result rejection even when cancellation is ignored
	}
	c.mu.Lock()
	c.listCalls++
	c.mu.Unlock()
	if c.returned != nil {
		close(c.returned)
	}
	return codexappserver.CatalogPage{Threads: []codexappserver.CatalogThread{{
		ID: "019f0000-0000-7000-8000-000000000041", CWD: "/work/app", Name: "native summary", RecencyAt: time.Unix(30, 0),
	}}}, nil
}

func (c *delayedSummaryCatalog) Read(context.Context, string) (codexappserver.CatalogThread, error) {
	c.mu.Lock()
	c.readCalls++
	c.mu.Unlock()
	return codexappserver.CatalogThread{}, nil
}

func (c *delayedSummaryCatalog) Close() error {
	c.mu.Lock()
	c.closeCalls++
	c.mu.Unlock()
	return nil
}

func TestResumeSummaryNativeDelayMatrixUsesNativeInBudgetAndFallbackAfterCutoff(t *testing.T) {
	for _, delay := range []time.Duration{0, 50 * time.Millisecond, 500 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			root := t.TempDir()
			day := filepath.Join(root, "2026", "08", "24")
			if err := os.MkdirAll(day, 0o755); err != nil {
				t.Fatal(err)
			}
			const fallbackID = "019f0000-0000-7000-8000-000000000077"
			writeFile(t, filepath.Join(day, "rollout-fallback.jsonl"), `{"type":"session_meta","payload":{"id":"`+fallbackID+`","cwd":"/work/app"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Fallback summary"}}
`)
			fake := &delayedSummaryCatalog{delay: delay, returned: make(chan struct{})}
			startedAt := time.Now()
			discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
				DiscoverOptions: DiscoverOptions{
					HomeDir: t.TempDir(), CodexSessionsDir: root,
					OpenCodexCatalog: func(context.Context) (CodexCatalog, error) { return fake, nil },
				},
				NativeBudget: DefaultResumeSummaryNativeBudget,
			}, 20)
			elapsed := time.Since(startedAt)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed >= 500*time.Millisecond {
				t.Fatalf("settled decision took %s, want <500ms", elapsed)
			}
			if delay < DefaultResumeSummaryNativeBudget {
				if discovery.Codex.Source != CatalogSourceNative || len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != "019f0000-0000-7000-8000-000000000041" {
					t.Fatalf("in-budget native decision = %#v", discovery)
				}
			} else {
				if discovery.Codex.Source != CatalogSourceFallback || len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != fallbackID {
					t.Fatalf("over-budget fallback decision = %#v", discovery)
				}
				select {
				case <-fake.returned:
				case <-time.After(time.Second):
					t.Fatal("late native List did not return")
				}
				if discovery.Summaries[0].ResumeID != fallbackID {
					t.Fatalf("late native completion mutated returned snapshot: %#v", discovery.Summaries)
				}
			}
			fake.mu.Lock()
			listCalls, readCalls, closeCalls := fake.listCalls, fake.readCalls, fake.closeCalls
			fake.mu.Unlock()
			if listCalls != 1 || readCalls != 0 || closeCalls != 1 {
				t.Fatalf("catalog calls list=%d read=%d close=%d, want 1/0/1", listCalls, readCalls, closeCalls)
			}
		})
	}
}

type blockingSummaryCatalog struct {
	closeOnce sync.Once
	closed    chan struct{}
	returned  chan struct{}

	mu         sync.Mutex
	closeCalls int
}

type fallbackOrderCatalog struct {
	fallbackStarted <-chan struct{}
	observed        chan<- struct{}
	closed          chan struct{}
	closeOnce       sync.Once
}

func (c *fallbackOrderCatalog) List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	select {
	case <-c.fallbackStarted:
		close(c.observed)
	case <-time.After(20 * time.Millisecond):
		return codexappserver.CatalogPage{}, errors.New("fallback did not start with native")
	}
	<-c.closed
	return codexappserver.CatalogPage{}, context.Canceled
}

func (*fallbackOrderCatalog) Read(context.Context, string) (codexappserver.CatalogThread, error) {
	return codexappserver.CatalogThread{}, nil
}

func (c *fallbackOrderCatalog) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func TestResumeSummaryHundredsOfRolloutsStartConcurrentlyWithNative(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "08", "24")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		id := fmt.Sprintf("019f0000-0000-7000-8000-%012d", i)
		writeFile(t, filepath.Join(day, fmt.Sprintf("rollout-%03d.jsonl", i)), fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/work/app"}}
{"type":"event_msg","payload":{"type":"user_message","message":"fallback"}}
`, id))
	}

	fallbackStarted := make(chan struct{})
	fallbackObserved := make(chan struct{})
	catalog := &fallbackOrderCatalog{fallbackStarted: fallbackStarted, observed: fallbackObserved, closed: make(chan struct{})}
	startedAt := time.Now()
	discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{CodexSessionsDir: root, OpenCodexCatalog: func(context.Context) (CodexCatalog, error) { return catalog, nil }},
		NativeBudget:    30 * time.Millisecond,
		discoverCodexFallback: func(ctx context.Context, cwd, sessionsDir string, depth int) []SessionMeta {
			close(fallbackStarted)
			return discoverCodexContext(ctx, cwd, sessionsDir, depth)
		},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fallbackObserved:
	default:
		t.Fatal("native attempt did not observe rollout fallback running concurrently")
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("concurrent fallback settled in %s, want <500ms", elapsed)
	}
	if discovery.Codex.Source != CatalogSourceFallback || len(discovery.Summaries) == 0 {
		t.Fatalf("large rollout fallback did not settle: %#v", discovery)
	}
}

// The rollout cutoff belongs to the rollout stage, not to the caller. A
// caller deadline that elapses first must not end the scan, and at the stage's
// own bound the rows it had already parsed must survive the exact edge where
// its result send races the stage's Done. The row set here is synthetic on
// purpose: this is the channel handoff, and a real store's scan time would
// race the bound instead of the send.
func TestResumeSummaryRolloutCutoffKeepsFullyParsedRowsAcrossTheSendEdge(t *testing.T) {
	const (
		currentOwnerID = "01a032ae-129b-7b73-95f9-e15300f130e7"
		callerBudget   = 40 * time.Millisecond
		nativeBudget   = 10 * time.Millisecond
		fallbackBudget = 150 * time.Millisecond
	)
	for _, tc := range []struct {
		name      string
		sendDelay time.Duration
		wantRows  int
	}{
		{name: "send inside the settlement window", sendDelay: time.Millisecond, wantRows: 1},
		{name: "send beyond the settlement window", sendDelay: 4 * resumeSummaryCancellationSettlementBudget, wantRows: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
			defer cancel()
			catalog := &blockingSummaryCatalog{closed: make(chan struct{}), returned: make(chan struct{})}
			startedAt := time.Now()
			discovery, err := DiscoverResumeSummariesContext(ctx, AgentCodex, "/work/app", ResumeSummaryOptions{
				DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: func(context.Context) (CodexCatalog, error) { return catalog, nil }},
				NativeBudget:    nativeBudget,
				FallbackBudget:  fallbackBudget,
				discoverCodexFallback: func(ctx context.Context, _, _ string, _ int) []SessionMeta {
					// One completely parsed row, then the exact stage Done /
					// result-send edge that used to settle empty.
					sessions := []SessionMeta{{
						Agent: AgentCodex, ResumeID: currentOwnerID, Title: "current owner",
						Context: SessionContext{CWD: "/work/app"}, Source: SourceCodexRollout,
					}}
					<-ctx.Done()
					time.Sleep(tc.sendDelay)
					return sessions
				},
			}, 20)
			if err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(startedAt)
			if elapsed < fallbackBudget {
				t.Fatalf("rollout scan settled in %s: the caller deadline (%s) cut the stage's own %s bound", elapsed, callerBudget, fallbackBudget)
			}
			if elapsed >= fallbackBudget+time.Second {
				t.Fatalf("rollout stage did not terminalize on its own bound: %s", elapsed)
			}
			if discovery.Codex.Source != CatalogSourceFallback || len(discovery.Summaries) != tc.wantRows {
				t.Fatalf("stage-edge fallback decision = %#v", discovery)
			}
			if tc.wantRows == 0 {
				// A result arriving after the settlement window must not reach the
				// snapshot this invocation already published.
				time.Sleep(tc.sendDelay)
				if len(discovery.Summaries) != 0 || len(discovery.DetailRefs) != 0 {
					t.Fatalf("late rollout send mutated the settled snapshot: %#v", discovery)
				}
				return
			}
			if got := discovery.Summaries[0]; got.ResumeID != currentOwnerID || got.Source != SourceCodexRollout {
				t.Fatalf("stage-edge exact id/source = %#v", got)
			}
		})
	}
}

func TestResumeSummaryNativeEmptyCancelsConcurrentFallbackWithoutMerge(t *testing.T) {
	canceled := make(chan struct{})
	lateID := "019f0000-0000-7000-8000-000000000077"
	discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{
			CodexSessionsDir: t.TempDir(),
			OpenCodexCatalog: openFakeCodexCatalog(&fakeCodexCatalog{pages: []codexappserver.CatalogPage{{}}}),
		},
		NativeBudget: time.Second,
		discoverCodexFallback: func(ctx context.Context, _, _ string, _ int) []SessionMeta {
			<-ctx.Done()
			close(canceled)
			return []SessionMeta{{Agent: AgentCodex, ResumeID: lateID, Source: SourceCodexRollout}}
		},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("native-empty did not cancel concurrent rollout fallback")
	}
	if discovery.Codex.Source != CatalogSourceNative || len(discovery.Summaries) != 0 {
		t.Fatalf("native-empty merged late fallback: %#v", discovery)
	}
}

func (c *blockingSummaryCatalog) List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	<-c.closed
	close(c.returned)
	return codexappserver.CatalogPage{Threads: []codexappserver.CatalogThread{{
		ID: "019f0000-0000-7000-8000-000000000099", CWD: "/work/app", Name: "late native must be discarded",
	}}}, nil
}

func (*blockingSummaryCatalog) Read(context.Context, string) (codexappserver.CatalogThread, error) {
	return codexappserver.CatalogThread{}, nil
}

func (c *blockingSummaryCatalog) Close() error {
	c.mu.Lock()
	c.closeCalls++
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func TestResumeSummaryNativeBudgetActivelyClosesBlockedListAndSettlesFallbackUnder500ms(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "08", "24")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	const fallbackID = "019f0000-0000-7000-8000-000000000077"
	writeFile(t, filepath.Join(day, "rollout-fallback.jsonl"), `{"type":"session_meta","payload":{"id":"`+fallbackID+`","cwd":"/work/app"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Fallback summary"}}
`)
	fake := &blockingSummaryCatalog{closed: make(chan struct{}), returned: make(chan struct{})}
	startedAt := time.Now()
	discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{
			HomeDir: t.TempDir(), CodexSessionsDir: root,
			OpenCodexCatalog: func(context.Context) (CodexCatalog, error) { return fake, nil },
		},
		NativeBudget: 20 * time.Millisecond,
	}, 20)
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("fallback decision took %s, want <500ms", elapsed)
	}
	select {
	case <-fake.returned:
	case <-time.After(time.Second):
		t.Fatal("budget expiry did not actively close blocked native List")
	}
	fake.mu.Lock()
	closeCalls := fake.closeCalls
	fake.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("catalog close calls=%d, want exactly one", closeCalls)
	}
	if discovery.Codex.Source != CatalogSourceFallback || discovery.Codex.Reason != CatalogReason(ReasonAppServerTimeout) || len(discovery.Summaries) != 1 {
		t.Fatalf("fallback decision = %#v", discovery)
	}
	if got := discovery.Summaries[0]; got.ResumeID != fallbackID || got.Source != SourceCodexRollout {
		t.Fatalf("fallback exact id/source = %#v", got)
	}
	for _, summary := range discovery.Summaries {
		if summary.ResumeID == "019f0000-0000-7000-8000-000000000099" {
			t.Fatalf("late native completion escaped into settled summaries: %#v", discovery.Summaries)
		}
	}
}

// assertStageBudgetOwned fails when a stage deadline looks like the caller's
// remainder instead of the bound declared for that stage.
func assertStageBudgetOwned(t *testing.T, stage string, startedAt, deadline time.Time, budget time.Duration) {
	t.Helper()

	span := deadline.Sub(startedAt)
	if span < budget-budget/2 || span > budget+time.Second {
		t.Fatalf("%s stage span = %s, want about %s measured from its own start", stage, span, budget)
	}
}

// A caller context with almost nothing left stands in for a route that spent
// most of its own bound before Codex discovery started. Neither stage may be
// reduced to that remainder, and the declared bounds are constants rather than
// values computed from a sibling.
func TestResumeSummaryStageBudgetsAreDeclaredAndOwnedPerStage(t *testing.T) {
	if DefaultResumeSummaryNativeBudget != 300*time.Millisecond {
		t.Fatalf("declared native bound = %s, want 300ms", DefaultResumeSummaryNativeBudget)
	}
	if DefaultResumeSummaryFallbackBudget != 1250*time.Millisecond {
		t.Fatalf("declared fallback bound = %s, want 1.25s", DefaultResumeSummaryFallbackBudget)
	}
	if got := resumeSummaryBudget(0, DefaultResumeSummaryNativeBudget); got != DefaultResumeSummaryNativeBudget {
		t.Fatalf("zero native bound resolved to %s", got)
	}
	if got := resumeSummaryBudget(7*time.Millisecond, DefaultResumeSummaryFallbackBudget); got != 7*time.Millisecond {
		t.Fatalf("declared fallback bound overridden to %s", got)
	}
	if resumeSummaryCancellationSettlementBudget >= DefaultResumeSummaryNativeBudget {
		t.Fatalf("settlement handoff %s is not a bounded window inside a stage bound", resumeSummaryCancellationSettlementBudget)
	}

	const (
		nativeBudget   = 120 * time.Millisecond
		fallbackBudget = 240 * time.Millisecond
		retainedID     = "019f0000-0000-7000-8000-0000000000b2"
	)
	caller, cancelCaller := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelCaller()
	<-caller.Done()

	nativeDeadlines := make(chan time.Time, 1)
	fallbackDeadlines := make(chan time.Time, 1)
	startedAt := time.Now()
	discovery, err := DiscoverResumeSummariesContext(caller, AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: func(ctx context.Context) (CodexCatalog, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("native stage context carries no deadline of its own")
			}
			nativeDeadlines <- deadline
			return nil, errors.New("native endpoint refused")
		}},
		NativeBudget:   nativeBudget,
		FallbackBudget: fallbackBudget,
		discoverCodexFallback: func(ctx context.Context, _, _ string, _ int) []SessionMeta {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("rollout stage context carries no deadline of its own")
			}
			fallbackDeadlines <- deadline
			return []SessionMeta{{Agent: AgentCodex, ResumeID: retainedID, Source: SourceCodexRollout}}
		},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertStageBudgetOwned(t, "native", startedAt, <-nativeDeadlines, nativeBudget)
	assertStageBudgetOwned(t, "rollout", startedAt, <-fallbackDeadlines, fallbackBudget)
	if discovery.Codex.Source != CatalogSourceFallback || len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != retainedID {
		t.Fatalf("an expired caller deadline discarded the rollout result: %#v", discovery)
	}
}

// One invocation runs exactly one rollout scan, and that scan is still alive
// when the native attempt reaches its own terminal. A caller deadline that
// elapses in between cancels neither stage.
func TestResumeSummaryRunsOneRolloutScanThatOutlivesTheNativeAttempt(t *testing.T) {
	const (
		callerBudget   = 20 * time.Millisecond
		nativeBudget   = 120 * time.Millisecond
		fallbackBudget = 900 * time.Millisecond
		retainedID     = "019f0000-0000-7000-8000-0000000000b2"
	)
	caller, cancelCaller := context.WithTimeout(context.Background(), callerBudget)
	defer cancelCaller()

	var mu sync.Mutex
	scans := 0
	nativeTerminal := make(chan struct{})
	rolloutAlive := make(chan bool, 1)
	startedAt := time.Now()
	discovery, err := DiscoverResumeSummariesContext(caller, AgentCodex, "/work/app", ResumeSummaryOptions{
		DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: func(ctx context.Context) (CodexCatalog, error) {
			<-ctx.Done() // spend the whole native bound, long past the caller deadline
			close(nativeTerminal)
			return nil, ctx.Err()
		}},
		NativeBudget:   nativeBudget,
		FallbackBudget: fallbackBudget,
		discoverCodexFallback: func(ctx context.Context, _, _ string, _ int) []SessionMeta {
			mu.Lock()
			scans++
			mu.Unlock()
			<-nativeTerminal
			rolloutAlive <- ctx.Err() == nil
			return []SessionMeta{{Agent: AgentCodex, ResumeID: retainedID, Source: SourceCodexRollout}}
		},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed < nativeBudget {
		t.Fatalf("native attempt terminalized in %s: the caller's %s remainder replaced its own %s bound", elapsed, callerBudget, nativeBudget)
	}
	if alive := <-rolloutAlive; !alive {
		t.Fatal("rollout stage was cancelled before the native attempt reached its own terminal")
	}
	mu.Lock()
	gotScans := scans
	mu.Unlock()
	if gotScans != 1 {
		t.Fatalf("rollout scans = %d, want exactly one per invocation", gotScans)
	}
	if discovery.Codex.Source != CatalogSourceFallback || discovery.Codex.Reason != CatalogReason(ReasonAppServerTimeout) {
		t.Fatalf("slow-native settlement = %#v", discovery.Codex)
	}
	if len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != retainedID {
		t.Fatalf("retained rollout rows = %#v", discovery.Summaries)
	}
}

// Every caller-state x native-outcome permutation settles on exactly one
// source and one snapshot. Rows from the source that lost never appear, in
// either direction, and no permutation merges the two.
func TestResumeSummarySourceSettlementPermutationsKeepOneSourceAndOneSnapshot(t *testing.T) {
	const (
		nativeID  = "019f0000-0000-7000-8000-0000000000a1"
		rolloutID = "019f0000-0000-7000-8000-0000000000b2"

		nativeBudget   = 80 * time.Millisecond
		fallbackBudget = 400 * time.Millisecond
	)
	nativePage := codexappserver.CatalogPage{Threads: []codexappserver.CatalogThread{{
		ID: nativeID, CWD: "/work/app", Name: "native row", RecencyAt: time.Unix(30, 0),
	}}}

	callers := []struct {
		name     string
		open     func(t *testing.T) (context.Context, context.CancelFunc)
		canceled bool
	}{
		{name: "caller-fresh", open: func(*testing.T) (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Second)
		}},
		{name: "caller-nearly-spent", open: func(*testing.T) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			<-ctx.Done()
			return ctx, cancel
		}},
		{name: "caller-canceled", canceled: true, open: func(*testing.T) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
	}
	// Each arm builds a fresh opener: a fake catalog shared across permutations
	// would run out of pages and report a protocol error instead of the outcome
	// the arm is meant to exercise.
	natives := []struct {
		name       string
		newOpen    func() OpenCodexCatalog
		wantSource CatalogSource
		wantIDs    []string
	}{
		{
			name: "native-rows",
			newOpen: func() OpenCodexCatalog {
				return openFakeCodexCatalog(&fakeCodexCatalog{pages: []codexappserver.CatalogPage{nativePage}})
			},
			wantSource: CatalogSourceNative, wantIDs: []string{nativeID},
		},
		{
			name: "native-empty",
			newOpen: func() OpenCodexCatalog {
				return openFakeCodexCatalog(&fakeCodexCatalog{pages: []codexappserver.CatalogPage{{}}})
			},
			wantSource: CatalogSourceNative, wantIDs: nil,
		},
		{
			name: "native-error",
			newOpen: func() OpenCodexCatalog {
				return func(context.Context) (CodexCatalog, error) { return nil, errors.New("native endpoint refused") }
			},
			wantSource: CatalogSourceFallback, wantIDs: []string{rolloutID},
		},
		{
			name: "native-overrun",
			newOpen: func() OpenCodexCatalog {
				return func(ctx context.Context) (CodexCatalog, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
			},
			wantSource: CatalogSourceFallback, wantIDs: []string{rolloutID},
		},
	}

	for _, caller := range callers {
		for _, native := range natives {
			t.Run(caller.name+"/"+native.name, func(t *testing.T) {
				ctx, cancel := caller.open(t)
				defer cancel()

				// A cancelled invocation ends the native read whatever it would
				// otherwise have returned, so the retained rollout rows win.
				wantSource, wantIDs := native.wantSource, native.wantIDs
				if caller.canceled {
					wantSource, wantIDs = CatalogSourceFallback, []string{rolloutID}
				}

				var mu sync.Mutex
				scans := 0
				discovery, err := DiscoverResumeSummariesContext(ctx, AgentCodex, "/work/app", ResumeSummaryOptions{
					DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: native.newOpen()},
					NativeBudget:    nativeBudget,
					FallbackBudget:  fallbackBudget,
					discoverCodexFallback: func(ctx context.Context, _, _ string, _ int) []SessionMeta {
						mu.Lock()
						scans++
						mu.Unlock()
						return []SessionMeta{{Agent: AgentCodex, ResumeID: rolloutID, Source: SourceCodexRollout}}
					},
				}, 20)
				if err != nil {
					t.Fatal(err)
				}
				if discovery.Codex.Source != wantSource {
					t.Fatalf("settled source = %q, want %q", discovery.Codex.Source, wantSource)
				}
				gotIDs := make([]string, 0, len(discovery.Summaries))
				for _, summary := range discovery.Summaries {
					gotIDs = append(gotIDs, summary.ResumeID)
				}
				if !reflect.DeepEqual(gotIDs, wantIDs) && !(len(gotIDs) == 0 && len(wantIDs) == 0) {
					t.Fatalf("settled rows = %v, want %v (merged or lost source)", gotIDs, wantIDs)
				}
				if len(discovery.DetailRefs) != len(gotIDs) {
					t.Fatalf("detail refs = %d for %d summaries", len(discovery.DetailRefs), len(gotIDs))
				}
				// One terminal snapshot: give both losers time to complete and
				// confirm neither reached the published result. The scan count is
				// read here because a rollout scan the native path cancelled is
				// still entered once, just after this invocation settled.
				time.Sleep(2 * nativeBudget)
				mu.Lock()
				gotScans := scans
				mu.Unlock()
				if gotScans != 1 {
					t.Fatalf("rollout scans = %d, want exactly one per invocation", gotScans)
				}
				gotAfter := make([]string, 0, len(discovery.Summaries))
				for _, summary := range discovery.Summaries {
					gotAfter = append(gotAfter, summary.ResumeID)
				}
				if !reflect.DeepEqual(gotAfter, gotIDs) {
					t.Fatalf("late completion mutated the settled snapshot: %v -> %v", gotIDs, gotAfter)
				}
			})
		}
	}
}

// Native success keeps the authority it always had, and the rollout rows that
// a slow native falls back to carry no endpoint, generation, or state axis:
// only the caller that resolved a route may stamp those, and only on native
// rows.
func TestResumeSummaryNativeAuthorityAndRolloutRowsKeepGenerationAxesEmpty(t *testing.T) {
	const (
		nativeID  = "019f0000-0000-7000-8000-0000000000a1"
		rolloutID = "019f0000-0000-7000-8000-0000000000b2"

		nativeBudget   = 60 * time.Millisecond
		fallbackBudget = 400 * time.Millisecond
	)
	rolloutRow := func(ctx context.Context, _, _ string, _ int) []SessionMeta {
		return []SessionMeta{{
			Agent: AgentCodex, ResumeID: rolloutID, Title: "rollout row",
			Context: SessionContext{CWD: "/work/app"}, Source: SourceCodexRollout,
		}}
	}

	t.Run("native rows keep authority without merging rollout rows", func(t *testing.T) {
		discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
			DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(&fakeCodexCatalog{
				pages: []codexappserver.CatalogPage{{Threads: []codexappserver.CatalogThread{{ID: nativeID, CWD: "/work/app"}}}},
			})},
			NativeBudget: nativeBudget, FallbackBudget: fallbackBudget, discoverCodexFallback: rolloutRow,
		}, 20)
		if err != nil {
			t.Fatal(err)
		}
		if discovery.Codex.Source != CatalogSourceNative || discovery.Codex.Confidence != CatalogConfidenceHigh {
			t.Fatalf("native authority = %#v", discovery.Codex)
		}
		if len(discovery.Summaries) != 1 || discovery.Summaries[0].ResumeID != nativeID || discovery.Summaries[0].Source != SourceCodexAppServer {
			t.Fatalf("native rows merged a rollout row: %#v", discovery.Summaries)
		}
	})

	t.Run("native empty keeps authority over a retained rollout row", func(t *testing.T) {
		discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
			DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(&fakeCodexCatalog{
				pages: []codexappserver.CatalogPage{{}},
			})},
			NativeBudget: nativeBudget, FallbackBudget: fallbackBudget, discoverCodexFallback: rolloutRow,
		}, 20)
		if err != nil {
			t.Fatal(err)
		}
		if discovery.Codex.Source != CatalogSourceNative || len(discovery.Summaries) != 0 || len(discovery.DetailRefs) != 0 {
			t.Fatalf("native-empty authority = %#v", discovery)
		}
	})

	t.Run("retained rollout rows carry no generation axis", func(t *testing.T) {
		discovery, err := DiscoverResumeSummariesContext(context.Background(), AgentCodex, "/work/app", ResumeSummaryOptions{
			DiscoverOptions: DiscoverOptions{CodexSessionsDir: t.TempDir(), OpenCodexCatalog: func(ctx context.Context) (CodexCatalog, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}},
			NativeBudget: nativeBudget, FallbackBudget: fallbackBudget, discoverCodexFallback: rolloutRow,
		}, 20)
		if err != nil {
			t.Fatal(err)
		}
		if discovery.Codex.Source != CatalogSourceFallback || discovery.Codex.Confidence != CatalogConfidenceMedium {
			t.Fatalf("rollout settlement = %#v", discovery.Codex)
		}
		if len(discovery.Summaries) != 1 || len(discovery.DetailRefs) != 1 {
			t.Fatalf("retained rollout rows = %#v", discovery)
		}
		summary := discovery.Summaries[0]
		if summary.ResumeID != rolloutID || summary.Source != SourceCodexRollout {
			t.Fatalf("retained rollout row id/source = %#v", summary)
		}
		if summary.StateDomainID != "" || summary.EndpointGenerationID != "" || summary.GenerationState != "" {
			t.Fatalf("rollout row synthesized a generation axis: %#v", summary)
		}
		ref := discovery.DetailRefs[0]
		if ref.StateDomainID != "" || ref.EndpointGenerationID != "" || ref.GenerationState != "" {
			t.Fatalf("rollout detail ref synthesized a generation axis: %#v", ref)
		}
		if ref.Confidence != ConfidenceMedium || ref.Reason != ReasonAppServerTimeout {
			t.Fatalf("rollout detail ref confidence/reason = %q/%q", ref.Confidence, ref.Reason)
		}
	})
}

// writeScaleRolloutTree writes an 828-session-equivalent store: exact-cwd rows
// first, then rows nested one to five levels under the base, then rows owned
// by another repo. Recency order matters because the bounded scan reads the
// newest files within its file budget.
func writeScaleRolloutTree(t *testing.T, root, base string, exact, nested, foreign int) {
	t.Helper()

	day := filepath.Join(root, "2026", "08", "24")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	newest := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	write := func(index int, cwd string) {
		id := fmt.Sprintf("019f0000-0000-7000-8000-%012d", index)
		path := filepath.Join(day, fmt.Sprintf("rollout-%04d.jsonl", index))
		writeFile(t, path, fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}
{"type":"event_msg","payload":{"type":"user_message","message":"scale row"}}
`, id, cwd))
		modTime := newest.Add(-time.Duration(index) * time.Second)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	index := 0
	for range exact {
		write(index, base)
		index++
	}
	for i := range nested {
		depth := i%5 + 1
		cwd := base
		for level := 1; level <= depth; level++ {
			cwd = filepath.Join(cwd, fmt.Sprintf("level%d", level))
		}
		write(index, cwd)
		index++
	}
	for range foreign {
		write(index, "/other/repo")
		index++
	}
}

// A native attempt that spends its whole bound and fails must leave the
// depth-aware rollout result intact at 828-session scale: the exact rows the
// scan completed, all of them sourced as rollout.
func TestResumeSummarySlowNativeRetainsDepthAwareRolloutRowsAtScale(t *testing.T) {
	const (
		base         = "/work/app"
		exactRows    = 43
		nestedRows   = 115
		foreignRows  = 670
		nativeBudget = 60 * time.Millisecond
	)
	root := t.TempDir()
	writeScaleRolloutTree(t, root, base, exactRows, nestedRows, foreignRows)

	for _, tc := range []struct {
		depth    int
		wantRows int
	}{
		{depth: 0, wantRows: exactRows},
		{depth: 5, wantRows: exactRows + nestedRows},
	} {
		t.Run(fmt.Sprintf("depth-%d", tc.depth), func(t *testing.T) {
			// The caller deadline is already spent: only a stage that owns its
			// own bound can still produce these rows.
			caller, cancelCaller := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancelCaller()
			<-caller.Done()

			startedAt := time.Now()
			discovery, err := DiscoverResumeSummariesContext(caller, AgentCodex, base, ResumeSummaryOptions{
				DiscoverOptions: DiscoverOptions{
					HomeDir: t.TempDir(), CodexSessionsDir: root, Depth: tc.depth,
					OpenCodexCatalog: func(ctx context.Context) (CodexCatalog, error) {
						<-ctx.Done() // native spends its whole bound and fails
						return nil, ctx.Err()
					},
				},
				NativeBudget: nativeBudget,
			}, 0)
			if err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(startedAt)
			if elapsed >= DefaultResumeSummaryFallbackBudget {
				t.Fatalf("depth-%d scan settled in %s, want inside the declared %s bound", tc.depth, elapsed, DefaultResumeSummaryFallbackBudget)
			}
			if discovery.Codex.Source != CatalogSourceFallback || discovery.Codex.Reason != CatalogReason(ReasonAppServerTimeout) {
				t.Fatalf("scale settlement = %#v", discovery.Codex)
			}
			if len(discovery.Summaries) != tc.wantRows {
				t.Fatalf("depth-%d rollout rows = %d, want the completed %d", tc.depth, len(discovery.Summaries), tc.wantRows)
			}
			if discovery.MoreNotLoaded {
				t.Fatal("a rollout result claimed a native continuation")
			}
			seen := make(map[string]bool, len(discovery.Summaries))
			for _, summary := range discovery.Summaries {
				if summary.Source != SourceCodexRollout {
					t.Fatalf("scale row source = %q", summary.Source)
				}
				if summary.ResumeID == "" || seen[summary.ResumeID] {
					t.Fatalf("scale row id = %q (empty or duplicate)", summary.ResumeID)
				}
				seen[summary.ResumeID] = true
				if summary.StateDomainID != "" || summary.EndpointGenerationID != "" || summary.GenerationState != "" {
					t.Fatalf("scale rollout row synthesized a generation axis: %#v", summary)
				}
			}
		})
	}
}
