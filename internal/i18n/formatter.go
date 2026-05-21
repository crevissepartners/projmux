package i18n

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// FormatVariant selects between prose-friendly and dense terminal forms.
type FormatVariant string

const (
	FormatFull    FormatVariant = "full"
	FormatCompact FormatVariant = "compact"
)

const relativeJustNowWindow = 5 * time.Second

// CountSubject identifies count labels whose plural rules are owned by i18n.
type CountSubject string

const (
	CountNotifications CountSubject = "notifications"
)

// TargetKind identifies tmux target labels while keeping target data separate.
type TargetKind string

const (
	TargetSession TargetKind = "session"
	TargetWindow  TargetKind = "window"
	TargetPane    TargetKind = "pane"
)

// StatusToken identifies short status labels shared by terminal surfaces.
type StatusToken string

const (
	StatusTokenActive  StatusToken = "active"
	StatusTokenBusy    StatusToken = "busy"
	StatusTokenDone    StatusToken = "done"
	StatusTokenError   StatusToken = "error"
	StatusTokenStale   StatusToken = "stale"
	StatusTokenGone    StatusToken = "gone"
	StatusTokenUnknown StatusToken = "unknown"
)

type formatterLocale int

const (
	formatterLocaleEN formatterLocale = iota
	formatterLocaleKO
)

type durationUnit int

const (
	durationSeconds durationUnit = iota
	durationMinutes
	durationHours
	durationDays
)

// FormatRelativeAge renders an elapsed age such as "3m ago" or "36초 전".
func FormatRelativeAge(age time.Duration, locale Locale, variant FormatVariant) string {
	if age < relativeJustNowWindow {
		switch formatterLocaleFor(locale) {
		case formatterLocaleKO:
			return "방금 전"
		default:
			return "just now"
		}
	}

	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		return FormatDuration(age, locale, variant) + " 전"
	default:
		return FormatDuration(age, locale, variant) + " ago"
	}
}

// FormatDuration renders a non-negative duration using the largest whole unit.
func FormatDuration(duration time.Duration, locale Locale, variant FormatVariant) string {
	if duration < 0 {
		duration = 0
	}
	value, unit := durationParts(duration)
	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		return strconv.FormatInt(value, 10) + koreanDurationUnit(unit)
	default:
		return strconv.FormatInt(value, 10) + englishDurationUnit(unit, value, variant)
	}
}

// FormatCount renders a localized count label while preserving the count value.
func FormatCount(count int, subject CountSubject, locale Locale, variant FormatVariant) string {
	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		return koreanCount(count, subject)
	default:
		return englishCount(count, subject, variant)
	}
}

// FormatList joins caller-owned payload items without translating them.
func FormatList(items []string, locale Locale, variant FormatVariant) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}

	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		if variant == FormatCompact {
			return strings.Join(items, ", ")
		}
		return joinFinal(items, " 및 ")
	default:
		if variant == FormatCompact {
			return strings.Join(items, ", ")
		}
		if len(items) == 2 {
			return items[0] + " and " + items[1]
		}
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// FormatStatusToken renders a localized terminal status token.
func FormatStatusToken(token StatusToken, locale Locale, variant FormatVariant) string {
	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		return koreanStatusToken(token, variant)
	default:
		return englishStatusToken(token, variant)
	}
}

// FormatTargetLabel renders the localized target kind and preserves the target number.
func FormatTargetLabel(kind TargetKind, number int, locale Locale, variant FormatVariant) string {
	switch formatterLocaleFor(locale) {
	case formatterLocaleKO:
		return koreanTargetKind(kind, variant) + " " + strconv.Itoa(number)
	default:
		return englishTargetKind(kind, variant) + " " + strconv.Itoa(number)
	}
}

// TerminalCellWidth measures visible terminal cells while ignoring ANSI escape
// sequences and tmux style wrappers such as "#[fg=red]".
func TerminalCellWidth(value string) int {
	width := 0
	for i := 0; i < len(value); {
		if n := ansiEscapeLen(value[i:]); n > 0 {
			i += n
			continue
		}
		if n := tmuxStyleLen(value[i:]); n > 0 {
			i += n
			continue
		}
		r, size := runeAt(value[i:])
		width += runeCellWidth(r)
		i += size
	}
	return width
}

