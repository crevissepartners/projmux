package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"maps"
	"slices"
	"strings"
)

type AgentSettingsFlags struct {
	Profile, ProfileProjectUID, Instructions, InstructionsOption, Model, Effort, Host, ResumeConversation string
	Labels, Payload                                                                                       []string
	DialogueReplyOnly, InteractiveOnly                                                                    bool
	PersonaLaunch                                                                                         PersonaLaunch
	ProfileLaunch                                                                                         ProfileLaunch
}
type AgentSettings struct {
	Fields         AgentInputFields
	Paths          func() (config.Paths, error)
	ProfileProject func(string, AgentSettingsFlags) (string, error)
}

func (c *AgentSettings) ProfileStore() (profile.Store, error) {
	paths, err := c.Paths()
	if err != nil {
		return profile.Store{}, err
	}
	return profile.NewDefaultStore(paths), nil
}
func AgentLabelMap(raw []string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range raw {
		label, err := selector.ParseLabel(value)
		if err != nil {
			return nil, err
		}
		out[label.Key] = label.Value
	}
	return out, nil
}
func NativeCodexFreshCreateRequired(provider string, interactiveOnly bool, payload []string, host, conversation string) bool {
	return provider == "codex" && !interactiveOnly && (len(payload) > 0 || host == "process") && strings.TrimSpace(conversation) == ""
}

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
	profileReasonProviderOptionUnsupported = "provider-Option-unsupported"
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

