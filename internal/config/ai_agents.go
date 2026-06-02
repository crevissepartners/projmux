package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AIEnabledAgentsFileName = "ai-enabled-agents"

type AIAgentProvider string

const (
	AIAgentClaude AIAgentProvider = "claude"
	AIAgentCodex  AIAgentProvider = "codex"
)

var DefaultAIEnabledAgents = []AIAgentProvider{AIAgentClaude, AIAgentCodex}

func (p Paths) AIEnabledAgentsFile() string {
	return filepath.Join(p.ConfigDir, AIEnabledAgentsFileName)
}

func KnownAIAgentProviders() []AIAgentProvider {
	return append([]AIAgentProvider(nil), DefaultAIEnabledAgents...)
}

func NormalizeAIEnabledAgents(values []string) []AIAgentProvider {
	seen := map[AIAgentProvider]bool{}
	normalized := make([]AIAgentProvider, 0, len(DefaultAIEnabledAgents))
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case string(AIAgentClaude):
			if !seen[AIAgentClaude] {
				normalized = append(normalized, AIAgentClaude)
				seen[AIAgentClaude] = true
			}
		case string(AIAgentCodex):
			if !seen[AIAgentCodex] {
				normalized = append(normalized, AIAgentCodex)
				seen[AIAgentCodex] = true
			}
		}
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
