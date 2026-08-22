# Usage tracking

`projmux agent usage` and `projmux internal status usage` report authoritative fixed-window
utilisation for Claude/Codex and official named quota buckets for Antigravity.
`--model all`, the tmux
HUD, and the statusbar usage popup use Settings > AI Settings > Enabled
agents as the source of truth, so disabled Claude/Codex/Antigravity providers are
not refreshed or rendered on ambient/all surfaces. Explicit read-only
requests such as `projmux agent usage --model claude`, `--model codex`, or
`--model antigravity`
still collect and render that provider even when it is disabled.

Claude and Codex adapters read the upstream's own account view. Antigravity
reads only the official managed statusline payload: `context_window` remains
private conversation-local diagnostic metadata, while each valid `quota` map
entry is a separate account row. Projmux preserves the upstream bucket ID and
never guesses that an undocumented ID means `5h` or `weekly`. It does not infer
quota, cadence, reset timestamps, or account limits from screen scraping,
tokens, history, OAuth/cache files, or binary strings.

Claude keeps the canonical aggregate `five_hour` and `seven_day` rows and also
preserves structurally valid typed `limits[]` rows as named account quotas for
inspection surfaces. These named rows never participate in the ambient status
projection.

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
- Canonical `five_hour` and `seven_day` blocks remain `5h` and `weekly`.
  Each valid typed `limits[]` row becomes `window=quota` with the exact opaque
  `group` copied to `bucket`. The snapshot also preserves `kind`, `severity`,
  `is_active`, and nullable `scope`, model ID, and surface metadata; percent and
  reset remain the authoritative common snapshot fields.
- A model-scoped quota renders as `quota/<group> · <model display name>` in
  text and popup inspection. Control characters and tmux format introducers are
  escaped and the display label is bounded, while the stored identity remains
  byte-for-byte unchanged. No model-family inference, aliasing, aggregation, or
  percentage-to-count derivation is performed.
