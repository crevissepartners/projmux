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
- `L2` is replaced for one role and reported for the rest. Replacing a file does
  not replace the image of a process already running it, so an install has to
  ask. `broker-runtime` is asked, through the drain this application already
  ships: a runtime whose own image has been unlinked drains at the next session
  that reaches it, refuses new work with `drain-required`, and closes when its
  last binding goes. Every other role is reported with the route that does
  replace it and is left alone — see *The L2 replacement policy* below. A
  replacement that has not finished within the drain cutoff is reported as
  `replacement-cutoff-reached` and is still not ended.
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
| `L2` | the whole-fleet vintage census, the newest `install-residue.jsonl` record whose own census observed anything, and the last `install-replacement.json` pass | end and relaunch the residual process through the routes this application already ships. The drain cutoff bounds the wait, and reaching it changes the row rather than the fleet, so a failed replacement leaves every process exactly where a successful one would have found it. |
| `L3` | generation-pool status, its qualification result, and the Running-versus-live-session census | **none once `draining` is entered without a qualification result** (measured 2026-09-07). Every other pool state has a handover route. |

**Enforcement.** `TestDoctorReplacementLayerVerdictsAreFixedByInputCombination`
fixes every input combination to its two verdicts and its token.
`TestDoctorReplacementUnknownVerdictsNameTheEvidenceGap` requires each `unknown`
to name the evidence that was missing.
`TestDoctorReplacementDarwinReportsUnsupportedPlatformForImageAndProcessLayers`
holds the platform branch.
`TestDoctorReplacementQualificationMissingMakesGenerationPoolNotRestorable`
holds the `L3` mapping. For the `L2` guarantee above,
`TestBrokerRuntimeDrainsWhenItsOwnImageWasReplaced` holds the vintage entry
condition and that a runtime on the installed image is not drained by it,
`TestReplacementRolePoliciesMatchTheContractDocument` and
`TestReplacementSessionClientIsNeverADrainTarget` hold the policy table, and
`TestInstallReplacementPassOutcomesAreFixedByFleetAndRequest` fixes every
outcome of the install pass.

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
`processes.residual`, `residual.role.*`, `residual.oldest-seconds`, and
`ledger.latest.residual` say how much, of what, how old, and whether the
reading was live or from the ledger. A `residual.role.*` key is emitted only
for a role that has at least one residual process, so the row states what an
install did not replace and never carries a zero.

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
| `L2` | `replacement-cutoff-reached` | `not-replaced` | `restorable` | a residual process has outlived the drain cutoff, so this replacement is not going to complete on its own |
| `L2` | `residual-processes-present` | `not-replaced` | `restorable` | a live child is still running the image from before the last install, and none of them is past the cutoff |
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

## Process role vocabulary

`L2` counts every running child of the installed executable, and it names them
by **route** — the internal command words of the argv, with `argv[0]` and
everything behind a bare `--` discarded. That is the whole classifier. No pid,
no path, and no argv word reaches a diagnostics surface from it; the role name
is a token from the closed list below, and the value beside it is a count.

The same vocabulary is used by `projmux doctor`'s `L2` row and by every
`install-residue.jsonl` record, so the two surfaces name the same processes with
the same words.

| Role | Route | Cardinality | What ending it costs |
| --- | --- | --- | --- |
| `broker-runtime` | `internal codex-broker serve` | one per machine | nothing directly: a new broker starts on the installed image when a binding needs one |
| `lifecycle-observer` | `internal agent-hook ingest codex-broker-watch` | one per Codex pane | that pane's lifecycle ingestion until it is relaunched |
| `supervisor` | `internal supervise` | one per pane | the pane it supervises |
| `session-client` | `shell` | one per attached client | **the operator's whole attached session.** This role is the reason a drain cannot be uniform: every other role is background work, and this one is the person at the terminal |
| `agent-endpoint` | `internal claude-endpoint-helper` | one per registered agent activation | that agent's messaging endpoint until its pane is recreated |
| `usage-watcher` | `internal status usage --watch-codex-rate-limits` | one per machine, lease-held | rate-limit sampling until the lease is taken again |
| `other` | every route not named above | unbounded | — |

