package pins

import (
	"errors"
	"reflect"
	"testing"
)

func candidateSet(paths ...string) Set {
	set := Set{Format: FormatLegacy}
	for _, path := range paths {
		set.Pins = append(set.Pins, Pin{Kind: KindCandidate, Value: path})
	}
	return set
}

// TestResolveTypesEachLegacyPathByItsRegistryMatchCount is the migration rule in
// one table. The three outcomes are the three things a bare path can mean, and
// none of them mints or merges a uid.
func TestResolveTypesEachLegacyPathByItsRegistryMatchCount(t *testing.T) {
	t.Parallel()

	resolver := Resolver{
		GOOS: "linux",
		Projects: []ProjectRef{
			{UID: "proj-one", Root: "/srv/one"},
			{UID: "proj-dup-a", Root: "/srv/dup"},
			{UID: "proj-dup-b", Root: "/srv/dup"},
		},
	}

	resolution := resolver.Resolve(candidateSet("/srv/one", "/srv/unclaimed", "/srv/dup"))

	wantPins := []Pin{
		{Kind: KindProject, Value: "proj-one"},
		{Kind: KindCandidate, Value: "/srv/unclaimed"},
		{Kind: KindCandidate, Value: "/srv/dup"},
	}
	if got := resolution.Set.Pins; !reflect.DeepEqual(got, wantPins) {
		t.Fatalf("resolved pins = %#v, want %#v", got, wantPins)
	}
	if resolution.Set.Format != FormatTyped {
		t.Fatalf("resolved format = %q, want %q", resolution.Set.Format, FormatTyped)
	}
	if got, want := resolution.Moved, []Move{{Path: "/srv/one", UID: "proj-one"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Moved = %#v, want %#v", got, want)
	}
	if got, want := resolution.Kept, []string{"/srv/unclaimed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Kept = %#v, want %#v", got, want)
	}
	wantAmbiguous := []Ambiguity{{Path: "/srv/dup", UIDs: []string{"proj-dup-a", "proj-dup-b"}}}
	if got := resolution.Ambiguous; !reflect.DeepEqual(got, wantAmbiguous) {
		t.Fatalf("Ambiguous = %#v, want %#v", got, wantAmbiguous)
	}
}

// TestResolveIsAWriteFreeNoOpForATypedSet keeps resolution off the write path. A
// typed file is returned unchanged, which is what lets every render call this.
func TestResolveIsAWriteFreeNoOpForATypedSet(t *testing.T) {
	t.Parallel()

	typed := Set{Format: FormatTyped, Pins: []Pin{{Kind: KindProject, Value: "proj-one"}}}
	resolution := Resolver{Projects: []ProjectRef{{UID: "proj-two", Root: "/srv/two"}}}.Resolve(typed)

	if !resolution.Set.Equal(typed) {
		t.Fatalf("Set = %#v, want the input unchanged", resolution.Set)
	}
	if len(resolution.Moved)+len(resolution.Kept)+len(resolution.Ambiguous) != 0 {
		t.Fatalf("a typed set reported migration work: %#v", resolution)
	}
}

// TestResolveCollapsesTwoSpellingsOfOneProjectOntoOneUID is the dedup half. Two
// legacy lines that name one directory become one managed pin, and the sidebar sees
// one pinned Project rather than a repeated row.
func TestResolveCollapsesTwoSpellingsOfOneProjectOntoOneUID(t *testing.T) {
	t.Parallel()

	resolver := Resolver{GOOS: "linux", Projects: []ProjectRef{{UID: "proj-one", Root: "/srv/one"}}}
	resolution := resolver.Resolve(candidateSet("/srv/one", "/srv/one/"))

	want := []Pin{{Kind: KindProject, Value: "proj-one"}}
	if got := resolution.Set.Pins; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved pins = %#v, want %#v", got, want)
	}
	if got := len(resolution.Moved); got != 2 {
		t.Fatalf("Moved count = %d, want both lines reported as moved", got)
	}
}

// TestWindowsResolutionFoldsSpellingWithoutMintingIdentity is the Windows path
// boundary, asserted from any host. Drive-letter case and separator differences are
// one directory for the purpose of matching a path; they are never a reason to mint
// a second Project or to merge two.
func TestWindowsResolutionFoldsSpellingWithoutMintingIdentity(t *testing.T) {
	t.Parallel()

	resolver := Resolver{
		GOOS:     "windows",
		Projects: []ProjectRef{{UID: "proj-win", Root: `C:\Users\dev\src\app`}},
	}
	resolution := resolver.Resolve(candidateSet(`c:/users/dev/src/app`, `C:\Users\dev\src\other`))

	want := []Pin{
		{Kind: KindProject, Value: "proj-win"},
		{Kind: KindCandidate, Value: `C:\Users\dev\src\other`},
	}
	if got := resolution.Set.Pins; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved pins = %#v, want %#v", got, want)
	}
	if len(resolution.Ambiguous) != 0 {
		t.Fatalf("folding one spelling produced an ambiguity: %#v", resolution.Ambiguous)
	}

	// The same input under case-sensitive rules is a different answer, which is
	// the point of naming the OS rather than folding everywhere.
	linux := Resolver{GOOS: "linux", Projects: resolver.Projects}.Resolve(candidateSet(`c:/users/dev/src/app`))
	if got := linux.Set.Pins[0].Kind; got != KindCandidate {
		t.Fatalf("linux resolution kind = %q, want %q", got, KindCandidate)
	}
}

func TestAmbiguousMigrationErrorNamesEveryPathAndTheRepair(t *testing.T) {
	t.Parallel()

	err := error(&AmbiguousMigrationError{
		Path:      "/home/dev/.config/projmux/pins",
		Ambiguous: []Ambiguity{{Path: "/srv/dup", UIDs: []string{"proj-a", "proj-b"}}},
	})
	message := err.Error()
	for _, want := range []string{"/home/dev/.config/projmux/pins", "/srv/dup", "proj-a", "proj-b", "unchanged", "rebind project", "pin project migrate", "pin project add uid:<uid>"} {
		if !contains(message, want) {
			t.Fatalf("error %q does not mention %q", message, want)
		}
	}
	var typed *AmbiguousMigrationError
	if !errors.As(err, &typed) {
		t.Fatal("AmbiguousMigrationError is not recoverable with errors.As")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
