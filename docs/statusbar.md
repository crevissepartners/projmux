# Statusbar

`projmux shell` configures tmux with `status 2` and renders a two-line
clickable status bar. The same dispatcher (`projmux statusbar click`)
handles both mouse clicks and the keyboard chord, so adding a new
segment only requires one wiring point.

## Layout

```
row 0  [#S] #{pane_current_path}  ⎈ <ctx>/<ns>  <git>               %H:%M
       └────────── native tmux window list (one entry per window) ──────────┘
row 1  #[range=user|notify] <notify HUD pill> #[norange] #[range=user|sessionstate]state#[norange]
                                           #[range=user|usage] <usage HUD bar> #[norange]
```

- Row 0 keeps tmux's native `window-status-format` so clicking a tab
  selects the window. The bind uses `if-shell -F
  "#{==:#{mouse_status_range},window}"` to run `select-window -t =`
  natively when the click lands on the window list, so the
  mouse-target context resolves the clicked window directly. All
  other ranges fall through to `run-shell projmux statusbar click
  ...`, which dispatches by range id. The in-config short-circuit
  is required because `#{mouse_window}` is empty for window-list
  clicks on tmux 3.4+, so a `run-shell` handler can't recover the
  target after the fact — the Go dispatcher's
  `isWindowListRangeToken` fallback is now defense-in-depth only.
  The session, pwd, kube, and git segments on this row are wrapped
  in `#[range=user|<id>]` ranges and dispatched through the projmux
  handler. The standalone config also wraps the right-side `projmux`
  badge as the `settings` range; the app config renders a compact
  `⚙` settings chip after the clock. The git segment shows the current
  branch or detached commit,
  then compact state indicators when available: `*` for local changes,
  `+N` for staged entries, and `↑N`/`↓N` for ahead/behind counts. Each
  state token gets its own compact foreground color while preserving the
  existing branch block background.
- Row 1 splits the line with `#[align=left]` (the pending AI notify
  queue, capped at 80
  cells, plus the compact `state` entrypoint) and `#[align=right]` (usage, capped at 120 cells). `notify` is the
  explicit-ack pending queue; live pane attention badges are a separate
  state surface. Both
  segments degrade gracefully when the cell budget is tight; see
  [notify-queue.md](notify-queue.md) and [usage-tracking.md](usage-tracking.md)
  for the per-segment tier ladder.

A single tmux bind handles both lines:

```tmux
bind-key -n MouseDown1Status if-shell -F "#{==:#{mouse_status_range},window}" \
  { select-window -t = } \
  { run-shell "'<projmux>' statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\" --mouse-window \"#{mouse_window}\"" }
```

`MouseDown1Status` fires from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to the range under the cursor.

## Range catalogue

| Range id | Row | Click action                              | Keyboard      |
| -------- | --- | ----------------------------------------- | ------------- |
| `session` | 0 | `projmux tmux popup-toggle sessionizer-sidebar` | `prefix s s` |
| `pwd`     | 0 | show a native-framed current-path popup; no clipboard or tmux buffer copy | `prefix s p`  |
| `kube`    | 0 | `projmux tmux popup-toggle sessionizer`   | `prefix s k`  |
| `git`     | 0 | `projmux tmux popup-toggle sessionizer`   | `prefix s g`  |
| `settings` | 0 | `projmux tmux popup-toggle --client <tty> ai-split-settings` | mouse only; `prefix s s` remains `session` |
| `sessionstate` | 1 | show a secondary Session State snapshot status and restore preview popup; primary inspection lives under Settings > Project > Session State | `prefix s r` |
| `usage`   | 1 | show a native-framed usage HUD popup from cached usage state | `prefix s u`  |
| `notify`  | 1 | `projmux focus --target <newest> --source status-bar --kind segment-click [--client <tty>]`, then ack on focus success | `prefix s n`  |

`notify` reads the pending queue only. For a live pane-state view that is
independent of queued reminders, use `projmux attention list`. To explain why
a live reply badge and the queue disagree, use `projmux notify list --live`.
The notify segment renders the newest queued item as a single notification
block: project, state (`NEED`/`INFO`/`WARN`/`CRIT`), optional agent, text,
age, and `+N` for older pending entries. Window/pane ids are not shown in the
compact status segment.
`usage` opens a native-framed detail HUD for the compact usage bar. It reads
the cached usage state in-process, keeps the existing `projmux usage` CLI
output shape unchanged for external consumers, aligns model/window rows with
right-aligned numeric values, dims unavailable values, and colors rows at the
same alert thresholds as the popup: amber at 80% and red at 95%.
`sessionstate` opens a read-only snapshot status popup for the current tmux
session. It shows the effective auto-save and startup picker toggles, saved
timestamp and age, window/pane counts, default cwd, and a bounded preview of
windows, panes, recipe kinds, agent resume ids, and startup commands without
dumping the raw JSON snapshot. Mutating actions remain in Settings, including
delete confirmation.

