# projmux

`projmux` is a terminal-first project-to-tmux session manager.

The product goal is narrow and explicit:

- map project directories to stable tmux sessions
- let users switch, preview, create, and clean up those sessions quickly
- keep terminal-specific keybindings and machine-specific install policy outside the core app

## Current status

This repository is in bootstrap.

The current baseline focuses on:

- defining the split between `projmux` and the existing `dotfiles` repo
- documenting the command surface and repository layout
- establishing agent workflow rules for parallel `wt`-based execution
- providing a minimal Go entrypoint and a placeholder-safe `Makefile`

## Product boundary

`projmux` owns:

- directory to session identity rules
- candidate discovery and root scanning
- pin management
- session create/switch/attach logic
- session preview state and selection state
- session lifecycle helpers such as tagged kill and ephemeral pruning
- session-scoped integration data such as per-session kubeconfig

`dotfiles` keeps owning:

- `.tmux.conf` bindings
- Ghostty or Windows Terminal key dispatch
- zsh auto-attach policy
- machine-specific install and symlink logic

## Development

Common commands:

- `make fmt`
- `make fix`
- `make test`
- `make test-integration`
- `make test-e2e`
- `make verify`

## Document map

- [AGENTS.md](AGENTS.md)
- [Architecture](docs/architecture.md)
- [CLI Shape](docs/cli.md)
- [Repo Layout](docs/repo-layout.md)
- [Migration Plan](docs/migration-plan.md)
- [Roadmap](docs/roadmap.md)
- [Agent Workflow](docs/agent-workflow.md)
