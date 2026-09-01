# projmux

<p align="center">
  <img src="docs/assets/projmux-icon.png" alt="projmux icon" width="112">
</p>

<p align="center">
  <strong>A tmux-native workspace for multi-agent AI development.</strong>
  <br>
  <em>Run Codex, Claude Code, and Antigravity side by side in one workspace.</em>
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
  <img src="docs/assets/projmux-overview.gif" alt="projmux workspace: switch projects, pick a session to resume, and continue it next to a running agent" width="820">
</p>

## Keys

- `Alt-1` projects
- `Alt-2` notifications
- `Alt-3` recent windows
- `Alt-4` resume an AI session
- `Alt-5` settings
- `Alt-7` new AI split

`Alt-1`–`Alt-5` work with no configuration. See
[Terminal Keybindings](docs/keybindings.md) to remap them.

## Pick up a conversation

`Alt-4` lists your Codex, Claude Code, and Antigravity sessions. Choose one and
it opens where you left it, as the same conversation.

## Get called back

Agent permission requests and completions land in one grouped inbox. `Alt-2`
takes you to the pane that is waiting.

<p align="center">
  <img src="docs/assets/projmux-ai-attention.gif" alt="projmux attention queue: a notification lands mid-conversation, and one key takes you to the pane that raised it" width="820">
</p>

## Run agents in parallel

Keep a shell, Codex, and Claude Code in the same window. Agents can open other
agents through the same CLI you use:

```sh
projmux create claude --project mobile-client -- "Draft the migration plan."
```

<p align="center">
  <img src="docs/assets/projmux-three-pane-workflow.gif" alt="projmux three-pane workflow: a shell, Codex, and a Claude Code pane Codex opened" width="820">
</p>

Templates and naming conventions are in
[AI Agent Shortcuts](docs/ai-agent-shortcuts.md).

## Also in the app

- Live per-project, per-window, and per-pane CPU/RSS in the
  [Resource Inspector](docs/resource-attribution.md).
- Layout and cwd snapshots in [Session State](docs/session-restore.md).
- Search roots, keybindings, and updates in Settings.

## Requirements

[Node.js](https://nodejs.org/) and npm, plus
[tmux](https://github.com/tmux/tmux/wiki/Installing) 3.4 or newer. Run
`projmux doctor` for read-only local diagnostics, or
[Troubleshooting](docs/troubleshooting.md) if the app will not start.

## Docs

[Install](docs/install.md) ·
[Configuration](docs/configuration.md) ·
[Upgrading](docs/upgrading.md) ·
[CLI Reference](docs/cli.md) ·
[CLI Task Guide](docs/cli-guide.md) ·
[Statusbar](docs/statusbar.md) ·
[Hooks](docs/hooks.md) ·
[Usage tracking](docs/usage-tracking.md) ·
[Operational Diagnostics](docs/operational-diagnostics.md) ·
[Agent Workflow](docs/agent-workflow.md)

## Development

```sh
make build
make fmt
make fix
make test
```

See [Testing](docs/testing.md), [Architecture](docs/architecture.md), and
[Repo Layout](docs/repo-layout.md).

## License

MIT. See [LICENSE](LICENSE).
