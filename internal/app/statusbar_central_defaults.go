package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
)

// The central status bar defaults sit under the TUI visibility files (see
// internal/config/statusbar_defaults.go). The loaders in
// statusbar_hud_visibility.go and statusbar_row_one_visibility.go resolve TUI,
// then central, then the built-in default.
//
// The functions below read the central layer alone. They take no settings
// command and read no TUI file, so a surface other than the TUI can fall back
// to the central default instead of the TUI's value. Each returns Source
// central with the stored value, or Source default with the built-in (usage
// window: capability) default when nothing is stored.

func loadCentralStatusbarHUDVisibility(homeDir func() (string, error), lookupEnv func(string) string, component statusbarHUDComponent) config.StatusbarVisibilityState {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := statusbarHUDVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	return loadCentralStatusbarLeaf(paths, path, config.StatusbarVisibilityOn)
}

func loadCentralStatusbarRowOneVisibility(homeDir func() (string, error), lookupEnv func(string) string, component statusbarRowOneComponent) config.StatusbarVisibilityState {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return config.DefaultStatusbarVisibilityState()
	}
	path, ok := statusbarRowOneVisibilityPath(paths, component)
	if !ok {
		return config.DefaultStatusbarVisibilityState()
	}
	return loadCentralStatusbarLeaf(paths, path, config.StatusbarVisibilityOn)
}

func loadCentralAgentUsageVisibility(homeDir func() (string, error), lookupEnv func(string) string, leaf agentUsageVisibilityLeaf) config.StatusbarVisibilityState {
	defaultValue := config.StatusbarVisibilityOn
	if window, ok := agentUsageWindowCapability(leaf.provider, leaf.window); ok && strings.TrimSpace(leaf.window) != "" {
		defaultValue = config.NormalizeStatusbarVisibility(string(window.DefaultVisibility))
	}
	defaultState := config.DefaultStatusbarVisibilityState()
	defaultState.Effective = defaultValue
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return defaultState
	}
	path, ok := agentUsageVisibilityPath(paths, leaf)
	if !ok {
		return defaultState
	}
	return loadCentralStatusbarLeaf(paths, path, defaultValue)
}

func loadCentralStatusbarLeaf(paths config.Paths, tuiPath string, defaultValue config.StatusbarVisibility) config.StatusbarVisibilityState {
	state, err := config.LoadCentralStatusbarVisibility(paths.StatusbarDefaultsFile(), tuiPath, defaultValue)
	if err != nil {
		state = config.DefaultStatusbarVisibilityState()
		state.Effective = config.NormalizeStatusbarVisibility(string(defaultValue))
	}
	return state
}

// centralStatusbarSeedLeaves are the TUI visibility files whose valid values
// seed the central defaults: both HUDs, every usage provider and window of the
// HUD capability map, and the four row one segments. The settings launcher is
// TUI only.
func centralStatusbarSeedLeaves(paths config.Paths) []string {
	leaves := []string{
		paths.StatusbarNotificationsHUDVisibilityFile(),
		paths.StatusbarAgentUsageHUDVisibilityFile(),
	}
	for _, capability := range usagecmd.HUDProviderCapabilities() {
		leaves = append(leaves, paths.StatusbarAgentUsageProviderVisibilityFile(string(capability.ID)))
		for _, window := range capability.Windows {
			leaves = append(leaves, paths.StatusbarAgentUsageWindowVisibilityFile(string(capability.ID), window.Key))
		}
	}
	return append(leaves,
		paths.StatusbarProjectVisibilityFile(),
		paths.StatusbarWorkingDirectoryVisibilityFile(),
		paths.StatusbarGitVisibilityFile(),
		paths.StatusbarClockVisibilityFile(),
	)
}

// seedCentralStatusbarDefaults copies the valid TUI status bar values into the
// central defaults once. `config apply` is the step every installer runs, so
// the copy happens there, before the route branches on --no-reload.
//
//   - Once: a record under StateDir marks it done, and later applies copy
//     nothing, whatever the TUI files say by then.
//   - Per key, never overwriting: a central value already stored stays.
//   - Valid values only: a missing, empty or invalid TUI file copies nothing,
//     so that key keeps following the built-in default.
//
// It writes at most one line to stdout and never fails the apply: the
// generated config does not depend on it, because the TUI loaders read the TUI
// files first and the copy holds the same values. After a failure no record is
// written, so the next apply tries again. Without an injected home resolver it
// does nothing, so a partially constructed command never falls back to the
// real home directory.
func (c *tmuxCommand) seedCentralStatusbarDefaults(stdout io.Writer) {
	if c.homeDir == nil || c.lookupEnv == nil {
		return
	}
	line := ""
	copied, err := seedCentralStatusbarDefaultsOnce(c.homeDir, c.lookupEnv, time.Now)
	switch {
	case err != nil:
		line = "seeded central status bar defaults: failed (" + err.Error() + ")"
	case copied > 0:
		line = fmt.Sprintf("seeded central status bar defaults: copied %d TUI value(s)", copied)
	}
	if line != "" && stdout != nil {
		fmt.Fprintln(stdout, line)
	}
}

// seedCentralStatusbarDefaultsOnce performs the copy and reports how many keys
// it added. It returns 0 without reading a TUI file once the record exists.
func seedCentralStatusbarDefaultsOnce(homeDir func() (string, error), lookupEnv func(string) string, now func() time.Time) (int, error) {
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return 0, err
	}
	record := paths.StatusbarDefaultsSeedStateFile()
	if _, err := os.Lstat(record); err == nil {
		return 0, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read seed record: %w", err)
	}
	values := map[string]config.StatusbarVisibility{}
	for _, leaf := range centralStatusbarSeedLeaves(paths) {
		key, ok := config.StatusbarDefaultKey(leaf)
		if !ok {
			continue
		}
		state, err := config.LoadStatusbarVisibilityFile(leaf)
		if err != nil {
			return 0, err
		}
		if state.Source == config.StatusbarVisibilitySourceSaved {
			values[key] = state.Effective
		}
	}
	added, err := config.SeedStatusbarDefaults(paths.StatusbarDefaultsFile(), values)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(record), 0o750); err != nil {
		return len(added), fmt.Errorf("write seed record: %w", err)
	}
	if err := os.WriteFile(record, []byte(now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return len(added), fmt.Errorf("write seed record: %w", err)
	}
	return len(added), nil
}
