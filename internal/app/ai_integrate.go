package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexConfigRelativePath = ".codex/config.toml"
	codexNotifyMarkerBegin  = "# >>> projmux managed codex legacy notify"
	codexNotifyMarkerEnd    = "# <<< projmux managed codex legacy notify"
	codexNotifyLine         = `notify = ["projmux", "ai", "ingest", "codex-notify"]`
	codexHooksMarkerBegin   = "# >>> projmux managed codex hooks"
	codexHooksMarkerEnd     = "# <<< projmux managed codex hooks"
	codexHookCommand        = "projmux ai ingest codex-hook >/dev/null 2>&1 || true"

	claudeSettingsRelativePath = ".claude/settings.json"
	claudeHookManagedMarker    = "projmux-managed:claude-hook:v1"
	claudeHookCommand          = "projmux ai ingest claude-hook >/dev/null 2>&1 || true # " + claudeHookManagedMarker

	tmuxBellManagedMarker = "projmux-managed:tmux-bell:v1"
	tmuxBellHookName      = "pane-bell-event"
	tmuxBellHookCommand   = `run-shell -b 'projmux ai ingest bell --pane "#{pane_id}" >/dev/null 2>&1 || true # ` + tmuxBellManagedMarker + `'`
)

type codexNotifyPlan struct {
	path     string
	current  string
	next     string
	action   string
	changed  bool
	conflict string
}

type codexIntegrationMode string

const (
	codexIntegrationLegacyNotify codexIntegrationMode = "legacy-notify"
	codexIntegrationHooks        codexIntegrationMode = "hooks"
)

type claudeHookPlan struct {
	path     string
	current  string
	next     string
	action   string
	changed  bool
	conflict string
}

type tmuxBellPlan struct {
	installCommands [][]string
	removeCommands  [][]string
	action          string
	changed         bool
}

var claudeHookEvents = []string{
	"Notification",
	"Stop",
	"UserPromptSubmit",
	"PermissionRequest",
	"StopFailure",
	"SubagentStop",
	"TeammateIdle",
}

var codexHookEvents = []string{
	"PermissionRequest",
	"UserPromptSubmit",
	"Stop",
}

func (c *aiCommand) runIntegrate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate requires <agent-kind>")
	}
	switch args[0] {
	case "codex":
		return c.runIntegrateCodex(args[1:], stdout, stderr)
	case "claude":
		return c.runIntegrateClaude(args[1:], stdout, stderr)
	case "tmux-bell":
		return c.runIntegrateTmuxBell(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printAIUsage(stdout)
		return nil
	default:
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai integrate agent-kind: %s", args[0])
	}
}

func (c *aiCommand) runIntegrateTmuxBell(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai integrate tmux-bell", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print planned tmux bell integration commands without writing")
	remove := fs.Bool("remove", false, "remove projmux-managed tmux bell hook wiring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate tmux-bell does not accept positional arguments")
	}

	plan := c.planTmuxBellIntegration(*remove)
	if *dryRun {
		return printTmuxBellDryRun(stdout, plan)
	}
	commands := plan.installCommands
	if *remove {
		commands = plan.removeCommands
	}
	for _, args := range commands {
		if err := c.run("tmux", args...); err != nil {
			return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
	}
	_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
	return err
}

