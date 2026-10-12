package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// ProviderOptionArity is how many operands one provider option consumes.
type ProviderOptionArity int

const (
	// OptionOneValuePerOccurrence is `--flag <value>`: exactly one operand per
	// occurrence, so several values mean several occurrences and a following
	// operand is never absorbed.
	OptionOneValuePerOccurrence ProviderOptionArity = iota
	// OptionVariadicValues is `--flag <values...>`: one occurrence carries every
	// value, and the option keeps consuming operands until an option-looking
	// token or an option terminator stops it.
	OptionVariadicValues
)

// ProviderLaunchGrammar is the part of one provider CLI's argv grammar the Agent
// launch depends on. It is deliberately a description of the upstream contract
// rather than a projmux preference: changing a field here is a claim about what
// the provider's parser does.
type ProviderLaunchGrammar struct {
	// WorkingRootFlag carries the workspace CWD when the provider needs it in
	// argv. It is empty for providers that take the working root from the
	// process working directory, which the launch already sets.
	WorkingRootFlag string
	// AdditionalRootFlag carries the additional writable roots. It is empty for
	// providers projmux does not give additional roots to at all, which makes a
	// stored additional root a refusal rather than an invented flag.
	AdditionalRootFlag string
	// AdditionalRootArity is the arity of AdditionalRootFlag.
	AdditionalRootArity ProviderOptionArity
	// PayloadTerminator is the token this provider's parser accepts to end
	// option parsing, so everything after it is an operand. It is emitted
	// before every non-empty payload and is what lets a payload follow a
	// variadic option without being eaten by it. Empty means the provider's
	// argv carries no terminator at all.
	PayloadTerminator string
}

// ProviderLaunchGrammarFor returns the argv grammar of one provider.
//
// Measured against the installed CLIs on 2026-08-19:
//
//   - Claude Code 2.1.234 documents `--add-dir <directories...>` and
//     `claude [options] [command] [prompt]`. `claude --add-dir DIR hello` was
//     observed rejecting the run with "Input must be provided either through
//     stdin or as a prompt argument", i.e. `hello` reached the parser as a
//     directory and no prompt survived; `claude --add-dir DIR -- hello` and
//     `claude --add-dir A B -- hello` both reached execution with the prompt
//     intact. That pair is the whole reason this file exists.
//   - Codex documents `-C, --cd <DIR>` and `--add-dir <DIR>`, each taking
//     exactly one value, with `codex [OPTIONS] [PROMPT]`. Its parser would
//     accept `--` as well, but Codex's argv is deliberately left byte-identical
//     to what shipped, because a one-value option cannot absorb the payload and
//     there is nothing here to repair.
//   - Antigravity's `agy` does have a repeatable `--add-dir`, but projmux does
//     not give it additional roots: `resolveAgentWorkspaceFor` refuses them for
//     every provider except Codex and Claude, and widening that is a product
//     decision rather than part of this repair. The empty grammar below records
//     that policy, so a stored root that outlived the gate refuses instead of
//     reaching `agy` through a flag this seam never validated.
func ProviderLaunchGrammarFor(provider string) ProviderLaunchGrammar {
	switch NormalizeAIMode(provider) {
	case "claude":
		return ProviderLaunchGrammar{
			AdditionalRootFlag:  "--add-dir",
			AdditionalRootArity: OptionVariadicValues,
			// Claude's parser is commander, which honors `--`.
			PayloadTerminator: "--",
		}
	case "codex":
		return ProviderLaunchGrammar{
			WorkingRootFlag:     "-C",
			AdditionalRootFlag:  "--add-dir",
			AdditionalRootArity: OptionOneValuePerOccurrence,
		}
	default:
		return ProviderLaunchGrammar{}
	}
}

// ProviderLaunchArgs builds the provider argv tail for one Agent launch: the
// workspace options followed by the initial task payload, separated so the
// provider parses each as what it is.
//
// Three properties are load bearing:
//
//   - Every additional root reaches the provider. A variadic option carries
//     them in one occurrence, a one-value option repeats.
//   - A non-empty payload stays the prompt. When the provider has a terminator
//     it is emitted before the payload -- always, not only after a variadic
//     option -- so one Claude argv shape carries every root count, and a prompt
//     that happens to begin with `-` is still an operand rather than an unknown
//     option.
//   - An empty payload adds nothing at all, so the interactive create and the
//     resume route keep the exact argv they have today.
func ProviderLaunchArgs(provider string, workspace coremetadata.AgentWorkspace, payload []string) ([]string, error) {
	grammar := ProviderLaunchGrammarFor(provider)
	if len(workspace.AdditionalWritableRoots) > 0 && grammar.AdditionalRootFlag == "" {
		// resolveAgentWorkspaceFor refuses this at parse time; reaching it here
		// means a stored workspace outlived that gate, and inventing a flag the
		// provider does not have would start a session with silently narrower
		// access than the Agent records.
		return nil, fmt.Errorf("provider %q does not support additional writable roots", provider)
	}

	args := make([]string, 0, len(payload)+3+2*len(workspace.AdditionalWritableRoots))
	if grammar.WorkingRootFlag != "" && workspace.CWD != "" {
		args = append(args, grammar.WorkingRootFlag, workspace.CWD)
	}
	variadicTail := false
	if roots := workspace.AdditionalWritableRoots; len(roots) > 0 {
		switch grammar.AdditionalRootArity {
		case OptionVariadicValues:
			args = append(args, grammar.AdditionalRootFlag)
			args = append(args, roots...)
			variadicTail = true
		default:
			for _, root := range roots {
				args = append(args, grammar.AdditionalRootFlag, root)
			}
		}
	}
	if len(payload) == 0 {
		return args, nil
	}
	switch {
	case grammar.PayloadTerminator != "":
		args = append(args, grammar.PayloadTerminator)
	case variadicTail:
		// A provider that ends its workspace options with a variadic option and
		// offers no terminator cannot express this argv at all. Emitting it
		// anyway would start a session whose task silently became a directory,
		// which is exactly the failure this seam exists to stop.
		return nil, fmt.Errorf("provider %q cannot carry an initial task after %s", provider, grammar.AdditionalRootFlag)
	}
	return append(args, payload...), nil
}

