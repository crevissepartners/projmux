# tmux Surface Inventory

This document inventories the tmux command, format-variable, hook, and option
surface that projmux currently calls or generates. It is the maintained
architecture boundary for the supported tmux runtime.

## Scope And Method

Primary production sources:

- `internal/integrations/mux/`
- `internal/integrations/tmux/`
- `internal/integrations/sessionstate/`
- `internal/app/`
- generated config from `projmux config render standalone`,
  `projmux config render app`, and `projmux shell`

Support and fixture sources:

- `test/`
- current docs that describe generated tmux snippets

Useful inventory searches:

- `rg 'run\("tmux"|read\("tmux"|exec\.Command\("tmux"' .`
- `rg 'display-message -p|display-popup|capture-pane|set-hook' .`
- `rg '@projmux_|#\{[^}]+\}' .`

Test fixture command strings confirm production contracts; they are not
separate product surface unless a corresponding runtime path exists.

## Integration Boundaries

`internal/integrations/mux` is a thin semantic command boundary over tmux.
App-layer subprocess calls should use its typed operations when one exists so
argument construction, output parsing, and test injection remain centralized.
Existing typed clients in `internal/integrations/tmux` remain the correct home
for richer session, window, pane, focus, and resource inventory models.

The boundary is intentionally pragmatic:

- keep semantic helpers where they encode tmux meaning or parsing policy;
- keep injectable runners where tests need deterministic command evidence;
- keep rich domain conversion in typed integration clients;
- avoid spreading raw tmux invocation through app packages;
- avoid introducing an abstraction that only renames a single local call.

## Semantic Command Surface

### Metadata And Inventory

- `SetPaneOption`, `UnsetPaneOption`, and `ShowPaneOption` cover pane
  user-option writes, unsets, and reads.
- `DisplayMessage` and `DisplayMessageTrimmed` cover formatted reads.
- `ListPanes`, `ListWindows`, and `DisplayPaneFields` cover structured
  pane/window inventory.
- `TmuxFormat`, `PaneOptionFormat`, `JoinFormats`, `FieldDelimiter`,
  and `ParseFormatRows` centralize format assembly and row parsing.

These surfaces support AI metadata, attention state, notify reconciliation,
focus resolution, preview, and recent-window inventory.

### Interactive Commands

- `DisplayPopup` and `ClosePopup` own popup launch/close argument ordering.
- `CapturePane` owns raw and joined capture forms.
- `SwitchClient`, `SelectPane`, and `SelectWindow` own focus/navigation
  targeting.
- `ResizePane` owns split-layout follow-up sizing.

### Lifecycle, Split, Hooks, And Options

- `NewSession`, `NewWindow`, and `SplitWindow` own creation commands.
- `SplitWindow` and `NewSession` optionally return pane ids from
  `-P -F "#{pane_id}"`; automation callers require the exact `%N` result.
- `SetHook`, `SetOption`, and `ShowOption` own generated hook/option
  mutations and reads.
- socket and config targeting stay explicit in the typed option structs.

The typed `internal/integrations/tmux.Client` owns higher-level operations
such as ensure/open/kill sessions, recent session summaries, preview inventory,
resource inventory, and session-state capture/replay.

## Generated Configuration Surface

The standalone and app configs own:

- app/runtime marker options such as `@projmux_app`;
- status rows, ranges, palette options, mouse dispatch, and popup bindings;
- AI, attention, notify, recent-window, session-state, and resource hooks;
- project-root and live-resource options;
- reload/apply behavior for default and named sockets.

Generated callbacks must continue to quote executable paths and user-controlled
arguments as data. Config changes require unit coverage for exact snippets plus
integration or e2e coverage when live tmux behavior changes.

## Maintained Command Inventory

| Family | Representative tmux surface | Main consumers |
| --- | --- | --- |
| Identity | `display-message -p`, `list-clients -F` | focus, switch, hooks |
| Pane/window inventory | `list-panes -a -F`, `list-windows -F` | preview, notify, attention, recent windows |
| Session lifecycle | `has-session`, `new-session`, `attach-session`, `switch-client`, `kill-session` | attach, switch, sessions |
| Split/window creation | `split-window`, `new-window` | AI split, shell, session restore |
| Metadata | `set-option -p`, `show-options`, `display-message` | AI state, labels, app ownership |
| Hooks | `set-hook`, `show-hooks`, `run-shell -b` | notify, attention, autosave, recent windows |
| Interactive UI | `display-popup`, `capture-pane`, `resize-pane` | popup surfaces, title watch, layout |
| State replay | `rename-window`, `select-layout`, `select-pane`, `send-keys` | session state |
| Config | `source-file`, global/session options | install, apply, shell |
| Resource inventory | `list-panes -a -F` with PID/TTY/project fields | Linux resource attribution |

## Validation Contract

Changes to this surface should preserve the repository validation order:

1. focused unit tests for the affected semantic helper or typed client;
2. `make fmt`;
3. `make fix`;
4. `make test`;
5. `make test-integration`;
6. `make test-e2e`.

The maintained test list in [agent-workflow.md](agent-workflow.md) must change
with behavior. Live tests must use isolated tmux sockets and must validate
returned ids or queried server state rather than pane ordering, screen content,
or `send-keys` as a completion signal.
