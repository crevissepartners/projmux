package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"slices"
	"strings"
	"time"
)

func NormalizeAIMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if slices.Contains(config.AINewWindowModes, mode) {
		return mode
	}
	return "selective"
}

type AgentLaunchService struct {
	LoadRegistry   func() (coremetadata.Registry, error)
	Sleep          func(time.Duration)
	Plan           func(string, string, []string) (string, []string, error)
	Now            func() time.Time
	DeclaredReason func(string) string
}

func (c *AgentLaunchService) NowTime() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

const (
	AiPaneAgentOption            = "@projmux_ai_agent"
	AiPaneCodexAuthorityOption   = "@projmux_codex_authority"
	AiPaneCodexDeclaredOption    = "@projmux_codex_native_declared"
	AiPaneCodexEpochOption       = "@projmux_codex_authority_epoch"
	AiPaneCodexReasonOption      = "@projmux_codex_authority_reason"
	AiPaneContextOption          = "@projmux_ai_context"
	AiPaneLaunchAuthorshipOption = tmuxopts.AgentLaunchAuthorshipPane
	AiPaneManagedOption          = "@projmux_ai_managed"
	AiPaneResumeIDOption         = "@projmux_ai_resume_id"
	AiPaneResumeSourceOption     = "@projmux_ai_resume_source"
	AiPaneResumeUpdatedAtOption  = "@projmux_ai_resume_updated_at"
	AiPaneSessionIDOption        = "@projmux_ai_session_id"
	AiPaneThreadIDOption         = "@projmux_ai_thread_id"
	CodexAuthorityHook           = "provider-hook"
	CodexNativeUnexplainedReason = "native-fallback"
)

