package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

const AIEnabledAgentsFileName = "ai-enabled-agents"

type AIAgentProvider string

const (
	AIAgentClaude      AIAgentProvider = "claude"
	AIAgentCodex       AIAgentProvider = "codex"
	AIAgentAntigravity AIAgentProvider = "antigravity"
)

var DefaultAIEnabledAgents = knownAIEnabledAgentProviders()

func (p Paths) AIEnabledAgentsFile() string {
	return filepath.Join(p.ConfigDir, AIEnabledAgentsFileName)
}

func KnownAIAgentProviders() []AIAgentProvider {
	return knownAIEnabledAgentProviders()
}

func NormalizeAIEnabledAgents(values []string) []AIAgentProvider {
	seen := map[AIAgentProvider]bool{}
	known := map[AIAgentProvider]bool{}
	for _, agent := range KnownAIAgentProviders() {
		known[agent] = true
	}
	normalized := make([]AIAgentProvider, 0, len(known))
	for _, value := range values {
		agent := AIAgentProvider(strings.ToLower(strings.TrimSpace(value)))
		if !known[agent] || seen[agent] {
			continue
		}
		normalized = append(normalized, agent)
		seen[agent] = true
	}
	return normalized
}

func LoadAIEnabledAgentsFile(path string) ([]AIAgentProvider, error) {
	if strings.TrimSpace(path) == "" {
		return append([]AIAgentProvider(nil), DefaultAIEnabledAgents...), nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return append([]AIAgentProvider(nil), DefaultAIEnabledAgents...), nil
		}
		return append([]AIAgentProvider(nil), DefaultAIEnabledAgents...), fmt.Errorf("read AI enabled agents file: %w", err)
	}
	return NormalizeAIEnabledAgents(splitAIEnabledAgentNames(string(content))), nil
}

func SaveAIEnabledAgentsFile(path string, agents []AIAgentProvider) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}

	values := make([]string, 0, len(agents))
	for _, agent := range agents {
		values = append(values, string(agent))
	}
	normalized := NormalizeAIEnabledAgents(values)
	names := make([]string, 0, len(normalized))
	for _, agent := range normalized {
		names = append(names, string(agent))
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create AI enabled agents directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, AIEnabledAgentsFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create AI enabled agents temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(strings.Join(names, ",") + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write AI enabled agents temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AI enabled agents temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod AI enabled agents temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename AI enabled agents temp file: %w", err)
	}
	return nil
}

func splitAIEnabledAgentNames(content string) []string {
	return strings.FieldsFunc(content, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func knownAIEnabledAgentProviders() []AIAgentProvider {
	providers := aiprovider.SettingsVisible()
	out := make([]AIAgentProvider, 0, len(providers))
	for _, provider := range providers {
		out = append(out, AIAgentProvider(provider.ID))
	}
	return out
}
