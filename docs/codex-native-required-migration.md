# Codex Native-Required Create and Payload-Free Fallback

## Current payload-free behavior (Codex 0.153.0)

`projmux create codex` and `projmux create agent --provider codex` with no
payload now open one managed plain-interactive Pane and return one Running
Agent for each exact target. The same pre-provider decision is used by the AI
picker's default launch intent. Projmux does not call app-server `Current` or
`Resolve`, start a thread or turn, or enter a durable-resume barrier for this
shape. The current Codex 0.153.0 tuple cannot durably hand a zero-turn thread
to an independent TUI, so a native attempt cannot be a prerequisite for a
usable payload-free create.

This automatic fallback uses the same provider argv and output as the existing
payload-free `--interactive-only` lane. It is distinguishable only through the
content-free lifecycle declaration `payload-free-fallback`: `describe agent`
shows it as `LifecycleDeclared`, and Doctor reports the aggregate
`payload_free_fallback` count. No prompt, hidden turn, transcript content, or
provider response is stored to produce that signal.

The Phase-7 zero-turn durable-readiness failure is retained only as historical
negative safety evidence. A typed Failed Agent with no Pane is not functional
create success. Prompted native create, app-server picker resume, existing
Agent resume, and generation-pinned routes keep their native contracts.

## Native-required prompted create (0.14.0)

0.14.0 makes native authority a requirement for a *prompted* managed Codex
create instead of something projmux attempts and silently gives up on. Where an
earlier release would quietly hand back a plain-CLI Codex Agent — one that looks
managed, carries no app-server thread binding, and answers no native turn
control — the create now refuses at the provider-mutation boundary and names the
one explicit way to ask for that plain Agent.

