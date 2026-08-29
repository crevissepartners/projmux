# Testing

The local validation contract is exposed through make targets so CI, agents,
and humans run the same entrypoints.

## Targets

- `make test` runs the fast Go unit suite. These tests avoid tmux, TTY, GUI,
  and host shell dependencies.
- Picker unit coverage includes the backend-neutral item/action contract,
  native title-focused filtering, numeric selection, and shared close actions.
- `make test-integration` builds `test/docker/Dockerfile` and runs
  `test/integration/linux-smoke.sh` in Docker. It validates Linux dependency
  discovery, tmux config generation/install, app config reload against a real
  `tmux` server, and notify queue CRUD.
- `make test-install-smoke` builds the same Docker image and runs
  `test/install/smoke.sh`. It validates `make install`, atomic binary
  replacement into an isolated install dir, pre-publication marker convergence,
  concurrent legacy/candidate/installed shell and attach consumers, exact
  server-generation/session preservation, `tmux apply`, and post-install notify
  reconcile initialization with a fresh HOME/XDG state tree.
- `make test-e2e` prepares one attempt-local immutable product binary, then
  runs four isolated Linux real-tmux fixtures plus the Codex lifecycle and npm
  staging fixtures. The required inventory is `L01`-`L19`, `C01`, and `N01`;
  every fixture has its own HOME/XDG/tmux/socket/evidence roots and every
  consumer records the same binary SHA. `E2E_SCENARIO=<ID>` selects one exact
  stable scenario for replay.
- Three mutually exclusive selectors narrow one invocation. `E2E_SCENARIO=<ID>`
  replays one scenario, `PROJMUX_E2E_LINUX_SHARD=<shard>` runs exactly one
  Linux fixture with the terminal inventory its `linux-shards.tsv` row owns,
  and `PROJMUX_E2E_SUITE=codex-lifecycle|npm-staging` runs one non-Linux suite.
  Setting none keeps the default: four Linux shards in parallel plus both
  suites. CI uses the shard/suite selectors to give every suite its own runner,
  so container isolation and schedule isolation are the same unit there; local
  `make test-e2e` still runs the whole matrix on one machine.
- Every scenario wait states a budget in seconds rather than a loop count, and
  `E2E_WAIT_SCALE=<factor>` multiplies all of them at once. Raise it when a
  runner is slow or loaded; a wait that expires still fails with the description
  of what it was waiting for, so a slow machine reports a timeout rather than
  the regression message of the assertion that would have run next.
- `make test-e2e-contract`, `make test-e2e-reliability`, and
  `make test-e2e-shards` validate typed attempt evidence, bounded semantic
  waits/owned cleanup, and exhaustive four-shard isolation without rerunning
  the full product matrix. The shard target also pins the CI job list to the
  manifest: one non-fail-fast job per shard and per suite, each with its own
  runner, its own timeout and its own uniquely named evidence artifacts, all of
  them required children of the aggregate `Test` gate. It also pins the thin
  `E2E Tests` job, which exists because the branch ruleset requires a status
  check under that exact name; a required context that is never reported stays
  pending rather than failing, so dropping that job would deadlock merges.
- `make test-e2e-coverage` validates
  `test/e2e/ags-oedr-manifest.json`: executable scenario markers and shard
  assignments must match all 21 rows with orphan count zero. A matrix may move
  out of real-tmux E2E only when its checked-in entry names executable lower
  positive, negative, and fixed-point evidence and retains a real-boundary
  sentinel. The manifest/orphan half is a prerequisite of `make test-e2e`,
  while the referenced lower test runs in both this coverage target and the
  required unit-test job.
- `make security` runs the exact three Security groups in parallel locally:
  Go vulnerability/security, Go static quality, and repository policy.
  `make security-serial` is the parity control and `make security-contract`
  checks scanner/rule/baseline identity, PR-range/full-history secret scans,
  cache miss-to-hit convergence, privacy-safe artifacts, and the fail-closed
  aggregate. CI exposes their stable aggregate as `Test`.
- `make deadcode` runs the focused baseline-contract fixture, then runs
  `go tool deadcode` (pinned via the go.mod tool directive) over the module.
  `.deadcode-allowlist.txt` is exact to current findings: duplicate and stale
  rows fail. `.deadcode-must-keep.txt` separately records proactive migration,
  compatibility, and proof APIs as `symbol<TAB>non-empty reason`; duplicates
  within either file, overlap across files, malformed reasons, and findings
  outside the two-file union fail deterministically. `make test` also runs the
  focused fixture, and `make fix` runs the complete deadcode gate after
  `go fix`.

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
no scenario ID: the audit still requires exactly 21 stable scenarios and fails
closed if either source commit, guarantee set, lower selector, supporting
marker, or marker-before-pass edge drifts.

The required result hash accepts exactly one `begin` followed by one typed
`pass` for every expected stable ID. A terminal `fail`, `cancel`, or
`unattributed` class, a terminal without its begin row, and an interrupted
unterminated attempt all keep the aggregate red. `cancel` remains a schema
value for an explicitly observed cancellation; the shell harness does not
invent cancellation evidence from a signal whose semantic cause it cannot
attribute.

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

- Docker-covered checks: `make test-integration`, `make test-install-smoke`,
  and `make test-e2e` cover portable Linux tmux/config/notify behavior only.
- Terminal key delivery: not run / run on <terminal>; `projmux setup --timeout
  10s` showed <result>; `Alt-1..5` app popup smoke <passed/failed/not run>.
- WSL toast: not run / run on <Windows + WSL distro>; detached-focus smoke
  produced `dispatch=notify-only`; observed <toast/no toast/notes>.
- macOS GUI notification: not run / run on <macOS version>; hook smoke
  produced `dispatch=notify-only`; observed <banner/permission prompt/notes>.
- Desktop notification mode: not run / run; `off` produced no toast and
  `notify` produced a passive toast with no window raise; result <notes>.
```
