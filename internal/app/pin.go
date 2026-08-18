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
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printPinUsage(stderr)
		return errors.New("pin requires a subcommand")
	}

	switch fs.Arg(0) {
	// `pin project <action>` is the canonical spelling: it names the resource
	// kind the pin list has always held. It forwards the remaining argv to the
	// same actions, so both spellings share one implementation and one output.
	case "project":
		rest := fs.Args()[1:]
		if len(rest) > 0 && rest[0] == "project" {
			printPinUsage(stderr)
			return fmt.Errorf("unknown pin project subcommand: %s", rest[0])
		}
		return c.Run(rest, stdout, stderr)
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
		printPinUsage(stdout)
		return nil
	default:
		printPinUsage(stderr)
		return fmt.Errorf("unknown pin subcommand: %s", fs.Arg(0))
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
	fs := flag.NewFlagSet("pin list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kind := fs.String("kind", "", "Limit the listing to one pin kind (project or candidate)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printPinUsage(stderr)
		return fmt.Errorf("pin list does not accept positional arguments")
	}
	filter, err := parsePinKindFilter(*kind)
	if err != nil {
		printPinUsage(stderr)
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
		return "", fmt.Errorf("unknown pin kind %q: use %s or %s", value, pins.KindProject, pins.KindCandidate)
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
	target, err := requireSinglePinArg("pin add", args, stderr)
	if err != nil {
		return err
	}
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
	target, err := requireSinglePinArg("pin remove", args, stderr)
	if err != nil {
		return err
	}
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
	target, err := requireSinglePinArg("pin toggle", args, stderr)
	if err != nil {
		return err
	}
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
	if len(args) != 0 {
		printPinUsage(stderr)
		return fmt.Errorf("pin clear does not accept positional arguments")
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
	fs := flag.NewFlagSet("pin migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "Report the migration without writing the pin file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printPinUsage(stderr)
		return fmt.Errorf("pin migrate does not accept positional arguments")
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

// requireSinglePinArg accepts either a directory or an explicit `uid:<uid>`.
//
// A bare directory keeps working exactly as it always has, which is the
// compatibility half of the split: the argv an operator already types still
// resolves, it just now resolves to a *typed* pin instead of a bare path line.
func requireSinglePinArg(command string, args []string, stderr io.Writer) (string, error) {
	if len(args) != 1 {
		printPinUsage(stderr)
		return "", fmt.Errorf("%s requires exactly 1 <dir|uid:uid> argument", command)
	}
	if strings.HasPrefix(strings.TrimSpace(args[0]), "uid:") {
		return strings.TrimSpace(args[0]), nil
	}
	return filepath.Clean(args[0]), nil
}

func printPinUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux pin project list [--kind project|candidate]")
	fmt.Fprintln(w, "  projmux pin project add <dir|uid:uid>")
	fmt.Fprintln(w, "  projmux pin project remove <dir|uid:uid>")
	fmt.Fprintln(w, "  projmux pin project toggle <dir|uid:uid>")
	fmt.Fprintln(w, "  projmux pin project clear")
	fmt.Fprintln(w, "  projmux pin project migrate [--dry-run]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Pins are presentation preferences in two kinds:")
	fmt.Fprintln(w, "  project    a Registry Project uid; its root and name are projected from the Registry")
	fmt.Fprintln(w, "  candidate  a filesystem path that no Registry Project claims")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Discovery roots (workdirs) are a separate collection; manage them in `projmux settings`.")
}
