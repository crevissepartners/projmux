package app

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// claudeModelAliases are the aliases `claude --help` names for --model; a
// full model name is accepted too, but these are what a launcher offers.
var claudeModelAliases = []string{"opus", "sonnet", "fable"}

// claudeEffortLevels is the set `claude --help` lists for --effort.
var claudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeModelName accepts an alias such as "opus" or a full name such as
// "claude-opus-5" or "opus[1m]". Anything else could be read by the provider as
// another option.
var claudeModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)

// requireClaudeLaunchOptions refuses --model and --effort where they would be
// ignored or misread: another provider, the reply-only activation, or a value
// Claude does not take. Like --interactive-only it is an argv-only refusal.
func requireClaudeLaunchOptions(spelling, provider string, flags resourceCreateFlags) error {
	if flags.model == "" && flags.effort == "" {
		return nil
	}
	if provider != aiModeClaude {
		return usageError(fmt.Sprintf("%s --model and --effort apply only to --provider %s; nothing was created", spelling, aiModeClaude))
	}
	if flags.dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s --model and --effort cannot be combined with --%s; nothing was created",
			spelling, claudeDialogueReplyOnlyFlag))
	}
	if flags.model != "" && !claudeModelName.MatchString(flags.model) {
		return usageError(fmt.Sprintf("%s --model %q is not a model name; nothing was created", spelling, flags.model))
	}
	if flags.effort != "" && !slices.Contains(claudeEffortLevels, flags.effort) {
		return usageError(fmt.Sprintf("%s --effort must be one of: %s; nothing was created",
			spelling, strings.Join(claudeEffortLevels, ", ")))
	}
	return nil
}

func claudeLaunchOptionArgs(model, effort string) []string {
	var args []string
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}
