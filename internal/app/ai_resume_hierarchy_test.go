package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
)

func TestAIResumeCodexConversationLabelPrecedence(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000041"
	base := aisessions.SessionMeta{
		Agent: aiModeCodex, ResumeID: id, Source: aisessions.SourceCodexAppServer,
	}
	for _, test := range []struct {
		name       string
		title      string
		boundLabel string
		want       string
	}{
		{name: "thread name", title: "Provider conversation", boundLabel: "Registry topic", want: "Provider conversation"},
		{name: "exact bound topic", title: aiResumeShortID(id), boundLabel: "Registry topic", want: "Registry topic"},
		{name: "untitled suffix", title: aiResumeShortID(id), want: "Untitled · …0041"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := base
			session.Title = test.title
			labels := aiResumeExactAgentLabel{Topic: test.boundLabel}
			if got := aiResumeDisplayLabel(session, labels, i18n.FallbackLocale); got != test.want {
				t.Fatalf("conversation label = %q, want %q", got, test.want)
			}
			row := aiResumeSessionRowWithResolvedLabel(session, labels, time.Time{}, i18n.FallbackLocale, "", 0)
			visible := stripANSI(row.Label)
			if !strings.Contains(visible, test.want) {
				t.Fatalf("visible row = %q, want label %q", visible, test.want)
			}
			if test.name == "untitled suffix" && strings.Contains(visible, aiResumeShortID(id)) {
				t.Fatalf("visible row = %q, must not use an id prefix as the title", visible)
			}
		})
	}
}

func TestAIResumeDisplayLabelAuthorityPrecedence(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000beef"
	base := aisessions.SessionMeta{Agent: aiModeCodex, ResumeID: id, Source: aisessions.SourceCodexAppServer}
	for _, test := range []struct {
		name   string
		title  string
		labels aiResumeExactAgentLabel
		locale i18n.Locale
		want   string
		forbid string
	}{
		{name: "display name beats provider", title: "Provider title", labels: aiResumeExactAgentLabel{DisplayName: "My conversation", Topic: "Agent topic", Name: "agent-name"}, want: "My conversation", forbid: "Provider title"},
		{name: "provider beats topic", title: "Provider title", labels: aiResumeExactAgentLabel{Topic: "Agent topic", Name: "agent-name"}, want: "Provider title", forbid: "Agent topic"},
		{name: "topic beats stable name", title: aiResumeShortID(id), labels: aiResumeExactAgentLabel{Topic: "Agent topic", Name: "agent-name"}, want: "Agent topic", forbid: "agent-name"},
		{name: "transcript title is rejected", title: "Prompt-derived transcript title", labels: aiResumeExactAgentLabel{Topic: "Agent topic", Name: "agent-name"}, want: "Agent topic", forbid: "Prompt-derived transcript title"},
		{name: "stable name", title: id, labels: aiResumeExactAgentLabel{Name: "agent-name"}, want: "agent-name", forbid: aiResumeShortID(id)},
		{name: "localized untitled", title: id, locale: i18n.Locale("ko-KR"), want: "제목 없음 · …beef", forbid: aiResumeShortID(id)},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := base
			session.Title = test.title
			if test.name == "transcript title is rejected" {
				session.Agent = aiModeClaude
				session.Source = aisessions.SourceClaudeTranscript
			}
			got := aiResumeDisplayLabel(session, test.labels, test.locale)
			if got != test.want || (test.forbid != "" && strings.Contains(got, test.forbid)) {
				t.Fatalf("label = %q, want %q without %q", got, test.want, test.forbid)
			}
		})
	}
}

