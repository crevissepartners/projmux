# Replacement completion and restorability

Three layers of this application hold an execution image, and every consumer of
`make install`, `npm install`, or a provider generation upgrade believes one
sentence about all three at once. That sentence is false in a different way on
each layer. This document fixes what is guaranteed, what is not, and how a
report can be recovered from the tokens the diagnosis prints.

The layers are named `L1`, `L2`, and `L3` throughout, and they are the rows of
the `projmux doctor` replacement table:

| Layer | Subject | What holds an image here |
| --- | --- | --- |
| `L1` | `installed-executable-image` | the file the installed path publishes |
| `L2` | `long-lived-projmux-processes` | every running child of that file |
| `L3` | `provider-sessions-and-generations` | provider sessions and the managed generation pool |

`projmux doctor` renders the table on an unfiltered run and under
`projmux doctor --section replacement`. It is read-only in the same sense as
every other section: the Registry read is the zero-write snapshot read, the
process table is two file reads per process, and nothing on this path starts,
signals, stops, or repairs anything it reports.

## C-1 Replacement completion and restorability

**Assumption.** A consumer — an operator, an owning session, or CI — believes
that *from the moment `install` or `update` returns success, the old execution
image accepts no new work on any of the three layers*.

**Guarantee.** Per layer, what is actually true:

- `L1` publishes the new binary with a rename, so a process that starts after
  the rename gets the new image. The *install* around that rename is not
  atomic: config convergence runs before the rename and again after it, and a
  failure between the two leaves a published binary whose live config was never
  converged. There is no atomic install; there is one atomic step inside a
  non-atomic install.
- `L2` is not replaced at all. Replacing a file does not replace the image of a
  process already running it, so every long-lived child keeps executing the code
  it started with until it exits on its own. No install path ends a running
  child, and the operator is not told.
- `L3` can enter `draining` without a qualified version pair. Once there, the
  handover that would move its live obligations has no verdict to run under, and
  the pool has no route back.

**Non-Guarantee.** Explicitly outside this contract:

- Darwin observation of `L1` and `L2`. There is no `/proc/<pid>/exe`, so neither
  the image link nor the process census can be taken, and both rows report
  `unsupported-platform` with the `unknown` verdict on both axes. This contract
  does not build a substitute observation for that platform; it states the gap.
- The provider's own upstream seamless-upgrade contract. `L3` reasons about the
  pool this application manages, never about what the provider promises.
- An externally owned (`unmanaged`) app-server. Its state is not this
  application's to observe or restore.
- Automatic classification of which changes form one invariant. See C-2.

**Scope.** In space: one machine's projmux installation, the long-lived
processes it owns, and the generation pool it manages. In time: from the return
of one `install` or `update` to the next. **Expiry: a verdict expires when the
installed binary's SHA changes.** A table printed before an install describes an
installation that no longer exists.

**Failure and recovery.** Per layer:

| Layer | Detection signal | Recovery route |
| --- | --- | --- |
| `L1` | this reader's own executable link carries the kernel's `(deleted)` suffix | none for the binary. No copy of the replaced image is retained, and the recovery text the install prints on failure is config convergence, never a rollback. Restorability is `not-restorable` on every supported path. |
| `L2` | the whole-fleet vintage census, and the newest `install-residue.jsonl` record whose own census observed anything | end and relaunch the residual process through the routes this application already ships. The residual age distribution bounds the drain, so the route is finite. |
| `L3` | generation-pool status, its qualification result, and the Running-versus-live-session census | **none once `draining` is entered without a qualification result** (measured 2026-09-07). Every other pool state has a handover route. |

**Enforcement.** `TestDoctorReplacementLayerVerdictsAreFixedByInputCombination`
fixes every input combination to its two verdicts and its token.
`TestDoctorReplacementUnknownVerdictsNameTheEvidenceGap` requires each `unknown`
to name the evidence that was missing.
`TestDoctorReplacementDarwinReportsUnsupportedPlatformForImageAndProcessLayers`
holds the platform branch.
`TestDoctorReplacementQualificationMissingMakesGenerationPoolNotRestorable`
holds the `L3` mapping.

## C-2 The unit of replacement is an invariant, not a file

**When one invariant is held by code in several files, installing part of that
set is an atomicity violation.** The file boundary is not the unit; the
invariant is. A build that carries half of one invariant is not a smaller
version of the change — it is a state neither the old code nor the new code was
written to produce.

**The 2026-09-08 split install.** Removing a gate in
`claude_explicit_reply.go` and routing `PutReply` through the target-derived
adapter in `store.go` were one invariant. Only the first was installed. The
intermediate build wrote one Agent-message record with `adapter=codex-inbox`
against `target.provider=claude`, and the strict record validator then refused
the whole store: **46 records became unreadable, and `agent message send` failed
in every session on the machine.** Expiry and prune both run after a load, so
the store could not clear the bad record by itself — the failure was not
self-healing. The recovery was not deleting the file; it was rewriting the
mismatched `adapter` to the value the record's own target implies.

