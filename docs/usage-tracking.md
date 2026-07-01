# Usage tracking

`projmux usage` and `projmux status usage` report authoritative 5-hour
and weekly utilisation for enabled AI agents. `--model all`, the tmux
HUD, and the statusbar usage popup use Settings > AI Settings > Enabled
agents as the source of truth, so disabled Claude/Codex providers are
not refreshed or rendered on ambient/all surfaces. Explicit read-only
requests such as `projmux usage --model claude` or `--model codex`
still collect and render that provider even when it is disabled.

Both adapters read from the upstream's own view of the account so the
percentages match what `claude /usage` and `codex` show natively.

Antigravity is intentionally not registered as a 5-hour/weekly quota adapter.
The only stable Phase 0b usage signal is statusline `context_window`, which is
conversation context-window usage, not account quota usage. `projmux usage
--model antigravity` and ambient all-model table output therefore render an
explicit unsupported note when Antigravity is enabled. The statusbar usage
popup shows an `Antigravity ctx ... unsupported` row, while the compact tmux
status segment stays silent unless Claude/Codex quota rows exist. Projmux does
not infer quota, reset timestamps, or account limits from screen scraping,
tokens, history, OAuth/cache files, or binary strings.

## Adapters

### Claude (`internal/core/usage/adapters/claude`)

OAuth-authenticated HTTPS fetch:

- `GET https://api.anthropic.com/api/oauth/usage` with the bearer token
  read from `~/.claude/.credentials.json`.
- On HTTP 401 the adapter performs a single refresh round-trip:
  `POST https://api.anthropic.com/api/oauth/token` with the stored
  refresh token, then rewrites `.credentials.json` with the rotated
  access/refresh pair before retrying the usage call.
- Tokens are never logged.

Throttle: 5 minutes (`ThrottleHinter`). The `60s` cadence used in 0.3
trips 429 in practice, so the adapter raises the manager's per-adapter
floor.

429 backoff (`BackoffStater`):

- Base `30m`, doubling per consecutive 429, cap `60m`.
- A `Retry-After` header (when ≥ 60s) raises the floor.
- During backoff `Collect` short-circuits — no network call, prior
  rows are preserved.
- A clean 200 resets the consecutive counter.
- `--force` (BackoffResetter) clears the persisted state and attempts
  the call regardless of streak.

### Codex (`internal/core/usage/adapters/codex`)

Local rollout JSONL parser. No network calls.

- Walks `${HOME}/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-*.jsonl`
  newest-first by mtime (NOT filename — Dropbox-synced rollouts can
  arrive out of order).
- Caps the search to 8 days back so a stale tree never blocks the
  status interval.
- Parses each line until it finds the latest `token_count` record's
  `rate_limits` payload:
  ```json
  {
    "limit_id": "codex",
    "primary": { "used_percent": 8.0, "resets_at": "..." },
    "secondary": { "used_percent": 4.5, "resets_at": "..." }
  }
  ```
- `primary` → 5h window, `secondary` → weekly window.

Codex shares the manager's default `30s` throttle (no
`ThrottleHinter`). It does not implement `BackoffStater` — local-only
read.

## Snapshot store

```
${PROJMUX_USAGE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/projmux/usage}/snapshots.json
```

JSON document keyed by adapter, recording:

- per-window `Snapshot{Model, Window, Pct, Limit, ResetsAt, UpdatedAt}`
- per-adapter `last_collect` timestamp (drives the throttle)
- per-adapter `Backoff{Until, Consecutive}` (drives the cooldown)

Adapter failures merge over the prior slice rather than replacing it,
so a 429 keeps the last known good rows visible. Force-redirect the
file at install time via `PROJMUX_USAGE_STATE_DIR` to share the cache
across machines (Dropbox, iCloud Drive).

## CLI

### `projmux usage`

```
projmux usage [--model codex|claude|antigravity|all] [--window 5h|weekly|all]
              [--json] [--force|-f]
```

For `--model all`, calls `Manager.Collect` (or `ForceCollect` with
`--force`) only for providers enabled in Settings > AI Settings >
Enabled agents, filters by window, and renders the tab-aligned table:

