# AI Agent Shortcut Registration

`projmux ai split` is intentionally tool-general: user-level shortcuts,
skills, slash commands, terminal actions, and editor commands can all map to
the same split contract.

```sh
projmux ai split --agent <agent> <right|down>
projmux ai split --agent <agent> <right|down> -- <extra args...>
```

Use this page for shareable registration patterns. Keep machine-local policy,
private prompt text, and personal workflow recipes in your own dotfiles or
local tool configuration, not in tracked repo docs.

## Command Contract

Choose a direct agent when a shortcut should always open that agent:

```sh
projmux ai split --agent codex right
projmux ai split --agent claude down
```

Add the separator only when you have extra arguments for the selected agent:

```sh
projmux ai split --agent codex right -- --model <model>
projmux ai split --agent claude down -- <agent flags>
```

The `--` separates projmux arguments from agent arguments. Everything after
that separator is passed to the resolved selected agent executable. It is not
an executable override. In the examples above, projmux still chooses the
configured `codex` or `claude` executable, creates the tmux split, sets pane
metadata, applies the title watcher, and appends the tail arguments to the
agent command.

Model, permission, and other agent flags are examples to customize privately in
your user-level config. Avoid treating placeholder flags in this guide as
project defaults or current recommendations. If there are no private extra
arguments, omit the separator entirely; do not leave a trailing bare `--`.

`shell` and `selective` are not targets for extra agent arguments. Use them
without a tail:

```sh
projmux ai split --agent shell right
projmux ai split --agent selective down
```

`shell` opens a plain shell split. `selective` opens the existing picker, where
the user chooses the launch mode interactively.

## Naming Pattern

Pick names that encode the target agent and split direction, then keep the
body as a small command wrapper.

For Codex-style skill surfaces, names can follow:

```text
$projmux-<agent>-right
$projmux-<agent>-down
```

Concrete examples:

```text
$projmux-codex-right
$projmux-claude-down
```

For Claude-style slash-command surfaces, names can follow:

```text
/projmux:<agent>-right
/projmux:<agent>-down
```

Concrete examples:

```text
/projmux:codex-right
/projmux:claude-down
```

The same pattern also works for editor commands, launcher actions, shell
aliases, or terminal custom actions. The important part is that the registered
surface calls `projmux ai split --agent <agent> <direction>` and passes only
agent flags after `--`.

### Bare-Name Default

When a surface registers a bare agent name without an explicit direction
suffix, treat it as the `right` variant. `right` is the convention for the
unqualified shortcut; `down` is always spelled out.

For Codex-style skill surfaces:

```text
$projmux-codex      → projmux ai split --agent codex right
$projmux-claude     → projmux ai split --agent claude right
```

For Claude-style slash-command surfaces:

```text
/projmux:codex      → projmux ai split --agent codex right
/projmux:claude     → projmux ai split --agent claude right
```

Register the bare name as a thin alias of the `*-right` shortcut so that the
two surfaces stay in lockstep. The `*-down` variant must be invoked by its
full name; do not introduce a separate bare default for `down`.

## Skill Template

Use this shape when a tool lets you define a user-level skill that tells an
agent to run a shell command. Actual loader paths vary by installation; the
intended home is user-level config or dotfiles, not this repository.

Example Codex-style skill file:

```text
~/.codex/skills/projmux-codex-right/SKILL.md
```

````markdown
---
name: projmux-codex-right
description: Open a projmux-managed Codex split to the right.
---

Run this command:

```sh
projmux ai split --agent codex right
```

If you have private Codex flags, use the explicit extra-args form instead:

```sh
projmux ai split --agent codex right -- --model <model>
```
````

Non-Codex target example:

```text
~/.codex/skills/projmux-claude-down/SKILL.md
```

````markdown
---
name: projmux-claude-down
description: Open a projmux-managed Claude split below.
---

Run this command:

```sh
projmux ai split --agent claude down
```

If you have private Claude flags, use the explicit extra-args form instead:

```sh
projmux ai split --agent claude down -- <agent flags>
```
````

If your tool stores skills as JSON, TOML, or another format, keep the same
fields conceptually:

- Name: the user-facing shortcut, such as `$projmux-codex-right`.
- Description: one sentence saying which agent and direction it opens.
- Command: the `projmux ai split` invocation.
- Arguments: optional agent arguments placed after `--`.

## Slash Command Template

Use this shape when a tool lets you define user-level slash commands. Actual
loader paths and interpolation variables can vary by installation; user-level
config or dotfiles is the intended home.

```text
~/.claude/commands/projmux/codex-right.md
```

This maps to a slash command named `/projmux:codex-right`:

````markdown
Open a Codex split to the right in the current projmux session.

Run this command:

```sh
projmux ai split --agent codex right
```
````

For an argument-aware wrapper, include the separator only when arguments are
non-empty. Adapt this shell shape to the tool's command-file format:

```sh
if [ -n "$ARGUMENTS" ]; then
  projmux ai split --agent codex right -- $ARGUMENTS
else
  projmux ai split --agent codex right
fi
```

Then invoke it with private agent flags only when needed, such as
`/projmux:codex-right --model <model>`.

For a Claude target, use a sibling file such as:

```text
~/.claude/commands/projmux/claude-down.md
```

This maps to `/projmux:claude-down`:

````markdown
Open a Claude split below in the current projmux session.

Run this command:

```sh
projmux ai split --agent claude down
```
````

For an argument-aware wrapper, use the same non-empty check:

```sh
if [ -n "$ARGUMENTS" ]; then
  projmux ai split --agent claude down -- $ARGUMENTS
else
  projmux ai split --agent claude down
fi
```

Then pass private Claude flags at invocation time only when needed, such as
`/projmux:claude-down <agent flags>`.

If the tool separates command metadata from the shell body, put only the
`projmux ai split ...` line in the executable body. Avoid embedding
machine-specific project roots, permission policies, or personal model choices
in shared docs; put those in your private user-level command files.

## Checklist

- Register shortcuts at user level in the AI tool, editor, launcher, or
  terminal that owns the surface.
- Use `--agent codex` or `--agent claude` when passing extra agent arguments.
- Put agent flags after the separator, for example `-- --model <model>`.
- Omit the separator entirely when there are no extra agent arguments.
- Use `--agent shell` with no tail for a plain shell split.
- Use `--agent selective` with no tail for the picker.
- Treat a bare agent name (`/projmux:codex`, `$projmux-claude`) as the
  `right` variant; spell out `*-down` shortcuts in full.
- Keep tracked project docs and `AGENTS.md` free of private shortcut policy.
