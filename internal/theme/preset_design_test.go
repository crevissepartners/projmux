package theme

import "testing"

// TestPresetTokensSatisfyDesignRubric enforces the Phase 1 design-quality
// rubric hard rules on every built-in preset (and so on any preset added
// later). WCAG contrast is a design guideline tracked separately; only the
// three hard rules below are machine-enforced because they catch the failures
// that make chrome unreadable or semantically ambiguous:
//
//	(a) the five state colors (progress/warning/critical/success/
//	    action_required) resolve to mutually distinct tmux colourN — you must
//	    be able to tell the states apart by color.
//	(b) muted resolves distinct from both foreground and background — low-
//	    signal text must still read.
//	(c) foreground != background.
//
// projmux-dark is the built-in fallback baseline: its values are frozen to
// preserve unset-theme byte-identity across the renderer goldens, and it
// historically maps warning and progress to the same amber (they appear on
// different surfaces). It is therefore exempt from rule (a) ONLY. Rules (b)/(c)
// still apply to it.
func TestPresetTokensSatisfyDesignRubric(t *testing.T) {
	stateTokens := []ColorToken{TokenProgress, TokenWarning, TokenCritical, TokenSuccess, TokenActionRequired}
	exemptStateDistinct := map[string]bool{"projmux-dark": true}

	colourOf := func(t *testing.T, preset string, tok ColorToken) (string, bool) {
		t.Helper()
		hex, ok := PresetColorHex(preset, tok)
		if !ok {
			t.Fatalf("%s: token %s unset; every preset must define all tokens", preset, tok)
		}
		// A terminal-default-sentinel token (background/surface family) carries no
		// fixed hex: it rides the terminal background, so there is no colourN to
		// compare. Report it as a sentinel so the caller skips bg-relative rules.
		if hex == "" {
			return "", true
		}
		return nearestTmuxColor(hex), false
	}

	for _, preset := range PresetNames() {

		// (a) state colors mutually distinct. State tokens never use the
		// terminal-default sentinel (it is restricted to the background/surface
		// family), so all five always resolve to a concrete colourN.
		if !exemptStateDistinct[preset] {
			seen := map[string]ColorToken{}
			for _, tok := range stateTokens {
				cn, _ := colourOf(t, preset, tok)
				if prev, dup := seen[cn]; dup {
					t.Errorf("%s: state colors %s and %s both resolve to %s; the five state colors must be distinguishable", preset, prev, tok, cn)
				}
				seen[cn] = tok
			}
		}

		muted, _ := colourOf(t, preset, TokenMuted)
		fg, fgSentinel := colourOf(t, preset, TokenForeground)
		bg, bgSentinel := colourOf(t, preset, TokenBackground)

		// (b) muted distinct from foreground and (when fixed) background.
		if muted == fg {
			t.Errorf("%s: muted (%s) equals foreground; low-signal text would be indistinguishable", preset, muted)
		}
		if !bgSentinel && muted == bg {
			t.Errorf("%s: muted (%s) equals background; muted text would be invisible", preset, muted)
		}

		// (c) foreground != background. Vacuous when background rides the terminal
		// default (no fixed background to clash with).
		if !bgSentinel && !fgSentinel && fg == bg {
			t.Errorf("%s: foreground equals background (%s)", preset, fg)
		}
	}
}
