## Usage snapshots

`projmux agent usage` and `projmux internal status usage` share a single `Manager`
that walks two registered adapters (Claude, Codex) and persists the
result to `<state>/projmux/usage/snapshots.json` (or
`PROJMUX_USAGE_STATE_DIR`). The cache file is the authoritative source
for the HUD render path so the tmux status interval never blocks on a
network call.

- **Per-adapter throttle** — Claude reports a 5-minute hint via the
  `ThrottleHinter` interface; Codex falls through to the global
  `30s` floor used by `internal status usage`. `MaybeCollect` only invokes an
  adapter when `now - last_collect >= throttle`. `--force` bypasses the
  gate.
- **429 backoff** — Claude implements `BackoffStater`. On HTTP 429
  the adapter persists `BackoffState{Until, Consecutive}`: the
  default cooldown is 30 minutes, doubling per consecutive 429 up to a
  60-minute cap. A `Retry-After` header (when present) raises the floor.
  During backoff `Collect` short-circuits (no network call). A clean
  200 resets the streak. `--force` clears the persisted state via the
  `BackoffResetter` interface so the next call attempts the network
  call regardless of streak.
- **Failure preservation** — adapter failures do not erase prior
  rows. The Manager merges new snapshots over the on-disk slice, so a
  transient 429 keeps the last known good numbers visible.
- **Codex native source selection** — the Codex adapter alone owns one
  invocation's source decision. It normalizes native
  `account/rateLimits/read` plus bounded sparse update events into snapshots;
  only unavailable/unsupported/account-empty outcomes invoke the newest
  rollout parser once. Native and rollout rows are never synthesized together.
  Optional snapshot provenance preserves source, fallback/stale reason, and
  native bucket label/cadence through Store and all public read surfaces.

See [usage-tracking.md](../usage-tracking.md) for adapter detail (token
refresh, rollout schema).
