# Terminal Keybindings

projmux is keyboard-driven. `projmux shell` writes a tmux config that owns
every binding listed here, so the keys are live the moment the app starts —
no extra setup in most terminals.

If your terminal emulator swallows `Alt-1`, `Ctrl-M`, or other shortcuts before
tmux sees them, jump to [If your terminal eats shortcuts](#if-your-terminal-eats-shortcuts).

> 한국어 요약: `projmux shell` 만 실행하면 아래 키가 바로 동작합니다. 터미널이
> `Alt-1` 같은 조합을 먼저 가로채는 경우 [If your terminal eats shortcuts](#if-your-terminal-eats-shortcuts)
> 의 Ghostty / Windows Terminal 설정을 참고해 CSI-u 시퀀스를 tmux 로 흘려보내면
> 됩니다.

The tmux **prefix** is the upstream default `Ctrl-b`. Inside the running
session, `Ctrl-b ?` lists every binding live.

## Pickers

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

## Windows, panes, AI splits

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

### Conditional rename keys

These two only fire if your terminal forwards CSI-u sequences (see
[CSI-u Map](#csi-u-map)). Without that wiring, plain `Ctrl-M` is just `Enter`.

| Shortcut | Action |
| --- | --- |
| `Ctrl-M` | Rename the current window (terminal must send `User10`) |
| `Ctrl-Shift-M` | Rename the current AI pane label / topic (terminal must send `User11`) |

## Inside the pickers

| Surface | Shortcut | Action |
| --- | --- | --- |
| Existing session popup | `Ctrl-X` | Kill the focused session and reopen the popup |
| Existing session popup | `Left/Right` | Preview previous/next window |
| Existing session popup | `Alt-Up/Alt-Down` | Preview previous/next pane |
| Project switcher | `Ctrl-X` | Kill the focused existing session and reopen the picker |
| Project switcher | `Alt-P` | Pin or unpin the focused directory |

## If your terminal eats shortcuts

Some terminals consume `Alt-1`, `Ctrl-M`, or `Ctrl-Shift-M` for their own
actions. The fix is to map those keystrokes to a CSI-u sequence the terminal
sends to tmux unchanged. projmux binds CSI-u escapes to tmux's `User0`–`User11`
keys, so once the terminal forwards the sequence the action fires.

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

### Ghostty

Add key bindings to your Ghostty config. Ghostty keybinds use
`keybind = trigger=action`; the `csi:` action sends a CSI sequence without the
leading `ESC [` bytes.

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

### Windows Terminal

Add `sendInput` actions to `settings.json` and bind them from `keybindings`.
Windows Terminal works well with plain tmux escape sequences for the default
`Alt` shortcuts, while the split actions can send tmux prefix sequences
directly. Escape bytes should be written as `\u001b`.

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
