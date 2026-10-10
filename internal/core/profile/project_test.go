package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scopeP = "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
const scopeQ = "proj-bbbbbbbbbbbbbbbbbbbbbbbbba"

func TestProjectScopeParsingAndInvalidFileIsolation(t *testing.T) {
	store, dir := newTestStore(t)
	entry, err := store.Write("scoped", []byte("project = \""+scopeP+"\"\n"))
	if err != nil || entry.Project != scopeP {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	for _, value := range []string{`""`, `"alpha"`, `"uid:` + scopeP + `"`, `"proj-../escape"`, `["` + scopeP + `"]`} {
		if _, err := Parse([]byte("project = " + value)); ReasonOf(err) != ReasonValueInvalid {
			t.Fatalf("value=%s err=%v", value, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte(`project = "alpha"`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 2 || entries[0].Valid || !entries[1].Valid || entries[1].Project != scopeP {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestStoreScopeChangeRefusesWithoutChangingBytes(t *testing.T) {
	for _, before := range []string{"", scopeP} {
		for _, after := range []string{"", scopeP, scopeQ} {
			t.Run(before+"/"+after, func(t *testing.T) {
				store, _ := newTestStore(t)
				content := func(scope string) []byte {
					if scope == "" {
						return []byte("model = \"opus\"\n")
					}
					return []byte("project = \"" + scope + "\"\nmodel = \"opus\"\n")
				}
				if _, err := store.Write("same", content(before)); err != nil {
					t.Fatal(err)
				}
				_, err := store.Write("same", content(after))
				if before == after {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if ReasonOf(err) != ReasonScopeChanged {
					t.Fatalf("err=%v", err)
				}
				got, err := store.Load("same")
				if err != nil || string(got.Content) != string(content(before)) {
					t.Fatalf("got=%q err=%v", got.Content, err)
				}
			})
		}
	}
}

func TestProjectRoleVisibilityPreservesGlobalUniqueness(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Write("scoped", []byte("project = \""+scopeP+"\"\nroles = [\"review\"]")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{scopeP, scopeQ} {
		name, err := store.RoleProfile("review", target)
		want := ""
		if target == scopeP {
			want = "scoped"
		}
		if err != nil || name != want {
			t.Fatalf("target=%s name=%s err=%v", target, name, err)
		}
	}
	if _, err := store.Write("other", []byte("project = \""+scopeQ+"\"\nroles = [\"review\"]")); ReasonOf(err) != ReasonRoleClaimed {
		t.Fatalf("err=%v", err)
	}
	if err := CheckProject("scoped", Spec{Project: scopeP}, scopeQ); ReasonOf(err) != ReasonOutOfScope || !strings.Contains(err.Error(), "scoped") || !strings.Contains(err.Error(), scopeP) {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreCanRepairOtherInvalidFieldsWithoutMovingScope(t *testing.T) {
	for _, scope := range []string{"", scopeP} {
		store, dir := newTestStore(t)
		prefix := ""
		if scope != "" {
			prefix = "project = \"" + scope + "\"\n"
		}
		broken := prefix + "model = opus\n"
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte(broken), 0600); err != nil {
			t.Fatal(err)
		}
		entry, err := store.Write("broken", []byte(prefix+"model = \"opus\"\n"))
		if err != nil || entry.Project != scope {
			t.Fatalf("entry=%+v err=%v", entry, err)
		}
	}
}
