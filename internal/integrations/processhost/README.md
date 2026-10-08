# Owned process host

This package supplies owned process hosts with Claude stream and Codex app-server
adapters. `create agent --host process --provider claude` (or `--provider codex`)
starts a public foreground owner without tmux. The
application writes Registry bindings and exact Wait receipts. This package does
not create tmux resources, adopt existing providers, or change user settings.
Operator-facing behavior is documented in
[Operating Process-Hosted Agents](../../../docs/process-host-operations.md).

## Schema v5 authority chain

The application supplies the schema v5 binding: host instance, Project, Window,
Agent, Pane, generation, and operation. Before a route reaches control,
`Registry.CurrentProcessActivation` verifies the Project → Window → Agent → Pane
owner references, provider, process runtime, and exact current activation.
Historical session records and retired generations cannot satisfy this check.
See [Registry schema v5](../../../docs/registry.md).

The host launches its dedicated supervisor through inherited descriptors. That
helper owns the provider process group and its child Wait; the host owns protocol
admission and stream draining. Cleanup authority comes from that live ownership,
not from discovering a PID in the Registry. The helper preserves the unreaped
group leader until signalling finishes, then records actual child exit.

Read-only application observation compares binding, provider, and the stored
host and child identities (PID, owner UID, and birth marker). For a live child it
also verifies the child → supervisor → host ancestry. Missing endpoints or
unverifiable births remain unknown; an exact supervisor receipt can project an
already reaped child as offline. Observation cannot grant control, adopt a
provider, or synthesize a successful exit.

## Host and inventory contract

`InventoryTarget` supplies an exact owned binding and an optional `Handle` for
one invocation. `ObserveInventory` converts only `Handle.Observe` results into
the pure resource graph inventory; it never discovers or starts a host.
Declarations remain present when a handle is unavailable or observation fails,
so the child stays unknown. A ready exact host is live; only actual child exit
evidence is offline. Inventory contains host, Pane, and generation identity,
without provider content or a fabricated tmux handle.

`NewHost` requires an explicit supervisor executable, transaction callbacks, and
limits. The consumer dispatches `ServeSupervisor` in that executable with inherited
file descriptors 3 (owner lifetime), 4 (launch spec), and 5 (status). The helper
accepts one launch, creates one dedicated provider group, drains no protocol data
itself, and exits after reaping its child and clearing the group. The host drains
stdout and stderr independently. After supervisor exit, both drains share one
grace deadline; inherited descriptors cannot delay Wait indefinitely. Closing an
unfinished drain emits a protected stream-gap before process-exited. Ordinary
EOF is reconciled with independent child Wait evidence; EOF alone is never exit0.
Linux uses a subreaper in the dedicated helper;
macOS waits for launchd to reap orphan descendants. The helper is not a daemon.
Before signalling a finished provider group, the helper observes exit without
reaping the group leader (Linux waitid WNOWAIT; Darwin owned-child SZOMB). Its
PID/PGID therefore remains reserved until group signalling finishes and actual
Wait consumes the exit status. Failed or unsupported exit observation yields
unknown
and does not recover cleanup authority from a stored PID or an already-reaped
group. Such a failure is not successful platform cleanup. Public entrypoint
wiring and durable process bindings require a later consumer.

The preparation handshake precedes delivery of the launch specification. A helper
that never prepares cannot have started a provider from that specification and is
killed and waited on. Startup cancellation closes the lifetime pipe and resumes a
stopped helper so it can clean up; a nonconforming helper is forcibly reaped and
reported as unknown. Loss of the supervisor is not an exit-zero receipt and does
not authorize PID-based adoption or cleanup. As with ordinary Unix process groups,
a descendant that deliberately escapes its owned group is outside this mechanism.