func TestAIResumeExactAgentLabelResolverUsesOnlyExactThreadBinding(t *testing.T) {
	const exactID = "thread-exact"
	registry := coremetadata.Registry{Agents: []coremetadata.Agent{
		{
			Spec: coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-z-topic", Name: "agent-name", DisplayName: "Custom display", Annotations: map[string]string{
				coremetadata.AnnotationAgentTopic: "Bound topic",
			}},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: exactID},
			}},
		},
		{
			Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-a-name", Name: "lexically-first-name"},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: exactID},
			}},
		},
		{
			Spec:     coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-other", Name: "wrong-agent"},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-other"},
			}},
		},
		{
			Spec:     coremetadata.AgentSpec{Provider: aiModeClaude},
			Metadata: coremetadata.ObjectMeta{UID: "agt-foreign", Name: "wrong-provider"},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: exactID},
			}},
		},
		{
			Spec:     coremetadata.AgentSpec{Provider: aiModeClaude},
			Metadata: coremetadata.ObjectMeta{UID: "agt-claude", Name: "claude-agent", DisplayName: "Claude display"},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeClaude, Claude: &coremetadata.ClaudeSessionRef{SessionID: "claude-exact"},
			}},
		},
		{
			Spec:     coremetadata.AgentSpec{Provider: aiModeAntigravity},
			Metadata: coremetadata.ObjectMeta{UID: "agt-antigravity", Name: "antigravity-agent"},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeAntigravity, Antigravity: &coremetadata.AntigravitySessionRef{ConversationID: "antigravity-exact"},
			}},
		},
	}}

	labels := aiResumeExactAgentLabels(registry)
	if len(labels) != 4 || labels[aiResumeExactLabelKey(aiModeCodex, exactID)].DisplayName != "Custom display" ||
		labels[aiResumeExactLabelKey(aiModeCodex, exactID)].Topic != "Bound topic" ||
		labels[aiResumeExactLabelKey(aiModeCodex, "thread-other")].Name != "wrong-agent" ||
		labels[aiResumeExactLabelKey(aiModeClaude, "claude-exact")].DisplayName != "Claude display" ||
		labels[aiResumeExactLabelKey(aiModeAntigravity, "antigravity-exact")].Name != "antigravity-agent" {
		t.Fatalf("resolved labels = %#v, want exact Codex bindings with topic/name precedence", labels)
	}
}

func TestAIResumeUntitledSuffixPreservesExactValueSearchAndDetailID(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000cafe"
	session := aisessions.SessionMeta{Agent: aiModeCodex, ResumeID: id, Title: aiResumeShortID(id), Source: aisessions.SourceCodexRollout}
	row := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{}, time.Time{}, i18n.FallbackLocale, "", 0)
	visible := stripANSI(row.Label)
	if !strings.Contains(visible, "Untitled · …cafe") || strings.Contains(visible, aiResumeShortID(id)) {
		t.Fatalf("visible row = %q, want readable untitled suffix without UUID prefix", visible)
	}
	if row.Value != aiResumePickerValue(aiModeCodex, id) || !strings.Contains(row.SearchKey, id) {
		t.Fatalf("exact routing/search lost: %#v", row)
	}
	selection, ok := parseAIResumePickerValue(row.Value)
	if !ok || selection.agent != aiModeCodex || selection.resumeID != id {
		t.Fatalf("exact value no longer parseable: %#v ok=%t", selection, ok)
	}
	summary := aisessions.ResumeSummary{Provider: aiModeCodex, ResumeID: id, Label: session.Title, Source: session.Source}
	detail := aiResumeDetailProjection(i18n.FallbackLocale, summary, aisessions.ResumeDetailRef{Source: session.Source}, aisessions.ResumeDetail{}, "preview", "Untitled · …cafe")
	if !strings.Contains(detail, "Conversation ID: "+id) || !strings.Contains(detail, "Source: "+aisessions.SourceCodexRollout) {
		t.Fatalf("selected detail lost exact id/provenance: %q", detail)
	}
}

func TestAIResumeCodexRuntimeAndFallbackStayOutOfVisibleRow(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000099"
	for _, test := range []struct {
		name       string
		source     string
		confidence string
		reason     string
		status     string
		locale     i18n.Locale
		wantStatus string
	}{
		{name: "native active en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "active", locale: i18n.FallbackLocale, wantStatus: "[active]"},
		{name: "native idle en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "idle", locale: i18n.FallbackLocale, wantStatus: "[idle]"},
		{name: "native idle ko", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "idle", locale: i18n.Locale("ko-KR"), wantStatus: "[대기]"},
		{name: "native not loaded en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "notLoaded", locale: i18n.FallbackLocale, wantStatus: "[not loaded]"},
		{name: "native not loaded ko", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "notLoaded", locale: i18n.Locale("ko-KR"), wantStatus: "[미로드]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := aisessions.SessionMeta{
				Agent: aiModeCodex, ResumeID: id, Title: "Conversation", Source: test.source,
				Confidence: test.confidence, Reason: test.reason, RuntimeStatus: test.status,
				Context: aisessions.SessionContext{Branch: "main"},
			}
			row := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{Topic: "Bound topic"}, time.Time{}, test.locale, "/work", 0)
			visible := stripANSI(row.Label)
			if test.wantStatus != "" && strings.Contains(visible, test.wantStatus) {
				t.Fatalf("visible row = %q, must hide status %q", visible, test.wantStatus)
			}
			if strings.Contains(visible, "[fallback]") {
				t.Fatalf("visible row = %q, must hide fallback qualifier", visible)
			}
			for _, hidden := range []string{aisessions.SourceCodexAppServer, test.confidence, test.reason} {
				if hidden != "" && strings.Contains(visible, hidden) {
					t.Fatalf("visible row leaks raw provenance %q: %q", hidden, visible)
				}
			}
			if !strings.Contains(row.SearchKey, id) {
				t.Fatalf("SearchKey = %q, want exact id %q", row.SearchKey, id)
			}
			if !strings.Contains(row.SearchKey, test.source) {
				t.Fatalf("SearchKey = %q, want frozen routing source %q", row.SearchKey, test.source)
			}
			for _, detailOnly := range []string{test.reason} {
				if detailOnly != "" && strings.Contains(row.SearchKey, detailOnly) {
					t.Fatalf("SearchKey = %q, provenance %q must be selected-detail-only", row.SearchKey, detailOnly)
				}
			}
			if got := row.Value; got != aiResumePickerValue(aiModeCodex, id) {
				t.Fatalf("selection value = %q, want exact resume value %q", got, aiResumePickerValue(aiModeCodex, id))
			}
		})
	}
}

