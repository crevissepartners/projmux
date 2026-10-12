package runtime

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// ProfileResumeError is a resume refused because the profile the Agent
// records cannot be applied again.
type ProfileResumeError struct {
	Name   string
	Reason string
	Detail string
}

func (e *ProfileResumeError) Error() string {
	return fmt.Sprintf("%s: profile %q cannot be applied again (%s): %s", profileReasonResumeUnavailable, e.Name, e.Reason, e.Detail)
}

// resolveRecordedProfile re-reads, by name, the profile an Agent records, the
// way a create resolves an explicit --profile. A profile that is gone or that
// `profile list` would mark invalid -- a role-claimed one included -- is a
// profile-resume-unavailable refusal carrying the store's own reason.
func ResolveRecordedProfile(paths config.Paths, name string, projectUID ...string) (config.Paths, profile.Profile, profile.Spec, error) {
	loaded, spec, err := profile.NewDefaultStore(paths).Resolve(name)
	if err != nil {
		reason := profile.ReasonOf(err)
		if reason == "" {
			reason = profile.ReasonValueInvalid
		}
		return config.Paths{}, profile.Profile{}, profile.Spec{}, &ProfileResumeError{Name: name, Reason: reason, Detail: err.Error()}
	}
	if len(projectUID) != 0 {
		if err := profile.CheckProject(name, spec, projectUID[0]); err != nil {
			return config.Paths{}, profile.Profile{}, profile.Spec{}, err
		}
	}
	return paths, loaded, spec, nil
}

// resumeProfileSettings re-reads the profile a resumed Agent records, by
// name, on the provider-CLI resume lane.
//
// For Claude it writes the settings snapshot of the profile's current
// permissions and returns the profile name, the digest of the content
// applied, and the snapshot path ("" when the profile has no allow or deny
// rule).
//
// For Codex this lane is `codex resume <id>`, including topology replay and
// rollout picker launches. It carries the current sandbox and approval as
// -s and -a. allow and deny have no CLI counterpart and remain unapplied.
//
// An Agent without the annotation, and any other provider, get nothing, so
// their resume argv stays byte-identical. A profile that is gone or invalid
// is a profile-resume-unavailable refusal.
func (c *ResumeLaunchService) ResumeProfileSettings(mode string, annotations map[string]string, projectUID ...string) (name, digest, settings string, policy codexappserver.ThreadPolicy, err error) {
	name = annotations[coremetadata.AnnotationAgentProfile]
	if name == "" || (mode != aiModeClaude && mode != aiModeCodex) {
		return "", "", "", codexappserver.ThreadPolicy{}, nil
	}
	paths, loaded, spec, err := ResolveRecordedProfileWithPaths(c.Paths, name, projectUID...)
	if err != nil {
		return "", "", "", codexappserver.ThreadPolicy{}, err
	}
	if mode == aiModeCodex {
		policy, err = CodexThreadPolicy(spec.Permissions)
		if err != nil {
			return "", "", "", codexappserver.ThreadPolicy{}, &ProfileResumeError{Name: name, Reason: profile.ReasonValueInvalid, Detail: err.Error()}
		}
		if policy.ApprovalPolicy == codexappserver.ApprovalUntrusted {
			return "", "", "", codexappserver.ThreadPolicy{}, &ProfileResumeError{Name: name, Reason: profileReasonCodexCLIUntrusted,
				Detail: "approval=untrusted cannot be applied by codex resume"}
		}
		return loaded.Name, loaded.Digest, "", policy, nil
	}
	settings, err = profile.WriteSettingsSnapshot(paths.StateDir, spec.Permissions)
	if err != nil {
		return "", "", "", codexappserver.ThreadPolicy{}, &ProfileResumeError{Name: name, Reason: profile.ReasonValueInvalid, Detail: err.Error()}
	}
	return loaded.Name, loaded.Digest, settings, codexappserver.ThreadPolicy{}, nil
}

func ResolveRecordedProfileWithPaths(paths func() (config.Paths, error), name string, projectUID ...string) (config.Paths, profile.Profile, profile.Spec, error) {
	resolved, err := paths()
	if err != nil {
		return config.Paths{}, profile.Profile{}, profile.Spec{}, &ProfileResumeError{Name: name, Reason: profile.ReasonNotFound, Detail: "the profile cannot be located: " + err.Error()}
	}
	return ResolveRecordedProfile(resolved, name, projectUID...)
}
