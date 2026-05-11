# Notify OS Focus Spike

This document is the on-floor matrix for the "Notify 시 터미널 OS 포커스"
roadmap item. The first step of that roadmap is explicitly a **spike with no
code** — fill in the tables below with real measurements, then make the
follow-on decisions before any adapter code lands.

The full design lives in the project notes (`로드맵 디테일 - Notify 시 터미널 OS
포커스`). This file is the in-tree, fill-in-the-blanks transcription so the
spike trail lives next to the code it informs.

## Background and scope

`projmux focus` (see [`internal/app/focus.go`](../internal/app/focus.go))
currently handles tmux pane focus only. Once a tmux client is already attached
and on screen, it can resolve a target session/window/pane, redirect an
existing client if one is attached, or emit a desktop notification as a last
resort. The notify path that produces the entries this focuses on lives in
[`internal/app/notify.go`](../internal/app/notify.go),
[`internal/app/notify_producer.go`](../internal/app/notify_producer.go), and
[`internal/app/notify_reconcile.go`](../internal/app/notify_reconcile.go).

The realistic stack for "go to the pane that fired this notification" has four
layers:

1. **tmux pane** — already solved by `projmux focus`.
2. **tmux session / client switch** — partially solved
   (`tmux switch-client -c <tty>`).
3. **Terminal app window / tab** — terminal-specific IPC, requires an adapter
   per terminal.
4. **OS-level window focus** — raise the app itself to foreground; platform
   policy varies and the result is best-effort.

Layers 3 and 4 are the unresolved area and the scope of this spike.

## Trigger mode question

Two entry points are possible:

- **(a) on-push (automatic)**: `projmux notify push` fires OS-level focus
  immediately when the notification is enqueued.
- **(b) on-click / keypress (user-initiated)**: extend the existing
  `projmux focus` path so a statusbar / sidebar click goes
  tmux pane focus → terminal tab focus → OS window focus.

**Recommendation: (b) is primary, (a) is opt-in only.**

- (a) steals the screen while the user is doing something else and is hostile
  UX by default.
- (b) only runs at the moment the user explicitly says "I want this now," so a
  larger focus action is acceptable.
- A system notification daemon (libnotify, macOS UserNotificationCenter,
  Windows Toast) is the (a) substitute: the OS shows the notification, the
  user clicks it, and that click drops into the (b) path.

Lock the decision once the matrix below is filled in:

- [x] Primary trigger mode confirmed as (b).
- [x] (a) shipped as opt-in only, or deferred entirely. — **deferred**.
- [x] System notification daemon path adopted in place of (a), or deferred. —
  **deferred**; the WSL toast path already in place covers the (a) substitute
  role for tier-1.

## Terminal × OS matrix

Fill the **Result** column with one of `OK`, `Partial`, `No`, plus a one-line
note. Tick **Tested?** once measured. Candidate commands are pre-filled from
the roadmap and are starting points, not prescriptions — note in **Result** if
a different command worked better.

| Terminal | OS | Candidate command(s) for window/tab focus | Result | Tested? |
|---|---|---|---|---|
| Ghostty | macOS | `osascript -e 'tell application "Ghostty" to activate'` + window-id match | | [ ] |
| Ghostty | Linux X11 | `wmctrl -ia <wid>` | | [ ] |
| Ghostty | Linux Wayland | Compositor-specific: gnome `dbus` (`org.gnome.Shell.Eval`), KDE `kdotool`, sway `swaymsg` | | [ ] |
| Kitty | Any | `kitty @ focus-window --match <expr>` | | [ ] |
| WezTerm | Any | `wezterm cli activate-pane --pane-id <id>` | | [ ] |
| Windows Terminal | Windows | `wt -w <id> focus-tab -t <n>` (same instance only) | | [ ] |
| Windows Terminal | WSL → Windows | WSL interop + `wt.exe -w <id> focus-tab` | OK — `wt.exe -w 0 focus-tab -t 0` raises the WT window (verified WSL2 Ubuntu-24.04, WT host) | [x] |
| iTerm2 | macOS | AppleScript (`tell application "iTerm" ...`) or Python API | | [ ] |
| Alacritty | Any | No remote IPC. OS-window focus only — fall back to the table below. | | [ ] |
| Foot | Linux | `footclient` is limited. OS-window focus only — fall back to the table below. | | [ ] |
| VS Code embedded | Any | Best-effort via `vscode://` URL handler or `code --command` | | [ ] |

## OS-level window activation matrix

