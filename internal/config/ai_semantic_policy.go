package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AISemanticPoliciesFileName = "ai-semantic-policies.json"

type AISemanticEvent string

const (
	AISemanticApprovalRequired AISemanticEvent = "approval_required"
	AISemanticResponseComplete AISemanticEvent = "response_complete"
)

type AISemanticPolicy string

const (
	AISemanticNotify    AISemanticPolicy = "notify"
	AISemanticStateOnly AISemanticPolicy = "state"
	AISemanticQuiet     AISemanticPolicy = "quiet"
)

type AISemanticPolicies struct {
	Events map[AISemanticEvent]AISemanticPolicy `json:"events"`
}

func (p Paths) AISemanticPoliciesFile() string {
	return filepath.Join(p.ConfigDir, AISemanticPoliciesFileName)
}

func DefaultAISemanticPolicies() AISemanticPolicies {
	return AISemanticPolicies{Events: map[AISemanticEvent]AISemanticPolicy{
		AISemanticApprovalRequired: AISemanticNotify,
		AISemanticResponseComplete: AISemanticNotify,
	}}
}

func ValidAISemanticPolicy(policy AISemanticPolicy) bool {
	switch policy {
	case AISemanticNotify, AISemanticStateOnly, AISemanticQuiet:
		return true
	default:
		return false
	}
}

func LoadAISemanticPoliciesFile(path string) (AISemanticPolicies, error) {
	defaults := DefaultAISemanticPolicies()
	if strings.TrimSpace(path) == "" {
		return defaults, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller, matching the other config file loaders in this package.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, nil
		}
		return defaults, fmt.Errorf("read AI semantic policies file: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return defaults, nil
	}
	var file AISemanticPolicies
	if err := json.Unmarshal(data, &file); err != nil {
		return defaults, fmt.Errorf("parse AI semantic policies file: %w", err)
	}
	for event := range defaults.Events {
		policy := file.Events[event]
		if ValidAISemanticPolicy(policy) {
			defaults.Events[event] = policy
		}
	}
	return defaults, nil
}

func SaveAISemanticPoliciesFile(path string, file AISemanticPolicies) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	normalized := DefaultAISemanticPolicies()
	for event := range normalized.Events {
		if policy := file.Events[event]; ValidAISemanticPolicy(policy) {
			normalized.Events[event] = policy
		}
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AI semantic policies file: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create AI semantic policies directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, AISemanticPoliciesFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create AI semantic policies temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write AI semantic policies temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close AI semantic policies temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod AI semantic policies temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename AI semantic policies temp file: %w", err)
	}
	return nil
}
