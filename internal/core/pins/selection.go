package pins

import (
	"github.com/crevissepartners/projmux/internal/core/candidates"
	"strings"
)

// Selection is the read-only pin lookup for one discovery or render snapshot.
type Selection struct {
	projectUIDs       map[string]bool
	candidateKeys     map[string]bool
	projectsByRootKey map[string][]string
}

// Select builds the render-pass pin lookup. It writes nothing.
func Select(stored Set, resolver Resolver) Selection {
	out := Selection{
		projectUIDs:       map[string]bool{},
		candidateKeys:     map[string]bool{},
		projectsByRootKey: map[string][]string{},
	}
	for _, ref := range resolver.Projects {
		if key := candidates.MatchKey(ref.Root); key != "" {
			out.projectsByRootKey[key] = append(out.projectsByRootKey[key], ref.UID)
		}
	}
	resolution := resolver.Resolve(stored)
	for _, uid := range resolution.Set.ProjectUIDs() {
		out.projectUIDs[uid] = true
	}
	for _, path := range resolution.Set.CandidatePaths() {
		if key := candidates.MatchKey(path); key != "" {
			out.candidateKeys[key] = true
		}
	}
	return out
}

// PinnedProject reports whether a Registry Project carries a managed pin.
func (s Selection) PinnedProject(uid string) bool {
	return s.projectUIDs[strings.TrimSpace(uid)]
}

// PinnedCandidate reports whether an unregistered path carries a candidate pin.
func (s Selection) PinnedCandidate(path string) bool {
	key := candidates.MatchKey(path)
	return key != "" && s.candidateKeys[key]
}

// PinnedPath reports whether the pin a path argument resolves to is present.
//
// It follows the same resolve-or-candidate rule the pin actions use, so the row
// that says "already pinned" and the action that would pin it agree about which
// entry they mean.
func (s Selection) PinnedPath(path string) bool {
	key := candidates.MatchKey(path)
	if key == "" {
		return false
	}
	for _, uid := range s.projectsByRootKey[key] {
		if s.projectUIDs[uid] {
			return true
		}
	}
	return s.candidateKeys[key]
}
