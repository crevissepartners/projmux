# Headless Codex async questions

Headless Codex can emit a question as an `item/started` or `item/completed`
notification whose item is an `agentMessage` with `delivery: "async"`, a
`call_…` item ID, and `questions` containing titles and string options. These
notifications are distinct from blocking `item/tool/requestUserInput` server
requests. A message's `phase: "final_answer"` does not mean the turn ended.

`projmux agent question list -o json` and SDK `ListQuestions` expose these
questions with `delivery: "async"`, `state: "waiting"`, and `canAnswer: false`.
Titles and option labels are preserved, including questions with no options;
`isOther: true` describes free-text input. The opaque question key addresses
one exact process binding, connection, thread, original turn, and item. It
contains no native reply token. Started/completed duplicates and replays do
not create another question or renew its deadline.

Questions remain visible through the original turn's completion and later
turns in the same generation, until the captured question window expires or
process ownership ends. The host retains question metadata independently of
the event ring, within its existing request count and payload bounds. It
refuses additional items at capacity without evicting a retained question or
blocking request. Closed item tombstones consume the same bounded lifetime
budget and cannot reopen through replay. Store and projection failures are
reported, rather than recorded as successful collection. Exit, stop, stale
binding, and replacement generations fence the question; expired and terminal
records cannot be answered.

This first slice provides registration, reads, and attention. Every async
answer entry point rejects submission without writing provider input or
persisting an answer. Blocking Codex requests and Claude questions retain
their existing answer paths. Async questions can coexist with an active Agent
turn and do not put a native RPC on hold.

A later slice will validate an explicit operator answer against the same key
and send it through Codex's ordinary user input (`turn/steer` while active,
`turn/start` while idle). There is no separate native per-question answer RPC
or acknowledgement in this contract. Native input acceptance confirms receipt
of input, not model consumption or a completed question answer. Peer messages
are coordination and do not submit operator answers.

Async records use `<state>/agent-questions/.async/questions.json`, with their
own directory, flock, and atomic temporary files. The legacy
`agent-questions/questions.json` retains its existing format and bytes when
async metadata is written. Older running controllers can continue to read
that legacy file; upgrading does not require resuming or retiring all Agents.
Each namespace rejects records belonging to the other namespace, and read
errors from either file are surfaced.

The legacy persistent budget remains 64 records / 2 MiB. The async namespace
adds a separate 64 records / 2 MiB budget, for a combined 128 records / 4 MiB,
excluding atomic temporary-file peaks. Host request, turn, and queue limits
remain unchanged. Both stores retain terminal history for 24 hours and prune
it on subsequent writes or to make room at capacity. Process exit and lost
ownership fence async questions; Agent deletion and Project stop use those
existing process barriers. Canonical history files and reusable flock files
remain in the state directory, following the legacy retention policy. No new
Agent-delete or Project-stop file-deletion path is added.
