package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

const (
	codexConfigRelativePath = ".codex/config.toml"
	codexHooksMarkerBegin   = "# >>> projmux managed codex hooks"
	codexHooksMarkerEnd     = "# <<< projmux managed codex hooks"
	codexHooksFeatureMarker = "# projmux-managed:codex-hooks-feature:v1"
	codexHookCommand        = canonicalCodexHookRoute + aiHookPaneArgument + " >/dev/null 2>&1 || true"
	canonicalCodexHookRoute = "projmux internal agent-hook ingest codex-hook"
	// priorCodexHookCommand is the pane-blind spelling projmux wrote before the
	// hook carried its own Pane. Installed configs still hold it, so ownership
	// keeps recognizing it as projmux-authored and `agent integrate codex`
	// converges it forward instead of refusing the file as hand-edited.
	priorCodexHookCommand = "projmux internal agent-hook ingest codex-hook >/dev/null 2>&1 || true"

	claudeSettingsRelativePath = ".claude/settings.json"
	claudeHookManagedMarker    = "projmux-managed:claude-hook:v1"
	claudeHookCommand          = canonicalClaudeHookRoute + aiHookPaneArgument + " >/dev/null 2>&1 || true # " + claudeHookManagedMarker
	canonicalClaudeHookRoute   = "projmux internal agent-hook ingest claude-hook"

	// aiHookPaneArgument hands a provider hook the Pane it belongs to instead of
	// letting it inherit one. projmux plants the activation envelope on the
	// process it launches in the Pane, so the hook keeps its own identity even
	// when the app-server that spawned it inherited no tmux environment at all.
	// The value is deliberately unquoted and `=`-joined: an app-server shared by
	// several Panes carries no envelope and expands this to a bare `--pane=`,
	// which is not an identity, and the established matcher stays its fallback.
	// The same argument is written for every provider; nothing here is
	// provider-specific.
	aiHookPaneArgument = " --pane=${" + internalActivationPaneUIDEnv + ":-}"

	tmuxBellManagedMarker = "projmux-managed:tmux-bell:v1"
	tmuxBellHookName      = "alert-bell"
	tmuxBellHookCommand   = `run-shell -b 'projmux internal agent-hook ingest bell --pane "#{pane_id}" >/dev/null 2>&1 || true # ` + tmuxBellManagedMarker + `'`
)

var (
	legacyCodexHookRoute      = legacyAIIngestCommand("codex-hook")
	legacyCodexHookCommand    = legacyCodexHookRoute + " >/dev/null 2>&1 || true"
	legacyClaudeHookRoute     = legacyAIIngestCommand("claude-hook")
	legacyClaudeHookCommand   = legacyClaudeHookRoute + " >/dev/null 2>&1 || true # " + claudeHookManagedMarker
	legacyTmuxBellHookCommand = `run-shell -b '` + legacyAIIngestCommand(`bell --pane "#{pane_id}"`) +
		` >/dev/null 2>&1 || true # ` + tmuxBellManagedMarker + `'`
)

// legacyAIIngestCommand assembles the removed producer prefix at runtime. The
// full spelling must remain recognizable in marker-owned v0.10.1 files, but it
// must not survive as contiguous read-only data in a current production binary.
func legacyAIIngestCommand(suffix string) string {
	return strings.Join([]string{"projmux", "ai", "ingest", suffix}, " ")
}

func legacyAIIngestArgs(suffix string) string {
	return strings.Join([]string{"", "ai", "ingest", suffix}, " ")
}

type codexIntegrationPlan struct {
	path     string
	current  string
	next     string
	action   string
	changed  bool
	conflict string
	// unmarked reports that the config carried projmux-authored hook wiring
	// outside a marker block, which the automatic convergence path treats as
	// projmux-owned exactly like a marker-owned file.
	unmarked bool
}

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
	migrations      []string
	action          string
	changed         bool
}

type tmuxBellSnapshot struct {
	options map[string]tmuxBellOptionSnapshot
	hooks   []string
}

type tmuxBellOptionSnapshot struct {
	value  string
	exists bool
}

