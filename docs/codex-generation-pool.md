# Codex app-server generation pool — Phase 0–4 contract

Phase 0 adds the identity, validation, qualification, immutable bundle, and
read-only planning contracts needed by a future bounded app-server pool. It
does not start or stop a product endpoint, change a current pointer, dial more
than the existing broker endpoint, or change Agent create/resume behavior.

Phase 2 adds a dark, bounded endpoint runtime pool and a private generation
host. It still does not change a current pointer or Agent create/resume
routing: even an initialized, ready `Preparing` endpoint refuses fresh create
admission before a provider, Registry, tmux, or lifecycle write.

Phase 4 adds the explicit rolling operation that prepares one private
successor, commits admission-current at most once, and marks the old generation
Draining without moving its live Agents. It does not stop the old endpoint,
resume a successor thread, rewrite an Agent endpoint ref, relaunch a Pane,
retire a generation, release a bundle lease, or adopt a foreign runtime.

## Identity and schema

A native Codex Agent has two independent generation axes:

- `Pane.status.activation.generation` identifies one Pane child
  materialization. Existing lifecycle and termination guards keep this meaning.
- `Agent.status.sessionRef.codex.endpoint` identifies the Codex state domain and
  app-server generation that durably owns the thread.

The additive endpoint shape is:

```json
{
  "stateDomainID": "opaque-state-domain",
  "endpointGenerationID": "opaque-endpoint-generation"
}
```

The optional live activation authority is:

```json
{
  "stateDomainID": "opaque-state-domain",
  "endpointGenerationID": "opaque-endpoint-generation",
  "brokerRuntimeID": "opaque-broker-runtime",
  "connectionEpoch": 1,
  "bindingEpoch": 1
}
```

Phase 1 adds an optional durable projection input beside that endpoint. Planned
states carry the exact operation that authorized them; ordinary states carry
no operation:

```json
{
  "state": "recovering",
  "operation": {
    "id": "opaque-operation",
    "endpoint": {
      "stateDomainID": "opaque-state-domain",
      "endpointGenerationID": "opaque-endpoint-generation"
    }
  }
}
```

All five live authority dimensions must match. Connection and binding epochs
remain broker-local counters; equal numbers from another endpoint generation or
restarted broker are not authority. A registry written before these optional
fields existed decodes and re-encodes with them absent. Absence is
`legacy-generation-unavailable`, never an inference to the current endpoint.

## Bounded model and read-only plan

The v1 model permits at most two non-retired slots: one `current` and one
draining obligation (`draining` or `handover-pending`). Preparing, recovering,
and blocked candidates also consume a live slot. It rejects, among other
invalid states:

- two current generations;
- two draining/handover-pending generations;
- more than two non-retired generations;
- a live Agent obligation without an admission-current generation;
- obligations naming a missing or retired generation.

`codexgeneration.PlanUpgrade` is a pure value reduction. It has no Registry
writer, provider, tmux, process, or filesystem/current-pointer adapter. JSON and
text output include the exact Agent UID and endpoint-generation ID for every
blocker and an explicit zero counter for each mutation class. An exact-current
unmanaged endpoint is attach-only: the plan can report it, but cannot stop,
restart, kill, or adopt it.

## Shared-state qualification gate

The pool lane remains closed unless one exact old/new version pair passes all
of the following in a single isolated state domain:

1. different private sockets concurrently create and turn different threads;
2. both endpoints read and list the exact threads without duplicates;
3. both endpoints survive an observed crash/exit and restart barrier;
4. a successor resume is not invoked while the old generation owns the same
   thread;
5. after the exact old process stops, only the completed persisted thread is
   resumed and its exact completed-turn snapshot is observed;
6. cross-thread writes, store corruption, and ambient mutations remain zero;
7. copied `auth.json` and `config.toml` are shared only inside the private state
   domain at mode `0600` and no values enter the receipt.

The strict `codexgeneration.QualificationResult` persists only versions,
booleans, counters, a closed verdict, and a closed reason. If distinct-thread
concurrency or same-thread ownership is `no`—or any other required fact is
missing—`GateQualification` keeps Phase 2+ closed and selects
`single-endpoint-journaled-handover`. It never stores prompts, responses,
credentials, paths, sockets, or rollout content.

