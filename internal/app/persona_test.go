package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/persona"
)

// newPersonaTestCommand roots one persona command at an isolated HOME. It
// returns the command and the persona directory that HOME resolves to.
func newPersonaTestCommand(t *testing.T, env map[string]string, stdin string) (*personaCommand, string) {
	t.Helper()
	home := t.TempDir()
	cmd := &personaCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(name string) string { return env[name] },
		stdin:     strings.NewReader(stdin),
		editorRunner: func(string, []string, io.Writer, io.Writer) error {
			return errors.New("editor runner should not be called")
		},
	}
	return cmd, filepath.Join(home, ".config", "projmux", "personas")
}

func runPersona(cmd *personaCommand, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := cmd.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestPersonaSetListShowRoundTripIsByteIdentical is C-3 acceptance 1: after
// `persona set reviewer --file x.md`, list names it and show prints x.md's
// exact bytes, from <ConfigDir>/personas/reviewer.md at 0600.
func TestPersonaSetListShowRoundTripIsByteIdentical(t *testing.T) {
	t.Parallel()
	cmd, dir := newPersonaTestCommand(t, nil, "")
	content := []byte("# Reviewer\n\nNo trailing newline, tabs\tand bytes \xe2\x80\x94 kept")
	source := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runPersona(cmd, "set", "reviewer", "--file", source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "reviewer.md")
	if !strings.Contains(stdout, persona.Digest(content)) || !strings.Contains(stdout, path) {
		t.Fatalf("set stdout = %q", stdout)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("persona file mode = %o", info.Mode().Perm())
	}

	stdout, _, err = runPersona(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "reviewer ") ||
		!strings.Contains(lines[1], persona.Digest(content)) {
		t.Fatalf("list stdout = %q", stdout)
	}

	stdout, _, err = runPersona(cmd, "show", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(content) {
		t.Fatalf("show = %q, want %q", stdout, content)
	}
}

func TestPersonaSetReadsStdinForDashAndOmittedFile(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"dash operand": {"set", "reviewer", "-"},
		"file dash":    {"set", "reviewer", "--file", "-"},
		"flag first":   {"set", "--file", "-", "reviewer"},
		"omitted":      {"set", "reviewer"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd, _ := newPersonaTestCommand(t, nil, "from stdin\n")
			if _, _, err := runPersona(cmd, args...); err != nil {
				t.Fatal(err)
			}
			stdout, _, err := runPersona(cmd, "show", "reviewer")
			if err != nil || stdout != "from stdin\n" {
				t.Fatalf("show = %q, %v", stdout, err)
			}
		})
	}
}

// TestPersonaCLIRefusalsCarryTheTokenAndWriteNothing is C-3 acceptance 2 on
// the CLI surface.
func TestPersonaCLIRefusalsCarryTheTokenAndWriteNothing(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("o", persona.MaxSize+1)
	for _, test := range []struct {
		name   string
		args   []string
		stdin  string
		reason string
		usage  bool
	}{
		{"traversal", []string{"set", "../x"}, "x", persona.ReasonNameInvalid, true},
		{"hidden", []string{"set", ".hidden"}, "x", persona.ReasonNameInvalid, true},
		{"flag-like", []string{"set", "-x"}, "x", persona.ReasonNameInvalid, true},
		{"too long name", []string{"set", strings.Repeat("n", 129)}, "x", persona.ReasonNameInvalid, true},
		{"too large", []string{"set", "big"}, oversized, persona.ReasonTooLarge, true},
		{"show missing", []string{"show", "absent"}, "", persona.ReasonNotFound, false},
		{"show invalid", []string{"show", "../x"}, "", persona.ReasonNameInvalid, true},
		{"delete missing", []string{"delete", "absent", "--yes"}, "", persona.ReasonNotFound, false},
		{"edit invalid", []string{"edit", ".x"}, "", persona.ReasonNameInvalid, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd, dir := newPersonaTestCommand(t, map[string]string{"EDITOR": "true"}, test.stdin)
			_, _, err := runPersona(cmd, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("err = %v, want %s", err, test.reason)
			}
			if IsUsageError(err) != test.usage {
				t.Fatalf("usage error = %v, want %v (%v)", IsUsageError(err), test.usage, err)
			}
			if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a refused command created %s: %v", dir, statErr)
			}
		})
	}
}

func TestPersonaDeleteRequiresYes(t *testing.T) {
	t.Parallel()
	cmd, dir := newPersonaTestCommand(t, nil, "keep")
	if _, _, err := runPersona(cmd, "set", "reviewer"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runPersona(cmd, "delete", "reviewer")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete without --yes = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); err != nil {
		t.Fatalf("delete without --yes removed the file: %v", err)
	}
	if _, _, err := runPersona(cmd, "delete", "reviewer", "--yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete --yes left the file: %v", err)
	}
}

// editWith returns an editor runner that replaces the edited file with
// content and records the path it was given.
func editWith(content []byte, seen *string) func(string, []string, io.Writer, io.Writer) error {
	return func(command string, args []string, _, _ io.Writer) error {
		path := args[len(args)-1]
		*seen = path
		return os.WriteFile(path, content, 0o600)
	}
}

