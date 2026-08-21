package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// `reconcile registry` is the operator boundary for a lost or damaged Registry.
//
// It is a sibling of `reconcile resources` rather than part of it, because the
// two repair different things and must not share a failure mode. `reconcile
// resources` converges a Registry that exists against one exact tmux server;
// this route runs when the Registry itself is the thing that is wrong, which is
// exactly the situation where the resource planner cannot even load its input.
//
// Three properties define the route:
//
//  1. Planning writes nothing. No Registry byte, no recovery copy, no marker, no
//     tmux state, and not even the metadata directory. A plan is most needed
//     against a state directory that no writer should touch yet, so the plan
//     must be safe to run there.
//  2. Restoring requires an explicit source. There is no "just fix it": the
//     command never picks a copy, because which copy is the truth is an operator
//     judgment about which mutations were wanted. The plan prints the exact
//     guarded command for the source it would suggest, and the operator runs it.
//     Restore serializes on the Store's recovery-only lock, never the ordinary
//     mutation lock, and retains its own source, staged, and checksum guards.
//  3. The live tmux mirror is a diagnostic, never a source. When no verified
//     copy exists, the route reports what identity the exact server can still
//     testify to and, beside it, an explicit list of what no mirror can return.
//     Building a Registry from fragments would convert a visible loss into an
//     invisible one.
type registryRecoveryCommand struct {
	runner    tmuxCommandRunner
	lookupEnv func(string) string
	// newStore is the store seam. Tests point it at a temp state directory so no
	// test ever inspects or restores the operator's own Registry.
	newStore func() (*intmetadata.Store, error)
	// observeFragments is the mirror seam, resolved against one exact target.
	observeFragments func(context.Context, explicitTmuxTarget) ([]intmetadata.IdentityFragment, error)
}

func newRegistryRecoveryCommand(tmux *tmuxCommand) *registryRecoveryCommand {
	command := &registryRecoveryCommand{
		lookupEnv: os.Getenv,
		newStore: func() (*intmetadata.Store, error) {
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				return nil, fmt.Errorf("resolve projmux state paths: %w", err)
			}
			return intmetadata.NewDefaultStore(paths), nil
		},
	}
	if tmux != nil {
		command.runner = tmux.runner
	}
	command.observeFragments = func(ctx context.Context, target explicitTmuxTarget) ([]intmetadata.IdentityFragment, error) {
		if command.runner == nil {
			return nil, errors.New("no tmux runner is configured")
		}
		return intmetadata.NewMirror(explicitTmuxRunner{runner: command.runner, target: target}).ObserveIdentityFragments(ctx)
	}
	return command
}

type registryRecoveryOptions struct {
	dryRun        bool
	output        string
	sources       repeatedFlag
	expectSource  string
	expectCurrent string
	socket        string
	socketPath    string
}

// registryRecoveryReport is the stable projection of one plan or restore.
//
// Field order is declaration order, every slice is sorted by the layer that
// produces it, and no map is marshalled, so the JSON of two runs over the same
// state is byte-identical.
type registryRecoveryReport struct {
	Mode    string `json:"mode"`
	Outcome string `json:"outcome"`
	// Selection records how the source was chosen, so a reader never has to
	// infer whether the command picked one.
	Selection    string                        `json:"selection"`
	RegistryPath string                        `json:"registryPath"`
	MarkerPath   string                        `json:"markerPath"`
	RecoveryDir  string                        `json:"recoveryDir"`
	Initialized  bool                          `json:"initialized"`
	Retention    int                           `json:"retention"`
	Current      intmetadata.RegistryFileInfo  `json:"current"`
	Selected     *intmetadata.RecoverySource   `json:"selected"`
	Sources      []intmetadata.RecoverySource  `json:"sources"`
	Restore      *registryRecoveryRestore      `json:"restore,omitempty"`
	Mirror       *registryRecoveryMirrorReport `json:"mirror,omitempty"`
	Next         string                        `json:"next,omitempty"`
	Error        string                        `json:"error,omitempty"`
}

