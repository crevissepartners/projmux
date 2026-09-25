package app

import (
	"errors"
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
	return resolveAgentWorkspaceFor(canonicalCreateAgent, registry, owner, provider, cwd, additional)
}

// errAgentWorkspaceNotAbsolute is the reason every empty or relative workspace
// path is refused with.
var errAgentWorkspaceNotAbsolute = errors.New("must be an absolute existing directory")

// refuseStatelessAgentWorkspace is the argv half of resolveAgentWorkspace: the
// refusals a --cwd or --add-dir value earns without reading the Registry or the
// filesystem, returned as usage errors before any lock is taken. The reason
// text is the resolver's own, spelling included -- the production resolver
// always says `create agent`, so a shortcut does too. What needs state (a
// missing path, a root outside every Project, a duplicate only canonicalization
// or the owner root reveals) is left to the resolver inside the transaction.
func refuseStatelessAgentWorkspace(provider, cwd string, additional []string) error {
	spelling := canonicalCreateAgent
	if len(additional) > 0 && !supportsAdditionalWritableRoots(provider) {
		return usageError(unsupportedAdditionalRootsError(spelling, provider).Error())
	}
	seen := make([]string, 0, len(additional)+1)
	if strings.TrimSpace(cwd) != "" {
		if !absoluteWorkspacePath(cwd) {
			return usageError(agentWorkspacePathError(spelling, "--cwd", cwd, errAgentWorkspaceNotAbsolute).Error())
		}
		seen = append(seen, filepath.Clean(strings.TrimSpace(cwd)))
	}
	for _, raw := range additional {
		if !absoluteWorkspacePath(raw) {
			return usageError(agentWorkspacePathError(spelling, "--add-dir", raw, errAgentWorkspaceNotAbsolute).Error())
		}
		clean := filepath.Clean(strings.TrimSpace(raw))
		if slices.Contains(seen, clean) {
			return usageError(duplicateAdditionalRootError(spelling, raw).Error())
		}
		seen = append(seen, clean)
	}
	return nil
}

func supportsAdditionalWritableRoots(provider string) bool {
	return provider == aiModeCodex || provider == aiModeClaude
}

func absoluteWorkspacePath(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && filepath.IsAbs(raw)
}

func unsupportedAdditionalRootsError(spelling, provider string) error {
	return fmt.Errorf("%s: provider %q does not support additional writable roots", spelling, provider)
}

func agentWorkspacePathError(spelling, label, raw string, err error) error {
	return fmt.Errorf("%s: %s %q: %w", spelling, label, raw, err)
}

func duplicateAdditionalRootError(spelling, raw string) error {
	return fmt.Errorf("%s: --add-dir %q duplicates the effective workspace or another explicit root", spelling, raw)
}

// resolveAgentWorkspaceFor is the whole workspace contract. The public argv
// routes refuse its stateless cases first (refuseStatelessAgentWorkspace); the
// checks stay here as the defense for stored values -- a restored UI intent, a
// resumed or rebound Agent -- whose refusals remain plain errors.
func resolveAgentWorkspaceFor(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	defaultCWD := strings.TrimSpace(cwd) == ""
	if defaultCWD {
		cwd = owner.Spec.Root
	}
	if len(additional) > 0 && !supportsAdditionalWritableRoots(provider) {
		return coremetadata.AgentWorkspace{}, unsupportedAdditionalRootsError(spelling, provider)
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
			return "", agentWorkspacePathError(spelling, label, raw, err)
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
			return coremetadata.AgentWorkspace{}, duplicateAdditionalRootError(spelling, raw)
		}
		roots = append(roots, root)
	}
	return coremetadata.AgentWorkspace{CWD: effective, AdditionalWritableRoots: roots}, nil
}

func canonicalExistingDir(raw string) (string, error) {
	if !absoluteWorkspacePath(raw) {
		return "", errAgentWorkspaceNotAbsolute
	}
	raw = strings.TrimSpace(raw)
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
