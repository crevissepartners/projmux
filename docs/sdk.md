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

`SettingsDirectories(configDir)` returns `(profilesDir, personasDir)` beneath an
already resolved projmux configuration directory, for example
`projmux.SettingsDirectories(paths.ConfigDir)`. It uses the CLI's profile and
persona directory names and `filepath.Join` lexical cleaning. Empty and relative
inputs remain relative; environment variables and tilde are not expanded.
The function accesses no environment, files, Registry, tmux or providers and
creates nothing, including when the directories are missing. It does not check
existence or access permissions or make paths absolute.

`SettingsInputPaths(configDir, projectUID)` returns
`core.SettingsInputPaths` beneath the same resolved configuration directory,
with the same lexical cleaning and the same absence of effects: the agent
guidance file (`GuidanceFile`), its policy (`GuidancePolicyFile`), its
fragments directory (`GuidanceFragmentsDir`, whose `.md` files are read), the
one-time migration receipt (`GuidanceMigrationReceiptFile`) and its backup
(`GuidanceMigrationBackupFile`), the directory holding every Project's label
link rules (`ProjectLinkRulesDir`), and, for a bare Project UID, that
Project's guidance (`ProjectGuidanceFile`) and label link rules
(`ProjectLinkRulesFile`, inside `ProjectLinkRulesDir`). An empty `projectUID`
returns the five global paths and `ProjectLinkRulesDir` and leaves the two
Project file fields empty; any other value that is not a bare Project UID is a
`CodeSelectorInvalid` refusal with the zero result. Nothing says a path exists
or can be read. The directories' entries and each fragment are for the caller
to observe.

`MaxProjectLinkRulesBytes` and `MaxAgentGuidanceBytes` are the byte limits the
SDK's own validators enforce, defined by them rather than copied. A stored
link rules file larger than `MaxProjectLinkRulesBytes` is a
`CodeLinkRulesUnreadable` read refusal, and rules whose stored encoding would
be larger are a `CodeInvalidLinkRules` write refusal on field `links`. Host-global
or Project guidance larger than `MaxAgentGuidanceBytes` is a
`CodeAgentGuidanceTooLarge` refusal. Rules or Project guidance of exactly the
limit are accepted. Host-global guidance is also refused with
`CodeAgentGuidanceTooLarge` when, while guidance is enabled, the built-in text,
the process note, fragments and it together would be larger than the same
limit; `AssembledBytes` and `ProcessAssembledBytes` report those sizes.
A caller that bounds a JSON request body before decoding it should leave room
for escaping, at most six bytes per input byte, so that one byte past the limit
still reaches the validator and gets its refusal rather than a transport error.

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

Each `DeletePlan.Targets[i].Owner` carries the target's owner chain exactly as
the CLI delete plan prints it after `owner=`, for example
`project/alpha window/main`. It is empty for a target without an owner, such as
the Project `UnregisterProject` removes, and cascade `Descendants` have no owner.

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

For tmux Agent cleanup, `Client.Delete` with `KindAgent` finds tmux only in
absolute directories on the PATH captured when the Client was created, even
if the ambient PATH later changes. A missing captured tmux or cleanup failure
is returned to the caller. Relative PATH entries and Clients created without
tmux on their captured PATH are not supported by this guarantee. No tmux
executable option is added: `Options.ProjmuxExecutable` names projmux, not
tmux. CLI behavior and SDK Window/Pane deletion are unchanged.

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
`Operator: core.NewOperatorCreator("<client>")` records an in-process operator
client as the creator (`creator-basis=operator`) on the Agent and its Pane,
under the same rule as the CLI's in-process operator seam. It takes precedence
over `Creator`, which must still name an Agent and is then reported in
`Notices` with the CLI's stderr line
`creator declaration not recorded: operator-client (--creator uid:<agent>)`.
`core.OperatorCreator` has no exported field, so decoded request data (JSON, a
form, a `Creator` string) never names an operator; its zero value names none. A
client name outside the operator client rule refuses with
`CodeCreateCreatorInvalid` before anything changes. The SDK does not know who
called it: attaching `Operator` only for a request the operator made is the
caller's responsibility. Detached process creates carry it to the owner route as
a typed request field, never as argv.
Profiles, instructions, model, effort, workspace roots and placement use the
CLI's existing validation. Set `Options.ProjmuxExecutable` from the same build.

`Client.PreflightCreateAgent(ctx, req)` answers, before the caller creates a
Window for the Agent, the refusals `CreateAgent` would give the same
`CreateAgentRequest` for its profile, instructions and model, with the same
`Code` and message as `CreateAgent` and `projmux create agent`. It runs the
checks `CreateAgent` runs before its first effect, in the same order, and reads
the stored instructions the request or its profile names; it writes nothing,
runs no tmux command and starts no process. `Window` may be empty, and a named
Window or Pane is only parsed, not resolved. A process request is first refused
as `CreateAgent` refuses its shape, and a provider its profile chooses must be
Claude or Codex. Without a refusal it returns `core.CreateAgentPreflight` with
the provider the create will run, the profile's when the request names none.
It does not authorize the create: `CreateAgent` checks again, so settings
changed in between decide. It cannot answer what only the create's effects
reveal: a Window or Pane that does not resolve, an instructions or profile
settings snapshot that cannot be written, Codex native preparation, a missing
Project root or the workspace. A request with more than one defect can meet a
different first refusal in the detached process creator, which checks in
another order.

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

`Client.CreateWindowFromIntent(ctx, intent)` creates a new Window in an explicit
Project from one `core.WindowCreateIntent`, with either its shell Pane (`Agent`
nil) or a new Agent, in one transaction. A tmux Agent commits the shape
`projmux create window --provider` commits: the Agent's Pane splits off the
Window's initial shell, the shell is retired, and the Window holds only the
Agent Pane. A failure on the way leaves neither the Window nor the Agent. A
process Agent requires `Detach` and goes through the process creator, which
reserves and rolls back the Window together with the Agent. `Host` defaults to
tmux; no host is inferred from the config. `Name` and `Labels` are the
Window's, and the Agent takes a generated name and its own `Agent.Labels`.

