# Go SDK deletion preflight

See [Agent deletion protection](registry.md#agent-deletion-protection).

`Client.PreflightDelete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:..."})`
checks the Registry cascade without runtime access or writes, including process
Agents and Project graphs. Call it before stopping a provider for deletion.
Protected Agents produce `*projmux.Refusal` with `CodeAgentProtected`
(`agent-protected`) and `AgentUID` identifying the protected Agent, without
parsing the message. This is the same token used by CLI delete, unregister and prune.
`Delete` and `UnregisterProject` also check previews and locked execution.
Preflight is a snapshot check, not authorization for a later runtime effect;
frontends must still use the deletion executor's locked admission when executing.
Remove the label with `projmux label agent <ref> projmux.io/protected-`
before retrying.

## Shell Window and Pane creation

`Client.CreateWindow(ctx, core.CreateWindowRequest{Project: "uid:..."})` creates
one shell Window. `Client.CreatePane(ctx, core.CreatePaneRequest{Project:
"uid:...", Window: "uid:...", Placement: "right", CwdFrom: "project"})` creates
one shell Pane, using the stored Window anchor unless `Pane` names another one.
Results include the CLI JSON projection and operation receipt.

Set `Options.ProjmuxExecutable` to an absolute executable from the same projmux
build as the SDK for supervised shell children. No PATH search or implicit
executable fallback is performed. Request `SocketPath` overrides the Option;
explicit sockets require a live server with exact PID, resource UID and root
proof. The empty route uses the app server and can start it. SDK calls do not
record `create.outcome` diagnostics yet (Backlog 622).


## tmux Agent creation

`Client.CreateAgent(ctx, core.CreateAgentRequest{Project: "uid:...", Window:
"uid:...", Provider: "claude", Name: "worker"})` creates one tmux Agent through
the same transaction, provider launch plan, hooks and rollback as the CLI.
It supports Claude, Antigravity, payload-free Codex and `InteractiveOnly` Codex.
`Pane` selects an optional anchor; `Creator` accepts only an explicit `uid:`
Agent declaration and records `creator-basis=explicit` without inferring a caller.
Profiles, instructions, model, effort, workspace roots and placement use the
CLI's existing validation. Set `Options.ProjmuxExecutable` from the same build.

Native-observed Codex creation (prompted default Codex and Codex instructions)
is not yet available in the SDK: `CodeCreateNativeCodexUnsupported` refuses it
before effects. Use `projmux create agent` for these combinations. The follow-up
is **SDK prompted Codex create (including explicit observer child state routing)**
in the conversation milestone (613-B).

An activation timeout or failure after commit preserves the Agent and Pane.
`CreateAgent` returns their projection, receipt and PaneID **together with**
`CodeCreateActivationUnconfirmed`; inspect the returned Agent before retrying.
Context cancellation prevents admission only. The tmux server owns an admitted
provider's lifetime, and cancelling the caller does not stop it.

`Client.ResumeAgent(ctx, core.ResumeAgentRequest{Ref: "uid:..."})` rebinds an
Offline or Failed Claude or Antigravity tmux Agent to a new Pane, preserving its
Agent UID and provider conversation. Project and Window optionally scope Ref;
Model, Effort, DialogueReplyOnly and SocketPath use the CLI's resume rules.
The result includes Agent, Projection, Receipt, PaneID and notices. It returns
immediately after commit, without waiting for activation acknowledgement,
like CLI `agent resume`; transaction failures roll back. Cancellation prevents
admission only. Process targets, including retired process candidates, return
`CodeResumeProcessUnsupported`. Codex resume returns
`CodeResumeNativeCodexUnsupported` before effects because its native observation
is not yet available in the SDK; use `projmux agent resume`. Prompt input is not
exposed. The same `Options.ProjmuxExecutable` contract applies. The tmux CLI
rejects `-o`; the SDK receipt uses its shared operation receipt model.
