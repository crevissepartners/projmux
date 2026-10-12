package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AgentCommandPlanner struct {
	ExecArgv              func(string, []string) ([]string, string, error)
	LookupEnv             func(string) string
	HomeDir, CommandShell string
	LookupBinary          func(string) string
}
type AgentLaunchPlan struct {
	// title is the agent pane title, also the seed of the pane's topic option.
	Title string
	// command is the argv for a caller that runs the launch in place.
	Command []string
	// commandArgs is the argv for a caller that hands the launch to a split.
	CommandArgs []string
}

// PlanLaunch builds the launch for one provider without creating anything.
//
// A failure here -- a missing agent binary, an unusable exec argv -- happens
// before any mutation, which is what lets the create routes guarantee a failed
// launch leaves zero resources behind.
func (c *AgentCommandPlanner) PlanLaunch(mode, contextDir string, extraArgs, execArgvOverride []string, pathPrependOverride string) (AgentLaunchPlan, error) {
	execArgv := append([]string(nil), execArgvOverride...)
	pathPrepend := pathPrependOverride
	if len(execArgv) == 0 {
		var err error
		execArgv, pathPrepend, err = c.ExecArgv(mode, extraArgs)
		if err != nil {
			return AgentLaunchPlan{}, err
		}
	}
	title := c.BuildAgentTitle(mode, contextDir)
	command, commandArgs, err := c.AgentSplitCommand(mode, pathPrepend, contextDir, title, execArgv)
	if err != nil {
		return AgentLaunchPlan{}, err
	}
	return AgentLaunchPlan{Title: title, Command: command, CommandArgs: commandArgs}, nil
}

func (c *AgentCommandPlanner) BuildAgentTitle(mode, contextDir string) string {
	switch mode {
	case "claude":
		if title := strings.TrimSpace(c.LookupEnv("CLAUDE_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if title := strings.TrimSpace(c.LookupEnv("AI_THREAD_TITLE")); title != "" {
			return "claude:" + title
		}
		if contextDir != "" {
			return "claude:" + filepath.Base(contextDir)
		}
		return "claude"
	case "codex":
		if title := strings.TrimSpace(c.LookupEnv("CODEX_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if title := strings.TrimSpace(c.LookupEnv("AI_THREAD_TITLE")); title != "" {
			return "codex:" + title
		}
		if contextDir != "" {
			return "codex:" + filepath.Base(contextDir)
		}
		return "codexcli"
	case "antigravity":
		if title := strings.TrimSpace(c.LookupEnv("ANTIGRAVITY_THREAD_TITLE")); title != "" {
			return "antigravity:" + title
		}
		if title := strings.TrimSpace(c.LookupEnv("AI_THREAD_TITLE")); title != "" {
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

func (c *AgentCommandPlanner) AgentSplitCommand(mode, pathPrepend, contextDir, title string, execArgv []string) ([]string, []string, error) {
	command := c.AgentLaunchCommandForArgv(mode, pathPrepend, contextDir, title, execArgv)
	commandShell := c.CommandShell
	return append([]string{commandShell, "-lc"}, command), []string{commandShell, "-lc", command}, nil
}

func (c *AgentCommandPlanner) AgentLaunchCommandForArgv(mode, pathPrepend, contextDir, title string, execArgv []string) string {
	titleVar := "__" + mode + "_title"
	parts := []string{}
	// nvm/fnm/asdf/volta colocate `node` with the agent CLI; non-interactive
	// login shells may not source their init scripts, so without this prepend
	// the agent's `env node` shebang can fail and the pane exits immediately.
	if pathPrepend != "" && pathPrepend != "." && pathPrepend != "/" {
		parts = append(parts, "export PATH="+ShellQuote(pathPrepend)+`":$PATH"`)
	}
	if contextDir != "" {
		parts = append(parts, "cd "+ShellQuote(contextDir))
	}
	execParts := make([]string, 0, len(execArgv)+1)
	execParts = append(execParts, "exec")
	for _, arg := range execArgv {
		execParts = append(execParts, ShellQuote(arg))
	}
	parts = append(parts,
		titleVar+"="+ShellQuote(title),
		`printf '\033]0;%s\007' "$`+titleVar+`"`,
		strings.Join(execParts, " "),
	)
	return strings.Join(parts, " && ")
}

func (c *AgentCommandPlanner) FindAgentBinary(mode string) string {
	provider, ok := aiprovider.Lookup(mode)
	if !ok || !provider.PickerEligible || strings.TrimSpace(provider.BinaryName) == "" {
		return ""
	}
	binName := provider.BinaryName

	home := c.HomeDir
	if path := FirstExecutable(
		c.LookupBinary(binName),
		filepath.Join(home, ".npm-global", "bin", binName),
		filepath.Join(home, ".local", "bin", binName),
	); path != "" {
		return path
	}
	if path := NewestExecutable(NodeManagerCandidates(home, binName)); path != "" {
		return path
	}
	if provider.ID == aiprovider.Codex {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-*", "bin", "*", "codex"))
		return NewestExecutable(matches)
	}
	return ""
}

func IsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func FirstExecutable(paths ...string) string {
	for _, path := range paths {
		if IsExecutable(path) {
			return path
		}
	}
	return ""
}

// NodeManagerCandidates lists the per-version bin paths that common Node
// version managers install an npm-distributed Agent CLI into.
func NodeManagerCandidates(home, binName string) []string {
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

func NewestExecutable(paths []string) string {
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

func (c *AgentCommandPlanner) PlanDialogueLaunch(workspace coremetadata.AgentWorkspace, conversation string, executable func() (string, error)) (string, []string, error) {
	if err := (AgentInputFields{}).RequireDialogueMode("claude", true, nil); err != nil {
		return "", nil, err
	}
	provider := c.FindAgentBinary("claude")
	if provider == "" {
		return "", nil, errors.New("selected runner is not installed: claude")
	}
	candidate, err := executable()
	if err != nil {
		return "", nil, err
	}
	native, err := ProviderLaunchArgs("claude", workspace, nil)
	if err != nil {
		return "", nil, err
	}
	if conversation != "" {
		resume, err := claude.ResumeArgs(conversation)
		if err != nil {
			return "", nil, err
		}
		native = append(native, resume[1:]...)
	}
	argv := append([]string{candidate, "internal", "claude-dialogue-exec", "--", provider}, native...)
	plan, err := c.PlanLaunch("claude", workspace.CWD, nil, argv, filepath.Dir(provider))
	if err != nil {
		return "", nil, fmt.Errorf("claude reply-only launch: %w", err)
	}
	return plan.Title, plan.CommandArgs, nil
}