The Agent half is checked by the validator `CreateAgent` runs, so its refusals
keep `CreateAgent`'s `Code` and words, and `Creator` and `Operator` follow
`CreateAgent`'s precedence. `Agent.CwdFrom` `pane` with an exact `uid:`
`Agent.CwdPane` of the same Project starts the Agent in that Pane's directory
under the split rule: inside the Project tree it is used, otherwise the Agent
starts in the Project root and `Notices` carries the fallback line. A tmux
create always refuses a provider the providers setting has switched off;
`RequireEnabledProvider` asks the same of a process intent, in the preflight
and again in the create. `Client.PreflightWindowIntent(ctx, intent)` runs every check the
create runs before its first effect and returns `core.WindowIntentPreview`
(shell, host, provider, profile, cwd source); like `PreflightCreateAgent` it
writes nothing and does not authorize the create.

`Client.ListResumeCandidates(ctx, core.ResumeCandidateQuery{Project, Provider, Cwd})`
lists the recorded sessions the CLI resume picker lists for the same
directory, in the same order: the same enabled providers, scan depth, row
limit, per-provider budgets, Codex catalog routes and collision rules. `Cwd`
is absolute and defaults to the Project root; `Provider` empty lists every
enabled provider. A `core.ResumeCandidate` carries what a selection needs
(`Provider`, `Conversation`, `Source`, `Cwd`) and the row summary (`Title`,
`Branch`, `UpdatedAt`); no transcript or preview text is read. Reading the
Codex catalog may start the default Codex app-server proxy, as the picker
does. Each call runs its own discovery and shares no state with another call.

`Agent.Resume` names one candidate to continue in the new Agent, on tmux only:
a process intent with `Resume` is refused before any effect, with no tmux
fallback. The Agent inherits its launch values and profile from the Agents
that record the conversation, as the picker's create does, so `Profile`,
`Instructions`, `Model`, `Effort`, `Payload` and `CwdFrom` `pane` are refused
with it, and `Agent.Provider`, when set, must equal `Resume.Provider`. For a
Claude, Antigravity or Codex rollout selection the preflight and the create
both list the selection's directory again, without the Codex catalog, and
refuse a selection that is no longer a candidate (`resume-candidate-stale`)
before any effect; inside the transaction, holders that disagree about the
profile refuse the create. The new Agent records the conversation as its
session in the same transaction.

A Codex app-server candidate resumes on the native lane, as the picker's
create does: the thread is resumed on the default Codex app-server endpoint
of its state domain with the policy of the inherited profile, and the new
Pane attaches to it. The preflight checks only its shape and does not read
the Codex catalog; the create finds the row again in the catalog right
before its transaction and refuses before any effect a row that is gone
(`resume-candidate-stale`) or whose endpoint or generation no longer
resolves to a current route.

`core.WindowIntentResult` carries the Window, the Agent, the `create.window`
receipt with the Agent row, the remaining PaneID and, for process, the
`Binding`. An unconfirmed tmux activation returns them together with
`CodeCreateActivationUnconfirmed`; a process startup failure returns a
`Refusal` whose `CreateProcessError` keeps the exact recovery references.

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

`ResumeAgent` and `RelaunchAgent` take no Labels input and do not change the
labels of the Agent they act on. Tests cover a process Agent resumed after its
host was lost and a Claude Agent relaunched from tmux to process and back; both
keep the labels a caller set.


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

## Observe a process Agent

`Client.ReadAgentProcess(ctx, core.AgentProcessRequest{Agent: "uid:..."})`
observes one exact process Agent generation, and
`Client.ReadAgentProcessEvents(ctx, core.AgentProcessEventsRequest{...})` reads
its events after a sequence cursor. Both read the generation's owner without
changing it: cancelling a read or losing the connection never stops the owner
or provider and never records a Wait.

- `Agent` must be one exact `uid:` reference. A zero `Binding` reads the
  current generation once and returns its `Binding` (Host, Pane, Generation);
  pass it to later reads. A Binding built from a graph `GraphProcessKey` is a
  request, not authority: each read compares it again with the live host and
  generation and never follows a newer generation.
- The Snapshot carries the state, whether a turn is running, the number of open
  control requests, the CLI's attention projection, the owner mode, the host and
  child process identities, and `Exit` from actual Wait evidence only. Session,
  connection and turn identifiers and request content are not part of it; read
  session IDs with `ListAgentSessions` and open requests with `ListQuestions`
  and `ListApprovals`.
- The Snapshot also carries the owner's advertised `Protocol` and `Actions`;
  `Running` is the owner's Turn, so a turn can be interrupted when
  `Protocol >= 1`, `Running` and `Actions` contains `interrupt`. `OwnerSocket`
  (device and inode) is the identity of the socket `ReadAgentProcess` read;
  pass it back as `AgentProcessRequest.OwnerSocket` to refuse a replaced
  socket (a zero `Inode` skips the check).
- An owner proven gone before any read is a `CodeProcessOwnerUnreachable`
  refusal (`ErrProcessOwnerUnreachable`, unchanged) whose `Refusal.Reason` is
  `owner-identity-gone` (recorded owner process absent or reused),
  `owner-socket-gone` (socket path missing) or `owner-socket-replaced` (not a
  socket, or a different identity than requested). An empty `Reason` is a
  transient or unknown failure. A recorded Wait, a closed generation or a newer
  generation still wins as the final Snapshot, `ErrProcessClosed` or
  `ErrProcessStale`.
- An event batch lists ascending events after `After`; `Next` is the next
  `After`. `Gap` reports events the host itself dropped (its `stream-gap` event
  is in the batch) and `Snapshot` resynchronizes. `More` reports that the
  answer frame left events for the next read. `Omitted` names returned events
  whose provider payload did not fit one frame; their identity and `Exit` are
  kept. `Terminal` reports a generation that ended with its final Snapshot.
- A call's owner reads and repeats share one owner read deadline (5s); the
  Registry file reads and scheduling delay around them are outside that budget,
  so it is not a strict wall-clock bound. `Wait` is how long a long poll waits
  for a new event after `After`: zero reads once, and any value is capped at
  5s. It never shortens the read of a slow owner. When the owner answered but
  no event followed, the call returns an empty batch. The SDK repeats the read
  without a polling goroutine, and reads the Registry again between reads, so a
  generation that ends during the wait returns its final Snapshot. A cancel
  also ends a read the owner has not answered yet.
- Refusals are `*Refusal` values whose `Unwrap` reaches both a sentinel and the
  original cause: `stale-binding` (`ErrProcessStale`) for another generation,
  `process-closed` (`ErrProcessClosed`) for a retired generation with no final
  evidence, `process-host-unsupported-read` (`ErrProcessReadUnsupported`) for an
  owner built before it served event reads (no request is sent to it), and
  `process-owner-unreachable` (`ErrProcessOwnerUnreachable`) for an owner that
  did not answer within the call, which is not a provider exit.

