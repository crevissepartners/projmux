package profile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/persona"
)

// newTestStore roots a store at a temp config/state pair and stores one
// instructions file named "reviewer" for profiles to name.
func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	configDir, stateDir := filepath.Join(root, "config"), filepath.Join(root, "state")
	if _, err := persona.NewStore(configDir, stateDir).Write("reviewer", []byte("You review.\n")); err != nil {
		t.Fatal(err)
	}
	return NewStore(configDir, stateDir), filepath.Join(configDir, DirName)
}

const fullProfile = `# A profile with every key.
instructions = "reviewer"   # stored instructions
model = "opus[1m]"
effort = "xhigh"
roles = ["review", "qa"]

[permissions]
sandbox = "workspace-write"
approval = "on-request"
allow = [
  "Bash(git status *)",  # read the tree
  "Read(./docs/**)",
  "WebFetch(domain:example.com)",
  "mcp__srv__tool",
]
deny = ["Edit", "Write(\"quoted\\path\")"]
`

func TestParseAcceptsTheFullVocabularyAndTheDocumentedSyntax(t *testing.T) {
	t.Parallel()
	spec, err := Parse([]byte(fullProfile))
	if err != nil {
		t.Fatal(err)
	}
	want := Spec{
		Instructions: "reviewer",
		Model:        "opus[1m]",
		Effort:       "xhigh",
		Roles:        []string{"review", "qa"},
		Permissions: Permissions{
			Sandbox:  "workspace-write",
			Approval: "on-request",
			Allow:    []string{"Bash(git status *)", "Read(./docs/**)", "WebFetch(domain:example.com)", "mcp__srv__tool"},
			Deny:     []string{"Edit", `Write("quoted\path")`},
		},
	}
	if spec.Instructions != want.Instructions || spec.Model != want.Model || spec.Effort != want.Effort ||
		!slices.Equal(spec.Roles, want.Roles) || spec.Permissions.Sandbox != want.Permissions.Sandbox ||
		spec.Permissions.Approval != want.Permissions.Approval ||
		!slices.Equal(spec.Permissions.Allow, want.Permissions.Allow) || !slices.Equal(spec.Permissions.Deny, want.Permissions.Deny) {
		t.Fatalf("spec = %+v\nwant %+v", spec, want)
	}
	for name, content := range map[string]string{
		"empty file":     "",
		"only comments":  "# nothing\n\n   # indented\n",
		"crlf":           "model = \"opus\"\r\n[permissions]\r\nsandbox = \"read-only\"\r\n",
		"no final eol":   `effort = "low"`,
		"empty array":    "roles = []\n",
		"spaced header":  "[ permissions ]\ndeny = [ ]\n",
		"trailing comma": "roles = [\"a\",]\n",
	} {
		if _, err := Parse([]byte(content)); err != nil {
			t.Errorf("%s: Parse = %v, want ok", name, err)
		}
	}
}

