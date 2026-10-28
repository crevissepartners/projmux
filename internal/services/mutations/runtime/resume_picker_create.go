package runtime

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// ResumePickerCreate is the new Agent of a resume-picker create that
// continues one recorded Claude, Antigravity or Codex rollout conversation,
// the launch `create_intent.go`'s openIntentAgent plans for the same pick. Its
// Prepare runs inside the create's transaction: the launch values and profile
// pair are inherited from the Agents that record the conversation in the
// working Registry, the settings layers resolve them, and the provider resume
// launch is planned from what they resolved. The Agent records the
// conversation as its session in the same transaction; History holds the
// session history lines to append once the transaction has committed.
type ResumePickerCreate struct {
	Paths                          config.Paths
	Provider, Conversation, Source string
	Launch                         *ResumeLaunchService
	Guidance                       *GuidanceService
	BindPane                       func(context.Context, AgentPaneBinding) error
	History                        []sessionhistory.Record
}

// Prepare plans the resume launch of one picker create. It refuses when the
// Agents recording the conversation disagree about its profile.
func (r *ResumePickerCreate) Prepare(working coremetadata.Registry, project coremetadata.Project, _ coremetadata.AgentWorkspace) (AgentPreparation, error) {
	provider, conversation := r.Provider, strings.TrimSpace(r.Conversation)
	r.History = nil
	var values map[string]string
	var notices []string
	if len(ResumeLaunchValueKeysFor(provider)) > 0 {
		var notice string
		values, notice = InheritedResumeLaunchValues(&working, provider, conversation)
		if notice != "" {
			notices = append(notices, notice)
		}
	}
	inherited, err := InheritedResumeProfile(&working, provider, conversation)
	if err != nil {
		return AgentPreparation{}, errors.New("create agent: " + err.Error())
	}
	if inherited != nil {
		merged := maps.Clone(values)
		if merged == nil {
			merged = make(map[string]string, len(inherited))
		}
		maps.Copy(merged, inherited)
		values = merged
	}
	settings, err := ResolvePickerSettings(func(name, projectUID string) error {
		_, _, _, err := ResolveRecordedProfile(r.Paths, name, projectUID)
		return err
	}, func(annotations map[string]string, request AgentSettingsRequest) (AgentSettingsLaunch, bool, error) {
		settings, err := ResolveAgentSettings(r.Paths, provider, annotations, request)
		return settings, true, err
	}, values, project.Metadata.UID)
	if err != nil {
		return AgentPreparation{}, err
	}
	values = settings.LaunchAnnotations(values)
	var links ProjectLinksLaunch
	var guidance AgentGuidanceLaunch
	if provider == aiModeClaude {
		links = r.Guidance.PlanProjectLinks(provider, project, values)
		values = links.ResumeLaunchAnnotations(values)
		guidance = r.Guidance.PlanAgentGuidance(provider, values)
		values = guidance.ResumeLaunchAnnotations(values)
	}
	var launch AgentResumeLaunch
	declared := ""
	if provider == aiModeCodex && strings.TrimSpace(r.Source) == aisessions.SourceCodexRollout {
		declared = "rollout-catalog-resume"
	}
	return AgentPreparation{
		Plan: func(workspace coremetadata.AgentWorkspace) (string, []string, error) {
			var err error
			launch, err = r.Launch.PlanAgentResume(provider, workspace, conversation, values)
			return launch.Title, launch.Argv, err
		},
		Bind: func(ctx context.Context, paneID string, workspace coremetadata.AgentWorkspace, title string) error {
			return r.BindPane(ctx, AgentPaneBinding{PaneID: paneID, Provider: provider, ContextDir: workspace.CWD, Title: title, ConversationID: conversation, ResumeSource: strings.TrimSpace(r.Source), CodexNativeDeclared: declared})
		},
		Annotations: func(creator map[string]string) map[string]string {
			annotations := maps.Clone(WithInheritedSettingSources(values, values))
			if annotations == nil {
				annotations = map[string]string{}
			}
			maps.Copy(annotations, creator)
			if launch.ProfileName != "" && annotations[coremetadata.AnnotationAgentProfileDigest] != launch.ProfileDigest {
				annotations[coremetadata.AnnotationAgentProfileDigest] = launch.ProfileDigest
			}
			return guidance.WithCreateAnnotation(links.WithCreateAnnotation(annotations))
		},
		Created: func(working *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string) error {
			if err := settings.Record(working, mutator, agentUID); err != nil {
				return err
			}
			recorded, changed, err := mutator.RecordAgentSessionRef(working, agentUID, PickerResumeSessionObservation(provider, conversation))
			if err != nil {
				return MapMetadataError(err)
			}
			if changed {
				if record, ok := sessionhistory.ObservedRecordFor(working, agentUID, recorded.Status.SessionRef); ok {
					r.History = append(r.History, record)
				}
			}
			return nil
		},
		Notices: func(name string) []string {
			out := append([]string(nil), notices...)
			for _, notice := range []string{
				launch.PersonaNotice(name), launch.EffortNotice(name),
				links.Notice(name), launch.ProjectLinksNotice(name),
				guidance.Notice(name), launch.AgentGuidanceNotice(name),
				PickerSettingsNotice(settings, name),
			} {
				if notice != "" {
					out = append(out, notice)
				}
			}
			return out
		},
	}, nil
}

// PickerSettingsNotice is the one settings notice a resume-picker create
// adds: instructions its Codex thread cannot take. Inherited instructions
// whose file cannot be read now launch with the inherited snapshot, and the
// persona notice already discloses what of that snapshot cannot be re-passed,
// so that is not said a second time.
func PickerSettingsNotice(settings AgentSettingsLaunch, name string) string {
	if !settings.Resolution.InstructionsNotApplied {
		return ""
	}
	return settings.Notice(name)
}

// ResumePickerSource reports whether source names a conversation a
// resume-picker create of provider continues without the Codex app-server:
// any Claude or Antigravity source, and a Codex rollout.
func ResumePickerSource(provider, source string) bool {
	switch provider {
	case aiModeClaude, aiModeAntigravity:
		return true
	case aiModeCodex:
		return strings.TrimSpace(source) == aisessions.SourceCodexRollout
	}
	return false
}