## Read every process Agent's attention

`Client.ReadProcessAttention(ctx, core.ProcessAttentionRequest{})` returns the
badge of every process Agent that has one. Whatever the number of Agents, it
reads the process attention store file once, loads the Registry once when the
store is not empty, and never touches an owner socket.

- `core.ProcessAttention.Agents` maps an Agent UID (without `uid:`) to a
  `core.ProcessAttentionBadge`: `in_progress`, `approval_required`,
  `input_required` or `response_complete`. An Agent with no badge is absent. The
  map belongs to the caller.
- An Agent is listed when its current declaration has a stored record of the
  same binding and provider, a nonempty generation and no `Terminal` mark, and
  the record's badge (aggregated with its open requests) is one of the four.
- The source is the durable store, not the Registry's interactions, so it
  differs from `AgentProcessSnapshot.Attention` and can show a badge for an
  owner that is already gone.
- A missing or empty store is an empty map with no error and no Registry read.
  A damaged store is a `CodeProcessAttentionDamaged` refusal
  (`process-attention-damaged`) whose `Unwrap` reaches `ErrProcessAttentionDamaged`
  and the original cause; the read never repairs the store.

## Reserve a process Agent without starting it

`Client.ReserveAgent(ctx, core.ReserveAgentRequest{...})` creates one `Pending`
Claude process Agent with its launch frozen and starts no provider process. The
entry that starts it comes later; until then a reservation can only be deleted.

- Exactly one of `Window` (an existing Window of `Project`) and `NewWindow`
  (`core.ReserveNewWindow{Name, Labels}`) is set, else `create-request-invalid`.
  There is no `Host`, `Detach`, `Pane`, `Placement` or `Payload`.
- The result carries the reserved `Binding`, `Agent` and receipt.
- The frozen launch is a private file (0600, at most 64 KiB) in the
  `created-launches` store, apart from `deferred-launches`. A launch that cannot
  be frozen fails the
  reservation and removes the Agent, its Pane and a Window the call created,
  except what could not be removed (a protected Agent, a worker that outlived
  the cleanup bound, a lost final reply). The failure then names the remains by
  exact refs, sets `Refusal.AgentUID` and the result's `Binding`, and `errors.As`
  reaches a `CreateProcessError`.
- Another provider is a `reserve-provider-unsupported` refusal.
- `Delete` of a reserved Agent removes its frozen launch, and with the last
  Agent of a new Window the Window. A launch left behind is reported in `Notice`.
- An older executable rejects a reservation request before any effect.

## Mark a process Agent whose host died offline

`Client.MarkAgentOffline(ctx, core.MarkAgentOfflineRequest{Ref: "uid:..."})`
retires one process Agent whose recorded host and provider child are both gone,
through the same executor as CLI `agent mark-offline`. `Project` and `Window`
optionally scope the reference; a nonzero `Binding` requires that exact recorded
generation and a different one is refused with `agent-mark-offline-stale` before
effects. It starts, signals, and invokes no provider and no CLI subprocess.

The kernel must prove both recorded identities absent, and the proof is repeated
inside the Registry transaction against the exact current activation. Success
clears the activation, records `status.lastTermination` with source `reconcile`
and classification `unknown` and no exit status, sets the Agent `Offline` and
`processSession.resumeState=resumable`, and keeps the Agent UID, Pane UID, and
conversation. This is not Wait evidence and never a stop; public CLI resume
continues the same conversation. A relaunch without a first prompt, including a
Claude resume prepared to wait for its first input, accepts this exact record
of the same generation as the old child's retirement, as it accepts a Wait, and
the first input continues the same conversation. An Agent that already holds
this exact record returns `Changed=false` without a write.

`MarkAgentOfflineResult` includes `AgentUID`, `PaneUID`, `Binding`,
`Termination`, `Changed`, and the `agent.mark-offline` `Receipt`. Typed
`Refusal.Code` values match the CLI tokens: `agent-mark-offline-process-unsupported`
(tmux or ambiguous), `agent-mark-offline-owner-present` (an identity is alive or
its absence cannot be proven; use `StopAgent`), `agent-mark-offline-not-host-lost`
(no recorded process host), `agent-mark-offline-conversation-unavailable`, and
`agent-mark-offline-stale`. Every refusal leaves the Registry unchanged.

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
`effects.runtime` is `stopped` only when a live tmux session and all selected
process Agents stopped. It is `partially-stopped` when confirmed stops coexist
with a failure or unconfirmed stop, and `agents-stopped` when no session existed
and all selected process Agents stopped. With no session, the Project action is
`unchanged`; confirmed Agent actions are `stopped`. The public `core.RuntimeEffect`
constants include `RuntimePartiallyStopped` and `RuntimeAgentsStopped`. Consumers
must accept these additional values; the receipt version and fields are unchanged.
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

For a tmux Claude Agent that records reply-only dialogue mode, both relaunch
prediction and the resume execution preserve that record. The fixed reply-only
launcher and activation flag are retained; ordinary guidance and project-link
prompt additions are not introduced by the relaunch plan. The existing fixed
launch restrictions on profiles, instructions, model and effort still apply.

`Capabilities` reports the five Codex process controls (`turn.start`,
`turn.steer`, `turn.deliver`, `turn.interrupt`, `approval.review`) from the
current owned process activation and matching durable thread, without requiring
tmux broker epochs. Stale, retired or mismatched process identities are not
eligible. Provider support and tmux authority checks are unchanged. This remains
Registry evidence, not a live execution guarantee: `LiveVerified` stays false,
and mutations revalidate the actual provider authority at execution time.

`CurrentModel` and `CurrentEffort` are the model and effort the Agent records,
the CLI's `currentModel` and `currentEffort`; they can differ from
`CurrentSettings`, which holds what the layers resolve to now.
`SettingsApplication` is the CLI's `settingsApplication`, computed by the same
shared code: `Entries` with `Setting`, `Status`, `Reason`, `Value`,
`RequestedValue` and `NextAction`, plus `NotAppliedCount`. It is nil until the
resume step completes, so a dry run, an unchanged outcome or a refusal before
resume leaves it nil. Process relaunches fill these fields from the CLI JSON.

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

### Operator messages

