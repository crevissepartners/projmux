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

A same-ref call returns the original immutable receipt and never pushes again.
Changing its payload is refused with the earlier ref and cause. A fresh ref
allows one new attempt only when every previous attempt is known-zero. Failed
records and the original request remain available; recovery never deletes or
resets them. Store capacity can refuse a new attempt rather than discard its
history.

Delivered replies, partial writes, unknown outcomes, pending attempts, expired
deadlines, and stale routes must not be resent. Inspect their status and the
provider outcome. A false `outcomeUnknown` flag by itself is insufficient:
the stored state and specific zero-write reason must agree. These rules also
apply after store reload and to concurrent callers. Automatic resend is
disabled, and replies whose delivery target is Codex do not gain a new retry
policy here.
