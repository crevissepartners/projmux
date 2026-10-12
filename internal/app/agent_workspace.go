package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

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
var errAgentWorkspaceNotAbsolute = mutationruntime.ErrWorkspaceNotAbsolute

// refuseStatelessAgentWorkspace is the argv half of resolveAgentWorkspace: the
// refusals a --cwd or --add-dir value earns without reading the Registry or the
// filesystem, returned as usage errors before any lock is taken. The reason
// text is the resolver's own, spelling included -- the production resolver
// always says `create agent`, so a shortcut does too. What needs state (a
// missing path, a root outside every Project, a duplicate only canonicalization
// or the owner root reveals) is left to the resolver inside the transaction.
func refuseStatelessAgentWorkspace(provider, cwd string, additional []string) error {
	return mutationruntime.RefuseStatelessAgentWorkspace(provider, cwd, additional)
}

// resolveAgentWorkspaceFor is the whole workspace contract. The public argv
// routes refuse its stateless cases first (refuseStatelessAgentWorkspace); the
// checks stay here as the defense for stored values -- a restored UI intent, a
// resumed or rebound Agent -- whose refusals remain plain errors.
func resolveAgentWorkspaceFor(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	return mutationruntime.ResolveAgentWorkspaceFor(spelling, registry, owner, provider, cwd, additional)
}

func canonicalExistingDir(raw string) (string, error) {
	return mutationruntime.CanonicalExistingDir(raw)
}
