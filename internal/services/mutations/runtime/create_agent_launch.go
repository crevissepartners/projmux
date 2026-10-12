package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/app/initcmd"
	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/version"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
)

// agentPaneBinding is the one typed materialization contract shared by create,
// resume, topology replay, and native Codex binding. The supplied runner has
// already been bound to one explicit logical tmux route; this value contains
// resource/presentation state only and therefore cannot reopen socket authority.
type AgentPaneBinding struct {
	PaneID         string
	Provider       string
	ContextDir     string
	Title          string
	Topic          string
	TopicManual    bool
	ConversationID string
	ResumeSource   string
	ThreadID       string
	NativeCodex    bool
	// CodexNativeDeclared names the by-design reason this managed Codex Pane
	// has no native binding, from the closed declared vocabulary. It is empty
	// for every other binding, and an empty value is what makes a hook
	// observation count as an unexplained native fallback.
	CodexNativeDeclared string
	UpdatedAt           time.Time
}

type AgentPaneBindError struct {
	Operation    string
	Cause        error
	Compensation error
}

func (e *AgentPaneBindError) Error() string {
	if e == nil {
		return "managed Agent Pane binding failed"
	}
	message := fmt.Sprintf("managed Agent Pane binding failed at %s: %v", e.Operation, e.Cause)
	if e.Compensation != nil {
		message += fmt.Sprintf("; compensation failed: %v", e.Compensation)
	}
	return message
}

func (e *AgentPaneBindError) Unwrap() error { return e.Cause }