func TestParseRefusesEverythingOutsideTheSubsetWithItsReason(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		content string
		reason  string
	}{
		{"unknown top-level key", "color = \"red\"\n", ReasonKeyUnknown},
		{"top-level key inside permissions", "[permissions]\nmodel = \"opus\"\n", ReasonKeyUnknown},
		{"provider escape hatch", "[permissions]\nclaude_raw = \"x\"\n", ReasonKeyUnknown},
		{"unknown table", "[claude]\n", ReasonTableUnknown},
		{"dotted table", "[permissions.extra]\n", ReasonSyntax},
		{"array of tables", "[[permissions]]\n", ReasonSyntax},
		{"second permissions table", "[permissions]\n[permissions]\n", ReasonSyntax},
		{"duplicate key", "model = \"opus\"\nmodel = \"sonnet\"\n", ReasonSyntax},
		{"dotted key", "model.name = \"opus\"\n", ReasonSyntax},
		{"quoted key", "\"model\" = \"opus\"\n", ReasonSyntax},
		{"missing equals", "model \"opus\"\n", ReasonSyntax},
		{"missing value", "model =\n", ReasonValueInvalid},
		{"number", "effort = 3\n", ReasonValueInvalid},
		{"bool", "model = true\n", ReasonValueInvalid},
		{"inline table", "model = { name = \"opus\" }\n", ReasonValueInvalid},
		{"literal string", "model = 'opus'\n", ReasonValueInvalid},
		{"multi-line string", "model = \"\"\"opus\"\"\"\n", ReasonSyntax},
		{"unsupported escape", "model = \"op\\nus\"\n", ReasonSyntax},
		{"raw tab in string", "model = \"op\tus\"\n", ReasonSyntax},
		{"unterminated string", "model = \"opus\n", ReasonSyntax},
		{"trailing garbage", "model = \"opus\" x\n", ReasonSyntax},
		{"nested array", "roles = [[\"a\"]]\n", ReasonValueInvalid},
		{"array without comma", "roles = [\"a\" \"b\"]\n", ReasonSyntax},
		{"unterminated array", "roles = [\"a\",\n", ReasonSyntax},
		{"string for array key", "roles = \"a\"\n", ReasonValueInvalid},
		{"array for string key", "model = [\"opus\"]\n", ReasonValueInvalid},
		{"invalid utf-8", "model = \"\xff\"\n", ReasonSyntax},
		{"bad sandbox", "[permissions]\nsandbox = \"danger\"\n", ReasonValueInvalid},
		{"bad approval", "[permissions]\napproval = \"always\"\n", ReasonValueInvalid},
		{"bad effort", "effort = \"extreme\"\n", ReasonValueInvalid},
		{"bad model", "model = \"-opus\"\n", ReasonValueInvalid},
		{"model with space", "model = \"claude opus\"\n", ReasonValueInvalid},
		{"empty rule", "[permissions]\nallow = [\"\"]\n", ReasonValueInvalid},
		{"rule starting with digit", "[permissions]\ndeny = [\"1Bash\"]\n", ReasonValueInvalid},
		{"rule with empty specifier", "[permissions]\ndeny = [\"Bash()\"]\n", ReasonValueInvalid},
		{"rule with trailing text", "[permissions]\ndeny = [\"Bash(ls) x\"]\n", ReasonValueInvalid},
		{"rule with space in tool", "[permissions]\ndeny = [\"Web Fetch\"]\n", ReasonValueInvalid},
		{"bad instructions name", "instructions = \"../x\"\n", ReasonValueInvalid},
		{"empty instructions name", "instructions = \"\"\n", ReasonValueInvalid},
		{"empty role", "roles = [\"\"]\n", ReasonValueInvalid},
		{"padded role", "roles = [\" qa\"]\n", ReasonValueInvalid},
		{"duplicate role in one file", "roles = [\"qa\", \"qa\"]\n", ReasonRoleDuplicate},
		{"unknown provider", "provider = \"nope\"\n", ReasonProviderUnknown},
		{"provider not spelled canonically", "provider = \"Claude\"\n", ReasonProviderUnknown},
		{"padded provider", "provider = \" codex\"\n", ReasonProviderUnknown},
		{"empty provider", "provider = \"\"\n", ReasonProviderUnknown},
		{"shell is not a provider", "provider = \"shell\"\n", ReasonProviderUnknown},
		{"provider array", "provider = [\"claude\"]\n", ReasonValueInvalid},
		{"provider inside permissions", "[permissions]\nprovider = \"claude\"\n", ReasonKeyUnknown},
		{"provider twice", "provider = \"claude\"\nprovider = \"codex\"\n", ReasonSyntax},
	} {
		_, err := Parse([]byte(test.content))
		if ReasonOf(err) != test.reason {
			t.Errorf("%s: Parse(%q) = %v, want %s", test.name, test.content, err, test.reason)
		}
	}
}

