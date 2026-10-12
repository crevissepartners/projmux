package runtime

import (
"context"
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/cli"
"github.com/crevissepartners/projmux/internal/config"
"github.com/crevissepartners/projmux/internal/core/persona"
"github.com/crevissepartners/projmux/internal/core/profile"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/services/mutations"
"io"
"maps"
"os"
"regexp"
"slices"
"strings"
"time"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

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

// claudeEffortLevels is the set `claude --help` lists for --effort.
var claudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeModelName accepts an alias such as "opus" or a full name such as
// "claude-opus-5" or "opus[1m]". Anything else could be read by the provider as
// another option.
var claudeModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)

// requireLaunchOptions is the one model and effort validator of create and
// `agent resume`. outcome ends every refusal: what the refusing verb left
// untouched ("nothing was created" or "nothing was changed").
func requireLaunchOptions(spelling, provider, model, effort string, dialogueReplyOnly bool, outcome string) error {
	if model == "" && effort == "" {
		return nil
	}
	if provider != aiModeClaude && provider != aiModeCodex {
		return usageError(fmt.Sprintf("%s --model and --effort apply only to --provider %s or %s; %s", spelling, aiModeClaude, aiModeCodex, outcome))
	}
	if dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s --model and --effort cannot be combined with --%s; %s",
			spelling, claudeDialogueReplyOnlyFlag, outcome))
	}
	if model != "" && !claudeModelName.MatchString(model) {
		return usageError(fmt.Sprintf("%s --model %q is not a model name; %s", spelling, model, outcome))
	}
	if effort != "" && !slices.Contains(claudeEffortLevels, effort) {
		return usageError(fmt.Sprintf("%s --effort must be one of: %s; %s",
			spelling, strings.Join(claudeEffortLevels, ", "), outcome))
	}
	return nil
}

// requirePersonaLane refuses --persona on every lane that cannot carry one.
//
// Claude takes a persona on every create: it is a file path on the command
// line. Codex takes one only on the native fresh lane, where the content is
// sent to the shared endpoint as the thread's developer instructions; its
// plain lane would have to spell the content into argv, where `ps` publishes
// it, so it refuses instead. Every other provider, and the reply-only
// activation with its own fixed launch, refuse as before.
//
// Like requireClaudeLaunchOptions it is an argv-only refusal, so it lands
// before the persona file is read and before any snapshot is written.
func requirePersonaLane(spelling, provider string, flags resourceCreateFlags) error {
	if flags.persona == "" {
		return nil
	}
	option := flags.personaOption
	if option == "" {
		option = "instructions"
	}
	if flags.dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s --%s cannot be combined with --%s (%s); nothing was created",
			spelling, option, claudeDialogueReplyOnlyFlag, persona.ReasonProviderUnsupported))
	}
	switch {
	case provider == aiModeClaude:
		return nil
	case provider == aiModeCodex && flags.resumeConversation != "":
		return usageError(fmt.Sprintf("%s --%s cannot change a Codex conversation's instructions: Codex fixes them when the thread starts and resume replays the original developer message (%s); create a new Codex Agent with -- <prompt>; nothing was created",
			spelling, option, personaReasonCodexInstructionsImmutable))
	case provider == aiModeCodex && nativeCodexFreshCreateRequired(provider, flags):
		// The native fresh lane is the only Codex create that opens a thread of
		// its own, and thread/start is the only moment a persona can be given.
		return nil
	case provider == aiModeCodex:
		return usageError(fmt.Sprintf("%s --%s applies to --provider %s only when creating a new Agent with a prompt through its native thread; add -- <prompt> and omit --interactive-only (%s); nothing was created",
			spelling, option, aiModeCodex, persona.ReasonProviderUnsupported))
	default:
		return usageError(fmt.Sprintf("%s --%s applies only to --provider %s and --provider %s (%s); nothing was created",
			spelling, option, aiModeClaude, aiModeCodex, persona.ReasonProviderUnsupported))
	}
}

// requireCanonicalProvider enforces the canonical Provider enum.
//
// The picker adapters get their own message because refusing them as "unknown"
// would hide the actual model: they are selection surfaces that end in one of
// these providers, not providers.
func requireCanonicalProvider(spelling, raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" {
		return "", usageError(fmt.Sprintf("%s requires --provider (%s); the saved split mode is not a canonical default",
			spelling, strings.Join(cli.AgentProviders(), ", ")))
	}
	if cli.IsPickerAdapter(provider) {
		return "", usageError(fmt.Sprintf("%s: %q is an interactive picker, not a provider; choose one of %s for `projmux %s --provider`",
			spelling, provider, strings.Join(cli.AgentProviders(), ", "), spelling))
	}
	if provider == aiModeShell {
		return "", usageError(fmt.Sprintf("%s: a shell surface is a Pane, not an Agent; use `projmux create pane`", spelling))
	}
	if !cli.IsAgentProvider(provider) {
		return "", usageError(fmt.Sprintf("%s: unknown provider %q; accepted providers: %s",
			spelling, provider, strings.Join(cli.AgentProviders(), ", ")))
	}
	return provider, nil
}