**`other` is a total guard, not a count of replacement targets.** It exists so
that a route this census has no name for cannot make the fleet look smaller than
it is, and naming more roles shrinks it without ever removing it. What remains
in it is the **short-lived invocation**: a status or statusbar render, a preview,
a picker, a hook callback, a `config apply`, the residue census itself. Those
processes are observed by whichever census happens to overlap them and are gone
seconds later. They are counted because the total has to be whole, and they are
not drain candidates because nothing has to end them.

That distinction is what the named roles buy. Before them, `other` was the
second-largest bucket on this repository's own ledger — 271 of 866 residual
observations — and it mixed the two kinds, so no reader could tell a long-lived
process an install failed to replace from a two-second render that happened to
be running. A residual `other` count is now read as measurement noise unless it
is large or persistent, in which case it names a long-lived route this list is
missing.

**Adding a role is not a change to the reason-token vocabulary.** The reason
tokens above are the closed list of *verdicts*; roles are the subjects those
verdicts are counted over. A role added here reaches the signal key inventory
automatically, because that inventory expands the census role order, and
`TestReplacementProcessRolesMatchTheContractDocument` holds this table and the
code's role order to each other in both directions.

## The L2 replacement policy

An install may end a long-lived process only where this application already
ships a way to ask one to stand down, and only where ending it costs nothing an
operator did not choose. Both halves of that sentence are decisions taken from
measurement, and they are fixed here rather than at a call site.

### Per-role disposition

`drain` means the install asks through a shipped path and the process decides
when it actually goes. `report-only` means no install touches it; the route that
does replace it is named instead, so a reader of the row knows what would.

`TestReplacementRolePoliciesMatchTheContractDocument` holds this table and the
code's policy map equal, in both directions.

| Role | Disposition | Replacement route |
| --- | --- | --- |
| `broker-runtime` | `drain` | `broker-drain` |
| `lifecycle-observer` | `report-only` | `pane-relaunch` |
| `supervisor` | `report-only` | `pane-relaunch` |
| `session-client` | `report-only` | `operator-reattach` |
| `agent-endpoint` | `report-only` | `pane-relaunch` |
| `usage-watcher` | `report-only` | `lease-expiry` |
| `other` | `report-only` | `process-exit` |

**Exactly one role drains, and that is a fact about what exists rather than a
first instalment.** A drain is a protocol: the runtime has to be able to hear
the request, refuse new work without failing anonymously, and carry the work it
already accepted to its end. `broker-runtime` is the only role that ships one.
Every other role is replaced by an event that already happens — a pane
recreated, an activation restarted, a lease taken again — and an install that
signalled them would sever live work to save an operator an action they can take
themselves.

**`session-client` is the entry that is a decision.** It is the operator's own
attached tmux session, the longest-lived role on this repository's ledger at
169h29m, and a uniform cutoff applied to it ends the terminal the install was
typed into. It is report-only and no cutoff reaches it.
`TestReplacementSessionClientIsNeverADrainTarget` holds that as a property of
the table rather than of any call site.

**No disposition severs work.** The one hard kill this repository has measured —
2026-09-05, four panes — brought three of the four back automatically and left
the fourth wedged on `backlog-overflow`. A recovery asymmetry that large is what
a default has to be chosen against, so `drain` asks and waits, and the cutoff
below reports rather than escalates.

### The vintage trigger

The drain already had one entry condition: a client arrives speaking a protocol
version this runtime cannot negotiate. That condition misses the case an install
produces. A new binary usually speaks the same protocol, so a compatible
handshake proves nothing about which image is behind it.

The second entry condition is the runtime's own image. A runtime reads
`/proc/self/exe` once per arriving session until the answer is yes; the kernel's
`(deleted)` suffix says the file it is running was unlinked, which is exactly
what a publication leaves behind. The runtime then enters **the same drain by
the same door** — the same `drain-required` refusal, the same wire frames, the
same "live work is carried to its end". No new protocol, no new refusal token,
and nothing added to the wire.