`Client.SendOperatorMessage(ctx, projmux.OperatorMessageRequest{Operator:
core.NewMessageOperator("<client>"), Target: "uid:agent-…", Payload: "…"})` sends
operator coordination input to one Claude Agent: the operator origin
(`{"kind":"operator","client":"<client>"}`) and operator authority, no source
route and no reply. `core.MessageOperator` has an unexported client name, so
decoding request data (JSON, a form, a peer envelope or a `Source` string) never
produces one; its zero value names none and is refused. A client name outside
the operator client rule refuses with `CodeMessageInvalidInput` before anything
is read or written. The SDK does not authenticate the person: confirm who the
operator is before you attach `Operator`, as with `CreateAgentRequest.Operator`.

The send runs the same target resolution, capability and route refusals, frame
pre-check, acceptance, delivery or hold as a peer send, with errors spelled
`<client> message send`. `MessageRef`, `TTL`, the payload and frame limits and
`Options.ProjmuxExecutable` behave as for `SendMessage`. The result carries the
`core.MessageReceipt`, the rendered Claude push `ContentBytes`, and the `Action`
the receipt names: the held action of a held receipt, the failure action of a
terminal undelivered one, and empty otherwise. An error with a receipt
`MessageRef` means the message was accepted; never resend it, and read an
uncertain outcome with `MessageStatus`.

Operator input to any target but Claude is refused with the reason
`operator-origin-target-not-claude` before the target's route is resolved or
anything is written; a Codex Agent takes operator input as native turns instead.
`projmux.CodeOperatorOriginTargetNotClaude` is that reason and the refusal's
`Code`, and `projmux.ErrOperatorOriginTargetNotClaude` matches it under
`errors.Is`, including through wrapping. `SendMessage` never sends operator
input.

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

A Codex Agent whose earlier host move left an unfinished transfer behaves as in
`agent relaunch`: that transfer takes precedence over every relaunch, on tmux
and process sources alike. `DryRun` and every request other than the exact
recovery one refuse before any effect with the CLI's text and its recovery
command; the Code is `relaunch-request-invalid`, where the CLI exits with a
usage error. Checks that run before this one on the same path (`Detach` for a
process destination, Codex prompt and provider checks) and a failure of the
exact recovery itself keep their own Codes. The exact recovery request is
`Host: "tmux", Confirm: true` with no Model, Effort, Profile, Instructions, Reset
or Prompt. It runs the CLI recovery through `Options.ProjmuxExecutable` and
reports `Outcome` `resumed`, `CurrentHost` and `TargetHost` `tmux`, and the
recovered `NewPaneUID`.

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
preserve the existing profile refusal. `projmux.ReservedProfileName` is the
reserved name, `none`.

```go
if err := projmux.ValidateProfileName(name); err != nil {
    var refusal *projmux.Refusal
    if errors.As(err, &refusal) {
        fmt.Println(refusal.Code, refusal.Name)
    }
    return err
}
```

## Provider name normalization

`projmux.NormalizeProvider(provider string) string` maps a provider spelling to
its registered id (`codex`, `claude`, `antigravity`) the way Agent records are
classified: it ignores case and surrounding space and returns an empty string
for an empty or unknown provider. It needs no Client or context and reads no
environment, file, Registry or network.

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

## Resolve an actual Project label link

`projmux.ResolveProjectLabelLink(rules core.ProjectLinkRules, project core.Project,
key, value string) (string, bool)` uses CORE's resolver for actual label values:

```go
url, linked := projmux.ResolveProjectLabelLink(rules, project, "pr", "725")
```

It returns `(URL, true)` on success and `("", false)` for missing rules or
variables, empty values, or expansions CORE rejects. Label values and Project
UID/name/labels are path-escaped; URL placeholders lose trailing slashes.
The function is deterministic, leaves its inputs unchanged, and accesses no
files, environment, Registry, tmux or providers. It needs no Client, does not
load or validate stored rules, and does not open the URL. Existing link-rule
CRUD and result DTOs retain their behavior.

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

### Project link rules by UID

`Client.ReadProjectLinkRulesByUID(ctx, projectUID)` returns
`core.ProjectLinkRulesRead`: the stored `Rules` of the bare Project UID and the
`*core.FileIdentity` of the file they were read from. It reads no Registry and
does not check that the Project exists, so a missing or malformed Registry does
not affect it; `ReadProjectLinkRules` keeps its selector, Registry admission and
result. The file is read with the same bounded, validating, symlink-confined
read: a missing file or directory is the zero rules with a nil identity and no
error, and nothing is created, locked or repaired; a malformed, invalid,
unreadable, non-regular, escaping or oversized file is a
`CodeLinkRulesUnreadable` refusal whose `errors.Is` and `errors.As` reach the
read's cause. An empty, `uid:`-prefixed or otherwise non-UID value is a
`CodeSelectorInvalid` refusal.

`FileIdentity` (device, inode, size, modification time and change time) is set
only when the stats taken immediately before and after the read find the same
regular file; compare whole values. It is not a content digest and proves
nothing about the Project or permissions. Cancellation is checked on entry and
before returning success, and a read failure takes precedence over it. The
result belongs to the caller.

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

Early stale-proof refusals preserve the Registry, sidecars, claims and provider
state. They can still append an ordinary CLI error outcome to
`logs/operations.jsonl`: a spawned validation or resume owner can each record
zero or one outcome (at most two for a validate/resume pair). Local rejections
that spawn no owner produce no owner outcome. The journal is best-effort;
zero records cannot distinguish an interrupted owner from an unreported storage
failure. This does not make later failures or other error classes read-only.

The process CLI regression explicitly permits this operational write instead of
asserting that the entire state directory remains byte-for-byte unchanged.
It checks the returned owner identity and source binding, unchanged regular-file
identity and private mode, the original byte prefix and snapshot hashes, and
complete appended events with exact fields, types, version, backend, unique run
IDs and per-call time/count bounds. Only then does it update the expected hash
for that one log; every other path retains the full snapshot comparison. A
random run ID and matching time window do not directly prove an event's PID.
The timestamp/PID fallback ID must match the returned owner and call interval.
Earlier failures without retained events cannot be assigned the same cause
retrospectively. Product journaling and lifecycle guards remain unchanged.

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

## Stored Registry snapshot without observation