## Release bundle lease

`codexbundle` retains the complete executable set a generation needs:

- the `codex` binary in both server and TUI roles;
- `codex-code-mode-host`, bundled `rg`, and bundled `bwrap` as helpers.

The lease ID hashes the canonical manifest, which covers version, protocol
range, role set, relative path, file mode, size, and SHA-256 for every artifact.
Creation copies into a private staging directory, verifies it, and only then
atomically commits the content-addressed directory. It never writes a current
symlink. Missing roles, manifest/hash/size/mode drift, or an unsupported
protocol range refuse before a usable lease is returned. `Open` repeats the
verification before launch, so removing the mutable upstream release directory
does not affect a valid lease.

## Validation workflow

The deterministic suite is part of `make test`. The real-Codex test is opt-in
and requires exact 0.152.0 and 0.152.1 standalone executables plus a source
Codex home containing `auth.json` and `config.toml`:

```sh
smoke_root="$(mktemp -d /tmp/projmux-codex-generation-XXXXXX)"
bundle_root="$(mktemp -d /path/with/at-least-700MiB/projmux-codex-bundles-XXXXXX)"
env -u TMUX -u TMUX_PANE \
  PROJMUX_CODEX_GENERATION_SMOKE_ROOT="$smoke_root" \
  PROJMUX_CODEX_GENERATION_BUNDLE_SMOKE_ROOT="$bundle_root" \
  PROJMUX_CODEX_GENERATION_OLD="/absolute/path/to/0.152.0/bin/codex" \
  PROJMUX_CODEX_GENERATION_NEW="/absolute/path/to/0.152.1/bin/codex" \
  PROJMUX_CODEX_GENERATION_SOURCE_HOME="/absolute/path/to/source-codex-home" \
  go test ./internal/testutil/codexinstalled \
    -run '^TestInstalledIsolatedGenerationPoolQualification$' -count=1 -v
```

The root must be an empty child of the system temporary directory. The test
uses two root-contained sockets, performs semantic readiness/completion/exit
barriers rather than fixed sleeps, removes the leased source directories, and
deletes only the exact owned root after both children and sockets are gone.

The four existing empty-prompt canonical tests retain their current plain-CLI
expectation. Their migration receipt belongs to Phase 3, not this additive
model phase.

## Phase 1 lifecycle projection

`codexgeneration.ProjectLifecycle` is the only interaction-plus-generation
mapper. A dash below means that the exact tmux option is absent. Draining,
handover-pending, recovering, and blocked rows require an operation ref whose
endpoint exactly matches the durable generation input; without that marker the
tuple is empty rather than inferred from a process exit or version change.

| Effective interaction | Preparing | Current | Draining | Handover pending | Retired | Recovering | Blocked |
| --- | --- | --- | --- | --- | --- | --- | --- |
| unknown | `-/-/-` | `-/-/-` | `draining/draining/-` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |
| idle | `-/-/-` | `idle/-/-` | `draining/draining/-` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |
| in progress | `-/-/-` | `thinking/in_progress/busy` | `draining/in_progress/busy` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |
| approval required | `-/-/-` | `waiting/approval_required/reply` | `draining/approval_required/reply` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |
| input required | `-/-/-` | `waiting/input_required/reply` | `draining/input_required/reply` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |
| response complete | `-/-/-` | `waiting/response_complete/reply` | `draining/response_complete/reply` | `draining/handover_pending/-` | `-/-/-` | `recovering/recovering/-` | `blocked/blocked/-` |

Provider-neutral callers use the same interaction tuples as `Current` without
claiming generation authority. Explicit generation events additionally require
the exact durable Agent endpoint and the live activation authority tuple:

```text
stateDomainID + endpointGenerationID + brokerRuntimeID
  + connectionEpoch + bindingEpoch
```

