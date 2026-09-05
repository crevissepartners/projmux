package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestAIResumeTitleAuthorityProviderSourceProvenanceMatrix(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000beef"
	type authority struct {
		provider, source string
		provenance       aisessions.TitleProvenance
	}
	allowed := map[authority]bool{
		{aiModeClaude, aisessions.SourceClaudeTranscript, aisessions.TitleExplicitProvider}:               true,
		{aiModeCodex, aisessions.SourceCodexAppServer, aisessions.TitleExplicitProvider}:                  true,
		{aiModeCodex, aisessions.SourceCodexRollout, aisessions.TitleDerivedUserPrompt}:                   true,
		{aiModeAntigravity, aisessions.SourceAntigravityHistory, aisessions.TitleProvenanceNone}:          true,
		{aiModeAntigravity, aisessions.SourceAntigravityMetadata, aisessions.TitleProvenanceNone}:         true,
		{aiModeAntigravity, aisessions.SourceAntigravityLastConversation, aisessions.TitleProvenanceNone}: true,
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex, aiModeAntigravity, "unknown"} {
		for _, source := range []string{aisessions.SourceClaudeTranscript, aisessions.SourceCodexAppServer, aisessions.SourceCodexRollout, aisessions.SourceAntigravityHistory, aisessions.SourceAntigravityMetadata, aisessions.SourceAntigravityLastConversation, "", "unknown"} {
			for _, provenance := range []aisessions.TitleProvenance{aisessions.TitleProvenanceNone, aisessions.TitleExplicitProvider, aisessions.TitleDerivedUserPrompt, "unknown"} {
				for _, candidate := range []string{"Canonical title", "", " \t ", id, strings.ToUpper(id), aiResumeShortID(id), id[:8]} {
					for _, bound := range []registryview.Context{{}, {Value: "Exact Agent topic", Source: registryview.ContextSourceAgentTopic}, {Value: provider, Source: registryview.ContextSourceAgentProvider}} {
						summary := aisessions.ResumeSummary{Provider: provider, ResumeID: id, Source: source, Label: candidate, TitleProvenance: provenance}
						session := aiResumeSessionMetaFromSummary(summary, "")
						want := "Untitled · …beef"
						if bound.Value != "" {
							want = bound.Value
						}
						if candidate == "Canonical title" && allowed[authority{provider, source, provenance}] && (provider != aiModeAntigravity || bound.Value == "") {
							want = candidate
						}
						label := aiResumeExactAgentLabel{Name: "exact-agent", PaneName: "exact-pane", Context: bound}
						if got := aiResumeDisplayLabel(session, label, i18n.FallbackLocale); got != want {
							t.Fatalf("provider=%s source=%s provenance=%q candidate=%q bound=%#v: got %q, want %q", provider, source, provenance, candidate, bound, got, want)
						}
						row := aiResumeSessionRowWithResolvedLabel(session, label, time.Time{}, i18n.FallbackLocale, "", 0)
						if !strings.Contains(row.SearchKey, id) || !strings.Contains(row.SearchKey, "exact-agent") || !strings.Contains(row.SearchKey, "exact-pane") || row.Value != aiResumePickerValueForSession(session) {
							t.Fatalf("authority changed exact binding or routing: %#v", row)
						}
						if session.TitleProvenance != summary.TitleProvenance {
							t.Fatal("summary reconstruction dropped provenance")
						}
					}
				}
			}
		}
	}
}

