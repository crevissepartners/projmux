package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ghosttyBinding is one keybind = trigger=action pair the projmux init
// command guarantees in the user's Ghostty config. The list is the single
// source of truth in code; docs/keybindings.md mirrors it for users.
type ghosttyBinding struct {
	Trigger string
	Action  string
}

// ghosttyDesiredBindings mirrors the "Ghostty" section of docs/keybindings.md.
// Update both when adjusting CSI-u routing.
var ghosttyDesiredBindings = []ghosttyBinding{
	{Trigger: "alt+1", Action: "csi:9005u"},
	{Trigger: "alt+2", Action: "csi:9003u"},
	{Trigger: "alt+3", Action: "csi:9004u"},
	{Trigger: "alt+4", Action: "csi:9006u"},
	{Trigger: "alt+5", Action: "csi:9007u"},
	{Trigger: "ctrl+shift+r", Action: "csi:9001u"},
	{Trigger: "ctrl+shift+l", Action: "csi:9002u"},
	{Trigger: "ctrl+shift+n", Action: "csi:9008u"},
	{Trigger: "ctrl+m", Action: "csi:9011u"},
	{Trigger: "ctrl+shift+m", Action: "csi:9012u"},
	{Trigger: "alt+shift+left", Action: "csi:9009u"},
	{Trigger: "alt+shift+right", Action: "csi:9010u"},
}

// ghosttyManagedHeader marks the projmux-managed block in the Ghostty config.
const (
	ghosttyManagedHeader = "# >>> projmux managed keybindings (do not edit between markers)"
	ghosttyManagedFooter = "# <<< projmux managed keybindings"
)

// GhosttyAdapter implements TerminalAdapter for the Ghostty terminal.
type GhosttyAdapter struct {
	// now allows tests to pin the timestamp used for backup file names.
	now func() time.Time
	// userHomeDir defaults to os.UserHomeDir for path resolution.
	userHomeDir func() (string, error)
	// writeFile defaults to os.WriteFile (used for backups + the new config).
	writeFile func(name string, data []byte, perm os.FileMode) error
	// mkdirAll defaults to os.MkdirAll for the parent config dir.
	mkdirAll func(path string, perm os.FileMode) error
}

// NewGhosttyAdapter constructs a Ghostty adapter wired to real filesystem
// helpers. Tests can override the internal hooks afterwards.
func NewGhosttyAdapter() *GhosttyAdapter {
	return &GhosttyAdapter{
		now:         time.Now,
		userHomeDir: os.UserHomeDir,
		writeFile:   os.WriteFile,
		mkdirAll:    os.MkdirAll,
	}
}

// Name implements TerminalAdapter.
func (g *GhosttyAdapter) Name() string { return "ghostty" }

// Detect implements TerminalAdapter. Ghostty exposes itself via either
// TERM_PROGRAM=ghostty (current builds) or GHOSTTY_RESOURCES_DIR (older
// + linux/macos installs). Either signal is sufficient.
func (g *GhosttyAdapter) Detect(env func(string) string) bool {
	if env == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(env("TERM_PROGRAM")), "ghostty") {
		return true
	}
	if strings.TrimSpace(env("GHOSTTY_RESOURCES_DIR")) != "" {
		return true
	}
	return false
}

// ConfigPath implements TerminalAdapter. Ghostty looks at
// `$XDG_CONFIG_HOME/ghostty/config` first, falling back to
// `$HOME/.config/ghostty/config`.
func (g *GhosttyAdapter) ConfigPath(env func(string) string) (string, error) {
	if env == nil {
		env = os.Getenv
	}
	if xdg := strings.TrimSpace(env("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "ghostty", "config"), nil
	}
	homeFn := g.userHomeDir
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for ghostty config: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("resolve home directory for ghostty config: empty home")
	}
	return filepath.Join(home, ".config", "ghostty", "config"), nil
}

// PlanMerge implements TerminalAdapter. The merge is idempotent: bindings
// already pointing at the desired action are no-ops, missing bindings are
// appended in a managed block, and triggers already mapped to a different
// action are left untouched (skip with a warning entry).
func (g *GhosttyAdapter) PlanMerge(currentConfig string, fileExists bool) (MergePlan, error) {
	plan := MergePlan{Original: currentConfig, CreateNew: !fileExists}

	// Parse triggers already present anywhere in the file (including in any
	// previous projmux managed block). The merge re-emits the managed block
	// from scratch, so we drop existing block lines from the user-side map.
	stripped, userBindings := splitGhosttyConfig(currentConfig)

	var (
		toAppend []ghosttyBinding
		changes  []MergeChange
	)
	for _, want := range ghosttyDesiredBindings {
		existing, ok := userBindings[want.Trigger]
		switch {
		case !ok:
			toAppend = append(toAppend, want)
			changes = append(changes, MergeChange{
				Trigger: want.Trigger,
				Action:  want.Action,
				Kind:    "add",
			})
		case existing == want.Action:
			changes = append(changes, MergeChange{
				Trigger:  want.Trigger,
				Action:   want.Action,
				Existing: existing,
				Kind:     "noop",
			})
		default:
			changes = append(changes, MergeChange{
				Trigger:  want.Trigger,
				Action:   want.Action,
				Existing: existing,
				Kind:     "skip-conflict",
				Reason:   "user mapped to " + existing,
			})
		}
	}

	plan.Changes = changes

	if len(toAppend) == 0 {
		// Nothing to add. Updated == Original keeps HasEffect false.
		plan.Updated = currentConfig
		return plan, nil
	}

	plan.Updated = renderGhosttyConfig(stripped, toAppend)
	return plan, nil
}

