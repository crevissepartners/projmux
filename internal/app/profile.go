package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/crevissepartners/projmux/internal/app/hookcmd"
	"github.com/crevissepartners/projmux/internal/app/personacmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/services/profiles"
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

// newPersonaCommand builds `projmux <noun>`, instructions or its deprecated
// persona spelling, over the config paths homeDir and lookupEnv resolve. Its
// `edit` opens the editor lookupEnv names ($EDITOR, then $VISUAL) through
// editorRunner.
func newPersonaCommand(noun string, homeDir func() (string, error), lookupEnv func(string) string, stdin io.Reader, editorRunner personacmd.EditorRunner) *personacmd.Command {
	return personacmd.New(noun,
		func() (config.Paths, error) { return configPaths(homeDir, lookupEnv) },
		func() string { return hookcmd.EditorFromEnv(lookupEnv) },
		editorRunner, stdin)
}

func (c *profileCommand) service() profiles.Service {
	return profiles.Service{
		Paths:         func() (config.Paths, error) { return savedSettingsPaths(c.homeDir, c.lookupEnv) },
		RegistryPaths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) },
	}
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
	case "parse", "preview":
		return c.runInspect(args[0], rest, stdout, stderr)
	case "set":
		return c.runSet(rest, stdout, stderr)
	case "delete":
		return c.runDelete(rest, stdout, stderr)
	default:
		printRouteUsage(stderr, "profile")
		return usageError("unknown profile subcommand: " + args[0])
	}
}

// profileRefusal maps a profile refusal onto the CLI's exit codes. A missing
// profile keeps its text and exits 1, as a missing persona does; every other
// refusal -- a bad or reserved name, oversized or invalid content, a claimed
// role -- is invalid input and exits 2. The reason token is
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
	setRouteUsage(fs)
	projectRef := fs.String("project", "", "list global profiles and profiles scoped to this Project")
	fs.StringVar(projectRef, "p", "", "alias of --project")
	operands, err := personacmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 0 {
		printRouteUsage(stderr, "profile list")
		return usageError("profile list does not accept positional arguments")
	}
	entries, err := c.service().List(context.Background(), *projectRef)
	if err != nil {
		var project *profiles.ProjectError
		if errors.As(err, &project) {
			return MapMetadataError(project.Err)
		}
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tPROVIDER\tINSTRUCTIONS\tMODEL\tEFFORT\tROLES\tDIGEST\tVALID\tSCOPE")
	for _, entry := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Source, entry.Provider, entry.Instructions, entry.Model, entry.Effort, entry.Roles, entry.Digest, entry.Valid, entry.Scope)
	}
	return tw.Flush()
}

func (c *profileCommand) runShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	operands, err := personacmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		printRouteUsage(stderr, "profile show")
		return usageError("profile show requires exactly one <name>")
	}
	loaded, err := c.service().Read(context.Background(), operands[0])
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
	setRouteUsage(fs)
	file := fs.String("file", "", "read the profile from this file; - reads stdin")
	operands, err := personacmd.ParseArgs(fs, args)
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
		opened, err := personacmd.OpenFileUnderParent(*file)
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
	entry, err := c.service().Write(context.Background(), name, content)
	if err != nil {
		return profileRefusal("profile set", err)
	}
	_, err = fmt.Fprintf(stdout, "set profile %s %s (%d bytes) at %s\n", entry.Name, entry.Digest, len(content), entry.Path)
	return err
}

func (c *profileCommand) runDelete(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	yes := fs.Bool("yes", false, "confirm the deletion")
	operands, err := personacmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		printRouteUsage(stderr, "profile delete")
		return usageError("profile delete requires exactly one <name>")
	}
	name := operands[0]
	if err := c.service().Delete(context.Background(), name, *yes); err != nil {
		var confirmation *profiles.ConfirmationError
		if errors.As(err, &confirmation) {
			return usageError(err.Error())
		}
		return profileRefusal("profile delete", err)
	}
	_, err = fmt.Fprintf(stdout, "deleted profile %s\n", name)
	return err
}

// runInspect keeps input bounded and checks a preview name before reading stdin.
func (c *profileCommand) runInspect(verb string, args []string, stdout, stderr io.Writer) error {
	spelling := "profile " + verb
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	file := fs.String("file", "", "read the profile from this file; - reads stdin")
	operands, err := personacmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	want := 0
	if verb == "preview" {
		want = 1
	}
	if len(operands) != want {
		printRouteUsage(stderr, spelling)
		return usageError(spelling + " requires " + map[string]string{"parse": "no positional arguments", "preview": "exactly one <name>"}[verb])
	}
	name := ""
	if verb == "preview" {
		name = operands[0]
		if err := profile.ValidateName(name); err != nil {
			return profileRefusal(spelling, err)
		}
	}
	source := c.stdin
	if source == nil {
		source = os.Stdin
	}
	if *file != "" && *file != "-" {
		opened, err := personacmd.OpenFileUnderParent(*file)
		if err != nil {
			return fmt.Errorf("%s: %w", spelling, err)
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
		return profileRefusal(spelling, err)
	}
	var result any
	if verb == "parse" {
		result, err = c.service().Parse(context.Background(), content)
	} else {
		result, err = c.service().Preview(context.Background(), name, content)
	}
	if err != nil {
		return profileRefusal(spelling, err)
	}
	return json.NewEncoder(stdout).Encode(result)
}
