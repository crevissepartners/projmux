# Installed Codex compatibility (legacy observation)

The machine-readable ledger is retained as historical liveness evidence only.
It is **not** capability authority for `durable-zero-turn-resume` or
`remote-new-session`. The private generation pool that once owned that
conformance record has been removed; see
[`codex-native-required-migration.md`](codex-native-required-migration.md) for
the payload-free create behavior that remains.
The legacy [`codex-installed-capabilities.json`](codex-installed-capabilities.json)
schema separates method evidence from the semantic result:

- `method` records the CLI/RPC spellings used by one observation. Changing a
  wire method does not itself change capability support.
- `result` is exactly `supported`, `unsupported`, or `infra-error`.
  An unavailable endpoint and incomplete evidence are infrastructure errors,
  never evidence that the upstream capability is unsupported.
- `evidence` contains only content-free semantic facts. A supported turn-free
  attach requires an exact living tmux Pane and the same no-turn thread to stay
  loaded with runtime state `idle` or `active` after the creator connection is
  closed.
- `versions` independently records the installed CLI, managed payload, and
  running app-server tuple. `last_observed` names the canonical probe and run.

## Last observation

On 2026-09-02, installed tuple `0.152.0 / 0.152.0 / 0.152.0` produced a living
Pane observation for the old `turn-free-thread-live-attach` predicate. It is
now classified `infra-error/evidence-incomplete`, because liveness plus
`thread/loaded/list` did not prove stored resume and did not observe the exact
remote-new thread's first real turn. The canonical
`TestInstalledIsolatedPreTurnBootstrapSmoke` created a thread without a turn,
observed it in `thread/loaded/list`, started `codex resume --remote unix://` in
an exact isolated tmux Pane, closed the creating connection, then passed two
fresh loaded/runtime observations while the exact Pane remained alive. Runtime
status was `idle`; model, turn, and network calls were zero.

The checked-in observation preserves exact branch-head facts from hosted
[Actions run 33566050834](https://github.com/crevissepartners/projmux/actions/runs/33566050834),
attempt 1. Aggregate artifact `9823166206`
(`installed-codex-qualification-33566050834-1`) is the durable last
observation. It must not be cited as payload-free support.

The observation historically extended the earlier `pre-turn-attach` owner.
That hosted evidence remains run `33560743314`,
aggregate artifact `9821171919`, where the same tuple's direct pre-turn
qualification was `pass`. Neither pass is capability authority for the
payload-free predicates above.

Scheduled and manual `Installed Codex Qualification` artifacts use
qualification schema v2 and embed this schema-versioned capability ledger.
`PROJMUX_CODEX_EVIDENCE_RUN` records the exact Actions run/attempt; this ledger
records `github-actions:33566050834:1`.

## Canonical test list

- `TestCapabilityReducerSeparatesMethodFromSemanticResult` — method changes do
  not alter supported/unsupported/infra-error reduction.
- `TestCapabilityReducerKeepsUnavailableEndpointAsInfraError` — an unavailable
  endpoint cannot become unsupported.
- `TestInstalledIsolatedPreTurnBootstrapSmoke` — historical owner for
  turn-free start/read/loaded observation and live-Pane liveness; not a
  payload-free support verdict.
- `TestInstalledCensusDeletionReceiptHasOneOwnerPerPrimitive` — topology and
  protocol ownership plus the Phase 2 merge receipt.

## Fresh bounded lifecycle reads

Production control reads require an experimental app-server connection with a
qualified server version of at least `0.160.1`. The first qualified tuple is
Codex `0.160.1`; generated schema alone does not prove runtime support. Older
or unidentifiable servers fail closed before a turn write. A newer server's
method refusal also fails closed, without a full-history or cached-idle fallback.

Each fresh observation requests `thread/read` with `includeTurns: false`, then
`thread/turns/list` with `limit: 1`, `sortDirection: "desc"` and
`itemsView: "notLoaded"`. It repeats both requests on the owned transport and
requires identical lifecycle observations. The broker's exact thread, binding
and epoch fences still govern publication and mutation. Unknown state, missing
active turn, inconsistent observations and payload limits refuse before writes.
The existing cumulative byte, frame, retained-state, operation and cleanup
budgets apply to all four reads. Budget exhaustion preserves `payload-too-large`;
cancellation and transport failures preserve their typed causes.

`TurnCount` remains a total: it is exactly zero or one only when the latest page
has no older cursor, otherwise `-1` means unknown. Page length never substitutes
for the historical total. Fresh-start admission continues to require exact idle
lifecycle evidence, independently of that unknown historical count.

The API and its experimental status are described in the official
[app-server documentation](https://learn.chatgpt.com/docs/app-server). The
[0.160.1 thread processor](https://github.com/openai/codex/blob/rust-v0.160.1/codex-rs/app-server/src/request_processors/thread_processor.rs)
merges the loaded active-turn snapshot before paging legacy history. Its legacy
implementation can replay full history internally: bounded response traffic and
projmux retained state do not establish bounded provider-side replay cost.
Paginated history and legacy history require separate runtime qualification.
The opt-in `TestInstalledBoundedPaginatedGrowingLifecycle` exercises the installed
provider against growing synthetic history, an exact active turn, the original
operation deadline, owned cleanup and a surviving sibling connection.

Replacing an installed executable does not replace an already loaded broker or
observer image. Deployment acceptance must identify both loaded images and safely
reattach the existing Agent and thread before fresh reads and human Input/message
acceptance. Isolated fixtures do not establish that live deployment acceptance.
