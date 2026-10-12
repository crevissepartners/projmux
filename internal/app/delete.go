package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// deleteKinds lists the kind spellings `delete` implements, in help order, each
// canonical token followed by its accepted aliases. See getKinds.
var deleteKinds = cli.ChildSpellings("delete")

// deleteRegistryKinds are the kinds this verb deletes out of the resource
// registry. `notification` is a parity alias over the existing queue handler
// instead.
var deleteRegistryKinds = map[string]coremetadata.Kind{
	"project": coremetadata.KindProject,
	"window":  coremetadata.KindWindow,
	"pane":    coremetadata.KindPane,
	"agent":   coremetadata.KindAgent,
}

// deleteCommand implements the canonical `delete` verb.
//
// Every registry-backed delete is atomic in the sense the contract requires: the
// full target uid set and the whole descendant plan are resolved before anything
// is removed, the plan is re-derived inside the store transaction and compared
// against the approved one, and any mismatch or validation failure aborts the
// transaction with zero mutations.
//
// An omitted selector no longer means the whole registry. `projmux delete pane`
// with nothing else on the argv used to resolve every Pane and delete all of
// them, in and out of tmux alike, because the declared 1..N cell is satisfied by
// any match and an empty query matches everything. It now addresses the active
// tmux Pane through the shared active-target seam, and refuses when no active
// target resolves. The whole-registry fan-out survives only under its own
// explicit flag; see deleteWholeSetFlag.
type deleteCommand struct {
	store        *resourceStore
	confirm      *confirmer
	notify       rawArgvCommand
	resolveKinds map[string]coremetadata.Kind
	// activeTarget is the empty-selector fallback seam; see active_target.go.
	activeTarget activeTargetLookup
	// Windows and Panes have exact live tmux halves. Agent runtime is represented
	// only by the managed Panes owned by its Registry resource.
	windows windowDeleteRuntime
	panes   paneDeleteRuntime
	// lookupEnv reads the inherited $TMUX that routes this delete's live half
	// when no socket flag was given.
	lookupEnv func(string) string
	// newOperationID labels the pre-mutation intentional termination receipt.
	newOperationID func() (string, error)
	// routeAnchor is private producer evidence for generated pane-menu delete.
	// It is reobserved with the inherited socket/pid instead of being written
	// into the process-global TMUX_PANE environment.
	routeAnchor string
	// via is the route that reached this delete, for its deletion record. The
	// zero value is the CLI; the generated UI keys set deletionViaUI.
	via deletionVia
	// processAncestors and actorRunner are the deletion record's pane-chain
	// actor seams: this process's parent chain, and the tmux runner that
	// confirms the ambient Pane on this delete's own route. Either nil leaves
	// the actor unjudged.
	processAncestors func() ([]int, error)
	actorRunner      tmuxCommandRunner
	// processDeleter is the process runtime Agent route; nil uses production.
	processDeleter *processAgentDeleter
}

func newDeleteCommand() *deleteCommand {
	return &deleteCommand{
		store:            newResourceStore(),
		confirm:          newConfirmer(),
		resolveKinds:     deleteRegistryKinds,
		activeTarget:     defaultActiveTargetLookup(),
		windows:          newTmuxWindowDeleteRuntime(),
		panes:            newTmuxPaneDeleteRuntime(),
		lookupEnv:        os.Getenv,
		newOperationID:   newCreateOperationID,
		processAncestors: processAncestry,
		actorRunner:      inttmux.ExecRunner{},
	}
}

// deletionVia is the record's route; the zero value is the CLI.
func (c *deleteCommand) deletionVia() deletionVia {
	if c.via == "" {
		return deletionViaCLI
	}
	return c.via
}

// observeDeletionActor judges this delete's actor on route against the
// pre-commit working Registry.
func (c *deleteCommand) observeDeletionActor(route tmuxTransport, working *coremetadata.Registry) DeletionActor {
	return observeDeletionActor(context.Background(), c.deletionVia(), c.lookupEnv, c.processAncestors, c.actorRunner, route, working)
}

