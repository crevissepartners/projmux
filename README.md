# projmux

<p align="center">
  <img src="docs/assets/projmux-icon.png" alt="projmux icon" width="112">
</p>

<p align="center">
  <strong>A tmux-native workspace for multi-agent AI development.</strong>
  <br>
  <em>Managed Claude Code, Codex, and Antigravity panes with hook-driven attention and agent-aware session resume.</em>
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/projmux"><img src="https://img.shields.io/npm/v/projmux?logo=npm" alt="npm version"></a>
  <a href="https://github.com/crevissepartners/projmux/actions/workflows/ci.yml"><img src="https://github.com/crevissepartners/projmux/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/crevissepartners/projmux" alt="MIT license"></a>
  <a href="README-ko.md"><img src="https://img.shields.io/badge/lang-한국어-blue" alt="Korean README"></a>
</p>

```sh
npm install -g projmux
projmux shell
```

<p align="center">
  <img src="docs/assets/projmux-ai-attention.gif" alt="projmux AI attention demo" width="820">
  <br>
  <em>Resume an existing AI session, keep working in another project, then jump back when the managed pane needs permission.</em>
</p>

## Why

Six tmux windows. Each one is running Claude Code, Codex, or Antigravity on a
different repo. Three are idle. One is waiting on a permission prompt. One
crashed an hour ago and you have no idea which.

projmux ingests Claude Code and Codex hook events directly, accepts manually
wired Antigravity hook/statusline events, shows live per-pane state in the tmux
status bar, and lets one keystroke take you to the pane that actually needs
you. It also remembers each agent's resume id, so after a reboot every pane
comes back as the *same* conversation — not a fresh one.

## Requirements

- [Node.js](https://nodejs.org/) and npm, for the main install path.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) **3.4 or newer**.

Run `projmux doctor` after installing to check the local runtime.

## Install

The npm package shown above installs a small Node.js shim plus the matching
projmux binary for Linux and macOS on x64 or arm64. npm is the primary
distribution path for normal users.

Verify with `projmux version`. Then `projmux doctor` checks the local runtime
(tmux 3.4+ and hook integration health).

Manual Go, source checkout, GitHub Release, and packaging details live in
[Install](docs/install.md).

## Quick Start

Open the isolated projmux tmux app:

```sh
projmux shell
```

Inside the app:

- `Alt-1` opens the project sidebar.
- `Alt-2` opens the notification list.
- `Alt-3` opens Recent Windows.
- `Alt-4` opens the AI resume session picker.
- `Alt-5` opens settings.
- `Alt-7` opens the AI split picker.

The `Alt-1` through `Alt-5` launch keys are the guaranteed zero-config
defaults. `Alt-7` is an additional editable built-in default. Add aliases in
Settings > Keybindings or `~/.config/projmux/keymap.toml`. If a key does not
fire, the Darwin release uses a native physical-key adapter automatically
inside `projmux shell` after one-time macOS Accessibility approval. On other
paths, run `projmux setup` outside tmux, then use
`projmux setup terminal [terminal] --apply` for supported terminal delivery
fallbacks.

## Day-To-Day Use

- Pick a project directory and projmux creates or reuses its tmux session.
- Pin important projects so they stay easy to reach.
- Preview windows, panes, git branch, Kubernetes context, and AI pane state
  before switching.
- Resume an existing Claude or Codex conversation, or open a new managed AI
  split.
- Review permission and completion events in one notification queue, then jump
  directly to the pane that needs attention.
- Use Settings > Project Picker to add roots and workdirs without editing env
  vars.
- Use Settings > About > Update or `projmux update apply` to upgrade.

<p align="center">
  <img src="docs/assets/projmux-shell-sidebar.gif" alt="projmux project switch and managed Codex demo" width="820">
  <br>
  <em>Switch projects, launch a managed Codex pane, and review its completion notification.</em>
</p>

For detailed configuration, including `PROJMUX_PROJDIR`, managed roots,
notifications, and usage tracking, see [Configuration](docs/configuration.md).
For update behavior by installer type, see [Upgrading](docs/upgrading.md).

## Multi-Agent Workflow

Keep a shell and multiple managed agents visible in the same tmux window.
Move between panes to start independent tasks while projmux collects their
permission and completion events in one notification queue.

<p align="center">
  <img src="docs/assets/projmux-three-pane-workflow.gif" alt="projmux shell, Codex, and Claude 3-pane workflow demo" width="820">
  <br>
  <em>Move across equal-width shell, Codex, and Claude panes while independent tasks report back through one notification queue.</em>
</p>

## Automation With Agent Skills

AI tools can invoke the same projmux CLI to open a managed pane and deliver a
prompt:

```sh
projmux ai split --agent codex right -- "Review the retry logic."
```

Templates and naming conventions for Claude, Codex, and other agents are in
[AI Agent Shortcuts](docs/ai-agent-shortcuts.md).

## More Docs

- [Install](docs/install.md)
- [Configuration](docs/configuration.md)
- [Terminal Keybindings](docs/keybindings.md)
- [AI Agent Shortcuts](docs/ai-agent-shortcuts.md)
- [CLI Reference](docs/cli.md)
- [Statusbar](docs/statusbar.md)
- [Hooks](docs/hooks.md)
- [Usage tracking](docs/usage-tracking.md)
- [Agent Workflow](docs/agent-workflow.md)

## Development

```sh
make build
make fmt
make fix
make test
```

See [Testing](docs/testing.md), [Architecture](docs/architecture.md), and
[Repo Layout](docs/repo-layout.md) for contributor details.

## License

MIT. See [LICENSE](LICENSE).
