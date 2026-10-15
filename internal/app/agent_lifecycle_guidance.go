package app

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Invocation-only disclosure. Neither this carrier nor its enums are persisted
// or part of the public SDK. A value pointer preserves explicit none.
type agentSettingsApplication struct {
	Entries         []agentSettingApplication `json:"entries"`
	NotAppliedCount int                       `json:"notAppliedCount"`
}
type agentSettingApplication struct {
	Setting        string  `json:"setting"`
	Status         string  `json:"status"`
	Reason         string  `json:"reason"`
	Value          *string `json:"value,omitempty"`
	RequestedValue *string `json:"requestedValue,omitempty"`
	NextAction     string  `json:"nextAction,omitempty"`
}

type agentApplicationSource struct {
	provider, host string
	settings       agentSettingsLaunch
	resumed        agentResumeLaunch
	guidance       agentGuidanceLaunch
	links          projectLinksLaunch
	workspace      coremetadata.AgentWorkspace
	annotations    map[string]string
	snapshot       bool // process ordinary resume consumes recorded guidance/rules.
}

// Called only after launch preparation/commit succeeds, with the actual values
// of that preparation, never with a restart preflight substituted for them.
func preparedAgentSettingsApplication(s agentApplicationSource) *agentSettingsApplication {
	a := &agentSettingsApplication{Entries: make([]agentSettingApplication, 0, 8)}
	add := func(setting, status, reason string, value *string) {
		a.Entries = append(a.Entries, agentSettingApplication{Setting: setting, Status: status, Reason: reason, Value: value})
	}
	value := func(s string) *string { return &s }
	r := s.settings.resolution
	if s.settings.layered {
		add("profile", "recorded", "profile-recipe-recorded", value(r.New.Profile.Name))
	} else if s.resumed.profileName != "" {
		add("profile", "recorded", "profile-recipe-recorded", value(s.resumed.profileName))
	} else {
		add("profile", "unobserved", "application-unobserved", nil)
	}
	reason := "launch-arguments"
	if s.provider == aiModeCodex {
		reason = "thread-settings-ready"
	}
	if s.settings.layered && r.New.Model.Value != "" && (s.host == "process" || r.PassModel) {
		add("model", "delivered", reason, value(r.New.Model.Value))
	} else {
		add("model", "preserved", "conversation-model-preserved", nil)
	}
	effort, _, _ := claudeResumeEffort(s.provider, s.settings.launchAnnotations(s.annotations))
	if s.resumed.effortSkipped {
		add("effort", "not-applied", "effort-invalid", nil)
		a.Entries[len(a.Entries)-1].RequestedValue = value(s.resumed.effortInvalid)
	} else if effort != "" {
		add("effort", "delivered", reason, value(effort))
	} else {
		add("effort", "unobserved", "application-unobserved", nil)
	}
	instructions := r.New.Instructions.Value
	if !s.settings.layered {
		instructions = s.annotations[coremetadata.AnnotationAgentPersona]
	}
	switch {
	case r.InstructionsNotApplied:
		add("instructions", "not-applied", "codex-instructions-immutable", value(instructions))
		a.Entries[len(a.Entries)-1].RequestedValue = value(r.WantedInstructions)
	case r.InstructionsUnavailable != "" || s.settings.instructionsErr != nil || s.settings.snapshotErr != nil || s.resumed.personaUnavailable != nil:
		add("instructions", "not-applied", "instructions-unavailable", nil)
		a.Entries[len(a.Entries)-1].RequestedValue = value(r.WantedInstructions)
	case s.provider == aiModeCodex:
		add("instructions", "preserved", "codex-instructions-immutable", value(instructions))
	default:
		add("instructions", "delivered", "launch-arguments", value(instructions))
	}
	for _, part := range []struct {
		setting, reason     string
		active, unavailable bool
	}{
		{"agent-guidance", "guidance-unavailable", s.guidance.active || s.snapshot, s.guidance.unavailable != nil || s.resumed.agentGuidanceUnavailable != nil},
		{"project-guidance", "project-guidance-unavailable", s.links.project.active || s.settings.projectGuidance.active, s.links.project.unavailable != nil || s.settings.projectGuidance.unavailable != nil},
		{"link-rules", "link-rules-unavailable", s.links.active || s.snapshot, s.links.unavailable != nil || s.resumed.projectLinksUnavailable != nil},
	} {
		switch {
		case part.unavailable:
			add(part.setting, "not-applied", part.reason, nil)
		case s.provider == aiModeCodex:
			add(part.setting, "preserved", "codex-instructions-immutable", nil)
		case part.active:
			add(part.setting, "delivered", "launch-arguments", nil)
		default:
			add(part.setting, "unobserved", "application-unobserved", nil)
		}
	}
	add("workspace", "delivered", "workspace-resolved", value(s.workspace.CWD))
	for i := range a.Entries {
		e := &a.Entries[i]
		if e.Status != "not-applied" {
			continue
		}
		a.NotAppliedCount++
		e.NextAction = lifecycleFallbackText("next." + e.Reason)
	}
	return a
}

