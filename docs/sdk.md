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
silently switched to another launch lane. SDK Codex resume uses the same native endpoint.

An activation timeout or failure after commit preserves the Agent and Pane.
`CreateAgent` returns their projection, receipt and PaneID **together with**
`CodeCreateActivationUnconfirmed`; inspect the returned Agent before retrying.
Context cancellation prevents admission only. The tmux server owns an admitted
provider's lifetime, and cancelling the caller does not stop it.

`Client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:..."})` rebinds an
Offline or Failed Claude, Antigravity or native Codex tmux Agent to a new Pane, preserving its
Agent UID and provider conversation. Project and Window optionally scope Ref;
Model, Effort, DialogueReplyOnly and SocketPath use the CLI's resume rules.
The result includes Agent, Projection, Receipt, PaneID and notices. It returns
immediately after commit, without waiting for activation acknowledgement,
like CLI `agent resume`; transaction failures roll back. Cancellation prevents
admission only. Process targets, including retired process candidates, return
`CodeResumeProcessUnsupported`. Native Codex resumes the exact stored thread with current profile policy, model
and effort settings on its stored state domain or the current default endpoint
of that same domain. Missing native lifecycle, a different provider state domain
or a still-listening retired endpoint refuses with `CodeResumeNativeCodexUnavailable`.
No new thread or turn is submitted, and an uncertain resume RPC is never retried.
`CodeResumeNativeCodexUnsupported` remains exported for source compatibility.
The captured HOME, CODEX_HOME and PATH also reach resume proxies, attachment and
observer recovery; Config, State, Registry, executable and tmux socket stay explicit. Prompt input is not
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

## Delete a headless Agent

`Client.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:...",
DryRun: true})` previews one exact process Agent without stopping its provider
or claimant. Set `Confirm: true` and clear `DryRun` to execute the reviewed
request. Project and Window can scope the exact UID. Process Agent deletion
requires no tmux socket; an explicitly supplied SocketPath must still be
absolute. Direct process Pane and Window deletion remains unsupported.

CLI and SDK use the same delete executor and protected `delete-stop` owner
admission. Protection and the exact cascade are rechecked before Stop or claim
release. A born provider must record its actual Wait for the same binding, and
both its owner and child must retire in the kernel before deletion. A child
that survives its owner refuses deletion. A child-free detached claim is
released under its birth guard, without a signal or a fabricated Wait, before
its Agent and managed Pane are removed. Existing empty virtual Window cleanup
is preserved. Cancellation prevents admission; admitted retirement and deletion
finish independently of later caller cancellation.

Inactive offline/unknown Registry cleanup keeps existing CLI parity when no
owner/child witness or claim is available. This differs from `StopAgent`, which
refuses an unwitnessed retired generation. Present private retirement evidence
must confirm exact owner/child retirement. Previously, CLI claim deletion could remove Registry resources and return while
the claimant remained alive. The shared executor now durably requests claim
release and waits for the claimant to retire before removing those resources.
The existing CLI runtime/evidence projection, text and receipt remain unchanged
for claim cleanup; no provider Wait is invented.

The SDK uses its frozen explicit Config/State/Registry roots and never invokes a
CLI subprocess. Failures preserve their cause and the reviewed exact resources
in `DeleteResult.Plan`; `Committed: false` can follow an admitted Stop or claim
release. Inspect those resources and their termination evidence before using
exact public stop/resume/delete recovery commands. Do not automatically replay
an uncertain deletion. Successful results carry the deletion receipt,
OperationID and any best-effort deletion-record warning in Notice.

## Exact Agent turn control

`StartTurn`, `SteerTurn`, `DeliverTurn` and `InterruptTurn` accept
`core.TurnRequest{Ref: "uid:..."}` and return `core.TurnResult`.
Project and Window optionally scope the explicit reference. Text is required for
input; Generation and process Binding optionally pin an observed activation.
Use the client's explicit ConfigDir, StateDir, HomeDir and SocketPath. No active
Pane or ambient operator identity is inferred.

Native Codex supports all four actions. Existing process Codex owners support
start, steer, deliver and interrupt; Claude process owners support start and
interrupt. Claude tmux supports interrupt with explicit caller-reported `Via`,
validated using the CLI's client-name rule. The durable audit precedes one Esc;
`Delivery="sent"` means tmux accepted the key, with cancellation unconfirmed.
Claude tmux input and Claude steer/deliver remain unsupported. SDK input refuses
child-free deferred claims with `CodeTurnDeferredUnsupported`; use the existing
CLI deferred start workflow separately.

