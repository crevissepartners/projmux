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
)

// deleteKinds lists the kind spellings `delete` implements, in help order, each
// canonical token followed by its accepted aliases. See getKinds.
var deleteKinds = cli.ChildSpellings("delete")

// deleteRegistryKinds are the kinds this verb deletes out of the resource
// registry. `notification` and `snapshot` are parity aliases over the existing
// queue and snapshot handlers instead.
var deleteRegistryKinds = map[string]coremetadata.Kind{
	"window": coremetadata.KindWindow,
	"pane":   coremetadata.KindPane,
	"agent":  coremetadata.KindAgent,
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
	snapshots    rawArgvCommand
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
}

func newDeleteCommand() *deleteCommand {
	return &deleteCommand{
		store:          newResourceStore(),
		confirm:        newConfirmer(),
		resolveKinds:   deleteRegistryKinds,
		activeTarget:   defaultActiveTargetLookup(),
		windows:        newTmuxWindowDeleteRuntime(),
		panes:          newTmuxPaneDeleteRuntime(),
		lookupEnv:      os.Getenv,
		newOperationID: newCreateOperationID,
	}
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
	case "snapshot":
		return forwardRawArgv(c.snapshots, "delete snapshot", "session-state", []string{"delete"}, args[1:], stdout, stderr)
	}
	kind, ok := c.resolveKinds[token]
	if !ok {
		return usageError(fmt.Sprintf("delete %s is not available; this release implements: %s",
			args[0], strings.Join(deleteKinds, ", ")))
	}
	return c.runKind(token, kind, args[1:], stdout, stderr)
}

// deleteDescendant is one resource the cascade removes together with a target.
type deleteDescendant struct {
	Kind coremetadata.Kind
	UID  string
	Name string
}

// deleteTarget is one resolved delete target plus its cascade plan.
type deleteTarget struct {
	Match       selector.Match
	Descendants []deleteDescendant
}

// deletePlan is the whole preflighted operation.
type deletePlan struct {
	Kind    coremetadata.Kind
	Targets []deleteTarget
	// ExactUID reports that every target occurrence was an explicit uid:<uid>
	// reference. It is the only selector shape allowed to turn a durable
	// Offline/MissingRuntime state plus a positive exact-server inventory into a
	// Registry-only Pane or Agent delete. Names, scopes, labels, --all, and the
	// active-target fallback keep requiring a live mirror.
	ExactUID bool
	// Unnamed reports that the invocation carried no selector at all, so the
	// target set came from the active tmux target or from --all rather than from
	// something the operator typed. It is not part of signature(): it describes
	// how the plan was asked for, not what the plan removes.
	Unnamed bool
	// Implicit distinguishes the active Window fallback from --all. Both are
	// unnamed for confirmation, but only the fallback must prove the caller is
	// attached to the same exact socket and Window.
	Implicit bool
}

// Cascades reports how many descendants the plan removes.
func (p deletePlan) Cascades() int {
	total := 0
	for _, target := range p.Targets {
		total += len(target.Descendants)
	}
	return total
}

// signature renders the plan as a comparable string. Execution re-derives it
// inside the store lock and refuses to run when it no longer matches what the
// preflight approved.
func (p deletePlan) signature() string {
	var b strings.Builder
	for _, target := range p.Targets {
		b.WriteString(target.Match.UID)
		for _, descendant := range target.Descendants {
			b.WriteString(",")
			b.WriteString(descendant.UID)
		}
		b.WriteString(";")
	}
	return b.String()
}

// needsConfirmation reports whether the plan is destructive enough to require an
// explicit answer. The contract carves out exactly one exception: an exact-one
// leaf Pane delete, which removes one resource and cascades to nothing.
//
// The carve-out is narrowed by exactly one condition: the operator has to have
// named the target. It was written for `delete pane log --window main`, where
// the argv already says which single cheap resource goes away, and the point of
// skipping the prompt is that re-reading it back would add nothing. An empty
// selector says none of that. Whether it resolved through the active tmux target
// or through --all, the argv names no resource, so the prompt is the only place
// the plan is ever stated before it runs -- and it is the same prompt either
// way, which is what keeps --yes a confirmation answer rather than a scope.
func (p deletePlan) needsConfirmation() bool {
	if p.Unnamed {
		return true
	}
	if len(p.Targets) == 1 && p.Kind == coremetadata.KindPane && len(p.Targets[0].Descendants) == 0 {
		return false
	}
	return true
}

