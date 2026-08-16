package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// renameKinds lists the kind spellings `rename` implements, in help order, each
// canonical token followed by its accepted aliases. See getKinds.
var renameKinds = cli.ChildSpellings("rename")

// renameCommand implements the canonical `rename` verb.
//
// It changes the Projmux `metadata.name` of exactly one resource and nothing
// else. It never writes the raw tmux `pane_title`, never touches a
// `displayName`, and never invents a suffix: an explicit name that is already
// reserved in the target scope is a usage error with zero mutations.
type renameCommand struct {
	store *resourceStore
	// runtime is the live-tmux observation the rendered result's Window and
	// Pane status is derived from; see runtime_observation.go.
	runtime runtimeLookup
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
}

func newRenameCommand() *renameCommand {
	return &renameCommand{
		store:        newResourceStore(),
		runtime:      defaultRuntimeLookup(),
		activeTarget: defaultActiveTargetLookup(),
	}
}

// Run dispatches one `rename <kind>` invocation.
func (c *renameCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("rename requires a resource kind: %s", strings.Join(renameKinds, ", ")))
	}
	// `agent` and `agents` need no special case here any more: the manifest node
	// for `rename` has no Agent child, so neither spelling normalizes and both
	// land on the same refusal every other unknown kind gets.
	token, ok := cli.CanonicalChildToken("rename", args[0])
	if !ok {
		return usageError(fmt.Sprintf("rename %s is not available; this release implements: %s",
			args[0], strings.Join(renameKinds, ", ")))
	}
	kind, ok := resourceKindTokens[token]
	if !ok {
		return usageError(fmt.Sprintf("rename %s is not available; this release implements: %s",
			args[0], strings.Join(renameKinds, ", ")))
	}
	return c.runKind(token, kind, args[1:], stdout, stderr)
}

func (c *renameCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "rename " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: kind, active: c.activeTarget, runtime: c.runtime}
	flags.register(fs)
	name := fs.String("name", "", "the new Projmux metadata.name")
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
	if strings.TrimSpace(*name) == "" {
		return usageError(spelling + " requires --name <name>")
	}

	mode, field, err := resolveOutputMode(spelling, flags.output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbRename, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	uid := resolution.Matches[0].UID

	if err := c.store.mutate(kind, []string{uid}, func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		switch kind {
		case coremetadata.KindProject:
			_, err := mutator.RenameProject(working, uid, *name)
			return err
		case coremetadata.KindWindow:
			_, err := mutator.RenameWindow(working, uid, *name)
			return err
		case coremetadata.KindPane:
			_, err := mutator.RenamePane(working, uid, *name)
			return err
		default:
			return fmt.Errorf("%s: unsupported kind %q", spelling, kind)
		}
	}); err != nil {
		return err
	}

	renamed, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]
	match.Name = *name
	// A singular result renders no elapsed time, so it passes no clock.
	return writeResourceProjection(stdout, spelling, mode, kind, []selector.Match{match}, renamed, false, time.Time{})
}
