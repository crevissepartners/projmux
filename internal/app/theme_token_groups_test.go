package app

import (
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

// TestThemeTokenGroupsCoverEditableTokens guards the Settings theme-editor
// presentation grouping: every non-legacy public token must be directly
// selectable exactly once. `foreground` remains parseable as a legacy config
// alias but is intentionally absent from Settings.
func TestThemeTokenGroupsCoverEditableTokens(t *testing.T) {
	t.Parallel()

	seen := map[theme.ColorToken]int{}
	for _, group := range themeTokenGroups {
		for _, tok := range group.Tokens {
			seen[tok]++
		}
	}

	for _, tok := range theme.ResolverColorTokens {
		if tok == theme.TokenForeground {
			continue
		}
		switch seen[tok] {
		case 0:
			t.Errorf("token %q is editable but not shown in any themeTokenGroups group", tok)
		case 1:
			// good
		default:
			t.Errorf("token %q appears in %d groups; it must appear exactly once", tok, seen[tok])
		}
		delete(seen, tok)
	}
	for tok, n := range seen {
		t.Errorf("themeTokenGroups lists %q (%d times) but it is not an editable token", tok, n)
	}
	if slices.Contains(themeSettingsColorTokens(), theme.TokenForeground) {
		t.Fatalf("foreground must stay out of Settings rows; use text_primary/chrome_foreground instead")
	}
}

func TestThemeEntriesRenderTokensWithoutGroupRows(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
	}
	entries := themeTokenRows(t, cmd)

	prefixByToken := map[theme.ColorToken]string{}
	for _, group := range themeTokenGroups {
		for _, token := range group.Tokens {
			prefixByToken[token] = group.Prefix
		}
	}

	seen := map[theme.ColorToken]int{}
	for _, entry := range entries {
		if entry.Value == settingsNoopValue {
			t.Fatalf("themeEntries() rendered non-actionable row %#v; group headers must not be shown", entry)
		}
		if strings.HasPrefix(entry.SearchKey, "theme group ") {
			t.Fatalf("themeEntries() rendered group-header search key in %#v", entry)
		}
		raw, ok := strings.CutPrefix(entry.Value, themeAction("color:"))
		if !ok {
			continue
		}
		token := theme.ColorToken(raw)
		prefix, ok := prefixByToken[token]
		if !ok {
			t.Fatalf("themeEntries() rendered unexpected token row %#v", entry)
		}
		seen[token]++
		if !strings.Contains(entry.Label, prefix) {
			t.Fatalf("themeEntries() label for %q = %q, want group prefix %q", token, entry.Label, prefix)
		}
		if !strings.Contains(entry.SearchKey, prefix) {
			t.Fatalf("themeEntries() search key for %q = %q, want group prefix %q", token, entry.SearchKey, prefix)
		}
	}

	for _, token := range themeSettingsColorTokens() {
		if seen[token] != 1 {
			t.Fatalf("themeEntries() rendered token %q %d times, want exactly once; entries=%#v", token, seen[token], entries)
		}
	}
	if len(seen) != len(themeSettingsColorTokens()) {
		t.Fatalf("themeEntries() rendered %d token rows, want %d; seen=%#v", len(seen), len(themeSettingsColorTokens()), seen)
	}
}

// themeTokenRows flattens the token rows the Theme view now nests under
// Tokens > <group>. The rows themselves are unchanged; only their owning View
// moved, so the coverage assertions stay on the same data.
func themeTokenRows(t *testing.T, cmd *settingsCommand) []intpickercompat.Entry {
	t.Helper()

	var rows []intpickercompat.Entry
	for _, group := range themeTokenGroups {
		cfg, err := cmd.currentGlobalProjectConfig()
		if err != nil {
			t.Fatalf("currentGlobalProjectConfig() error = %v", err)
		}
		effective := theme.ResolveTheme(cfg.Theme)
		for _, token := range group.Tokens {
			rows = append(rows, intpickercompat.Entry{
				Label:     cmd.rowLabel(settingsGlyphOpen, settingsColorType, themeColorLabel(token), group.Prefix+" "+themeColorSummaryEffective(cfg.Theme, effective, token)),
				Value:     themeAction("color:" + string(token)),
				SearchKey: "theme color " + group.Prefix + " " + strings.Trim(group.Prefix, "[]") + " swatch hex input " + string(token),
			})
		}
	}
	return rows
}
