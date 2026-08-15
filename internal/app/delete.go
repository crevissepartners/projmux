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

// deleteKinds lists the resource kinds `delete` implements, in help order.
var deleteKinds = []string{"window", "pane", "agent", "notification", "snapshot"}

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
}

func newDeleteCommand() *deleteCommand {
	return &deleteCommand{
		store:        newResourceStore(),
		confirm:      newConfirmer(),
		resolveKinds: deleteRegistryKinds,
		activeTarget: defaultActiveTargetLookup(),
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
	switch args[0] {
	case "notification":
		return forwardRawArgv(c.notify, "delete notification", "notify", []string{"ack"}, args[1:], stdout, stderr)
	case "snapshot":
		return forwardRawArgv(c.snapshots, "delete snapshot", "session-state", []string{"delete"}, args[1:], stdout, stderr)
	}
	kind, ok := c.resolveKinds[args[0]]
	if !ok {
		return usageError(fmt.Sprintf("delete %s is not available; this release implements: %s",
			args[0], strings.Join(deleteKinds, ", ")))
	}
	return c.runKind(args[0], kind, args[1:], stdout, stderr)
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
	// Unnamed reports that the invocation carried no selector at all, so the
	// target set came from the active tmux target or from --all rather than from
	// something the operator typed. It is not part of signature(): it describes
	// how the plan was asked for, not what the plan removes.
	Unnamed bool
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

	if *dryRun {
		return writeDeletePlan(stdout, spelling, plan, true)
	}
	if plan.needsConfirmation() {
		prompt := fmt.Sprintf("%s will remove %d %s and %d descendant resources",
			spelling, len(plan.Targets), strings.ToLower(string(kind))+plural(len(plan.Targets)), plan.Cascades())
		refusal := fmt.Sprintf("%s needs confirmation: %d targets and %d descendant resources. Re-run with --yes, or with --dry-run to review the plan first.",
			spelling, len(plan.Targets), plan.Cascades())
		if err := c.confirm.confirm(*yes, prompt, refusal, stdout); err != nil {
			return err
		}
	}

	approved := plan.signature()
	if err := c.store.mutate(kind, resolution.UIDs(), func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
		if current := buildDeletePlan(*working, kind, resolution).signature(); current != approved {
			return fmt.Errorf("%s: the cascade plan changed between preflight and execution; nothing was deleted", spelling)
		}
		for _, uid := range resolution.UIDs() {
			if err := deleteResource(working, mutator, kind, uid); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return writeDeletePlan(stdout, spelling, plan, false)
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
func writeDeletePlan(stdout io.Writer, spelling string, plan deletePlan, dryRun bool) error {
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
	}
	if dryRun {
		b.WriteString("dry-run: nothing was deleted\n")
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}