The event endpoint, durable endpoint, stored durable state/operation, stored
activation authority, presented authority, and exact target runtime are
compared before either bounded writer runs. A provider event cannot authorize a
syntactically valid operation that differs from the stored operation. Local
epoch numbers are never compared outside the endpoint and broker-runtime
namespace.

| Owner | Fence | Target | Effect |
| --- | --- | --- | --- |
| owner | current | target | semantic effect |
| owner | current | sibling | zero write |
| owner | stale | target | zero write |
| owner | stale | sibling | zero write |
| foreign | current | target | zero write |
| foreign | current | sibling | zero write |
| foreign | stale | target | zero write |
| foreign | stale | sibling | zero write |

Every native semantic `Apply` owns the existing exact-Pane authority fence for
its complete Registry/queue/tmux write set, including the provider-neutral
lane. `SetAuthority` therefore cannot invalidate between an older Apply's
Registry and tmux halves. Generation-aware Apply additionally repeats its
composite comparison inside the Registry transaction and after the Registry
commit before presentation writes. The production resource
reconciler consumes the same durable state/operation and exact stored
activation fence; an unavailable fence is zero-write rather than a reason to
overwrite the planned tuple with legacy interaction state. Reconciliation
compares the desired tuple with the exact live options, so its second full pass
emits no writes.

Contract enforcement is split by invariant rather than duplicated by adapter:

- C-1 Generation-pinned routing: `TestRuntimeMutationEquivalenceTableIsClosed`,
  `TestRuntimeMutationClassesMatchDecisionKernel`,
  `TestRuntimeMutationCompositeFenceAndSiblingRecorder`, and
  `TestGenerationLifecycleSinkCompositeAuthorityHasZeroCrossWrites`, plus
  `TestGenerationLifecycleProductionReconcileRejectsForeignOrSiblingAuthorityWithZeroWrites`.
- C-5 Exact lifecycle projection and actionability:
  `TestGenerationLifecycleProjectionClosedTable`,
  `FuzzGenerationLifecycleProjectionMatchesClosedTable`,
  `TestPlannedGenerationProjectionRequiresExactDurableOperationRef`,
  `TestMarkerlessCrashAndVersionDriftRemainOrdinaryFailure`,
  `TestMarkerlessCrashAndVersionDriftRemainOrdinaryFailureThroughProductionReconcile`,
  `TestGenerationLifecycleProjectionReconcileWritesOnceThenZero`, and
  `TestGenerationLifecycleProjectionUsesIsolatedRealTmuxAndExactCleanup`.

### Mapping and authority test migration ledger

No canonical test symbol was deleted. Phase 0 endpoint-schema/authority tests,
the pure lifecycle reducer property, and the broker reconnect/C01 canaries each
retain a unique boundary. The one duplicate assertion family was merged in
place:

