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

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

type resourceReconcileCommand struct {
	runner          tmuxCommandRunner
	resources       *resourceStore
	lookupEnv       func(string) string
	newReconciler   func(tmuxCommandRunner, sessionLister) *registryReconciler
	newOperationID  func() (string, error)
	newGeneration   func() (string, error)
	newMaterializer func(tmuxCommandRunner, io.Writer) *materializer
	// registry is the sibling recovery boundary. `reconcile` dispatches to it
	// rather than owning recovery here: the resource planner needs a loadable
	// Registry as its input, and recovery is what runs when that input is the
	// thing that is broken.
	registry *registryRecoveryCommand
}

func newResourceReconcileCommand(tmux *tmuxCommand) *resourceReconcileCommand {
	command := &resourceReconcileCommand{
		lookupEnv: os.Getenv, newOperationID: newCreateOperationID, newGeneration: coremetadata.NewGeneration,
		registry: newRegistryRecoveryCommand(tmux),
	}
	if tmux != nil {
		command.runner = tmux.runner
		command.resources = tmux.resources
		command.newReconciler = tmux.bindingReconciler
	}
	return command
}

type resourceReconcileOptions struct {
	dryRun              bool
	output              string
	socket              string
	socketPath          string
	materializeProjects repeatedFlag
}

type resourceReconcileTarget struct {
	Mode  string `json:"mode"`
	Flag  string `json:"tmuxFlag"`
	Value string `json:"value"`
}

type resourceReconcileCounts struct {
	Changed int `json:"changed"`
	NoOp    int `json:"noOp"`
	Failed  int `json:"failed"`
}

type resourceReconcileReport struct {
	Target resourceReconcileTarget `json:"target"`
	DryRun bool                    `json:"dryRun"`
	// HostMode is which of the two supported hosts the exact socket turned out
	// to be. It is reported because the same Registry produces the same managed
	// rows on both, and an operator debugging a refusal needs to know which
	// host answered.
	HostMode        string                  `json:"hostMode,omitempty"`
	Outcome         string                  `json:"outcome"`
	Counts          resourceReconcileCounts `json:"counts"`
	Items           []resourceReconcileItem `json:"items"`
	CompletedStages []string                `json:"completedStages"`
	RemainingDrift  []resourceReconcileItem `json:"remainingDrift"`
	// Policy is the subset of the controller's authority table this run
	// exercised. It is what turns "nothing was started" from a claim into
	// evidence.
	Policy []controller.Verdict `json:"policy,omitempty"`
	// Reobserved is the post-execute replan against fresh bytes. It is absent
	// on a dry-run and on a run that changed nothing, because there is then
	// nothing a reobservation could have discovered.
	Reobserved *controllerReobservation `json:"reobserved,omitempty"`
	Retry      string                   `json:"retry,omitempty"`
	Error      string                   `json:"error,omitempty"`
}