`Client.ReadRegistrySnapshot(ctx)` returns one caller-owned
`core.RegistrySnapshot` with the stored envelope (`APIVersion`,
`SchemaVersion`, `UpdatedAt`) and non-nil `Projects`, `ControlSessions`,
`Windows`, `Panes`, `Agents`, and `NameReservations` slices.
It reads the captured Registry once through the CLI's degraded read-only loader.
It preserves stored values, row order, duplicate UIDs, Offline Agents, and
nested data. It adds no runtime context or ownership resolution. Consumers
that follow owner references should use the first matching Project or Window
in these slices and process Agent rows directly, without deduplicating by UID.

```go
snapshot, err := client.ReadRegistrySnapshot(ctx)
if err != nil {
    return err
}
for _, agent := range snapshot.Agents {
    fmt.Println(agent.Metadata.UID, agent.Metadata.Name)
}
```

Pane rows keep their stored order. A Pane's runtime kind is its stored
`Spec.Runtime`; read it with `Spec.Runtime.EffectiveKind()`, which reports
`tmux` for legacy rows that stored no runtime. The stored value is not
rewritten.

This call performs no tmux observation, provider contact, locking, writing,
migration save, or permission repair. Its JSON has the stored Registry's keys
in the stored order, with every collection present; it exposes no Registry
path. The envelope, ControlSession and NameReservation fields were added after
the first release of this call, so its JSON now carries them as well. The
existing public resource values include their stored status fields, which are
returned unchanged and may be stale.
`ListResources`, `GetResource`, and `ObserveRuntime` retain their existing
observation behavior.

First use returns empty slices without creating state. Initialized missing or
empty state, malformed JSON, unsupported schema, and file read failures keep
the existing `CodeRegistryUnavailable` refusal and detail. Decodeable invalid
graphs remain readable. Cancellation is checked before captured path errors
and before returning success. A loader or projection failure wins over
cancellation during that work; cancellation does not interrupt file I/O.

### Reusing a held snapshot while the file is unchanged

`Client.ReadRegistrySnapshotCached(ctx, previous)` returns a
`core.RegistrySnapshotRead`: the `Snapshot`, the `RegistryIdentity` of the file
it was read from, and `Reused`. With a nil `previous`, or one whose identity is
nil or differs from the file's current stat, it stats the file, reads it once
exactly as `ReadRegistrySnapshot` does, and stats it again; the identity is set
only when both stats succeed and agree. When `previous.RegistryIdentity` equals
the current stat, the file is neither read nor decoded: `previous.Snapshot` is
returned as is, sharing its memory, with `Reused` set.

The SDK keeps no cache; the caller holds `previous` and decides how long to
keep it. Treat a held snapshot as read-only, and call `RegistrySnapshot.Clone()`
for an independent copy: Clone allocates every nested slice, map and pointer
anew and keeps nil and empty values, row order and values exactly. A snapshot
the caller changed is not re-validated against the file.

```go
var held *core.RegistrySnapshotRead
read, err := client.ReadRegistrySnapshotCached(ctx, held)
if err != nil {
    return err
}
held = &read // read.Reused reports that nothing was decoded
```

A first-use missing file reads empty with a nil identity, so it is read again
on every call. Registry failures are the `CodeRegistryUnavailable` refusal and
detail of `ReadRegistrySnapshot`, and `errors.Is` and `errors.As` reach the
loader's cause (for example a schema that is too new). Cancellation is checked
on entry and before returning success.

## One coherent resource graph and its Registry file identity

`Client.ObserveResourceGraph(ctx, projmux.ResourceGraphRequest{...})` returns
one `core.ResourceGraph` built from a single observation: it stats the Registry
file, reads the Registry once through the CLI's degraded read-only loader,
stats the file again, then observes the selected tmux server once and the
process hosts that Registry declares once, and resolves them with the graph
rules the CLI uses. `Graph` is a `core.ResourceGraphView` whose JSON is the
CLI's internal graph JSON byte for byte: Projects, ControlSessions, Windows,
Panes and Agents with their class, status and owner chain, every observed tmux
object with its attribution, conflicts, and the scopes that could not be read.
Kind-by-kind reads would join different Registry reads; this call never does.

`ResourceGraphRequest` routes exactly like `RuntimeRequest`: `SocketName`
corresponds to `--socket`, `SocketPath` must be absolute, both together are a
`CodeRuntimeRequestInvalid` refusal, and empty fields use an explicit
`Options.SocketPath`, otherwise no server. TMUX and the app/default socket are
never consulted. Without a server, or when the server cannot be read, the rows
tmux would judge have status `unknown` and `Graph.Unavailable` names the tmux
scope. A process host that cannot be read leaves its process Pane `unknown`
with no `Graph.Unavailable` entry. Neither is an error.

`RegistryIdentity` is the `core.RegistryFileIdentity` of the file the rows were
read from. It is set only when both stats succeed and agree; a replacement in
between, or a file that cannot be stat'ed, leaves it nil and the graph is still
returned. `Client.ReadRegistryFileIdentity(ctx)` stats the same file without
reading, locking, repairing it, or contacting tmux or providers, and for the
same file state returns the same value. Use it to decide cheaply whether a held
graph is still current; a missing file is a `CodeRegistryUnavailable` refusal,
never a zero identity.

```go
observed, err := client.ObserveResourceGraph(ctx, projmux.ResourceGraphRequest{SocketName: "projmux"})
if err != nil {
    return err
}
for _, pane := range observed.Graph.Panes {
    fmt.Println(pane.Pane.Metadata.UID, pane.Class, pane.Status)
}
if observed.RegistryIdentity != nil {
    current, err := client.ReadRegistryFileIdentity(ctx)
    if err == nil && current == *observed.RegistryIdentity {
        // The Registry has not been rewritten since this graph was read.
    }
}
```

A Pane node judged as a process child carries `Process`, a
`core.GraphProcessKey` with the host, Pane and generation of that child, and
its Agent node carries a copy. Tmux Panes carry nil. A key whose host could not
be resolved still names the Pane and generation; `Valid()` reports whether all
three are set. `Process` is excluded from JSON, so the graph JSON is unchanged.
The key is observation evidence only and grants no process admission, binding
or control.

### A graph from a held snapshot and process evidence

`Client.ObserveResourceGraphFromSnapshot(ctx, snapshot, projmux.ResourceGraphOverlayRequest{...})`
returns the graph `ObserveResourceGraph` would return for that Registry, from a
`core.RegistrySnapshot` and a `core.GraphProcessInventory` the caller already
holds. The Registry is not read or stat'ed and no process host is observed,
even when `Processes` is empty; only the selected tmux server is observed,
once. `SocketName` and `SocketPath` route exactly as in `ResourceGraphRequest`.