| Previous assertion or owner | Phase 1 action | Old mutant still detected | New mutant receipt / canonical owner |
| --- | --- | --- | --- |
| `agentTmuxProjection` interaction switch plus hard-coded `waiting`/badge expectations in `TestCodexSemanticDeliveryMatrix` | production switch replaced by the core mapper; policy test now consumes its tuple and owns only Notify/State only/Quiet overlay | quiet or state-only incorrectly retains actionability | wrong interaction or generation tuple fails `TestGenerationLifecycleProjectionClosedTable` and `FuzzGenerationLifecycleProjectionMatchesClosedTable` |
| `TestResourceReconcileProjectsAllAgentFieldsFromRegistryAuthority` | retained at the unique Registry effective-interaction/topic/offline adapter boundary | stale/offline options survive or a repeat writes | planned-state fixed point is owned by `TestGenerationLifecycleProjectionReconcileWritesOnceThenZero` and the isolated real-tmux test |
| test-only `planAuthorizedGenerationLifecycleProjection` | deleted; fake and real tests now execute `planResourceAgentProjections` with durable Registry lifecycle plus exact activation authority | a generation-aware sink writes a planned tuple, then ordinary production reconcile erases it through the legacy-only input | `TestGenerationLifecycleProjectionReconcileWritesOnceThenZero` fails both the production-input deletion mutant and a second-pass rewrite; the isolated real-tmux test owns the same boundary on an explicit returned physical socket |
| `TestCompositeAuthorityRejectsSameNumberCrossGenerationAndLegacyWithZeroWrites` | retained as the Phase 0 pure five-part schema fence | missing endpoint namespace authorizes a legacy ref | owner/foreign × current/stale × target/sibling closure is owned by `TestRuntimeMutationEquivalenceTableIsClosed`, `TestRuntimeMutationClassesMatchDecisionKernel`, and `TestRuntimeMutationCompositeFenceAndSiblingRecorder` |
| native hook authority and reconnect tests, including the C01 sentinel | retained at the provider-hook/control-plane ordering boundary; no generation expectation copied into them | hook writes during pending/invalidating, an older native Apply restores stale Pane semantics after disconnect, or a retired broker epoch writes | `TestNativeCodexHookAuthorityChangeAfterGuardCommitsZero` and `TestNativeSemanticApplyAndInvalidationShareExactPaneFence` close both hook and native-Apply split-write races; `TestGenerationLifecycleSinkCompositeAuthorityHasZeroCrossWrites` rejects reused epochs across generations and broker restarts before Registry or tmux writes |
| `FuzzCodexLifecycleReferenceModel` and its readable reducer regressions | retained unchanged as the pure provider-event reducer owner | duplicate, stale, foreign, or reordered provider operations mutate reducer state | generation presentation is a downstream mapper and is independently exhausted by the 6×7 table; no reducer transition was reimplemented |

The deletion count is zero test symbols, one obsolete test-only authorization
helper, and one merged duplicate mapping-expectation family. This is
intentional: removing any retained row
would delete a distinct schema, adapter, provider-ordering, or reducer mutant.
The new runtime inventory keeps `agent.presentation` as the sole typed mutation
surface and records all five live authority dimensions plus durable owner and
target runtime. Process pool/host, create routing, consumer notification/sidebar/
statusbar/reply behavior, badge rendering, reducer transitions, and Phase 2+
remain outside Phase 1.

## Phase 2 endpoint pool and private host

`codexbroker.GenerationPool` keys every managed endpoint by the canonical
`stateDomainID + endpointGenerationID` pair and enforces the Phase 0 two-slot
bound again at runtime construction. Each generation owns an independent
Broker, random broker-runtime ID, connection epoch, binding epoch, and
initialize/snapshot/reconnect/binding ledger. A sibling reconnect cannot touch
another generation's opener, fence, binding, or provider wire. A broker
restart closes a generation-local restart fence, restores the exact sorted
binding ledger, and issues a new broker-runtime ID; authority from the old
runtime writes zero even when its local epoch numbers repeat. The restart fence
also refuses a bind or second restart that raced the restore snapshot.

Thread routing is the exact endpoint plus exact thread ID. Presenting the same
thread ID under another state domain or generation returns the typed
`route-mismatch` refusal before the endpoint wire. `Preparing` readiness is a
separate fact from admission: Phase 2's `AdmitCreate` always returns
`admission-closed`, including after a snapshot proves the endpoint ready.

`codexgenerationhost` launches only from the Phase 0-qualified immutable
bundle layout. The required `codex`, `codex-code-mode-host`, bundled `rg`, and
bundled `bwrap` paths are a closed package-owned set, not caller-overridable
configuration. The content-addressed lease is re-opened and every manifest,
role, mode, size, and hash is revalidated before publication and again before
any lifecycle signal. The versioned socket must be directly below an existing
owner-private `0700` root and must use the exact endpoint-generation name;
ambient/default parents, symlinks, permissive roots, and occupied paths are
never repaired or changed.

