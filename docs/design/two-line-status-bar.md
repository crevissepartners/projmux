## Two-line clickable status bar

projmux configures tmux with `status 2`. Line 0 is the existing
session/window/path/git/clock row. Line 1 splits the notification bar
(left half, capped at 80 cells) and the AI usage HUD (right half, capped at
120 cells) using tmux `#[align=left]` / `#[align=right]`. Each clickable
segment is wrapped in a tmux user-defined range (`#[range=user|<id>]...
#[norange]`) and dispatched through `projmux internal statusbar click <range-id>`. A
single `bind -n MouseDown1Status` covers both lines because tmux fires
`MouseDown1Status` from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to whichever range the cursor was over.

| Range id | Line | Click action                              | Keybinding   |
|----------|------|-------------------------------------------|--------------|
| session  | 0    | popup `projmux runtime sessions --ui=popup` | prefix+s s |
| pwd      | 0    | show pane_current_path in a display-only path popup | prefix+s p   |
| git      | 0    | popup `projmux switch --ui=popup`         | prefix+s g   |
| usage    | 1    | popup `projmux agent usage`               | prefix+s u   |
| notify   | 1    | focus origin pane of newest notification  | prefix+s n   |

The keyboard chord uses `bind-key s switch-client -T projmux-status` so the
prefix-then-`s`-then-letter shortcut routes through the same dispatcher as
the mouse click. Empty `#{mouse_status_range}` (clicks on whitespace) is a
no-op so the binding never flashes a spurious error.
The hardcoded `prefix s r` sibling is usage-specific: it runs the existing
throttled collector and then reopens the same display-only usage popup from
cache.