**And `L2` widens that window.** `make install` does not replace the image of a
running helper, so a process started from the intermediate build keeps writing
bad records after the source file has been corrected. That is exactly what
happened: the record was corrected once and reappeared. The three layers do not
fail independently — an `L1` partial publication is multiplied by the `L2`
processes still running the partial image.

**Non-Guarantee.** This contract does **not** decide automatically which change
set forms one invariant. There is no analysis here that groups files by the
invariants they hold, and none is implied. The contract says only that a partial
install of such a set is an atomicity violation, and that `L2` extends how long
that violation stays observable.

**Enforcement.** The `L2` row is what makes the widened window visible at all:
`TestDoctorReplacementLayerVerdictsAreFixedByInputCombination` fixes
`residual-processes-present` and `install-residue-recorded` to `not-replaced`,
so a report taken after an install states how much of the fleet that install did
not reach.

## C-3 A failure path preserves its discriminant

**Assumption.** A consumer believes that *when a refusal or failure is reported,
the reason token arrives together with enough of the underlying evidence to
identify which case produced it*.

**Guarantee.** This is a **partial** guarantee today, and points where the
underlying evidence is discarded are known. On the surface this document owns,
the guarantee is total: **every row carries at least one discriminant, and a row
with a token and no discriminant is a contract violation**.

**Non-Guarantee.** Exposing the *whole* underlying text on a user-facing
surface. What is required is a **recoverable discriminant**, not a dump, and
this contract does not dictate a logging surface.

**The 2026-09-08 case.** The only symptom was the single line
`malformed Agent message store`. It named neither the offending record nor the
condition that rejected it. A peer session had to re-implement the record
validator and walk all 46 records to identify the one. The `status` route took
the same load, so the fallback diagnosis path was dead too. **Failing closed was
correct. A fail-closed with no discriminant, combined with a deadlock, is
indistinguishable from unrecoverable.**

**How this surface preserves the discriminant.** Each row carries a `signals`
list of `key=value` pairs beside its reason token. The keys come from a closed
inventory and the values are either counters this surface formats or tokens
drawn from another closed vocabulary. `residual-processes-present` and
`install-residue-recorded` both say an install left work behind; only
`processes.residual`, `residual.oldest-seconds`, and `ledger.latest.residual`
say how much, how old, and whether the reading was live or from the ledger.

**Where the discriminant is published, and why.** The signals are serialized in
`--json`, unlike `doctorFinding.Details`, which is `json:"-"` in every format.
That difference is deliberate and rests on one fact: a stored Registry refusal
reason quotes absolute paths, so it cannot be serialized without the support
report depending on a redaction allowlist. **No signal value here is free-form.**
Every value is a counter or a closed token, a regular expression rejects
anything else at construction, and a value that fails it is replaced with
`unclassified` rather than serialized. No path, argv word, pid, Agent UID, Pane
UID, thread id, or prose sentence can reach the JSON on this path. Because the
discriminant is closed, it needs no terminal-only channel, and putting it in
JSON is what lets CI and a support report recover a verdict at all.

**Registry `Running` is not a provider session.** `Running` is a statement about
a Pane's managed activation. It is not a statement about the provider session
behind it: a Pane can be present in the runtime while the session it was bound
to is gone, and the Registry spells both `Running`. The discriminant differs by
provider and that difference is structural — a Claude Pane carries a real
process handle at `status.activation.claude.process`, so liveness is a direct
question about a pid; a Codex Pane carries no pid at all, only a thread and a
composite authority, so the only checkable fact is whether that authority still
belongs to the runtime currently serving the state domain. The census therefore
reports three counts — `sessions.live`, `sessions.dead`, `sessions.unobservable`
— and the third is not a rounding bucket: it is the count of Running Agents
resting on the Registry alone.

The census is **count-only**. It names no Agent and no Pane, for the reason the
managed-authority census states: a diagnostics surface that names Agents is one
that cannot be shared. It also **repairs nothing**. A mismatch is reported and
left exactly as it was found.

**Enforcement.**
`TestDoctorReplacementEveryReasonTokenCarriesARecoverableDiscriminant` requires
zero rows with a token and no discriminant across every reachable input
combination. `TestDoctorReplacementSignalValuesCarryNoPathProcessOrFreeFormText`
holds the serialization rule, and
`TestDoctorReplacementSignalKeysMatchTheContractDocument` holds the key
inventory equal to the one published below.
`TestDoctorReplacementCensusSeparatesRegistryRunningFromLiveProviderSession`
fixes the Running-versus-live-session split against a fixture.
`TestDoctorRendersReplacementTableInDefaultRunAndSectionFilter` holds the output
contract for the unfiltered run, the section filter, and the JSON surface, and
`TestDoctorReplacementSectionCreatesNothing` holds the read-only claim by
running the section against an empty home and requiring it to stay empty.

