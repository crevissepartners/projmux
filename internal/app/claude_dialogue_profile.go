package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// This mode belongs to one public create/resume invocation, never AgentSpec or
// the inherited environment. Every new generation needs another explicit opt-in.
const claudeDialogueReplyOnlyFlag = "dialogue-reply-only"

type claudeDialogueLauncher interface {
	PlanClaudeDialogueLaunch(coremetadata.AgentWorkspace, string) (string, []string, error)
}

func requireClaudeDialogueMode(provider string, enabled bool, payload []string) error {
	if !enabled {
		return nil
	}
	if provider != aiModeClaude {
		return usageError("--dialogue-reply-only requires a Claude Agent/provider")
	}
	if len(payload) != 0 {
		return usageError("--dialogue-reply-only starts with a fixed readiness turn and accepts no initial payload")
	}
	if runtime.GOOS != "linux" {
		return errors.New("claude reply-only activation requires Linux process birth and pinned executable support")
	}
	return nil
}

func hasClaudeDialogueModeFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--"+claudeDialogueReplyOnlyFlag || arg == "-"+claudeDialogueReplyOnlyFlag || strings.HasPrefix(arg, "--"+claudeDialogueReplyOnlyFlag+"=") || strings.HasPrefix(arg, "-"+claudeDialogueReplyOnlyFlag+"=") {
			return true
		}
	}
	return false
}

const internalClaudeDialogueProfileEnv = "PMX_INTERNAL_CLAUDE_DIALOGUE_PROFILE"
const claudeDialoguePrefixName = "projmux-claude-reply-prefix"

type claudeDialogueProfile struct {
	Version    int    `json:"version"`
	AgentUID   string `json:"agentUID"`
	PaneUID    string `json:"paneUID"`
	Generation string `json:"generation"`
	Candidate  string `json:"candidate"`
}

func claudeDialogueProfilePath(spec superviseSpec) (string, error) {
	if exactActivationRegistryPath(spec.RegistryPath) != nil || !spec.DialogueReplyOnly {
		return "", errClaudeReplyTool
	}
	for _, value := range []string{spec.AgentUID, spec.PaneUID, spec.Generation} {
		if value == "" || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, "\\\r\n\x00") {
			return "", errClaudeReplyTool
		}
	}
	return filepath.Join(filepath.Dir(filepath.Dir(spec.RegistryPath)), "claude-dialogue", spec.PaneUID+"_"+spec.Generation), nil
}

func (p claudeDialogueProfile) files() (map[string][]byte, error) {
	hook := func(args ...string) map[string]any {
		return map[string]any{"type": "command", "command": p.Candidate, "args": args, "timeout": 5}
	}
	state := func() map[string]any {
		return hook("internal", "agent-hook", "ingest", "claude-hook", "--pane="+p.PaneUID)
	}
	entry := func(callbacks ...map[string]any) []any { return []any{map[string]any{"hooks": callbacks}} }
	settings := map[string]any{"hooks": map[string]any{
		"SessionStart":     entry(state(), hook("internal", "claude-endpoint-register")),
		"UserPromptSubmit": entry(state()), "Stop": entry(state()),
		"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{hook("internal", "claude-reply-tool", "prepare")}}},
	}, "permissions": map[string]any{"defaultMode": "dontAsk"}}
	result := map[string][]byte{"mcp.json": []byte("{\"mcpServers\":{}}\n")}
	for name, value := range map[string]any{"profile.json": p, "settings.json": settings} {
		body, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		result[name] = append(body, '\n')
	}
	return result, nil
}

func createClaudeDialogueProfile(spec superviseSpec, candidate string) (string, error) {
	directory, err := claudeDialogueProfilePath(spec)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(candidate)
	if err != nil || !filepath.IsAbs(candidate) || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !localipc.OwnedByCurrentUser(info) {
		return "", errClaudeReplyTool
	}
	base := filepath.Dir(directory)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", errClaudeReplyTool
	}
	baseInfo, err := os.Lstat(base)
	if err != nil || !baseInfo.IsDir() || !localipc.OwnedByCurrentUser(baseInfo) || baseInfo.Mode().Perm()&0o077 != 0 {
		return "", errClaudeReplyTool
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", errClaudeReplyTool
	}
	ownedInfo, err := os.Lstat(directory)
	if err != nil {
		return "", errClaudeReplyTool
	}
	complete := false
	defer func() {
		if !complete {
			_ = removeClaudeDialogueProfileFiles(directory, ownedInfo, true)
		}
	}()
	profile := claudeDialogueProfile{Version: 1, AgentUID: spec.AgentUID, PaneUID: spec.PaneUID, Generation: spec.Generation, Candidate: candidate}
	files, err := profile.files()
	if err != nil {
		return "", err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			return "", errClaudeReplyTool
		}
	}
	// SHELL_PREFIX is an official executable path. A fixed alias selects the
	// candidate's narrow dispatch; no mutable shell script or vendor FD contract.
	if err := os.Symlink(candidate, filepath.Join(directory, claudeDialoguePrefixName)); err != nil {
		return "", errClaudeReplyTool
	}
	if _, err := readClaudeDialogueProfile(directory, candidate); err != nil {
		return "", err
	}
	complete = true
	return directory, nil
}

