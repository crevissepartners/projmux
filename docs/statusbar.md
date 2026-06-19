# Statusbar

`projmux shell` configures tmux with `status 2` and renders a two-line
clickable status bar. The same dispatcher (`projmux statusbar click`)
handles both mouse clicks and the keyboard chord, so adding a new
segment only requires one wiring point.

The built-in fallback colors used by the statusbar are semantic tokens in
`internal/theme/palette.go`; see [theme-palette.md](theme-palette.md) for the
truecolor to tmux 256-color mapping policy and palette inventory.

## Layout

```
row 0  #[range=user|notify] <notify HUD pill> #[norange]
                                      #[range=user|usage] <usage HUD bar> #[norange]
row 1  [#S]  #{pane_current_path}  ⎈ <ctx>/<ns>  <git>   %H:%M
       └────────── native tmux window list (one entry per window) ──────────┘
```

- Row 0 splits the line with `#[align=left]` (the pending AI notify
  queue, capped at 80 cells) and `#[align=right]` (usage, capped at 120
  cells). `notify` is the explicit-ack pending queue; live pane attention
  badges are a separate state surface.
- Row 1 keeps tmux's native `window-status-format` so clicking a tab
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
  Each window tab reserves a one-cell live pane attention prefix from
  `projmux attention window #{window_id}` before the index. AI panes use the
  semantic `@projmux_ai_badge_kind` first: approval/input-required panes use
  the action-required amber-orange role, response-complete panes use the
  non-critical success green role, and in-progress panes use the progress
  yellow role. Red/critical is reserved for error, failure, and risk chrome;
  permission or input-required status badges do not use it.
  Legacy busy/reply title and attention-state markers remain the fallback, and
  no-state windows render a blank placeholder so title alignment and click range
  width stay stable. This live window-list badge is independent from the row-0
  notify queue segment and from notify queue severity or desktop notification
  urgency. For example, an approval request may remain a critical queued
  notification while its live status badge renders action-required amber-orange.
  Pane focus hooks and `projmux attention clear` consume only the
  response-complete live badge, including stale `@projmux_ai_state=waiting`
  fallback state; action-required and in-progress live badges remain visible.
  Window-list badges and app pane-border badges use the same semantic priority,
  with display style controlled by Settings > Appearance > AI badge style and persisted in
  `~/.config/projmux/ai-badge-style`. The default is `dot`; `emoji` renders
  `⏳` for approval/input-required, `✅` for response-complete, and `🔄` for
  in-progress. `off` (also accepted as `minimal` when read from disk) preserves
  the same spacing without drawing a marker.
  The session, pwd, kube, and git segments on this row are wrapped
  in `#[range=user|<id>]` ranges and dispatched through the projmux
  handler. The standalone config also wraps the right-side `projmux`
  badge as the `settings` range; the app config renders a compact
  `` settings chip after the clock. The git segment shows the current
  branch or detached commit,
  then compact state indicators when available: `*` for local changes,
  `+N` for staged entries, and `↑N`/`↓N` for ahead/behind counts. Each
  state token gets its own compact foreground color on the same muted branch
  block; the indicators drop bold styling so git state remains readable
  without dominating the status row. Window tab indexes stay left of each tab,
  and tab titles are centered in a fixed-width trim so long active pane names
  do not resize the status row.
- The settings chip keeps its label padding inside the `settings` range
  and inside the chip background. The compact app chip renders `` with
  the extra right-side icon padding painted by the same background, while
  the standalone ` projmux` label keeps symmetric one-cell label padding.
  The chip does not append an extra default-styled trailing space after
  `#[default]`, so the painted button reaches the status row's right edge.
- Both HUD segments degrade gracefully when the cell budget is tight; see
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
| `usage`   | 1 | show a native-framed usage HUD popup from cached usage state | `prefix s u`  |
| `notify`  | 1 | `projmux focus --target <newest> --source status-bar --kind segment-click [--client <tty>]`, then ack on focus success | `prefix s n`  |

