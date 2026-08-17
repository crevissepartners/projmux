package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type managedIngestFileMutation struct {
	path    string
	current string
	next    string
	mode    os.FileMode
	label   string
	write   func() error
	existed bool
}

// migrateManagedIngestProducerFiles canonicalizes only marker-owned producer
// entries. It plans every provider before the first write so a malformed or
// conflicting surface cannot leave an earlier provider partially migrated.
// Missing providers are deliberately not installed by this convergence path.
func (c *aiCommand) beginManagedIngestProducerFileMigration() (int, func() error, error) {
	codexPlan, err := c.planCodexIntegration(false)
	if err != nil {
		return 0, nil, err
	}
	claudePlan, err := c.planClaudeHookIntegration(false)
	if err != nil {
		return 0, nil, err
	}
	hookPlan, err := c.planAntigravityHookIntegration(false)
	if err != nil {
		return 0, nil, err
	}
	statusPlan, err := c.planAntigravityStatusLineIntegration(false)
	if err != nil {
		return 0, nil, err
	}

	for _, conflict := range []string{codexPlan.conflict, claudePlan.conflict, hookPlan.conflict, statusPlan.conflict} {
		if conflict != "" {
			return 0, nil, errors.New(conflict)
		}
	}

	mutations := make([]managedIngestFileMutation, 0, 4)
	if strings.Contains(codexPlan.current, codexHooksMarkerBegin) && codexPlan.changed {
		mutations = append(mutations, managedIngestFileMutation{
			path: codexPlan.path, current: codexPlan.current, next: codexPlan.next, mode: 0o644, label: "Codex config",
			write: func() error { return c.writeCodexConfig(codexPlan.path, []byte(codexPlan.next)) },
		})
	}
	if strings.Contains(claudePlan.current, claudeHookManagedMarker) && claudePlan.changed {
		mutations = append(mutations, managedIngestFileMutation{
			path: claudePlan.path, current: claudePlan.current, next: claudePlan.next, mode: 0o644, label: "Claude settings",
			write: func() error { return c.writeClaudeSettings(claudePlan.path, []byte(claudePlan.next)) },
		})
	}
	if strings.Contains(hookPlan.current, antigravityManagedMarker) && hookPlan.changed {
		mutations = append(mutations, managedIngestFileMutation{
			path: hookPlan.path, current: hookPlan.current, next: hookPlan.next, mode: 0o644, label: "Antigravity hooks",
			write: func() error { return c.writeAntigravityJSON(hookPlan.path, []byte(hookPlan.next), 0o644, "hooks") },
		})
	}
	if strings.Contains(statusPlan.current, antigravityManagedStatusLineMarker) && statusPlan.changed {
		mutations = append(mutations, managedIngestFileMutation{
			path: statusPlan.path, current: statusPlan.current, next: statusPlan.next, mode: 0o600, label: "Antigravity settings",
			write: func() error {
				return c.writeAntigravityJSON(statusPlan.path, []byte(statusPlan.next), 0o600, "settings")
			},
		})
	}

	for i := range mutations {
		info, statErr := os.Stat(mutations[i].path)
		switch {
		case statErr == nil:
			mutations[i].existed = true
			mutations[i].mode = info.Mode().Perm()
		case errors.Is(statErr, os.ErrNotExist):
			mutations[i].existed = false
		default:
			return 0, nil, fmt.Errorf("inspect %s %s: %w", mutations[i].label, mutations[i].path, statErr)
		}
		if err := preflightManagedIngestWrite(mutations[i].path); err != nil {
			return 0, nil, fmt.Errorf("preflight %s %s: %w", mutations[i].label, mutations[i].path, err)
		}
	}

	for i := range mutations {
		if err := mutations[i].write(); err != nil {
			rollbackErr := c.rollbackManagedIngestMutations(mutations[:i+1])
			if rollbackErr != nil {
				return 0, nil, fmt.Errorf("migrate %s: %w (rollback failed: %v)", mutations[i].label, err, rollbackErr)
			}
			return 0, nil, fmt.Errorf("migrate %s: %w", mutations[i].label, err)
		}
	}
	rollback := func() error { return c.rollbackManagedIngestMutations(mutations) }
	return len(mutations), rollback, nil
}

func preflightManagedIngestWrite(path string) error {
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if info.Mode().Perm()&0o222 == 0 {
				return os.ErrPermission
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return os.ErrPermission
		}
		current = parent
	}
}

func (c *aiCommand) rollbackManagedIngestMutations(mutations []managedIngestFileMutation) error {
	var rollbackErr error
	for i := len(mutations) - 1; i >= 0; i-- {
		mutation := mutations[i]
		if !mutation.existed {
			if err := os.Remove(mutation.path); err != nil && !errors.Is(err, os.ErrNotExist) && rollbackErr == nil {
				rollbackErr = err
			}
			continue
		}
		writeFile := c.writeFile
		if writeFile == nil {
			writeFile = os.WriteFile
		}
		if err := writeFile(mutation.path, []byte(mutation.current), mutation.mode); err != nil {
			if rollbackErr == nil {
				rollbackErr = err
			}
			continue
		}
		if err := os.Chmod(mutation.path, mutation.mode); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	return rollbackErr
}
