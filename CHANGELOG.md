# Changelog

## [0.4.3](https://github.com/crevissepartners/projmux/compare/v0.4.2...v0.4.3) (2026-05-06)


### Features

* prompt for updates on shell startup ([#57](https://github.com/crevissepartners/projmux/issues/57)) ([c3bd949](https://github.com/crevissepartners/projmux/commit/c3bd94925c1847819da083a131d416c5b9812fd7))

## [0.4.2](https://github.com/crevissepartners/projmux/compare/v0.4.1...v0.4.2) (2026-05-06)


### Bug Fixes

* reject go upgrade for npm installs ([#55](https://github.com/crevissepartners/projmux/issues/55)) ([223eed1](https://github.com/crevissepartners/projmux/commit/223eed12cf22ebfc3cfe09a7469369e423d84fc7))

## [0.4.1](https://github.com/crevissepartners/projmux/compare/v0.4.0...v0.4.1) (2026-05-06)


### Features

* add npm binary package scaffold ([#47](https://github.com/crevissepartners/projmux/issues/47)) ([71252e3](https://github.com/crevissepartners/projmux/commit/71252e33bcafc31526af69d6d0a561ac6cfff01a))
* add update status check ([#44](https://github.com/crevissepartners/projmux/issues/44)) ([b32317a](https://github.com/crevissepartners/projmux/commit/b32317a08ec985b62fb6efc5f2c26be671b94c30))
* apply installer updates ([#49](https://github.com/crevissepartners/projmux/issues/49)) ([fb5a797](https://github.com/crevissepartners/projmux/commit/fb5a7971f6ad1a8de481230efbde13fd6eea038e))
* install missing doctor deps ([#50](https://github.com/crevissepartners/projmux/issues/50)) ([bfe431d](https://github.com/crevissepartners/projmux/commit/bfe431d32d8c74ae8c1194ee0f62d9446f6914ba))
* show updates in settings ([#46](https://github.com/crevissepartners/projmux/issues/46)) ([da1ebb7](https://github.com/crevissepartners/projmux/commit/da1ebb75e1d220cbf526883fcbacd808446a76ab))
* update from GitHub releases ([#51](https://github.com/crevissepartners/projmux/issues/51)) ([acea5eb](https://github.com/crevissepartners/projmux/commit/acea5ebb978092ce1a8ceff29d4fb3f5588c6bc3))


### Bug Fixes

* include release installer update guidance ([#52](https://github.com/crevissepartners/projmux/issues/52)) ([525698f](https://github.com/crevissepartners/projmux/commit/525698fe86709a74c0f2e2f1d1b377fd2724d218))
* keep update tests release-safe ([#54](https://github.com/crevissepartners/projmux/issues/54)) ([560375f](https://github.com/crevissepartners/projmux/commit/560375f8701ec357db459eefcb1760185eadad82))

## [0.4.0](https://github.com/crevissepartners/projmux/compare/v0.3.0...v0.4.0) (2026-05-06)


### ⚠ BREAKING CHANGES

* rename PROJDIR env to PROJMUX_PROJDIR and support multi-path ([#12](https://github.com/crevissepartners/projmux/issues/12))

### Features

* **doctor:** add projmux doctor for runtime dep diagnostics ([#19](https://github.com/crevissepartners/projmux/issues/19)) ([78a0376](https://github.com/crevissepartners/projmux/commit/78a0376462d18a8f308c3b4299f39e5cd80baff6))
* **doctor:** enforce minimum tmux 3.4 and fzf 0.55 with [stale] status ([#20](https://github.com/crevissepartners/projmux/issues/20)) ([2bc3b0f](https://github.com/crevissepartners/projmux/commit/2bc3b0f379a0d774e34b97264a1c9fc2a1fe9cac))
* **focus:** unified focus dispatch core (projmux focus) ([#23](https://github.com/crevissepartners/projmux/issues/23)) ([740d175](https://github.com/crevissepartners/projmux/commit/740d175267129349f5b11ef674f649a9bedee1fe))
* **hooks:** add post-create hook for tmux sessions ([#10](https://github.com/crevissepartners/projmux/issues/10)) ([6d3baf6](https://github.com/crevissepartners/projmux/commit/6d3baf60a89cefc1b8103d0f2615ac325d22cc67))
* **init:** add `projmux init` to auto-merge keybindings (Ghostty first) ([6ba3f7a](https://github.com/crevissepartners/projmux/commit/6ba3f7a7604d2705291ba0034c0fcb4190d39219))
* **init:** add projmux init to auto-merge keybindings (Ghostty first) ([#14](https://github.com/crevissepartners/projmux/issues/14)) ([6ba3f7a](https://github.com/crevissepartners/projmux/commit/6ba3f7a7604d2705291ba0034c0fcb4190d39219))
* **init:** add Windows Terminal adapter (WSL + native) ([#15](https://github.com/crevissepartners/projmux/issues/15)) ([fca004d](https://github.com/crevissepartners/projmux/commit/fca004d27ceae8d2ff783bca341277c30c751fa0))
* **init:** handle Ghostty config/config.ghostty paths and symlinks safely ([#16](https://github.com/crevissepartners/projmux/issues/16)) ([6ac5b76](https://github.com/crevissepartners/projmux/commit/6ac5b76512e5ac61fd1ee4ed041750eb93b063cb))
* **notify-producer:** push reply-ready into queue on attention transitions ([#29](https://github.com/crevissepartners/projmux/issues/29)) ([5fcd6bc](https://github.com/crevissepartners/projmux/commit/5fcd6bc93a4e17b5232a5f97a41a4ac0cd2780f3))
* **notify:** persistent notification queue (push/list/ack) ([#22](https://github.com/crevissepartners/projmux/issues/22)) ([d1a2af6](https://github.com/crevissepartners/projmux/commit/d1a2af69695019ce5c9c516baa334a42296bab12))
* **notify:** reconcile subcommand to back-fill queue from live pane state ([#32](https://github.com/crevissepartners/projmux/issues/32)) ([2853425](https://github.com/crevissepartners/projmux/commit/28534255b63ba69b4fce7d020658c4b2c4eb96a1))
* rename PROJDIR env to PROJMUX_PROJDIR and support multi-path ([#12](https://github.com/crevissepartners/projmux/issues/12)) ([20f6d41](https://github.com/crevissepartners/projmux/commit/20f6d417bc9a9d78d72fb74be8253b713ba03389))
* **setup:** add `projmux setup` to probe terminal key delivery ([f24815f](https://github.com/crevissepartners/projmux/commit/f24815f4f12f202f33dd2edb7a8a6fb7025f96ac))
* **setup:** add projmux setup to probe terminal key delivery ([#13](https://github.com/crevissepartners/projmux/issues/13)) ([f24815f](https://github.com/crevissepartners/projmux/commit/f24815f4f12f202f33dd2edb7a8a6fb7025f96ac))
* **status-notify:** HUD-style redesign with severity icon, agent block, age ([#34](https://github.com/crevissepartners/projmux/issues/34)) ([d74ca36](https://github.com/crevissepartners/projmux/commit/d74ca36fcc3e0980266f9936cd6a58897d5e0508))
* **status-notify:** HUD-style redesign with severity icon, agent block, age, and width tiers ([d74ca36](https://github.com/crevissepartners/projmux/commit/d74ca36fcc3e0980266f9936cd6a58897d5e0508))
* **status-notify:** render leading severity+agent as solid color badge ([#37](https://github.com/crevissepartners/projmux/issues/37)) ([b0f60a1](https://github.com/crevissepartners/projmux/commit/b0f60a18d4f4f0d562d9f314829252120c82edf9))
* **statusbar:** collapse to two-line layout (notify+usage split row) ([#27](https://github.com/crevissepartners/projmux/issues/27)) ([8a4acab](https://github.com/crevissepartners/projmux/commit/8a4acabed541c3e9d06e7bbb109a871481a371c9))
* **statusbar:** three-line clickable status bar ([#25](https://github.com/crevissepartners/projmux/issues/25)) ([76e0b1d](https://github.com/crevissepartners/projmux/commit/76e0b1d6ec4df8c2aad546130c8df8078366c24e))
* **usage/hud:** show Claude last-sync age, drop tilde markers ([#35](https://github.com/crevissepartners/projmux/issues/35)) ([937e2af](https://github.com/crevissepartners/projmux/commit/937e2affedd4f55db1f32c67b9cd1aa97b15a9fc))
* **usage/hud:** show last-sync age indicator for claude, drop ~ markers ([937e2af](https://github.com/crevissepartners/projmux/commit/937e2affedd4f55db1f32c67b9cd1aa97b15a9fc))
* **usage:** codex/claude usage tracker (5h + weekly) ([#24](https://github.com/crevissepartners/projmux/issues/24)) ([2eade85](https://github.com/crevissepartners/projmux/commit/2eade85ef541c9d6fb0c024e2a5e03d31b349412))
* **usage:** HUD bar layout + 30s auto-refresh ([#26](https://github.com/crevissepartners/projmux/issues/26)) ([26a07b4](https://github.com/crevissepartners/projmux/commit/26a07b407cf90aa8ba84f15f74cd94bab77b8b72))
* **usage:** replace local token counting with authoritative server-side data ([#28](https://github.com/crevissepartners/projmux/issues/28)) ([1d2740a](https://github.com/crevissepartners/projmux/commit/1d2740a39a1d8df09691fb8ff76b49067117b71c))


### Bug Fixes

* **statusbar:** ack notify entry after successful click-to-focus ([#33](https://github.com/crevissepartners/projmux/issues/33)) ([7bf3635](https://github.com/crevissepartners/projmux/commit/7bf36356c72cce86cfd69e53ea19fd38191f3c4c))
* **statusbar:** parse click flags in any order around positional ([#39](https://github.com/crevissepartners/projmux/issues/39)) ([0ccb84f](https://github.com/crevissepartners/projmux/commit/0ccb84ff4d3e00cab13ce600b13f32ee2f5320a6))
* **statusbar:** pass mouse-window so window-list click still switches tabs ([#38](https://github.com/crevissepartners/projmux/issues/38)) ([001eb93](https://github.com/crevissepartners/projmux/commit/001eb9374867d2476867ce94b80728ece00c76c6))
* **statusbar:** route window|&lt;idx&gt; range tokens to window-list handler ([#40](https://github.com/crevissepartners/projmux/issues/40)) ([7d7c134](https://github.com/crevissepartners/projmux/commit/7d7c13446c29529c983d6d67eefe45d9c29e5684))
* **statusbar:** short-circuit window-list clicks to native select-window ([#41](https://github.com/crevissepartners/projmux/issues/41)) ([863f79e](https://github.com/crevissepartners/projmux/commit/863f79ebf4eedb6125743120a626027f54533e27))
* **statusbar:** swallow focus exit codes — show toast instead of tmux error popup ([72d862a](https://github.com/crevissepartners/projmux/commit/72d862a153b16ec29a42777b3a7018dc72f8680e))
* **statusbar:** swallow focus exit codes — toast instead of tmux error popup ([#36](https://github.com/crevissepartners/projmux/issues/36)) ([72d862a](https://github.com/crevissepartners/projmux/commit/72d862a153b16ec29a42777b3a7018dc72f8680e))
* **statusbar:** use tmux block syntax for window-click if-shell to fix syntax error ([#42](https://github.com/crevissepartners/projmux/issues/42)) ([4149f69](https://github.com/crevissepartners/projmux/commit/4149f6960d88058f2ea6cde69e4dd16c912f40f4))
* **switch:** wire sidebar focus binding to switch sessions on navigation ([#18](https://github.com/crevissepartners/projmux/issues/18)) ([cfcf34a](https://github.com/crevissepartners/projmux/commit/cfcf34a39cf1358f3c6345956a687c672f89ef79))
* **usage:** claude throttle 5min, backoff 30m-1h, --force flag ([#31](https://github.com/crevissepartners/projmux/issues/31)) ([2f218db](https://github.com/crevissepartners/projmux/commit/2f218db02599c283cd54169615bd5aeb989ed2cf))
* **usage:** preserve snapshots on failure, per-adapter throttle, 429 backoff ([#30](https://github.com/crevissepartners/projmux/issues/30)) ([6edf0e1](https://github.com/crevissepartners/projmux/commit/6edf0e1c6132d2b441ecaef10d4764a6a04b0924))

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
