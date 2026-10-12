package app

import (
	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"slices"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

var claudeEffortLevels = mutationruntime.ClaudeEffortLevels

var claudeModelName = mutationruntime.ClaudeModelName

// requireClaudeLaunchOptions validates the shared model and effort create
// surface before any resource is allocated. The name is retained for callers
// that originally used the Claude-only validator.
func requireClaudeLaunchOptions(spelling, provider string, flags resourceCreateFlags) error {
	return requireLaunchOptions(spelling, provider, flags.model, flags.effort, flags.dialogueReplyOnly, "nothing was created")
}

func requireLaunchOptions(spelling, provider, model, effort string, dialogueReplyOnly bool, outcome string) error {
	return (mutationruntime.AgentInputFields{}).RequireLaunchOptions(spelling, provider, model, effort, dialogueReplyOnly, outcome)
}

// codexLaunchOptionArgs keeps provider options ahead of the resume subcommand
// and workspace arguments. Codex accepts model and effort on both CLI lanes.
func codexLaunchOptionArgs(model, effort string) []string {
	return mutationruntime.CodexLaunchOptionArgs(model, effort)
}

// codexCLIProfileArgs carries a validated profile policy on Codex's plain
// create and CLI resume lanes. The profile's full-access value was already
// translated to danger-full-access by codexThreadPolicy.
func codexCLIProfileArgs(policy codexappserver.ThreadPolicy) []string {
	return mutationruntime.CodexCLIProfileArgs(policy)
}

// claudeLaunchOptionArgs spells the Claude launch options. personaFile is the
// persona snapshot path: only the path reaches argv, never the content, so the
// persona does not show in `ps`.
func claudeLaunchOptionArgs(model, effort, personaFile string) []string {
	return mutationruntime.ClaudeLaunchOptionArgs(model, effort, personaFile)
}

// claudeSystemPromptSnapshotFlag is Claude's switch for the system prompt it
// records on a conversation's first request and replays on every resume.
const claudeSystemPromptSnapshotFlag = "--system-prompt-snapshot"



// personaLaunch is the persona one Agent create starts with: the stored name,
// the snapshot the provider is given, and that snapshot's content. The zero
// value means no persona.
//
// Both forms of the same bytes are kept because the two providers take the
// persona differently and neither should have to read the snapshot back:
// Claude is given the snapshot path on its command line, and Codex is given
// the content itself as the thread's developer instructions, because putting
// it in argv would publish it to every reader of `ps`.
type personaLaunch struct {
	name     string
	snapshot persona.Snapshot
	content  string
}

func (p personaLaunch) withAnnotations(base map[string]string) map[string]string {
	return p.shared().WithAnnotations(base)
}

// withEffortAnnotation adds the effort a new Agent is created with to
// base, the Agent annotations the create already records. Without an effort
// it returns base itself, so a create without --effort stores exactly what it
// stored before the effort was recorded -- nil included. Otherwise it returns
// a new map and never writes into base. The create preflight has already
// refused unsupported providers and invalid values.
func withEffortAnnotation(effort string, base map[string]string) map[string]string {
	return mutationruntime.WithEffortAnnotation(effort, base)
}

// withModelAnnotation adds the model a new Agent is created with to base, the
// Agent annotations the create already records, with the same contract as
// withEffortAnnotation: without a model it returns base itself (nil included),
// otherwise a new map, and it never writes into base.
func withModelAnnotation(model string, base map[string]string) map[string]string {
	return mutationruntime.WithModelAnnotation(model, base)
}

// claudeEffortReasonInvalid is the reason token of a recorded effort a resume
// does not re-pass because it is not one of claudeEffortLevels.
const claudeEffortReasonInvalid = "effort-invalid"



// personaReasonCodexInstructionsImmutable is the public refusal for trying to
// change the developer instructions of an existing Codex conversation.
const personaReasonCodexInstructionsImmutable = "codex-instructions-immutable"

func requirePersonaLane(spelling, provider string, flags resourceCreateFlags) error {
	return (mutationruntime.AgentInputFields{}).RequirePersonaLane(spelling, provider, flags.sharedAgentSettingsFlags())
}

func (c *createCommand) preparePersonaLaunch(spelling, name string, optionNames ...string) (personaLaunch, error) {
	shared := &mutationruntime.AgentSettings{Paths: func() (config.Paths, error) { return configPaths(c.homeDir, c.lookupEnv) }}
	p, err := shared.PreparePersonaLaunch(spelling, name, optionNames...)
	return personaLaunch{name: p.Name, snapshot: p.Snapshot, content: p.Content}, err
}

func (p personaLaunch) shared() mutationruntime.PersonaLaunch {
	return mutationruntime.PersonaLaunch{Name: p.name, Snapshot: p.snapshot, Content: p.content}
}