func readClaudeDialogueProfile(directory, candidate string) (claudeDialogueProfile, error) {
	refused := func() (claudeDialogueProfile, error) { return claudeDialogueProfile{}, errClaudeReplyTool }
	dirInfo, err := os.Lstat(directory)
	if err != nil || !filepath.IsAbs(directory) || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0o077 != 0 || !localipc.OwnedByCurrentUser(dirInfo) {
		return refused()
	}
	read := func(name string) ([]byte, error) { return readClaudeDialoguePrivateFile(directory, name) }
	body, err := read("profile.json")
	if err != nil {
		return refused()
	}
	var profile claudeDialogueProfile
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&profile) != nil || decoder.Decode(new(any)) != io.EOF || profile.Version != 1 || profile.Candidate != candidate || profile.AgentUID == "" || profile.PaneUID == "" || profile.Generation == "" {
		return refused()
	}
	if filepath.Base(directory) != profile.PaneUID+"_"+profile.Generation {
		return refused()
	}
	files, err := profile.files()
	if err != nil {
		return refused()
	}
	for name, expected := range files {
		actual, err := read(name)
		if err != nil || !bytes.Equal(actual, expected) {
			return refused()
		}
	}
	prefix := filepath.Join(directory, claudeDialoguePrefixName)
	target, err := os.Readlink(prefix)
	if err != nil || target != candidate {
		return refused()
	}
	aliasInfo, err := os.Stat(prefix)
	if err != nil {
		return refused()
	}
	candidateInfo, err := os.Lstat(candidate)
	if err != nil || !candidateInfo.Mode().IsRegular() || candidateInfo.Mode().Perm()&0o022 != 0 || !localipc.OwnedByCurrentUser(candidateInfo) || !os.SameFile(aliasInfo, candidateInfo) {
		return refused()
	}
	return profile, nil
}

func claudeDialoguePrefixArgs(argv0, profile string, args []string) ([]string, bool) {
	if filepath.Base(argv0) != claudeDialoguePrefixName {
		return args, false
	}
	// A malformed alias invocation has no fallback to general CLI dispatch.
	if profile == "" || argv0 != filepath.Join(profile, claudeDialoguePrefixName) || len(args) != 1 {
		return []string{"internal", "claude-reply-tool", "execute"}, true
	}
	return append([]string{"internal", "claude-reply-tool", "execute"}, args...), true
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

// A next-activation opt-in never leaks through the inherited environment.
// Preserve an ordinary user's official shell prefix outside this mode.
func withoutClaudeDialoguePolicy(environment []string) []string {
	output := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != internalClaudeReplyGuardEnv && key != internalClaudeDialogueProfileEnv {
			output = append(output, entry)
		}
	}
	return output
}

func readClaudeDialoguePrivateFile(directory, name string) ([]byte, error) {
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- exact private profile and closed internal filename; nofollow/nonblock then owned regular-file and size checks before bounded read.
	if err != nil {
		return nil, errClaudeReplyTool
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !localipc.OwnedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 || info.Size() > 16384 {
		return nil, errClaudeReplyTool
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 {
		return nil, errClaudeReplyTool
	}
	return data, nil
}

func readClaudeDialogueObserver(directory string) (coremetadata.ProcessIdentity, error) {
	data, err := readClaudeDialoguePrivateFile(directory, "observer.json")
	if err != nil {
		return coremetadata.ProcessIdentity{}, err
	}
	var identity coremetadata.ProcessIdentity
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil || decoder.Decode(new(any)) != io.EOF || !identity.Valid() {
		return coremetadata.ProcessIdentity{}, errClaudeReplyTool
	}
	return identity, nil
}

func removeClaudeDialogueProfileFiles(directory string, expected os.FileInfo, allowMissing bool) error {
	current, err := os.Lstat(directory)
	if err != nil || !current.IsDir() || !os.SameFile(current, expected) {
		return errClaudeReplyTool
	}
	for _, name := range []string{"profile.json", "settings.json", "mcp.json", "observer.json", claudeDialoguePrefixName} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !(allowMissing && errors.Is(err, os.ErrNotExist)) {
			return errClaudeReplyTool
		}
	}
	if err := os.Remove(directory); err != nil {
		return errors.New("reply-only profile contains unexpected residue")
	}
	_ = os.Remove(filepath.Dir(directory))
	return nil
}

var errClaudeDialogueCleanup = errors.New("reply-only observer cleanup failed; profile retained")

func claudeDialogueNativeEnvironment(inherited []string, profile string) []string {
	output := []string{}
	for _, entry := range claudeHelperEnvironment(inherited) {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CLAUDE_CODE_SHELL_PREFIX" && key != "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC" {
			output = append(output, entry)
		}
	}
	return append(output, "CLAUDE_CODE_SHELL_PREFIX="+filepath.Join(profile, claudeDialoguePrefixName), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}