// ProfileLaunch is the profile one Agent create applies: its Name, the
// Digest of the content applied, the checked content, the Claude Settings
// snapshot its allow and deny rules are launched with, the Codex policy its
// sandbox and approval are started with on native or CLI lanes, and items
// not applied. The zero value means no profile.
type ProfileLaunch struct {
	Name        string
	Digest      string
	Spec        profile.Spec
	Settings    string
	CodexPolicy codexappserver.ThreadPolicy
	NotApplied  []receipt.ReceiptProfileItem
	// Source is how the create selected the profile
	// (coremetadata.SettingSourceFlag or coremetadata.SettingSourceRole), and
	// FilledModel and FilledEffort say whether the profile, not a flag, gave
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
func (p ProfileLaunch) Receipt() *receipt.ReceiptProfile {
	if !p.Active() {
		return nil
	}
	return &receipt.ReceiptProfile{Name: p.Name, Digest: p.Digest, NotApplied: append([]receipt.ReceiptProfileItem{}, p.NotApplied...)}
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
	p.NotApplied = append(p.NotApplied, receipt.ReceiptProfileItem{Item: item, Provider: provider, Reason: reason})
}

// SelectedCreateProfile is the profile one Agent create selected: its Name,
// the Option that selected it as the refusals quote it, and what it resolved
// to. The zero value means the create applies no profile.
type SelectedCreateProfile struct {
	Name   string
	Option string
	Source string
	Loaded profile.Profile
	Spec   profile.Spec
}

// SelectCreateProfile is the one definition of which profile an Agent create
// uses, and it resolves that profile.
//
// An explicit --profile names it; `none` means no profile and no role
// mapping. Without --profile, a `role` creation label selects the one profile
// listing that role; a role no profile lists selects none, and so does a label
// argv the create itself refuses. Every way the selection or the resolution
// fails is a refusal that says nothing was created.
//
// Both ResolveCreateProfile and resolveCreateProvider call it, so the profile
// that decides an omitted --provider is the profile the create then applies.
func (c *AgentSettings) SelectCreateProfile(spelling string, flags AgentSettingsFlags) (SelectedCreateProfile, error) {
	if flags.Profile == profile.ReservedName {
		return SelectedCreateProfile{}, nil
	}
	Name := flags.Profile
	Option := "" + c.Fields.Option("profile") + " " + flags.Profile
	Source := coremetadata.SettingSourceFlag
	role := ""
	if Name == "" {
		labels, err := AgentLabelMap(flags.Labels)
		if err != nil {
			// The create refuses the label itself, with its own message.
			return SelectedCreateProfile{}, nil
		}
		var ok bool
		if role, ok = labels[profileRoleLabel]; !ok {
			return SelectedCreateProfile{}, nil
		}
		Option = "" + c.Fields.Option("label") + " " + profileRoleLabel + "=" + role
		Source = coremetadata.SettingSourceRole
	}
	// The store is opened only once a profile has to be read, so a create
	// that selects none never depends on the config home.
	store, err := c.ProfileStore()
	if err != nil {
		if flags.Profile == "" && errors.Is(err, config.ErrHomeDirRequired) {
			// A role mapping is a read of saved profiles; without a config
			// home there are none, so no profile claims a role.
			return SelectedCreateProfile{}, nil
		}
		return SelectedCreateProfile{}, fmt.Errorf("%s "+c.Fields.Option("profile")+": %w; nothing was created", spelling, err)
	}
	if Name == "" {
		entries, listErr := store.List()
		if listErr != nil {
			return SelectedCreateProfile{}, listErr
		}
		projectUID := flags.ProfileProjectUID
		for _, entry := range entries {
			if entry.Project != "" && slices.Contains(entry.Roles, role) {
				projectUID, err = c.ProfileProject(spelling, flags)
				if err != nil {
					return SelectedCreateProfile{}, err
				}
				break
			}
		}
		if Name, err = store.RoleProfile(role, projectUID); err != nil {
			switch profile.ReasonOf(err) {
			case profile.ReasonRoleClaimed:
				return SelectedCreateProfile{}, createAgentUsage(profile.ReasonRoleClaimed, fmt.Sprintf("%s %s: %v; remove the role from all but one of the listed profiles, or pass "+c.Fields.Option("profile")+" none to create without a profile; nothing was created",
					spelling, Option, err))
			case profile.ReasonRoleProfileInvalid:
				return SelectedCreateProfile{}, createAgentUsage(profile.ReasonRoleProfileInvalid, fmt.Sprintf("%s %s: %v; fix the profile with `projmux profile set <name>`, or pass "+c.Fields.Option("profile")+" none to create without a profile; nothing was created",
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
			return SelectedCreateProfile{}, createAgentUsage(profile.ReasonOf(err), fmt.Sprintf("%s %s: %v; nothing was created", spelling, Option, err))
		}
		return SelectedCreateProfile{}, fmt.Errorf("%s %s: %w; nothing was created", spelling, Option, err)
	}
	if Spec.Project != "" {
		projectUID, scopeErr := c.ProfileProject(spelling, flags)
		if scopeErr != nil {
			return SelectedCreateProfile{}, scopeErr
		}
		if err := profile.CheckProject(Name, Spec, projectUID); err != nil {
			return SelectedCreateProfile{}, createAgentUsage(profile.ReasonOf(err), fmt.Sprintf("%s %s: %v; nothing was created", spelling, Option, err))
		}
	}
	return SelectedCreateProfile{Name: Name, Option: Option, Source: Source, Loaded: Loaded, Spec: Spec}, nil
}

// ResolveCreateProfile decides the profile one Agent create applies and merges
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
// SelectCreateProfile decides which profile that is, the same way it does
// for resolveCreateProvider.
//
// It reads files and writes none: the Settings snapshot is written by
// PrepareProfileSettings, later, next to the persona snapshot.
func (c *AgentSettings) ResolveCreateProfile(spelling, provider string, flags *AgentSettingsFlags) error {
	selected, err := c.SelectCreateProfile(spelling, *flags)
	if err != nil || selected.Name == "" {
		return err
	}
	Name, Option, Loaded, Spec := selected.Name, selected.Option, selected.Loaded, selected.Spec
	if Spec.Provider != "" && Spec.Provider != provider {
		return createAgentUsage(profileReasonProviderMismatch, fmt.Sprintf("%s %s: profile %q is for provider %s, not %s (%s); create the Agent with provider %s, or pass "+c.Fields.Option("profile")+" none to create without a profile; nothing was created",
			spelling, Option, Name, Spec.Provider, provider, profileReasonProviderMismatch, Spec.Provider))
	}
	if flags.DialogueReplyOnly {
		return createAgentUsage(profileReasonLaneUnsupported, fmt.Sprintf("%s %s: profile %q cannot apply to %s (%s); nothing was created",
			spelling, Option, Name, c.Fields.Option(DialogueReplyOnlyFlag), profileReasonLaneUnsupported))
	}
	claude := provider == "claude"
	codexNative := NativeCodexFreshCreateRequired(provider, flags.InteractiveOnly, flags.Payload, flags.Host, flags.ResumeConversation)
	switch {
	case claude, provider == "codex", !Spec.Permissions.HasPermissions():
	default:
		return createAgentUsage(profileReasonPermissionsUnsupported, fmt.Sprintf("%s %s: profile %q sets permissions, which "+c.Fields.Option("provider")+" %s cannot apply (%s); nothing was created",
			spelling, Option, Name, provider, profileReasonPermissionsUnsupported))
	}
	launch := ProfileLaunch{Name: Loaded.Name, Digest: Loaded.Digest, Spec: Spec, Source: selected.Source}
	if provider == "codex" {
		if launch.CodexPolicy, err = CodexThreadPolicy(Spec.Permissions); err != nil {
			return createUsage(fmt.Sprintf("%s %s: profile %q: %v; nothing was created", spelling, Option, Name, err))
		}
		if !codexNative && launch.CodexPolicy.ApprovalPolicy == codexappserver.ApprovalUntrusted {
			return createAgentUsage(profileReasonCodexCLIUntrusted, fmt.Sprintf("%s %s: profile %q requests approval=untrusted, which the Codex CLI cannot apply (%s); nothing was created",
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
		case !claude && provider != "codex":
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

// CodexThreadPolicy spells a profile's sandbox and approval in the Codex
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

// PrepareProfileSettings writes the Claude Settings snapshot of the applied
// profile's allow and deny rules. Like preparePersonaLaunch it runs before the
// create transaction opens, and a snapshot a later failure leaves behind is
// harmless: it is content addressed.
func (c *AgentSettings) PrepareProfileSettings(spelling, provider string, flags *AgentSettingsFlags) error {
	if !flags.ProfileLaunch.Active() || provider != "claude" {
		return nil
	}
	paths, err := c.Paths()
	if err != nil {
		return fmt.Errorf("%s "+c.Fields.Option("profile")+": %w; nothing was created", spelling, err)
	}
	Settings, err := profile.WriteSettingsSnapshot(paths.StateDir, flags.ProfileLaunch.Spec.Permissions)
	if err != nil {
		return fmt.Errorf("%s "+c.Fields.Option("profile")+" %s: %w; nothing was created", spelling, flags.ProfileLaunch.Name, err)
	}
	flags.ProfileLaunch.Settings = Settings
	return nil
}