`GraphProcessInventory` keeps `Declared` process keys apart from `Observed`
`GraphProcessObservation` values, each a key and a `Status` of `live`,
`offline` or `unknown` (any other value counts as `unknown`; a failed host read
is `unknown`, never `offline`). A Pane stored with the process runtime is
always treated as declared. Without a matching declared key and observation
for its current generation its status is `unknown`, and Window and Project
status aggregate from it as they do in `ObserveResourceGraph`.

```go
graph, err := client.ObserveResourceGraphFromSnapshot(ctx, read.Snapshot, projmux.ResourceGraphOverlayRequest{
    SocketName:       "projmux",
    Processes:        processes, // core.GraphProcessInventory the caller holds
    RegistryIdentity: read.RegistryIdentity,
})
```

The result's `RegistryIdentity` is a copy of the request's: the caller's own
record of the file the snapshot came from, not a new stat and not proof that
the snapshot matches the file. Neither input is changed, and the result owns
its nested values. Cancellation is checked between the steps.

None of these calls writes tmux, the Registry, provider hosts, or the caller's
stderr. Process hosts are only observed by `ObserveResourceGraph`, within the
CLI's existing read budgets; no process admission, binding or control
authority is returned.
`ReadRegistrySnapshot`, `ListResources`, `GetResource`, and `ObserveRuntime`
keep their existing behavior.

### Prepared Registry snapshots and graphs from observed values

A caller that resolves graphs repeatedly can decode the Registry once and keep
the Registry read apart from the runtime observation:

```go
prepared, reuse, err := client.PrepareRegistrySnapshot(ctx, held) // held may be nil
if err != nil {
    return err // held is unchanged and still usable
}
held = prepared // reuse.Reused: nothing was read or decoded

inventory, err := client.ObserveGraphInventory(ctx, projmux.GraphObservationRequest{SocketName: "projmux"})
if err != nil {
    return err
}
graph, err := client.ResolveResourceGraphFromPrepared(ctx, prepared, projmux.PreparedGraphOverlay{
    Runtime:   inventory,
    Processes: processes, // core.GraphProcessInventory the caller holds
})
if err != nil {
    return err
}
contexts := projmux.NewResourceContextProjector(graph)
fmt.Println(contexts.For(core.KindWindow, windowUID).Value)
```

`Client.PrepareRegistrySnapshot(ctx, previous)` returns an immutable
`*PreparedRegistrySnapshot` and a `SnapshotReuse` with the file `Identity` and
`Reused`. When `previous` was prepared from this Client's state root and its
identity equals the file's current stat, `previous` itself is returned and the
Registry is neither read nor decoded. Otherwise the Registry is read once
between two stats, exactly as `ReadRegistrySnapshotCached` reads it: a nil or
foreign `previous`, a nil identity, a stat failure, a first-use missing file or
a rewrite between the stats never reuses. The state root is compared as a
value, so a symlinked state directory is a different root. A failure returns
no handle, keeps `ReadRegistrySnapshotCached`'s refusal and cause, and leaves
`previous` unchanged and usable. The SDK keeps no cache or watcher.

`PreparedRegistrySnapshot.Snapshot()` returns an independent clone of the rows,
equal to `ReadRegistrySnapshot` for the same file state; a caller may call it
once per handle and share it read-only. `IsVirtualWindow(windowUID)` applies
the Registry's exact eligible-anchor rule: the Window is virtual when that
anchor Pane is process-hosted, whatever the Agent phase or host liveness.

`Client.ObserveGraphInventory(ctx, projmux.GraphObservationRequest{...})`
observes the selected tmux server once and returns a
`core.GraphRuntimeInventory`: the transport, host mode, sessions, windows,
panes and unavailable scopes. It reads no Registry and observes no process
host. Routing is that of `RuntimeRequest`, including the
`CodeRuntimeRequestInvalid` refusals; an unreadable server is reported in
`Unavailable`, not as an error.

`Client.ResolveResourceGraphFromPrepared(ctx, prepared, overlay)` is a pure
computation: it reads and stats no file, observes no tmux server or process
host and reads no clock. The same handle and equal `Runtime` and `Processes`
values give an equal graph (order, empty slices, conflicts and process keys
included), so a caller can keep its last graph while the handle and the
observed values are unchanged and resolve only when they change. File identity
never stands in for runtime evidence. The graph rules, the `unknown` process
Panes and the graph JSON are those of `ObserveResourceGraphFromSnapshot`, and
the result's `RegistryIdentity` is a copy of the handle's. A nil or zero handle,
or one prepared by a Client of another state root, is a
`CodePreparedSnapshotInvalid` (`prepared-snapshot-invalid`) refusal. A handle
grants no authority and cannot be serialized.

`NewResourceContextProjector(graph)` indexes one graph and its `For(kind, uid)`
returns a `core.ResourceContext`: `Value`, `Source` and `Observed`, by the
CLI's precedence (a live Window name or Pane title only from a managed runtime
ref, then Agent topic or provider, command, or the Window fallback). An unknown
UID or kind is the zero value; it never falls back to the resource name. The
projector copies what it reads, so later changes to the graph do not reach it.
Contexts are presentation only; they are not admission evidence, and mutations
re-validate their own targets.

## Agent settings without relaunch admission

`Client.ReadAgentSettings(ctx, core.ReadAgentSettingsRequest{AgentUIDs: ...})`
returns, for each selected Agent, the settings its plain `agent relaunch` would
resolve, without admitting that relaunch. An empty `AgentUIDs` selects every
Agent in the Registry; otherwise it holds bare Agent UIDs, read once each, and
a UID the Registry does not hold yields an entry with `Unavailable`
`resource-not-found`. An empty or `uid:`-prefixed UID is a `CodeSelectorInvalid`
refusal. `Agents` are in UID order.

