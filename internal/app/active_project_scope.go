package app

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// The active Project namespace of an explicit singular reference.
//
// A `metadata.name` is unique inside its owner scope, never across the
// registry: a Window name is unique inside its Project, a Pane name inside its
// Window or Agent, an Agent name inside its Window. So a bare `describe window
// zsh` typed inside a managed Project used to compare the operator's word
// against every Project at once and fail the exact-one cell on names that are
// not in fact in conflict with each other. The fix is a namespace, not a
// smarter target rule: inside tmux the search universe of a singular
// Window/Pane/Agent reference is the Project that owns the active Window,
// exactly the universe `get windows|panes|agents` already reads.
//
// Four edges are deliberate, and each is the reason this is a separate seam
// from the implicit active target in active_target.go:
//
//  1. It narrows, it never selects. The Project fills selector.Query's
//     DefaultProject and nothing else, so the ref the operator typed is still
//     the only thing that picks a resource. The active Window and the active
//     Pane are not pushed down into the query, and two same-named resources
//     inside the one Project stay the ordinary bounded exact-one ambiguity.
//  2. Explicit `--project`/`-p` wins with zero observations. The lookup is not
//     consulted at all, so naming a Project can never cost a tmux round trip
//     or fail because the surrounding pane is unmanaged.
//  3. Outside tmux nothing changes. The observation reports "not inside a
//     client", the query keeps no default, and the whole-registry result --
//     including its `matched N ..., want exactly one` ambiguity -- is
//     byte-identical to the pre-namespace contract. No default server is
//     probed.
//  4. A broken owner chain inside tmux refuses. It does not fall back to the
//     whole registry, because a silent global fallback is exactly the
//     cross-Project match this seam exists to prevent.
//
// The empty-selector case is not this seam. An invocation with no reference at
// all keeps meaning the exact active resource, resolved by activeTargetRef.

// activeProjectScopeRef resolves the Project that scopes an explicit singular
// reference.
//
// The three results mirror activeTargetRef's: (ref, true, nil) resolved the
// namespace, (_, false, nil) means the invocation is not inside a tmux client
// and the caller must keep its whole-registry behavior, and (_, false, err) is
// the refusal of an active target whose Project owner chain is broken.
func activeProjectScopeRef(lookup activeTargetLookup, registry coremetadata.Registry) (selector.Ref, bool, error) {
	uid, resolved, detail := activeUID(lookup, coremetadata.KindProject, registry)
	switch {
	case resolved:
		return activeUIDRef(coremetadata.KindProject, uid), true, nil
	case detail == "":
		return selector.Ref{}, false, nil
	default:
		return selector.Ref{}, false, activeProjectScopeError(detail)
	}
}

// activeProjectScopeError is the refusal of a namespace that could not be
// derived inside tmux.
//
// It is not activeTargetError: that message opens with "no selector was given",
// which would be a plain falsehood here -- the operator typed a reference, and
// what failed was the scope it was going to be resolved in. Reporting the
// wrong cause would send the operator looking for a target that is not the
// problem, so this names the scope and offers the flag that fixes it.
//
// Like every other selector refusal it is a *selector.SelectorError with no
// Want and no Candidates: exit code 2, zero bytes on stdout, and IsNoMatch
// false so it does not unwrap to metadata.ErrNotFound. Candidates would be
// wrong here for the same reason they are wrong on the containment refusal --
// there is no ambiguity to list, the search never ran.
func activeProjectScopeError(detail string) error {
	return &selector.SelectorError{
		Op: "resolve project scope",
		Detail: "a resource reference was given inside tmux and " + detail +
			"; the active Project namespace is undecidable, so nothing was selected -- pass --project <ref> to name the scope explicitly",
	}
}

// singularProjectNamespaceKind is the verb-independent half of the applied
// matrix: the kinds whose universe a Project actually encloses.
//
// Project is absent because a Project has no enclosing Project -- the selector
// engine's ResolveProjects never consults DefaultProject, so setting one would
// be a silent no-op that reads like a contract. Which verbs opt in is the other
// half, decided where each route builds its flags.
func singularProjectNamespaceKind(kind coremetadata.Kind) bool {
	switch kind {
	case coremetadata.KindWindow, coremetadata.KindPane, coremetadata.KindAgent:
		return true
	default:
		return false
	}
}
