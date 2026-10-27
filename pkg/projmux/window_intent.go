package projmux

import (
	"context"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// PreflightWindowIntent gives, before anything exists, the refusals
// CreateWindowFromIntent would give intent before its first effect, with the
// same Code and message, and otherwise what the create will run. An Agent
// intent is checked by the validator CreateAgent and PreflightCreateAgent run
// (scope, profile, provider and launch lanes, instructions, creator), on the
// host the intent names. It writes nothing, runs no tmux command and starts no
// process.
//
// It does not authorize the create: CreateWindowFromIntent checks everything
// again, so settings changed between the two calls decide the create.
func (c *Client) PreflightWindowIntent(ctx context.Context, intent core.WindowCreateIntent) (core.WindowIntentPreview, error) {
	checked, err := c.checkWindowIntent(ctx, intent, tmux.ExecRunner{})
	if err != nil {
		return core.WindowIntentPreview{}, err
	}
	return checked.preview, nil
}

// CreateWindowFromIntent creates intent's Window and, for an Agent intent, its
// Agent in one transaction: a failure on the way leaves neither. A tmux Agent
// splits off the Window's initial shell, which is then retired, so the Window
// holds exactly the Agent Pane, the shape `create window --provider` commits.
// A process Agent is created detached with its new Window by the process
// creator, which reserves and rolls back both together. Every check of
// PreflightWindowIntent runs again first.
//
// Cancellation is effective before admission; admitted tmux execution and
// rollback finish independently. A tmux activation-unconfirmed error returns
// the committed Window and Agent with the error. A process startup failure
// returns a Refusal whose CreateProcessError keeps the exact recovery refs.
func (c *Client) CreateWindowFromIntent(ctx context.Context, intent core.WindowCreateIntent) (core.WindowIntentResult, error) {
	return c.createWindowFromIntent(ctx, intent, tmux.ExecRunner{})
}

// windowIntentChecks is what the checks of an intent resolved, for the create
// that runs after them.
type windowIntentChecks struct {
	preview  core.WindowIntentPreview
	scope    mutationruntime.CreateScope
	labels   map[string]string
	socket   string
	agentReq core.CreateAgentRequest
	agent    createAgentChecks
	cwdPane  string
}

func windowIntentRefusal(message string) error {
	return &Refusal{Code: CodeCreateRequestInvalid, message: message}
}

// checkWindowIntent gives every refusal the create gives before its first
// effect, in the create's order: the intent's own shape, the Window's inputs,
// then the Agent through CreateAgent's validator, the source Pane and the
// enabled-provider requirement.
func (c *Client) checkWindowIntent(ctx context.Context, intent core.WindowCreateIntent, runner mutationruntime.TmuxCommandRunner) (windowIntentChecks, error) {
	var checked windowIntentChecks
	if err := ctx.Err(); err != nil {
		return checked, err
	}
	host := intent.Host
	switch {
	case host != "" && host != "tmux" && host != "process":
		return checked, windowIntentRefusal("WindowCreateIntent.Host requires tmux or process")
	case host == "process" && intent.Agent == nil:
		return checked, windowIntentRefusal("process-window-provider-required: WindowCreateIntent with host process requires an Agent of provider claude or codex; nothing was created")
	case host != "process" && intent.Detach:
		return checked, windowIntentRefusal("WindowCreateIntent.Detach requires host process")
	}
	if host == "" {
		host = "tmux"
	}
	agent := intent.Agent
	if agent != nil {
		if agent.Resume != nil {
			return checked, windowIntentRefusal("WindowCreateIntent.Agent.Resume is not supported yet; nothing was created")
		}
		cwdFrom := strings.TrimSpace(agent.CwdFrom)
		switch {
		case cwdFrom == "pane" && agent.CwdPane == "":
			return checked, windowIntentRefusal("WindowCreateIntent.Agent.CwdFrom pane requires CwdPane, an exact uid: Pane")
		case cwdFrom != "pane" && agent.CwdPane != "":
			return checked, windowIntentRefusal("WindowCreateIntent.Agent.CwdPane requires CwdFrom pane")
		}
		if agent.CwdPane != "" {
			ref, err := selector.ParseRef(metadata.KindPane, agent.CwdPane)
			if err != nil {
				return checked, resourceRefusal(selectorInputError(err, "WindowCreateIntent.Agent.CwdPane"))
			}
			if ref.UID == "" {
				return checked, windowIntentRefusal("WindowCreateIntent.Agent.CwdPane must be an exact uid: Pane")
			}
			checked.cwdPane = ref.UID
		}
	}
	scope, labels, socket, err := c.createInputs(ctx, "WindowCreateIntent", intent.Project, intent.Name, intent.Labels, intent.SocketPath)
	if err != nil {
		return checked, err
	}
	checked.scope, checked.labels, checked.socket = scope, labels, socket
	checked.preview = core.WindowIntentPreview{Shell: agent == nil, Host: host}
	if agent == nil {
		return checked, nil
	}
	req := core.CreateAgentRequest{Host: intent.Host, Detach: intent.Detach, Project: intent.Project, Provider: agent.Provider, Profile: agent.Profile, Instructions: agent.Instructions, Model: agent.Model, Effort: agent.Effort, Labels: agent.Labels, CwdFrom: agent.CwdFrom, Payload: agent.Payload, Creator: agent.Creator, SocketPath: intent.SocketPath, Operator: agent.Operator}
	process := host == "process"
	if process {
		if err := processCreateShape(req); err != nil {
			return checked, err
		}
	}
	checked.agentReq = req
	// A tmux create always holds the providers setting, as CreateAgent does.
	// The process creator does not, so a process intent asks for it.
	skipEnabled := process && !intent.RequireEnabledProvider
	if checked.agent, err = c.checkCreateAgent(ctx, req, runner, process, skipEnabled); err != nil {
		return checked, err
	}
	planner := checked.agent.planner
	// The read half of PrepareSnapshots, as PreflightCreateAgent runs it.
	if settings := planner.Settings; settings.Instructions != "" {
		if err := planner.SettingsService().CheckPersonaLaunch("create agent", settings.Instructions, settings.InstructionsOption); err != nil {
			return checked, createRefusal(err, "CreateAgentRequest")
		}
	}
	if checked.cwdPane != "" {
		reg, err := resources.Load(c.paths)
		if err != nil {
			return checked, resourceRefusal(err)
		}
		project, err := checked.agent.execution.ResolveProject(reg, scope)
		if err != nil {
			return checked, createRefusal(err, "WindowCreateIntent")
		}
		if _, ok := mutationruntime.ProjectPaneWindow(reg, project.Metadata.UID, checked.cwdPane); !ok {
			return checked, windowIntentRefusal("WindowCreateIntent.Agent.CwdPane uid:" + checked.cwdPane + " is not a Pane of the intent's Project; nothing was created")
		}
	}
	if err := ctx.Err(); err != nil {
		return checked, err
	}
	checked.preview.Provider = checked.agent.provider
	if profile := planner.ProfileReceipt(); profile != nil {
		checked.preview.Profile = profile.Name
	}
	checked.preview.CwdFrom = "project"
	if checked.cwdPane != "" {
		checked.preview.CwdFrom = "pane"
	}
	return checked, nil
}

func (c *Client) createWindowFromIntent(ctx context.Context, intent core.WindowCreateIntent, runner mutationruntime.TmuxCommandRunner) (core.WindowIntentResult, error) {
	var result core.WindowIntentResult
	checked, err := c.checkWindowIntent(ctx, intent, runner)
	if err != nil {
		return result, err
	}
	if intent.Agent == nil {
		window, err := c.createWindow(ctx, core.CreateWindowRequest{Project: intent.Project, Name: intent.Name, Labels: intent.Labels, SocketPath: intent.SocketPath}, runner)
		result.Window, result.PaneID, result.Projection, result.Receipt = window.Window, window.PaneID, window.Projection, window.Receipt
		return result, err
	}
	if checked.preview.Host == "process" {
		return c.createProcessWindowFromIntent(ctx, intent, checked)
	}
	return c.createTmuxWindowFromIntent(ctx, intent, checked)
}

func (c *Client) createTmuxWindowFromIntent(ctx context.Context, intent core.WindowCreateIntent, checked windowIntentChecks) (core.WindowIntentResult, error) {
	var result core.WindowIntentResult
	agent := checked.agent
	planner, provider := agent.planner, agent.provider
	planner.Initialize()
	native, err := planner.PrepareNative(ctx)
	if err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Admission is the last Context boundary, as in CreateAgent.
	if err := planner.PrepareSnapshots(provider); err != nil {
		return result, createRefusal(err, "CreateAgentRequest")
	}
	var declined string
	machine := agent.agentCreation(native, &declined)
	request := mutationruntime.WindowAgentRequest{
		Window:     mutationruntime.WindowRequest{Name: intent.Name, Labels: checked.labels, Payload: checked.agentReq.Payload},
		Agent:      mutationruntime.AgentCreateRequest{Provider: provider, Placement: "right", Payload: checked.agentReq.Payload, Labels: agent.labels},
		CwdPaneUID: checked.cwdPane,
	}
	var out mutationruntime.WindowAgentResult
	err = agent.transaction.Transact(mutationruntime.CreateKindWindow, machine.WindowOperation(checked.scope, request, "create window", &out), agent.execution.ProjectOwnershipGuard(checked.scope))
	if err != nil {
		return result, createRefusal(err, "WindowCreateIntent")
	}
	if native != nil {
		native.Observe(out.Agent)
	}
	activationErr := c.confirmActivations(agent, out.Agent.ActivationTargets)
	agentRow, err := createFirstResult(out.Agent.Results)
	if err != nil {
		return result, err
	}
	notices := out.Agent.Notices
	if declined != "" {
		notices = append(notices, declined)
	}
	// The receipt records what the operation did: the Window row alone would
	// report a create.window that opened no Agent.
	rcpt := createReceipt(metadata.KindWindow, []mutationruntime.ShellCreateResource{out.Window}, []string{out.Window.UID})
	rcpt.Add(strings.ToLower(string(metadata.KindAgent)), agentRow.UID, agentRow.Name, receipt.ActionCreated)
	rcpt.Profile = planner.ProfileReceipt()
	if err := c.windowIntentProjection(&result, out.Window.UID, agentRow.UID, rcpt); err != nil {
		return result, err
	}
	result.PaneID, result.Notices = out.Window.PaneID, notices
	if activationErr != nil {
		return result, createRefusal(activationErr, "WindowCreateIntent")
	}
	return result, nil
}

func (c *Client) createProcessWindowFromIntent(ctx context.Context, intent core.WindowCreateIntent, checked windowIntentChecks) (core.WindowIntentResult, error) {
	var result core.WindowIntentResult
	created, err := c.createProcessAgentIn(ctx, checked.agentReq, &metadata.BootstrapWindow{Name: intent.Name, Labels: checked.labels})
	result.Binding, result.Notices = created.Binding, created.Notices
	if created.Agent.Metadata.UID == "" {
		return result, err
	}
	// The process creator committed the Window with its Agent; the Window is
	// read back from the Registry by the Agent's owner.
	reg, loadErr := resources.Load(c.paths)
	if loadErr != nil {
		if err != nil {
			return result, err
		}
		return result, resourceRefusal(loadErr)
	}
	windowUID := created.Binding.WindowUID
	if stored, ok := reg.Agent(created.Agent.Metadata.UID); ok && stored.Metadata.OwnerRef != nil {
		windowUID = stored.Metadata.OwnerRef.UID
	}
	if projErr := c.windowIntentProjection(&result, windowUID, created.Agent.Metadata.UID, created.Receipt); projErr != nil && err == nil {
		err = projErr
	}
	return result, err
}

// windowIntentProjection fills result with the committed Window and Agent as
// the Registry holds them now.
func (c *Client) windowIntentProjection(result *core.WindowIntentResult, windowUID, agentUID string, rcpt any) error {
	reg, err := resources.Load(c.paths)
	if err != nil {
		return resourceRefusal(err)
	}
	window, err := publicResource(reg, metadata.KindWindow, windowUID)
	if err != nil {
		return err
	}
	agent, err := publicResource(reg, metadata.KindAgent, agentUID)
	if err != nil {
		return err
	}
	result.Window = window.(core.Window)
	typed := agent.(core.Agent)
	result.Agent = &typed
	result.Projection = core.ResourceList{APIVersion: metadata.APIVersion, Kind: "WindowList", Items: []core.Resource{window, agent}}
	return unregisterCopy(rcpt, &result.Receipt)
}
