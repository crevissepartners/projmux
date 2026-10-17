# Testing

The local validation contract is exposed through make targets so CI, agents,
and humans run the same entrypoints.

## Darwin CI

Two independent `macos-15` children retain the original commands, selectors,
packages, CGO settings, race detector, counts and timeouts. The stable
`Darwin Native Key Adapter` check requires every child to succeed, including
when a child fails, is cancelled, skips or has no result. Aggregate `Test`
continues to require that stable check.

| Original Darwin step | Child job |
| --- | --- |
| Native key adapter execution and CGO CLI build | `darwin-build` |
| Complete processhost race suite | `darwin-build` |
| Compile/link every test package without executing tests | `darwin-build` |
| Claude binding and endpoint receipts | `darwin-process` |
| Codex binding and receipt fixtures | `darwin-process` |
| Attention store and consumer fixtures | `darwin-process` |
| Typed inventory, convergence and admission | `darwin-process` |
| Detached process CLI lifetime boundaries | `darwin-build` |

The native/link child builds its own CGO CLI before setting `PMX_TEST_CLI`.
It compiles all test packages before the detached CLI tests, reusing non-race app
compilation. The process child runs Claude before Codex, attention and inventory,
reusing race app compilation. Neither child consumes another runner's binary. The workflow contract pins the
original command for each row and exercises incomplete child results against
the aggregate's actual script. More runners trade repeated setup/compilation
for a shorter critical path; runner time and wall time must be measured separately.

## Targets

- `make test` runs the fast Go unit suite. Apart from the real-tmux tests
  below, these tests avoid tmux, TTY, GUI, and host shell dependencies.
- Detached process creation adds CLI fixtures for caller EOF/exit, parent
  provider-group TERM/HUP, exact stop/Wait and repeated stop, create-window,
  startup hook failure, and caller cancellation. The Darwin native/link
  child builds its own checkout and executes these boundary fixtures as well as
  their unit tests; it does not substitute cross-compilation for execution.
