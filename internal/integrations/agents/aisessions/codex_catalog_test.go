package aisessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type fakeCodexCatalog struct {
	pages  []codexappserver.CatalogPage
	errAt  int
	calls  []codexappserver.CatalogQuery
	closed int
}

func (f *fakeCodexCatalog) List(_ context.Context, query codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	f.calls = append(f.calls, query)
	if f.errAt > 0 && len(f.calls) == f.errAt {
		return codexappserver.CatalogPage{}, codexappserver.ErrProtocol
	}
	if len(f.calls) > len(f.pages) {
		return codexappserver.CatalogPage{}, errors.New("unexpected page")
	}
	return f.pages[len(f.calls)-1], nil
}
func (f *fakeCodexCatalog) Read(context.Context, string) (codexappserver.CatalogThread, error) {
	return codexappserver.CatalogThread{}, nil
}
func (f *fakeCodexCatalog) Close() error { f.closed++; return nil }

func openFakeCodexCatalog(fake *fakeCodexCatalog) OpenCodexCatalog {
	return func(context.Context) (CodexCatalog, error) { return fake, nil }
}

func TestNativeCatalogPaginationDepthNameStatusOrderAndNoPromptTitleInference(t *testing.T) {
	cursor := " opaque:page-2 "
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{
			{ID: "019f0000-0000-7000-8000-000000000041", CWD: "/work/app", Name: "", Branch: "main", RecencyAt: time.Unix(20, 0), RuntimeStatus: "idle"},
			{ID: "019f0000-0000-7000-8000-000000000042", CWD: "/work/app/child", Name: "<environment_context>provider-owned</environment_context>", RecencyAt: time.Unix(30, 0), RuntimeStatus: "active"},
		}, NextCursor: &cursor},
		{Threads: []codexappserver.CatalogThread{
			{ID: "019f0000-0000-7000-8000-000000000043", CWD: "/work/sibling", Name: "excluded", RecencyAt: time.Unix(40, 0), RuntimeStatus: "notLoaded"},
		}},
	}}
	discovery, err := DiscoverWithOutcomeContext(context.Background(), "/work/app", DiscoverOptions{
		Depth: 1, ClaudeProjectsDir: t.TempDir(), CodexSessionsDir: t.TempDir(),
		AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing"), OpenCodexCatalog: openFakeCodexCatalog(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Codex != (CodexCatalogOutcome{Source: CatalogSourceNative, Confidence: CatalogConfidenceHigh}) {
		t.Fatalf("outcome = %#v", discovery.Codex)
	}
	if len(fake.calls) != 2 || fake.calls[0].Cursor != nil || fake.calls[0].CWD != "" ||
		fake.calls[1].Cursor == nil || *fake.calls[1].Cursor != cursor || fake.closed != 1 {
		t.Fatalf("calls=%#v closed=%d", fake.calls, fake.closed)
	}
	if len(discovery.Sessions) != 2 || discovery.Sessions[0].ResumeID != "019f0000-0000-7000-8000-000000000042" ||
		discovery.Sessions[0].Title != "<environment_context>provider-owned</environment_context>" ||
		discovery.Sessions[0].RuntimeStatus != "active" || discovery.Sessions[1].Title != "019f0000-000" {
		t.Fatalf("sessions = %#v", discovery.Sessions)
	}
}

