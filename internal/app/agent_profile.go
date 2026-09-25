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
	// permission on a provider, or a Codex lane, that cannot apply
	// permissions.
	profileReasonPermissionsUnsupported = "profile-permissions-unsupported-provider"
	// profileReasonCodexCommandRulesUnsupported is a profile `allow` or
	// `deny` a Codex Agent is not given. They are Claude permission rules;
	// Codex has no per-thread rule list a create could hand them to, and the
	// profile's sandbox and approval are what bound a Codex thread.
	profileReasonCodexCommandRulesUnsupported = "codex-command-rules-unsupported"
	// profileReasonLaneUnsupported refuses a profile on the reply-only
	// activation, whose launch is fixed.
	profileReasonLaneUnsupported = "profile-lane-unsupported"
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

// profileLaunch is the profile one Agent create applies: its name, the
// digest of the content applied, the checked content, the Claude settings
// snapshot its allow and deny rules are launched with, the Codex thread
// policy its sandbox and approval are started with, and every item that is
// not applied. The zero value means no profile.
type profileLaunch struct {
	name        string
	digest      string
	spec        profile.Spec
	settings    string
	codexPolicy codexappserver.ThreadPolicy
	notApplied  []cli.ReceiptProfileItem
}

func (p profileLaunch) active() bool { return p.name != "" }

// withAnnotations adds the profile pair to base. Without a profile it returns
// base itself, so a create without one stores exactly what it stored before
// profiles existed -- nil included. Otherwise it returns a new map and never
// writes into base.
func (p profileLaunch) withAnnotations(base map[string]string) map[string]string {
	if !p.active() {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	out[coremetadata.AnnotationAgentProfile] = p.name
	out[coremetadata.AnnotationAgentProfileDigest] = p.digest
	return out
}

// receipt is the receipt disclosure of the applied profile, or nil.
func (p profileLaunch) receipt() *cli.ReceiptProfile {
	if !p.active() {
		return nil
	}
	return &cli.ReceiptProfile{Name: p.name, Digest: p.digest, NotApplied: append([]cli.ReceiptProfileItem{}, p.notApplied...)}
}

// notices are the one-line disclosures of the items not applied, for a
// surface whose only disclosure channel is a notice line.
func (p profileLaunch) notices() []string {
	out := make([]string, 0, len(p.notApplied))
	for _, item := range p.notApplied {
		out = append(out, fmt.Sprintf("profile %s: %s not applied on %s (%s)", p.name, item.Item, item.Provider, item.Reason))
	}
	return out
}

func (p *profileLaunch) skip(item, provider, reason string) {
	p.notApplied = append(p.notApplied, cli.ReceiptProfileItem{Item: item, Provider: provider, Reason: reason})
}

// requireProfileLane refuses an explicit --profile on the reply-only
// activation. Like the other argv-only refusals it lands before any file is
// read.
func requireProfileLane(spelling string, flags resourceCreateFlags) error {
	if flags.profile == "" || flags.profile == profile.ReservedName || !flags.dialogueReplyOnly {
		return nil
	}
	return usageError(fmt.Sprintf("%s --profile cannot be combined with --%s (%s); nothing was created",
		spelling, claudeDialogueReplyOnlyFlag, profileReasonLaneUnsupported))
}

// profileStore is the profile store of the create command's home.
func (c *createCommand) profileStore() (profile.Store, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return profile.Store{}, err
	}
	return profile.NewDefaultStore(paths), nil
}

