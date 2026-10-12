package runtime

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"path/filepath"
	"slices"
	"strings"
)

// RefuseStatelessAgentWorkspace is the argv half of resolveAgentWorkspace: the
// refusals a --cwd or --add-dir value earns without reading the Registry or the
// filesystem, returned as usage errors before any lock is taken. The reason
// text is the resolver's own, spelling included -- the production resolver
// always says `create agent`, so a shortcut does too. What needs state (a
// missing path, a root outside every Project, a duplicate only canonicalization
// or the owner root reveals) is left to the resolver inside the transaction.
func RefuseStatelessAgentWorkspace(provider, cwd string, additional []string) error {
	return RefuseStatelessAgentWorkspaceWithFields(provider, cwd, additional, "--cwd", "--add-dir")
}
func RefuseStatelessAgentWorkspaceWithFields(provider, cwd string, additional []string, cwdField, addField string) error {
	spelling := "create agent"
	if len(additional) > 0 && !SupportsAdditionalWritableRoots(provider) {
		return createUsage(UnsupportedAdditionalRootsError(spelling, provider).Error())
	}
	seen := make([]string, 0, len(additional)+1)
	if strings.TrimSpace(cwd) != "" {
		if !AbsoluteWorkspacePath(cwd) {
			return createUsage(AgentWorkspacePathError(spelling, cwdField, cwd, ErrWorkspaceNotAbsolute).Error())
		}
		seen = append(seen, filepath.Clean(strings.TrimSpace(cwd)))
	}
	for _, raw := range additional {
		if !AbsoluteWorkspacePath(raw) {
			return createUsage(AgentWorkspacePathError(spelling, addField, raw, ErrWorkspaceNotAbsolute).Error())
		}
		clean := filepath.Clean(strings.TrimSpace(raw))
		if slices.Contains(seen, clean) {
			return createUsage(DuplicateAdditionalRootError(spelling, raw).Error())
		}
		seen = append(seen, clean)
	}
	return nil
}

func SupportsAdditionalWritableRoots(provider string) bool {
	return provider == "codex" || provider == "claude"
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

// ResolveAgentWorkspaceFor is the whole workspace contract. The public argv
// routes refuse its stateless cases first (RefuseStatelessAgentWorkspace); the
// checks stay here as the defense for stored values -- a restored UI intent, a
// resumed or rebound Agent -- whose refusals remain plain errors.
func ResolveAgentWorkspaceFor(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	return ResolveAgentWorkspaceWithFields(spelling, registry, owner, provider, cwd, additional, "--cwd", "--add-dir")
}
func ResolveAgentWorkspaceWithFields(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string, cwdField, addField string) (coremetadata.AgentWorkspace, error) {
	defaultCWD := strings.TrimSpace(cwd) == ""
	if defaultCWD {
		cwd = owner.Spec.Root
	}
	if len(additional) > 0 && !SupportsAdditionalWritableRoots(provider) {
		return coremetadata.AgentWorkspace{}, UnsupportedAdditionalRootsError(spelling, provider)
	}

	ownerRoot, err := CanonicalExistingDir(owner.Spec.Root)
	if err != nil {
		return coremetadata.AgentWorkspace{}, fmt.Errorf("%s: owner Project root %q: %w", spelling, owner.Spec.Root, err)
	}
	authorized := make([]string, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		root, err := CanonicalExistingDir(project.Spec.Root)
		if err != nil {
			continue
		}
		authorized = append(authorized, root)
	}
	if !slices.Contains(authorized, ownerRoot) {
		authorized = append(authorized, ownerRoot)
	}
	resolve := func(label, raw string) (string, error) {
		clean, err := CanonicalExistingDir(raw)
		if err != nil {
			return "", AgentWorkspacePathError(spelling, label, raw, err)
		}
		for _, root := range authorized {
			if PathWithinTree(root, clean) {
				return clean, nil
			}
		}
		return "", fmt.Errorf("%s: %s %q is outside every registered Project root", spelling, label, raw)
	}

	effective, err := resolve(cwdField, cwd)
	if err != nil {
		return coremetadata.AgentWorkspace{}, err
	}
	roots := make([]string, 0, len(additional))
	for _, raw := range additional {
		root, err := resolve(addField, raw)
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
