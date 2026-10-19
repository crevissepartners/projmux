# Go SDK

## Config and state paths

`ResolvePaths(ResolvePathsOptions{})` returns `Paths{ConfigDir, StateDir}`
using the CLI's HOME/XDG resolver, without constructing a Client. Both paths
include the `projmux` suffix. It reads no files (including locale, profiles,
guidance or Registry), creates no directories, changes no environment variables,
and contacts no tmux server or provider. Existence and write access are not
checked. Effects inside caller-supplied callbacks belong to the caller.

```go
paths, err := projmux.ResolvePaths(projmux.ResolvePathsOptions{})
if errors.Is(err, projmux.ErrHomeDirRequired) {
    // Ask the operator to provide HOME or the missing absolute XDG home.
    return err
}
if err != nil { return err }
settingsPath := filepath.Join(paths.ConfigDir, "web.toml")
```

`HomeDir func() (string, error)` and `LookupEnv func(string) string` callbacks
allow explicit inputs. Each nil callback independently uses `os.UserHomeDir`
or `os.Getenv`; a callback returning an empty string does not request a fallback.
`ResolvePathsOptions.HomeDir` is unrelated to `Options.HomeDir`, which controls
session naming only.

Resolution calls HomeDir first. If it fails, the error is wrapped as
`resolve home directory: %w` and no environment callback runs. Otherwise it
collects `XDG_CONFIG_HOME`, then `XDG_STATE_HOME`, before resolving config and
then state. Absolute XDG values are whitespace-trimmed and cleaned. Blank,
relative and literal tilde XDG values are treated as unset, using
`HOME/.config/projmux` or `HOME/.local/state/projmux`. Relative HOME values and
spaces within HOME retain the CLI's existing path semantics.

When HomeDir successfully returns an empty or blank home with no error, two
absolute XDG homes still succeed. With the default nil HomeDir callback, an
unset or empty HOME causes `os.UserHomeDir` to fail before XDG inputs are read,
even when both XDG homes are absolute. That OS error is wrapped as
`resolve home directory: $HOME is not defined` and does not match
`ErrHomeDirRequired`. After HomeDir succeeds, a missing fallback
returns the existing error text `HOME or an absolute XDG_CONFIG_HOME is required`
or `HOME or an absolute XDG_STATE_HOME is required`, matching the public
`ErrHomeDirRequired` sentinel through `errors.Is`. Config errors take precedence,
but both environment callbacks have already run. Every error returns zero Paths;
wrapped callback errors retain their original cause.

## Host-global Agent guidance

`ReadAgentGuidance(ctx)` returns `core.AgentGuidanceFile`: user-file presence,
text, byte count, digest, revision, limit, path, and built-in `DefaultText` /
`DefaultDigest`. It performs no migration or writes.
`ValidateAgentGuidance(ctx, content)` returns `core.AgentGuidanceValidation`,
including both effective assembly sizes and the revision of current inputs,
without creating directories, locks, receipts, policy, or snapshots.

Pass that revision to `WriteAgentGuidance(ctx, content, revision)` or
`ResetAgentGuidance(ctx, revision)`. Writes recheck current inputs and candidate
validation under the shared policy lock. A changed user file, policy, or active
fragment input produces `CodeAgentGuidanceStale`; read/validate again.
`CodeAgentGuidanceInvalid`, `CodeAgentGuidanceTooLarge`, and
`CodeAgentGuidanceUnreadable` classify other editing failures. Refusals retain
the underlying error through `errors.Is` / `errors.As`; context cancellation
is returned as a context error. External editors ignoring the lock are outside
the concurrency guarantee. Reset retains the existing multi-file limitation:
user removal can succeed before policy restoration fails.

