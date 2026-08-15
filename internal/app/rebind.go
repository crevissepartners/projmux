package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// rebindKinds lists the resource kinds `rebind` implements.
var rebindKinds = []string{"project"}

// rebindCommand implements the canonical `rebind` verb.
//
// `rebind project <ref> --root <absolute-path>` rewrites the `spec.root` of
// exactly one Project and nothing else. It performs zero filesystem moves: the
// command never creates, renames, copies, or deletes a path, it only records a
// new binding. It also performs zero heuristic merges: the collision check is an
// exact cleaned-path comparison, so a shared basename, a shared git origin, a
// shared inode, or scan order can never fold two Projects onto one uid.
type rebindCommand struct {
	store *resourceStore
}

func newRebindCommand() *rebindCommand {
	return &rebindCommand{store: newResourceStore()}
}

// Run dispatches one `rebind <kind>` invocation.
func (c *rebindCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("rebind requires a resource kind: %s", strings.Join(rebindKinds, ", ")))
	}
	if args[0] != "project" {
		return usageError(fmt.Sprintf("rebind %s is not available; this release implements: %s",
			args[0], strings.Join(rebindKinds, ", ")))
	}
	return c.runProject(args[1:], stdout, stderr)
}

func (c *rebindCommand) runProject(args []string, stdout, stderr io.Writer) error {
	const spelling = "rebind project"

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: coremetadata.KindProject}
	flags.register(fs)
	root := fs.String("root", "", "the new absolute project root; it must already exist")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if len(refs) > 1 {
		return usageError(fmt.Sprintf("%s accepts at most one resource reference; got %q", spelling, refs[1]))
	}
	for _, ref := range refs {
		flags.addPositionalRef(ref)
	}
	if strings.TrimSpace(*root) == "" {
		return usageError(spelling + " requires --root <absolute-path>")
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbRebind, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]

	var bound coremetadata.Project
	if err := c.store.mutate(coremetadata.KindProject, []string{match.UID},
		func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
			project, err := mutator.RebindProjectRoot(working, match.UID, *root)
			if err != nil {
				return err
			}
			bound = project
			return nil
		}); err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "project/%s root=%s\n", bound.Metadata.Name, bound.Spec.Root)
	return err
}