- `make test-process-host-cli` builds this checkout, copies the binary into a
  temporary root, and runs every top-level test in
  `codex_process_binding_test.go`, `codex_process_ownership_test.go`,
  `process_attention_wiring_test.go`, and `process_host_supervisor_test.go`.
  SDK process relaunch fixtures in `sdk_relaunch_process_cli_test.go` cover
  frozen domains, host moves, child-free claims and startup cancellation.
  The runner sets `PMX_TEST_CLI` to that copy, isolates HOME, XDG directories
  and tmux's temporary directory, removes inherited `TMUX`, `TMUX_PANE`,
  `PROJMUX_*` and provider credentials, and keeps the Go caches fixed with
  `GOTOOLCHAIN=local`. Providers are Python protocol fixtures; no real API
  or installed provider is used. The runner also audits every `internal/**/*_test.go` consumer of
  `"PMX_TEST_CLI"` against its selected files and explicit exclusions, so a new
  consumer file cannot silently skip this gate. Missing tests, any Skip (including a subtest),
  and any failure fail the target. Verbose PASS lines and a final count summary
  prove execution. The Linux `Process Host CLI Tests` job runs this target
  behind aggregate `Test`, separately from the five required job names.
  The runner additionally builds a CLI with the private
  `projmux_process_cli_regression` tag and supplies it through
  `PMX_TEST_CLAUDE_REGRESSION_CLI`. The detached Claude SIGTERM read-deadline
  regression uses the original parent signal/Wait, single delivered send,
  unchanged binding, and exact child stop assertions. It holds one validated
  Ready helper's plain host-check response until the existing five-second
  native read deadline, releases it, and waits for the exact helper's completed
  clear-or-retain decision before continuing that same body. The control uses
  private `PMX_TEST_CLAUDE_REGRESSION_CONTROL` receipts pinned to the proof,
  executable, host, provider and helper; mismatched or overlapping readers,
  missing receipts and non-exclusive decisions fail the regression. Ordinary
  builds contain no control environment/filesystem reads or waiting, and the
  tagged build without the control variable follows the original transport.
  The deferred Claude absent-launch reservation race also runs its original
  scenario body with the tagged CLI. After final resume admission, a one-shot
  hook uses a Registry snapshot from the original `deferredReady` loop once
  the current activation and frozen session match. It reuses the same native
  deadline and clear-or-retain completion barrier before the original readiness
  evaluation. The hook adds no RPC, Registry polling loop or timeout; ordinary
  calls have no hook. All original readiness predicates, their evaluation order
  and the 40-second context remain unchanged.
  The controlled case also requires the barrier to complete; readiness alone
  cannot pass a run that never injected the deadline.
  `TestDeferredClaudeAbsentLaunchReservationRaceActualCLI` keeps its ordinary
  execution; `TestDeferredClaudeAbsentLaunchReservationRaceHostReadDeadlineActualCLI`
  adds the controlled case. Measure that new test's elapsed time from its Go
  JSON terminal event separately: the runner's `regression_test_seconds`
  summary currently counts the detached regression only.
  This covers an observed failure mechanism without establishing the cause of
  earlier CI failures. A native host read deadline remains not-ready and cannot
  start a registration writer; only an already-Ready helper's idle retirement
  is deferred. Persistent deadlines can therefore keep an idle helper alive
  longer; confirmed refusals and replacements still retire it. Transport tests
  also cover partial frames, oversize precedence and unrelated timeout errors.
  The summary reports the additional tagged build and regression test seconds
  separately; test selection remains discovered from the current sources.
  Build has a 180-second bound and precompilation has a 120-second bound.
  Go package tests have a 420-second timeout; the test subprocess has 480
  seconds to preserve Go timeout diagnostics. The job has 15 minutes.
  To run it manually, use `make test-process-host-cli` (or
  `make test-process-host-cli GO=/path/to/go` for a local toolchain matching
  go.mod). The real-tmux window rename test remains in its existing unit and
  integration jobs and is outside this process fixture target.
  `process_codex_host_upgrade_cli_test.go` checks public Stop/actual Wait and
  detached resume of the same Codex Agent/Pane/thread, current profile
  model/effort preparation against the wire and settings receipt, immutable
  legacy instructions, live-writer refusal, and provider-refusal cleanup.
  It consumes the existing CLI/protocol fixtures; the selected suite also
  retains `TestProcessResumeOwnerKillIsNotResumableActualCLI` and
  `TestProcessCodexRelaunchUnretiredWriterActualCLI` for unretired writers.
  This is current-image regression evidence. A separate old/new binary
  observation must record each image's source and SHA, actual Wait/retirement,
  same conversation and new host generation, and both peer endpoints.
  A real model's nonce transformation in a new generated reply is separate
  evidence again. Neither peer delivery nor that reply proves instruction
  updates; rollout instruction records are not proof of transmitted instructions.
- `make vet` runs `go vet ./...`. CI runs it with deadcode and the non-Go
  contracts in `Unit / contracts and static gates`.
