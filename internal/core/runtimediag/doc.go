// Package runtimediag is the read-only projection of one resolved resource
// graph's runtime half: every tmux object on one exact server, with its
// attribution, its exact handle, and the reason it is classified that way.
//
// It exists because the Registry-first surfaces answer a different question
// than an operator debugging a machine does. The managed UI shows the Registry's
// desired topology, so an object projmux does not own is correctly absent from
// it -- and "correctly absent" is indistinguishable from "lost" without a
// surface that shows the machine as it is. This package is that surface's data,
// shared by the CLI read route and the diagnostics picker so the two cannot
// disagree about what is running.
//
// Four properties are load bearing:
//
//  1. It is a projection, not a second join. Every row comes from
//     resourcegraph.Graph, which already decided attribution from exact uid,
//     owner, and role evidence. Nothing here re-derives a class, and nothing
//     here consults a session name, a working directory, or a running command.
//
//  2. Every observed object is emitted, managed ones included. A managed object
//     that needs no repair is exactly the row an operator looks for when the
//     managed UI shows it and the machine seems not to; dropping it because it
//     is uninteresting to a reconciler would hide the answer.
//
//  3. An unreadable scope is a row of its own kind. Unavailability travels with
//     the report rather than being flattened into an empty item list, so "the
//     server has nothing on it" and "I could not look" stay different answers.
//     Outside tmux -- no transport at all -- every scope is unavailable and the
//     item list is legitimately empty.
//
//  4. It is pure. Building a report performs no I/O and mutates nothing, so a
//     read can never materialize state and the JSON schema is a function of the
//     graph alone.
package runtimediag
