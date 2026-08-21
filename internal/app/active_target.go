package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The invocation-derived active identity of read and rename verbs.
//
// Inside a tmux client, a singular invocation that carries no selector resolves
// the resource the operator is looking at instead of the whole registry. The
// Window/Pane/Agent plural reads consume its managed-root ancestor as a default
// enclosing scope, never as an individual target. A singular reference remains
// Project-namespaced per active_project_scope.go. The contract has
// four deliberate edges, each of which is a decision rather than an
// implementation detail:
//
//  1. There is no sentinel value token. `--pane current` is not added, and
//     neither is `active`: selector.ParseRef treats any non-`uid:` token as a
//     bare metadata.name, and metadata.ValidateName reserves only `.` and `..`
//     while forbidding a fixed rune set that contains neither word. `current`
//     and `active` are therefore legal names today -- `rename pane --name
//     current` succeeds -- so a value-token sentinel would silently shadow a
//     real resource. Omission is the only spelling. If a later Phase needs an
//     explicit one, the two collision-free candidates are `.` (already reserved
//     by ValidateName) and `@` (already a forbidden name rune); nothing here
//     reserves them.
//  2. The observation is read from tmux on every invocation. There is no
//     persistent, queryable scope: a stored "current pane" would need a writer,
//     new on-disk state, and a staleness policy, and tmux focus is already
//     authoritative for one `display-message`. `projmux describe pane` with no
//     selector *is* the preview of what the fallback will pick, and it is
//     authoritative for every verb in this family because they all resolve
//     through the same seam.
//  3. Being inside tmux is decided by the environment, not by whether a tmux
//     server answers. A bare `tmux display-message -p` from outside a client
//     still succeeds against a running server and silently targets the
//     most-recently-used session, which would pick a wrong target. The pane id
//     comes from $TMUX_PANE and is passed as an explicit `-t`, and $TMUX must
//     also be set so a leaked $TMUX_PANE cannot address a different server. A
//     caller that runs inside the client but not inside a pane -- a tmux popup
//     job, which gets $TMUX and no $TMUX_PANE -- may state its target instead of
//     inheriting one; see anchoredActiveTargetLookup, which keeps the $TMUX half
//     of this rule and takes the pane from the caller rather than the
//     environment.
//  4. An active target that maps onto no registry resource is a refusal, never a
//     fallthrough. See activeTargetError.
//
// Ancestors are derived from registry ownership rather than from more tmux
// options on purpose: `@projmux_project_uid` is measurably empty on live
// sessions, while `@projmux_window_uid` resolves from a pane target, so the
// managed root of the active target is the owner of the active Window.

// activeTargetObserver reads the Projmux identity mirror off the tmux target the
// current invocation is running in.
//
// The two uid reads are lazy and independent so a route only pays for the option
// its own kind needs: a Pane or Agent route reads @projmux_pane_uid, a Window or
// Project route reads @projmux_window_uid, and neither reads both.
type activeTargetObserver struct {
	// paneID is the tmux pane id of the client this process runs in. It is the
	// explicit -t target of every read and appears verbatim in the refusal text
	// so an operator can see what was inspected.
	paneID string
	// paneUID and windowUID return the mirrored uid, or "" when the option is
	// unset or the query failed. A failed query is indistinguishable from an
	// unset option on purpose: both mean "this target carries no Projmux
	// identity", and both must refuse rather than select something else.
	paneUID   func() string
	windowUID func() string
}

// activeTargetLookup observes the active tmux target once per invocation. The
// second result is false when the process is not inside a tmux client at all,
// which is what keeps the outside-tmux behavior byte-identical to the
// pre-fallback contract.
type activeTargetLookup func() (activeTargetObserver, bool)

// defaultActiveTargetLookup is the production seam: the process environment plus
// the existing tmux identity mirror. No parallel option reader is introduced.
func defaultActiveTargetLookup() activeTargetLookup {
	return tmuxActiveTargetLookup(os.Getenv, intmetadata.NewMirror(inttmux.ExecRunner{}))
}

// tmuxActiveTargetLookup builds the lookup over an injectable environment and
// mirror.
func tmuxActiveTargetLookup(getenv func(string) string, mirror intmetadata.Mirror) activeTargetLookup {
	return func() (activeTargetObserver, bool) {
		if getenv == nil {
			return activeTargetObserver{}, false
		}
		paneID := strings.TrimSpace(getenv("TMUX_PANE"))
		if paneID == "" || strings.TrimSpace(getenv("TMUX")) == "" {
			return activeTargetObserver{}, false
		}
		return newActiveTargetObserver(paneID, mirror), true
	}
}