func TestNativeCatalogDedupesByExactIDAndOrdersRecencyDeterministically(t *testing.T) {
	id1 := "019f0000-0000-7000-8000-000000000041"
	id2 := "019f0000-0000-7000-8000-000000000042"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{{Threads: []codexappserver.CatalogThread{
		{ID: id2, CWD: "/work/app", Name: "older duplicate", RecencyAt: time.Unix(10, 0), RuntimeStatus: "idle"},
		{ID: id1, CWD: "/work/app", Name: "same recency lower id", RecencyAt: time.Unix(30, 0), RuntimeStatus: "idle"},
		{ID: id2, CWD: "/work/app", Name: "newer duplicate", RecencyAt: time.Unix(30, 0), RuntimeStatus: "active"},
	}}}}
	discovery, err := DiscoverWithOutcomeContext(context.Background(), "/work/app", DiscoverOptions{
		ClaudeProjectsDir: t.TempDir(), CodexSessionsDir: t.TempDir(),
		AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing"), OpenCodexCatalog: openFakeCodexCatalog(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 2 || discovery.Sessions[0].ResumeID != id1 ||
		discovery.Sessions[1].ResumeID != id2 || discovery.Sessions[1].Title != "newer duplicate" {
		t.Fatalf("sessions=%#v", discovery.Sessions)
	}
}

func TestMalformedPaginationDiscardsNativeAndRunsOneAnnotatedRolloutFallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "08", "22")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "019f0000-0000-7000-8000-000000000077"
	path := filepath.Join(dir, "rollout-fallback.jsonl")
	writeFile(t, path, `{"type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/app"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Fallback title"}}
`)
	next := "same"
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000088", CWD: "/work/app", Name: "discard me", RecencyAt: time.Unix(40, 0), RuntimeStatus: "idle"}}, NextCursor: &next},
		{Threads: []codexappserver.CatalogThread{{ID: "019f0000-0000-7000-8000-000000000089", CWD: "/work/app", Name: "discard too", RecencyAt: time.Unix(30, 0), RuntimeStatus: "idle"}}, NextCursor: &next},
	}}
	discovery, err := DiscoverWithOutcomeContext(context.Background(), "/work/app", DiscoverOptions{
		ClaudeProjectsDir: t.TempDir(), CodexSessionsDir: root,
		AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing"), OpenCodexCatalog: openFakeCodexCatalog(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOutcome := CodexCatalogOutcome{Source: CatalogSourceFallback, Confidence: CatalogConfidenceMedium, Reason: CatalogReason(ReasonMalformedPagination)}
	if discovery.Codex != wantOutcome || len(fake.calls) != 2 || len(discovery.Sessions) != 1 {
		t.Fatalf("outcome=%#v calls=%d sessions=%#v", discovery.Codex, len(fake.calls), discovery.Sessions)
	}
	row := discovery.Sessions[0]
	if row.ResumeID != id || row.Source != SourceCodexRollout || row.Confidence != ConfidenceMedium || row.Reason != ReasonMalformedPagination {
		t.Fatalf("fallback row = %#v", row)
	}
}

func TestEmptyAndExhaustedCatalogPaginationFallBackAsMalformed(t *testing.T) {
	thread := func(index int) codexappserver.CatalogThread {
		return codexappserver.CatalogThread{
			ID: fmt.Sprintf("019f0000-0000-7000-8000-%012d", index), CWD: "/work/app",
			Name: "native", RecencyAt: time.Unix(int64(index+1), 0), RuntimeStatus: "idle",
		}
	}
	empty := ""
	pagesAtBudget := make([]codexappserver.CatalogPage, codexCatalogMaxPages)
	for i := range pagesAtBudget {
		next := fmt.Sprintf("opaque:%d", i+1)
		pagesAtBudget[i] = codexappserver.CatalogPage{Threads: []codexappserver.CatalogThread{thread(i + 1)}, NextCursor: &next}
	}
	for _, test := range []struct {
		name      string
		pages     []codexappserver.CatalogPage
		wantCalls int
	}{
		{name: "empty cursor", pages: []codexappserver.CatalogPage{{Threads: []codexappserver.CatalogThread{thread(1)}, NextCursor: &empty}}, wantCalls: 1},
		{name: "page budget exhausted", pages: pagesAtBudget, wantCalls: codexCatalogMaxPages},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeCodexCatalog{pages: test.pages}
			discovery, err := DiscoverWithOutcomeContext(context.Background(), "/work/app", DiscoverOptions{
				ClaudeProjectsDir: t.TempDir(), CodexSessionsDir: t.TempDir(),
				AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing"), OpenCodexCatalog: openFakeCodexCatalog(fake),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.calls) != test.wantCalls || len(discovery.Sessions) != 0 ||
				discovery.Codex.Reason != CatalogReason(ReasonMalformedPagination) {
				t.Fatalf("calls=%d discovery=%#v", len(fake.calls), discovery)
			}
		})
	}
}

func TestEmptyNativeCatalogDoesNotMergeExistingRollout(t *testing.T) {
	fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{{}}}
	discovery, err := DiscoverWithOutcomeContext(context.Background(), "/work/app", DiscoverOptions{
		ClaudeProjectsDir: t.TempDir(), CodexSessionsDir: filepath.Join("testdata", "discover", "codex", "sessions"),
		AntigravityHistoryPath: filepath.Join(t.TempDir(), "missing"), OpenCodexCatalog: openFakeCodexCatalog(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 0 || discovery.Codex.Source != CatalogSourceNative {
		t.Fatalf("native empty merged fallback: %#v", discovery)
	}
}

func TestCatalogOutcomeTypesStayClosed(t *testing.T) {
	got := []CatalogReason{CatalogReasonNone, CatalogReason(ReasonAppServerUnavailable), CatalogReason(ReasonAppServerUnsupported), CatalogReason(ReasonAppServerTimeout), CatalogReason(ReasonAppServerProtocol), CatalogReason(ReasonMalformedPagination)}
	want := []CatalogReason{"", "app-server-unavailable", "app-server-unsupported", "app-server-timeout", "app-server-protocol", "malformed-pagination"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons=%v want=%v", got, want)
	}
}

func TestCatalogFallbackReasonClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want CatalogReason
	}{
		{name: "unavailable", err: errors.New("socket unavailable"), want: CatalogReason(ReasonAppServerUnavailable)},
		{name: "unsupported", err: codexappserver.ErrUnsupported, want: CatalogReason(ReasonAppServerUnsupported)},
		{name: "timeout", err: context.DeadlineExceeded, want: CatalogReason(ReasonAppServerTimeout)},
		{name: "protocol", err: codexappserver.ErrProtocol, want: CatalogReason(ReasonAppServerProtocol)},
		{name: "pagination", err: errMalformedCatalogPagination, want: CatalogReason(ReasonMalformedPagination)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := codexCatalogFallbackReason(test.err); got != test.want {
				t.Fatalf("reason=%q want=%q", got, test.want)
			}
		})
	}
}
