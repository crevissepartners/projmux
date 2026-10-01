# Repository Layout

## Current layout

The tree lists every top-level `internal/` directory and a few of the packages
below them. It is not a full package list.
`TestRepoMapsListEveryTopLevelInternalDirectory` in `internal/tools/gendocs`
fails when a top-level `internal/` directory is missing from this tree or from
the `AGENTS.md` repo map, or when a path in this tree does not exist.

A derived tree can list the top-level directories it adds in an optional
`docs/repo-layout.local.md` table instead of editing these maps. The test
counts that table toward both maps, and every path it lists must exist.

```text
projmux/
  cmd/
    projmux/
  internal/
    aiprovider/
    app/
    cli/
    config/
    core/
      candidates/
      pins/
      preview/
      sessions/
    diagnostics/
    i18n/
    integrations/
      agents/
      hooks/
      metadata/
      mux/
      procfsresources/
      tmux/
      tmuxexec/
      tmuxopts/
    platformkeys/
    state/
    systemstatus/
    testutil/
    theme/
    tools/
      gendocs/
      gennotices/
    ui/
      picker/
      pickercompat/
      projmuxpicker/
      render/
    version/
  docs/
  npm/
  scripts/
  test/
    integration/
    e2e/
    install/
```

## Notes

- `cmd/projmux` contains only CLI wiring.
- `internal/app` implements the commands and wires the other packages together.
- `internal/cli` owns the canonical command catalog, help, output, and receipts.
- `internal/core` contains product behavior that should be testable without tmux.
- `internal/aiprovider` is the AI provider registry: IDs, names, binaries, and capability flags.
- `internal/diagnostics` is the bounded operational event journal that `projmux diagnostics` reads.
- `internal/i18n` holds the message catalog and locales; `internal/theme` holds the built-in palette and theme resolution.
- `internal/platformkeys` captures physical key chords for native keybindings. Only macOS builds have a real
  source; other platforms get a stub.
- `internal/systemstatus` samples host CPU and memory for the status bar.
- `internal/testutil` holds test-only support packages. Product code never imports them.
- `internal/version` holds the release version string that release-please bumps.
- `internal/tools/gendocs` is a build-time `main` package, not part of the shipped
  binary. `make docs` runs it to regenerate `docs/cli.md` from the command manifest.
- `internal/tools/gennotices` is a build-time `main` package too. `make notices` runs it
  to regenerate `THIRD_PARTY_NOTICES` from the modules `./cmd/projmux` links.
- `internal/integrations/tmux` should be the only place that knows tmux command strings and output formats.
- `internal/ui/picker` and `internal/ui/projmuxpicker` own native picker behavior.
- `internal/ui/pickercompat` is an internal compatibility option/result shape for older app call sites. It is not a runtime backend; product code should route through the native picker.
- `scripts/` is for development tooling only, not product logic.