// ApplyMerge implements TerminalAdapter. It backs up the existing config to
// `<config>.bak.<YYYYMMDD-HHMMSS>` (when the file exists) and writes the
// plan's Updated contents to ConfigPath. The caller is expected to have
// populated plan.ConfigPath via the init dispatcher.
func (g *GhosttyAdapter) ApplyMerge(plan MergePlan) error {
	if plan.ConfigPath == "" {
		return fmt.Errorf("apply ghostty merge: plan has no ConfigPath")
	}
	if !plan.HasEffect() {
		return nil
	}

	dir := filepath.Dir(plan.ConfigPath)
	mkdir := g.mkdirAll
	if mkdir == nil {
		mkdir = os.MkdirAll
	}
	if err := mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("create ghostty config dir %s: %w", dir, err)
	}

	write := g.writeFile
	if write == nil {
		write = os.WriteFile
	}

	if !plan.CreateNew {
		nowFn := g.now
		if nowFn == nil {
			nowFn = time.Now
		}
		stamp := nowFn().Format("20060102-150405")
		backup := plan.ConfigPath + ".bak." + stamp
		if err := write(backup, []byte(plan.Original), 0o644); err != nil {
			return fmt.Errorf("write ghostty config backup %s: %w", backup, err)
		}
	}

	if err := write(plan.ConfigPath, []byte(plan.Updated), 0o644); err != nil {
		return fmt.Errorf("write ghostty config %s: %w", plan.ConfigPath, err)
	}
	return nil
}

// splitGhosttyConfig parses the supplied config text and returns:
//   - stripped: the original text with any prior projmux-managed block removed
//   - bindings: trigger -> action map of every keybind line in the file,
//     including ones inside a previously-emitted managed block. Conflict
//     detection therefore sees the user's true intent (mappings outside the
//     block override anything we previously appended), and re-runs treat
//     already-managed bindings as noop instead of fresh adds.
//
// The output text drops the managed block so the next render can append a
// fresh one; the file content remains stable when the bindings already
// match.
func splitGhosttyConfig(raw string) (string, map[string]string) {
	bindings := map[string]string{}
	if raw == "" {
		return "", bindings
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	inManaged := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inManaged && trimmed == ghosttyManagedHeader {
			inManaged = true
			// Capture managed-block bindings into the map so the merge
			// recognises them as noop; do not echo them into stripped.
			continue
		}
		if inManaged {
			if trimmed == ghosttyManagedFooter {
				inManaged = false
				continue
			}
			if trigger, action, ok := parseGhosttyKeybind(line); ok {
				if _, set := bindings[trigger]; !set {
					// User-owned mappings outside the block win; only
					// adopt managed-block entries when no user override
					// already appeared above the block.
					bindings[trigger] = action
				}
			}
			continue
		}
		out = append(out, line)
		trigger, action, ok := parseGhosttyKeybind(line)
		if !ok {
			continue
		}
		// Last write wins: matches Ghostty's own behavior where the final
		// keybind line for a trigger overrides earlier ones.
		bindings[trigger] = action
	}
	stripped := strings.Join(out, "\n")
	return stripped, bindings
}

// parseGhosttyKeybind extracts (trigger, action) from a `keybind = X=Y` line.
// Comments (`#`) and non-keybind lines return ok=false.
func parseGhosttyKeybind(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if !strings.HasPrefix(trimmed, "keybind") {
		return "", "", false
	}
	rest := strings.TrimPrefix(trimmed, "keybind")
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	eq := strings.Index(rest, "=")
	if eq <= 0 {
		return "", "", false
	}
	trigger := strings.TrimSpace(rest[:eq])
	action := strings.TrimSpace(rest[eq+1:])
	if trigger == "" || action == "" {
		return "", "", false
	}
	return trigger, action, true
}

// renderGhosttyConfig appends a managed projmux block containing the supplied
// bindings to stripped. It guarantees a single blank line separator between
// existing user content and the managed block, and a trailing newline.
func renderGhosttyConfig(stripped string, additions []ghosttyBinding) string {
	var b strings.Builder
	if stripped != "" {
		b.WriteString(strings.TrimRight(stripped, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(ghosttyManagedHeader)
	b.WriteString("\n")
	for _, kb := range additions {
		fmt.Fprintf(&b, "keybind = %s=%s\n", kb.Trigger, kb.Action)
	}
	b.WriteString(ghosttyManagedFooter)
	b.WriteString("\n")
	return b.String()
}
