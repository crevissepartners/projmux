package app

import (
	"fmt"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
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
var errAgentWorkspaceNotAbsolute = mutationruntime.ErrWorkspaceNotAbsolute















func canonicalExistingDir(raw string) (string, error) {
	return mutationruntime.CanonicalExistingDir(raw)
}
