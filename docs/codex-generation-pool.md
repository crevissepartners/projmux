# Codex app-server generation pool — Phase 0 contract

Phase 0 adds the identity, validation, qualification, immutable bundle, and
read-only planning contracts needed by a future bounded app-server pool. It
does not start or stop a product endpoint, change a current pointer, dial more
than the existing broker endpoint, or change Agent create/resume behavior.

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