func (c *aiCommand) runIntegrateClaude(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai integrate claude", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print planned Claude Code hook settings changes without writing")
	remove := fs.Bool("remove", false, "remove projmux-managed Claude Code hook wiring")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate claude does not accept positional arguments")
	}

	plan, err := c.planClaudeHookIntegration(*remove)
	if err != nil {
		return err
	}
	if *dryRun {
		return printClaudeHookDryRun(stdout, plan)
	}
	if plan.conflict != "" {
		return errors.New(plan.conflict + "; run `projmux ai integrate claude --dry-run` to preview without writing")
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	if err := c.writeClaudeSettings(plan.path, []byte(plan.next)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", plan.action)
	return err
}

func (c *aiCommand) runIntegrateCodex(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ai integrate codex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print planned Codex config changes without writing")
	remove := fs.Bool("remove", false, "remove projmux-managed Codex wiring")
	modeFlag := fs.String("mode", string(codexIntegrationLegacyNotify), "Codex integration mode: legacy-notify or hooks")
	modeExplicit := flagWasProvided(args, "mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate codex does not accept positional arguments")
	}

	mode := codexIntegrationMode(strings.TrimSpace(*modeFlag))
	switch mode {
	case codexIntegrationLegacyNotify, codexIntegrationHooks:
	default:
		return fmt.Errorf("unknown Codex integration mode %q (want legacy-notify or hooks)", *modeFlag)
	}

	removeAllManaged := *remove && !modeExplicit
	plan, err := c.planCodexIntegration(mode, *remove, removeAllManaged)
	if err != nil {
		return err
	}
	if *dryRun {
		return printCodexIntegrationDryRun(stdout, plan)
	}
	if plan.conflict != "" {
		return errors.New(plan.conflict + "; run `projmux ai integrate codex --dry-run` to preview without writing")
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	if err := c.writeCodexConfig(plan.path, []byte(plan.next)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", plan.action)
	return err
}

func (c *aiCommand) planCodexIntegration(mode codexIntegrationMode, remove, removeAllManaged bool) (codexNotifyPlan, error) {
	if mode == codexIntegrationHooks || removeAllManaged {
		return c.planCodexHooksIntegration(remove, removeAllManaged)
	}
	return c.planCodexNotifyIntegration(remove)
}

func (c *aiCommand) planCodexNotifyIntegration(remove bool) (codexNotifyPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return codexNotifyPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	current, err := c.readCodexConfig(path)
	if err != nil {
		return codexNotifyPlan{}, err
	}

	withoutManaged, hadManaged, err := removeCodexNotifyManagedBlocks(current)
	if err != nil {
		return codexNotifyPlan{}, err
	}
	plan := codexNotifyPlan{path: path, current: current, next: withoutManaged}

	if remove {
		plan.changed = hadManaged
		if hadManaged {
			plan.action = "removed projmux-managed Codex legacy notify wiring from " + path
		} else {
			plan.action = "no changes: projmux-managed Codex legacy notify wiring is not present in " + path
		}
		return plan, nil
	}

	if line, ok := findUnmanagedNotifyLine(withoutManaged); ok {
		plan.conflict = fmt.Sprintf("Codex notify is already configured outside a projmux-managed block in %s: %s", path, line)
		plan.action = "would refuse to install Codex legacy notify wiring"
		return plan, nil
	}

	next := codexNotifyBlock() + withoutManaged
	plan.next = next
	plan.changed = next != current
	if plan.changed {
		plan.action = "configured Codex legacy notify in " + path
	} else {
		plan.action = "no changes: Codex legacy notify is already configured in " + path
	}
	return plan, nil
}

func (c *aiCommand) planCodexHooksIntegration(remove, removeAllManaged bool) (codexNotifyPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return codexNotifyPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	current, err := c.readCodexConfig(path)
	if err != nil {
		return codexNotifyPlan{}, err
	}

	withoutHooks, hadHooks, err := removeManagedBlock(current, codexHooksMarkerBegin, codexHooksMarkerEnd, "Codex config contains an unterminated projmux-managed hooks block")
	if err != nil {
		return codexNotifyPlan{}, err
	}
	withoutManaged := withoutHooks
	hadNotify := false
	if removeAllManaged {
		withoutManaged, hadNotify, err = removeCodexNotifyManagedBlocks(withoutManaged)
		if err != nil {
			return codexNotifyPlan{}, err
		}
	}
	plan := codexNotifyPlan{path: path, current: current, next: withoutManaged}

	if remove {
		plan.changed = (hadHooks || hadNotify) && withoutManaged != current
		switch {
		case hadHooks && hadNotify:
			plan.action = "removed projmux-managed Codex legacy notify and hooks wiring from " + path
		case hadHooks:
			plan.action = "removed projmux-managed Codex hooks wiring from " + path
		case hadNotify:
			plan.action = "removed projmux-managed Codex legacy notify wiring from " + path
		default:
			plan.action = "no changes: projmux-managed Codex wiring is not present in " + path
		}
		return plan, nil
	}

	if line, ok := findUnmanagedCodexHooksLine(withoutManaged); ok {
		plan.conflict = fmt.Sprintf("Codex hooks are already configured outside a projmux-managed block in %s: %s", path, line)
		plan.action = "would refuse to install Codex hooks wiring"
		return plan, nil
	}
	next := codexHooksBlock() + withoutManaged
	plan.next = next
	plan.changed = next != current
	if plan.changed {
		plan.action = "configured Codex hooks in " + path
	} else {
		plan.action = "no changes: Codex hooks are already configured in " + path
	}
	return plan, nil
}

func (c *aiCommand) planClaudeHookIntegration(remove bool) (claudeHookPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return claudeHookPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, claudeSettingsRelativePath)
	current, err := c.readClaudeSettings(path)
	if err != nil {
		return claudeHookPlan{}, err
	}

	settings, err := parseClaudeSettings(current, path)
	if err != nil {
		return claudeHookPlan{}, err
	}
	hooks, err := claudeSettingsHooks(settings, false)
	if err != nil {
		return claudeHookPlan{}, err
	}
	cleaned := false
	conflict := ""
	if hooks != nil {
		for _, event := range claudeHookEvents {
			nextEntries, removed, eventConflict, err := claudeHookEntriesWithoutManaged(hooks[event], event, path)
			if err != nil {
				return claudeHookPlan{}, err
			}
			cleaned = cleaned || removed
			if eventConflict != "" && conflict == "" {
				conflict = eventConflict
			}
			if len(nextEntries) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = nextEntries
			}
		}
	}

	plan := claudeHookPlan{path: path, current: current, conflict: conflict}
	if remove {
		next, err := encodeClaudeSettings(settings)
		if err != nil {
			return claudeHookPlan{}, err
		}
		plan.next = next
		plan.changed = cleaned && next != current
		if cleaned {
			plan.action = "removed projmux-managed Claude Code hook wiring from " + path
		} else {
			plan.action = "no changes: projmux-managed Claude Code hook wiring is not present in " + path
		}
		return plan, nil
	}
	if conflict != "" {
		plan.action = "would refuse to install Claude Code hook wiring"
		plan.next = current
		return plan, nil
	}

	hooks, err = claudeSettingsHooks(settings, true)
	if err != nil {
		return claudeHookPlan{}, err
	}
	for _, event := range claudeHookEvents {
		hooks[event] = append(claudeHookEntrySlice(hooks[event]), claudeHookManagedEntry())
	}
	next, err := encodeClaudeSettings(settings)
	if err != nil {
		return claudeHookPlan{}, err
	}
	plan.next = next
	plan.changed = next != current
	if plan.changed {
		plan.action = "configured Claude Code hooks in " + path
	} else {
		plan.action = "no changes: Claude Code hooks are already configured in " + path
	}
	return plan, nil
}