Each `core.AgentSettingsResolution` carries `Provider`, the `Host` the relaunch
launches on (`tmux`, or `process` for an Agent with exactly one process Pane),
and `Current`, `New` and `Reasons`: the same values `agent relaunch --dry-run
-o json` reports as `currentSettings`, `newSettings` and `relaunchReasons`. The
SDK builds the request with the code the CLI relaunch uses for that host,
including the agent guidance and the Project's label link rules (none for an
Agent that records the reply-only activation on tmux), and resolves it with
the same resolution. `core.AgentLaunchSettings` JSON is the CLI's
`currentSettings` JSON byte for byte, so `json.Marshal(resolution.Current)` can
stand in for it. Only Claude and Codex Agents are layered; others have
`Layered` false and no settings. When the settings cannot be resolved now and
the relaunch would be refused for it, `Unavailable` holds that refusal token
(`relaunch-no-conversation`, or the profile's own token) and the settings are
zero.

One call reads the Registry once, through the degraded read-only loader, and
never locks, repairs or writes it. It contacts no tmux server, process host or
provider, writes no instructions snapshot, and checks no phase, interaction,
conversation or Codex preflight: a Pending, busy or Running Agent is resolved
like any other, and a resolution does not say the relaunch would be admitted.
The agent guidance is planned at most once per provider and host and the label
link rules at most once per provider and Project; that planning writes what
every launch plan writes, their content-addressed snapshots and the guidance's
one-time first-use migration. The settings application of a relaunch and the
internal resolution fields are not returned.

```go
read, err := client.ReadAgentSettings(ctx, core.ReadAgentSettingsRequest{})
if err != nil {
    return err
}
for _, agent := range read.Agents {
    if !agent.Layered || agent.Unavailable != "" {
        continue
    }
    current, _ := json.Marshal(agent.Current) // the CLI's currentSettings JSON
    fmt.Println(agent.AgentUID, string(current), agent.Reasons)
}
```

### Display settings from a held snapshot

`Client.ReadAgentDisplaySettingsFromSnapshot(ctx, snapshot, request)` resolves
the same settings for the Agent rows of a `core.RegistrySnapshot` the caller
holds, without reading the Registry. `ReadAgentSettings` and its results,
including their JSON, are unchanged.

- An empty `AgentUIDs` selects every row; otherwise only rows with those bare
  UIDs, with request duplicates ignored and the same `CodeSelectorInvalid`
  refusal. Results follow snapshot row order and keep duplicate UID rows, each
  resolved from its own annotations. Requested UIDs without a row follow in UID
  order with `Unavailable` `resource-not-found`.
- A row's inputs are its UID, owner Window, provider (its declared one, else
  its conversation's), annotations and `PaneRef`; the host is `process` when
  that Pane is a process Pane, else `tmux`; its Project is the Project owning
  that Window, read by UID, name and labels. Nothing else in the snapshot is
  read: other rows, other Window fields, `UpdatedAt` and the remaining
  resources cannot change a result. Unselected rows and unknown UIDs plan and
  resolve nothing.
- The request is built with the code `ReadAgentSettings` uses. A Window or
  Project missing from the snapshot omits only the Project's prompt parts, and
  instructions that cannot be read now keep the settings; neither makes the row
  `Unavailable`. `Unavailable` is set only when the recorded profile cannot be
  resolved, with the same tokens, and for unknown UIDs.
- `Cacheable` (`json:"cacheable,omitempty"`) is true when the row was planned
  and resolved without an observed failure, and false when a prompt part could
  not be read, snapshotted or migrated, the instructions could not be read, or
  `Unavailable` is set. An off part, or one omitted for a missing Project, is
  not a failure. It describes this call only; do not cache a false result.
  `ReadAgentSettings` leaves it false.
- A result also depends on the files `SettingsInputPaths` names, the profiles
  and personas the annotations name, the built-in guidance of this build and
  the captured state directory. Planning writes what every launch plan writes,
  content-addressed snapshots and the guidance's one-time first-use migration,
  and shares the agent guidance per provider and host and the label link rules
  per provider and Project among the selected rows only.

```go
read, err := client.ReadAgentDisplaySettingsFromSnapshot(ctx, held.Snapshot, core.ReadAgentSettingsRequest{AgentUIDs: uids})
if err != nil {
    return err
}
for _, agent := range read.Agents {
    if agent.Cacheable {
        cache[agent.AgentUID] = agent
    }
}
```

### Complete recorded session history

`Client.ListRecordedSessions(ctx)` returns `core.RecordedSessionsList` with
`Sessions []core.AgentSession` and `CorruptLines int`. It reads the complete
history in the captured StateDir, including deleted Agents and missing Projects,
without reading the Registry or adding current sessions. Missing or corrupt
Registry state does not prevent this read. Agents follow their first appearance
in the file; each Agent's rows use CORE duplicate merging and stable time order.
Skipped nonempty lines are counted across the whole file. No provider filter,
pagination or arbitrary row limit is applied.

Missing history returns `sessions: []`. Path resolution and history read errors
are returned. A context canceled before admission reads nothing. Returned slices
and optional time pointers are caller-owned. This method writes no state, opens
no transcripts and contacts neither tmux nor the network; it does not aggregate
usage or change the existing Agent and Project session APIs.

### Agent persona attach and detach

`AttachAgentPersona(ctx, core.AttachAgentPersonaRequest{Ref: "uid:…", Persona: "reviewer"})`
and `DetachAgentPersona(ctx, core.DetachAgentPersonaRequest{Ref: "uid:…"})` share
`agent persona attach|detach` policy and execution. They support Claude tmux
Agents and keep the Agent identity and existing conversation. Both record the
instructions source as `attach` and turn the system prompt snapshot off.
`RelaunchAgent` continues to record the `relaunch` source.

Supply an exact UID or a name with explicit Project/Window scope. The SDK has no
ambient caller Pane; the CLI additionally refuses its own Pane. `SocketPath` is
an absolute tmux socket path; an empty value uses the Client's explicit route.
`DryRun` writes no snapshots or Registry/runtime state. `Confirm` permits
interrupting a busy or unknown interaction. Running Agents already using the
same persona digest and resolved settings return `unchanged`.

Always inspect `AgentPersonaResult` even when an error is returned. It preserves
the CLI plan projection and supplies `Notices` and `Recovery` for failed stops
and resumes. A failed stop restores the previous persona unless the old Pane is
known closed. Resume errors keep the new annotations, known new Pane and original cause.
Recovery suggests resume only for confirmed Offline with no known new Pane;
Running, unobserved or partial results require exact Agent/Pane/socket state inspection.
No operation is automatically retried. Errors support
`errors.As(err, &refusal)` with persona reason codes, existing selector/Registry
codes, and `process-relaunch-unsupported`. `CodePersonaProviderUnsupported` and
`CodePersonaUnavailable` carry the same values as the CLI's persona reasons. Cancellation before admission retains
`errors.Is`; after admission, stop and recovery complete independently of it.
Session authorization and consumer mutation locks remain with the caller.


### Save and apply statusbar resources

`client.SetStatusbarResources(value)` is an opt-in synchronous method with no
context argument. It accepts only trimmed exact `on` or `off`. Invalid input
returns `CodeConfigValueInvalid` before effects; unsupported builds return
`CodeStatusbarResourcesUnsupported` before resolving paths or saving.

```go
result, err := client.SetStatusbarResources("on")
if err != nil {
    // Saved can be true even when preparing or applying failed.
    fmt.Println(result.Saved, result.Prepared.Status, result.Live.Status)
}
```

`core.StatusbarResourcesApplyResult` contains `Mode`, `Saved`, `Prepared` and
`Live`. Each stage has string `Status` (`ok`, `skipped`, `failed`) and `Detail`.
A save failure returns `CodeStatusbarResourcesSaveFailed`. After a successful
save, preparation failure returns `CodeStatusbarResourcesPrepareFailed` with
Saved true and Live skipped; application failure returns
`CodeStatusbarResourcesApplyFailed` with Saved true, Prepared ok and Live failed.
The returned `*projmux.Refusal` keeps the original message and wraps the cause
for `errors.Is`, `errors.As` and `errors.Unwrap`, including the original
loader/route errors' existing wrapping limits.

The method uses captured ConfigDir/StateDir/HomeDir/DefaultShell and the explicit
absolute same-build `Options.ProjmuxExecutable`. A ConfigDir is the actual
settings directory, including an arbitrary basename. No XDG parent or executable
path is inferred. Missing or invalid helper input fails preparation **after**
saving. The setter never executes the helper itself; the complete generated
config embeds it. When tmux sources that config, it starts `<helper> window
record` once in the background with `run-shell -b`, and the same config's
`after-select-window` and `client-session-changed` hooks start it again later.
The setter neither waits for that command nor reports its result. Exact
generated bytes skip a rewrite on repeated calls.

An explicit SocketPath selects only that exact app-owned physical socket. With
no SocketPath, the method explicitly selects the logical app socket `projmux`.
It ignores ambient TMUX, TMUX_PANE and private anchors, refuses foreign or
standalone servers, and shares the existing server-generation guards and
sequence retirement before `source-file`. There is no socket fallback.

Once admitted, the operation finishes synchronously without cancellation.
Failure preserves completed file and tmux effects; there is no rollback or
multi-call transaction, and concurrent saves are not serialized. Existing
GetConfig/SetConfig keep their central-storage behavior and do not acquire
this apply contract.

### Read central statusbar defaults

`client.GetCentralStatusbarDefaults()` is an opt-in synchronous read with no
context argument, no error and no refusal. It returns the twelve central
visibility leaves of `core.ConfigStatusbarVisibility` as
`[]core.ConfigVisibilityState`, sorted by `Key`, in a new slice on every call.

```go
for _, leaf := range client.GetCentralStatusbarDefaults() {
    fmt.Println(leaf.Key, leaf.Effective, leaf.Source)
}
```

A valid stored value reads as Source `central` with `Saved` set. Without one
the leaf reads as Source `default` with its built-in value: `on`, except a
usage window takes its capability default, so Codex 5h is `off`. Unlike
`GetConfig(core.ConfigStatusbarVisibility)`, which returns an empty value and
the error, a ConfigDir that cannot be resolved and a central file that cannot
be read or parsed also read as those defaults. This is the fallback the
receiver-less app readers, and the web through them, have always used; the
error is not reported and no other value is normalized.

The method reads only `statusbar-defaults.json` in the captured ConfigDir. It
reads no TUI leaf or `web.toml`, writes nothing, samples nothing, does not
touch the Registry, tmux or providers, and observes no cancellation.
GetConfig and SetConfig are unchanged.

## Claude hook integration preview and apply

`Client.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{Remove: ...})` returns
the plan `projmux agent integrate claude` (with `--remove` when `Remove`) makes
for Claude Code's `.claude/settings.json` under the Client's `HomeDir`, and
writes nothing. The plan is the command's own: the same code reads the file,
finds the projmux-managed entries and builds `Next`, and the installed events
come from the embedded Claude hook catalog merged with the
`ai-hooks.d/claude.json` override in the Client's ConfigDir. `Path`, `Exists`,
`Current`, `Next`, `Changed`, `Conflict` and `Action` are the command's plan
and outcome text; `Base` is the lowercase hex SHA-256 of `Current`, or empty
when the file does not exist. A conflict the command would refuse is the
plan's `Conflict`, not an error. A file that cannot be read or parsed is a
`CodeClaudeSettingsUnreadable` (`claude-settings-unreadable`) refusal.

`Client.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove, Base})`
plans again and carries the plan out as the command does. It is refused with
`CodeClaudeSettingsChanged` (`claude-settings-changed`) when `Base` is not the
`Base` of the file it now reads (`Refusal.Reason == "since-plan"`), with `CodeClaudeHooksConflict`
(`claude-hooks-conflict`) when the plan has a `Conflict`, and with
`CodeClaudeSettingsUnreadable` as the preview is. A plan with nothing to change
writes nothing and returns `Changed` false. Otherwise the file is replaced with
the command's writer: atomically, keeping an existing file's mode and a
symbolic link's target, and refused with `CodeClaudeSettingsChanged` if the
file stopped holding the bytes it read before the replace
(`Refusal.Reason == "while-writing"`). Both refusals preserve the external edit;
classify them using `Code` and `Reason`, not the message or unwrapped cause.
The existing Code, Field, error text and wrapped-cause behavior are unchanged.
That check is a
re-read, not a lock; applies are serialized only within one process. The
command's dry-run diff lines and any localized text are not returned.

```go
plan, err := client.PreviewClaudeHooks(ctx, core.ClaudeHooksRequest{})
if err != nil {
    return err
}
if plan.Conflict != "" || !plan.Changed {
    fmt.Println(plan.Action, plan.Conflict)
    return nil
}
result, err := client.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Base: plan.Base})
var refusal *projmux.Refusal
if errors.As(err, &refusal) && refusal.Code == projmux.CodeClaudeSettingsChanged {
    switch refusal.Reason {
    case "since-plan":
        // The preview is stale: preview again before applying.
    case "while-writing":
        // The file changed during apply: inspect the external edit and re-preview.
    }
}
```