// registryRecoveryRestore is the committed half of the report.
type registryRecoveryRestore struct {
	SourcePath        string                       `json:"sourcePath"`
	SourceChecksum    string                       `json:"sourceChecksum"`
	Changed           bool                         `json:"changed"`
	ReplacedState     intmetadata.RegistryState    `json:"replacedState,omitempty"`
	PreservedPath     string                       `json:"preservedPath,omitempty"`
	PreservedChecksum string                       `json:"preservedChecksum,omitempty"`
	Contents          intmetadata.RegistryContents `json:"contents"`
}

// registryRecoveryMirrorReport is the partial-evidence diagnostic.
type registryRecoveryMirrorReport struct {
	// Available is false when there is no exact transport or the queries failed.
	// It is never false-because-empty: a reachable server with nothing mirrored
	// is available with zero fragments, which is a different fact.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Target    string `json:"target,omitempty"`
	Counts    struct {
		Projects int `json:"projects"`
		Windows  int `json:"windows"`
		Panes    int `json:"panes"`
		// AgentPanes is how many mirrored Panes carry a provider option. Each
		// one proves an Agent existed whose own uid is nowhere on the server.
		AgentPanes int `json:"agentPanes"`
	} `json:"counts"`
	Recoverable   []intmetadata.IdentityFragment `json:"recoverable"`
	Unrecoverable []registryRecoveryGap          `json:"unrecoverable"`
}

// registryRecoveryGap is one class of state a mirror cannot return.
type registryRecoveryGap struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

func (c *registryRecoveryCommand) Run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseRegistryRecoveryOptions(args, stderr)
	if err != nil {
		return err
	}
	if c.newStore == nil {
		return errors.New("registry recovery store is not configured")
	}
	store, err := c.newStore()
	if err != nil {
		return err
	}
	inspection, err := store.InspectRecovery()
	if err != nil {
		return MapMetadataError(err)
	}

	report := registryRecoveryReport{
		Mode: "dry-run", Selection: "none",
		RegistryPath: inspection.RegistryPath, MarkerPath: inspection.MarkerPath,
		RecoveryDir: inspection.RecoveryDir, Initialized: inspection.Initialized,
		Retention: inspection.Retention, Current: inspection.Current,
		Sources: inspection.Sources,
	}
	if report.Sources == nil {
		report.Sources = []intmetadata.RecoverySource{}
	}

	selector := strings.TrimSpace(firstRepeatedValue(opts.sources))
	// Partial evidence is reported exactly when recovery is needed and the
	// bounded history cannot answer it. With a verified copy in hand the mirror
	// adds noise, and against a healthy Registry it would answer a question
	// nobody asked -- while a lost Registry with no copy is the one case where
	// fragments are all that is left to look at.
	if len(inspection.EligibleSources()) == 0 && registryRecoveryNeeded(inspection.Current.State) {
		report.Mirror = c.observeMirror(context.Background(), opts)
	}

	if selector == "" {
		report.Outcome, report.Next = registryRecoveryPlanOutcome(inspection)
		return writeRegistryRecoveryReport(stdout, opts.output, report)
	}

	selected, err := c.resolveSource(store, inspection, selector)
	if err != nil {
		report.Outcome, report.Error = "refused", err.Error()
		if writeErr := writeRegistryRecoveryReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return err
	}
	report.Selection = "explicit"
	report.Selected = &selected
	if !selected.Eligible {
		report.Outcome = "refused"
		refusal := fmt.Errorf("reconcile registry refused %s: %s", selected.Path, selected.Reason)
		report.Error = refusal.Error()
		if writeErr := writeRegistryRecoveryReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return refusal
	}

	if opts.dryRun {
		report.Outcome = "planned"
		if selected.Checksum == inspection.Current.Checksum {
			report.Outcome = "no-op"
		} else {
			report.Next = registryRecoveryRestoreCommand(selected, inspection.Current)
		}
		return writeRegistryRecoveryReport(stdout, opts.output, report)
	}

	result, err := store.RestoreFrom(intmetadata.RestoreRequest{
		SourcePath:            selected.Path,
		ExpectSourceChecksum:  opts.expectSource,
		ExpectCurrentChecksum: opts.expectCurrent,
	})
	if err != nil {
		report.Mode, report.Outcome, report.Error = "restore", "refused", err.Error()
		report.Next = registryRecoveryRestoreCommand(selected, inspection.Current)
		if writeErr := writeRegistryRecoveryReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return MapMetadataError(err)
	}
	report.Mode = "restore"
	report.Outcome = "restored"
	if !result.Changed {
		report.Outcome = "no-op"
	}
	report.Restore = &registryRecoveryRestore{
		SourcePath: result.SourcePath, SourceChecksum: result.SourceChecksum, Changed: result.Changed,
		ReplacedState: result.ReplacedState, PreservedPath: result.PreservedPath,
		PreservedChecksum: result.PreservedChecksum, Contents: result.Contents,
	}
	// The post-restore state is re-read rather than assumed, so the report shows
	// what the next command will actually load.
	if after, err := store.InspectRecovery(); err == nil {
		report.Current = after.Current
		report.Initialized = after.Initialized
		report.Sources = after.Sources
		if report.Sources == nil {
			report.Sources = []intmetadata.RecoverySource{}
		}
	}
	return writeRegistryRecoveryReport(stdout, opts.output, report)
}

