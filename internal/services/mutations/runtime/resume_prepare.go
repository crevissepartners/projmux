package runtime

import (
	"cmp"
	"errors"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

type AgentResumePreparation struct {
	Dialogue        func(coremetadata.AgentWorkspace, string) (string, []string, error)
	Plan            func(string, coremetadata.AgentWorkspace, string, map[string]string) (AgentResumeLaunch, error)
	PlanWithModel   func(string, coremetadata.AgentWorkspace, string, map[string]string, string) (AgentResumeLaunch, error)
	PlanLinks       func(string, coremetadata.Project, map[string]string) ProjectLinksLaunch
	PlanGuidance    func(string, map[string]string) AgentGuidanceLaunch
	ResolveSettings func(string, map[string]string, AgentSettingsRequest) (AgentSettingsLaunch, error)
}
type PreparedAgentResume struct {
	Title    string
	Argv     []string
	Resumed  AgentResumeLaunch
	Settings AgentSettingsLaunch
	Links    ProjectLinksLaunch
	Guidance AgentGuidanceLaunch
	Notices  []string
}

func (r AgentResumePreparation) Prepare(spelling string, plan AgentResumePlan) (PreparedAgentResume, error) {
	var Title string
	var launchArgv []string
	var PersonaNotice, EffortNotice, linksNotice, guidanceNotice, settingsNotice string
	var resumed AgentResumeLaunch
	var settings AgentSettingsLaunch
	var links ProjectLinksLaunch
	var guidance AgentGuidanceLaunch
	var err error
	Workspace := plan.Workspace
	if Workspace.CWD == "" {
		Workspace.CWD = plan.ProjectRoot
	}
	if plan.DialogueReplyOnly && plan.Annotations[coremetadata.AnnotationAgentProfile] != "" {
		// The reply-only launch is fixed and cannot carry a profile's
		// permissions, so it is refused rather than resumed without them.
		err = &ProfileResumeError{Name: plan.Annotations[coremetadata.AnnotationAgentProfile], Reason: profileReasonLaneUnsupported,
			Detail: "the reply-only activation cannot carry a profile"}
	} else if plan.DialogueReplyOnly {
		launcher, ok := r.Dialogue, r.Dialogue != nil
		if !ok {
			return PreparedAgentResume{}, errors.New("claude reply-only resume launcher is unavailable")
		}
		Title, launchArgv, err = launcher(Workspace, plan.ConversationID)
	} else {
		// The Project's current label link rules are read from the Project
		// that owns the Agent's Window. Rules that differ from the recorded
		// digest launch with the snapshot off, and the transaction below
		// records exactly the digest and mode this launch reads.
		links = r.PlanLinks(plan.Provider, plan.Project, plan.Annotations)
		// The agent guidance is compared with its recorded digest the same
		// way, and launched and recorded the same way.
		guidance = r.PlanGuidance(plan.Provider, plan.Annotations)
		// The settings are resolved from the layers. A profile that is
		// gone or invalid leaves them unlayered, so the seam below
		// refuses the resume exactly as it did before layers existed.
		if resolved, resolveErr := r.ResolveSettings(plan.Provider, plan.Annotations, plan.SettingsRequest().WithPromptParts(guidance, links)); resolveErr == nil {
			settings = resolved.WriteSnapshot()
		} else if plan.LayerChanges.ChangesLayers() || profile.ReasonOf(resolveErr) == profile.ReasonOutOfScope {
			// A change to the layers is never launched without them.
			err = resolveErr
		}
		LaunchAnnotations := guidance.ResumeLaunchAnnotations(links.ResumeLaunchAnnotations(settings.LaunchAnnotations(plan.LaunchAnnotations())))
		switch model := settings.Model(plan.ModelOverride); {
		case err != nil:
		case settings.SnapshotErr != nil:
			err = settings.SnapshotErr
		case model != "":
			launcher, ok := r.PlanWithModel, r.PlanWithModel != nil
			if !ok {
				return PreparedAgentResume{}, errors.New(spelling + ": the resume launcher cannot pass --model")
			}
			resumed, err = launcher(plan.Provider, Workspace, plan.ConversationID, LaunchAnnotations, model)
		default:
			resumed, err = r.Plan(plan.Provider, Workspace, plan.ConversationID, LaunchAnnotations)
		}
		Title, launchArgv = resumed.Title, resumed.Argv
		PersonaNotice, EffortNotice = resumed.PersonaNotice(plan.AgentName), resumed.EffortNotice(plan.AgentName)
		linksNotice = cmp.Or(links.Notice(plan.AgentName), resumed.ProjectLinksNotice(plan.AgentName))
		guidanceNotice = cmp.Or(guidance.Notice(plan.AgentName), resumed.AgentGuidanceNotice(plan.AgentName))
		settingsNotice = settings.Notice(plan.AgentName)
	}

	if err != nil {
		return PreparedAgentResume{}, fmt.Errorf("%s: agent/%s cannot resume %s conversation %s: %w", spelling, plan.AgentName, plan.Provider, plan.ConversationID, err)
	}
	out := PreparedAgentResume{Title: Title, Argv: launchArgv, Resumed: resumed, Settings: settings, Links: links, Guidance: guidance}
	for _, n := range []string{PersonaNotice, EffortNotice, linksNotice, guidanceNotice, settingsNotice} {
		if n != "" {
			out.Notices = append(out.Notices, n)
		}
	}
	return out, nil
}
func (p PreparedAgentResume) Record(reg *coremetadata.Registry, mut coremetadata.Mutator, uid string) error {
	if err := p.Settings.Record(reg, mut, uid); err != nil {
		return err
	}
	if p.Resumed.ProfileName != "" {
		if _, err := mut.SetAgentProfileDigest(reg, uid, p.Resumed.ProfileName, p.Resumed.ProfileDigest); err != nil {
			return MapMetadataError(err)
		}
	}
	if err := p.Links.Record(reg, mut, uid); err != nil {
		return err
	}
	return p.Guidance.Record(reg, mut, uid)
}
