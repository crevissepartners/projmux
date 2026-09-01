# Installed Codex compatibility

The machine-readable last-observed ledger is
[`codex-installed-capabilities.json`](codex-installed-capabilities.json). Its
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

On 2026-09-02, installed tuple `0.152.0 / 0.152.0 / 0.152.0` was
`supported` for `turn-free-thread-live-attach` with reason
`live-pane-attached`. The canonical
`TestInstalledIsolatedPreTurnBootstrapSmoke` created a thread without a turn,
observed it in `thread/loaded/list`, started `codex resume --remote unix://` in
an exact isolated tmux Pane, closed the creating connection, then passed two
fresh loaded/runtime observations while the exact Pane remained alive. Runtime
status was `idle`; model, turn, and network calls were zero.

The checked-in observation is the exact branch-head evidence from hosted
[Actions run 33566050834](https://github.com/crevissepartners/projmux/actions/runs/33566050834),
attempt 1. Aggregate artifact `9823166206`
(`installed-codex-qualification-33566050834-1`) is the durable last
observation; the roadmap gate must cite that same run, artifact, and canonical
probe.

The observation extends the Phase 1 `pre-turn-attach` owner instead of adding a
second protocol body. Phase 1 hosted evidence remains run `33560743314`,
aggregate artifact `9821171919`, where the same tuple's direct pre-turn
qualification was `pass`. That pass is an input only: the ledger's supported
result additionally requires the Phase 2 loaded/runtime and living-Pane facts.

Scheduled and manual `Installed Codex Qualification` artifacts use
qualification schema v2 and embed this schema-versioned capability ledger.
`PROJMUX_CODEX_EVIDENCE_RUN` records the exact Actions run/attempt; this ledger
records `github-actions:33566050834:1`.

## Canonical test list

- `TestCapabilityReducerSeparatesMethodFromSemanticResult` — method changes do
  not alter supported/unsupported/infra-error reduction.
- `TestCapabilityReducerKeepsUnavailableEndpointAsInfraError` — an unavailable
  endpoint cannot become unsupported.
- `TestInstalledIsolatedPreTurnBootstrapSmoke` — the sole installed owner for
  turn-free start/read/loaded observation and live-Pane attach.
- `TestInstalledCensusDeletionReceiptHasOneOwnerPerPrimitive` — topology and
  protocol ownership plus the Phase 2 merge receipt.

The maintained repository-wide list in `docs/agent-workflow.md` should gain the
same Phase 2 row only after its parallel owner has merged; this change does not
edit that shared file.
