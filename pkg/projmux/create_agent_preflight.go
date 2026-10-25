package projmux

import (
	"context"

	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// PreflightCreateAgent gives, before a Window for the Agent exists, the
// refusals CreateAgent would give req for its profile, instructions and
// model, with CreateAgent's Code and message, and otherwise the provider the
// create will run. It runs the checks CreateAgent runs before its first
// effect, in the same order, and reads the stored instructions req names;
// it resolves no Window or Pane (an empty Window is allowed, a named one is
// only parsed), writes nothing, runs no tmux command and starts no process.
// A process request is first refused as CreateAgent refuses its shape, and
// its resolved provider must be one the process creator hosts.
//
// It does not authorize the create: CreateAgent checks again, so settings
// changed between the two calls decide the create. It cannot answer what
// only the create's effects reveal: a Window or Pane that does not resolve,
// an instructions or profile settings snapshot that cannot be written, Codex
// native preparation, a missing Project root, or the workspace. A request
// with more than one defect can meet a different first refusal in the
// detached process creator, whose checks run in another order.
func (c *Client) PreflightCreateAgent(ctx context.Context, req core.CreateAgentRequest) (core.CreateAgentPreflight, error) {
	return c.preflightCreateAgent(ctx, req, tmux.ExecRunner{})
}

func (c *Client) preflightCreateAgent(ctx context.Context, req core.CreateAgentRequest, runner mutationruntime.TmuxCommandRunner) (core.CreateAgentPreflight, error) {
	var result core.CreateAgentPreflight
	if err := ctx.Err(); err != nil {
		return result, err
	}
	process := req.Host == "process"
	if process {
		if err := processCreateShape(req); err != nil {
			return result, err
		}
	} else if (req.Host != "" && req.Host != "tmux") || req.Detach {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreateAgentRequest requires tmux or explicit process with Detach"}
	}
	checked, err := c.checkCreateAgent(ctx, req, runner, process)
	if err != nil {
		return result, err
	}
	// The read half of PrepareSnapshots, where CreateAgent resolves the
	// instructions a request or its profile names.
	settings := checked.planner.Settings
	if settings.Instructions != "" {
		if err := checked.planner.SettingsService().CheckPersonaLaunch("create agent", settings.Instructions, settings.InstructionsOption); err != nil {
			return result, createRefusal(err, "CreateAgentRequest")
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Provider = checked.provider
	return result, nil
}