func (c *aiCommand) planTmuxBellIntegration(remove bool) tmuxBellPlan {
	hooks := c.readTmuxBellHooks()
	managed := tmuxBellManagedHookTargets(hooks)
	install := [][]string{
		{"set-option", "-g", "allow-passthrough", "on"},
		{"set-option", "-g", "monitor-bell", "on"},
		{"set-option", "-g", "bell-action", "other"},
	}
	if len(managed) == 0 {
		install = append(install, []string{"set-hook", "-ag", tmuxBellHookName, tmuxBellHookCommand})
	}

	removeCommands := make([][]string, 0, len(managed))
	for _, target := range managed {
		removeCommands = append(removeCommands, []string{"set-hook", "-gu", target})
	}

	if remove {
		action := "no changes: projmux-managed tmux bell hook is not present"
		if len(removeCommands) > 0 {
			action = "removed projmux-managed tmux bell hook"
		}
		return tmuxBellPlan{removeCommands: removeCommands, action: action, changed: len(removeCommands) > 0}
	}
	action := "configured tmux bell fallback"
	if len(managed) > 0 {
		action = "refreshed tmux bell fallback options"
	}
	return tmuxBellPlan{installCommands: install, action: action, changed: true}
}

func (c *aiCommand) readTmuxBellHooks() []string {
	out, err := c.read("tmux", "show-hooks", "-g", tmuxBellHookName)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func tmuxBellManagedHookTargets(lines []string) []string {
	targets := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, tmuxBellManagedMarker) {
			continue
		}
		name := strings.Fields(line)
		if len(name) == 0 {
			continue
		}
		target := strings.TrimSpace(name[0])
		if target == tmuxBellHookName {
			target = tmuxBellHookName
		}
		targets = append(targets, target)
	}
	return targets
}

