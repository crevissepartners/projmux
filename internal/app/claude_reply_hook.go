package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// runClaudeMessageReply is a short synchronous official Stop hook. It is reply
// egress only: ingress never waits on or writes to a hook pipe.
func runClaudeMessageReply(args []string) error {
	return runClaudeMessageReplyInput(args, os.Stdin, os.Getenv)
}

func runClaudeMessageReplyInput(args []string, stdin io.Reader, getenv func(string) string) error {
	if len(args) != 0 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(stdin, localipc.MaxFrameBytes+1))
	if err != nil || len(data) > localipc.MaxFrameBytes {
		return nil
	}
	var hook struct {
		Event                string `json:"hook_event_name"`
		SessionID            string `json:"session_id"`
		LastAssistantMessage string `json:"last_assistant_message"`
		StopHookActive       *bool  `json:"stop_hook_active"`
	}
	if json.Unmarshal(data, &hook) != nil || hook.Event != "Stop" || !validCoordinationRef(hook.SessionID) ||
		hook.StopHookActive == nil || !validClaudeAssistantReply(hook.LastAssistantMessage) {
		return nil
	}
	registryPath := getenv(internalClaudeRegistryPathEnv)
	paneUID := getenv(internalActivationPaneUIDEnv)
	generation := getenv(internalActivationGenerationEnv)
	deadline := time.Now().Add(claudeCoordinationHookTimeout)
	var target claudeCoordinationTarget
	var routeOK bool
	for !routeOK && time.Now().Before(deadline) {
		route, current := resolveCurrentClaudeCoordinationRoute(registryPath, paneUID, generation, hook.SessionID)
		if current {
			target, routeOK = claudeTargetForRoute(route)
			if routeOK {
				ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
				_, _ = callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{
					Version: claudeCoordinationVersion, Operation: "stop-reply", Target: target,
					SessionID: hook.SessionID, AssistantMessage: hook.LastAssistantMessage,
					StopHookActive: *hook.StopHookActive,
				})
				cancel()
				return nil
			}
		}
		time.Sleep(claudeEndpointPollInterval)
	}
	return nil
}
