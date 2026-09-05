package aisessions

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const titleFixtureID = "019f0000-0000-7000-8000-00000000beef"

func TestClaudeExplicitTitleProvenanceWithinBoundedScan(t *testing.T) {
	meta := fmt.Sprintf(`{"sessionId":%q,"cwd":"/work","gitBranch":"main","type":"user","message":{"content":"Ordinary Claude prompt"}}`, titleFixtureID)
	explicit := func(title string) string {
		return fmt.Sprintf(`{"type":"ai-title","sessionId":%q,"aiTitle":%q}`, titleFixtureID, title)
	}
	for _, tc := range []struct {
		name       string
		lines      []string
		want       string
		provenance TitleProvenance
	}{
		{"id learned after candidate", []string{fmt.Sprintf(`{"type":"ai-title","aiTitle":%q}`, titleFixtureID), meta, explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"prompt before explicit", []string{meta, explicit("  Canonical   title "), explicit("Later duplicate")}, "Canonical title", TitleExplicitProvider},
		{"ordinary prompt has no authority", []string{meta}, "Ordinary Claude prompt", TitleProvenanceNone},
		{"blank before valid", []string{meta, explicit(" \t "), explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"full id before valid", []string{meta, explicit(strings.ToUpper(titleFixtureID)), explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"short id before valid", []string{meta, explicit(shortResumeID(titleFixtureID)), explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"prefix before valid", []string{meta, explicit(titleFixtureID[:8]), explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"non title records", []string{meta, `{"type":"summary","aiTitle":"Wrong type"}`, `{"type":"ai-title","payload":{"aiTitle":"Nested title"}}`, `{"type":"ai-title","aiTitle":42}`, explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"foreign explicit session", []string{meta, `{"type":"ai-title","sessionId":"foreign","aiTitle":"Foreign title"}`, explicit("Valid title")}, "Valid title", TitleExplicitProvider},
		{"explicit bypasses prompt noise", []string{meta, explicit("<system-reminder> title about reminders")}, "<system-reminder> title about reminders", TitleExplicitProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			details, ok := scanSessionJSONLReaderContext(context.Background(), strings.NewReader(strings.Join(tc.lines, "\n")), sessionScanOptions{provider: AgentClaude, targetCWD: "/work", requireCWD: true})
			if !ok || details.title != tc.want || details.titleProvenance != tc.provenance || details.id != titleFixtureID {
				t.Fatalf("scan = %#v, %v; want title %q provenance %q", details, ok, tc.want, tc.provenance)
			}
		})
	}
	for _, line := range []int{100, 101} {
		lines := []string{meta}
		for len(lines) < line-1 {
			lines = append(lines, `{}`)
		}
		lines = append(lines, explicit("Boundary title"))
		reader := &titleLineReader{lines: lines}
		details, ok := scanSessionJSONLReaderContext(context.Background(), reader, sessionScanOptions{provider: AgentClaude, targetCWD: "/work"})
		if !ok || reader.reads != 100 || (details.titleProvenance == TitleExplicitProvider) != (line == 100) {
			t.Fatalf("line %d: details=%#v reads=%d ok=%v", line, details, reader.reads, ok)
		}
	}
}

// Give Scanner exactly one line per Read, making an attempted line 101 visible.
type titleLineReader struct {
	lines []string
	reads int
}

func (r *titleLineReader) Read(p []byte) (int, error) {
	if r.reads == len(r.lines) {
		return 0, io.EOF
	}
	n := copy(p, r.lines[r.reads]+"\n")
	r.reads++
	return n, nil
}

func TestCodexFirstRealPromptHasDerivedTitleProvenance(t *testing.T) {
	meta := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/work","git":{"branch":"main"}}}`, titleFixtureID)
	for _, prompt := range []string{
		`{"type":"event_msg","payload":{"type":"user_message","message":"First real prompt"}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"tool_result","text":"Tool carrier"},{"type":"input_text","text":"First real prompt"}]}}`,
	} {
		lines := []string{meta,
			`{"type":"event_msg","payload":{"message":"Legacy untyped candidate"}}`,
			`{"type":"user","content":"Noncanonical user shape"}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"Assistant response"}}`,
			`{"type":"response_item","payload":{"role":"user","content":[{"type":"tool_result","text":"Tool carrier"}]}}`,
			`{"type":"ai-title","aiTitle":"Wrong provider explicit"}`,
			`{"type":"event_msg","payload":{"type":"user_message","message":"# AGENTS.md instructions for /work"}}`,
			prompt,
			`{"type":"event_msg","payload":{"type":"user_message","message":"Second real prompt"}}`,
		}
		details, ok := scanSessionJSONLReaderContext(context.Background(), strings.NewReader(strings.Join(lines, "\n")), sessionScanOptions{provider: AgentCodex, targetCWD: "/work", requireCWD: true})
		if !ok || details.title != "First real prompt" || details.titleProvenance != TitleDerivedUserPrompt || details.id != titleFixtureID {
			t.Fatalf("scan = %#v, %v", details, ok)
		}
	}
}

func TestNativeTitleProvenanceRejectsBlankAndIDCandidates(t *testing.T) {
	for _, title := range []string{"", " \t ", titleFixtureID, strings.ToUpper(titleFixtureID), shortResumeID(titleFixtureID), titleFixtureID[:8], "Native title"} {
		fake := &fakeCodexCatalog{pages: []codexappserver.CatalogPage{{Threads: []codexappserver.CatalogThread{{ID: titleFixtureID, CWD: "/work", Name: title}}}}}
		state, err := openCodexNativeCatalogState(context.Background(), "/work", 0, openFakeCodexCatalog(fake))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.readPage(context.Background(), "/work", 0); err != nil {
			t.Fatal(err)
		}
		_ = state.close()
		got := state.sessions()
		want := TitleProvenanceNone
		if title == "Native title" {
			want = TitleExplicitProvider
		}
		if len(got) != 1 || got[0].TitleProvenance != want || got[0].ResumeID != titleFixtureID || len(fake.calls) != 1 {
			t.Fatalf("title %q: sessions=%#v calls=%d", title, got, len(fake.calls))
		}
	}
}

func TestResumeSummaryPreservesTitleProvenanceWithoutAdditionalReads(t *testing.T) {
	for _, provenance := range []TitleProvenance{TitleProvenanceNone, TitleExplicitProvider, TitleDerivedUserPrompt} {
		session := SessionMeta{Agent: AgentCodex, ResumeID: titleFixtureID, Title: "Canonical title", TitleProvenance: provenance, Source: SourceCodexRollout, LastModified: time.Unix(10, 0), sourcePath: "/must-not-be-read"}
		summary, ref := projectResumeSummary(session, "/work", 0)
		if summary.TitleProvenance != provenance || summary.Label != session.Title || summary.ResumeID != session.ResumeID || ref.transcriptPath != session.sourcePath {
			t.Fatalf("projection = %#v, %#v", summary, ref)
		}
	}
}
