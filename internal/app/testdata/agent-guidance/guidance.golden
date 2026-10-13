# Working with other agents through projmux

You are running as an agent managed by projmux.

- When you need another agent to take on work, create it with `projmux create agent` instead of starting a subagent built into your provider. An agent created this way gets its own pane, stays visible to the operator, and can be resumed and messaged.
- New CLI Agents follow `projmux config agent-host`: omitted host uses tmux by default, or detached process startup when configured as process. Explicit `--host tmux|process` overrides it; explicit process is foreground unless `--detach` is given. Agent routes and provider shortcuts accept `--detach=false` to keep inferred process foreground. Process creation supports Claude/Codex and exact scopes; unsupported tmux topology is refused.
- When you need to send a message to another agent, use `projmux agent message send` instead of a message channel local to your provider. projmux routes the message to the agent and keeps a record of it.
- Run `projmux create agent --help` and `projmux agent message send --help` for their options.