// defaultAnchoredActiveTargetLookup is the production seam of an invocation
// whose anchor pane is explicit rather than inherited. See
// anchoredActiveTargetLookup.
func defaultAnchoredActiveTargetLookup(paneID string) activeTargetLookup {
	return anchoredActiveTargetLookup(paneID, os.Getenv, intmetadata.NewMirror(inttmux.ExecRunner{}))
}

// anchoredActiveTargetLookup observes an explicitly named pane instead of the
// inherited $TMUX_PANE.
//
// It exists for exactly one shape of invocation: a job that runs inside the
// tmux client but not inside the pane it acts on. A tmux popup is that job --
// `display-popup -E` inherits $TMUX and deliberately exports no $TMUX_PANE,
// because the popup is not a pane -- so the split UI running in a popup has no
// inherited target at all while still knowing, from the keypress that opened
// it, exactly which pane the operator was in.
//
// The two edges are the reason this is a separate constructor rather than a
// fallback inside tmuxActiveTargetLookup:
//
//  1. The anchor is passed in, never read from the environment here. Promoting
//     $TMUX_SPLIT_TARGET_PANE to an implicit target every verb consults would
//     make one popup's origin pane a global scope override; the caller that
//     owns the anchor is the only one that may supply it.
//  2. $TMUX must still be set, for the same reason edge 3 of the contract above
//     requires it: an anchor that leaked into a process outside any client
//     would otherwise address a pane on a server this invocation never
//     inherited.
func anchoredActiveTargetLookup(paneID string, getenv func(string) string, mirror intmetadata.Mirror) activeTargetLookup {
	anchor := strings.TrimSpace(paneID)
	return func() (activeTargetObserver, bool) {
		if getenv == nil || anchor == "" {
			return activeTargetObserver{}, false
		}
		if strings.TrimSpace(getenv("TMUX")) == "" {
			return activeTargetObserver{}, false
		}
		return newActiveTargetObserver(anchor, mirror), true
	}
}

// newActiveTargetObserver builds the lazy identity reads of one tmux pane
// target. The two uid reads stay independent so a route only pays for the option
// its own kind needs.
func newActiveTargetObserver(paneID string, mirror intmetadata.Mirror) activeTargetObserver {
	read := func(resolve func(context.Context, string) (string, error)) func() string {
		return func() string {
			value, err := resolve(context.Background(), paneID)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(value)
		}
	}
	return activeTargetObserver{
		paneID:    paneID,
		paneUID:   read(mirror.ResolvePaneUID),
		windowUID: read(mirror.ResolveWindowUID),
	}
}

func (o activeTargetObserver) mirroredPaneUID() string {
	if o.paneUID == nil {
		return ""
	}
	return strings.TrimSpace(o.paneUID())
}

func (o activeTargetObserver) mirroredWindowUID() string {
	if o.windowUID == nil {
		return ""
	}
	return strings.TrimSpace(o.windowUID())
}

// activeTargetRef maps the observed active tmux target onto a `uid:` selector
// ref of kind.
//
// The three results are distinct outcomes, not a bool plus an error: (ref, true,
// nil) resolved the active target, (_, false, nil) means the invocation is not
// inside tmux and the caller must keep its pre-fallback behavior, and
// (_, false, err) is the refusal of an active target that maps onto no registry
// resource.
func activeTargetRef(lookup activeTargetLookup, kind coremetadata.Kind, registry coremetadata.Registry) (selector.Ref, bool, error) {
	uid, resolved, detail := activeUID(lookup, kind, registry)
	switch {
	case resolved:
		return activeUIDRef(kind, uid), true, nil
	case detail == "":
		return selector.Ref{}, false, nil
	default:
		return selector.Ref{}, false, activeTargetError(kind, detail)
	}
}