// resolveSource turns one selector into a candidate. An absolute path bypasses
// the bounded enumeration but not the verification.
func (c *registryRecoveryCommand) resolveSource(store *intmetadata.Store, inspection intmetadata.RecoveryInspection, selector string) (intmetadata.RecoverySource, error) {
	if filepath.IsAbs(selector) {
		return store.InspectExplicitSource(selector)
	}
	return inspection.SelectSource(selector)
}

// registryRecoveryNeeded reports whether the current Registry is a state an
// operator has to act on. A valid Registry and a genuine first use are both
// healthy, and conflating either with damage would turn a routine check into a
// false alarm -- the same conflation the initialized marker exists to prevent.
func registryRecoveryNeeded(state intmetadata.RegistryState) bool {
	switch state {
	case intmetadata.RegistryStateValid, intmetadata.RegistryStateFirstUse:
		return false
	default:
		return true
	}
}

// registryRecoveryPlanOutcome classifies a plan that selected nothing.
func registryRecoveryPlanOutcome(inspection intmetadata.RecoveryInspection) (string, string) {
	eligible := inspection.EligibleSources()
	if len(eligible) == 0 {
		if !registryRecoveryNeeded(inspection.Current.State) {
			// Nothing is wrong and nothing is restorable. Saying "unrecoverable"
			// here would alarm an operator whose Registry is simply fine.
			return "no-op", ""
		}
		return "unrecoverable", ""
	}
	newest := eligible[0]
	if newest.Checksum == inspection.Current.Checksum {
		return "no-op", ""
	}
	return "planned", registryRecoveryRestoreCommand(newest, inspection.Current)
}

// registryRecoveryRestoreCommand renders the exact guarded follow-up.
//
// Both checksums are baked in on purpose. It makes the printed command refer to
// the state the operator just read, so a restore run after something else moved
// underneath refuses instead of publishing over a state nobody looked at.
func registryRecoveryRestoreCommand(source intmetadata.RecoverySource, current intmetadata.RegistryFileInfo) string {
	command := "projmux reconcile registry --source " + shellQuote(registryRecoverySelector(source))
	if source.Checksum != "" {
		command += " --expect-source-checksum " + source.Checksum
	}
	if current.Checksum != "" {
		command += " --expect-current-checksum " + current.Checksum
	}
	return command
}

// registryRecoverySelector prefers the bounded copy name and falls back to the
// absolute path, which is the only stable handle an explicit source has.
func registryRecoverySelector(source intmetadata.RecoverySource) string {
	if source.Kind == intmetadata.RecoverySourceExplicitPath {
		return source.Path
	}
	return source.Name
}

