// Package projmux is the experimental v0 Go SDK for public Projmux operations.
// API compatibility is not promised during v0: minor releases may change the
// API. The SDK ships with the root module's v0 tags; consult that tag's release
// notes and compile your consumer against the selected version before upgrading.
//
// The SDK exposes stored instructions and read-only Project, Window, Pane,
// and Agent resource lists and descriptions, runtime inventories, Agent capabilities,
// provider model suggestions, stored profile list/read/write/delete, Project link rule
// read/write (including dry-run)/delete, central settings,
// saved usage snapshots/history, notification list/ack, and Agent question list/answer. Public resource types live in
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
// are *Refusal values with machine-readable Code fields. SDK input validation
// names Go fields rather than CLI flags. Native approval refusals have declared
// CodeControl constants for stale bindings, unavailable control and unsafe inputs;
// unknown native refusal codes use CodePermissionControlUnavailable.
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
//
// Profile types live in core. ListProfiles returns the CLI table cells verbatim,
// including "-" blanks, and optionally resolves one explicit Project name/uid:.
// ReadProfile preserves raw TOML bytes. WriteProfile keeps the existing validation,
// scope immutability, role claims, size limit and private permissions. DeleteProfile
// requires confirm=true (CLI --yes); it performs no running-Agent usage check.
// Profile store refusals retain profile.ReasonOf tokens as Code; a missing
// confirmation returns CodeProfileConfirmationRequired. Other filesystem errors
// stay ordinary. No profile operation applies settings to an Agent.
//
// GetConfig and SetConfig cover providers, locale, agent-questions and
// agent-approvals only. ConfigUpdate's nil policy pointers preserve existing
// values; question Window accepts "unlimited", approval Window does not. The
// files and validators are shared with the CLI; no config apply or tmux write
// follows. Config refusal codes classify invalid keys and values. Policy values
// are validated together, but the two-file write is not a transaction.
//
// GetUsage shares the statusbar cache reader and enabled-provider scope;
// GetUsageHistory shares agent usage --history's retained metric reader. Neither
// collects usage or starts providers. Existing private-permission repair and
// history locking remain. Snapshot/history contents and collection metadata
// are never written. Options.UsageStateDir explicitly selects the saved usage
// directory; empty uses the captured StateDir/usage. PROJMUX_USAGE_STATE_DIR
// is a CLI override only and does not affect SDK clients.
// UsageCache includes sorted snapshots, per-provider timestamps/backoff,
// unsupported providers and cache mtime. Encode history with a plain JSON
// Encoder (without indent) to match agent usage --history --json.
//
// ListNotifications applies the saved queue's severity/source/limit filters;
// AckNotifications removes one id or all entries under the existing store lock.
// Queue results own their maps. No attention read, reconcile, push, or sidebar
// runs. Encode notification lists with the same indentation as resource results.
// Invalid filters, invalid ack inputs and missing ids are typed Refusal values;
// filesystem and cancellation errors remain ordinary errors.
//
// ListQuestions requires an explicit Agent name or uid: reference and returns
// agent question list -o json's stored and exact process question display data.
// Encode it with a plain JSON Encoder with SetEscapeHTML(false), without indent.
// AnswerQuestion accepts repeated core.QuestionSelection values (1-based
// question/option numbers; exactly one Option, Index or Text pointer per value).
// Empty Text is preserved. CLI question refusal tokens are typed Code values.
// Secret answers are refused like the CLI; opaque provider request tokens are
// never exposed. Running process Agents use the existing exact host dispatcher;
// other Agents use the existing question store. These methods never create a
// provider or infer an active Pane. Querying a running host can reconcile its
// pending questions into the stored list. Options.QuestionDispatcher is a
// public-type transport override for isolated fixtures and must be safe for
// concurrent calls. An accepted process answer is never automatically retried.
//
// ListApprovals returns agent approval list -o json's exact display projection;
// encode with a plain JSON Encoder and SetEscapeHTML(false). AnswerApproval
// shares allow/deny mapping, answering policy, channel validation (default cli),
// audit-before-send, first-answer-wins and uncertain-delivery handling. Process
// Agents retain the CLI store path; native Codex uses existing exact control.
// ReviewApproval takes one offered decision instead of a picker. Like CLI review,
// it bypasses answering policy and adds no answer audit lines. It does not focus
// a window, start a provider or add permission choices. Options.ApprovalControl
// overrides the fenced native transport for fixtures; implementations must honor
// cancellation, reject stale exact identities and support concurrent requests.
// Store sync failures after commit are returned as successful receipts with a
// Warning; callers must not send a second answer. Context cancellation before
// admission changes nothing, while an admitted answer may already have landed.
//
// CreateProject registers or reuses an explicit absolute root with the CLI
// bootstrap rules, without materializing a runtime. Its result exposes the
// CLI JSON projection and operation receipt. Registry writes finish atomically
// after admission; canceled requests before admission write nothing. Encode
// Projection and Receipt with json.MarshalIndent(value, "", "  ") followed by
// a newline to match the CLI. Internal helpers capture HOME and SHELL at
// NewClient construction. Options.HomeDir overrides session naming only;
// Options.DefaultShell overrides the CLI-compatible shell input. Neither
// changes the environment or the configuration/state path defaults.
//
// ReadProjectLinkRules, WriteProjectLinkRules and DeleteProjectLinkRules require an
// explicit registered Project name or uid: ref and never contact tmux or providers.
// The store format is ConfigDir/project-links/<uid>.json; result JSON includes
// examples, placeholders and Project variables. DeleteProjectLinkRules requires
// confirm=true (CLI --yes); false returns CodeLinkRulesConfirmationRequired without
// deleting. Refusal carries Code plus optional Field and Reason for invalid rules,
// confirmation refusals or unreadable files; other storage errors
// remain ordinary errors. Dry runs check the same encoded size as writes.
//
// ListProjectCandidates reads configured paths in discovery order without writing
// pin preferences or saved discovery memory.
//
// Rename changes one explicitly selected Project, Window, Pane or Agent.
// Ref and optional Project/Window scopes never infer an active target. Encode
// its singular Projection and Receipt with the same JSON encoding as above.
// Request SocketPath overrides Options.SocketPath; with neither, tmux is never
// called and Window returns the CLI Registry-only Notice. An explicit socket
// must prove one UID in its Registry Project on the same server PID throughout
// writes and reobservation. A mirror failure returns Committed=true and
// CodeMirrorNotConverged with a wrapped cause; the Registry name is durable.
// Process Pane/Agent and virtual Window names are metadata-only and own no
// provider lifetime. An explicit socket observes their runtime inventory: an
// absent target produces no writes or Notice; an unavailable route gives the
// existing Window Notice. Without a socket, only Window returns that Notice.
// ControlSession descendants are outside this mutation API.
package projmux
