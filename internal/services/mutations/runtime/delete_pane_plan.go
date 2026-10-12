package runtime

import (
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// paneReplacementShell binds one desired replacement row to the exact live
// Pane that still anchors its Window immediately before deletion.
type PaneReplacementShell struct {
	Pane   coremetadata.Pane
	Anchor PaneLiveDeleteTarget
}

// paneReplacementReceipt contains only runtime objects this delete created.
// Rollback is therefore exact and never targets a pre-existing sibling.
type PaneReplacementReceipt struct {
	Created []RuntimeObject
}

type PaneLiveDeleteTarget struct {
	ResourceUID string
	PaneUID     string
	PaneID      string
	WindowUID   string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	EndsWindow  bool
	EndsSession bool
	Self        bool
	OwnerKind   coremetadata.Kind
	OwnerUID    string
	AgentUID    string
	PaneRole    coremetadata.PaneRole
	Generation  string
	// SupervisorPID is the retained dead Pane's original pane_pid. A non-zero
	// value requires the first-write guard to prove both PID identity and bounded
	// process absence again immediately before cleanup.
	SupervisorPID int
	// RequireDead is set only by retained lifecycle cleanup. Its first-write
	// guard must re-prove positive pane_dead on the current UID containment;
	// canonical live delete intentionally leaves this false.
	RequireDead bool
}

type PaneLiveDeletePlan struct {
	Targets      []PaneLiveDeleteTarget
	RegistryOnly []PaneRegistryOnlyDeleteTarget
	SocketPath   string
	// Authority is the exact Registry owner/generation/lifecycle signature
	// observed together with SocketPath. Locked revalidation must reproduce it
	// before either a live kill or a Registry-only commit is allowed.
	Authority string
}

type PaneRegistryOnlyDeleteTarget struct {
	ResourceUID string
	Kind        string
	Evidence    string
	WindowUID   string
	RootKind    coremetadata.Kind
	RootUID     string
}

func (p PaneLiveDeletePlan) Signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "socket=%q;authority=%q;", p.SocketPath, p.Authority)
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%t,%t,%t,%s,%s,%s,%s,%s,%t;", target.ResourceUID,
			target.PaneUID, target.PaneID, target.WindowUID, target.WindowID,
			target.SessionID, target.SessionName, target.RootKind, target.RootUID,
			target.EndsWindow, target.EndsSession, target.Self, target.OwnerKind, target.OwnerUID,
			target.AgentUID, target.PaneRole, target.Generation, target.RequireDead)
	}
	for _, target := range p.RegistryOnly {
		fmt.Fprintf(&b, "registry-only,%s,%s,%s,%s,%s,%s;", target.ResourceUID,
			target.Kind, target.Evidence, target.WindowUID, target.RootKind, target.RootUID)
	}
	return b.String()
}

func (p PaneLiveDeletePlan) EndsWindows() int {
	// Canonical Pane/Agent delete always preserves its owning Window. The
	// EndsWindow bit means a replacement must be created before the kill, not
	// that the final plan ends the Window.
	return 0
}

func (p PaneLiveDeletePlan) EndsSessions() int {
	return 0
}

func (p PaneLiveDeletePlan) HasSelfTarget() bool {
	return slices.ContainsFunc(p.Targets, func(target PaneLiveDeleteTarget) bool { return target.Self })
}
