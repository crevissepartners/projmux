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
	if detailRef.transcriptPath != session.sourcePath || detailRef.ResumeID != summary.ResumeID || detailRef.Source != summary.Source {
		t.Fatalf("separate detail ref = %#v", detailRef)
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
