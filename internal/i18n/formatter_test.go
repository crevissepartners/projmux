package i18n

import (
	"strings"
	"testing"
	"time"
)

func TestFormatRelativeAgeCompactByLocale(t *testing.T) {
	tests := []struct {
		name   string
		age    time.Duration
		locale Locale
		want   string
	}{
		{name: "korean seconds", age: 36 * time.Second, locale: Locale("ko-KR"), want: "36초 전"},
		{name: "english minutes", age: 3 * time.Minute, locale: FallbackLocale, want: "3m ago"},
		{name: "english just now", age: 4 * time.Second, locale: FallbackLocale, want: "just now"},
		{name: "korean just now", age: 4 * time.Second, locale: Locale("ko-KR"), want: "방금 전"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatRelativeAge(tt.age, tt.locale, FormatCompact); got != tt.want {
				t.Fatalf("FormatRelativeAge() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDurationFullAndCompact(t *testing.T) {
	tests := []struct {
		name    string
		value   time.Duration
		locale  Locale
		variant FormatVariant
		want    string
	}{
		{name: "english compact hour", value: 2 * time.Hour, locale: FallbackLocale, variant: FormatCompact, want: "2h"},
		{name: "english full singular", value: time.Minute, locale: FallbackLocale, variant: FormatFull, want: "1 minute"},
		{name: "english full plural", value: 2 * time.Minute, locale: FallbackLocale, variant: FormatFull, want: "2 minutes"},
		{name: "korean compact", value: 90 * time.Minute, locale: Locale("ko-KR"), variant: FormatCompact, want: "1시간"},
		{name: "negative clamps", value: -time.Second, locale: FallbackLocale, variant: FormatCompact, want: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDuration(tt.value, tt.locale, tt.variant); got != tt.want {
				t.Fatalf("FormatDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCountPreservesCountAndPluralizes(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		locale Locale
		want   string
	}{
		{name: "english singular", count: 1, locale: FallbackLocale, want: "1 notification"},
		{name: "english plural", count: 2, locale: FallbackLocale, want: "2 notifications"},
		{name: "korean singular data", count: 1, locale: Locale("ko-KR"), want: "알림 1개"},
		{name: "korean plural data", count: 2, locale: Locale("ko-KR"), want: "알림 2개"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCount(tt.count, CountNotifications, tt.locale, FormatFull); got != tt.want {
				t.Fatalf("FormatCount() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTargetLabelLocalizesKindOnly(t *testing.T) {
	tests := []struct {
		name    string
		kind    TargetKind
		number  int
		locale  Locale
		variant FormatVariant
		want    string
	}{
		{name: "english window", kind: TargetWindow, number: 2, locale: FallbackLocale, variant: FormatFull, want: "window 2"},
		{name: "english compact window", kind: TargetWindow, number: 2, locale: FallbackLocale, variant: FormatCompact, want: "win 2"},
		{name: "english pane", kind: TargetPane, number: 4, locale: FallbackLocale, variant: FormatFull, want: "pane 4"},
		{name: "korean window", kind: TargetWindow, number: 2, locale: Locale("ko-KR"), variant: FormatFull, want: "창 2"},
		{name: "korean pane", kind: TargetPane, number: 4, locale: Locale("ko-KR"), variant: FormatFull, want: "페인 4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatTargetLabel(tt.kind, tt.number, tt.locale, tt.variant); got != tt.want {
				t.Fatalf("FormatTargetLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatListPreservesPayloadItems(t *testing.T) {
	items := []string{"Codex", "/tmp/work tree", "tmux split-window"}

	tests := []struct {
		name    string
		locale  Locale
		variant FormatVariant
		want    string
	}{
		{name: "english full", locale: FallbackLocale, variant: FormatFull, want: "Codex, /tmp/work tree, and tmux split-window"},
		{name: "english compact", locale: FallbackLocale, variant: FormatCompact, want: "Codex, /tmp/work tree, tmux split-window"},
		{name: "korean full", locale: Locale("ko-KR"), variant: FormatFull, want: "Codex, /tmp/work tree 및 tmux split-window"},
		{name: "korean compact", locale: Locale("ko-KR"), variant: FormatCompact, want: "Codex, /tmp/work tree, tmux split-window"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatList(items, tt.locale, tt.variant); got != tt.want {
				t.Fatalf("FormatList() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatStatusTokenVariants(t *testing.T) {
	tests := []struct {
		name    string
		token   StatusToken
		locale  Locale
		variant FormatVariant
		want    string
	}{
		{name: "english full", token: StatusTokenError, locale: FallbackLocale, variant: FormatFull, want: "error"},
		{name: "english compact", token: StatusTokenError, locale: FallbackLocale, variant: FormatCompact, want: "err"},
		{name: "english stale full is inactive", token: StatusTokenStale, locale: FallbackLocale, variant: FormatFull, want: "inactive"},
		{name: "english stale compact is INA", token: StatusTokenStale, locale: FallbackLocale, variant: FormatCompact, want: "ina"},
		{name: "korean full", token: StatusTokenBusy, locale: Locale("ko-KR"), variant: FormatFull, want: "작업 중"},
		{name: "korean compact", token: StatusTokenBusy, locale: Locale("ko-KR"), variant: FormatCompact, want: "작업"},
		{name: "korean stale full is inactive", token: StatusTokenStale, locale: Locale("ko-KR"), variant: FormatFull, want: "비활성"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatStatusToken(tt.token, tt.locale, tt.variant); got != tt.want {
				t.Fatalf("FormatStatusToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTerminalCellWidthIgnoresANSIAndTmuxStyles(t *testing.T) {
	value := "\x1b[31m응답\x1b[0m #[fg=red]OK#[default]"
	if got, want := TerminalCellWidth(value), 7; got != want {
		t.Fatalf("TerminalCellWidth() = %d, want %d", got, want)
	}
}

func TestTerminalCellWidthTreatsUnicodeFormatCharactersAsZeroWidth(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "variation selector", value: "✳\ufe0f", want: 1},
		{name: "zero width joiner sequence", value: "👩\u200d💻", want: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TerminalCellWidth(tt.value); got != tt.want {
				t.Fatalf("TerminalCellWidth(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestTruncateTerminalCellsClipsByCellsAndPreservesStyles(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "korean cells", value: "응답 ready", width: 5, want: "응답 "},
		{name: "localized formatter output", value: FormatCount(12, CountNotifications, Locale("ko-KR"), FormatFull), width: 5, want: "알림 "},
		{name: "ansi wrappers", value: "\x1b[31m응답\x1b[0m ready", width: 4, want: "\x1b[31m응답\x1b[0m"},
		{name: "tmux wrappers", value: "#[fg=red]응답#[default] ready", width: 4, want: "#[fg=red]응답#[default]"},
		{name: "zero width keeps wrappers", value: "\x1b[31m응답\x1b[0m", width: 0, want: "\x1b[31m\x1b[0m"},
		{name: "wide leading rune does not skip ahead", value: "응a", width: 1, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateTerminalCells(tt.value, tt.width); got != tt.want {
				t.Fatalf("TruncateTerminalCells() = %q, want %q", got, tt.want)
			}
			if got := TerminalCellWidth(tt.want); got > tt.width {
				t.Fatalf("truncated width = %d, max %d", got, tt.width)
			}
		})
	}
}

func TestTruncateTerminalCellsKeepsLongKoreanGuideStyleSafe(t *testing.T) {
	value := "\x1b[90m긴 한국어 안내 문장은 좁은 팝업 footer에서도 안전하게 잘려야 합니다\x1b[0m #[fg=green]Enter#[default]"
	got := TruncateTerminalCells(value, 24)
	if width := TerminalCellWidth(got); width > 24 {
		t.Fatalf("truncated width = %d, want <= 24: %q", width, got)
	}
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("truncated guide = %q, want ANSI reset preserved", got)
	}
	if !strings.Contains(TruncateTerminalCells(value, 0), "#[fg=green]#[default]") {
		t.Fatalf("zero-width truncation should keep tmux wrappers: %q", TruncateTerminalCells(value, 0))
	}
}
