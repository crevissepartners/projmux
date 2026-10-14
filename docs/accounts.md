# Provider account references

An account registration is a non-sensitive reference to an existing provider
root directory. It records a name, opaque UID, provider, canonical absolute root,
registration version, state, and `authenticationStatus: "unverified"`. It does
not log in, inspect credentials, authenticate an account, or start a provider.
Account names and Agent profiles are independent.

```sh
projmux config accounts add work --provider codex --root /absolute/existing/root -o json
projmux config accounts list -o json
projmux config accounts remove work -o json
projmux config accounts remove uid:account-0123456789abcdef0123456789abcdef -o json
```

Only `codex` and `claude` are accepted. Names contain 1–128 ASCII letters,
digits, dots, underscores, or hyphens and start with a letter or digit. Active
names are globally unique across providers. Roots must be existing absolute
directories. Registration resolves symlinks and stats directory metadata without
reading directory contents. A second active reference to the same provider and
canonical root is refused. A directory proof is private storage metadata, not
proof of a login identity. Different roots can belong to the same real login.

Bare `config accounts` is a usage error. Without `-o json`, output is one tab
separated row per account: name, UID, provider, root, version, state, and
authentication status. JSON add/remove return an account object; list returns
an array of active accounts (including `[]` for an absent catalog).

Removal stores a tombstone and excludes the reference from active lists. Removing
an already removed UID returns its removed value. Re-registering a retired name
creates a fresh UID and version 1; an old UID never redirects to that registration.
Names select active registrations only. Removal does not stop an Agent, delete a
provider directory, revoke credentials, or select an account for an Agent.

The version 1 catalog is `provider-accounts.json` in Projmux's ConfigDir. Writers
serialize through a persistent kernel lock, read the latest catalog, and replace
it atomically through a private sibling temporary file with file and directory
sync. New catalog directories use mode 0700; catalog and lock files use 0600.
Credentials, environment, settings, history, models, and profile data are absent.

Errors expose safe typed `account-*` reasons: invalid name/provider/root,
duplicate name/root, missing reference, invalid store, lock timeout, I/O,
cancellation, or unknown write outcome. A corrupt catalog is refused rather
than overwritten or treated as empty. Cancellation before atomic replace leaves
the catalog unchanged. After replace, durability is completed without honoring
late cancellation. `account-write-unknown` means the write may have committed;
inspect the catalog before deciding what to do and never automatically retry.
There is no fallback to another root or account.

The SDK provides `RegisterAccount`, `ListAccounts`, and `RemoveAccount` with
`core.RegisterAccountRequest`, `core.ProviderAccount`, and
`core.RemoveAccountRequest`. It uses the Client's captured ConfigDir. Returned
values and slices belong to the caller; independent Clients share only the
explicit on-disk catalog. Account selection across Agent lifecycle operations
and account usage collection are separate capabilities.
