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