func (c *aiCommand) readCodexConfig(path string) (string, error) {
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err == nil {
		return string(data), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", fmt.Errorf("read Codex config %s: %w", path, err)
}

func (c *aiCommand) writeCodexConfig(path string, data []byte) error {
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Codex config directory: %w", err)
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write Codex config %s: %w", path, err)
	}
	return nil
}

func (c *aiCommand) readClaudeSettings(path string) (string, error) {
	readFile := c.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err == nil {
		return string(data), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", fmt.Errorf("read Claude settings %s: %w", path, err)
}

func (c *aiCommand) writeClaudeSettings(path string, data []byte) error {
	mkdirAll := c.mkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if err := mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Claude settings directory: %w", err)
	}
	writeFile := c.writeFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	if err := writeFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write Claude settings %s: %w", path, err)
	}
	return nil
}

func printCodexNotifyDryRun(stdout io.Writer, plan codexNotifyPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux ai integrate codex (dry-run)\nconfig: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	switch {
	case plan.next == "":
		_, err := fmt.Fprintln(stdout, "would remove projmux-managed Codex legacy notify wiring")
		return err
	case plan.current == "":
		_, err := fmt.Fprintf(stdout, "would create config with managed block:\n%s", codexNotifyBlock())
		return err
	default:
		_, err := fmt.Fprintf(stdout, "would update config to:\n%s", plan.next)
		return err
	}
}

func printCodexIntegrationDryRun(stdout io.Writer, plan codexNotifyPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux ai integrate codex (dry-run)\nconfig: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	switch {
	case plan.next == "":
		_, err := fmt.Fprintln(stdout, "would remove projmux-managed Codex wiring")
		return err
	case plan.current == "":
		_, err := fmt.Fprintf(stdout, "would create config with managed block:\n%s", plan.next)
		return err
	default:
		_, err := fmt.Fprintf(stdout, "would update config to:\n%s", plan.next)
		return err
	}
}

func printClaudeHookDryRun(stdout io.Writer, plan claudeHookPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux ai integrate claude (dry-run)\nsettings: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	switch {
	case plan.current == "":
		_, err := fmt.Fprintf(stdout, "would create settings with managed hooks:\n%s", plan.next)
		return err
	default:
		_, err := fmt.Fprintf(stdout, "would update settings to:\n%s", plan.next)
		return err
	}
}

func printTmuxBellDryRun(stdout io.Writer, plan tmuxBellPlan) error {
	if _, err := fmt.Fprintln(stdout, "projmux ai integrate tmux-bell (dry-run)"); err != nil {
		return err
	}
	commands := plan.installCommands
	if len(plan.removeCommands) > 0 && len(plan.installCommands) == 0 {
		commands = plan.removeCommands
	}
	if !plan.changed {
		_, err := fmt.Fprintf(stdout, "%s\n", plan.action)
		return err
	}
	if _, err := fmt.Fprintf(stdout, "would %s:\n", plan.action); err != nil {
		return err
	}
	for _, args := range commands {
		if _, err := fmt.Fprintf(stdout, "tmux %s\n", strings.Join(args, " ")); err != nil {
			return err
		}
	}
	return nil
}

func codexNotifyBlock() string {
	return strings.Join([]string{
		codexNotifyMarkerBegin,
		codexNotifyLine,
		codexNotifyMarkerEnd,
	}, "\n") + "\n"
}

func codexHooksBlock() string {
	lines := []string{
		codexHooksMarkerBegin,
		"[features]",
		"codex_hooks = true",
		"",
	}
	for _, event := range codexHookEvents {
		lines = append(lines,
			"[[hooks."+event+"]]",
			`command = "`+codexHookCommand+`"`,
			"",
		)
	}
	lines = append(lines, codexHooksMarkerEnd)
	return strings.Join(lines, "\n") + "\n"
}

