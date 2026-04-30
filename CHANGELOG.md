# Changelog

## [0.4.0](https://github.com/crevissepartners/projmux/compare/v0.3.0...v0.4.0) (2026-04-30)


### ⚠ BREAKING CHANGES

* rename PROJDIR env to PROJMUX_PROJDIR and support multi-path ([#12](https://github.com/crevissepartners/projmux/issues/12))

### Features

* **doctor:** add projmux doctor for runtime dep diagnostics ([#19](https://github.com/crevissepartners/projmux/issues/19)) ([78a0376](https://github.com/crevissepartners/projmux/commit/78a0376462d18a8f308c3b4299f39e5cd80baff6))
* **doctor:** enforce minimum tmux 3.4 and fzf 0.55 with [stale] status ([#20](https://github.com/crevissepartners/projmux/issues/20)) ([2bc3b0f](https://github.com/crevissepartners/projmux/commit/2bc3b0f379a0d774e34b97264a1c9fc2a1fe9cac))
* **hooks:** add post-create hook for tmux sessions ([#10](https://github.com/crevissepartners/projmux/issues/10)) ([6d3baf6](https://github.com/crevissepartners/projmux/commit/6d3baf60a89cefc1b8103d0f2615ac325d22cc67))
* **init:** add `projmux init` to auto-merge keybindings (Ghostty first) ([6ba3f7a](https://github.com/crevissepartners/projmux/commit/6ba3f7a7604d2705291ba0034c0fcb4190d39219))
* **init:** add projmux init to auto-merge keybindings (Ghostty first) ([#14](https://github.com/crevissepartners/projmux/issues/14)) ([6ba3f7a](https://github.com/crevissepartners/projmux/commit/6ba3f7a7604d2705291ba0034c0fcb4190d39219))
* **init:** add Windows Terminal adapter (WSL + native) ([#15](https://github.com/crevissepartners/projmux/issues/15)) ([fca004d](https://github.com/crevissepartners/projmux/commit/fca004d27ceae8d2ff783bca341277c30c751fa0))
* **init:** handle Ghostty config/config.ghostty paths and symlinks safely ([#16](https://github.com/crevissepartners/projmux/issues/16)) ([6ac5b76](https://github.com/crevissepartners/projmux/commit/6ac5b76512e5ac61fd1ee4ed041750eb93b063cb))
* rename PROJDIR env to PROJMUX_PROJDIR and support multi-path ([#12](https://github.com/crevissepartners/projmux/issues/12)) ([20f6d41](https://github.com/crevissepartners/projmux/commit/20f6d417bc9a9d78d72fb74be8253b713ba03389))
* **setup:** add `projmux setup` to probe terminal key delivery ([f24815f](https://github.com/crevissepartners/projmux/commit/f24815f4f12f202f33dd2edb7a8a6fb7025f96ac))
* **setup:** add projmux setup to probe terminal key delivery ([#13](https://github.com/crevissepartners/projmux/issues/13)) ([f24815f](https://github.com/crevissepartners/projmux/commit/f24815f4f12f202f33dd2edb7a8a6fb7025f96ac))


### Bug Fixes

* **switch:** wire sidebar focus binding to switch sessions on navigation ([#18](https://github.com/crevissepartners/projmux/issues/18)) ([cfcf34a](https://github.com/crevissepartners/projmux/commit/cfcf34a39cf1358f3c6345956a687c672f89ef79))

## [0.3.0](https://github.com/crevissepartners/projmux/compare/v0.2.1...v0.3.0) (2026-04-29)


### ⚠ BREAKING CHANGES

* the Go module path changed; downstream importers and anyone following the published `go install` / `git clone` URLs must update to the new owner.

### Features

* **ai:** add 'topic' subcommand for pane topic control ([#5](https://github.com/crevissepartners/projmux/issues/5)) ([5512fe0](https://github.com/crevissepartners/projmux/commit/5512fe03426ea1289692e7128039a48845c93e30))
* **ai:** add 'topic' subcommand for tmux pane topic option control ([5512fe0](https://github.com/crevissepartners/projmux/commit/5512fe03426ea1289692e7128039a48845c93e30))
* discover codex/claude binaries under nvm/fnm/asdf/volta ([7d75be0](https://github.com/crevissepartners/projmux/commit/7d75be0e2c41243d6341b04850bcad6da78bd729))


### Bug Fixes

* **ai:** prepend agent bin dir to PATH so node-managed CLIs find node ([b10fdaf](https://github.com/crevissepartners/projmux/commit/b10fdafdfab6f41be08765544cffaebabdbb6597))


### Miscellaneous Chores

* transfer ownership to crevissepartners ([#6](https://github.com/crevissepartners/projmux/issues/6)) ([dc1720b](https://github.com/crevissepartners/projmux/commit/dc1720b8968eae83fce2236d2ab827485784378c))