// TruncateTerminalCells clips visible content to maxCells while preserving
// zero-width ANSI and tmux style wrappers that appear in the input.
func TruncateTerminalCells(value string, maxCells int) string {
	if maxCells <= 0 {
		return zeroWidthFragments(value)
	}

	var b strings.Builder
	width := 0
	clipped := false
	for i := 0; i < len(value); {
		if n := ansiEscapeLen(value[i:]); n > 0 {
			b.WriteString(value[i : i+n])
			i += n
			continue
		}
		if n := tmuxStyleLen(value[i:]); n > 0 {
			b.WriteString(value[i : i+n])
			i += n
			continue
		}

		r, size := runeAt(value[i:])
		cellWidth := runeCellWidth(r)
		if !clipped && width+cellWidth <= maxCells {
			b.WriteString(value[i : i+size])
			width += cellWidth
		} else if cellWidth > 0 {
			clipped = true
		}
		i += size
	}
	return b.String()
}

func formatterLocaleFor(locale Locale) formatterLocale {
	normalized, ok := NormalizeLocale(string(locale))
	if !ok {
		normalized = FallbackLocale
	}
	if strings.HasPrefix(string(normalized), "ko") {
		return formatterLocaleKO
	}
	return formatterLocaleEN
}

func durationParts(duration time.Duration) (int64, durationUnit) {
	seconds := int64(duration / time.Second)
	switch {
	case seconds < 60:
		return seconds, durationSeconds
	case seconds < 60*60:
		return seconds / 60, durationMinutes
	case seconds < 24*60*60:
		return seconds / (60 * 60), durationHours
	default:
		return seconds / (24 * 60 * 60), durationDays
	}
}

func englishDurationUnit(unit durationUnit, value int64, variant FormatVariant) string {
	if variant == FormatCompact {
		switch unit {
		case durationSeconds:
			return "s"
		case durationMinutes:
			return "m"
		case durationHours:
			return "h"
		default:
			return "d"
		}
	}

	switch unit {
	case durationSeconds:
		return pluralSuffix(value, " second", " seconds")
	case durationMinutes:
		return pluralSuffix(value, " minute", " minutes")
	case durationHours:
		return pluralSuffix(value, " hour", " hours")
	default:
		return pluralSuffix(value, " day", " days")
	}
}

func koreanDurationUnit(unit durationUnit) string {
	switch unit {
	case durationSeconds:
		return "초"
	case durationMinutes:
		return "분"
	case durationHours:
		return "시간"
	default:
		return "일"
	}
}

func englishCount(count int, subject CountSubject, variant FormatVariant) string {
	countText := strconv.Itoa(count)
	switch subject {
	case CountNotifications:
		if count == 1 {
			return countText + " notification"
		}
		return countText + " notifications"
	default:
		return countText + " " + string(subject)
	}
}

func koreanCount(count int, subject CountSubject) string {
	countText := strconv.Itoa(count)
	switch subject {
	case CountNotifications:
		return "알림 " + countText + "개"
	default:
		return string(subject) + " " + countText
	}
}

func englishStatusToken(token StatusToken, variant FormatVariant) string {
	if variant == FormatCompact {
		switch token {
		case StatusTokenActive:
			return "act"
		case StatusTokenBusy:
			return "busy"
		case StatusTokenDone:
			return "done"
		case StatusTokenError:
			return "err"
		case StatusTokenStale:
			return "stale"
		case StatusTokenGone:
			return "gone"
		case StatusTokenUnknown:
			return "unk"
		default:
			return string(token)
		}
	}

	switch token {
	case StatusTokenActive:
		return "active"
	case StatusTokenBusy:
		return "busy"
	case StatusTokenDone:
		return "done"
	case StatusTokenError:
		return "error"
	case StatusTokenStale:
		return "stale"
	case StatusTokenGone:
		return "gone"
	case StatusTokenUnknown:
		return "unknown"
	default:
		return string(token)
	}
}

func koreanStatusToken(token StatusToken, variant FormatVariant) string {
	if variant == FormatCompact {
		switch token {
		case StatusTokenActive:
			return "활성"
		case StatusTokenBusy:
			return "작업"
		case StatusTokenDone:
			return "완료"
		case StatusTokenError:
			return "오류"
		case StatusTokenStale:
			return "오래됨"
		case StatusTokenGone:
			return "없음"
		case StatusTokenUnknown:
			return "알수없음"
		default:
			return string(token)
		}
	}

	switch token {
	case StatusTokenActive:
		return "활성"
	case StatusTokenBusy:
		return "작업 중"
	case StatusTokenDone:
		return "완료"
	case StatusTokenError:
		return "오류"
	case StatusTokenStale:
		return "오래됨"
	case StatusTokenGone:
		return "없음"
	case StatusTokenUnknown:
		return "알 수 없음"
	default:
		return string(token)
	}
}

func englishTargetKind(kind TargetKind, variant FormatVariant) string {
	if variant == FormatCompact {
		switch kind {
		case TargetSession:
			return "session"
		case TargetWindow:
			return "win"
		case TargetPane:
			return "pane"
		default:
			return string(kind)
		}
	}

	switch kind {
	case TargetSession:
		return "session"
	case TargetWindow:
		return "window"
	case TargetPane:
		return "pane"
	default:
		return string(kind)
	}
}