Two consequences follow from putting the trigger there rather than in the
installer. Any client of the newly installed binary drains a superseded runtime,
so the npm install path is covered without a second implementation. And a
platform with no executable link to read declines to drain rather than draining
on a guess: `defaultProjmuxImageReplaced` answers false on darwin, and the
absence is stated by this table's `unsupported-platform` row.

### The install pass

`projmux internal install-replace` runs as a step of `make install`, immediately
before the residue census so the census measures the fleet the pass left. It
takes the census, splits the residual processes by disposition, dials the
published broker runtime for this state domain, waits a bounded moment, and
writes `install-replacement.json`.

It **starts nothing** — a replacement pass that launched what it was sent to
replace would leave more behind than it found, so it dials and never ensures.
It **signals nothing**: the only request it makes is a socket handshake. And it
never fails an install: it runs after the install has already succeeded, and
everything it could not do is on the record it writes.

Its outcome vocabulary is closed, and it reaches the `L2` row as
`replacement.outcome`:

| Outcome | Meaning |
| --- | --- |
| `replacement-unsupported-platform` | no process table to take a census from |
| `replacement-no-target` | no residual process sits in a drainable role |
| `replacement-complete` | the drain was asked for and finished inside the settle window |
| `replacement-drain-pending` | the drain was accepted and is still carrying work |
| `replacement-target-unreachable` | a residual target the shipped path could not reach; `replacement.refusal` says which door was closed |
| `replacement-not-attempted` | written by no pass — what the row says when no record exists at all |

### The drain cutoff

A drain that carries live work to its end has no bound of its own: the runtime
stays up while it still has bindings, and a binding that is never released keeps
it up forever. **The install has to decide when to stop calling such a
replacement "in progress" and start calling it unfinished.**

`projmux internal install-residue --survival` answers that from measurement.
On this repository's ledger at 2026-09-08 — 55 records, 884 residual
observations, **137 distinct processes** — the fraction of residual processes
still running at each cutoff is:

| Cutoff | Residual processes still running | Truncation rate |
| --- | --: | --: |
| 1h | 84 of 137 | 61.3% |
| 4h | 38 of 137 | 27.7% |
| 12h | 19 of 137 | 13.9% |
| **24h** | **13 of 137** | **9.5%** |
| 72h | 7 of 137 | 5.1% |
| 168h | 4 of 137 | 2.9% |

These are the **process-identity** numbers, not the observation numbers. The
section below says why the two bases disagree in both directions at once and why
a cutoff chosen from the observation basis would be wrong at both ends.

**Adopted: 24h. Truncation rate 9.5% — 13 of 137 by process identity.**

The reasoning is the shape of the curve against the cost of being wrong in
either direction. 1h and 4h report a majority and a large minority of ordinary
drains as failures, which turns the token into noise rather than a discriminant.
Past 24h the curve flattens: 24h to 72h buys 4.4 points and costs two more days
before an operator learns a replacement is never going to complete. 24h is also
the horizon the answer is read on — a residual process a day old is not a drain
still finishing its work, it is a drain that is not going to.

**The truncation rate is what gets reported, not what gets severed.** Reaching
the cutoff ends the install's claim about the replacement and never the process:
the `L2` row turns to `replacement-cutoff-reached`, `replacement.beyond-cutoff`
counts how many are past it, and every one of them keeps running. That is the
whole difference between this bound and a kill timer, and it is why 9.5% is a
reporting rate rather than a severed-session rate.

The cutoff is `PROJMUX_REPLACEMENT_CUTOFF`, a Go duration, and it exists so the
branch is reachable in a bounded test and in an isolated-fleet smoke. An unset,
unparsable, or non-positive value is the adopted 24h. **There is no value that
disables it**: an unbounded drain is the state this bound rules out, and offering
a spelling for it would put that state one environment variable away.

