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

## Two-line clickable status bar

projmux configures tmux with `status 2`. Line 0 is the existing
session/window/path/git/kube/clock row. Line 1 splits the notification bar
(left half, capped at 80 cells) and the AI usage HUD (right half, capped at
120 cells) using tmux `#[align=left]` / `#[align=right]`. Each clickable
segment is wrapped in a tmux user-defined range (`#[range=user|<id>]...
#[norange]`) and dispatched through `projmux statusbar click <range-id>`. A
single `bind -n MouseDown1Status` covers both lines because tmux fires
`MouseDown1Status` from any line of a multi-line status bar with
`#{mouse_status_range}` resolving to whichever range the cursor was over.

| Range id | Line | Click action                              | Keybinding   |
|----------|------|-------------------------------------------|--------------|
| session  | 0    | display session name (TODO: picker)       | prefix+s s   |
| pwd      | 0    | display pane_current_path                 | prefix+s p   |
| kube     | 0    | (TODO: kube filter picker)                | prefix+s k   |
| git      | 0    | (TODO: git filter picker)                 | prefix+s g   |
| usage    | 1    | popup `projmux usage`                     | prefix+s u   |
| notify   | 1    | focus origin pane of newest notification  | prefix+s n   |

The keyboard chord uses `bind-key s switch-client -T projmux-status` so the
prefix-then-`s`-then-letter shortcut routes through the same dispatcher as
the mouse click. Empty `#{mouse_status_range}` (clicks on whitespace) is a
no-op so the binding never flashes a spurious error.

## Non-goals

- replacing tmux
- owning terminal emulator bindings
- becoming a generic worktree orchestrator
- implementing a fully custom TUI before parity is reached
