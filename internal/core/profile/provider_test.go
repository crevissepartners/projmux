package profile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

// TestParseAcceptsExactlyTheRegisteredProviders pins the `provider` authority
// to the provider registry `create agent --provider` is checked against: every
// registered id is accepted as spelled, and the refusal names them all.
func TestParseAcceptsExactlyTheRegisteredProviders(t *testing.T) {
	t.Parallel()
	registered := make([]string, 0)
	for _, meta := range aiprovider.All() {
		registered = append(registered, string(meta.ID))
	}
	if got := Providers(); len(got) == 0 || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(registered))) {
		t.Fatalf("Providers() = %v, want the registry ids %v", got, registered)
	}
	for _, id := range registered {
		spec, err := Parse([]byte("provider = \"" + id + "\"\n"))
		if err != nil || spec.Provider != id {
			t.Errorf("provider %q = %+v, %v", id, spec, err)
		}
	}
	_, err := Parse([]byte("provider = \"nope\"\n"))
	if ReasonOf(err) != ReasonProviderUnknown {
		t.Fatalf("provider nope = %v", err)
	}
	for _, id := range registered {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("refusal %q does not name accepted provider %s", err, id)
		}
	}
}

// TestAProfileWithoutProviderParsesAndDigestsAsBefore holds a file without
// the key to what it was before `provider` existed: the builtin readonly keeps
// its digest and spec, and adding the key changes Provider and nothing else.
func TestAProfileWithoutProviderParsesAndDigestsAsBefore(t *testing.T) {
	t.Parallel()
	const readonlyDigest = "sha256:bca5e1424e9d43d92ac9f66b0ab3c0540452b2acb369091ff6ea3ba8523240a1"
	if got := Digest([]byte(builtins["readonly"])); got != readonlyDigest {
		t.Fatalf("builtin readonly digest = %s, want %s", got, readonlyDigest)
	}
	spec, err := Parse([]byte(builtins["readonly"]))
	if err != nil {
		t.Fatal(err)
	}
	want := Spec{Permissions: Permissions{Sandbox: "read-only", Approval: "never", Deny: []string{"Edit", "Write", "NotebookEdit"}}}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("readonly spec = %+v, want %+v", spec, want)
	}
	neutral, err := Parse([]byte(fullProfile))
	if err != nil {
		t.Fatal(err)
	}
	named, err := Parse([]byte("provider = \"claude\"\n" + fullProfile))
	if err != nil {
		t.Fatal(err)
	}
	if neutral.Provider != "" || named.Provider != "claude" {
		t.Fatalf("providers = %q, %q", neutral.Provider, named.Provider)
	}
	named.Provider = ""
	if !reflect.DeepEqual(neutral, named) {
		t.Fatalf("the provider key changed more than Provider: %+v vs %+v", neutral, named)
	}
}

func writeHandPlaced(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+FileExt), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStoreListKeepsTheItemsOfAnInvalidProfileThatParses: a profile whose
// instructions are missing still shows its provider, instructions, model,
// effort, and roles; a file that does not parse shows none.
func TestStoreListKeepsTheItemsOfAnInvalidProfileThatParses(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	if _, err := store.Write("good", []byte("provider = \"codex\"\ninstructions = \"reviewer\"\nmodel = \"gpt-5\"\neffort = \"high\"\nroles = [\"review\"]\n")); err != nil {
		t.Fatal(err)
	}
	writeHandPlaced(t, dir, "orphan", "provider = \"claude\"\ninstructions = \"gone\"\nmodel = \"opus\"\neffort = \"low\"\nroles = [\"qa\"]\n")
	writeHandPlaced(t, dir, "broken", "roles = [\"ops\"]\nbogus = \"x\"\n")
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	good := got["good"]
	if !good.Valid || good.Provider != "codex" || good.Instructions != "reviewer" || good.Model != "gpt-5" || good.Effort != "high" || !slices.Equal(good.Roles, []string{"review"}) {
		t.Fatalf("good = %+v", good)
	}
	orphan := got["orphan"]
	if orphan.Valid || orphan.Reason != ReasonInstructionsNotFound || orphan.Provider != "claude" || orphan.Instructions != "gone" ||
		orphan.Model != "opus" || orphan.Effort != "low" || !slices.Equal(orphan.Roles, []string{"qa"}) {
		t.Fatalf("orphan = %+v", orphan)
	}
	broken := got["broken"]
	if broken.Valid || broken.Reason != ReasonKeyUnknown || broken.Provider != "" || broken.Instructions != "" || len(broken.Roles) != 0 {
		t.Fatalf("broken = %+v", broken)
	}
	if ro := got["readonly"]; !ro.Valid || ro.Provider != "" || ro.Instructions != "" || ro.Model != "" || ro.Effort != "" || len(ro.Roles) != 0 {
		t.Fatalf("readonly = %+v", ro)
	}
}

