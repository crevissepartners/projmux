# Picker UI Plan

## Goal

The project switcher needs a rich native picker surface. The target interaction
is a card-like list where each item can show a title plus small contextual lines
such as session state, window/pane summary, branch, or path. Search should stay
focused on stable identity text, especially the project or session title,
instead of matching every contextual preview line.

## Current Contract

The picker contract is split in two layers:

- `internal/ui/picker` owns backend-neutral items, actions, preview metadata,
  title-focused filtering, and the native runner.
- `internal/ui/pickercompat` is a retired legacy picker option/result adapter
  for old structs. Product flows do not execute the external fzf binary.

## fzf Capability Check

This section is historical context. Earlier design work evaluated fzf because it
supported multi-line items with `--read0`, where a single item can contain
newline characters when input records are NUL-delimited.

The simple fzf option path is not enough for the desired search behavior:

- `--read0` can display multi-line items.
- `--nth` can restrict search to selected fields.
- `--with-nth` can transform the displayed fields.
- In practice, once `--with-nth` is used to show a card field, fzf searches the
  transformed visible text. Context lines become searchable.

So fzf can support "multi-line cards", but not "multi-line cards with title-only
search" through a small option-only extension while preserving the current
selection contract.

## Viable Paths

### 1. fzf card approximation

Use `--read0` and NUL-delimited multi-line entries. This is the smallest change,
but contextual card text will participate in search unless the visible card is
kept title-only. This does not meet the intended search model.

This path is retired and is not supported.

### 2. fzf custom filtering

Run fzf in a more controlled mode where query changes reload a filtered list
from `projmux`, and `projmux` performs title-focused matching. This keeps fzf as
the renderer but moves filtering into the app.

Tradeoffs:

- More shell quoting and reload complexity.
- More edge cases around selection identity and tracking.
- Still constrained by fzf's list layout and event model.

This bridge path is retired and is not supported.

### 3. Native picker TUI

Introduce a picker abstraction and implement a native terminal UI for card rows,
title-focused search, stable selection identity, and app-owned key handling.

This best matches the desired product direction:

- card rows are first-class data, not encoded fzf strings
- search fields are explicit
- preview/context fields can be visible but non-searchable
- future key behavior can be tested without relying on fzf internals

## Implemented Direction

Do not extend the retired fzf row format again. The previous hidden-field attempt
showed that small fzf encoding changes can break selection and navigation in
subtle ways.

Current implementation:

- Picker-domain model exists as `picker.Item` with `Title`, `Value`,
  `SearchText`, `MetaLines`, `Badges`, and `PreviewTarget`.
- `picker.Options` carries backend-neutral actions, preview metadata, prompt,
  footer, initial query, and multiline intent.
- Native is the picker backend and renders the popup/sidebar surfaces.
- Native supports multiline item rendering, title-focused search via
  `SearchText`, numeric selection, and shared close actions.
- Switcher popup/sidebar use native preview panes, raw-key navigation, and
  sidebar focus tracking.
