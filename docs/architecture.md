# Architecture

## Core model

`projmux` is built around a small set of domain objects:

- `ProjectRoot`: a directory that may map to a tmux session
- `SessionIdentity`: the stable session name derived from a directory
- `SessionTarget`: the current selected session/window/pane target
- `CandidateSet`: the ordered list of project directories presented to the user
- `PinSet`: user-curated candidate priority state
- `PreviewState`: selected window/pane state used by popup and session previews

## Layers

Every package below `cmd/` and every top-level directory under `internal/` sits
in one layer. A package may import its own layer and the layers below it. The
`Imports` column is the measured set of other top-level directories each one
imports in non-test code (`go list -f '{{.Imports}}' ./...`), not a wish list.

| Layer | Path | Responsibility | Imports |
| --- | --- | --- | --- |
| Entry | `cmd/projmux` | Binary entrypoint. Records the run in the diagnostics journal and hands the arguments to `internal/app`. | app, cli, diagnostics, version |
| Application | `internal/app` | Command implementations: parses each route, reads config and state, calls core rules and integration adapters, and renders through `internal/cli` and `internal/ui`. | every directory except testutil and tools |
| UI | `internal/ui` | Native picker model, input handling, and row and preview rendering for popups and the sidebar. | core, i18n, theme |
| Integrations | `internal/integrations` | Adapters to the outside world: tmux, the Registry file and its tmux mirror, AI agent providers and their local transports, lifecycle hooks, and procfs resource collection. | config, core, diagnostics, state, theme, version |
| Core | `internal/core` | Product rules and state transitions that are testable without tmux: the resource model, naming, the resolved resource graph, the controller plan, candidates, pins, sessions, messages, and usage. Core does not shell out. | aiprovider, config, integrations (one known exception, below), state, version |
| Support | `internal/diagnostics` | Bounded operational event journal that `projmux diagnostics` reads. | cli, config, state |
| Support | `internal/cli` | Canonical command catalog, help, output formats, and receipts. | aiprovider |
| Support | `internal/config` | Config files, layering, and saved settings. | aiprovider, state |
| Leaf | `internal/state` | Simple file-backed state helpers. | — |
| Leaf | `internal/aiprovider` | AI provider registry: IDs, names, binaries, and capability flags. | — |
| Leaf | `internal/i18n` | Message catalog and locales. | — |
| Leaf | `internal/theme` | Built-in palette and theme resolution. | — |
| Leaf | `internal/platformkeys` | Physical key chord capture for native keybindings. | — |
| Leaf | `internal/systemstatus` | Host CPU and memory samples for the status bar. | — |
| Leaf | `internal/version` | Release version string. | — |
| Test support | `internal/testutil` | Test-only helpers. Product code never imports them. | integrations |
| Build tools | `internal/tools` | Build-time generators (`make docs`, `make notices`). Not part of the shipped binary. | cli |

`TestRepoMapsListEveryTopLevelInternalDirectory` in `internal/tools/gendocs`
fails when this table misses a top-level `internal/` directory or `cmd/projmux`,
or names a path that does not exist. Like the other repository maps, it also
counts the optional `docs/repo-layout.local.md` table.

### Dependency direction

- Imports point down the table, apart from the one exception below. Nothing
  imports `internal/app` except `cmd/projmux`.
- `internal/testutil` and `internal/tools` sit beside the stack. Only tests
  import `internal/testutil`, and nothing imports `internal/tools`.
- `internal/core` stays free of tmux and process I/O. Clocks, uid sources, and
  filesystem probes are injected by the caller.
- `internal/integrations` may import core types to fill them, for example
  `internal/integrations/metadata` builds a `resourcegraph.Inventory`.
- Known exception: `internal/core/usage/adapters/codex` imports
  `internal/integrations/agents/codexappserver`. It is the only core package
  that imports an integration. The direction is not enforced by a test.
- The repository's Go test suites under `test/` import core and integrations
  directly.

### Main flows

- **CLI command.** `cmd/projmux` calls `internal/app`, which resolves the route
  against the `internal/cli` catalog, applies `internal/config` and
  `internal/state`, asks `internal/core` for the decision, performs it through
  `internal/integrations`, and prints the `internal/cli` output or receipt.
- **Registry convergence.** `internal/integrations/metadata` loads the Registry
  under its lock and observes one exact tmux server into a
  `resourcegraph.Inventory`. `internal/core/resourcegraph` joins it with the
  desired topology, `internal/core/controller` turns the graph into a guarded,
  ordered plan, and `internal/app` executes the plan through the tmux adapter and
  writes the Registry back atomically.
- **Agent message delivery.** `internal/app` validates the sender and target
  against the Registry, `internal/core/agentmessage` builds the
  provider-neutral envelope, `internal/integrations/agents/agentmessage` stores
  it in the local inbox, and the provider adapter under
  `internal/integrations/agents` delivers it to the target Agent.
- **Picker and popup.** `internal/app` builds backend-neutral `picker.Item`
  rows from core state, `internal/ui` renders them and maps keys to actions, and
  the selected action returns to `internal/app` to run.
