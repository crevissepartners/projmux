package profile

import "slices"

// claudeModels is the ordered list of Claude model names projmux suggests for
// `model` and `create --model`. It is a suggestion, not an allowlist: any name
// ModelName accepts is still passed through. fable, opus, and sonnet are the
// aliases Claude Code 2.1.282 `claude --help` names for --model; haiku is an
// alias from the Claude Code model configuration docs.
var claudeModels = []string{"fable", "opus", "sonnet", "haiku"}

// ClaudeModels returns a copy of the suggested Claude model names, in order.
func ClaudeModels() []string {
	return slices.Clone(claudeModels)
}
