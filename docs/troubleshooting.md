# Troubleshooting

Start with read-only diagnostics:

```sh
projmux doctor
```

`doctor` never installs packages, rewrites configuration, or changes a tmux
server. Run a remediation command only after reviewing the finding that
recommended it. For the operational journal's privacy and retention contract,
see [Operational Diagnostics](operational-diagnostics.md).

## App socket marker migration

Projmux 0.13 and newer require two server-global markers before an ordinary
command may mutate the app tmux server:

- `@projmux_app=1` declares app ownership.
- `@projmux_socket_name=<name>` declares the logical `-L <name>` route.

An app server started by a pre-0.13 release can still be live with the first
marker and no logical marker. In that partial state, `shell`, attach, and
materialization commands fail closed and print the exact recovery command.
For the default app socket, run:

```sh
projmux config apply --socket projmux
```

This explicit apply keeps the live server and its sessions, binds `-L projmux`
to one absolute socket path and server PID, sources the generated config,
writes the missing logical marker, and verifies both markers against the same
server generation. Ordinary commands do not write the marker. Apply also
refuses a foreign server, a different existing logical marker, an alias/path
mismatch, or PID drift; do not replace the refusal with a raw `tmux set-option`
command.

For a non-default socket, use the exact name printed by the failing command:

```sh
projmux config apply --socket <name>
```

Then retry the original command.

## Diagnostic sequence

Use this order so each step remains read-only until you deliberately run the
recovery command:

```sh
projmux doctor --section runtime --verbose
projmux diagnostics log
tmux -L projmux show-options -gqv @projmux_app
tmux -L projmux show-options -gqv @projmux_socket_name
tmux -L projmux display-message -p -F '#{socket_path} #{pid}'
```

Replace `projmux` in all three tmux commands with the exact logical socket name
you are diagnosing. Expected healthy marker output is `1` and that same socket
name. The final read records the physical socket/PID pair for comparison; it
does not grant authority or repair anything.

The marker-specific Doctor codes map to these actions:

| Code | Meaning | Remediation |
| --- | --- | --- |
| `runtime.route-marker.missing` | App-owned live server has no logical marker, normally after a pre-0.13 live-server upgrade. | Run the exact `projmux config apply --socket <name>` printed by the failing ordinary command, then retry it. |
| `runtime.route-marker.mismatch` | The app-owned server declares a different logical route. | Do not overwrite it. Inspect the diagnostic sequence and confirm which `-L` route owns the server. |
| `runtime.route-marker.unreadable` | Doctor could reach the socket but could not read one or both ownership markers. | Inspect `projmux diagnostics log`, tmux/socket permissions, and the exact marker reads. Do not apply until the read failure is understood. |

Other runtime Doctor codes use bounded remediation identifiers in text and
JSON:

| Code family | Remediation |
| --- | --- |
| `runtime.socket.unreachable` | Start the app with `projmux shell`; if a server should already exist, inspect the exact socket first. |
| `runtime.socket.probe-failed`, `runtime.backend.unknown` | Inspect `projmux diagnostics log` and the operational journal. |
| `runtime.config.generated-missing`, `runtime.config.generated-invalid` | Run `projmux config apply --socket <name>` after confirming the target server. |
| `runtime.config.generated-unreadable` | Inspect config and directory permissions before applying. |
| `runtime.config.applied-stale` | Run the exact config apply command to reload the generated config. |
| `runtime.config.applied-unknown` | Start or identify the app runtime before attempting a reload. |
| `logs.*` | Follow the finding's `inspect-state-permissions`, `inspect-log-permissions`, or `inspect-operational-journal` remediation. |
| `registry.materialize.*` | Inspect the reported Registry topology. Doctor is read-only and does not repair it. |

Informational `*.ready`, `*.current`, `*.reachable`, `*.none`, `*.clean`, and
`*.audited` codes need no remediation. JSON exposes the same `code` and
`remediation` values as verbose text.

## Codex app-server install topology

Start with the read-only integration report:

```sh
projmux doctor --section integrations --verbose
```

The `Codex app-server` result keeps four readiness axes separate:

- `Endpoint readiness` says whether the existing endpoint is ready, dead, or
  failed with a bounded reason.
- `running executable` plus the sanitized version fields distinguish a proven
  managed executable from unknown identity and current from skewed versions.
- `manager ownership` comes only from the official daemon backend result; an
  absent or unclear result is never guessed from endpoint health.
- `remote control` independently reports disabled, connecting, connected,
  errored, unsupported, unavailable, or unknown.

`Source`/`reason`, `App-server probe`, `install capability`, and `lifecycle`
remain separate supporting fields. A ready endpoint therefore does not hide an
unmanaged process or version skew.

`external-cli-only` states only two observed facts: the ordinary Codex CLI
exists, and the managed standalone payload was not observed. It does not mean
the ordinary CLI is unsupported, identify how that CLI was installed, or prove
who owns a running process.

An explicit native action refuses a ready unmanaged or version-skewed endpoint.
The refusal reports `shared-clients-disconnect`: replacing this shared process
can interrupt every attached Codex client. For a managed skew, confirm the
interruption and run `codex app-server daemon restart`. For an unmanaged
endpoint, close every sharing client, stop the process through the operator
that owns it, then run `codex app-server daemon start`. Projmux never performs
those stop/restart steps or invents an ownership-specific kill command.

A prompted managed Codex create also requires that endpoint. When it is not
ready or not attachable, `projmux create codex -- "<prompt>"` refuses instead of
silently creating a plain-CLI Agent, and the refusal carries the same typed
reason Doctor reports. `--interactive-only` creates that plain Agent on purpose,
without native turn control. See
[Codex Native-Required Create Migration](codex-native-required-migration.md).

Native create refusal is the primary actionable surface: it names
`--interactive-only`, reports the same observed install-capability guidance as
Doctor and Settings, and links to the
[official Codex CLI capability guidance](https://learn.chatgpt.com/docs/codex/cli).
After changing the Codex installation through an operator-chosen supported
method, rerun Doctor. Do not copy binaries, create symlinks in the Codex home,
or edit the control socket as a diagnostic workaround. Doctor, Settings, and
support-report collection never start the daemon or modify the installation.

## Incomplete npm install

If the npm shim exits before Projmux starts with:

```text
projmux: unsupported or incomplete npm install for <platform>/<arch>.
Expected optional dependency @projmux/<platform>-<arch> to provide bin/projmux.
```

the platform-specific optional package is missing. This can happen when npm
reuses stale package metadata or optional dependencies were disabled. Because
the Go binary is absent, `projmux doctor` cannot run yet. Re-resolve the current
release and its optional dependency:

```sh
npm cache verify
npm install -g projmux@latest --include=optional
projmux version
projmux doctor
```

If the same error remains, remove the incomplete global package, refresh npm's
package metadata, and reinstall. Do not copy a binary from another platform.
GitHub Release and source alternatives are documented in [Install](install.md).
