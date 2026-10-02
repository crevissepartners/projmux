package i18n

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// catalogFragment is a message set kept in its own source file and merged into
// the default catalog. Every locale it lists must define the same keys, so a
// fragment cannot ship one of its locales partly translated.
type catalogFragment struct {
	name    string
	locales map[Locale]map[Key]Entry
}

// catalogFragments are the fragments a build adds from its own source files.
// A file appends to it from an init function, so every fragment is in place
// before the default catalog is first read.
var catalogFragments []catalogFragment

// defaultCatalogLocales merges the fragments into the embedded catalog once. A
// fragment that cannot merge is a build defect, not an input error, so it
// panics the way an invalid command manifest does.
var defaultCatalogLocales = sync.OnceValue(func() map[Locale]map[Key]Entry {
	merged, err := mergeCatalogFragments(defaultCatalogData, catalogFragments)
	if err != nil {
		panic(err)
	}
	return merged
})

// mergeCatalogFragments returns base with every fragment added. base is not
// modified. A fragment must list the fallback locale, use only supported
// locales, define the same non-empty keys in each of them, and add only keys
// that neither base nor an earlier fragment defines in any locale.
func mergeCatalogFragments(base map[Locale]map[Key]Entry, fragments []catalogFragment) (map[Locale]map[Key]Entry, error) {
	if len(fragments) == 0 {
		return base, nil
	}
	merged := make(map[Locale]map[Key]Entry, len(base))
	defined := map[Key]string{}
	for locale, entries := range base {
		merged[locale] = maps.Clone(entries)
		for key := range entries {
			defined[key] = "the embedded catalog"
		}
	}
	for _, fragment := range fragments {
		if err := validateCatalogFragment(fragment); err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(fragment.locales[FallbackLocale])) {
			if owner, ok := defined[key]; ok {
				return nil, fmt.Errorf("i18n: catalog fragment %q defines key %q, already defined by %s", fragment.name, key, owner)
			}
			defined[key] = fmt.Sprintf("catalog fragment %q", fragment.name)
		}
		for locale, entries := range fragment.locales {
			if merged[locale] == nil {
				merged[locale] = make(map[Key]Entry, len(entries))
			}
			maps.Copy(merged[locale], entries)
		}
	}
	return merged, nil
}

func validateCatalogFragment(fragment catalogFragment) error {
	fallback, ok := fragment.locales[FallbackLocale]
	if !ok || len(fallback) == 0 {
		return fmt.Errorf("i18n: catalog fragment %q has no %s entries", fragment.name, FallbackLocale)
	}
	for _, locale := range slices.Sorted(maps.Keys(fragment.locales)) {
		if !IsSupportedLocale(locale) {
			return fmt.Errorf("i18n: catalog fragment %q lists unsupported locale %q", fragment.name, locale)
		}
		entries := fragment.locales[locale]
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			if entries[key].Value == "" {
				return fmt.Errorf("i18n: catalog fragment %q has an empty %s value for key %q", fragment.name, locale, key)
			}
			if _, ok := fallback[key]; !ok {
				return fmt.Errorf("i18n: catalog fragment %q defines key %q in %s but not in %s", fragment.name, key, locale, FallbackLocale)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(fallback)) {
			if _, ok := entries[key]; !ok {
				return fmt.Errorf("i18n: catalog fragment %q defines key %q in %s but not in %s", fragment.name, key, FallbackLocale, locale)
			}
		}
	}
	return nil
}
