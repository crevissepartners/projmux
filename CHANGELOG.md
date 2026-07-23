# Changelog

## [0.7.4](https://github.com/crevissepartners/projmux/compare/v0.7.3...v0.7.4) (2026-07-23)


### Features

* **insert:** generic InsertFileText pane insert (config + CLI + keybinding) ([6ca1e86](https://github.com/crevissepartners/projmux/commit/6ca1e86822f21d35d32315b5afbbdc6ccf35efe1))
* **insert:** generic InsertFileText pane insert (Phase 0+1) ([#504](https://github.com/crevissepartners/projmux/issues/504)) ([6ca1e86](https://github.com/crevissepartners/projmux/commit/6ca1e86822f21d35d32315b5afbbdc6ccf35efe1))
* **keybindings:** move AI split picker to Alt-7 ([#514](https://github.com/crevissepartners/projmux/issues/514)) ([6a28b83](https://github.com/crevissepartners/projmux/commit/6a28b83d8b25384117719710e759dcd8e891ded7))


### Bug Fixes

* **attention:** skip focus-hook attention arm/clear during sidebar preview (Phase 1) ([cd28209](https://github.com/crevissepartners/projmux/commit/cd282099f0139dacf69fb43db986a8c4863908f4))
* **attention:** suppress sidebar-preview attention arm/clear churn (Phase 1) ([#519](https://github.com/crevissepartners/projmux/issues/519)) ([cd28209](https://github.com/crevissepartners/projmux/commit/cd282099f0139dacf69fb43db986a8c4863908f4))
* **sidebar:** make live switch a side-effect-free preview for recent-windows ([9af6dcb](https://github.com/crevissepartners/projmux/commit/9af6dcbec5a338cb0ffbd38b03d67c0f809c5119))
* **sidebar:** sidebar live switch = preview, recent record on commit only (Phase 0) ([#518](https://github.com/crevissepartners/projmux/issues/518)) ([9af6dcb](https://github.com/crevissepartners/projmux/commit/9af6dcbec5a338cb0ffbd38b03d67c0f809c5119))
* **switch:** dedup symlinked projects via canonical real-path identity ([#501](https://github.com/crevissepartners/projmux/issues/501)) ([0221702](https://github.com/crevissepartners/projmux/commit/02217027597edc6bfac28d1275561e159d245603))
* **switch:** rebuild symlinked current-path candidate in alias form ([#503](https://github.com/crevissepartners/projmux/issues/503)) ([05a2537](https://github.com/crevissepartners/projmux/commit/05a253712caa3818dceed30a7bea2f3d48ec9e6a))

## [0.7.3](https://github.com/crevissepartners/projmux/compare/v0.7.2...v0.7.3) (2026-07-07)


### Features

* **notify:** clear gone notifications via G key ([#499](https://github.com/crevissepartners/projmux/issues/499)) ([99e4f66](https://github.com/crevissepartners/projmux/commit/99e4f662a088d28d4d5a533ff8798c107931487b))


### Bug Fixes

* **notify:** rebind clear-gone to lowercase g ([#500](https://github.com/crevissepartners/projmux/issues/500)) ([907fcde](https://github.com/crevissepartners/projmux/commit/907fcde581dd5d57866d8414c1a5b18c6a0adbd6))
* **recent-windows:** de-slug project badge via shared display-name wrapper ([#498](https://github.com/crevissepartners/projmux/issues/498)) ([91f94cd](https://github.com/crevissepartners/projmux/commit/91f94cdb9b00e8aad28a31cadb89b89f4b4b818e))
* **recent-windows:** session over drifted cwd for anchor-less badge ([#493](https://github.com/crevissepartners/projmux/issues/493)) ([726a2c2](https://github.com/crevissepartners/projmux/commit/726a2c2fb58c8a6c27e09b0f451ee767d4fd7e70))
* **update:** npm install -g [@latest](https://github.com/latest) + installer autodetect + surfaced feedback ([#495](https://github.com/crevissepartners/projmux/issues/495)) ([34c7d3e](https://github.com/crevissepartners/projmux/commit/34c7d3eee3cf858d0cfcdfe7fe85d6617bcb13fb))
* **update:** npm install -g [@latest](https://github.com/latest), installer autodetect, surfaced feedback ([34c7d3e](https://github.com/crevissepartners/projmux/commit/34c7d3eee3cf858d0cfcdfe7fe85d6617bcb13fb))

## [0.7.2](https://github.com/crevissepartners/projmux/compare/v0.7.1...v0.7.2) (2026-07-01)


### Features

* **ai:** antigravity resume discovery from history.jsonl (Phase 2) ([43acdbf](https://github.com/crevissepartners/projmux/commit/43acdbfabcc22b95a5ba46bd7d6839d36a0f081e))
* **ai:** antigravity session inclusion in resume picker — disk discovery (Phase 2) ([#487](https://github.com/crevissepartners/projmux/issues/487)) ([43acdbf](https://github.com/crevissepartners/projmux/commit/43acdbfabcc22b95a5ba46bd7d6839d36a0f081e))
* **ai:** antigravity usage parity — context-window HUD/status exposure (Phase 0) ([#486](https://github.com/crevissepartners/projmux/issues/486)) ([fb8822c](https://github.com/crevissepartners/projmux/commit/fb8822c87fc64ec3dee4ac2265ef2d9e9deb2f90))
* **ai:** configurable resume picker limit (Phase 1) ([#480](https://github.com/crevissepartners/projmux/issues/480)) ([ca47811](https://github.com/crevissepartners/projmux/commit/ca47811a8c3761a255b22954859170dddc39e9aa))
* **ai:** cwd-depth scope for resume picker (Phase 2) ([#481](https://github.com/crevissepartners/projmux/issues/481)) ([125dfc5](https://github.com/crevissepartners/projmux/commit/125dfc51da193906f4a8dead300b2a255ffa9e67))
* **ai:** drill-in IA for resume picker settings (Phase 3) ([#482](https://github.com/crevissepartners/projmux/issues/482)) ([5f88b86](https://github.com/crevissepartners/projmux/commit/5f88b86516b2290143874097e6a3d015ffbd9630))
* **ai:** fixed-column resume picker row view (Phase 0) ([#479](https://github.com/crevissepartners/projmux/issues/479)) ([3ffcd3e](https://github.com/crevissepartners/projmux/commit/3ffcd3e17003bf0a2151929b5adcb94eb2f5eae1))
* **ai:** recency-anchored resume row with per-agent badge + turn count (Phase 0) ([#485](https://github.com/crevissepartners/projmux/issues/485)) ([3ac6e8a](https://github.com/crevissepartners/projmux/commit/3ac6e8a34135d47d9227067f6a88ff0893d6b47b))
* **ai:** resume session split picker ([#476](https://github.com/crevissepartners/projmux/issues/476)) ([44edb99](https://github.com/crevissepartners/projmux/commit/44edb993fc6f4dc89e684f74316f1eacf867df81))
* **ai:** session-anchored cwd for AI split/resume (Phase 1) ([#488](https://github.com/crevissepartners/projmux/issues/488)) ([2167fb8](https://github.com/crevissepartners/projmux/commit/2167fb8d8ca0e2bdceac88530adb5480a4c54366))
* **ai:** symlink loop guard + session uniqueness for resume discovery (Phase 4) ([#483](https://github.com/crevissepartners/projmux/issues/483)) ([92b8241](https://github.com/crevissepartners/projmux/commit/92b82417e24683ef489be8830437f2ddeef279e1))


### Bug Fixes

* **ai:** accurate resume turn count via full-file deferred scan ([#490](https://github.com/crevissepartners/projmux/issues/490) follow-up) ([#492](https://github.com/crevissepartners/projmux/issues/492)) ([541b0a7](https://github.com/crevissepartners/projmux/commit/541b0a775f8dc5846acaf8d04ff9561b865b9d91))
* **ai:** count resume turns over the whole log, not the 100-line window ([541b0a7](https://github.com/crevissepartners/projmux/commit/541b0a775f8dc5846acaf8d04ff9561b865b9d91))
* close notify sidebar after child enter ([#475](https://github.com/crevissepartners/projmux/issues/475)) ([17330ad](https://github.com/crevissepartners/projmux/commit/17330ad4aac5cc61598be4d15f84078dcccec7ad))
* keep notify sidebar open after child enter ([#473](https://github.com/crevissepartners/projmux/issues/473)) ([765f949](https://github.com/crevissepartners/projmux/commit/765f94981f6266081a77ab6d0266693e55c9aaaa))
* **recent-windows:** project badge = session-anchor basename ([#489](https://github.com/crevissepartners/projmux/issues/489)) ([67c30c1](https://github.com/crevissepartners/projmux/commit/67c30c196ebc18af4f7e00b18af0c046a6f527ba))
* **recent-windows:** resolve worktree cwd to main repo project badge ([6eb64e9](https://github.com/crevissepartners/projmux/commit/6eb64e9deea27690a34df119435548e8ac6222f1))
* **recent-windows:** worktree cwd resolves to main repo project badge ([#489](https://github.com/crevissepartners/projmux/issues/489) follow-up) ([#491](https://github.com/crevissepartners/projmux/issues/491)) ([6eb64e9](https://github.com/crevissepartners/projmux/commit/6eb64e9deea27690a34df119435548e8ac6222f1))
* **sidebar:** follow active session cursor after Ctrl-X kill ([#484](https://github.com/crevissepartners/projmux/issues/484)) ([3b270eb](https://github.com/crevissepartners/projmux/commit/3b270ebf1d46d43ee5481ef106f12a5b08ce322e))


### Performance Improvements

* **ai:** defer resume turn count so discovery renders fast again ([#490](https://github.com/crevissepartners/projmux/issues/490)) ([357a2ef](https://github.com/crevissepartners/projmux/commit/357a2efad166b65edee507b35efd8b1fb21d14d3))
* **ai:** limit resume discovery to recent sessions ([#478](https://github.com/crevissepartners/projmux/issues/478)) ([703a1dd](https://github.com/crevissepartners/projmux/commit/703a1ddfc82476d97710bf1fdf9a7c9b0fe14184))
* **ai:** speed up resume session discovery ([#477](https://github.com/crevissepartners/projmux/issues/477)) ([9fc6687](https://github.com/crevissepartners/projmux/commit/9fc66876a87ee7b86584f2430576510be89abbf6))

## [0.7.1](https://github.com/crevissepartners/projmux/compare/v0.7.0...v0.7.1) (2026-06-21)


### Features

* add keybinding delivery diagnostics ([#440](https://github.com/crevissepartners/projmux/issues/440)) ([b601433](https://github.com/crevissepartners/projmux/commit/b6014334d62ca43225dcb4f40a14bc019bcc6370))
* **i18n:** complete localization coverage for pickers and project settings ([#455](https://github.com/crevissepartners/projmux/issues/455)) ([dc2a8c1](https://github.com/crevissepartners/projmux/commit/dc2a8c10885dfa3a634c5a96ba687a2a393ad91c))
* **theme:** 256-color grid picker with live preview (P4) ([#462](https://github.com/crevissepartners/projmux/issues/462)) ([8e90270](https://github.com/crevissepartners/projmux/commit/8e902702ca3bb34291f2445def37ae5c47d397dc))
* **theme:** active pane & popup chrome correction (Phase 5.5) ([#449](https://github.com/crevissepartners/projmux/issues/449)) ([06eb530](https://github.com/crevissepartners/projmux/commit/06eb5307c3cb4f1ab7106d6f6862180335dcb6aa))
* **theme:** add inspired preset pairs ([#465](https://github.com/crevissepartners/projmux/issues/465)) ([bf39737](https://github.com/crevissepartners/projmux/commit/bf3973743e0a0bb6878c7da38cd2d137f5f58035))
* **theme:** add terminal and fixed background presets ([#464](https://github.com/crevissepartners/projmux/issues/464)) ([ea0ff8a](https://github.com/crevissepartners/projmux/commit/ea0ff8abfc89e3e6500e48e53aa4a78ad93032f3))
* **theme:** add terminal-native preset using default backgrounds (P2) ([#460](https://github.com/crevissepartners/projmux/issues/460)) ([d192b0e](https://github.com/crevissepartners/projmux/commit/d192b0e746bc32dc8943a2d5ff0ea241bbff013c))
* **theme:** close theme apply-path gaps and add terminal-default option ([#454](https://github.com/crevissepartners/projmux/issues/454)) ([20dac90](https://github.com/crevissepartners/projmux/commit/20dac90ee9c8163018e59378e9dce791e0d360e0))
* **theme:** group theme editor tokens by priority (P3) ([#461](https://github.com/crevissepartners/projmux/issues/461)) ([2d1e914](https://github.com/crevissepartners/projmux/commit/2d1e91420469a869d91c67586006e1d6236f1fcc))
* **theme:** migrate state/severity + AI status cluster to role map (Phase 3) ([1dc6fd8](https://github.com/crevissepartners/projmux/commit/1dc6fd8a7ce8d5aeac0ddf3ffde00dba2d827495))
* **theme:** native UI semantic consolidation (Phase 5) ([#448](https://github.com/crevissepartners/projmux/issues/448)) ([8caf8b3](https://github.com/crevissepartners/projmux/commit/8caf8b36105b543b6cbaa5e92e476200baa9158f))
* **theme:** pane vs popup background separation (Phase 6b) ([#451](https://github.com/crevissepartners/projmux/issues/451)) ([1d6926a](https://github.com/crevissepartners/projmux/commit/1d6926a91ce12c4e04afab8b2c951f0b4ced85b6))
* **theme:** preset design rubric + fix midnight state-color collision (P1) ([#459](https://github.com/crevissepartners/projmux/issues/459)) ([e974e46](https://github.com/crevissepartners/projmux/commit/e974e46e59ee59596d01c8d92288e65350149205))
* **theme:** public schema expansion + Settings merge (Phase 6) ([#450](https://github.com/crevissepartners/projmux/issues/450)) ([a3e195f](https://github.com/crevissepartners/projmux/commit/a3e195f9b196f86fd7248131ae7b68aefb379e60))
* **theme:** semantic role map foundation + active pane tint (Phase 2) ([#445](https://github.com/crevissepartners/projmux/issues/445)) ([24146b0](https://github.com/crevissepartners/projmux/commit/24146b09773b02b1b52a4edc252841564ca4c604))
* **theme:** split foreground theme roles ([#463](https://github.com/crevissepartners/projmux/issues/463)) ([80c213e](https://github.com/crevissepartners/projmux/commit/80c213eae28beb37307184e7e6b68d7a5d935169))
* **theme:** split status background defaults ([#469](https://github.com/crevissepartners/projmux/issues/469)) ([0ae6e9d](https://github.com/crevissepartners/projmux/commit/0ae6e9d9875a59d4b5f92da16be3bee062843780))
* **theme:** state/severity + AI status role migration (Phase 3) ([#446](https://github.com/crevissepartners/projmux/issues/446)) ([1dc6fd8](https://github.com/crevissepartners/projmux/commit/1dc6fd8a7ce8d5aeac0ddf3ffde00dba2d827495))
* **theme:** statusbar segment role migration (Phase 4) ([#447](https://github.com/crevissepartners/projmux/issues/447)) ([68a8f77](https://github.com/crevissepartners/projmux/commit/68a8f7770f98d8731a847e7c1e95bb371ab198aa))
* **theme:** theme remaining pickers by default at the choke point ([#458](https://github.com/crevissepartners/projmux/issues/458)) ([4a72eca](https://github.com/crevissepartners/projmux/commit/4a72ecab615a11771dfed023c32d92f08b6d1e6c))


### Bug Fixes

* couple settings keymap saves to tmux apply ([#437](https://github.com/crevissepartners/projmux/issues/437)) ([cb9317e](https://github.com/crevissepartners/projmux/commit/cb9317e4db63a4a4f660b46277a350a494428e65))
* derive popup close aliases from keybinding catalog ([#435](https://github.com/crevissepartners/projmux/issues/435)) ([02404f3](https://github.com/crevissepartners/projmux/commit/02404f3211a81ff872b5ebb910fd499ad96f4a2f))
* **i18n:** honor global config [ui] locale in picker chrome localization ([#456](https://github.com/crevissepartners/projmux/issues/456)) ([cbb6903](https://github.com/crevissepartners/projmux/commit/cbb6903527d514d23400fa5873215d5b2a671143))
* **i18n:** settingsLocale honors global config [ui] locale (footers/labels) ([#457](https://github.com/crevissepartners/projmux/issues/457)) ([bf377f7](https://github.com/crevissepartners/projmux/commit/bf377f7039dda9869d1297f620916b865b64483c))
* **theme:** apply global theme to generated tmux chrome ([#453](https://github.com/crevissepartners/projmux/issues/453)) ([b3399b6](https://github.com/crevissepartners/projmux/commit/b3399b68b2d97860949ffd2efa3c4aca5ec57131))
* **theme:** clarify preset contrast intent ([#467](https://github.com/crevissepartners/projmux/issues/467)) ([bcd1c3e](https://github.com/crevissepartners/projmux/commit/bcd1c3e7924b47a2f581a65582547eae9a37c978))
* **theme:** darken active surfaces ([#472](https://github.com/crevissepartners/projmux/issues/472)) ([fe33715](https://github.com/crevissepartners/projmux/commit/fe33715a32d4aea4e78a38788a7c905445770a07))
* **theme:** darken readable preset surfaces ([#470](https://github.com/crevissepartners/projmux/issues/470)) ([3c98f4d](https://github.com/crevissepartners/projmux/commit/3c98f4d5134eab30491ed268ac03adbc7f87c8ba))
* **theme:** retune blue hour pane contrast ([#471](https://github.com/crevissepartners/projmux/issues/471)) ([69f2178](https://github.com/crevissepartners/projmux/commit/69f2178fe51fd2a280d832ab6ffed02e9c105dc6))
* **theme:** separate picker surface from pane background ([#468](https://github.com/crevissepartners/projmux/issues/468)) ([b8a8283](https://github.com/crevissepartners/projmux/commit/b8a82832b8b1822cf1f8c3a2589a4182a2d94069))
* **theme:** tune inspired preset pane colors ([#466](https://github.com/crevissepartners/projmux/issues/466)) ([18808df](https://github.com/crevissepartners/projmux/commit/18808df033b672a4fe3869078e61fda11222db5e))
* **ui:** align pane labels in tmux and recent windows ([#438](https://github.com/crevissepartners/projmux/issues/438)) ([75279ae](https://github.com/crevissepartners/projmux/commit/75279ae39af3dc92a6a218771f60925519c9c415))
* **ui:** restore active pane border chip ([#439](https://github.com/crevissepartners/projmux/issues/439)) ([024711e](https://github.com/crevissepartners/projmux/commit/024711e0a958097639decf287a2430c865bdde0c))

## [0.7.0](https://github.com/crevissepartners/projmux/compare/v0.6.7...v0.7.0) (2026-06-19)


### ⚠ BREAKING CHANGES

* **legacy:** Old keymap.toml entries using the dropped legacy action ids (sessionizer-sidebar, notify-sidebar, session-popup, ai-split-picker-right, ai-split-settings, sessionizer) no longer bind and are silently ignored; rebind under the canonical action ids. The catalog PrefixChord remnants for SessionPopupToggle (b), ProjectSwitcherToggle (f), rename-window (R), ai-split-right (r), ai-split-down (l), current-project-session (g), and toggle-mouse (M) are removed.

### Features

* add notify sidebar group fold and ack ([#413](https://github.com/crevissepartners/projmux/issues/413)) ([8da417a](https://github.com/crevissepartners/projmux/commit/8da417a09c5cc8561939742dead6664d3365b190))
* add recent windows phase 0 model ([#415](https://github.com/crevissepartners/projmux/issues/415)) ([c9795fa](https://github.com/crevissepartners/projmux/commit/c9795faa10ca977d999cdede1ba36419106ec050))
* add recent windows picker ([#418](https://github.com/crevissepartners/projmux/issues/418)) ([6f46cc5](https://github.com/crevissepartners/projmux/commit/6f46cc5ed965909e52fa1436c885e95601a54cb0))
* clean stale notify groups on enter ([#417](https://github.com/crevissepartners/projmux/issues/417)) ([a26b03c](https://github.com/crevissepartners/projmux/commit/a26b03c684ac63d4bf3a1f8841491f44c3997321))
* flatten settings keybinding keys ([#410](https://github.com/crevissepartners/projmux/issues/410)) ([5271b50](https://github.com/crevissepartners/projmux/commit/5271b50e2a341c954cf573c8ae8354ab1bb1f4bb))
* focus and ack notify groups on enter ([#416](https://github.com/crevissepartners/projmux/issues/416)) ([940d2b1](https://github.com/crevissepartners/projmux/commit/940d2b155074921ea0cb356dfb9a6938b65a6a3e))
* group notify sidebar by pane ([#411](https://github.com/crevissepartners/projmux/issues/411)) ([df41ceb](https://github.com/crevissepartners/projmux/commit/df41cebb44ee899996fd0b8b0c68310c908552a6))
* **keybindings:** clean up settings edit keys coverage ([#407](https://github.com/crevissepartners/projmux/issues/407)) ([15596da](https://github.com/crevissepartners/projmux/commit/15596da0f802b17ffeae295d46833b2d0d833bc4))
* **keybindings:** make Alt-3 open recent windows ([#420](https://github.com/crevissepartners/projmux/issues/420)) ([b95755a](https://github.com/crevissepartners/projmux/commit/b95755a047bba8ea0826a0fc75fdbdcf499aff23))
* **keybindings:** preview-only action list hierarchy (Phase 0.7) ([#433](https://github.com/crevissepartners/projmux/issues/433)) ([dcca408](https://github.com/crevissepartners/projmux/commit/dcca40899d00e250809f7721349fd9646c5dddb2))
* polish notify grouped sidebar card IA ([#412](https://github.com/crevissepartners/projmux/issues/412)) ([f100ef9](https://github.com/crevissepartners/projmux/commit/f100ef9fc67edd4b5c728021509546a9b9d3f8d8))
* **recent-windows:** Alt-3 card badge visibility and visual polish (Phase 6) ([#426](https://github.com/crevissepartners/projmux/issues/426)) ([a8d123e](https://github.com/crevissepartners/projmux/commit/a8d123e114ea07428244f0cb4fab1ec393af9ade))
* **recent-windows:** current window visible CURRENT no-op row (Phase 7) ([#427](https://github.com/crevissepartners/projmux/issues/427)) ([137dbc0](https://github.com/crevissepartners/projmux/commit/137dbc0501b4dbe8ad54e85e231be901489f4ab6))
* **recent-windows:** default cursor to first non-current row, flat line-2 perceived titles (Phase 9) ([#429](https://github.com/crevissepartners/projmux/issues/429)) ([8a8c3e6](https://github.com/crevissepartners/projmux/commit/8a8c3e632edcb9c11ba48b37ea472298adbbbe85))
* **recent-windows:** drop CURRENT badge, dedupe card context, richer pane preview (Phase 8) ([#428](https://github.com/crevissepartners/projmux/issues/428)) ([ddadc72](https://github.com/crevissepartners/projmux/commit/ddadc724ae2cb089c07277daf057fbabc9d110a2))
* **recent-windows:** polish Alt-3 card badge visibility (Phase 6) ([a8d123e](https://github.com/crevissepartners/projmux/commit/a8d123e114ea07428244f0cb4fab1ec393af9ade))
* **recent-windows:** polish Alt-3 picker card information hierarchy ([#425](https://github.com/crevissepartners/projmux/issues/425)) ([4f14ac2](https://github.com/crevissepartners/projmux/commit/4f14ac27d8bcee98f76489364eb58eac91be2c67))
* **recent-windows:** show current window as CURRENT no-op row (Phase 7) ([137dbc0](https://github.com/crevissepartners/projmux/commit/137dbc0501b4dbe8ad54e85e231be901489f4ab6))
* record recent windows at runtime ([#423](https://github.com/crevissepartners/projmux/issues/423)) ([0dfd859](https://github.com/crevissepartners/projmux/commit/0dfd85961263a7016addc991dce7c5d1e4c5dae6))


### Bug Fixes

* **notify:** clarify sidebar child counts ([#419](https://github.com/crevissepartners/projmux/issues/419)) ([4ab591e](https://github.com/crevissepartners/projmux/commit/4ab591e3ae7f52f1333d35cd4001639b875b4026))
* **notify:** classify gone targets from real tmux inventory ([#424](https://github.com/crevissepartners/projmux/issues/424)) ([a4003e8](https://github.com/crevissepartners/projmux/commit/a4003e853c6d0face5ab3d6c200ce214ea5bd031))
* **notify:** focus inactive targets ([#422](https://github.com/crevissepartners/projmux/issues/422)) ([b5c793f](https://github.com/crevissepartners/projmux/commit/b5c793fff0e3094d06fecab5c21b487fadc938c7))
* **recent-windows:** show last-visit absolute time in local timezone (Phase 0) ([#432](https://github.com/crevissepartners/projmux/issues/432)) ([845e70e](https://github.com/crevissepartners/projmux/commit/845e70eda14d3213400c09b72badc230a1259320))
* route recent windows through popup toggle ([#421](https://github.com/crevissepartners/projmux/issues/421)) ([fa36de8](https://github.com/crevissepartners/projmux/commit/fa36de8b1587f2b680b3fe059fd5d4479a228d61))
* **settings:** simplify keybinding action detail UI ([#409](https://github.com/crevissepartners/projmux/issues/409)) ([c7ee35e](https://github.com/crevissepartners/projmux/commit/c7ee35e86496dca8224f10d473ba42ec16a75884))
* stabilize notify sidebar group cards ([#414](https://github.com/crevissepartners/projmux/issues/414)) ([2d8a3f2](https://github.com/crevissepartners/projmux/commit/2d8a3f246588933900892ea0bdc847f94f1d6be2))


### Miscellaneous Chores

* **legacy:** drop keybinding LegacyIDs + PrefixChord remnants (Phase 4) ([#434](https://github.com/crevissepartners/projmux/issues/434)) ([0bf3819](https://github.com/crevissepartners/projmux/commit/0bf381972fd001c562382f5c883075ca7b45baa2))

## [0.6.7](https://github.com/crevissepartners/projmux/compare/v0.6.6...v0.6.7) (2026-06-04)


### Features

* add AI provider metadata registry ([#392](https://github.com/crevissepartners/projmux/issues/392)) ([a957662](https://github.com/crevissepartners/projmux/commit/a957662ac58c585b90c09cb6c102f0af711e3df4))
* **ai:** add antigravity launch support ([#391](https://github.com/crevissepartners/projmux/issues/391)) ([6946e1f](https://github.com/crevissepartners/projmux/commit/6946e1f9b6b9bc89ebbb73d7fd05b34695dee8f2))
* **ai:** add antigravity notify ingest ([#398](https://github.com/crevissepartners/projmux/issues/398)) ([fa4cc21](https://github.com/crevissepartners/projmux/commit/fa4cc217b62324347f0803e0f50654f0b0bdebd4))
* **ai:** add antigravity session state usage ([#400](https://github.com/crevissepartners/projmux/issues/400)) ([5e31d94](https://github.com/crevissepartners/projmux/commit/5e31d94578ebb0e41cb4764aa93a806cb4a6ec41))
* propagate native picker badge styles ([#394](https://github.com/crevissepartners/projmux/issues/394)) ([d2d8cff](https://github.com/crevissepartners/projmux/commit/d2d8cff29dc207c8ce9e24d7567e347d6e0b26cf))
* refresh notify sidebar actions in native picker ([#397](https://github.com/crevissepartners/projmux/issues/397)) ([23d34c4](https://github.com/crevissepartners/projmux/commit/23d34c405113893e80f6fcced6de6c55af55e7b9))
* refresh notify sidebar on queue writes ([#402](https://github.com/crevissepartners/projmux/issues/402)) ([1e159b4](https://github.com/crevissepartners/projmux/commit/1e159b4e7fb5c19a29102aaa0606b1d06bc7e15b))
* refresh sessionizer kill in native sidebar ([#399](https://github.com/crevissepartners/projmux/issues/399)) ([252cd33](https://github.com/crevissepartners/projmux/commit/252cd33f52132a50b43496c5c434ad401d2c83ab))
* reuse existing AI split panes ([#388](https://github.com/crevissepartners/projmux/issues/388)) ([c3bb41e](https://github.com/crevissepartners/projmux/commit/c3bb41e383fb1809145b640c90ad4d1c0f2019f2))
* **shell:** unify welcome update prompt ([#401](https://github.com/crevissepartners/projmux/issues/401)) ([1e328c2](https://github.com/crevissepartners/projmux/commit/1e328c27ef9fe2f2f37e1d259750b87901f224b6))


### Bug Fixes

* **ai:** restore split as new pane ([#403](https://github.com/crevissepartners/projmux/issues/403)) ([9971cc8](https://github.com/crevissepartners/projmux/commit/9971cc8246ad487df2cae5ee49b0ab890d3e3836))
* consume response-complete live badges ([#395](https://github.com/crevissepartners/projmux/issues/395)) ([43fa936](https://github.com/crevissepartners/projmux/commit/43fa9364388ed4b5ac15ad283e7ade373e9f1677))
* filter usage HUD by enabled agents ([#385](https://github.com/crevissepartners/projmux/issues/385)) ([cf1a1a7](https://github.com/crevissepartners/projmux/commit/cf1a1a732c031b116fdb9c54d0050f339e1ff08f))
* gate focus osfocus by desktop notify mode ([#393](https://github.com/crevissepartners/projmux/issues/393)) ([d67f9bc](https://github.com/crevissepartners/projmux/commit/d67f9bc781da604bde0eb95f348892fadd8ff761))
* **settings:** propagate saved locale to picker chrome ([#404](https://github.com/crevissepartners/projmux/issues/404)) ([0454884](https://github.com/crevissepartners/projmux/commit/04548844330e1e58b3c8b890f1c98c5a86cb348f))
* style native picker popup predraw body ([#396](https://github.com/crevissepartners/projmux/issues/396)) ([d499251](https://github.com/crevissepartners/projmux/commit/d4992515cd1be2aa80c88b21b3e1ce4125508092))

## [0.6.6](https://github.com/crevissepartners/projmux/compare/v0.6.5...v0.6.6) (2026-06-02)


### Features

* add AI agent enablement settings ([#381](https://github.com/crevissepartners/projmux/issues/381)) ([b7f72fb](https://github.com/crevissepartners/projmux/commit/b7f72fbb318dd8ec6b10e3c0642c0d316e03234b))
* add AI badge display styles ([#374](https://github.com/crevissepartners/projmux/issues/374)) ([e9ca5e6](https://github.com/crevissepartners/projmux/commit/e9ca5e647c885eccfd496b8aaf91bb539beaf564))
* add AI semantic badge state contract ([#370](https://github.com/crevissepartners/projmux/issues/370)) ([3b32f76](https://github.com/crevissepartners/projmux/commit/3b32f7679a6775daf20d6d83fee5f215ba19519c))
* cover Settings picker i18n ([#359](https://github.com/crevissepartners/projmux/issues/359)) ([429f159](https://github.com/crevissepartners/projmux/commit/429f159d8b1454f0943a74fd87dcdc577e6ff11c))
* gate AI split launches by enabled agents ([#382](https://github.com/crevissepartners/projmux/issues/382)) ([774e0dd](https://github.com/crevissepartners/projmux/commit/774e0dda9a3ae29408d206b81af2bd5990e6c731))
* harden AI semantic badge theme roles ([#375](https://github.com/crevissepartners/projmux/issues/375)) ([8418c1a](https://github.com/crevissepartners/projmux/commit/8418c1a9d2ab383190eddc82c2d4926759b66e49))
* render AI semantic status badges ([#371](https://github.com/crevissepartners/projmux/issues/371)) ([d910f4d](https://github.com/crevissepartners/projmux/commit/d910f4d01e5070a0a6f85047ee7a8f7970916076))


### Bug Fixes

* clamp native picker row width ([#380](https://github.com/crevissepartners/projmux/issues/380)) ([d5390d3](https://github.com/crevissepartners/projmux/commit/d5390d30b950e8a478c23643a25526ca1f8550dd))
* drop sidebar switch metadata lines ([#366](https://github.com/crevissepartners/projmux/issues/366)) ([44eff9c](https://github.com/crevissepartners/projmux/commit/44eff9cc5dc2170cb99723665776f0210dbe1b38))
* fill native picker app background ([#383](https://github.com/crevissepartners/projmux/issues/383)) ([8f0b984](https://github.com/crevissepartners/projmux/commit/8f0b984f8544691567f6c0440193435ea49064a3))
* keep Alt-1 branch chip compact ([#369](https://github.com/crevissepartners/projmux/issues/369)) ([804135e](https://github.com/crevissepartners/projmux/commit/804135e6f7e88fe97b670f9ea8a263f5f03bb5dd))
* keep Alt-1 sidebar rows compact ([#364](https://github.com/crevissepartners/projmux/issues/364)) ([75d9c82](https://github.com/crevissepartners/projmux/commit/75d9c82a8209be3f8f4431d806cf150a9de4a0a5))
* keep appearance parent rows inside native frame ([#384](https://github.com/crevissepartners/projmux/issues/384)) ([c5886af](https://github.com/crevissepartners/projmux/commit/c5886afad1b6036bdabbeff251a848f30a8dd8ae))
* **npm:** generate optional package metadata at staging ([#362](https://github.com/crevissepartners/projmux/issues/362)) ([3e1b10e](https://github.com/crevissepartners/projmux/commit/3e1b10e5f790aa7ae8d991a4a1a740b99c99688e))
* persist desktop notification setting ([#379](https://github.com/crevissepartners/projmux/issues/379)) ([9dc71a0](https://github.com/crevissepartners/projmux/commit/9dc71a0bad724140d5ef8bf1f70b797dc5305a02))
* preserve legacy attention window rows ([#373](https://github.com/crevissepartners/projmux/issues/373)) ([9e79302](https://github.com/crevissepartners/projmux/commit/9e793022cf22206e91361c7b0f96569c14c6c2a4))
* reserve blank Alt-1 sidebar lanes ([#368](https://github.com/crevissepartners/projmux/issues/368)) ([d007ddb](https://github.com/crevissepartners/projmux/commit/d007ddbfa01b4597b886e6bbe6989a8990e17527))
* restore Alt-1 sidebar card rows ([#367](https://github.com/crevissepartners/projmux/issues/367)) ([cf7c716](https://github.com/crevissepartners/projmux/commit/cf7c716ad7160088fb28bcf2b8f2ef761ad8790a))
* reuse palette warning for AI badges ([#372](https://github.com/crevissepartners/projmux/issues/372)) ([e5b8031](https://github.com/crevissepartners/projmux/commit/e5b8031efcd4e3195e7a169c02829b29e7098384))
* simplify native picker titlebar ANSI ([#358](https://github.com/crevissepartners/projmux/issues/358)) ([6942fd2](https://github.com/crevissepartners/projmux/commit/6942fd272503b13dcb49c0547f061940611cf796))
* soften window-list attention badge ([#365](https://github.com/crevissepartners/projmux/issues/365)) ([a8db0d2](https://github.com/crevissepartners/projmux/commit/a8db0d2a1c92808e014870f5251d0a7886954996))
* stabilize Alt-1 sidebar row geometry ([#360](https://github.com/crevissepartners/projmux/issues/360)) ([092fff9](https://github.com/crevissepartners/projmux/commit/092fff9dde41f8ce8b78b2e212fc4ab4e70a93da))
* **statusbar:** clean visible notify settings chrome ([#355](https://github.com/crevissepartners/projmux/issues/355)) ([8bd4544](https://github.com/crevissepartners/projmux/commit/8bd4544118478bed01f120886a0857404fc5d99c))


### Performance Improvements

* improve Alt-1 sidebar first paint ([#357](https://github.com/crevissepartners/projmux/issues/357)) ([533f273](https://github.com/crevissepartners/projmux/commit/533f2733b9fd7ea87a018659f3bbc8e0bd89ff3b))

## [0.6.5](https://github.com/crevissepartners/projmux/compare/v0.6.4...v0.6.5) (2026-05-21)


### Features

* add locale formatter primitives ([9c9ed25](https://github.com/crevissepartners/projmux/commit/9c9ed2593a234ddf86ba6747191598bfcf0df096))
* add theme resolver foundation ([074eee8](https://github.com/crevissepartners/projmux/commit/074eee8b1944e8975c00b02faf97d12d303fca95))
* **globalization:** add locale settings override ([#353](https://github.com/crevissepartners/projmux/issues/353)) ([eb363d3](https://github.com/crevissepartners/projmux/commit/eb363d33383666ca069624c54b69082474c72436))
* **globalization:** add phase 2 locale format primitives ([#345](https://github.com/crevissepartners/projmux/issues/345)) ([9c9ed25](https://github.com/crevissepartners/projmux/commit/9c9ed2593a234ddf86ba6747191598bfcf0df096))
* **globalization:** localize settings guidance ([#351](https://github.com/crevissepartners/projmux/issues/351)) ([18b84af](https://github.com/crevissepartners/projmux/commit/18b84af9f4bb55b8224f80173c913843670bcee1))
* localize notify AI messages ([#348](https://github.com/crevissepartners/projmux/issues/348)) ([a141743](https://github.com/crevissepartners/projmux/commit/a14174359500d420f2d720082ea2189f34dcc055))
* **theme:** add resolver foundation ([#347](https://github.com/crevissepartners/projmux/issues/347)) ([074eee8](https://github.com/crevissepartners/projmux/commit/074eee8b1944e8975c00b02faf97d12d303fca95))
* **theme:** add settings editor ([#352](https://github.com/crevissepartners/projmux/issues/352)) ([db5678e](https://github.com/crevissepartners/projmux/commit/db5678e3c31f0db8d81c0b5b63ae0d840930eb85))
* **theme:** apply background render tokens ([#349](https://github.com/crevissepartners/projmux/issues/349)) ([eb83444](https://github.com/crevissepartners/projmux/commit/eb83444a365f7488595c3cca71fa5585756276a4))
* **theme:** surface desired font status ([#350](https://github.com/crevissepartners/projmux/issues/350)) ([a10ec9b](https://github.com/crevissepartners/projmux/commit/a10ec9b5c5399ba992762e1ff4dcfd9cae8aaa2c))

## [0.6.4](https://github.com/crevissepartners/projmux/compare/v0.6.3...v0.6.4) (2026-05-21)


### Features

* add mux inventory read API ([#300](https://github.com/crevissepartners/projmux/issues/300)) ([8577bbd](https://github.com/crevissepartners/projmux/commit/8577bbd967ebfcc187112e5516b44710ffcb00dd))
* add mux runner wrapper ([#297](https://github.com/crevissepartners/projmux/issues/297)) ([3bc6a71](https://github.com/crevissepartners/projmux/commit/3bc6a718172e54ebdc08d19b16854c8a0cba5779))
* add native psmux shell entry smoke ([#308](https://github.com/crevissepartners/projmux/issues/308)) ([f4f48d3](https://github.com/crevissepartners/projmux/commit/f4f48d30d063f6dfa4b2d2904ca821817d86c083))
* add psmux ai split mvp ([#311](https://github.com/crevissepartners/projmux/issues/311)) ([92b1bb8](https://github.com/crevissepartners/projmux/commit/92b1bb81f7d94117e9383c0831cd22242713a1a1))
* add psmux app session foundation ([#310](https://github.com/crevissepartners/projmux/issues/310)) ([928ba75](https://github.com/crevissepartners/projmux/commit/928ba75415359ba7ccd4a8c084dec08cfba2099c))
* add psmux project switch entrypoint ([#312](https://github.com/crevissepartners/projmux/issues/312)) ([83a3130](https://github.com/crevissepartners/projmux/commit/83a3130c61115d66053ed06f068f0ce107e4a844))
* add semantic mux pane option API ([#299](https://github.com/crevissepartners/projmux/issues/299)) ([5264d41](https://github.com/crevissepartners/projmux/commit/5264d417c88cd5ec057e6b2a190b719239ee7b4c))
* add semantic palette foundation ([#334](https://github.com/crevissepartners/projmux/issues/334)) ([701d606](https://github.com/crevissepartners/projmux/commit/701d6062ef327944336f821eeba5b9c2bf0a846f))
* complete visual palette state slice ([#333](https://github.com/crevissepartners/projmux/issues/333)) ([af31f2b](https://github.com/crevissepartners/projmux/commit/af31f2b5187b7e71291d600a80ba5a773bbd056d))
* complete visual palette theme handoff ([#335](https://github.com/crevissepartners/projmux/issues/335)) ([bb6f81f](https://github.com/crevissepartners/projmux/commit/bb6f81f052dd3f172bc0402eba9f1fd97eef602d))
* define psmux command rendering policy ([#307](https://github.com/crevissepartners/projmux/issues/307)) ([4288410](https://github.com/crevissepartners/projmux/commit/4288410033ffa1ef70637bd789a430450b68e775))
* extract interactive mux API ([#302](https://github.com/crevissepartners/projmux/issues/302)) ([fd7d23f](https://github.com/crevissepartners/projmux/commit/fd7d23f4166abc2b35053ce039dd4f11be5f3a3d))
* extract mux lifecycle split hook APIs ([#303](https://github.com/crevissepartners/projmux/issues/303)) ([5e1a0ba](https://github.com/crevissepartners/projmux/commit/5e1a0baf87ff45a9728c6d1430895ba86c0c19ef))
* **globalization:** add phase 1 catalog foundation ([#344](https://github.com/crevissepartners/projmux/issues/344)) ([7243f80](https://github.com/crevissepartners/projmux/commit/7243f8097f222fad6ef30c9277ff764d957dd224))
* **globalization:** complete phase 0 inventory contract ([29f2129](https://github.com/crevissepartners/projmux/commit/29f21294880fa0f174de3e9101b99f0cef67aa09))
* **keybindings:** reorganize keybinding surface ([#316](https://github.com/crevissepartners/projmux/issues/316)) ([d4e4af0](https://github.com/crevissepartners/projmux/commit/d4e4af098fd1a3eb82c039eb3cfd90710ab8af9b))


### Bug Fixes

* align pane border and git badge colors ([2649f6b](https://github.com/crevissepartners/projmux/commit/2649f6b98994b018e978c8082f4809db6ffd51cf))
* align tmux window naming metadata ([#301](https://github.com/crevissepartners/projmux/issues/301)) ([0af72b6](https://github.com/crevissepartners/projmux/commit/0af72b665e5053a3407262b0c0f81edbfba5add8))
* hide AI notify target labels ([#342](https://github.com/crevissepartners/projmux/issues/342)) ([7b8e393](https://github.com/crevissepartners/projmux/commit/7b8e39328b0f50bfa9566e700cfa23694d9aec8e))
* improve AI notify sidebar layout ([#343](https://github.com/crevissepartners/projmux/issues/343)) ([e2f27ad](https://github.com/crevissepartners/projmux/commit/e2f27adb0839bfe53fe67a4c30c05151a42c96ff))
* isolate trust gate popup from sidebar ([#314](https://github.com/crevissepartners/projmux/issues/314)) ([54dbf1d](https://github.com/crevissepartners/projmux/commit/54dbf1d7bf22127f0762ff7094e9ad63a9e179fe))
* **keybindings:** allow plain aliases for transport actions ([#324](https://github.com/crevissepartners/projmux/issues/324)) ([a46a2ad](https://github.com/crevissepartners/projmux/commit/a46a2ad026ac829d33a9123b93e3b5374424ce57))
* **keybindings:** prefix sidebar action labels ([#322](https://github.com/crevissepartners/projmux/issues/322)) ([272130d](https://github.com/crevissepartners/projmux/commit/272130dd14f0a4ae5f4eb23b44c31b98e93d9a3e))
* **keybindings:** remove UserKey CSI-u route ([#319](https://github.com/crevissepartners/projmux/issues/319)) ([31e1f6e](https://github.com/crevissepartners/projmux/commit/31e1f6e65acfc0da486886a3f9f6f9d0ce2294a5))
* **keybindings:** restore transport-dependent arrow binds ([#321](https://github.com/crevissepartners/projmux/issues/321)) ([769bf48](https://github.com/crevissepartners/projmux/commit/769bf486b0b19140dcd1bb9e3257e77c592d80a0))
* **keybindings:** show readable settings labels ([#317](https://github.com/crevissepartners/projmux/issues/317)) ([3097b0f](https://github.com/crevissepartners/projmux/commit/3097b0fa43a41a26b06bfa8ee823eb9a86a7f920))
* **keybindings:** surface picker and movement actions ([#318](https://github.com/crevissepartners/projmux/issues/318)) ([b7ffe1b](https://github.com/crevissepartners/projmux/commit/b7ffe1b617358581d39c4e5005429649690298ac))
* **keybindings:** unbind retired key routes ([#326](https://github.com/crevissepartners/projmux/issues/326)) ([d04427c](https://github.com/crevissepartners/projmux/commit/d04427cb9ac3b6c278654ca226c56fc02adf1001))
* prefer show-options for mux option reads ([#305](https://github.com/crevissepartners/projmux/issues/305)) ([f363f80](https://github.com/crevissepartners/projmux/commit/f363f8022791bfc3785ba7e81afad2746f0b49c2))
* preserve lead topic pane color ([#337](https://github.com/crevissepartners/projmux/issues/337)) ([b8bbf6f](https://github.com/crevissepartners/projmux/commit/b8bbf6f7646a24fbe3a5224b31fe8bbaf989456b))
* remove user-key csi-u route ([31e1f6e](https://github.com/crevissepartners/projmux/commit/31e1f6e65acfc0da486886a3f9f6f9d0ce2294a5))
* render ready pane border green ([#338](https://github.com/crevissepartners/projmux/issues/338)) ([d69d98c](https://github.com/crevissepartners/projmux/commit/d69d98cd87eb54993b498127e4a0f3f8d7e4472e))
* render sidebar footer key guides from keymap ([#341](https://github.com/crevissepartners/projmux/issues/341)) ([d0b2e52](https://github.com/crevissepartners/projmux/commit/d0b2e52cfbab1c7c36786a37850f057a95b03f45))
* separate AI notification body labels ([#340](https://github.com/crevissepartners/projmux/issues/340)) ([b994b61](https://github.com/crevissepartners/projmux/commit/b994b613ad444ac6380be2f5e333c52a8d2f60f6))
* separate attention palette from AI state ([#330](https://github.com/crevissepartners/projmux/issues/330)) ([6302ad4](https://github.com/crevissepartners/projmux/commit/6302ad4dbb9f0ab743a2a140ec3b9a29e8cb1d0d))
* **settings:** show welcome in native viewer ([#331](https://github.com/crevissepartners/projmux/issues/331)) ([4f2a656](https://github.com/crevissepartners/projmux/commit/4f2a65617e8cb8fc6f4bbe90375df0d01156c34e))
* stabilize psmux project switch and agent lookup ([#313](https://github.com/crevissepartners/projmux/issues/313)) ([d0270ae](https://github.com/crevissepartners/projmux/commit/d0270ae58251e0b7b5206ec0710bf99a9cdffcb6))
* **statusbar:** prevent popup wait-key layout shift ([#315](https://github.com/crevissepartners/projmux/issues/315)) ([4c347f4](https://github.com/crevissepartners/projmux/commit/4c347f4d222060ee3e40158ca658774a27d4c4a5))
* **tmux:** recover stale popup markers ([#323](https://github.com/crevissepartners/projmux/issues/323)) ([1c075da](https://github.com/crevissepartners/projmux/commit/1c075da082d28f7a737c2142a48c110781bed0f2))
* **ui:** align picker and statusbar chrome ([#325](https://github.com/crevissepartners/projmux/issues/325)) ([8ba09f5](https://github.com/crevissepartners/projmux/commit/8ba09f5595fd4fddc35ea3af534b012f1b6ec270))
* **ui:** align visual palette chrome ([#327](https://github.com/crevissepartners/projmux/issues/327)) ([c40b77e](https://github.com/crevissepartners/projmux/commit/c40b77ecc610bda63891937e9753de581d81e532))
* **ui:** remove statusbar settings edge gap ([#332](https://github.com/crevissepartners/projmux/issues/332)) ([5ba58fc](https://github.com/crevissepartners/projmux/commit/5ba58fc6860dd18b244e14634798eddbf9f9d839))
* **ui:** style settings chip row right edge ([#328](https://github.com/crevissepartners/projmux/issues/328)) ([ac873b1](https://github.com/crevissepartners/projmux/commit/ac873b13df33678c7e4fa7fe242cb9125f6eaa3a))
* unblock native windows build ([#304](https://github.com/crevissepartners/projmux/issues/304)) ([ea44b0b](https://github.com/crevissepartners/projmux/commit/ea44b0b9f25c050687908343b52b41d4c8e2bde1))
* update windows native doctor policy ([#306](https://github.com/crevissepartners/projmux/issues/306)) ([5bb9e55](https://github.com/crevissepartners/projmux/commit/5bb9e552038577d8e8b71f703f5954d4efb587f9))
* **welcome:** revisit until version skip ([#329](https://github.com/crevissepartners/projmux/issues/329)) ([fcece33](https://github.com/crevissepartners/projmux/commit/fcece338a4e4cfcc9e3bc4a7deeb3db00847f95c))

## [0.6.3](https://github.com/crevissepartners/projmux/compare/v0.6.2...v0.6.3) (2026-05-14)


### Bug Fixes

* clean up notify sidebar keymap ([#294](https://github.com/crevissepartners/projmux/issues/294)) ([ef11881](https://github.com/crevissepartners/projmux/commit/ef118812156a3724c36ae7f466451cddcfd142ba))

## [0.6.2](https://github.com/crevissepartners/projmux/compare/v0.6.1...v0.6.2) (2026-05-14)


### Features

* add projmux quit lifecycle ([#278](https://github.com/crevissepartners/projmux/issues/278)) ([5d302a9](https://github.com/crevissepartners/projmux/commit/5d302a9fb887f96f1ee5afababae80855fd60197))
* **ai:** add direct split agent override ([#287](https://github.com/crevissepartners/projmux/issues/287)) ([5041d74](https://github.com/crevissepartners/projmux/commit/5041d74106227ce578b7033790a6c39c2634098e))
* **notify:** configure AI hook quiet policy ([#276](https://github.com/crevissepartners/projmux/issues/276)) ([5b2857f](https://github.com/crevissepartners/projmux/commit/5b2857f0387e9e2922686128a7991c70f1eb8cf7))
* **notify:** make AI OS notifications transient ([#279](https://github.com/crevissepartners/projmux/issues/279)) ([d591a6d](https://github.com/crevissepartners/projmux/commit/d591a6d160f01145255ccfbe7df5c2ab4748358f))
* **notify:** tune AI notification consumption ([#273](https://github.com/crevissepartners/projmux/issues/273)) ([b712dc4](https://github.com/crevissepartners/projmux/commit/b712dc42c4c4bd03981b6ca8bf2d2d801c49c332))


### Bug Fixes

* **ai:** append split extra args to resolved agent ([#289](https://github.com/crevissepartners/projmux/issues/289)) ([35127f7](https://github.com/crevissepartners/projmux/commit/35127f77cdbb016b7c4dc982f4929c0fd642c223))
* append ai split agent extra args ([35127f7](https://github.com/crevissepartners/projmux/commit/35127f77cdbb016b7c4dc982f4929c0fd642c223))
* clip notify status body before metadata ([#282](https://github.com/crevissepartners/projmux/issues/282)) ([0bba0fe](https://github.com/crevissepartners/projmux/commit/0bba0fee4620c98a28fb21f11d1245e26f442fc6))
* **notify:** bulk ack stale AI rows ([#280](https://github.com/crevissepartners/projmux/issues/280)) ([3b89030](https://github.com/crevissepartners/projmux/commit/3b89030a49b57b24d0b171623b90be1be4013d9a))
* **notify:** clean old URI protocol markers ([#292](https://github.com/crevissepartners/projmux/issues/292)) ([bb1fdff](https://github.com/crevissepartners/projmux/commit/bb1fdff7c6c4a7b2ec879840e34fb0c3a1c1dd4f))
* **notify:** hide WSL toast click handler ([#284](https://github.com/crevissepartners/projmux/issues/284)) ([4e1b577](https://github.com/crevissepartners/projmux/commit/4e1b5774835da0211f0ad65f08d787ac0b5d37aa))
* **notify:** implement generic hook runtime notify ([#281](https://github.com/crevissepartners/projmux/issues/281)) ([2a2958f](https://github.com/crevissepartners/projmux/commit/2a2958f97edb442c081d7741b6e4eececf6175e2))
* **notify:** keep sidebar open after row ack ([#288](https://github.com/crevissepartners/projmux/issues/288)) ([77c812b](https://github.com/crevissepartners/projmux/commit/77c812be6443c09021813d3301df380189960a9b))
* **notify:** preserve WSL toast URI forwarding ([#285](https://github.com/crevissepartners/projmux/issues/285)) ([258e12a](https://github.com/crevissepartners/projmux/commit/258e12a9857c12ee225c16857085b73130fffb3f))
* **notify:** route WSL toast clicks through hidden cmd ([#290](https://github.com/crevissepartners/projmux/issues/290)) ([811ce46](https://github.com/crevissepartners/projmux/commit/811ce4651f51d23696681e53750e4dde5798b54f))
* **notify:** use WScript for WSL toast clicks ([#286](https://github.com/crevissepartners/projmux/issues/286)) ([7e3c7e7](https://github.com/crevissepartners/projmux/commit/7e3c7e71759da62603c1ff4afa82bd3bfa2eb591))
* prompt stale project hook trust in popups ([#275](https://github.com/crevissepartners/projmux/issues/275)) ([2b5755c](https://github.com/crevissepartners/projmux/commit/2b5755ce69bb4b5b177d5b1b7c18a4af09c24579))
* **sessionstate:** direct-start agent replay ([#283](https://github.com/crevissepartners/projmux/issues/283)) ([ae1ba21](https://github.com/crevissepartners/projmux/commit/ae1ba215476c0fb73032217ef47975f6986fb15e))
* **settings:** flatten appearance icon picker ([b0c91f0](https://github.com/crevissepartners/projmux/commit/b0c91f09110b1ce5e6595fdbcbd130ecfb2616a9))
* use exact tmux session targets ([#274](https://github.com/crevissepartners/projmux/issues/274)) ([c85df48](https://github.com/crevissepartners/projmux/commit/c85df48d607a15bd66ebb1489ba16515105c5d95))


### Reverts

* **settings:** expand loop-compacted presence checks back inline ([#267](https://github.com/crevissepartners/projmux/issues/267)) ([0c5e867](https://github.com/crevissepartners/projmux/commit/0c5e8675ef99b97214facac1abc9f91a032f249e))

## [0.6.1](https://github.com/crevissepartners/projmux/compare/v0.6.0...v0.6.1) (2026-05-13)


### Bug Fixes

* **badge:** decouple pane border badge from ai_state lifecycle ([#259](https://github.com/crevissepartners/projmux/issues/259)) ([1a85e87](https://github.com/crevissepartners/projmux/commit/1a85e8728f2821cb1b2515db63c03c173a680fce))
* correct statusbar row order ([#260](https://github.com/crevissepartners/projmux/issues/260)) ([bc61fd2](https://github.com/crevissepartners/projmux/commit/bc61fd23e4c7074c4311725e8cd2579f0d38deaf))
* polish statusbar controls ([#257](https://github.com/crevissepartners/projmux/issues/257)) ([8e57090](https://github.com/crevissepartners/projmux/commit/8e57090a943055e6b998c8377f08dcbead3bbd61))
* polish statusbar tabs and appearance icons ([#262](https://github.com/crevissepartners/projmux/issues/262)) ([e542317](https://github.com/crevissepartners/projmux/commit/e5423173b481d83d48168a355ec915b3aafcfe17))
* refine statusbar state button label ([#261](https://github.com/crevissepartners/projmux/issues/261)) ([b7b80a0](https://github.com/crevissepartners/projmux/commit/b7b80a0a4311388937898b37b165d03c27a392e9))

## [0.6.0](https://github.com/crevissepartners/projmux/compare/v0.5.3...v0.6.0) (2026-05-13)


### ⚠ BREAKING CHANGES

* Remove Codex legacy notify ingest/integration paths and the pane-startup lifecycle hook shim. Use Codex hooks and [startup] run instead.

### Features

* add manual session-state actions ([cc45d83](https://github.com/crevissepartners/projmux/commit/cc45d831734ff099dab4f7c543367d9ab2bcef1e))
* **ai:** add tmux bell fallback ([#219](https://github.com/crevissepartners/projmux/issues/219)) ([86e96aa](https://github.com/crevissepartners/projmux/commit/86e96aae952540aceeff2b7893b64f4b981546ca))
* **ai:** catalog hook notification bodies ([#217](https://github.com/crevissepartners/projmux/issues/217)) ([fa9fedb](https://github.com/crevissepartners/projmux/commit/fa9fedb06241629b66a1787414fe15109016bfe1))
* **ai:** handle Claude extra hook events ([#214](https://github.com/crevissepartners/projmux/issues/214)) ([4e9e047](https://github.com/crevissepartners/projmux/commit/4e9e047f7b4f1e8dde50def9cb66e07f9b162f6a))
* **ai:** ingest Claude hook events ([#211](https://github.com/crevissepartners/projmux/issues/211)) ([ba26ca0](https://github.com/crevissepartners/projmux/commit/ba26ca0252385c2405dd5350b0e1aefc1d7c4f85))
* **ai:** ingest Codex notify events ([bc9d94a](https://github.com/crevissepartners/projmux/commit/bc9d94af30cdfab89ad48e197753ce98a19bdb48))
* **ai:** integrate Claude hooks ([#212](https://github.com/crevissepartners/projmux/issues/212)) ([277b9d0](https://github.com/crevissepartners/projmux/commit/277b9d041c6a17c0d6c0f1a64483e5e82023c7d0))
* **ai:** integrate Codex hooks mode ([#220](https://github.com/crevissepartners/projmux/issues/220)) ([b6c9dcc](https://github.com/crevissepartners/projmux/commit/b6c9dcccb6591af5be2ed3f007b4ae252f60f807))
* **ai:** integrate Codex notify ([d55ec66](https://github.com/crevissepartners/projmux/commit/d55ec66f53db7df012500da8c91023e32c47ff0c))
* copy notification source install commands ([#241](https://github.com/crevissepartners/projmux/issues/241)) ([28d30bc](https://github.com/crevissepartners/projmux/commit/28d30bc2631fbb033a1753c820c59b8bd0b32a04))
* **doctor:** add AI notify integration diagnostics ([#222](https://github.com/crevissepartners/projmux/issues/222)) ([a5c7041](https://github.com/crevissepartners/projmux/commit/a5c70417e7a2369419d1aa79d4917ecf1fe796ae))
* **hooks:** add send-noti notification hook ([#190](https://github.com/crevissepartners/projmux/issues/190)) ([fbf1034](https://github.com/crevissepartners/projmux/commit/fbf1034e138a05085bed9e649401f688b6fe0498))
* **layout:** apply presets to live sessions ([#213](https://github.com/crevissepartners/projmux/issues/213)) ([75fbdc9](https://github.com/crevissepartners/projmux/commit/75fbdc92246f907b5a527454dcd000dd8e8fb32d))
* **layout:** honor fresh startup presets ([#218](https://github.com/crevissepartners/projmux/issues/218)) ([e454111](https://github.com/crevissepartners/projmux/commit/e454111e4acb4a8fb42b392869363c70f825d7ff))
* **layout:** list project presets ([6b854db](https://github.com/crevissepartners/projmux/commit/6b854db0de25b83d168fc2242d021d60c6dda666))
* **layout:** preview preset apply ([#210](https://github.com/crevissepartners/projmux/issues/210)) ([684099b](https://github.com/crevissepartners/projmux/commit/684099b606f4eb1c42301349b4f40e879524a5a7))
* **layout:** save project presets ([7a0a653](https://github.com/crevissepartners/projmux/commit/7a0a653669c4f8cab7d297fcbe4c0574087e760a))
* remove compatibility shims for 0.6.0 ([#253](https://github.com/crevissepartners/projmux/issues/253)) ([ca6691c](https://github.com/crevissepartners/projmux/commit/ca6691cf3d484443a701ee609312ee5fa76f70f4))
* **session-state:** capture hook resume metadata ([#228](https://github.com/crevissepartners/projmux/issues/228)) ([f94abe2](https://github.com/crevissepartners/projmux/commit/f94abe26dc0e7fa4a088166b1553f87256c08b1f))
* **session-state:** capture pane titles in snapshots ([#226](https://github.com/crevissepartners/projmux/issues/226)) ([66d9824](https://github.com/crevissepartners/projmux/commit/66d9824bbe51e0fe88c8616e38dfde6486a58930))
* **session-state:** derive Claude resume ids from transcripts ([#230](https://github.com/crevissepartners/projmux/issues/230)) ([2786313](https://github.com/crevissepartners/projmux/commit/2786313f169fdd52ce80e8598b54c7d62dbe3815))
* **session-state:** derive Codex resume ids from logs ([#231](https://github.com/crevissepartners/projmux/issues/231)) ([741577b](https://github.com/crevissepartners/projmux/commit/741577b4eaa8af464b8776a9167ecb55b6069478))
* **session-state:** refresh resume metadata before save ([#229](https://github.com/crevissepartners/projmux/issues/229)) ([4dd2101](https://github.com/crevissepartners/projmux/commit/4dd2101a2aa53e1d01061eacf86b000b0e1f3f17))
* **session-state:** surface resume metadata health ([#233](https://github.com/crevissepartners/projmux/issues/233)) ([0de4f06](https://github.com/crevissepartners/projmux/commit/0de4f06d75ed44e69ae1d37c57894a213ead0245))
* **sessionstate:** add manual actions ([#202](https://github.com/crevissepartners/projmux/issues/202)) ([cc45d83](https://github.com/crevissepartners/projmux/commit/cc45d831734ff099dab4f7c543367d9ab2bcef1e))
* **sessionstate:** add status popup actions ([efa3d9a](https://github.com/crevissepartners/projmux/commit/efa3d9af0038b59396c7e65520faaf249b216642))
* **sessionstate:** auto restore shell sessions ([#201](https://github.com/crevissepartners/projmux/issues/201)) ([857d9e7](https://github.com/crevissepartners/projmux/commit/857d9e7e15aa24c00ae31408db1994bd626ac0a7))
* **sessionstate:** autosave shell snapshots ([#196](https://github.com/crevissepartners/projmux/issues/196)) ([f444dd5](https://github.com/crevissepartners/projmux/commit/f444dd56769b51451ac3d1813f67ec6fc4cd5e88))
* **sessionstate:** replay startup recipes ([#195](https://github.com/crevissepartners/projmux/issues/195)) ([835c5ae](https://github.com/crevissepartners/projmux/commit/835c5ae97975fd167a2cb1ad2608e5200365534a))
* **sessionstate:** resume Codex sessions ([#194](https://github.com/crevissepartners/projmux/issues/194)) ([c157b28](https://github.com/crevissepartners/projmux/commit/c157b2824c5c8c5b79624d7e1904564968269b07))
* **sessionstate:** show status popup ([#200](https://github.com/crevissepartners/projmux/issues/200)) ([70c239c](https://github.com/crevissepartners/projmux/commit/70c239c5b89123d99af6300f9c0b70b953e73155))
* **settings:** add project session state view ([#227](https://github.com/crevissepartners/projmux/issues/227)) ([e16190f](https://github.com/crevissepartners/projmux/commit/e16190f9faab5f90d5be354c6b4493a79f3a051d))
* **settings:** complete roadmap settings UX ([#191](https://github.com/crevissepartners/projmux/issues/191)) ([bc12454](https://github.com/crevissepartners/projmux/commit/bc12454bd22452583efc37c973a5afced24de543))
* **settings:** expose session state controls ([#198](https://github.com/crevissepartners/projmux/issues/198)) ([7611bce](https://github.com/crevissepartners/projmux/commit/7611bce63c700094b175bed1b898746d5e3b278a))
* **settings:** show AI notify integration diagnostics ([#223](https://github.com/crevissepartners/projmux/issues/223)) ([736e2ad](https://github.com/crevissepartners/projmux/commit/736e2adf8f3847f07874c80f4e001be04d1817ea))
* **settings:** simplify keybinding capture ([bc0cd2c](https://github.com/crevissepartners/projmux/commit/bc0cd2c99034b2c079f5d6b231c9edc23665ce3e))
* **shell:** add startup picker ([#216](https://github.com/crevissepartners/projmux/issues/216)) ([a88d46b](https://github.com/crevissepartners/projmux/commit/a88d46b53b8ce1590f55e9fe6cadea3ac6059f79))
* **shell:** add startup session selectors ([#215](https://github.com/crevissepartners/projmux/issues/215)) ([0068194](https://github.com/crevissepartners/projmux/commit/006819428d410b5cbf87230d013f7f1047163d74))
* **shell:** show welcome popup on attach ([#192](https://github.com/crevissepartners/projmux/issues/192)) ([dd81cf4](https://github.com/crevissepartners/projmux/commit/dd81cf4c005692b3e0eee62171ddf09733723eea))
* **statusbar:** polish display-only popups (any-key close, drop inline title) ([#185](https://github.com/crevissepartners/projmux/issues/185)) ([95f48ec](https://github.com/crevissepartners/projmux/commit/95f48ecca2321b6409d384ba9a24fd29661a3b5e))


### Bug Fixes

* ack notify clicks for missing targets ([#197](https://github.com/crevissepartners/projmux/issues/197)) ([c2130ae](https://github.com/crevissepartners/projmux/commit/c2130ae4869b0b6b38aefb45afe7ed153b4a183c))
* add project session-state policy controls ([#247](https://github.com/crevissepartners/projmux/issues/247)) ([3dab118](https://github.com/crevissepartners/projmux/commit/3dab118d30481a8135cf5e70ac16446e87e2efd3))
* **ai:** append Codex hooks after config ([a855c03](https://github.com/crevissepartners/projmux/commit/a855c03265647c7599814ab1e0e4f210db75acfb))
* **ai:** catalog agent hook events ([#238](https://github.com/crevissepartners/projmux/issues/238)) ([4ee7bde](https://github.com/crevissepartners/projmux/commit/4ee7bde4ac008d1e0ca363c0022275d791c691f0))
* **ai:** make Codex hooks the default integration ([#246](https://github.com/crevissepartners/projmux/issues/246)) ([64dc1cd](https://github.com/crevissepartners/projmux/commit/64dc1cd718b9bd4fc514fe1e0a351453ffb2679b))
* **ai:** parse tmux escaped bell fields ([#243](https://github.com/crevissepartners/projmux/issues/243)) ([eef4c04](https://github.com/crevissepartners/projmux/commit/eef4c044b653c4737b645c1664b6e130fbd4bfde))
* **ai:** use Codex inline hook schema ([#232](https://github.com/crevissepartners/projmux/issues/232)) ([3d7d301](https://github.com/crevissepartners/projmux/commit/3d7d301b8264c05974b3357e2dc32d2a620fcb4a))
* **ai:** use pane id for tmux bell hook ([#245](https://github.com/crevissepartners/projmux/issues/245)) ([83cb426](https://github.com/crevissepartners/projmux/commit/83cb4269ae6cf7511f8ab130fb796bce1260df78))
* clarify shell startup picker surface ([#239](https://github.com/crevissepartners/projmux/issues/239)) ([305eead](https://github.com/crevissepartners/projmux/commit/305eead29125b164b86795f151e253356939ab43))
* classify unresolved focus ids as target gone ([#199](https://github.com/crevissepartners/projmux/issues/199)) ([6d4b2f1](https://github.com/crevissepartners/projmux/commit/6d4b2f13aee0ba4194a458598d77c343c37dcd5d))
* copy notification commands from details ([#244](https://github.com/crevissepartners/projmux/issues/244)) ([ae2ddd8](https://github.com/crevissepartners/projmux/commit/ae2ddd884e6d64c61c0640660146b1f360e62afa))
* **hooks:** ignore temp root project markers ([#203](https://github.com/crevissepartners/projmux/issues/203)) ([92fa2b1](https://github.com/crevissepartners/projmux/commit/92fa2b1ddee743b89fd5e890d48fe8805a9f5482))
* move project startup selection into sidebar ([9f8ee19](https://github.com/crevissepartners/projmux/commit/9f8ee19202bb2807b6d220184dfcff2c6e876767))
* **notify:** route focus to origin client ([#235](https://github.com/crevissepartners/projmux/issues/235)) ([022b058](https://github.com/crevissepartners/projmux/commit/022b05897b530e553be81e7bfaa7f3c60c3be02e))
* **picker:** reset titlebar frame border styling ([#189](https://github.com/crevissepartners/projmux/issues/189)) ([abe330f](https://github.com/crevissepartners/projmux/commit/abe330f0f45c700ebf8207ca00f9a575b998b6f1))
* preserve ai topic during hook ingest ([#255](https://github.com/crevissepartners/projmux/issues/255)) ([692592a](https://github.com/crevissepartners/projmux/commit/692592ac5002e5ae77f51239bb3774244723a3df))
* preserve statusbar popup client ([#252](https://github.com/crevissepartners/projmux/issues/252)) ([71aa6c6](https://github.com/crevissepartners/projmux/commit/71aa6c6e5ddd8396f6fd5662b0799c68983510d1))
* route project open through startup picker ([#237](https://github.com/crevissepartners/projmux/issues/237)) ([4b194f4](https://github.com/crevissepartners/projmux/commit/4b194f454161523362a52bda426d0197b927fda3))
* separate settings views from actions ([#248](https://github.com/crevissepartners/projmux/issues/248)) ([946fed9](https://github.com/crevissepartners/projmux/commit/946fed9f7ac6064a66949ff07ee9dcd75b90006c))
* **session-state:** complete project restore slice ([#234](https://github.com/crevissepartners/projmux/issues/234)) ([031ac30](https://github.com/crevissepartners/projmux/commit/031ac309e29920e0f86687061669fbad5712fc7d))
* **session-state:** label restore gate as startup picker ([#225](https://github.com/crevissepartners/projmux/issues/225)) ([d214d4d](https://github.com/crevissepartners/projmux/commit/d214d4d01cdc60a450fe69137ae453eb8db69867))
* **settings:** restore view-first IA ([#193](https://github.com/crevissepartners/projmux/issues/193)) ([01be152](https://github.com/crevissepartners/projmux/commit/01be152e9d99e8886aea78b2b8760895ac07b6c7))
* **shell:** gate fresh project startup on picker ([#224](https://github.com/crevissepartners/projmux/issues/224)) ([9319508](https://github.com/crevissepartners/projmux/commit/9319508fd7b2f8318ace72160e20354cf6e51034))
* **shell:** target project session by default ([#221](https://github.com/crevissepartners/projmux/issues/221)) ([abbe86c](https://github.com/crevissepartners/projmux/commit/abbe86c760edb9014b031ac7a54cf5890d4506c5))
* show sidebar trust as popup ([#251](https://github.com/crevissepartners/projmux/issues/251)) ([91cc8cb](https://github.com/crevissepartners/projmux/commit/91cc8cb6a53ad190c4b3cd926f73092bbdd5baad))
* show sidebar trust prompt inline ([#250](https://github.com/crevissepartners/projmux/issues/250)) ([0473d15](https://github.com/crevissepartners/projmux/commit/0473d155644658bd9cee2022cc28dab4f35944f0))
* simplify notification and session state controls ([#249](https://github.com/crevissepartners/projmux/issues/249)) ([d061a2d](https://github.com/crevissepartners/projmux/commit/d061a2d4b13bb1464122249f6205922fd347707f))
* **statusbar:** wrap display-only popups in native picker frame chrome ([#187](https://github.com/crevissepartners/projmux/issues/187)) ([f1ff4ae](https://github.com/crevissepartners/projmux/commit/f1ff4aef08f7b5bb76766af8986a4868e66c5e1c))
* **switch:** open empty session when project trust is denied ([#256](https://github.com/crevissepartners/projmux/issues/256)) ([eac8271](https://github.com/crevissepartners/projmux/commit/eac82716b4a8cc8929bd6d72642b92df4a2e58fe))
* target client when opening sidebar project ([#254](https://github.com/crevissepartners/projmux/issues/254)) ([61e369a](https://github.com/crevissepartners/projmux/commit/61e369a4220795b596b7aa4b40a78c9d496df23b))

## [0.5.3](https://github.com/crevissepartners/projmux/compare/v0.5.2...v0.5.3) (2026-05-11)


### Bug Fixes

* **osfocus:** setsid bash subprocess so wt.exe survives parent exit ([#183](https://github.com/crevissepartners/projmux/issues/183)) ([55c1f69](https://github.com/crevissepartners/projmux/commit/55c1f69c168973f05f4c595f57de6692cea9b9bd))

## [0.5.2](https://github.com/crevissepartners/projmux/compare/v0.5.1...v0.5.2) (2026-05-11)


### Features

* **notify:** 3-way desktop notification mode (none/notify/raise) + Defender-safe shortcut + on-push auto-raise ([#180](https://github.com/crevissepartners/projmux/issues/180)) ([e960456](https://github.com/crevissepartners/projmux/commit/e960456ae8974a720a7c3fde72307f02c13ce481))
* **notify:** desktop notification on/off toggle + AppName branding ([#175](https://github.com/crevissepartners/projmux/issues/175)) ([5254882](https://github.com/crevissepartners/projmux/commit/52548826b323f577656ebb1766e87ce2f17dd487))
* **notify:** Toast click → projmux focus via projmux:// URI (Tier 1.5, Windows scope) ([#178](https://github.com/crevissepartners/projmux/issues/178)) ([64e83f2](https://github.com/crevissepartners/projmux/commit/64e83f226f967c97592eea1ecd695768eda830b7))
* **osfocus:** tier-1 adapter for Windows Terminal × WSL → Windows ([#177](https://github.com/crevissepartners/projmux/issues/177)) ([a7ed6d2](https://github.com/crevissepartners/projmux/commit/a7ed6d210f7d5cc65da3951416c8cde3ce28f941))


### Bug Fixes

* **notify:** correct projmux:// registry command to bypass zsh shell ([#179](https://github.com/crevissepartners/projmux/issues/179)) ([451e38f](https://github.com/crevissepartners/projmux/commit/451e38fe41be6bca7bf0ef62f4d4cd2e1df2ff90))
* **notify:** use client-visible check for auto-ack, not pane_active ([#172](https://github.com/crevissepartners/projmux/issues/172)) ([4c5fd55](https://github.com/crevissepartners/projmux/commit/4c5fd55f612b83df002d96a89aaf657de338e8b7))
* **osfocus:** remove racing goroutine wrap in Focus so wt.exe actually spawns ([2321527](https://github.com/crevissepartners/projmux/commit/2321527c52f2ab91a2c7e667104ab2d32f88bb09))
* **osfocus:** remove racing goroutine wrap so wt.exe actually spawns ([#181](https://github.com/crevissepartners/projmux/issues/181)) ([2321527](https://github.com/crevissepartners/projmux/commit/2321527c52f2ab91a2c7e667104ab2d32f88bb09))
* **osfocus:** wrap wt.exe spawn through bash -c for WSL foreground rights ([#182](https://github.com/crevissepartners/projmux/issues/182)) ([608de42](https://github.com/crevissepartners/projmux/commit/608de42c374941092c7ab1783b5d4aa01f8ad33e))

## [0.5.1](https://github.com/crevissepartners/projmux/compare/v0.5.0...v0.5.1) (2026-05-11)


### Features

* add stale/gone notify state badges ([#170](https://github.com/crevissepartners/projmux/issues/170)) ([02baedd](https://github.com/crevissepartners/projmux/commit/02baedd19ecd0a4278f712a6cda3ef93b17111ab))

## [0.5.0](https://github.com/crevissepartners/projmux/compare/v0.4.10...v0.5.0) (2026-05-11)


### ⚠ BREAKING CHANGES

* file-form lifecycle hook (`pre-create`/`post-create`/`pane-startup`/`post-attach`) 이 더 이상 실행되지 않는다. 자동 마이그레이션이 한 줄짜리 script 만 declarative 로 옮기며, multi-line 과 symlink 는 silent stop 위험이 있어 Settings UI 에 legacy 행을 표시한다. 복잡한 명령은 `run = "bash -c '...'"` 또는 외부 스크립트 호출 (`run = "./scripts/foo.sh"`) 으로 표현.

### Features

* add project config form editor ([#158](https://github.com/crevissepartners/projmux/issues/158)) ([1a64043](https://github.com/crevissepartners/projmux/commit/1a64043ce17242441ace4bc6c9526dad6b100089))
* add projmux hook cli surface ([#167](https://github.com/crevissepartners/projmux/issues/167)) ([1dd9fdd](https://github.com/crevissepartners/projmux/commit/1dd9fddb328d07784ea410db005f5e2212e84321))
* add session snapshot replay ([#156](https://github.com/crevissepartners/projmux/issues/156)) ([85a189a](https://github.com/crevissepartners/projmux/commit/85a189a71b33984cc4e44834a7c71700a286cbeb))
* add session snapshot store ([#154](https://github.com/crevissepartners/projmux/issues/154)) ([9a6b9ea](https://github.com/crevissepartners/projmux/commit/9a6b9eaacb99b043662ba30a6b375fe12d351784))
* add settings axis metadata ([#152](https://github.com/crevissepartners/projmux/issues/152)) ([788e4a1](https://github.com/crevissepartners/projmux/commit/788e4a1ebd5b20e3b130f76267459ec56492617d))
* add settings chip click and alt-shift chord ([#163](https://github.com/crevissepartners/projmux/issues/163)) ([ce3441f](https://github.com/crevissepartners/projmux/commit/ce3441fa2757f99aae0ec312d37107630610a843))
* add settings effective merge view ([#162](https://github.com/crevissepartners/projmux/issues/162)) ([f52d3de](https://github.com/crevissepartners/projmux/commit/f52d3def92734df98a1b35a8fe6a05bfcc5248ec))
* add settings global project tabs ([#155](https://github.com/crevissepartners/projmux/issues/155)) ([e562d89](https://github.com/crevissepartners/projmux/commit/e562d892f7b9ccb556eb029b95dff5652c9b5f01))
* add settings hooks read-only page ([#157](https://github.com/crevissepartners/projmux/issues/157)) ([28851d9](https://github.com/crevissepartners/projmux/commit/28851d9da8a0cf7b45c0067a93bb4da2692dd541))
* align alt-shift-arrow chord with xterm standard ([8db9e41](https://github.com/crevissepartners/projmux/commit/8db9e4198c2c5839cb9d16cac979f56b3dedac18))
* align alt-shift-arrow chord with xterm standard (Settings 2.8) ([#169](https://github.com/crevissepartners/projmux/issues/169)) ([8db9e41](https://github.com/crevissepartners/projmux/commit/8db9e4198c2c5839cb9d16cac979f56b3dedac18))
* complete hook maker authoring ui ([#160](https://github.com/crevissepartners/projmux/issues/160)) ([5a61afe](https://github.com/crevissepartners/projmux/commit/5a61afee8529180e4e9db15ed7e1e91327412281))
* drop redundant project context header and info rows ([#168](https://github.com/crevissepartners/projmux/issues/168)) ([2e08632](https://github.com/crevissepartners/projmux/commit/2e08632beded4248606d91693f44c42d92c7d796))
* drop script hook runner for declarative-only ([#164](https://github.com/crevissepartners/projmux/issues/164)) ([dd2803f](https://github.com/crevissepartners/projmux/commit/dd2803f720c30fd07cb97eff0be5c5724bba015f))
* extend effective merge view with hook entries ([#165](https://github.com/crevissepartners/projmux/issues/165)) ([8b2a4af](https://github.com/crevissepartners/projmux/commit/8b2a4afbbfa89211a5b7d1349f3ba300274bad08))
* integrate trust badge into project settings tab ([#166](https://github.com/crevissepartners/projmux/issues/166)) ([cdeee0d](https://github.com/crevissepartners/projmux/commit/cdeee0d9623366801efdc2827c8537ea64a0b2b7))
* restore Claude session state panes ([#159](https://github.com/crevissepartners/projmux/issues/159)) ([d04f2ec](https://github.com/crevissepartners/projmux/commit/d04f2ec3d9a0ce2517a782f597d3471bee052668))
* ship settings tab chips and alt-arrow chord ([#161](https://github.com/crevissepartners/projmux/issues/161)) ([3fb3257](https://github.com/crevissepartners/projmux/commit/3fb32574c0dabd60b776d2609986ff168e7ed075))

## [0.4.10](https://github.com/crevissepartners/projmux/compare/v0.4.9...v0.4.10) (2026-05-10)


### Features

* add lifecycle hook events ([#141](https://github.com/crevissepartners/projmux/issues/141)) ([7fa02e4](https://github.com/crevissepartners/projmux/commit/7fa02e435b7c6bef1b1b515baf5a6c73e9d164e6))
* gate project hooks on trust ([#139](https://github.com/crevissepartners/projmux/issues/139)) ([fa38041](https://github.com/crevissepartners/projmux/commit/fa3804104554307f074d65fa631090e977ffbec2))
* **keybindings:** add labs diagnostic flow ([#143](https://github.com/crevissepartners/projmux/issues/143)) ([9329b46](https://github.com/crevissepartners/projmux/commit/9329b468be93d62b501cb238675b720bc66fbf7d))
* **keybindings:** confirm lab plain overrides ([#144](https://github.com/crevissepartners/projmux/issues/144)) ([2d5a4cb](https://github.com/crevissepartners/projmux/commit/2d5a4cb15df994732039855702a2f8e0d82ae81c))
* **keybindings:** edit keymap in settings ([#140](https://github.com/crevissepartners/projmux/issues/140)) ([695db83](https://github.com/crevissepartners/projmux/commit/695db83d9925a959c1f4945b62d898628c098d29))
* **keybindings:** support user keymap file ([#137](https://github.com/crevissepartners/projmux/issues/137)) ([218335a](https://github.com/crevissepartners/projmux/commit/218335a9cfe31d4de502d83177a26005e6eb6ce3))
* load declarative project hook config ([#146](https://github.com/crevissepartners/projmux/issues/146)) ([c6338eb](https://github.com/crevissepartners/projmux/commit/c6338ebb819bb5367f8404d0ae2980ca0658be63))
* render statusbar usage HUD popup ([#147](https://github.com/crevissepartners/projmux/issues/147)) ([028a81b](https://github.com/crevissepartners/projmux/commit/028a81b3e7664472a930cd84bd0630f057eca354))
* **shell:** show first-run bootstrap welcome ([#133](https://github.com/crevissepartners/projmux/issues/133)) ([c71b00c](https://github.com/crevissepartners/projmux/commit/c71b00c82e02ace3bfc4e4c1d75cc8d8a0a727f7))
* **statusbar:** add settings click target ([#134](https://github.com/crevissepartners/projmux/issues/134)) ([2c3250c](https://github.com/crevissepartners/projmux/commit/2c3250c3abb171eb8e094210a54bd2abaf227424))


### Bug Fixes

* align popup content styling ([#151](https://github.com/crevissepartners/projmux/issues/151)) ([d8a0c3c](https://github.com/crevissepartners/projmux/commit/d8a0c3cda02a10ef88ed9e74edfffa85a97d087a))
* avoid nested statusbar popup frames ([#148](https://github.com/crevissepartners/projmux/issues/148)) ([20003fe](https://github.com/crevissepartners/projmux/commit/20003fe2e283127bad9a1a95d0f1a915c5cc134a))
* copy statusbar cwd to system clipboard ([#142](https://github.com/crevissepartners/projmux/issues/142)) ([3b24bcb](https://github.com/crevissepartners/projmux/commit/3b24bcb60d02239a5a78dba74baadeeb2ab2955b))
* hand off sidebar hook trust to wide popup ([#150](https://github.com/crevissepartners/projmux/issues/150)) ([ffc0104](https://github.com/crevissepartners/projmux/commit/ffc010444511ff698fa0d78df120ffbf01079bb1))
* make statusbar cwd popup display-only ([#145](https://github.com/crevissepartners/projmux/issues/145)) ([4ebdb12](https://github.com/crevissepartners/projmux/commit/4ebdb1251e1f200ffa57ae7e2caeb75986a5c476))
* normalize native picker frame chrome ([#138](https://github.com/crevissepartners/projmux/issues/138)) ([837714d](https://github.com/crevissepartners/projmux/commit/837714d8d3ead8ce8aa8338064de2bd270b3a0cb))
* **picker:** retire fzf dependency ([#128](https://github.com/crevissepartners/projmux/issues/128)) ([50e71ee](https://github.com/crevissepartners/projmux/commit/50e71eef0d0494d5697545718e18ec566d51e728))
* **picker:** wrap native arrow navigation ([#131](https://github.com/crevissepartners/projmux/issues/131)) ([9dc69fd](https://github.com/crevissepartners/projmux/commit/9dc69fdf660a95eb9d660e2e5ddefa79833c319f))
* show hook trust prompt in wide popup ([#149](https://github.com/crevissepartners/projmux/issues/149)) ([464f21e](https://github.com/crevissepartners/projmux/commit/464f21e5316be43bfb8fafbe681afc2a16bd1dac))

## [0.4.9](https://github.com/crevissepartners/projmux/compare/v0.4.8...v0.4.9) (2026-05-10)


### Bug Fixes

* **picker:** align multiline gap separators ([#127](https://github.com/crevissepartners/projmux/issues/127)) ([1053e8d](https://github.com/crevissepartners/projmux/commit/1053e8d8b69313c923d8c99c8b562aea90c36bdf))
* **picker:** keep multiline partial gap rows ([#125](https://github.com/crevissepartners/projmux/issues/125)) ([9b740e5](https://github.com/crevissepartners/projmux/commit/9b740e53544ef127e61d652d0783305da4406672))

## [0.4.8](https://github.com/crevissepartners/projmux/compare/v0.4.7...v0.4.8) (2026-05-10)


### Bug Fixes

* **ai:** stabilize native launch picker order ([#122](https://github.com/crevissepartners/projmux/issues/122)) ([68fc036](https://github.com/crevissepartners/projmux/commit/68fc036b210e01f75d2253c0f0a871af3c9be7c2))
* **picker:** distinguish native titlebar chrome ([#116](https://github.com/crevissepartners/projmux/issues/116)) ([6ea8c0a](https://github.com/crevissepartners/projmux/commit/6ea8c0a9fadfd3c2d4b1752f9e2f8b065ab5a978))
* **picker:** follow native mouse drag selection ([#120](https://github.com/crevissepartners/projmux/issues/120)) ([a20406e](https://github.com/crevissepartners/projmux/commit/a20406eff4584345f0bf5e847bc453e09a82a8cd))
* **picker:** move settings descriptions into titles ([#123](https://github.com/crevissepartners/projmux/issues/123)) ([5175b85](https://github.com/crevissepartners/projmux/commit/5175b85b835765ab479fbc401aca5a31dafbbced))
* **picker:** neutralize native titlebar accent ([#118](https://github.com/crevissepartners/projmux/issues/118)) ([fe66cb3](https://github.com/crevissepartners/projmux/commit/fe66cb32870dd2ec5616e639ae0d783a6517789e))
* **picker:** refine native sidebar chrome and keys ([#119](https://github.com/crevissepartners/projmux/issues/119)) ([e5d89c6](https://github.com/crevissepartners/projmux/commit/e5d89c6c7dd7ec11a71f88c6dee0a4ae3f045029))
* **picker:** separate native title from search ([#117](https://github.com/crevissepartners/projmux/issues/117)) ([e45ce8b](https://github.com/crevissepartners/projmux/commit/e45ce8b67b95540f3200aef647a5870144a6ff91))
* **picker:** trigger native mouse clicks on release ([#114](https://github.com/crevissepartners/projmux/issues/114)) ([3ba6d80](https://github.com/crevissepartners/projmux/commit/3ba6d8032a70bc3ee9e88c3a9ea904ed1ff66b57))
* **settings:** nest project picker list actions ([#124](https://github.com/crevissepartners/projmux/issues/124)) ([a1526c2](https://github.com/crevissepartners/projmux/commit/a1526c2b2350a2e0dcd62c15b7cb4148e2e9d7df))
* **settings:** promote picker context to native titles ([#121](https://github.com/crevissepartners/projmux/issues/121)) ([12ca31f](https://github.com/crevissepartners/projmux/commit/12ca31f103faaecf050834f3ad0c7ea04db82e74))

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