Lifecycle authority is the full PID plus `Setsid` process-group ID, socket
device/inode/change-time, executable device/inode/change-time/mode/size/hash,
bundle ID, endpoint identity, and random endpoint-runtime ID proof. Change time
keeps replacement fail-closed even when a filesystem immediately reuses a
socket inode. That private app-server host proof feeds only an exact opener;
`codexbroker.GenerationPool` separately owns the broker-runtime ID and composite
connection/binding fence. The existing OS broker Host and hidden CLI remain
default-only and are not claimed as generation-enabled. Drift in any one axis,
an exited/reused PID, or bundle-helper drift
keeps stop/restart/kill effects at zero. Cleanup signals only the revalidated
private process group and waits for both the repeatable leader-exit channel and
EOF on an inherited session-lifetime token, including leader-first and
token-first exit orders, before removing the exact socket or letting the
installed smoke remove its caller-owned roots.
Readiness is driven by private-root filesystem events followed by an initialized
app-server handshake; neither readiness nor cleanup uses a fixed sleep. The
launch argv always names the leased executable and versioned private socket, so
mutable current/source removal cannot redirect it. Phase 2 exposes no successful
lease-release path: process exit and caller claims remain refused, and the later
journaled handover owner must establish terminal retirement. Phase 2 does not
delete bundle bytes.

The opt-in installed smoke uses only exact private `0.152.0` and `0.152.1`
leases and a unique empty root:

```sh
smoke_root="$(mktemp -d "${TMPDIR:-/tmp}/projmux-codex-host-XXXXXX")"
bundle_root="$(mktemp -d /var/tmp/projmux-codex-host-bundles-XXXXXX)"
env -u TMUX -u TMUX_PANE \
  PROJMUX_CODEX_GENERATION_HOST_SMOKE_ROOT="$smoke_root" \
  PROJMUX_CODEX_GENERATION_BUNDLE_SMOKE_ROOT="$bundle_root" \
  PROJMUX_CODEX_GENERATION_OLD=/absolute/0.152.0/bin/codex \
  PROJMUX_CODEX_GENERATION_NEW=/absolute/0.152.1/bin/codex \
  PROJMUX_CODEX_GENERATION_SOURCE_HOME=/absolute/private/source-home \
  go test ./internal/testutil/codexinstalled \
    -run '^TestInstalledPrivateGenerationHostDualListenerSmoke$' -count=1 -v
```

It requires inherited tmux routing to be absent, observes two initialized
private listeners and complete held leases, compares ambient/default socket
and PID-record identity before/after, waits for both exact private process
groups and their token-bearing descendants to exit, and removes only the exact
two private sockets, leases, and smoke root.

### Phase 2 migration and deletion ledger

No canonical test symbol was deleted or renamed. Existing default-endpoint
broker reconnect, binding, foreign-event, approval, runtime-loss, Phase 0
bundle/qualification, and Phase 1 composite-fence/projection tests retain their
unique owners. Phase 2 adds generation-local mutants instead of replacing
those canaries. Current-pointer/drain, Agent create/resume, foreign adoption,
Settings/background UX, derived consumers, and unrelated cleanup remain
excluded for their later phases.

## Phase 4 rolling admission and drain

`projmux agent app-server upgrade plan|apply --request <absolute-json>` and
`resume|abort --operation <ref>` are explicit-only operations over one
owner-private journal. The journal contains exact state-domain/generation and
operation identities, private routing material, monotonic receipts, and a
content-free Agent obligation ledger. It never persists prompt, response,
approval, transcript, credential, or tmux title content. Plan is read-only and
reports all Registry, provider, tmux, process, and current-pointer mutation
counts as zero.

Plan re-opens both complete leases read-only and binds the request to the Phase
0 tuple before any candidate start. The current and target bundle IDs, manifest
versions, server/TUI/helper inventory, and qualification old/new version pair
must agree exactly. Both sockets must be distinct direct children of distinct
owner-private roots; those roots must be physically non-overlapping with each
other, the shared state-domain path, and either immutable lease root. Current
and target must name the same canonical state-domain path. Any mismatch is a
mutation-zero refusal.

Apply durably records launch intent before starting the exact leased successor.
The operation journal lock spans the final authorization check and physical
supervisor start, so an abort fence either wins before launch or observes the
exact operation-owned durable intent. A coordinator crash after supervisor
launch but before launch-proof publication resumes the existing candidate; it
cannot start a duplicate. The supervisor captures the exact socket identity,
keeps a semantic guard for the child lifetime, and cleans only that identity.
An unknown or rebound socket is refused and never unlinked. CandidateReady and
in-flight pre-admission abort both clean the exact new candidate while retaining
the immutable bundle lease. If a crash lands between intent unlink and guard
unlink, a retry may remove only the exact regular mode-0600 guard after proving
that it is unlocked and that no socket exists; a locked guard, rebound path, or
present socket is left untouched and refused.