func TestBuiltinReadonlyPassesTheValidatorAndClaimsNoRole(t *testing.T) {
	t.Parallel()
	raw, ok := builtins["readonly"]
	if !ok {
		t.Fatal("builtin readonly is missing")
	}
	for name, content := range builtins {
		if _, err := Parse([]byte(content)); err != nil {
			t.Errorf("builtin %s does not parse: %v", name, err)
		}
		if err := ValidateName(name); err != nil {
			t.Errorf("builtin name %s: %v", name, err)
		}
	}
	spec, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Roles) != 0 || spec.Permissions.Sandbox != "read-only" || spec.Permissions.Approval != "never" ||
		!slices.Equal(spec.Permissions.Deny, []string{"Edit", "Write", "NotebookEdit"}) {
		t.Fatalf("builtin readonly = %+v", spec)
	}
}

func TestValidateNameRefusesInvalidNamesAndTheReservedNone(t *testing.T) {
	t.Parallel()
	for name, reason := range map[string]string{
		"none":                   ReasonNameReserved,
		"../x":                   ReasonNameInvalid,
		".hidden":                ReasonNameInvalid,
		"-flag":                  ReasonNameInvalid,
		"":                       ReasonNameInvalid,
		strings.Repeat("n", 129): ReasonNameInvalid,
		"reviewer":               "",
		"None":                   "",
	} {
		if got := ReasonOf(ValidateName(name)); got != reason {
			t.Errorf("ValidateName(%q) reason = %q, want %q", name, got, reason)
		}
	}
}

func TestStoreWriteLoadRoundTripsBytesAtPrivateModes(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	entry, err := store.Write("reviewer", []byte(fullProfile))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Digest != Digest([]byte(fullProfile)) || !strings.HasPrefix(entry.Digest, "sha256:") || entry.Source != SourceUser {
		t.Fatalf("entry = %+v", entry)
	}
	loaded, err := store.Load("reviewer")
	if err != nil || string(loaded.Content) != fullProfile || loaded.Source != SourceUser {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(dir, "reviewer.toml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, %v", dirInfo, err)
	}
}

