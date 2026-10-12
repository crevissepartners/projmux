package pins

import "strings"

// DiscoveryPaths projects stored pins to configured paths without migration.
func DiscoveryPaths(stored Set, resolver Resolver) []string {
	roots := map[string]string{}
	for _, ref := range resolver.Projects {
		roots[ref.UID] = ref.Root
	}
	resolution := resolver.Resolve(stored)
	out := make([]string, 0, len(resolution.Set.Pins))
	for _, pin := range resolution.Set.Pins {
		switch pin.Kind {
		case KindCandidate:
			out = append(out, pin.Value)
		case KindProject:
			if root := strings.TrimSpace(roots[pin.Value]); root != "" {
				out = append(out, root)
			}
		}
	}
	return out
}