// resolveCreateProfile decides the profile one Agent create applies and merges
// it into flags.
//
// An explicit --profile names it; `none` means no profile and no role
// mapping. A profile `profile list` marks invalid -- a role it shares with
// another profile included -- is refused with that reason. Without --profile, a `role` creation label selects the one valid
// profile listing that role; a role no profile lists selects none, and a role
// several profiles list -- which the store marks invalid with
// profile-role-claimed -- refuses rather than create the Agent without the
// permissions one of them would have given it. The labels are read here, at
// creation, and never again.
//
// An explicit flag wins over the profile item it overlaps: --instructions or
// --persona over `instructions`, --model over `model`, --effort over
// `effort`. A replaced item is disclosed, as is every item the provider does
// not take. Permissions come only from the profile.
//
// Claude applies allow and deny and discloses sandbox and approval. Codex
// applies sandbox and approval only on the native fresh lane
// (nativeCodexFreshCreateRequired), the one Codex create that starts its own
// thread: they become the thread/start policy, and the thread's answer is
// held to them. allow and deny are disclosed there as not applied. Every
// other Codex lane, and every other provider, refuses a profile that sets any
// permission rather than create the Agent without it.
//
// It reads files and writes none: the settings snapshot is written by
// prepareProfileSettings, later, next to the persona snapshot.
func (c *createCommand) resolveCreateProfile(spelling, provider string, flags *resourceCreateFlags) error {
	if flags.profile == profile.ReservedName {
		return nil
	}
	store, err := c.profileStore()
	if err != nil {
		return fmt.Errorf("%s --profile: %w; nothing was created", spelling, err)
	}
	name := flags.profile
	option := "--profile " + flags.profile
	if name == "" {
		labels, err := labelMap(flags.labels)
		if err != nil {
			// The create refuses the label itself, with its own message.
			return nil
		}
		role, ok := labels[profileRoleLabel]
		if !ok {
			return nil
		}
		option = "--label " + profileRoleLabel + "=" + role
		if name, err = roleProfile(store, role); err != nil {
			return usageError(fmt.Sprintf("%s %s: %v; remove the role from all but one of the listed profiles, or pass --profile none to create without a profile; nothing was created",
				spelling, option, err))
		}
		if name == "" {
			return nil
		}
	}
	loaded, spec, err := store.Resolve(name)
	if err != nil {
		if profile.ReasonOf(err) != "" {
			return usageError(fmt.Sprintf("%s %s: %v; nothing was created", spelling, option, err))
		}
		return fmt.Errorf("%s %s: %w; nothing was created", spelling, option, err)
	}
	if flags.dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s %s: profile %q cannot apply to --%s (%s); nothing was created",
			spelling, option, name, claudeDialogueReplyOnlyFlag, profileReasonLaneUnsupported))
	}
	claude := provider == aiModeClaude
	codexNative := nativeCodexFreshCreateRequired(provider, *flags)
	switch {
	case claude, codexNative, !spec.Permissions.HasPermissions():
	case provider == aiModeCodex:
		return usageError(fmt.Sprintf("%s %s: profile %q sets permissions, which --provider %s applies only on a create with a prompt (%s); nothing was created",
			spelling, option, name, provider, profileReasonPermissionsUnsupported))
	default:
		return usageError(fmt.Sprintf("%s %s: profile %q sets permissions, which --provider %s cannot apply (%s); nothing was created",
			spelling, option, name, provider, profileReasonPermissionsUnsupported))
	}
	launch := profileLaunch{name: loaded.Name, digest: loaded.Digest, spec: spec}
	if codexNative {
		if launch.codexPolicy, err = codexThreadPolicy(spec.Permissions); err != nil {
			return usageError(fmt.Sprintf("%s %s: profile %q: %v; nothing was created", spelling, option, name, err))
		}
		if len(spec.Permissions.Allow) > 0 {
			launch.skip(profileItemAllow, provider, profileReasonCodexCommandRulesUnsupported)
		}
		if len(spec.Permissions.Deny) > 0 {
			launch.skip(profileItemDeny, provider, profileReasonCodexCommandRulesUnsupported)
		}
	}
	if spec.Instructions != "" {
		if flags.persona != "" {
			launch.skip(profileItemInstructions, provider, profileReasonOverriddenByFlag)
		} else {
			// The profile's instructions take the persona path --instructions
			// takes: the same lane check, snapshot, and annotations.
			flags.persona, flags.personaOption = spec.Instructions, "profile"
		}
	}
	for _, overlap := range []struct {
		item, value string
		flag        *string
	}{
		{profileItemModel, spec.Model, &flags.model},
		{profileItemEffort, spec.Effort, &flags.effort},
	} {
		switch {
		case overlap.value == "":
		case !claude && provider != aiModeCodex:
			launch.skip(overlap.item, provider, profileReasonProviderOptionUnsupported)
		case *overlap.flag != "":
			launch.skip(overlap.item, provider, profileReasonOverriddenByFlag)
		default:
			*overlap.flag = overlap.value
		}
	}
	if claude && spec.Permissions.Sandbox != "" {
		launch.skip(profileItemSandbox, provider, profileReasonClaudeSandboxBashOnly)
	}
	if claude && spec.Permissions.Approval != "" {
		launch.skip(profileItemApproval, provider, profileReasonClaudeNoPermissionMode)
	}
	flags.profileLaunch = launch
	return nil
}

// codexThreadPolicy spells a profile's sandbox and approval in the Codex
// thread/start and thread/resume vocabulary. The profile says `full-access`
// where Codex says `danger-full-access`; every other value is spelled the
// same. An absent value stays absent, so a profile without either maps to the
// zero policy and the request carries neither key. allow and deny have no
// Codex counterpart and are not part of the policy.
func codexThreadPolicy(perms profile.Permissions) (codexappserver.ThreadPolicy, error) {
	var policy codexappserver.ThreadPolicy
	switch perms.Sandbox {
	case "":
	case "read-only":
		policy.Sandbox = codexappserver.SandboxReadOnly
	case "workspace-write":
		policy.Sandbox = codexappserver.SandboxWorkspaceWrite
	case "full-access":
		policy.Sandbox = codexappserver.SandboxDangerFullAccess
	default:
		return codexappserver.ThreadPolicy{}, fmt.Errorf("sandbox %q has no Codex thread sandbox", perms.Sandbox)
	}
	switch perms.Approval {
	case "":
	case "never":
		policy.ApprovalPolicy = codexappserver.ApprovalNever
	case "on-request":
		policy.ApprovalPolicy = codexappserver.ApprovalOnRequest
	case "untrusted":
		policy.ApprovalPolicy = codexappserver.ApprovalUntrusted
	default:
		return codexappserver.ThreadPolicy{}, fmt.Errorf("approval %q has no Codex approval policy", perms.Approval)
	}
	return policy, nil
}

