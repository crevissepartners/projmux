// Package codexbroker is the in-process endpoint broker that multiplexes one
// shared Codex app-server connection across many independently bound threads.
//
// The package is deliberately dark: it owns no OS process, no socket, no
// discovery, no tmux, and no Registry. It reaches the endpoint only through an
// injected Opener whose production default is the Phase 0 attach seam, so it
// can never start, stop, restart, kill, configure, or log in to the upstream
// daemon.
//
// Two invariants shape every type here.
//
// Authority is two-axis. A delivery or a mutation is allowed only when the
// connection epoch and the binding epoch both match the current ones. Durable
// binding is never derived from a working directory, a wall clock, a pid, a
// pane order, or a first match; it is always the exact thread id the caller
// bound.
//
// State is content-free. Refusals, telemetry, and the write ledger are closed
// string constants and counters. Prompt, response, and command bodies pass
// through Event.Params and Mutation.Params as opaque bytes and are never
// copied into anything this package retains.
package codexbroker
