# Claude coordination endpoint registration

Phase 1 binds a Claude-created endpoint to a managed Agent activation. It does
not implement message delivery, a provider message schema, a message/wait parser,
or a new public provider command. `agent capabilities` reports the exact Agent's
`runtimeEligibility.coordination` readiness and evidence. Static provider queries
remain static, and existing action cells stay unchanged.

`AgentRouteRef` shares stable Agent UID, Pane UID, and activation generation.
Its sealed provider authority union consumes the existing Codex thread and
`CodexAuthorityRef` composite unchanged. Claude authority instead consists of the
actual public SessionStart session ID, kernel process birth identity, a fresh
registration generation, and the kernel identity of the helper retaining its
registration lease. No Claude connection/binding epochs are synthesized.

Managed Agent/Pane names and Claude session titles are not route authority.
Renaming an unchanged UID/activation preserves its registration. Phase 1 does
not discover unmanaged provider names or observe live Claude `/rename` events.
Competing session/process/helper claims for an exact activation are refused;
the exact child's next SessionStart atomically claims a new registration
generation before launching its helper. Delayed admission and cleanup from an
older generation cannot overwrite or clear that newer registration.

The activation gate records the actual child PID and kernel birth identity
before replacing itself with Claude. The separate managed SessionStart hook
uses `exec`, making the registered provider its direct parent. The helper
verifies the complete helper → hook → provider process chain while the hook
waits for a bounded startup acknowledgement. A nested unmanaged Claude cannot
register its own endpoint through inherited activation environment variables.
The creator-selected Registry path travels only in private Claude activation
context, independent of a tmux server's older XDG environment.

The hook obtains the socket and credential solely from Claude's documented
`CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN` environment.
These values pass to the helper over an anonymous pipe and remain in process
memory. They are absent from Registry, helper argv, helper environment, public
capabilities, diagnostic messages, and the content-free cleanup receipt.
Projmux does not predict, issue, read a vendor registration file for, connect
to, or write to the Claude inbox.

Readiness validates exact Registry ownership/generation, provider and helper
kernel birth identity, socket type/owner/mode and unchanged inode. The read-only
capability query probes only a short, private Projmux readiness socket and
authenticates its kernel peer PID and birth. Linux birth identity includes boot
ID and start ticks; Darwin uses kernel process start time. The private socket
path fits both supported Unix socket limits and does not depend on TMPDIR.

The helper invalidates on provider exit, socket replacement, or loss of exact
Registry authority. The supervisor independently detects helper death while
the provider remains alive, clears only that exact lease, and removes orphaned
helper files. Provider exit keeps the existing lock-free termination journal
boundary; it never waits for the cleanup watcher's Registry transaction.
Provider sockets are never removed by Projmux. Generation replacement and
termination convergence discard old registration observations.

The required deterministic process integration is
`test/integration/claude-endpoint-binding.sh`. It is included in
`make test-integration`. The opt-in `TestClaudeEndpointInstalledSourceGate`
requires `PMX_TEST_CLAUDE_ENDPOINT_BIN`, `PMX_TEST_REAL_CLAUDE_BIN`, and a prepared
disposable authenticated `PMX_TEST_REAL_CLAUDE_CONFIG_DIR`. The harness creates
its own provider config with an authentication symlink; it never reads or copies
the caller's credentials. Its one-shot uses
strict empty MCP, empty tools, empty setting sources, and no session persistence;
it checks public init tools/MCP/plugins and tool-use are all zero. Raw public
stream and messaging credentials stay in memory; only allowlisted receipts
are reported. The token scan covers all owned files, including provider source
state. Upstream may store its own public locator; the test observes only a
boolean and removes the entire owned provider config before reporting cleanup.
Projmux Registry, helper files, diagnostics, and evidence contain neither value.
Inbound cross-session messaging is explicitly refused during the one-shot.
This is historical Phase 1 registration-source evidence only. It does not
establish or relax Phase 4 qualification: safe mode disables hooks and cannot
substitute for the separately owned, long-lived, hook-enabled current-version
public-init and marker gate.

