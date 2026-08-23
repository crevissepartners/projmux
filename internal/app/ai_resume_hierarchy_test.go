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
		{name: "short id", title: aiResumeShortID(id), want: aiResumeShortID(id)},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := base
			session.Title = test.title
			if got := aiResumeCodexConversationLabel(session, test.boundLabel); got != test.want {
				t.Fatalf("conversation label = %q, want %q", got, test.want)
			}
			row := aiResumeSessionRowWithLabel(session, test.boundLabel, time.Time{}, i18n.FallbackLocale, "", 0)
			visible := stripANSI(row.Label)
			if !strings.Contains(visible, test.want) {
				t.Fatalf("visible row = %q, want label %q", visible, test.want)
			}
			if test.name == "short id" && strings.Count(visible, test.want) != 1 {
				t.Fatalf("visible row = %q, short id must occur exactly once", visible)
			}
		})
	}
}

func TestAIResumeExactAgentLabelResolverUsesOnlyExactThreadBinding(t *testing.T) {
	const exactID = "thread-exact"
	registry := coremetadata.Registry{Agents: []coremetadata.Agent{
		{
			Spec: coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-z-topic", Name: "agent-name", Annotations: map[string]string{
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
	}}

	labels := aiResumeExactAgentLabels(registry)
	if len(labels) != 2 || labels[exactID] != "Bound topic" || labels["thread-other"] != "wrong-agent" {
		t.Fatalf("resolved labels = %#v, want exact Codex bindings with topic/name precedence", labels)
	}
}

func TestAIResumeCodexStatusQualifierSearchAndSelectionParity(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000099"
	for _, test := range []struct {
		name         string
		source       string
		confidence   string
		reason       string
		status       string
		locale       i18n.Locale
		wantStatus   string
		wantFallback bool
	}{
		{name: "native active en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "active", locale: i18n.FallbackLocale, wantStatus: "[active]"},
		{name: "native idle en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "idle", locale: i18n.FallbackLocale, wantStatus: "[idle]"},
		{name: "native idle ko", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "idle", locale: i18n.Locale("ko-KR"), wantStatus: "[대기]"},
		{name: "native not loaded en", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "notLoaded", locale: i18n.FallbackLocale, wantStatus: "[not loaded]"},
		{name: "native not loaded ko", source: aisessions.SourceCodexAppServer, confidence: aisessions.ConfidenceHigh, status: "notLoaded", locale: i18n.Locale("ko-KR"), wantStatus: "[미로드]"},
		{name: "rollout fallback", source: aisessions.SourceCodexRollout, confidence: aisessions.ConfidenceMedium, reason: aisessions.ReasonAppServerUnavailable, locale: i18n.FallbackLocale, wantFallback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := aisessions.SessionMeta{
				Agent: aiModeCodex, ResumeID: id, Title: "Conversation", Source: test.source,
				Confidence: test.confidence, Reason: test.reason, RuntimeStatus: test.status,
				Context: aisessions.SessionContext{Branch: "main"},
			}
			row := aiResumeSessionRowWithLabel(session, "Bound topic", time.Time{}, test.locale, "/work", 0)
			visible := stripANSI(row.Label)
			if test.wantStatus != "" && !strings.Contains(visible, test.wantStatus) {
				t.Fatalf("visible row = %q, want status %q", visible, test.wantStatus)
			}
			if got := strings.Contains(visible, "[fallback]"); got != test.wantFallback {
				t.Fatalf("visible row = %q, fallback=%v want %v", visible, got, test.wantFallback)
			}
			for _, hidden := range []string{aisessions.SourceCodexAppServer, test.confidence, test.reason} {
				if hidden != "" && strings.Contains(visible, hidden) {
					t.Fatalf("visible row leaks raw provenance %q: %q", hidden, visible)
				}
			}
			for _, searchable := range []string{id, test.source, test.confidence, test.reason, test.status} {
				if searchable != "" && !strings.Contains(row.SearchKey, searchable) {
					t.Fatalf("SearchKey = %q, want %q", row.SearchKey, searchable)
				}
			}
			if got := row.Value; got != aiResumePickerValue(aiModeCodex, id) {
				t.Fatalf("selection value = %q, want exact resume value %q", got, aiResumePickerValue(aiModeCodex, id))
			}
		})
	}
}

func TestAIResumeRolloutProjectionDoesNotInferConversationTitle(t *testing.T) {
	const id = "019f0000-0000-7000-8000-000000000077"
	session := aisessions.SessionMeta{
		Agent: aiModeCodex, ResumeID: id, Source: aisessions.SourceCodexRollout,
		Title:   "prompt-derived rollout title must remain search-only",
		Context: aisessions.SessionContext{Branch: "main"},
	}
	row := aiResumeSessionRowWithLabel(session, "", time.Time{}, i18n.FallbackLocale, "", 0)
	visible := stripANSI(row.Label)
	if !strings.Contains(visible, aiResumeShortID(id)) || strings.Contains(visible, session.Title) {
		t.Fatalf("rollout row = %q, want one short id and no inferred title", visible)
	}
	if !strings.Contains(row.SearchKey, session.Title) {
		t.Fatalf("SearchKey = %q, existing catalog metadata must remain searchable", row.SearchKey)
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
		row := aiResumeSessionRowWithLabel(session, "Registry topic must lose", now, locale, "/workspace/projmux", 1)
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

func TestAIResumeClaudeAndAntigravityRowsKeepLegacyProjection(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{aiModeClaude, aiModeAntigravity} {
		session := aisessions.SessionMeta{
			Agent: provider, ResumeID: provider + "-session", Title: provider + " title",
			LastModified: now.Add(-time.Hour), Context: aisessions.SessionContext{Branch: "main"},
		}
		got := aiResumeSessionRowWithLabel(session, "must be ignored", now, i18n.FallbackLocale, "/work", 0)
		want := aiResumeLegacySessionRow(session, now, i18n.FallbackLocale, "/work", 0)
		if got != want {
			t.Fatalf("%s row drifted: got=%#v want=%#v", provider, got, want)
		}
	}
}