func TestPersonaEditCreatesThroughATempCopyAndWritesAtomically(t *testing.T) {
	t.Parallel()
	cmd, dir := newPersonaTestCommand(t, map[string]string{"VISUAL": "vi -n"}, "")
	var seen string
	var gotCommand string
	var gotArgs []string
	cmd.editorRunner = func(command string, args []string, stdout, stderr io.Writer) error {
		gotCommand, gotArgs = command, args
		return editWith([]byte("new persona\n"), &seen)(command, args, stdout, stderr)
	}

	stdout, _, err := runPersona(cmd, "edit", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if gotCommand != "vi" || len(gotArgs) != 2 || gotArgs[0] != "-n" {
		t.Fatalf("editor = %q %q, want $VISUAL when $EDITOR is unset", gotCommand, gotArgs)
	}
	path := filepath.Join(dir, "reviewer.md")
	if seen == path || strings.HasPrefix(seen, dir) {
		t.Fatalf("the editor was given the persona file itself: %s", seen)
	}
	if _, err := os.Stat(filepath.Dir(seen)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary copy was not removed: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != "new persona\n" {
		t.Fatalf("persona = %q, %v", written, err)
	}
	if !strings.Contains(stdout, "edited persona reviewer") {
		t.Fatalf("stdout = %q", stdout)
	}

	// $EDITOR wins over $VISUAL, and the existing content is what it opens.
	cmd.lookupEnv = func(name string) string {
		return map[string]string{"EDITOR": "nano", "VISUAL": "vi"}[name]
	}
	var opened []byte
	cmd.editorRunner = func(command string, args []string, _, _ io.Writer) error {
		gotCommand = command
		opened, _ = os.ReadFile(args[len(args)-1])
		return nil
	}
	stdout, _, err = runPersona(cmd, "edit", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if gotCommand != "nano" || string(opened) != "new persona\n" || !strings.Contains(stdout, "unchanged") {
		t.Fatalf("editor %q opened %q; stdout %q", gotCommand, opened, stdout)
	}
}

func TestPersonaEditRefusesOversizedResultAndKeepsTheStoredPersona(t *testing.T) {
	t.Parallel()
	cmd, dir := newPersonaTestCommand(t, map[string]string{"EDITOR": "ed"}, "original")
	if _, _, err := runPersona(cmd, "set", "reviewer"); err != nil {
		t.Fatal(err)
	}
	var seen string
	cmd.editorRunner = editWith(bytes.Repeat([]byte("x"), persona.MaxSize+1), &seen)
	_, _, err := runPersona(cmd, "edit", "reviewer")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), persona.ReasonTooLarge) {
		t.Fatalf("oversized edit = %v", err)
	}
	stored, readErr := os.ReadFile(filepath.Join(dir, "reviewer.md"))
	if readErr != nil || string(stored) != "original" {
		t.Fatalf("stored persona = %q, %v", stored, readErr)
	}
	// The edit is not thrown away: the error names the kept copy.
	if !strings.Contains(err.Error(), seen) {
		t.Fatalf("error %q does not name the kept copy %s", err, seen)
	}
	_ = os.RemoveAll(filepath.Dir(seen))
}

func TestPersonaEditWithoutAnEditorOrWithAnEmptyNewPersonaWritesNothing(t *testing.T) {
	t.Parallel()
	cmd, dir := newPersonaTestCommand(t, nil, "")
	if _, _, err := runPersona(cmd, "edit", "reviewer"); err == nil || !strings.Contains(err.Error(), "$EDITOR") {
		t.Fatalf("edit without an editor = %v", err)
	}
	cmd.lookupEnv = func(name string) string { return map[string]string{"EDITOR": "true"}[name] }
	cmd.editorRunner = func(string, []string, io.Writer, io.Writer) error { return nil }
	stdout, _, err := runPersona(cmd, "edit", "reviewer")
	if err != nil || !strings.Contains(stdout, "not created") {
		t.Fatalf("empty new persona = %q, %v", stdout, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty new persona was written: %v", err)
	}
	cmd.editorRunner = func(string, []string, io.Writer, io.Writer) error { return errors.New("exit status 1") }
	if _, _, err := runPersona(cmd, "edit", "reviewer"); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("failed editor = %v", err)
	}
}

func TestPersonaCommandRequiresAKnownSubcommand(t *testing.T) {
	t.Parallel()
	cmd, _ := newPersonaTestCommand(t, nil, "")
	for _, args := range [][]string{nil, {"rename"}, {"list", "extra"}, {"show"}, {"set", "a", "b", "c"}} {
		if _, _, err := runPersona(cmd, args...); err == nil || !IsUsageError(err) {
			t.Fatalf("persona %q = %v, want a usage error", args, err)
		}
	}
	stdout, _, err := runPersona(cmd, "list")
	if err != nil || strings.TrimSpace(stdout) != "NAME  DIGEST  SIZE  MODIFIED" {
		t.Fatalf("empty list = %q, %v", stdout, err)
	}
}