func TestAIResumeTitleDiscoverySummaryRoundTripNeedsNoTranscriptReread(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000beef"
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			claudeDir, codexDir := filepath.Join(root, "claude"), filepath.Join(root, "codex")
			path := filepath.Join(claudeDir, aisessions.EncodeClaudeProjectPath("/work"), "session.jsonl")
			content := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":"/work","gitBranch":"main","message":{"content":"Ordinary prompt"}}
{"type":"ai-title","sessionId":%q,"aiTitle":"Canonical title"}
`, id, id)
			want := aisessions.TitleExplicitProvider
			if provider == aiModeCodex {
				path = filepath.Join(codexDir, "rollout-session.jsonl")
				content = fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":"/work","git":{"branch":"main"}}}
{"type":"event_msg","payload":{"type":"user_message","message":"Canonical title"}}
`, id)
				want = aisessions.TitleDerivedUserPrompt
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			discovery, err := aisessions.DiscoverResumeSummariesContext(context.Background(), provider, "/work", aisessions.ResumeSummaryOptions{DiscoverOptions: aisessions.DiscoverOptions{HomeDir: root, ClaudeProjectsDir: claudeDir, CodexSessionsDir: codexDir}}, 20)
			if err != nil || len(discovery.Summaries) != 1 {
				t.Fatalf("discovery=%#v err=%v", discovery, err)
			}
			// Projection after discovery must not depend on transcript availability.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			summary := discovery.Summaries[0]
			session := aiResumeSessionMetaFromSummary(summary, "/work")
			if summary.TitleProvenance != want || session.TitleProvenance != want || session.Title != "Canonical title" || session.ResumeID != id {
				t.Fatalf("roundtrip=%#v -> %#v", summary, session)
			}
			for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
				rows := aiResumeSummaryRowsWithLabels(discovery.Summaries, nil, time.Time{}, locale, "/work", 0)
				detail := aiResumeDetailProjection(locale, summary, discovery.DetailRefs[0], aisessions.ResumeDetail{}, "Independent preview")
				if len(rows) != 2 || rows[1].Value != aiResumePickerValueForSession(session) || !strings.HasSuffix(stripANSI(rows[1].Label), "Canonical title") || !strings.HasPrefix(detail, provider+" · Canonical title\n") || !strings.Contains(detail, id) {
					t.Fatalf("roundtrip rows=%#v detail=%q", rows, detail)
				}
			}
		})
	}
}

