package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"path/filepath"
	"slices"
	"strings"
)

// refuseStatelessAgentWorkspace is the argv half of resolveAgentWorkspace: the
// refusals a --cwd or --add-dir value earns without reading the Registry or the
// filesystem, returned as usage errors before any lock is taken. The reason
// text is the resolver's own, spelling included -- the production resolver
// always says `create agent`, so a shortcut does too. What needs state (a
// missing path, a root outside every Project, a duplicate only canonicalization
// or the owner root reveals) is left to the resolver inside the transaction.
func RefuseStatelessAgentWorkspace(provider, cwd string, additional []string) error {
	spelling := canonicalCreateAgent
	if len(additional) > 0 && !SupportsAdditionalWritableRoots(provider) {
		return usageError(UnsupportedAdditionalRootsError(spelling, provider).Error())
	}
	seen := make([]string, 0, len(additional)+1)
	if strings.TrimSpace(cwd) != "" {
		if !AbsoluteWorkspacePath(cwd) {
			return usageError(AgentWorkspacePathError(spelling, "--cwd", cwd, errAgentWorkspaceNotAbsolute).Error())
		}
		seen = append(seen, filepath.Clean(strings.TrimSpace(cwd)))
	}
	for _, raw := range additional {
		if !AbsoluteWorkspacePath(raw) {
			return usageError(AgentWorkspacePathError(spelling, "--add-dir", raw, errAgentWorkspaceNotAbsolute).Error())
		}
		clean := filepath.Clean(strings.TrimSpace(raw))
		if slices.Contains(seen, clean) {
			return usageError(DuplicateAdditionalRootError(spelling, raw).Error())
		}
		seen = append(seen, clean)
	}
	return nil
}

func SupportsAdditionalWritableRoots(provider string) bool {
	return provider == aiModeCodex || provider == aiModeClaude
}

func AbsoluteWorkspacePath(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw != "" && filepath.IsAbs(raw)
}

func UnsupportedAdditionalRootsError(spelling, provider string) error {
	return fmt.Errorf("%s: provider %q does not support additional writable roots", spelling, provider)
}

func AgentWorkspacePathError(spelling, label, raw string, err error) error {
	return fmt.Errorf("%s: %s %q: %w", spelling, label, raw, err)
}

func DuplicateAdditionalRootError(spelling, raw string) error {
	return fmt.Errorf("%s: --add-dir %q duplicates the effective workspace or another explicit root", spelling, raw)
}

// resolveAgentWorkspaceFor is the whole workspace contract. The public argv
// routes refuse its stateless cases first (refuseStatelessAgentWorkspace); the
// checks stay here as the defense for stored values -- a restored UI intent, a
// resumed or rebound Agent -- whose refusals remain plain errors.
func ResolveAgentWorkspaceFor(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	defaultCWD := strings.TrimSpace(cwd) == ""
	if defaultCWD {
		cwd = owner.Spec.Root
	}
	if len(additional) > 0 && !SupportsAdditionalWritableRoots(provider) {
		return coremetadata.AgentWorkspace{}, UnsupportedAdditionalRootsError(spelling, provider)
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
			return "", AgentWorkspacePathError(spelling, label, raw, err)
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
			return coremetadata.AgentWorkspace{}, DuplicateAdditionalRootError(spelling, raw)
		}
		roots = append(roots, root)
	}
	return coremetadata.AgentWorkspace{CWD: effective, AdditionalWritableRoots: roots}, nil
}
