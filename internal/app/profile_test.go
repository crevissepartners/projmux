package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
)

// newProfileTestCommand roots one profile command at an isolated HOME with one
// stored instructions file, "reviewer". It returns the command and the
// profiles directory that HOME resolves to.
func newProfileTestCommand(t *testing.T, stdin string) (*profileCommand, string) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "projmux")
	if err := os.MkdirAll(filepath.Join(configDir, "personas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "personas", "reviewer.md"), []byte("You review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &profileCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		stdin:     strings.NewReader(stdin),
	}
	return cmd, filepath.Join(configDir, "profiles")
}

func runProfile(cmd *profileCommand, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := cmd.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// profileListRows parses `profile list` into rows keyed by NAME.
func profileListRows(t *testing.T, stdout string) map[string][]string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "NAME SOURCE PROVIDER INSTRUCTIONS MODEL EFFORT ROLES DIGEST VALID" {
		t.Fatalf("list header = %q", stdout)
	}
	rows := map[string][]string{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		rows[fields[0]] = fields
	}
	return rows
}

const profileTestContent = `instructions = "reviewer"
model = "opus"
effort = "high"
roles = ["review"]

[permissions]
sandbox = "workspace-write"
approval = "on-request"
allow = ["Bash(git status *)", "Read(./docs/**)"]
deny = ["WebFetch(domain:example.com)"]
`

func TestProfileSetValidFileShowsIdenticalBytesAndListsUserSourceWithDigest(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "")
	source := filepath.Join(t.TempDir(), "p.toml")
	if err := os.WriteFile(source, []byte(profileTestContent), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := profile.Digest([]byte(profileTestContent))

	stdout, _, err := runProfile(cmd, "set", "reviewer", "--file", source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "reviewer.toml")
	if !strings.Contains(stdout, digest) || !strings.Contains(stdout, path) || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("set stdout = %q", stdout)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile file = %v, %v; want mode 0600", info, err)
	}

	stdout, _, err = runProfile(cmd, "show", "reviewer")
	if err != nil || stdout != profileTestContent {
		t.Fatalf("show = %q, %v", stdout, err)
	}

	stdout, _, err = runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	row := profileListRows(t, stdout)["reviewer"]
	if !slices.Equal(row, []string{"reviewer", "user", "-", "reviewer", "opus", "high", "review", digest, "yes"}) {
		t.Fatalf("list row = %q in %q", row, stdout)
	}

	// Stdin works as with instructions.
	cmd.stdin = strings.NewReader("effort = \"low\"\n")
	if _, _, err := runProfile(cmd, "set", "quick", "-"); err != nil {
		t.Fatal(err)
	}
	if stdout, _, err := runProfile(cmd, "show", "quick"); err != nil || stdout != "effort = \"low\"\n" {
		t.Fatalf("show quick = %q, %v", stdout, err)
	}
}

func TestProfileSetRejectionsExitTwoWithTheReasonAndWriteNothing(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		profile string
		content string
		reason  string
	}{
		{"unknown key", "p", "colour = \"red\"\n", profile.ReasonKeyUnknown},
		{"unknown table", "p", "[claude]\n", profile.ReasonTableUnknown},
		{"bad sandbox", "p", "[permissions]\nsandbox = \"yolo\"\n", profile.ReasonValueInvalid},
		{"bad approval", "p", "[permissions]\napproval = \"sometimes\"\n", profile.ReasonValueInvalid},
		{"bad effort", "p", "effort = \"ultra\"\n", profile.ReasonValueInvalid},
		{"bad model", "p", "model = \"opus 4\"\n", profile.ReasonValueInvalid},
		{"bad rule", "p", "[permissions]\ndeny = [\"Bash(\"]\n", profile.ReasonValueInvalid},
		{"empty rule", "p", "[permissions]\nallow = [\"\"]\n", profile.ReasonValueInvalid},
		{"missing instructions", "p", "instructions = \"absent\"\n", profile.ReasonInstructionsNotFound},
		{"duplicate role in file", "p", "roles = [\"qa\", \"qa\"]\n", profile.ReasonRoleDuplicate},
		{"role claimed by another profile", "p", "roles = [\"owned\"]\n", profile.ReasonRoleClaimed},
		{"invalid name", "../escape", "effort = \"low\"\n", profile.ReasonNameInvalid},
		{"flag-like name", "-x", "effort = \"low\"\n", profile.ReasonNameInvalid},
		{"reserved none", "none", "effort = \"low\"\n", profile.ReasonNameReserved},
		{"too large", "p", strings.Repeat("#", profile.MaxSize+1), profile.ReasonTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, dir := newProfileTestCommand(t, "")
			// One existing profile owns the role "owned", and p already
			// exists so a refusal can be seen not to change it.
			cmd.stdin = strings.NewReader("roles = [\"owned\"]\n")
			if _, _, err := runProfile(cmd, "set", "owner"); err != nil {
				t.Fatal(err)
			}
			const original = "effort = \"medium\"\n"
			cmd.stdin = strings.NewReader(original)
			if _, _, err := runProfile(cmd, "set", "p"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}

			cmd.stdin = strings.NewReader(test.content)
			_, _, err = runProfile(cmd, "set", test.profile)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("set = %v, want %s", err, test.reason)
			}
			if code := exitCodeOf(err); code != 2 {
				t.Fatalf("exit code = %d, want 2 (%v)", code, err)
			}
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("a refused set changed the directory: %v -> %v", before, after)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "p.toml")); err != nil || string(got) != original {
				t.Fatalf("p.toml after a refused set = %q, %v", got, err)
			}
		})
	}
}