- CI partitions the ordinary Go suite into three `internal/app` shards and
  one shard containing every other package. `scripts/unit-shards.py` assigns
  app Test/Example/Fuzz names by SHA-256 of their first ten characters modulo
  three, keeping each prefix cohort together. A compressed trie selector has
  a 4KiB budget: existing process identity tests retain their 8KiB argv limit.
  Short names match exactly, and the runner checks the compact selector
  against the selected inventory before execution. Evidence records UTF-8
  selector bytes and the NUL-delimited Go command argv byte count; the app
  command is also bounded to 8KiB. Each shard lists
  its unfiltered names on the same checkout, then runs its selection with
  `go test -json -count=1`; ordinary Fuzz seed execution stays enabled,
  without starting fuzzing. Empty packages are retained. The remaining
  packages run without a name filter.
  `make test-unit-shard UNIT_SHARD=app-0 UNIT_EVIDENCE_DIR=.bin/unit/app-0`
  is the shard entrypoint (`app-1`, `app-2`, and `rest` are the other values).
  `make test-contracts` shares the complete dependency list with local
  `make test`, which still runs all contracts followed by `go test ./...`.
  The stable required `Unit Tests` aggregate requires every child to succeed
  and compares uploaded package/name inventories against actual top-level
  execution. Missing, duplicate, failed, skipped, cancelled, or stale shard
  evidence fails closed. A skipped individual test retains its original
  semantics; strict tmux still refuses missing tmux.
  Each workflow attempt preserves `unit-evidence-*` (manifests and Go JSON
  events) and `unit-inventory-comparison-<attempt>` (complete unpartitioned
  inventory, selected/executed names, Test/Example/Fuzz counts, and child
  run/attempt provenance) for comparing repeated runs on one PR head.
  Child artifacts overwrite their own fixed shard name on a rerun, so a
  failed-child rerun can reuse successful children from an earlier attempt
  of that same run and head. The comparison retains each child's attempt
  number and rejects other runs or future attempts. Comparison artifacts
  are unique per attempt; download child event artifacts before rerunning
  when their earlier detailed logs are needed.
- The real-tmux Go tests in `internal/app` find tmux through one helper,
  `requireRealTmux`. Without tmux they skip; `PROJMUX_REAL_TMUX_STRICT=1`
  turns a missing tmux into a test failure instead, so a run that is meant to
  exercise real tmux cannot pass by skipping. A new real-tmux test must call
  the helper rather than its own `exec.LookPath("tmux")` skip, and the source
  audit `TestRealTmuxSkipIsOwnedByOneHelper` fails on any other skip.
  Every CI unit execution child installs tmux and runs its target with
  `PROJMUX_REAL_TMUX_STRICT=1` and a TMPDIR of at least 100 bytes,
  so a missing tmux there fails the job instead of skipping those tests.
  The same switch turns on the isolated real-tmux smokes in
  `internal/integrations/tmux` and `internal/integrations/metadata`, which
  otherwise wait for their own opt-in variable.
- A Go test that opens a unix socket must put it under a short root (the
  `/tmp` + `os.MkdirTemp` idiom, e.g. codexbroker `newRuntimeDomain`,
  `internal/app` `shortTempDomain`) rather than `t.TempDir()`, because the
  socket path has a platform bound (Linux 108B, macOS 104B) and the long CI
  TMPDIR fails such a test. Real-tmux tests put their `TMUX_TMPDIR` or
  `tmux -S` root under `os.MkdirTemp("/tmp", …)` the same way, and the
  liveguard private root, which holds a guarded package's `TMUX_TMPDIR` and
  `XDG_STATE_HOME`, is made under `/tmp` too. Its owner holds a lock file in
  that root for its whole life, so a later guarded owner reclaims the roots of
  killed runs, whose lock is free, and leaves roots without that lock file
  alone.
- Picker unit coverage includes the backend-neutral item/action contract,
  native title-focused filtering, numeric selection, and shared close actions.
- `make test-integration` builds `test/docker/Dockerfile` and runs
  `test/integration/linux-smoke.sh` in Docker. It validates Linux dependency
  discovery, tmux config generation/install, app config reload against a real
  `tmux` server, and notify queue CRUD. It also runs `test/install/smoke.sh`,
  so the required `Integration Tests` job covers the install smoke.
- `make test-install-smoke` builds the same Docker image and runs only
  `test/install/smoke.sh`, as a local entrypoint. It validates `make install`,
  atomic binary replacement into an isolated install dir, pre-publication
  marker convergence, concurrent legacy/candidate/installed shell and attach
  consumers, exact server-generation/session preservation, `tmux apply` into
  the resolved `$XDG_CONFIG_HOME/projmux/tmux.conf`, and post-install notify
  reconcile initialization with a fresh HOME/XDG state tree. It ends with a
  `>> install smoke passed` line. `make verify` does not repeat it, because
  `make test-integration` already runs it.
