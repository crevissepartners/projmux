package cli

import (
	"fmt"
	"slices"
)

// routeExtension adds one top-level route to the manifest from outside the
// built-in route table. after names the top-level route it is listed after,
// which fixes its place in the primary command listing.
type routeExtension struct {
	after string
	route Route
}

// routeExtensions are the top-level routes a build adds from its own source
// files. A file appends to it from an init function, so every extension is in
// place before a command tree, a help page, or a reference is built.
//
// The tests that pin the built-in surface byte for byte (the command tree, the
// primary listing, the effect manifest, and the route tallies) read the
// built-in table alone; an extension carries its own tests.
var routeExtensions []routeExtension

// manifestRoutes returns the built-in routes with every extension in place. An
// extension that cannot be placed is a build defect, not an input error, so it
// panics the way an invalid effect record does.
func manifestRoutes() []Route {
	nodes, err := extendRoutes(routes, routeExtensions)
	if err != nil {
		panic(err)
	}
	return nodes
}

// extendRoutes returns base with each extension inserted right after its
// anchor. base is not modified. An extension must be named, must not reuse a
// top-level name, and must name an anchor that is already listed.
func extendRoutes(base []Route, extensions []routeExtension) ([]Route, error) {
	if len(extensions) == 0 {
		return base, nil
	}
	out := slices.Clone(base)
	for _, extension := range extensions {
		name := extension.route.Name
		if name == "" {
			return nil, fmt.Errorf("cli: route extension after %q has no name", extension.after)
		}
		if slices.ContainsFunc(out, func(route Route) bool { return route.Name == name }) {
			return nil, fmt.Errorf("cli: route extension %q reuses a top-level route name", name)
		}
		anchor := slices.IndexFunc(out, func(route Route) bool { return route.Name == extension.after })
		if anchor < 0 {
			return nil, fmt.Errorf("cli: route extension %q is listed after %q, which is not a top-level route", name, extension.after)
		}
		out = slices.Insert(out, anchor+1, extension.route)
	}
	return out, nil
}