For a promptless Claude created through the process CLI, `StartTurn` accepts the
first input even when its verified `SessionStart` precedes the call. Stream
readiness still requires matching `system/init`; the hook alone is insufficient.
`DeliverTurn` retains its existing Claude unsupported refusal with no input
write. Remote process control has an existing 64 KiB JSON envelope limit,
including escaped text and routing fields. This fix does not expand that limit;
larger first frames can use the CLI's initial prompt creation path, subject to
the existing provider frame and write budgets. SDK Agent creation remains tmux-only.


Deliver asks the native epoch or process owner to select idle start or busy
steer once. Only an explicit native `invalid-operation` refusal permits the
legacy start, then `turn-in-progress`, then steer path. Transport loss, timeout,
stale bindings and uncertain receipts never replay input. Steer preserves
`Acceptance="provider"` and `Delivery="unconfirmed"`.

Cancellation reaches admission, native authority fencing, Registry rechecks and
bounded transport. Inspect the result even when an error is returned: exact
refs, turn IDs and observed acceptance survive a later recording failure.
`OutcomeUnknown` identifies an indeterminate transport or malformed acceptance;
`Accepted` does not mean provider completion. Control replies may collapse lost
steer receipts into `stale-turn` or an incomplete process refusal. These ambiguous
responses conservatively report `OutcomeUnknown`; explicit pre-write binding
and socket refusals remain known. For explicit process start or
interrupt, `Operation` and the legacy CLI-compatible `TurnID` identify the host
request. Deliver/steer typed receipts instead identify the provider turn; a
public session read observes that provider turn separately. Inspect current identity before
recovery rather than automatically resubmitting. `*projmux.Refusal` exposes
provider/control/transport reason codes; context errors remain ordinary errors.
These methods invoke shared execution directly, without a CLI subprocess,
provider creation, Stop, resume or daemon changes.

## Agent questions

`Client.ListQuestions(ctx, "uid:agent-…")` reads the CLI's redacted question
projection. Use its opaque question ID and `CanAnswer`, including the captured
deadline and exact process generation. A completed original turn does not end a
still-valid async question. Older hosts and dispatchers remain read-only.

Submit every prompt explicitly in one `AnswerQuestion` call. Question and option
indexes are 1-based; use exactly one Option, Index, or Text per selection.

```go
text := "An explicit answer"
result, err := client.AnswerQuestion(ctx, projmux.AnswerQuestionRequest{
    Agent: "uid:agent-…", QuestionID: question.ID,
    Selections: []core.QuestionSelection{{Question: 1, Text: &text}},
})
// Inspect result even when err != nil: unknown delivery preserves the ID.
if result.Delivery == "async" && result.Outcome == "unknown" {
    // Lock the form. Do not resend this submission.
    return err
}
if err != nil { return err }
```

An async success returns `Delivery: "async"`, `Outcome: "submitted"` after ordinary
operator start/steer acceptance and durable app receipt settlement. This does not
prove model consumption or a completed answer, and adds no native per-question
RPC or acknowledgement. Readback uses `closed` / `async-submitted` /
`CanAnswer: false`. `CodeQuestionDeliveryUnknown` preserves the same ID and
`Outcome: "unknown"` with the error; its retained claim uses `async-unknown`. Never retry an
unknown submission. A repeated attempt returns `CodeQuestionNotPending` without
asserting that an earlier attempt had no effect.

The call receipt and a later exact, valid read are separate observations. A
submitted-row rename followed by a directory fsync error can leave an unknown
call receipt while a later read shows `async-submitted`. Preserve the original
receipt and error: the visible row is additional current storage evidence, not
retroactive call success, proof of crash durability, or model receipt/consumption.
An `async-unknown` row, read error, or absence does not resolve uncertainty. Keep
`CanAnswer: false` and never resend. When storage fails, a call's proven absence
of native input (`not-submitted`) and its visible stored disposition must also
be kept distinct; a retained unknown claim does not prove native input occurred.

`MaxAsyncQuestionTextBytes` is 4096 UTF-8 bytes per original, untrimmed direct
text. `MaxAsyncQuestionSelectionsBytes` is 16384 bytes for the normalized zero-based
`map[int]Selection` encoded by Go's `encoding/json`, including JSON escapes and
all fields. Private IPC retains its 65536-byte frame bound and native frames keep
their existing limits. Overflow returns `CodeQuestionAnswerTooLarge`; there is no
truncation or splitting. A set containing any secret prompt requires the native
surface (`CodeQuestionSecretNativeOnly`).

