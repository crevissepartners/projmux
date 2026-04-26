# Terminal Keybindings

`projmux shell` and `projmux tmux install` generate the tmux bindings used by the
app. In many terminals, `Alt-1`, `Alt-2`, and the other default shortcuts pass
through to tmux without extra setup.

Use this page when your terminal emulator consumes a shortcut before tmux sees
it, or when you want explicit terminal-level bindings. The examples send CSI-u
escape sequences that projmux maps to tmux `User0` through `User10` keys.

한국어 요약: 보통은 별도 설정 없이 `projmux shell`이 생성한 tmux 키가 동작합니다.
터미널이 `Alt-1` 같은 조합을 먼저 가로채면 아래 Ghostty 또는 Windows Terminal
설정을 추가해서 projmux 전용 CSI-u 시퀀스를 tmux로 보내면 됩니다.

## Generated App Bindings

| Shortcut | Action |
| --- | --- |
| `Alt-1` | Open project sidebar |
| `Alt-2` | Open existing session popup |
| `Alt-3` | Open project switcher popup |
| `Alt-4` | Open AI split picker |
| `Alt-5` | Open settings |
| `Ctrl-n` | New tmux window in the current pane directory |
| `Alt-r` | Rename the current tmux window |
| `Alt-Left/Right/Up/Down` | Move between panes |
| `Alt-Shift-Left/Right` | Previous/next window |
| `Prefix b` | Existing session popup |
| `Prefix f` | Project switcher popup |
| `Prefix F` | Project sidebar |
| `Prefix g` | Jump to the current pane project session |
| `Prefix r` | Open AI split to the right |
| `Prefix l` | Open AI split below |
| `Prefix R` | Rename the current tmux window |

When a pane exits, projmux asks tmux to spread the remaining panes evenly in the
window so the previous split does not leave one side oversized.

## Picker Actions

| Surface | Shortcut | Action |
| --- | --- | --- |
| Existing session popup | `Ctrl-X` | Kill the focused session and reopen the popup |
| Existing session popup | `Left/Right` | Preview previous/next window |
| Existing session popup | `Alt-Up/Alt-Down` | Preview previous/next pane |
| Project switcher | `Ctrl-X` | Kill the focused existing session and reopen the picker |
| Project switcher | `Alt-P` | Pin or unpin the focused directory |

## CSI-u Map

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

## Ghostty

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
keybind = alt+r=csi:9011u
keybind = alt+shift+left=csi:9009u
keybind = alt+shift+right=csi:9010u
```

Reload Ghostty or restart the terminal after changing the config.

## Windows Terminal

Add entries to `settings.json` under `actions`. Windows Terminal uses
`sendInput` for arbitrary text input, and escape bytes should be written as
`\u001b`.

```json
{
  "actions": [
    { "keys": "alt+1", "command": { "action": "sendInput", "input": "\u001b[9005u" } },
    { "keys": "alt+2", "command": { "action": "sendInput", "input": "\u001b[9003u" } },
    { "keys": "alt+3", "command": { "action": "sendInput", "input": "\u001b[9004u" } },
    { "keys": "alt+4", "command": { "action": "sendInput", "input": "\u001b[9006u" } },
    { "keys": "alt+5", "command": { "action": "sendInput", "input": "\u001b[9007u" } },

    { "keys": "ctrl+shift+r", "command": { "action": "sendInput", "input": "\u001b[9001u" } },
    { "keys": "ctrl+shift+l", "command": { "action": "sendInput", "input": "\u001b[9002u" } },

    { "keys": "ctrl+shift+n", "command": { "action": "sendInput", "input": "\u001b[9008u" } },
    { "keys": "alt+r", "command": { "action": "sendInput", "input": "\u001b[9011u" } },
    { "keys": "alt+shift+left", "command": { "action": "sendInput", "input": "\u001b[9009u" } },
    { "keys": "alt+shift+right", "command": { "action": "sendInput", "input": "\u001b[9010u" } }
  ]
}
```

If a key is already bound by Windows Terminal, remove or change the conflicting
entry before adding the projmux binding.
