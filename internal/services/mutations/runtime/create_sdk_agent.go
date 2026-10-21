package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"path/filepath"
	"slices"
	"strings"
)

// SDKAgentPlanner owns no provider process or background work.
type SDKAgentPlanner struct {
	Paths                      config.Paths
	HomeDir, Shell, Executable string
	Environment                map[string]string
	Store                      *CreateStore
	Runtime                    *Materializer
	Settings                   AgentSettingsFlags
	Provider                   string
	Fields                     AgentInputFields
	Launch                     *AgentLaunchService
	Command                    *AgentCommandPlanner
	Persona                    PersonaLaunch
}

func (p *SDKAgentPlanner) Initialize() {
	lookup := func(key string) string { return p.Environment[key] }
	shell := strings.TrimSpace(p.Shell)
	if !filepath.IsAbs(shell) || strings.ContainsAny(shell, "\x00\r\n") {
		shell = "/bin/sh"
	}
	switch filepath.Base(shell) {
	case "bash", "dash", "ksh", "mksh", "sh", "zsh":
	default:
		shell = "/bin/sh"
	}
	command := &AgentCommandPlanner{LookupEnv: lookup, HomeDir: p.HomeDir, CommandShell: shell, LookupBinary: func(bin string) string {
		for _, dir := range filepath.SplitList(p.Environment["PATH"]) {
			candidate := filepath.Join(dir, bin)
			if filepath.IsAbs(candidate) && IsExecutable(candidate) {
				return candidate
			}
		}
		return ""
	}}
	command.ExecArgv = func(provider string, extra []string) ([]string, string, error) {
		bin := command.FindAgentBinary(provider)
		if bin == "" {
			return nil, "", fmt.Errorf("selected runner is not installed: %s", provider)
		}
		return append([]string{bin}, extra...), filepath.Dir(bin), nil
	}
	p.Command = command
	p.Launch = &AgentLaunchService{LoadRegistry: p.Store.Load, DeclaredReason: func(value string) string {
		if slices.Contains([]string{"interactive-only", "payload-free-fallback", "rollout-catalog-resume"}, value) {
			return value
		}
		return ""
	}, Plan: func(provider, cwd string, extra []string) (string, []string, error) {
		plan, err := command.PlanLaunch(provider, cwd, extra, nil, "")
		return plan.Title, plan.CommandArgs, err
	}}
}
func (p *SDKAgentPlanner) SettingsService() *AgentSettings {
	return &AgentSettings{Fields: p.Fields, Paths: func() (config.Paths, error) { return p.Paths, nil }, ProfileProject: func(_ string, f AgentSettingsFlags) (string, error) { return f.ProfileProjectUID, nil }}
}
func (p *SDKAgentPlanner) RequireEnabled(provider string) error {
	enabled, err := config.LoadAIEnabledAgentsFile(p.Paths.AIEnabledAgentsFile())
	if err != nil {
		enabled = config.DefaultAIEnabledAgents
	}
	if slices.Contains(enabled, config.AIAgentProvider(provider)) {
		return nil
	}
	return errors.New("AI agent " + provider + " is disabled; enable it with: projmux config providers --enable " + provider)
}
func (p *SDKAgentPlanner) PrepareSnapshots(provider string) error {
	var err error
	if p.Settings.Instructions != "" {
		p.Persona, err = p.SettingsService().PreparePersonaLaunch("create agent", p.Settings.Instructions, p.Settings.InstructionsOption)
		if err != nil {
			return err
		}
	}
	p.Settings.PersonaLaunch = p.Persona
	return p.SettingsService().PrepareProfileSettings("create agent", provider, &p.Settings)
}
func (p *SDKAgentPlanner) Prepare(_ coremetadata.Registry, project coremetadata.Project, _ coremetadata.AgentWorkspace) (AgentPreparation, error) {
	provider := p.Provider
	var links ProjectLinksLaunch
	var guidance AgentGuidanceLaunch
	if !p.Settings.DialogueReplyOnly {
		service := &GuidanceService{Paths: func() (config.Paths, error) { return p.Paths, nil }}
		if provider == "claude" {
			links = service.PlanProjectLinks(provider, project, nil).WithCreateFile(p.Persona)
			base := links.SystemPromptFile
			if base == "" && p.Persona.Name != "" {
				base = p.Persona.Snapshot.Path
			}
			guidance = service.PlanAgentGuidance(provider, nil).WithCreateFile(base)
		} else if provider == "codex" {
			links = service.PlanCodexProjectLinks(project)
			guidance = service.PlanCodexAgentGuidance()
			if !NativeCodexFreshCreateRequired(provider, p.Settings.InteractiveOnly, p.Settings.Payload, "", "") {
				guidance = guidance.WithCodexBaseFile(p.Paths, links, p.Persona.Content)
			}
		}
	}
	f := p.Settings
	plan := func(workspace coremetadata.AgentWorkspace) (string, []string, error) {
		if f.DialogueReplyOnly {
			return p.Command.PlanDialogueLaunch(workspace, "", func() (string, error) { return p.Executable, nil })
		}
		personaFile := p.Persona.Snapshot.Path
		if links.SystemPromptFile != "" {
			personaFile = links.SystemPromptFile
		}
		if guidance.SystemPromptFile != "" {
			personaFile = guidance.SystemPromptFile
		}
		if provider != "claude" {
			personaFile = ""
		}
		if provider == "codex" && guidance.CodexBaseFile {
			personaFile = guidance.SystemPromptFile
		}
		if provider == "codex" && guidance.Unavailable != nil {
			return "", nil, guidance.Unavailable
		}
		return PlanFreshAgent(p.Launch, provider, workspace, f, personaFile)
	}
	declared := ""
	if provider == "codex" {
		declared = "payload-free-fallback"
		if f.InteractiveOnly {
			declared = "interactive-only"
		}
	}
	return AgentPreparation{Plan: plan, Bind: func(ctx context.Context, paneID string, w coremetadata.AgentWorkspace, title string) error {
		return p.Launch.BindAgentPaneOnRoute(ctx, p.Runtime.Runner, AgentPaneBinding{PaneID: paneID, Provider: provider, ContextDir: w.CWD, Title: title, CodexNativeDeclared: declared})
	}, Annotations: func(base map[string]string) map[string]string {
		if f.DialogueReplyOnly {
			base = WithDialogueReplyOnlyAnnotation(true, base)
		}
		base = p.Persona.WithAnnotations(base)
		base = WithEffortAnnotation(f.Effort, base)
		base = WithModelAnnotation(f.Model, base)
		base = f.ProfileLaunch.WithAnnotations(base)
		base = WithCreateSettingSources(f, base)
		return guidance.WithCreateAnnotation(links.WithCreateAnnotation(base))
	}, ManagedBase: func(uid string) (*ManagedCodexBase, error) {
		return PrepareManagedCodexBase(p.Paths, uid, guidance.CodexDeveloperInstructions(uid, links.DeveloperInstructions(p.Persona.Content)), nil, errors.Join(guidance.Unavailable, links.Unavailable, links.Project.Unavailable))
	}, DeveloperInstructions: func(uid string) string {
		return guidance.CodexDeveloperInstructions(uid, links.DeveloperInstructions(p.Persona.Content))
	}, Policy: f.ProfileLaunch.CodexPolicy, Model: f.Model, Effort: f.Effort, Notices: func(name string) []string {
		var out []string
		for _, v := range []string{links.Notice(name), guidance.Notice(name)} {
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}}, nil
}
func (p *SDKAgentPlanner) ProfileReceipt() *receipt.ReceiptProfile {
	return p.Settings.ProfileLaunch.Receipt()
}
