# Roadmap

## Milestone 0: Bootstrap
- repository scaffold
- agent docs
- architecture and migration plan

## Milestone 1: Core parity
- session naming package
- pin store
- preview state store
- current-directory jump command
- tagged kill logic

## Milestone 2: Sessionizer parity
- candidate discovery
- popup mode
- sidebar mode
- settings hub and pin management
- preview target restore on selection

## Milestone 3: Session popup parity
- list sessions by recent activity
- session preview metadata
- pane snapshot support
- window and pane cycling

## Milestone 4: Lifecycle helpers
- auto attach
- ephemeral prune
- kube session support

## Milestone 5: Standalone integration
- replace shell wrappers with thin `projmux` commands
- update generated install flow
- document setup steps for source and target machines
- define picker-agnostic popup close/toggle handling so AI picker dismissal does not depend on fzf-specific key bindings

## Milestone 6: Rich picker UI
- introduce a picker-domain model separate from fzf row encoding
- keep fzf as the stable fallback backend
- add an opt-in native picker backend for multi-line card rows and title-focused search
- port switcher popup/sidebar surfaces after parity tests cover selection, preview, and key actions
