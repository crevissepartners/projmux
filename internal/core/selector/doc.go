// Package selector resolves Projmux resource selectors against an already
// loaded resource registry.
//
// The package is pure. It performs no I/O, reads no clock, never touches tmux,
// and never mutates the registry it is handed: every exported entry point takes
// a value copy of the model and returns read-only projections. That is what
// makes it safe to use from read-only routes and, later, from the preflight
// half of a mutation route.
//
// # Selector grammar
//
// The CLI information architecture v2 selector grammar is deliberately narrow:
//
//   - --project takes at most one occurrence and resolves exact-one.
//   - --window and --pane are singular spellings; repeating an occurrence
//     unions the resolved uid sets.
//   - A value prefixed with "uid:" is an opaque Projmux uid. Any other value is
//     a metadata.name.
//   - --selector key=value filters the target kind by label; repeating it ANDs
//     the conditions.
//
// # What is deliberately not a selector
//
//   - Implicit comma splitting. A selector value is one literal token, so
//     "--window a,b" is a single name. metadata.ValidateName rejects ',' so no
//     resource can ever carry that name and the selector resolves to nothing.
//   - Invocation-scoped presentation context. It may duplicate and can never
//     be identity; ambiguity rendering receives it only after selection.
//   - Filesystem paths. Project spec.root is spec, not a query key, and '/'
//     cannot appear in a valid name.
//   - tmux ids. '%N', '@N', and '$N' are status transport; '%', '@', and '$'
//     cannot appear in a valid name.
//
// # Resolution order
//
// Resolution always runs in exactly three recorded stages, in this order:
//
//  1. StageNameUIDUnion  — union of the name/uid selector occurrences
//  2. StageLabelFilter   — AND of the --selector label conditions
//  3. StageUIDDedupe     — collapse repeated uids, keeping first occurrence
//
// Resolution.Trace records the surviving count after each stage so the order is
// observable rather than merely documented.
//
// # Cardinality
//
// Each <verb, kind> pair declares 0..N, exact-one, or 1..N. A violation is a
// usage error carrying a bounded candidate listing, which reaches CLI exit code
// 2 through app.MapMetadataError.
package selector
