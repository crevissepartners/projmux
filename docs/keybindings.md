# Terminal Keybindings

projmux is keyboard-driven. `projmux shell` writes a tmux config that owns
every binding listed here, so the keys are live the moment the app starts —
no extra setup in most terminals.

The recommended path when a key does not fire:

1. Press the key inside `projmux shell` and see what works on your terminal
   out of the box ([Quick start](#quick-start-no-setup)).
2. If something is swallowed, run [`projmux setup`](#diagnose-projmux-setup) —
   it tells you exactly which sequences reach the process and which the
   terminal is eating.
3. For terminals projmux knows how to configure, run
   [`projmux init [terminal]`](#auto-config-projmux-init) to merge the right
   bindings into your terminal config (dry-run by default, `--apply` writes a
   timestamped backup).
4. If your terminal is not in the init list (or you prefer to edit configs by
   hand), use the [Manual fallback / advanced (CSI-u)](#manual-fallback--advanced-csi-u)
   section.

> 한국어 요약: 대부분의 터미널은 `projmux shell` 만으로 아래 키가 바로 동작합니다.
> 동작하지 않으면 `projmux setup` 으로 어떤 키가 막혔는지 진단하고,
> `projmux init [terminal]` 으로 자동 설정하세요. 자동 설정이 없는 터미널은
> 마지막 [Manual fallback / advanced (CSI-u)](#manual-fallback--advanced-csi-u)
> 섹션의 매핑표를 참고해 직접 설정하면 됩니다.

The tmux **prefix** is the upstream default `Ctrl-b`. Inside the running
session, `Ctrl-b ?` lists every binding live.

## Quick start (no setup)

These shortcuts are bound by projmux's generated tmux config. On terminals
that pass `Alt-*` and `Ctrl-*` chords through to the running process
unchanged (e.g. Linux gnome-terminal, foot, most stock Linux terminals),
they work the moment you launch `projmux shell` — no terminal config
required.

If a key does nothing, jump to
[Diagnose: `projmux setup`](#diagnose-projmux-setup) instead of guessing.

### Pickers

These open the projmux popups and the sidebar. No prefix needed.

| Shortcut | Action |
| --- | --- |
| `Alt-1` | Project sidebar |
| `Alt-2` | Existing session popup |
| `Alt-3` | Project switcher popup |
| `Alt-4` | AI split picker |
| `Alt-5` | Settings |

The same surfaces are also available from the prefix table:

| Shortcut | Action |
| --- | --- |
| `Prefix F` | Project sidebar |
| `Prefix b` | Existing session popup |
| `Prefix f` | Project switcher popup |
| `Prefix g` | Jump to the current pane's project session |

### Windows, panes, AI splits

| Shortcut | Action |
| --- | --- |
| `Ctrl-n` | New tmux window in the current pane's directory |
| `Alt-Shift-Left` / `Alt-Shift-Right` | Previous / next window |
| `Alt-Left` / `Right` / `Up` / `Down` | Move focus between panes |
| `Alt-r` | Rename the current window |
| `Prefix R` | Rename the current window |
| `Prefix r` | Open an AI split to the right |
| `Prefix l` | Open an AI split below |

When a pane closes, projmux re-spreads remaining panes so the surviving split
does not stretch lopsided.

### Inside the pickers

| Surface | Shortcut | Action |
| --- | --- | --- |
| Existing session popup | `Ctrl-X` | Kill the focused session and reopen the popup |
| Existing session popup | `Left/Right` | Preview previous/next window |
| Existing session popup | `Alt-Up/Alt-Down` | Preview previous/next pane |
| Project switcher | `Ctrl-X` | Kill the focused existing session and reopen the picker |
| Project switcher | `Alt-P` | Pin or unpin the focused directory |

### Conditional rename keys

These two only fire if your terminal forwards CSI-u sequences for `Ctrl-M`
and `Ctrl-Shift-M`. Without that wiring, plain `Ctrl-M` is just `Enter` and
`Ctrl-Shift-M` typically does nothing. `projmux init` configures this for
the terminals it supports; otherwise see
[Manual fallback / advanced (CSI-u)](#manual-fallback--advanced-csi-u).

| Shortcut | Action |
| --- | --- |
| `Ctrl-M` | Rename the current window (terminal must send `User10`) |
| `Ctrl-Shift-M` | Rename the current AI pane label / topic (terminal must send `User11`) |

## Diagnose: `projmux setup`

Run `projmux setup` *outside* tmux (in your raw terminal window) to find out
which projmux keys actually reach the process. The command auto-detects your
terminal, then asks you to press each shortcut in turn and classifies the
result:

| Status | Meaning |
| --- | --- |
| `OK plain` | The terminal forwarded the bare escape (e.g. `\x1b1` for `Alt-1`); tmux's plain bind handles it. |
| `OK csi-u` | The terminal already forwards a `ESC [ NNNNu` sequence routed to a tmux `User*` key. You are fully wired. |
| `MISS timeout` | No bytes arrived — the terminal swallowed the key for its own action. |
| `MISS unknown` | Something arrived, but it does not match either expected sequence — the terminal bound this combo to a different action. |

The summary at the end lists the failing keys and a remediation hint
tailored to the detected terminal (Ghostty, WezTerm, kitty, iTerm2,
Alacritty, Windows Terminal, foot, VS Code, …). When projmux ships an init
adapter for the terminal, the hint ends with the exact command to run, e.g.
`projmux init ghostty`.

Useful flags:

```sh
projmux setup                       # interactive probe (default)
projmux setup --timeout 10s         # wait longer per key
projmux setup --non-interactive     # just print the expected key map and exit
```

`--non-interactive` is handy when you only want to see, on this machine and
shell, which `plain` and `csi-u` byte sequences projmux is listening for —
useful for hand-rolling a config in a terminal projmux does not yet know
about.

> 한국어 요약: `projmux setup` 을 tmux *밖에서* 실행하면, 진단 대상 키를 차례로
> 누르며 어떤 키가 plain/CSI-u 로 도달하는지, 어떤 키가 swallow 되는지 표로
> 알려줍니다. 끝에서 터미널별 다음 단계를 제안합니다.

## Auto-config: `projmux init`

`projmux init` merges the projmux CSI-u keybindings into a terminal
emulator's config file. Default mode is **dry-run** — nothing is written
until you pass `--apply`. Applying the merge always creates a timestamped
backup of the previous file.

```sh
projmux init                              # auto-detect + dry-run preview
projmux init ghostty                      # explicit terminal, dry-run preview
projmux init ghostty --apply              # write changes (with .bak.<ts> backup)
projmux init --config /path/to/file       # bypass auto-detected paths
projmux init --allow-symlink              # opt in to merging through a symlink
projmux init --dry-run                    # force dry-run even with no other flag
```

The merge is idempotent: bindings already pointing at the desired action are
no-ops, missing bindings are appended in a managed block, and triggers
already mapped to a *different* action are left untouched and reported as
`skip-conflict` warnings — projmux never silently overwrites your custom
mappings. Re-running `projmux init --apply` after editing the file refreshes
just the projmux-owned region.

### Supported terminals

#### Ghostty

- Detection: `TERM_PROGRAM=ghostty` or `GHOSTTY_RESOURCES_DIR` set.
- Config candidates (resolved in this order):
  1. `<config-dir>/ghostty/config` — canonical default
  2. `<config-dir>/ghostty/config.ghostty` — common dotfiles convention

  `<config-dir>` honours `$XDG_CONFIG_HOME` first, then `$HOME/.config`.
  When both candidates exist, init refuses to guess and asks for
  `--config <path>` to disambiguate.
- Idempotency marker: bindings live inside a managed block delimited by

  ```text
  # >>> projmux managed keybindings (do not edit between markers)
  ...
  # <<< projmux managed keybindings
  ```

  Edit anything *outside* those markers freely; init re-renders the inside
  on every `--apply`.
- Symlink guard: if the resolved config path is a symlink (typical when
  dotfiles users symlink the terminal config into a tracked repo), init
  refuses by default to avoid silently mutating the symlink target. Pass
  `--allow-symlink` to opt in, or `--config <path>` to point at a
  non-symlinked file.

#### Windows Terminal (including WSL)

- Detection: `WT_SESSION` set (native Windows), or `WSL_DISTRO_NAME` /
  `WSL_INTEROP` set (running from inside WSL where the host terminal is
  Windows Terminal).
- Config resolution:
  - Native Windows: `%LOCALAPPDATA%\Packages\Microsoft.WindowsTerminal_*\LocalState\settings.json`.
  - WSL: resolves `%LOCALAPPDATA%` through `cmd.exe /c echo %LOCALAPPDATA%`
    interop, then translates `C:\Users\...` into `/mnt/c/Users/...` so the
    Linux side can read/write the file directly.
- Merge model: settings.json is JSONC (allows `//` and `/* */` comments,
  trailing commas). Init parses, locates `actions[]` and `keybindings[]`,
  and merges entries identified by the `User.projmux*` ID prefix. Anything
  outside that prefix is preserved verbatim, including comments and
  formatting where reasonable.
- Conflicts: a key already mapped to a non-projmux action is skipped with a
  warning, same as Ghostty.

### Reading dry-run output

Each line of the dry-run plan prefixes the action with one of:

| Prefix | Meaning |
| --- | --- |
| `+` | New binding will be added. |
| `=` | Binding already matches; no change. |
| `!` | Trigger is already mapped to a *different* action; will be skipped. Resolve manually before applying. |

The trailing `note:` lines flag whether the config file does not yet exist
(it will be created), and the final line tells you whether you still need
`--apply` or whether the file is already up to date.

> 한국어 요약: `projmux init` 은 dry-run 이 기본이고 `--apply` 시 timestamped
> 백업 후 머지합니다. Ghostty 는 managed 블록 마커로 idempotent, 두 표준
> path 모두 인식하며 symlink 는 default 거절. Windows Terminal 은 WSL interop
> 으로 `/mnt/c/...` 경로까지 알아서 풀고, JSONC 형식의 `settings.json` 에
> `User.projmux*` prefix 로 머지합니다. 사용자가 같은 키를 다른 액션에 매핑한
> 경우 skip + warning.

## Manual fallback / advanced (CSI-u)

Use this section when:

- your terminal is not yet supported by `projmux init`, or
- you prefer to manage the config by hand, or
- `projmux setup` shows specific keys still being swallowed and you want to
  redirect just those.

The fix is to map the swallowed keystroke to a CSI-u sequence the terminal
forwards to tmux unchanged. projmux binds CSI-u escapes to tmux's
`User0`–`User11` keys, so once the terminal forwards the sequence the
action fires.

### CSI-u Map

| CSI-u sequence | tmux key | Action |
| --- | --- | --- |
| `ESC [ 9001 u` | `User0` | Open AI split to the right |
| `ESC [ 9002 u` | `User1` | Open AI split below |
| `ESC [ 9003 u` | `User2` | Existing session popup |
| `ESC [ 9004 u` | `User3` | Project switcher popup |
| `ESC [ 9005 u` | `User4` | Project sidebar |
| `ESC [ 9006 u` | `User5` | AI split picker |
| `ESC [ 9007 u` | `User6` | Settings |
| `ESC [ 9008 u` | `User7` | New tmux window in the current pane directory |
| `ESC [ 9009 u` | `User8` | Previous tmux window |
| `ESC [ 9010 u` | `User9` | Next tmux window |
| `ESC [ 9011 u` | `User10` | Rename the current tmux window |
| `ESC [ 9012 u` | `User11` | Rename the current tmux pane label |

### Ghostty (manual)

`projmux init ghostty` is the maintained path. The block below is what the
managed region in `~/.config/ghostty/config` ends up looking like — useful
if you want to author it by hand or vet what init produces. Ghostty
keybinds use `keybind = trigger=action`; the `csi:` action sends a CSI
sequence without the leading `ESC [` bytes.

```text
keybind = alt+1=csi:9005u
keybind = alt+2=csi:9003u
keybind = alt+3=csi:9004u
keybind = alt+4=csi:9006u
keybind = alt+5=csi:9007u

keybind = ctrl+shift+r=csi:9001u
keybind = ctrl+shift+l=csi:9002u

keybind = ctrl+shift+n=csi:9008u
keybind = ctrl+m=csi:9011u
keybind = ctrl+shift+m=csi:9012u
keybind = alt+shift+left=csi:9009u
keybind = alt+shift+right=csi:9010u
```

Reload Ghostty or restart the terminal after changing the config.

### Windows Terminal (manual)

`projmux init windows-terminal` (which also covers WSL) is the maintained
path. Use the snippet below to author `settings.json` by hand. Add
`sendInput` actions and bind them from `keybindings`. Windows Terminal
works well with plain tmux escape sequences for the default `Alt`
shortcuts, while the split actions can send tmux prefix sequences directly. Escape bytes should be written as `\u001b`.

```json
{
  "actions": [
    { "command": { "action": "sendInput", "input": "\u001b1" }, "id": "User.projmuxSidebar" },
    { "command": { "action": "sendInput", "input": "\u001b2" }, "id": "User.projmuxSessions" },
    { "command": { "action": "sendInput", "input": "\u001b3" }, "id": "User.projmuxSwitch" },
    { "command": { "action": "sendInput", "input": "\u001b4" }, "id": "User.projmuxAIPicker" },
    { "command": { "action": "sendInput", "input": "\u001b5" }, "id": "User.projmuxSettings" },
    { "command": { "action": "sendInput", "input": "\u0002r" }, "id": "User.projmuxAISplitRight" },
    { "command": { "action": "sendInput", "input": "\u0002l" }, "id": "User.projmuxAISplitDown" },
    { "command": { "action": "sendInput", "input": "\u000e" }, "id": "User.projmuxNewWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[1;4D" }, "id": "User.projmuxPrevWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[1;4C" }, "id": "User.projmuxNextWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[9011u" }, "id": "User.projmuxRenameWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[9012u" }, "id": "User.projmuxRenamePane" }
  ],
  "keybindings": [
    { "id": "User.projmuxSidebar", "keys": "alt+1" },
    { "id": "User.projmuxSessions", "keys": "alt+2" },
    { "id": "User.projmuxSwitch", "keys": "alt+3" },
    { "id": "User.projmuxAIPicker", "keys": "alt+4" },
    { "id": "User.projmuxSettings", "keys": "alt+5" },
    { "id": "User.projmuxAISplitRight", "keys": "ctrl+shift+r" },
    { "id": "User.projmuxAISplitDown", "keys": "ctrl+shift+l" },
    { "id": "User.projmuxNewWindow", "keys": "ctrl+n" },
    { "id": "User.projmuxPrevWindow", "keys": "alt+shift+left" },
    { "id": "User.projmuxNextWindow", "keys": "alt+shift+right" },
    { "id": "User.projmuxRenameWindow", "keys": "ctrl+m" },
    { "id": "User.projmuxRenamePane", "keys": "ctrl+shift+m" }
  ]
}
```

This example matches the default projmux app shortcuts without depending on
CSI-u support from Windows Terminal for the `Alt` shortcuts. `Ctrl-M` and
`Ctrl-Shift-M` still use CSI-u so tmux can distinguish rename commands from
plain Enter. If a key is already bound by Windows Terminal, remove or change
the conflicting `keybindings` entry before adding the projmux binding.
