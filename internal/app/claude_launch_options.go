package app

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
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

// claudeLaunchOptionArgs spells the Claude launch options. personaFile is the
// persona snapshot path: only the path reaches argv, never the content, so the
// persona does not show in `ps`.
func claudeLaunchOptionArgs(model, effort, personaFile string) []string {
	var args []string
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if personaFile != "" {
		args = append(args, "--append-system-prompt-file", personaFile)
	}
	return args
}

// claudeSystemPromptSnapshotFlag is Claude's switch for the system prompt it
// records on a conversation's first request and replays on every resume.
const claudeSystemPromptSnapshotFlag = "--system-prompt-snapshot"

// claudeResumeSnapshotArgs spells the system prompt snapshot mode a resumed
// Agent records. Only a Claude Agent annotated with
// coremetadata.SystemPromptSnapshotOff gets an argument; any other Agent, and
// any other provider carrying the annotation, gets none, so its resume argv
// stays byte-identical to the one it had before the annotation existed.
func claudeResumeSnapshotArgs(mode string, annotations map[string]string) []string {
	if mode != aiModeClaude || annotations[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		return nil
	}
	return []string{claudeSystemPromptSnapshotFlag, coremetadata.SystemPromptSnapshotOff}
}

// personaLaunch is the persona one Agent create starts with: the stored name,
// and the snapshot the provider is given. The zero value means no persona.
type personaLaunch struct {
	name     string
	snapshot persona.Snapshot
}

// withAnnotations adds the two keys that link the new Agent to its persona to
// base, the Agent annotations the create already records (the creator keys).
// Without a persona it returns base itself, so a create without --persona
// stores exactly what it stored before the flag existed -- nil included.
// Otherwise it returns a new map and never writes into base.
func (p personaLaunch) withAnnotations(base map[string]string) map[string]string {
	if p.name == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 2)
	}
	out[coremetadata.AnnotationAgentPersona] = p.name
	out[coremetadata.AnnotationAgentPersonaDigest] = p.snapshot.Digest
	return out
}

// requireClaudePersona refuses --persona where it would be ignored: another
// provider, or the reply-only activation, which has its own fixed launch. Like
// requireClaudeLaunchOptions it is an argv-only refusal, so it lands before the
// persona file is read and before any snapshot is written.
func requireClaudePersona(spelling, provider string, flags resourceCreateFlags) error {
	if flags.persona == "" {
		return nil
	}
	if provider != aiModeClaude {
		return usageError(fmt.Sprintf("%s --persona applies only to --provider %s (%s); nothing was created",
			spelling, aiModeClaude, persona.ReasonProviderUnsupported))
	}
	if flags.dialogueReplyOnly {
		return usageError(fmt.Sprintf("%s --persona cannot be combined with --%s (%s); nothing was created",
			spelling, claudeDialogueReplyOnlyFlag, persona.ReasonProviderUnsupported))
	}
	return nil
}

// preparePersonaLaunch resolves the named persona and writes its snapshot.
//
// It runs after every argv-only refusal and after scope resolution, and before
// the create transaction opens: a persona that is missing, too large, or badly
// named refuses with its reason token while nothing exists, and a snapshot
// that cannot be written refuses the same way. A later failure can leave the
// snapshot behind, which is harmless: it is content addressed.
func (c *createCommand) preparePersonaLaunch(spelling, name string) (personaLaunch, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return personaLaunch{}, fmt.Errorf("%s --persona: %w; nothing was created", spelling, err)
	}
	store := persona.NewDefaultStore(paths)
	loaded, err := store.Load(name)
	if err != nil {
		if persona.ReasonOf(err) != "" {
			return personaLaunch{}, usageError(fmt.Sprintf("%s --persona: %v; nothing was created", spelling, err))
		}
		return personaLaunch{}, fmt.Errorf("%s --persona: %w; nothing was created", spelling, err)
	}
	snapshot, err := store.WriteSnapshot(loaded.Content)
	if err != nil {
		return personaLaunch{}, fmt.Errorf("%s --persona: %w; nothing was created", spelling, err)
	}
	return personaLaunch{name: loaded.Name, snapshot: snapshot}, nil
}
