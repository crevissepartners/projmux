package app

import (
	"encoding/json"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
)

// closeClaudePermissionAnsweredInTerminal runs next to the PostToolUse and
// PostToolUseFailure ingest. A "Yes" in Claude Code's own prompt never reaches
// the permission hook that is still waiting, so the tool call that follows
// closes the one waiting record of the same session, tool, and canonical tool
// input (agentapproval.Store.CloseAnsweredInTerminal); the hook then reads it
// closed and exits with no output, and a later `agent approval answer` is
// refused as permission-not-pending.
//
// It is an addition that never changes the ingest: it prints nothing, returns
// nothing, and swallows every error and panic. A nil seam, which every
// aiCommand fixture has, does nothing.
func (c *aiCommand) closeClaudePermissionAnsweredInTerminal(data []byte, payload claudeHookPayload) {
	if c == nil || c.permissionAnsweredInTerminal == nil {
		return
	}
	defer func() { _ = recover() }()
	var raw struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(data, &raw) != nil || len(raw.ToolInput) == 0 {
		return
	}
	c.permissionAnsweredInTerminal(payload.SessionID, payload.ToolName, raw.ToolInput)
}

// defaultClaudePermissionAnsweredInTerminal is the production seam: the
// central setting and store under the process environment.
func defaultClaudePermissionAnsweredInTerminal(sessionID, toolName string, toolInput json.RawMessage) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return
	}
	closeClaudePermissionAnsweredInTerminal(
		func() config.AgentApprovalAnswering { return claudePermissionAnsweringFromPaths(paths) },
		func() *agentapproval.Store { return agentapproval.NewStore(paths.StateDir) },
		sessionID, toolName, toolInput)
}

// closeClaudePermissionAnsweredInTerminal is the cheap path PostToolUse takes
// on every tool call: in way 1 it reads the one setting and stops, without
// opening the store; otherwise the store itself returns at once when its file
// is missing or holds no single matching waiting record, before it locks.
func closeClaudePermissionAnsweredInTerminal(answering func() config.AgentApprovalAnswering, openStore func() *agentapproval.Store,
	sessionID, toolName string, toolInput json.RawMessage,
) {
	if sessionID == "" || toolName == "" || answering == nil || answering() != config.AgentApprovalAnsweringProjmux || openStore == nil {
		return
	}
	store := openStore()
	if store == nil {
		return
	}
	_, _ = store.CloseAnsweredInTerminal(sessionID, toolName, toolInput)
}
