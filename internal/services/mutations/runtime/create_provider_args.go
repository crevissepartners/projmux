package runtime

import (
"bytes"
"context"
"encoding/base64"
"encoding/binary"
"encoding/json"
"errors"
"flag"
"fmt"
"github.com/crevissepartners/projmux/internal/aiprovider"
"github.com/crevissepartners/projmux/internal/app/initcmd"
"github.com/crevissepartners/projmux/internal/app/keybinding"
"github.com/crevissepartners/projmux/internal/cli"
"github.com/crevissepartners/projmux/internal/config"
"github.com/crevissepartners/projmux/internal/core/aibadge"
"github.com/crevissepartners/projmux/internal/core/notify"
"github.com/crevissepartners/projmux/internal/core/persona"
"github.com/crevissepartners/projmux/internal/core/profile"
"github.com/crevissepartners/projmux/internal/core/registryview"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/i18n"
"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
"github.com/crevissepartners/projmux/internal/version"
"io"
"maps"
"os"
"os/exec"
"path/filepath"
"regexp"
"slices"
"strings"
"sync"
"syscall"
"time"
"unicode/utf16"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// providerOptionArity is how many operands one provider option consumes.
type providerOptionArity int

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

// codexLaunchOptionArgs keeps provider options ahead of the resume subcommand
// and workspace arguments. Codex accepts model and effort on both CLI lanes.
func codexLaunchOptionArgs(model, effort string) []string {
	var args []string
	if model != "" {
		args = append(args, "-m", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	return args
}

// codexCLIProfileArgs carries a validated profile policy on Codex's plain
// create and CLI resume lanes. The profile's full-access value was already
// translated to danger-full-access by codexThreadPolicy.
func codexCLIProfileArgs(policy codexappserver.ThreadPolicy) []string {
	var args []string
	if policy.Sandbox != "" {
		args = append(args, "-s", string(policy.Sandbox))
	}
	if policy.ApprovalPolicy != "" {
		args = append(args, "-a", string(policy.ApprovalPolicy))
	}
	return args
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

// claudeSettingsArgs spells the Claude settings snapshot a profile's
// permissions are launched with. Only the path reaches argv.
func claudeSettingsArgs(settingsFile string) []string {
	if settingsFile == "" {
		return nil
	}
	return []string{"--settings", settingsFile}
}

// PlanAgentLaunch builds the provider launch for one detached Agent create.
//
// The workspace and the initial task are assembled by providerLaunchArgs rather
// than concatenated here, because where the workspace options stop is a property
// of the provider's own parser: Claude's variadic `--add-dir` eats a payload
// appended straight after it and starts a session with no task at all.
func (c *aiCommand) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (title string, argv []string, err error) {
	extra, err := providerLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	plan, err := c.planAgentLaunch(provider, workspace.CWD, extra, nil, "")
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

// PlanAgentLaunchWithOptions applies model and effort to Claude or Codex.
// Claude alone accepts the persona snapshot path. Options precede workspace
// arguments so Claude's variadic --add-dir cannot consume them.
func (c *aiCommand) PlanAgentLaunchWithOptions(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile string) (title string, argv []string, err error) {
	return c.PlanAgentLaunchWithSettings(provider, workspace, payload, model, effort, personaFile, "")
}

// PlanAgentLaunchWithSettings is PlanAgentLaunchWithOptions that also passes
// a profile's Claude settings snapshot as --settings. An empty settingsFile
// adds nothing, which is exactly PlanAgentLaunchWithOptions.
func (c *aiCommand) PlanAgentLaunchWithSettings(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile, settingsFile string) (title string, argv []string, err error) {
	mode := normalizeAIMode(provider)
	if mode != aiModeClaude && mode != aiModeCodex {
		return "", nil, fmt.Errorf("provider %q does not accept --model, --effort, or --instructions", provider)
	}
	if mode == aiModeCodex && (personaFile != "" || settingsFile != "") {
		return "", nil, fmt.Errorf("provider %q does not accept Claude instructions or settings options", provider)
	}
	extra, err := providerLaunchArgs(provider, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	options := append(claudeLaunchOptionArgs(model, effort, personaFile), claudeSettingsArgs(settingsFile)...)
	if mode == aiModeCodex {
		options = codexLaunchOptionArgs(model, effort)
	}
	extra = append(options, extra...)
	plan, err := c.planAgentLaunch(provider, workspace.CWD, extra, nil, "")
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}

// PlanCodexAgentLaunchWithPolicy plans a plain Codex CLI create with the
// profile permissions ahead of workspace options and any prompt.
func (c *aiCommand) PlanCodexAgentLaunchWithPolicy(workspace coremetadata.AgentWorkspace, payload []string, model, effort string, policy codexappserver.ThreadPolicy) (string, []string, error) {
	extra, err := providerLaunchArgs(aiModeCodex, workspace, payload)
	if err != nil {
		return "", nil, err
	}
	options := append(codexLaunchOptionArgs(model, effort), codexCLIProfileArgs(policy)...)
	plan, err := c.planAgentLaunch(aiModeCodex, workspace.CWD, append(options, extra...), nil, "")
	if err != nil {
		return "", nil, err
	}
	return plan.title, plan.commandArgs, nil
}
