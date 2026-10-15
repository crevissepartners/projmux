# Headless Codex async questions

Headless Codex can emit a question as an `item/started` or `item/completed`
notification whose item is an `agentMessage` with `delivery: "async"`, a
`call_…` item ID, and `questions` containing titles and string options. These
notifications are distinct from blocking `item/tool/requestUserInput` server
requests. A message's `phase: "final_answer"` does not mean the turn ended.

`projmux agent question list -o json` and SDK `ListQuestions` expose these
questions with `delivery: "async"` and `state: "waiting"`. A current host that
supports answer receipts sets `canAnswer: true` while the captured deadline and
ownership remain valid. Older hosts remain read-only (`canAnswer: false`).
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

Submit explicit answers to every prompt in the set through the existing
`agent question answer` command or SDK `AnswerQuestion`, using the opaque ID.
Options, indexes, and free text retain their existing validation; partial
submissions and mixed option/free-text selections are refused. If any prompt
is secret, the entire set requires the native surface
(`question-secret-native-only`). Blocking Codex requests and Claude questions
retain their existing answer paths.

An async answer uses Codex's ordinary operator input: `turn/steer` while active
or `turn/start` while idle. The input includes the original item ID, prompt
text, and explicit answers. There is no separate native per-question answer
RPC or acknowledgement. Peer messages do not submit operator answers.

Successful submission returns `Delivery: "async"`, `Outcome: "submitted"` and
the same question ID. The CLI prints `submitted`. Readback shows
`state: "closed"`, `disposition: "async-submitted"`, and `canAnswer: false`.
This confirms operator input acceptance and app receipt settlement; it does
not confirm model consumption or completion of the question.

After an uncertain delivery, the typed refusal is `question-delivery-unknown`.
The result preserves the question ID, `Delivery: "async"`, and
`Outcome: "unknown"`, including when an error is returned. Readback uses
`closed` / `async-unknown` / `canAnswer: false`. Do not retry an unknown
submission. A second attempt is refused with `question-not-pending`; that
refusal says nothing about the effects of the previous attempt.

Async free text is limited to 4096 UTF-8 bytes per prompt, counted without
trimming. The complete normalized zero-based `map[int]Selection`, encoded
with Go's `encoding/json`, is limited to 16384 bytes, including spaces and
JSON escapes. SDK constants `MaxAsyncQuestionTextBytes` and
`MaxAsyncQuestionSelectionsBytes` expose these limits. Private IPC retains
its 65536-byte frame limit, and native frame limits also apply. Oversized
answers return `question-answer-too-large` before submission, without
splitting or truncating input.

Missing IDs, expiry, changed ownership, and unsupported hosts return typed
refusals (`question-not-found`, `question-expired`, `question-stale` or
`question-closed`, and `question-host-unsupported`). A definite local busy
refusal is `question-busy`. Re-read before any corrected, explicit submission;
only `canAnswer: true` permits one. Transport uncertainty must never be inferred
from an error message or interpreted as proof that nothing changed.

After all validation, the host locks the form before attempting the durable
claim. If storage or a definite local admission fails at that point, provider
input is not written and the form stays locked. When storage remains writable,
readback uses `closed` / `async-not-submitted` / `canAnswer: false`; storage
errors remain errors. Handle this as “not submitted; form locked” and use the
native surface. It is distinct from uncertain delivery after the native call
boundary. The sticky host tombstone prevents old writers' normal pruning from
reopening the question, including after a controller replacement.

No submission timestamp is added. `updatedAt` is the store's last state change,
not a provider-acceptance or model-consumption timestamp. Terminal history may
disappear through normal pruning or generation replacement; absence is not
permission to recreate or resend an answer.

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