Before candidate-ready publication and again while the Registry admission
barrier is held, the complete server/TUI/helper lease is re-opened and verified.
The recorded TUI executable must equal the lease's single `RoleTUI` artifact;
an arbitrary absolute path or post-proof TUI drift leaves admission-current
unchanged. The current pointer and the old/new Draining/Current route transition
are one atomic journal commit relative to Agent create transactions. A create
therefore uses either the old Current route before the barrier or the new
Current route after it; it can never create on Draining. Retired receipt rows do
not consume a live slot, while Current+Draining closes a third upgrade with all
mutation surfaces at zero.

Existing live Agents remain pinned to the old generation for turn, reply,
approval, observation, and TUI routing. An Offline resume on Draining or
HandoverPending never takes the ordinary rebind path: it creates or reuses one
generation-wide HandoverPending operation ref and then refuses with
`handover-required`. An unconfigured requester fails closed before provider,
Registry, or tmux writes. The drain ledger classifies exact Agent UID plus old
generation as `active`, `approval-pending`, `no-turn`, `unknown`,
`completed-persisted`, or `closed`; the first four remain durable blockers.

Every Phase 4 receipt carries seven explicit zero counters:
`oldEndpointStop`, `successorResume`, `endpointRefCAS`, `paneRelaunch`,
`retirement`, `leaseRelease`, and `foreignAdoption`. Those effects, same-thread
successor resume, endpoint-ref CAS, Pane relaunch, retirement, and foreign
adoption remain Phase 5 work. The default installed Codex endpoint remains
unmanaged attach-only; an absent rolling journal preserves that read-only route
and no Phase 4 lifecycle path can stop, restart, kill, or adopt it.

The installed observation is opt-in and must use one unique empty temp root,
which it partitions into separate state-domain, generation, bundle, and XDG
roots, plus the existing old/new installed bundle inputs:

```sh
PROJMUX_CODEX_PHASE4_SMOKE_ROOT=/tmp/<unique-empty-root> \
PROJMUX_CODEX_PHASE4_PROJMUX=/absolute/installed/projmux \
PROJMUX_CODEX_GENERATION_OLD=/absolute/0.152.0/bin/codex \
PROJMUX_CODEX_GENERATION_NEW=/absolute/0.152.1/bin/codex \
PROJMUX_CODEX_GENERATION_SOURCE_HOME=/absolute/private/source-home \
go test ./internal/testutil/codexinstalled \
  -run '^TestInstalledPrivateRollingAdmissionReceipt$' -count=1 -v
```

It observes the ambient socket/PID record read-only, starts only fixture-owned
private process groups, checks the rendered seven-zero receipt and durable
candidate/admission/drain counters, re-observes the old Draining proof and
reads/lists its exact content-free thread, then creates and reads one
payload-free thread through the journal-selected new Current. It finally uses
the exact operation proof for semantic teardown. It never signals or adopts the
default endpoint.

### Phase 4 migration and deletion ledger

No canonical invariant test symbol is deleted or renamed. The canonical graph
baseline expands only for the four explicit upgrade leaves, and generated CLI
reference coverage remains owned by its existing exact-tree tests. Phase 0
qualification, Phase 1 lifecycle fencing, Phase 2 host/pool, and Phase 3
generation-pinned routing tests retain their unique boundaries. Phase 4 adds
model/fuzz, an exhaustive ten-coordinator-failpoint plus two-journal-hook restart
table whose four durable effects each converge exactly once, launch-intent
recovery, admission/abort race, Phase-0 tuple/root topology refusal, orphan-guard
recovery, TUI drift, two-slot, Draining/HandoverPending resume, direct old-route
continuity, and content-free receipt mutants rather than merging or deleting
those existing suites.
