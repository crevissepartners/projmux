package projmux

import (
	"context"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeInstructionsProviderUnsupported Code = "persona-provider-unsupported"
	CodeCreateCreatorInvalid            Code = "create-creator-invalid"
	CodeCreateActivationUnconfirmed     Code = "create-activation-unconfirmed"
	CodeCreateNativeCodexUnsupported    Code = "create-native-codex-unsupported"
	CodeProfileProviderMismatch         Code = "profile-provider-mismatch"
	CodeProfileLaneUnsupported          Code = "profile-lane-unsupported"
	CodeProfilePermissionsUnsupported   Code = "profile-permissions-unsupported-provider"
	CodeProfileCodexCLIUntrusted        Code = "codex-cli-untrusted-approval-unsupported"
	CodeProfileRoleProfileInvalid       Code = "profile-role-profile-invalid"
)

// CreateAgent uses the CLI's shared tmux transaction and activation observer.
// An activation-unconfirmed error returns the committed resources with the
// error. Context cancellation only prevents admission; admitted work completes.
func (c *Client) CreateAgent(ctx context.Context, req core.CreateAgentRequest) (core.CreateAgentResult, error) {
	return c.createAgent(ctx, req, tmux.ExecRunner{})
}
func (c *Client) createAgent(ctx context.Context, req core.CreateAgentRequest, runner mutationruntime.TmuxCommandRunner) (core.CreateAgentResult, error) {
	var result core.CreateAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Window) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "CreateAgentRequest.Window must name one Window"}
	}
	for _, input := range []struct {
		kind       metadata.Kind
		raw, field string
	}{{metadata.KindWindow, req.Window, "Window"}, {metadata.KindPane, req.Pane, "Pane"}} {
		if input.raw != "" {
			if _, err := selector.ParseRef(input.kind, input.raw); err != nil {
				return result, resourceRefusal(selectorInputError(err, "CreateAgentRequest."+input.field))
			}
		}
	}
	if req.Placement != "" && req.Placement != "right" && req.Placement != "down" {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest.Placement requires right or down"}
	}
	req.CwdFrom = strings.TrimSpace(req.CwdFrom)
	if req.CwdFrom != "" && req.CwdFrom != "project" && req.CwdFrom != "pane" {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest.CwdFrom requires project or pane"}
	}
	creator := ""
	if req.Creator != "" {
		var ok bool
		creator, ok = strings.CutPrefix(req.Creator, "uid:")
		if !ok || creator == "" || strings.TrimSpace(creator) != creator || strings.ContainsAny(creator, ":/ \t") {
			return result, &Refusal{Code: CodeCreateCreatorInvalid, message: "CreateAgentRequest.Creator must be an exact Agent reference uid:<agent>"}
		}
	}
	scope, labels, socket, err := c.createInputs(ctx, "CreateAgentRequest", req.Project, req.Name, req.Labels, req.SocketPath)
	if err != nil {
		return result, err
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	execution, transaction := mutationruntime.NewSDKCreate(c.paths, socket, c.projectShell, c.projmuxExecutable, c.projectSessionName, runner, c.createDiscoveryRoots)
	transaction.Outcomes = c.createOutcomes()
	project, err := execution.ResolveProject(reg, scope)
	if err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	fields := mutationruntime.AgentInputFields{Prefix: "CreateAgentRequest"}
	settings := mutationruntime.AgentSettingsFlags{Profile: req.Profile, ProfileProjectUID: project.Metadata.UID, Instructions: req.Instructions, InstructionsOption: "instructions", Model: req.Model, Effort: req.Effort, Labels: req.Labels, Payload: req.Payload, InteractiveOnly: req.InteractiveOnly, DialogueReplyOnly: req.DialogueReplyOnly}
	planner := &mutationruntime.SDKAgentPlanner{Paths: c.paths, HomeDir: c.discoveryHome, Shell: c.projectShell, Executable: c.projmuxExecutable, Environment: c.agentEnvironment, Store: transaction.Store, Runtime: execution.Runtime, Settings: settings, Fields: fields}
	provider := req.Provider
	if provider == "" {
		selected, err := planner.SettingsService().SelectCreateProfile("create agent", settings)
		if err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
		provider = selected.Spec.Provider
	}
	provider, err = fields.RequireCanonicalProvider("create agent", provider)
	if err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	planner.Provider = provider
	if provider == "codex" && (!settings.InteractiveOnly && len(settings.Payload) > 0 || settings.Instructions != "") {
		return result, &Refusal{Code: CodeCreateNativeCodexUnsupported, message: "CreateAgentRequest.Payload and Instructions requiring native Codex observation are not yet available in the SDK; use projmux create agent"}
	}
	for _, err := range []error{fields.RequireInteractiveOnlyProvider("create agent", provider, settings.InteractiveOnly), fields.RequireLaunchOptions("create agent", provider, settings.Model, settings.Effort, settings.DialogueReplyOnly, "nothing was created"), fields.RequirePersonaLane("create agent", provider, settings), fields.RequireProfileLane("create agent", settings.Profile, settings.DialogueReplyOnly), fields.RequireDialogueMode(provider, settings.DialogueReplyOnly, settings.Payload), mutationruntime.RefuseStatelessAgentWorkspaceWithFields(provider, req.Cwd, req.AddDirs, "CreateAgentRequest.Cwd", "CreateAgentRequest.AddDirs")} {
		if err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
	}
	if err = planner.SettingsService().ResolveCreateProfile("create agent", provider, &planner.Settings); err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	// Native observations need a detached child with explicit state routing. This
	// combination is deferred; refuse before snapshots, hooks or any tmux call.
	if provider == "codex" && (!settings.InteractiveOnly && len(settings.Payload) > 0 || planner.Settings.Instructions != "") {
		return result, &Refusal{Code: CodeCreateNativeCodexUnsupported, message: "CreateAgentRequest.Payload and Instructions requiring native Codex observation are not yet available in the SDK; use projmux create agent"}
	}
	if planner.Settings.ProfileLaunch.Active() {
		if err = fields.RequireLaunchOptions("create agent", provider, planner.Settings.Model, planner.Settings.Effort, settings.DialogueReplyOnly, "nothing was created"); err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
		if err = fields.RequirePersonaLane("create agent", provider, planner.Settings); err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
	}
	if _, err := mutationruntime.DeclaredAgentCreator(&reg, creator, "create agent", fields.Option("creator")); err != nil {
		return result, &Refusal{Code: CodeCreateCreatorInvalid, message: err.Error(), cause: err}
	}
	if err := planner.RequireEnabled(provider); err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Admission is the last Context boundary, before content-addressed preparation
	// and the same uncancelled transaction/rollback path the CLI runs.
	if err := planner.PrepareSnapshots(provider); err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	planner.Initialize()
	selection := mutationruntime.CreateSelection{Name: req.Name, Windows: []string{req.Window}}
	if req.Pane != "" {
		selection.Panes = []string{req.Pane}
	}
	placement := req.Placement
	if placement == "" {
		placement = "right"
	}
	machine := &mutationruntime.AgentCreation{Execution: execution, Prepare: planner.Prepare, ResolveWorkspace: func(reg metadata.Registry, p metadata.Project, provider, cwd string, dirs []string) (metadata.AgentWorkspace, error) {
		return mutationruntime.ResolveAgentWorkspaceWithFields("create agent", reg, p, provider, cwd, dirs, "CreateAgentRequest.Cwd", "CreateAgentRequest.AddDirs")
	}, Creator: func(_ context.Context, reg *metadata.Registry) (mutationruntime.AgentCreatorRecord, error) {
		return mutationruntime.DeclaredAgentCreator(reg, creator, "create agent", fields.Option("creator"))
	}}
	var out mutationruntime.AgentCreateResult
	request := mutationruntime.AgentCreateRequest{CreateSelection: selection, Provider: provider, Cwd: req.Cwd, CwdSet: req.Cwd != "", CwdFrom: req.CwdFrom, Placement: placement, AddDirs: req.AddDirs, Payload: req.Payload, DialogueReplyOnly: req.DialogueReplyOnly, Labels: labels}
	err = transaction.TransactAdmitted(mutationruntime.CreateKindAgent, execution.ProcessAnchorAdmission(scope, selection, "create agent"), machine.Operation(scope, request, "create agent", &out), execution.ProjectOwnershipGuard(scope))
	if err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	first, err := createFirstResult(out.Results)
	if err != nil {
		return result, err
	}
	activation := mutationruntime.AgentActivationService{Store: transaction.Store, Runner: execution.Runtime.Runner, Await: planner.Launch.AwaitAgentActivation}
	activationErr := activation.Confirm(out.ActivationTargets)
	projection, err := c.createProjection(socket, core.KindAgent, first)
	if err != nil {
		return result, err
	}
	result.Projection = projection
	result.Agent = projection.Items[0].(core.Agent)
	result.PaneID = first.PaneID
	result.Notices = out.Notices
	receipt := createReceipt(metadata.KindAgent, out.Results, out.SelectedWindowUIDs)
	receipt.Profile = planner.ProfileReceipt()
	if err := unregisterCopy(receipt, &result.Receipt); err != nil {
		return result, err
	}
	if activationErr != nil {
		return result, createRefusal(activationErr, "CreateAgentRequest")
	}
	return result, nil
}

func createFirstResult(rows []mutationruntime.ShellCreateResource) (mutationruntime.ShellCreateResource, error) {
	if len(rows) == 0 {
		return mutationruntime.ShellCreateResource{}, &Refusal{Code: CodeCreateExecutionFailed, message: "shared create execution returned no resource"}
	}
	return rows[0], nil
}