// BindAgentPaneOnRoute applies the complete managed-Agent option set as one
// all-or-error result. If any write fails, every preceding write is restored in
// reverse order on the same exact runner. The surrounding materializer may then
// roll back the Pane itself without leaving a partially authoritative live
// object behind.
func (c *AgentLaunchService) BindAgentPaneOnRoute(ctx context.Context, runner TmuxCommandRunner, binding AgentPaneBinding) error {
	paneID := strings.TrimSpace(binding.PaneID)
	if paneID == "" {
		return errors.New("managed Agent Pane binding requires an exact Pane runtime id")
	}
	if runner == nil {
		return errors.New("managed Agent Pane binding requires an exact tmux runner")
	}
	binding.Provider = normalizeAIMode(binding.Provider)
	binding.ContextDir = strings.TrimSpace(binding.ContextDir)
	binding.Topic = strings.TrimSpace(binding.Topic)
	binding.ConversationID = strings.TrimSpace(binding.ConversationID)
	binding.ResumeSource = strings.TrimSpace(binding.ResumeSource)
	binding.ThreadID = strings.TrimSpace(binding.ThreadID)
	if binding.UpdatedAt.IsZero() && binding.ConversationID != "" {
		binding.UpdatedAt = c.NowTime().UTC()
	}

	writes := []struct {
		Option string
		value  string
		unset  bool
	}{
		{Option: aiPaneManagedOption, value: "1"},
		{Option: aiPaneAgentOption, value: binding.Provider},
		{Option: aiPaneLaunchAuthorshipOption, value: "1"},
		{Option: aiPaneContextOption, value: binding.ContextDir},
		{Option: aiPaneTopicOption, value: binding.Topic, unset: binding.Topic == ""},
		{Option: aiPaneTopicManualOption, value: "on", unset: !binding.TopicManual},
		{Option: aiPaneStateOption, value: "idle"},
		{Option: aiPaneSessionIDOption, value: binding.ConversationID, unset: binding.ConversationID == ""},
		{Option: aiPaneResumeIDOption, value: binding.ConversationID, unset: binding.ConversationID == ""},
		{Option: aiPaneResumeSourceOption, value: binding.ResumeSource, unset: binding.ResumeSource == ""},
		{Option: aiPaneResumeUpdatedAtOption, unset: binding.UpdatedAt.IsZero()},
		{Option: aiPaneThreadIDOption, value: binding.ThreadID, unset: binding.ThreadID == ""},
		{Option: aiPaneCodexAuthorityOption, unset: true},
		{Option: aiPaneCodexEpochOption, unset: true},
		{Option: aiPaneCodexReasonOption, unset: true},
		{Option: aiPaneCodexDeclaredOption, unset: true},
	}
	if !binding.UpdatedAt.IsZero() {
		writes[10].value = binding.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if binding.Provider == aiModeCodex {
		writes[12] = struct {
			Option string
			value  string
			unset  bool
		}{Option: aiPaneCodexAuthorityOption, value: codexAuthorityHook}
		writes[14] = struct {
			Option string
			value  string
			unset  bool
		}{Option: aiPaneCodexReasonOption, value: codexNativeUnexplainedReason}
		if declared := codexNativeDeclaredReason(binding.CodexNativeDeclared); declared != "" {
			writes[15] = struct {
				Option string
				value  string
				unset  bool
			}{Option: aiPaneCodexDeclaredOption, value: declared}
		}
	}
	if binding.NativeCodex {
		if binding.ThreadID == "" || binding.ConversationID == "" {
			return errors.New("native Codex Pane binding requires an exact thread identity")
		}
		writes[12] = struct {
			Option string
			value  string
			unset  bool
		}{Option: aiPaneCodexAuthorityOption, value: codexAuthorityHook}
		writes[14] = struct {
			Option string
			value  string
			unset  bool
		}{Option: aiPaneCodexReasonOption, value: "observer-starting"}
	}

	titleBefore, err := ReadAgentPaneFieldOnRoute(ctx, runner, paneID, "#{pane_title}")
	if err != nil {
		return &AgentPaneBindError{Operation: "read Pane title", Cause: err}
	}
	before := make(map[string]string, len(writes))
	for _, write := range writes {
		value, readErr := ReadAgentPaneOptionOnRoute(ctx, runner, paneID, write.Option)
		if readErr != nil {
			return &AgentPaneBindError{Operation: "read Pane option " + write.Option, Cause: readErr}
		}
		before[write.Option] = value
	}

	applied := make([]struct {
		Option string
		value  string
		unset  bool
	}, 0, len(writes))
	titleApplied := false
	writeBindingOption := func(write struct {
		Option string
		value  string
		unset  bool
	}) error {
		args := []string{"set-option", "-p", "-t", paneID, write.Option, write.value}
		if write.unset {
			args = []string{"set-option", "-p", "-u", "-t", paneID, write.Option}
		}
		if _, err := runner.Run(ctx, "tmux", args...); err != nil {
			return fmt.Errorf("write Pane option %s: %w", write.Option, err)
		}
		return nil
	}
	compensate := func() error {
		var failures []error
		for i := len(applied) - 1; i >= 0; i-- {
			prior := before[applied[i].Option]
			restore := struct {
				Option string
				value  string
				unset  bool
			}{Option: applied[i].Option, value: prior, unset: prior == ""}
			if restoreErr := writeBindingOption(restore); restoreErr != nil {
				failures = append(failures, restoreErr)
			}
		}
		if titleApplied {
			if _, restoreErr := runner.Run(ctx, "tmux", "select-pane", "-T", titleBefore, "-t", paneID); restoreErr != nil {
				failures = append(failures, fmt.Errorf("restore Pane title: %w", restoreErr))
			}
		}
		return errors.Join(failures...)
	}
	if _, err := runner.Run(ctx, "tmux", "select-pane", "-T", binding.Title, "-t", paneID); err != nil {
		return &AgentPaneBindError{Operation: "set Pane title", Cause: err}
	}
	titleApplied = true
	for _, write := range writes {
		if err := writeBindingOption(write); err != nil {
			return &AgentPaneBindError{Operation: "set Pane option " + write.Option, Cause: err, Compensation: compensate()}
		}
		applied = append(applied, write)
	}
	return nil
}

func ReadAgentPaneFieldOnRoute(ctx context.Context, runner TmuxCommandRunner, paneID, format string) (string, error) {
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, format)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func ReadAgentPaneOptionOnRoute(ctx context.Context, runner TmuxCommandRunner, paneID, option string) (string, error) {
	out, err := runner.Run(ctx, "tmux", "show-options", "-pqv", "-t", paneID, option)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// AwaitAgentActivation waits only for exact provider-hook evidence committed to
// Agent authority. SessionStart keeps the Agent pending but opens a separately
// bounded acknowledgement window; only the initial-task hook acknowledges. It
// does not inspect pane_title, capture-pane, semantic tmux presentation, or
// provider conversation content.
func (c *AgentLaunchService) AwaitAgentActivation(ctx context.Context, runner TmuxCommandRunner, paneID string, startupTimeout, acknowledgementTimeout time.Duration) (bool, string, error) {
	if runner == nil {
		return false, "provider-hook", errors.New("agent activation observer is not configured")
	}
	if c.LoadRegistry == nil {
		return false, "provider-hook", errors.New("agent activation Registry observer is not configured")
	}
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{"+tmuxopts.PaneUID+"}")
	if err != nil {
		return false, "provider-hook", fmt.Errorf("read managed Pane identity: %w", err)
	}
	paneUID := strings.TrimSpace(string(out))
	if paneUID == "" {
		return false, "provider-hook", errors.New("managed Pane carries no resource identity")
	}
	initial, loadErr := c.LoadRegistry()
	if loadErr != nil {
		return false, "provider-hook", fmt.Errorf("read Agent activation authority: %w", loadErr)
	}
	paneID = strings.TrimSpace(paneID)
	first := checkAgentActivationBinding(initial, paneUID, paneID, nil)
	if !first.bound() {
		return false, "provider-hook", &agentActivationBindingError{
			Stage: agentBindingStageBeforeAwaiting, paneUID: paneUID, paneID: paneID, check: first,
		}
	}
	expect := &agentActivationBindingExpectation{AgentUID: first.AgentUID, Generation: first.Generation}
	agentUID := first.AgentUID
	startupDeadline := c.NowTime().Add(startupTimeout)
	var acknowledgementDeadline time.Time
	for {
		registry, loadErr := c.LoadRegistry()
		if loadErr != nil {
			return false, "provider-hook", fmt.Errorf("read Agent activation authority: %w", loadErr)
		}
		if current := checkAgentActivationBinding(registry, paneUID, paneID, expect); !current.bound() {
			return false, "provider-hook", &agentActivationBindingError{
				Stage: agentBindingStageWhileAwaiting, paneUID: paneUID, paneID: paneID, check: current,
			}
		}
		agent, present := registry.Agent(agentUID)
		if present && agent.Status.Activation.State == coremetadata.ActivationAcknowledged &&
			agent.Status.Activation.Source == string(coremetadata.InteractionSourceProviderHook) {
			return true, "provider-hook", nil
		}
		if acknowledgementDeadline.IsZero() && present &&
			agent.Status.Activation.State == coremetadata.ActivationPending &&
			agent.Status.Activation.Source == string(coremetadata.InteractionSourceProviderHook) &&
			!agent.Status.Activation.ObservedAt.IsZero() {
			// The Registry timestamp is the provider's exact SessionStart commit,
			// not the observer's later poll, so polling cannot silently extend the
			// five-second acknowledgement budget.
			acknowledgementDeadline = agent.Status.Activation.ObservedAt.Add(acknowledgementTimeout)
		}
		if err := ctx.Err(); err != nil {
			return false, "provider-hook", err
		}
		now := c.NowTime()
		if (!acknowledgementDeadline.IsZero() && !now.Before(acknowledgementDeadline)) ||
			(acknowledgementDeadline.IsZero() && !now.Before(startupDeadline)) {
			return false, "provider-hook", nil
		}
		c.SleepFor(50 * time.Millisecond)
	}
}

// planAgentPaneLaunchWithResume is planAgentPaneLaunch that also returns the
// resume launch, whose persona and effort notices a resume-picker create
// discloses once its Agent has a name. Every other launch returns the zero
// resume launch, which discloses nothing.
func (c *createCommand) PlanAgentPaneLaunchWithResume(provider string, workspace coremetadata.AgentWorkspace, flags resourceCreateFlags) (string, []string, agentResumeLaunch, error) {
	conversation := strings.TrimSpace(flags.resumeConversation)
	if flags.dialogueReplyOnly {
		launcher, ok := c.agents.(claudeDialogueLauncher)
		if !ok {
			return "", nil, agentResumeLaunch{}, errors.New("claude reply-only launcher is unavailable")
		}
		title, argv, err := launcher.PlanClaudeDialogueLaunch(workspace, conversation)
		return title, argv, agentResumeLaunch{}, err
	}
	if conversation == "" {
		var title string
		var argv []string
		var err error
		// Only Claude is given its persona on the command line. A Codex
		// persona rides thread/start instead, so it must not reach the Claude
		// options launcher -- which refuses every other provider outright --
		// and the plain launch below is the one the native create replaces.
		personaFile := flags.personaLaunch.snapshot.Path
		settingsFile := flags.profileLaunch.settings
		if provider != aiModeClaude {
			personaFile, settingsFile = "", ""
		}
		// Claude keeps only the last --append-system-prompt-file, so the
		// Project's label link rules ride the one file: the rules snapshot,
		// or the composite of the persona and the rules.
		if provider == aiModeClaude && flags.projectLinks.systemPromptFile != "" {
			personaFile = flags.projectLinks.systemPromptFile
		}
		// The agent guidance goes in front of both, in the same one file.
		if provider == aiModeClaude && flags.agentGuidance.systemPromptFile != "" {
			personaFile = flags.agentGuidance.systemPromptFile
		}
		switch {
		case provider == aiModeCodex && !nativeCodexFreshCreateRequired(provider, flags) && !flags.profileLaunch.codexPolicy.IsZero():
			launcher, ok := c.agents.(codexPolicyAgentLauncher)
			if !ok {
				return "", nil, agentResumeLaunch{}, errors.New("create agent: the Codex profile launcher is not configured")
			}
			title, argv, err = launcher.PlanCodexAgentLaunchWithPolicy(workspace, flags.payload, flags.model, flags.effort, flags.profileLaunch.codexPolicy)
		case settingsFile != "":
			// Only a profile with allow or deny rules reaches this launcher,
			// so every other create keeps exactly the launch it had.
			launcher, ok := c.agents.(claudeSettingsAgentLauncher)
			if !ok {
				return "", nil, agentResumeLaunch{}, errors.New("create agent: the Claude profile launcher is not configured")
			}
			title, argv, err = launcher.PlanAgentLaunchWithSettings(provider, workspace, flags.payload, flags.model, flags.effort, personaFile, settingsFile)
		case flags.model != "" || flags.effort != "" || personaFile != "":
			launcher, ok := c.agents.(claudeOptionsAgentLauncher)
			if !ok {
				return "", nil, agentResumeLaunch{}, errors.New("create agent: the Claude model launcher is not configured")
			}
			title, argv, err = launcher.PlanAgentLaunchWithOptions(provider, workspace, flags.payload, flags.model, flags.effort, personaFile)
		case provider == aiModeCodex && len(flags.payload) == 0:
			// A payload-free Codex create always takes the plain lane.
			title, argv, err = c.agents.PlanAgentLaunch(aiModeCodex, workspace, nil)
		default:
			title, argv, err = c.agents.PlanAgentLaunch(provider, workspace, flags.payload)
		}
		return title, argv, agentResumeLaunch{}, err
	}
	if c.resumes == nil {
		return "", nil, agentResumeLaunch{}, errors.New("create agent: the provider resume launcher is not configured")
	}
	// A resume-picker create hands the seam the launch values it inherited
	// from the Agents that already recorded this conversation, so the new
	// Agent starts with the persona snapshot, snapshot mode and effort they
	// would resume with. With nothing inherited the seam gets no annotations
	// and the argv is what it always was.
	launch, err := c.resumes.PlanAgentResume(provider, workspace, conversation, flags.resumeLaunchValues)
	return launch.title, launch.argv, launch, err
}
