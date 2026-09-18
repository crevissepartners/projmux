package app

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/crevissepartners/projmux/internal/core/persona"
)

// personaCommand implements `projmux persona list|show|edit|set|delete`.
//
// A persona is a file, not a Registry resource, so it has its own noun group
// like `hook` and `config` rather than the get/create/delete resource verbs.
// Every path, name rule, size limit, and write goes through the persona
// package; this command only parses argv and prints.
type personaCommand struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	stdin     io.Reader
	// editorRunner runs the $EDITOR command for `edit`. Tests stub it.
	editorRunner func(command string, args []string, stdout, stderr io.Writer) error
}

func newPersonaCommand() *personaCommand {
	return &personaCommand{
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		stdin:        os.Stdin,
		editorRunner: defaultEditorRunner,
	}
}

func (c *personaCommand) store() (persona.Store, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return persona.Store{}, err
	}
	return persona.NewDefaultStore(paths), nil
}

// Run dispatches `projmux persona <verb>`.
func (c *personaCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPersonaUsage(stderr)
		return usageError("persona requires a subcommand")
	}
	rest := args[1:]
	switch args[0] {
	case "list":
		return c.runList(rest, stdout, stderr)
	case "show":
		return c.runShow(rest, stdout, stderr)
	case "edit":
		return c.runEdit(rest, stdout, stderr)
	case "set":
		return c.runSet(rest, stdout, stderr)
	case "delete":
		return c.runDelete(rest, stdout, stderr)
	case "help", "--help", "-h":
		printPersonaUsage(stdout)
		return nil
	default:
		printPersonaUsage(stderr)
		return usageError("unknown persona subcommand: " + args[0])
	}
}

// parsePersonaArgs parses flags that may appear before or after the
// positional operands, and returns the operands. A lone "-" is an operand.
//
// A leading argument that is not one of this route's flags is always the name
// operand, even when it starts with "-": `persona show -x` is a persona name
// the name rule refuses with its reason token, not an unknown flag.
func parsePersonaArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var operands []string
	if len(args) > 0 && !personaFlagToken(fs, args[0]) {
		operands = append(operands, args[0])
		args = args[1:]
	}
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError(err.Error())
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return operands, nil
		}
		operands = append(operands, rest[0])
		args = rest[1:]
	}
}

func personaFlagToken(fs *flag.FlagSet, arg string) bool {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return false
	}
	name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
	return name == "h" || name == "help" || fs.Lookup(name) != nil
}

// personaRefusal maps a persona package refusal onto the CLI's exit codes: a
// bad name or oversized content is invalid input (exit 2). Every other error,
// including persona-not-found, keeps its text and exits 1. The reason token is
// in the text either way.
func personaRefusal(spelling string, err error) error {
	switch persona.ReasonOf(err) {
	case persona.ReasonNameInvalid, persona.ReasonTooLarge:
		return usageError(spelling + ": " + err.Error())
	default:
		return fmt.Errorf("%s: %w", spelling, err)
	}
}