// deleteWholeSetFlag is the explicit whole-registry spelling of the destructive
// verb, and the only way to ask for the fan-out an omitted selector used to
// perform.
//
// It is a flag rather than a value token on purpose. selector.ParseRef treats
// any non-`uid:` token as a bare metadata.name and metadata.ValidateName
// reserves only `.` and `..`, so `all`, `current`, and `active` are all legal
// resource names today: `projmux delete pane all` has to keep meaning the Pane
// literally named `all`, and a sentinel token would silently widen it to the
// registry. A flag lives in a namespace the resource names cannot reach.
// `--all` is also the spelling kubectl already established for exactly this
// operation, so the muscle memory transfers.
//
// kubectl's vocabulary splits along the scope boundary: `--all` is
// all-within-the-current-namespace and `--all-namespaces` is the one that
// crosses the boundary. `--all` here is the first of those two, and it reaches
// the whole registry only because the registry is the one and only scope projmux
// has -- there is nothing narrower for it to be bounded by. That is why every
// string this route prints says "in the registry" instead of a bare "all": if a
// narrower default scope is ever introduced, this flag's meaning has to be
// re-adjudicated in the open rather than quietly re-read as "all within the new
// scope" while the code still deletes registry-wide.
const deleteWholeSetFlag = "--all"

// Run dispatches one `delete <kind>` invocation.
func (c *deleteCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("delete requires a resource kind: %s", strings.Join(deleteKinds, ", ")))
	}
	// Normalizing the alias first is what keeps the destructive verb's plural
	// spelling from being a second implementation: `delete panes` reaches the
	// same cascade planner, the same confirmation prompt, and the same
	// --all containment as `delete pane`, because by this line it *is*
	// `delete pane`.
	token, ok := cli.CanonicalChildToken("delete", args[0])
	if !ok {
		return usageError(fmt.Sprintf("delete %s is not available; this release implements: %s",
			args[0], strings.Join(deleteKinds, ", ")))
	}
	switch token {
	case "notification":
		return forwardRawArgv(c.notify, "delete notification", "notify", []string{"ack"}, args[1:], stdout, stderr)
	}
	kind, ok := c.resolveKinds[token]
	if !ok {
		return usageError(fmt.Sprintf("delete %s is not available; this release implements: %s",
			args[0], strings.Join(deleteKinds, ", ")))
	}
	return c.runKind(deleteVerbDelete, token, kind, args[1:], stdout, stderr)
}

// The two verbs that reach the Registry-only Project removal. They are two
// spellings of one implementation, not two operations: the canonical
// `unregister project` and the deprecated `delete project` share this file's
// planner, confirmation, transaction, and receipt, and differ only in the
// compatibility warning the deprecated one adds.
const (
	deleteVerbDelete     = "delete"
	deleteVerbUnregister = "unregister"
)

// unregisterCommand implements the canonical `unregister project`.
//
// It exists as a separate top-level verb rather than another `delete` child
// because the defect it fixes is exactly that `delete` promised one cascade
// meaning across four kinds and delivered two: `delete window|pane|agent` kill
// an exact live mirror, and `delete project` never has. Naming the Registry-only
// half is what makes both halves readable without a footnote.
type unregisterCommand struct {
	delete *deleteCommand
}

func (c *unregisterCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("unregister requires a resource kind: project")
	}
	token, ok := cli.CanonicalChildToken(deleteVerbUnregister, args[0])
	if !ok || token != "project" {
		return usageError(fmt.Sprintf("unregister %s is not available; this release implements: project", args[0]))
	}
	if c.delete == nil {
		return errors.New("unregister project: the Registry removal executor is not configured")
	}
	return c.delete.runKind(deleteVerbUnregister, token, coremetadata.KindProject, args[1:], stdout, stderr)
}