Independent of the terminal: "raise this app to the foreground." Fill the
**Result** column the same way.

| OS | Candidate command(s) | Notes | Result | Tested? |
|---|---|---|---|---|
| macOS | `osascript -e 'tell application "<AppName>" to activate'` | Stable; no special permission. | | [ ] |
| Linux X11 | `wmctrl -a <window-name>` or `xdotool windowactivate <wid>` | Requires `wmctrl` / `xdotool` installed; standard. | | [ ] |
| Linux Wayland (GNOME) | `gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.gnome.Shell.Eval ...` | Depends on shell version; recent GNOME locks `Eval` outside dev mode. | | [ ] |
| Linux Wayland (KDE) | `kdotool windowactivate <wid>` | KWin script bridge; requires `kdotool`. | | [ ] |
| Linux Wayland (sway) | `swaymsg '[con_id=<id>] focus'` | Works for sway / wlroots compositors. | | [ ] |
| Windows | `SetForegroundWindow` via PowerShell + P/Invoke | Foreground-lock policy: if the target hasn't recently been active, the OS will only flash the taskbar entry. | | [ ] |
| WSL → Windows | Same as Windows, invoked through `powershell.exe` interop | Inherits Windows foreground-lock. | Partial — raw `SetForegroundWindow` returns `False` under foreground-lock; the `AttachThreadInput` + `BringWindowToTop` + `SetForegroundWindow` workaround does bypass the lock (verified WSL2 Ubuntu-24.04, WT host). Caveat: pairing it with `ShowWindow(SW_RESTORE)` unconditionally restores **maximized** windows to normal size — adapter must `IsIconic`-guard `ShowWindow` so only minimized windows are restored. | [x] |

## Detection signals

Each terminal adapter has to detect that it's the right one to run before
touching IPC. These are the env vars / files we can rely on. The precedent for
this style of detection is
[`internal/app/init_ghostty.go`](../internal/app/init_ghostty.go) and
[`internal/app/init_windows_terminal.go`](../internal/app/init_windows_terminal.go) —
the new `osfocus` adapters should reuse the same detect-then-dispatch pattern.

| Terminal | Signal(s) |
|---|---|
| iTerm2 | `TERM_PROGRAM=iTerm.app` (plus `ITERM_SESSION_ID`) |
| Apple Terminal | `TERM_PROGRAM=Apple_Terminal` |
| VS Code (embedded) | `TERM_PROGRAM=vscode` (plus `VSCODE_INJECTION`, `VSCODE_PID`) |
| Windows Terminal | `WT_SESSION`, `WT_PROFILE_ID` |
| Ghostty | `GHOSTTY_RESOURCES_DIR`, `GHOSTTY_BIN_DIR` |
| Kitty | `KITTY_PID`, `KITTY_LISTEN_ON`, `KITTY_WINDOW_ID` |
| WezTerm | `WEZTERM_PANE`, `WEZTERM_EXECUTABLE`, `WEZTERM_UNIX_SOCKET` |
| Alacritty | `ALACRITTY_LOG`, `ALACRITTY_WINDOW_ID`, `ALACRITTY_SOCKET` |
| Foot | `FOOT_SOCK_*`, `FOOTCLIENT` (varies by build) |

Notes for adapter authors (from Test 1, WSL2 Ubuntu-24.04 inside Windows
Terminal):

- Inside tmux, `TERM_PROGRAM` is rewritten to `tmux` and masks the host
  terminal. Detect Windows Terminal via `WT_SESSION` / `WT_PROFILE_ID`
  (forwarded into WSL via `WSLENV`), not `TERM_PROGRAM`.
- `WAYLAND_DISPLAY=wayland-0` is present in WSL2 via WSLg. Do **not** treat it
  as a "running on native Wayland" signal — gate Wayland adapters on a real
  Wayland session (e.g. absence of `WSL_INTEROP`).
- `WSL_INTEROP` + `WSL_DISTRO_NAME` reliably identify WSL2; use them to gate
  the WSL → Windows adapter branch.

Notes from Test 2 (WSL → Windows OS-level activation):

- Raw `SetForegroundWindow` is unreliable under Windows foreground-lock; it
  returns `False` whenever the calling process isn't already foreground (the
  common case for a background-triggered focus).
- The `AttachThreadInput(fgThread, thisThread, true)` + `BringWindowToTop` +
  `SetForegroundWindow` + detach combo bypasses the lock and does raise the
  target window in this configuration.
