// Package registryview projects a resolved resource graph onto the rows the
// primary navigation surfaces list.
//
// The primary surfaces used to enumerate the machine: a filesystem scan
// produced the Projects, `list-sessions` decided which of them existed, and a
// Window or a Pane was whatever tmux happened to be running. That made the
// Registry -- which owns managed identity and the desired topology -- invisible
// exactly when it mattered most. A Project whose session was closed vanished
// from the list that was supposed to let an operator reopen it, and the same
// Registry produced different rows on two different tmux servers.
//
// This package inverts the direction. Rows are enumerated from the Registry, in
// Registry order, and the runtime is an overlay on rows that already exist. The
// consequences are the contract:
//
//   - Identity and order do not depend on tmux. The same Registry produces the
//     same rows in the same order on an app-owned server, on the operator's own
//     standalone server, and outside tmux entirely. Only Status differs, and it
//     differs per exact host, which is what a status is for.
//   - A refresh cannot re-identify a row. Opening or closing a runtime object
//     changes Status and nothing else, so a selection survives it.
//   - Filesystem discovery is still shown, but it is not the Project list. A
//     scanned directory is a candidate for bootstrapping a Project, and it
//     lives in its own section where it cannot be mistaken for one.
//   - Runtime-only objects are absent by construction. The Home control
//     session, a scratch session, an operator's own shell, anything on a guest
//     server -- none are Registry resources, so none can appear among managed
//     rows. They are counted here and reached through the Runtime diagnostics
//     surface, which is the escape hatch that keeps "correctly absent" from
//     being indistinguishable from "lost".
//
// Home is deliberately not a row. It is an app control runtime with no
// resourceRef, and the only evidence that a session is one is the exact
// @projmux_session_role option the graph reads. A session named "home" with no
// marker is honestly unattributed; inventing a control role from a name is the
// same heuristic identity merge the resolved graph exists to refuse.
//
// Build performs no I/O and mutates neither argument. Every write decision --
// materialize, reconcile, adopt -- is somewhere else on purpose: a navigation
// refresh must be indistinguishable, from the machine's point of view, from not
// having run it.
package registryview
