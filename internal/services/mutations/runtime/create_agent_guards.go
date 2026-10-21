package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

type AgentInputFields struct{ Prefix string }

func (c AgentInputFields) Option(name string) string {
	if c.Prefix == "" {
		return "--" + name
	}
	fields := map[string]string{"model": "Model", "effort": "Effort", "provider": "Provider", "profile": "Profile", "instructions": "Instructions", "persona": "Instructions", "label": "Labels", "interactive-only": "InteractiveOnly", "dialogue-reply-only": "DialogueReplyOnly", "creator": "Creator"}
	return c.Prefix + "." + fields[name]
}
func (c AgentInputFields) RequireInteractiveOnlyProvider(spelling, provider string, enabled bool) error {
	if !enabled || provider == "codex" {
		return nil
	}
	return createUsage(fmt.Sprintf("%s %s applies only to %s codex; %s has no native thread binding to opt out of", spelling, c.Option("interactive-only"), c.Option("provider"), provider))
}
func (c AgentInputFields) RequireProfileLane(spelling, profile string, enabled bool) error {
	if profile == "" || profile == "none" || !enabled {
		return nil
	}
	return createAgentUsage(profileReasonLaneUnsupported, fmt.Sprintf("%s %s cannot be combined with %s (%s); nothing was created", spelling, c.Option("profile"), c.Option("dialogue-reply-only"), profileReasonLaneUnsupported))
}
func (c AgentInputFields) RequireDialogueMode(provider string, enabled bool, payload []string) error {
	if !enabled {
		return nil
	}
	if provider != "claude" {
		return createUsage(c.Option("dialogue-reply-only") + " requires a Claude Agent/provider")
	}
	if len(payload) != 0 {
		return createUsage(c.Option("dialogue-reply-only") + " starts with a fixed readiness turn and accepts no initial payload")
	}
	if runtime.GOOS != "linux" {
		return errors.New("claude reply-only activation requires Linux process birth and pinned executable support")
	}
	return nil
}

// ClaudeEffortLevels is the set `claude --help` lists for --effort.
var ClaudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ClaudeModelName accepts an alias such as "opus" or a full name such as
// "claude-opus-5" or "opus[1m]". Anything else could be read by the provider as
// another option.
var ClaudeModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)

// RequireLaunchOptions is the one model and effort validator of create and
// `agent resume`. outcome ends every refusal: what the refusing verb left
// untouched ("nothing was created" or "nothing was changed").
func (c AgentInputFields) RequireLaunchOptions(spelling, provider, model, effort string, dialogueReplyOnly bool, outcome string) error {
	if model == "" && effort == "" {
		return nil
	}
	if provider != "claude" && provider != "codex" {
		return createUsage(fmt.Sprintf("%s "+c.Option("model")+" and "+c.Option("effort")+" apply only to "+c.Option("provider")+" %s or %s; %s", spelling, "claude", "codex", outcome))
	}
	if dialogueReplyOnly {
		return createUsage(fmt.Sprintf("%s "+c.Option("model")+" and "+c.Option("effort")+" cannot be combined with %s; %s",
			spelling, c.Option(DialogueReplyOnlyFlag), outcome))
	}
	if model != "" && !ClaudeModelName.MatchString(model) {
		return createUsage(fmt.Sprintf("%s "+c.Option("model")+" %q is not a model name; %s", spelling, model, outcome))
	}
	if effort != "" && !slices.Contains(ClaudeEffortLevels, effort) {
		return createUsage(fmt.Sprintf("%s "+c.Option("effort")+" must be one of: %s; %s",
			spelling, strings.Join(ClaudeEffortLevels, ", "), outcome))
	}
	return nil
}

// RequirePersonaLane keeps the reply-only and other-provider guards. Codex
// fresh creation can privately carry managed instructions over the native base
// request or a plain TUI model_instructions_file snapshot path.
func (c AgentInputFields) RequirePersonaLane(spelling, provider string, flags AgentSettingsFlags) error {
	if flags.Instructions == "" {
		return nil
	}
	option := flags.InstructionsOption
	if option == "" {
		option = "instructions"
	}
	if flags.DialogueReplyOnly {
		return createAgentUsage(persona.ReasonProviderUnsupported, fmt.Sprintf("%s %s cannot be combined with %s (%s); nothing was created",
			spelling, c.Option(option), c.Option(DialogueReplyOnlyFlag), persona.ReasonProviderUnsupported), &persona.Error{Reason: persona.ReasonProviderUnsupported, Name: flags.Instructions, Detail: "cannot be combined with " + c.Option(DialogueReplyOnlyFlag)})
	}
	switch {
	case provider == "claude":
		return nil
	case provider == "codex" && flags.ResumeConversation != "":
		return createUsage(fmt.Sprintf("%s %s cannot change a Codex conversation's instructions: Codex fixes them when the thread starts and resume replays the original developer message (%s); create a new Codex Agent with -- <prompt>; nothing was created",
			spelling, c.Option(option), "codex-instructions-immutable"))
	case provider == "codex":
		return nil

	default:
		return createAgentUsage(persona.ReasonProviderUnsupported, fmt.Sprintf("%s %s applies only to "+c.Option("provider")+" %s and "+c.Option("provider")+" %s (%s); nothing was created",
			spelling, c.Option(option), "claude", "codex", persona.ReasonProviderUnsupported), &persona.Error{Reason: persona.ReasonProviderUnsupported, Name: flags.Instructions, Detail: "applies only to " + c.Option("provider") + " claude and " + c.Option("provider") + " codex"})
	}
}

// RequireCanonicalProvider enforces the canonical Provider enum.
//
// The picker adapters get their own message because refusing them as "unknown"
// would hide the actual model: they are selection surfaces that end in one of
// these providers, not providers.
func (c AgentInputFields) RequireCanonicalProvider(spelling, raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" {
		return "", createUsage(fmt.Sprintf("%s requires "+c.Option("provider")+" (%s); the saved split mode is not a canonical default",
			spelling, strings.Join(canonicalAgentProviders(), ", ")))
	}
	if provider == "selective" || provider == "resume" {
		return "", createUsage(fmt.Sprintf("%s: %q is an interactive picker, not a provider; choose one of %s for `projmux %s "+c.Option("provider")+"`",
			spelling, provider, strings.Join(canonicalAgentProviders(), ", "), spelling))
	}
	if provider == "shell" {
		return "", createUsage(fmt.Sprintf("%s: a shell surface is a Pane, not an Agent; use `projmux create pane`", spelling))
	}
	if !isAgentProvider(provider) {
		return "", createUsage(fmt.Sprintf("%s: unknown provider %q; accepted providers: %s",
			spelling, provider, strings.Join(canonicalAgentProviders(), ", ")))
	}
	return provider, nil
}
func canonicalAgentProviders() []string {
	var out []string
	for _, p := range aiprovider.PickerEligible() {
		out = append(out, string(p.ID))
	}
	return out
}
func isAgentProvider(p string) bool { _, ok := aiprovider.Lookup(p); return ok }