`InspectAgentGuidance(ctx, process)` and
`SetAgentGuidanceEnabled(ctx, fragment, enabled)` correspond to existing CLI
show/enable/disable behavior. Inspection includes first-use migration. Its
Go-only `Source` metadata reports the existing assembly classification
(`default`, `file`, or `off`) and is omitted from JSON. An empty
fragment selects the whole switch; a basename selects its fragment mask.
See [guidance editing](configuration.md#editing-host-global-agent-guidance)
for JSON fields, the UTF-8/NUL and 64 KiB limits, and policy preservation.

## Host resource support

`projmux.ResourcesSupported() bool` reports whether the current build supports
statusbar host resources: Linux (including WSL) and macOS with cgo return `true`;
macOS without cgo returns `false`. No Client, context, or configuration path is
needed, and there is no error or refusal result.

```go
supported := projmux.ResourcesSupported()
```

This call does not sample metrics, update the CPU cache, read or write files,
or contact a runtime, CLI, or provider. It does not read or change the saved or
effective resources setting; `GetConfig(ConfigStatusbarResources)` retains its
existing central configuration meaning.

## Host system samples

`Client.SampleSystem(ctx)` returns `core.SystemSample` with `Supported` and
nullable integer `CPUPercent`/`MemoryPercent` (0–100). It uses the existing
[statusbar host sampler](configuration.md#live-system-resources): Linux/WSL
procfs, or macOS Mach statistics and `hw.memsize` when built with cgo. macOS
without cgo is unsupported. WSL reports the Linux guest/VM view. These values
describe the host, without attribution to a Project, Window, Pane or Agent.
No metrics process or provider API is called.

```go
client, err := projmux.NewClient(projmux.Options{StateDir: "/absolute/projmux-state"})
if err != nil { return err }
sample, err := client.SampleSystem(ctx)
if err != nil { return err }
if sample.Supported && sample.MemoryPercent != nil {
    fmt.Println(*sample.MemoryPercent)
}
```

Sampling **updates the CPU delta cache**, so this is not an effect-free read.
The only write destination is `live-resources-sample.json` under the explicit
`Options.StateDir`, or the HOME/XDG state directory captured by `NewClient`.
Clients sharing that directory also share the statusbar's CPU reference; a
sample can change the next caller's delta interval. References older than 30
seconds are replaced without a CPU percentage. The first sample, invalid or
reset counters, or unavailable inputs can also yield nil. Memory availability
is independent of CPU availability. `Supported=false` means the platform has
no sampler, with both percentages nil; `Supported=true` does not guarantee
either percentage is available. Cache failures retain the sampler's best
effort behavior and are not returned as errors.

An already-canceled context prevents sampling and cache writes. Cancellation
after the synchronous sampler starts does not undo its effects. Each returned
pointer belongs to the caller. Sampling neither reads nor changes statusbar
enablement and does not collect usage history; explicit SDK sampling works
independently of the Resources display setting. Existing CLI/statusbar behavior
is unchanged.

## Deletion preflight

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

`Client.Delete` returns the CLI's process Agent classification in
`DeleteResult.Processes`: AgentUID, WindowUID, RootKind, RootUID, Runtime,
Evidence and HostPID. Dry runs and confirmation refusals include the reviewed
plan; a committed deletion includes the final classification, including
`stopped` after an owner Stop and confirmed Wait. Execution errors retain the
preflight evidence, which does not prove that the runtime is still unchanged.
Runtime is the CLI's live classification, separate from Registry Agent phase;
HostPID is zero when no live owner PID is reported. The returned slice belongs
to the caller. Owner credentials and private admission evidence are excluded.
`PreflightDelete` continues to check Registry protection without runtime access.

Instruction/profile metadata, process input UI descriptors, question session
metadata and running Agent aggregates that the corresponding CLI output does
not expose are outside these result projections. Existing `ListProfiles.Valid`
and capabilities `Agent.Phase` remain available on those separate reads.

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


Invalid `Options.ProjmuxExecutable` values in `ResumeAgent` and `RelaunchAgent`
now return `CodeExecutableInvalid` (`executable-invalid`) before effects, replacing
their previous `CodeCreateExecutableInvalid` result. Consumers matching those
errors should update their code checks. `CreateAgent`, `CreateWindow`, and
`CreatePane` retain `CodeCreateExecutableInvalid` (`create-executable-invalid`);
the existing constant remains exported. Tmux relaunch dry runs and unchanged
plans retain their behavior without requiring an executable. Process planning
requires the same-build executable for its internal owned executor.


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

## Central resources, mode and visibility

`GetConfig` and `SetConfig` also accept these additive keys:

| Key | Read field | Update field | Default |
| --- | --- | --- | --- |
| `core.ConfigStatusbarResources` (`statusbar.resources`) | `Resources *core.ConfigToggleState` | `Resources *string` (`on`/`off`) | `off` |
| `core.ConfigAIDefaultMode` (`ai.defaultMode`) | `DefaultMode string` | `DefaultMode *string` | `selective` |
| `core.ConfigStatusbarVisibility` (`statusbar.visibility`) | `Visibility []core.ConfigVisibilityState` | `Visibility map[string]string` patch | `on`, except Codex 5h `off` |

Modes are `claude`, `codex`, `antigravity`, `selective`, `resume`, and `shell`
(case sensitive). Nil pointers and an empty visibility patch preserve storage.
Toggle states contain `Effective`, `Saved`, `Source`, and `Invalid`; visibility
states also contain `Key`, sorted ascending. Resources source is `default` or
`saved`; visibility source is `default` or `central`. Existing loader semantics
apply: invalid resource text appears in `Invalid`, while invalid central
visibility values are omitted by the loader and read as built-in defaults.
The three new read fields use `omitempty`, preserving existing five-key JSON.

Central visibility keys are `notifications-hud`, `agent-usage-hud`, `project`,
`working-directory`, `git`, `clock`, `agent-usage-provider-claude`,
`agent-usage-provider-codex`, `agent-usage-window-claude-5h`,
`agent-usage-window-claude-weekly`, `agent-usage-window-codex-5h`, and
`agent-usage-window-codex-weekly`. `settings-launcher` is TUI only and refused.
A patch validates every leaf and value before one atomic replacement of
`statusbar-defaults.json`. It preserves unspecified leaves, invalid untouched
values, and unknown stored fields. Concurrent writers are not serialized;
there is no compare-and-swap guarantee.

These methods use only the client's explicit central ConfigDir. Invalid
keys/leaves return `CodeConfigKeyInvalid`; invalid values return
`CodeConfigValueInvalid`. Invalid input or cancellation before write admission
preserves bytes, mode and mtime in explicit ConfigDir/StateDir. Admitted atomic
writes finish without interrupting the replacement. They do not read or write
TUI leaves or `web.toml`, reload tmux, start providers, or change the Registry.
Valid TUI/web overrides continue to take priority over central visibility.

The CLI routes are `config resources [--set <on|off>]`,
`config ai-default-mode [--set <mode>]`, and
`config statusbar-visibility [--key <leaf> --set <on|off>]`. Each read or write
returns its key's JSON projection (`key` plus `resources`, `defaultMode`, or
`visibility`); state field names are lowercase and arrays are never null.
The CLI preserves its existing bootstrap migration and error journal policy;
it does not promise that the entire HOME remains unwritten. Invalid CLI inputs
exit 2. Existing five-key CLI output is unchanged.

## Legacy AI split mode

`Client.GetAISplitMode() core.AISplitModeState` reads the first valid TUI
`tmux-ai-split-mode`, then central `ai-new-window-mode`, then `selective`.
`Source` is `tui`, `central`, or `default`. Missing, empty, invalid, unreadable
files and path resolution failures retain that fallback behavior. Paths remain
the client's captured ConfigDir; reads retain TUI front-read tracking.

`Client.SetAISplitMode(mode string) (core.AISplitModeState, error)` trims
and validates the existing case-sensitive modes before I/O. Invalid input returns
`CodeConfigValueInvalid`. It writes the TUI file with a trailing newline and
returns the saved mode with `Source="tui"`. Save errors return
`CodeAISplitModeSaveFailed` (`ai-split-mode-save-failed`), retaining the
original error text and context unchanged, with the I/O cause available through
`errors.Is`/`errors.As`. Path-resolution failures retain the legacy
`save AI split default` context; mkdir/write failures retain their PathError text.

GetAISplitMode and SetAISplitMode preserve the synchronous legacy settings contract and take no context; they do not observe cancellation.
SetAISplitMode uses a non-atomic plain write (0644 on creation, subject to umask; existing file mode preserved).
Unlike context-taking atomic APIs, these opt-in methods preserve the old
synchronous settings behavior. An interrupted write may leave partial content;
there is no atomic replacement or multi-call transaction guarantee.

Only after a successful save, the setter reads current `TMUX`, `TMUX_PANE`, and
`PATH` for the legacy best-effort `tmux -u display-message` with inherited stdio.
An empty `TMUX` suppresses display; display failures do not undo storage success.
It uses the ambient tmux context, independently of Options.SocketPath.
The existing central `GetConfig`/`SetConfig` paths, cancellation and atomic
semantics are unchanged. This API defines no HTTP error mapping.

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

## File-only approval reads

```go
list, err := client.ListApprovalsWithOptions(ctx, "uid:agent-…",
    core.ApprovalListOptions{FileOnly: true})
if err != nil {
    return err
}
switch list.ReadMode {
case core.ApprovalReadModeStored:
    // Claude's saved waiting requests, with the existing answering and ToolInput DTO.
case core.ApprovalReadModeSingleRead:
    // This provider needs a separate ListApprovals call if live reads are wanted.
}
```

`FileOnly` selects the Agent from a degraded read-only Registry snapshot. It
never observes a runtime, invokes tmux or a provider/host RPC, acquires a
mutation lock, reconciles or writes the Registry, or starts a provider. Every
normalized non-Claude provider receives `single-read` success metadata with
its own Provider and AgentUID and an empty requests array. An empty array alone
is not a provider classification. Opening the saved source can fail before
classification; Claude store errors are returned without live fallback.
`stored` preserves the existing waiting-only, effective-expiration projection,
answering policy and ToolInput display fields. Reads leave source bytes,
permissions and modification times unchanged.

`ListApprovals` and `ListApprovalsWithOptions` with zero options retain their
existing live/store selection, refusal codes and JSON bytes. Their optional
`readMode` field is omitted. No new refusal token is introduced.

## Agent questions

For captured source metadata, opt in explicitly:

```go
list, err := client.ListQuestionsWithOptions(ctx, "uid:agent-…",
    core.QuestionListOptions{IncludeSource: true})
```

The envelope's optional `provider`, `runtime` (`core.RuntimeKind`), and `phase`
(`core.AgentPhase`) describe the exact recorded Agent and its owned Pane.
`session` is present only for a running process whose captured conversation
passed the before/after fence: Claude session ID or Codex thread ID. These
values describe this read; they are not control credentials or a guarantee
about subsequent reads. Missing ownership or an unidentified running
conversation is refused with `CodeQuestionStale`. Agent/Pane ownership,
activation, binding, generation, operation, provider, phase, conversation and
connection changes discard the entire result with that code. Progress,
observation timestamps and ordinary turn updates do not change source identity.

Optional `QuestionView.ItemID` and `ExactQuestion.ItemID` (`itemId` in JSON)
come exclusively from the same Agent's async record's opaque RequestID.
History keeps its recorded ID across generations. Exact async views use a
same-Agent, same-QuestionID saved record after source fencing. Native questions,
CLI async-message questions, absent records/IDs and other Agents supply no
item ID; values returned by a custom dispatcher cannot inject one.

Process history has `CanAnswer=false`; use `ExactQuestions` for current native
and async host state. Opt-in unmatched history preserves its saved State and
Disposition: an unavailable delivery does not turn a waiting question into
closed/process-stale. Actual saved transitions and same-ID exact projections
retain their existing meaning. Stopped process reads omit session and exact questions,
preserve partial/empty saved history even on saved-source errors, and never
query the host. Running process reads query the existing host with the supplied
context; a successful exact read remains available even if saved history is
unreadable. CLI async-message records keep their independent Agent-UID policy
and answerability; failure to read that namespace fails the single read. If
opening the saved source failed, no CLI namespace read is attempted. No provider
is started. This API does not batch reads or set a polling timeout; callers own
batch deadlines, concurrency limits and per-Agent unavailable reporting.

`ListQuestions` and zero options preserve the original behavior, error order
and JSON bytes, with source fields and item IDs omitted. This is an explicit
existing-host read option, not a file-only question API.


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

For a current async set with `CanDecline: true`, call
`client.DeclineQuestion(ctx, agentRef, questionID)`. It returns a distinct
`core.QuestionDeclineResult`; its `Outcome: "submitted"` means an operator
non-answer notice was submitted, not an answer, native cancellation, or proof
of model receipt. The original turn may already have completed. The notice
covers the whole set and takes no selections. Successful readback is
`closed` / `async-declined` / `CanAnswer: false` / `CanDecline: false`.
Answer and decline race for the same once claim. A repeat returns
`CodeQuestionNotPending`; an earlier answer is never labeled a submitted
non-answer notice, nor is an earlier decline labeled a submitted answer.
Keep both actions locked for unknown or post-fence not-submitted outcomes,
including when a later valid read shows `async-declined`. Preserve the original
receipt and error; read failure or missing history never permits a resend.

`CodeQuestionDeclineUnsupported` covers blocking Codex, Claude, legacy, old
hosts, and dispatchers without decline support. Missing `CanDecline` means
false, independently of `CanAnswer`. Secret sets require the native surface.
An optional `QuestionDeclineReceiptDispatcher` supplies
`DeclineQuestionReceipt(context.Context, string, string)
(core.QuestionDeclineResult, error)` without extending `QuestionDispatcher` or
requiring `QuestionReceiptDispatcher`. A decline-only dispatcher can advertise
that capability while async answers remain unavailable. Wrapped errors retain
`errors.Is`/`errors.As` causes alongside typed outcome metadata. Already canceled
contexts before admission return the ordinary context error with no dispatch.

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
through usage and host-lost wrappers. SDK process resume preserves this detail through its detached startup error.

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

### Relaunch an Agent

`Client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: "uid:..."})` uses the
CLI's shared restart plan and stop/rebind sequence for an exact Claude or Codex
Agent. Project and Window optionally scope the explicit reference; no current
Pane or caller Agent is inferred. Process Agents and public CLI host moves are supported as described below. Configure explicit Home, Config, State and socket paths,
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
a committed tmux rebind creates a new Pane/generation. Success acknowledges the
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

## Agent and Project session history

`Client.ListAgentSessions(ctx, projmux.ListAgentSessionsRequest{Agent:
"uid:...", Project: "uid:...", Windows: []string{"uid:..."}, Labels:
[]string{"team=review"}})` returns `core.AgentSessionsList`. `Agent` is required;
Project, Windows and Labels are the explicit scopes and AND label filters from
`agent sessions list`. No active Pane, current directory or implicit scope is
used. `Client.ListProjectSessions(ctx, projmux.ListProjectSessionsRequest{
Project: "uid:..."})` returns `core.ProjectSessionsList`. Its only selector is
one required Project name or `uid:` reference, as on `agent sessions project`.
A removed Project can be read only by its exact canonical Project UID; its
`ProjectName` is empty.

Both methods use the Client's captured HomeDir, ConfigDir and StateDir without
changing the environment. They share the CLI's read service and preserve its
current/history join, ordering, recorded affiliations, removed Agent membership,
corrupt-line counts, unattributed and ambiguous session counts, and JSON keys.
Missing history produces an empty session array. Returned slices and optional
time pointers belong to the caller; modifying them cannot affect later reads.
Cancellation before admission reads nothing, and neither successful nor refused
reads write Registry or history state.

Selection failures use the existing `CodeSelectorInvalid`,
`CodeResourceNotFound` and `CodeSelectorAmbiguous` typed refusals. Agent providers
other than Claude or Codex return `CodeSessionsProviderUnsupported`. Registry read
failures use `CodeRegistryUnavailable`; history filesystem errors and context
cancellation remain ordinary errors.

`TranscriptPath` is recorded metadata only. Codex paths that the CLI leaves
empty remain empty. These methods never read transcript contents, discover
provider accounts or transcript locations, backfill history, or infer new
Project affiliations. They do not start or contact a provider.

## Refusal text shared with the CLI

Creation name validation, Agent launch settings (including profiles,
instructions, model and effort), approval provider refusals, notification store
failures, and Project name exhaustion preserve the public CLI error text.
These messages can contain CLI flags such as `--effort`; callers should inspect
`Refusal.Code` for classification instead of parsing messages. Existing Code
values are unchanged.

`Rename` accepts exact Window, Pane and Agent references under a ControlSession
root as well as under a Project. It uses the same Registry transaction and
runtime mirror checks as CLI rename, without deriving an active target.

## Peer messages and receipts

`Client.SendMessage(ctx, projmux.SendMessageRequest{Source: "uid:agent-…",
Target: "uid:agent-…", Text: "untrusted coordination"})` submits the same peer
coordination as CLI `agent message send`. Both references must resolve to exactly
one Agent. Source selects an unverified Agent anchor; it does not authenticate the
caller, grant operator authority, or select a user-input channel. No active Pane,
current directory or inherited tmux actor is inferred.

`MessageRef` optionally supplies the CLI's idempotency reference. After current route admission, a repeated matching reference returns its
stored receipt without dispatching again; a
changed immutable envelope refuses. `ReplyTo` preserves conversation correlation,
exact conversation incarnations and the original deadline. Operator-origin
messages cannot be replied to through a peer route. `TTL` zero selects ten
minutes; positive values are bounded by 24 hours. Payloads retain the existing
4096-byte UTF-8 limit, and Claude's serialized auth-plus-content frame retains its
8192-byte limit. There is no truncation, splitting or automatic retry.

Use the Client's explicit Home, Config, State and socket paths. Claude sends also
require an absolute `Options.ProjmuxExecutable` of this build so held delivery can
use the existing internal release helper with those same captured paths. That
helper inherits no provider messaging credentials or active Pane context. Sending
to an existing child-free deferred claim retains its existing held receipt and
claimant-driven first-peer delivery; this API does not create a new owner.

Both methods return `core.MessageReceipt`, including MessageRef, ConversationRef,
ReplyTo, opaque Source and Target fences, Deadline and Delivery. Delivery.State
uses `accepted`, `held`, `delivered`, `refused`, `expired`, `stale` or `failed`.
`Delivery.OutcomeUnknown` is a separate boolean; preserve it and Reason even when
the call returns an error. Delivered acknowledges peer context acceptance, not
model consumption, a response, or completion. A held receipt preserves the CLI's
existing release order and deadline. An uncertain outcome must never be
interpreted as a proven absence of delivery or automatically resent.

`Client.MessageStatus(ctx, messageRef)` follows the original receipt without
submitting input. Terminal receipts are returned as stored. Nonterminal reads can
persist the CLI's existing expiry, removed/stale target, or provider status
projection; status is not a promise of a write-free filesystem read. The result
is a snapshot whose values belong to the caller.

Inspect a partial receipt on error before recovery. Typed `*projmux.Refusal`
codes distinguish invalid input, missing messages, immutable retry mismatches,
route unavailability and delivery failures. Context cancellation remains available
through `errors.Is`. Qualification, provider creation, user turn input and
operator authentication are separate APIs and are not added by peer messaging.

### Read-only message archives

`Client.ReadMessageArchive(ctx)` returns `core.MessageArchive`: live `Records`
with full `Payload`, reclaim-log `History` without bodies, and `Skipped` for
malformed or ref-less history lines (blank lines do not count).

Payload access: **권한: 같은 사용자의 StateDir 읽기, 비밀 처리 없음**.
Live bodies are exposed without redaction or secret filtering. Consumers decide
where they may display or forward them; this API adds no network endpoint.

The Client uses only its captured `StateDir`, including an explicit absolute
`Options.StateDir` override. It directly shares CORE's archive reader and store
validation, without Registry, provider or tmux observation. Missing files are
empty; read or store validation errors keep their original text and wrapped
causes and return no partial archive. Legacy store conversion happens in memory,
without migration writes. Unknown history keys and known fields from higher
history schemas retain the existing reader's meaning.

Live records preserve message/conversation/reply refs, full routes, origin and
all `MessageDelivery` fields, including reason and outcome uncertainty.
`PayloadBytes` is the UTF-8 byte length. History preserves schema version, refs,
state, acceptance time, payload length/digest, origin and both full or narrow
routes. An empty digest means it was not recorded. `Operator` on both shapes
comes from CORE's existing origin judgment; callers need not copy that policy.

Order and duplicates remain store → history.jsonl → history.jsonl.1, including
messages present in both live and history or repeated across generations.
Deduplication, sorting, graph aggregation and relay decisions belong to the
consumer. Slices are caller-owned, preserve nil/empty shape, and cannot change
later calls. Reads create nothing and take no lock. A store read observes one
complete renamed version, but there is no atomic snapshot across the three files.

`Client.MessageArchiveFiles(ctx)` returns a caller-owned slice of exactly these
three paths under `StateDir/agent-messages`, in the same order: `messages.json`,
`history.jsonl`, `history.jsonl.1`. It opens and stats nothing and works even
when contents are missing or corrupt. Consumers can stat size and mtime for
change detection; the SDK does not replace file timestamps with the current time.

Both methods check cancellation before admission and after a successful
synchronous read or path projection. They cannot interrupt an ongoing file read;
a reader error takes precedence over cancellation during that read. Cancellation
returns a context error and no partial result. Existing `SendMessage` and
`MessageStatus` behavior is unchanged.

### Detached process Agent resume

`Client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:agent-…",
Detach: true, Prompt: "continue"})` resumes an existing process Agent through
the same internal detached execution service as CLI `agent resume --detach`.
Claude requires a nonempty Prompt; Codex permits promptless resume. Agent UID,
Pane UID and provider conversation remain the same, with a fresh owner generation.
The process result has no tmux PaneID. `Binding` identifies the exact generation;
provide the last observed Binding in the request to refuse a changed generation.
Project and Window optionally scope the explicit Ref.

`WaitForPeer: true` requires an empty Prompt. Success sets `ClaimWaiting` and
acknowledges an exact child-free owner claim with the previous Binding. The first
peer input starts the preserved conversation under a new generation. Stopping
that claim before birth releases it without inventing a provider Wait record.
Use the born binding from a fresh resource read after peer delivery.

Process requests require Detach and reject SocketPath and DialogueReplyOnly.
The zero values preserve the tmux resume API; process-specific inputs on tmux
Agents refuse before effects. Use `RelaunchAgent` for process relaunch and host moves.

Use an absolute `Options.ProjmuxExecutable` from the same build. The constructor's
captured Home, Config, State and provider environment reach the detached owner
and supervisor, including arbitrary absolute Config/State directories. Later
ambient environment changes do not alter the request. No account selection or
AccountRef is added: captured CODEX_HOME and CLAUDE_CONFIG_DIR retain their existing
meaning. Authentication files are neither read nor copied by this API.

Cancellation before admission prevents execution. Startup cancellation settles
through the owned rollback path; after success, caller cancellation or exit does
not stop the detached owner. Inspect partial results on error: `*projmux.Refusal`
contains a typed code and wraps `*projmux.ResumeProcessError` with Owner and Binding.
Unknown cleanup retains exact recovery resources and must not be automatically
retried. A committed generation that later exits retains its actual termination
record; a child-free claim does not prove provider birth or a response.


### Process relaunch and execution host moves

`RelaunchAgentRequest{Ref: "uid:agent-…", Detach: true}` restarts an existing
process Claude or Codex Agent through the same internal owned executor as CLI
`agent relaunch --detach`, even when its settings are unchanged. `Prompt` supplies
explicit first input. Promptless same-host Claude instead returns `ClaimWaiting`
with an exact child-free claim and a frozen launch recipe. The first peer input
starts that recipe in the preserved conversation. Stopping the claim before
birth releases it without inventing an actual Wait record.

Set `Host: "process", Detach: true` to move a tmux Agent into an independent
process owner; Claude requires a Prompt for this move. Set `Host: "tmux"` with an
explicit absolute `SocketPath` to move a process Agent into tmux. Unsupported
public CLI combinations refuse before Stop. SDK foreground process ownership is
unsupported. Omitted Host retains the source host and never uses the configured
creation default. Existing tmux-only calls retain their behavior.

Agent UID and conversation are preserved; same-host process relaunch also keeps
the Pane UID and starts a fresh owner generation. `CurrentHost`, `TargetHost`,
`Binding` and `ClaimWaiting` identify the result. An optional request Binding
fences the source generation. DryRun, self-target, busy without Confirm, stale
bindings and unsupported requests cannot stop the source. Old process writers
must pass exact Stop, actual Wait and retirement barriers before replacement.

Use `Options.ProjmuxExecutable` from the same build. Frozen Home, Config, State
and captured provider environment reach the existing internal owner, including
arbitrary absolute Config/State roots. The SDK reads or changes no ambient
environment during the operation and does not select accounts or read/copy
credentials. No public CLI command is used as an execution subprocess.

Cancellation prevents admission; admitted tmux rebind/recovery finishes despite
later cancellation. Process startup failure or cancellation settles through the
owned rollback path. After a successful detached handshake, caller cancellation
or exit cannot stop its owner. Success proves ownership/commit, not a response.
Inspect partial results and `*projmux.RelaunchProcessError` (Owner, Binding, Code)
through `*projmux.Refusal` and errors.As. `CodeRelaunchSelfTarget` and
`CodeRelaunchSourceStale` distinguish those refusals; wrapped causes retain context
and stale-binding identity. Unknown cleanup never means Offline success; inspect
the exact resources before public Stop/Resume/Delete recovery and do not retry
automatically. The existing CLI admission and output contracts are unchanged.
`CodeRelaunchProcessUnsupported` remains exported for source compatibility.

Host-lost recovery is outside this SDK relaunch contract; use the public CLI
`agent relaunch` recovery path after inspecting the exact resources. The SDK
refuses absent ownership evidence without replacing it with an invented Wait.

## Profile name validation

`projmux.ValidateProfileName(name string) error` checks a name without a Client
or context. It uses the existing profile name rules, preserves case, and rejects
the reserved name `none`. It does not trim or normalize the input, check whether
a profile exists, access the environment, files, Registry, paths or network,
write anything, or take locks.

A valid name returns nil. Invalid names return `*projmux.Refusal` with
`CodeProfileNameInvalid` or `CodeProfileNameReserved`; `Name` and error text
preserve the existing profile refusal.

```go
if err := projmux.ValidateProfileName(name); err != nil {
    var refusal *projmux.Refusal
    if errors.As(err, &refusal) {
        fmt.Println(refusal.Code, refusal.Name)
    }
    return err
}
```

## Profile parsing and previews

`Client.ParseProfile(ctx, content)` checks UTF-8, the 64 KiB byte limit and
profile TOML vocabulary, returning `core.ProfileSpec`. It does not read profile,
instructions or Registry storage. Named instructions need not exist for parsing.
`ProfileSpec` exposes project, provider, instructions, model, effort, roles and
`ProfilePermissions` (sandbox, approval, allow, deny). `HasPermissions()` is true
when any permission value is set; empty tables and lists return false. Returned
lists encode as `[]`, and JSON fields use lower camel case.

`Client.PreviewProfile(ctx, name, content)` checks a storage candidate with the
same name, size, parse, instructions existence, role claim and immutable scope
rules as `WriteProfile`. It returns `core.ProfilePreview` with name, raw-byte
digest, byte size, spec and `DryRun: true`. Success reserves nothing: a later
write repeats all checks. Preview creates no files, directories, migrations or
locks, and does not read or change the Registry. Context cancellation returns a
context error; invalid profiles return the existing typed `CodeProfile*`
refusals and no partial result. I/O errors remain ordinary errors.

The matching CLI commands are `projmux profile parse [--file <path>]` and
`projmux profile preview <name> [--file <path>]`. Omitting `--file`, or using
`--file -`, reads stdin. Both commands print typed JSON, exit 2 for invalid input
and exit 1 for I/O failures. Existing `profile show` and `profile set` output is
unchanged; raw bytes remain available through `ReadProfile`.

These operations cover profile TOML and local storage admission. Bundle JSON,
bundled instructions, overwrite decisions, permission expansion confirmation
and atomic bundle import are outside this API.

### Replacing a profile and restoring its original

`Client.RecreateProfile(ctx, name, content, true)` replaces an existing profile
and permits a project scope change. It returns the existing
`core.ProfileWriteResult`. Confirmation is required. Cancellation, name validation,
confirmation, the new content's 64 KiB limit and writable path resolution precede
snapshot admission. The original must exist, fit the limit and pass pure parsing;
named instructions and role claims need not currently be usable.

The store prepares a same-directory snapshot before calling its existing Delete
and Write operations. New content receives every existing Write validation. If
Write fails, the store restores only the original bytes captured by that call,
without checking instructions or role claims again. A successful restoration
returns the original Write error and a zero result; the original file mode is preserved, including 0600 and 0640.
This intentionally changes the former web recovery behavior, which normalized
nonstandard original modes to 0600. A successful replacement uses Write's 0600 mode. Cancellation after admission
does not interrupt restoration. A readable in-directory symlink follows the
existing store behavior: Delete removes the link itself and restoration produces
a regular file, leaving its former target untouched.

If restoration also fails, `*projmux.Refusal` has
`CodeProfileRestoreFailed` (`profile-restore-failed`). Its message identifies the
profile, says the original was not restored and gives the retained snapshot path.
Both replacement and restoration causes remain traversable with `errors.Is` and
`errors.As`; the replacement's typed refusal is available below `Refusal.Unwrap()`.
Inspect the profile and retained snapshot before recovering or retrying. This is
not a successful replacement or a claim that nothing changed. The API accepts no
caller-supplied restoration bytes, path or mode. It does not provide crash
recovery or serialize concurrent external edits. Existing Write, Delete and
Preview behavior is unchanged; this operation adds no CLI command.

### Project guidance editing

`ReadProjectGuidance(ctx, projectRef)`, `ValidateProjectGuidance(ctx, projectRef,
content)`, `WriteProjectGuidance(ctx, projectRef, content, revision)` and
`DeleteProjectGuidance(ctx, projectRef, revision)` share the CLI's `agent guidance
read/validate/set --project <ref>` and `delete --project <ref>` service. A ref is an
explicit registered Project name or `uid:` ref; inactive Projects are supported.
An empty ref is refused. These methods read Registry through the same degraded,
read-only resource loader: a graph-invalid Registry can still select its Project,
while malformed, unsupported and lost Registry state is refused without repair.

`core.ProjectGuidanceFile` carries `projectUID`, `path`, `present`, raw `text`,
`bytes`, `digest`, `revision` and `maxBytes` (65536). The separate `source`,
`effectiveText` and `effectiveDigest` retain the existing Project guidance load
meaning: absent, empty and whitespace-only files are off. Raw bytes still
identify each stored state within the limit. Existing NUL is returned unchanged; invalid UTF-8
reads are refused with `agent-guidance-unreadable`. Validate a valid candidate to
obtain the current revision and replace invalid existing UTF-8 without reading it
as text. Candidates must be UTF-8 without NUL and fit the byte limit.

Existing oversized **Project** files return metadata with `oversize: true`,
`bytes` equal to the actual file size, `prefixBytes` equal to 65537 and
`prefixDigest` equal to the SHA-256 of those leading bytes. Total file reads are
bounded to 65537 bytes per state observation. `text`, `digest`, `source`,
`effectiveText` and `effectiveDigest` are empty; no partial body is returned or
used as effective guidance, and oversized bodies are not fully UTF-8 checked.
CLI `agent guidance read --project <ref> -o json` returns the same metadata;
plain reads retain `agent-guidance-too-large` with empty stdout. Global guidance
editing retains its existing oversized-file refusal.

The oversized file's revision binds the selected UID, presence, actual size,
file identity (device/inode), mtime and prefix digest. Inconsistent stat
observations return `agent-guidance-unreadable` without retrying. Use the revision
from read or validate to replace the file with a candidate within the limit, or
to delete it. A changed size, identity, mtime or prefix yields
`agent-guidance-stale`. A suffix-only edit that restores size, mtime and identity
is outside this bounded fingerprint guarantee. Recovery clears `oversize`,
`prefixDigest` and `prefixBytes`; normal raw revisions remain unchanged.
These three additive fields are always present in Project JSON (false/empty/zero
for normal files), which affects exact JSON comparisons and unkeyed Go struct
literals.

`core.ProjectGuidanceValidation` returns candidate text, bytes, digest, maxBytes,
projectUID and the current target revision. Read and validate create no locks,
directories, migration receipts or snapshots. A revision identifies the selected
Project UID, raw bytes and presence (or the bounded fingerprint above). Write and delete require it; stale inputs
return `agent-guidance-stale`. Both recheck under the shared guidance writer lock
and preserve global user guidance, policy and fragments. Writes use the existing
atomic save and permission policy. Deleting an absent file with its current
revision succeeds. Context cancellation or deadline before writer admission is
preserved without changing guidance; an admitted atomic mutation finishes.
External editors that ignore the lock are outside this concurrency guarantee.

## Project link rules in a degraded Registry

The existing `ReadProjectLinkRules`, `WriteProjectLinkRules`, and
`DeleteProjectLinkRules` methods require a valid Registry. Their `WithOptions`
variants accept a final value argument, `core.ProjectLinkRulesOptions`:

```go
options := core.ProjectLinkRulesOptions{AllowDegradedRegistry: true}
rules, err := client.ReadProjectLinkRulesWithOptions(ctx, "uid:proj-...", options)
preview, err := client.WriteProjectLinkRulesWithOptions(ctx, "uid:proj-...", input, true, options)
empty, err := client.DeleteProjectLinkRulesWithOptions(ctx, "uid:proj-...", true, options)
```

Omitting the option, or setting it to false, preserves strict behavior and result
JSON. CLI `link-rules show`, `set`, and `delete` accept the corresponding
`--allow-degraded-registry` boolean flag. This is a per-call choice, with no
saved setting or environment toggle.

Opting in permits link rules CRUD when an unrelated resource has a dangling
owner. Registry access stays read-only. The raw Project reference must match
exactly one row, and its UID must occur exactly once across all Projects, before
selector deduplication. A unique UID reference works even when another Project
shares its name. Missing, ambiguous, invalid, malformed, unsupported, lost, and
unreadable inputs retain the existing typed refusal codes. Rule validation,
`dryRun`, deletion confirmation, and result fields retain their existing meaning.

SDK reads, target resolution, validation, dry runs, and cancellation before
mutation admission leave the explicit Config/State paths unchanged, including
Registry and target/non-target rules bytes, directory modes, and modification
times. After admission, existing writer semantics apply: I/O errors or permission
repair can leave partial effects such as created directories. These methods add
no rollback or concurrency guarantee. CLI bootstrap, diagnostics journaling, and
legacy migration retain their existing policy; the CLI does not promise an
unchanged entire HOME directory.

### Agent deletion protection label

`SetAgentProtection` changes only `projmux.io/protected` on one existing Agent.
Use an explicit `uid:` ref, or a single name with explicit `Project`/`Window`
scope; the SDK never infers a target from an active Pane, cwd or environment.
Both Project and ControlSession descendants and tmux/process Agents are supported.

```go
result, err := client.SetAgentProtection(ctx, core.SetAgentProtectionRequest{
    Ref: "uid:agent-example",
    Protected: true,
})
// Set Protected: false to remove the key.
```

True writes the value `true`; false removes the key even if its old value is a
legacy value. Other labels, fields and relationships remain unchanged. `Agent`
is the projection from the same Registry commit; `Changed` is false on repeats.
The shared CLI label service performs the locked existence check and CORE write,
without CLI subprocesses, tmux or provider calls.

Handle `*projmux.Refusal` with `errors.As`: `CodeSelectorInvalid`,
`CodeSelectorAmbiguous`, `CodeResourceNotFound` and `CodeRegistryUnavailable`
distinguish selector and storage failures. A target that disappears before the
locked write returns `CodeResourceNotFound`. Cancellation before the locked write
preserves `errors.Is(err, context.Canceled)`/`context.DeadlineExceeded`; once the
write starts, the existing Store transaction completes.

Protection is still evaluated by the existing delete policy: presence of the
reserved key, including legacy values, protects deletion. After removing it,
call `PreflightDelete` again before any deletion effects. Profiles may still
protect an Agent. Setting the label grants no deletion permission. Web session
authorization and the consumer's `agent.protection` mutation lock remain the
consumer's responsibility; this method supplies neither.

### Prepared Claude process resumes

`PrepareAgentResume` explicitly freezes the existing process Claude resume
recipe without starting a provider, retaining an owner or acquiring a claim.
`AllowHostLost: true` enables the existing absent-host checks; its default is
false. A nonzero `Binding` fences the observed generation. Codex continues to
use ordinary detached `ResumeAgent` with an empty prompt.

```go
prepared, err := client.PrepareAgentResume(ctx, core.PrepareAgentResumeRequest{
    Ref: agentRef, Binding: observedBinding, AllowHostLost: true,
})
if err != nil {
    return err
}
proof, err := client.ValidateAgentResumePreparation(ctx,
    core.ValidateAgentResumePreparationRequest{
        Ref: agentRef, Preparation: prepared.Preparation,
    })
if err != nil {
    return err
}
result, err := client.ResumeAgent(ctx, core.ResumeAgentRequest{
    Ref: agentRef, Detach: true, Prompt: userInput,
    Binding: proof.Binding, Preparation: &proof,
})
```

The proof is observed evidence, **not authorization or a writer grant**.
Session authorization, mutation locks and acknowledgement storage remain the
consumer's responsibility. Validation only reads the existing plan, source and
snapshot digests; it does not reconcile a plan. It cannot replace the independent
owner's proof check under the claim guard before reservation. A missing,
modified or replaced proof is a `Refusal` with `CodeResumePreparationStale`.
A stale explicit request `Binding` remains `CodeResumePlanInvalid`; unsupported
providers or input combinations remain `CodeResumeRequestInvalid`.

Supplying `Preparation` requires process Claude, `Detach: true`, a nonempty
user `Prompt`, `WaitForPeer: false` and no model or effort override. A nil
`Preparation` preserves ordinary Resume, including its existing host-lost
admission and Claude prompt requirement. Preparing without input is distinct
from `WaitForPeer`, which retains its existing claim and retirement contract.

On success `result.Preparation` is nil. On failure it may contain the still
valid original proof, or the same frozen recipe readdressed by the existing
engine after an exactly reaped failed child with actual Wait. Unknown ownership,
replacement and changed recipes return no retry proof. No input is replayed.
The consumer decides whether to acknowledge a returned proof, validate it again
and submit another explicit input. Existing `ResumeProcessError` recovery
identity and cancellation/cleanup errors remain available through `errors.As`.
Success establishes the detached resume; it does not promise a provider reply.

Successful process `ResumeAgent` returns `MayBeTruncated` from the independent
owner's existing CORE `Previous.MayBeTruncated` value. True advises that prior
input may have been cut off; it proves neither actual truncation nor actual
Wait. Host-lost ordinary Resume with nil `Preparation` also returns this new
boolean. Failures, tmux, claim waiting and CORE results without that advisory
return false. Preparation proof does not determine the advisory, and existing
Notices and CLI output are unchanged. Read the prior generation's
`LastTermination` (`reconcile` / `unknown`, no invented exit code) and
`ProcessSession.History` through `GetResource` on its Pane; the Agent exposes
the same termination evidence.