`Start` reserves the exact operation once. A retry must use the identical binding
and launch. Spawn only yields `starting`; the first real user input triggers
Claude init, and only an exact session plus a successful binding CAS yields
`ready`. `Current` validates ownership before each control write. Callbacks must
honor their contexts; they must not retain registry locks while waiting for I/O.
`Stop` closes control admission and expires pending requests, closes provider
stdin, then closes the owner lifetime pipe. The supervisor waits up to `Grace`
for SessionEnd and actual child exit before sending TERM to its owned group;
a further `Grace` without exit escalates to KILL. Owner lifetime loss and
supervisor termination signals use the same bounded grace sequence. A completed
child observation skips the remaining grace. Group cleanup and stream drain each
retain their separate grace budget. Startup rollback allows these four budgets
plus one grace of scheduling margin before reaping a nonconforming helper.
Repeated Stop is idempotent; a different binding is refused.

`Wait` reports actual child evidence. `Snapshot.Termination` uses the existing
metadata classifier, and the consumer offers it to `Mutator.RecordTermination`.
Turn results and interrupt acknowledgments never produce termination evidence.

`ClaudeCommand` preserves supplied launch arguments and environment, adding only
the stream transport. `Respond` is the only control-response writer: questions
carry answers in `updatedInput.answers`; permission decisions carry allow/deny.
Tokens bind connection, session and turn. Consumed/expired/stale responses never
write again. Consumer-owned deadlines use `Expire`; expiry and disconnection never
synthesize allow. Hook integration must consume these events rather than obtain a
second response authority.

All retained state is bounded. Defaults retain 64 launch operations, 256 output
and completed control-history events per lane, 32 pending requests, 8 KiB of
diagnostics, and frames below 1 MiB. Output cannot evict protected control
transitions. Completed control history expires independently and reports a
stream-gap; current-turn transitions and pending-request evidence remain
protected. Admission counts the live turn's transitions, not completed history.
Reserved expiry and termination evidence bounds protected overflow by
`Events + Requests + 6`.

Turn operation IDs retain a bounded stale fence of the most recent `Events`
operations, including the current operation. A recent duplicate is refused;
completed IDs outside that horizon are forgotten. Consumers must generate fresh
operation IDs and must not treat a gap as a receipt or retry an old operation
outside the retained horizon. Request IDs are scoped to the exact current turn.
Launch operation history is separate and is never evicted to spawn again.
Sequential completed turns therefore do not exhaust lifetime admission. Stream
gaps identify incomplete observation, not successful completion or an archive.

The deterministic suite covers operation concurrency, preparation and init
failure, binding rollback, multi-turn NDJSON, question/permission response races,
interrupt ack/result ordering, overflow, malformed/oversized frames, blocked
stdin, stdout EOF, stderr pressure, and existing metadata receipt guards.
`TestStopAllowsSessionEnd` verifies a 300ms SessionEnd marker after stdin EOF
within a 2s grace, actual exit0 with no signal, normal classification, and stale
and repeated Stop behavior. `TestStopEscalatesUnresponsiveProvider` checks the
TERM and KILL grace stages against actual child Wait.
`TestOwnerLifetimeReclaimsOnlyOwnedGroup` kills an isolated owner through normal
shutdown, stdin EOF, SIGTERM and SIGKILL; it checks child, descendant and helper
absence while a sibling stays alive. Linux unit/race CI and Darwin's native job
execute the package, not just compile it.

`PROCESSHOST_TEST_CLAUDE=1 go test ./internal/integrations/processhost -run
'^TestInstalledClaudeStream$' -v` opts into a real installed-Claude qualification.
By default it needs existing authentication, disables tools/hooks/MCP
configuration for its isolated process, verifies two turns on one session, and
checks that the user's settings file is unchanged. Questions and permission
allow remain fixture-only; the opt-in deny/cancellation probes below qualify
those narrower real-provider paths. None is public headless feature acceptance.

