package runtime

import (
"context"
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/aiprovider"
"github.com/crevissepartners/projmux/internal/cli"
"github.com/crevissepartners/projmux/internal/config"
"github.com/crevissepartners/projmux/internal/core/profile"
"github.com/crevissepartners/projmux/internal/core/selector"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
"io"
"maps"
"slices"
"strings"
"time"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// profileStore is the profile store of the create command's home. Without
// HOME or an absolute XDG home it returns the missing-HOME reason.
func (c *createCommand) profileStore() (profile.Store, error) {
	paths, err := savedSettingsPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return profile.Store{}, err
	}
	return profile.NewDefaultStore(paths), nil
}

// nativeCodexFreshCreateRequired is the pre-provider lane decision shared by
// public create and every canonical UI intent. A fresh payload-free Codex
// create is deliberately plain: the current installed tuple cannot hand a
// zero-turn thread to an independent TUI durably, so consulting Current,
// Resolve, or Create would already be too late to fall back safely.
//
// Prompted creates retain the native-required contract. Multi-operand payloads
// also stay on that contract and are rejected by nativePrompt instead of being
// silently reinterpreted by this decision.
func nativeCodexFreshCreateRequired(provider string, flags resourceCreateFlags) bool {
	return provider == aiModeCodex && !flags.interactiveOnly && (len(flags.payload) > 0 || flags.host == "process") &&
		strings.TrimSpace(flags.resumeConversation) == ""
}

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

// selectCreateProfile is the one definition of which profile an Agent create
// uses, and it resolves that profile.
//
// An explicit --profile names it; `none` means no profile and no role
// mapping. Without --profile, a `role` creation label selects the one profile
// listing that role; a role no profile lists selects none, and so does a label
// argv the create itself refuses. Every way the selection or the resolution
// fails is a refusal that says nothing was created.
//
// Both resolveCreateProfile and resolveCreateProvider call it, so the profile
// that decides an omitted --provider is the profile the create then applies.
func (c *createCommand) selectCreateProfile(spelling string, flags resourceCreateFlags) (selectedCreateProfile, error) {
	if flags.profile == profile.ReservedName {
		return selectedCreateProfile{}, nil
	}
	name := flags.profile
	option := "--profile " + flags.profile
	source := coremetadata.SettingSourceFlag
	role := ""
	if name == "" {
		labels, err := labelMap(flags.labels)
		if err != nil {
			// The create refuses the label itself, with its own message.
			return selectedCreateProfile{}, nil
		}
		var ok bool
		if role, ok = labels[profileRoleLabel]; !ok {
			return selectedCreateProfile{}, nil
		}
		option = "--label " + profileRoleLabel + "=" + role
		source = coremetadata.SettingSourceRole
	}
	// The store is opened only once a profile has to be read, so a create
	// that selects none never depends on the config home.
	store, err := c.profileStore()
	if err != nil {
		if flags.profile == "" && isMissingHome(err) {
			// A role mapping is a read of saved profiles; without a config
			// home there are none, so no profile claims a role.
			return selectedCreateProfile{}, nil
		}
		return selectedCreateProfile{}, fmt.Errorf("%s --profile: %w; nothing was created", spelling, err)
	}
	if name == "" {
		entries, listErr := store.List()
		if listErr != nil {
			return selectedCreateProfile{}, listErr
		}
		projectUID := flags.profileProjectUID
		for _, entry := range entries {
			if entry.Project != "" && slices.Contains(entry.Roles, role) {
				projectUID, err = c.profileCreateProject(spelling, flags)
				if err != nil {
					return selectedCreateProfile{}, err
				}
				break
			}
		}
		if name, err = store.RoleProfile(role, projectUID); err != nil {
			switch profile.ReasonOf(err) {
			case profile.ReasonRoleClaimed:
				return selectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; remove the role from all but one of the listed profiles, or pass --profile none to create without a profile; nothing was created",
					spelling, option, err))
			case profile.ReasonRoleProfileInvalid:
				return selectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; fix the profile with `projmux profile set <name>`, or pass --profile none to create without a profile; nothing was created",
					spelling, option, err))
			}
			return selectedCreateProfile{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, option, err)
		}
		if name == "" {
			return selectedCreateProfile{}, nil
		}
	}
	loaded, spec, err := store.Resolve(name)
	if err != nil {
		if profile.ReasonOf(err) != "" {
			return selectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; nothing was created", spelling, option, err))
		}
		return selectedCreateProfile{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, option, err)
	}
	if spec.Project != "" {
		projectUID, scopeErr := c.profileCreateProject(spelling, flags)
		if scopeErr != nil {
			return selectedCreateProfile{}, scopeErr
		}
		if err := profile.CheckProject(name, spec, projectUID); err != nil {
			return selectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; nothing was created", spelling, option, err))
		}
	}
	return selectedCreateProfile{name: name, option: option, source: source, loaded: loaded, spec: spec}, nil
}