// AgentPaneBinding is the one typed materialization contract shared by create,
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
	binding.Provider = NormalizeAIMode(binding.Provider)
	binding.ContextDir = strings.TrimSpace(binding.ContextDir)
	binding.Topic = strings.TrimSpace(binding.Topic)
	binding.ConversationID = strings.TrimSpace(binding.ConversationID)
	binding.ResumeSource = strings.TrimSpace(binding.ResumeSource)
	binding.ThreadID = strings.TrimSpace(binding.ThreadID)
	if binding.UpdatedAt.IsZero() && binding.ConversationID != "" {
		binding.UpdatedAt = c.NowTime().UTC()
	}

	writes := []struct {
		option string
		value  string
		unset  bool
	}{
		{option: AiPaneManagedOption, value: "1"},
		{option: AiPaneAgentOption, value: binding.Provider},
		{option: AiPaneLaunchAuthorshipOption, value: "1"},
		{option: AiPaneContextOption, value: binding.ContextDir},
		{option: AiPaneTopicOption, value: binding.Topic, unset: binding.Topic == ""},
		{option: AiPaneTopicManualOption, value: "on", unset: !binding.TopicManual},
		{option: AiPaneStateOption, value: "idle"},
		{option: AiPaneSessionIDOption, value: binding.ConversationID, unset: binding.ConversationID == ""},
		{option: AiPaneResumeIDOption, value: binding.ConversationID, unset: binding.ConversationID == ""},
		{option: AiPaneResumeSourceOption, value: binding.ResumeSource, unset: binding.ResumeSource == ""},
		{option: AiPaneResumeUpdatedAtOption, unset: binding.UpdatedAt.IsZero()},
		{option: AiPaneThreadIDOption, value: binding.ThreadID, unset: binding.ThreadID == ""},
		{option: AiPaneCodexAuthorityOption, unset: true},
		{option: AiPaneCodexEpochOption, unset: true},
		{option: AiPaneCodexReasonOption, unset: true},
		{option: AiPaneCodexDeclaredOption, unset: true},
	}
	if !binding.UpdatedAt.IsZero() {
		writes[10].value = binding.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if binding.Provider == "codex" {
		writes[12] = struct {
			option string
			value  string
			unset  bool
		}{option: AiPaneCodexAuthorityOption, value: CodexAuthorityHook}
		writes[14] = struct {
			option string
			value  string
			unset  bool
		}{option: AiPaneCodexReasonOption, value: CodexNativeUnexplainedReason}
		if declared := c.DeclaredReason(binding.CodexNativeDeclared); declared != "" {
			writes[15] = struct {
				option string
				value  string
				unset  bool
			}{option: AiPaneCodexDeclaredOption, value: declared}
		}
	}
	if binding.NativeCodex {
		if binding.ThreadID == "" || binding.ConversationID == "" {
			return errors.New("native Codex Pane binding requires an exact thread identity")
		}
		writes[12] = struct {
			option string
			value  string
			unset  bool
		}{option: AiPaneCodexAuthorityOption, value: CodexAuthorityHook}
		writes[14] = struct {
			option string
			value  string
			unset  bool
		}{option: AiPaneCodexReasonOption, value: "observer-starting"}
	}

	titleBefore, err := ReadAgentPaneFieldOnRoute(ctx, runner, paneID, "#{pane_title}")
	if err != nil {
		return &AgentPaneBindError{Operation: "read Pane title", Cause: err}
	}
	before := make(map[string]string, len(writes))
	for _, write := range writes {
		value, readErr := ReadAgentPaneOptionOnRoute(ctx, runner, paneID, write.option)
		if readErr != nil {
			return &AgentPaneBindError{Operation: "read Pane option " + write.option, Cause: readErr}
		}
		before[write.option] = value
	}

	applied := make([]struct {
		option string
		value  string
		unset  bool
	}, 0, len(writes))
	titleApplied := false
	writeBindingOption := func(write struct {
		option string
		value  string
		unset  bool
	}) error {
		args := []string{"set-option", "-p", "-t", paneID, write.option, write.value}
		if write.unset {
			args = []string{"set-option", "-p", "-u", "-t", paneID, write.option}
		}
		if _, err := runner.Run(ctx, "tmux", args...); err != nil {
			return fmt.Errorf("write Pane option %s: %w", write.option, err)
		}
		return nil
	}
	compensate := func() error {
		var failures []error
		for i := len(applied) - 1; i >= 0; i-- {
			prior := before[applied[i].option]
			restore := struct {
				option string
				value  string
				unset  bool
			}{option: applied[i].option, value: prior, unset: prior == ""}
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
			return &AgentPaneBindError{Operation: "set Pane option " + write.option, Cause: err, Compensation: compensate()}
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

func (c *AgentLaunchService) SleepFor(d time.Duration) {
	if c.Sleep == nil {
		time.Sleep(d)
	} else {
		c.Sleep(d)
	}
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
	first := CheckAgentActivationBinding(initial, paneUID, paneID, nil)
	if !first.Bound() {
		return false, "provider-hook", &AgentActivationBindingError{
			Stage: AgentBindingStageBeforeAwaiting, PaneUID: paneUID, PaneID: paneID, Check: first,
		}
	}
	expect := &AgentActivationBindingExpectation{AgentUID: first.AgentUID, Generation: first.Generation}
	agentUID := first.AgentUID
	startupDeadline := c.NowTime().Add(startupTimeout)
	var acknowledgementDeadline time.Time
	for {
		registry, loadErr := c.LoadRegistry()
		if loadErr != nil {
			return false, "provider-hook", fmt.Errorf("read Agent activation authority: %w", loadErr)
		}
		if current := CheckAgentActivationBinding(registry, paneUID, paneID, expect); !current.Bound() {
			return false, "provider-hook", &AgentActivationBindingError{
				Stage: AgentBindingStageWhileAwaiting, PaneUID: paneUID, PaneID: paneID, Check: current,
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

// FreshAgentLauncher is the provider launch portion every create caller shares.
type FreshAgentLauncher interface {
	PlanAgentLaunch(string, coremetadata.AgentWorkspace, []string) (string, []string, error)
}

func PlanFreshAgent(launcher FreshAgentLauncher, provider string, workspace coremetadata.AgentWorkspace, flags AgentSettingsFlags, personaFile string) (string, []string, error) {
	switch {
	case provider == "codex" && personaFile != "":
		p, ok := launcher.(interface {
			PlanCodexAgentLaunchWithBaseFile(coremetadata.AgentWorkspace, []string, string, string, codexappserver.ThreadPolicy, string) (string, []string, error)
		})
		if !ok {
			return "", nil, errors.New("create agent: managed Codex base file launcher is not configured")
		}
		return p.PlanCodexAgentLaunchWithBaseFile(workspace, flags.Payload, flags.Model, flags.Effort, flags.ProfileLaunch.CodexPolicy, personaFile)
	case provider == "codex" && !NativeCodexFreshCreateRequired(provider, flags.InteractiveOnly, flags.Payload, flags.Host, flags.ResumeConversation) && !flags.ProfileLaunch.CodexPolicy.IsZero():
		p, ok := launcher.(interface {
			PlanCodexAgentLaunchWithPolicy(coremetadata.AgentWorkspace, []string, string, string, codexappserver.ThreadPolicy) (string, []string, error)
		})
		if !ok {
			return "", nil, errors.New("create agent: the Codex profile launcher is not configured")
		}
		return p.PlanCodexAgentLaunchWithPolicy(workspace, flags.Payload, flags.Model, flags.Effort, flags.ProfileLaunch.CodexPolicy)
	case flags.ProfileLaunch.Settings != "":
		p, ok := launcher.(interface {
			PlanAgentLaunchWithSettings(string, coremetadata.AgentWorkspace, []string, string, string, string, string) (string, []string, error)
		})
		if !ok {
			return "", nil, errors.New("create agent: the Claude profile launcher is not configured")
		}
		return p.PlanAgentLaunchWithSettings(provider, workspace, flags.Payload, flags.Model, flags.Effort, personaFile, flags.ProfileLaunch.Settings)
	case flags.Model != "" || flags.Effort != "" || personaFile != "":
		p, ok := launcher.(interface {
			PlanAgentLaunchWithOptions(string, coremetadata.AgentWorkspace, []string, string, string, string) (string, []string, error)
		})
		if !ok {
			return "", nil, errors.New("create agent: the Claude model launcher is not configured")
		}
		return p.PlanAgentLaunchWithOptions(provider, workspace, flags.Payload, flags.Model, flags.Effort, personaFile)
	case provider == "codex" && len(flags.Payload) == 0:
		return launcher.PlanAgentLaunch(provider, workspace, nil)
	default:
		return launcher.PlanAgentLaunch(provider, workspace, flags.Payload)
	}
}
