package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// describeKinds lists the kind spellings `describe` implements, in help order,
// each canonical token followed by its accepted aliases. See getKinds.
var describeKinds = cli.ChildSpellings("describe")

// describeCommand implements the read-only `describe` verb.
//
// `describe` is the singular counterpart of the plural `get` reads: it resolves
// exactly one resource and renders its full stored shape. Like `get` it loads
// the registry without creating it and writes to stdout only after the
// resolution succeeds.
type describeCommand struct {
	loadRegistry func() (coremetadata.Registry, error)
	// runtime is the live-tmux observation Window and Pane status is derived
	// from; see runtime_observation.go.
	runtime runtimeLookup
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
}

func newDescribeCommand() *describeCommand {
	return &describeCommand{
		loadRegistry: loadResourceRegistry,
		runtime:      defaultRuntimeLookup(),
		activeTarget: defaultActiveTargetLookup(),
	}
}

// Run dispatches one `describe <kind>` invocation.
func (c *describeCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("describe requires a resource kind: %s", strings.Join(describeKinds, ", ")))
	}
	token, ok := cli.CanonicalChildToken("describe", args[0])
	if !ok {
		return usageError(fmt.Sprintf("describe %s is not available; this release implements: %s",
			args[0], strings.Join(describeKinds, ", ")))
	}
	kind, ok := resourceKindTokens[token]
	if !ok {
		return usageError(fmt.Sprintf("describe %s is not available; this release implements: %s",
			args[0], strings.Join(describeKinds, ", ")))
	}
	return c.runKind(token, kind, args[1:], stdout, stderr)
}

func (c *describeCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "describe " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := resourceQueryFlags{kind: kind, active: c.activeTarget, runtime: c.runtime}
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
		// A singular read renders no elapsed time, so it passes no clock.
		return writeResourceProjection(stdout, spelling, mode, kind, resolution.Matches, registry, false, time.Time{})
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
	// CreatedAt sits with the identity rows because that is what it is: the
	// instant this uid came into existence, immutable for as long as the uid is.
	// It is absolute here rather than the plural read's relative `AGE`, because
	// a description answers "when", not "how long ago", and an absolute instant
	// is the only form two descriptions taken at different times can be compared
	// across. A registry written before the field was stamped omits the row
	// instead of dating the resource to year 1.
	if !meta.CreatedAt.IsZero() {
		rows = append(rows, [2]string{"CreatedAt", describeTimestamp(meta.CreatedAt)})
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
		return append(rows, describeConditionRows(typed.Status.Conditions)...)
	case coremetadata.Window:
		rows := [][2]string{{"PrimaryPaneRef", typed.Spec.PrimaryPaneRef}}
		return append(rows, describeConditionRows(typed.Status.Conditions)...)
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
		rows = append(rows, describeTerminationRows(typed.Status.LastTermination)...)
		return append(rows, describeConditionRows(typed.Status.Conditions)...)
	case coremetadata.Agent:
		rows := [][2]string{{"Provider", typed.Spec.Provider}, {"Phase", string(typed.Status.Phase)}}
		interaction := typed.Status.Interaction
		rows = append(rows, [2]string{"Interaction", string(interaction.Kind)})
		if !interaction.ObservedAt.IsZero() {
			rows = append(rows, [2]string{"InteractionObservedAt", describeTimestamp(interaction.ObservedAt)})
		}
		if interaction.Source != "" {
			rows = append(rows, [2]string{"InteractionSource", interaction.Source})
		}
		rows = append(rows, [2]string{"Activation", string(typed.Status.Activation.State)})
		if typed.Status.Activation.Reason != "" {
			rows = append(rows, [2]string{"ActivationReason", typed.Status.Activation.Reason})
		}
		if typed.Spec.Workspace.CWD != "" {
			rows = append(rows, [2]string{"WorkspaceCWD", typed.Spec.Workspace.CWD})
		}
		for _, root := range typed.Spec.Workspace.AdditionalWritableRoots {
			rows = append(rows, [2]string{"AdditionalWritableRoot", root})
		}
		// The transition time is rendered directly under the phase it dates. An
		// Agent's phase is the one status field on any kind that moves on its
		// own -- Pending to Running to Offline -- and until now the instant it
		// last moved was stored on every Agent and shown on none of them.
		if !typed.Status.LastTransitionAt.IsZero() {
			rows = append(rows, [2]string{"PhaseSince", describeTimestamp(typed.Status.LastTransitionAt)})
		}
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
			rows = append(rows, [2]string{"SessionObservedAt", describeTimestamp(ref.ObservedAt)})
		}
		// The termination rows are rendered last, under the phase they explain.
		// An Offline Agent's phase says it is resumable; these say whether it got
		// there by a deliberate close, a clean exit, a crash, or a disappearance
		// nothing accounted for -- which is the distinction the two-valued phase
		// cannot carry and the one an operator needs before resuming.
		return append(rows, describeTerminationRows(typed.Status.LastTermination)...)
	default:
		return nil
	}
}