func (c *personaCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("persona list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 0 {
		printPersonaUsage(stderr)
		return usageError("persona list does not accept positional arguments")
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	entries, err := store.List()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDIGEST\tSIZE\tMODIFIED")
	for _, entry := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", entry.Name, entry.Digest, entry.Size, entry.ModTime.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

func (c *personaCommand) runShow(args []string, stdout, stderr io.Writer) error {
	name, err := personaNameOperand("persona show", args, stderr)
	if err != nil {
		return err
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	loaded, err := store.Load(name)
	if err != nil {
		return personaRefusal("persona show", err)
	}
	// The bytes exactly as stored: no trailing newline is added or removed.
	_, err = stdout.Write(loaded.Content)
	return err
}

func personaNameOperand(spelling string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return "", err
	}
	if len(operands) != 1 {
		printPersonaUsage(stderr)
		return "", usageError(spelling + " requires exactly one <name>")
	}
	return operands[0], nil
}

func (c *personaCommand) runSet(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("persona set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "read the persona from this file; - reads stdin")
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	fromStdin := false
	switch {
	case len(operands) == 2 && operands[1] == "-" && *file == "":
		fromStdin = true
	case len(operands) == 1 && (*file == "" || *file == "-"):
		fromStdin = true
	case len(operands) == 1:
	default:
		printPersonaUsage(stderr)
		return usageError("persona set requires exactly one <name> and at most one of --file <path> or -")
	}
	name := operands[0]
	// The name is checked before any input is read, so a bad name never
	// waits on stdin.
	if err := persona.ValidateName(name); err != nil {
		return personaRefusal("persona set", err)
	}
	var source io.Reader
	if fromStdin {
		source = c.stdin
		if source == nil {
			source = os.Stdin
		}
	} else {
		opened, err := openFileUnderParent(*file)
		if err != nil {
			return fmt.Errorf("persona set: %w", err)
		}
		defer opened.Close()
		source = opened
	}
	content, err := persona.ReadLimited(source)
	if err != nil {
		var refusal *persona.Error
		if errors.As(err, &refusal) {
			refusal.Name = name
		}
		return personaRefusal("persona set", err)
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	entry, err := store.Write(name, content)
	if err != nil {
		return personaRefusal("persona set", err)
	}
	_, err = fmt.Fprintf(stdout, "set persona %s %s (%d bytes) at %s\n", entry.Name, entry.Digest, entry.Size, entry.Path)
	return err
}

func (c *personaCommand) runDelete(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("persona delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "confirm the deletion")
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		printPersonaUsage(stderr)
		return usageError("persona delete requires exactly one <name>")
	}
	name := operands[0]
	if err := persona.ValidateName(name); err != nil {
		return personaRefusal("persona delete", err)
	}
	if !*yes {
		return usageError(fmt.Sprintf("persona delete %s requires --yes; nothing was deleted", name))
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	if err := store.Delete(name); err != nil {
		return personaRefusal("persona delete", err)
	}
	_, err = fmt.Fprintf(stdout, "deleted persona %s\n", name)
	return err
}

// runEdit opens the persona in $EDITOR (then $VISUAL), creating it when it
// does not exist yet.
//
// The editor works on a private temporary copy, never on the persona file
// itself: whatever the editor does mid-session, the stored persona is either
// the old content or the new content written atomically by the persona store,
// and content over the size limit is refused rather than stored.
func (c *personaCommand) runEdit(args []string, stdout, stderr io.Writer) error {
	name, err := personaNameOperand("persona edit", args, stderr)
	if err != nil {
		return err
	}
	if err := persona.ValidateName(name); err != nil {
		return personaRefusal("persona edit", err)
	}
	editor := editorFromEnv(c.lookupEnv)
	if editor == "" {
		return errors.New("persona edit: $EDITOR and $VISUAL are unset; cannot open editor (use `projmux persona set` instead)")
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	var original []byte
	existed := true
	loaded, err := store.Load(name)
	switch {
	case err == nil:
		original = loaded.Content
	case persona.ReasonOf(err) == persona.ReasonNotFound:
		existed = false
	default:
		return personaRefusal("persona edit", err)
	}

	tempDir, err := os.MkdirTemp("", "projmux-persona-*")
	if err != nil {
		return fmt.Errorf("persona edit: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()
	tempPath := filepath.Join(tempDir, name+persona.FileExt)
	if err := os.WriteFile(tempPath, original, 0o600); err != nil {
		return fmt.Errorf("persona edit: %w", err)
	}
	parts := strings.Fields(editor)
	runner := c.editorRunner
	if runner == nil {
		runner = defaultEditorRunner
	}
	if err := runner(parts[0], append(parts[1:], tempPath), stdout, stderr); err != nil {
		return fmt.Errorf("persona edit: editor %q exited: %w; nothing was written", editor, err)
	}
	edited, err := openFileUnderParent(tempPath)
	if err != nil {
		return fmt.Errorf("persona edit: read the edited copy: %w; nothing was written", err)
	}
	content, err := persona.ReadLimited(edited)
	_ = edited.Close()
	if err != nil {
		var refusal *persona.Error
		if errors.As(err, &refusal) {
			refusal.Name = name
			// Keep the edit so the work is not lost; the stored persona
			// is unchanged.
			keepTemp = true
			return usageError(fmt.Sprintf("persona edit: %v; nothing was written, the edited copy is kept at %s", err, tempPath))
		}
		return fmt.Errorf("persona edit: read the edited copy: %w; nothing was written", err)
	}
	if existed && bytes.Equal(content, original) {
		_, err = fmt.Fprintf(stdout, "persona %s unchanged\n", name)
		return err
	}
	if !existed && len(content) == 0 {
		_, err = fmt.Fprintf(stdout, "persona %s not created: the editor saved nothing\n", name)
		return err
	}
	entry, err := store.Write(name, content)
	if err != nil {
		return personaRefusal("persona edit", err)
	}
	_, err = fmt.Fprintf(stdout, "edited persona %s %s (%d bytes) at %s\n", entry.Name, entry.Digest, entry.Size, entry.Path)
	return err
}

// openFileUnderParent opens one operator-named file through an os.Root on its
// parent directory, as internal/testutil/codexinstalled does, so the open is
// confined to that directory.
func openFileUnderParent(path string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(filepath.Base(path))
}

func printPersonaUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux persona list")
	fmt.Fprintln(w, "  projmux persona show <name>")
	fmt.Fprintln(w, "  projmux persona edit <name>")
	fmt.Fprintln(w, "  projmux persona set <name> [--file <path> | -]")
	fmt.Fprintln(w, "  projmux persona delete <name> --yes")
}
