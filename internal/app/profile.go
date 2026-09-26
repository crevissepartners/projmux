package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crevissepartners/projmux/internal/core/profile"
)

// profileCommand implements `projmux profile list|show|set|delete`.
//
// A profile is a file, not a Registry resource, so like `instructions` it is a
// noun group of its own. Every path, name rule, size limit, validation, and
// write goes through the profile package; this command only parses argv and
// prints. Nothing here applies a profile to an Agent.
type profileCommand struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	stdin     io.Reader
}

func newProfileCommand() *profileCommand {
	return &profileCommand{homeDir: os.UserHomeDir, lookupEnv: os.Getenv, stdin: os.Stdin}
}

func (c *profileCommand) store() (profile.Store, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return profile.Store{}, err
	}
	return profile.NewDefaultStore(paths), nil
}

// Run dispatches `projmux profile <verb>`.
func (c *profileCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printRouteUsage(stderr, "profile")
		return usageError("profile requires a subcommand")
	}
	rest := args[1:]
	switch args[0] {
	case "list":
		return c.runList(rest, stdout, stderr)
	case "show":
		return c.runShow(rest, stdout, stderr)
	case "set":
		return c.runSet(rest, stdout, stderr)
	case "delete":
		return c.runDelete(rest, stdout, stderr)
	// Only `profile help <more tokens>` gets here: the help boundary answers `profile help`, `--help`, and `-h` first.
	case "help":
		return printRouteHelp(stdout, "profile")
	default:
		printRouteUsage(stderr, "profile")
		return usageError("unknown profile subcommand: " + args[0])
	}
}

// profileRefusal maps a profile refusal onto the CLI's exit codes. A missing
// profile keeps its text and exits 1, as a missing persona does; every other
// refusal -- a bad or reserved name, oversized or invalid content, a claimed
// role, a builtin delete -- is invalid input and exits 2. The reason token is
// in the text either way.
func profileRefusal(spelling string, err error) error {
	switch profile.ReasonOf(err) {
	case "", profile.ReasonNotFound:
		return fmt.Errorf("%s: %w", spelling, err)
	default:
		return usageError(spelling + ": " + err.Error())
	}
}

func (c *profileCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 0 {
		printRouteUsage(stderr, "profile list")
		return usageError("profile list does not accept positional arguments")
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
	fmt.Fprintln(tw, "NAME\tSOURCE\tPROVIDER\tINSTRUCTIONS\tMODEL\tEFFORT\tROLES\tDIGEST\tVALID")
	// An item the profile does not name -- a provider-neutral profile's
	// provider included -- and every item of a file that does not parse is
	// "-". An invalid profile that parses still shows what it names.
	cell := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	for _, entry := range entries {
		valid := "yes"
		if !entry.Valid {
			valid = "no (" + entry.Reason + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Source,
			cell(entry.Provider), cell(entry.Instructions), cell(entry.Model), cell(entry.Effort),
			cell(strings.Join(entry.Roles, ",")), cell(entry.Digest), valid)
	}
	return tw.Flush()
}

func (c *profileCommand) runShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		printRouteUsage(stderr, "profile show")
		return usageError("profile show requires exactly one <name>")
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	loaded, err := store.Load(operands[0])
	if err != nil {
		return profileRefusal("profile show", err)
	}
	// The bytes exactly as stored: no trailing newline is added or removed.
	_, err = stdout.Write(loaded.Content)
	return err
}

func (c *profileCommand) runSet(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "read the profile from this file; - reads stdin")
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
		printRouteUsage(stderr, "profile set")
		return usageError("profile set requires exactly one <name> and at most one of --file <path> or -")
	}
	name := operands[0]
	// The name is checked before any input is read, so a bad name never
	// waits on stdin.
	if err := profile.ValidateName(name); err != nil {
		return profileRefusal("profile set", err)
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
			return fmt.Errorf("profile set: %w", err)
		}
		defer opened.Close()
		source = opened
	}
	content, err := profile.ReadLimited(source)
	if err != nil {
		var refusal *profile.Error
		if errors.As(err, &refusal) {
			refusal.Name = name
		}
		return profileRefusal("profile set", err)
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	entry, err := store.Write(name, content)
	if err != nil {
		return profileRefusal("profile set", err)
	}
	_, err = fmt.Fprintf(stdout, "set profile %s %s (%d bytes) at %s\n", entry.Name, entry.Digest, len(content), entry.Path)
	return err
}

func (c *profileCommand) runDelete(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "confirm the deletion")
	operands, err := parsePersonaArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		printRouteUsage(stderr, "profile delete")
		return usageError("profile delete requires exactly one <name>")
	}
	name := operands[0]
	if err := profile.ValidateName(name); err != nil {
		return profileRefusal("profile delete", err)
	}
	if !*yes {
		return usageError(fmt.Sprintf("profile delete %s requires --yes; nothing was deleted", name))
	}
	store, err := c.store()
	if err != nil {
		return err
	}
	if err := store.Delete(name); err != nil {
		return profileRefusal("profile delete", err)
	}
	_, err = fmt.Fprintf(stdout, "deleted profile %s\n", name)
	return err
}
