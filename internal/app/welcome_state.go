package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/version"
)

const welcomeStateVersion = 1

type shellWelcomeState struct {
	Version              int        `json:"version"`
	LastWelcomedVersion  string     `json:"last_welcomed_version"`
	WelcomedAt           time.Time  `json:"welcomed_at"`
	PendingAttachWelcome bool       `json:"pending_attach_welcome,omitempty"`
	SkipVersion          string     `json:"skip_version,omitempty"`
	SkippedAt            *time.Time `json:"skipped_at,omitempty"`
}

func (c *shellCommand) prepareWelcomeState() (string, bool) {
	current := strings.TrimSpace(version.String())
	if current == "" {
		current = "unknown"
	}
	path, err := c.welcomeStatePath(current)
	if err != nil {
		return current, false
	}
	state := shellWelcomeState{}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &state)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return current, false
	}
	state.Version = welcomeStateVersion
	state.LastWelcomedVersion = current
	state.WelcomedAt = c.welcomeClock().UTC()
	state.PendingAttachWelcome = false
	_ = c.writeWelcomeState(path, state)
	return current, true
}

func (c *shellCommand) skipWelcomeVersion(current string) error {
	current = strings.TrimSpace(current)
	if current == "" {
		current = "unknown"
	}
	path, err := c.welcomeStatePath(current)
	if err != nil {
		return err
	}
	state := shellWelcomeState{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	now := c.welcomeClock().UTC()
	state.Version = welcomeStateVersion
	state.LastWelcomedVersion = current
	if state.WelcomedAt.IsZero() {
		state.WelcomedAt = now
	}
	state.PendingAttachWelcome = false
	state.SkipVersion = current
	state.SkippedAt = &now
	return c.writeWelcomeState(path, state)
}

func (c *shellCommand) writeWelcomeState(path string, state shellWelcomeState) error {
	if c.writeFile == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return c.writeFile(path, data, 0o644)
}

func (c *shellCommand) welcomeStatePath(current string) (string, error) {
	home, err := c.home()
	if err != nil {
		return "", err
	}
	return welcomeStatePath(home, c.env("XDG_STATE_HOME"), current)
}

func welcomeStatePath(home, stateHome, current string) (string, error) {
	paths, err := config.Homes{
		HomeDir:   home,
		StateHome: strings.TrimRight(stateHome, string(os.PathSeparator)),
	}.Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, welcomeStateFileName(current)), nil
}

func welcomeStateFileName(current string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(current) {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	safe := strings.Trim(builder.String(), "-")
	if safe == "" {
		safe = "unknown"
	}
	if !strings.HasPrefix(safe, "v") {
		safe = "v" + safe
	}
	return fmt.Sprintf("welcomed-%s.json", safe)
}

func (c *shellCommand) welcomeClock() time.Time {
	if c.update != nil {
		return c.update.clock()
	}
	return time.Now()
}
