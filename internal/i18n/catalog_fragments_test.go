package i18n

import (
	"maps"
	"strings"
	"testing"
)

func fragmentTestBase() map[Locale]map[Key]Entry {
	return map[Locale]map[Key]Entry{
		FallbackLocale:  {Key("base.one"): textEntry("one")},
		Locale("ko-KR"): {Key("base.one"): textEntry("하나")},
	}
}

func TestMergeCatalogFragmentsAddsEveryLocaleWithoutTouchingBase(t *testing.T) {
	t.Parallel()

	base := fragmentTestBase()
	merged, err := mergeCatalogFragments(base, []catalogFragment{{
		name: "extra",
		locales: map[Locale]map[Key]Entry{
			FallbackLocale:  {Key("extra.two"): textEntry("two")},
			Locale("ko-KR"): {Key("extra.two"): textEntry("둘")},
		},
	}})
	if err != nil {
		t.Fatalf("mergeCatalogFragments error = %v", err)
	}
	for locale, want := range map[Locale]string{FallbackLocale: "two", Locale("ko-KR"): "둘"} {
		text, err := NewLocalizerWithCatalog(NewCatalog(merged), locale).Text(Key("extra.two"))
		if err != nil || text.String() != want || text.Locale() != locale {
			t.Errorf("%s extra.two = %q (locale %s, error %v), want %q", locale, text.String(), text.Locale(), err, want)
		}
	}
	if !maps.Equal(base[FallbackLocale], fragmentTestBase()[FallbackLocale]) || len(base[FallbackLocale]) != 1 {
		t.Fatalf("base was modified: %v", base)
	}
}

func TestMergeCatalogFragmentsWithNoFragmentsIsTheBase(t *testing.T) {
	t.Parallel()

	base := fragmentTestBase()
	merged, err := mergeCatalogFragments(base, nil)
	if err != nil {
		t.Fatalf("mergeCatalogFragments error = %v", err)
	}
	if len(merged) != len(base) || !maps.Equal(merged[FallbackLocale], base[FallbackLocale]) {
		t.Fatalf("merged = %v, want the base %v", merged, base)
	}
}

func TestMergeCatalogFragmentsRejectsAnInvalidFragment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		fragments []catalogFragment
		want      string
	}{
		{
			name: "key already in base",
			fragments: []catalogFragment{{name: "dup", locales: map[Locale]map[Key]Entry{
				FallbackLocale: {Key("base.one"): textEntry("again")},
			}}},
			want: `catalog fragment "dup" defines key "base.one", already defined by the embedded catalog`,
		},
		{
			name: "key in an earlier fragment",
			fragments: []catalogFragment{
				{name: "first", locales: map[Locale]map[Key]Entry{FallbackLocale: {Key("x.k"): textEntry("a")}}},
				{name: "second", locales: map[Locale]map[Key]Entry{FallbackLocale: {Key("x.k"): textEntry("b")}}},
			},
			want: `catalog fragment "second" defines key "x.k", already defined by catalog fragment "first"`,
		},
		{
			name: "no fallback locale",
			fragments: []catalogFragment{{name: "kr-only", locales: map[Locale]map[Key]Entry{
				Locale("ko-KR"): {Key("x.k"): textEntry("가")},
			}}},
			want: `catalog fragment "kr-only" has no en-US entries`,
		},
		{
			name: "unsupported locale",
			fragments: []catalogFragment{{name: "fr", locales: map[Locale]map[Key]Entry{
				FallbackLocale:  {Key("x.k"): textEntry("a")},
				Locale("fr-FR"): {Key("x.k"): textEntry("a")},
			}}},
			want: `catalog fragment "fr" lists unsupported locale "fr-FR"`,
		},
		{
			name: "locale missing a key",
			fragments: []catalogFragment{{name: "partial", locales: map[Locale]map[Key]Entry{
				FallbackLocale:  {Key("x.a"): textEntry("a"), Key("x.b"): textEntry("b")},
				Locale("ko-KR"): {Key("x.a"): textEntry("가")},
			}}},
			want: `catalog fragment "partial" defines key "x.b" in en-US but not in ko-KR`,
		},
		{
			name: "locale with an extra key",
			fragments: []catalogFragment{{name: "extra", locales: map[Locale]map[Key]Entry{
				FallbackLocale:  {Key("x.a"): textEntry("a")},
				Locale("ko-KR"): {Key("x.a"): textEntry("가"), Key("x.b"): textEntry("나")},
			}}},
			want: `catalog fragment "extra" defines key "x.b" in ko-KR but not in en-US`,
		},
		{
			name: "empty value",
			fragments: []catalogFragment{{name: "empty", locales: map[Locale]map[Key]Entry{
				FallbackLocale: {Key("x.a"): textEntry("")},
			}}},
			want: `catalog fragment "empty" has an empty en-US value for key "x.a"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := mergeCatalogFragments(fragmentTestBase(), test.fragments)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mergeCatalogFragments error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestDefaultCatalogMergesTheRegisteredFragments proves the fragments this
// build registers merge into the default catalog, whatever they are.
func TestDefaultCatalogMergesTheRegisteredFragments(t *testing.T) {
	t.Parallel()

	if _, err := mergeCatalogFragments(defaultCatalogData, catalogFragments); err != nil {
		t.Fatalf("registered catalog fragments do not merge: %v", err)
	}
	catalog := DefaultCatalog()
	for _, fragment := range catalogFragments {
		for locale, entries := range fragment.locales {
			for key, entry := range entries {
				text, err := NewLocalizerWithCatalog(catalog, locale).Text(key)
				if entry.Kind != "" && entry.Kind != MessageKindText {
					continue
				}
				if err != nil || text.String() != entry.Value {
					t.Errorf("fragment %q %s %s = %q (error %v), want %q", fragment.name, locale, key, text.String(), err, entry.Value)
				}
			}
		}
	}
}
