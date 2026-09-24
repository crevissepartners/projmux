package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsContentIsExactlyThePermissionRules(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		perms Permissions
		want  string
	}{
		{"no rules", Permissions{Sandbox: "read-only", Approval: "never"}, ""},
		{"deny only", Permissions{Deny: []string{"Edit", "Write"}}, `{"permissions":{"deny":["Edit","Write"]}}`},
		{"allow only", Permissions{Allow: []string{"Read"}}, `{"permissions":{"allow":["Read"]}}`},
		{"both, specifiers kept verbatim", Permissions{Allow: []string{"Bash(git log > /dev/null)"}, Deny: []string{"WebFetch(domain:a&b.example)"}},
			`{"permissions":{"allow":["Bash(git log > /dev/null)"],"deny":["WebFetch(domain:a&b.example)"]}}`},
	} {
		got, err := SettingsContent(test.perms)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if string(got) != test.want {
			t.Errorf("%s: content = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestWriteSettingsSnapshotIsContentAddressedAndPrivate(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	perms := Permissions{Allow: []string{"Read"}, Deny: []string{"Edit"}}

	path, err := WriteSettingsSnapshot(stateDir, perms)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := SettingsContent(perms)
	if want := SettingsSnapshotPath(stateDir, content); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if dir := filepath.Join(stateDir, SettingsDirName); filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "sha256-") || filepath.Ext(path) != ".json" {
		t.Fatalf("path %q is not <state>/%s/sha256-<hex>.json", path, SettingsDirName)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"permissions":{"allow":["Read"],"deny":["Edit"]}}` {
		t.Fatalf("snapshot = %q, %v", got, err)
	}
	assertPerm(t, filepath.Dir(path), 0o700)
	assertPerm(t, path, 0o600)

	// Determinism: the same rules land at the same path with the same bytes,
	// and a loosened mode is repaired rather than kept.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := WriteSettingsSnapshot(stateDir, Permissions{Allow: []string{"Read"}, Deny: []string{"Edit"}, Sandbox: "read-only"})
	if err != nil || again != path {
		t.Fatalf("second write = %q, %v; want the same path %q", again, err, path)
	}
	assertPerm(t, path, 0o600)
	other, err := WriteSettingsSnapshot(stateDir, Permissions{Deny: []string{"Edit"}})
	if err != nil || other == path {
		t.Fatalf("different rules = %q, %v; want a different path", other, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("snapshot dir holds %v (%v), want exactly the two snapshots and no temporary file", entries, err)
	}

	none, err := WriteSettingsSnapshot(stateDir, Permissions{Sandbox: "read-only"})
	if err != nil || none != "" {
		t.Fatalf("no rules = %q, %v; want no snapshot", none, err)
	}
}

// TestStoreResolveRefusesExactlyWhatListMarksInvalid pins that Resolve and
// List agree on every profile: a profile List marks invalid -- a role claimed
// by another profile included -- is refused with List's reason token, and a
// valid one resolves.
func TestStoreResolveRefusesExactlyWhatListMarksInvalid(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	store := NewStore(configDir, t.TempDir())
	dir := filepath.Join(configDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"a":      "roles = [\"reviewer\"]\n",
		"b":      "roles = [\"reviewer\"]\n",
		"solo":   "roles = [\"writer\"]\n",
		"broken": "model = 3\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+FileExt), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		_, _, err := store.Resolve(entry.Name)
		if entry.Valid != (err == nil) || ReasonOf(err) != entry.Reason {
			t.Errorf("%s: List valid=%t reason=%q, Resolve = %v", entry.Name, entry.Valid, entry.Reason, err)
		}
	}
	for _, name := range []string{"a", "b"} {
		if _, _, err := store.Resolve(name); ReasonOf(err) != ReasonRoleClaimed || !strings.Contains(err.Error(), "reviewer") {
			t.Fatalf("Resolve(%s) = %v, want %s", name, err, ReasonRoleClaimed)
		}
	}
	loaded, spec, err := store.Resolve("solo")
	if err != nil || loaded.Name != "solo" || loaded.Digest != Digest([]byte("roles = [\"writer\"]\n")) || len(spec.Roles) != 1 {
		t.Fatalf("Resolve(solo) = %+v %+v %v", loaded, spec, err)
	}
	if _, _, err := store.Resolve("broken"); ReasonOf(err) != ReasonValueInvalid {
		t.Fatalf("Resolve(broken) = %v, want %s", err, ReasonValueInvalid)
	}
	if _, _, err := store.Resolve("gone"); ReasonOf(err) != ReasonNotFound {
		t.Fatalf("Resolve(gone) = %v, want %s", err, ReasonNotFound)
	}
	if _, spec, err := store.Resolve("readonly"); err != nil || spec.Permissions.Sandbox != "read-only" || !spec.Permissions.HasPermissions() {
		t.Fatalf("Resolve(readonly) = %+v, %v", spec, err)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
