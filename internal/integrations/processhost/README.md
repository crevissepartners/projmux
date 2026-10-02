# Owned process host (internal preparation)

This package is an internal host and Claude stream adapter, exercised through
fixtures. No public command starts it yet. It does not write Registry records,
create tmux resources, attach to existing providers, or change user settings.

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
events, 32 pending requests, 8 KiB of diagnostics, and frames below 1 MiB. Output
overflow records a gap and returns the current snapshot. Protected control and
terminal transitions are retained separately and cannot be evicted by output.
New turns and requests stop at the protected-event admission limit; reserved
space for outstanding request expiry and terminal evidence bounds that list by
`Events + Requests + 6`. Used turn/request IDs are capped by `Events`; operation
history is never evicted to make a retry spawn again. Capacity exhaustion requires
an explicit new lifecycle rather than silent history loss. Stream gaps identify
incomplete observation, not successful completion or an archive.

The deterministic suite covers operation concurrency, preparation and init
failure, binding rollback, multi-turn NDJSON, question/permission response races,
interrupt ack/result ordering, overflow, malformed/oversized frames, blocked
stdin, stdout EOF, stderr pressure, and existing metadata receipt guards.
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
Other unsupported frame types remain protocol failures. Opt-in qualification can
set `PROCESSHOST_TEST_PERMISSION=deny` or `cancel` together with
`PROCESSHOST_TEST_CLAUDE=1`: a process-local Bash ask rule prevents automatic tool
execution; the probe only denies or interrupts, then verifies stale response
rejection and two subsequent turns. User settings remain untouched.