// roleProfile returns the name of the one valid profile that lists role, ""
// when none does, or a profile-role-claimed refusal when several profiles
// list it.
func roleProfile(store profile.Store, role string) (string, error) {
	entries, err := store.List()
	if err != nil {
		return "", err
	}
	var valid, claimed []string
	for _, entry := range entries {
		if !slices.Contains(entry.Roles, role) {
			continue
		}
		switch {
		case entry.Valid:
			valid = append(valid, entry.Name)
		case entry.Reason == profile.ReasonRoleClaimed:
			claimed = append(claimed, entry.Name)
		}
	}
	if len(claimed) > 0 {
		return "", &profile.Error{Reason: profile.ReasonRoleClaimed,
			Detail: fmt.Sprintf("role %q is listed by more than one profile (%s)", role, strings.Join(claimed, ", "))}
	}
	if len(valid) == 1 {
		return valid[0], nil
	}
	return "", nil
}

// prepareProfileSettings writes the Claude settings snapshot of the applied
// profile's allow and deny rules. Like preparePersonaLaunch it runs before the
// create transaction opens, and a snapshot a later failure leaves behind is
// harmless: it is content addressed.
func (c *createCommand) prepareProfileSettings(spelling, provider string, flags *resourceCreateFlags) error {
	if !flags.profileLaunch.active() || provider != aiModeClaude {
		return nil
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return fmt.Errorf("%s --profile: %w; nothing was created", spelling, err)
	}
	settings, err := profile.WriteSettingsSnapshot(paths.StateDir, flags.profileLaunch.spec.Permissions)
	if err != nil {
		return fmt.Errorf("%s --profile %s: %w; nothing was created", spelling, flags.profileLaunch.name, err)
	}
	flags.profileLaunch.settings = settings
	return nil
}

// claudeSettingsArgs spells the Claude settings snapshot a profile's
// permissions are launched with. Only the path reaches argv.
func claudeSettingsArgs(settingsFile string) []string {
	if settingsFile == "" {
		return nil
	}
	return []string{"--settings", settingsFile}
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

// resolveRecordedProfile re-reads, by name, the profile an Agent records, the
// way a create resolves an explicit --profile. A profile that is gone or that
// `profile list` would mark invalid -- a role-claimed one included -- is a
// profile-resume-unavailable refusal carrying the store's own reason.
func resolveRecordedProfile(homeDir func() (string, error), lookupEnv func(string) string, name string) (config.Paths, profile.Profile, profile.Spec, error) {
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
// For Codex this lane is `codex resume <id>`, which topology replay and a
// rollout row of the resume picker launch. It has no way to carry a sandbox or
// an approval -- only the native lane's thread/resume does -- so a profile
// whose current content sets any permission refuses with profile-lane-
// unsupported rather than resume the Agent without them. A profile without
// permissions resumes exactly as before and records nothing: nothing of it
// is applied here.
//
// An Agent without the annotation, and any other provider, get nothing, so
// their resume argv stays byte-identical. A profile that is gone or invalid
// is a profile-resume-unavailable refusal.
func (c *aiCommand) resumeProfileSettings(mode string, annotations map[string]string) (name, digest, settings string, err error) {
	name = annotations[coremetadata.AnnotationAgentProfile]
	if name == "" || (mode != aiModeClaude && mode != aiModeCodex) {
		return "", "", "", nil
	}
	paths, loaded, spec, err := resolveRecordedProfile(c.homeDir, c.lookupEnv, name)
	if err != nil {
		return "", "", "", err
	}
	if mode == aiModeCodex {
		if spec.Permissions.HasPermissions() {
			return "", "", "", &profileResumeError{name: name, reason: profileReasonLaneUnsupported,
				detail: "the Codex CLI resume lane cannot carry profile permissions; only a native app-server thread can be resumed with them"}
		}
		return "", "", "", nil
	}
	settings, err = profile.WriteSettingsSnapshot(paths.StateDir, spec.Permissions)
	if err != nil {
		return "", "", "", &profileResumeError{name: name, reason: profile.ReasonValueInvalid, detail: err.Error()}
	}
	return loaded.Name, loaded.Digest, settings, nil
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
func (c *createCommand) codexResumeProfile(annotations map[string]string) (agentResumeLaunch, codexappserver.ThreadPolicy, error) {
	name := annotations[coremetadata.AnnotationAgentProfile]
	if name == "" {
		return agentResumeLaunch{}, codexappserver.ThreadPolicy{}, nil
	}
	_, loaded, spec, err := resolveRecordedProfile(c.homeDir, c.lookupEnv, name)
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
// resume refuses a profile with permissions (resumeProfileSettings). Any
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
