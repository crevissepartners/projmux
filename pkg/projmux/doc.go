// Package projmux is the experimental v0 Go SDK for public Projmux operations.
// API compatibility is not promised during v0: minor releases may change the
// API. The SDK ships with the root module's v0 tags; consult that tag's release
// notes and compile your consumer against the selected version before upgrading.
//
// This first slice exposes stored instructions only. The CLI and SDK execute
// the same internal service and retain the existing personas/<name>.md format,
// case-sensitive names, 64 KiB limit and private filesystem permissions.
//
// NewClient(Options{}) captures HOME/XDG_CONFIG_HOME/XDG_STATE_HOME defaults
// exactly as the CLI does. Options{ConfigDir: "/absolute/path/projmux"} instead
// selects that configuration directory, without consulting or changing global
// environment variables. Instructions live in ConfigDir/personas; deletion
// checks user profiles in ConfigDir/profiles, including invalid profiles that
// still name those instructions. No Registry or provider process is opened.
//
// Pass a non-nil context to every operation. Cancellation is checked before
// admission; a mutation already admitted finishes its atomic store operation.
// Clients own no processes or goroutines. Concurrent calls are supported, but
// multi-call transactions and concurrent profile edits are not serialized.
// Returned bytes and slices belong to the caller. Filesystem failures remain
// ordinary errors; rejected names, sizes, missing files and in-use instructions
// are *Refusal values with machine-readable Code fields.
package projmux