// describeTerminationRows renders the stored termination receipt of one Pane or
// Agent.
//
// It is a pure projection of what is already in the registry. Nothing here reads
// a transcript, a pane's contents, a process table, or a provider history, and
// nothing here consumes the receipt: describe is a read verb, and a read that
// advanced a lifecycle would make querying the state change it.
//
// A resource with no receipt renders no rows at all rather than a row saying
// "none". Absence of evidence is the normal state of a live resource, and a
// permanent empty row would make every healthy Pane look like it was missing
// something.
//
// The classification, its provenance and the instant it was observed are always
// rendered together. Classification alone is not actionable -- "abnormal" from a
// supervisor that read a wait status and "unknown" from a reconciliation that
// found an empty socket are different kinds of claim -- and an undated one
// cannot be told apart from a stale one.
func describeTerminationRows(receipt *coremetadata.TerminationEvidence) [][2]string {
	if receipt == nil || receipt.IsZero() {
		return nil
	}
	rows := [][2]string{
		{"Termination", string(receipt.Classification)},
		{"TerminationSource", string(receipt.Source)},
	}
	if !receipt.ObservedAt.IsZero() {
		rows = append(rows, [2]string{"TerminationObservedAt", describeTimestamp(receipt.ObservedAt)})
	}
	// The exit status is rendered only when one was actually read. A receipt with
	// no exit code is not an exit code of zero: see TerminationEvidence.ExitCode.
	if receipt.ExitCode != nil {
		rows = append(rows, [2]string{"TerminationExitCode", strconv.Itoa(*receipt.ExitCode)})
	}
	if receipt.Signal != "" {
		rows = append(rows, [2]string{"TerminationSignal", receipt.Signal})
	}
	if receipt.PaneUID != "" {
		rows = append(rows, [2]string{"TerminationPaneRef", receipt.PaneUID})
	}
	if receipt.Generation != "" {
		rows = append(rows, [2]string{"TerminationGeneration", receipt.Generation})
	}
	if receipt.OperationID != "" {
		rows = append(rows, [2]string{"TerminationOperationID", receipt.OperationID})
	}
	return rows
}

// describeConditionRows renders the observed conditions of one resource.
//
// This is how the *reason* a runtime object went away stays visible after the
// observation that noticed it is gone. Status says a Window or Pane is offline;
// its MissingRuntime condition says since when and against what. The resource
// itself is never deleted, so both rows keep answering for as long as the
// operator cares to ask.
// Both stored instants are rendered, and they answer different questions: a
// condition that has been re-observed unchanged keeps its original
// firstObservedAt while lastTransitionAt moves, so the pair is what separates
// "offline since Tuesday" from "offline, last checked a minute ago". Storing
// both and showing one made the second reading unavailable at every surface.
func describeConditionRows(conditions []coremetadata.Condition) [][2]string {
	rows := make([][2]string, 0, len(conditions))
	for _, condition := range conditions {
		rows = append(rows, [2]string{"Condition", fmt.Sprintf("%s=%s reason=%s firstObservedAt=%s lastTransitionAt=%s",
			condition.Type, condition.Status, condition.Reason,
			describeTimestamp(condition.FirstObservedAt), describeTimestamp(condition.LastTransitionAt))})
	}
	return rows
}

// describeTimestamp renders one stored instant in the absolute UTC form the
// description block uses everywhere.
//
// It is a second-precision RFC 3339 instant in UTC, never the operator's local
// zone: a description is routinely read next to a registry file and next to
// `-o json`, both of which store UTC, and a locally-rendered row would silently
// disagree with them. The format string lived on the Agent session row alone
// before there was more than one timestamp to render.
func describeTimestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05Z")
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
