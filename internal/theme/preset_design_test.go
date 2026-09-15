package theme

import (
	"math"
	"testing"
)

// TestPresetTokensSatisfyDesignRubric enforces the Phase 1 design-quality
// rubric hard rules on every built-in preset (and so on any preset added
// later). WCAG contrast is a design guideline tracked separately; only the
// four hard rules below are machine-enforced because they catch the failures
// that make chrome unreadable or semantically ambiguous:
//
//	(a) the five state colors (progress/warning/critical/success/
//	    action_required) resolve to mutually distinct tmux colourN — you must
//	    be able to tell the states apart by color.
//	(b) muted resolves distinct from both foreground and background — low-
//	    signal text must still read.
//	(c) foreground != background.
//	(d) provenance — the compact usage label color for a row served by a
//	    fallback data source — is an orange (hue 15-45°) whose nearest colourN
//	    differs from all five state colors and from the AI label color, and
//	    whose resolved role stays readable on that preset's status background.
//	    It is the whole signal for a fallback row, so it may not read as a
//	    usage threshold (warning/critical) or as a healthy native label.
//
// projmux is the built-in fallback baseline: its non-background values preserve
// the established renderer palette, and it
// historically maps warning and progress to the same amber (they appear on
// different surfaces). It is therefore exempt from rule (a) ONLY. Rules
// (b)/(c)/(d) still apply to it.
func TestPresetTokensSatisfyDesignRubric(t *testing.T) {
	stateTokens := []ColorToken{TokenProgress, TokenWarning, TokenCritical, TokenSuccess, TokenActionRequired}
	exemptStateDistinct := map[string]bool{"projmux": true}

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

		// (d) provenance distinct, orange, and readable on the status bar.
		provenance, provenanceSentinel := colourOf(t, preset, TokenProvenance)
		if provenanceSentinel {
			t.Errorf("%s: provenance must be a concrete color, not the terminal-default sentinel", preset)
			continue
		}
		for _, tok := range stateTokens {
			if cn, _ := colourOf(t, preset, tok); cn == provenance {
				t.Errorf("%s: provenance and %s both resolve to %s; a fallback label must not read as a usage state", preset, tok, provenance)
			}
		}
		if provenance == TmuxAccentAIFg {
			t.Errorf("%s: provenance resolves to the AI label color %s; a fallback row would look native", preset, TmuxAccentAIFg)
		}
		provenanceHex, _ := PresetColorHex(preset, TokenProvenance)
		if hue := hueDegrees(t, provenanceHex); hue < 15 || hue > 45 {
			t.Errorf("%s: provenance %s hue %.1f° is outside the orange band [15, 45]", preset, provenanceHex, hue)
		}
		effective := ResolveTheme(ThemeConfig{Preset: preset})
		role := RenderRolesFromEffective(effective).ProvenanceFg
		r, g, b, ok := parseHexRGB(role)
		if !ok {
			t.Fatalf("%s: resolved provenance role %q is not a hex color", preset, role)
		}
		luma := rec601Luma(r, g, b)
		if colorFieldIsLight(effective.StatusBackground) {
			if luma > contrastDarkenTargetLuma {
				t.Errorf("%s: provenance role %s luma %.1f is too bright for its light status background (max %.1f)", preset, role, luma, contrastDarkenTargetLuma)
			}
		} else if luma < lightBackgroundLumaThreshold {
			t.Errorf("%s: provenance role %s luma %.1f is too dark for its dark status background (min %.1f)", preset, role, luma, lightBackgroundLumaThreshold)
		}
	}
}

// hueDegrees returns the HSV hue of a #rrggbb color, in degrees.
func hueDegrees(t *testing.T, hex string) float64 {
	t.Helper()
	r, g, b, ok := parseHexRGB(hex)
	if !ok {
		t.Fatalf("hue: %q is not a hex color", hex)
	}
	high := max(r, max(g, b))
	low := min(r, min(g, b))
	if high == low {
		return 0
	}
	span := float64(high - low)
	var hue float64
	switch high {
	case r:
		hue = math.Mod(float64(g-b)/span, 6)
	case g:
		hue = float64(b-r)/span + 2
	default:
		hue = float64(r-g)/span + 4
	}
	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue
}