The live canary accepts only paired, zero-output lifecycle events for its two
owned SessionStart callbacks before init, correlated by exact session, hook ID,
name, and event. It strips the empty output fields before storing evidence.
Unobserved hook names, Setup, plugin installation, unknown fields/events,
nonzero output, and incomplete pairs fail closed before any peer push. This
startup ordering follows the [public stream contract](https://code.claude.com/docs/en/headless#read-session-metadata)
and [hook lifecycle schemas](https://code.claude.com/docs/en/agent-sdk/typescript#sdkhookstartedmessage).
The observed 2.1.263 public init also carries `capabilities` (protocol feature
identifiers), `fast_mode_disabled_reason` (`sdk_opt_in_required`), and boolean
`analytics_disabled` / `product_feedback_disabled` metadata. These fields are
validated by type and known shape; none grants tool, plugin, or messaging
authority. The owned wrapper sets the documented
[`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`](https://code.claude.com/docs/en/data-usage)
opt-out. A feedback metadata boolean is not a record of external traffic.
The observed `system/thinking_tokens` event carries only numeric progress
estimates. The collector accepts its exact public shape after init for the same
session, validates finite nonnegative numbers, and gives it no reply or tool
authority. It does not carry or collect thinking text.
Public assistant `request_id` and timestamp are validated metadata, never reply
selectors. The observed `context_management`, `diagnostics`, and `stop_details`
fields are accepted only as null. A public Messages API thinking block is
validated in memory, then replaced by an omitted-content marker before disk;
neither reasoning text nor signature is retained or sent to the provider. Only
text blocks can provide semantic reply evidence; tool and unknown blocks fail
closed. See the public [Messages schema](https://platform.claude.com/docs/en/api/http/beta/messages/create)
and [ThinkingBlock schema](https://platform.claude.com/docs/en/api/typescript/messages).

## Phase 4 push ingress and heterogeneous dialogue

The registration helper owns the existing private `coord-*` Unix socket for
the same lease. Its address is derived from the exact `AgentRouteRef`; names,
tmux `%N`, the provider socket, and the provider token do not enter that address
or its protocol. Every operation revalidates Agent UID, Pane UID, activation
generation, provider/helper process births, registration generation, and route
incarnation. Normal exit and cleanup unlink only the exact owned coord socket
inode. Projmux never creates a replacement provider listener or relay.

Claude ingress is immediate push. There is no receiver waiter, pending ingress
queue, `asyncRewake`, `begin-handoff`, or `no-waiter` state. The helper retains
the opaque `CLAUDE_CODE_MESSAGING_SOCKET` and token only from the original
anonymous bootstrap pipe, removes both keys from its environment, validates the
unchanged 0600 provider socket and exact provider peer process, then performs
one write containing the documented auth line followed by the single frozen
current-version user frame:

```json
{"type":"user","message":{"role":"user","content":"..."}}
```

No control, status, reply, or inferred vendor frame is implemented. A known
failure before any byte is a non-ambiguous `provider-write-zero`; partial bytes,
a write error after bytes, or a lost helper response is ambiguous and has
`autoResend=false`. A complete write plus helper return is only a transport
handoff, not proof that Claude parsed, displayed, processed, or answered it.

The frozen frame is closed to Claude Code `2.1.263`. A different provider
version, helper replacement, provider restart, registration replacement, or
activation restart inherits no qualification. An ordinary send cannot qualify
the endpoint. The dedicated opt-in command requires a private 0600 sanitized
collector record bound to the same public init session/process/Pane/generation
and helper route, exact version, empty tools/MCP/plugins, zero pre-marker tool
use/stderr, frozen stream, and inbound policy `accept`:

```sh
projmux agent message qualify uid:<claude-agent> \
  --evidence /absolute/owned/private-init.json \
  --confirm-isolated-provider-push -o json
```

The helper pushes a unique non-secret qualification marker. Only the exact
ordinary official `Stop.last_assistant_message` at the unchanged safe-boundary
epoch opens helper-memory eligibility. A missing/mismatched/recursive Stop,
`UserPromptSubmit`, timeout, target exit, or post-write uncertainty closes the
attempt as ambiguous/no-auto-resend. Late Stop cannot reopen it; retry requires
a new explicit command. A version string supplied by the caller is never proof.

After qualification, `agent message send` persists the exact immutable broker
handoff before any provider byte. A missing, mismatched, or unwritable durable
record therefore writes zero. The pushed content is a structured
`projmux-coordination` object with `untrusted-coordination-only` authority and
the exact source/target routes, `messageRef`, `conversationRef`, `replyTo`, and
payload. It cannot start or steer a user turn, answer an approval, interrupt a
turn, execute a tool, call a connector, or write Codex app-server/model history.

Reply egress uses only documented official `Stop.last_assistant_message` plus
one delivered Projmux-owned pending record at the same boundary. Push ingress
correlates only an ordinary Stop (`stop_hook_active=false`); recursive Stop,
`UserPromptSubmit`, multiple candidates, or authority drift fails closed.
Assistant wording is never a selector. The reply reverses the routes, preserves
`conversationRef`, sets `replyTo`, and completes only when the original exact
Codex Agent self-claims it.

| Receiver state or transition | Product result |
| --- | --- |
| Before readiness or qualification | Refused/failed terminal, held count zero, provider writes zero |
| Ready and idle | Immediate one-frame push; model visibility is separate evidence |
| Active tool/turn | Push never interrupts; next official human boundary makes reply correlation ambiguous |
| Provider exit or activation restart | Old generation writes and claims zero |
| Same-generation registration/helper replacement | Old incarnation writes zero; fresh exact-version qualification required |
| Provider version replacement | Old qualification is cleared; unqualified writes zero |
| Codex endpoint replacement | Old incarnation self-claim zero; exact new incarnation may claim |

Message state is receipt-only. It performs no Registry Agent-interaction or
tmux badge write, so it cannot overwrite `in_progress`, approval-required, or
their badges. Public terminal states remain terminal-once.

Explicit integration installs the SessionStart registration hook, one short
synchronous Stop reply hook, and one short synchronous UserPromptSubmit boundary
hook. It installs no long-lived ingress waiter and does not use `asyncRewake`.
Capability reads never modify settings. Preview, enable, and remove only the
owned hook entries:

```sh
projmux agent integrate claude --dry-run
projmux agent integrate claude
projmux agent integrate claude --remove
```

For a pre-install Running Claude whose activation has no registration, install
the hooks, let that process exit normally, then run
`projmux agent resume uid:<same-agent-uid>`. Resume preserves the same Agent UID
and creates no replacement Agent. Delivery remains zero until the resumed
process has a current registration and fresh exact-version qualification.
`agent capabilities uid:<agent> -o json` reports source registration readiness
separately from target `runtimeEligibility.coordination` and includes the exact
recovery action and non-secret route incarnation.

The only selectorless required E2E is offline scenario `L20`. It uses one
synthetic auth+frozen-frame fixture, official-hook-shaped Stop input, semantic
barriers, exact structured receipts, and no model or installed provider. The
real-provider observation is opt-in through
[`heterogeneous-dialogue-canary.md`](heterogeneous-dialogue-canary.md) and
`scripts/agent-dialogue-live-canary.sh`; each version-stress row must qualify
independently.

Provider sources: [SessionStart and Stop hooks](https://code.claude.com/docs/en/hooks)
and [cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging).

Once a delivered message has unresolved reply ambiguity (an overlapping human
turn, multiple pending messages, expiry, or an uncertain write/reply outcome),
a later idle Stop cannot restore automatic reply correlation. The helper keeps
push ingress available but refuses automatic replies for that incarnation.
Use the documented public recovery on the same Agent UID and qualify its new
activation before resuming automatic dialogue. Source and target routes are
revalidated after durable handoff and immediately before the provider write.

The final source check also asks the existing Codex broker to verify the exact
runtime, connection and binding lease. This read-only IPC observation binds
nothing and sends no provider request. An older broker that does not support
this observation refuses coordination; it is never restarted implicitly.
Helper store lock contention fails immediately. Concurrent official hooks
invalidate reply correlation without waiting for another hook to finish.

The live harness begins with the benign `Reply READY.` control. The later broker
payload contains its own exact acknowledgement request; the initial user turn
does not authorize hypothetical future messages. Qualification keeps the same
frozen user frame and its existing one-shot marker. Observed rate/result metadata
is validated by closed shape and reduced before persistence. A refusal, API
error, queued user turn, permission denial, or nonzero subagent activity closes
the pre-inbound gate. Neither rate status nor timing/cost metadata is authority.

Usage is validated and discarded before persistence. Server tool counters must
be zero and tool/subagent iterations empty. After the semantic reply, the harness
requires three successful results in the same session, the public assistant
reply marker, and empty provider/collector stderr through owned cleanup.

Public model IDs and usage labels are bounded strings, including provider aliases
and context suffixes. They are discarded without imposing identifier syntax or
using pricing metadata as a permission or correlation source.