## Reason token vocabulary

Exactly one token governs each row: the first entry of its layer's list below
that the evidence selects. Everything else the layer observed stays in that
row's signals, so a fact that loses the precedence contest is never lost from
the report. The lists are in precedence order.

`TestDoctorReplacementReasonTokensMatchTheContractDocument` holds this table and
the token constants in the code equal, in both directions.

| Layer | Token | Replacement | Restoration | Selected when |
| --- | --- | --- | --- | --- |
| `L1` | `unsupported-platform` | `unknown` | `unknown` | the platform exposes no executable link |
| `L1` | `image-unresolved` | `unknown` | `unknown` | this reader's own image could not be resolved back to its executable path |
| `L1` | `image-unlinked` | `not-replaced` | `not-restorable` | this reader's image was unlinked out from under it, so the diagnosis is taken from a superseded build |
| `L1` | `image-current` | `replaced` | `not-restorable` | the installed path still publishes the image this diagnosis runs |
| `L2` | `unsupported-platform` | `unknown` | `unknown` | the platform exposes no process table |
| `L2` | `residual-processes-present` | `not-replaced` | `restorable` | a live child is still running the image from before the last install |
| `L2` | `no-residual-processes` | `replaced` | `restorable` | live children were observed and every one runs the installed image |
| `L2` | `install-residue-recorded` | `not-replaced` | `restorable` | no live child was observable, and the newest ledger record whose own census observed anything says that install left residue |
| `L2` | `install-residue-clean` | `replaced` | `restorable` | no live child was observable, and the newest ledger record whose own census observed anything says that install left none |
| `L2` | `no-observed-processes` | `unknown` | `unknown` | neither the live census nor any ledger record observed anything. Every reading is silent; none says the fleet is clean |
| `L3` | `generation-pool-unobserved` | `unknown` | `unknown` | no pool diagnosis was read |
| `L3` | `registry-running-provider-session-dead` | pool-derived | `not-restorable` | provider evidence contradicts a Registry `Running` Agent |
| `L3` | `qualification-missing` | pool-derived | `not-restorable` | the pool is installed and carries no qualification result |
| `L3` | `generation-pool-blocked` | pool-derived | `not-restorable` | the pool diagnosis is blocked |
| `L3` | `generation-handover-required` | pool-derived | `restorable` | the pool has a pending operation with a handover route |
| `L3` | `registry-running-session-unobserved` | pool-derived | `unknown` | Running Agents rest on the Registry alone, with no provider handle to check |
| `L3` | `generation-pool-not-installed` | `not-replaced` | `unknown` | no generation journal exists. Absence of a journal is absence of evidence about a restore route, not evidence of one |
| `L3` | `generation-pool-ready` | pool-derived | `restorable` | the pool is installed, qualified, and settled |

`pool-derived` means the replacement axis follows the pool rather than the
token: `replaced` once a generation is draining, handover-pending, or retired —
because such a generation accepts no new admission, which is exactly the
sentence C-1's Assumption makes — and `not-replaced` otherwise.

## Signal key inventory

| Key | Layer | Value |
| --- | --- | --- |
| `platform.observable` | `L1`, `L2` | `true` / `false` |
| `image.link` | `L1` | `current` / `unlinked` / `unresolved` |
| `retained.previous-image` | `L1` | `none` |
| `processes.observed` | `L2` | counter |
| `processes.residual` | `L2` | counter |
| `residual.oldest-seconds` | `L2` | counter |
| `ledger.records` | `L2` | counter |
| `ledger.latest.installer` | `L2` | install path token, or `unclassified` |
| `ledger.latest.observed` | `L2` | counter |
| `ledger.latest.residual` | `L2` | counter |
| `registry.observed` | `L3` | `true` / `false` |
| `pool.status` | `L3` | pool status token, or `unobserved` when no pool diagnosis was read |
| `pool.reason` | `L3` | pool reason token |
| `pool.action` | `L3` | pool action token |
| `pool.generations.live` | `L3` | counter |
| `pool.generations.draining` | `L3` | counter |
| `qualification.verdict` | `L3` | qualification verdict token |
| `qualification.reason` | `L3` | qualification reason token |
| `sessions.running` | `L3` | counter |
| `sessions.live` | `L3` | counter |
| `sessions.dead` | `L3` | counter |
| `sessions.unobservable` | `L3` | counter |

## What this surface does not do

It does not replace anything, restore anything, end a process, start a process,
or repair a Registry. It reports, and a reported mismatch is left exactly as it
was found. The three implementation tracks that change actual replacement
behavior — `L2` replacement, `L1` atomicity, and the `L3` qualification gate —
are separate work, and no change on this path may alter their behavior.
