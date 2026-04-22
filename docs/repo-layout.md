# Repository Layout

## Planned layout

```text
projmux/
  cmd/
    projmux/
  internal/
    app/
    config/
    core/
      candidates/
      pins/
      preview/
      sessions/
    integrations/
      filesystem/
      git/
      kube/
      tmux/
    state/
    ui/
      fzf/
      render/
    version/
  docs/
  scripts/
  test/
    integration/
    e2e/
```

## Notes

- `cmd/projmux` contains only CLI wiring.
- `internal/core` contains product behavior that should be testable without tmux.
- `internal/integrations/tmux` should be the only place that knows tmux command strings and output formats.
- `internal/ui/fzf` may depend on shelling out to `fzf`, but should call typed core services.
- `scripts/` is for development tooling only, not product logic.

## Early implementation order

1. `internal/core/sessions`
2. `internal/core/candidates`
3. `internal/core/pins`
4. `internal/state`
5. `internal/integrations/tmux`
6. `internal/ui/fzf`
7. CLI command wiring