// deleteDescendant is one resource the cascade removes together with a target.
type deleteDescendant = mutations.DeleteDescendant
type deleteTarget = mutations.DeleteTarget

func (c *deleteCommand) runKind(verb, token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := verb + " " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	flags := resourceQueryFlags{kind: kind}
	flags.register(fs)
	dryRun := fs.Bool("dry-run", false, "print the full target and cascade plan without deleting anything")
	yes := fs.Bool("yes", false, "skip the interactive confirmation")
	socket := fs.String("socket", "", "exact tmux socket name (tmux -L) the live half of this delete addresses")
	socketPath := fs.String("socket-path", "", "exact absolute tmux socket path (tmux -S) the live half of this delete addresses")
	all := fs.Bool("all", false,
		"delete every "+strings.ToLower(string(kind))+" in the registry, the only scope projmux has today; "+
			"it is the sole way to fan out without a selector")
	refs, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	for _, ref := range refs {
		flags.addPositionalRef(ref)
	}

	// The empty selector is the only argv shape whose meaning this route
	// decides. Anything the operator typed -- a positional ref, a
	// --project/--window/--pane scope, a --selector label -- keeps its exact
	// historical meaning, so `--all` on top of one of those would be either a
	// no-op or a second, contradictory answer to a question already answered.
	// Refusing is the only reading that cannot surprise anyone.
	implicit := flags.selectorIsEmpty()
	switch {
	case *all && !implicit:
		return usageError(fmt.Sprintf("%s: %s addresses every %s in the registry and cannot be combined with a selector",
			spelling, deleteWholeSetFlag, strings.ToLower(string(kind))))
	case *all:
		// Restore the pre-containment path exactly: no fallback is consulted and
		// the empty query fans out over the whole registry, which is what the
		// operator just asked for in writing.
	default:
		flags.active = c.activeTarget
		flags.wholeSetFlag = deleteWholeSetFlag
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	resolution, err := flags.resolve(selector.VerbDelete, false, registry)
	if err != nil {
		return MapMetadataError(err)
	}
	// A 1..N route is satisfied by one match, so a fan-out over several explicit
	// references would otherwise delete the ones that resolve and silently drop
	// the ones that do not. That is the partial outcome the preflight contract
	// forbids, so every named reference has to address something.
	unmatched, err := flags.unmatchedTargetRefs(registry)
	if err != nil {
		return MapMetadataError(err)
	}
	if len(unmatched) > 0 {
		return usageError(fmt.Sprintf("%s: %s matched no %ss; the whole target set must resolve before anything is deleted",
			spelling, strings.Join(unmatched, ", "), strings.ToLower(string(kind))))
	}
	plan := buildDeletePlan(registry, kind, resolution)
	if err := mutations.ValidateDeleteProtection(registry, mutations.DeletePlan(plan)); err != nil {
		return err
	}
	plan.Unnamed = implicit
	plan.Implicit = implicit && !*all
	plan.ExactUID = explicitUIDTargetRefs(flags.targetRefs())
	if kind == coremetadata.KindProject {
		// Project unregister has no live half, so its route is resolved only to
		// judge the deletion record's actor, and an unresolvable one is simply
		// unproven rather than a refusal.
		actorRoute := lenientDeletionRoute(deleteSocketFlags{socket: *socket, socketPath: *socketPath}, c.lookupEnv)
		return c.runProjectUnregister(verb, spelling, plan, resolution, actorRoute, *dryRun, *yes, stdout, stderr)
	}

	if isVirtualDeletePlan(registry, plan) {
		return c.runVirtualDelete(spelling, plan, deleteSocketFlags{socket: *socket, socketPath: *socketPath}, *dryRun, *yes, stdout, stderr)
	}

	// Process runtime Agents have no tmux half, so their route needs no server.
	if isProcessAgentPlan(registry, plan) {
		return c.runProcessAgentDelete(spelling, plan, deleteSocketFlags{socket: *socket, socketPath: *socketPath}, *dryRun, *yes, stdout, stderr)
	}

	target, err := resolveDeleteTarget(spelling, deleteSocketFlags{socket: *socket, socketPath: *socketPath}, c.lookupEnv)
	if err != nil {
		return err
	}

	var livePlan windowLiveDeletePlan
	var panePlan paneLiveDeletePlan
	if kind == coremetadata.KindWindow {
		if c.windows == nil {
			return errors.New("delete window: live tmux deletion is not configured")
		}
		c.windows.UseExactTarget(target)
		livePlan, err = c.windows.Preflight(context.Background(), registry, plan)
		if err != nil {
			return err
		}
	} else {
		if c.panes == nil {
			return fmt.Errorf("delete %s: live tmux deletion is not configured", token)
		}
		c.panes.useExactTarget(target)
		if anchored, ok := c.panes.(interface{ UseRouteAnchor(string) }); ok {
			anchored.UseRouteAnchor(c.routeAnchor)
		}
		panePlan, err = c.panes.preflight(context.Background(), registry, plan)
		if err != nil {
			return err
		}
	}

	if *dryRun {
		return writeDeletePlan(stdout, spelling, plan, livePlan, panePlan, target, true, false)
	}
	needsConfirmation := mutationruntime.NeedsDeleteConfirmation(plan, panePlan)
	if needsConfirmation {
		prompt := fmt.Sprintf("%s will remove %d %s and %d descendant resources",
			spelling, len(plan.Targets), strings.ToLower(string(kind))+plural(len(plan.Targets)), plan.Cascades())
		if kind == coremetadata.KindWindow {
			prompt += fmt.Sprintf(", kill %d exact live tmux Window%s, and end %d managed root session%s",
				len(livePlan.Targets), plural(len(livePlan.Targets)), livePlan.EndsSessions(), plural(livePlan.EndsSessions()))
		} else {
			prompt += fmt.Sprintf(", kill %d exact live tmux Pane%s, end %d Window%s, and end %d managed root session%s",
				len(panePlan.Targets), plural(len(panePlan.Targets)), panePlan.EndsWindows(), plural(panePlan.EndsWindows()),
				panePlan.EndsSessions(), plural(panePlan.EndsSessions()))
		}
		refusal := fmt.Sprintf("%s needs confirmation: %d targets and %d descendant resources. Re-run with --yes, or with --dry-run to review the plan first.",
			spelling, len(plan.Targets), plan.Cascades())
		if err := c.confirm.confirm(*yes, prompt, refusal, stdout); err != nil {
			return err
		}
	}

	executor := c.deleteExecutor(target)
	executor.Warnings = stderr
	result, err := executor.Execute(context.Background(), spelling, plan, resolution, livePlan, panePlan)
	if err != nil {
		return err
	}
	selfTarget := result.SelfTarget
	writeResult := func() error {
		if err := writeDeletePlan(stdout, spelling, plan, livePlan, panePlan, target, false, selfTarget); err != nil {
			return err
		}
		return childDeleteReceipt(kind, plan, livePlan, panePlan, selfTarget).WriteHuman(stdout)
	}
	if err := writeResult(); err != nil {
		if selfTarget {
			// The registry result is already durable. Still queue the exact live
			// half so an unavailable output sink cannot leave a live orphan.
			if kind == coremetadata.KindWindow {
				_ = c.windows.QueueSelfKill(context.Background(), livePlan.Targets)
			} else {
				_ = c.panes.queueSelfKill(context.Background(), panePlan.Targets)
			}
		}
		return err
	}
	if err := flushDeleteResult(stdout); err != nil {
		if selfTarget {
			if kind == coremetadata.KindWindow {
				_ = c.windows.QueueSelfKill(context.Background(), livePlan.Targets)
			} else {
				_ = c.panes.queueSelfKill(context.Background(), panePlan.Targets)
			}
		}
		return err
	}
	if selfTarget {
		if kind == coremetadata.KindWindow {
			if err := c.windows.QueueSelfKill(context.Background(), livePlan.Targets); err != nil {
				return fmt.Errorf("delete window: registry cascade committed and the complete result was written, but %w; exact live target remains as retryable orphan drift", err)
			}
		} else if err := c.panes.queueSelfKill(context.Background(), panePlan.Targets); err != nil {
			return fmt.Errorf("delete %s: registry cascade committed and the complete result was written, but %w; exact live target remains tombstoned as retryable orphan drift",
				token, err)
		}
	}
	return nil
}

// runProjectUnregister is the explicit unregister cell of the Project lifecycle
// table. It performs one Registry-only cascade and deliberately does not stop a
// runtime, close a Window, or touch the root or Git/worktree state.
//
// Both public spellings land here with the same plan, the same confirmation,
// and the same transaction. The deprecated `delete project` differs by one
// stderr line and one receipt warning, which is the whole meaning of
// "behavior-preserving alias": the bytes on stdout, the Registry writes, and the
// preserved runtime are identical.
func (c *deleteCommand) runProjectUnregister(verb, spelling string, plan deletePlan, resolution selector.Resolution, actorRoute tmuxTransport, dryRun, yes bool, stdout, stderr io.Writer) error {
	deprecated := verb == deleteVerbDelete
	if deprecated {
		warnDeprecatedProjectDeleteAlias(stderr)
	}
	if dryRun {
		return writeDeletePlan(stdout, spelling, plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, tmuxTransport{}, true, false)
	}
	prompt := fmt.Sprintf("%s will unregister %d Project%s and %d descendant Registry resources; runtime and external assets are preserved",
		spelling, len(plan.Targets), plural(len(plan.Targets)), plan.Cascades())
	refusal := fmt.Sprintf("%s needs confirmation. Re-run with --yes, or with --dry-run to review the Registry-only plan first.", spelling)
	if err := c.confirm.confirm(yes, prompt, refusal, stdout); err != nil {
		return err
	}
	oldUIDs := resolution.UIDs()
	operation := deletionOperationUnregisterProject
	if deprecated {
		operation = deletionOperationDeleteProject
	}
	_, err := mutations.Unregister(context.Background(), mutations.DeletePlan(plan), resolution, mutations.UnregisterOptions{
		Mutate:   c.store.mutate,
		Actor:    func(working *coremetadata.Registry) DeletionActor { return c.observeDeletionActor(actorRoute, working) },
		StateDir: c.store.stateDir, NewOperationID: c.mintOperationID, Via: c.deletionVia(), Operation: operation, Warnings: stderr,
	})
	if err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleDeleteProject, "registry-unregister", strings.Join(oldUIDs, ","), "", err)
	}
	if err := writeDeletePlan(stdout, spelling, plan, windowLiveDeletePlan{}, paneLiveDeletePlan{}, tmuxTransport{}, false, false); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleDeleteProject, "result-write", strings.Join(oldUIDs, ","), "", err)
	}
	if _, err := fmt.Fprintf(stdout, "operation=delete-project stage=registry-unregister old_uid=%s new_uid=%s runtime=preserved external-assets=preserved\n",
		strings.Join(oldUIDs, ","), absentProjectLifecycleUID); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleDeleteProject, "result-write", strings.Join(oldUIDs, ","), "", err)
	}
	if err := projectUnregisterReceipt(plan, deprecated).WriteHuman(stdout); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleDeleteProject, "result-write", strings.Join(oldUIDs, ","), "", err)
	}
	if err := flushDeleteResult(stdout); err != nil {
		return wrapProjectLifecycleError(coremetadata.ProjectLifecycleDeleteProject, "result-flush", strings.Join(oldUIDs, ","), "", err)
	}
	return nil
}

