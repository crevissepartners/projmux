# Registry schema v5

The Registry lives at `$XDG_STATE_HOME/projmux/metadata/registry.json`.
When `XDG_STATE_HOME` is absent or relative, the location is
`$HOME/.local/state/projmux/metadata/registry.json`. It is a private file
(0600). The resource API remains `projmux.io/v1alpha1`; the envelope's
`schemaVersion` is now 5.

Versions 1 through 4 migrate forward. The v4 step makes every existing
Pane's `spec.runtime.kind` explicitly `tmux`, preserving its UID, name,
owner chain, reservations, and tmux/provider activation evidence. An omitted
kind still means `tmux`; the only other kind is `process`. Unknown kinds are
refused with `runtime-kind-unsupported`. Read-only `get` and `describe` can
consume migrated data without saving it. A successful mutation saves v5.

## Process evidence

`get pane -o json` and `get panes -o json` expose the stored model. The same
Pane model is included in resource graph output. `describe pane` displays
`RuntimeKind` and, for a process Pane, the readable rows `ProcessHost`,
`HostPID`, `ChildPID`, `ResumeState`, and `PendingControls`; `describe agent`
also displays those rows from its current process Pane. They are explained in
[Operating Process-Hosted Agents](process-host-operations.md#reading-its-state).
JSON Agent resources continue to reference the Pane through `status.paneRef`.

| Field | Type and omission rule |
| --- | --- |
| `spec.runtime` | Object, omitted for an empty legacy/default recipe |
| `spec.runtime.kind` | String: `tmux` or `process`; omitted means `tmux` |
| `status.activation.kind` | String: `tmux` or `process`; omitted means a legacy tmux activation |
| `status.activation.process` | Optional object; present only with the `process` activation tag |
| `status.activation.process.binding` | Required object identifying the exact host and ownership generation |
| `binding.hostInstanceID`, `projectUID`, `windowUID`, `agentUID`, `paneUID`, `generation`, `operationID` | Required strings, at most 256 bytes each |
| `status.activation.process.hostProcess`, `.child` | Required process identity objects: integer `pid`, integer `ownerUID`, string `start` |
| `status.processSession` | Optional durable resume record, independent of current host liveness |
| `status.processSession.provider` | Required string: `claude` or `codex`, matching the owning Agent |
| `status.processSession.binding` | Required ownership object with the fields listed above; identifies the recorded generation |
| `status.processSession.sessionID`, `.threadID` | Optional strings; Claude uses only `sessionID`, Codex only `threadID` |
| `status.processSession.connectionID`, `.turnID` | Optional strings identifying the recorded connection and turn |
| `status.processSession.resumeState` | Required string: `unknown` or `resumable`; the latter requires a recorded conversation and connection |
| `status.processSession.pending` | Optional array of control identity objects, omitted when empty |
| Control `id`, `kind`, `connectionID`, `sessionID`, `turnID` | Required strings; kind is `question` or `permission`; sessionID denotes the provider's conversation ID, including a Codex thread ID |
| `status.processSession.history` | Optional object recording the previous, retired resume generation |
| History `binding`, `sessionID` | Required ownership object and conversation ID |
| History `interruptedTurnID`, `expired` | Optional string and optional control identity array; empty values are omitted |

Process evidence never stores a tmux `%N` handle as a PID. Mixed tmux/process
activation or foreign ownership evidence is refused with
`runtime-binding-invalid`; inconsistent durable session evidence is refused
with `process-session-invalid`. These are structural state errors (exit 1).
`resumable` denotes a candidate, not proof that the provider accepts resume.
The record contains identities, never prompts, credentials, or control answers.
Schema support does not enable process creation or resume commands by itself.

## Back up before upgrading

Older binaries reject v5 with `ErrSchemaTooNew` before decoding or writing
the Registry. This is a breaking storage change. **Keep a verified pre-upgrade
backup and the matching old binary.** A binary-only downgrade is unsupported.

Stop or quiesce all Registry writers before taking the snapshot. This includes
background endpoint helpers, lifecycle observers, and ingest invocations.
Keep writers stopped through replacement and migration. Installing the new
binary does not replace every running helper. A supervisor's termination
journal is separate from Registry writes; preserve it with the state directory.

The following commands create a private snapshot outside the state directory:

```sh
state_home=${XDG_STATE_HOME:-$HOME/.local/state}
case "$state_home" in /*) ;; *) state_home="$HOME/.local/state" ;; esac
registry="$state_home/projmux/metadata/registry.json"
backup_root=$(mktemp -d "${TMPDIR:-/tmp}/projmux-before-v5.XXXXXX")
chmod 0700 "$backup_root"
cp -p "$registry" "$backup_root/registry-v4.json"
tar -C "$state_home" -cpf "$backup_root/projmux-state.tar" projmux
cp -p "$(command -v projmux)" "$backup_root/projmux-old"
sha256sum "$backup_root/registry-v4.json" "$backup_root/projmux-state.tar" \
  "$backup_root/projmux-old" > "$backup_root/SHA256SUMS"
```

On macOS use `shasum -a 256` instead of `sha256sum`. Verify the saved Registry
has `schemaVersion: 4` before upgrading. Keep this snapshot until the upgrade
and the chosen rollback window have closed. The Store also saves an exact
automatic backup adjacent to the Registry, named
`registry.json.v4.<UTC timestamp>.bak` (with a numeric suffix on collision),
and a checksum-bearing `.migration-report.json` beside it. That automatic copy
does not replace the full state snapshot.

## Restore and rollback window

The rollback window is the maintenance interval while writers remain stopped
and no activity after the snapshot needs to be preserved. Restore the snapshot
and the old binary as one operation before restarting writers. Never run an
old binary against v5 and expect it to rewrite the document.

For an exact Registry restore, with writers still stopped:

```sh
# Reuse the verified backup_root and registry values from the snapshot.
sha256sum -c "$backup_root/SHA256SUMS"
cp -p "$backup_root/registry-v4.json" "$registry.restore.tmp"
chmod 0600 "$registry.restore.tmp"
mv "$registry.restore.tmp" "$registry"
cmp "$backup_root/registry-v4.json" "$registry"
```

Verify the snapshot checksums before restoring. If other state changed during
the attempted upgrade, restore the corresponding full state snapshot while
writers are stopped, rather than combining generations from separate copies.
For example, after checksum verification, retain the attempted state and
replace it from the archive:

```sh
restore_root=$(mktemp -d "$state_home/.projmux-restore.XXXXXX")
tar -C "$restore_root" -xpf "$backup_root/projmux-state.tar"
mv "$state_home/projmux" "$backup_root/projmux-after-upgrade"
mv "$restore_root/projmux" "$state_home/projmux"
```

Use `shasum -a 256 -c` for checksum verification on macOS. Restore the old
binary to its installation path before restarting writers.
After new activity, restoring a backup loses everything written since it was
taken. Prefer a forward fix, or explicitly decide what to preserve or convert
before restoring. If process records have been written, stop their owned
children first and decide their preservation; restoring the old Registry
alone cannot stop or account for those children.

Stop the upgrade if migration, backup verification, or install convergence
fails. In particular, `make install` runs configuration convergence using the
new build **before binary publication**: schema v5 may already have been
written even when the installed binary is still old. Inspect the envelope and
restore the verified snapshot or complete a forward fix before permitting
old writers to resume.

### Process caller creator provenance

Agent creates from a process-hosted provider's descendants record
`projmux.io/creator-basis=process-chain`, `projmux.io/creator-agent` and
`projmux.io/creator-pane` on the created Agent and its managed Pane.
A bounded 64-step nearest-first kernel ancestry walk matches the provider
child's PID, OS uid and birth identity to a live process activation in the same
Registry. Project, Window, Agent, Pane, generation and current ownership must
agree. A common host process alone is insufficient. The nearest unique valid
provider child wins; multiple valid Panes for the same child fail closed.

Valid pane-chain evidence takes precedence and skips process observation.
Process-chain precedes an explicit declaration; a disagreeing declaration
prints `creator declaration not recorded: process-chain-disagrees`.
In-process operator overrides retain precedence. Invalid explicit references
still refuse creation. These annotations are provenance, never authentication.

Unrelated shells do not infer a creator or print a process diagnostic.
An ancestry read failure after observing a candidate prints
`process-chain-unobservable` and does not infer a creator. Matching candidates rejected by identity or binding checks
print `creator not recorded: process-child-identity-mismatch` or
`process-binding-mismatch`; ambiguous matches print `process-child-ambiguous`.
Observation failures preserve create stdout, routing and exit status.