**Re-evaluation.** The identity basis rests on 137 processes of which 128 carry a
start reconstructed by subtraction, exact to one second. As records written with
a recorded start instant accumulate, that proportion falls and the tail of this
distribution sharpens. The cutoff is re-derived from the same command when it
does.

## Reading residual survival: observations are not processes

The ledger records one census per install, and installs on a development
machine land minutes apart. A long-lived process is therefore recorded again in
record after record, once per install it survived. **Counting ledger samples
weights each process by its own longevity, which is the very quantity being
measured.**

`projmux internal install-residue --survival` reads the accumulated ledger and
answers each cutoff `T` twice: over samples, and over distinct processes. On
this repository's ledger at 2026-09-08 (54 records with a non-empty census, 866
residual samples, 128 distinct processes) the two bases disagree by several
times:

| Cutoff | By observation | By process identity |
| --- | --: | --: |
| 1h | 72.2% | 60.9% |
| 4h | 53.3% | 25.8% |
| 12h | 49.0% | 13.3% |
| 24h | 40.9% | 8.6% |
| 72h | 24.6% | 3.9% |
| 168h | 0.3% | **2.3%** |

The disagreement runs in both directions and neither one is a correction of the
other. Below 72h the sample basis is inflated, because the processes that
survive many installs contribute many samples each. At 168h it is *deflated*,
because the handful of week-old processes are diluted across a sample count they
did not generate. **A drain cutoff chosen from the sample basis would be wrong
in both directions at once.**

Identity is the pair `(role, start instant)`. The ledger has no pid, path, or
argv to key on, and does not acquire one for this: a start instant is when
something began, not which process it was. Records written from this change on
carry the start instants directly. Older records carry only ages, from which a
start is reconstructed by subtraction; both the instant and the age are whole
truncated seconds, so a reconstructed start lands on the true second or the one
after it, and the report says how many of its identities rest on that
reconstruction.

## Signal key inventory

| Key | Layer | Value |
| --- | --- | --- |
| `platform.observable` | `L1`, `L2` | `true` / `false` |
| `image.link` | `L1` | `current` / `unlinked` / `unresolved` |
| `retained.previous-image` | `L1` | `none` |
| `processes.observed` | `L2` | counter |
| `processes.residual` | `L2` | counter |
| `residual.role.broker-runtime` | `L2` | counter |
| `residual.role.lifecycle-observer` | `L2` | counter |
| `residual.role.supervisor` | `L2` | counter |
| `residual.role.session-client` | `L2` | counter |
| `residual.role.agent-endpoint` | `L2` | counter |
| `residual.role.usage-watcher` | `L2` | counter |
| `residual.role.other` | `L2` | counter |
| `residual.oldest-seconds` | `L2` | counter |
| `ledger.records` | `L2` | counter |
| `ledger.latest.installer` | `L2` | install path token, or `unclassified` |
| `ledger.latest.observed` | `L2` | counter |
| `ledger.latest.residual` | `L2` | counter |
| `replacement.cutoff-seconds` | `L2` | counter |
| `replacement.beyond-cutoff` | `L2` | counter |
| `replacement.outcome` | `L2` | install replacement pass outcome token |
| `replacement.refusal` | `L2` | broker refusal token |
| `replacement.attempted` | `L2` | counter |
| `replacement.drained` | `L2` | counter |
| `replacement.reported` | `L2` | counter |
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
was found. `TestReplacementMeasurementPathEndsNoProcess` holds that as a
property of the source: the census, the residue ledger, the survival report, and
this table are checked to carry no process termination, signalling, or restart.

The replacement policy above is the one thing on this page that acts, and it
acts through a strictly narrower door. `TestReplacementPathEndsNoProcess` holds
the same guard over the policy table and the install pass: they carry no
termination, no signalling, and no restart either. **The whole of the action is
a socket handshake, and the runtime decides.** `L1` atomicity and the `L3`
qualification gate remain separate work, and no change on this path may alter
their behavior.
