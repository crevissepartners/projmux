// Migration of legacy lifecycle script files into the declarative
// [hooks.<event>] config.toml entry. The runner no longer executes script
// files directly; this code path drains the historical layout so users do not
// silently lose hook behaviour after upgrading.
//
// Legacy: retained for draining legacy `.projmux/<event>` scripts into
// declarative config without data loss; sunset when a post-0.7 review after
// two minor releases or 90 days confirms legacy script migration has had
// enough release/time coverage.
package hooks

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// LegacyScope identifies which legacy script layout to scan during migration.
type LegacyScope string

const (
	// LegacyScopeProject scans `<repo>/.projmux/<event>` and
	// `<repo>/.projmux/hooks/<event>` for each supported lifecycle event.
	LegacyScopeProject LegacyScope = "project"
	// LegacyScopeGlobal scans `${XDG_CONFIG_HOME}/projmux/hooks/<event>`.
	LegacyScopeGlobal LegacyScope = "global"
)

// MigrationResult summarises which legacy script files were converted into
// declarative entries and which were skipped because they contain too many
// active lines to safely flatten.
type MigrationResult struct {
	Scope LegacyScope
	// Migrated lists events whose script was converted to a [hooks.<event>]
	// run = "..." entry. The original script is renamed to `<path>.bak` so
	// the user can recover the source if needed.
	Migrated []MigratedHook
	// Skipped lists scripts that contain two or more meaningful lines.
	// These are surfaced verbatim in the Settings UI as a "legacy script"
	// row alongside the manual-cleanup advice; the runner does NOT execute
	// them.
	Skipped []SkippedHook
}

// MigratedHook describes a single converted script.
type MigratedHook struct {
	Event      Event
	ScriptPath string
	BackupPath string
	Command    string
}

// SkippedHook describes a legacy script that was left in place because the
// migrator does not know how to flatten it into a single declarative line,
// or because it is a symlink (e.g. dotfiles-managed) that the migrator must
// not rename out from under the user.
type SkippedHook struct {
	Event      Event
	ScriptPath string
	Lines      int
	Reason     string
	// Symlink is true when ScriptPath resolved to a symbolic link via Lstat.
	// The migrator never reads, rewrites, or backs up symlinks regardless of
	// whether the target appears to be single-line, because the source-of-truth
	// for the file lives outside the projmux config directory.
	Symlink bool
}

// SkipReasonSymlink is the canonical Reason value used when a legacy script
// is skipped because Lstat reported a symbolic link.
const SkipReasonSymlink = "symlink"

// MigrateProjectLegacyScripts inspects `<repo>/.projmux/` for legacy script
// files. The result is non-fatal: callers should log a warning and continue
// even on partial failure. configPath is the target config.toml that picks
// up converted entries; pass "" to compute the default `<repo>/.projmux/config.toml`.
func MigrateProjectLegacyScripts(repoPath, configPath string, logger io.Writer) (MigrationResult, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return MigrationResult{Scope: LegacyScopeProject}, nil
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return MigrationResult{Scope: LegacyScopeProject}, err
	}
	if configPath == "" {
		configPath = filepath.Join(abs, projectConfigRelativePath)
	}
	candidates := func(event Event) []string {
		name := string(event)
		return []string{
			filepath.Join(abs, ".projmux", name),
			filepath.Join(abs, ".projmux", config.HooksDirName, name),
		}
	}
	return migrateLegacyScripts(LegacyScopeProject, configPath, candidates, logger)
}

// MigrateGlobalLegacyScripts inspects the legacy global hooks directory
// `${XDG_CONFIG_HOME}/projmux/hooks/`. configPath is the target global
// config.toml; pass "" to resolve the default location.
func MigrateGlobalLegacyScripts(getenv func(string) string, homeDir func() (string, error), configPath string, logger io.Writer) (MigrationResult, error) {
	dir, err := resolveGlobalConfigDir(getenv, homeDir)
	if err != nil {
		return MigrationResult{Scope: LegacyScopeGlobal}, err
	}
	if configPath == "" {
		configPath = filepath.Join(dir, GlobalConfigRelativePath)
	}
	candidates := func(event Event) []string {
		return []string{
			filepath.Join(dir, config.AppName, config.HooksDirName, string(event)),
		}
	}
	return migrateLegacyScripts(LegacyScopeGlobal, configPath, candidates, logger)
}

