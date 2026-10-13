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
It supports Claude, Antigravity, payload-free Codex and `InteractiveOnly` Codex.
`Pane` selects an optional anchor; `Creator` accepts only an explicit `uid:`
Agent declaration and records `creator-basis=explicit` without inferring a caller.
Profiles, instructions, model, effort, workspace roots and placement use the
CLI's existing validation. Set `Options.ProjmuxExecutable` from the same build.

Native-observed Codex creation (prompted default Codex and Codex instructions)
is not yet available in the SDK: `CodeCreateNativeCodexUnsupported` refuses it
before effects. Use `projmux create agent` for these combinations. The follow-up
is **SDK prompted Codex create (including explicit observer child state routing)**
in the conversation milestone (613-B).

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