```
MODEL   WINDOW  PCT   RESETS_AT                STALE
claude  5h      80%   2026-05-07T14:00:00+09:00
claude  weekly  35%   2026-05-09T00:00:00+09:00
codex   5h      8%    2026-05-07T11:30:00+09:00
codex   weekly  4%    2026-05-09T00:00:00+09:00
```

`STALE` is `*` when `now - UpdatedAt > 10m`. A `--json` payload returns
the `Snapshot` array; if any adapter is in backoff, the wrapper
`{snapshots, backoff: {model: {until, consecutive}}}` is emitted
instead. A backoff note is appended to the human table:

```
claude is in backoff, try again in 30m (use --force to bypass)
```

When no AI agents are enabled, all-model table output contains no
provider rows and prints a short Settings hint. `--json` returns an
empty array. Explicit `--model claude`, `--model codex` and
`--model antigravity` bypass the enabled-agent filter for read-only
inspection and collect/render only the requested adapter. Antigravity
exposes no 5h/weekly quota, so its adapter reports a single `context`
window row (context-window fullness, no `RESETS_AT`) sourced from the
latest statusline `context_window` observed via hook ingest.

### `projmux status usage`

```
projmux status usage [--max-width N] [--force|-f]
```

The HUD bar wired to the tmux status interval. It scopes the registry to
enabled AI agents, then triggers an opportunistic refresh:
`MaybeCollect(throttle=30s)` (subject to per-adapter throttle and active
backoff). Disabled providers are not refreshed just to be hidden. Errors
are swallowed unless `PROJMUX_USAGE_DEBUG` is set. Then it loads the
cache, filters to the same enabled-agent scope, and renders. If no AI
agents are enabled, the status segment emits nothing.

Output degrades through six tiers as `--max-width` shrinks:

1. Long form with last-sync age + bars: `Claude (3m) 5h [████████░░]
   80% · weekly [...]   Codex 5h [...] 20% · weekly [...]`
2. Drop the age indicator (legacy long form).
3. Drop the weekly bar.
4. Drop bars entirely (`Claude 5h:80% weekly:30%`).
5. Single-letter labels (`C 5h:80% weekly:30%`).
6. Hard rune-truncate with trailing `…`.

The age indicator stays muted as data gets older: dim grey below 1h, then
muted grey at 1h and beyond. Warning and critical colors are reserved for
usage thresholds rather than cache age. Codex opts out of the indicator
because the rollout file is always near-current (no throttle gap to report).

### Statusbar usage popup

`projmux statusbar click usage` renders a native-framed popup from the same
cache instead of shelling out to `projmux usage`. The popup filters rows and
sync metadata to the same enabled-agent scope as the ambient HUD. This keeps
`projmux usage --json` backwards-compatible for CLI consumers while giving the
tmux click path a structured table with aligned rows, right-aligned numeric
values, dim unavailable cells, amber usage at 80%, and red usage at 95%.

The popup sync line uses the maximum authoritative `LastCollect` timestamp from
the cache. If that field is unavailable, it falls back to the snapshots file
mtime. The sync line turns amber when the timestamp is more than 60 seconds
old. Enter closes the popup.

## Force semantics

`--force` / `-f` does two things:

1. Bypasses the per-adapter throttle gate so every adapter's `Collect`
   runs even if `now - last_collect < throttle`.
2. Calls `BackoffResetter.ResetBackoff` on adapters that implement it,
   clearing the in-memory and on-disk backoff so the network call
   attempts regardless of any active 429 cooldown.

A force that itself returns 429 records a fresh `consecutive=1` —
forcing does NOT preserve the prior streak (the user explicitly chose
to attempt now). Bind it to a tmux key (e.g. `prefix U`) for a manual
"refresh now" gesture.

## Environment variables

| Variable | Effect |
| --- | --- |
| `PROJMUX_USAGE_STATE_DIR` | Override snapshot directory. Resolved verbatim, no `~` expansion. |
| `PROJMUX_USAGE_DEBUG` | Surface adapter errors from `status usage` to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated; read but ignored (limits come from upstream APIs). |
