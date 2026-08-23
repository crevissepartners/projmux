package aisessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func TestInteractiveCodexDiscoveryStopsAtInitialPageBudget(t *testing.T) {
	pages := make([]codexappserver.CatalogPage, InteractiveCatalogPageBudget+1)
	for i := range pages {
		next := fmt.Sprintf("cursor-%d", i+1)
		pages[i] = codexappserver.CatalogPage{Threads: []codexappserver.CatalogThread{{
			ID: fmt.Sprintf("019f0000-0000-7000-8000-%012d", i), CWD: "/other", Name: "outside",
		}}, NextCursor: &next}
	}
	fake := &fakeCodexCatalog{pages: pages}
	discovery, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{
		HomeDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(fake),
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != InteractiveCatalogPageBudget {
		t.Fatalf("calls = %d, want page budget %d", len(fake.calls), InteractiveCatalogPageBudget)
	}
	if discovery.Continuation == nil {
		t.Fatal("initial page budget discarded exact continuation")
	}
	_ = discovery.Continuation.Close()
}

func TestInteractiveCodexContinuationKeepsOneConnectionAndExactPageFourCursor(t *testing.T) {
	cursors := []string{"opaque:page-2", "opaque:page-3", "opaque:page-4"}
	pageFourID := "019f0000-0000-7000-8000-000000000044"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000041", CWD: "/other"}}, NextCursor: &cursors[0]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000042", CWD: "/other"}}, NextCursor: &cursors[1]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000043", CWD: "/other"}}, NextCursor: &cursors[2]},
		{Threads: []codexappserver.CatalogThread{{ID: pageFourID, CWD: "/work", Name: "page four exact", RecencyAt: time.Unix(44, 0)}}, NextCursor: nil},
	}}
	opens := 0
	discovery, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{
		HomeDir: t.TempDir(), OpenCodexCatalog: func(context.Context) (CodexCatalog, error) {
			opens++
			return fake, nil
		},
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 || len(discovery.Sessions) != 0 || discovery.Continuation == nil || fake.closed != 0 {
		t.Fatalf("initial opens=%d sessions=%#v continuation=%v closed=%d", opens, discovery.Sessions, discovery.Continuation != nil, fake.closed)
	}
	wantInitialCursors := []*string{nil, &cursors[0], &cursors[1]}
	for i, want := range wantInitialCursors {
		if (fake.calls[i].Cursor == nil) != (want == nil) || want != nil && *fake.calls[i].Cursor != *want {
			t.Fatalf("initial call %d cursor=%v want=%v", i+1, fake.calls[i].Cursor, want)
		}
	}
	continued, err := discovery.Continuation.Continue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 || len(fake.calls) != 4 || fake.calls[3].Cursor == nil || *fake.calls[3].Cursor != cursors[2] || fake.closed != 1 {
		t.Fatalf("continuation opens=%d calls=%#v closed=%d", opens, fake.calls, fake.closed)
	}
	if continued.HasMore || len(continued.Sessions) != 1 || continued.Sessions[0].ResumeID != pageFourID {
		t.Fatalf("continued=%#v", continued)
	}
}

func TestInteractiveCodexContinuationRejectsRepeatedCursorWithoutAdmittingFaultPage(t *testing.T) {
	cursors := []string{"page-2", "page-3", "page-4"}
	initialID := "019f0000-0000-7000-8000-000000000041"
	faultID := "019f0000-0000-7000-8000-000000000044"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{{ID: initialID, CWD: "/work", Name: "keep"}}, NextCursor: &cursors[0]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000042", CWD: "/other"}}, NextCursor: &cursors[1]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000043", CWD: "/other"}}, NextCursor: &cursors[2]},
		{Threads: []codexappserver.CatalogThread{{ID: faultID, CWD: "/work", Name: "must not publish"}}, NextCursor: &cursors[2]},
	}}
	discovery, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{HomeDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(fake)}, 20)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := discovery.Continuation.Continue(context.Background())
	if !errors.Is(err, errMalformedCatalogPagination) || continued.Reason != CatalogReason(ReasonMalformedPagination) || continued.HasMore {
		t.Fatalf("continued=%#v err=%v", continued, err)
	}
	if len(continued.Sessions) != 1 || continued.Sessions[0].ResumeID != initialID || fake.closed != 1 {
		t.Fatalf("fault page escaped: continued=%#v closed=%d", continued, fake.closed)
	}
	for _, session := range continued.Sessions {
		if session.ResumeID == faultID {
			t.Fatalf("malformed page row admitted: %#v", continued.Sessions)
		}
	}
}

