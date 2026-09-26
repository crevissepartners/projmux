package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
)

type pinCommand struct {
	authority pinAuthority
	storeErr  error
	// registry projects the display root and name of a managed pin. Listing a pin
	// reads the Registry rather than remembering a path, which is what makes the
	// list survive a rebind.
	registry func() (coremetadata.Registry, error)
}

func newPinCommand() *pinCommand {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return &pinCommand{
			storeErr: fmt.Errorf("resolve default config paths: %w", err),
			registry: loadResourceRegistry,
		}
	}

	return &pinCommand{
		authority: newPinAuthority(pins.NewDefaultStore(paths)),
		registry:  loadResourceRegistry,
	}
}

// Run manages the configured pin subcommands.
func (c *pinCommand) Run(args []string, stdout, stderr io.Writer) error {
	return c.runLevel("pin", args, stdout, stderr)
}

// runLevel parses one pin dispatch level. route is the spelling being parsed:
// `pin` for the top level, and `pin project` once the canonical kind token is
// consumed, so a flag error there prints `Usage of pin project:`. The route
// gate only lets `pin project …` through, so every public flag error lands on
// the `pin project` level.
func (c *pinCommand) runLevel(route string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(route, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() == 0 {
		printPinHelp(stderr, route)
		return usageError(route + " requires a subcommand")
	}

	switch fs.Arg(0) {
	// `pin project <action>` is the canonical spelling: it names the resource
	// kind the pin list has always held. It forwards the remaining argv to the
	// same actions, so both spellings share one implementation and one output.
	case "project":
		rest := fs.Args()[1:]
		if len(rest) > 0 && rest[0] == "project" {
			printRouteUsage(stderr, "pin project")
			printPinNotes(stderr)
			return usageError(fmt.Sprintf("unknown pin project subcommand: %s", rest[0]))
		}
		return c.runLevel("pin project", rest, stdout, stderr)
	case "list":
		return c.runList(fs.Args()[1:], stdout, stderr)
	case "add":
		return c.runAdd(fs.Args()[1:], stdout, stderr)
	case "remove":
		return c.runRemove(fs.Args()[1:], stdout, stderr)
	case "toggle":
		return c.runToggle(fs.Args()[1:], stdout, stderr)
	case "clear":
		return c.runClear(fs.Args()[1:], stdout, stderr)
	case "migrate":
		return c.runMigrate(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		if route == "pin project" {
			return printRouteHelp(stdout, "pin project")
		}
		return printRouteHelp(stdout, "pin")
	default:
		printPinHelp(stderr, route)
		return usageError(fmt.Sprintf("unknown %s subcommand: %s", route, fs.Arg(0)))
	}
}

// runList prints the two pin collections as typed rows.
//
// The kind is the first column because it is the fact the old one-path-per-line
// output could not state: a managed pin is a Registry uid whose root is projected
// on every read, and a candidate pin is a path that no Project claims. Workdirs
// are neither and are not listed here -- they are the scan roots, owned by
// `projmux settings` and PROJMUX_MANAGED_ROOTS.
func (c *pinCommand) runList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pin project list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	kind := fs.String("kind", "", "Limit the listing to one pin kind (project or candidate)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		printRouteUsage(stderr, "pin project list")
		printPinNotes(stderr)
		return usageError("pin project list does not accept positional arguments")
	}
	filter, err := parsePinKindFilter(*kind)
	if err != nil {
		printRouteUsage(stderr, "pin project list")
		printPinNotes(stderr)
		return err
	}

	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	resolution, err := authority.resolved()
	if err != nil {
		return fmt.Errorf("list pins: %w", err)
	}
	reportPinResolution(stderr, authority.store.Path(), resolution)

	registry := c.readRegistry()
	for _, pin := range resolution.Set.Pins {
		if filter != "" && pin.Kind != filter {
			continue
		}
		if _, err := fmt.Fprintln(stdout, pinListLine(registry, pin)); err != nil {
			return err
		}
	}
	return nil
}

func parsePinKindFilter(value string) (pins.Kind, error) {
	switch strings.TrimSpace(value) {
	case "":
		return "", nil
	case string(pins.KindProject):
		return pins.KindProject, nil
	case string(pins.KindCandidate):
		return pins.KindCandidate, nil
	default:
		return "", usageError(fmt.Sprintf("unknown pin kind %q: use %s or %s", value, pins.KindProject, pins.KindCandidate))
	}
}

// pinListLine renders one typed pin as tab-separated columns: kind, canonical
// reference, projected detail.
func pinListLine(registry coremetadata.Registry, pin pins.Pin) string {
	if pin.Kind != pins.KindProject {
		return string(pin.Kind) + "\t" + pin.Value
	}
	reference := "uid:" + pin.Value
	project, ok := registry.Project(pin.Value)
	if !ok {
		// A managed pin whose Project left the Registry is stated as such rather
		// than dropped: the pin file is the operator's, and a read does not edit
		// it. `pin project remove uid:<uid>` is the action.
		return string(pin.Kind) + "\t" + reference + "\t(no Registry Project)"
	}
	detail := strings.TrimSpace(project.Spec.Root)
	if detail == "" {
		detail = "(no root)"
	}
	return string(pin.Kind) + "\t" + reference + "\t" + detail + "\t" + project.Metadata.Name
}

// reportPinResolution states, on stderr, the two things a projected read knows and
// the stdout rows cannot say: that the file is still legacy, and which of its
// paths no single Project claims.
func reportPinResolution(stderr io.Writer, path string, resolution pins.Resolution) {
	if stderr == nil {
		return
	}
	for _, ambiguity := range resolution.Ambiguous {
		fmt.Fprintf(stderr, "pin %s matches %d Projects (%s); it stays a candidate pin until one claims it\n",
			ambiguity.Path, len(ambiguity.UIDs), strings.Join(ambiguity.UIDs, ", "))
	}
	if !resolution.From.Typed() {
		fmt.Fprintf(stderr, "pin file %s still holds legacy path lines; run `projmux pin project migrate` to store the typed form\n", path)
	}
}

func (c *pinCommand) runAdd(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		printRouteUsage(stderr, "pin project add")
		printPinNotes(stderr)
		return pinArgCountError("pin project add")
	}
	target := pinTargetArg(args[0])
	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	pin, err := authority.pinTargetForSelector(target)
	if err != nil {
		return err
	}
	if err := authority.add(pin); err != nil {
		return fmt.Errorf("add pin: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "pinned: %s\n", pin)
	return err
}

func (c *pinCommand) runRemove(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		printRouteUsage(stderr, "pin project remove")
		printPinNotes(stderr)
		return pinArgCountError("pin project remove")
	}
	target := pinTargetArg(args[0])
	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	pin, err := authority.pinTargetForSelector(target)
	if err != nil {
		return err
	}
	if err := authority.remove(pin); err != nil {
		return fmt.Errorf("remove pin: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "unpinned: %s\n", pin)
	return err
}

func (c *pinCommand) runToggle(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		printRouteUsage(stderr, "pin project toggle")
		printPinNotes(stderr)
		return pinArgCountError("pin project toggle")
	}
	target := pinTargetArg(args[0])
	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	pin, err := authority.pinTargetForSelector(target)
	if err != nil {
		return err
	}

	pinned, err := authority.toggle(pin)
	if err != nil {
		return fmt.Errorf("toggle pin: %w", err)
	}

	if pinned {
		_, err = fmt.Fprintf(stdout, "pinned: %s\n", pin)
		return err
	}

	_, err = fmt.Fprintf(stdout, "unpinned: %s\n", pin)
	return err
}

func (c *pinCommand) runClear(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pin project clear", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		printRouteUsage(stderr, "pin project clear")
		printPinNotes(stderr)
		return usageError("pin project clear does not accept positional arguments")
	}

	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	if err := authority.clear(); err != nil {
		return fmt.Errorf("clear pins: %w", err)
	}

	_, err = fmt.Fprintln(stdout, "cleared pins")
	return err
}

// runMigrate stores the typed form of a legacy pin file.
//
// It is the one route that rewrites the file's shape, and it is atomic in the only
// sense that matters here: either every legacy line is typed, or the file keeps
// the bytes it had. A path that no Project claims stays a candidate pin, so
// nothing is lost by migrating early; a path that two Projects claim refuses the
// whole migration rather than picking a uid.
func (c *pinCommand) runMigrate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pin project migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	dryRun := fs.Bool("dry-run", false, "Report the migration without writing the pin file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		printRouteUsage(stderr, "pin project migrate")
		printPinNotes(stderr)
		return usageError("pin project migrate does not accept positional arguments")
	}

	authority, err := c.requireAuthority()
	if err != nil {
		return err
	}
	var resolution pins.Resolution
	if *dryRun {
		resolution, err = authority.planMigration()
	} else {
		resolution, err = authority.migrate()
	}
	if err != nil {
		return err
	}

	prefix := "migrated"
	if *dryRun {
		prefix = "would migrate"
	}
	for _, move := range resolution.Moved {
		if _, err := fmt.Fprintf(stdout, "%s: %s -> uid:%s\n", prefix, move.Path, move.UID); err != nil {
			return err
		}
	}
	for _, kept := range resolution.Kept {
		if _, err := fmt.Fprintf(stdout, "candidate: %s (no Registry Project claims it)\n", kept); err != nil {
			return err
		}
	}
	if len(resolution.Moved) == 0 && len(resolution.Kept) == 0 {
		_, err = fmt.Fprintln(stdout, "pin file is already typed; nothing to migrate")
		return err
	}
	return nil
}