// explicitUIDTargetRefs reports whether the operator named the complete target
// set with opaque uid references. Enclosing scope flags may still be present;
// they only constrain an already exact identity and cannot widen it.
func explicitUIDTargetRefs(refs []string) bool {
	if len(refs) == 0 {
		return false
	}
	for _, raw := range refs {
		ref, err := selector.ParseRef(coremetadata.KindPane, raw)
		if err != nil || !ref.IsUID() {
			return false
		}
	}
	return true
}

// childDeletionOperation is the deletion record operation of a Window, Pane,
// or Agent delete.
func childDeletionOperation(kind coremetadata.Kind) string {
	return mutationruntime.ChildDeletionOperation(kind)
}

// mintOperationID labels one delete's intentional termination receipt.
func (c *deleteCommand) mintOperationID() (string, error) {
	mint := c.newOperationID
	if mint == nil {
		mint = newCreateOperationID
	}
	return mint()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// buildDeletePlan expands every resolved target into its cascade plan.
// buildDeletePlan is the CLI adapter to the shared planning engine.
func buildDeletePlan(reg coremetadata.Registry, kind coremetadata.Kind, resolution selector.Resolution) deletePlan {
	return deletePlan(mutations.BuildDeletePlan(reg, kind, resolution))
}

// writeDeletePlan renders the plan for both the dry run and the executed run.
func writeDeletePlan(stdout io.Writer, spelling string, plan deletePlan, live windowLiveDeletePlan, panes paneLiveDeletePlan, socket tmuxTransport, dryRun, selfQueued bool) error {
	var b strings.Builder
	verb := "deleting"
	if dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(&b, "%s: %s %d %s and %d descendant resource%s\n",
		spelling, verb, len(plan.Targets), strings.ToLower(string(plan.Kind))+plural(len(plan.Targets)),
		plan.Cascades(), plural(plan.Cascades()))
	for _, target := range plan.Targets {
		fmt.Fprintf(&b, "%s uid=%s", resourceRef(target.Match), target.Match.UID)
		if owner := target.Match.Owner.String(); owner != "" {
			fmt.Fprintf(&b, " owner=%s", owner)
		}
		b.WriteString("\n")
		for _, descendant := range target.Descendants {
			fmt.Fprintf(&b, "  cascade %s/%s uid=%s\n",
				strings.ToLower(string(descendant.Kind)), descendant.Name, descendant.UID)
		}
		windowLive := false
		for _, liveTarget := range live.Targets {
			if liveTarget.UID != target.Match.UID {
				continue
			}
			windowLive = true
			action := "killed"
			if dryRun {
				action = "would kill"
			} else if selfQueued {
				action = "will queue after this result is flushed to kill"
			}
			fmt.Fprintf(&b, "  live %s tmux window %s session=%s session-id=%s socket=%s\n",
				action, liveTarget.WindowID, liveTarget.SessionName, liveTarget.SessionID, socket.Label())
			if liveTarget.EndsSession {
				impact := "ended"
				if dryRun {
					impact = "would end"
				} else if selfQueued {
					impact = "will end after this result is flushed"
				}
				fmt.Fprintf(&b, "  live cascade %s %s session %s because its last live Window is deleted\n",
					impact, liveTarget.RootKind, liveTarget.SessionName)
			}
		}
		if plan.Kind == coremetadata.KindWindow && !windowLive {
			action := "deleted this Window; no tmux Window was killed"
			if dryRun {
				action = "would delete this Window; no tmux Window would be killed"
			}
			fmt.Fprintf(&b, "  registry-only %s on socket=%s\n", action, socket.Label())
		}
		for _, liveTarget := range panes.Targets {
			if liveTarget.ResourceUID != target.Match.UID {
				continue
			}
			action := "killed"
			if dryRun {
				action = "would kill"
			} else if selfQueued {
				action = "will queue after this result is flushed to kill"
			}
			fmt.Fprintf(&b, "  live %s tmux pane %s pane-uid=%s window=%s session=%s session-id=%s socket=%s\n",
				action, liveTarget.PaneID, liveTarget.PaneUID, liveTarget.WindowID,
				liveTarget.SessionName, liveTarget.SessionID, socket.Label())
			if liveTarget.EndsWindow {
				impact := "created"
				if dryRun {
					impact = "would create"
				} else if selfQueued {
					impact = "was created before this result and the exact target kill will be queued after flush"
				}
				fmt.Fprintf(&b, "  live lifecycle %s a replacement shell in Window %s before its last prior Pane is deleted; Window uid=%s and name are preserved\n",
					impact, liveTarget.WindowID, liveTarget.WindowUID)
			}
		}
		for _, registryTarget := range panes.RegistryOnly {
			if registryTarget.ResourceUID != target.Match.UID {
				continue
			}
			action := "deleted"
			liveImpact := "was killed"
			if dryRun {
				action = "would delete"
				liveImpact = "would be killed"
			}
			fmt.Fprintf(&b, "  registry-only %s this %s; no tmux Pane %s on socket=%s evidence=%s owner-window=%s root=%s/%s preserving owner and siblings\n",
				action, registryTarget.Kind, liveImpact, socket.Label(), registryTarget.Evidence,
				registryTarget.WindowUID, strings.ToLower(string(registryTarget.RootKind)), registryTarget.RootUID)
		}
	}
	if dryRun {
		b.WriteString("dry-run: nothing was deleted\n")
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}

func flushDeleteResult(stdout io.Writer) error {
	if flusher, ok := stdout.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return fmt.Errorf("flush delete result: %w", err)
		}
	}
	return nil
}

