package app

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// Reason tokens of applying a named profile to an Agent. They are stable
// strings: each refusal and each not-applied disclosure carries exactly one.
const (
	// profileReasonClaudeSandboxBashOnly is a profile `sandbox` a Claude Agent
	// is not given. Claude's sandbox confines only Bash subprocesses and falls
	// back to running unsandboxed, so it cannot stand for the profile's
	// sandbox.
	profileReasonClaudeSandboxBashOnly = "claude-sandbox-bash-only"
	// profileReasonClaudeNoPermissionMode is a profile `approval` a Claude
	// Agent is not given: no Claude permission mode has the approval
	// semantics the profile vocabulary names.
	profileReasonClaudeNoPermissionMode = "claude-no-matching-permission-mode"
	// profileReasonOverriddenByFlag is a profile item an explicit create flag
	// replaced: --instructions/--persona, --model, or --effort.
	profileReasonOverriddenByFlag = "overridden-by-flag"
	// profileReasonProviderOptionUnsupported is a profile model or effort on a
	// provider that takes neither.
	profileReasonProviderOptionUnsupported = "provider-option-unsupported"
	// profileReasonPermissionsUnsupported refuses a profile with any
	// permission on a provider that cannot apply permissions.
	profileReasonPermissionsUnsupported = "profile-permissions-unsupported-provider"
	// profileReasonCodexCLIUntrusted refuses a Codex CLI launch because this
	// Codex version accepts untrusted only on the native app-server lane.
	profileReasonCodexCLIUntrusted = "codex-cli-untrusted-approval-unsupported"
	// profileReasonCodexCommandRulesUnsupported is a profile `allow` or
	// `deny` a Codex Agent is not given. They are Claude permission rules;
	// Codex has no per-thread rule list a create could hand them to, and the
	// profile's sandbox and approval are what bound a Codex thread.
	profileReasonCodexCommandRulesUnsupported = "codex-command-rules-unsupported"
	// profileReasonLaneUnsupported refuses a profile on the reply-only
	// activation, whose launch is fixed.
	profileReasonLaneUnsupported = "profile-lane-unsupported"
	// profileReasonProviderMismatch refuses a create whose provider is not
	// the one the profile names. A provider-neutral profile never mismatches.
	profileReasonProviderMismatch = "profile-provider-mismatch"
	// profileReasonResumeUnavailable refuses a resume of an Agent whose
	// recorded profile cannot be applied again. There is no fallback that
	// resumes it without the profile's permissions.
	profileReasonResumeUnavailable = "profile-resume-unavailable"
)

// profileRoleLabel is the creation label a profile's `roles` are matched
// against when no --profile was spelled.
const profileRoleLabel = "role"

// Profile item names, as disclosed.
const (
	profileItemInstructions = "instructions"
	profileItemModel        = "model"
	profileItemEffort       = "effort"
	profileItemSandbox      = "permissions.sandbox"
	profileItemApproval     = "permissions.approval"
	profileItemAllow        = "permissions.allow"
	profileItemDeny         = "permissions.deny"
)





























// writeProfileDisclosure writes the profile disclosure lines of a create whose
// projection has no room for them (pane-id, and the resource projections) to
// w, so a profile item that was not applied is never silent.
func writeProfileDisclosure(w io.Writer, disclosure *cli.ReceiptProfile) error {
	if w == nil {
		return nil
	}
	for _, line := range disclosure.HumanLines() {
		if _, err := fmt.Fprintln(w, "projmux: "+line); err != nil {
			return err
		}
	}
	return nil
}

// profileResumeError is a resume refused because the profile the Agent
// records cannot be applied again.
type profileResumeError struct {
	name   string
	reason string
	detail string
}

func (e *profileResumeError) Error() string {
	return fmt.Sprintf("%s: profile %q cannot be applied again (%s): %s", profileReasonResumeUnavailable, e.name, e.reason, e.detail)
}

