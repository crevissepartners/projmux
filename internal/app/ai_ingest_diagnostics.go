package app

import (
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

func (c *aiCommand) recordAIWatcher(result diagnostics.AIResult, failure diagnostics.AIFailure, started time.Time, ownsTopLevel bool) {
	if c != nil && c.operationalDiagnostics != nil {
		c.operationalDiagnostics.RecordWatcher(result, failure, started, ownsTopLevel)
	}
}

func (c *aiCommand) recordAIIngestFailure(provider diagnostics.Provider, kind diagnostics.AIKind, failure diagnostics.AIFailure) {
	if c != nil && c.operationalDiagnostics != nil {
		c.operationalDiagnostics.RecordIngest(provider, kind, diagnostics.AIResultFailed, failure, c.now(), true)
	}
}

func (c *aiCommand) recordAIIngestFromLegacy(entry aiIngestLogEntry) {
	if c == nil || c.operationalDiagnostics == nil {
		return
	}
	provider := aiDiagnosticProvider(entry.Source)
	kind := classifyAIHookKind(provider, entry.Event)
	started := c.now()
	switch entry.Result {
	case "error":
		failure := diagnostics.AIFailureRoute
		if strings.TrimSpace(entry.Event) == "" {
			failure = diagnostics.AIFailurePayloadInvalid
			kind = diagnostics.AIKindPayload
		}
		c.operationalDiagnostics.RecordIngest(provider, kind, diagnostics.AIResultFailed, failure, started, true)
	case "ignored":
		failure := diagnostics.AIFailureTargetUnmatched
		if entry.Source == "tmux-bell" && strings.TrimSpace(entry.Pane) == "" {
			failure = diagnostics.AIFailureTargetInvalid
		}
		c.operationalDiagnostics.RecordIngest(provider, kind, diagnostics.AIResultIgnored, failure, started, false)
	case "quiet":
		if kind == diagnostics.AIKindUnknown {
			c.operationalDiagnostics.RecordIngest(provider, kind, diagnostics.AIResultIgnored, diagnostics.AIFailureUnsupportedEvent, started, false)
		}
	}
}

func aiDiagnosticProvider(source string) diagnostics.Provider {
	switch strings.TrimSpace(source) {
	case "codex-hook":
		return diagnostics.ProviderCodex
	case "claude-hook":
		return diagnostics.ProviderClaude
	case "antigravity-hook":
		return diagnostics.ProviderAntigravity
	case "tmux-bell":
		return diagnostics.ProviderTmuxBell
	default:
		return diagnostics.ProviderOther
	}
}

// classifyAIHookKind projects provider-owned names to a small semantic enum.
// Unknown names stay diagnosable without entering the operations journal.
func classifyAIHookKind(provider diagnostics.Provider, event string) diagnostics.AIKind {
	event = strings.TrimSpace(event)
	if provider == diagnostics.ProviderTmuxBell {
		return diagnostics.AIKindBell
	}
	switch event {
	case "UserPromptSubmit", "UserPromptExpansion":
		return diagnostics.AIKindPrompt
	case "PermissionRequest", "PermissionDenied":
		return diagnostics.AIKindPermission
	case "Stop", "StopFailure":
		return diagnostics.AIKindStop
	case "Notification":
		return diagnostics.AIKindNotification
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch":
		return diagnostics.AIKindTool
	case "SessionStart", "SessionEnd":
		return diagnostics.AIKindSession
	case "PreCompact", "PostCompact":
		return diagnostics.AIKindCompact
	case "SubagentStart", "SubagentStop":
		return diagnostics.AIKindSubagent
	case "TeammateIdle":
		return diagnostics.AIKindTeammate
	case "Statusline":
		return diagnostics.AIKindStatusline
	case "PreInvocation", "PostInvocation":
		return diagnostics.AIKindInvocation
	case "Setup", "TaskCreated", "TaskCompleted", "Elicitation", "ElicitationResult", "ConfigChange", "InstructionsLoaded", "WorktreeCreate", "WorktreeRemove", "CwdChanged", "FileChanged":
		return diagnostics.AIKindLifecycle
	default:
		return diagnostics.AIKindUnknown
	}
}
