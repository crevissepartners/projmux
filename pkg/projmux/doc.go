// Package projmux is the experimental v0 Go SDK for public Projmux operations.
// API compatibility is not promised during v0: minor releases may change the
// API. The SDK ships with the root module's v0 tags; consult that tag's release
// notes and compile your consumer against the selected version before upgrading.
//
// The SDK exposes stored instructions and read-only Project, Window, Pane,
// and Agent resource lists and descriptions, runtime inventories, Agent capabilities,
// and provider model suggestions. Public resource types live in
// pkg/projmux/core and mirror CLI JSON, including list-only context. The CLI and SDK execute
// the same internal service and retain the existing personas/<name>.md format,
// case-sensitive names, 64 KiB limit and private filesystem permissions.
//
// NewClient(Options{}) captures HOME/XDG_CONFIG_HOME/XDG_STATE_HOME defaults
// exactly as the CLI does. Options{ConfigDir: "/absolute/path/projmux"} instead
// overrides that configuration directory. StateDir overrides the captured
// state directory. SocketPath chooses an exact tmux server; when empty, resource
// reads observe the projmux app socket and runtime reads observe no server.
// Neither reads the active TMUX context. No method changes
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
// ObserveRuntime reads sessions, windows or panes on one exact server and mirrors
// get runtime JSON. Its route is RuntimeRequest, then an explicit Options.SocketPath,
// then none (zero tmux calls). It does not inherit TMUX or use the app socket.
// Missing servers return RuntimeReport.Unavailable; invalid requests return a
// Refusal with CodeRuntimeRequestInvalid. Runtime results own all nested data.
// Capabilities selects one provider or an explicit Agent name/uid with
// CapabilitiesRequest. Agent results include current Registry evidence and, for
// Claude, bounded reads of Projmux's local registration helper. These reads do
// not submit provider operations; LiveVerified stays false. Models defaults to
// Claude's static suggestions; Codex attaches to the existing default endpoint
// with the CLI's 15-second bound and never starts/configures its daemon.
// CodeModelsUnavailable and CodeModelsTimeout refusals distinguish failed reads from
// expired deadlines. A custom Options.CodexModelLister must honor its context,
// remain read-only, and be safe for concurrent calls.
package projmux