func TestStoreWriteRefusalLeavesNoFileAndKeepsTheExistingOne(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	if _, err := store.Write("fresh", []byte("colour = \"x\"\n")); ReasonOf(err) != ReasonKeyUnknown {
		t.Fatalf("Write unknown key = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write created %s: %v", dir, err)
	}
	original := []byte("effort = \"low\"\n")
	if _, err := store.Write("kept", original); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("kept", []byte("effort = \"huge\"\n")); ReasonOf(err) != ReasonValueInvalid {
		t.Fatalf("Write bad effort = %v", err)
	}
	if _, err := store.Write("kept", []byte("instructions = \"absent\"\n")); ReasonOf(err) != ReasonInstructionsNotFound {
		t.Fatalf("Write missing instructions = %v", err)
	}
	if _, err := store.Write("kept", []byte(strings.Repeat("#", MaxSize+1))); ReasonOf(err) != ReasonTooLarge {
		t.Fatalf("Write oversized = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "kept.toml"))
	if err != nil || string(got) != string(original) {
		t.Fatalf("kept.toml = %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("profiles dir = %v, %v; want only kept.toml", entries, err)
	}
}

func TestStoreWriteRefusesARoleAnotherValidProfileClaims(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	if _, err := store.Write("first", []byte("roles = [\"review\"]\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("second", []byte("roles = [\"qa\", \"review\"]\n")); ReasonOf(err) != ReasonRoleClaimed {
		t.Fatalf("Write claimed role = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "second.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write left second.toml: %v", err)
	}
	// Rewriting the claimant itself is not a conflict.
	if _, err := store.Write("first", []byte("roles = [\"review\", \"qa\"]\n")); err != nil {
		t.Fatalf("rewrite of the claimant = %v", err)
	}
	// A hand-placed file that does not parse claims nothing.
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("roles = [\"ops\"]\nbogus = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("third", []byte("roles = [\"ops\"]\n")); err != nil {
		t.Fatalf("role listed only by an unparseable file = %v", err)
	}
}

func TestStoreListShowsBuiltinsUserFilesAndInvalidFilesWithoutStopping(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "readonly" || entries[0].Source != SourceBuiltin || !entries[0].Valid ||
		entries[0].Digest != Digest([]byte(builtins["readonly"])) || len(entries[0].Roles) != 0 {
		t.Fatalf("empty-config list = %+v", entries)
	}
	if _, err := store.Write("alpha", []byte("roles = [\"review\"]\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("[claude]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "huge.toml"), []byte(strings.Repeat("#", MaxSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, skipped := range []string{".hidden.toml", "notes.txt", "none.toml"} {
		if err := os.WriteFile(filepath.Join(dir, skipped), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	var names []string
	for _, entry := range entries {
		got[entry.Name] = entry
		names = append(names, entry.Name)
	}
	if !slices.Equal(names, []string{"alpha", "broken", "huge", "readonly"}) {
		t.Fatalf("names = %v", names)
	}
	if !got["alpha"].Valid || got["alpha"].Source != SourceUser || !slices.Equal(got["alpha"].Roles, []string{"review"}) {
		t.Fatalf("alpha = %+v", got["alpha"])
	}
	if got["broken"].Valid || got["broken"].Reason != ReasonTableUnknown || got["broken"].Digest != Digest([]byte("[claude]\n")) {
		t.Fatalf("broken = %+v", got["broken"])
	}
	if got["huge"].Valid || got["huge"].Reason != ReasonTooLarge {
		t.Fatalf("huge = %+v", got["huge"])
	}
}

func TestStoreListMarksEveryProfileSharingARoleInvalid(t *testing.T) {
	t.Parallel()
	store, dir := newTestStore(t)
	for name, content := range map[string]string{"a": "roles = [\"qa\"]\n", "b": "roles = [\"qa\"]\n", "c": "roles = [\"ops\"]\n"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+FileExt), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		wantValid := entry.Name == "c" || entry.Name == "readonly"
		if entry.Valid != wantValid || (!wantValid && entry.Reason != ReasonRoleClaimed) {
			t.Errorf("%s = %+v, want valid=%v", entry.Name, entry, wantValid)
		}
	}
}

func TestUserReadonlyShadowsTheBuiltinAndDeletingItRevealsTheBuiltin(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	if err := store.Delete("readonly"); ReasonOf(err) != ReasonBuiltin {
		t.Fatalf("Delete builtin-only = %v", err)
	}
	if err := store.Delete("absent"); ReasonOf(err) != ReasonNotFound {
		t.Fatalf("Delete absent = %v", err)
	}
	if _, err := store.Load("absent"); ReasonOf(err) != ReasonNotFound {
		t.Fatalf("Load absent = %v", err)
	}
	if _, err := store.Load("none"); ReasonOf(err) != ReasonNameReserved {
		t.Fatalf("Load none = %v", err)
	}
	user := []byte("effort = \"low\"\n")
	if _, err := store.Write("readonly", user); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("readonly")
	if err != nil || string(loaded.Content) != string(user) || loaded.Source != SourceUser {
		t.Fatalf("Load shadowed readonly = %+v, %v", loaded, err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Source != SourceUser || entries[0].Digest != Digest(user) {
		t.Fatalf("list with a user readonly = %+v, %v", entries, err)
	}
	if err := store.Delete("readonly"); err != nil {
		t.Fatalf("Delete user readonly = %v", err)
	}
	loaded, err = store.Load("readonly")
	if err != nil || loaded.Source != SourceBuiltin || string(loaded.Content) != builtins["readonly"] {
		t.Fatalf("Load after deleting the user readonly = %+v, %v", loaded, err)
	}
}