- **Hook ingest.** A provider hook calls `projmux internal agent-hook ingest`.
  `internal/app` finds the Pane the event came from, records the provider
  session on that Pane and its managed Agent, and queues a notification when
  the event asks for attention. See
  [hooks.md](hooks.md#claude-code-hook-ingest).

### Extension seam

An in-process client layered on top of projmux names itself through
`internal/core/operatorclient`. That package only checks that a client name is
well formed. It knows no particular client.

### Integrations detail

#### Codex app-server compatibility and lifecycle bridge
`internal/integrations/agents/codexappserver` is a Codex-only vertical slice.
It owns the headerless JSON-RPC request, response, and notification wire types,
the newline-delimited direct-stdio framing limit, request IDs,
initialize/initialized handshake, local cancellation, and connection
replacement. The local proxy transport performs the required HTTP Upgrade and
bounded RFC6455 WebSocket framing (including masked client frames) before
carrying those JSON-RPC messages. Core metadata and UI packages receive only
its closed, content-free health result; they do not import app-server request
or event types.

The compatibility probe runs the fixed read-only bridge `codex app-server
proxy` against the local control socket and sends only `initialize` plus
`initialized`. Doctor, Settings, and support-report triggers remain probe-only
and never mutate daemon state. A future native user-action trigger may enter the
lifecycle seam, but only the exact closed `daemon-not-running` classification
(the official local socket is missing or refuses a local connection) may invoke
the installed CLI's idempotent `codex app-server daemon start`, at most once for
the shared in-flight attempt in this process. The start and readiness retry are
bounded, each caller can cancel its own wait, and readiness must still complete
the proxy initialize handshake. All other executable, timeout, unsupported,
protocol, and endpoint failures stay on the existing fallback without a start
attempt.

The bridge discards command output and reports only closed, content-free health
and lifecycle reasons; prompts, tokens, paths, and process output do not cross
the integration boundary. It does not install, bootstrap, restart, or stop the
daemon, change Codex configuration, perform login, manage a custom socket, or
accept remote WebSocket control. Projmux shutdown does not stop the shared
daemon. No existing Agent create/resume, hook, review, catalog, model, or usage
consumer uses the native source in this phase. Settings displays the decision
as a read-only state row; it is not a user-selectable authority.

### UI orchestration

Picker data is modeled independently from row rendering. The app builds
backend-neutral `picker.Item` values (`Title`, `Value`, `SearchText`,
`MetaLines`, `Badges`, `PreviewTarget`) and renders them through the native
picker.

Responsibilities:
- rows for popup and sidebar views
- preview rendering
- keybind-to-action dispatch
- selection handoff into core actions
- picker-agnostic close/dismiss actions

Picker-specific display, search, input, and popup rules are tracked in
[native-picker.md](native-picker.md).

### Local environment

This repo owns the portable application behavior and generated tmux config.

Responsibilities that remain outside `projmux`:
- terminal emulator key dispatch
- shell startup policy
- install-time package checks
- machine-specific path and symlink choices

## Configuration model

Config should be explicit and file-backed.

Candidate areas:
- managed roots (scan roots for candidate discovery; never managed identity)
- default home-like roots
- preview preferences
- session naming exceptions
- ephemeral session retention defaults

## State model

Persistent state:
- pins (typed: managed Project uid, or unregistered candidate path)
- lightweight user preferences

Ephemeral runtime state:
- preview selection
- popup marker files
- current tagged selection set

## Resource attribution model

The Linux resource-attribution core is an ephemeral, read-only projection. A
tmux-specific typed inventory supplies socket/session/window/pane identities,
pane PID/TTY, and the stable session `@projmux_project_path` anchor. A one-pass
procfs collector supplies PID+starttime identity, SID, CPU ticks, RSS, and host
capacity. Pure aggregation builds pane, unique-window, and project rows without
using labels, topics, titles, or cwd-derived names as ownership keys.

Resource snapshots are in-memory only and are never saved or restored. See
[resource-attribution.md](resource-attribution.md) for metric, partial-state,
host-remainder, privacy, and measurement contracts.


## Design notes

The longer design notes live under [design/](design/), one file per subject:

- [Resource metadata model](design/resource-metadata-model.md) — the
  persistent Project, Window, Pane, and Agent Registry: identity, naming,
  ownership, convergence, session history, and the contracts around them.
- [Naming metadata model](design/naming-metadata-model.md) — visible names
  kept apart from source metadata.
- [Notify queue](design/notify-queue.md) — the JSON-backed queue of pending
  notifications.
- [Usage snapshots](design/usage-snapshots.md) — the shared usage manager and
  its snapshot cache.
- [Two-line clickable status bar](design/two-line-status-bar.md) — the second
  tmux status line and its click targets.
- [Related design and inventory notes](design/related-notes.md) — plan-only
  managed runtime mutation, and links to the other design and inventory
  documents.

## Non-goals

- replacing tmux
- owning terminal emulator bindings
- becoming a generic worktree orchestrator
- implementing a fully custom TUI before parity is reached
