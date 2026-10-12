package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

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

const optionVariadicValues providerOptionArity = providerOptionArity(mutationruntime.OptionVariadicValues)

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
	return mutationruntime.ProviderLaunchArgs(provider, workspace, payload)
}