// observeMirror builds the partial-evidence diagnostic for one exact server.
//
// No transport is a reported reason rather than an error: a restore is a
// filesystem operation, so refusing to plan outside tmux would block recovery on
// exactly the machine state where tmux is least likely to be running.
func (c *registryRecoveryCommand) observeMirror(ctx context.Context, opts registryRecoveryOptions) *registryRecoveryMirrorReport {
	report := &registryRecoveryMirrorReport{Unrecoverable: registryRecoveryGaps()}
	report.Recoverable = []intmetadata.IdentityFragment{}
	target, err := c.resolveMirrorTarget(opts)
	if err != nil {
		report.Reason = err.Error()
		return report
	}
	report.Target = "tmux " + target.flag + " " + target.value
	if c.observeFragments == nil {
		report.Reason = "no tmux mirror reader is configured"
		return report
	}
	fragments, err := c.observeFragments(ctx, target)
	if err != nil {
		report.Reason = "the exact tmux server could not be observed: " + err.Error()
		return report
	}
	report.Available = true
	report.Reason = "mirrored identity of live objects on this exact server only"
	for _, fragment := range fragments {
		switch fragment.Kind {
		case coremetadata.KindProject:
			report.Counts.Projects++
		case coremetadata.KindWindow:
			report.Counts.Windows++
		case coremetadata.KindPane:
			report.Counts.Panes++
			if fragment.AgentProvider != "" {
				report.Counts.AgentPanes++
			}
		}
	}
	if len(fragments) > 0 {
		report.Recoverable = fragments
	}
	return report
}

// resolveMirrorTarget picks the exact server to observe. Unlike `reconcile
// resources` an absent target is not a usage error here; it is a reason.
func (c *registryRecoveryCommand) resolveMirrorTarget(opts registryRecoveryOptions) (explicitTmuxTarget, error) {
	if strings.TrimSpace(opts.socket) != "" {
		return tmuxSocketNameTarget(opts.socket)
	}
	if strings.TrimSpace(opts.socketPath) != "" {
		return tmuxSocketPathTarget(opts.socketPath)
	}
	tmuxEnv := ""
	if c.lookupEnv != nil {
		tmuxEnv = strings.TrimSpace(c.lookupEnv("TMUX"))
	}
	inherited, _, _ := strings.Cut(tmuxEnv, ",")
	if inherited == "" || !filepath.IsAbs(inherited) {
		return explicitTmuxTarget{}, errors.New("no exact tmux transport: pass --socket <name> or --socket-path <absolute> to inspect a live mirror")
	}
	return tmuxSocketPathTarget(inherited)
}

// registryRecoveryGaps is the fixed statement of what no mirror can return.
//
// It is a constant list rather than a computed one because every entry is
// unrecoverable by construction, not by the contents of a particular server. A
// computed list would shrink on a busy machine and read as "these are the only
// gaps", which is the false full-recovery impression this whole diagnostic
// exists to prevent.
func registryRecoveryGaps() []registryRecoveryGap {
	return []registryRecoveryGap{
		{Scope: "offline-resources", Reason: "a Project, Window, or Pane with no live tmux object mirrors no option, so it cannot appear in this observation at all"},
		{Scope: "agent-resources", Reason: "no tmux option carries an Agent uid; every Agent, its provider sessionRef, and its phase are unrecoverable from the mirror"},
		{Scope: "pane-owner-relation", Reason: "a mirrored Pane shows the Window that contains it, not its registry owner; an Agent-owned Pane's ownerRef cannot be rebuilt"},
		{Scope: "name-reservations", Reason: "the reservation table holds names for resources that are not live, and no live object testifies to a reservation"},
		{Scope: "window-primary-pane", Reason: "Window spec.primaryPaneRef is not mirrored onto tmux"},
		{Scope: "labels-annotations-status", Reason: "metadata.labels, metadata.annotations, createdAt/updatedAt, and status are not mirrored onto tmux"},
	}
}

