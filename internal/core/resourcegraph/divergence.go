package resourcegraph

import (
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Divergence is the closed vocabulary for one difference between Registry
// desired state and one exact runtime observation. It is deliberately separate
// from Class (attribution) and from app-level action/drift vocabulary.
type Divergence string

const (
	DivergenceUnrealized   Divergence = "D1-unrealized"
	DivergenceUnattributed Divergence = "D2-unattributed"
	DivergenceOrphanMirror Divergence = "D3-orphan-mirror"
	DivergenceContaminated Divergence = "D4-contamination"
	DivergenceDrifted      Divergence = "D5-drift"
	DivergenceUnknown      Divergence = "D6-unknown"
)

// Divergences returns the complete declaration order used by reports.
func Divergences() []Divergence {
	return []Divergence{
		DivergenceUnrealized,
		DivergenceUnattributed,
		DivergenceOrphanMirror,
		DivergenceContaminated,
		DivergenceDrifted,
		DivergenceUnknown,
	}
}

// Valid reports whether d is exactly one member of the closed vocabulary.
func (d Divergence) Valid() bool { return slices.Contains(Divergences(), d) }

// DivergenceItem is one classified difference. Reason is human-facing context;
// Divergence is the stable machine label and never derives from Reason text.
type DivergenceItem struct {
	Key        string     `json:"key"`
	Divergence Divergence `json:"divergence"`
	Reason     string     `json:"reason"`
}

// DivergenceCount is one closed label and its count. Zero entries are retained
// so callers can print the complete taxonomy without maintaining another list.
type DivergenceCount struct {
	Divergence Divergence `json:"divergence"`
	Count      int        `json:"count"`
}

// CountDivergences counts items in declaration order, including zeroes.
func CountDivergences(items []DivergenceItem) []DivergenceCount {
	out := make([]DivergenceCount, 0, len(Divergences()))
	for _, divergence := range Divergences() {
		count := 0
		for _, item := range items {
			if item.Divergence == divergence {
				count++
			}
		}
		out = append(out, DivergenceCount{Divergence: divergence, Count: count})
	}
	return out
}

// ClassifyDivergences resolves arbitrary Registry and Inventory values and
// emits every difference the current observation can state. Exact conflicts
// are one D4 event each; Registry rows and runtime-only objects are otherwise
// emitted once each, so a missing row plus its orphan mirror cannot be counted
// twice under two labels.
func ClassifyDivergences(registry coremetadata.Registry, inventory Inventory) []DivergenceItem {
	graph := Resolve(registry, inventory)
	items := make([]DivergenceItem, 0)
	for index, conflict := range graph.Conflicts {
		items = append(items, DivergenceItem{
			Key:        fmt.Sprintf("conflict:%s:%s:%d", conflict.Kind, conflict.UID, index),
			Divergence: DivergenceContaminated,
			Reason:     conflict.Detail,
		})
	}
	for _, node := range graph.Projects {
		items = appendRegistryDivergence(items, "project:"+node.Project.Metadata.UID, node.Class, node.Status, node.Runtime,
			projectFieldsDrifted(node.Project, inventory), "Project")
	}
	for _, node := range graph.ControlSessions {
		items = appendRegistryDivergence(items, "controlsession:"+node.ControlSession.Metadata.UID, node.Class, node.Status, node.Runtime,
			false, "ControlSession")
	}
	for _, node := range graph.Windows {
		items = appendRegistryDivergence(items, "window:"+node.Window.Metadata.UID, node.Class, node.Status, node.Runtime,
			windowFieldsDrifted(node.Window, node.Runtime, inventory), "Window")
	}
	for _, node := range graph.Panes {
		items = appendRegistryDivergence(items, "pane:"+node.Pane.Metadata.UID, node.Class, node.Status, node.Runtime,
			paneFieldsDrifted(node.Pane, node.Runtime, inventory), "Pane")
	}
	for _, node := range graph.Agents {
		items = appendRegistryDivergence(items, "agent:"+node.Agent.Metadata.UID, node.Class, node.Status, node.Runtime,
			false, "Agent")
	}
	for _, node := range graph.Runtime {
		var divergence Divergence
		switch node.Class {
		case ClassRecoverable:
			divergence = DivergenceOrphanMirror
		case ClassControl, ClassEphemeral, ClassUnattributed, ClassForeign:
			divergence = DivergenceUnattributed
		default:
			continue
		}
		reason := strings.TrimSpace(node.Reason)
		if reason == "" {
			reason = "runtime object is not bound to a Registry resource"
		}
		items = append(items, DivergenceItem{
			Key: "runtime:" + string(node.Ref.Kind) + ":" + node.Ref.ID, Divergence: divergence, Reason: reason,
		})
	}
	return items
}

func appendRegistryDivergence(items []DivergenceItem, key string, class Class, status Status, runtime *RuntimeRef, fieldsDrifted bool, kind string) []DivergenceItem {
	if class == ClassConflict {
		return items
	}
	switch status {
	case StatusUnknown:
		return append(items, DivergenceItem{Key: key, Divergence: DivergenceUnknown, Reason: kind + " runtime observation is unavailable"})
	case StatusOffline, StatusMissingRoot:
		return append(items, DivergenceItem{Key: key, Divergence: DivergenceUnrealized, Reason: kind + " exists in the Registry but has no observed runtime object"})
	case StatusLive:
		if runtime != nil && fieldsDrifted {
			return append(items, DivergenceItem{Key: key, Divergence: DivergenceDrifted, Reason: kind + " runtime fields differ from Registry authority"})
		}
	}
	return items
}

func projectFieldsDrifted(project coremetadata.Project, inventory Inventory) bool {
	for _, session := range inventory.Sessions {
		if session.ProjectUID != project.Metadata.UID {
			continue
		}
		return strings.TrimSpace(session.ProjectName) != strings.TrimSpace(project.Metadata.Name) ||
			strings.TrimSpace(session.Root) != strings.TrimSpace(project.Spec.Root)
	}
	return false
}

func windowFieldsDrifted(window coremetadata.Window, ref *RuntimeRef, inventory Inventory) bool {
	if ref == nil {
		return false
	}
	for _, observed := range inventory.Windows {
		if observed.ID != ref.ID {
			continue
		}
		return strings.TrimSpace(observed.MirroredName) != strings.TrimSpace(window.Metadata.Name)
	}
	return false
}

func paneFieldsDrifted(pane coremetadata.Pane, ref *RuntimeRef, inventory Inventory) bool {
	if ref == nil {
		return false
	}
	for _, observed := range inventory.Panes {
		if observed.ID == ref.ID {
			return strings.TrimSpace(observed.MirroredName) != strings.TrimSpace(pane.Metadata.Name)
		}
	}
	return false
}
