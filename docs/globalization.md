# Globalization Contract

This document is the inventory and contract for globalization. Phase 0 defined
the user-facing text boundary and literal preservation rules. Phase 1 adds the
message catalog foundation, locale resolver, fallback policy, and contribution
rules for future runtime surface migrations.

## Phase 0 Scope

Phase 0 does three things:

- Sets the temporary English baseline for remaining hardcoded Korean
  user-facing product text in code and tests.
- Classifies existing string families as `translate`, `literal`, `data`, or
  `debug-only`.
- Defines the surface boundaries for later catalog work.

Phase 0 explicitly preserves data and literals. It does not mass-translate docs
or fixtures.

## Surface Inventory

### AI Notifications

Files:

- `internal/app/ai_notify_body.go`
- `internal/app/ai.go`
- `internal/app/ai_test.go`
- `internal/app/ai_ingest_test.go`
- `internal/app/notify_producer_test.go`

Classification:

| Family | Examples | Class | Phase 0 policy |
| --- | --- | --- | --- |
| Agent names | `Codex`, `Claude`, `AI` | `literal` | Preserve exactly. |
| Category labels | `Response complete`, `Approval required`, `Input required`, `Error`, `Subagent stopped`, `Teammate waiting` | `translate` | English baseline now; catalog keys later. |
| Review body prefix | `Review pending:` | `translate` | English baseline now; catalog key later. |
| Tool names | `Bash`, `Read`, `WebFetch`, `Shell` | `literal` | Preserve provider/source spelling. |
| Tool input summaries | commands, paths, URLs, queries | `data` | Preserve payload content; only truncate for UI width. |
| Hook event names | `Stop`, `Notification`, `permission_prompt`, `idle_prompt` | `literal` | Preserve source values. |
| Severity values | `info`, `warn`, `critical` | `literal` | Preserve enum/source values. |

### Notify Queue, Sidebar, And Statusbar

Files:

- `internal/app/notify.go`
- `internal/app/status.go`
- `docs/statusbar.md`
- `docs/cli.md`

Classification:

| Family | Examples | Class | Phase 0 policy |
| --- | --- | --- | --- |
| CLI flag help and usage | `--text`, `--target`, `notify push`, usage errors | `translate` | Inventory only; convert in catalog phases. |
| Queue row labels and action hints | ack, clear, focus, open, reconcile labels | `translate` | Inventory only; convert in catalog phases. |
| Statusbar compact text | notify count, age, stale/gone hints | `translate` | Inventory only; needs compact locale formatter later. |
| Source/severity enum values | `ai`, `k8s`, `git`, `external`, `critical` | `literal` | Preserve exactly. |
| tmux style fragments | `#[...]`, tmux format strings | `literal` | Preserve syntax and measure display text separately. |
| Debug/error internals | wrapped Go errors, diagnostics for failed store reads | `debug-only` | Not catalog-blocking unless surfaced as normal UX. |

### Settings

Files:

- `internal/app/settings.go`
- `internal/app/settings_*.go`
- `docs/settings-ia.md`

Classification:

| Family | Examples | Class | Phase 0 policy |
| --- | --- | --- | --- |
| Root and section labels | Settings, Project, Notifications, Appearance, About | `translate` | Inventory only; catalog later. |
| Row labels and previews | enabled/disabled state, current source, saved values | `translate` | Inventory only; catalog later. |
| Disabled reasons and warnings | missing project, env override, conflict text | `translate` | Inventory only; catalog later. |
| Config keys and env vars | `PROJMUX_PROJDIR`, `config.toml`, `ui.locale` | `literal` | Preserve exactly. |
| Commands shown for copying | `projmux ai integrate codex --dry-run` | `literal` | Preserve exactly. |
| Persisted values | `none`, `notify`, `raise`, `auto` | `literal` | Preserve enum values. |

### Native Picker And Render Surfaces

Files:

- `internal/ui/projmuxpicker/*`
- `internal/ui/render/*`
- `docs/native-picker-no-fzf-poc.md`

Classification:

| Family | Examples | Class | Phase 0 policy |
| --- | --- | --- | --- |
| Search prompt and footer labels | Search, open rows, close, back, preview hints | `translate` | Inventory only; catalog later. |
| Picker titles and empty states | Projects, Settings, no matches | `translate` | Inventory only; catalog later. |
| Key names | `Enter`, `Esc`, `Alt-1`, `Ctrl-C` | `literal` | Preserve exactly. |
| Row data | project names, branch names, paths, session/window names | `data` | Preserve source content. |
| Width fixtures | CJK path/project examples and Unicode socket paths | `data` | Preserve as test fixtures. |

### Welcome, Update, About, And Help

Files:

- `internal/app/welcome*.go`
- `internal/app/update*.go`
- `internal/app/settings*.go`
- `internal/app/*help*`
- `docs/cli.md`
- `docs/agent-workflow.md`

Classification:

| Family | Examples | Class | Phase 0 policy |
| --- | --- | --- | --- |
| Welcome guide and shell prompt copy | first-run guidance, skip action text | `translate` | Inventory only; catalog later. |
| Update messages | update available, installer source, apply status | `translate` | Inventory only; catalog later. |
| About labels | version, source, welcome, quit action | `translate` | Inventory only; catalog later. |
| Command syntax | `projmux shell`, `make test`, `gh pr create` | `literal` | Preserve exactly. |
| Version strings and release tags | `vX.Y.Z`, git SHA, installer source | `data` | Preserve source content. |

## Literal Preservation Rules

Do not translate these families:

- Product and agent names: `Codex`, `Claude`, `projmux`, `tmux`, `psmux`,
  `GitHub`, `npm`.
- Terminal and app names: `Windows Terminal`, `Ghostty`, `WezTerm`, `Kitty`,
  `iTerm2`, `Alacritty`, `Foot`.
- Commands, flags, config, env vars, and paths: `projmux shell`, `make test`,
  `~/.config/projmux/config.toml`, `PROJMUX_NOTIFY_HOOK`, `Alt-1`.
- Protocol, enum, and source values: `reply-ready`, `approval_required`,
  `state.progress`, `ko-KR`, `en-US`, `critical`.
- Provider payload data: commands, file paths, URLs, query strings, project
  names, branch names, session/window/pane names, and transcript excerpts.
- ANSI, terminal, and tmux syntax: escape sequences, `#[...]` style fragments,
  tmux format expressions, and key escape fixtures.

These values may appear inside translated sentences, but the value itself stays
unchanged.

## Remaining Korean Hit Policy

Korean text is allowed when it is not English baseline product copy:

- `README-ko.md` and README language badges are localized docs and links.
- Roadmap title references in docs are source-note references, not product UI.
- CJK and Unicode path/socket fixtures are data for width, truncation, URI, and
  filesystem behavior tests.
- Historical or roadmap-note titles in docs may remain when they identify an
  external planning note.

New Korean product UI strings should not be added directly to code during Phase
0. Add English baseline copy now and move it behind message keys in Phase 1+.

## Draft Phase 1 Key Prefixes

Use stable, surface-oriented prefixes:

- `notify.ai.response_complete`
- `notify.ai.approval_required`
- `notify.ai.input_required`
- `notify.ai.error`
- `notify.ai.subagent_stopped`
- `notify.ai.teammate_waiting`
- `notify.ai.review_pending`
- `notify.queue.row.age_compact`
- `notify.queue.action.ack`
- `status.notify.count`
- `status.notify.age_compact`
- `settings.root.notifications`
- `settings.root.project`
- `settings.root.appearance`
- `settings.notifications.desktop`
- `settings.notifications.delivery_sources`
- `settings.about.welcome`
- `picker.prompt.search`
- `picker.footer.open_rows`
- `picker.footer.back`
- `picker.footer.close`
- `picker.empty.no_matches`
- `welcome.shell.title`
- `update.status.available`
- `help.usage.command`

Phase 1 should define the catalog API and fallback behavior before converting
large surfaces. Phase 2 should add locale-aware formatting for relative age,
duration, counts, list joins, and terminal cell-width-safe rendering. Later
phases can then move notify, Settings, picker, welcome, update, and help copy
behind catalog keys without changing source payload values.

## Phase 1 Catalog Foundation

Package:

- `internal/i18n`

Catalog policy:

- Catalog data is embedded Go data. Do not download catalog content at runtime
  and do not add a translation service dependency.
- `en-US` is the fallback locale. Every Phase 0 foundation key must have a
  non-empty `en-US` entry.
- `ko-KR` is intentionally partial in Phase 1 so fallback behavior is covered
  before broad runtime migration.
- Missing preferred-locale keys fall back to `en-US`.
- Missing fallback-locale keys are errors and must also be caught by the
  catalog completeness unit test.

Locale resolution:

- Precedence is explicit override/API, then `LC_ALL`, then `LC_MESSAGES`, then
  `LANG`, then `en-US`.
- Common POSIX forms normalize into catalog tags: `ko`, `ko_KR.UTF-8`, and
  `ko-KR` become `ko-KR`; `en` and `en_US` become `en-US`.
- `C` and `POSIX` locales are skipped as non-user-language values.
- Phase 1 does not add Settings language UI, persisted config/env locale
  schema, or runtime surface migration.

API convention:

- Use `i18n.ResolveLocale` to pick the preferred locale.
- Use `i18n.NewLocalizer(locale).Text(key)` for plain user-facing text.
- Use `i18n.NewLocalizer(locale).Styled(key)` only for messages that contain
  ANSI or tmux style syntax.
- Do not pass styled terminal fragments through the plain text API. The lookup
  API returns a kind mismatch error when a caller requests the wrong shape.
- Future formatter phases should keep payload data, command strings, paths,
  key names, enum/source values, and tmux/ANSI syntax outside translated text
  unless they are inserted as preserved literal values.

Contribution convention:

- Add new translatable user-facing strings as stable `i18n.Key` constants.
- Add the `en-US` entry in the same change as the key.
- Add `ko-KR` when the translation is known; otherwise rely on fallback while
  keeping the missing key intentional in review notes.
- Do not translate product names (`projmux`, `tmux`, `Codex`, `Claude`),
  commands, paths, config keys, environment variables, provider payloads, or
  source enum values.
- Runtime migrations should be narrow by surface. Move a string family behind
  the catalog only when its tests can show literal preservation and fallback
  behavior for that surface.

## Phase 2 Formatter Foundation

Package:

- `internal/i18n`

Formatter policy:

- Formatter functions localize grammar, units, and labels. They do not translate
  caller-owned payload values such as product names, commands, paths, provider
  text, tmux targets, or enum/source data inserted as arguments.
- Formatter functions accept a `FormatVariant`. `i18n.FormatFull` is for
  prose-friendly labels and `i18n.FormatCompact` is for dense terminal surfaces.
- Unsupported or empty locales fall back to `en-US`. Korean locale forms
  normalized by `ResolveLocale` use the Korean formatter rules.
- Relative age has a pinned just-now window of less than five seconds:
  `just now` for `en-US`, `방금 전` for `ko-KR`.

Formatter API:

- `i18n.FormatRelativeAge(age, locale, variant)` renders elapsed age, for
  example `3m ago` in compact `en-US` and `36초 전` in compact `ko-KR`.
- `i18n.FormatDuration(duration, locale, variant)` renders the largest whole
  unit in seconds, minutes, hours, or days.
- `i18n.FormatCount(count, subject, locale, variant)` applies locale-specific
  count grammar for owned subjects such as `i18n.CountNotifications` while
  preserving the numeric value.
- `i18n.FormatList(items, locale, variant)` joins caller-owned item strings
  using locale-specific separators without translating item payloads.
- `i18n.FormatStatusToken(token, locale, variant)` renders shared terminal
  status labels for known `i18n.StatusToken` values.
- `i18n.FormatTargetLabel(kind, number, locale, variant)` localizes the target
  label, such as window or pane, while preserving the target number.

Width-safe rendering:

- Use `i18n.TerminalCellWidth(value)` when terminal layout depends on visible
  width. It measures terminal cells, not bytes or runes.
- Use `i18n.TruncateTerminalCells(value, maxCells)` before placing
  locale-specific output into fixed-width terminal columns.
- ANSI escape sequences and tmux style wrappers such as `#[fg=red]` are
  zero-width for measurement and truncation. Truncation preserves those wrappers
  in the returned string while clipping only visible content.
- Width clipping is a rendering concern. Keep translated format strings and
  caller-owned payload arguments separate until the final render step.

## Phase 3 Notify Runtime Migration

Runtime surfaces migrated:

- AI desktop notification summaries for Codex and Claude hook payloads.
- In-app notify queue table/sidebar/statusbar display text for AI entries.
- Notify live explanation text for `projmux notify list --live`.
- Sidebar/table/statusbar age formatting and sidebar stale/gone/target labels.

Storage and dispatch policy:

- Queue storage remains schema-compatible. Stored `notify.Notification.Text`,
  metadata keys, severity/source enum values, targets, and timestamps are not
  localized before persistence.
- OS notification click/focus routing is unchanged. Locale formatting only
  changes the summary/body strings sent to the configured notifier.
- JSON queue payloads keep raw queue entries. Localized live-row explanation
  and row display text may appear in `notify list --live` row fields because
  those fields are render/report output, not stored queue schema.

Literal preservation and parity rules:

- Translate only catalog-owned category labels such as `Response complete`,
  `Approval required`, `Input required`, `Error`, `Subagent stopped`, and
  `Teammate waiting`.
- Preserve provider-owned payloads verbatim: `Codex`, `Claude`, tool names,
  commands, paths, URLs, query strings, transcript excerpts, teammate IDs, and
  subagent IDs.
- Desktop notification summaries and in-app queue display text must use the
  same rendered category labels for the same locale while preserving the same
  literal payload body.
- The response-complete fallback body `Ready` is treated as provider state and
  suppressed in display detail; it is not translated or persisted differently.