// projectDeleteAliasWarning is the exact deprecation sentence the deprecated
// spelling prints. It names the replacement rather than only the deprecation,
// because a warning an operator cannot act on is noise.
const projectDeleteAliasWarning = "delete project is deprecated; use `projmux unregister project` instead. " +
	"Behavior is unchanged: the Registry subtree is removed and the runtime, root, and Git/worktrees are preserved."

// warnDeprecatedProjectDeleteAlias writes the alias notice to stderr.
//
// stderr rather than stdout on purpose: the stdout bytes of the deprecated
// spelling have to stay byte-identical to the canonical one, or a script that
// parses the result would be broken by the very warning that exists to avoid
// breaking it.
func warnDeprecatedProjectDeleteAlias(stderr io.Writer) {
	if stderr == nil {
		return
	}
	fmt.Fprintln(stderr, "projmux: "+projectDeleteAliasWarning)
}

// projectUnregisterReceipt projects one committed Project unregister onto the
// shared operation receipt.
//
// The runtime axis is `preserved` rather than `unchanged`, and that distinction
// is the reason the receipt exists: `unchanged` would say the operation had no
// opinion about the runtime, when in fact it has a deliberate one that differs
// from every other delete in the CLI.
func projectUnregisterReceipt(plan deletePlan, deprecated bool) cli.OperationReceipt {
	r := cli.OperationReceipt(mutations.ProjectUnregisterReceipt(mutations.DeletePlan(plan)))
	if deprecated {
		r.Warn(projectDeleteAliasWarning)
	}
	return r
}

// childDeleteReceipt projects one committed Window, Pane, or Agent delete.
//
// The runtime axis is the whole reason a child delete and a Project unregister
// cannot share a receipt: this one stops an exact live mirror when it finds
// one, and reports `unchanged` only when the delete was Registry-only. A
// self-targeted Window kill is still `stopped`; the queue after the flush is a
// transport detail, not a different outcome.
func childDeleteReceipt(kind coremetadata.Kind, plan deletePlan, windows windowLiveDeletePlan, panes paneLiveDeletePlan, selfTarget bool) cli.OperationReceipt {
	return cli.OperationReceipt(mutationruntime.ChildDeleteReceipt(kind, plan, windows, panes, selfTarget))
}
