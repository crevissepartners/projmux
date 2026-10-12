package mutations

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"strings"
)

type DeleteDescendant struct {
	Kind coremetadata.Kind
	UID  string
	Name string
}

// DeleteTarget is one resolved delete target plus its cascade plan.
type DeleteTarget struct {
	Match       selector.Match
	Descendants []DeleteDescendant
}

// DeletePlan is the whole preflighted operation.
type DeletePlan struct {
	Kind    coremetadata.Kind
	Targets []DeleteTarget
	// ExactUID reports that every target occurrence was an explicit uid:<uid>
	// reference. It is the only selector shape allowed to turn a durable
	// Offline/Failed/MissingRuntime state plus a positive exact-server inventory
	// into a Registry-only Pane or Agent delete. Names, scopes, labels, --all,
	// and the active-target fallback keep requiring a live mirror.
	ExactUID bool
	// Unnamed reports that the invocation carried no selector at all, so the
	// target set came from the active tmux target or from --all rather than from
	// something the operator typed. It is not part of Signature(): it describes
	// how the plan was asked for, not what the plan removes.
	Unnamed bool
	// Implicit distinguishes the active Window fallback from --all. Both are
	// unnamed for confirmation, but only the fallback must prove the caller is
	// attached to the same exact socket and Window.
	Implicit bool
}

// Cascades reports how many descendants the plan removes.
func (p DeletePlan) Cascades() int {
	total := 0
	for _, target := range p.Targets {
		total += len(target.Descendants)
	}
	return total
}

// Signature renders the plan as a comparable string. Execution re-derives it
// inside the store lock and refuses to run when it no longer matches what the
// preflight approved.
func (p DeletePlan) Signature() string {
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

// NeedsConfirmation reports whether the plan is destructive enough to require an
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
func (p DeletePlan) NeedsConfirmation() bool {
	if p.Unnamed {
		return true
	}
	if len(p.Targets) == 1 && p.Kind == coremetadata.KindPane && len(p.Targets[0].Descendants) == 0 {
		return false
	}
	return true
}

func BuildDeletePlan(registry coremetadata.Registry, kind coremetadata.Kind, resolution selector.Resolution) DeletePlan {
	plan := DeletePlan{Kind: kind}
	for _, match := range resolution.Matches {
		plan.Targets = append(plan.Targets, DeleteTarget{
			Match:       match,
			Descendants: CascadeOf(registry, kind, match.UID),
		})
	}
	if kind == coremetadata.KindPane {
		completeVirtualWindowCascades(registry, &plan)
	}
	return plan
}

// A selector may remove the last Panes together even when no individual
// target is the last one yet. Preview that parent cascade on the final target.
func completeVirtualWindowCascades(reg coremetadata.Registry, plan *DeletePlan) {
	removed := map[string]int{}
	for i, target := range plan.Targets {
		removed[target.Match.UID] = i
		for _, child := range target.Descendants {
			removed[child.UID] = i
		}
	}
	for _, window := range reg.Windows {
		uid := window.Metadata.UID
		if _, included := removed[uid]; included || !reg.IsVirtualWindow(uid) {
			continue
		}
		panes := reg.PanesOf(uid)
		for _, agent := range reg.AgentsOf(uid) {
			panes = append(panes, reg.PanesOf(agent.Metadata.UID)...)
		}
		last, complete := -1, true
		for _, pane := range panes {
			i, ok := removed[pane.Metadata.UID]
			if !ok {
				complete = false
				break
			}
			last = max(last, i)
		}
		if !complete || last < 0 {
			continue
		}
		out := &plan.Targets[last].Descendants
		*out = append(*out, DeleteDescendant{Kind: coremetadata.KindWindow, UID: uid, Name: window.Metadata.Name})
		for _, agent := range reg.AgentsOf(uid) {
			if _, included := removed[agent.Metadata.UID]; !included {
				*out = append(*out, DeleteDescendant{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID, Name: agent.Metadata.Name})
			}
		}
	}
}

// CascadeOf returns the descendants one delete removes, in registry insertion
// order.
//
//   - a Window cascades to its Agents, every Agent's managed Panes, and its own
//     shell Panes; the owning Project is preserved.
//   - an Agent cascades to its managed Panes; the Window and its sibling Panes
//     are preserved.
//   - a Pane is a leaf. Deleting an Agent's current managed Pane leaves the
//     Agent itself alive as an Offline resource, so it is not a descendant.
//
// A virtual Window is removed with its final Pane or Agent; that parent and
// any retained Agents are included in the cascade too.
func CascadeOf(registry coremetadata.Registry, kind coremetadata.Kind, uid string) []DeleteDescendant {
	var out []DeleteDescendant
	switch kind {
	case coremetadata.KindProject:
		for _, window := range registry.WindowsOf(uid) {
			out = append(out, DeleteDescendant{Kind: coremetadata.KindWindow, UID: window.Metadata.UID, Name: window.Metadata.Name})
			out = append(out, CascadeOf(registry, coremetadata.KindWindow, window.Metadata.UID)...)
		}
	case coremetadata.KindWindow:
		for _, agent := range registry.AgentsOf(uid) {
			out = append(out, DeleteDescendant{Kind: coremetadata.KindAgent, UID: agent.Metadata.UID, Name: agent.Metadata.Name})
			for _, pane := range registry.PanesOf(agent.Metadata.UID) {
				out = append(out, DeleteDescendant{Kind: coremetadata.KindPane, UID: pane.Metadata.UID, Name: pane.Metadata.Name})
			}
		}
		for _, pane := range registry.PanesOf(uid) {
			out = append(out, DeleteDescendant{Kind: coremetadata.KindPane, UID: pane.Metadata.UID, Name: pane.Metadata.Name})
		}
	case coremetadata.KindAgent:
		for _, pane := range registry.PanesOf(uid) {
			out = append(out, DeleteDescendant{Kind: coremetadata.KindPane, UID: pane.Metadata.UID, Name: pane.Metadata.Name})
		}
	}
	if windowUID := registry.VirtualWindowDeleteCascade(kind, uid); windowUID != "" {
		window, _ := registry.Window(windowUID)
		out = append(out, DeleteDescendant{Kind: coremetadata.KindWindow, UID: windowUID, Name: window.Metadata.Name})
		for _, sibling := range registry.AgentsOf(windowUID) {
			if kind == coremetadata.KindAgent && sibling.Metadata.UID == uid {
				continue
			}
			out = append(out, DeleteDescendant{Kind: coremetadata.KindAgent, UID: sibling.Metadata.UID, Name: sibling.Metadata.Name})
		}
	}
	return out
}
