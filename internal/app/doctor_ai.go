package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
)

type doctorAINotifyStatus string

const (
	doctorAINotifyStatusInstalled doctorAINotifyStatus = "installed"
	doctorAINotifyStatusStale     doctorAINotifyStatus = "stale"
	doctorAINotifyStatusMissing   doctorAINotifyStatus = "missing"
	doctorAINotifyStatusConflict  doctorAINotifyStatus = "conflict"
	doctorAINotifyStatusSkip      doctorAINotifyStatus = "skip"
)

type doctorAINotifyIntegration struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	ProviderID      string               `json:"provider_id,omitempty"`
	ProviderEnabled *bool                `json:"provider_enabled,omitempty"`
	Status          doctorAINotifyStatus `json:"status"`
	ConfigPath      string               `json:"config_path,omitempty"`
	StatusLinePath  string               `json:"statusline_config_path,omitempty"`
	ConflictReason  string               `json:"conflict_reason,omitempty"`
	Guidance        string               `json:"guidance,omitempty"`
	TestedVersion   string               `json:"tested_version,omitempty"`
	InstallCommand  string               `json:"install_command,omitempty"`
	RemoveCommand   string               `json:"remove_command,omitempty"`
	DryRunCommand   string               `json:"dry_run_command,omitempty"`
}

func doctorAINotifyDiagnostics(ai *aiCommand) []doctorAINotifyIntegration {
	if ai == nil {
		ai = newAICommand()
	}
	enabled := doctorAIEnabledProviders()
	var diagnostics []doctorAINotifyIntegration
	for _, provider := range aiprovider.HookDiagnosticSupported() {
		switch provider.ID {
		case aiprovider.Claude:
			diagnostics = append(diagnostics, withProviderDiagnosticMetadata(doctorClaudeIntegrationDiagnostic(ai), provider, enabled))
		case aiprovider.Codex:
			diagnostics = append(diagnostics, withProviderDiagnosticMetadata(doctorCodexIntegrationDiagnostic(ai), provider, enabled))
		case aiprovider.Antigravity:
			diagnostics = append(diagnostics, withProviderDiagnosticMetadata(doctorAntigravityIntegrationDiagnostic(ai), provider, enabled))
		}
	}
	diagnostics = append(diagnostics, doctorTmuxBellIntegrationDiagnostic(ai))
	return diagnostics
}

func doctorCodexIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	provider, _ := aiprovider.Lookup(string(aiprovider.Codex))
	base := provider.Integrate.Command
	out := doctorAINotifyIntegration{
		ID:             provider.HookDiagnostics.ID,
		Name:           provider.HookDiagnostics.Name,
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
	provider, _ := aiprovider.Lookup(string(aiprovider.Claude))
	base := provider.Integrate.Command
	out := doctorAINotifyIntegration{
		ID:             provider.HookDiagnostics.ID,
		Name:           provider.HookDiagnostics.Name,
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

func doctorAntigravityIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	provider, _ := aiprovider.Lookup(string(aiprovider.Antigravity))
	base := provider.Integrate.Command
	out := doctorAINotifyIntegration{
		ID:             provider.HookDiagnostics.ID,
		Name:           provider.HookDiagnostics.Name,
		TestedVersion:  ai.aiHookObservedVersion(aiHookProviderAntigravity),
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
		Guidance:       "Managed hooks use the named hooks.json entry as their install source of truth; the statusline bridge separately owns only settings.json.statusLine and uses official stack_with_default with empty custom output. Existing custom statuslines conflict and are never chained or rewritten. Use `agy -p '/hooks' --output-format json` only for read-only runtime diagnosis; PreToolUse permission policy is never installed or changed.",
	}
	removePlan, err := ai.planAntigravityHookIntegration(true)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	out.ConfigPath = removePlan.path
	removeStatusLine, err := ai.planAntigravityStatusLineIntegration(true)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	out.StatusLinePath = removeStatusLine.path
	installPlan, err := ai.planAntigravityHookIntegration(false)
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
	installStatusLine, err := ai.planAntigravityStatusLineIntegration(false)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	if installStatusLine.conflict != "" {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = installStatusLine.conflict
		return out
	}
	hooksManaged := removePlan.changed
	statusLineManaged := removeStatusLine.managed
	if hooksManaged && statusLineManaged {
		if installPlan.changed || installStatusLine.changed {
			out.Status = doctorAINotifyStatusStale
			out.ConflictReason = "managed Antigravity hooks or statusline differs from the current absolute executable, official stacked settings object, event command schema, or stdout contract; run the install command to refresh it"
			return out
		}
		out.Status = doctorAINotifyStatusInstalled
		return out
	}
	if hooksManaged || statusLineManaged {
		out.Status = doctorAINotifyStatusStale
		out.ConflictReason = "Antigravity integration is partial: both the managed hooks entry and managed stacked statusline bridge are required; run the install command to reconcile it"
		return out
	}
	out.Status = doctorAINotifyStatusMissing
	return out
}

func doctorTmuxBellIntegrationDiagnostic(ai *aiCommand) doctorAINotifyIntegration {
	base := "projmux agent integrate tmux-bell"
	out := doctorAINotifyIntegration{
		ID:             "tmux-bell",
		Name:           "tmux bell fallback",
		InstallCommand: base,
		RemoveCommand:  base + " --remove",
		DryRunCommand:  base + " --dry-run",
	}

	removePlan, err := ai.planTmuxBellIntegration(true)
	if err != nil {
		out.Status = doctorAINotifyStatusConflict
		out.ConflictReason = err.Error()
		return out
	}
	if removePlan.changed {
		out.Status = doctorAINotifyStatusInstalled
		return out
	}
	out.Status = doctorAINotifyStatusMissing
	return out
}

func doctorAIEnabledProviders() map[aiprovider.ID]bool {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return defaultEnabledProviderSet()
	}
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return defaultEnabledProviderSet()
	}
	enabled := map[aiprovider.ID]bool{}
	for _, agent := range agents {
		enabled[aiprovider.ID(agent)] = true
	}
	return enabled
}

func defaultEnabledProviderSet() map[aiprovider.ID]bool {
	enabled := map[aiprovider.ID]bool{}
	for _, agent := range config.DefaultAIEnabledAgents {
		enabled[aiprovider.ID(agent)] = true
	}
	return enabled
}

func withProviderDiagnosticMetadata(diag doctorAINotifyIntegration, provider aiprovider.Metadata, enabled map[aiprovider.ID]bool) doctorAINotifyIntegration {
	on := enabled[provider.ID]
	diag.ProviderID = string(provider.ID)
	diag.ProviderEnabled = &on
	if !on {
		diag.Guidance = appendDiagnosticGuidance(diag.Guidance, "provider disabled in Settings > AI Settings > Enabled agents; explicit diagnostics still show existing hook state")
	}
	return diag
}

// codexHookFallbackAvailable projects the existing hook diagnostic into the
// compatibility bridge's closed fallback decision. Only an enabled, fully
// installed Codex hook is treated as available; stale, missing, conflicted, or
// skipped integrations need operator repair before they can be an authority.
func codexHookFallbackAvailable(diagnostics []doctorAINotifyIntegration) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.ProviderID != string(aiprovider.Codex) {
			continue
		}
		return diagnostic.ProviderEnabled != nil &&
			*diagnostic.ProviderEnabled &&
			diagnostic.Status == doctorAINotifyStatusInstalled
	}
	return false
}

func appendDiagnosticGuidance(existing, extra string) string {
	existing = strings.TrimSpace(existing)
	extra = strings.TrimSpace(extra)
	if existing == "" {
		return extra
	}
	if extra == "" {
		return existing
	}
	return existing + " " + extra
}
