// Package resourcegraph is the one read model that joins the Registry's
// desired resource graph to what a single exact tmux server is actually
// running.
//
// Before it, every consumer built its own join. The read verbs derived Window
// and Pane status from a mirrored-uid observation, the reconciler kept its own
// matcher, the pickers listed tmux sessions directly, and the recovery
// diagnostic read a third, independent fragment view. Four joins meant four
// answers to "is this Window live", four answers to "may I mutate this pane",
// and no shared vocabulary for the objects that belong to nobody. This package
// exists so the next UI, controller, and diagnostic surface consume one
// resolution instead of adding a fifth.
//
// Five properties are load-bearing, and each is a decision rather than an
// implementation detail:
//
//  1. The Registry is the source of truth for managed identity and for the
//     logical desired topology. Runtime is an overlay: Resolve never invents,
//     adopts, deletes, or re-parents a resource, and a Registry row survives
//     every observation outcome. A tmux server that cannot be read downgrades
//     status to unknown; it never removes a row.
//
//  2. Attribution uses exact evidence only -- a mirrored uid, a mirrored owner
//     uid, a role option value, and the stable containment ids tmux itself
//     reports. Session name, working directory, and running command are
//     deliberately not consulted. Every heuristic merge available here would
//     silently attach an operator's unrelated shell to a managed resource, and
//     a wrong identity is worse than an unattributed object.
//
//  3. Resolve is pure. It takes a Registry snapshot and one Inventory value and
//     returns a Graph. It performs no filesystem access, no process execution,
//     and no tmux call, so a read can never materialize state, and the same
//     inputs always produce byte-identical output. The observation itself
//     happens in the adapter that fills Inventory.
//
//  4. Both host modes are first class. app-owned means projmux started the
//     server it is looking at; standalone means projmux is a guest on the
//     operator's own tmux. The same Registry produces the same managed rows and
//     the same managed identity under both; only the status overlay and the
//     classification of objects projmux does not own differ.
//
//  5. Transport is explicit. An Inventory is always taken through exactly one
//     named socket, absolute socket path, or nothing at all. There is no
//     implicit default-server probe: a graph with no transport is a
//     Registry-only snapshot that reports every runtime status as unknown,
//     which is honest, where a silent fallback would report another server's
//     objects as this one's.
package resourcegraph
