package picker

import (
	"reflect"
	"testing"
)

// TestFilterItemsGatesSearchOnlyRows pins the one opt-in field: a SearchOnly row
// is dropped while the query is empty and takes part normally once it is not,
// and a list that never sets the field is filtered exactly as before.
func TestFilterItemsGatesSearchOnlyRows(t *testing.T) {
	t.Parallel()

	plain := []Item{
		{Label: "alpha", Value: "a"},
		{Label: "beta", Value: "b"},
	}
	mixed := []Item{
		{Label: "alpha", Value: "a"},
		{Label: "hidden alpha", Value: "h", SearchOnly: true},
		{Label: "beta", Value: "b"},
		{Label: "hidden beta", Value: "hb", SearchOnly: true},
	}
	onlySearchOnly := []Item{{Label: "hidden", Value: "h", SearchOnly: true}}

	for _, tt := range []struct {
		name  string
		items []Item
		query string
		want  []string
	}{
		{"empty query keeps every plain row", plain, "", []string{"a", "b"}},
		{"empty query drops search-only rows", mixed, "", []string{"a", "b"}},
		{"empty query over search-only rows alone", onlySearchOnly, "", nil},
		{"blank query is an empty query", mixed, "   ", []string{"a", "b"}},
		{"a query lets search-only rows compete", mixed, "alpha", []string{"a", "h"}},
		{"a query still filters search-only rows out on their text", mixed, "beta", []string{"b", "hb"}},
		{"plain rows are unaffected by a query", plain, "alpha", []string{"a"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, item := range FilterItems(tt.items, tt.query) {
				got = append(got, item.Value)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FilterItems(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

// TestSearchOnlyRowsAreInvisibleOnEveryRenderPath covers the paths that do not
// go through FilterItems: a search-disabled picker and the prompt line's total.
func TestSearchOnlyRowsAreInvisibleOnEveryRenderPath(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Label: "alpha", Value: "a"},
		{Label: "hidden", Value: "h", SearchOnly: true},
	}
	options := Options{Items: items, DisableSearch: true}
	got := nativeFilteredItems(options, "")
	if len(got) != 1 || got[0].Value != "a" {
		t.Fatalf("nativeFilteredItems(DisableSearch) = %#v, want the plain row alone", got)
	}
	if total := nativeVisibleItemTotal(Options{Items: items}, ""); total != 1 {
		t.Fatalf("prompt total at an empty query = %d, want 1", total)
	}
	if total := nativeVisibleItemTotal(Options{Items: items}, "alpha"); total != 2 {
		t.Fatalf("prompt total at a non-empty query = %d, want 2", total)
	}

	// A list without the opt-in keeps the pre-existing identity: the same
	// backing slice is handed back for a search-disabled picker.
	plain := []Item{{Label: "alpha", Value: "a"}}
	plainOptions := Options{Items: plain, DisableSearch: true}
	if same := nativeFilteredItems(plainOptions, ""); &same[0] != &plain[0] {
		t.Fatalf("nativeFilteredItems copied a list with no SearchOnly row")
	}
}
