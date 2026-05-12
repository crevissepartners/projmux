# Settings Information Architecture

This branch finishes the current Settings/onboarding roadmap slice with a
view-first layout:

- `Settings > Project Picker > Workdirs` is the list/overview entry. Add/remove
  actions live inside that view.
- `Settings > Project Picker > Project Root` shows effective and saved values
  first, then the edit actions, then the explanatory hints.
- `Settings > Keybindings` is the single entry point for keybinding work. The
  page is split into four chips: `Bindings`, `Diagnostic`, `Probe`, and `Init`.
- `Settings > Labs` keeps experimental toggles, but keybindings no longer have a
  visible Labs row. The hidden compatibility action still redirects to the
  unified Keybindings page.
- `Settings > Project > Project recipe` is the functional label for
  `.projmux/config.toml`. Search still matches `config.toml` as an alias.

Hooks remain the reference pattern for this IA:

- project-scoped rows stay editable in-app
- global/system rows are read-only in-app
- project overrides are created intentionally from the project surface or
  `projmux hook edit <event> --project`

Shell bootstrap UX is phase-split:

- Phase 1 is complete in this branch: `projmux welcome`, the About-screen
  Welcome entry, and `pending_attach_welcome` state make the guide revisit-able.
- Phase 2 is not implemented here: there is no automatic attach-time popup from
  the status bar tick path in this branch.