This note covers only that change. For update mechanics by installer type see
[Upgrading](upgrading.md); for endpoint readiness diagnosis see
[Troubleshooting](troubleshooting.md#codex-app-server-install-topology).

## There is nothing to migrate

Upgrading is the whole migration. Every existing resource keeps working as it
is:

| Surface | Change in 0.14.0 |
| --- | --- |
| Registry schema | none — `internal/core/metadata` is byte-identical across the release range |
| Existing Agents and Panes | none — no stored field is added, read differently, or backfilled |
| Configuration files | none — no key added, renamed, or removed |
| Post-create and Agent hook contract | none — no `PROJMUX_*` env var added, renamed, or removed, and no payload schema change |

No command has to be run before or after the upgrade for this change. What
changes is the answer a *new* prompted Codex create gives when native authority
cannot be proven.

## The shape that is now gated

Exactly one create shape is native-required. All five conditions must hold:

- provider is `codex`, and
- the payload is exactly one operand, and
- that operand is non-empty, and
- `--interactive-only` is not passed, and
- the create is not carrying a capability selection from the split-UI picker.

The payload-free exception above is selected before this gate. The closed
outcome table lives in `internal/app/codex_native_thread.go`
(`codexNativeLaunchOutcomeTable`) and is pinned by
`TestCodexNativeLaunchOutcomeTableIsClosed`.

## Calls that now refuse

### 1. Prompted create against an endpoint that is not ready or not attachable

```sh
projmux create codex -- "review the diff"
projmux create agent --provider codex -- "review the diff"
```

Before: native thread preparation failed as a safe fallback and the create
silently continued on the plain CLI lane, producing a managed Agent with no
native binding.

Now: the create refuses before the split, before the hook probe, and before the
Registry commit. Zero threads, zero Panes, zero Registry writes, zero tmux
objects. The refusal carries the typed reason from the endpoint (for example
`daemon-not-running`), names `--interactive-only`, reports only the observed
install-capability facts, and links to the
[official Codex CLI capability guidance](https://learn.chatgpt.com/docs/codex/cli).
Exit code 1. Doctor and Settings render that same typed guidance authority;
they do not maintain separate installer wording.

Fix it by making the app-server endpoint available — start with
`projmux doctor --section integrations --verbose` — or ask for the plain lane on
purpose with `--interactive-only`.

### 2. Prompted create with `--add-dir` against an endpoint that cannot negotiate roots

```sh
projmux create codex --add-dir /path/to/other-root -- "review the diff"
```

Additional writable roots travel on the upstream experimental API. Before, the
create connection never negotiated that capability, so roots always failed the
request; that failure was classified as a safe fallback and the create silently
dropped to the plain CLI lane.

Now a rooted create negotiates the capability on its own connection and delivers
the exact cleaned list. An endpoint that cannot answer the negotiated form
fails closed with reason `additional-writable-roots-unsupported` rather than
creating an Agent whose writable workspace is narrower than what was asked for.
Exit code 1.

A create with no `--add-dir` keeps the plain, non-negotiated connection exactly
as before.

### 3. Prompted create whose selector resolves several Windows

```sh
projmux create codex --window main --window side -- "review the diff"
```

One create owns exactly one native thread, and a Registry rollback cannot delete
an app-server thread, so a prompted native fan-out has no atomic shape. Before,
every target dropped to the plain CLI lane. Now the fan-out is refused before
the first allocation, as a usage error (exit code 2) naming how many Windows the
selector resolved.

Narrow the selector to one Window, or pass `--interactive-only` to keep the
previous one-Agent-per-Window cardinality.

### 4. Resume picker: selecting an app-server-sourced Codex row

This is the split-UI resume picker (`Alt-7` / the resume selection surface), not
the `projmux agent resume` command.

When the picker's Codex rows come from a healthy app-server, selecting one of
those native rows resumes that exact thread through the native lane. Before, a
failed native resume preparation silently rebound the selection onto the rollout
CLI lane — it reported a resume while answering no native turn control. Now it
refuses. Exit code 1.

There is no `--interactive-only` escape hatch here: the operator picked an
existing conversation, not a launch mode. Rows that came from the rollout scan
rather than the app-server are unaffected and keep the current CLI lane.

## The escape hatch: `--interactive-only`

`--interactive-only` remains the only public spelling that explicitly asks for
a plain interactive Codex Agent with no native thread binding. Payload-free
fresh create now chooses that existing lane automatically, without changing the
flag's spelling or prompted-create meaning.

```sh
projmux create codex --interactive-only -- "interactive task"
projmux create agent --provider codex --interactive-only -- "interactive task"
```

- Both spellings are equivalent: identical flag acceptance, identical manifest
  and rendered help, byte-equal stdout.
- The native controller is not consulted at all. No thread is created, no native
  Pane state is bound, and the Agent keeps the existing hook activation contract
  (`provider-hook`).
- The payload stays the provider's initial task on the CLI argv, exactly as
  before.
- It gives up native turn control for that Agent — start, steer, interrupt, and
  approval routing through the app-server. That is a deliberate reduced
  capability, not a defect.
- It is Codex-only. Passing it to `--provider claude` or `--provider antigravity`,
  or to the `create claude` / `create antigravity` shortcuts, is a usage error
  (exit code 2) raised before any transaction opens, so nothing is created.
- It is a create-time flag and is not stored on the Agent. `projmux agent resume`
  has no way to tell an interactive-only Agent apart later and does not treat one
  specially.

## What did not change

| Surface | Behavior |
| --- | --- |
| `projmux agent resume` | unchanged. A stored Agent whose native resume cannot be proven keeps its existing safe fallback to one provider resume of the stored conversation. `internal/app/agent_resume.go` has a net diff of zero lines in this release. |
| Payload-free Codex create (`projmux create codex` with no payload) | pre-provider plain fallback: one usable managed Pane and one Running Agent per exact target, with zero native thread/turn/resume-barrier calls and the content-free `payload-free-fallback` declaration. |
| Multi-operand payload (`projmux create codex -- a b`) | unchanged refusal. Native `turn/start` accepts one text item; Projmux does not join operands or silently reinterpret them as a plain fallback. |
| Claude and Antigravity | unchanged lifecycle, fan-out, and hook activation contract. |
| Public hook env and payload schema | unchanged. |
| Post-thread-creation failures | unchanged. A native failure *after* `thread/start` returned still refuses without offering any second lane — including `--interactive-only` — because starting another Codex process could submit the same prompt twice. |

## Verifying this yourself

```sh
go test ./internal/app/ -run 'TestCodexCreatePayloadCardinalityInteractiveOnlyAndReadinessOutcomeTable|TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane|TestPayloadFreeCodexPlainLaunchFailureRollsBackWithoutProviderMutation|TestEmptyPromptCodexSplitProducersKeepOnePlainCLILane|TestPromptedNativeCodexCreateIssuesOneTurnAndNeverRepeatsThePromptInPaneArgv|TestNativeResumePicker|TestUnavailableNativeResume|TestClaudeAndAntigravity|TestCodexNativeLaunchOutcomeTableIsClosed'
go test ./internal/integrations/agents/codexappserver/ -run TestStartDefaultThread
```

| Claim in this note | Test |
| --- | --- |
| Prompted create refuses instead of silently creating a plain Agent, for both the unavailable-endpoint and unsupported-roots rows | `TestUnavailableNativeCreateRefusesInsteadOfSilentlyCreatingAPlainAgent` |
| Roots are delivered exactly on a negotiated connection, fail closed otherwise, and stay off the wire when empty | `TestStartDefaultThreadDeliversAdditionalRootsOrFailsClosed` |
| Prompted fan-out refuses with zero mutations; `--interactive-only` keeps the old cardinality | `TestDefaultNativeCodexFanOutRefusesWithZeroMutationsAndInteractiveOnlyKeepsCardinality` |
| Picker resume refuses instead of rebinding onto the rollout lane, and offers no launch-mode escape hatch | `TestUnavailableNativePickerResumeRefusesInsteadOfRebindingOntoTheRolloutLane` |
| `agent resume` keeps its safe fallback to one provider resume | `TestUnavailableNativeResumeKeepsTheStoredConversationOnTheProviderResumeLane` |
| Both `--interactive-only` spellings are equivalent, and non-Codex providers refuse it at zero transactions | `TestInteractiveOnlyIsTheOnlyPlainCodexLaneAndBothSpellingsAreEquivalent` |
| Payload cardinality × `--interactive-only` × readiness stays a closed pre-provider decision table | `TestCodexCreatePayloadCardinalityInteractiveOnlyAndReadinessOutcomeTable` |
| Canonical and shortcut payload-free create are byte/argv-equivalent to the explicit plain lane and touch no provider route | `TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane` |
| Saved-default, provider-picker, and direct-provider AI intents produce one managed plain lane without native mutation | `TestEmptyPromptCodexSplitProducersKeepOnePlainCLILane` |
| A failed plain launch rolls Registry and tmux back and never tries a provider lane | `TestPayloadFreeCodexPlainLaunchFailureRollsBackWithoutProviderMutation` |
| The installed outcome requires a Running plain Agent/Pane, no session ref, zero provider-thread delta, diagnostic signals, isolated socket cleanup, and ambient mutation zero | `TestInstalledPayloadFreePlainFallbackOutcomeSmoke` |
| Phase-7 readiness failure remains negative safety evidence rather than functional create success | `TestPhase7PayloadFreeReadinessFailureIsNegativeSafetyEvidenceOnly`, `TestInstalledPayloadFreeCreateOutputClassificationRequiresFunctionalPlainSuccess` |
| Claude and Antigravity are unchanged | `TestClaudeAndAntigravityLifecycleAndHookContractAreUnchangedByTheNativeGate` |
| One payload sends exactly one `turn/start` and never repeats the prompt in Pane argv | `TestPromptedNativeCodexCreateIssuesOneTurnAndNeverRepeatsThePromptInPaneArgv` |
| The post-mutation row still refuses a second lane | `TestIndeterminateNativeCreateRefusesASecondLaneAndWritesZero` |
| The outcome table describes exactly these rows and no others | `TestCodexNativeLaunchOutcomeTableIsClosed` |
| Managed-ready/external-only/CLI-missing/unknown guidance states only observed facts and all three consumers render the same authority | `TestCodexInstallCapabilityGuidanceMatrixStatesOnlyObservedFacts`, `TestCodexInstallCapabilityGuidanceHasThreeConsumerParity`, `TestCodexInstallCapabilityConsumersCarryNoSurfaceLocalCopyOrURL` |
