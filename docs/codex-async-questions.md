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