func TestProfileEmptyConfigListsBuiltinReadonlyRefusesItsDeleteAndAUserReadonlyWins(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "")
	stdout, _, err := runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	rows := profileListRows(t, stdout)
	readonly := rows["readonly"]
	if len(rows) != 1 || len(readonly) != 9 || !slices.Equal(readonly[:7], []string{"readonly", "builtin", "-", "-", "-", "-", "-"}) ||
		!strings.HasPrefix(readonly[7], "sha256:") || readonly[8] != "yes" {
		t.Fatalf("empty-config list = %q", stdout)
	}
	builtin, _, err := runProfile(cmd, "show", "readonly")
	if err != nil || !strings.Contains(builtin, "sandbox = \"read-only\"") {
		t.Fatalf("show builtin readonly = %q, %v", builtin, err)
	}
	if profile.Digest([]byte(builtin)) != readonly[7] {
		t.Fatalf("builtin digest %s does not hash the shown bytes", readonly[7])
	}

	_, _, err = runProfile(cmd, "delete", "readonly", "--yes")
	if err == nil || !strings.Contains(err.Error(), profile.ReasonBuiltin) || exitCodeOf(err) != 2 {
		t.Fatalf("delete builtin readonly = %v", err)
	}

	cmd.stdin = strings.NewReader("effort = \"low\"\n")
	if _, _, err := runProfile(cmd, "set", "readonly"); err != nil {
		t.Fatal(err)
	}
	if stdout, _, err := runProfile(cmd, "show", "readonly"); err != nil || stdout != "effort = \"low\"\n" {
		t.Fatalf("show user readonly = %q, %v", stdout, err)
	}
	stdout, _, err = runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	rows = profileListRows(t, stdout)
	if len(rows) != 1 || rows["readonly"][1] != "user" {
		t.Fatalf("list with a user readonly = %q", stdout)
	}

	// Deleting the user file reveals the builtin again.
	if _, _, err := runProfile(cmd, "delete", "readonly", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "readonly.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete left readonly.toml: %v", err)
	}
	if stdout, _, err := runProfile(cmd, "show", "readonly"); err != nil || stdout != builtin {
		t.Fatalf("show after delete = %q, %v", stdout, err)
	}
}

func TestProfileListShowsAHandPlacedInvalidFileWithoutBlockingOthers(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "roles = [\"review\"]\n")
	if _, _, err := runProfile(cmd, "set", "good"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("model = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	rows := profileListRows(t, stdout)
	if len(rows) != 3 {
		t.Fatalf("list = %q; want bad, good, and readonly", stdout)
	}
	if bad := strings.Join(rows["bad"], " "); !strings.HasPrefix(bad, "bad user - - - - - sha256:") || !strings.HasSuffix(bad, "no ("+profile.ReasonValueInvalid+")") {
		t.Fatalf("bad row = %q", bad)
	}
	if good := rows["good"]; good[1] != "user" || good[6] != "review" || good[8] != "yes" {
		t.Fatalf("good row = %q", good)
	}
	// The invalid file is still shown byte for byte.
	if stdout, _, err := runProfile(cmd, "show", "bad"); err != nil || stdout != "model = 5\n" {
		t.Fatalf("show bad = %q, %v", stdout, err)
	}
}

func TestProfileCommandRefusesUnknownVerbsMissingNamesAndDeleteWithoutYes(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "effort = \"low\"\n")
	for _, args := range [][]string{nil, {"edit", "x"}, {"list", "extra"}, {"show"}, {"set", "a", "b", "c"}, {"delete"}} {
		if _, _, err := runProfile(cmd, args...); exitCodeOf(err) != 2 {
			t.Fatalf("profile %q = %v, want exit 2", args, err)
		}
	}
	if _, _, err := runProfile(cmd, "show", "absent"); profile.ReasonOf(err) != profile.ReasonNotFound || exitCodeOf(err) != 1 {
		t.Fatalf("show absent = %v, want %s with exit 1", err, profile.ReasonNotFound)
	}
	if _, _, err := runProfile(cmd, "set", "kept"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runProfile(cmd, "delete", "kept"); err == nil || !strings.Contains(err.Error(), "--yes") || exitCodeOf(err) != 2 {
		t.Fatalf("delete without --yes = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.toml")); err != nil {
		t.Fatalf("delete without --yes removed the file: %v", err)
	}
}

// TestProfileModelAndEffortVocabularyMatchesClaudeLaunchOptions holds the
// profile package's copies of the `model` shape and `effort` set equal to the
// --model/--effort checks create uses, which that package cannot import.
func TestProfileModelAndEffortVocabularyMatchesClaudeLaunchOptions(t *testing.T) {
	t.Parallel()
	if !slices.Equal(profile.EffortLevels, claudeEffortLevels) {
		t.Fatalf("profile.EffortLevels = %v, claudeEffortLevels = %v", profile.EffortLevels, claudeEffortLevels)
	}
	if profile.ModelName.String() != claudeModelName.String() {
		t.Fatalf("profile.ModelName = %s, claudeModelName = %s", profile.ModelName, claudeModelName)
	}
}