// activeRootScope resolves the exact managed root that owns the active Window.
//
// Project and ControlSession are peers here: both are Registry roots and both
// own Windows. The public selector grammar remains Project-only; this value is
// an invocation default derived from the Window's exact ownerRef, not a new
// user-spellable ControlSession selector.
//
// The three outcomes match activeTargetRef: a resolved root, the intentional
// outside-tmux compatibility path, or an in-tmux refusal. In particular an
// unknown Window or a Window without one existing managed root never falls
// through to the whole Registry.
func activeRootScope(lookup activeTargetLookup, registry coremetadata.Registry) (selector.RootScope, bool, error) {
	if lookup == nil {
		return selector.RootScope{}, false, nil
	}
	observer, inside := lookup()
	if !inside {
		return selector.RootScope{}, false, nil
	}

	window, detail := observer.activeWindow(registry)
	if window == nil {
		return selector.RootScope{}, false, activeRootScopeError(detail)
	}
	owner := window.Metadata.OwnerRef
	if owner == nil {
		return selector.RootScope{}, false, activeRootScopeError(observer.noManagedRootOwnerDetail(window))
	}
	switch owner.Kind {
	case coremetadata.KindProject:
		if _, ok := registry.Project(owner.UID); !ok {
			return selector.RootScope{}, false, activeRootScopeError(observer.noManagedRootOwnerDetail(window))
		}
	case coremetadata.KindControlSession:
		if _, ok := registry.ControlSession(owner.UID); !ok {
			return selector.RootScope{}, false, activeRootScopeError(observer.noManagedRootOwnerDetail(window))
		}
	default:
		return selector.RootScope{}, false, activeRootScopeError(observer.noManagedRootOwnerDetail(window))
	}
	return selector.RootScope{Kind: owner.Kind, UID: owner.UID}, true, nil
}

// activeRootScopeError keeps a failed in-tmux root derivation distinct from an
// intentional outside-tmux whole-Registry read.
func activeRootScopeError(detail string) error {
	return &selector.SelectorError{
		Op: "resolve managed root scope",
		Detail: detail + "; the active managed-root scope is undecidable, so nothing was selected -- " +
			"pass --project <ref> to name a Project or --all-projects to request the whole registry",
	}
}

// activeUID is the observation half of the seam, shared by the implicit target
// above and by the Project namespace default in active_project_scope.go.
//
// The three results are the same three outcomes activeTargetRef reports, minus
// the refusal wording: (uid, true, "") resolved, ("", false, "") means the
// invocation is not inside a tmux client, and ("", false, detail) is an active
// target that maps onto no registry resource, where detail names exactly what
// was inspected. The wording is left to the caller because the same failed
// observation refuses differently depending on whether it was asked to pick a
// target or to fix a search scope.
func activeUID(lookup activeTargetLookup, kind coremetadata.Kind, registry coremetadata.Registry) (string, bool, string) {
	if lookup == nil {
		return "", false, ""
	}
	observer, inside := lookup()
	if !inside {
		return "", false, ""
	}
	uid, detail := observer.uidFor(kind, registry)
	if uid == "" {
		return "", false, detail
	}
	return uid, true, ""
}

// activeUIDRef spells an observed uid as a selector ref.
//
// The ref is spelled in its uid form so any downstream error text reports the
// identity that was actually selected rather than a name that was never typed.
func activeUIDRef(kind coremetadata.Kind, uid string) selector.Ref {
	return selector.Ref{Kind: kind, UID: uid, Raw: selector.UIDPrefix + uid}
}

// uidFor resolves the registry uid of kind for this observation. An empty uid is
// always accompanied by the detail sentence naming exactly what was inspected.
func (o activeTargetObserver) uidFor(kind coremetadata.Kind, registry coremetadata.Registry) (string, string) {
	switch kind {
	case coremetadata.KindPane:
		pane, detail := o.activePane(registry)
		if pane == nil {
			return "", detail
		}
		return pane.Metadata.UID, ""
	case coremetadata.KindWindow:
		window, detail := o.activeWindow(registry)
		if window == nil {
			return "", detail
		}
		return window.Metadata.UID, ""
	case coremetadata.KindAgent:
		pane, detail := o.activePane(registry)
		if pane == nil {
			return "", detail
		}
		// Only an Agent-owned Pane resolves. A shell Pane is owned by its
		// Window and has no Agent, so it refuses rather than being attributed
		// to one of the Window's Agents.
		agentUID, ok := agentUIDForPaneUID(registry, pane.Metadata.UID)
		if !ok {
			return "", fmt.Sprintf("the active tmux pane %s is a shell Pane with no owning Agent", o.paneID)
		}
		return agentUID, ""
	case coremetadata.KindProject:
		// The Project ancestor is derived from registry ownership, not from
		// @projmux_project_uid: that session-scoped option is measurably empty
		// on live sessions, so trusting it would refuse targets that resolve
		// perfectly well through the owner chain.
		window, detail := o.activeWindow(registry)
		if window == nil {
			return "", detail
		}
		owner := window.Metadata.OwnerRef
		if owner == nil || owner.Kind != coremetadata.KindProject {
			return "", o.noProjectOwnerDetail(window)
		}
		if _, ok := registry.Project(owner.UID); !ok {
			return "", o.noProjectOwnerDetail(window)
		}
		return owner.UID, ""
	default:
		return "", fmt.Sprintf("kind %q has no active tmux target", kind)
	}
}

