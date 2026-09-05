# Claude coordination endpoint registration

Phase 1 binds a Claude-created endpoint to a managed Agent activation. It does
not implement message delivery, a provider message schema, a message/wait parser,
or a new public provider command. `agent capabilities` reports the exact Agent's
`runtimeEligibility.coordination` readiness and evidence. Static provider queries
remain static, and existing action cells stay unchanged.

`AgentRouteRef` shares stable Agent UID, Pane UID, and activation generation.
Its sealed provider authority union consumes the existing Codex thread and
`CodexAuthorityRef` composite unchanged. Claude authority instead consists of the
actual public SessionStart session ID, kernel process birth identity, a fresh
registration generation, and the kernel identity of the helper retaining its
registration lease. No Claude connection/binding epochs are synthesized.

Managed Agent/Pane names and Claude session titles are not route authority.
Renaming an unchanged UID/activation preserves its registration. Phase 1 does
not discover unmanaged provider names or observe live Claude `/rename` events.
Competing session/process/helper claims for an exact activation are refused;
the exact child's next SessionStart atomically claims a new registration
generation before launching its helper. Delayed admission and cleanup from an
older generation cannot overwrite or clear that newer registration.

The activation gate records the actual child PID and kernel birth identity
before replacing itself with Claude. The separate managed SessionStart hook
uses `exec`, making the registered provider its direct parent. The helper
verifies the complete helper → hook → provider process chain while the hook
waits for a bounded startup acknowledgement. A nested unmanaged Claude cannot
register its own endpoint through inherited activation environment variables.
The creator-selected Registry path travels only in private Claude activation
context, independent of a tmux server's older XDG environment.

The hook obtains the socket and credential solely from Claude's documented
`CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN` environment.
These values pass to the helper over an anonymous pipe and remain in process
memory. They are absent from Registry, helper argv, helper environment, public
capabilities, diagnostic messages, and the content-free cleanup receipt.
Projmux does not predict, issue, read a vendor registration file for, connect
to, or write to the Claude inbox.

Readiness validates exact Registry ownership/generation, provider and helper
kernel birth identity, socket type/owner/mode and unchanged inode. The read-only
capability query probes only a short, private Projmux readiness socket and
authenticates its kernel peer PID and birth. Linux birth identity includes boot
ID and start ticks; Darwin uses kernel process start time. The private socket
path fits both supported Unix socket limits and does not depend on TMPDIR.

The helper invalidates on provider exit, socket replacement, or loss of exact
Registry authority. The supervisor independently detects helper death while
the provider remains alive, clears only that exact lease, and removes orphaned
helper files. Provider exit keeps the existing lock-free termination journal
boundary; it never waits for the cleanup watcher's Registry transaction.
Provider sockets are never removed by Projmux. Generation replacement and
termination convergence discard old registration observations.

The required deterministic process integration is
`test/integration/claude-endpoint-binding.sh`. It is included in
`make test-integration`. The opt-in `TestClaudeEndpointInstalledSourceGate`
requires `PMX_TEST_CLAUDE_ENDPOINT_BIN`, `PMX_TEST_REAL_CLAUDE_BIN`, and a prepared
disposable authenticated `PMX_TEST_REAL_CLAUDE_CONFIG_DIR`. The harness creates
its own provider config with an authentication symlink; it never reads or copies
the caller's credentials. Its one-shot uses
strict empty MCP, empty tools, empty setting sources, and no session persistence;
it checks public init tools/MCP/plugins and tool-use are all zero. Raw public
stream and messaging credentials stay in memory; only allowlisted receipts
are reported. The token scan covers all owned files, including provider source
state. Upstream may store its own public locator; the test observes only a
boolean and removes the entire owned provider config before reporting cleanup.
Projmux Registry, helper files, diagnostics, and evidence contain neither value.
Inbound cross-session messaging is explicitly refused during the one-shot.
This is a Phase 1 registration source gate without `--safe-mode`.
It does not establish or relax the later mandatory safe-mode delivery gate.

Provider sources: [SessionStart hooks](https://code.claude.com/docs/en/hooks)
and [cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging).