func TestAIResumeRolloutProjectionDoesNotInferConversationTitle(t *testing.T) {
	for _, test := range []struct {
		name, provider, source, id, title, suffix string
	}{
		{name: "codex rollout", provider: aiModeCodex, source: aisessions.SourceCodexRollout, id: "019f0000-0000-7000-8000-000000000077", title: "prompt-derived rollout title", suffix: "0077"},
		{name: "claude transcript", provider: aiModeClaude, source: aisessions.SourceClaudeTranscript, id: "019f0000-0000-7000-8000-000000000066", title: "prompt-derived transcript title", suffix: "0066"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := aisessions.SessionMeta{
				Agent: test.provider, ResumeID: test.id, Source: test.source, Title: test.title,
				Context: aisessions.SessionContext{Branch: "main"},
			}
			row := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{}, time.Time{}, i18n.FallbackLocale, "", 0)
			visible := stripANSI(row.Label)
			if !strings.Contains(visible, "Untitled · …"+test.suffix) || strings.Contains(visible, session.Title) || strings.Contains(visible, aiResumeShortID(test.id)) {
				t.Fatalf("%s row = %q, want localized untitled suffix and no inferred/id-prefix title", test.name, visible)
			}
			if strings.Contains(row.SearchKey, session.Title) || !strings.Contains(row.SearchKey, test.source) {
				t.Fatalf("SearchKey = %q, rejected prompt title must be absent while routing source remains", row.SearchKey)
			}
		})
	}
}

func TestAIResumeConversationHierarchyWidthAndLocaleGolden(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := aisessions.SessionMeta{
		Agent: aiModeCodex, ResumeID: "019f0000-0000-7000-8000-000000000088",
		Title:        "Conversation-first picker hierarchy with a deliberately long conversation name",
		LastModified: now.Add(-2 * time.Hour), Source: aisessions.SourceCodexAppServer,
		Confidence: aisessions.ConfidenceHigh, RuntimeStatus: "notLoaded", Turns: 31,
		Context: aisessions.SessionContext{Branch: "feature/conversation-first-long", CWD: "/workspace/projmux/internal/app"},
	}
	var got strings.Builder
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		row := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{Topic: "Registry topic must lose"}, now, locale, "/workspace/projmux", 1)
		plain := stripANSI(row.Label)
		fmt.Fprintf(&got, "%s\n", locale)
		for _, width := range []int{80, 100, 120} {
			visible := strings.TrimRight(i18n.TruncateTerminalCells(plain, width), " ")
			fmt.Fprintf(&got, "%d|%s\n", width, visible)
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "ai-resume-conversation-hierarchy.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got.String() != string(want) {
		t.Fatalf("conversation hierarchy golden mismatch:\ngot:\n%swant:\n%s", got.String(), want)
	}
}

func TestAIResumeClaudeAndAntigravityRowsUseCommonProjection(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{aiModeClaude, aiModeAntigravity} {
		session := aisessions.SessionMeta{
			Agent: provider, ResumeID: provider + "-session", Title: provider + " title",
			LastModified: now.Add(-time.Hour), Context: aisessions.SessionContext{Branch: "main"},
		}
		got := aiResumeSessionRowWithResolvedLabel(session, aiResumeExactAgentLabel{Topic: "must be ignored"}, now, i18n.FallbackLocale, "/work", 0)
		visible := stripANSI(got.Label)
		if !strings.HasPrefix(visible, "1h") || !strings.Contains(visible, "["+provider[:min(len(provider), aiResumeAgentCellWidth)]+"]") ||
			!strings.Contains(visible, "main") || !strings.HasSuffix(visible, provider+" title") {
			t.Fatalf("%s common row grammar drifted: %q", provider, visible)
		}
	}
}
