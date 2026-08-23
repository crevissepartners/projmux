package aisessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	_, err := DiscoverProviderContext(context.Background(), AgentCodex, "/work", DiscoverOptions{
		HomeDir: t.TempDir(), OpenCodexCatalog: openFakeCodexCatalog(fake),
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != InteractiveCatalogPageBudget {
		t.Fatalf("calls = %d, want page budget %d", len(fake.calls), InteractiveCatalogPageBudget)
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
