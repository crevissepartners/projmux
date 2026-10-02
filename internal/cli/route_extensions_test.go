package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func extensionTestRoute(name string) Route {
	return Route{
		Effects:     unchangedEffects(CardinalityUnchanged),
		Name:        name,
		Invocation:  InvocationNatural,
		Summary:     "Extension route " + name,
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux " + name},
	}
}

func routeNames(nodes []Route) []string {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	return names
}

func TestExtendRoutesPlacesEachExtensionAfterItsAnchor(t *testing.T) {
	t.Parallel()

	base := []Route{extensionTestRoute("alpha"), extensionTestRoute("gamma"), extensionTestRoute("help")}
	got, err := extendRoutes(base, []routeExtension{
		{after: "alpha", route: extensionTestRoute("beta")},
		{after: "gamma", route: extensionTestRoute("delta")},
	})
	if err != nil {
		t.Fatalf("extendRoutes error = %v", err)
	}
	if names := strings.Join(routeNames(got), " "); names != "alpha beta gamma delta help" {
		t.Fatalf("extended routes = %q, want alpha beta gamma delta help", names)
	}
	if names := strings.Join(routeNames(base), " "); names != "alpha gamma help" {
		t.Fatalf("base was modified: %q", names)
	}

	var help bytes.Buffer
	if err := renderRootHelp(&help, got); err != nil {
		t.Fatalf("renderRootHelp error = %v", err)
	}
	if !strings.Contains(help.String(), "  beta      Extension route beta\n  gamma") {
		t.Fatalf("root help does not list the extension after its anchor:\n%s", help.String())
	}
	handlers := map[string]Handler{}
	for _, node := range got {
		handlers[node.Name] = func([]string, io.Writer, io.Writer) error { return nil }
	}
	delete(handlers, "help")
	if _, err := newRoot(RootOptions{Stdout: io.Discard, Stderr: io.Discard, Handlers: handlers}, got); err != nil {
		t.Fatalf("newRoot over the extended routes error = %v", err)
	}
	delete(handlers, "beta")
	if _, err := newRoot(RootOptions{Stdout: io.Discard, Stderr: io.Discard, Handlers: handlers}, got); err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("newRoot without the extension's handler error = %v, want missing beta", err)
	}
}

func TestExtendRoutesRejectsAnExtensionItCannotPlace(t *testing.T) {
	t.Parallel()

	base := []Route{extensionTestRoute("alpha"), extensionTestRoute("help")}
	for _, test := range []struct {
		name       string
		extensions []routeExtension
		want       string
	}{
		{name: "no name", extensions: []routeExtension{{after: "alpha"}}, want: `route extension after "alpha" has no name`},
		{name: "built-in name", extensions: []routeExtension{{after: "alpha", route: extensionTestRoute("help")}}, want: `route extension "help" reuses a top-level route name`},
		{name: "two extensions, one name", extensions: []routeExtension{
			{after: "alpha", route: extensionTestRoute("beta")},
			{after: "help", route: extensionTestRoute("beta")},
		}, want: `route extension "beta" reuses a top-level route name`},
		{name: "unknown anchor", extensions: []routeExtension{{after: "omega", route: extensionTestRoute("beta")}}, want: `route extension "beta" is listed after "omega", which is not a top-level route`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := extendRoutes(base, test.extensions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("extendRoutes error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExtendRoutesWithNoExtensionsIsTheBase(t *testing.T) {
	t.Parallel()

	got, err := extendRoutes(routes, nil)
	if err != nil {
		t.Fatalf("extendRoutes error = %v", err)
	}
	if len(got) != len(routes) || &got[0] != &routes[0] {
		t.Fatalf("extendRoutes with no extensions did not return the built-in table")
	}
}

// TestManifestRoutesHoldTheBuiltInTableInOrder proves the registered
// extensions, whatever they are, only add routes: every built-in route is
// still listed, in its built-in order.
func TestManifestRoutesHoldTheBuiltInTableInOrder(t *testing.T) {
	t.Parallel()

	all := Routes()
	if want := len(routes) + len(routeExtensions); len(all) != want {
		t.Fatalf("manifest has %d top-level routes, want %d built-in plus extensions", len(all), want)
	}
	next := 0
	for _, route := range all {
		if next < len(routes) && route.Name == routes[next].Name {
			next++
		}
	}
	if next != len(routes) {
		t.Fatalf("built-in route %q is missing or out of order in the manifest", routes[next].Name)
	}
}
