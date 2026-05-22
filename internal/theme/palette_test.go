package theme

import "testing"

func TestFallbackPalettePreservesPhaseBaselineTokens(t *testing.T) {
	checks := map[string]string{
		"surface active truecolor":  ANSISurfaceActiveStart,
		"text muted truecolor":      ANSITextMutedStart,
		"action truecolor":          ANSIAccentActionStart,
		"attention tmux bg":         TmuxAccentAttentionBg,
		"ai tmux bg":                TmuxAccentAIBg,
		"progress truecolor":        ANSIStateProgressStart,
		"progress tmux fg":          TmuxStateProgressFg,
		"warning tmux fg":           TmuxStateWarningFg,
		"ai badge action tmux fg":   TmuxAIBadgeActionRequiredFg,
		"ai badge success tmux fg":  TmuxAIBadgeSuccessFg,
		"ai badge progress tmux fg": TmuxAIBadgeProgressFg,
		"critical tmux fg":          TmuxStateCriticalFg,
		"window active tmux bg":     TmuxWindowActiveBg,
		"git staged tmux fg":        TmuxStateStagedFg,
	}
	for name, value := range checks {
		if value == "" {
			t.Fatalf("%s token is empty", name)
		}
	}
	if TmuxAccentAttentionStrongBg == TmuxAccentAIBg || TmuxAccentAttentionStrongBg == TmuxActionBg {
		t.Fatalf("attention, ai, and action tokens must remain visually distinct")
	}
	if ANSIStateDangerStart == ANSIAccentActionStart || ANSIStateDangerStart == ANSITextDimStart {
		t.Fatalf("danger truecolor token must not alias action or dim text")
	}
	if TmuxStateProgressFg == TmuxAccentAIFg || TmuxStateProgressFg == TmuxStateCriticalFg {
		t.Fatalf("progress tmux token must not alias AI or danger tokens")
	}
	if TmuxAIBadgeActionRequiredFg == TmuxStateCriticalFg {
		t.Fatalf("AI prompt-required status badge must not use critical red")
	}
	if TmuxAIBadgeActionRequiredFg == TmuxAIBadgeProgressFg || TmuxAIBadgeActionRequiredFg == TmuxAIBadgeSuccessFg || TmuxAIBadgeProgressFg == TmuxAIBadgeSuccessFg {
		t.Fatalf("AI status badge progress, success, and action-required roles must stay distinct")
	}
	if got, want := ANSIAIBadgeActionRequiredStart, "\x1b[38;5;214m"; got != want {
		t.Fatalf("ANSIAIBadgeActionRequiredStart = %q, want amber-orange %q", got, want)
	}
}

func TestANSI256FgStart(t *testing.T) {
	if got, want := ANSI256FgStart(TmuxStateWarningFg), "\x1b[38;5;214m"; got != want {
		t.Fatalf("ANSI256FgStart(warning) = %q, want %q", got, want)
	}
	if got, want := ANSI256FgStart(TmuxUsageCriticalBoldFg), "\x1b[38;5;160m"; got != want {
		t.Fatalf("ANSI256FgStart(critical,bold) = %q, want %q", got, want)
	}
}
