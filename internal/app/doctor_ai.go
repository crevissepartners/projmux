package app

type doctorAINotifyStatus string

const (
	doctorAINotifyStatusInstalled doctorAINotifyStatus = "installed"
	doctorAINotifyStatusMissing   doctorAINotifyStatus = "missing"
	doctorAINotifyStatusConflict  doctorAINotifyStatus = "conflict"
)

type doctorAINotifyIntegration struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Status         doctorAINotifyStatus `json:"status"`
	ConfigPath     string               `json:"config_path,omitempty"`
	ConflictReason string               `json:"conflict_reason,omitempty"`
	InstallCommand string               `json:"install_command,omitempty"`
	RemoveCommand  string               `json:"remove_command,omitempty"`
	DryRunCommand  string               `json:"dry_run_command,omitempty"`
}

func doctorAINotifyDiagnostics(ai *aiCommand) []doctorAINotifyIntegration {
	if ai == nil {
		ai = newAICommand()
	}
	return []doctorAINotifyIntegration{
		doctorCodexIntegrationDiagnostic(ai, codexIntegrationLegacyNotify),
		doctorCodexIntegrationDiagnostic(ai, codexIntegrationHooks),
		doctorClaudeIntegrationDiagnostic(ai),
		doctorTmuxBellIntegrationDiagnostic(ai),
	}
}

func doctorCodexIntegrationDiagnostic(ai *aiCommand, mode codexIntegrationMode) doctorAINotifyIntegration {
	base := "projmux ai integrate codex --mode " + string(mode)
	out := doctorAINotifyIntegration{
		ID:             "codex-" + string(mode),
		Name:           "Codex " + doctorCodexModeLabel(mode),
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
	}

	removePlan, err := ai.planCodexIntegration(mode, true, false)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	out.ConfigPath = removePlan.path

	installPlan, err := ai.planCodexIntegration(mode, false, false)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	if installPlan.conflict != "" {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = installPlan.conflict
		return out
	}
	if removePlan.changed {
		out.Status = doctorAINotifyStatusInstalled
		return out
	}
	out.Status = doctorAINotifyStatusMissing
	return out
}

func doctorCodexModeLabel(mode codexIntegrationMode) string {
	switch mode {
	case codexIntegrationHooks:
		return "hooks"
	default:
		return "legacy notify"
	}
}

func doctorClaudeIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	base := "projmux ai integrate claude"
	out := doctorAINotifyIntegration{
		ID:             "claude-hooks",
		Name:           "Claude Code hooks",
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
	}

	removePlan, err := ai.planClaudeHookIntegration(true)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	out.ConfigPath = removePlan.path

	installPlan, err := ai.planClaudeHookIntegration(false)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	if installPlan.conflict != "" {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = installPlan.conflict
		return out
	}
	if removePlan.changed {
		out.Status = doctorAINotifyStatusInstalled
		return out
	}
	out.Status = doctorAINotifyStatusMissing
	return out
}

func doctorTmuxBellIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	base := "projmux ai integrate tmux-bell"
	out := doctorAINotifyIntegration{
		ID:             "tmux-bell",
		Name:           "tmux bell fallback",
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
	}

	removePlan := ai.planTmuxBellIntegration(true)
	if removePlan.changed {
		out.Status = doctorAINotifyStatusInstalled
		return out
	}
	out.Status = doctorAINotifyStatusMissing
	return out
}
