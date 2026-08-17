package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// resolveAgentWorkspace validates the provider-neutral filesystem contract
// before provider argv construction or any create mutation. The authorized set
// is deliberately closed: existing Registry Project roots and their
// descendants. A caller may select another registered Project tree without
// changing the owner Window's Project, but cannot widen access to a parent or
// an unregistered sibling.
func resolveAgentWorkspace(registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	return resolveAgentWorkspaceFor("create agent", registry, owner, provider, cwd, additional)
}

func resolveAgentWorkspaceFor(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	defaultCWD := strings.TrimSpace(cwd) == ""
	if defaultCWD {
		cwd = owner.Spec.Root
	}
	if len(additional) > 0 && provider != aiModeCodex && provider != aiModeClaude {
		return coremetadata.AgentWorkspace{}, fmt.Errorf("%s: provider %q does not support additional writable roots", spelling, provider)
	}

	ownerRoot, err := canonicalExistingDir(owner.Spec.Root)
	if err != nil {
		return coremetadata.AgentWorkspace{}, fmt.Errorf("%s: owner Project root %q: %w", spelling, owner.Spec.Root, err)
	}
	authorized := make([]string, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		root, err := canonicalExistingDir(project.Spec.Root)
		if err != nil {
			continue
		}
		authorized = append(authorized, root)
	}
	if !slices.Contains(authorized, ownerRoot) {
		authorized = append(authorized, ownerRoot)
	}
	resolve := func(label, raw string) (string, error) {
		clean, err := canonicalExistingDir(raw)
		if err != nil {
			return "", fmt.Errorf("%s: %s %q: %w", spelling, label, raw, err)
		}
		for _, root := range authorized {
			if pathWithinTree(root, clean) {
				return clean, nil
			}
		}
		return "", fmt.Errorf("%s: %s %q is outside every registered Project root", spelling, label, raw)
	}

	effective, err := resolve("--cwd", cwd)
	if err != nil {
		return coremetadata.AgentWorkspace{}, err
	}
	roots := make([]string, 0, len(additional))
	for _, raw := range additional {
		root, err := resolve("--add-dir", raw)
		if err != nil {
			return coremetadata.AgentWorkspace{}, err
		}
		if root == effective || slices.Contains(roots, root) {
			return coremetadata.AgentWorkspace{}, fmt.Errorf("%s: --add-dir %q duplicates the effective workspace or another explicit root", spelling, raw)
		}
		roots = append(roots, root)
	}
	return coremetadata.AgentWorkspace{CWD: effective, AdditionalWritableRoots: roots}, nil
}

func canonicalExistingDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return "", fmt.Errorf("must be an absolute existing directory")
	}
	clean := filepath.Clean(raw)
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("is not a directory")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}
