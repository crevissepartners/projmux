package app

import (
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The provider argv boundary: how one Agent's workspace and its initial task
// survive the provider CLI's own parser.
//
// `create agent` hands two independent things to a provider CLI in one argv: the
// workspace (the working root and every additional writable root) and the
// initial task payload. Concatenating them is only safe if the provider's own
// option grammar agrees on where the workspace options stop. Claude's does not:
// `--add-dir <directories...>` is variadic, so it consumes every following
// operand until an option-looking token or an option terminator stops it, and a
// payload appended straight after it is parsed as one more directory rather than
// as the prompt. The pane then starts a session with no task at all, which is
// observable only as a missing activation acknowledgement -- the argv itself
// looks right.
//
// So the boundary is provider-specific data rather than a string concatenation,
// and the arity of each option is written down next to it.

// providerOptionArity is how many operands one provider option consumes.
type providerOptionArity int

const (
	// optionOneValuePerOccurrence is `--flag <value>`: exactly one operand per
	// occurrence, so several values mean several occurrences and a following
	// operand is never absorbed.
	optionOneValuePerOccurrence providerOptionArity = iota
	// optionVariadicValues is `--flag <values...>`: one occurrence carries every
	// value, and the option keeps consuming operands until an option-looking
	// token or an option terminator stops it.
	optionVariadicValues
)

// providerLaunchGrammar is the part of one provider CLI's argv grammar the Agent
// launch depends on. It is deliberately a description of the upstream contract
// rather than a projmux preference: changing a field here is a claim about what
// the provider's parser does.
type providerLaunchGrammar struct {
	// workingRootFlag carries the workspace CWD when the provider needs it in
	// argv. It is empty for providers that take the working root from the
	// process working directory, which the launch already sets.
	workingRootFlag string
	// additionalRootFlag carries the additional writable roots. It is empty for
	// providers projmux does not give additional roots to at all, which makes a
	// stored additional root a refusal rather than an invented flag.
	additionalRootFlag string
	// additionalRootArity is the arity of additionalRootFlag.
	additionalRootArity providerOptionArity
	// payloadTerminator is the token this provider's parser accepts to end
	// option parsing, so everything after it is an operand. It is emitted
	// before every non-empty payload and is what lets a payload follow a
	// variadic option without being eaten by it. Empty means the provider's
	// argv carries no terminator at all.
	payloadTerminator string
}

// providerLaunchGrammarFor returns the argv grammar of one provider.
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
func providerLaunchGrammarFor(provider string) providerLaunchGrammar {
	switch normalizeAIMode(provider) {
	case aiModeClaude:
		return providerLaunchGrammar{
			additionalRootFlag:  "--add-dir",
			additionalRootArity: optionVariadicValues,
			// Claude's parser is commander, which honors `--`.
			payloadTerminator: "--",
		}
	case aiModeCodex:
		return providerLaunchGrammar{
			workingRootFlag:     "-C",
			additionalRootFlag:  "--add-dir",
			additionalRootArity: optionOneValuePerOccurrence,
		}
	default:
		return providerLaunchGrammar{}
	}
}

// providerLaunchArgs builds the provider argv tail for one Agent launch: the
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
func providerLaunchArgs(provider string, workspace coremetadata.AgentWorkspace, payload []string) ([]string, error) {
	grammar := providerLaunchGrammarFor(provider)
	if len(workspace.AdditionalWritableRoots) > 0 && grammar.additionalRootFlag == "" {
		// resolveAgentWorkspaceFor refuses this at parse time; reaching it here
		// means a stored workspace outlived that gate, and inventing a flag the
		// provider does not have would start a session with silently narrower
		// access than the Agent records.
		return nil, fmt.Errorf("provider %q does not support additional writable roots", provider)
	}

	args := make([]string, 0, len(payload)+3+2*len(workspace.AdditionalWritableRoots))
	if grammar.workingRootFlag != "" && workspace.CWD != "" {
		args = append(args, grammar.workingRootFlag, workspace.CWD)
	}
	variadicTail := false
	if roots := workspace.AdditionalWritableRoots; len(roots) > 0 {
		switch grammar.additionalRootArity {
		case optionVariadicValues:
			args = append(args, grammar.additionalRootFlag)
			args = append(args, roots...)
			variadicTail = true
		default:
			for _, root := range roots {
				args = append(args, grammar.additionalRootFlag, root)
			}
		}
	}
	if len(payload) == 0 {
		return args, nil
	}
	switch {
	case grammar.payloadTerminator != "":
		args = append(args, grammar.payloadTerminator)
	case variadicTail:
		// A provider that ends its workspace options with a variadic option and
		// offers no terminator cannot express this argv at all. Emitting it
		// anyway would start a session whose task silently became a directory,
		// which is exactly the failure this seam exists to stop.
		return nil, fmt.Errorf("provider %q cannot carry an initial task after %s", provider, grammar.additionalRootFlag)
	}
	return append(args, payload...), nil
}
