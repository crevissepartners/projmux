package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
)

// Invocation-only disclosure, never persisted. The CLI and the public SDK
// both report it from PreparedAgentSettingsApplication; the SDK copies it
// into its own public type. A value pointer preserves explicit none.
type AgentSettingsApplication struct {
	Entries         []AgentSettingApplication `json:"entries"`
	NotAppliedCount int                       `json:"notAppliedCount"`
}
type AgentSettingApplication struct {
	Setting        string  `json:"setting"`
	Status         string  `json:"status"`
	Reason         string  `json:"reason"`
	Value          *string `json:"value,omitempty"`
	RequestedValue *string `json:"requestedValue,omitempty"`
	NextAction     string  `json:"nextAction,omitempty"`
}

type AgentApplicationSource struct {
	ReplyOnly      bool
	NativeReady    bool
	Provider, Host string
	ModelArg       string
	Settings       AgentSettingsLaunch
	Resumed        AgentResumeLaunch
	Guidance       AgentGuidanceLaunch
	Links          ProjectLinksLaunch
	Workspace      coremetadata.AgentWorkspace
	Annotations    map[string]string
	Snapshot       bool // process ordinary resume consumes recorded guidance/rules.
}

// Called only after launch preparation/commit succeeds, with the actual values
// of that preparation, never with a restart preflight substituted for them.
func PreparedAgentSettingsApplication(s AgentApplicationSource) *AgentSettingsApplication {
	a := &AgentSettingsApplication{Entries: make([]AgentSettingApplication, 0, 8)}
	add := func(setting, status, reason string, value *string) {
		a.Entries = append(a.Entries, AgentSettingApplication{Setting: setting, Status: status, Reason: reason, Value: value})
	}
	value := func(s string) *string { return &s }
	r := s.Settings.Resolution
	if s.ReplyOnly {
		for _, setting := range []string{"profile", "model", "effort", "instructions", "agent-guidance", "project-guidance", "link-rules"} {
			add(setting, "unobserved", "application-unobserved", nil)
		}
		add("workspace", "delivered", "workspace-resolved", value(s.Workspace.CWD))
		return a
	}
	if s.Settings.Layered {
		add("profile", "recorded", "profile-recipe-recorded", value(r.New.Profile.Name))
	} else if s.Resumed.ProfileName != "" {
		add("profile", "recorded", "profile-recipe-recorded", value(s.Resumed.ProfileName))
	} else {
		add("profile", "unobserved", "application-unobserved", nil)
	}
	reason := "launch-arguments"
	if s.Provider == aiModeCodex && (s.Host == "process" || s.NativeReady) {
		reason = "thread-settings-ready"
	}
	if s.Settings.Layered && r.New.Model.Value != "" && (s.Host == "process" || r.PassModel) {
		add("model", "delivered", reason, value(r.New.Model.Value))
	} else if !s.Settings.Layered && s.ModelArg != "" {
		add("model", "delivered", reason, value(s.ModelArg))
	} else if s.Host == "process" {
		add("model", "unobserved", "application-unobserved", nil)
	} else {
		add("model", "preserved", "conversation-model-preserved", nil)
	}
	effort, _, _ := ClaudeResumeEffort(s.Provider, s.Settings.LaunchAnnotations(s.Annotations))
	if s.Resumed.EffortSkipped {
		add("effort", "not-applied", "effort-invalid", nil)
		a.Entries[len(a.Entries)-1].RequestedValue = value(s.Resumed.EffortInvalid)
	} else if effort != "" {
		add("effort", "delivered", reason, value(effort))
	} else {
		add("effort", "unobserved", "application-unobserved", nil)
	}
	instructions, wantedInstructions := r.New.Instructions.Value, r.WantedInstructions
	if !s.Settings.Layered {
		instructions = s.Annotations[coremetadata.AnnotationAgentPersona]
		wantedInstructions = instructions
	}
	managed := s.Settings.ManagedBase
	managedStatus, managedReason := "delivered", "managed-base-delivered"
	if managed != nil && (managed.Fallback || managed.Instructions.Fallback || managed.Instructions.Shared) {
		managedStatus, managedReason = "not-applied", "managed-base-not-refreshed"
	}
	switch {
	case managed != nil:
		if managedStatus == "not-applied" {
			prior := managed.PreviousAnnotations[coremetadata.AnnotationAgentPersona]
			add("instructions", managedStatus, managedReason, value(prior))
			if wantedInstructions != prior {
				a.Entries[len(a.Entries)-1].RequestedValue = value(wantedInstructions)
			}
		} else {
			add("instructions", managedStatus, managedReason, value(instructions))
		}
	case r.InstructionsNotApplied:
		add("instructions", "not-applied", "codex-instructions-immutable", value(instructions))
		if wantedInstructions != instructions {
			a.Entries[len(a.Entries)-1].RequestedValue = value(wantedInstructions)
		}
	case r.InstructionsUnavailable != "" || s.Settings.InstructionsErr != nil || s.Settings.SnapshotErr != nil || s.Resumed.PersonaUnavailable != nil:
		add("instructions", "not-applied", "instructions-unavailable", nil)
		a.Entries[len(a.Entries)-1].RequestedValue = value(wantedInstructions)
	case s.Provider == aiModeCodex:
		add("instructions", "preserved", "codex-instructions-immutable", value(instructions))
	default:
		add("instructions", "delivered", "launch-arguments", value(instructions))
	}
	for _, part := range []struct {
		setting, reason     string
		active, unavailable bool
	}{
		{"agent-guidance", "guidance-unavailable", s.Guidance.Active || s.Snapshot, s.Guidance.Unavailable != nil || s.Resumed.AgentGuidanceUnavailable != nil},
		{"project-guidance", "project-guidance-unavailable", s.Links.Project.Active || s.Settings.ProjectGuidance.Active, s.Links.Project.Unavailable != nil || s.Settings.ProjectGuidance.Unavailable != nil},
		{"link-rules", "link-rules-unavailable", s.Links.Active || s.Snapshot, s.Links.Unavailable != nil || s.Resumed.ProjectLinksUnavailable != nil},
	} {
		switch {
		case managed != nil:
			add(part.setting, managedStatus, managedReason, nil)
		case s.Provider == aiModeCodex:
			add(part.setting, "preserved", "codex-instructions-immutable", nil)
		case part.unavailable:
			add(part.setting, "not-applied", part.reason, nil)
		case s.Snapshot && part.setting != "project-guidance":
			add(part.setting, "preserved", "recorded-snapshot", nil)
		case part.active:
			add(part.setting, "delivered", "launch-arguments", nil)
		default:
			add(part.setting, "unobserved", "application-unobserved", nil)
		}
	}
	add("workspace", "delivered", "workspace-resolved", value(s.Workspace.CWD))
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

// NextAction is the fallback-locale text, independent of the caller's locale.
func lifecycleFallbackText(key string) string {
	text, err := i18n.NewLocalizer(i18n.FallbackLocale).Text(i18n.Key("agent.lifecycle." + key))
	if err != nil {
		return ""
	}
	return text.String()
}
