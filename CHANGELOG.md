# Changelog

## [0.4.7](https://github.com/crevissepartners/projmux/compare/v0.4.6...v0.4.7) (2026-05-10)


### Features

* **picker:** add experimental native picker engine ([#98](https://github.com/crevissepartners/projmux/issues/98)) ([c49aa68](https://github.com/crevissepartners/projmux/commit/c49aa68cc05fdde59f387ea0938f7b531fe23ba6))
* **picker:** make native picker default ([#113](https://github.com/crevissepartners/projmux/issues/113)) ([5609d1e](https://github.com/crevissepartners/projmux/commit/5609d1e47a5ad54b2eb3fe8b9d008f0d0120b958))


### Bug Fixes

* **notify:** decorate sidebar popup title ([#104](https://github.com/crevissepartners/projmux/issues/104)) ([b19ec03](https://github.com/crevissepartners/projmux/commit/b19ec03c8a5ffa686234834373a49cf71d318484))
* **notify:** make sidebar navigation-only ([#100](https://github.com/crevissepartners/projmux/issues/100)) ([11ce5d3](https://github.com/crevissepartners/projmux/commit/11ce5d38c44fd4e0044e4bde6fcd9a46282faafc))
* **picker:** align native metadata indent ([#108](https://github.com/crevissepartners/projmux/issues/108)) ([0b55df8](https://github.com/crevissepartners/projmux/commit/0b55df8b8c4fb3f2436048a926b47fdbc494cb31))
* **picker:** keep sidebars off statusbar ([#111](https://github.com/crevissepartners/projmux/issues/111)) ([320e023](https://github.com/crevissepartners/projmux/commit/320e0231987c853eb4c8decce48aa093366e92a3))
* **picker:** polish native switch preview ([#106](https://github.com/crevissepartners/projmux/issues/106)) ([5fa3827](https://github.com/crevissepartners/projmux/commit/5fa3827368d06a277f1883ae828740234d2c83c8))
* **picker:** refine native sidebar chrome ([#109](https://github.com/crevissepartners/projmux/issues/109)) ([aaea6ee](https://github.com/crevissepartners/projmux/commit/aaea6ee13c54ad8cbdb619d4e670295020d13a79))
* **picker:** stabilize native preview chrome ([#110](https://github.com/crevissepartners/projmux/issues/110)) ([dce6bbf](https://github.com/crevissepartners/projmux/commit/dce6bbff55d4790e2e1881ab0e07935610912c94))
* **picker:** stabilize native switch viewport ([#107](https://github.com/crevissepartners/projmux/issues/107)) ([77d91c3](https://github.com/crevissepartners/projmux/commit/77d91c3f828913f74f53542ea777fbac009c844a))
* **picker:** tune sidebar geometry ([#112](https://github.com/crevissepartners/projmux/issues/112)) ([36aeda9](https://github.com/crevissepartners/projmux/commit/36aeda9863fc2cd19d907af70c50f9d6b90dd05d))
* **statusbar:** refine notify and git decorations ([#105](https://github.com/crevissepartners/projmux/issues/105)) ([b46e386](https://github.com/crevissepartners/projmux/commit/b46e3869ba30a8fe0ead9b57290b7dfe1f4a59af))

## [0.4.6](https://github.com/crevissepartners/projmux/compare/v0.4.5...v0.4.6) (2026-05-08)


### Features

* configure statusbar decorations ([#89](https://github.com/crevissepartners/projmux/issues/89)) ([13cb7ff](https://github.com/crevissepartners/projmux/commit/13cb7ffaa12957ace98102eaf23d179fd2807e38))
* **notify:** add contextual sidebar badges ([#82](https://github.com/crevissepartners/projmux/issues/82)) ([8297385](https://github.com/crevissepartners/projmux/commit/8297385597615e5182452ac95a2d0755ca33c4ad))
* **notify:** add strict queue sidebar ([#75](https://github.com/crevissepartners/projmux/issues/75)) ([989353d](https://github.com/crevissepartners/projmux/commit/989353d871da6651e282ed095d82cfb8d9aaa3fb))


### Bug Fixes

* **notify:** anchor sidebar to client right ([#78](https://github.com/crevissepartners/projmux/issues/78)) ([af87386](https://github.com/crevissepartners/projmux/commit/af8738611d70a19d546f14bd3f8598900885c4aa))
* **notify:** let sidebar close on alt-2 ([#81](https://github.com/crevissepartners/projmux/issues/81)) ([f3cd46d](https://github.com/crevissepartners/projmux/commit/f3cd46d225d8acd3cc5c892d627b85189ca10f61))
* **notify:** render compact HUD as notification block ([7b3b858](https://github.com/crevissepartners/projmux/commit/7b3b85848fac6dd434887e93796cfe1767408371))
* **notify:** restore sidebar toggle ([#79](https://github.com/crevissepartners/projmux/issues/79)) ([95a03ac](https://github.com/crevissepartners/projmux/commit/95a03ac58fa7842c4cc00ea05a028032b4c0d493))
* **notify:** target sidebar popup by client ([#80](https://github.com/crevissepartners/projmux/issues/80)) ([2f7e96e](https://github.com/crevissepartners/projmux/commit/2f7e96e57bea37f6aaef92b7253bfffa9e9c8dfe))
* **notify:** use outline status badges ([e33dbcb](https://github.com/crevissepartners/projmux/commit/e33dbcb99cb0cab4c529b3ef16292d4ba19fa4dd))
* polish statusbar popups ([#88](https://github.com/crevissepartners/projmux/issues/88)) ([0333af0](https://github.com/crevissepartners/projmux/commit/0333af0975886da9899130e3d44a2eb61cdb6237))
* refine notify focus UI ([#90](https://github.com/crevissepartners/projmux/issues/90)) ([14222c0](https://github.com/crevissepartners/projmux/commit/14222c095087fe3e3ac9c5471aec6dced45f6586))
* render notify sidebar cards ([#86](https://github.com/crevissepartners/projmux/issues/86)) ([fb1b48c](https://github.com/crevissepartners/projmux/commit/fb1b48c05593c0bb452cd2767b554504c9090273))
* require fzf 0.65 ([#87](https://github.com/crevissepartners/projmux/issues/87)) ([73f86ce](https://github.com/crevissepartners/projmux/commit/73f86ce08f13c1666cbd1e86d0e8a416bd6a5451))
* restore notify agent badges ([#91](https://github.com/crevissepartners/projmux/issues/91)) ([0dc8894](https://github.com/crevissepartners/projmux/commit/0dc8894f8bd2da3f8911a31229fc75aaae71b6e3))
* start shell on home session ([#93](https://github.com/crevissepartners/projmux/issues/93)) ([9fc2fb9](https://github.com/crevissepartners/projmux/commit/9fc2fb941f405a8db18b95a832f9f95cbff890f4))
* **statusbar:** clarify labels and notify sidebar ([#77](https://github.com/crevissepartners/projmux/issues/77)) ([6372ced](https://github.com/crevissepartners/projmux/commit/6372ced1986ce77b402e160d03ddddf4cc3e99a8))
* **statusbar:** open project sidebar from session badge ([#83](https://github.com/crevissepartners/projmux/issues/83)) ([2fc6525](https://github.com/crevissepartners/projmux/commit/2fc65256ed64e97b25f2822ba0ddd14ba8c361b6))

## [0.4.5](https://github.com/crevissepartners/projmux/compare/v0.4.4...v0.4.5) (2026-05-08)


### Features

* **notify:** explain live attention and focus dispatch state ([#71](https://github.com/crevissepartners/projmux/issues/71)) ([612ab76](https://github.com/crevissepartners/projmux/commit/612ab76a4f183f49d4daa36cf3a89e49bd618fa8))
* **picker:** add backend-neutral picker contract ([#72](https://github.com/crevissepartners/projmux/issues/72)) ([77a3a9f](https://github.com/crevissepartners/projmux/commit/77a3a9faf3ce3b63c74a8b58a623b94a7950065d))
* **settings:** add project root management UI ([#68](https://github.com/crevissepartners/projmux/issues/68)) ([0903881](https://github.com/crevissepartners/projmux/commit/0903881d8a011a57ed16166a221ed557dc0f26c4))


### Bug Fixes

* **settings:** prefill project root prompt when unconfigured ([#70](https://github.com/crevissepartners/projmux/issues/70)) ([59b7a0d](https://github.com/crevissepartners/projmux/commit/59b7a0d90d63b5a95e793401893d318cdc83f056))

## [0.4.4](https://github.com/crevissepartners/projmux/compare/v0.4.3...v0.4.4) (2026-05-08)


### Features

* complete roadmap polish ([#59](https://github.com/crevissepartners/projmux/issues/59)) ([504921e](https://github.com/crevissepartners/projmux/commit/504921e1ce23cc6f161bf09c3dd65e9b85857aad))
* improve statusbar click ux ([#63](https://github.com/crevissepartners/projmux/issues/63)) ([b6d8a3a](https://github.com/crevissepartners/projmux/commit/b6d8a3aa2a2e91d944eed3158052b6e0ebea7b5d))
* support bash shell config ([#67](https://github.com/crevissepartners/projmux/issues/67)) ([e146d36](https://github.com/crevissepartners/projmux/commit/e146d36f23a83fcde24cf9a2988e077b8ba89fa4))


### Bug Fixes

* depersonalize project root defaults ([#65](https://github.com/crevissepartners/projmux/issues/65)) ([206a14f](https://github.com/crevissepartners/projmux/commit/206a14f37f62b801ef99b900b72a032df391355c))
* require enter to close usage hud popup ([#64](https://github.com/crevissepartners/projmux/issues/64)) ([8a8b915](https://github.com/crevissepartners/projmux/commit/8a8b915fa84da3d58d1b2f7ff25820e8ee8082a1))

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
