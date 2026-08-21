package controller

import (
	"fmt"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// ControlTargetDeclaration is the only authority that may create a missing
// ControlSession identity. Session names and cwd values are deliberately absent
// from the evidence vocabulary: Session is a literal target only after the app
// lifecycle/config has declared it on one exact transport.
type ControlTargetDeclaration struct {
	Transport resourcegraph.Transport
	Session   string
	Declared  bool
}

// ControlMirrorClaim is one exact live Window or Pane uid claim together with
// the Registry owner chain, when that uid is known. Unknown non-empty mirrors
// are tolerated only while the root itself is absent: that is the interrupted
// old-control-root state the declaration is authorized to replace.
type ControlMirrorClaim struct {
	Handle    string
	UID       string
	Known     bool
	RootKind  string
	RootUID   string
	WindowUID string
}

// ControlWindowClaim is one live Window and its contained Panes.
type ControlWindowClaim struct {
	ControlMirrorClaim
	Panes []ControlMirrorClaim
}

// ControlTargetState is the complete pure input to control-root convergence.
type ControlTargetState struct {
	Declaration  ControlTargetDeclaration
	AppOwned     bool
	Ephemeral    bool
	Role         string
	ProjectUID   string
	ProjectKnown bool
	RootUIDs     []string
	Windows      []ControlWindowClaim
}

// ControlTargetStep is one declarative field that is absent or stale.
type ControlTargetStep string

const (
	ControlEnsureRoot         ControlTargetStep = "ensure-control-root"
	ControlEnsureRole         ControlTargetStep = "ensure-control-role"
	ControlEnsureWindowMirror ControlTargetStep = "ensure-window-mirror"
	ControlEnsurePaneMirror   ControlTargetStep = "ensure-pane-mirror"
)

// ControlTargetAction is a stable, printable item in the convergence plan.
type ControlTargetAction struct {
	Step   ControlTargetStep
	Target string
}

// ControlTargetPlan has exactly one outcome: Actions are executable, or Reason
// records a refusal. It can never contain both.
type ControlTargetPlan struct {
	Actions []ControlTargetAction
	Reason  string
}

func (p ControlTargetPlan) Refused() bool   { return p.Reason != "" }
func (p ControlTargetPlan) Converged() bool { return p.Reason == "" && len(p.Actions) == 0 }

func refuseControlTarget(format string, args ...any) ControlTargetPlan {
	return ControlTargetPlan{Reason: fmt.Sprintf(format, args...)}
}

// PlanControlTargetConvergence closes every root/role/Window/Pane partial
// state. It performs no I/O and never derives authority from display names,
// session-name similarity, cwd, commands, or the app marker alone.
func PlanControlTargetConvergence(state ControlTargetState) ControlTargetPlan {
	decl := state.Declaration
	if !decl.Transport.Present() || strings.TrimSpace(decl.Session) == "" {
		return refuseControlTarget("control target requires one exact transport and exact declared session")
	}
	rootUIDs := compactSortedUnique(state.RootUIDs)
	if !decl.Declared && len(rootUIDs) == 0 {
		return refuseControlTarget("control target has neither an exact lifecycle/config declaration nor an existing ControlSession identity")
	}
	if !state.AppOwned {
		return refuseControlTarget("declared control target is not app-owned (@projmux_app is not 1)")
	}
	if state.Ephemeral {
		return refuseControlTarget("declared control target is ephemeral (@projmux_ephemeral=1)")
	}
	if len(rootUIDs) > 1 {
		return refuseControlTarget("multiple ControlSession claimants name exact session %q: %s", decl.Session, strings.Join(rootUIDs, ", "))
	}
	if projectUID := strings.TrimSpace(state.ProjectUID); projectUID != "" {
		if state.ProjectKnown {
			return refuseControlTarget("declared control target carries Project uid conflict %q", projectUID)
		}
		return refuseControlTarget("declared control target carries foreign Project uid claimant %q", projectUID)
	}
	role := strings.TrimSpace(state.Role)
	if role != "" && role != resourcegraph.ControlSessionRole {
		return refuseControlTarget("declared control target carries foreign session-role claimant %q", role)
	}

	rootUID := ""
	if len(rootUIDs) == 1 {
		rootUID = rootUIDs[0]
	}
	if reason := controlMirrorConflict(state.Windows, rootUID); reason != "" {
		return ControlTargetPlan{Reason: reason}
	}

	var actions []ControlTargetAction
	if rootUID == "" {
		actions = append(actions, ControlTargetAction{Step: ControlEnsureRoot, Target: decl.Session})
	}
	if role != resourcegraph.ControlSessionRole {
		actions = append(actions, ControlTargetAction{Step: ControlEnsureRole, Target: decl.Session})
	}
	for _, window := range state.Windows {
		if strings.TrimSpace(window.UID) == "" || !window.Known {
			actions = append(actions, ControlTargetAction{Step: ControlEnsureWindowMirror, Target: window.Handle})
		}
		for _, pane := range window.Panes {
			if strings.TrimSpace(pane.UID) == "" || !pane.Known {
				actions = append(actions, ControlTargetAction{Step: ControlEnsurePaneMirror, Target: pane.Handle})
			}
		}
	}
	return ControlTargetPlan{Actions: actions}
}

func controlMirrorConflict(windows []ControlWindowClaim, rootUID string) string {
	windowClaims := map[string][]string{}
	paneClaims := map[string][]string{}
	for _, window := range windows {
		if uid := strings.TrimSpace(window.UID); uid != "" {
			windowClaims[uid] = append(windowClaims[uid], window.Handle)
		}
		for _, pane := range window.Panes {
			if uid := strings.TrimSpace(pane.UID); uid != "" {
				paneClaims[uid] = append(paneClaims[uid], pane.Handle)
			}
		}
	}
	for _, claims := range []struct {
		kind string
		set  map[string][]string
	}{{"Window", windowClaims}, {"Pane", paneClaims}} {
		for uid, handles := range claims.set {
			if len(handles) > 1 {
				slices.Sort(handles)
				return fmt.Sprintf("multiple %s claimants carry uid %q: %s", claims.kind, uid, strings.Join(handles, ", "))
			}
		}
	}
	for _, window := range windows {
		if reason := controlClaimConflict("Window", window.ControlMirrorClaim, rootUID); reason != "" {
			return reason
		}
		for _, pane := range window.Panes {
			if reason := controlClaimConflict("Pane", pane, rootUID); reason != "" {
				return reason
			}
			if pane.Known && window.Known && strings.TrimSpace(pane.WindowUID) != strings.TrimSpace(window.UID) {
				return fmt.Sprintf("Pane %s uid %q contradicts exact Window owner chain %q", pane.Handle, pane.UID, window.UID)
			}
		}
	}
	return ""
}

func controlClaimConflict(kind string, claim ControlMirrorClaim, rootUID string) string {
	uid := strings.TrimSpace(claim.UID)
	if uid == "" {
		return ""
	}
	if !claim.Known {
		if rootUID != "" {
			return fmt.Sprintf("%s %s carries foreign uid claimant %q while ControlSession %q already exists", kind, claim.Handle, uid, rootUID)
		}
		return ""
	}
	if claim.RootKind == "Project" {
		return fmt.Sprintf("%s %s uid %q conflicts with Project owner %q", kind, claim.Handle, uid, claim.RootUID)
	}
	if claim.RootKind != "ControlSession" || claim.RootUID != rootUID {
		return fmt.Sprintf("%s %s uid %q is claimed by foreign %s owner %q", kind, claim.Handle, uid, claim.RootKind, claim.RootUID)
	}
	return ""
}

func compactSortedUnique(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
