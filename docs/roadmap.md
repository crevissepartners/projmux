# Roadmap

## Done (0.4.x)

The 0.4 line filled in the operational surface around the session-management
core that 0.3 had landed.

### Setup and install

- `projmux setup` — TTY raw-mode probe that reports which projmux key
  sequences (`Alt-1..5`, `Ctrl-N`, `Ctrl-Shift-{R,L,M}`, `Ctrl-M`,
  `Alt-Shift-{Left,Right}`) reach the process and which the terminal
  swallows.
- `projmux init [terminal]` — auto-merges projmux's CSI-u + chord
  bindings into a terminal config. Adapters: Ghostty (with the
  `config` / `config.ghostty` candidate split and symlink guard) and
  Windows Terminal (WSL + native).

### Diagnostics

- `projmux doctor` — runtime dependency report. Enforces minimum tmux
  3.4 and checks workflow dependencies such as `git` and `stty`.

### Focus

- `projmux focus` — unified switch-client dispatch. Resolves a target
  session against the live tmux inventory, redirects an existing client
  if one is attached, otherwise emits a desktop notification. Used by
  the status-bar notify click and by the AI reply-ready handler.

### Notify queue

- `projmux notify push|list|ack` — persistent JSON-backed queue at
  `<state>/projmux/notify.json` with TTL, severity, source, and target
  metadata.
- `projmux notify reconcile` — back-fills the queue from live pane
  state by walking `tmux list-panes -a`.
- Producer wired to the attention state machine: a pane transitioning
  to `reply` with an AI agent option set pushes an `ai:<session>:<pane>`
  entry; the matching `clear` acks it.

### Usage tracking

- `projmux usage` (and `status usage`) — authoritative 5h + weekly
  utilisation for both Claude (OAuth `api/oauth/usage` endpoint with
  401 token refresh) and Codex (latest rollout `rate_limits` JSONL).
- Per-adapter throttle (Claude `5m`, default `30s`), 429 backoff
  (`30m`–`60m` exponential), `--force` to bypass both. Snapshots
  preserved on failure so a 429 does not erase prior rows.

### Statusbar and HUD

- Two-line clickable status bar: row 0 is the existing
  session/window/path/git/kube row, row 1 splits notify (left) and
  usage (right), with a row-0 settings click fallback.
- `projmux statusbar click` — single dispatcher for both mouse clicks
  and the `prefix s {u,n,g,k,p,s}` keyboard chord. Window-list clicks
  on tabs short-circuit to native `select-window`.
- `pwd` status click copies the current pane path into the tmux paste
  buffer and shows a compact path popup instead of a transient
  warning-coloured toast.
- HUD-style notify segment with severity+agent badge, midpoint dot
  separators, and an age field.
- HUD-style usage segment with bars, last-sync age indicator (Claude),
  and graceful degradation through six tiers as `--max-width` shrinks.

## Next (0.5+)

Carried forward from earlier milestones — items still outstanding when
v0.4 shipped.

### Picker UI

- Picker-domain model separate from row rendering. Done in the 0.5 picker
  contract slice.
- Native picker backend for multi-line card rows and title-focused search.
  Done in the 0.5 picker contract slice, later promoted to the only picker
  backend.
- Port switcher popup/sidebar surfaces after parity tests cover
  selection, preview, and key actions.

### Picker dismissal

- Picker-agnostic popup close/toggle handling so AI picker dismissal
  does not depend on backend-specific key bindings. Done in the 0.5 picker
  contract slice; the native runner consumes shared close action keys.

### Docker install and E2E harness

- Initial Docker-backed Linux smoke suites are available through
  `make test-integration`, `make test-install-smoke`, and `make test-e2e`.
  They cover install/runtime substrate checks against real `tmux`; host-only
  terminal, WSL, macOS, and GUI checks remain separate in
  [docs/testing.md](testing.md).