The path popup uses the native picker frame chrome, a one-line title,
the full wrapped current path, cheap project/git metadata when available, and
an `Enter closes this popup` prompt. The click is display-only: it does not
invoke system clipboard tools and does not write a tmux paste buffer. The
popup command prints one quoted payload and waits for a plain Enter read so it
does not leave terminal key state behind. The usage popup uses the same
single-payload print and plain Enter-close pattern. It shows the authoritative
last collect timestamp when present, falls back to the cache file mtime when
needed, and colors that sync line amber once it is more than 60 seconds old.
The notification HUD detail surface
(`Alt-2` / `User2`) opens the
right-side notification popup with newest-first rows. The popup itself is
untitled; when decoration mode is `symbol` or `emoji`, the bell appears before
the fzf header text instead. Selecting a row still focuses and acknowledges
that notification.

Empty `#{mouse_status_range}` (a click on whitespace) falls through to
`select-window -t @<mouse_window>` when `--mouse-window` is non-empty,
otherwise it is a no-op. Unknown user range ids are non-specialized
placeholder surfaces and no-op until a handler is wired into the dispatcher.

## Keyboard chord

```tmux
bind-key s switch-client -T projmux-status
bind-key -T projmux-status u run-shell '#{q:projmux} statusbar click usage'
bind-key -T projmux-status n run-shell '#{q:projmux} statusbar click notify'
bind-key -T projmux-status g run-shell '#{q:projmux} statusbar click git'
bind-key -T projmux-status k run-shell '#{q:projmux} statusbar click kube'
bind-key -T projmux-status p run-shell '#{q:projmux} statusbar click pwd'
bind-key -T projmux-status r run-shell '#{q:projmux} statusbar click sessionstate'
bind-key -T projmux-status s run-shell '#{q:projmux} statusbar click session'
```

The chord routes through the same dispatcher as the mouse click, so
keyboard and mouse paths are functionally identical for keyed ranges.
There is intentionally no `prefix s s` settings fallback because that chord
already opens the session/sidebar range.

## Click failure handling

Every status-bar click runs from tmux's `run-shell`. A non-zero exit
there triggers a tmux error popup, which is hostile UX for a casual
click. Each handler therefore swallows runtime failures and surfaces
them as `display-message` toasts:

- `notify` click whose focus dispatch exits 2 (target unresolved):
  ack the entry, toast `notify target gone; cleared`.
- Any other focus failure: keep the entry, toast `focus failed:
  <reason>`.
- `session`, `kube`, or `git` popup launch failure: toast
  `statusbar <range>: popup failed`.
- `settings` popup launch failure: toast `statusbar settings: popup failed`.
- `pwd` path popup failure: fall back to a short `display-message`
  containing the current path.
- `sessionstate` popup failure: fall back to a compact snapshot status
  `display-message`.
- `usage` popup failure: fall back to a compact usage summary
  `display-message`.

`MouseX` / `MouseY` are accepted but not consumed today; the fields
are wired through so click telemetry can land without changing the
bind.

## Customizing

The status bar is generated by `projmux tmux print-config` /
`print-app-config`. To change a segment, edit the generator (it
emits a deterministic block per segment), regenerate, and re-apply:

```sh
projmux tmux apply
```

Settings > Appearance controls the optional decoration mode used by
the path, git branch, and notification sidebar header. The persisted enum lives at
`~/.config/projmux/statusbar-decoration`; valid values are `off` (default,
font-safe), `symbol` (Nerd Font-style folder/GitHub/bell icons), and `emoji`.
Settings also updates tmux `@projmux_statusbar_decoration` for the live
server when run inside tmux.

To add a new clickable segment:

1. Add a `statusbarRangeID` constant in
   `internal/app/statusbar.go`.
2. Wire its handler into `dispatchTable`.
3. Update the generator so the segment is wrapped in
   `#[range=user|<id>]...#[norange]` on the chosen row.
4. Add a chord key under `projmux-status` if a keyboard binding is
   wanted.

Custom user ranges can coexist with the built-in window-list range:
the dispatcher detects the `window` / `window|<idx>` token before
the user-range table, so a hostile range named `window` cannot
shadow the built-in.
