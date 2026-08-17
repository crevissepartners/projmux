# AI Agent Shortcut Registration

Use the canonical resource-create routes for user-level shortcuts, skills,
slash commands, terminal actions, and editor commands:

```sh
projmux create agent --provider <codex|claude|antigravity> [--placement right|down]
projmux create agent --provider <provider> [--placement right|down] -- <extra args...>
projmux create agent --provider <provider> [--placement right|down] -o pane-id [-- <extra args...>]
projmux create pane [--placement right|down] [-o pane-id]
```

Provider shortcuts (`projmux create codex|claude|antigravity`) normalize to the
same Agent route. Keep machine-local policy, private prompt text, and personal
workflow recipes in user config or dotfiles rather than tracked project docs.

## Command contract

Choose a direct provider when a shortcut must always open that provider:

```sh
projmux create codex --placement right
projmux create claude --placement down
```

Each invocation creates a new managed Agent and Pane. Existing Agent panes are
left in place. `--placement right|down` selects the tmux split axis; right is
the default. Use `--` only when forwarding extra provider arguments:

```sh
projmux create codex --placement right -- --model <model>
projmux create claude --placement down -- <agent flags>
```

Everything after `--` reaches the configured provider executable. Placeholder
model, permission, and agent flags are private customization examples, not
project defaults. Omit the separator when there is no payload.

Settings > AI Settings > Enabled providers controls whether Claude, Codex, and
Antigravity may launch. Canonical create routes respect that setting. There is
no shared command-line override for a disabled provider; change Settings when
the provider should become available. For a plain shell split use `projmux
create pane`, not an Agent provider.

Interactive provider and resume selection belong to the app's picker surfaces.
CLI automation should choose an explicit provider or use `projmux agent resume
<ref>` for an exact existing Agent.

### Automation pane handle

Use `-o pane-id` only when an automation wrapper needs the newly created Pane
as a stable follow-up handle:

```sh
pane_id="$(projmux create codex --placement right -o pane-id -- "prompt")"
```

Success prints exactly one `%N` Pane id. Without `-o pane-id`, the direct
current-Window form defaults to no output. Failure to obtain a valid tmux Pane
id is nonzero rather than a false success.

## Naming pattern

Encode the provider and placement in the registered name. A bare provider name
may be a thin alias of its right-placement variant:

```text
$projmux-codex       -> projmux create codex --placement right
$projmux-codex-down  -> projmux create codex --placement down
/projmux:claude      -> projmux create claude --placement right
/projmux:claude-down -> projmux create claude --placement down
```

The same pattern works for editor commands, launchers, shell aliases, and
terminal custom actions.

## Skill template

Example user-level Codex-style skill body:

````markdown
---
name: projmux-codex-right
description: Open a projmux-managed Codex Pane to the right.
---

Run this command:

```sh
projmux create codex --placement right
```
````

For a Claude-down variant, change only the description and command:

```sh
projmux create claude --placement down
```

## Slash-command template

A user-level `/projmux:codex-right` command can carry this executable body:

```sh
if [ -n "$ARGUMENTS" ]; then
  projmux create codex --placement right -- $ARGUMENTS
else
  projmux create codex --placement right
fi
```

Adapt argument interpolation to the owning tool. Do not embed machine-specific
project roots, permission policies, or model choices in shared docs.

## Checklist

- Register the shortcut in the user-level surface that owns it.
- Use a canonical provider shortcut or `create agent --provider ...`.
- Put provider arguments after `--`; omit it when the payload is empty.
- Use `create pane` for a shell Pane.
- Use `-o pane-id` only when automation consumes the returned `%N` handle.
- Keep private workflow policy outside tracked project docs.