- `make test-e2e` prepares one attempt-local immutable product binary, then
  runs four isolated Linux real-tmux fixtures plus the Codex lifecycle and npm
  staging fixtures. The required inventory is `L01`-`L20`, `C01`, and `N01`;
  every fixture has its own HOME/XDG/tmux/socket/evidence roots and every
  consumer records the same binary SHA. `E2E_SCENARIO=<ID>` selects one exact
  stable scenario for replay.
- Three mutually exclusive selectors narrow one invocation. `E2E_SCENARIO=<ID>`
  replays one scenario, `PROJMUX_E2E_LINUX_SHARD=<shard>` runs exactly one
  Linux fixture with the terminal inventory its `linux-shards.tsv` row owns,
  and `PROJMUX_E2E_SUITE=codex-lifecycle|npm-staging` runs one non-Linux suite.
  Setting none keeps the local acceptance profile: all four Linux shards run
  serially in canonical `fixture-1` through `fixture-4` order on the one host,
  followed by both suites. CI uses the shard/suite selectors to give every
  suite its own runner, so each runner likewise has capacity for one shard.
  `PROJMUX_E2E_LINUX_MODE=parallel make test-e2e` is the explicit same-host
  stress profile; `serial` and `parallel` are the only accepted mode values.
- Every scenario wait states a budget in seconds rather than a loop count, and
  `E2E_WAIT_SCALE=<factor>` multiplies all of them at once. Raise it when a
  runner is slow or loaded; a wait that expires still fails with the description
  of what it was waiting for, so a slow machine reports a timeout rather than
  the regression message of the assertion that would have run next.
- `make test-e2e-update` runs `test/e2e/update-flow.sh` in the Node image
  (`test/docker/Dockerfile.node`) on a bridged network. It installs an older
  published `projmux` from the public npm registry, runs the source build's
  `update apply`, and asserts the global install reaches the latest published
  version while the exact legacy tmux server, socket, and sessions survive,
  then checks npm installer autodetection from an npm-shaped path. Because it
  depends on the public registry and the published package, it is not a
  required check and stays out of the aggregate `Test`: the separate
  `Update Flow E2E` workflow runs it on pull requests that change the update
  path, daily on a schedule, and on manual dispatch. A local run skips when the
  registry is unreachable; under CI (`CI=true`) or
  `PROJMUX_UPDATE_FLOW_STRICT=1` the script runs with `--strict`, where a skip
  fails the run instead.
- `make test-e2e-contract`, `make test-e2e-reliability`, and
  `make test-e2e-shards` validate typed attempt evidence, bounded semantic
  waits/owned cleanup, and exhaustive four-shard isolation without rerunning
  the full product matrix. The shard target also pins the CI job list to the
  manifest. A deterministic fake-runner start/release barrier proves local
  default overlap is one and explicit parallel overlap is four while both use
  one immutable binary, the exact 22-scenario inventory, and the same result
  hash. The target also pins one non-fail-fast CI job per shard and per suite,
  each with its own runner, timeout, and uniquely named evidence artifacts, all
  of them required children of the aggregate `Test` gate. It also pins the thin
  `E2E Tests` job, which exists because the branch ruleset requires a status
  check under that exact name; a required context that is never reported stays
  pending rather than failing, so dropping that job would deadlock merges. The
  tag-triggered Release workflow uses the same four Linux shards and two suite
  selectors on separate runners, then reduces them through the fail-closed
  `Release E2E Tests` aggregate before `Build Release` starts. `make test`
  runs `make test-e2e-shards`, so the required Unit Tests job fails on shard
  contract drift. The live dialogue canary's own contract is
  `test/agent_dialogue_canary_test.py`, not this target.