Provider cancellation is explicit: `control_cancel_request` expires only the
matching pending request on this connection; duplicate/unknown IDs have no effect
and no permission response is written. This shape is qualified against Claude
2.1.287 and the [official SDK control reader](https://github.com/anthropics/claude-agent-sdk-python/blob/bfb895c6ef46e095191938b4eda798a025957c09/src/claude_agent_sdk/_internal/query.py#L389).
Unknown nonempty frame types, including `command_lifecycle`, are bounded
`provider-event` observations. They never imply control authority or exit.
Malformed frames, session drift and frame-size violations remain protocol failures. Opt-in qualification can
set `PROCESSHOST_TEST_PERMISSION=deny` or `cancel` together with
`PROCESSHOST_TEST_CLAUDE=1`: a process-local Bash ask rule prevents automatic tool
execution; the probe only denies or interrupts, then verifies stale response
rejection and two subsequent turns. User settings remain untouched.

## Dedicated Codex stdio adapter

`CodexCommand` selects `codex app-server --listen stdio://` with an explicit
resolved executable, environment and settings arguments. `Host.StartCodex` owns
this app-server through the same supervisor, lifetime pipe and real child Wait
as Claude. It never uses a daemon, default proxy, broker, or fallback endpoint.
The public foreground create command uses this dedicated connection for scripts
and CI without tmux.

The host supplies a bounded owned stream to the existing `codexappserver.Client`.
Initialization negotiates the experimental capability, starts exactly one thread,
checks effective model, reasoning effort and sandbox/approval policy, then commits
the exact binding before ready. Operation retries return the same handle, including
failed starts, and never create another thread. A fresh thread has no durable
rollout before its first turn, so `thread/resume` cannot verify its initial settings.
The small `StartThreadWithSettings` extension checks the `thread/start` answer
using the existing settings verifier and sends requested effort through the native
`config.model_reasoning_effort` override. Existing thread/start callers retain their
wire format and policy checks.

A well-formed server refusal of turn/start produces a failed `turn-result`
with the consumed operation ID while preserving the session and owned child.
A refused interrupt produces `interrupt-refused`, without an acknowledgement or
an inferred turn completion. A refused turn operation stays consumed. A confirmed
interrupt refusal permits a later explicit attempt on the still-active turn;
uncertain transport outcomes do not permit replay.
Malformed protocol, transport loss, and uncertain request outcomes still stop
the owned connection. The typed refusal classifier preserves existing callers'
error classifications and request bytes.

`CodexHandle` keeps the Client private. Turn operations use the typed turn request
and consume their operation identity before any uncertain write. Interrupt uses
an exact provider turn ID from `Snapshot.Turn`; its acknowledgement and the eventual
turn result remain separate from child exit. Approval responses use the existing
safe-decision decoder and response builder. Question responses reuse the existing
Codex question parser and selection validation. Both carry the original scalar
request ID on the exact connection; numeric and string IDs remain distinct.
Stale, consumed or expired tokens write nothing. Unknown server requests and
connection loss fail explicitly and never grant permission. Hook registration and
consumer-owned deadline policy remain outside this adapter.

The same frame, diagnostic, output-event and protected-control limits apply.
The typed Client also has its own bounded notification backlog; overflow terminates
the connection explicitly rather than blocking control or Wait. Notifications are
drained independently of typed control waits. A reply queue bounded by the host's
`Events` limit preserves turn/start and interrupt ordering; exceeding that bound
fails the owned connection explicitly. Notifications before the turn/start answer
cannot be assigned to another turn. Stream closure unblocks owned reads and writes;
independent supervisor Wait remains authoritative.

`TestCodexDedicatedThreadSettingsAndOperationRetry`,
`TestCodexTurnsQuestionsApprovalsInterrupt`,
`TestCodexInitializationFailureRollsBackOnlyOwnedChild`,
`TestCodexBoundedStreamsWaitAndControl`, and
`TestCodexOwnerLifetimeReclaimsOnlyOwnedGroup` cover settings/identity, startup
rollback, exact responses, bounded I/O and owner EOF/TERM/KILL with a surviving
sibling. Linux unit/race and native Darwin CI run these fixtures on the same head.

`PROCESSHOST_TEST_CODEX=1 go test ./internal/integrations/processhost -run
'^TestInstalledCodexStdio$' -v` qualifies an installed Codex using isolated
HOME/CODEX_HOME and an in-process local stub model provider, with no credentials
or real model API calls. It checks two turn results on one owned thread, requested
settings, and actual exit0 after Stop. Interactive question/approval semantics
remain deterministic-fixture evidence; this probe does not claim public CLI
or hook acceptance.


The internal application seam `startProcessClaude` supplies `PMX_INTERNAL_*`
activation claims and an owned host socket to Claude. SessionStart registration
requires the hook's kernel parent to be the exact owned child, its process birth
identity, the live host peer/socket, the current generation and the same session
as stream init. Claims alone never register an endpoint. The existing endpoint
helper and dialogue receipts are reused through an injected process resolver;
the default tmux resolver remains unchanged. A process source is discovered
read-only from its registered process ancestry and revalidated by its own host.
No public runtime kind, environment contract or command starts this seam yet.

Process question and permission hooks are observation-only, including when the
host is lost. `claudeProcessControl` projects stream requests into the existing
answer stores, with IDs bound to the activation, connection and request ID.
Only `Handle.Respond` writes decisions. Expiry and disconnect close admission
without synthesizing allow. Interrupt does not record process termination.

`TestClaudeProcess*` in `internal/app` exercises a copied fixture executable in
isolated HOME: endpoint bootstrap, question/approval deduplication, denied/allowed
responses, stale host/generation/PID/birth rejection and bidirectional durable
message receipts with one provider write per message. Endpoint inputs reserve a
MessageRef turn through the exact registered helper's kernel identity before the
native post. Host stdin turns and endpoint reservations share one admission
mutex; busy and duplicate inputs write nothing. Definite prewrite failure releases
the reservation. Uncertain delivery appears as `awaiting-message-handoff` in the
internal snapshot. The internal `Limits.MessageReservation` defaults to 30 seconds
and bounds that handoff wait, including a helper that never reports an outcome.
At expiry the snapshot reports `MessageReservation = "expired"` and the host emits
`message-reservation-expired`. Expiry does not infer completion, replay input,
terminate the child, or admit another turn on that generation. An interrupt ack
alone does not settle the reservation. A late actual result may still settle the
old turn and return the child to idle; otherwise the caller must Stop, observe
actual Wait evidence, and explicitly resume with a new generation before sending
the next turn. Before expiry, a proven handoff or definite prewrite refusal cancels the timer.
Late helper outcomes cannot reopen an expired reservation; only an actual result
can settle it on the same generation.

A Claude turn holds zero or more host inputs and exactly one result. After the
session is bound, a same-session `system/init`, turn frame, `control_request` or
`result` with no admitted turn opens a provider turn (`provider-turn-started`,
ID `provider-<connection>-<n>`) that the next result closes
(`provider-turn-ended`, then `turn-result` and the `TurnCompleted` wake).
Idle `system` notifications do not open a turn. Before binding these frames,
and any frame on another session, remain protocol failures. `UserInput` writes
operator input into a turn the provider has visibly opened and emits
`input-joined`; pending controls (`ErrClaudeControlPending`), a pending
handoff, an interrupt, a turn not yet opened, and more than
`ClaudeJoinedInputs`/`ClaudeJoinedInputBytes` (`ErrClaudeJoinLimit`) refuse with
zero writes. `Turn` keeps its one-turn admission. Inputs joined to a closed turn
carry to the next provider turn, which records them as
`joined-input-unattributed`: the stream does not echo user frames, so their
result attribution is unknown and they are never rewritten. Peer reservations
stay busy during any open turn.
Stop resolves the owned process lifetime; it does not reopen the stopped generation. Existing tmux tests are
unchanged. The processhost fixtures additionally check hook/init agreement.

`PMX_TEST_REAL_CLAUDE_BIN=/absolute/path/to/claude go test ./internal/app -run
'^TestInstalledProcessClaudeBinding$' -count=1 -v` opts into installed Claude
qualification. It uses an isolated HOME, copied test executable, dummy credentials
and a localhost SSE stub, with no real model API. It covers SessionStart binding,
question and permission deny/allow, repeated init, interrupt, native endpoint
receipt round trips with question/approval/result, duplicate rejection and a
subsequent host turn. Real-model behavior and the future public consumer remain unqualified.


## Codex process binding and answer bridge

The app preparation seam registers a non-durable endpoint only after the owned
host witnesses typed readiness and the Registry's exact ownership generation.
Its authority includes the host instance, child and host kernel birth identities,
thread and connection. It does not borrow shared broker epochs. The public tmux
route resolver remains unchanged; a separate process constructor requires both
Registry ownership and live Handle proof.

Process launch marks internal hooks as observation-only, including malformed or
stale bootstrap claims. The host alone projects stream requests into the existing
question and approval stores. IDs derive from binding, connection and the scalar
request ID; identical hook/stream observations produce one question, and stale,
expired or duplicate answers write no provider response. Configured windows and
safe decision selection retain their existing owners. Timeout or disconnect never
automatically allows a request.

The internal endpoint preserves the exact owning-host loopback for messages.
Short-lived foreground clients use the per-user boundary: a 0700 directory,
0600 socket, and kernel peer credentials whose user matches the host's user.
Every request revalidates the exact Agent, Pane, host, generation, connection
and session against both Registry ownership and the live host. Claimed caller
PIDs and source UIDs provide no authentication and no client enrollment is
needed. A foreign user, stale generation, another host or mismatched ownership
writes no provider control. Clients independently verify the owning host's
kernel birth and socket inode; replacement never causes rediscovery or
automatic replay. Existing hook and message peer boundaries remain unchanged.
The public foreground create command consumes these authority checks.

The host lease directory is exclusive. An existing directory returns an error
matching both `ErrBusy` and the underlying existence error. A caller may retry
after the owning lifetime has finished and removed its lease; it must not adopt,
remove or replace the existing directory. This does not promise that reissuing
an app launch returns an already running endpoint. Launch-level idempotency in
`Host.Start` remains separate from socket admission.

Peer coordination uses the existing message store and immutable envelope, with
both source and target revalidated before the host submits a typed turn. A receipt
is delivered after turn/start acceptance; later completion remains a separate
event. Reply correlation and terminal-once receipts reuse the existing store.
Busy and stale routes write no provider turn, and an uncertain delivery is never
automatically resent. The foreground owner consumes this endpoint for typed control and coordination.

App typed fixtures cover binding rejection, duplicate questions/approvals,
response races, timeout/disconnect, unchanged termination evidence, and
bidirectional receipts through real local sockets. The opt-in app test
`PROCESSHOST_TEST_CODEX=1 go test ./internal/app -run
'^TestCodexProcessInstalledBidirectionalReceipts$' -v` qualifies installed Codex
against a localhost model stub with isolated HOME and no real API or credentials.
Native questions and approvals remain fixture-qualified. Late notifications
following turn/completed were not observed in the installed 0.160.0 success and
failure stub probes; no speculative late-frame policy is added.

## Process attention preparation

The application seam stores process attention in the per-user state directory's
`process-attention.json`, keyed by PaneUID with the exact host binding and
generation in each record. Activation requires the previous generation; event
writes and badge clears compare the current binding, and clears also compare the
observed sequence. Older events or acknowledgments cannot clear a newer request.
A persistent-inode writer lock serializes updates; private temporary files, file
and directory sync, and rename make publication atomic. Only request identities,
kinds, sequences and badge metadata are stored, never conversation text.

A malformed JSON store is reported by read-only consumers without mutation. The
next writer preserves its exact bytes in a private, uniquely named
`process-attention.json.damaged-*` sibling before atomically recreating `{}`.
Backup failure leaves the original store intact and returns an error. Backups
are retained for operator inspection; no automatic deletion policy is applied.
An active host may reconstruct only its own record after revalidating the exact
binding and generation while holding the writer lock. Observations without that
authority, stale hosts and stale badge clears cannot recreate records. Missing
history is recovered from the bounded host snapshot/events and the existing
notification queue, not invented provider results.

Process Ready, Input required, Approval required and Process attention error
messages use `notify.process.*` catalog keys with en-US/ko-KR parity tests.
Persisted notification text keeps the queue's canonical English contract.

Claude and Codex stream events share the existing badge priority and aggregation.
Actual host termination closes pending requests while retaining completion and
error notices. Badge clear and notification acknowledgment remain separate, and
the notification queue retains its existing TTL, cap and expired-plus-gone rules.
Mixed inventory failures preserve the tmux error diagnostic and treat unobserved
tmux targets as unknown while still projecting process rows. The seam is dormant
until explicitly injected; public activation remains a later step.

## Explicit recorded-session resume

`SessionRecord` is an internal persistence value containing the provider session,
old ownership binding, connection, turn and pending control identities. It stores
no prompts, question text or decisions and does not write Registry schema.
`Availability` distinguishes a recorded resume candidate (`resumable`) from a
missing identity (`unknown`); it does not promise provider acceptance or imply
that a live host has exited. The consumer must retire the old host and reserve
the new ownership generation before resuming. Actual process termination still
requires Wait evidence.

`ResumeCodex` starts a new dedicated app-server and reuses the typed Client's
`ResumeThreadWithSettings`. It never invokes thread/start or shared discovery.
`ResumeClaude` adds `--resume` for exactly the recorded session and requires an
explicit new prompt and turn ID. Stream-json emits init only after input, so
success waits for bounded init with the same session. If init reports a different
session, the new prompt has already been sent before the child is terminated.
The previous turn is never
resent. Conflicting session arguments, unchanged generations, missing sessions,
provider refusals and returned identity mismatches fail explicitly through
`ErrResumeRefused`; they never authorize a fresh conversation. Retrying the same
launch operation returns the same handle without writing another prompt.

The new snapshot's `ResumeHistory` retains the old binding, an interrupted turn
and expired control identities. These are historical records, never current
pending requests. All control admission still requires the new binding and
connection, even when the provider session is unchanged.

`agent resume <agent> -- <prompt>` explicitly resumes an offline process Agent
under the same Agent and Pane UIDs with a new owned generation. Claude requires
a nonempty first frame because stream-json emits init only after input; Codex can
reattach without a prompt. The typed first frame distinguishes user text from
an existing untrusted peer coordination envelope. Previous turns are never
replayed and expired controls appear only in history. This does not change
message delivery to offline Agents.

The foreground owner persists actual Wait and marks a recorded conversation
resumable in the same Registry transaction. Killing the owner before its Wait
receipt leaves resume state unknown, so resume returns
`process-resume-not-resumable`. A live owner returns `process-resume-owned`;
ambiguous ownership or provider rejection returns `process-resume-refused`.
No automatic restart or relaunch follows an exit. Output and exit match process
creation: the ownership line identifies both UIDs, projected results use the
selected output mode, and the exit status comes from actual provider Wait.

`TestResume*` covers content-free record round trips, interrupted/expired history,
new-generation control, stale response/ack wire zero, idempotent launch and
refusal without replacement conversations. Linux and native Darwin run these
fixtures. `PROCESSHOST_TEST_CLAUDE=1 PROCESSHOST_TEST_CODEX=1 go test
./internal/integrations/processhost -run '^TestInstalledResume' -v` qualifies
installed providers against localhost SSE stubs in isolated HOME with no real
API. Both verify the preceding user turn and assistant reply in the resumed
model request and explicit missing-session refusal without another model call.
Codex also rejects thread/resume before the first durable turn. Native question
and approval controls remain fixture-qualified; public activation is separate.

## Owned process Codex input

Internal consumers of an owned process Codex connection can use
`CodexHandle.DeliverUserTurn(ctx, authority, operation, prompt)` to start an idle
conversation or steer its exact active turn. The successful `UserTurnDelivery`
reports `Mode` (`start` or `steer`), the caller's deduplication `Operation`, and
the provider's `TurnID`. Acceptance does not guarantee model consumption.
An admitted steer never falls back to starting another turn, including on
provider refusal or an uncertain write. Pending questions and approvals retain
their exact response tokens. The delivery replay fence retains the last
`Limits.Events` admitted operations in FIFO order, including refusals; IDs may
be reused after eviction. Existing explicit `Turn` remains idle-only.
