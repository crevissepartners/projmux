package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// describeKinds lists the resource kinds `describe` implements, in help order.
var describeKinds = []string{"project", "window", "pane", "agent"}

// describeCommand implements the read-only `describe` verb.
//
// `describe` is the singular counterpart of the plural `get` reads: it resolves
// exactly one resource and renders its full stored shape. Like `get` it loads
// the registry without creating it and writes to stdout only after the
// resolution succeeds.
type describeCommand struct {
	loadRegistry func() (coremetadata.Registry, error)
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
}

func newDescribeCommand() *describeCommand {
	return &describeCommand{
		loadRegistry: loadResourceRegistry,
		activeTarget: defaultActiveTargetLookup(),
	}
}

// Run dispatches one `describe <kind>` invocation.
func (c *describeCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("describe requires a resource kind: %s", strings.Join(describeKinds, ", ")))
	}
	kind, ok := resourceKindTokens[args[0]]
	if !ok {
		return usageError(fmt.Sprintf("describe %s is not available; this release implements: %s",
			args[0], strings.Join(describeKinds, ", ")))
	}
	return c.runKind(args[0], kind, args[1:], stdout, stderr)
}

func (c *describeCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "describe " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: kind, active: c.activeTarget}
	flags.register(fs)
	flags.registerOutput(fs)
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

	mode, field, err := resolveOutputMode(spelling, flags.output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}

	registry, err := c.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbDescribe, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	match := resolution.Matches[0]
	if mode != cli.OutputModeDefault {
		return writeResourceProjection(stdout, spelling, mode, kind, resolution.Matches, registry, false)
	}
	return writeResourceDescription(stdout, spelling, kind, match, registry)
}

// writeResourceDescription renders the human description block of one resource.
func writeResourceDescription(stdout io.Writer, spelling string, kind coremetadata.Kind, match selector.Match, registry coremetadata.Registry) error {
	resource, meta, ok := resourceFor(registry, kind, match.UID)
	if !ok {
		return fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, match.UID)
	}

	rows := [][2]string{
		{"Kind", string(kind)},
		{"Name", meta.Name},
		{"UID", meta.UID},
	}
	if meta.DisplayName != "" {
		rows = append(rows, [2]string{"DisplayName", meta.DisplayName})
	}
	if owner := match.Owner.String(); owner != "" {
		rows = append(rows, [2]string{"Owner", owner})
	}
	rows = append(rows, [2]string{"Status", string(match.Status)})
	rows = append(rows, describeSpecRows(resource)...)
	rows = append(rows, describeMapRows("Labels", meta.Labels)...)
	rows = append(rows, describeMapRows("Annotations", meta.Annotations)...)

	width := 0
	for _, row := range rows {
		if len(row[0]) > width {
			width = len(row[0])
		}
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "%s:%s%s\n", row[0], strings.Repeat(" ", width-len(row[0])+1), row[1])
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}

// describeSpecRows renders the kind-specific spec and status fields.
func describeSpecRows(resource any) [][2]string {
	switch typed := resource.(type) {
	case coremetadata.Project:
		rows := [][2]string{{"Root", typed.Spec.Root}}
		if session := typed.Status.Session; session != nil {
			rows = append(rows, [2]string{"Session", fmt.Sprintf("%s live=%t", session.Name, session.Live)})
		}
		for _, condition := range typed.Status.Conditions {
			rows = append(rows, [2]string{"Condition", fmt.Sprintf("%s=%s reason=%s firstObservedAt=%s",
				condition.Type, condition.Status, condition.Reason, condition.FirstObservedAt.UTC().Format("2006-01-02T15:04:05Z"))})
		}
		return rows
	case coremetadata.Window:
		return [][2]string{{"PrimaryPaneRef", typed.Spec.PrimaryPaneRef}}
	case coremetadata.Pane:
		rows := [][2]string{{"Role", string(typed.Spec.Role)}}
		if typed.Spec.CWD != "" {
			rows = append(rows, [2]string{"CWD", typed.Spec.CWD})
		}
		if typed.Spec.Command != "" {
			rows = append(rows, [2]string{"Command", typed.Spec.Command})
		}
		if typed.Status.DisplayTitle != "" {
			rows = append(rows, [2]string{"DisplayTitle", typed.Status.DisplayTitle})
		}
		return rows
	case coremetadata.Agent:
		rows := [][2]string{{"Provider", typed.Spec.Provider}, {"Phase", string(typed.Status.Phase)}}
		if typed.Status.PaneRef != "" {
			rows = append(rows, [2]string{"PaneRef", typed.Status.PaneRef})
		}
		// The session ref rows are rendered from the populated provider member,
		// so the key names differ per provider (Claude reports a SessionID and a
		// TranscriptPath, Codex a ThreadID and a SessionID, Antigravity a
		// ConversationID). That is the point of the union: describe shows the
		// identifiers the provider actually issued instead of a normalized
		// lowest common denominator.
		if ref := typed.Status.SessionRef; !ref.Empty() {
			rows = append(rows, [2]string{"SessionProvider", ref.Provider})
			rows = append(rows, ref.Fields()...)
			rows = append(rows, [2]string{"SessionObservedAt", ref.ObservedAt.UTC().Format("2006-01-02T15:04:05Z")})
		}
		return rows
	default:
		return nil
	}
}

// describeMapRows renders a metadata map in deterministic key order.
func describeMapRows(label string, values map[string]string) [][2]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values[key])
	}
	return [][2]string{{label, strings.Join(pairs, " ")}}
}