- `ShowWindow(handle, SW_RESTORE)` un-maximizes maximized windows as a side
  effect. The adapter must `IsIconic`-guard the call so it only restores
  minimized windows; do not call `ShowWindow` unconditionally.
- For Windows Terminal specifically, prefer the Test 1 path
  (`wt.exe -w 0 focus-tab`) — it raises WT without any of the above
  ceremony and has no side effect on window state. The `SetForegroundWindow`
  combo is the fallback for non-WT Windows apps.

Notes from Test 3 (`wt.exe -w 0` bare, no subcommand):

- Raises the WT window OK and preserves maximization, **but adds a new tab
  as a side effect** (the bare invocation defaults to "new tab in window
  0"). Unsuitable as a raise-only call.
- Always pair `wt.exe -w 0` with a no-op subcommand such as `focus-tab -t 0`
  (Test 1) to raise without creating a tab. Treat the bare form as
  reserved for the "open new tab" case only.

If multiple signals are present (e.g. tmux inside VS Code's embedded
terminal), the adapter chain picks the innermost match the IPC can actually
talk to and falls through to the next on detect failure.

## Decisions to lock after measurement

- [x] Tier-1 support matrix — which terminal × OS cells ship in the first
  adapter PR. → **Windows Terminal × WSL → Windows only** (the single
  combination measured this session). Other matrix rows stay pending tier-2
  measurements.
- [x] Trigger mode — confirm (a), (b), or both (b primary + (a) opt-in). →
  **(b) on-click/keypress is primary; (a) on-push is deferred** (the WSL
  toast already serves as the (a) substitute).
- [x] System notification daemon integration — in scope for tier-1, or
  deferred to a follow-on PR. → **deferred** (WSL toast path already in
  place).
- [x] Adapter module path — proposed `internal/integrations/osfocus/`. Confirm
  or pick alternative. → confirmed `internal/integrations/osfocus/`.
- [x] Adapter call style — synchronous from `projmux focus`, or background
  (`tmux run-shell -b`-style) so the click → toast path stays responsive. →
  **background goroutine, non-blocking** inside the adapter; the chain
  itself returns immediately to the caller.
- [x] Failure policy — silent fallback (keep entry in queue, no error
  surfaced) confirmed as the default. → confirmed; the chain returns nil
  even when an adapter's Focus errors.

Locked 2026-05-11.

### Tier-1 status

Tier-1 adapter shipped: `internal/integrations/osfocus/` with
`WindowsTerminalWSLAdapter`. Other matrix rows remain pending tier-2
measurements.

## Out of scope

- Guaranteeing identical behavior across every environment. Cells the matrix
  marks `No` stay fallback-only (notification stays in the queue); we do not
  try to force-support them.
- Shipping on-push automatic focus (a) as the default. If (a) ships at all it
  is opt-in.
- Bypassing OS foreground-lock policy (Windows in particular). The adapter
  does what the OS allows and no more.

## Risks and cost

- Adapter calls spawn external processes, which adds latency on the focus
  path. Tier-1 adapters should be invoked asynchronously (background) so the
  visible click → focus response stays snappy.
- `wmctrl`, `xdotool`, `wt.exe`, `osascript`, `kdotool`, etc. are not all
  installed by default on user systems. Adapter `Detect()` must verify the
  binary is on `PATH` before claiming the slot.
- System notification daemon integration drags in OS permission flows (macOS
  notification permission, Windows Toast registration, libnotify
  availability). Estimate that cost in the spike before committing.

## Source links

- [`internal/app/focus.go`](../internal/app/focus.go) — current tmux pane
  focus dispatcher; the planned hook point for the adapter chain.
- [`internal/app/notify.go`](../internal/app/notify.go) — notify queue API.
- [`internal/app/notify_producer.go`](../internal/app/notify_producer.go) —
  pushes notifications from the attention state machine.
- [`internal/app/notify_reconcile.go`](../internal/app/notify_reconcile.go) —
  back-fills the queue from live tmux state.
- [`internal/app/init_ghostty.go`](../internal/app/init_ghostty.go),
  [`internal/app/init_windows_terminal.go`](../internal/app/init_windows_terminal.go) —
  precedent for terminal detection and per-terminal adapter dispatch.
- [`internal/integrations/tmux/`](../internal/integrations/tmux/) — external
  process helpers; pattern to reuse for the new adapters.
- Planned: `internal/integrations/osfocus/` — new home for the OS / terminal
  focus adapters once the matrix above is filled in.
