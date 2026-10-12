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
"github.com/crevissepartners/projmux/internal/core/registryview"
"github.com/crevissepartners/projmux/internal/diagnostics"
"github.com/crevissepartners/projmux/internal/i18n"
"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
"github.com/crevissepartners/projmux/internal/integrations/tmuxexec"
"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
"github.com/crevissepartners/projmux/internal/version"
"io"
"maps"
"os"
"os/exec"
"path/filepath"
"runtime"
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

type agentLaunchPlan struct {
	// title is the agent pane title, also the seed of the pane's topic option.
	title string
	// command is the argv for a caller that runs the launch in place.
	command []string
	// commandArgs is the argv for a caller that hands the launch to a split.
	commandArgs []string
}

func (c *aiCommand) buildAgentTitle(mode, contextDir string) string {
	switch mode {
	case aiModeClaude:
		if title := strings.TrimSpace(c.env("CLAUDE_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if contextDir != "" {
			return "claude:" + filepath.Base(contextDir)
		}
		return "claude"
	case aiModeCodex:
		if title := strings.TrimSpace(c.env("CODEX_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if contextDir != "" {
			return "codex:" + filepath.Base(contextDir)
		}
		return "codexcli"
	case aiModeAntigravity:
		if title := strings.TrimSpace(c.env("ANTIGRAVITY_THREAD_TITLE")); title != "" {
			return "antigravity:" + title
		}
		if title := strings.TrimSpace(c.env("AI_THREAD_TITLE")); title != "" {
			return "antigravity:" + title
		}
		if contextDir != "" {
			return "antigravity:" + filepath.Base(contextDir)
		}
		return "antigravity"
	default:
		return mode
	}
}

func (c *aiCommand) agentSplitCommand(mode, pathPrepend, contextDir, title string, execArgv []string) ([]string, []string, error) {
	command := c.agentLaunchCommandForArgv(mode, pathPrepend, contextDir, title, execArgv)
	commandShell := posixCommandShell(c.lookupEnv)
	return append([]string{commandShell, "-lc"}, command), []string{commandShell, "-lc", command}, nil
}

func (c *aiCommand) agentLaunchCommandForArgv(mode, pathPrepend, contextDir, title string, execArgv []string) string {
	titleVar := "__" + mode + "_title"
	parts := []string{}
	// nvm/fnm/asdf/volta colocate `node` with the agent CLI; non-interactive
	// login shells may not source their init scripts, so without this prepend
	// the agent's `env node` shebang can fail and the pane exits immediately.
	if pathPrepend != "" && pathPrepend != "." && pathPrepend != "/" {
		parts = append(parts, "export PATH="+shellQuote(pathPrepend)+`":$PATH"`)
	}
	if contextDir != "" {
		parts = append(parts, "cd "+shellQuote(contextDir))
	}
	execParts := make([]string, 0, len(execArgv)+1)
	execParts = append(execParts, "exec")
	for _, arg := range execArgv {
		execParts = append(execParts, shellQuote(arg))
	}
	parts = append(parts,
		titleVar+"="+shellQuote(title),
		`printf '\033]0;%s\007' "$`+titleVar+`"`,
		strings.Join(execParts, " "),
	)
	return strings.Join(parts, " && ")
}

func (c *aiCommand) findAgentBinary(mode string) string {
	provider, ok := aiprovider.Lookup(mode)
	if !ok || !provider.PickerEligible || strings.TrimSpace(provider.BinaryName) == "" {
		return ""
	}
	binName := provider.BinaryName

	home := c.homeOrEmpty()
	if path := firstExecutable(
		c.readTrimmed("command", "-v", binName),
		filepath.Join(home, ".npm-global", "bin", binName),
		filepath.Join(home, ".local", "bin", binName),
	); path != "" {
		return path
	}
	if path := newestExecutable(nodeManagerCandidates(home, binName)); path != "" {
		return path
	}
	if provider.ID == aiprovider.Codex {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		return newestExecutable(matches)
	}
	return ""
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func firstExecutable(paths ...string) string {
	for _, path := range paths {
		if isExecutable(path) {
			return path
		}
	}
	return ""
}

// nodeManagerCandidates lists the per-version bin paths that common Node
// version managers install an npm-distributed Agent CLI into.
func nodeManagerCandidates(home, binName string) []string {
	if home == "" || binName == "" {
		return nil
	}
	var candidates []string
	globs := []string{
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin", binName),
		filepath.Join(home, ".fnm", "node-versions", "*", "installation", "bin", binName),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", binName),
	}
	for _, pattern := range globs {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	return append(candidates, filepath.Join(home, ".volta", "bin", binName))
}

func newestExecutable(paths []string) string {
	var newestPath string
	var newestMod time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if newestPath == "" || info.ModTime().After(newestMod) {
			newestPath = path
			newestMod = info.ModTime()
		}
	}
	return newestPath
}

// planAgentLaunch builds the launch for one provider without creating anything.
//
// A failure here -- a missing agent binary, an unusable exec argv -- happens
// before any mutation, which is what lets the create routes guarantee a failed
// launch leaves zero resources behind.
func (c *aiCommand) planAgentLaunch(mode, contextDir string, extraArgs, execArgvOverride []string, pathPrependOverride string) (agentLaunchPlan, error) {
	execArgv := append([]string(nil), execArgvOverride...)
	pathPrepend := pathPrependOverride
	if len(execArgv) == 0 {
		var err error
		execArgv, pathPrepend, err = c.agentExecArgv(mode, extraArgs)
		if err != nil {
			return agentLaunchPlan{}, err
		}
	}
	title := c.buildAgentTitle(mode, contextDir)
	command, commandArgs, err := c.agentSplitCommand(mode, pathPrepend, contextDir, title, execArgv)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	return agentLaunchPlan{title: title, command: command, commandArgs: commandArgs}, nil
}

func (c *aiCommand) PlanClaudeDialogueLaunch(workspace coremetadata.AgentWorkspace, conversation string) (string, []string, error) {
	if err := requireClaudeDialogueMode(aiModeClaude, true, nil); err != nil {
		return "", nil, err
	}
	provider := c.findAgentBinary(aiModeClaude)
	if provider == "" {
		return "", nil, errors.New(c.missingAgentRunnerMessage(aiModeClaude))
	}
	candidate, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	native, err := providerLaunchArgs(aiModeClaude, workspace, nil)
	if err != nil {
		return "", nil, err
	}
	if conversation != "" {
		resume, err := resumeArgsForAgent(aiModeClaude, conversation)
		if err != nil {
			return "", nil, err
		}
		native = append(native, resume[1:]...)
	}
	argv := append([]string{candidate, "internal", "claude-dialogue-exec", "--", provider}, native...)
	plan, err := c.planAgentLaunch(aiModeClaude, workspace.CWD, nil, argv, filepath.Dir(provider))
	if err != nil {
		return "", nil, fmt.Errorf("claude reply-only launch: %w", err)
	}
	return plan.title, plan.commandArgs, nil
}
