# Linux resource attribution core

Phase 0 provides the read-only attribution contract consumed by the Resource
Inspector shipped in Phase 1. `projmux resources`, the client-scoped
`resource-inspector` popup, the statusbar range, and `Resources:Open` all keep
the snapshot in memory only for the interactive process lifetime; it remains
outside Session State.

## Identity and inventory

`tmux.Client.ListResourcePanes` reads a resource-specific inventory containing
socket path, session id/name, window id, pane id, pane PID/TTY, and the session
`@projmux_project_path` anchor. This is deliberately separate from the general
cross-backend `tmux.Pane`: psmux remains a valid degraded inventory adapter and
does not fabricate PID, TTY, or project-anchor data.

The ownership key is `(socket, pane_id)`. Process identity is `(PID,
/proc/<pid>/stat starttime)` and a process is attributed only when its POSIX
SID maps to exactly one unique pane PID. Pane labels, AI topics, raw titles,
current commands, and cwd-derived names are not ownership inputs.

Linked appearances of one pane are deduplicated. Multiple non-empty project
anchors become `Shared / ambiguous`; no anchor becomes `Unassigned`. Processes
that use `setsid` or otherwise leave the pane SID remain host-only and are
counted at the escaped boundary instead of guessed back onto a pane.

## Sampling and read model

The Linux collector enumerates `/proc` once per sample, then reads only
`/proc/stat`, `/proc/meminfo`, and numeric `/proc/<pid>/stat` files. It never
reads command lines, environment, prompts, pane content, transcripts, memory
maps, SQLite, protobuf, or remote telemetry.

- CPU needs two samples with the same positive logical CPU count. Primary CPU
  is process tick delta divided by aggregate host tick delta (host capacity
  share, normally comparable on a 0–100% scale); secondary CPU is that share
  multiplied by logical CPUs (core-equivalent). First sample, invalid/reset
  deltas, PID reuse, and logical CPU changes remain unknown/partial, never zero
  or silently clamped.
- Memory is summed RSS bytes plus `RSS / MemTotal`. RSS is a per-process sum;
  shared pages may therefore be counted more than once.
- Pane values are aggregated into unique window and project rows. Tests enforce
  that window/project totals equal the same set of unique pane totals.
- `Attributed` is not host total. `Other / unattributed` is a separate host
  remainder. When process-delta timing or RSS sharing makes attributed values
  exceed the host comparison sample, the model exposes an overage and leaves
  remainder unknown instead of clamping it to zero.
- Snapshot state is `warming`, `ready`, `partial`, or `unavailable`. Bounded
  diagnostics contain scan duration and sampled/skipped/race/permission counts,
  plus identity/delta quality counts; they contain no user payload.

## Measurements

Measured 2026-08-12 on Linux amd64, Intel Core Ultra 5 125U. Collector cases
used generated procfs directory fixtures and `-benchtime=20x`; aggregation used
`-benchtime=100x`. Commands:

```text
go test -run '^$' -bench BenchmarkCollectorScan -benchtime=20x -count=1 ./internal/integrations/procfsresources
go test -run '^$' -bench BenchmarkBuildSnapshot -benchtime=100x -count=1 ./internal/core/resources
```

| Panes | Processes | Procfs scan | Scan allocation | Aggregation | Aggregation allocation |
|---:|---:|---:|---:|---:|---:|
| 10 | 50 | 0.407 ms | 80.6 KB | 0.026 ms | 32.1 KB |
| 10 | 200 | 0.927 ms | 313.0 KB | 0.074 ms | 75.0 KB |
| 10 | 1000 | 6.332 ms | 1.56 MB | 0.230 ms | 481.6 KB |
| 50 | 50 | 0.407 ms | 80.6 KB | 0.084 ms | 90.4 KB |
| 50 | 200 | 0.927 ms | 313.0 KB | 0.111 ms | 133.2 KB |
| 50 | 1000 | 6.332 ms | 1.56 MB | 0.261 ms | 539.9 KB |

The scan is process-count-bound and does not multiply by pane count. This
supports the planned 2-second popup cadence without a daemon or persistent
history. A separate count-only live cost probe (`PROJMUX_RESOURCE_PSS_MEASURE=1
go test -run TestPSSReadCostMeasurement -v
./internal/integrations/procfsresources`) attempted 200 host processes: 53
`smaps_rollup` reads succeeded and 147 were skipped because of access
restrictions. Those reads took 92.865058 ms, versus 7.323994 ms for the matching
one-pass RSS/stat scan. This is not a universal cost multiplier: accessibility,
process shape, kernel state, and cache effects differ by host. It does show the
structural cost of a separate `smaps_rollup` read and kernel page accounting per
accessible process, whereas RSS comes from the stat file already needed for
CPU/identity. PSS is therefore **deferred**. It may be reconsidered only as an
on-demand pane-detail measurement with a concrete consumer; it is not part of
continuous refresh.

## Sanitized real tmux smoke

The opt-in read-only test below observes an existing socket and emits counts
only. It neither captures pane content nor reads process command lines.

```text
PROJMUX_RESOURCE_TMUX_SOCKET=projmux go test \
  -run TestResourceAttributionRealTmuxReadOnlySmoke -v \
  ./internal/integrations/tmux
```

2026-08-12 result: `panes=8`, `pane_pid_eq_sid=8`, `missing_pids=0`,
`attributed_processes=13`, `escaped_boundary=0`, `sampled=474`, `skipped=0`,
`race=0`, `permission=0`, `status=ready`. A separate tmux format-only count
showed four shell panes, three direct agent panes, and one launcher pane. The
zero escaped count is a valid observed boundary; the setsid fixture test pins
the non-attribution behavior deterministically. Race and permission states are
also deterministic fixtures: the collector test injects `fs.ErrNotExist` and
`fs.ErrPermission` for numeric proc entries and checks the separate counts.

A positive real-kernel setsid boundary is covered by an isolated transient
smoke. It creates and removes its own tmux socket/server and never touches the
existing production socket:

```text
PROJMUX_RESOURCE_TRANSIENT_SMOKE=1 go test \
  -run TestResourceAttributionTransientSetsidSmoke -v \
  ./internal/integrations/tmux
```

2026-08-12 result: `panes=1`, `attributed_processes=1`,
`escaped_boundary=1`, `sampled=480`, `skipped=0`, `race=0`, `permission=0`.
The pane shell remained attributed while its real `setsid` child was counted
at the escaped/Other boundary, without reading the child command line.

## Phase 1 inspector

The popup retains warming/partial/unavailable and overage states, renders RSS
explicitly as a sum, keeps `Other / unattributed` non-drillable, and discards
samples when it closes. Its non-overlapping default cadence is two seconds;
Ctrl-R shares the same scan gate. Selection and query survive refresh by stable
row identity, while a vanished row clamps to the nearest valid neighbor.
Display labels use label → agent topic → known interactive shell → raw title,
but those values never become ownership keys. Unsupported platforms show an
unavailable reason, not zero metrics. PSS, non-Linux collectors, process-list
drill-down, history, and resource mutation remain outside this contract.
