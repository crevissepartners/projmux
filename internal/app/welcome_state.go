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
	Version             int       `json:"version"`
	LastWelcomedVersion string    `json:"last_welcomed_version"`
	WelcomedAt          time.Time `json:"welcomed_at"`
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
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var state shellWelcomeState
		if err := json.Unmarshal(data, &state); err != nil {
			return current, false
		}
		if strings.TrimSpace(state.LastWelcomedVersion) == current {
			return current, false
		}
	case errors.Is(err, os.ErrNotExist):
		// First run for this version.
	default:
		return current, false
	}
	if c.writeFile == nil {
		return current, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return current, false
	}
	state := shellWelcomeState{
		Version:             welcomeStateVersion,
		LastWelcomedVersion: current,
		WelcomedAt:          c.welcomeClock().UTC(),
	}
	data, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		return current, false
	}
	if err := c.writeFile(path, data, 0o644); err != nil {
		return current, false
	}
	return current, true
}

func (c *shellCommand) welcomeStatePath(current string) (string, error) {
	home, err := c.home()
	if err != nil {
		return "", err
	}
	paths, err := config.Homes{
		HomeDir:   home,
		StateHome: strings.TrimRight(c.env("XDG_STATE_HOME"), string(os.PathSeparator)),
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
