// Package controller is the command-scoped kernel that turns one resolved
// resource graph into one authorized, guarded, deterministically ordered plan.
//
// Before it, "may I touch this tmux object?" was answered in three places with
// three different predicates. The reconciler asked it by re-deriving its own
// session/window/pane matcher from a shadow tmux runner. The materializer asked
// it with an ownership preflight of its own. The read verbs did not ask it at
// all, because they never wrote. Three predicates meant three blind spots, and
// each one authorized a mutation. This package exists so a mutation is
// authorized once, from the one attribution vocabulary the resolved graph
// already publishes, and so the next producer -- a hook trigger, a create, a
// UI action -- inherits the same refusals instead of writing a fourth matcher.
//
// Four properties are load-bearing, and each is a decision rather than an
// implementation detail:
//
//  1. Authority is a closed table, not a predicate. Intent x attribution is a
//     finite cross product, it is enumerable, and Table renders all of it. A
//     policy you can print is a policy a table test can pin; a policy expressed
//     as scattered `if` statements is one whose forbidden cases are discovered
//     in production. Start, import, and delete are refused for every class in
//     this kernel, so "the controller started something" is not a bug that can
//     be introduced by a new caller -- it is a row that would have to be
//     rewritten first.
//
//  2. The plan is pure and totally ordered. Planning takes a graph and returns
//     actions sorted by a stable key, so a dry-run and the execute that follows
//     it project the same bytes, and a repeat after a successful execute is
//     provably empty rather than merely usually empty. Nothing in this package
//     performs I/O; the orchestration seam that does lives in the app layer and
//     consumes these values.
//
//  3. A guard is exact evidence, captured at plan time, re-read before the
//     first live write. tmux ids are recycled: the `%7` that was a managed Pane
//     when the plan was built can be an unrelated shell by the time the plan
//     runs. Guarding on the mirrored uid and on the containing object's id is
//     what makes a stale plan abort instead of landing a Registry-owned mirror
//     on somebody else's pane.
//
//  4. Refusal is an outcome, not an error. A foreign object, a contradicted
//     uid, and an offline resource each produce a recorded action with a stated
//     reason and no write. Dropping them silently would make "nothing to do"
//     and "I am not allowed to do this" indistinguishable in the one report an
//     operator reads.
package controller
