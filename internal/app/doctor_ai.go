package app

type doctorAINotifyStatus string

const (
	doctorAINotifyStatusInstalled doctorAINotifyStatus = "installed"
	doctorAINotifyStatusMissing   doctorAINotifyStatus = "missing"
	doctorAINotifyStatusConflict  doctorAINotifyStatus = "conflict"
	doctorAINotifyStatusSkip      doctorAINotifyStatus = "skip"
)

type doctorAINotifyIntegration struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Status         doctorAINotifyStatus `json:"status"`
	ConfigPath     string               `json:"config_path,omitempty"`
	ConflictReason string               `json:"conflict_reason,omitempty"`
	Guidance       string               `json:"guidance,omitempty"`
	TestedVersion  string               `json:"tested_version,omitempty"`
	InstallCommand string               `json:"install_command,omitempty"`
	RemoveCommand  string               `json:"remove_command,omitempty"`
	DryRunCommand  string               `json:"dry_run_command,omitempty"`
}

func doctorAINotifyDiagnostics(ai *aiCommand) []doctorAINotifyIntegration {
	if ai == nil {
		ai = newAICommand()
	}
	return []doctorAINotifyIntegration{
		doctorCodexIntegrationDiagnostic(ai),
		doctorClaudeIntegrationDiagnostic(ai),
		doctorTmuxBellIntegrationDiagnostic(ai),
	}
}

func doctorCodexIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	base := "projmux ai integrate codex"
	out := doctorAINotifyIntegration{
		ID:             "codex-hooks",
		Name:           "Codex hooks",
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
		Guidance:       "Codex requires reviewing/enabling installed hook commands from /hooks before they run.",
		TestedVersion:  ai.aiHookObservedVersion(aiHookProviderCodex),
	}

	removePlan, err := ai.planCodexIntegration(true)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	out.ConfigPath = removePlan.path

	installPlan, err := ai.planCodexIntegration(false)
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

func doctorClaudeIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	base := "projmux ai integrate claude"
	out := doctorAINotifyIntegration{
		ID:             "claude-hooks",
		Name:           "Claude Code hooks",
		TestedVersion:  ai.aiHookObservedVersion(aiHookProviderClaude),
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

func doctorTmuxBellUnsupportedDiagnostic() doctorAINotifyIntegration {
	return doctorAINotifyIntegration{
		ID:       "tmux-bell",
		Name:     "tmux bell fallback",
		Status:   doctorAINotifyStatusSkip,
		Guidance: "unsupported on the native Windows psmux track; use Codex or Claude hooks for AI notifications",
	}
}