func (c *deleteCommand) runKind(token string, kind coremetadata.Kind, args []string, stdout, stderr io.Writer) error {
	spelling := "delete " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
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
		return usageError(err.Error())
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
	plan.Unnamed = implicit
	plan.Implicit = implicit && !*all
	plan.ExactUID = explicitUIDTargetRefs(flags.targetRefs())

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
		c.windows.useExactTarget(target)
		livePlan, err = c.windows.preflight(context.Background(), registry, plan)
		if err != nil {
			return err
		}
	} else {
		if c.panes == nil {
			return fmt.Errorf("delete %s: live tmux deletion is not configured", token)
		}
		c.panes.useExactTarget(target)
		panePlan, err = c.panes.preflight(context.Background(), registry, plan)
		if err != nil {
			return err
		}
	}

	if *dryRun {
		return writeDeletePlan(stdout, spelling, plan, livePlan, panePlan, target, true, false)
	}
	needsConfirmation := plan.needsConfirmation() || panePlan.endsWindows() > 0 || panePlan.endsSessions() > 0
	if needsConfirmation {
		prompt := fmt.Sprintf("%s will remove %d %s and %d descendant resources",
			spelling, len(plan.Targets), strings.ToLower(string(kind))+plural(len(plan.Targets)), plan.Cascades())
		if kind == coremetadata.KindWindow {
			prompt += fmt.Sprintf(", kill %d exact live tmux Window%s, and end %d managed root session%s",
				len(livePlan.Targets), plural(len(livePlan.Targets)), livePlan.endsSessions(), plural(livePlan.endsSessions()))
		} else {
			prompt += fmt.Sprintf(", kill %d exact live tmux Pane%s, end %d Window%s, and end %d managed root session%s",
				len(panePlan.Targets), plural(len(panePlan.Targets)), panePlan.endsWindows(), plural(panePlan.endsWindows()),
				panePlan.endsSessions(), plural(panePlan.endsSessions()))
		}
		refusal := fmt.Sprintf("%s needs confirmation: %d targets and %d descendant resources. Re-run with --yes, or with --dry-run to review the plan first.",
			spelling, len(plan.Targets), plan.Cascades())
		if err := c.confirm.confirm(*yes, prompt, refusal, stdout); err != nil {
			return err
		}
	}

	// Intent is durable before the first live mutation. See
	// recordIntentionalTermination: a failure here aborts with zero tmux
	// mutations, because nothing live has been touched yet.
	operationID, err := c.mintOperationID()
	if err != nil {
		return err
	}
	recordedIntent, err := c.recordIntentionalTermination(spelling, plan, livePlan, panePlan, operationID)
	if err != nil {
		return err
	}
	// Every refusal below leaves live processes running, so the recorded intent
	// has to be withdrawn on the way out.
	withdrawIntent := func(cause error) error {
		if withdrawErr := c.withdrawIntentionalTermination(recordedIntent, operationID); withdrawErr != nil {
			return fmt.Errorf("%w; recorded intentional termination evidence for %s could not be withdrawn: %v; those Panes carry a stale intentional receipt until they are relaunched",
				cause, strings.Join(recordedIntent, ","), withdrawErr)
		}
		return cause
	}

	approved := plan.signature()
	approvedLive := livePlan.signature()
	approvedPanes := panePlan.signature()
	selfTarget := (kind == coremetadata.KindWindow && livePlan.hasSelfTarget()) ||
		(kind != coremetadata.KindWindow && panePlan.hasSelfTarget())
	var killedLive []windowLiveDeleteTarget
	var killedPanes []paneLiveDeleteTarget
	paneTombstoned := false
	if err := c.store.mutate(kind, resolution.UIDs(), func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current := buildDeletePlan(*working, kind, resolution).signature(); current != approved {
			return fmt.Errorf("%s: the cascade plan changed between preflight and execution; nothing was deleted", spelling)
		}
		var preparedWindowDelete *coremetadata.Registry
		if kind == coremetadata.KindWindow {
			currentLive, err := c.windows.preflight(context.Background(), *working, plan)
			if err != nil {
				return err
			}
			if currentLive.signature() != approvedLive {
				return fmt.Errorf("%s: the exact live cascade changed between preflight and execution; nothing was deleted", spelling)
			}
			// Prepare and validate the complete Registry result before touching
			// tmux. Deleting a Project's last Window may mint its replacement
			// anchor; a uid/name failure must therefore happen while every exact
			// live target is still intact, never after its kill.
			candidate := working.Clone()
			for _, uid := range resolution.UIDs() {
				if err := deleteResource(&candidate, mutator, kind, uid); err != nil {
					return err
				}
			}
			if err := candidate.Validate(); err != nil {
				return err
			}
			preparedWindowDelete = &candidate
			// A self-target cannot synchronously kill its own Window: tmux tears
			// down the caller's pty before the registry transaction can commit or
			// the result can be written. Its exact kill is queued only after the
			// durable commit and flushed result below.
			if !selfTarget {
				for _, target := range currentLive.Targets {
					if err := c.windows.kill(context.Background(), target); err != nil {
						return err
					}
					killedLive = append(killedLive, target)
				}
			}
		} else {
			currentPanes, err := c.panes.preflight(context.Background(), *working, plan)
			if err != nil {
				return err
			}
			if currentPanes.signature() != approvedPanes {
				return fmt.Errorf("%s: the exact live cascade changed between preflight and execution; nothing was deleted", spelling)
			}
			// A caller-containing plan marks the complete exact live set before
			// deleting Registry resources. A partial mark is rolled back while the
			// Registry still owns every uid; after commit, every queued or unqueued
			// survivor is therefore protected from orphan import.
			if selfTarget {
				if err := c.panes.tombstoneSelfKill(context.Background(), currentPanes.Targets); err != nil {
					return err
				}
				paneTombstoned = true
			} else {
				for _, target := range currentPanes.Targets {
					if err := c.panes.kill(context.Background(), target); err != nil {
						return err
					}
					killedPanes = append(killedPanes, target)
				}
			}
		}
		if preparedWindowDelete != nil {
			*working = *preparedWindowDelete
		} else {
			for _, uid := range resolution.UIDs() {
				if err := deleteResource(working, mutator, kind, uid); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		if paneTombstoned {
			if restoreErr := c.panes.restoreSelfKill(context.Background(), panePlan.Targets); restoreErr != nil {
				return withdrawIntent(fmt.Errorf("%w; Registry uid(s) %s remain, but rollback of pre-commit exact Pane tombstones was incomplete: %v; reported tombstoned drift cannot be orphan-imported",
					err, strings.Join(resolution.UIDs(), ","), restoreErr))
			}
			return withdrawIntent(fmt.Errorf("%w; pre-commit exact Pane tombstones %s were restored and Registry uid(s) %s remain unchanged for retry",
				err, paneDeleteIDs(panePlan.Targets), strings.Join(resolution.UIDs(), ",")))
		}
		if len(killedLive) > 0 {
			var removed []string
			for _, target := range killedLive {
				removed = append(removed, fmt.Sprintf("%s/session=%s(%s)", target.WindowID, target.SessionName, target.SessionID))
			}
			// The exact live Windows are already gone, so the intent that
			// explains their removal stays recorded rather than being withdrawn.
			return fmt.Errorf("%w; exact live target(s) %s were removed before the store failure, while registry uid(s) %s remain as retryable drift; no unplanned Window was targeted",
				err, strings.Join(removed, ","), strings.Join(resolution.UIDs(), ","))
		}
		if len(killedPanes) > 0 {
			var removed []string
			for _, target := range killedPanes {
				removed = append(removed, fmt.Sprintf("%s/window=%s/session=%s(%s)/pane-uid=%s",
					target.PaneID, target.WindowID, target.SessionName, target.SessionID, target.PaneUID))
			}
			// As above: those Panes really were terminated on purpose.
			return fmt.Errorf("%w; exact live target(s) %s were removed before the store failure, while registry uid(s) %s remain as retryable drift; no unplanned Pane was targeted",
				err, strings.Join(removed, ","), strings.Join(resolution.UIDs(), ","))
		}
		return withdrawIntent(err)
	}
	if err := writeDeletePlan(stdout, spelling, plan, livePlan, panePlan, target, false, selfTarget); err != nil {
		if selfTarget {
			// The registry result is already durable. Still queue the exact live
			// half so an unavailable output sink cannot leave a live orphan.
			if kind == coremetadata.KindWindow {
				_ = c.windows.queueSelfKill(context.Background(), livePlan.Targets)
			} else {
				_ = c.panes.queueSelfKill(context.Background(), panePlan.Targets)
			}
		}
		return err
	}
	if err := flushDeleteResult(stdout); err != nil {
		if selfTarget {
			if kind == coremetadata.KindWindow {
				_ = c.windows.queueSelfKill(context.Background(), livePlan.Targets)
			} else {
				_ = c.panes.queueSelfKill(context.Background(), panePlan.Targets)
			}
		}
		return err
	}
	if selfTarget {
		if kind == coremetadata.KindWindow {
			if err := c.windows.queueSelfKill(context.Background(), livePlan.Targets); err != nil {
				return fmt.Errorf("delete window: registry cascade committed and the complete result was written, but %w; exact live target remains as retryable orphan drift", err)
			}
		} else if err := c.panes.queueSelfKill(context.Background(), panePlan.Targets); err != nil {
			return fmt.Errorf("delete %s: registry cascade committed and the complete result was written, but %w; exact live target remains tombstoned as retryable orphan drift",
				token, err)
		}
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

// deleteResource routes one uid onto the mutator that owns its cascade.
func deleteResource(registry *coremetadata.Registry, mutator coremetadata.Mutator, kind coremetadata.Kind, uid string) error {
	switch kind {
	case coremetadata.KindWindow:
		return mutator.DeleteWindow(registry, uid)
	case coremetadata.KindAgent:
		return mutator.DeleteAgent(registry, uid)
	case coremetadata.KindPane:
		return mutator.DeletePane(registry, uid)
	default:
		return fmt.Errorf("delete: unsupported kind %q", kind)
	}
}

// buildDeletePlan expands every resolved target into its cascade plan.
func buildDeletePlan(registry coremetadata.Registry, kind coremetadata.Kind, resolution selector.Resolution) deletePlan {
	plan := deletePlan{Kind: kind}
	for _, match := range resolution.Matches {
		plan.Targets = append(plan.Targets, deleteTarget{
			Match:       match,
			Descendants: cascadeOf(registry, kind, match.UID),
		})
	}
	return plan
}

// cascadeOf returns the descendants one delete removes, in registry insertion
// order.
//
//   - a Window cascades to its Agents, every Agent's managed Panes, and its own
//     shell Panes; the owning Project is preserved.
//   - an Agent cascades to its managed Panes; the Window and its sibling Panes
//     are preserved.
//   - a Pane is a leaf. Deleting an Agent's current managed Pane leaves the
//     Agent itself alive as an Offline resource, so it is not a descendant.
func cascadeOf(registry coremetadata.Registry, kind coremetadata.Kind, uid string) []deleteDescendant {
	var out []deleteDescendant
	switch kind {
	case coremetadata.KindWindow:
		for _, agent := range registry.AgentsOf(uid) {
			out = append(out, deleteDescendant{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID, Name: agent.Metadata.Name})
			out = append(out, cascadeOf(registry, coremetadata.KindAgent, agent.Metadata.UID)...)
		}
		for _, pane := range registry.PanesOf(uid) {
			out = append(out, deleteDescendant{Kind: coremetadata.KindPane, UID: pane.Metadata.UID, Name: pane.Metadata.Name})
		}
	case coremetadata.KindAgent:
		for _, pane := range registry.PanesOf(uid) {
			out = append(out, deleteDescendant{Kind: coremetadata.KindPane, UID: pane.Metadata.UID, Name: pane.Metadata.Name})
		}
	}
	return out
}

// writeDeletePlan renders the plan for both the dry run and the executed run.
func writeDeletePlan(stdout io.Writer, spelling string, plan deletePlan, live windowLiveDeletePlan, panes paneLiveDeletePlan, socket explicitTmuxTarget, dryRun, selfQueued bool) error {
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
				action, liveTarget.WindowID, liveTarget.SessionName, liveTarget.SessionID, socket.label())
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
			fmt.Fprintf(&b, "  registry-only %s on socket=%s\n", action, socket.label())
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
				liveTarget.SessionName, liveTarget.SessionID, socket.label())
			if liveTarget.EndsWindow {
				impact := "ended"
				if dryRun {
					impact = "would end"
				} else if selfQueued {
					impact = "will end after this result is flushed"
				}
				fmt.Fprintf(&b, "  live cascade %s Window %s because its last live Pane is deleted\n", impact, liveTarget.WindowID)
			}
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
				action, registryTarget.Kind, liveImpact, socket.label(), registryTarget.Evidence,
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
