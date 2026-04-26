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

### 1. Core
Pure rules and state transitions.

Responsibilities:
- directory normalization
- session naming
- candidate ordering
- pin state changes
- tagged selection state
- lifecycle decisions such as reuse, create, kill, fallback

This layer should not shell out directly.

### 2. Integrations
Adapters for external systems.

Initial adapters:
- tmux
- kubeconfig per-session state
- filesystem
- git metadata for preview enrichment

Responsibilities:
- execute commands
- parse command output
- convert failures into typed errors

### 3. UI orchestration
The first implementation can remain `fzf`-driven, but picker data should be
modeled independently from fzf rows so richer backends can render multi-line
cards without changing core selection behavior.

Responsibilities:
- rows for popup and sidebar views
- preview rendering
- keybind-to-action dispatch
- selection handoff into core actions

Picker-specific display and search rules are tracked in
[picker-ui-plan.md](picker-ui-plan.md).

This keeps parity with the existing shell workflow while moving state and behavior into Go.

### 4. Local environment
This repo owns the portable application behavior and generated tmux config.

Responsibilities that remain outside `projmux`:
- terminal emulator key dispatch
- shell startup policy
- install-time package checks
- machine-specific path and symlink choices

## Configuration model

Config should be explicit and file-backed.

Candidate areas:
- managed roots
- default home-like roots
- preview preferences
- session naming exceptions
- kube session settings
- ephemeral session retention defaults

## State model

Persistent state:
- pins
- lightweight user preferences

Ephemeral runtime state:
- preview selection
- popup marker files
- current tagged selection set

## Non-goals

- replacing tmux
- owning terminal emulator bindings
- becoming a generic worktree orchestrator
- implementing a fully custom TUI before parity is reached