// CodexLaunchOptionArgs keeps provider options ahead of the resume subcommand
// and workspace arguments. Codex accepts model and effort on both CLI lanes.
func CodexLaunchOptionArgs(model, effort string) []string {
	var args []string
	if model != "" {
		args = append(args, "-m", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	return args
}

// CodexCLIProfileArgs carries a validated profile policy on Codex's plain
// create and CLI resume lanes. The profile's full-access value was already
// translated to danger-full-access by codexThreadPolicy.
func CodexCLIProfileArgs(policy codexappserver.ThreadPolicy) []string {
	var args []string
	if policy.Sandbox != "" {
		args = append(args, "-s", string(policy.Sandbox))
	}
	if policy.ApprovalPolicy != "" {
		args = append(args, "-a", string(policy.ApprovalPolicy))
	}
	return args
}

// ClaudeLaunchOptionArgs spells the Claude launch options. personaFile is the
// persona snapshot path: only the path reaches argv, never the content, so the
// persona does not show in `ps`.
func ClaudeLaunchOptionArgs(model, effort, personaFile string) []string {
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

// ClaudeSettingsArgs spells the Claude settings snapshot a profile's
// permissions are launched with. Only the path reaches argv.
func ClaudeSettingsArgs(settingsFile string) []string {
	if settingsFile == "" {
		return nil
	}
	return []string{"--settings", settingsFile}
}

// PlanAgentLaunch builds the provider launch for one detached Agent create.
//
// The workspace and the initial task are assembled by ProviderLaunchArgs rather
// than concatenated here, because where the workspace options stop is a property
// of the provider's own parser: Claude's variadic `--add-dir` eats a payload
// appended straight after it and starts a session with no task at all.
func (c *AgentLaunchService) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (title string, argv []string, err error) {
	extra, err := ProviderLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	title, argv, err = c.Plan(provider, workspace.CWD, extra)
	if err != nil {
		return "", nil, err
	}
	return title, argv, nil
}

// PlanAgentLaunchWithOptions applies model and effort to Claude or Codex.
// Claude alone accepts the persona snapshot path. Options precede workspace
// arguments so Claude's variadic --add-dir cannot consume them.
func (c *AgentLaunchService) PlanAgentLaunchWithOptions(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile string) (title string, argv []string, err error) {
	return c.PlanAgentLaunchWithSettings(provider, workspace, payload, model, effort, personaFile, "")
}

// PlanAgentLaunchWithSettings is PlanAgentLaunchWithOptions that also passes
// a profile's Claude settings snapshot as --settings. An empty settingsFile
// adds nothing, which is exactly PlanAgentLaunchWithOptions.
func (c *AgentLaunchService) PlanAgentLaunchWithSettings(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile, settingsFile string) (title string, argv []string, err error) {
	mode := NormalizeAIMode(provider)
	if mode != "claude" && mode != "codex" {
		return "", nil, fmt.Errorf("provider %q does not accept --model, --effort, or --instructions", provider)
	}
	if mode == "codex" && (personaFile != "" || settingsFile != "") {
		return "", nil, fmt.Errorf("provider %q does not accept Claude instructions or settings options", provider)
	}
	extra, err := ProviderLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	options := append(ClaudeLaunchOptionArgs(model, effort, personaFile), ClaudeSettingsArgs(settingsFile)...)
	if mode == "codex" {
		options = CodexLaunchOptionArgs(model, effort)
	}
	extra = append(options, extra...)
	title, argv, err = c.Plan(provider, workspace.CWD, extra)
	if err != nil {
		return "", nil, err
	}
	return title, argv, nil
}

// PlanCodexAgentLaunchWithPolicy plans a plain Codex CLI create with the
// profile permissions ahead of workspace options and any prompt.
func (c *AgentLaunchService) PlanCodexAgentLaunchWithPolicy(workspace coremetadata.AgentWorkspace, payload []string, model, effort string, policy codexappserver.ThreadPolicy) (string, []string, error) {
	extra, err := ProviderLaunchArgs("codex", workspace, payload)
	if err != nil {
		return "", nil, err
	}
	options := append(CodexLaunchOptionArgs(model, effort), CodexCLIProfileArgs(policy)...)
	title, argv, err := c.Plan("codex", workspace.CWD, append(options, extra...))
	if err != nil {
		return "", nil, err
	}
	return title, argv, nil
}