// A frozen recipe has committed values but no provider yet. Consuming it later
// reports its frozen command, without consulting the current profile again.
func frozenAgentSettingsApplication(record *deferredLaunchRecord, pending bool) *agentSettingsApplication {
	a := &agentSettingsApplication{Entries: make([]agentSettingApplication, 0, 8)}
	for _, setting := range []string{"profile", "model", "effort", "instructions", "agent-guidance", "project-guidance", "link-rules", "workspace"} {
		e := agentSettingApplication{Setting: setting, Status: "pending", Reason: "first-input-required"}
		if !pending {
			e.Status, e.Reason = "delivered", "launch-arguments"
		}
		var v string
		switch setting {
		case "profile":
			v = record.NewAnnotations[coremetadata.AnnotationAgentProfile]
			e.Status, e.Reason = "recorded", "profile-recipe-recorded"
		case "model":
			v = record.Model
		case "effort":
			v = record.Effort
		case "instructions":
			v = record.NewAnnotations[coremetadata.AnnotationAgentPersona]
		case "workspace":
			v = record.Command.Dir
			if !pending {
				e.Reason = "workspace-resolved"
			}
		default:
			a.Entries = append(a.Entries, e)
			continue
		}
		e.Value = &v
		a.Entries = append(a.Entries, e)
	}
	return a
}

func processAgentSettingsApplication(provider string, settings agentSettingsLaunch, command processhost.Command, annotations map[string]string) *agentSettingsApplication {
	return preparedAgentSettingsApplication(agentApplicationSource{provider: provider, host: "process", settings: settings, annotations: annotations, workspace: coremetadata.AgentWorkspace{CWD: command.Dir}, snapshot: true})
}

func lifecycleFallbackText(key string) string {
	return LocalizeText(i18n.FallbackLocale, i18n.Key("agent.lifecycle."+key), "")
}

func lifecycleText(key string) string {
	return LocalizeText(appLocale(os.UserHomeDir, os.Getenv), i18n.Key("agent.lifecycle."+key), "")
}

// Existing notices are best effort; a new disclosure never changes ownership,
// exit status, or failure recovery. Zero confirmed nonapplications adds no line.
func writeAgentSettingsNotice(w io.Writer, a *agentSettingsApplication) {
	if a == nil || a.NotAppliedCount == 0 {
		return
	}
	delivered := 0
	var missing []string
	for _, e := range a.Entries {
		if e.Status == "delivered" {
			delivered++
		}
		if e.Status == "not-applied" {
			missing = append(missing, fmt.Sprintf(lifecycleText("missing"), lifecycleText("setting."+e.Setting), lifecycleText("reason."+e.Reason), lifecycleText("next."+e.Reason)))
		}
	}
	fmt.Fprintf(w, lifecycleText("summary")+"\n", delivered, a.NotAppliedCount, strings.Join(missing, "; "))
}

func trailingAgentPromptFlag(fs *flag.FlagSet, prompt []string) string {
	if len(prompt) == 0 {
		return ""
	}
	token := prompt[len(prompt)-1]
	if !strings.HasPrefix(token, "-") || token == "-" || token == "--" {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimLeft(token, "-"), "=")
	if fs.Lookup(name) == nil {
		return ""
	}
	return token
}
func writeAgentLifecyclePromptNotice(w io.Writer, token string) {
	if token != "" {
		fmt.Fprintf(w, lifecycleText("prompt")+"\n", token)
	}
}

type agentLifecycleNotices struct {
	trailingPromptFlag string
	foreground         bool
}

func (n agentLifecycleNotices) write(w io.Writer) {
	writeAgentLifecyclePromptNotice(w, n.trailingPromptFlag)
	if n.foreground {
		fmt.Fprintln(w, lifecycleText("foreground"))
	}
}
func (c *agentCommand) lifecycleNotices(fs *flag.FlagSet, prompt []string) agentLifecycleNotices {
	n := agentLifecycleNotices{trailingPromptFlag: trailingAgentPromptFlag(fs, prompt)}
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "host" || f.Name == "detach" {
			explicit = true
		}
	})
	n.foreground = c.defaultProcessForegroundNotice(!explicit)
	return n
}
func (c *agentCommand) defaultProcessForegroundNotice(eligible bool) bool {
	if !eligible || c.rebind == nil || c.rebind.create == nil {
		return false
	}
	creator := c.rebind.create
	paths, err := savedSettingsPaths(creator.homeDir, creator.lookupEnv)
	if err != nil {
		return false
	}
	host, err := config.LoadAgentDefaultHostFile(paths.AgentDefaultHostFile())
	return err == nil && host == config.AgentHostProcess
}

// Preserve the selected mode's display suppression, including new warnings.
func (n agentLifecycleNotices) writeForMode(w io.Writer, mode cli.OutputMode) {
	if mode != cli.OutputModeNone {
		n.write(w)
	}
}

func processResumeSettingsApplication(candidate processResumeCandidate, settings agentSettingsLaunch, command processhost.Command, frozen *deferredLaunchRecord) *agentSettingsApplication {
	if frozen != nil {
		return frozenAgentSettingsApplication(frozen, false)
	}
	return processAgentSettingsApplication(candidate.Record.Provider, settings, command, candidate.Agent.Metadata.Annotations)
}

func writeRelaunchLifecycleNotices(w io.Writer, request agentRelaunchRequest) {
	agentLifecycleNotices{trailingPromptFlag: request.trailingPromptFlag, foreground: request.foregroundNotice && !request.detach && request.host == ""}.write(w)
}