func parseRegistryRecoveryOptions(args []string, stderr io.Writer) (registryRecoveryOptions, error) {
	var opts registryRecoveryOptions
	fs := flag.NewFlagSet("reconcile registry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.dryRun, "dry-run", false, "preview the recovery plan without writing")
	fs.StringVar(&opts.output, "output", "", "output mode: json")
	fs.StringVar(&opts.output, "o", "", "output mode (alias of --output)")
	fs.Var(&opts.sources, "source", "exact recovery copy name, unique name fragment, or absolute path")
	fs.StringVar(&opts.expectSource, "expect-source-checksum", "", "refuse unless the source still hashes to this sha256:<hex>")
	fs.StringVar(&opts.expectCurrent, "expect-current-checksum", "", "refuse unless the current registry still hashes to this sha256:<hex>")
	fs.StringVar(&opts.socket, "socket", "", "exact tmux socket name (tmux -L) for the mirror diagnostic")
	fs.StringVar(&opts.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S) for the mirror diagnostic")
	if err := fs.Parse(args); err != nil {
		return registryRecoveryOptions{}, err
	}
	if fs.NArg() != 0 {
		return registryRecoveryOptions{}, usageError("reconcile registry does not accept positional arguments")
	}
	opts.output = strings.TrimSpace(strings.ToLower(opts.output))
	if opts.output != "" && opts.output != "json" {
		return registryRecoveryOptions{}, usageError(fmt.Sprintf("unsupported reconcile registry output %q; use json", opts.output))
	}
	if strings.TrimSpace(opts.socket) != "" && strings.TrimSpace(opts.socketPath) != "" {
		return registryRecoveryOptions{}, usageError("reconcile registry accepts only one of --socket and --socket-path")
	}
	// Two sources are two decisions. Choosing between them here would be the
	// command picking a source, which is the one thing this route must not do.
	if len(opts.sources) > 1 {
		return registryRecoveryOptions{}, usageError("reconcile registry accepts exactly one --source occurrence; name one recovery source")
	}
	if len(opts.sources) == 1 && strings.TrimSpace(opts.sources[0]) == "" {
		return registryRecoveryOptions{}, usageError("reconcile registry --source requires a non-empty recovery copy name or absolute path")
	}
	if err := validateRecoveryChecksumFlag("--expect-source-checksum", opts.expectSource); err != nil {
		return registryRecoveryOptions{}, err
	}
	if err := validateRecoveryChecksumFlag("--expect-current-checksum", opts.expectCurrent); err != nil {
		return registryRecoveryOptions{}, err
	}
	// A guard without a source guards nothing, and silently ignoring it would
	// let an operator believe a restore was checked when it was not.
	if len(opts.sources) == 0 && (strings.TrimSpace(opts.expectSource) != "" || strings.TrimSpace(opts.expectCurrent) != "") {
		return registryRecoveryOptions{}, usageError("reconcile registry checksum guards require --source")
	}
	opts.expectSource = strings.TrimSpace(opts.expectSource)
	opts.expectCurrent = strings.TrimSpace(opts.expectCurrent)
	return opts, nil
}

// validateRecoveryChecksumFlag refuses a malformed guard before any read. A
// typo'd digest that fell through would compare unequal and be reported as a
// race, which sends the operator looking for a concurrent writer that does not
// exist.
func validateRecoveryChecksumFlag(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	hex, ok := strings.CutPrefix(trimmed, "sha256:")
	if !ok || len(hex) != 64 {
		return usageError(fmt.Sprintf("%s must be a sha256:<64 hex characters> digest as printed by the preview", name))
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return usageError(fmt.Sprintf("%s must be a sha256:<64 hex characters> digest as printed by the preview", name))
		}
	}
	return nil
}

