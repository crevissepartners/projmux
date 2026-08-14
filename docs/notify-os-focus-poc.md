# Notify OS Focus Spike (HISTORICAL — retired in 0.11.0)

> **Status: retired. This document is kept for its measurement trail only and
> does not describe current behavior.**
>
> Desktop notification became a two-state model (`off` / `notify`) in 0.11.0.
> Notification delivery never takes host terminal window focus, Toasts carry no
> click URI, and projmux registers no `projmux://` protocol handler. The
> `raise` mode, the `internal/integrations/osfocus/` adapter package, and the
> Toast click handler described below were all **removed**. `projmux focus`
> stops at the tmux layer in every mode.
>
> Everything below — the terminal × OS matrix, the tier decisions, the "shipped"
> sections, and the file-map — records what was measured and shipped before that
> removal. Read it as history, not as a description of the product. Current
> behavior is documented in
> [configuration.md](configuration.md#desktop-notification-mode).

This document was the on-floor matrix for the "Notify 시 터미널 OS 포커스"
roadmap item. The first step of that roadmap was explicitly a **spike with no
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
| Ghostty | Linux Wayland | Compositor-specific: gnome `dbus` (`org.gnome.Shell.Eval`), KDE `kdotool`, sway `swaymsg` | No — Ubuntu GNOME 50 Wayland with Ghostty 1.3.1 exposes `GHOSTTY_*` detection signals, but Ghostty has no raise/focus CLI action and GNOME Shell `Eval` returns `(false, '')` outside unsafe mode. | [x] |
| Kitty | Any | `kitty @ focus-window --match <expr>` | Partial — Ubuntu GNOME Wayland default kitty 0.45.0 attach exposes `KITTY_WINDOW_ID`/`KITTY_PID`, but no `KITTY_LISTEN_ON`; external `kitty @ ls` fails without a controlling TTY unless kitty is launched with a remote-control socket. | [x] |
| WezTerm | Any | `wezterm cli activate-pane --pane-id <id>` | Partial — WezTerm 20240203 crashes on native GNOME 50 Wayland in this host, but the X11 fallback (`WAYLAND_DISPLAY` unset) attaches and `wezterm cli activate-pane --pane-id 0` succeeds. | [x] |
| Windows Terminal | Windows | `wt -w <id> focus-tab -t <n>` (same instance only) | | [ ] |
| Windows Terminal | WSL → Windows | WSL interop + `wt.exe -w <id> focus-tab` | OK — `wt.exe -w 0 focus-tab -t 0` raises the WT window (verified WSL2 Ubuntu-24.04, WT host) | [x] |
| iTerm2 | macOS | AppleScript (`tell application "iTerm" ...`) or Python API | | [ ] |
| Alacritty | Any | No remote IPC. OS-window focus only — fall back to the table below. | No — Alacritty 0.16.1 IPC socket is reachable and can create windows/read config, but exposes no existing-window focus or raise command. | [x] |
| Foot | Linux | `footclient` is limited. OS-window focus only — fall back to the table below. | No — foot 1.25.0 server/footclient can create a new terminal window, but exposes no existing-window focus or raise command. | [x] |
| VS Code embedded | Any | Best-effort via `vscode://` URL handler or `code --command` | | [ ] |

## OS-level window activation matrix

Independent of the terminal: "raise this app to the foreground." Fill the
**Result** column the same way.

| OS | Candidate command(s) | Notes | Result | Tested? |
|---|---|---|---|---|
| macOS | `osascript -e 'tell application "<AppName>" to activate'` | Stable; no special permission. | | [ ] |
| Linux X11 | `wmctrl -a <window-name>` or `xdotool windowactivate <wid>` | Requires `wmctrl` / `xdotool` installed; standard. | | [ ] |
| Linux Wayland (GNOME) | `gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.gnome.Shell.Eval ...` | Depends on shell version; recent GNOME locks `Eval` outside dev mode. | No — GNOME Shell 50 returns `(false, '')` for `Eval`; X11 fallbacks also do not see native Wayland Ghostty windows (`wmctrl -lx` empty, `xdotool` cannot resolve title/pid). | [x] |
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

Notes from Test 4 (Ubuntu GNOME Wayland + Ghostty):

- Environment: Ubuntu GNOME Wayland (`XDG_SESSION_TYPE=wayland`,
  `XDG_CURRENT_DESKTOP=ubuntu:GNOME`, `WAYLAND_DISPLAY=wayland-0`) with
  Ghostty 1.3.1 (`GHOSTTY_RESOURCES_DIR`, `GHOSTTY_BIN_DIR` set). Inside tmux,
  `TERM_PROGRAM=tmux`, so Ghostty detection must use the `GHOSTTY_*` signals.
- Ghostty 1.3.1 exposes helper actions such as `+new-window`,
  `+show-config`, and `+list-actions`, but no CLI action that raises an
  existing window, focuses a tab, or activates a split. Treat Ghostty on
  native Linux Wayland as OS-window-focus-only unless a future Ghostty IPC
  surface lands.
- GNOME Shell `Eval` is not usable as a default adapter path on GNOME 50:
  `gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell
  --method org.gnome.Shell.Eval 'global.display.focus_window ?
  global.display.focus_window.get_title() : ""'` returned `(false, '')`.
- X11 fallbacks are insufficient for native Wayland windows in this
  configuration: `wmctrl -lx` returned an empty list, and `xdotool
  getactivewindow getwindowname getwindowpid` could only see an Xwayland
  window id with no associated pid/title.

Notes from Test 5 (Ubuntu GNOME Wayland + kitty default attach):

- Environment: Ubuntu GNOME Wayland with kitty 0.45.0 attached to the projmux
  tmux server (`client_termname=xterm-kitty`).
- The tmux client process inherited `KITTY_WINDOW_ID=1`, `KITTY_PID=<pid>`,
  `TERM=xterm-kitty`, and `COLORTERM=truecolor`, but not `KITTY_LISTEN_ON`.
- From a non-interactive external process, `kitty @ ls` failed with
  `open /dev/tty: no such device or address`. That confirms the default
  no-socket launch cannot be driven by an osfocus adapter running outside the
  kitty window.
- The candidate command remains viable only when kitty is launched with a
  remote-control surface (`--listen-on ...` or `listen_on` plus
  `allow_remote_control`) and the adapter can discover that address.

Notes from Test 6 (Ubuntu GNOME Wayland + kitty socket attach):

- Launching kitty with `--listen-on unix:/tmp/projmux-kitty.sock --override
  allow_remote_control=yes` passed `KITTY_LISTEN_ON` through to the tmux client
  and allowed an external process to run `kitty @ --to
  unix:/tmp/projmux-kitty.sock ls`.
- `kitty @ --to unix:/tmp/projmux-kitty.sock focus-window --match id:1`
  exited successfully. The adapter path is viable when the launch/config
  exposes a discoverable remote-control socket.

Notes from Test 7 (Ubuntu GNOME Wayland + WezTerm):

- Native Wayland launch failed on GNOME 50 with `wl_surface ... Buffer size
  ... must be an integer multiple of the buffer_scale (2)`.
- X11 fallback launch (`WAYLAND_DISPLAY` unset) attached to tmux successfully
  (`client_termname=xterm-256color`).
- `wezterm cli list` failed while `WAYLAND_DISPLAY` pointed at the stale
  Wayland socket, but `env -u WAYLAND_DISPLAY wezterm cli list` found the X11
  GUI socket and reported `PANEID 0`.
- `env -u WAYLAND_DISPLAY wezterm cli activate-pane --pane-id 0` exited
  successfully. A WezTerm adapter on GNOME Wayland must avoid stale Wayland
  socket selection when the GUI is actually running on X11.

Notes from Test 8 (Ubuntu GNOME Wayland + Alacritty):

- Alacritty 0.16.1 daemon mode created an IPC socket at
  `/run/user/1000/Alacritty-wayland-0-<pid>.sock`; `alacritty msg --socket
  ... create-window -e tmux -L projmux attach -t repos-projmux` attached a new
  tmux client (`client_termname=alacritty`).
- `alacritty msg --socket ... get-config` succeeded, confirming that the
  socket is externally reachable.
- `alacritty msg` exposes `create-window`, `config`, and `get-config`; it does
  not expose an existing-window focus/raise command. Treat Alacritty as
  OS-window-focus-only for this roadmap item.

Notes from Test 9 (Ubuntu GNOME Wayland + foot):

- Starting `foot-server.socket` and then running `footclient -N ... tmux -L
  projmux attach -t repos-projmux` attached a new tmux client
  (`client_termname=foot`).
- The footclient surface is useful for creating terminals against the server,
  but does not provide an IPC command to focus or raise an existing window.
  Treat foot as OS-window-focus-only.

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
  deferred to a follow-on PR. → **shipped (Windows scope only)**. The WSL
  toast now carries a `projmux://focus?pane_id=…&socket=…&source=toast`
  launch URI; a one-shot HKCU registration on first dispatch wires
  `wsl.exe -d <distro> -- projmux focus --uri "%1"` so the click
  round-trips back into the (b) focus path. Tier-2 covers
  non-WSL/non-Windows daemons (libnotify, UNUserNotificationCenter) and
  the multi-distro handler.
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

### Tier-1 status (RETIRED)

Tier-1 adapter shipped as `internal/integrations/osfocus/` with
`WindowsTerminalWSLAdapter`; other matrix rows stayed pending tier-2
measurements. **The package and its call sites were removed in 0.11.0** —
notification delivery must not take host terminal window focus. Tier-2 was
never started and is not planned.

### Tier-1.5 (RETIRED) — Toast click handler (WSL + Windows Terminal)

**Removed in 0.11.0.** Toasts are passive, no `projmux://` handler is
registered, and the mode selector below no longer exists. Kept for history.

The toast notification produced by `aiDesktopNotifier.Notify` on WSL
carries a `launch="projmux://focus?..." activationType="protocol"`
attribute, and the first dispatch of a tmux server boot registers the
`projmux://` scheme in `HKCU\SOFTWARE\Classes\projmux\…` so Windows
routes the click to
`powershell.exe -NoProfile -WindowStyle Hidden ... "%1"`. That hidden
launcher starts `wsl.exe -d <distro> --exec <abs-path> focus --uri <uri>`
without routing the URI through a shell. Inside WSL, `projmux focus --uri`
parses the URI, resolves the pane id to its
`session:window.%paneID` target via `tmux display-message`, and reuses
the existing focus dispatch. This closes the previously-deferred (a)
on-push trigger mode in the Windows-only scope: the toast becomes the
(a) surface and its click drops into the (b) `projmux focus` path.

Trigger mode is a 3-way selector now (Settings > AI Settings > Desktop
notifications, tmux option `@projmux_desktop_notify_mode`):

| Mode | On push | On click |
|---|---|---|
| `none` | no toast | n/a |
| `notify` | show toast | toast click → `projmux focus` via URI handler |
| `raise` | show toast + raise host terminal via osfocus chain | toast click → `projmux focus` via URI handler |

Click handling is always wired regardless of mode — the URI handler
registration is gated on its own one-shot marker, not on the mode.

#### Retrospective — what the working configuration is

An earlier Tier-1.5 spike explored a COM Toast Activator path (PR-H
tree: the `feat/toast-com-activator` branch and tooling under
`tools/win-toast-activator`). That path is **abandoned**: an unpackaged
Win32 binary running under WSL cannot reliably take a
`INotificationActivationCallback` dispatch even with a fully-wired
registry chain. The configuration that works in live testing on this
machine is:

1. **No COM activator** at all. The shortcut writes only
   `PKEY_AppUserModel_ID` (pid=5) and intentionally omits
   `PKEY_AppUserModel_ToastActivatorCLSID` (pid=26). When a COM activator
   is registered, Windows tries COM first and silently fails — it does
   not fall through to the launch URI. Stripping the COM side makes
   Windows ShellExecute the URI on click.
2. **AppID-tagged shortcut present** (`projmux.lnk` in the Start Menu's
   Programs folder, target `cmd.exe /c exit`). The toast platform
   requires an AppID-tagged shortcut to be discoverable for the toast to
   route under the right DisplayName + icon. The target is never
   launched — it is a property bag.
3. **Shortcut target = `cmd.exe /c exit`**. Earlier code used
   `powershell.exe -WindowStyle Hidden -Command exit`. Windows Defender
   silently quarantines such shortcuts within seconds of creation, which
   leaves no AppID-tagged shortcut at all and breaks both the toast
   routing and (since the AppID has to be live when the toast fires) the
   click path. `cmd.exe /c exit` is benign and survives.
4. **WSL handler command uses a GUI launcher and keeps `--exec`**. The
   registry protocol handler launches `wscript.exe //B //Nologo` with a
   VBScript launcher written under `%LOCALAPPDATA%\projmux`, so ShellExecute
   does not start a console-subsystem first process. The launcher starts a
   hidden `%ComSpec% /d /s /c` command through `WScript.Shell.Run`; that cmd
   command invokes `wsl.exe` after stripping caret escapes from URI query
   separators. Inside that command, `wsl.exe -- <cmd>` remains forbidden
   because it routes its tail through the user's default login shell, which
   parses `&` query-string separators as background-job operators (zsh emits
   `parse error near '&'`). `--exec` skips the shell and invokes the binary
   directly. PATH is empty under `--exec`, so the registry command uses the
   absolute WSL filesystem path captured at registration time. The `%1` URI is
   passed as a WScript argument and then forwarded as the `--uri` argv value,
   not shell-interpolated.

#### Lessons (so future readers don't repeat them)

- Do not "fix" the shortcut target back to `powershell.exe -WindowStyle
  Hidden -Command exit`. It triggers Defender quarantine and breaks
  every routing path that depends on the AppID shortcut existing.
- Do not add `PKEY_AppUserModel_ToastActivatorCLSID` (pid=26) thinking
  it would "unlock click activation". For unpackaged binaries it does
  the opposite — Windows routes through the COM path, the COM call
  silently fails, and the launch URI is never invoked.
- Do not use `wsl.exe -- projmux ...` in the registry handler.
  Re-introducing the login-shell hop will surface as `parse error near
  '&'` from the user's shell at click time.
- The `@projmux_uri_protocol_registered_v6` marker exists because v5 invoked
  `wsl.exe` directly from WScript with quoted fixed arguments, which avoided
  flash but broke the focus command. Re-registration is idempotent so upgrades
  from v5 transparently install the new handler. Once v6 registration
  succeeds, projmux removes the legacy URI marker keys from v1 through v5 so
  old handler generations do not linger in tmux state.

Multi-distro dispatch (one handler per distro, or a distro-selector
arg) is a known tier-2 follow-up — current registration captures the
first distro to fire a toast and pins the handler to it. See
docs/configuration.md → "Toast click handler" for the limitation
summary.

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
- Retired: `internal/integrations/osfocus/` — was the home for the OS /
  terminal focus adapters; removed in 0.11.0 along with the `raise` mode.