func migrateLegacyScripts(scope LegacyScope, configPath string, candidates func(Event) []string, logger io.Writer) (MigrationResult, error) {
	result := MigrationResult{Scope: scope}
	pending := map[Event]MigratedHook{}
	for _, event := range SupportedEvents {
		for _, path := range candidates(event) {
			// Lstat first so we can detect symlinks before os.Stat follows
			// them. Dotfiles repos (e.g. github.com/es5h/dotfiles) deploy
			// global hook files as symlinks; renaming the link to .bak would
			// silently break the user's source-of-truth.
			linfo, err := os.Lstat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				warnMigration(logger, scope, "lstat %q: %v", path, err)
				continue
			}
			if linfo.Mode()&os.ModeSymlink != 0 {
				result.Skipped = append(result.Skipped, SkippedHook{
					Event:      event,
					ScriptPath: path,
					Lines:      0,
					Reason:     SkipReasonSymlink,
					Symlink:    true,
				})
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				warnMigration(logger, scope, "stat %q: %v", path, err)
				continue
			}
			if info.IsDir() {
				continue
			}
			command, lineCount, err := analyzeLegacyScript(path)
			if err != nil {
				warnMigration(logger, scope, "read %q: %v", path, err)
				continue
			}
			if lineCount > 1 {
				result.Skipped = append(result.Skipped, SkippedHook{
					Event:      event,
					ScriptPath: path,
					Lines:      lineCount,
					Reason:     "multi-line scripts are no longer executed; rewrite manually as run = \"bash -c '...'\" or run = \"./scripts/foo.sh\"",
				})
				continue
			}
			if strings.TrimSpace(command) == "" {
				// Empty file — nothing to migrate but rename so it does not
				// keep tripping the scan.
				if err := backupScript(path); err != nil {
					warnMigration(logger, scope, "backup %q: %v", path, err)
				}
				continue
			}
			pending[event] = MigratedHook{
				Event:      event,
				ScriptPath: path,
				BackupPath: path + ".bak",
				Command:    command,
			}
		}
	}
	if len(pending) == 0 {
		// Even without migration work we still surface skipped multi-line
		// scripts so callers can warn the user.
		logSkipped(logger, scope, result.Skipped)
		return result, nil
	}

	_, err := UpdateProjectConfig(configPath, func(cfg *ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[Event]string{}
		}
		for event, hook := range pending {
			// Do not overwrite an existing declarative entry; the user has
			// already authored one and the legacy script must lose. We still
			// rename the script so the duplicate row stops showing up.
			if existing := strings.TrimSpace(cfg.Hooks[event]); existing != "" {
				continue
			}
			cfg.Hooks[event] = hook.Command
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("write %s: %w", configPath, err)
	}
	for event, hook := range pending {
		if err := backupScript(hook.ScriptPath); err != nil {
			warnMigration(logger, scope, "backup %q: %v", hook.ScriptPath, err)
			continue
		}
		result.Migrated = append(result.Migrated, MigratedHook{
			Event:      event,
			ScriptPath: hook.ScriptPath,
			BackupPath: hook.BackupPath,
			Command:    hook.Command,
		})
		if logger != nil {
			fmt.Fprintf(logger, "projmux: migrated legacy %s hook %s -> [hooks.%s] run; original kept at %s\n", scope, hook.ScriptPath, event, hook.BackupPath)
		}
	}
	logSkipped(logger, scope, result.Skipped)
	return result, nil
}

func logSkipped(logger io.Writer, scope LegacyScope, skipped []SkippedHook) {
	if logger == nil || len(skipped) == 0 {
		return
	}
	for _, s := range skipped {
		if s.Symlink {
			fmt.Fprintf(logger, "projmux: legacy %s hook %s is a symlink; declarative migration skipped (clean up via the source dotfiles repo)\n", scope, s.ScriptPath)
			continue
		}
		fmt.Fprintf(logger, "projmux: legacy %s hook %s has %d non-trivial lines; declarative migration skipped (%s)\n", scope, s.ScriptPath, s.Lines, s.Reason)
	}
}

// analyzeLegacyScript reads the file at path and returns (command, lineCount, error).
//   - lineCount counts all non-empty, non-comment, non-shebang lines.
//   - command is the single command suitable for a declarative run = "..." entry
//     when lineCount == 1, otherwise empty.
func analyzeLegacyScript(path string) (string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	var (
		count int
		line  string
	)
	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		count++
		if count == 1 {
			line = trimmed
		}
	}
	if err := scanner.Err(); err != nil {
		return "", 0, err
	}
	if count != 1 {
		return "", count, nil
	}
	return line, 1, nil
}

func backupScript(path string) error {
	backup := path + ".bak"
	// Remove any prior .bak so rename does not surprise on Windows.
	if _, err := os.Stat(backup); err == nil {
		if err := os.Remove(backup); err != nil {
			return err
		}
	}
	return os.Rename(path, backup)
}

func warnMigration(logger io.Writer, scope LegacyScope, format string, args ...any) {
	if logger == nil {
		return
	}
	fmt.Fprintf(logger, "projmux: %s hook migration: "+format+"\n", append([]any{scope}, args...)...)
}
