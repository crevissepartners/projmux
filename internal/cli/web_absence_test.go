package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webSurfacePaths are the repository paths the HTTP server and the browser
// client occupied. They are listed by the shape they had, not derived from a
// search, so a file reintroduced under any of them fails this test by name.
var webSurfacePaths = []struct {
	dir     string
	pattern string
}{
	{dir: "internal/web"},
	{dir: "internal/app", pattern: "web_*.go"},
	{dir: "internal/config", pattern: "web_settings*.go"},
	{dir: "docs", pattern: "web-api.md"},
}

// TestPublicTreeAndCatalogHaveNoWebSurface is C-1's enforcement: the public
// build ships the CLI and the TUI, so neither the repository tree nor the
// command catalog carries an HTTP server or a browser client.
//
// It holds both halves of the guarantee at once. A source file returning under
// one of the retired paths fails the tree half even when nothing dispatches to
// it, and a route spelled `web` fails the catalog half even when no server
// backs it -- either one alone would let the surface come back by the other
// door.
func TestPublicTreeAndCatalogHaveNoWebSurface(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	for _, surface := range webSurfacePaths {
		dir := filepath.Join(root, filepath.FromSlash(surface.dir))
		if surface.pattern == "" {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("%s exists; the web surface is not part of this repository", surface.dir)
			}
			continue
		}
		matches, err := fs.Glob(os.DirFS(dir), surface.pattern)
		if err != nil {
			t.Fatalf("glob %s/%s: %v", surface.dir, surface.pattern, err)
		}
		for _, match := range matches {
			t.Errorf("%s/%s exists; the web surface is not part of this repository", surface.dir, match)
		}
	}

	if route, ok := LookupRoute("web"); ok {
		t.Errorf("`web` resolves to route %q; it is not a command of this build", route.Name)
	}
	var walk func(path []string, routes []Route)
	walk = func(path []string, routes []Route) {
		for _, route := range routes {
			here := append(append([]string(nil), path...), route.Name)
			if route.Name == "web" {
				t.Errorf("route %q is spelled web", strings.Join(here, " "))
			}
			walk(here, route.Children)
		}
	}
	walk(nil, Routes())
}
