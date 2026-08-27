package cli

import (
	"slices"
	"strings"
)

// CanonicalRoute is the canonical projection of one executable command-graph
// node. The graph owns every spelling, summary, source edge, output mode, and
// field; this value contains no independently maintained command data.
type CanonicalRoute struct {
	Spelling string
	Summary  string
	Sources  []string
	Outputs  []OutputMode
	Fields   []FieldProjection
}

type canonicalProjection struct {
	route     CanonicalRoute
	order     int
	nodeOrder int
	index     int
	selfFirst bool
}

// CanonicalRoutes projects the canonical command contract from the executable
// graph. A node owns a canonical command when its own path appears in its
// Canonical edges. Canonical family order is carried by the owning top-level
// graph node; order within a family is the graph's depth-first command order.
func CanonicalRoutes() []CanonicalRoute {
	owners := make(map[string]*canonicalProjection)
	var sequence int
	walkCanonicalGraph(Routes(), func(path []string, node Route) {
		spelling := strings.Join(path, " ")
		if !slices.Contains(node.Canonical, spelling) {
			return
		}
		summary := node.Summary
		if node.CanonicalSummary != "" {
			summary = node.CanonicalSummary
		}
		outputs := node.Outputs
		if node.AcceptedOutputs != nil {
			outputs = node.AcceptedOutputs
		}
		owners[spelling] = &canonicalProjection{
			route: CanonicalRoute{
				Spelling: spelling,
				Summary:  summary,
				Outputs:  slices.Clone(outputs),
				Fields:   slices.Clone(node.Fields),
			},
			order:     canonicalFamilyOrder(path[0]),
			nodeOrder: node.CanonicalNodeOrder,
			index:     sequence,
			selfFirst: node.CanonicalSelfFirst,
		}
		sequence++
	})

	// Source mappings are graph edges too. Keep the historical projection order:
	// compatibility sources precede the canonical owner except where the owner
	// explicitly marks itself first.
	walkCanonicalGraph(Routes(), func(path []string, node Route) {
		for _, spelling := range node.Canonical {
			owner, ok := owners[spelling]
			if !ok {
				continue
			}
			source := path[0]
			if slices.Contains(owner.route.Sources, source) {
				continue
			}
			owner.route.Sources = append(owner.route.Sources, source)
		}
	})

	projected := make([]canonicalProjection, 0, len(owners))
	for _, owner := range owners {
		self := strings.Fields(owner.route.Spelling)[0]
		slices.SortStableFunc(owner.route.Sources, func(a, b string) int {
			aSelf, bSelf := a == self, b == self
			if aSelf == bSelf {
				return 0
			}
			if owner.selfFirst {
				if aSelf {
					return -1
				}
				return 1
			}
			if aSelf {
				return 1
			}
			return -1
		})
		projected = append(projected, *owner)
	}
	slices.SortFunc(projected, func(a, b canonicalProjection) int {
		if a.order != b.order {
			return a.order - b.order
		}
		if a.nodeOrder != 0 || b.nodeOrder != 0 {
			if a.nodeOrder == 0 {
				return 1
			}
			if b.nodeOrder == 0 {
				return -1
			}
			return a.nodeOrder - b.nodeOrder
		}
		return a.index - b.index
	})
	out := make([]CanonicalRoute, len(projected))
	for i := range projected {
		out[i] = projected[i].route
	}
	return out
}

func walkCanonicalGraph(nodes []Route, visit func(path []string, route Route)) {
	var walk func(prefix []string, nodes []Route)
	walk = func(prefix []string, nodes []Route) {
		for _, node := range nodes {
			path := append(append([]string{}, prefix...), node.Name)
			visit(path, node)
			walk(path, node.Children)
		}
	}
	walk(nil, nodes)
}

func canonicalFamilyOrder(root string) int {
	for _, route := range routes {
		if route.Name == root {
			return route.CanonicalOrder
		}
	}
	return 0
}

// LookupCanonicalRoute returns the canonical projection for spelling.
func LookupCanonicalRoute(spelling string) (CanonicalRoute, bool) {
	for _, route := range CanonicalRoutes() {
		if route.Spelling == spelling {
			return route, true
		}
	}
	return CanonicalRoute{}, false
}
