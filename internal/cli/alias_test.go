package cli

import (
	"slices"
	"strings"
	"testing"
)

// resourceVerbs are the four verbs the kind-spelling parity contract covers.
// `create` is deliberately absent: its kinds take a different argument contract
// and its spelling set is adjudicated separately.
var resourceVerbs = []string{"get", "describe", "delete", "rename"}

// canonicalKindSpellings pins the canonical kind token of every resource-verb
// child, exactly as the CLI information architecture v2 contract fixed it.
//
// This is the removal guard. Adding an alias is additive by construction, but
// nothing in the manifest's shape stops someone from "simplifying" a canonical
// spelling into an alias of a different one, which would silently retire a
// shipped route. Pinning the canonical set as a literal makes that a failing
// test rather than a diff nobody reads.
var canonicalKindSpellings = map[string][]string{
	"get":      {"projects", "windows", "panes", "agents", "notifications", "snapshots", "pane"},
	"describe": {"project", "window", "pane", "agent"},
	"delete":   {"project", "window", "pane", "agent", "notification", "snapshot"},
	"rename":   {"project", "window", "pane", "agent"},
}

// splitKindSpellingPair is the one place a kind's two forms are related. Every
// kind projmux addresses pluralizes regularly, so the rule is the trailing `s`
// and nothing more; a kind that ever breaks it has to state both forms here
// rather than have the test quietly stop covering it.
func splitKindSpellingPair(token string) (singular, plural string) {
	if trimmed, ok := strings.CutSuffix(token, "s"); ok {
		return trimmed, token
	}
	return token, token + "s"
}

// resourceVerbChildren returns the kind children of one resource verb.
//
// Namespace children are skipped. `get runtime` groups tmux object kinds, which
// are not Projmux resource kinds and have no singular resource read, so folding
// it in here would make the parity matrix demand a `runtimes` spelling for
// objects the Registry does not name.
func resourceVerbChildren(t *testing.T, verb string) []Route {
	t.Helper()
	route, ok := LookupRoute(verb)
	if !ok {
		t.Fatalf("%s is not a top-level route", verb)
	}
	kinds := make([]Route, 0, len(route.Children))
	for _, child := range route.Children {
		if child.Namespace {
			continue
		}
		kinds = append(kinds, child)
	}
	return kinds
}

// TestCanonicalKindSpellingsSurviveTheAliasContract is the negative half of the
// parity work: this track adds spellings and removes none.
func TestCanonicalKindSpellingsSurviveTheAliasContract(t *testing.T) {
	t.Parallel()

	for _, verb := range resourceVerbs {
		var got []string
		for _, child := range resourceVerbChildren(t, verb) {
			got = append(got, child.Name)
		}
		want := canonicalKindSpellings[verb]
		if !slices.Equal(got, want) {
			t.Fatalf("%s canonical kinds = %v, want %v", verb, got, want)
		}
	}
}

// TestNoChildAliasShadowsACanonicalSpelling walks the whole manifest, not only
// the resource verbs: an alias that collides with a canonical sibling would
// make one of the two unreachable, and which one depends on declaration order.
func TestNoChildAliasShadowsACanonicalSpelling(t *testing.T) {
	t.Parallel()

	var walk func(path []string, children []Route)
	walk = func(path []string, children []Route) {
		names := map[string]bool{}
		for _, child := range children {
			names[child.Name] = true
		}
		claimed := map[string]string{}
		for _, child := range children {
			for _, alias := range child.Aliases {
				where := strings.Join(append(append([]string{}, path...), child.Name), " ")
				if names[alias] {
					t.Fatalf("%s declares alias %q, which is already a canonical sibling spelling", where, alias)
				}
				if alias == child.Name {
					t.Fatalf("%s declares its own name %q as an alias", where, alias)
				}
				if prior, ok := claimed[alias]; ok {
					t.Fatalf("alias %q is declared by both %q and %q under %q",
						alias, prior, child.Name, strings.Join(path, " "))
				}
				claimed[alias] = child.Name
			}
			walk(append(append([]string{}, path...), child.Name), child.Children)
		}
	}
	for _, route := range routes {
		if len(route.Aliases) > 0 {
			t.Fatalf("top-level route %q declares aliases; the alias contract covers kind tokens only", route.Name)
		}
		walk([]string{route.Name}, route.Children)
	}
}

// TestEveryKindSpellingNormalizesOntoItsCanonicalToken is the property the
// whole design rests on: an alias is a spelling, so normalization is total and
// idempotent, and a canonical token normalizes onto itself.
func TestEveryKindSpellingNormalizesOntoItsCanonicalToken(t *testing.T) {
	t.Parallel()

	for _, verb := range resourceVerbs {
		for _, child := range resourceVerbChildren(t, verb) {
			got, ok := CanonicalChildToken(verb, child.Name)
			if !ok || got != child.Name {
				t.Fatalf("%s %s normalized to (%q, %t), want (%q, true)", verb, child.Name, got, ok, child.Name)
			}
			for _, alias := range child.Aliases {
				got, ok := CanonicalChildToken(verb, alias)
				if !ok || got != child.Name {
					t.Fatalf("%s %s normalized to (%q, %t), want (%q, true)", verb, alias, got, ok, child.Name)
				}
			}
		}
	}
}