// resolveCreateProfile decides the profile one Agent create applies and merges
// it into flags.
//
// An explicit --profile names it; `none` means no profile and no role
// mapping. A profile `profile list` marks invalid -- a role it shares with
// another profile included -- is refused with that reason. Without --profile,
// a `role` creation label selects the one profile listing that role when it is
// valid; a role no profile lists selects none. A role several profiles list
// (profile-role-claimed), and a role whose one listing profile is invalid
// (profile-role-profile-invalid), refuse rather than create the Agent without
// the permissions that profile would have given it. The labels are read here,
// at creation, and never again.
//
// A profile that names a provider applies only to that provider: any other
// refuses with profile-provider-mismatch before anything is written. A
// profile without `provider` applies to every provider, as before. A
// canonical create that omitted --provider took the profile's provider in
// resolveCreateProvider, so there the check passes.
//
// An explicit flag wins over the profile item it overlaps: --instructions or
// --persona over `instructions`, --model over `model`, --effort over
// `effort`. A replaced item is disclosed, as is every item the provider does
// not take. Permissions come only from the profile.
//
// Claude applies allow and deny and discloses sandbox and approval. Codex
// applies sandbox and approval on both the native fresh lane and the plain
// CLI lane. The native lane sends a thread/start policy; the CLI lane sends
// -s and -a. allow and deny are disclosed on both lanes as not applied.
// Other providers refuse a profile with permissions.
//
// selectCreateProfile decides which profile that is, the same way it does
// for resolveCreateProvider.
//
// It reads files and writes none: the settings snapshot is written by
// prepareProfileSettings, later, next to the persona snapshot.
func (c *createCommand) resolveCreateProfile(spelling, provider string, flags *resourceCreateFlags) error {
	selected, err := c.selectCreateProfile(spelling, *flags)
	if err != nil || selected.name == "" {
		return err
	}
	name, option, loaded, spec := selected.name, selected.option, selected.loaded, selected.spec
	if spec.Provider != "" && spec.Provider != provider {
		return usageError(fmt.Sprintf("%s %s: profile %q is for provider %s, not %s (%s); create the Agent with provider %s, or pass --profile none to create without a profile; nothing was created",
			spelling, option, name, spec.Provider, provider, profileReasonProviderMismatch, spec.Provider))
	}
	if flags.dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s %s: profile %q cannot apply to --%s (%s); nothing was created",
			spelling, option, name, claudeDialogueReplyOnlyFlag, profileReasonLaneUnsupported))
	}
	claude := provider == aiModeClaude
	codexNative := nativeCodexFreshCreateRequired(provider, *flags)
	switch {
	case claude, provider == aiModeCodex, !spec.Permissions.HasPermissions():
	default:
		return usageError(fmt.Sprintf("%s %s: profile %q sets permissions, which --provider %s cannot apply (%s); nothing was created",
			spelling, option, name, provider, profileReasonPermissionsUnsupported))
	}
	launch := profileLaunch{name: loaded.Name, digest: loaded.Digest, spec: spec, source: selected.source}
	if provider == aiModeCodex {
		if launch.codexPolicy, err = codexThreadPolicy(spec.Permissions); err != nil {
			return usageError(fmt.Sprintf("%s %s: profile %q: %v; nothing was created", spelling, option, name, err))
		}
		if !codexNative && launch.codexPolicy.ApprovalPolicy == codexappserver.ApprovalUntrusted {
			return usageError(fmt.Sprintf("%s %s: profile %q requests approval=untrusted, which the Codex CLI cannot apply (%s); nothing was created",
				spelling, option, name, profileReasonCodexCLIUntrusted))
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
		filled      *bool
	}{
		{profileItemModel, spec.Model, &flags.model, &launch.filledModel},
		{profileItemEffort, spec.Effort, &flags.effort, &launch.filledEffort},
	} {
		switch {
		case overlap.value == "":
		case !claude && provider != aiModeCodex:
			launch.skip(overlap.item, provider, profileReasonProviderOptionUnsupported)
		case *overlap.flag != "":
			launch.skip(overlap.item, provider, profileReasonOverriddenByFlag)
		default:
			*overlap.flag, *overlap.filled = overlap.value, true
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
