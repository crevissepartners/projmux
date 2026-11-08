package projmux

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// catalogRoutePaths lists the exact canonical path of every catalog route,
// nested routes included.
func catalogRoutePaths() []string {
	var paths []string
	var walk func(prefix []string, routes []cli.Route)
	walk = func(prefix []string, routes []cli.Route) {
		for _, route := range routes {
			path := append(append([]string{}, prefix...), route.Name)
			paths = append(paths, strings.Join(path, " "))
			walk(path, route.Children)
		}
	}
	walk(nil, cli.Routes())
	return paths
}

func lookupUsage(t *testing.T, route string) []string {
	t.Helper()
	_, resolved, ok := cli.Resolve(strings.Fields(route))
	if !ok {
		t.Fatalf("route %q does not resolve", route)
	}
	return resolved.Usage
}

// TestPublicUsageEqualsCatalogRoute pins WriteUsage and SetUsage over a
// catalog route's own Usage lines byte-equal to cli.WriteRouteUsage.
func TestPublicUsageEqualsCatalogRoute(t *testing.T) {
	for _, route := range catalogRoutePaths() {
		lines := lookupUsage(t, route)
		var want, got bytes.Buffer
		cli.WriteRouteUsage(&want, route)
		WriteUsage(&got, lines)
		if got.String() != want.String() {
			t.Fatalf("WriteUsage(%q lines) = %q, want %q", route, got.String(), want.String())
		}
		fs := flag.NewFlagSet("not-a-catalog-route", flag.ContinueOnError)
		var out bytes.Buffer
		fs.SetOutput(&out)
		SetUsage(fs, lines)
		fs.Usage()
		if out.String() != want.String() {
			t.Fatalf("SetUsage(%q lines) = %q, want %q", route, out.String(), want.String())
		}
	}
}

// TestPublicUsageEdges covers the boundaries: no lines write nothing and
// SetUsage copies its lines.
func TestPublicUsageEdges(t *testing.T) {
	var buf bytes.Buffer
	WriteUsage(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("empty or unknown usage wrote %q", buf.String())
	}
	WriteUsage(&buf, []string{"tool run [flags]"})
	if want := "Usage:\n  tool run [flags]\n"; buf.String() != want {
		t.Fatalf("WriteUsage = %q, want %q", buf.String(), want)
	}
	lines := []string{"a"}
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	var out bytes.Buffer
	fs.SetOutput(&out)
	SetUsage(fs, lines)
	lines[0] = "mutated"
	fs.Usage()
	if want := "Usage:\n  a\n"; out.String() != want {
		t.Fatalf("SetUsage kept caller slice: %q, want %q", out.String(), want)
	}
}

// TestPublicUsageHasNoSecondDefinition keeps the block heading out of the
// public package: the format lives in internal/cli only.
func TestPublicUsageHasNoSecondDefinition(t *testing.T) {
	src, err := os.ReadFile("route_usage.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"Usage:`) {
		t.Fatal("route_usage.go defines the Usage heading; it must call internal/cli")
	}
}
