# Picker UI Plan

## Goal

The project switcher needs a richer picker surface than a single-line fzf row.
The target interaction is a card-like list where each item can show a title plus
small contextual lines such as session state, window/pane summary, branch, or
path. Search should stay focused on stable identity text, especially the project
or session title, instead of matching every contextual preview line.

## Current Contract

`internal/ui/fzf` sends one logical row per item:

```text
<visible label>\t<selection value>
```

fzf is configured with:

- `--delimiter "\t"`
- `--with-nth 1`
- `--exit-0`
- optional `--preview` and `--preview-window`

The app depends on fzf returning the selected row and then extracts the hidden
value after the first tab. This contract is simple and stable, but it limits each
row to one visible line.

## fzf Capability Check

The installed fzf version supports multi-line items with `--read0`. That means a
single item can contain newline characters when input records are NUL-delimited.
This can render card-like rows.

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

This path is acceptable only as a temporary visual experiment.

### 2. fzf custom filtering

Run fzf in a more controlled mode where query changes reload a filtered list
from `projmux`, and `projmux` performs title-focused matching. This keeps fzf as
the renderer but moves filtering into the app.

Tradeoffs:

- More shell quoting and reload complexity.
- More edge cases around selection identity and tracking.
- Still constrained by fzf's list layout and event model.

This is viable, but it is a bridge rather than a clean long-term model.

### 3. Native picker TUI

Introduce a picker abstraction and implement a native terminal UI for card rows,
title-focused search, stable selection identity, and app-owned key handling. fzf
remains the default backend until parity is reached.

This best matches the desired product direction:

- card rows are first-class data, not encoded fzf strings
- search fields are explicit
- preview/context fields can be visible but non-searchable
- future key behavior can be tested without relying on fzf internals

## Recommendation

Do not extend the current fzf row format again as the main implementation. The
previous hidden-field attempt showed that small fzf encoding changes can break
selection and navigation in subtle ways.

Proceed in two narrow branches:

1. Add a picker-domain model:
   `Title`, `Value`, `SearchText`, `MetaLines`, `Badges`, and `PreviewTarget`.
   Keep the fzf backend rendering only the current one-line label first.
2. Add a native picker backend behind an opt-in flag or config value, then port
   the switcher sidebar/popup one surface at a time.

fzf can stay as the stable fallback while the native picker reaches parity.

