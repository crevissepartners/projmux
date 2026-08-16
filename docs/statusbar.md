# Statusbar

`projmux shell` configures tmux with `status 2` and renders a two-line
clickable status bar. The same dispatcher (`projmux internal statusbar click`)
handles both mouse clicks and the keyboard chord, so adding a new
segment only requires one wiring point. `statusbar` is machine-invoked
plumbing, so it lives in the hidden `internal` namespace; the pre-namespace
`projmux statusbar click` spelling still works for config a previously
installed binary generated.

The built-in fallback colors used by the statusbar are semantic tokens in
`internal/theme/palette.go`; see [theme-palette.md](theme-palette.md) for the
truecolor to tmux 256-color mapping policy and palette inventory.

## Layout

```
row 0  #[range=user|notify] <notify HUD pill> #[norange]
                                      #[range=user|usage] <usage HUD bar> #[norange]
row 1  [#S]  #{pane_current_path}  <git>  CPU 12%  MEM 41%   %H:%M
       └────────── native tmux window list (one entry per window) ──────────┘
```

- Row 0 normally splits the line with `#[align=left]` (the pending AI notify
  queue) and `#[align=right]` (usage). Both cell budgets are derived from
  the width of the client the row is being drawn for, and the row holds back
  notify's *minimum* rather than its design cap — see
  [Row 0 width budgets](#row-0-width-budgets). `notify` is the
  explicit-ack pending queue; live pane attention badges are a separate
  state surface.
  Settings > Appearance > Status Bar controls `Notifications HUD` and `Agent
  Usage HUD` independently. Agent Usage HUD is a View whose `Visible` parent
  contains Claude/Codex/Antigravity provider Views; each provider has its own
  `Visible` plus explicit supported windows (Claude/Codex: `5h`, `Weekly`;
  Antigravity: `Weekly`). Every leaf defaults on. Parent off preserves saved
  children, and returning it on restores them. When only one HUD is visible, its
  sole range receives the full `#{client_width}` budget and the absent range and
  alignment are not emitted. When both are hidden, tmux collapses to one line
  with `status on`,
  moves the native Window row to `status-format[0]`, unsets stale higher rows,
  and keeps the app's quiet Session State autosave job on that surviving row;
  there is no empty HUD row. These toggles hide presentation only and do not
  mutate the Notification queue or usage collection/cache/API state.
  Provider/window visibility also changes only the ambient status projection;
  the cached popup and explicit `agent usage` table/JSON stay lossless. If every
  provider/window is hidden, the usage command emits empty text without
  changing the overall saved HUD value or row-0 structural policy.
- Row 1 keeps tmux's native `window-status-format` so clicking a tab
  selects the window. The bind uses `if-shell -F
  "#{==:#{mouse_status_range},window}"` to run `select-window -t =`
  natively when the click lands on the window list, so the
  mouse-target context resolves the clicked window directly. All
  other ranges fall through to `run-shell projmux internal statusbar click
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
  The session, pwd, and git segments on this row are wrapped
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
- `Settings > Labs > Live system resources` adds the compact
  `CPU N%  MEM N%`
  segment between git and the clock on macOS, Linux, and WSL. It is global,
  default off, and updates with tmux's existing five-second status interval.
  CPU and memory are host-scoped telemetry, not pane, window, project, or
  session attribution. Each value has an independent semantic style: CPU is
  normal below 70%, warning at 70–89%, and critical at 90% or above; memory is
  normal below 75%, warning at 75–89%, and critical at 90% or above. Normal and
  unavailable (`--`) values use the secondary status-text role, warnings use
  the warning role, and critical values use the bold critical role. Severity
  words are omitted. Each percent value, including `%`, occupies one fixed
  four-column slot (`  9%`, ` 15%`, `100%`, or ` --%`), so styling or changing
  either metric cannot move the following segment. Styling one value never
  promotes the other value. The Resource Inspector uses this same classifier
  and semantic roles for host and attributed CPU/memory while rendering
  unavailable metrics as `--` without severity suffixes.
  Linux CPU is the aggregate delta from `/proc/stat`; memory is
  `(MemTotal - MemAvailable) / MemTotal` from `/proc/meminfo`. macOS CPU uses
  the aggregate Mach host tick delta; memory is total physical memory minus
  free and inactive pages. The first CPU
  sample renders `CPU --%` until a second counter sample exists. WSL values are
  the Linux guest/VM view, not whole-Windows host utilization. The generated
  generated config prevents the status subprocess from running while Resources is
  off. CPU reference samples older than 30 seconds are discarded, so
  re-enabling after a pause starts at `CPU --%` instead of showing a long-term
  average. Missing or malformed procfs data degrades to `--` or an empty segment
  without producing a tmux error popup.
  The complete live segment is wrapped in `#[range=user|resources]...#[norange]`.
  Clicking it opens the same canonical client-scoped `resource-inspector`
  popup as the `Resources:Open` keybinding action. Turning Resources off hides only
  the segment; it does not disable a custom action or `projmux resources`.
- The settings chip keeps its label padding inside the `settings` range
  and inside the chip background. The compact app chip renders `` with
  the extra right-side icon padding painted by the same background, while
  the standalone ` projmux` label keeps symmetric one-cell label padding.
  The chip does not append an extra default-styled trailing space after
  `#[default]`, so the painted button reaches the status row's right edge.
- Both HUD segments degrade gracefully when the cell budget is tight; see
  [notify-queue.md](notify-queue.md) for notify's ladder and
  [Usage element drop order](#usage-element-drop-order) below for usage's.

## Row 0 width budgets

Row 0 does not share space with the window list — that lives on
`status-format[1]` — so the whole client width is row 0's budget, and the two
segments split it exactly when both HUDs are visible:

```
notify = min(client_width / 2, clamp(client_width - 140, 20, 80))
usage  = client_width - notify
```

Both `--max-width` arguments are tmux formats, not constants:

```tmux
#(<projmux> internal status notify --max-width <N>)
#(<projmux> internal status usage  --max-width #{e|-:#{client_width},<N>})

N = #{?#{e|<:#{client_width},40},#{e|/:#{client_width},2},
     #{?#{e|<:#{client_width},160},20,
      #{?#{e|<:#{client_width},220},#{e|-:#{client_width},140},80}}}
```

The reservation is notify's **minimum**, not its design cap. 20 cells is the
narrowest budget at which the notify segment still renders body text plus the
`+N` older-entry count, and 140 is `120 + 20` — usage's historical hardcoded
budget plus that floor, so usage recovers exactly the cells it used to get from
a 140-column client up. Above 160 columns notify grows on the surplus until it
reaches its unchanged 80-cell design cap on a 220-column row. Below 40 columns
neither budget can be met and the row is split evenly instead, because
`--max-width 0` means "no truncation" to both renderers, so a zero budget is
read as *unbounded* and overflows the row.

Measured against a real attached client on tmux 3.6 (the number in the middle
column is what `tmux display-message -p '<the format above>'` answered, and the
right column is what that integer then rendered):

| client | notify | usage | Claude weekly bar |
| ---: | ---: | ---: | --- |
| 40 | 20 | 20 | no |
| 60 | 20 | 40 | no |
| 80 | 20 | 60 | no |
| 100 | 20 | 80 | no |
| 120 | 20 | 100 | no |
| 140 | 20 | 120 | no |
| 144 | 20 | 124 | **yes** |
| 160 | 20 | 140 | yes |
| 180 | 40 | 140 | yes |
| 191 | 51 | 140 | yes |
| 200 | 60 | 140 | yes |
| 219 | 79 | 140 | yes |
| 220 | 80 | 140 | yes |
| 240 | 80 | 160 | yes |

Four tmux behaviours this relies on, all verified against a real server
(tmux 3.6) rather than assumed:

1. **tmux expands formats inside `#()` before running the job**, and keeps one
   job per client. Two clients of different widths attached to the same session
   each render row 0 at their own budget. Resizing changes the expanded command,
   which makes tmux re-run that client's job immediately rather than waiting for
   the next `status-interval` tick — the segments repaint at their new budgets as
   soon as the terminal is resized. The re-run is a render from cache; usage
   collection stays behind its own throttle, so resizing cannot drive adapter
   traffic.
2. **The numeric comparison must use the `e|` modifier.** tmux's bare
   `#{<:a,b}` / `#{>:a,b}` are *string* comparisons — `#{>:191,80}` is `0` and
   `#{>:9,10}` is `1` — so a bare comparison would silently never fire. The `<`
   direction is also the fail-safe one: tmux treats an unevaluable condition as
   false, so a build that cannot evaluate the comparisons walks every false
   branch to the literal `80`, the historical reservation. tmux has no `min`/
   `max` operator (`e|` offers `+ - * / m %%` and comparisons only), which is
   why the clamp is spelled as nested conditionals.
3. **A tmux format cannot measure a `#()` job's output.** `#{w:...}` and
   `#{n:...}` measure a *format*, not a job: `#{w:#{client_width}}` is `3` on a
   191-column client, but `#{w:#(printf …)}` and `#{n:#(printf …)}` are both `0`
   — in `display-message` and in a live status line alike, even where the same
   `#()` renders normally on its own. So row 0 cannot learn how long the notify
   segment actually came out and size usage from it; the reservation has to be
   a static derivation from the client width.
4. **When the two sections do not both fit, tmux draws the left one in full and
   clips the right one from its LEFT edge.** Re-verified against this exact
   generated config: budgeting usage at the whole client width on a 191-column
   row with a queued notification drew
   `… · 1분 전   +7░░░] 39% · weekly […`, i.e. the usage segment lost its
   leading `Claude (4m) 5h [███` outright instead of degrading through its own
   drop order. That is why the two budgets sum to exactly the client width
   rather than each being capped independently, and why the reservation cannot
   simply be dropped: the notify segment fills whatever budget it is given while
   anything is queued — its rendered display width equals its budget exactly, up
   to the queue's natural length.

Why the reservation is notify's floor and not its cap: the two segments degrade
on different curves. Notify's ladder is **linear** — from about 32 cells up,
every extra cell buys exactly one more character of body text, and the project
pill, severity badge, age and `+N` count are complete by then. The usage
ladder is **discrete** — 98 cells buys a primary bar per provider, 124 buys
Claude's official weekly bar back, 134 adds the age indicator. A cell moved
from notify to usage can restore a whole provider window; the same cell moved
the other way buys one character.

One thing the budget derivation does *not* fix. Below 140 columns "usage keeps
its historical 120 cells" and "notify keeps a usable segment" are physically
incompatible — 120 usage cells plus any notify at all does not fit — so
notify's 20-cell floor wins there and usage gets `client_width - 20`. That is
never less than the previous even split gave, and strictly more above 40
columns. How the usage segment then spends the cells it does get is the drop
order below.

A single tmux bind handles both lines:

```tmux
bind-key -n MouseDown1Status if-shell -F "#{==:#{mouse_status_range},window}" \
  { select-window -t = } \
  { run-shell "'<projmux>' statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\" --mouse-window \"#{mouse_window}\"" }
```

`MouseDown1Status` fires from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to the range under the cursor.

## Usage element drop order

The usage segment does not pick a whole-segment tier. It starts from its
richest render and sheds **one optional element at a time** until the result
fits its budget. The order below is the drop order; index 1 goes first.

1. `cosmetic age text` — the `(3m)` on a provider that is *not* stale. Per
   provider.
2. `stale age text (the ~ / ~~ marker stays)` — `(3d~~)` collapses to `~~`.
   Only the "how old exactly" text goes. Per provider.
3. `secondary window bar` — the *second* window of a provider that reports two,
   i.e. Claude's weekly next to its 5h. Per provider.
4. `bars (every provider switches to text pairs)` — `5h [████░░░░░░] 42%`
   becomes `5h:42%`. Segment-wide, because a row that mixes bar and text
   providers reads as a rendering bug, and because the text pair is cheap
   enough that every provider's second window comes back with it.
5. `long labels (single-letter fallback)` — `Claude` becomes `C`.
   Segment-wide.

Then, and only then, hard rune-truncation with a trailing `…`.

Two elements are deliberately **absent** from that list, which is what makes
them survive everything in it:

- **The `~` / `~~` staleness marker.** It has no entry, so no width sheds it
  while any listed element still renders. This is the contract PR #620
  established, and rule 2 exists precisely so the age *text* can go without the
  marker going with it.
- **Each provider's official window bar** — 5h when the provider reports one,
  otherwise weekly. Rule 3 only ever touches a *second* window; rules 4 and 5
  change how the official window is drawn, never whether it is drawn. No step
  hides a provider wholesale.

Visibility filtering runs before this classification. Thus hiding `5h` while
keeping `Weekly` makes Weekly the provider's official window; it is not treated
as a secondary bar and cannot be shed by rule 3.

Only hard rune-truncation, below every listed step, can reach either.

Within a per-provider rule the steps run **tail-first** over the canonical
provider order (Claude, Codex, Antigravity), so the provider a user reads first
is the last to lose detail. This is why a 120-cell budget renders
`Claude 5h [bar] · weekly [bar]   Codex 5h [bar]   Antigravity~~ weekly [bar]`
— Codex's second window paid for Claude's — where whole-segment tier selection
dropped both second windows at once and rendered 92 cells into a 120-cell row.

The order lives in **exactly one place in code**: `usageShedOrder` in
`internal/app/usagecmd/usage.go`. The entry names above are that variable's
`name` fields verbatim, and `TestDropOrderMatchesTheDocumentedOrder` reads this
section to prove the two have not drifted.

## Range catalogue

| Range id | Generated row | Click action                              | Keyboard      |
| -------- | ------------- | ----------------------------------------- | ------------- |
| `notify`  | `status-format[0]` | `projmux focus --target <newest> --source status-bar --kind segment-click [--client <tty>]`, then ack on focus success | `prefix s n`  |
| `usage`   | `status-format[0]` | show a native-framed usage HUD popup from cached usage state | `prefix s u`  |
| `session` | `status-format[1]` | `projmux internal tmux popup-toggle sessionizer-sidebar` | `prefix s s` |
| `pwd`     | `status-format[1]` | show a native-framed current-path popup; no clipboard or tmux buffer copy | `prefix s p`  |
| `git`     | `status-format[1]` | `projmux internal tmux popup-toggle sessionizer` | `prefix s g`  |
| `resources` | `status-format[1]` | `projmux internal tmux popup-toggle --client <tty> resource-inspector` | mouse or custom `Resources:Open`; no default key |
| `settings` | `status-format[1]` | `projmux internal tmux popup-toggle --client <tty> ai-split-settings` | mouse only; `prefix s s` remains `session` |

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
the cached usage state in-process and aligns model/window rows with
right-aligned numeric values, dims unavailable values, keeps stale sync/age
metadata muted, and colors only threshold values: amber at 80% and red at 95%.
Antigravity rows keep conversation-local `context` separate from account
`quota/<exact upstream bucket ID>` rows; the popup displays an absolute reset
when provided and otherwise the exact optional relative reset seconds. Opaque
bucket IDs are escaped for terminal/tmux safety and are never assigned a
`5h`/`weekly` cadence. Claude retains aggregate `5h`/`weekly` rows alongside
typed named/model `limits[]` rows in this popup: model-scoped rows display the
exact upstream group plus model display identity with a bounded terminal-safe
label, reset, and per-row age. The compact status line excludes every Claude
named/model row and continues to use only the aggregate official windows.
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
warning color. Percent-only named rows do not synthesize `USED`, `LIMIT`, or
`LEFT` counts.
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
only after focus succeeds. Inactive means an `ai:` queue entry no longer
matches live reply+agent state (its pane still EXISTS in tmux), not that the
row is old; if the target remains routable, Enter and statusbar clicks still
focus and then ack. Gone means the target is unroutable (empty session) or the
row's pane id is absent from the real tmux live pane inventory
(`tmux list-panes -a`). The inventory check is best-effort: an unreadable or
empty/unrecognized tmux reply is treated as "unavailable", so a missing tmux
server never falsely gones routable rows. If the
representative target is gone/unroutable, Enter cleans up the selected group
without focusing and acknowledges every visible notification in that group,
including critical notifications. A
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
bind-key -T projmux-status r run-shell '#{q:projmux} statusbar usage-refresh'
bind-key -T projmux-status n run-shell '#{q:projmux} statusbar click notify'
bind-key -T projmux-status g run-shell '#{q:projmux} statusbar click git'
bind-key -T projmux-status p run-shell '#{q:projmux} statusbar click pwd'
bind-key -T projmux-status s run-shell '#{q:projmux} statusbar click session'
```

The chord routes through the same dispatcher as the mouse click, so
keyboard and mouse paths are functionally identical for keyed ranges.
The `r` key is the one exception: it asks the usage manager for a throttled
refresh and then reopens the same display-only usage popup from cache.
Per-adapter throttle hints and backoff remain in force, so repeated refreshes
inside the cooldown rerender cached data without another adapter collection.
There is intentionally no `prefix s s` settings fallback because that chord
already opens the session/sidebar range.

## Click failure handling

Every status-bar click runs from tmux's `run-shell`. A non-zero exit
there triggers a tmux error popup, which is hostile UX for a casual
click. Each handler therefore swallows runtime failures and surfaces
them as `display-message` toasts:

- `notify` click whose focus dispatch exits 2 (target unresolved):
  ack the entry, toast `notify target gone; cleared`.
- `notify` click whose AI target is inactive because it no longer matches live
  reply+agent state: focus and ack when the target is still routable.
- Any other focus failure: keep the entry, toast `focus failed:
  <reason>`.
- `session` or `git` popup launch failure: toast
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

Settings > Appearance > Status Bar controls Project, Working directory, Git,
Resources, Clock, and the Settings launcher independently. Working directory
and Git keep icon decoration as a separate choice: icon `off` never hides the
text segment. Each icon row
can be `off` (default, font-safe), `symbol` (Nerd Font-style folder,
git-provider, or bell icon), or `emoji`. Git branch decoration follows
`remote.origin.url`: GitHub remotes use a cat-style mark, GitLab remotes use a
fox-style mark, and other remotes use a generic git branch mark. Saving
visibility or Resources regenerates the app/standalone config and source-loads
the generated app config when Settings is running inside tmux. The legacy
`~/.config/projmux/statusbar-decoration` and
`@projmux_statusbar_decoration` remain fallback defaults for older configs.
Settings > Theme controls the bottom status bar background through
`status_background`; `surface` controls popup and native frame backgrounds.

Resources uses `~/.config/projmux/live-resources` as its single saved enabled
state; there is no separate Resources visibility file or duplicate toggle. CPU
sampling state is an internal,
atomically replaced file under `${XDG_STATE_HOME:-~/.local/state}/projmux/`
and is not a user-edited setting.

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