func koreanTargetKind(kind TargetKind, variant FormatVariant) string {
	if variant == FormatCompact {
		switch kind {
		case TargetSession:
			return "세션"
		case TargetWindow:
			return "창"
		case TargetPane:
			return "페인"
		default:
			return string(kind)
		}
	}

	switch kind {
	case TargetSession:
		return "세션"
	case TargetWindow:
		return "창"
	case TargetPane:
		return "페인"
	default:
		return string(kind)
	}
}

func joinFinal(items []string, finalSeparator string) string {
	if len(items) == 2 {
		return items[0] + finalSeparator + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + finalSeparator + items[len(items)-1]
}

func pluralSuffix(value int64, singular, plural string) string {
	if value == 1 {
		return singular
	}
	return plural
}

func zeroWidthFragments(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		if n := ansiEscapeLen(value[i:]); n > 0 {
			b.WriteString(value[i : i+n])
			i += n
			continue
		}
		if n := tmuxStyleLen(value[i:]); n > 0 {
			b.WriteString(value[i : i+n])
			i += n
			continue
		}
		_, size := runeAt(value[i:])
		i += size
	}
	return b.String()
}

func ansiEscapeLen(value string) int {
	if len(value) < 2 || value[0] != 0x1b {
		return 0
	}
	switch value[1] {
	case '[':
		for i := 2; i < len(value); i++ {
			if value[i] >= 0x40 && value[i] <= 0x7e {
				return i + 1
			}
		}
		return len(value)
	case ']':
		for i := 2; i < len(value); i++ {
			if value[i] == 0x07 {
				return i + 1
			}
			if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
				return i + 2
			}
		}
		return len(value)
	default:
		return 2
	}
}

func tmuxStyleLen(value string) int {
	if !strings.HasPrefix(value, "#[") {
		return 0
	}
	if end := strings.IndexByte(value, ']'); end >= 0 {
		return end + 1
	}
	return len(value)
}

func runeAt(value string) (rune, int) {
	r, size := utf8.DecodeRuneInString(value)
	return r, size
}

func runeCellWidth(r rune) int {
	switch {
	case r == '\t':
		return 4
	case r == 0 || r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r):
		return 0
	case isWideRune(r):
		return 2
	default:
		return 1
	}
}

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) ||
		(r >= 0x231a && r <= 0x231b) ||
		(r >= 0x2329 && r <= 0x232a) ||
		(r >= 0x23e9 && r <= 0x23ec) ||
		(r >= 0x23f0 && r <= 0x23f0) ||
		(r >= 0x23f3 && r <= 0x23f3) ||
		(r >= 0x25fd && r <= 0x25fe) ||
		(r >= 0x2614 && r <= 0x2615) ||
		(r >= 0x2648 && r <= 0x2653) ||
		(r >= 0x267f && r <= 0x267f) ||
		(r >= 0x2693 && r <= 0x2693) ||
		(r >= 0x26a1 && r <= 0x26a1) ||
		(r >= 0x26aa && r <= 0x26ab) ||
		(r >= 0x26bd && r <= 0x26be) ||
		(r >= 0x26c4 && r <= 0x26c5) ||
		(r >= 0x26ce && r <= 0x26ce) ||
		(r >= 0x26d4 && r <= 0x26d4) ||
		(r >= 0x26ea && r <= 0x26ea) ||
		(r >= 0x26f2 && r <= 0x26f3) ||
		(r >= 0x26f5 && r <= 0x26f5) ||
		(r >= 0x26fa && r <= 0x26fa) ||
		(r >= 0x26fd && r <= 0x26fd) ||
		(r >= 0x2705 && r <= 0x2705) ||
		(r >= 0x270a && r <= 0x270b) ||
		(r >= 0x2728 && r <= 0x2728) ||
		(r >= 0x274c && r <= 0x274c) ||
		(r >= 0x274e && r <= 0x274e) ||
		(r >= 0x2753 && r <= 0x2755) ||
		(r >= 0x2757 && r <= 0x2757) ||
		(r >= 0x2795 && r <= 0x2797) ||
		(r >= 0x27b0 && r <= 0x27b0) ||
		(r >= 0x27bf && r <= 0x27bf) ||
		(r >= 0x2b1b && r <= 0x2b1c) ||
		(r >= 0x2b50 && r <= 0x2b50) ||
		(r >= 0x2b55 && r <= 0x2b55) ||
		(r >= 0x2e80 && r <= 0xa4cf) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x20000 && r <= 0x3fffd)
}