// TestResourceVerbsAcceptBothFormsOfEveryKind is the parity matrix at the
// manifest level: a kind spelling accepted by a resource verb has its
// counterpart form accepted by that same verb, with zero missing combinations.
//
// The assertion is deliberately about acceptance and not about destination.
// Every pair but one lands on the same node; the exception is enumerated below
// and checked separately, so "these two forms mean different things" can only
// be true where the contract says so.
func TestResourceVerbsAcceptBothFormsOfEveryKind(t *testing.T) {
	t.Parallel()

	// divergentKindPairs are the singular/plural pairs a verb accepts that
	// deliberately reach different routes. `get pane` is the exact-one Pane read
	// that owns `--current -o cwd` and is what `projmux current` maps onto; it
	// was never the singular of the `get panes` inventory, so aliasing the two
	// together would delete a shipped route's meaning rather than add a
	// spelling.
	divergentKindPairs := map[string]bool{"get pane|panes": true}

	for _, verb := range resourceVerbs {
		for _, child := range resourceVerbChildren(t, verb) {
			for _, spelling := range append([]string{child.Name}, child.Aliases...) {
				singular, plural := splitKindSpellingPair(spelling)
				singularTarget, singularOK := CanonicalChildToken(verb, singular)
				pluralTarget, pluralOK := CanonicalChildToken(verb, plural)
				if !singularOK {
					t.Fatalf("%s accepts %q but rejects its singular %q", verb, spelling, singular)
				}
				if !pluralOK {
					t.Fatalf("%s accepts %q but rejects its plural %q", verb, spelling, plural)
				}
				key := verb + " " + singular + "|" + plural
				if singularTarget == pluralTarget {
					if divergentKindPairs[key] {
						t.Fatalf("%q is listed as a divergent pair but both forms reach %q", key, singularTarget)
					}
					continue
				}
				if !divergentKindPairs[key] {
					t.Fatalf("%s %q reaches %q but %s %q reaches %q; an alias must reach the canonical route",
						verb, singular, singularTarget, verb, plural, pluralTarget)
				}
			}
		}
	}
}

// TestGetPaneKeepsItsOwnRouteUnderBothSpellings pins the one product decision
// this track made explicitly, so a later "consistency" cleanup has to argue
// with a test instead of a comment.
func TestGetPaneKeepsItsOwnRouteUnderBothSpellings(t *testing.T) {
	t.Parallel()

	singular, ok := CanonicalChildToken("get", "pane")
	if !ok || singular != "pane" {
		t.Fatalf("get pane normalized to (%q, %t), want (\"pane\", true)", singular, ok)
	}
	plural, ok := CanonicalChildToken("get", "panes")
	if !ok || plural != "panes" {
		t.Fatalf("get panes normalized to (%q, %t), want (\"panes\", true)", plural, ok)
	}

	route, _ := LookupRoute("get")
	for _, child := range route.Children {
		if child.Name == "panes" && len(child.Aliases) > 0 {
			t.Fatalf("get panes declares aliases %v; the singular belongs to the exact-one Pane read", child.Aliases)
		}
		if child.Name == "pane" && len(child.Aliases) > 0 {
			t.Fatalf("get pane declares aliases %v; it is not the singular of the Pane inventory", child.Aliases)
		}
	}
}

// TestChildSpellingsListsEveryAcceptedKindToken keeps the unknown-kind refusals
// honest: the list they print is this function's output, so a spelling missing
// from it is a spelling the operator is never told about.
func TestChildSpellingsListsEveryAcceptedKindToken(t *testing.T) {
	t.Parallel()

	for _, verb := range resourceVerbs {
		spellings := ChildSpellings(verb)
		// Every accepted child token, namespace nodes included: the refusal
		// offers what argv accepts, and `get runtime` is accepted.
		route, ok := LookupRoute(verb)
		if !ok {
			t.Fatalf("%s is not a top-level route", verb)
		}
		children := route.Children
		if len(spellings) != len(children) {
			t.Fatalf("%s renders %d spelling groups for %d children", verb, len(spellings), len(children))
		}
		for i, child := range children {
			want := strings.Join(append([]string{child.Name}, child.Aliases...), "|")
			if spellings[i] != want {
				t.Fatalf("%s spelling group %d = %q, want %q", verb, i, spellings[i], want)
			}
			for token := range strings.SplitSeq(spellings[i], "|") {
				if got, ok := CanonicalChildToken(verb, token); !ok || got != child.Name {
					t.Fatalf("%s advertises %q, which normalizes to (%q, %t)", verb, token, got, ok)
				}
			}
		}
	}
}

// TestUnknownChildTokensStayUnknown is the other half of normalization: it
// widens the accepted set by exactly the declared aliases and by nothing else.
func TestUnknownChildTokensStayUnknown(t *testing.T) {
	t.Parallel()

	for _, verb := range resourceVerbs {
		for _, token := range []string{"", "zzz", "paness", "Pane", "agent-hook"} {
			if _, ok := CanonicalChildToken(verb, token); ok {
				accepted := true
				for _, child := range resourceVerbChildren(t, verb) {
					if child.Name == token || slices.Contains(child.Aliases, token) {
						accepted = false
					}
				}
				if accepted {
					t.Fatalf("%s accepted undeclared kind token %q", verb, token)
				}
			}
		}
	}
	if _, ok := CanonicalChildToken("nosuchverb", "pane"); ok {
		t.Fatal("CanonicalChildToken resolved a child of an unknown parent route")
	}
	if got := ChildSpellings("nosuchverb"); got != nil {
		t.Fatalf("ChildSpellings(unknown) = %v, want nil", got)
	}
}

// TestRenameAcceptsAgentUnderBothForms pins the public stable-name parity added
// to the rename family.
func TestRenameAcceptsAgentUnderBothForms(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"agent", "agents"} {
		if got, ok := CanonicalChildToken("rename", token); !ok || got != "agent" {
			t.Fatalf("rename %s resolved to %q,%v, want agent,true", token, got, ok)
		}
	}
}