func (o activeTargetObserver) noProjectOwnerDetail(window *coremetadata.Window) string {
	return fmt.Sprintf("the active tmux pane %s resolves to window %q, which has no owning Project in the registry",
		o.paneID, window.Metadata.Name)
}

func (o activeTargetObserver) noManagedRootOwnerDetail(window *coremetadata.Window) string {
	return fmt.Sprintf("the active tmux pane %s resolves to window %q, which has no owning Project or ControlSession in the registry",
		o.paneID, window.Metadata.Name)
}

// activePane resolves the registry Pane the active tmux pane mirrors.
func (o activeTargetObserver) activePane(registry coremetadata.Registry) (*coremetadata.Pane, string) {
	uid := o.mirroredPaneUID()
	if uid == "" {
		return nil, fmt.Sprintf("the active tmux pane %s carries no %s", o.paneID, tmuxopts.PaneUID)
	}
	pane, ok := registry.Pane(uid)
	if !ok {
		return nil, fmt.Sprintf("the active tmux pane %s mirrors pane uid %q, which is not in the registry", o.paneID, uid)
	}
	return pane, ""
}

// activeWindow resolves the registry Window the active tmux pane's window
// mirrors. The window-scoped option is read through the pane target, which is
// what tmux resolves for a window-scoped format.
func (o activeTargetObserver) activeWindow(registry coremetadata.Registry) (*coremetadata.Window, string) {
	uid := o.mirroredWindowUID()
	if uid == "" {
		return nil, fmt.Sprintf("the active tmux pane %s carries no %s", o.paneID, tmuxopts.WindowUID)
	}
	window, ok := registry.Window(uid)
	if !ok {
		return nil, fmt.Sprintf("the active tmux pane %s mirrors window uid %q, which is not in the registry", o.paneID, uid)
	}
	return window, ""
}

// activeTargetError is the refusal of an active target that maps onto no
// registry resource.
//
// It is deliberately NOT the ordinary "matched N, want exactly one" cardinality
// failure. That message plus its candidate listing reads as garden-variety
// ambiguity and would hide the real cause, which is that the pane the operator
// is sitting in is not a Projmux resource at all -- the common case for any pane
// created outside the registry-backed routes. So the refusal names what was
// inspected, states plainly that nothing was selected, and tells the operator
// how to proceed. Nothing is selected, nothing is created, and nothing is
// written.
//
// It is a *selector.SelectorError with no Want and no Candidates, so it carries
// the metadata usage-error marker (exit code 2, zero bytes on stdout) while
// staying distinguishable from a no-match: SelectorError.IsNoMatch is false, so
// it does not unwrap to metadata.ErrNotFound.
func activeTargetError(kind coremetadata.Kind, detail string) error {
	return &selector.SelectorError{
		Op:     "resolve " + strings.ToLower(string(kind)),
		Detail: "no selector was given and " + detail + "; nothing was selected, so pass an explicit resource reference or --selector",
	}
}

// withActiveTargetRef narrows a query onto the resolved active target.
//
// It only ever fills the occurrence list of the route's own target kind, and it
// is only reached when the whole query was empty, so it can never blend an
// implicit target with an explicit scope the operator typed.
func withActiveTargetRef(query selector.Query, kind coremetadata.Kind, ref selector.Ref) selector.Query {
	switch kind {
	case coremetadata.KindProject:
		query.Project = &ref
	case coremetadata.KindWindow:
		query.Windows = []selector.Ref{ref}
	case coremetadata.KindPane:
		query.Panes = []selector.Ref{ref}
	case coremetadata.KindAgent:
		query.Agents = []selector.Ref{ref}
	}
	return query
}
