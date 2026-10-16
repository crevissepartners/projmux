package app

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

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
	profileItemSandbox  = "permissions.sandbox"
	profileItemApproval = "permissions.approval"
	profileItemAllow    = "permissions.allow"
	profileItemDeny     = "permissions.deny"
)

// profileLaunch is the profile one Agent create applies: its name, the
// digest of the content applied, the checked content, the Claude settings
// snapshot its allow and deny rules are launched with, the Codex policy its
// sandbox and approval are started with on native or CLI lanes, and items
// not applied. The zero value means no profile.
type profileLaunch struct {
	name        string
	digest      string
	spec        profile.Spec
	settings    string
	codexPolicy codexappserver.ThreadPolicy
	notApplied  []cli.ReceiptProfileItem
	// source is how the create selected the profile
	// (coremetadata.SettingSourceFlag or coremetadata.SettingSourceRole), and
	// filledModel and filledEffort say whether the profile, not a flag, gave
	// the create its model and effort. The instructions say so themselves
	// (personaOption "profile").
	source       string
	filledModel  bool
	filledEffort bool
}

func (p profileLaunch) active() bool { return p.shared().Active() }

// withAnnotations adds the profile pair to base. Without a profile it returns
// base itself, so a create without one stores exactly what it stored before
// profiles existed -- nil included. Otherwise it returns a new map and never
// writes into base.
func (p profileLaunch) withAnnotations(base map[string]string) map[string]string {
	return p.shared().WithAnnotations(base)
}

// receipt is the receipt disclosure of the applied profile, or nil.
func (p profileLaunch) receipt() *cli.ReceiptProfile { return p.shared().Receipt() }

// notices are the one-line disclosures of the items not applied, for a
// surface whose only disclosure channel is a notice line.
func (p profileLaunch) notices() []string { return p.shared().Notices() }

func requireProfileLane(spelling string, flags resourceCreateFlags) error {
	return (mutationruntime.AgentInputFields{}).RequireProfileLane(spelling, flags.profile, flags.dialogueReplyOnly)
}

// selectedCreateProfile is the profile one Agent create selected: its name,
// the option that selected it as the refusals quote it, and what it resolved
// to. The zero value means the create applies no profile.
type selectedCreateProfile struct {
	name   string
	option string
	source string
	loaded profile.Profile
	spec   profile.Spec
}

func (c *createCommand) selectCreateProfile(spelling string, flags resourceCreateFlags) (selectedCreateProfile, error) {
	selected, err := c.sharedAgentSettings(flags).SelectCreateProfile(spelling, flags.sharedAgentSettingsFlags())
	return selectedCreateProfile{name: selected.Name, option: selected.Option, source: selected.Source, loaded: selected.Loaded, spec: selected.Spec}, err
}

func (c *createCommand) resolveCreateProfile(spelling, provider string, flags *resourceCreateFlags) error {
	shared := flags.sharedAgentSettingsFlags()
	err := c.sharedAgentSettings(*flags).ResolveCreateProfile(spelling, provider, &shared)
	flags.persona, flags.personaOption, flags.model, flags.effort = shared.Instructions, shared.InstructionsOption, shared.Model, shared.Effort
	flags.profileLaunch = localProfileLaunch(shared.ProfileLaunch)
	return err
}

func (c *createCommand) prepareProfileSettings(spelling, provider string, flags *resourceCreateFlags) error {
	shared := flags.sharedAgentSettingsFlags()
	err := c.sharedAgentSettings(*flags).PrepareProfileSettings(spelling, provider, &shared)
	flags.profileLaunch = localProfileLaunch(shared.ProfileLaunch)
	return err
}

// claudeSettingsArgs spells the Claude settings snapshot a profile's
// permissions are launched with. Only the path reaches argv.
func claudeSettingsArgs(settingsFile string) []string {
	return mutationruntime.ClaudeSettingsArgs(settingsFile)
}

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

func resolveRecordedProfile(h func() (string, error), e func(string) string, n string, u ...string) (config.Paths, profile.Profile, profile.Spec, error) {
	paths, err := configPaths(h, e)
	if err != nil {
		return config.Paths{}, profile.Profile{}, profile.Spec{}, &profileResumeError{name: n, reason: profile.ReasonNotFound, detail: "the profile cannot be located: " + err.Error()}
	}
	paths, p, s, err := mutationruntime.ResolveRecordedProfile(paths, n, u...)
	return paths, p, s, localResumeError(err)
}

func (c *aiCommand) resumeProfileSettings(m string, a map[string]string, u ...string) (string, string, string, codexappserver.ThreadPolicy, error) {
	n, d, s, p, e := c.resumeLaunchService().ResumeProfileSettings(m, a, u...)
	return n, d, s, p, localResumeError(e)
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
	launch, policy, err := mutationruntime.NativeCodexResumeProfile(c.processCreatePaths, annotations, projectUID...)
	return localAgentResumeLaunch(launch), policy, localResumeError(err)
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

func (p profileLaunch) shared() mutationruntime.ProfileLaunch {
	return mutationruntime.ProfileLaunch{Name: p.name, Digest: p.digest, Spec: p.spec, Settings: p.settings, CodexPolicy: p.codexPolicy, NotApplied: p.notApplied, Source: p.source, FilledModel: p.filledModel, FilledEffort: p.filledEffort}
}
func localProfileLaunch(p mutationruntime.ProfileLaunch) profileLaunch {
	return profileLaunch{name: p.Name, digest: p.Digest, spec: p.Spec, settings: p.Settings, codexPolicy: p.CodexPolicy, notApplied: p.NotApplied, source: p.Source, filledModel: p.FilledModel, filledEffort: p.FilledEffort}
}
func (f resourceCreateFlags) sharedAgentSettingsFlags() mutationruntime.AgentSettingsFlags {
	return mutationruntime.AgentSettingsFlags{Profile: f.profile, ProfileProjectUID: f.profileProjectUID, Instructions: f.persona, InstructionsOption: f.personaOption, Model: f.model, Effort: f.effort, Host: f.host, ResumeConversation: f.resumeConversation, Labels: f.labels, Payload: f.payload, DialogueReplyOnly: f.dialogueReplyOnly, InteractiveOnly: f.interactiveOnly, PersonaLaunch: f.personaLaunch.shared(), ProfileLaunch: f.profileLaunch.shared()}
}
func (c *createCommand) sharedAgentSettings(f resourceCreateFlags) *mutationruntime.AgentSettings {
	return &mutationruntime.AgentSettings{Paths: func() (config.Paths, error) { return c.processCreateSettingsPaths() }, ProfileProject: func(spelling string, _ mutationruntime.AgentSettingsFlags) (string, error) {
		return c.profileCreateProject(spelling, f)
	}}
}
