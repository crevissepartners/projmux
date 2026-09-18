# Explicit reply recovery

An explicit Claude reply names the original request with `--reply-to`. The
broker preserves that request's `conversationRef`, reverses its exact source
and target routes, and retains peer, untrusted, coordination-only authority.
Both activations and the original deadline must still be current. A reply
cannot extend that deadline. Source metadata remains an unverified routing
claim; an explicit reply also requires the registered provider's descendant
caller and any existing qualification and execution guard.

When a reply fails, `agent message send` exits nonzero and reports its ref,
underlying reason, outcome uncertainty, and safe action. Inspect the durable
attempt before deciding whether to retry:

```sh
projmux agent message status <failed-reply-ref>
projmux agent message status <failed-reply-ref> -o json
```

Only a confirmed zero-write Claude failure permits a new manual attempt.
Examples include rejected content or frame size, invalid auth configuration,
pre-write refusal, and an actual zero-byte write. Correct the reported cause,
then send a new reply ref on the same original correlation:

```sh
projmux agent message send uid:<original-source-agent> --reply-to <original-request-ref> --message-ref <fresh-reply-ref> -- '<corrected reply>'
```

Omitting `--message-ref` generates a fresh ref. In the qualified reply-only
execution guard, use the existing bounded command without that flag:

```sh
projmux agent message send uid:<original-source-agent> --reply-to <original-request-ref> -- '<corrected reply>'
```

Operator input from the web client has no Agent route to reverse, so a reply
to it is refused with `explicit-reply-operator-origin` and stores nothing.

A same-ref call returns the original immutable receipt and never pushes again.
Changing its payload is refused with the earlier ref and cause. A fresh ref
allows one new attempt only when every previous attempt is known-zero. Failed
records and the original request remain available; recovery never deletes or
resets them.

Store capacity treats the two kinds of attempt differently. A first attempt,
one whose original has no stored attempt yet, is a new acceptance rather than a
recovery, so a full store reclaims room for it under the same rule a new
message uses, while pinning the original and every attempt already stored
against it. A recovery attempt, which follows an earlier known-zero attempt, is
still refused with a capacity error rather than discarding the history it is
recovering from. Either way a reclaimed record moves to the history log below
instead of being deleted, and a store with nothing reclaimable refuses both.

Delivered replies, partial writes, unknown outcomes, pending attempts, expired
deadlines, and stale routes must not be resent. Inspect their status and the
provider outcome. A false `outcomeUnknown` flag by itself is insufficient:
the stored state and specific zero-write reason must agree. These rules also
apply after store reload and to concurrent callers. Automatic resend is
disabled, and replies whose delivery target is Codex do not gain a new retry
policy here.

## Reclaimed records move to a history log

The store is a bounded hot inbox. When it accepts a new message, or a first
explicit reply attempt, it first reclaims records that went terminal more than
24 hours ago, and then, if it is still at its record limit, the oldest
unprotected terminal record. Before either step it expires any accepted or
held record whose deadline has passed, exactly as a status read would, so such
a record becomes terminal at that moment and follows the same reclaim rules;
its 24 hours count from that expiry, not from its deadline. Reclaiming is not
deleting: every reclaimed record is appended to
`<state>/agent-messages/history.jsonl`, one JSON object per line, under the same
file lock and with the same private directory and file permissions as the store.

The append is fsynced before the store renames its own new file into place, so a
crash in that window can leave a record in both the store and the log, but never
in neither.

Each line carries the envelope's own key names:

```json
{"schemaVersion":1,"evictedAt":"…","reason":"retention","adapter":"claude-coordination",
 "messageRef":"…","conversationRef":"…","replyTo":"…",
 "state":"delivered","deliveryReason":"unspecified","handoffObserved":false,
 "acceptedAt":"…","terminalAt":"…","payloadBytes":466,
 "source":{"agentUID":"…","provider":"…"},
 "target":{"agentUID":"…","provider":"…"}}
```

`reason` is `retention` for the 24-hour rule and `capacity` for the record
limit. `replyTo` is omitted when the record is not a reply. `outcomeUnknown`
appears, as `true`, only on a failed record whose outcome is unknown; it is
absent otherwise. `source` and `target` carry only `agentUID` and `provider`:
the Pane, activation generation and incarnation fence a live delivery and mean
nothing once the record has left the store. The envelope's `deadline` is not
written. A line for operator input (see
[Operator input](claude-coordination-endpoints.md#operator-input)) carries
`"origin":{"kind":"operator","client":"web"}` in place of `source`; an Agent
message's line has no `origin`. Every other key is always present.

Lines written by earlier builds may still be in the same file. They carry full
routes (`paneUID`, `activationGeneration`, `incarnation`), a `deadline`, and
`outcomeUnknown` even when it is `false`, and they read the same way: a reader
ignores keys it does not expect and reads an absent key as its zero value, so
both shapes give the same edges, states and times. Both are `schemaVersion` 1.

`schemaVersion` starts at 1 and follows the same rule as the
coordination frame's field of that name: an absent or zero value reads as 1, and
a reader that meets a higher version reads the fields it knows rather than
discarding the line or failing. It is independent of the durable envelope
version and of the store file version.

The log does not keep the message body. It records `payloadBytes`, the original
payload length, and has no `payload` key at all: the log is unbounded in time,
and its intended consumers need the message graph, not its text.

The active log rotates to `history.jsonl.1` once it would pass 8 MiB, replacing
any earlier `history.jsonl.1`. At most two generations exist, so the log's disk
use is bounded even though its history is not complete.