`CodeQuestionNotFound`, `CodeQuestionExpired`, `CodeQuestionStale`,
`CodeQuestionClosed`, `CodeQuestionBusy`, and `CodeQuestionHostUnsupported`
distinguish admission refusals. After validation the host locks the form before
claiming durable submission. A storage or proven pre-write admission failure
keeps it locked without native input; when storage is writable, readback uses
`closed` / `async-not-submitted`. Preserve storage errors and use the native
surface. Terminal history can disappear through normal pruning or generation
replacement without reopening the submission. `UpdatedAt` is a store state-change
time; no provider-acceptance or model-consumption timestamp is added.

Existing `Options.QuestionDispatcher` implementations retain their interface.
An implementation can additionally implement `QuestionReceiptDispatcher` to
return async answer receipts; implementations without it cannot submit async
answers. Blocking Codex, Claude, and legacy question contracts remain unchanged.
See [Headless Codex async questions](codex-async-questions.md).

## Stop a Project including headless Agents

`Client.StopProject(ctx, core.StopProjectRequest{Ref: "uid:..."})` stops the
Project's Running process Agents and its exact persistent tmux session through
the same batch executor as CLI `stop project`. Explicit ConfigDir, StateDir,
Registry and socket routing remain frozen on the Client. A process-only Project
can stop its born Agents when tmux is absent; no CLI subprocess or provider
executable is started. Project, desired topology, Agent, Pane and conversation
identities remain available for public CLI resume.

Process Stops dispatch concurrently with one shared deadline, bounded by the
earlier caller deadline or ten seconds. Cancellation prevents admission;
admitted Stop and tmux compensation finish independently of later cancellation.
A child-free claim keeps the CLI Project-stop refusal policy: Running is retained
and no termination evidence is fabricated. This differs from `StopAgent`'s
individual claim release. Inactive Projects without a live session or Running
process Agent retain `CodeProjectNotLive`.

Born success requires the exact generation's actual Wait and both owner and child
kernel retirement. Previously CLI Project stop could confirm success after an
unreadable owner identity, or while its child still lived. Such uncertainty now
refuses success; the existing CLI failure token, timeout text and recovery
instructions remain stable. Typed causes remain available through error unwrap.

`StopProjectResult.Receipt` includes confirmed Agent stops even when another
owner or tmux fails. A failed tmux stop leaves the Project receipt action unchanged;
a successful tmux stop remains reported even when a process owner refuses.
`Stopped=false` does not mean that every runtime survived. `Interruptions` and
`OperationID` retain the existing tmux evidence/compensation summary. Inspect the
exact affected resources and failed Agent names/UIDs before public
stop/resume/delete recovery; do not automatically replay an uncertain Stop.
Failures use `CodeProjectStopExecutionFailed` with the original causes.
`CodeProjectHasRunningProcessAgents` remains exported for source compatibility;
Running process Agents no longer cause a batch preflight refusal.

### Recorded process resume recipe refusals

CLI and private process resume consumers can use `errors.As` to read
`*projmux.ResumeRecipeChangedError` (also `*core.ResumeRecipeChangedError`)
through usage and host-lost wrappers. This additive error does not enable process
`Client.ResumeAgent`: its `resume-process-unsupported` refusal is unchanged.

The JSON fields are `Code` (`process-resume-refused`), `Reason`
(`relaunch-no-conversation`), `agentUID`, `changes`, `relaunchCommand`, and
`guidance`. Each change has `field`, `reason`, `recorded`, and `current`.
Values are string arrays: scalars have one element, including `""` for an empty
value; writable roots retain their order and use `[]` for no roots. Unknown
reasons have field `unknown` and values `["<unknown>"]`.

Fields appear in stable order: `profile`, `profileDigest`, `instructions`,
`instructionsDigest`, `model`, `effort`, `guidanceDigest`,
`projectGuidanceDigest`, `linkRulesDigest`, unknown reasons sorted by token,
`workspace.cwd`, and `workspace.additionalWritableRoots`. Only differences
reported by the existing resume guard are included. Profile changes include
name and digest; content changes expose digests, never instructions/profile
bodies, environment, credentials, or link URLs. Existing reason tokens and CLI
exit behavior remain unchanged; the human refusal adds these differences and
recovery guidance.

`relaunchCommand` names the exact Agent UID:
`projmux agent relaunch uid:<agent> --detach`; Claude adds `-- <prompt>`
(replace the placeholder with your next message). Explicit process relaunch
uses current settings and preserves the Agent UID, Pane UID, and recorded
conversation. `relaunch-no-conversation` still identifies the resume refusal;
it does not report conversation loss. Present the structured guidance directly
instead of parsing the error string. Relaunch remains an explicit user action.

## Provider account references

