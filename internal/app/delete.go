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
type deleteCommand struct {
	store        *resourceStore
	confirm      *confirmer
	notify       rawArgvCommand
	snapshots    rawArgvCommand
	resolveKinds map[string]coremetadata.Kind
}

func newDeleteCommand() *deleteCommand {
	return &deleteCommand{
		store:        newResourceStore(),
		confirm:      newConfirmer(),
		resolveKinds: deleteRegistryKinds,
	}
}

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
func (p deletePlan) needsConfirmation() bool {
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
