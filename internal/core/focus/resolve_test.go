package focus

import "testing"

func TestResolve_ExactMatch(t *testing.T) {
	t.Parallel()

	inv := []Candidate{
		{Name: "foo-main", Attached: true},
		{Name: "foo-feat", Attached: false},
	}

	res, ok := Resolve("foo-main", inv)
	if !ok {
		t.Fatalf("Resolve returned ok=false, want true")
	}
	if res.Name != "foo-main" || res.Fallback != "" || !res.Attached {
		t.Fatalf("Resolve = %#v, want exact attached match", res)
	}
}

func TestResolve_PrefixFallbackPicksMostRecent(t *testing.T) {
	t.Parallel()

	// Inventory is most-recent-first by contract.
	inv := []Candidate{
		{Name: "foo-feat-bar"},
		{Name: "foo-main"},
		{Name: "unrelated"},
	}

	res, ok := Resolve("foo-feat-baz", inv)
	if !ok {
		t.Fatalf("Resolve returned ok=false, want true")
	}
	if res.Name != "foo-feat-bar" {
		t.Fatalf("Resolve.Name = %q, want %q", res.Name, "foo-feat-bar")
	}
	if res.Fallback != "prefix-match" {
		t.Fatalf("Resolve.Fallback = %q, want prefix-match", res.Fallback)
	}
}

func TestResolve_PrefersLongerSharedPrefixOverRecency(t *testing.T) {
	t.Parallel()

	inv := []Candidate{
		{Name: "foo-other"},    // shares 1 token (foo)
		{Name: "foo-feat-zzz"}, // shares 2 tokens (foo, feat)
	}

	res, ok := Resolve("foo-feat-baz", inv)
	if !ok {
		t.Fatalf("Resolve returned ok=false, want true")
	}
	if res.Name != "foo-feat-zzz" {
		t.Fatalf("Resolve.Name = %q, want %q", res.Name, "foo-feat-zzz")
	}
}

func TestResolve_NoMatch(t *testing.T) {
	t.Parallel()

	inv := []Candidate{
		{Name: "alpha"},
		{Name: "beta"},
	}
	if _, ok := Resolve("gamma", inv); ok {
		t.Fatal("Resolve returned ok=true for unrelated request, want false")
	}
}

func TestResolve_EmptyRequest(t *testing.T) {
	t.Parallel()

	if _, ok := Resolve("", []Candidate{{Name: "x"}}); ok {
		t.Fatal("Resolve returned ok=true for empty request, want false")
	}
}

func TestResolve_UnderscoreToken(t *testing.T) {
	t.Parallel()

	inv := []Candidate{
		{Name: "team_lead_qa"},
	}
	res, ok := Resolve("team_lead_dev", inv)
	if !ok {
		t.Fatalf("Resolve returned ok=false, want true")
	}
	if res.Name != "team_lead_qa" || res.Fallback != "prefix-match" {
		t.Fatalf("Resolve = %#v, want team_lead_qa prefix-match", res)
	}
}