func resumeTitleNativeFixture() ([]aisessions.ResumeSummary, coremetadata.Registry) {
	summaries := []aisessions.ResumeSummary{
		{Provider: aiModeClaude, ResumeID: "019f0000-0000-7000-8000-000000000001", Source: aisessions.SourceClaudeTranscript, Label: "Claude title", TitleProvenance: aisessions.TitleExplicitProvider, Branch: "main"},
		{Provider: aiModeCodex, ResumeID: "019f0000-0000-7000-8000-000000000002", Source: aisessions.SourceCodexRollout, Label: "Rollout prompt", TitleProvenance: aisessions.TitleDerivedUserPrompt, Branch: "main"},
		{Provider: aiModeCodex, ResumeID: "019f0000-0000-7000-8000-000000000003", Source: aisessions.SourceCodexAppServer, Label: "Native title", TitleProvenance: aisessions.TitleExplicitProvider, Branch: "main", StateDomainID: "state-title", EndpointGenerationID: "generation-title", GenerationState: string(coremetadata.CodexGenerationCurrent)},
	}
	var registry coremetadata.Registry
	for i, summary := range summaries {
		name := []string{"a", "c", "n"}[i]
		ref := &coremetadata.AgentSessionRef{Provider: summary.Provider}
		if summary.Provider == aiModeClaude {
			ref.Claude = &coremetadata.ClaudeSessionRef{SessionID: summary.ResumeID}
		} else {
			ref.Codex = &coremetadata.CodexSessionRef{ThreadID: summary.ResumeID}
		}
		registry.Agents = append(registry.Agents, coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-" + name, Name: name, Annotations: map[string]string{coremetadata.AnnotationAgentTopic: "Lower priority topic"}}, Spec: coremetadata.AgentSpec{Provider: summary.Provider}, Status: coremetadata.AgentStatus{PaneRef: "pane-" + name, SessionRef: ref}})
		registry.Panes = append(registry.Panes, coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pane-" + name, Name: name + "-pane", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: "agent-" + name}}})
	}
	return summaries, registry
}

func TestAIResumeProviderTitleBindingNativeFramesGolden(t *testing.T) {
	if locale := os.Getenv("_PROJMUX_TITLE_NATIVE_FIXTURE"); locale != "" {
		summaries, registry := resumeTitleNativeFixture()
		cmd := testAICommand(t.TempDir())
		cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }
		controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
		defer controller.close()
		controller.locale = i18n.Locale(locale)
		rows := controller.entries(summaries)
		items := make([]intpicker.Item, len(rows))
		details := make(map[string]string)
		selected := 1
		for i, row := range rows {
			items[i] = intpicker.Item{Label: row.Label, Title: row.Label, Value: row.Value, SearchText: row.SearchKey}
			if i == 0 {
				continue
			}
			summary := summaries[i-1]
			if summary.Source == os.Getenv("_PROJMUX_TITLE_NATIVE_SOURCE") {
				selected = i
			}
			binding := controller.labels[aiResumeExactLabelKey(summary.Provider, summary.ResumeID)]
			details[row.Value] = aiResumeDetailProjection(controller.locale, summary, aisessions.ResumeDetailRef{Source: summary.Source}, aisessions.ResumeDetail{}, "Independent preview", binding)
		}
		result, err := (intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout}).Run(intpicker.Options{UI: "ai-resume-picker", Locale: controller.locale, Title: "AI Resume", Prompt: "AI Resume > ", Items: items, InitialIndex: selected, InitialIndexSet: true, SelectionDetail: &intpicker.SelectionDetail{TextByValue: details}})
		if err != nil || !result.Closed {
			t.Fatalf("native close = %#v, %v", result, err)
		}
		return
	}
	var golden strings.Builder
	summaries, _ := resumeTitleNativeFixture()
	driver := strings.ReplaceAll(resumeBindingPTYDriver, "_PROJMUX_BINDING_NATIVE_FIXTURE", "_PROJMUX_TITLE_NATIVE_FIXTURE")
	driver = strings.ReplaceAll(driver, "TestAIResumeBindingNativeFramesLocaleAndSizeGolden", "TestAIResumeProviderTitleBindingNativeFramesGolden")
	for _, locale := range []string{"en-US", "ko-KR"} {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			for _, summary := range summaries {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				command := exec.CommandContext(ctx, "python3", "-c", driver, os.Args[0], locale, fmt.Sprint(size[0]), fmt.Sprint(size[1]))
				command.Env = append(os.Environ(), "_PROJMUX_TITLE_NATIVE_SOURCE="+summary.Source)
				output, err := command.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("native %s %v %s: %v\n%s", locale, size, summary.Source, err, output)
				}
				frame := regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`).ReplaceAllString(string(output), "")
				lines := strings.Split(strings.TrimRight(strings.ReplaceAll(frame, "\r", ""), "\n"), "\n")
				if len(lines) != size[1] {
					t.Fatalf("frame lines=%d want %d", len(lines), size[1])
				}
				fmt.Fprintf(&golden, "[%s %dx%d %s]\n", locale, size[0], size[1], summary.Source)
				for _, line := range lines {
					if i18n.TerminalCellWidth(line) != size[0] {
						t.Fatalf("frame width drift: %q", line)
					}
					fmt.Fprintln(&golden, strings.TrimRight(line, " "))
				}
				for _, row := range []string{"[a → a-pane] Claude title", "[c → c-pane] Rollout prompt", "[n → n-pane] Native title"} {
					if !strings.Contains(frame, row) {
						t.Fatalf("native row missing %q:\n%s", row, frame)
					}
				}
				if !strings.Contains(frame, summary.Provider+" · "+summary.Label) || !strings.Contains(frame, summary.ResumeID) || strings.Contains(frame, "Lower priority topic") {
					t.Fatalf("native title heading / exact ID drift:\n%s", frame)
				}
			}
		}
	}
	path := filepath.Join("testdata", "ai-resume-provider-title-native.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(golden.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if golden.String() != string(want) {
		t.Fatalf("native provider title golden mismatch:\n%s", golden.String())
	}
}
