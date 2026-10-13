package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentDefaultHostFileName holds the central default host preference.
const AgentDefaultHostFileName = "agent-default-host"

type AgentHost string

const (
	AgentHostTmux    AgentHost = "tmux"
	AgentHostProcess AgentHost = "process"
)

func (p Paths) AgentDefaultHostFile() string {
	return filepath.Join(p.ConfigDir, AgentDefaultHostFileName)
}

// NormalizeAgentHost returns tmux for unset or invalid stored content.
func NormalizeAgentHost(value string) AgentHost {
	if strings.EqualFold(strings.TrimSpace(value), string(AgentHostProcess)) {
		return AgentHostProcess
	}
	return AgentHostTmux
}

// LoadAgentDefaultHostFile returns a usable default alongside any read error.
func LoadAgentDefaultHostFile(path string) (AgentHost, error) {
	if strings.TrimSpace(path) == "" {
		return AgentHostTmux, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentHostTmux, nil
		}
		return AgentHostTmux, fmt.Errorf("read agent default host file: %w", err)
	}
	return NormalizeAgentHost(string(content)), nil
}

// SaveAgentDefaultHostFile validates before creating directories or files.
func SaveAgentDefaultHostFile(path string, host AgentHost) error {
	switch host {
	case AgentHostTmux, AgentHostProcess:
		return saveAgentQuestionFile(path, AgentDefaultHostFileName, "agent default host", string(host))
	default:
		return fmt.Errorf("unknown agent default host %q; want tmux or process", host)
	}
}
