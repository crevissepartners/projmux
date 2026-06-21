package app

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

// TestThemeTokenGroupsCoverAllTokens guards the Settings theme-editor
// presentation grouping: it must cover theme.ResolverColorTokens (the stable
// serialization order) exactly once — no token dropped from the menu, none
// shown twice. The grouping reorders rows for display; this keeps it in sync
// with the canonical token set as tokens are added or removed.
func TestThemeTokenGroupsCoverAllTokens(t *testing.T) {
	t.Parallel()

	seen := map[theme.ColorToken]int{}
	for _, group := range themeTokenGroups {
		for _, tok := range group.Tokens {
			seen[tok]++
		}
	}

	for _, tok := range theme.ResolverColorTokens {
		switch seen[tok] {
		case 0:
			t.Errorf("token %q is in ResolverColorTokens but not shown in any themeTokenGroups group", tok)
		case 1:
			// good
		default:
			t.Errorf("token %q appears in %d groups; it must appear exactly once", tok, seen[tok])
		}
		delete(seen, tok)
	}
	for tok, n := range seen {
		t.Errorf("themeTokenGroups lists %q (%d times) but it is not a ResolverColorTokens token", tok, n)
	}
}