func (c *aiCommand) runIntegrate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate requires <agent-kind>")
	}
	target := strings.TrimSpace(args[0])
	if target == "help" || target == "--help" || target == "-h" {
		printAIUsage(stdout)
		return nil
	}
	if aiprovider.IntegrationCommand(target) == "" {
		printAIUsage(stderr)
		return fmt.Errorf("unknown ai integrate agent-kind: %s", args[0])
	}
	switch target {
	case "codex":
		return c.runIntegrateCodex(args[1:], stdout, stderr)
	case "claude":
		return c.runIntegrateClaude(args[1:], stdout, stderr)
	case "antigravity":
		return c.runIntegrateAntigravity(args[1:], stdout, stderr)
	case "tmux-bell":
		return c.runIntegrateTmuxBell(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("integration target %q is catalogued but has no dispatcher", target)
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

	plan, err := c.planTmuxBellIntegration(*remove)
	if err != nil {
		return err
	}
	if *dryRun {
		return printTmuxBellDryRun(stdout, plan)
	}
	commands := plan.installCommands
	if *remove {
		commands = plan.removeCommands
	}
	snapshot, err := c.snapshotTmuxBellState()
	if err != nil {
		return err
	}
	for _, args := range commands {
		if err := c.runTmuxBellCommand(args); err != nil {
			if rollbackErr := c.restoreTmuxBellState(snapshot); rollbackErr != nil {
				return fmt.Errorf("tmux %s: %w (rollback failed: %v)", strings.Join(args, " "), err, rollbackErr)
			}
			return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
	}
	_, err = fmt.Fprintf(stdout, "%s\n", plan.action)
	return err
}

func (c *aiCommand) runTmuxBellCommand(args []string) error {
	if len(args) == 4 && args[0] == "set-option" && args[1] == "-g" {
		return c.muxRunner().SetOption(context.Background(), intmux.SetOptionOptions{
			Global: true,
			Option: args[2],
			Value:  args[3], // #nosec G602 -- the enclosing len(args) == 4 guard proves this command slot exists.
		})
	}
	if len(args) == 3 && args[0] == "set-option" && args[1] == "-gu" {
		return c.muxRunner().SetOption(context.Background(), intmux.SetOptionOptions{
			Global: true,
			Unset:  true,
			Option: args[2],
		})
	}
	if len(args) == 4 && args[0] == "set-hook" && args[1] == "-ag" {
		return c.muxRunner().SetHook(context.Background(), intmux.SetHookOptions{
			Global:  true,
			Append:  true,
			Hook:    args[2],
			Command: args[3], // #nosec G602 -- the enclosing len(args) == 4 guard proves this command slot exists.
		})
	}
	if len(args) == 3 && args[0] == "set-hook" && args[1] == "-gu" {
		return c.muxRunner().SetHook(context.Background(), intmux.SetHookOptions{
			Global: true,
			Unset:  true,
			Hook:   args[2],
		})
	}
	return c.run("tmux", args...)
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
		return errors.New(plan.conflict + "; run `projmux agent integrate claude --dry-run` to preview without writing")
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		printAIUsage(stderr)
		return errors.New("ai integrate codex does not accept positional arguments")
	}

	plan, err := c.planCodexIntegration(*remove)
	if err != nil {
		return err
	}
	if *dryRun {
		return printCodexIntegrationDryRun(stdout, plan)
	}
	if plan.conflict != "" {
		return errors.New(plan.conflict + "; run `projmux agent integrate codex --dry-run` to preview without writing")
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

func (c *aiCommand) planCodexIntegration(remove bool) (codexIntegrationPlan, error) {
	return c.planCodexHooksIntegration(remove)
}

func (c *aiCommand) planCodexHooksIntegration(remove bool) (codexIntegrationPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return codexIntegrationPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, codexConfigRelativePath)
	current, err := c.readCodexConfig(path)
	if err != nil {
		return codexIntegrationPlan{}, err
	}

	withoutHooks, hadHooks, err := removeManagedCodexHooksBlock(current)
	if err != nil {
		return codexIntegrationPlan{}, err
	}
	withoutManaged, unmarked := stripUnmarkedProjmuxCodexHooks(withoutHooks)
	plan := codexIntegrationPlan{path: path, current: current, next: withoutManaged, unmarked: unmarked > 0}

	if remove {
		withoutManaged, removedFeature := removeManagedCodexHooksFeature(withoutManaged)
		plan.next = withoutManaged
		plan.changed = (hadHooks || plan.unmarked || removedFeature) && withoutManaged != current
		switch {
		case hadHooks || plan.unmarked || removedFeature:
			plan.action = "removed projmux-managed Codex hooks wiring from " + path
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
	hookEvents, err := c.aiHookInstallEvents(aiHookProviderCodex)
	if err != nil {
		return codexIntegrationPlan{}, err
	}
	nextConfig := withoutManaged
	nextConfig, _ = removeManagedCodexHooksFeature(nextConfig)
	includeFeatureInBlock := !codexConfigHasFeaturesTable(nextConfig) && !codexConfigHasDottedCodexHooksFeature(nextConfig)
	if !includeFeatureInBlock {
		nextConfig = ensureCodexHooksFeatureEnabled(nextConfig)
	}
	next := appendCodexHooksBlock(nextConfig, codexHooksBlockForEvents(includeFeatureInBlock, hookEvents))
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
	return c.planClaudeHookIntegrationMode(remove, !remove)
}

// planClaudeHookMigration converges only the managed hook families already
// present in the file. Automatic config apply/install migration must not plant
// a new async provider process into a user's live Claude settings; the explicit
// integrate command is the sole installer for coordination hooks.
func (c *aiCommand) planClaudeHookMigration() (claudeHookPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return claudeHookPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, claudeSettingsRelativePath)
	current, err := c.readClaudeSettings(path)
	if err != nil {
		return claudeHookPlan{}, err
	}
	return c.planClaudeHookIntegrationFromCurrent(false, strings.Contains(current, claudeCoordinationManagedMarker), path, current)
}

func (c *aiCommand) planClaudeHookIntegrationMode(remove, includeCoordination bool) (claudeHookPlan, error) {
	home, err := c.homeDir()
	if err != nil {
		return claudeHookPlan{}, fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, claudeSettingsRelativePath)
	current, err := c.readClaudeSettings(path)
	if err != nil {
		return claudeHookPlan{}, err
	}
	return c.planClaudeHookIntegrationFromCurrent(remove, includeCoordination, path, current)
}

func (c *aiCommand) planClaudeHookIntegrationFromCurrent(remove, includeCoordination bool, path, current string) (claudeHookPlan, error) {
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
		for event, value := range hooks {
			nextEntries, removed, eventConflict, err := claudeHookEntriesWithoutManaged(value, event, path)
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
	hookEvents, err := c.aiHookInstallEvents(aiHookProviderClaude)
	if err != nil {
		return claudeHookPlan{}, err
	}

	hooks, err = claudeSettingsHooks(settings, true)
	if err != nil {
		return claudeHookPlan{}, err
	}
	for _, event := range hookEvents {
		entry := claudeHookManagedEntry()
		if event == "SessionStart" {
			entry["hooks"] = append(entry["hooks"].([]any), map[string]any{
				"type": "command", "command": claudeRegistrationHookCommand, "timeout": 5,
			})
		}
		hooks[event] = append(claudeHookEntrySlice(hooks[event]), entry)
	}
	if includeCoordination {
		for _, event := range []string{"SessionStart", "Stop"} {
			hooks[event] = append(claudeHookEntrySlice(hooks[event]), claudeCoordinationManagedEntry())
		}
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

func (c *aiCommand) planTmuxBellIntegration(remove bool) (tmuxBellPlan, error) {
	hooks, err := c.readTmuxBellHooks()
	if err != nil {
		return tmuxBellPlan{}, fmt.Errorf("read tmux bell hooks: %w", err)
	}
	managed := tmuxBellManagedHookTargets(hooks)
	removeCommands := make([][]string, 0, len(managed))
	for _, target := range managed {
		removeCommands = append(removeCommands, []string{"set-hook", "-gu", target})
	}

	if remove {
		action := "no changes: projmux-managed tmux bell hook is not present"
		if len(removeCommands) > 0 {
			action = "removed projmux-managed tmux bell hook"
		}
		return tmuxBellPlan{removeCommands: removeCommands, action: action, changed: len(removeCommands) > 0}, nil
	}

	install := make([][]string, 0, 6)
	for _, desired := range []struct {
		option string
		value  string
	}{
		{option: "allow-passthrough", value: "on"},
		{option: "monitor-bell", value: "on"},
		{option: "bell-action", value: "other"},
	} {
		out, err := c.read("tmux", "show-options", "-gqv", desired.option)
		if err != nil {
			return tmuxBellPlan{}, fmt.Errorf("read tmux option %s: %w", desired.option, err)
		}
		if strings.TrimSpace(string(out)) != desired.value {
			install = append(install, []string{"set-option", "-g", desired.option, desired.value})
		}
	}
	managedCurrent := false
	migrations := make([]string, 0, len(managed))
	for _, line := range hooks {
		if strings.Contains(line, tmuxBellManagedMarker) && strings.Contains(line, tmuxBellHookCommand) {
			managedCurrent = true
		}
		if strings.Contains(line, legacyTmuxBellHookCommand) {
			migrations = append(migrations, legacyTmuxBellHookCommand+" -> "+tmuxBellHookCommand)
		}
	}
	if len(managed) > 0 && !managedCurrent {
		for _, target := range managed {
			install = append(install, []string{"set-hook", "-gu", target})
		}
	}
	if len(managed) == 0 || !managedCurrent {
		install = append(install, []string{"set-hook", "-ag", tmuxBellHookName, tmuxBellHookCommand})
	}
	action := "configured tmux bell fallback"
	if len(install) == 0 {
		action = "no changes: tmux bell fallback is already configured"
	} else if len(managed) > 0 {
		action = "refreshed tmux bell fallback options"
	}
	return tmuxBellPlan{installCommands: install, migrations: migrations, action: action, changed: len(install) > 0}, nil
}

func (c *aiCommand) beginManagedTmuxBellProducerMigration() (bool, func() error, error) {
	hooks, err := c.readTmuxBellHooks()
	if err != nil {
		return false, nil, fmt.Errorf("read tmux bell hooks: %w", err)
	}
	if len(tmuxBellManagedHookTargets(hooks)) == 0 {
		return false, nil, nil
	}
	plan, err := c.planTmuxBellIntegration(false)
	if err != nil {
		return false, nil, err
	}
	if !plan.changed {
		return false, nil, nil
	}
	snapshot, err := c.snapshotTmuxBellState()
	if err != nil {
		return false, nil, err
	}
	for _, args := range plan.installCommands {
		if err := c.runTmuxBellCommand(args); err != nil {
			if rollbackErr := c.restoreTmuxBellState(snapshot); rollbackErr != nil {
				return false, nil, fmt.Errorf("tmux %s: %w (rollback failed: %v)", strings.Join(args, " "), err, rollbackErr)
			}
			return false, nil, fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
	}
	return true, func() error { return c.restoreTmuxBellState(snapshot) }, nil
}

func (c *aiCommand) readTmuxBellHooks() ([]string, error) {
	out, err := c.read("tmux", "show-hooks", "-g", tmuxBellHookName)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
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

func (c *aiCommand) snapshotTmuxBellState() (tmuxBellSnapshot, error) {
	hooks, err := c.readTmuxBellHooks()
	if err != nil {
		return tmuxBellSnapshot{}, fmt.Errorf("snapshot tmux bell hooks: %w", err)
	}
	snapshot := tmuxBellSnapshot{options: map[string]tmuxBellOptionSnapshot{}, hooks: hooks}
	for _, option := range []string{"allow-passthrough", "monitor-bell", "bell-action"} {
		out, err := c.read("tmux", "show-options", "-gqv", option)
		if err != nil {
			return tmuxBellSnapshot{}, fmt.Errorf("snapshot tmux option %s: %w", option, err)
		}
		value := strings.TrimSpace(string(out))
		snapshot.options[option] = tmuxBellOptionSnapshot{value: value, exists: value != ""}
	}
	return snapshot, nil
}

func (c *aiCommand) restoreTmuxBellState(snapshot tmuxBellSnapshot) error {
	var rollbackErr error
	for _, option := range []string{"allow-passthrough", "monitor-bell", "bell-action"} {
		before := snapshot.options[option]
		args := []string{"set-option", "-gu", option}
		if before.exists {
			args = []string{"set-option", "-g", option, before.value}
		}
		if err := c.runTmuxBellCommand(args); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	if err := c.runTmuxBellCommand([]string{"set-hook", "-gu", tmuxBellHookName}); err != nil && rollbackErr == nil {
		rollbackErr = err
	}
	for _, line := range snapshot.hooks {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if err := c.runTmuxBellCommand([]string{"set-hook", "-ag", tmuxBellHookName, command}); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	return rollbackErr
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

func printCodexIntegrationDryRun(stdout io.Writer, plan codexIntegrationPlan) error {
	if _, err := fmt.Fprintf(stdout, "projmux agent integrate codex (dry-run)\nconfig: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if strings.Contains(plan.current, legacyCodexHookRoute) {
		if _, err := fmt.Fprintf(stdout, "migration: %s -> %s\n", legacyCodexHookRoute, canonicalCodexHookRoute); err != nil {
			return err
		}
	}
	if plan.unmarked {
		if _, err := fmt.Fprintln(stdout, "recovery: adopting projmux-authored Codex hook wiring left outside a managed block"); err != nil {
			return err
		}
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
	if _, err := fmt.Fprintf(stdout, "projmux agent integrate claude (dry-run)\nsettings: %s\n", plan.path); err != nil {
		return err
	}
	if plan.conflict != "" {
		_, err := fmt.Fprintf(stdout, "%s\n%s\n", plan.action, plan.conflict)
		return err
	}
	if strings.Contains(plan.current, legacyClaudeHookRoute) {
		if _, err := fmt.Fprintf(stdout, "migration: %s -> %s\n", legacyClaudeHookRoute, canonicalClaudeHookRoute); err != nil {
			return err
		}
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
	if _, err := fmt.Fprintln(stdout, "projmux agent integrate tmux-bell (dry-run)"); err != nil {
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
	for _, migration := range plan.migrations {
		if _, err := fmt.Fprintf(stdout, "migration: %s\n", migration); err != nil {
			return err
		}
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

func codexHooksBlock(includeFeature bool) string {
	return codexHooksBlockForEvents(includeFeature, defaultAIHookInstallEvents(aiHookProviderCodex))
}

func appendCodexHooksBlock(content, block string) string {
	if strings.TrimSpace(content) == "" {
		return block
	}
	content = strings.TrimRight(content, "\r\n")
	return content + "\n\n" + block
}

func codexHooksBlockForEvents(includeFeature bool, events []string) string {
	lines := []string{
		codexHooksMarkerBegin,
	}
	if includeFeature {
		lines = append(lines,
			"[features]",
			"hooks = true",
			"",
		)
	}
	for _, event := range events {
		lines = append(lines,
			"[[hooks."+event+"]]",
			`matcher = "*"`,
			"[[hooks."+event+".hooks]]",
			`type = "command"`,
			`command = "`+codexHookCommand+`"`,
			"",
		)
	}
	lines = append(lines, codexHooksMarkerEnd)
	return strings.Join(lines, "\n") + "\n"
}

// removeManagedCodexHooksBlock drops the projmux-managed marker block but keeps
// whatever the provider wrote inside it. Codex re-serializes the same file and
// can land `[hooks.state]` — the trust the user granted — between the markers,
// so the block is not a projmux-owned byte range: only the hook definitions and
// the feature toggle projmux authored are. Everything else is lifted back out
// verbatim, in document order, where the block used to be.
func removeManagedCodexHooksBlock(content string) (string, bool, error) {
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	removed := false
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != codexHooksMarkerBegin {
			out.WriteString(lines[i])
			continue
		}
		removed = true
		var block strings.Builder
		foundEnd := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == codexHooksMarkerEnd {
				foundEnd = true
				break
			}
			block.WriteString(lines[i])
		}
		if !foundEnd {
			//lint:ignore ST1005 Codex is the canonical provider name in this diagnostic.
			return "", false, errors.New("Codex config contains an unterminated projmux-managed hooks block")
		}
		preserved, _ := stripProjmuxManagedCodexHookSections(block.String(), true)
		if strings.TrimSpace(preserved) != "" {
			out.WriteString(strings.Trim(preserved, "\n") + "\n")
		}
	}
	return out.String(), removed, nil
}

func findUnmanagedCodexHooksLine(content string) (string, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, legacyCodexHookRoute) || strings.Contains(trimmed, canonicalCodexHookRoute) {
			return trimmed, true
		}
	}
	return "", false
}

func codexConfigHasFeaturesTable(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(stripCodexTomlComment(line)) == "[features]" {
			return true
		}
	}
	return false
}

func codexConfigHasDottedCodexHooksFeature(content string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(stripCodexTomlComment(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "[") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if ok && (strings.TrimSpace(key) == "features.hooks" || strings.TrimSpace(key) == "features.codex_hooks") {
			return true
		}
	}
	return false
}

func ensureCodexHooksFeatureEnabled(content string) string {
	lines := strings.SplitAfter(content, "\n")
	section := ""
	featuresEnd := len(lines)
	featuresStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(stripCodexTomlComment(line))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if section == "features" && featuresEnd == len(lines) {
				featuresEnd = i
			}
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			if section == "features" {
				featuresStart = i
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "features.hooks" || (section == "features" && key == "hooks") {
			if strings.TrimSpace(value) == "true" {
				return content
			}
			indent := lineIndent(line)
			lines[i] = indent + codexHooksFeatureMarker + "\n" + indent + keyNameForCodexHooksFeature(section) + " = true\n"
			return strings.Join(lines, "")
		}
		if key == "features.codex_hooks" || (section == "features" && key == "codex_hooks") {
			indent := lineIndent(line)
			lines[i] = indent + codexHooksFeatureMarker + "\n" + indent + keyNameForCodexHooksFeature(section) + " = true\n"
			return strings.Join(lines, "")
		}
	}
	if featuresStart < 0 {
		return content
	}
	insert := lineIndent(lines[featuresStart]) + codexHooksFeatureMarker + "\n" + lineIndent(lines[featuresStart]) + "hooks = true\n"
	next := append([]string{}, lines[:featuresEnd]...)
	next = append(next, insert)
	next = append(next, lines[featuresEnd:]...)
	return strings.Join(next, "")
}

func keyNameForCodexHooksFeature(section string) string {
	if section == "features" {
		return "hooks"
	}
	return "features.hooks"
}

func removeManagedCodexHooksFeature(content string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	removed := false
	skipCodexHooks := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == codexHooksFeatureMarker {
			removed = true
			skipCodexHooks = true
			continue
		}
		if skipCodexHooks {
			withoutComment := strings.TrimSpace(stripCodexTomlComment(line))
			key, _, ok := strings.Cut(withoutComment, "=")
			if ok && (strings.TrimSpace(key) == "hooks" || strings.TrimSpace(key) == "features.hooks" || strings.TrimSpace(key) == "codex_hooks" || strings.TrimSpace(key) == "features.codex_hooks") {
				skipCodexHooks = false
				continue
			}
			skipCodexHooks = false
		}
		out.WriteString(line)
	}
	return out.String(), removed
}

func lineIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func stripCodexTomlComment(line string) string {
	inString := rune(0)
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if inString == '"' && r == '\\' {
			escaped = true
			continue
		}
		if inString != 0 {
			if r == inString {
				inString = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = r
			continue
		}
		if r == '#' {
			return line[:i]
		}
	}
	return line
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
			if hook["type"] == "command" && (strings.Contains(command, claudeHookManagedMarker) || strings.Contains(command, claudeCoordinationManagedMarker)) {
				removed = true
				continue
			}
			if hook["type"] == "command" && (strings.Contains(command, legacyClaudeHookRoute) || strings.Contains(command, canonicalClaudeHookRoute) || strings.Contains(command, "internal claude-message-wait")) && conflict == "" {
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

func claudeCoordinationManagedEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":        "command",
				"command":     claudeCoordinationHookCommand,
				"timeout":     int(claudeCoordinationHookTimeout / time.Second),
				"asyncRewake": true,
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