func (c *pinCommand) requireAuthority() (pinAuthority, error) {
	if c.storeErr != nil {
		return pinAuthority{}, fmt.Errorf("configure pin store: %w", c.storeErr)
	}
	if c.authority.store.Path() == "" {
		return pinAuthority{}, fmt.Errorf("configure pin store: %w", errNoPinStore)
	}
	return c.authority, nil
}

func (c *pinCommand) readRegistry() coremetadata.Registry {
	if c.registry == nil {
		return coremetadata.Registry{}
	}
	registry, err := c.registry()
	if err != nil {
		return coremetadata.Registry{}
	}
	return registry
}

// pinArgCountError is the usage error (exit 2) of a single-operand pin verb
// given no operand or more than one; the caller has printed its own usage.
// These verbs parse no flags: every argv token, a leading dash included, is an
// operand, so `pin project add -foo` pins the path `-foo`.
func pinArgCountError(route string) error {
	return usageError(fmt.Sprintf("%s requires exactly 1 <dir|uid:uid> argument", route))
}

// pinTargetArg accepts either a directory or an explicit `uid:<uid>`.
//
// A bare directory keeps working exactly as it always has, which is the
// compatibility half of the split: the argv an operator already types still
// resolves, it just now resolves to a *typed* pin instead of a bare path line.
func pinTargetArg(arg string) string {
	if strings.HasPrefix(strings.TrimSpace(arg), "uid:") {
		return strings.TrimSpace(arg)
	}
	return filepath.Clean(arg)
}

// printPinHelp prints the catalog usage of route, the pin dispatch level that
// rejected the call (`pin` or `pin project`), then the pin kinds a synopsis
// cannot state.
func printPinHelp(w io.Writer, route string) {
	if route == "pin project" {
		printRouteUsage(w, "pin project")
	} else {
		printRouteUsage(w, "pin")
	}
	printPinNotes(w)
}

// printPinNotes prints the catalog notes of `pin project`, the pin kinds and
// the workdir boundary, under a pin usage block.
func printPinNotes(w io.Writer) {
	printRouteNotes(w, "pin project")
}
