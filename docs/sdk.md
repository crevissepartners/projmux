# Go SDK deletion preflight

See [Agent deletion protection](registry.md#agent-deletion-protection).

`Client.PreflightDelete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:..."})`
checks the Registry cascade without runtime access or writes, including process
Agents and Project graphs. Call it before stopping a provider for deletion.
Protected Agents produce `*projmux.Refusal` with `CodeAgentProtected`
(`agent-protected`) and `AgentUID` identifying the protected Agent, without
parsing the message. This is the same token used by CLI delete, unregister and prune.
`Delete` and `UnregisterProject` also check previews and locked execution.
Preflight is a snapshot check, not authorization for a later runtime effect;
frontends must still use the deletion executor's locked admission when executing.
Remove the label with `projmux label agent <ref> projmux.io/protected-`
before retrying.

## Shell Window and Pane creation

`Client.CreateWindow(ctx, core.CreateWindowRequest{Project: "uid:..."})` creates
one shell Window. `Client.CreatePane(ctx, core.CreatePaneRequest{Project:
"uid:...", Window: "uid:...", Placement: "right", CwdFrom: "project"})` creates
one shell Pane, using the stored Window anchor unless `Pane` names another one.
Results include the CLI JSON projection and operation receipt.

Set `Options.ProjmuxExecutable` to an absolute executable from the same projmux
build as the SDK for supervised shell children. No PATH search or implicit
executable fallback is performed. Request `SocketPath` overrides the Option;
explicit sockets require a live server with exact PID, resource UID and root
proof. The empty route uses the app server and can start it. SDK calls can record `create.outcome` measurements through the optional
`Options.CreateRecorder` described below.


## tmux Agent creation

`Client.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:...", Window:
"uid:...", Provider: "claude", Name: "worker"})` creates one tmux Agent through
the same transaction, provider launch plan, hooks and rollback as the CLI.
It supports Claude, Antigravity, native prompted Codex, payload-free Codex and
`InteractiveOnly` Codex.
`Pane` selects an optional anchor; `Creator` accepts only an explicit `uid:`
Agent declaration and records `creator-basis=explicit` without inferring a caller.
Profiles, instructions, model, effort, workspace roots and placement use the
CLI's existing validation. Set `Options.ProjmuxExecutable` from the same build.

Prompted Codex uses the existing official app-server endpoint and the CLI's native
thread/start and initial turn path. Instructions and profile settings are applied
when the thread starts. The captured provider PATH, CODEX_HOME and HomeDir reach
read-only readiness probes, proxy connections, the tmux attachment and the
observer child. Config, State and Registry routing is explicit throughout;
provider state is a separate domain. This does not start or restart a daemon.
An unavailable endpoint refuses with `CodeCreateNativeCodexUnavailable` before
resources are committed. An indeterminate provider RPC is never retried or
silently switched to another launch lane. SDK Codex resume remains deferred.

An activation timeout or failure after commit preserves the Agent and Pane.
`CreateAgent` returns their projection, receipt and PaneID **together with**
`CodeCreateActivationUnconfirmed`; inspect the returned Agent before retrying.
Context cancellation prevents admission only. The tmux server owns an admitted
provider's lifetime, and cancelling the caller does not stop it.

`Client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:..."})` rebinds an
Offline or Failed Claude or Antigravity tmux Agent to a new Pane, preserving its
Agent UID and provider conversation. Project and Window optionally scope Ref;
Model, Effort, DialogueReplyOnly and SocketPath use the CLI's resume rules.
The result includes Agent, Projection, Receipt, PaneID and notices. It returns
immediately after commit, without waiting for activation acknowledgement,
like CLI `agent resume`; transaction failures roll back. Cancellation prevents
admission only. Process targets, including retired process candidates, return
`CodeResumeProcessUnsupported`. Codex resume returns
`CodeResumeNativeCodexUnsupported` before effects because its native observation
is not yet available in the SDK; use `projmux agent resume`. Prompt input is not
exposed. The same `Options.ProjmuxExecutable` contract applies. The tmux CLI
rejects `-o`; the SDK receipt uses its shared operation receipt model.


## Optional create outcome recording

Set `Options.CreateRecorder` to a `func(projmux.CreateOutcome) error` to receive
one measurement per Window, Pane, Agent or Resume transaction. All types are
public; an embedding application needs no `internal` imports. Nil disables
recording. The SDK neither chooses a journal path nor reads environment variables
for recording. The caller owns the sink, its run ID and journal formatting.

```go
client, err := projmux.NewClient(projmux.Options{
    ConfigDir: "/absolute/config/projmux",
    StateDir: "/absolute/state/projmux",
    ProjmuxExecutable: "/absolute/bin/projmux",
    CreateRecorder: func(outcome projmux.CreateOutcome) error {
        // Append to your explicitly configured sink here.
        // Kind: window/pane/agent/resume; Result: success/error.
        return recordCreateOutcome(outcome)
    },
})
```

The callback runs synchronously after the Registry lock is released and all
transaction rollback and lease cleanup finish. Input validation and cancellation
before transaction admission produce no callback. A route refusal after admission
produces an error outcome with no lock timing. Each admitted transaction records
separately, including multiple calls on one Client. A successful commit records
success even if later projection or Agent activation acknowledgement fails;
inspect the method's result and error for that later failure.

`Duration` includes transaction execution and cleanup, excluding recording.
`LockHeld` starts at mutation entry and ends when Store update returns, excluding
lock wait. `Phases` tiles that hold with guard, first reconcile, operation, second
reconcile, reproof and store write/unlock; nil marks a phase never entered.
`SpawnToRelease`, for Agent/Resume only, measures the first supervised child spawn
through Store update return. These are the CLI's raw measurements, before journal
rounding/clamping. To preserve its event shape, write `create.outcome`, component
`create`, operation `Kind`, result `Result`, and duration/lock/phase/spawn fields in
milliseconds. Omit nil fields; the CLI clamps lock to duration and spawn to lock,
and drops phase fields if their rounded sum exceeds lock.

All duration pointers belong to the callback and can be retained or changed.
Errors and panics from the callback are ignored; they cannot alter the creation
result. Keep the callback short: its execution delays the method's return.
Concurrent Client methods may invoke it concurrently, so synchronize shared sinks.
The SDK starts no recording goroutine and does not retry failed records.

## Central default host setting

The central default host preference is available through
`GetConfig(ctx, core.ConfigAgentHost)` and
`SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: &host})`.
`ConfigValue.DefaultHost` is `tmux` (default) or `process`; a nil update pointer
preserves the saved value. Invalid input returns `CodeConfigValueInvalid`
and cancellation before admission writes nothing. This API uses the client's
explicit ConfigDir and the same `agent-default-host` file as the CLI and Settings.
It stores the preference only; SDK creation keeps its existing tmux contract.

## Process Agent stop

`Client.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:..."})` stops one process
Agent through the same executor as CLI `agent stop`. `Project` and `Window`
optionally scope the explicit reference. Set `Binding` from the observed Pane
when the caller needs to require that exact generation; a replacement is refused
before effects. Tmux Agents and ambiguous process Pane ownership are unsupported.

The call returns only after the exact generation's actual termination evidence
and owner retirement are observed. Agent, Pane and provider conversation remain
available for public CLI resume. An already retired generation with matching
Wait evidence succeeds without another control request. A child-free detached
claim is released under the same guard as provider birth, without fabricating
termination evidence; `ClaimReleased` distinguishes this result.

Use explicit `Options.ConfigDir` and `Options.StateDir` for isolated callers.
Stop uses the owned host's exact socket and kernel identity directly; it neither
starts an executable nor invokes the CLI, and needs no `ProjmuxExecutable`.
Cancellation prevents admission. After admission, retirement finishes separately
from caller cancellation, bounded by the earlier caller deadline or ten seconds.

`StopAgentResult` includes `AgentUID`, `PaneUID`, `Binding`, and any observed
`Termination` on failure as well as success. `Stopped=false` does not imply the
provider survived. Typed `Refusal.Code` distinguishes stale/busy targets,
unsupported process targets, unconfirmed Wait or claim release, and execution
failure. Inspect the returned exact refs and generation before using public
stop/resume/delete recovery; do not automatically replay an uncertain Stop.

Retired generations must retain private evidence of the exact owner and child
birth identities as well as their actual Wait receipt. The Wait writer preserves
that evidence before clearing Registry activation; an admitted Stop can also
preserve its original exact activation when stopping an older running owner.
CLI and SDK now refuse an already retired legacy generation without this evidence
with `agent-stop-wait-unconfirmed`, rather than treating Offline plus Wait as
proof of owner retirement. This refusal sends no control or signal and leaves
Registry resources unchanged. Inspect the exact Agent/generation before using
public resume or delete recovery; uncertain retirement is not automatically
retried. Claim lock admission honors caller cancellation and deadlines, including
when another birth or claim release holds the guard.
