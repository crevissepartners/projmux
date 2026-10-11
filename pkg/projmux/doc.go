// Package projmux is the experimental v0 Go SDK for public Projmux operations.
// API compatibility is not promised during v0: minor releases may change the
// API. The SDK ships with the root module's v0 tags; consult that tag's release
// notes and compile your consumer against the selected version before upgrading.
//
// The SDK exposes stored instructions and read-only Project, Window, Pane,
// and Agent resource lists and descriptions. Public resource types live in
// pkg/projmux/core and mirror CLI JSON, including list-only context. The CLI and SDK execute
// the same internal service and retain the existing personas/<name>.md format,
// case-sensitive names, 64 KiB limit and private filesystem permissions.
//
// NewClient(Options{}) captures HOME/XDG_CONFIG_HOME/XDG_STATE_HOME defaults
// exactly as the CLI does. Options{ConfigDir: "/absolute/path/projmux"} instead
// overrides that configuration directory. StateDir overrides the captured
// state directory. SocketPath chooses an exact tmux server; empty observes the
// projmux app socket, without reading the active TMUX context. No method changes
// global environment variables. Instructions live in ConfigDir/personas; deletion
// checks user profiles in ConfigDir/profiles, including invalid profiles that
// still name those instructions. Instruction methods never open the Registry or provider processes. Resource
// methods read the Registry without creating or repairing it, and take bounded
// tmux observations for context. They never control provider processes.
//
// Pass a non-nil context to every operation. Cancellation is checked before
// admission; a mutation already admitted finishes its atomic store operation.
// Clients own no processes or goroutines. Concurrent calls are supported, but
// multi-call transactions and concurrent profile edits are not serialized.
// Returned resource graphs, bytes, maps, and slices belong to the caller.
// Encode resource results with json.Encoder.SetIndent("", "  ") and Encode to
// reproduce CLI stdout including its trailing newline. Resource selection and
// Registry failures are *Refusal values; context cancellation stays ordinary.
// Instruction filesystem failures remain
// ordinary errors; rejected names, sizes, missing files and in-use instructions
// are *Refusal values with machine-readable Code fields.
package projmux