- `limits[]` is capped at 64 rows and required field presence/types are checked
  explicitly. Validation is **row-level**: a row that fails a field check is
  dropped, the remaining rows still reach the snapshot, and the drop is
  reported through the warning channel (see [Collection
  failures](#collection-failures)). Only two shape failures still abort the
  whole collection so the manager retains the complete last-known-good Claude
  slice: a `limits` value that is not a JSON array, and a row count above the
  64-row cap. A valid aggregate-only response succeeds and therefore removes
  obsolete named rows.
- Null legacy top-level model hints and unknown experiment keys are ignored.
  Billing/credit blocks such as `extra_usage` and `spend` are not ingested or
  rendered.

### Codex (`internal/core/usage/adapters/codex`)

The native Codex app-server account API is primary:

- An explicit `projmux agent usage` request reuses the app-server
  ensure-ready lifecycle, then opens one initialized official stdio-proxy
  connection and calls `account/rateLimits/read` with a bounded deadline.
  Automatic HUD refreshes probe/read an already-running daemon but do not gain
  daemon-start authority.
- When `rateLimitsByLimitId` is present it is the authoritative multi-bucket
  view; the backward-compatible `rateLimits` mirror is not duplicated. Each
  bucket preserves its map key, nullable `limitId`, nullable `limitName`,
  primary/secondary slot, percentage, nullable reset, and nullable cadence.
  `300` and `10080` minutes project to `5h` and `weekly`. Missing or unknown
  positive cadences remain lossless `quota` rows instead of disappearing.
- `account/rateLimits/updated` is merged sparsely into the just-read native
  snapshot during one bounded event-settle window. Null account metadata does
  not clear a prior label or identity. The event-refreshed rows then use the
  same Manager throttle, replace, store, and last-known-good path as a read.
- Validation is row-level. A malformed bucket/window is dropped with a bounded,
  field-only warning while valid siblings remain native. Native and rollout
  rows are never combined in one invocation.

When app-server is unavailable, unsupported, disconnected, timed out, or the
account exposes no supported rate-limit bucket (including API-key-style
accounts), the adapter invokes the existing newest-rollout collector exactly
once without attempting login, logout, token refresh, or config writes:

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
- `primary` → 5h window, `secondary` → weekly window. Window classification is
  by `window_minutes` (`300` → 5h, `10080` → weekly); an unrecognised cadence is
  a window this build does not render, not a defective row, so it is skipped
  silently.
- A slot that is present but carries no `used_percent` is a defective row: it is
  dropped and reported rather than projected as a genuine `0%`.

Codex shares the manager's default `30s` throttle (no
`ThrottleHinter`) and does not implement `BackoffStater`. A successful
rollout fallback row records `source=rollout` plus the closed fallback reason.
If neither lane produces rows, Manager preserves the previous source/value and
adds a closed `stale_reason` to the last-known-good row.

### Antigravity (`internal/core/usage/adapters/antigravity`)

Local managed-statusline sidecars. No network or credential reads.

- `context_window.used_percentage` and its conversation ID remain in the
  private context sidecar for hook/notify diagnostics. They do not become
  Usage snapshots. The legacy string percentage remains a writer fallback.
- The official `quota` map is sorted by its exact bucket ID. Each valid bucket
  becomes `window=quota`, `bucket=<upstream ID>` and renders as
  `quota/<upstream ID>` on account-inspection surfaces.
- Used percent is `100 * (1 - remaining_fraction)`. Non-finite or values
  outside `[0,1]`, empty IDs, null/disabled entries, and negative relative
  resets are ignored safely. Rejected and duplicate buckets are skipped
  individually — the healthy buckets in the same map still become rows — and the
  skip is reported by sorted row index only, never by bucket ID.
- `reset_time` and optional `reset_in_seconds` are stored independently. An
  absent relative reset differs from explicit zero; no value is derived from
  the other.
- Context and quota use independent private sidecars. A context-only payload
  does not erase the last quota observation. An explicit empty/null quota map
  records no buckets; the manager's existing rule still preserves prior model
  rows when an adapter returns zero total rows. Context never participates in
  that account-row replacement decision.

## Snapshot store

```
${PROJMUX_USAGE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/projmux/usage}/snapshots.json
```

JSON document keyed by adapter, recording:

- per-window `Snapshot{Model, Window, Bucket, Pct, Limit, ResetsAt,
  ResetInSeconds, UpdatedAt, NamedQuota}`; `Bucket` is populated only for
  `window=quota`, and `NamedQuota` is populated only when an upstream typed
  named-quota contract supplies the metadata
- per-adapter `last_collect` timestamp (drives the throttle)
- per-adapter `Backoff{Until, Consecutive}` (drives the cooldown)

Adapter failures merge over the prior slice rather than replacing it,
so a 429 keeps the last known good rows visible. A *partial* collect — some
rows dropped, some kept — still counts as a successful refresh for the models
it touched, so the surviving rows replace the prior slice rather than
preserving it. Force-redirect the
file at install time via `PROJMUX_USAGE_STATE_DIR` to share the cache
across machines (Dropbox, iCloud Drive).

## CLI

### `projmux agent usage`

```
projmux agent usage [--model codex|claude|antigravity|all] [--window 5h|weekly|context|quota|all]
              [--json] [--force|-f]
```

For `--model all`, calls `Manager.Collect` (or `ForceCollect` with
`--force`) only for providers enabled in Settings > AI Settings >
Enabled agents, filters by window, and renders the tab-aligned table:

```
MODEL        WINDOW                 PCT  RESETS_AT                 RESET_IN  STALE  SOURCE      REASON
codex        5h/codex · General     12%  2026-05-07T14:00:00+09:00 -         app-server
claude       5h                     80%  2026-05-07T14:00:00+09:00 -
antigravity  quota/gemini-weekly    6%   2026-07-06T16:50:32+09:00 560580s
claude       quota/group-redacted · Model Redacted Alpha  38%  2031-02-03T15:05:06+09:00  -  *
```

`SOURCE`/`REASON` are included when source-aware rows are present. Native
Codex rows report `app-server`; fresh rollout fallback and retained
last-known-good rows report their closed fallback/stale reasons. `STALE` is
`*` when `now - UpdatedAt > 10m`. A `--json` payload returns
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
inspection and collect/render only the requested adapter. Antigravity reports
zero or more account `quota/<bucket-id>` rows. Legacy cached `window=context`
rows are suppressed in text and JSON output. `--window quota` selects only
account buckets; `--window weekly` never matches an opaque quota bucket named
`weekly`. `--window context` remains an accepted compatibility filter and
returns no Usage rows.

### `projmux internal status usage`

```
projmux internal status usage [--max-width N] [--force|-f]
```

The HUD bar wired to the tmux status interval. It scopes the registry to
enabled AI agents, then triggers an opportunistic refresh:
`MaybeCollect(throttle=30s)` (subject to per-adapter throttle and active
backoff). Disabled providers are not refreshed just to be hidden. Errors
are swallowed unless `PROJMUX_USAGE_DEBUG` is set. Then it loads the
cache, filters to the same enabled-agent scope, applies provider/window
presentation preferences, and renders. Preference filtering therefore happens
after collection, throttle/backoff, and cache load. If no AI
agents are enabled, the status segment emits nothing.

The HUD first derives an ambient projection separate from lossless account
snapshots. The explicit HUD capability map admits Claude/Codex `5h` and
`weekly`, while Antigravity's exact `quota/gemini-weekly` identity alone is
projected as `weekly`
without rewriting the cache. Context, `3p-weekly`, and unknown quota buckets
do not participate in status width. Claude `limits[]` named/model rows are also
excluded; only its aggregate official `5h` and `weekly` rows reach the HUD.
The Settings provider list consumes `aiprovider.UsageSupported()` order, but a
window toggle exists only when this same projection seam declares it. A future
provider or an opaque bucket cannot manufacture a window row.
For native Codex multi-bucket rows, the exact `codex` bucket wins the HUD
projection, then the legacy empty bucket, then lexical bucket order. The HUD
source/reason annotation is copied from that same row, so its percent and
source always match an explicit `agent usage --model codex` JSON/table row.

Settings > Appearance > Status Bar > Agent Usage HUD can hide the whole HUD,
a provider, or one supported window. Parent off states preserve child saved
values. The filter is ambient-only: `agent usage` table/JSON, explicit model or
window reads, `CachedState` popup data, provider enablement, refresh/backoff,
and snapshot bytes are unchanged. After filtering, official/secondary is
recomputed; a weekly-only provider keeps weekly as its official window through
the normal width shed order.

As `--max-width` shrinks the segment does not fall to a coarser whole-segment
tier. It sheds **one optional element at a time** — a cosmetic age indicator
first, a provider's *second* window bar much later — so one provider's
decoration can no longer cost another provider its official window bar. The
staleness marker and every provider's official window survive the entire order.

That order is defined in exactly one place in code, `usageShedOrder` in
`internal/app/usagecmd/usage.go`, and documented in exactly one place in prose:
[statusbar.md → Usage element drop order](statusbar.md#usage-element-drop-order).
It is deliberately not repeated here.

The age indicator is the HUD's staleness signal, and it uses the same two
thresholds as the `STALE` column and the JSON `stale` flag — `staleAfter` (10m)
and `veryStaleAfter` (1h). There is no separate age constant:

| age | indicator | color |
| --- | --- | --- |
| `< 1m` | none | — |
| `1m` … `10m` | `(3m)` | dim grey |
| `> 10m` … `1h` | `(15m~)` | muted grey |
| `> 1h` | `(3h~~)` | muted grey |

The `~` / `~~` markers are the legacy stale vocabulary, carried inside the
indicator while the age text is still rendered and glued to the label once the
drop order has shed it. Staleness stays muted however far the segment has
degraded: warning and critical colors are reserved for usage thresholds, not
cache age. A healthy Codex row does not need a cosmetic age indicator, while a
retained Codex last-known-good row opts in and also carries its exact closed
stale reason in the HUD provenance annotation.

The statusbar usage **popup**'s sync line is a different surface with a
different meaning (last successful collect, 60s amber threshold) and is
unaffected by these thresholds.

## Collection failures

When a collection fails, the failure is visible in three places:

1. **The rest of the response survives.** A defective row is dropped, never
   substituted — no synthesized `0%`, zero time, or invented reset. The healthy
   rows still refresh, so a single broken row can no longer freeze a provider's
   whole slice while `last_collect` keeps advancing.
2. **A bounded warning.** `projmux agent usage` prints one line to stderr:

   ```
   usage: warning: claude: skipped 2 usage rows: row 0: missing kind; row 2: missing resets_at
   ```

   Reasons are row-index plus field-name context only. Raw upstream values,
   reset timestamps, opaque bucket identities, and wrapped decoder messages are
   never included — decoder errors routinely quote the offending input. The
   reason list is capped at five entries with a `(+N more)` suffix so a hostile
   payload cannot dictate the length of the line.
3. **The operations journal.** Each failing provider produces one
   `usage.collect.outcome` row in the private journal
   (`internal/diagnostics`), readable with:

   ```
   projmux diagnostics log --component usage --tail 20
   ```

   The row carries the provider and closed source/failure enums and nothing else:
   `collect-failed` (whole-adapter failure, `level=error`) or `rows-skipped`
   (partial failure, `level=info`). Codex rollout fallback records
   `source=rollout` plus its closed fallback reason; retained data records
   `source=last-known-good` plus its closed stale reason. A healthy native
   collection writes no row at all. Identical `(provider, source, failure)`
   tuples are recorded at most once per
   process run — the same suppression the notify/focus recorder uses — so a
   repeating failure cannot flood the bounded journal. Journal writes are
   best-effort and never change what the usage command returns.

   Because `projmux internal status usage` runs as a short-lived process per refresh,
   the once-per-run suppression bounds repeats within a single invocation; the
   cross-invocation rate is bounded by the adapter throttle instead.

### Statusbar usage popup

`projmux internal statusbar click usage` renders a native-framed popup from the same
cache instead of shelling out to `projmux agent usage`. The popup filters rows and
sync metadata to the same enabled-agent scope as the ambient HUD. This keeps
`projmux agent usage --json` stable for CLI consumers while giving the
tmux click path a structured table with aligned rows, right-aligned numeric
values, dim unavailable cells, amber usage at 80%, and red usage at 95%.

The popup suppresses legacy cached context rows while preserving every valid
named quota ID, reset, and freshness value. Its columns are data-driven: when
no displayed row has authoritative absolute token counts, `USED`, `LIMIT`, and
`LEFT` are omitted together. If any row has real counts, all three columns are
shown; percent-only rows use unavailable cells. Counts are never derived from
percentages.

Named Claude rows use the same bounded, injection-safe label as text output and
include their reset plus per-row `AGE`. JSON retains exact opaque group/model
identity, nullable scope fields, `updated_at`, and the derived `stale` flag.

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
| `PROJMUX_USAGE_DEBUG` | Surface adapter errors from `internal status usage` to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated; read but ignored (limits come from upstream APIs). |
