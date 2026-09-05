package app

import (
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
)

// Phase 2 criterion ① distinguishes an unfinished empty scan from a genuine
// error without a timer, sleep or scheduler ordering. The handoff/late-result
// producer is separately exercised with the virtual clock by
// TestClaudeHandoffExpiryKeepsLateResultOutsideSettlement.
func TestResumeProviderUnfinishedAndFailureHaveDistinctLocalizedTerminals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result aiResumeProviderResult
		want   aiResumeProviderState
		en, ko string
	}{
		{"expired without rows", aiResumeProviderResult{provider: aiModeClaude, envelopeExpired: true}, aiResumeProviderScanUnfinished, "scan unfinished", "검색 미완료"},
		{"real provider error", aiResumeProviderResult{provider: aiModeClaude, err: errors.New("provider failed")}, aiResumeProviderSearchFailed, "search failed", "검색 실패"},
		{"error still wins over expiry marker", aiResumeProviderResult{provider: aiModeClaude, err: errors.New("provider failed"), envelopeExpired: true}, aiResumeProviderSearchFailed, "search failed", "검색 실패"},
		{"completed empty scan", aiResumeProviderResult{provider: aiModeClaude}, aiResumeProviderCount, "0 found", "0건 발견"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resumeProviderProjection(tc.result, true)
			if got.state != tc.want || got.count != 0 {
				t.Fatalf("projection=%+v want %s/0", got, tc.want)
			}
			for locale, want := range map[i18n.Locale]string{i18n.FallbackLocale: tc.en, i18n.Locale("ko-KR"): tc.ko} {
				if text := resumeProviderStateText(locale, got); text != want {
					t.Fatalf("locale %s text=%q want %q", locale, text, want)
				}
				footer := resumeProviderFooterLine(map[string]aiResumeProviderProjection{aiModeClaude: got}, locale)
				if text := resumeSummaryProviderStatus(footer, aiModeClaude); text != want {
					t.Fatalf("locale %s footer state=%q want %q", locale, text, want)
				}
			}
		})
	}
}