- `make test` also runs the evidence contract (`make e2e-evidence-contract`),
  `make test-e2e-reliability`, and `make test-e2e-residual-policy`, so the
  required Unit Tests job fails when any of them does. `make test-e2e-contract`
  is a local entry point for the admission and evidence halves, both of which
  `make test` already runs. The reliability contract removes the artifact
  directory it creates on every exit, and keeps one the caller passes in
  `PROJMUX_E2E_ARTIFACTS`.
- `make test-e2e-coverage` validates
  `test/e2e/ags-oedr-manifest.json`: executable scenario markers and shard
  assignments must match all 22 rows with orphan count zero. A matrix may move
  out of real-tmux E2E only when its checked-in entry names executable lower
  positive, negative, and fixed-point evidence and retains a real-boundary
  sentinel. The manifest/orphan half is a prerequisite of `make test-e2e`,
  while the referenced lower test runs in both this coverage target and the
  required unit-test job, so `make test-e2e-coverage` is a local entry point
  whose two halves are already enforced.
- `make security` runs the exact three Security groups in parallel locally:
  Go vulnerability/security, Go static quality, and repository policy.
  `make security-serial` is the parity control and `make security-contract`
  checks scanner/rule/baseline identity, PR-range and HEAD-history secret scans,
  cache miss-to-hit convergence, privacy-safe artifacts, and the fail-closed
  aggregate. CI exposes their stable aggregate as `Test`.
  After adding or removing a Go package, refresh the package-set pin with
  `make security-pin-refresh` (see [Security gates](#security-gates)).
- `make deadcode` runs the focused baseline-contract fixture, then runs
  `go tool deadcode` (pinned via the go.mod tool directive) over the module.
  `.deadcode-allowlist.txt` is exact to current findings: duplicate and stale
  rows fail. `.deadcode-must-keep.txt` separately records proactive migration,
  compatibility, and proof APIs as `symbol<TAB>non-empty reason`; duplicates
  within either file, overlap across files, malformed reasons, and findings
  outside the two-file union fail deterministically. `make test` also runs the
  focused fixture, and `make fix` runs the complete deadcode gate after
  `go fix`.

## Security gates

- `make security` runs three groups in parallel; CI runs each as its own job: `make security-go` (govulncheck, gosec), `make security-static` (staticcheck), `make security-policy` (gitleaks, actionlint, shellcheck).
- `make security-tools` installs the Go-based scanners at the versions pinned in `.security/security-tools.versions`, and ShellCheck from the release assets whose digests `.security/shellcheck.sha256` pins; `make security-policy` refuses any other ShellCheck version.
- gosec and staticcheck findings are compared with the reviewed baselines `.security/gosec-baseline.json` and `.security/staticcheck-baseline.json`. A finding beyond its baseline count fails the gate.
- `.security/security-current-findings.json` pins the current finding counts and the baseline digests. **It is the only place a reviewed baseline digest is defined.** Change a baseline file and you change this pin in the same commit, or the gate fails.
- Never restate a baseline digest anywhere else. Consumers resolve it through `scripts/security-baseline-pin.py`, and `make security-pin-contract` (part of `make test`) fails on a second copy.
- `make security-contract` is that gate: scanner parity, gitleaks history range, evidence typing, the stable aggregate, and actionlint. CI runs it as the job `Security / Contract gate`, which fans into the aggregate `Test`.
- The same pin holds `package_count` and `package_set_sha256` for `go list ./...`, computed only by `scripts/security-package-pin.py`. If you add or remove a Go package, run `make security-pin-refresh` and commit the resulting diff; `make test` (`make security-pin-contract`) and the `Security / Contract gate` job fail on a mismatch and name that target.

## Docker-Covered Checks

The Docker suites are intended to cover portable Linux behavior that can be
made deterministic in a container:

- binary build and source install into an isolated prefix
- `doctor` dependency checks for `tmux`, `git`, and `stty`
- tmux config print/install/apply paths
- notify queue push/list/ack/reconcile state transitions
- focus fallback behavior when a tmux server has sessions but no attached
  client
- status rendering that only depends on tmux state and local files

The test container disables networking during `docker run`. The image build may
use the network to fetch the pinned base image and apt packages, but suite
execution should not need network access after the image is built.

## Layered E2E Evidence

The AGS-OEDR manifest is the source of truth for which layer owns each
guarantee. Its `A/G/S` fields describe the scenario contract and `O/E/D/R`
identify the owner, enforcement, detection, and recovery boundary. The
coverage audit reads the actual `smoke_contract_begin` markers and
`linux-shards.tsv`; documentation-only rows cannot satisfy it.

L19's plural-read context/selector table is the first evidence-backed move.
`TestPluralReadContextSelectorMatrix` executes all 60 cells twice at the app
layer, covering exact positive sets, foreign-scope refusal, and read-only
fixed-point behavior with zero Registry transaction/write/model change. L19
still drives five representative sentinels through the built binary on the
queried exact real-tmux socket below its owned smoke root. Lower-layer parity
therefore owns the combinatorial table; E2E still owns transport, origin, and
socket/root containment.

Merged main evidence is closed by the same manifest without adding stable IDs.
L17 links the simultaneous/coalesced exact-generation exit unit tests and the
integration marker to its real-hook replay: both dead/mirrored Panes disappear,
Agents become resumable Offline with cleared pane refs, siblings survive, and
the repeat is Registry-byte-identical. L18 links the closed foreground
`run-shell` producer ledger and integration transport marker to its attached
client replay: success is silent or one bounded exact-client message, no
view-mode overlay appears, and origin PID, focus, and Registry identity remain.
The two lifecycle boundaries merged at `3322b5f7` stay on existing IDs rather
than expanding the inventory. L17 links exact clean last-Pane plus
Window-unlinked causality, zero-Window Project retention, stale-resume refusal,
sibling reanchor/containment, and byte-identical replay to the Project-stop
marker. L19 separately links ControlSession last-Window descendant cleanup,
root retention, zero replacement allocation, sibling containment, and
fixed-point replay to its own marker. The shared integration marker closes the
real last-Pane transport boundary. The audit fails when any linked guarantee,
exact lower test inventory/symbol/selector, or executable integration/E2E
marker is missing, misspelled, or duplicated.

Lifecycle commit `dcffa5da` remains inside L11. Its closed evidence row links
the retained/zero-Window lifecycle table, managed runtime stop, Continue, and
always-new Fresh lower tests to the existing lifecycle integration completion
and the L11 attached-client marker before pass. Authority commit `de52d15d`
remains inside L17. Its row links creator-Registry admission, journal-path and
CLOEXEC handshake tests to the immediate-exit integration/E2E markers, and
also closes the focused fresh-root repeat harness plus the read-only
owner/queue observation and product-terminal controller marker. These rows add
no scenario ID: the audit still requires exactly 22 stable scenarios and fails
closed if either source commit, guarantee set, lower selector, supporting
marker, or marker-before-pass edge drifts.

The required result hash accepts exactly one `begin` followed by one typed
`pass` for every expected stable ID. A terminal `fail`, `cancel`, or
`unattributed` class, a terminal without its begin row, and an interrupted
unterminated attempt all keep the aggregate red. `cancel` remains a schema
value for an explicitly observed cancellation; the shell harness does not
invent cancellation evidence from a signal whose semantic cause it cannot
attribute.

`smoke_contract_pass` records `pass` only when the contract shell's own stdout
and stderr between `smoke_contract_begin` and that call carry no Python
traceback, even one whose child status was consumed. A traceback ends the
scenario through the ordinary fail path at the pass line and logs
`E2E_CONTRACT id=<ID> guard=python-traceback stream=<stream> exception=<last
exception line>`; there is no exemption list. Output redirected to a file never
reaches the guard. `make e2e-traceback-guard-contract` pins this, including a
traceback written while the job log lags behind the pass call.

## Host-Only Checks

The Docker suites do not replace checks that depend on a real host terminal,
desktop shell, or OS integration:

- terminal emulator key delivery and swallowing for guaranteed `Alt-1..5`
  launch keys, optional user-configured direct aliases, and transport-dependent
  chords
- Windows Terminal and WSL interop
- macOS host path, shell, and GUI behavior
- desktop notification click callbacks
- terminal-specific popup rendering and interactive key dispatch

Keep those checks as manual or host-run smoke validation until a dedicated
host harness exists.

Use this smoke checklist when a change touches terminal delivery, host desktop
notifications, or reviewer confidence around those boundaries. If the change
does not touch those areas, copy the PR-note block below and mark the relevant
rows `not run`.

### Terminal Key Delivery

Run the raw key probe outside tmux, in the terminal emulator being claimed:

```sh
projmux setup --timeout 10s
```

Observe:

- `Alt-1` through `Alt-5` report `OK plain`. These are the guaranteed
  zero-config launch defaults.
- If a guaranteed key reports `MISS timeout`, preview a supported terminal
  mapping with `projmux setup terminal ghostty` or `projmux setup terminal windows-terminal`,
  apply it with the same command plus `--apply`, restart that terminal if
  required, and rerun `projmux setup --timeout 10s`.
- Optional direct aliases and transport-dependent chords may be reported by
  the probe, but they are not part of the guaranteed host smoke unless the PR
  explicitly changes them.

Then run the app in the same terminal:

```sh
projmux shell
```

Observe:

- `Alt-1` opens the project sidebar.
- `Alt-2` opens the notification sidebar.
- `Alt-3` opens Recent Windows.
- `Alt-4` opens the AI resume session picker.
- `Alt-5` opens Settings.
- `Alt-7` opens the AI split picker.
- Pressing the same launch key again closes the popup instead of typing escape
  bytes into the shell or picker input.

### WSL Toast

Run this from WSL with Windows Terminal available. The detached tmux server is
intentional: `projmux focus` falls back to the product desktop notification
path when there is no attached client to switch.

```sh
sock="${TMPDIR:-/tmp}/projmux-host-smoke.sock"
tmux -S "$sock" kill-server 2>/dev/null || true
tmux -S "$sock" new-session -d -s projmux-host-smoke 'sleep 600'
PROJMUX_DESKTOP_NOTIFY_MODE=notify \
  projmux focus --socket "$sock" --target projmux-host-smoke --json
tmux -S "$sock" kill-server
```

Observe:

- The JSON includes `"ok":true`, `"dispatch":"notify-only"`, and
  `"reason":"no-attached-client"`.
- Windows shows a short projmux toast with `session ready:
  projmux-host-smoke`.
- The toast is passive: clicking it does nothing, and the host terminal window
  never comes forward on its own.
- No visible PowerShell or console window remains open after the toast.

Desktop notification mode has exactly two states, `off` and `notify`. Repeat
with `PROJMUX_DESKTOP_NOTIFY_MODE=off` and confirm no toast appears while the
in-app notify queue / statusbar / sidebar still record the event. Repeating
with the retired `PROJMUX_DESKTOP_NOTIFY_MODE=raise` must behave identically to
`notify` — toast only, no window raise, no clickable action.

### macOS GUI Notification

The built-in desktop sender is Linux/WSL-oriented. On macOS, smoke the
documented `PROJMUX_NOTIFY_HOOK` escape hatch with an `osascript` sender:

```sh
hook="${TMPDIR:-/tmp}/projmux-macos-notify.sh"
cat >"$hook" <<'SH'
#!/bin/sh
title=${1:-projmux}
body=${2:-}
osascript \
  -e 'on run argv' \
  -e 'display notification (item 2 of argv) with title (item 1 of argv)' \
  -e 'end run' \
  "$title" "$body"
SH
chmod 0755 "$hook"

sock="${TMPDIR:-/tmp}/projmux-host-smoke.sock"
tmux -S "$sock" kill-server 2>/dev/null || true
tmux -S "$sock" new-session -d -s projmux-host-smoke 'sleep 600'
PROJMUX_NOTIFY_HOOK="$hook" \
  projmux focus --socket "$sock" --target projmux-host-smoke --json
tmux -S "$sock" kill-server
```

Observe:

- The JSON includes `"ok":true`, `"dispatch":"notify-only"`, and
  `"reason":"no-attached-client"`.
- macOS shows a Notification Center banner with `session ready:
  projmux-host-smoke`.
- If macOS prompts for notification permission, record that state in the PR
  instead of treating the product command as verified.

### PR Note Template

```markdown
Host-only smoke validation:

- Docker-covered checks: `make test-integration` (which includes the install
  smoke) and `make test-e2e` cover portable Linux tmux/config/notify behavior
  only.
- Terminal key delivery: not run / run on <terminal>; `projmux setup --timeout
  10s` showed <result>; `Alt-1..5` app popup smoke <passed/failed/not run>.
- WSL toast: not run / run on <Windows + WSL distro>; detached-focus smoke
  produced `dispatch=notify-only`; observed <toast/no toast/notes>.
- macOS GUI notification: not run / run on <macOS version>; hook smoke
  produced `dispatch=notify-only`; observed <banner/permission prompt/notes>.
- Desktop notification mode: not run / run; `off` produced no toast and
  `notify` produced a passive toast with no window raise; result <notes>.
```

## Public v0 SDK boundary

`make test-sdk-boundary` is a prerequisite of `make test`, including the CI
`Unit Tests` job. It builds a compiler-export-data checker over `pkg/projmux`,
checks every reachable implementation dependency against an exact allowlist
(standard library plus the instruction service and its current support packages),
and walks all exported types, fields, methods, aliases and generic arguments
for internal type leaks. Private implementation fields may use internal services.

A separate `example.com` consumer module imports only `pkg/projmux` and exercises
instruction List/Read/Write/Delete with typed refusals under `GOWORK=off`.
Positive and negative fixture modules prove the checker accepts public types
and rejects app/web dependencies and leaked internal aliases; a fixture compile
failure is not accepted as proof of boundary rejection. Fixtures use local
`replace` directives to test this checkout, without a workspace file. The negative modules live under `test/sdk/testdata`, and
the checker's `_test.go` assertions prove app/web rejection without teaching
product code an operator client name.
`pkg/projmux` parity tests compare the SDK to the CLI adapter using isolated
configuration roots, exact bytes, private permissions, names, limits, missing
files, filesystem failures and profile use refusals. They also test client path
isolation and cancellation before mutation admission. Existing CLI tests retain
its output, reason and exit classification contracts.

Resource SDK parity is checked by `TestResourceSDKCLIParity` in `internal/app`
under the live-machine guard: all four kinds, list envelopes and singular
descriptions, explicit scope combinations, labels and empty results. The SDK
results use the same indented JSON encoder and final newline as CLI stdout.
`TestResourceSDKRefusalParity` and `TestResourceSDKMissingRegistryParity` pair
the existing CLI failure messages and exit classes with SDK typed refusals.
`TestResourceSDKDeepCopy` checks caller-owned results and client isolation.
`TestPublicResourceFieldCoverage` walks the entire reachable JSON field graph
and checks populated and zero-value serialization, so a new internal field
cannot silently disappear from public results. The external consumer also
compiles resource reads with `GOWORK=off`; the boundary checker separately
requires `pkg/projmux/core` to depend only on standard-library packages.