`Client.RegisterAccount(ctx, core.RegisterAccountRequest)` registers a
non-sensitive reference to an existing absolute provider directory.
`Client.ListAccounts(ctx)` returns active references; `Client.RemoveAccount(ctx,
core.RemoveAccountRequest)` retires an exact active name or `uid:account-…`.
All three use the Client's captured ConfigDir and the same catalog service as
`config accounts`. Authentication is always `unverified`; no credentials or
provider APIs are read and no provider is started.

```go
account, err := client.RegisterAccount(ctx, core.RegisterAccountRequest{
    Name: "work", Provider: "codex", Root: "/absolute/existing/root",
})
if err != nil { return err }
_, err = client.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: "uid:" + account.UID})
```

`core.ProviderAccount` contains UID, Name, Provider, Root, Version, State, and
AuthenticationStatus only. Active names are globally unique; symlink aliases of
the same provider/root are duplicates. Removal keeps a tombstone and repeated
removal by UID returns `removed`. Re-registering the name creates a fresh UID.
Returned slices and values belong to the caller. `*projmux.Refusal` exposes
`CodeAccount*` reasons; `CodeAccountWriteUnknown` must not be automatically
retried. Cancellation retains `errors.Is` context identity. See
[provider account references](accounts.md) for storage and failure semantics.

### Detached process Agent creation

`CreateAgentRequest{Host: "process", Detach: true}` starts one Claude or Codex
Agent in the explicitly named Project and Window. Omitted `Host` continues to
mean tmux; the SDK does not infer the configured default host. Foreground process
ownership, tmux split placement, Pane anchors, and alternative launch lanes are
unsupported for this request.

Set `Options.ProjmuxExecutable` to the absolute executable of the same build.
The client freezes its explicit Home, Config and State directories and provider
environment at construction. Arbitrary absolute Config/State directories are
supported; they need not end in `projmux`. Creation uses the shared internal owned
launcher and startup identity/binding commit protocol. It does not invoke the
public `create agent` command or change the caller's environment.

The returned `CreateAgentResult.Binding` identifies the exact Agent, Pane and
owner generation; `PaneID` is empty because there is no tmux Pane. A successful
return transfers lifetime to the independent owner. Canceling the request or
exiting the SDK caller afterward does not stop the provider. Startup cancellation
and failure settle through the owned rollback path. Failures retain typed codes
and `CreateProcessError` causes with exact owner and binding references; a nonzero Binding identifies exact recovery resources when cleanup
cannot be confirmed. Inspect that generation before using `StopAgent` or `Delete`;
an uncertain startup must not be automatically retried.

### Relaunch a tmux Agent

`Client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: "uid:..."})` uses the
CLI's shared restart plan and stop/rebind sequence for an exact Claude or Codex
Agent. Project and Window optionally scope the explicit reference; no current
Pane or caller Agent is inferred. Process Agents and host moves are unsupported
and refused before Stop. Configure explicit Home, Config, State and socket paths,
and an absolute `Options.ProjmuxExecutable` from the same build.

`Profile` and `Instructions` are pointers: nil leaves the selection unchanged;
a pointer to `""` selects none. `Reset` contains `instructions`, `model` or
`effort` and cannot reset an item also explicitly set by the request. Profile,
instructions, model, effort and reset retain the CLI's layered settings rules.
A Running Agent with unchanged settings is left alone; an explicit Model always
restarts it. Offline and Failed Agents resume without Stop.

`DryRun` returns the plan without writes. A busy Running Agent requires
`Confirm`; this can interrupt its current turn. Cancellation prevents admission.
After Stop is admitted, rebind or failure recovery finishes independently of
later caller cancellation. The Agent UID and provider conversation are preserved;
a committed rebind creates a new Pane/generation. Success acknowledges the
commit, not provider activation, a model response or terminal rendering.

Inspect the result even when the call fails: `Stop` and `Resume` retain their
receipts, `Outcome` distinguishes a failed Stop from failed resume, and `Recovery`
names the exact target. A failed Stop is observed as alive, closed or unknown;
closed can continue with a notice, while alive and unknown refuse success.
If the fresh Registry phase then refuses resume, `Recovery` is empty.
The error and notice identify the Agent and selected socket; when Stop was
attempted, they also identify the old Pane.
Inspect Registry and runtime state before further action; no immediate recovery
command is available. Other resumable failures retain their recovery guidance.
Typed `*projmux.Refusal` codes and wrapped causes distinguish those results.
Native Codex uses its existing same-thread settings-update/verification protocol;
it never creates a new thread or turn as fallback. Do not automatically replay
an uncertain operation or a recovery command.