`notify` reads the pending queue only. For a live pane-state view that is
independent of queued reminders, use `projmux attention list`. To explain why
a live reply badge and the queue disagree, use `projmux notify list --live`.
The notify segment renders the newest queued item as a single notification
block: project, state (`NEED`/`INFO`/`WARN`/`CRIT`), optional agent, text,
age, and `+N` for older pending entries. Window/pane ids are not shown in the
compact status segment.
The compact age text is locale-formatted through `internal/i18n` (`2m ago` in
`en-US`, `36초 전` in `ko-KR`). AI notify body text uses catalog-owned category
labels while preserving agent names, commands, paths, URLs, and provider
payload excerpts. The `+N` older-entry count remains a numeric compact badge so
it does not expand the status segment.
When the notify block is wider than its cell budget, clipping shrinks the body
text first and appends an ellipsis while preserving project, state, agent, age,
and count metadata. If the segment is still too wide, the age is dropped next
while badges and the `+N` count stay visible when possible. Very narrow widths
fall back to dotless clipped text plus the `+N` count when it fits; the final
hard-truncate path still closes with `#[default]` so later status segments do
not inherit notification styling. That dotless narrow fallback applies only to
the queued notify segment, not to the separate window-list live attention badge.
`usage` opens a native-framed detail HUD for the compact usage bar. It reads
the cached usage state in-process, keeps the existing `projmux usage` CLI
output shape unchanged for external consumers, aligns model/window rows with
right-aligned numeric values, dims unavailable values, keeps stale sync/age
metadata muted, and colors only threshold values: amber at 80% and red at 95%.
Session State inspection lives under `Projects > Sessions > State`; global
Settings > Session State is settings-only and the statusbar no longer exposes a
duplicate State button.

The path popup uses the native picker frame chrome, a one-line title,
the full wrapped current path, cheap project/git metadata when available, and
an `Enter closes this popup` prompt. The click is display-only: it does not
invoke system clipboard tools and does not write a tmux paste buffer. The
popup command prints one quoted payload and waits for a plain Enter read so it
does not leave terminal key state behind. The usage popup uses the same
single-payload print and plain Enter-close pattern. It shows the authoritative
last collect timestamp when present, falls back to the cache file mtime when
needed, and keeps stale sync metadata muted instead of escalating it to a
warning color.
The notification HUD detail surface opens the right-side notification popup
through the notify sidebar action, showing the grouped pane/session inbox with
collapsed group rows and the same attention-tinted title. When notification
icon decoration is `symbol` or `emoji`, the bell appears before the title text.
Foldable group rows show `+N`, where `N` is the number of child notification
rows shown after Right. One-notification group headers omit the count and do
not render as strongly foldable. Right/Left show and hide child rows locally
inside the native sidebar; Right on a childless group refreshes without adding
rows. Enter on a group row, whether folded or expanded, focuses the group's
representative pane and acknowledges every visible notification in that group
only after focus succeeds. If the representative target is stale or gone,
Enter cleans up the selected group without focusing and acknowledges every
visible notification in that group, including critical notifications. A
target-gone focus race follows the same cleanup policy; other focus failures
keep the group pending and show a clear message before refresh/prune. Enter on
a child notification preserves the existing focus and ack-one behavior.
`NotifySidebar:AckGroup` remains the explicit group ack action and acknowledges
every visible notification in the selected group, including critical
notifications.
Internal notify commands use `NotifySidebar:*` IDs in `keymap.toml`; runtime
footers render key guides from the merged keymap and prefer the default alias
when it is still configured.

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

Settings > Appearance controls optional icon decoration separately for the
path/cwd marker, git branch marker, and notification sidebar header. Each row
can be `off` (default, font-safe), `symbol` (Nerd Font-style folder,
git-provider, or bell icon), or `emoji`. Git branch decoration follows
`remote.origin.url`: GitHub remotes use a cat-style mark, GitLab remotes use a
fox-style mark, and other remotes use a generic git branch mark. Settings also
updates the matching live tmux option
(`@projmux_statusbar_decoration_cwd`, `_git`, or `_notify`) when run inside
tmux. The legacy `~/.config/projmux/statusbar-decoration` and
`@projmux_statusbar_decoration` remain fallback defaults for older configs.
Appearance also shows the effective desired theme font. This is a status row,
not a font editor: tmux status strings and ANSI output cannot force terminal
font family or size, so unsupported environments report `not applied`.

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
