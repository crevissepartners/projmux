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
// Delete removes explicitly selected tmux Windows, Panes and Agents through
// the CLI's deletion planner, exact socket/PID/UID guards, durable intent, rollback and Registry
// transaction. One Ref may select multiple resources, returned in Plan.Targets;
// more than one requires Confirm, using the same CLI confirmation rule.
// DeleteRequest.SocketPath overrides Options.SocketPath; empty
// routing refuses without observing an ambient server. DryRun and missing Confirm
// return a plan without writing. Process targets return declared unsupported
// Codes. The SDK never queues a self-kill. Deletion records use via=sdk; actor is unproven outside an ambient Pane.
// Delete requires an explicit socket outside tmux, matching CLI delete; CLI
// stop project has no socket flags and uses the proved app-owned server, so
// StopProject defaults to that app socket instead.
// StopProject stops Running process Agents and the exact persistent tmux session
// through the shared CLI executor. StopProjectRequest.SocketPath overrides
// Options.SocketPath; empty routing uses the app-owned projmux socket with server
// generation and Project UID guards. On success, Interruptions contains only
// evidence remaining in the Registry. Its receipt always reports focus unchanged.
// Born process stops require actual Wait and owner/child retirement; child-free
// claims remain refused. Partial failures retain confirmed Agent stops. Project
// identity, desired topology, Agent/Pane identities and conversations are
// preserved. A process-only Project needs no live tmux server.
// CreateWindow and CreatePane create shell resources through the same CLI
// transaction, hooks, exact socket/PID/UID guards and rollback. Requests require
// an explicit Project; CreatePane also requires an explicit Window. Request
// SocketPath overrides Options.SocketPath; empty uses the proved app socket.
// Options.CreateRecorder optionally receives each finished Window, Pane, Agent
// or Resume transaction with CLI create.outcome timings, after unlock and
// rollback/cleanup. Nil records nothing; no journal path is inferred.
// Options.ProjmuxExecutable must name an absolute executable from the same
// projmux build (same module version and commit) as this SDK. Other builds are
// unsupported and their results are undefined; no version probe is performed.
// An embedding projmux executable can explicitly pass its own os.Executable()
// path. The SDK uses this binary for supervised shell children and owned
// internal process launchers, owners and supervisors. It never delegates
// mutations to public CLI commands. Missing, relative or invalid executables
// return CodeCreateExecutableInvalid before effects for creation; ResumeAgent
// and RelaunchAgent return CodeExecutableInvalid instead. The older constant
// remains exported. Delete replacement shells
// do not use this supervisor and do not require ProjmuxExecutable.
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
// ParseProfile checks supplied TOML without reading storage and returns typed
// ProfileSpec values. PreviewProfile applies storage admission without writes
// or reservations; a later WriteProfile repeats the checks. Both return no
// partial result on errors and honor context cancellation.
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
// UnregisterProject removes one explicitly named Project Registry graph while
// preserving runtime and external assets. UnregisterRequest.Ref names
// one Project by name or uid:. DryRun and missing Confirm write nothing; the latter
// returns CodeConfirmationRequired with the plan. Committed results carry the
// shared receipt and deletion-record OperationID. Records use via sdk and no
// ambient Pane actor. Record failures are best effort and appear in Notice.
// Plan changes, lifecycle refusals and Registry execution failures have declared
// Code constants and preserve their causes. CLI selection-result messages remain
// unchanged; SDK input-shape messages name Go fields.
// Delete selects one explicit Window, Pane or Agent ref on a required explicit
// tmux socket. Process targets are refused before runtime or Registry effects.
// Its reviewed plan includes exact live Pane targets and required replacement
// shells. One named leaf Pane uses the CLI confirmation exemption. Admitted
// execution and compensation ignore cancellation; a partial kill failure can
// return Committed=false with runtime effects and retained Registry resources.
// The SDK never infers a caller Pane or queues self-kill.
// Agent deletion protection is the presence of metadata.labels["projmux.io/protected"].
// Delete and UnregisterProject return CodeAgentProtected in preview and execution
// when the plan removes a protected Agent. PreflightDelete checks the same graph,
// including process Agents and Projects, without runtime access. Frontends call
// it before provider Stop, then recheck under effect admission at execution.
// ResumeAgent rebinds an explicitly selected Offline or Failed tmux Agent to
// a new Pane through the CLI's shared transaction, preserving Agent UID and
// provider conversation. It returns Projection, Receipt, PaneID and notices
// immediately after commit. ResumeAgent does not wait for activation
// acknowledgement, like CLI agent resume. Transaction failures roll back.
// Caller Context cancellation prevents admission only; admitted work completes.
// Process Agents (including retired process Pane candidates) return
// CodeResumeRequestInvalid unless explicitly detached. Native Codex resumes the stored
// thread with current settings; CodeResumeNativeCodexUnavailable refuses missing
// or unavailable native routing. CodeResumeNativeCodexUnsupported remains
// exported for source compatibility. Prompt is process-only; tmux resume rejects
// process inputs. SocketPath overrides Options.SocketPath, or uses the proved app
// route when empty. Options.ProjmuxExecutable has the create supervisor contract.
//
// CreateAgent creates one tmux Claude, Antigravity or Codex Agent in explicit
// Project/Window scope. Prompted Codex shares the CLI native create transaction,
// instructions/profile policy and initial turn. Read-only endpoint readiness,
// proxy and observer children use captured provider environment and explicit
// Projmux paths; no daemon lifecycle is acquired. Creator is a uid: declaration
// only; no Pane or process-chain caller is inferred. Plain and InteractiveOnly
// Codex retain their existing launch lanes. Native resume reuses its stored thread.
// After commit, failed activation confirmation preserves the Agent and Pane.
// CreateAgent returns the committed result and CodeCreateActivationUnconfirmed
// together. Callers must inspect both the result and error before retrying.

// SampleSystem exposes the existing host statusbar CPU/memory sampler as
// core.SystemSample. Sampling updates StateDir/live-resources-sample.json for
// the 30-second CPU delta reference, independently of statusbar enablement.
// It does not collect usage history. Unsupported platforms and unavailable
// percentages are distinct; result pointers belong to the caller.

package projmux