func TestInteractiveCodexContinuationRejectsBlankCursorWithoutFallbackOrFaultPage(t *testing.T) {
	cursors := []string{"page-2", "page-3", "page-4"}
	blank := "   "
	initialID := "019f0000-0000-7000-8000-000000000041"
	faultID := "019f0000-0000-7000-8000-000000000044"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{{ID: initialID, CWD: "/work", Name: "keep"}}, NextCursor: &cursors[0]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000042", CWD: "/other"}}, NextCursor: &cursors[1]},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000043", CWD: "/other"}}, NextCursor: &cursors[2]},
		{Threads: []codexappserver.CatalogThread{{ID: faultID, CWD: "/work", Name: "must not publish"}}, NextCursor: &blank},
	}}
	discovery, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{HomeDir: t.TempDir(), CodexSessionsDir: filepath.Join("testdata", "discover", "codex", "sessions"), OpenCodexCatalog: openFakeCodexCatalog(fake)}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Codex.Source != CatalogSourceNative || discovery.Continuation == nil {
		t.Fatalf("initial native authority=%#v", discovery)
	}
	continued, err := discovery.Continuation.Continue(context.Background())
	if !errors.Is(err, errMalformedCatalogPagination) || continued.Reason != CatalogReason(ReasonMalformedPagination) {
		t.Fatalf("continued=%#v err=%v", continued, err)
	}
	if len(continued.Sessions) != 1 || continued.Sessions[0].ResumeID != initialID || fake.closed != 1 {
		t.Fatalf("blank cursor crossed native boundary: continued=%#v closed=%d", continued, fake.closed)
	}
	for _, session := range continued.Sessions {
		if session.ResumeID == faultID || session.Source == SourceCodexRollout {
			t.Fatalf("fault page or rollout fallback merged after initial publish: %#v", continued.Sessions)
		}
	}
}

func TestInteractiveCodexVisibleLimitClosesInitialCursorContentFree(t *testing.T) {
	next := "page-2"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{{
		Threads:    []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000041", CWD: "/work", Name: "visible"}},
		NextCursor: &next,
	}}}
	discovery, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{HomeDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(fake)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || discovery.Continuation != nil || !discovery.MoreNotLoaded || fake.closed != 1 {
		t.Fatalf("discovery=%#v calls=%d closed=%d", discovery, len(fake.calls), fake.closed)
	}
}

func TestBoundedExcerptKeepsKoreanUTF8Valid(t *testing.T) {
	value := strings.Repeat("가", previewExcerptBytes/3+2)
	got := boundedExcerpt(value)
	if !utf8.ValidString(got) || len(got) > previewExcerptBytes+len("…") {
		t.Fatalf("bounded excerpt invalid or oversized: valid=%v bytes=%d", utf8.ValidString(got), len(got))
	}
}

func TestReadPreviewReadsOnlySelectedLocalTranscriptLatestPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.jsonl")
	data := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"old question"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"최신 질문"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"latest answer"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := ReadPreview(context.Background(), SessionMeta{Agent: AgentClaude, ResumeID: "exact", sourcePath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.User != "최신 질문" || preview.Assistant != "latest answer" {
		t.Fatalf("preview = %#v", preview)
	}
}
