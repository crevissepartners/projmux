# Stored Codex version-pair qualification

`projmux agent app-server upgrade qualify --receipt <absolute-json>` installs
one measured receipt for its exact old/new Codex version pair. Inspect the
saved receipts with:

```sh
projmux doctor --section integrations
projmux doctor --section integrations --json
```

The default Doctor report includes the same inventory. Its
`codex_stored_qualification` JSON field also appears in support `doctor.json`,
with version pairs, statuses, verdicts, reasons, and readiness preserved.
The read does not depend on a generation-pool journal or a readable Registry.

```text
Codex stored version-pair qualification
  Store: stored
  0.153.2 -> 0.153.4: stored; verdict: yes; reason: qualified; qualification ready: true
```

| Store status | Meaning |
| --- | --- |
| `stored` | All enumerated receipts decoded and matched their filenames. |
| `absent` | No saved receipt exists; the directory may be absent or empty. |
| `damaged` | At least one receipt could not be read as its filename's pair. Other receipts remain visible. |
| `unavailable` | The store path could not be resolved or the directory could not be read. |

Each `version_pairs` row reports `stored`, `damaged`, or
`version-pair-mismatch`. A mismatch means a valid receipt names a different pair
from its filename; its verdict is not reported for the filename's pair. A
malformed `.json` filename produces a damaged row without exposing that name.
Non-regular receipt files are damaged; temporary save files are ignored.

For a stored row, `verdict` and `reason` come from the existing strict receipt
decoder. `qualification_ready` comes from the existing qualification gate:
a saved `no` or a self-consistent `yes` with unbacked evidence counters remains
not ready. This field describes only the pair's qualification, and does not
assert that the generation pool or an upgrade operation is ready.

Schema v2 has no timestamps or expiry policy. A receipt stays reportable until
it is replaced or removed. Doctor reads it without running qualification,
writing receipts or journals, restoring upgrade state, or executing lifecycle
commands.
