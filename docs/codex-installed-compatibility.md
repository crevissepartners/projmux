# Installed Codex compatibility (legacy observation)

The machine-readable ledger is retained as historical liveness evidence only.
It is **not** capability authority for `durable-zero-turn-resume` or
`remote-new-session`; those predicates require the exact executable tuple and
the Phase-1 conformance record described in
[`codex-native-required-migration.md`](codex-native-required-migration.md).
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
qualification was `pass`. Neither pass is an input to the new exact
payload-free capability authority.

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
- `TestInstalledExactPayloadFreeCapabilityMatrix` — exact private owner for
  zero-turn start/read/stored-resume plus content-free remote-new liveness. It
  sends no input or turn, so remote-new remains unknown.
- `TestInstalledCensusDeletionReceiptHasOneOwnerPerPrimitive` — topology and
  protocol ownership plus the Phase 2 merge receipt.

The maintained repository-wide list in `docs/agent-workflow.md` records the
current Phase-1 authority separately.
