package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
)

// profileStore is the profile store of the create command's home. Without
// HOME or an absolute XDG home it returns the missing-HOME reason.
func (c *AgentSettings) ProfileStore() (profile.Store, error) {
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
func NativeCodexFreshCreateRequired(provider string, flags AgentSettingsFlags) bool {
	return provider == aiModeCodex && !flags.interactiveOnly && (len(flags.payload) > 0 || flags.host == "process") &&
		strings.TrimSpace(flags.resumeConversation) == ""
}

// profileLaunch is the profile one Agent create applies: its name, the
// digest of the content applied, the checked content, the Claude settings
// snapshot its allow and deny rules are launched with, the Codex policy its
// sandbox and approval are started with on native or CLI lanes, and items
// not applied. The zero value means no profile.
type ProfileLaunch struct {
	Name        string
	Digest      string
	Spec        profile.Spec
	Settings    string
	CodexPolicy codexappserver.ThreadPolicy
	NotApplied  []cli.ReceiptProfileItem
	// source is how the create selected the profile
	// (coremetadata.SettingSourceFlag or coremetadata.SettingSourceRole), and
	// filledModel and filledEffort say whether the profile, not a flag, gave
	// the create its model and effort. The instructions say so themselves
	// (personaOption "profile").
	Source       string
	FilledModel  bool
	FilledEffort bool
}

func (p ProfileLaunch) Active() bool { return p.Name != "" }

// withAnnotations adds the profile pair to base. Without a profile it returns
// base itself, so a create without one stores exactly what it stored before
// profiles existed -- nil included. Otherwise it returns a new map and never
// writes into base.
func (p ProfileLaunch) WithAnnotations(base map[string]string) map[string]string {
	if !p.Active() {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	out[coremetadata.AnnotationAgentProfile] = p.Name
	out[coremetadata.AnnotationAgentProfileDigest] = p.Digest
	return out
}

// receipt is the receipt disclosure of the applied profile, or nil.
func (p ProfileLaunch) Receipt() *cli.ReceiptProfile {
	if !p.Active() {
		return nil
	}
	return &cli.ReceiptProfile{Name: p.Name, Digest: p.Digest, NotApplied: append([]cli.ReceiptProfileItem{}, p.NotApplied...)}
}

// notices are the one-line disclosures of the items not applied, for a
// surface whose only disclosure channel is a notice line.
func (p ProfileLaunch) Notices() []string {
	out := make([]string, 0, len(p.NotApplied))
	for _, item := range p.NotApplied {
		out = append(out, fmt.Sprintf("profile %s: %s not applied on %s (%s)", p.Name, item.Item, item.Provider, item.Reason))
	}
	return out
}

func (p *ProfileLaunch) Skip(item, provider, reason string) {
	p.NotApplied = append(p.NotApplied, cli.ReceiptProfileItem{Item: item, Provider: provider, Reason: reason})
}

// selectedCreateProfile is the profile one Agent create selected: its name,
// the option that selected it as the refusals quote it, and what it resolved
// to. The zero value means the create applies no profile.
type SelectedCreateProfile struct {
	Name   string
	Option string
	Source string
	Loaded profile.Profile
	Spec   profile.Spec
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
func (c *AgentSettings) SelectCreateProfile(spelling string, flags AgentSettingsFlags) (SelectedCreateProfile, error) {
	if flags.Profile == profile.ReservedName {
		return SelectedCreateProfile{}, nil
	}
	Name := flags.Profile
	Option := "--profile " + flags.Profile
	Source := coremetadata.SettingSourceFlag
	role := ""
	if Name == "" {
		Labels, err := labelMap(flags.Labels)
		if err != nil {
			// The create refuses the label itself, with its own message.
			return SelectedCreateProfile{}, nil
		}
		var ok bool
		if role, ok = labels[profileRoleLabel]; !ok {
			return SelectedCreateProfile{}, nil
		}
		Option = "--label " + profileRoleLabel + "=" + role
		Source = coremetadata.SettingSourceRole
	}
	// The store is opened only once a profile has to be read, so a create
	// that selects none never depends on the config home.
	store, err := c.ProfileStore()
	if err != nil {
		if flags.Profile == "" && isMissingHome(err) {
			// A role mapping is a read of saved profiles; without a config
			// home there are none, so no profile claims a role.
			return SelectedCreateProfile{}, nil
		}
		return SelectedCreateProfile{}, fmt.Errorf("%s --profile: %w; nothing was created", spelling, err)
	}
	if Name == "" {
		entries, listErr := store.List()
		if listErr != nil {
			return SelectedCreateProfile{}, listErr
		}
		projectUID := flags.ProfileProjectUID
		for _, entry := range entries {
			if entry.Project != "" && slices.Contains(entry.Roles, role) {
				projectUID, err = c.profileCreateProject(spelling, flags)
				if err != nil {
					return SelectedCreateProfile{}, err
				}
				break
			}
		}
		if Name, err = store.RoleProfile(role, projectUID); err != nil {
			switch profile.ReasonOf(err) {
			case profile.ReasonRoleClaimed:
				return SelectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; remove the role from all but one of the listed profiles, or pass --profile none to create without a profile; nothing was created",
					spelling, Option, err))
			case profile.ReasonRoleProfileInvalid:
				return SelectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; fix the profile with `projmux profile set <name>`, or pass --profile none to create without a profile; nothing was created",
					spelling, Option, err))
			}
			return SelectedCreateProfile{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, Option, err)
		}
		if Name == "" {
			return SelectedCreateProfile{}, nil
		}
	}
	Loaded, Spec, err := store.Resolve(Name)
	if err != nil {
		if profile.ReasonOf(err) != "" {
			return SelectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; nothing was created", spelling, Option, err))
		}
		return SelectedCreateProfile{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, Option, err)
	}
	if Spec.Project != "" {
		projectUID, scopeErr := c.profileCreateProject(spelling, flags)
		if scopeErr != nil {
			return SelectedCreateProfile{}, scopeErr
		}
		if err := profile.CheckProject(Name, Spec, projectUID); err != nil {
			return SelectedCreateProfile{}, usageError(fmt.Sprintf("%s %s: %v; nothing was created", spelling, Option, err))
		}
	}
	return SelectedCreateProfile{Name: Name, Option: Option, Source: Source, Loaded: Loaded, Spec: Spec}, nil
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
func (c *AgentSettings) ResolveCreateProfile(spelling, provider string, flags *AgentSettingsFlags) error {
	selected, err := c.SelectCreateProfile(spelling, *flags)
	if err != nil || selected.Name == "" {
		return err
	}
	Name, Option, Loaded, Spec := selected.Name, selected.Option, selected.Loaded, selected.Spec
	if Spec.Provider != "" && Spec.Provider != provider {
		return usageError(fmt.Sprintf("%s %s: profile %q is for provider %s, not %s (%s); create the Agent with provider %s, or pass --profile none to create without a profile; nothing was created",
			spelling, Option, Name, Spec.Provider, provider, profileReasonProviderMismatch, Spec.Provider))
	}
	if flags.DialogueReplyOnly {
		return usageError(fmt.Sprintf("%s %s: profile %q cannot apply to --%s (%s); nothing was created",
			spelling, Option, Name, claudeDialogueReplyOnlyFlag, profileReasonLaneUnsupported))
	}
	claude := provider == aiModeClaude
	codexNative := NativeCodexFreshCreateRequired(provider, *flags)
	switch {
	case claude, provider == aiModeCodex, !Spec.Permissions.HasPermissions():
	default:
		return usageError(fmt.Sprintf("%s %s: profile %q sets permissions, which --provider %s cannot apply (%s); nothing was created",
			spelling, Option, Name, provider, profileReasonPermissionsUnsupported))
	}
	launch := ProfileLaunch{Name: Loaded.Name, Digest: Loaded.Digest, Spec: Spec, Source: selected.Source}
	if provider == aiModeCodex {
		if launch.CodexPolicy, err = CodexThreadPolicy(Spec.Permissions); err != nil {
			return usageError(fmt.Sprintf("%s %s: profile %q: %v; nothing was created", spelling, Option, Name, err))
		}
		if !codexNative && launch.CodexPolicy.ApprovalPolicy == codexappserver.ApprovalUntrusted {
			return usageError(fmt.Sprintf("%s %s: profile %q requests approval=untrusted, which the Codex CLI cannot apply (%s); nothing was created",
				spelling, Option, Name, profileReasonCodexCLIUntrusted))
		}
		if len(Spec.Permissions.Allow) > 0 {
			launch.Skip(profileItemAllow, provider, profileReasonCodexCommandRulesUnsupported)
		}
		if len(Spec.Permissions.Deny) > 0 {
			launch.Skip(profileItemDeny, provider, profileReasonCodexCommandRulesUnsupported)
		}
	}
	if Spec.Instructions != "" {
		if flags.Instructions != "" {
			launch.Skip(profileItemInstructions, provider, profileReasonOverriddenByFlag)
		} else {
			// The profile's instructions take the persona path --instructions
			// takes: the same lane check, snapshot, and annotations.
			flags.Instructions, flags.InstructionsOption = Spec.Instructions, "profile"
		}
	}
	for _, overlap := range []struct {
		item, value string
		flag        *string
		filled      *bool
	}{
		{profileItemModel, Spec.Model, &flags.Model, &launch.FilledModel},
		{profileItemEffort, Spec.Effort, &flags.Effort, &launch.FilledEffort},
	} {
		switch {
		case overlap.value == "":
		case !claude && provider != aiModeCodex:
			launch.Skip(overlap.item, provider, profileReasonProviderOptionUnsupported)
		case *overlap.flag != "":
			launch.Skip(overlap.item, provider, profileReasonOverriddenByFlag)
		default:
			*overlap.flag, *overlap.filled = overlap.value, true
		}
	}
	if claude && Spec.Permissions.Sandbox != "" {
		launch.Skip(profileItemSandbox, provider, profileReasonClaudeSandboxBashOnly)
	}
	if claude && Spec.Permissions.Approval != "" {
		launch.Skip(profileItemApproval, provider, profileReasonClaudeNoPermissionMode)
	}
	flags.ProfileLaunch = launch
	return nil
}

// codexThreadPolicy spells a profile's sandbox and approval in the Codex
// thread/start and thread/resume vocabulary. The profile says `full-access`
// where Codex says `danger-full-access`; every other value is spelled the
// same. An absent value stays absent, so a profile without either maps to the
// zero policy and the request carries neither key. allow and deny have no
// Codex counterpart and are not part of the policy.
func CodexThreadPolicy(perms profile.Permissions) (codexappserver.ThreadPolicy, error) {
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
func (c *AgentSettings) PrepareProfileSettings(spelling, provider string, flags *AgentSettingsFlags) error {
	if !flags.ProfileLaunch.Active() || provider != aiModeClaude {
		return nil
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return fmt.Errorf("%s --profile: %w; nothing was created", spelling, err)
	}
	Settings, err := profile.WriteSettingsSnapshot(paths.StateDir, flags.ProfileLaunch.Spec.Permissions)
	if err != nil {
		return fmt.Errorf("%s --profile %s: %w; nothing was created", spelling, flags.ProfileLaunch.Name, err)
	}
	flags.ProfileLaunch.Settings = Settings
	return nil
}
