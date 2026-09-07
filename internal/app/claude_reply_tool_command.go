package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func readClaudeReplyToolInput(stdin io.Reader) (claudeReplyToolInput, string, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, localipc.MaxFrameBytes+1))
	if err != nil || len(data) > localipc.MaxFrameBytes {
		return claudeReplyToolInput{}, "", errClaudeReplyTool
	}
	var hook struct {
		Event     string                     `json:"hook_event_name"`
		SessionID string                     `json:"session_id"`
		Directory string                     `json:"cwd"`
		ToolName  string                     `json:"tool_name"`
		ToolUseID string                     `json:"tool_use_id"`
		AgentID   string                     `json:"agent_id"`
		AgentType string                     `json:"agent_type"`
		Input     map[string]json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(data, &hook) != nil || hook.Event != "PreToolUse" || hook.ToolName != "Bash" || hook.AgentID != "" || hook.AgentType != "" || !validCoordinationRef(hook.SessionID) || !validCoordinationRef(hook.ToolUseID) {
		return claudeReplyToolInput{}, "", errClaudeReplyTool
	}
	var command string
	for key, value := range hook.Input {
		switch key {
		case "command":
			if json.Unmarshal(value, &command) != nil {
				return claudeReplyToolInput{}, "", errClaudeReplyTool
			}
		case "description":
			var description string
			if json.Unmarshal(value, &description) != nil || len(description) > 1024 {
				return claudeReplyToolInput{}, "", errClaudeReplyTool
			}
		case "timeout":
			var timeout int
			if json.Unmarshal(value, &timeout) != nil || timeout <= 0 || timeout > 30000 {
				return claudeReplyToolInput{}, "", errClaudeReplyTool
			}
		case "run_in_background":
			if !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
				return claudeReplyToolInput{}, "", errClaudeReplyTool
			}
		default:
			return claudeReplyToolInput{}, "", errClaudeReplyTool
		}
	}
	if command == "" {
		return claudeReplyToolInput{}, "", errClaudeReplyTool
	}
	return claudeReplyToolInput{ToolUseID: hook.ToolUseID, Command: command, Directory: hook.Directory}, hook.SessionID, nil
}

func claudeReplyToolRoute(getenv func(string) string) (string, coremetadata.AgentRouteRef, error) {
	registry := getenv(internalClaudeRegistryPathEnv)
	route, ok := resolveCurrentClaudeCoordinationActivation(registry, getenv(internalActivationPaneUIDEnv), getenv(internalActivationGenerationEnv))
	if !ok {
		return "", coremetadata.AgentRouteRef{}, errClaudeReplyTool
	}
	return registry, route, nil
}

func runClaudeReplyTool(args []string, stdout io.Writer) error {
	if len(args) == 1 && args[0] == "prepare" {
		decision := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "Only the exact current broker reply command is permitted."}}
		input, session, err := readClaudeReplyToolInput(os.Stdin)
		registry, route, routeErr := claudeReplyToolRoute(os.Getenv)
		if err == nil && routeErr == nil {
			target, _ := claudeTargetForRoute(route)
			if session == target.Authority.SessionID {
				ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
				response, callErr := callClaudeCoordination(ctx, registry, route, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "tool-prepare", Target: target, SessionID: session, ToolInput: &input})
				cancel()
				if callErr == nil && response.Kind == "tool-permitted" && response.ToolResult != nil && claudeReplyMarkerPattern.MatchString(response.ToolResult.Marker) {
					decision = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": map[string]any{"command": response.ToolResult.Marker, "timeout": 5000, "run_in_background": false}}}
				}
			}
		}
		return json.NewEncoder(stdout).Encode(decision)
	}
	if len(args) != 2 || args[0] != "execute" || runtime.GOOS != "linux" {
		return errClaudeReplyTool
	}
	marker, err := claudeReplyCarrierMarker(args[1])
	if err != nil {
		return err
	}
	registry, route, err := claudeReplyToolRoute(os.Getenv)
	if err != nil {
		return err
	}
	target, _ := claudeTargetForRoute(route)
	ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
	response, err := callClaudeCoordination(ctx, registry, route, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "tool-consume", Target: target, SessionID: target.Authority.SessionID, ToolMarker: marker})
	cancel()
	if err != nil || response.Kind != "tool-permitted" || response.ToolResult == nil || response.ToolResult.Policy == nil {
		return errClaudeReplyTool
	}
	result := response.ToolResult
	gate, err := newClaudeReplyToolGate(*result.Policy)
	if err != nil {
		return err
	}
	defer gate.close()
	process, _, err := localipc.Process(os.Getpid())
	if err != nil || !gate.currentExecutable(process) || gate.digest != result.ExecutableSHA256 || len(result.Argv) != 9 || result.Argv[0] != gate.policy.Executable {
		return errClaudeReplyTool
	}
	if err := os.Chdir(gate.policy.Directory); err != nil {
		return errClaudeReplyTool
	}
	// Exec the already-open reviewed image. Replacing the pathname cannot load
	// a different executable between the last fence and execve.
	executable := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(gate.executable.Fd()), 10))
	// #nosec G204 -- pinned owned image fd/hash and current caller proved above;
	// argv comes only from the helper's exact one-use broker reply permit, with
	// a fixed environment. No carrier text is executed or passed to a shell.
	return syscall.Exec(executable, result.Argv, gate.policy.Environment)
}
