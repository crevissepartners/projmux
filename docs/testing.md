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
  replacement into an isolated install dir, `tmux apply`, and post-install
  `notify reconcile` initialization with a fresh HOME/XDG state tree.
- `make test-e2e` builds the same Docker image and runs
  `test/e2e/linux-smoke.sh`. It validates a minimal real-tmux workflow:
  sessions, panes, config sourcing, reply-state notify reconciliation, focus
  notify fallback, and status notify rendering.
- `make deadcode` runs `go tool deadcode` (pinned via the go.mod tool
  directive) over the module and reports unreachable functions, filtering out
  the intentional/MUST-KEEP baseline in `.deadcode-allowlist.txt`; it fails
  only on NEW dead code, and `make fix` runs it after `go fix`.

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
- In `notify` mode, the toast has no click-to-focus action and should not
  auto-raise the host terminal.
- No visible PowerShell or console window remains open after the toast.

If the PR changes click-to-focus behavior, repeat with
`PROJMUX_DESKTOP_NOTIFY_MODE=raise`, click the toast, and record whether the
host terminal returns to the target. `raise` should also be the only mode where
`projmux focus` performs post-switch osfocus. Otherwise leave click callbacks
marked as manual/not run.

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
- Desktop notification click callbacks: not run unless this PR changes
  click-to-focus behavior; result <notes>.
```