func writeRegistryRecoveryReport(w io.Writer, output string, report registryRecoveryReport) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(w, "mode: %s\noutcome: %s\nselection: %s\nregistry: %s\ncurrent: %s%s\nmarker: %s (initialized=%t)\nrecovery: %s (retention=%d)\n",
		report.Mode, report.Outcome, report.Selection, report.RegistryPath,
		report.Current.State, registryRecoveryDetailSuffix(report.Current),
		report.MarkerPath, report.Initialized, report.RecoveryDir, report.Retention); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "sources: %d candidate(s)\n", len(report.Sources)); err != nil {
		return err
	}
	for _, source := range report.Sources {
		if _, err := fmt.Fprintln(w, "- "+formatRegistryRecoverySource(source)); err != nil {
			return err
		}
	}
	if report.Selected != nil {
		if _, err := fmt.Fprintln(w, "selected: "+formatRegistryRecoverySource(*report.Selected)); err != nil {
			return err
		}
	}
	if report.Restore != nil {
		if _, err := fmt.Fprintf(w, "restored: %s changed=%t contents=%s\n", report.Restore.SourcePath, report.Restore.Changed, formatRegistryRecoveryContents(report.Restore.Contents)); err != nil {
			return err
		}
		if report.Restore.PreservedPath != "" {
			if _, err := fmt.Fprintf(w, "preserved: %s (%s bytes were %s)\n", report.Restore.PreservedPath, report.Restore.PreservedChecksum, report.Restore.ReplacedState); err != nil {
				return err
			}
		}
	}
	if report.Mirror != nil {
		if err := writeRegistryRecoveryMirror(w, *report.Mirror); err != nil {
			return err
		}
	}
	if report.Next != "" {
		if _, err := fmt.Fprintln(w, "next: "+report.Next); err != nil {
			return err
		}
	}
	if report.Error != "" {
		_, err := fmt.Fprintln(w, "error: "+report.Error)
		return err
	}
	return nil
}

func writeRegistryRecoveryMirror(w io.Writer, mirror registryRecoveryMirrorReport) error {
	if _, err := fmt.Fprintf(w, "mirror: available=%t", mirror.Available); err != nil {
		return err
	}
	if mirror.Target != "" {
		if _, err := fmt.Fprintf(w, " target=%s", mirror.Target); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\nmirror reason: %s\n", mirror.Reason); err != nil {
		return err
	}
	if mirror.Available {
		if _, err := fmt.Fprintf(w, "mirror recoverable: projects=%d windows=%d panes=%d agent-panes=%d\n",
			mirror.Counts.Projects, mirror.Counts.Windows, mirror.Counts.Panes, mirror.Counts.AgentPanes); err != nil {
			return err
		}
		for _, fragment := range mirror.Recoverable {
			line := fmt.Sprintf("- %s %s", fragment.Kind, fragment.UID)
			if fragment.Name != "" {
				line += " name=" + fragment.Name
			}
			line += " target=" + fragment.Target
			if fragment.ContainerUID != "" {
				line += " in=" + fragment.ContainerUID
			}
			if fragment.AgentProvider != "" {
				line += " agent-provider=" + fragment.AgentProvider
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, "mirror unrecoverable:"); err != nil {
		return err
	}
	for _, gap := range mirror.Unrecoverable {
		if _, err := fmt.Fprintf(w, "- %s: %s\n", gap.Scope, gap.Reason); err != nil {
			return err
		}
	}
	return nil
}

func formatRegistryRecoverySource(source intmetadata.RecoverySource) string {
	eligibility := "rejected"
	if source.Eligible {
		eligibility = "eligible"
	}
	line := fmt.Sprintf("%s [%s] %s %s", source.Name, source.Kind, eligibility, source.State)
	if source.Eligible {
		line += " contents=" + formatRegistryRecoveryContents(source.Contents)
	}
	if source.Checksum != "" {
		line += " " + source.Checksum
	}
	if source.ModifiedAt != "" {
		line += " modified=" + source.ModifiedAt
	}
	if !source.Eligible && source.Reason != "" {
		line += " (" + source.Reason + ")"
	}
	return line
}

func formatRegistryRecoveryContents(contents intmetadata.RegistryContents) string {
	return fmt.Sprintf("projects=%d/windows=%d/panes=%d/agents=%d/reservations=%d",
		contents.Projects, contents.Windows, contents.Panes, contents.Agents, contents.Reservations)
}

func registryRecoveryDetailSuffix(info intmetadata.RegistryFileInfo) string {
	suffix := ""
	if info.Checksum != "" {
		suffix += " " + info.Checksum
	}
	if info.State == intmetadata.RegistryStateValid {
		return suffix + " contents=" + formatRegistryRecoveryContents(info.Contents)
	}
	if info.Detail != "" {
		suffix += " (" + info.Detail + ")"
	}
	return suffix
}

func printRegistryRecoveryUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]")
}