func removeCodexNotifyManagedBlocks(content string) (string, bool, error) {
	return removeManagedBlock(content, codexNotifyMarkerBegin, codexNotifyMarkerEnd, "Codex config contains an unterminated projmux-managed notify block")
}

func removeManagedBlock(content, begin, end, unterminatedMessage string) (string, bool, error) {
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	removed := false
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != begin {
			out.WriteString(lines[i])
			continue
		}
		removed = true
		foundEnd := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == end {
				foundEnd = true
				break
			}
		}
		if !foundEnd {
			return "", false, errors.New(unterminatedMessage)
		}
	}
	return out.String(), removed, nil
}

func findUnmanagedNotifyLine(content string) (string, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "notify"); ok && strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return trimmed, true
		}
	}
	return "", false
}

func findUnmanagedCodexHooksLine(content string) (string, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "projmux ai ingest codex-hook") {
			return trimmed, true
		}
		if trimmed == "[features]" {
			return trimmed, true
		}
		if rest, ok := strings.CutPrefix(trimmed, "codex_hooks"); ok && strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return trimmed, true
		}
		if strings.HasPrefix(trimmed, "features.codex_hooks") {
			return trimmed, true
		}
	}
	return "", false
}

func flagWasProvided(args []string, name string) bool {
	long := "--" + name
	for _, arg := range args {
		if arg == long || strings.HasPrefix(arg, long+"=") {
			return true
		}
	}
	return false
}

func parseClaudeSettings(content, path string) (map[string]any, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(content), &settings); err != nil {
		return nil, fmt.Errorf("parse Claude settings %s: %w", path, err)
	}
	if settings == nil {
		return map[string]any{}, nil
	}
	return settings, nil
}

func claudeSettingsHooks(settings map[string]any, create bool) (map[string]any, error) {
	value, ok := settings["hooks"]
	if !ok {
		if !create {
			return nil, nil
		}
		hooks := map[string]any{}
		settings["hooks"] = hooks
		return hooks, nil
	}
	hooks, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Claude settings hooks field must be a JSON object")
	}
	return hooks, nil
}

func claudeHookEntriesWithoutManaged(value any, event, path string) ([]any, bool, string, error) {
	if value == nil {
		return nil, false, "", nil
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, false, "", fmt.Errorf("Claude settings %s hook event %s must be an array", path, event)
	}
	out := make([]any, 0, len(entries))
	removed := false
	conflict := ""
	for _, entryValue := range entries {
		entry, ok := entryValue.(map[string]any)
		if !ok {
			return nil, false, "", fmt.Errorf("Claude settings %s hook event %s contains a non-object matcher entry", path, event)
		}
		hookValues, ok := entry["hooks"].([]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		nextHooks := make([]any, 0, len(hookValues))
		for _, hookValue := range hookValues {
			hook, ok := hookValue.(map[string]any)
			if !ok {
				nextHooks = append(nextHooks, hookValue)
				continue
			}
			command, _ := hook["command"].(string)
			if hook["type"] == "command" && strings.Contains(command, claudeHookManagedMarker) {
				removed = true
				continue
			}
			if hook["type"] == "command" && strings.Contains(command, "projmux ai ingest claude-hook") && conflict == "" {
				conflict = fmt.Sprintf("Claude Code hook %s already contains unmanaged projmux ingest command in %s: %s", event, path, command)
			}
			nextHooks = append(nextHooks, hookValue)
		}
		if len(nextHooks) == 0 && len(entry) == 1 {
			continue
		}
		if len(nextHooks) == 0 {
			delete(entry, "hooks")
		} else {
			entry["hooks"] = nextHooks
		}
		out = append(out, entry)
	}
	return out, removed, conflict, nil
}

func claudeHookEntrySlice(value any) []any {
	if entries, ok := value.([]any); ok {
		return entries
	}
	return nil
}

func claudeHookManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": claudeHookCommand,
			},
		},
	}
}

func encodeClaudeSettings(settings map[string]any) (string, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(settings); err != nil {
		return "", fmt.Errorf("encode Claude settings: %w", err)
	}
	return out.String(), nil
}
