package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// labelKinds lists the kind spellings `label` implements, in help order, each
// canonical token followed by its accepted aliases. See renameKinds.
var labelKinds = cli.ChildSpellings("label")

// labelCommand implements the canonical `label` verb.
//
// It writes `metadata.labels` of exactly one resource and nothing else. There
// is no tmux mirror to converge afterwards, which is the whole difference from
// `rename`: a name is projected onto a live tab and a pane option, a label is
// projected nowhere. The Registry write is therefore the entire operation.
type labelCommand struct {
	store *resourceStore
	// runtime is the live-tmux observation the resolved Match's status is
	// derived from; see runtime_observation.go.
	runtime runtimeLookup
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
}

func newLabelCommand() *labelCommand {
	return &labelCommand{
		store:        newResourceStore(),
		runtime:      defaultRuntimeLookup(),
		activeTarget: defaultActiveTargetLookup(),
	}
}

// Run dispatches one `label <kind>` invocation.
func (c *labelCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("label requires a resource kind: %s", strings.Join(labelKinds, ", ")))
	}
	token, ok := cli.CanonicalChildToken("label", args[0])
	if !ok {
		return usageError(fmt.Sprintf("label %s is not available; this release implements: %s",
			args[0], strings.Join(labelKinds, ", ")))
	}
	kind, ok := resourceKindTokens[token]
	if !ok {
		return usageError(fmt.Sprintf("label %s is not available; this release implements: %s",
			args[0], strings.Join(labelKinds, ", ")))
	}
	return c.runKind(token, kind, args[1:], stdout, stderr)
}

func (c *labelCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "label " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: kind, active: c.activeTarget, runtime: c.runtime}
	// A generic descendant write resolves an explicit name inside the exact root
	// that owns the active Window, exactly as `rename` does. A Project has no
	// enclosing root.
	flags.managedRootNamespaceScope = kind == coremetadata.KindWindow || kind == coremetadata.KindPane || kind == coremetadata.KindAgent
	flags.register(fs)
	positionals, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}

	refs, operands := splitLabelPositionals(positionals)
	if len(refs) > 1 {
		// Naming both readings is the point. A second bare token is either one
		// reference too many or a label operand the operator misspelled, and
		// the shape rule cannot tell which, so the refusal offers both instead
		// of asserting the one it happened to classify.
		return usageError(fmt.Sprintf(
			"%s accepts at most one resource reference; %q is neither a second reference nor a label change (key=value sets, key- removes)",
			spelling, refs[1]))
	}
	for _, ref := range refs {
		flags.addPositionalRef(ref)
	}
	if len(operands) == 0 {
		return usageError(spelling + " requires at least one key=value to set a label or key- to remove one")
	}
	changes, err := selector.ParseLabelChanges(operands)
	if err != nil {
		return MapMetadataError(err)
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbLabel, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]

	set, remove := selector.LabelChangeSets(changes)
	var (
		meta    coremetadata.ObjectMeta
		changed bool
	)
	if err := c.store.mutate(kind, []string{match.UID}, func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		var err error
		meta, changed, err = mutator.SetLabels(working, kind, match.UID, set, remove)
		return err
	}); err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, labelResultLine(spelling, kind, meta, changed))
	return err
}

// splitLabelPositionals separates the optional resource reference from the
// label operands.
//
// The split is lexical and never consults the Registry: an operand is anything
// shaped like one, and whatever is left is the reference. A token carrying `=`
// can never be a name at all, and a name ending in `-` is addressed by `uid:`
// or by its scope flag instead. Deciding by shape is what keeps the same argv
// meaning the same thing on every machine.
func splitLabelPositionals(positionals []string) (refs, operands []string) {
	for _, token := range positionals {
		if selector.IsLabelChangeOperand(token) {
			operands = append(operands, token)
			continue
		}
		refs = append(refs, token)
	}
	return refs, operands
}

// labelResultLine reports the resulting label set rather than the requested
// change, so a removal of a key that was never there and an overwrite of a key
// that already held the value both print what the resource now carries.
func labelResultLine(spelling string, kind coremetadata.Kind, meta coremetadata.ObjectMeta, changed bool) string {
	state := "labels"
	if !changed {
		state = "labels unchanged"
	}
	rendered := selector.FormatLabels(meta.Labels)
	if rendered == "" {
		rendered = "(none)"
	}
	return fmt.Sprintf("%s: %s/%s: %s %s", spelling, strings.ToLower(string(kind)), meta.Name, state, rendered)
}