func (c *resourceReconcileCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "registry" {
		if c.registry == nil {
			return errors.New("registry recovery is not configured")
		}
		return c.registry.Run(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "resources" {
		printResourceReconcileUsage(stderr)
		printRegistryRecoveryUsage(stderr)
		return usageError("reconcile requires the resources or registry subcommand")
	}
	opts, err := parseResourceReconcileOptions(args[1:], stderr)
	if err != nil {
		return err
	}
	target, err := c.resolveTarget(opts)
	if err != nil {
		return err
	}
	if c.runner == nil || c.resources == nil {
		return errors.New("resource reconciliation is not configured")
	}
	reportTarget := resourceReconcileTarget{Mode: "socket-name", Flag: target.flag, Value: target.value}
	if target.flag == "-S" {
		reportTarget.Mode = "socket-path"
	}
	planner := resourceReconcilePlanner{
		reader:             explicitTmuxRunner{runner: c.runner, target: target},
		store:              c.resources,
		newReconciler:      c.newReconciler,
		materializeProject: firstRepeatedValue(opts.materializeProjects),
		exactTarget:        target,
	}
	ctx := context.Background()
	// Explicit topology materialization keeps its own engine. It activates
	// resources, and activation is exactly the authority this kernel refuses to
	// hold, so routing it through the policy table would either weaken the table
	// or break the materializer.
	if planner.materializeProject != "" {
		if opts.dryRun {
			return c.runDryRun(ctx, planner, reportTarget, opts, stdout)
		}
		return c.runMaterializeExecute(ctx, planner, target, reportTarget, opts, stdout, stderr)
	}
	kernel := newResourceControllerKernel(c.runner, c.resources, planner, target)
	if opts.dryRun {
		return c.runControllerDryRun(ctx, kernel, reportTarget, opts, stdout)
	}
	return c.runControllerExecute(ctx, kernel, reportTarget, opts, stdout)
}

func parseResourceReconcileOptions(args []string, stderr io.Writer) (resourceReconcileOptions, error) {
	var opts resourceReconcileOptions
	fs := flag.NewFlagSet("reconcile resources", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.dryRun, "dry-run", false, "preview drift and writes without mutation")
	fs.StringVar(&opts.output, "output", "", "output mode: json")
	fs.StringVar(&opts.output, "o", "", "output mode (alias of --output)")
	fs.StringVar(&opts.socket, "socket", "", "exact tmux socket name (tmux -L)")
	fs.StringVar(&opts.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S)")
	fs.Var(&opts.materializeProjects, "materialize-project", "materialize one exact Project by name or uid:<uid>")
	if err := fs.Parse(args); err != nil {
		return resourceReconcileOptions{}, err
	}
	if fs.NArg() != 0 {
		return resourceReconcileOptions{}, usageError("reconcile resources does not accept positional arguments")
	}
	opts.output = strings.TrimSpace(strings.ToLower(opts.output))
	if opts.output != "" && opts.output != "json" {
		return resourceReconcileOptions{}, usageError(fmt.Sprintf("unsupported reconcile resources output %q; use json", opts.output))
	}
	if strings.TrimSpace(opts.socket) != "" && strings.TrimSpace(opts.socketPath) != "" {
		return resourceReconcileOptions{}, usageError("reconcile resources accepts only one of --socket and --socket-path")
	}
	if len(opts.materializeProjects) > 1 {
		return resourceReconcileOptions{}, usageError("reconcile resources accepts exactly one --materialize-project occurrence")
	}
	// A present-but-blank selector must not silently degrade into the broad
	// default reconcile. The caller asked for one scoped Project; an empty
	// value names none, so refuse before any read, transaction, or write.
	if len(opts.materializeProjects) == 1 && strings.TrimSpace(opts.materializeProjects[0]) == "" {
		return resourceReconcileOptions{}, usageError("reconcile resources --materialize-project requires a non-empty Project name or uid:<uid>")
	}
	return opts, nil
}

func firstRepeatedValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func (c *resourceReconcileCommand) resolveTarget(opts resourceReconcileOptions) (explicitTmuxTarget, error) {
	if strings.TrimSpace(opts.socket) != "" {
		target, err := tmuxSocketNameTarget(opts.socket)
		if err != nil {
			return explicitTmuxTarget{}, usageError("reconcile resources: " + err.Error())
		}
		return target, nil
	}
	if strings.TrimSpace(opts.socketPath) != "" {
		target, err := tmuxSocketPathTarget(opts.socketPath)
		if err != nil {
			return explicitTmuxTarget{}, usageError("reconcile resources: --socket-path must be absolute")
		}
		return target, nil
	}
	tmuxEnv := ""
	if c.lookupEnv != nil {
		tmuxEnv = strings.TrimSpace(c.lookupEnv("TMUX"))
	}
	inherited, _, _ := strings.Cut(tmuxEnv, ",")
	if inherited == "" || !filepath.IsAbs(inherited) {
		return explicitTmuxTarget{}, usageError("reconcile resources requires --socket <name> or --socket-path <absolute> outside tmux")
	}
	target, err := tmuxSocketPathTarget(inherited)
	if err != nil {
		return explicitTmuxTarget{}, usageError("reconcile resources: inherited $TMUX socket path is not absolute")
	}
	return target, nil
}

func (c *resourceReconcileCommand) runDryRun(ctx context.Context, planner resourceReconcilePlanner, target resourceReconcileTarget, opts resourceReconcileOptions, stdout io.Writer) error {
	load := c.resources.snapshot
	if load == nil {
		load = c.resources.load
	}
	if load == nil {
		return errors.New("resource reconciliation read store is not configured")
	}
	registry, err := load()
	if err != nil {
		return MapMetadataError(err)
	}
	plan, err := planner.build(ctx, registry)
	if err != nil {
		return fmt.Errorf("plan resource reconciliation: %w", err)
	}
	report := reportForDryRun(plan, target, retryResourceReconcileProject(target, planner.materializeProject))
	return writeResourceReconcileReport(stdout, opts.output, report)
}

// runControllerDryRun is the observe-plan half of the kernel with the write
// half deliberately absent. It reads the Registry, resolves one graph, plans,
// applies the policy, and returns the same projection an execute produces.
func (c *resourceReconcileCommand) runControllerDryRun(ctx context.Context, kernel *resourceControllerKernel, target resourceReconcileTarget, opts resourceReconcileOptions, stdout io.Writer) error {
	registry, err := kernel.loadRegistry()
	if err != nil {
		return MapMetadataError(err)
	}
	pass, err := kernel.plan(ctx, registry)
	if err != nil {
		return fmt.Errorf("plan resource reconciliation: %w", err)
	}
	report := reportForDryRun(pass.registry, target, retryResourceReconcile(target))
	applyControllerProjection(&report, pass.plan)
	return writeResourceReconcileReport(stdout, opts.output, report)
}

// runControllerExecute reports one kernel convergence.
//
// The stage sequence itself is the kernel's (see resourceControllerKernel.
// converge). What is left here is what only this route owns: the operator-facing
// report, the retry hint, and the refused-drift exit code. Keeping the sequence
// out of the report body is what lets a lifecycle trigger reach the same six
// stages without reimplementing five of them and forgetting the sixth.
func (c *resourceReconcileCommand) runControllerExecute(ctx context.Context, kernel *resourceControllerKernel, reportTarget resourceReconcileTarget, opts resourceReconcileOptions, stdout io.Writer) error {
	if c.resources.updateConvergent == nil {
		return errors.New("resource reconciliation write store is not configured")
	}
	run, err := kernel.converge(ctx)
	if err != nil {
		var runErr *controllerRunError
		if !errors.As(err, &runErr) {
			return err
		}
		if runErr.stage == "registry read" {
			return runErr.err
		}
		remaining, replanErr := c.replanAfterFailure(ctx, kernel.planner)
		report := reportForFailure(runErr.run.plan, remaining, reportTarget, runErr.run.completed, runErr.stage,
			retryResourceReconcile(reportTarget), runErr.err, replanErr)
		applyControllerProjection(&report, runErr.run.authorized)
		if writeErr := writeResourceReconcileReport(stdout, opts.output, report); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("reconcile resources failed at %s: %w", runErr.stage, runErr.err)
	}
	report := reportForExecute(run.plan, reportTarget, run.completed, retryResourceReconcile(reportTarget))
	applyControllerProjection(&report, run.authorized)
	report.Reobserved = run.reobserved
	if err := writeResourceReconcileReport(stdout, opts.output, report); err != nil {
		return err
	}
	if run.plan.refusedItems() > 0 {
		return fmt.Errorf("reconcile resources left %d refused drift item(s); Registry identity remains authoritative", run.plan.refusedItems())
	}
	return nil
}

// applyControllerProjection copies the kernel's own facts onto the report so
// human and JSON output read from one value.
func applyControllerProjection(report *resourceReconcileReport, plan controller.Plan) {
	if plan.HostMode != "" && plan.HostMode != resourcegraph.HostModeUnknown {
		report.HostMode = string(plan.HostMode)
	}
	report.Policy = plan.Policy
}

func controllerReobserveStage(reobserved controllerReobservation) string {
	switch {
	case reobserved.Unavailable != "":
		return "reobserved: unavailable (" + reobserved.Unavailable + ")"
	case reobserved.Converged:
		return "reobserved: converged"
	default:
		return fmt.Sprintf("reobserved: %d residual item(s)", len(reobserved.Residual))
	}
}

// validateResourcePlanWrites proves every UID-bearing live target still has
// the value the locked plan observed. Validation is all-or-nothing and runs
// before the first mirror write, so a recycled tmux handle cannot redirect one
// plan item onto an unrelated object after the Registry commit.
//
// It is the explicit-materialization engine's guard. The reconcile route uses
// the controller kernel's graph-derived guards instead: materialization plans
// against objects it is about to create, which the observation cannot have seen,
// so the two cannot share one evidence source.
func validateResourcePlanWrites(ctx context.Context, routed explicitTmuxRunner, writes []plannedTmuxWrite) error {
	for _, write := range writes {
		if write.guardField == "" {
			continue
		}
		out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", write.target, "-F", "#{"+write.guardField+"}")
		if err != nil {
			return fmt.Errorf("revalidate exact tmux target %s: %w", write.target, err)
		}
		observed := strings.TrimSpace(string(out))
		if observed != strings.TrimSpace(write.guardBefore) {
			return fmt.Errorf("exact tmux target %s changed before repair: %s is %q, planned from %q", write.target, write.guardField, observed, write.guardBefore)
		}
	}
	return nil
}

func (c *resourceReconcileCommand) replanAfterFailure(ctx context.Context, planner resourceReconcilePlanner) (resourceReconcilePlan, error) {
	load := c.resources.snapshot
	if load == nil {
		load = c.resources.load
	}
	if load == nil {
		return resourceReconcilePlan{}, errors.New("resource reconciliation read store is not configured")
	}
	registry, err := load()
	if err != nil {
		return resourceReconcilePlan{}, err
	}
	return planner.build(ctx, registry)
}

func reportForDryRun(plan resourceReconcilePlan, target resourceReconcileTarget, retry string) resourceReconcileReport {
	report := resourceReconcileReport{Target: target, DryRun: true, Outcome: "planned", Items: clonePlanItems(plan.items), Retry: retry}
	report.Counts.Changed = plan.safeItems()
	report.Counts.Failed = plan.refusedItems()
	if len(plan.items) == 0 {
		report.Outcome = "no-op"
		report.Counts.NoOp = 1
		report.Retry = ""
	}
	for _, item := range report.Items {
		if item.refused {
			report.RemainingDrift = append(report.RemainingDrift, item)
		}
	}
	return report
}

func reportForExecute(plan resourceReconcilePlan, target resourceReconcileTarget, completed []string, retry string) resourceReconcileReport {
	items := clonePlanItems(plan.items)
	changed := 0
	failed := 0
	var remaining []resourceReconcileItem
	for index := range items {
		if items[index].refused {
			items[index].Outcome = "refused"
			remaining = append(remaining, items[index])
			failed++
			continue
		}
		items[index].Outcome = "changed"
		changed++
	}
	outcome := "changed"
	noOp := 0
	if changed == 0 && failed == 0 {
		outcome, noOp, retry = "no-op", 1, ""
	} else if failed > 0 {
		outcome = "partial"
	}
	return resourceReconcileReport{
		Target: target, Outcome: outcome, Counts: resourceReconcileCounts{Changed: changed, NoOp: noOp, Failed: failed},
		Items: items, CompletedStages: completed, RemainingDrift: remaining, Retry: retry,
	}
}

func reportForFailure(plan, remaining resourceReconcilePlan, target resourceReconcileTarget, completed []string, failedStage, retry string, updateErr, replanErr error) resourceReconcileReport {
	items := clonePlanItems(plan.items)
	completedSet := map[string]bool{}
	for _, stage := range completed {
		completedSet[stage] = true
	}
	changed := 0
	for index := range items {
		switch {
		case completedSet[items[index].Key]:
			items[index].Outcome = "changed"
			changed++
		case items[index].Key == failedStage:
			items[index].Outcome = "failed"
		default:
			items[index].Outcome = "pending"
		}
	}
	remainingItems := clonePlanItems(remaining.items)
	for index := range remainingItems {
		remainingItems[index].Outcome = "remaining"
	}
	errorText := updateErr.Error()
	if replanErr != nil {
		errorText += "; remaining drift unavailable: " + replanErr.Error()
	}
	return resourceReconcileReport{
		Target: target, Outcome: "failed", Counts: resourceReconcileCounts{Changed: changed, Failed: 1}, Items: items,
		CompletedStages: completed, RemainingDrift: remainingItems, Retry: retry, Error: errorText,
	}
}

func clonePlanItems(items []resourceReconcileItem) []resourceReconcileItem {
	out := make([]resourceReconcileItem, len(items))
	copy(out, items)
	return out
}

func retryResourceReconcile(target resourceReconcileTarget) string {
	return retryResourceReconcileProject(target, "")
}

func retryResourceReconcileProject(target resourceReconcileTarget, projectRef string) string {
	flagName := "--socket"
	if target.Flag == "-S" {
		flagName = "--socket-path"
	}
	retry := "projmux reconcile resources " + flagName + " " + shellQuote(target.Value)
	if strings.TrimSpace(projectRef) != "" {
		retry += " --materialize-project " + shellQuote(projectRef)
	}
	return retry
}

func writeResourceReconcileReport(w io.Writer, output string, report resourceReconcileReport) error {
	if output == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(w, "target: tmux %s %s\nmode: %s\noutcome: %s\ncounts: changed=%d no-op=%d failed=%d\n",
		report.Target.Flag, report.Target.Value, reconcileReportMode(report), report.Outcome,
		report.Counts.Changed, report.Counts.NoOp, report.Counts.Failed); err != nil {
		return err
	}
	if len(report.Items) > 0 {
		if _, err := fmt.Fprintln(w, "items:"); err != nil {
			return err
		}
		for _, item := range report.Items {
			if _, err := fmt.Fprintln(w, formatResourceReconcileItem(item)); err != nil {
				return err
			}
		}
	}
	if len(report.CompletedStages) > 0 {
		if _, err := fmt.Fprintln(w, "completed stages:"); err != nil {
			return err
		}
		for _, stage := range report.CompletedStages {
			if _, err := fmt.Fprintln(w, "- "+stage); err != nil {
				return err
			}
		}
	}
	if len(report.RemainingDrift) > 0 {
		if _, err := fmt.Fprintf(w, "remaining drift: %d item(s)\n", len(report.RemainingDrift)); err != nil {
			return err
		}
		for _, item := range report.RemainingDrift {
			if _, err := fmt.Fprintf(w, "- %s %s\n", item.Key, strings.TrimPrefix(formatResourceReconcileItem(item), "- ")); err != nil {
				return err
			}
		}
	}
	if report.Retry != "" {
		if _, err := fmt.Fprintln(w, "retry: "+report.Retry); err != nil {
			return err
		}
	}
	if report.Error != "" {
		_, err := fmt.Fprintln(w, "error: "+report.Error)
		return err
	}
	return nil
}

func formatResourceReconcileItem(item resourceReconcileItem) string {
	line := fmt.Sprintf("- %s [%s] %s %s %s", item.Outcome, item.Drift, item.Surface, item.Action, item.Target)
	if item.Field != "" {
		line += " " + item.Field
	}
	if item.Before != "" || item.After != "" {
		line += " " + displayPlanValue(item.Before) + " -> " + displayPlanValue(item.After)
	}
	if item.Reason != "" {
		line += " (" + item.Reason + ")"
	}
	return line
}

func reconcileReportMode(report resourceReconcileReport) string {
	if report.DryRun {
		return "dry-run"
	}
	return "execute"
}

func displayPlanValue(value string) string {
	if value == "" {
		return "<missing>"
	}
	return value
}

func printResourceReconcileUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]")
}