// TestStoreRoleProfileCountsEveryProfileThatParses is the role-selection
// table: one valid listing profile selects it, none selects nothing, and
// several listing profiles or one invalid listing profile refuse.
func TestStoreRoleProfileCountsEveryProfileThatParses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		files  map[string]string
		want   string
		reason string
		detail []string
	}{
		{name: "none lists it", files: map[string]string{"a": "roles = [\"qa\"]\n"}},
		{name: "one valid", files: map[string]string{"a": "roles = [\"review\"]\n", "b": "roles = [\"qa\"]\n"}, want: "a"},
		{name: "two valid", files: map[string]string{"a": "roles = [\"review\"]\n", "b": "roles = [\"review\"]\n"},
			reason: ReasonRoleClaimed, detail: []string{`role "review"`, "(a, b)"}},
		{name: "one invalid", files: map[string]string{"a": "instructions = \"gone\"\nroles = [\"review\"]\n"},
			reason: ReasonRoleProfileInvalid, detail: []string{`profile "a"`, `role "review"`, ReasonInstructionsNotFound}},
		{name: "one valid and one invalid", files: map[string]string{"a": "roles = [\"review\"]\n", "b": "instructions = \"gone\"\nroles = [\"review\"]\n"},
			reason: ReasonRoleClaimed, detail: []string{"(a, b)"}},
		{name: "an unparseable file lists nothing", files: map[string]string{"a": "roles = [\"review\"]\n", "b": "roles = [\"review\"]\nbogus = \"x\"\n"}, want: "a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, dir := newTestStore(t)
			for name, content := range test.files {
				writeHandPlaced(t, dir, name, content)
			}
			got, err := store.RoleProfile("review")
			if test.reason == "" {
				if err != nil || got != test.want {
					t.Fatalf("RoleProfile = %q, %v; want %q", got, err, test.want)
				}
				return
			}
			if got != "" || ReasonOf(err) != test.reason {
				t.Fatalf("RoleProfile = %q, %v; want %s", got, err, test.reason)
			}
			for _, want := range test.detail {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestStoreRoleClaimCountsAnInvalidProfileThatParses: List and Write count a
// parsed invalid profile's roles the way RoleProfile does. A valid profile
// sharing a role with it is role-claimed while the invalid one keeps its own
// reason, and writing a profile that lists that role is refused.
func TestStoreRoleClaimCountsAnInvalidProfileThatParses(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	writeHandPlaced(t, dir, "orphan", "instructions = \"gone\"\nroles = [\"review\"]\n")
	writeHandPlaced(t, dir, "rival", "roles = [\"review\"]\n")
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		switch entry.Name {
		case "orphan":
			if entry.Valid || entry.Reason != ReasonInstructionsNotFound {
				t.Errorf("orphan = %+v", entry)
			}
		case "rival":
			if entry.Valid || entry.Reason != ReasonRoleClaimed {
				t.Errorf("rival = %+v", entry)
			}
		}
	}
	_, err = store.Write("third", []byte("roles = [\"review\"]\n"))
	if ReasonOf(err) != ReasonRoleClaimed || !strings.Contains(err.Error(), "that profile is invalid") {
		t.Fatalf("Write of a role an invalid profile lists = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "third.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write left third.toml: %v", err)
	}
}

// TestStoreProfilesUsingInstructionsNamesEveryUserProfileThatParses: valid
// and invalid user profiles naming the instructions count, a file that does
// not parse and a profile naming other instructions do not.
func TestStoreProfilesUsingInstructionsNamesEveryUserProfileThatParses(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	if _, err := store.Write("b-valid", []byte("instructions = \"reviewer\"\n")); err != nil {
		t.Fatal(err)
	}
	writeHandPlaced(t, dir, "a-invalid", "instructions = \"reviewer\"\neffort = \"high\"\nroles = [\"qa\"]\n")
	writeHandPlaced(t, dir, "a-claimant", "roles = [\"qa\"]\n")
	writeHandPlaced(t, dir, "broken", "instructions = \"reviewer\"\nbogus = \"x\"\n")
	writeHandPlaced(t, dir, "other", "instructions = \"gone\"\n")
	got, err := store.ProfilesUsingInstructions("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a-invalid", "b-valid"}; !slices.Equal(got, want) {
		t.Fatalf("ProfilesUsingInstructions(reviewer) = %v, want %v", got, want)
	}
	if got, err := store.ProfilesUsingInstructions("unused"); err != nil || len(got) != 0 {
		t.Fatalf("ProfilesUsingInstructions(unused) = %v, %v", got, err)
	}
}
