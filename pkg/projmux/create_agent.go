package projmux

import (
	"context"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
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
	// CodeCreateNativeCodexUnsupported is retained for source compatibility.
	CodeCreateNativeCodexUnsupported  Code = "create-native-codex-unsupported"
	CodeCreateNativeCodexUnavailable  Code = "create-native-codex-unavailable"
	CodeProfileProviderMismatch       Code = "profile-provider-mismatch"
	CodeProfileLaneUnsupported        Code = "profile-lane-unsupported"
	CodeProfilePermissionsUnsupported Code = "profile-permissions-unsupported-provider"
	CodeProfileCodexCLIUntrusted      Code = "codex-cli-untrusted-approval-unsupported"
	CodeProfileRoleProfileInvalid     Code = "profile-role-profile-invalid"
)

// CreateProcessError is the typed startup/rollback cause of a process Refusal.
// Binding and Owner retain exact recovery references even when no resource
// projection was committed. Unwrap preserves cancellation and filesystem causes.
type CreateProcessError struct {
	Code    Code
	Binding core.ProcessBinding
	Owner   core.ProcessIdentity
	message string
	cause   error
}

func (e *CreateProcessError) Error() string { return e.message }
func (e *CreateProcessError) Unwrap() error { return e.cause }

// CreateAgent uses the shared tmux transaction or explicit detached process
// startup/commit executor. Successful detached owners outlive this request.
// For tmux, cancellation prevents admission; admitted work completes. A tmux
// activation-unconfirmed error returns the committed resources with the error.
// Process startup observes cancellation and rolls back its owned birth. After
// a successful detached return, cancellation does not stop the independent owner.
func (c *Client) CreateAgent(ctx context.Context, req core.CreateAgentRequest) (core.CreateAgentResult, error) {
	if req.Host == "process" {
		return c.createProcessAgent(ctx, req)
	}
	if (req.Host != "" && req.Host != "tmux") || req.Detach {
		return core.CreateAgentResult{}, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest requires tmux or explicit process with Detach"}
	}
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
	operator := req.Operator.Client()
	if operator != "" {
		if err := operatorclient.Validate(operator); err != nil {
			return result, &Refusal{Code: CodeCreateCreatorInvalid, message: "CreateAgentRequest.Operator: " + err.Error(), cause: err}
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
	fields := mutationruntime.AgentInputFields{}
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
	for _, err := range []error{fields.RequireInteractiveOnlyProvider("create agent", provider, settings.InteractiveOnly), fields.RequireLaunchOptions("create agent", provider, settings.Model, settings.Effort, settings.DialogueReplyOnly, "nothing was created"), fields.RequirePersonaLane("create agent", provider, settings), fields.RequireProfileLane("create agent", settings.Profile, settings.DialogueReplyOnly), fields.RequireDialogueMode(provider, settings.DialogueReplyOnly, settings.Payload), mutationruntime.RefuseStatelessAgentWorkspace(provider, req.Cwd, req.AddDirs)} {
		if err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
	}
	if err = planner.SettingsService().ResolveCreateProfile("create agent", provider, &planner.Settings); err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
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
	planner.Initialize()
	native, err := planner.PrepareNative(ctx)
	if err != nil {
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
	selection := mutationruntime.CreateSelection{Name: req.Name, Windows: []string{req.Window}}
	if req.Pane != "" {
		selection.Panes = []string{req.Pane}
	}
	placement := req.Placement
	if placement == "" {
		placement = "right"
	}
	// The CLI's creator precedence: a declaration naming no Agent refuses, then
	// an in-process operator is recorded in its place.
	var declined string
	machine := &mutationruntime.AgentCreation{Execution: execution, Prepare: planner.Prepare, ResolveWorkspace: func(reg metadata.Registry, p metadata.Project, provider, cwd string, dirs []string) (metadata.AgentWorkspace, error) {
		return mutationruntime.ResolveAgentWorkspaceFor("create agent", reg, p, provider, cwd, dirs)
	}, Creator: func(_ context.Context, reg *metadata.Registry) (mutationruntime.AgentCreatorRecord, error) {
		declared, err := mutationruntime.DeclaredAgentCreator(reg, creator, "create agent", fields.Option("creator"))
		if err != nil || operator == "" {
			return declared, err
		}
		decision := mutationruntime.DecideOperatorCreator(operator, creator)
		declined = ""
		if decision.Declined != "" {
			declined = mutationruntime.CreatorDeclinedNotice(decision.Declined, decision.Declared)
		}
		return decision.AgentCreatorRecord, nil
	}}
	if native != nil {
		machine.Native = native.Engine
	}
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
	if native != nil {
		native.Observe(out)
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
	if declined != "" {
		result.Notices = append(result.Notices, declined)
	}
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

func (c *Client) createProcessAgent(ctx context.Context, req core.CreateAgentRequest) (core.CreateAgentResult, error) {
	var result core.CreateAgentResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !req.Detach || req.Pane != "" || req.Placement != "" || (req.CwdFrom != "" && req.CwdFrom != "project") || req.InteractiveOnly || req.DialogueReplyOnly {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest process requires Detach and does not support tmux placement, Pane, or alternate launch lanes"}
	}
	if req.Provider != "" && req.Provider != "claude" && req.Provider != "codex" {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest process supports Claude or Codex"}
	}
	scope, labels, _, err := c.createInputs(ctx, "CreateAgentRequest", req.Project, req.Name, req.Labels, req.SocketPath)
	if err != nil {
		return result, err
	}
	window, err := selector.ParseRef(metadata.KindWindow, req.Window)
	if err != nil {
		return result, resourceRefusal(selectorInputError(err, "CreateAgentRequest.Window"))
	}
	creator := ""
	if req.Creator != "" {
		var ok bool
		creator, ok = strings.CutPrefix(req.Creator, "uid:")
		if !ok || creator == "" || strings.TrimSpace(creator) != creator || strings.ContainsAny(creator, ":/ \t\r\n") {
			return result, &Refusal{Code: CodeCreateCreatorInvalid, message: "CreateAgentRequest.Creator must be an exact Agent reference uid:<agent>"}
		}
	}
	operator := req.Operator.Client()
	if operator != "" {
		if err := operatorclient.Validate(operator); err != nil {
			return result, &Refusal{Code: CodeCreateCreatorInvalid, message: "CreateAgentRequest.Operator: " + err.Error(), cause: err}
		}
	}
	// The operator travels to the owner route as a typed field of the internal
	// request, never as argv.
	options := mutationruntime.ProcessAgentCreateOptions{Project: scope.Project, Window: window, Provider: req.Provider, Name: req.Name, CWD: req.Cwd, Model: req.Model, Effort: req.Effort, Instructions: req.Instructions, Profile: req.Profile, Creator: creator, OperatorCreatorClient: operator, Payload: req.Payload, AddDirs: req.AddDirs, Labels: labels}
	reply, err := mutationruntime.ExecuteDetachedProcessCreate(ctx, c.processCreateContext, options)
	if reply.Binding.Agent != "" {
		_ = unregisterCopy(metadata.ProcessBinding{HostInstanceID: reply.Binding.Host, ProjectUID: reply.Binding.Project, WindowUID: reply.Binding.Window, AgentUID: reply.Binding.Agent, PaneUID: reply.Binding.Pane, Generation: reply.Binding.Generation, OperationID: reply.Binding.Operation}, &result.Binding)
	}
	if reply.Result != nil {
		var agent core.Agent
		if copyErr := unregisterCopy(reply.Result.Agent, &agent); copyErr != nil {
			return result, copyErr
		}
		result.Agent = agent
		result.Projection = core.ResourceList{APIVersion: metadata.APIVersion, Kind: "AgentList", Items: []core.Resource{agent}}
		result.Notices = reply.Result.Notices
		if copyErr := unregisterCopy(reply.Result.Receipt, &result.Receipt); copyErr != nil {
			return result, copyErr
		}
	}
	if err != nil {
		var failure *mutationruntime.ProcessCreateFailure
		if errors.As(err, &failure) {
			var owner core.ProcessIdentity
			if copyErr := unregisterCopy(failure.Owner, &owner); copyErr != nil {
				return result, copyErr
			}
			detail := &CreateProcessError{Code: Code(failure.Code), Binding: result.Binding, Owner: owner, message: err.Error(), cause: err}
			return result, &Refusal{Code: Code(failure.Code), AgentUID: failure.Binding.Agent, message: err.Error(), cause: detail}
		}
		return result, createRefusal(err, "CreateAgentRequest")
	}
	return result, nil
}