// resolveRecordedProfile re-reads, by name, the profile an Agent records, the
// way a create resolves an explicit --profile. A profile that is gone or that
// `profile list` would mark invalid -- a role-claimed one included -- is a
// profile-resume-unavailable refusal carrying the store's own reason.
func resolveRecordedProfile(homeDir func() (string, error), lookupEnv func(string) string, name string, projectUID ...string) (config.Paths, profile.Profile, profile.Spec, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.Paths{}, profile.Profile{}, profile.Spec{}, &profileResumeError{name: name, reason: profile.ReasonNotFound,
			detail: "the profile cannot be located: " + err.Error()}
	}
	loaded, spec, err := profile.NewDefaultStore(paths).Resolve(name)
	if err != nil {
		reason := profile.ReasonOf(err)
		if reason == "" {
			reason = profile.ReasonValueInvalid
		}
		return config.Paths{}, profile.Profile{}, profile.Spec{}, &profileResumeError{name: name, reason: reason, detail: err.Error()}
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
func (c *aiCommand) resumeProfileSettings(mode string, annotations map[string]string, projectUID ...string) (name, digest, settings string, policy codexappserver.ThreadPolicy, err error) {
	name = annotations[coremetadata.AnnotationAgentProfile]
	if name == "" || (mode != aiModeClaude && mode != aiModeCodex) {
		return "", "", "", codexappserver.ThreadPolicy{}, nil
	}
	paths, loaded, spec, err := resolveRecordedProfile(c.homeDir, c.lookupEnv, name, projectUID...)
	if err != nil {
		return "", "", "", codexappserver.ThreadPolicy{}, err
	}
	if mode == aiModeCodex {
		policy, err = codexThreadPolicy(spec.Permissions)
		if err != nil {
			return "", "", "", codexappserver.ThreadPolicy{}, &profileResumeError{name: name, reason: profile.ReasonValueInvalid, detail: err.Error()}
		}
		if policy.ApprovalPolicy == codexappserver.ApprovalUntrusted {
			return "", "", "", codexappserver.ThreadPolicy{}, &profileResumeError{name: name, reason: profileReasonCodexCLIUntrusted,
				detail: "approval=untrusted cannot be applied by codex resume"}
		}
		return loaded.Name, loaded.Digest, "", policy, nil
	}
	settings, err = profile.WriteSettingsSnapshot(paths.StateDir, spec.Permissions)
	if err != nil {
		return "", "", "", codexappserver.ThreadPolicy{}, &profileResumeError{name: name, reason: profile.ReasonValueInvalid, detail: err.Error()}
	}
	return loaded.Name, loaded.Digest, settings, codexappserver.ThreadPolicy{}, nil
}

// codexResumeProfile re-reads, by name, the profile a natively resumed Codex
// Agent records and returns the thread policy its current content asks for,
// together with a launch naming the profile and the digest of that content,
// which the caller records once the resume commits. Like the Claude resume it
// applies the profile as it is now, not as it was at creation: an edited
// sandbox or approval reaches the thread on its next cold resume.
//
// An Agent without the annotation gets the zero policy and an empty launch,
// so its thread/resume request stays byte-identical. A profile that is gone or
// invalid refuses with profile-resume-unavailable; there is no resume that
// drops its permissions.
func (c *createCommand) codexResumeProfile(annotations map[string]string, projectUID ...string) (agentResumeLaunch, codexappserver.ThreadPolicy, error) {
	name := annotations[coremetadata.AnnotationAgentProfile]
	if name == "" {
		return agentResumeLaunch{}, codexappserver.ThreadPolicy{}, nil
	}
	_, loaded, spec, err := resolveRecordedProfile(c.homeDir, c.lookupEnv, name, projectUID...)
	if err != nil {
		return agentResumeLaunch{}, codexappserver.ThreadPolicy{}, err
	}
	policy, err := codexThreadPolicy(spec.Permissions)
	if err != nil {
		return agentResumeLaunch{}, codexappserver.ThreadPolicy{}, &profileResumeError{name: name, reason: profile.ReasonValueInvalid, detail: err.Error()}
	}
	return agentResumeLaunch{profileName: loaded.Name, profileDigest: loaded.Digest}, policy, nil
}

// withResumedProfileDigest returns annotations with the profile digest the
// resume launch applied, or annotations itself when it applied no profile.
// It never writes into annotations.
func withResumedProfileDigest(annotations map[string]string, launch agentResumeLaunch) map[string]string {
	if launch.profileName == "" || annotations[coremetadata.AnnotationAgentProfileDigest] == launch.profileDigest {
		return annotations
	}
	out := maps.Clone(annotations)
	out[coremetadata.AnnotationAgentProfileDigest] = launch.profileDigest
	return out
}

// recordResumedProfileDigest updates the Registry digest of the profile a
// resume just applied to agentUID. A launch that applied no profile changes
// nothing.
func recordResumedProfileDigest(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID string, launch agentResumeLaunch) error {
	if launch.profileName == "" {
		return nil
	}
	if _, err := mutator.SetAgentProfileDigest(registry, agentUID, launch.profileName, launch.profileDigest); err != nil {
		return MapMetadataError(err)
	}
	return nil
}

// inheritedResumeProfile is the profile pair a resume-picker create of one
// Claude or Codex conversation inherits from the Agents that already recorded
// it. When every holder records the same profile name, the new Agent records
// that profile and its resume applies it; the digest is refreshed by that
// resume.
// Holders that disagree about the profile refuse the create: picking one, or
// none, could launch the conversation without the permissions it runs with.
// A Codex conversation inherits on the same terms: its native catalog resume
// re-sends the profile's current policy (codexResumeProfile), and its rollout
// resume passes the policy as CLI flags (resumeProfileSettings). Any
// other provider, and a conversation no holder records a profile for,
// inherits nothing.
func inheritedResumeProfile(registry *coremetadata.Registry, provider, conversation string) (map[string]string, error) {
	conversation = strings.TrimSpace(conversation)
	if registry == nil || (provider != aiModeClaude && provider != aiModeCodex) || conversation == "" {
		return nil, nil
	}
	holders := registry.AgentsRecordingConversation(pickerResumeSessionObservation(provider, conversation))
	if len(holders) == 0 {
		return nil, nil
	}
	first := holders[0].Metadata.Annotations
	for _, holder := range holders[1:] {
		if holder.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != first[coremetadata.AnnotationAgentProfile] {
			names := make([]string, 0, len(holders))
			for _, h := range holders {
				names = append(names, fmt.Sprintf("agent/%s (uid:%s)", h.Metadata.Name, h.Metadata.UID))
			}
			return nil, errors.New((&profileResumeError{name: first[coremetadata.AnnotationAgentProfile], reason: launchValuesReasonAmbiguous,
				detail: fmt.Sprintf("%s conversation %s is recorded with different profiles by %s", provider, conversation, strings.Join(names, ", "))}).Error() +
				"; nothing was created")
		}
	}
	name := first[coremetadata.AnnotationAgentProfile]
	if name == "" {
		return nil, nil
	}
	return map[string]string{
		coremetadata.AnnotationAgentProfile:       name,
		coremetadata.AnnotationAgentProfileDigest: first[coremetadata.AnnotationAgentProfileDigest],
	}, nil
}

// profileCreateProject resolves only when a scoped profile requires an owner.
// Global-only creates retain their existing argv-only preflight ordering.
func (c *createCommand) profileCreateProject(spelling string, flags resourceCreateFlags) (string, error) {
	if flags.profileProjectUID != "" {
		return flags.profileProjectUID, nil
	}
	scope, err := c.resolveCreateScope(spelling, flags, resourceCreateShape{split: true, provider: true})
	if err != nil {
		return "", err
	}
	registry, err := c.store.snapshot()
	if err != nil {
		return "", err
	}
	project, err := c.resolveProject(registry, scope)
	if err != nil {
		return "", MapMetadataError(err)
	}
	return project.Metadata.UID, nil
}
