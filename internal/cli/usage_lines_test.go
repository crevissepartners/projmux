package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestWriteRouteUsageBlockFormatUnchanged pins the `Usage:` block format that
// WriteRouteUsage printed before its block writing moved into WriteUsageLines:
// the heading, then every catalog line indented two spaces, nothing else.
func TestWriteRouteUsageBlockFormatUnchanged(t *testing.T) {
	checked := 0
	var walk func(prefix []string, routes []Route)
	walk = func(prefix []string, routes []Route) {
		for _, route := range routes {
			path := append(append([]string{}, prefix...), route.Name)
			if len(route.Usage) > 0 {
				var want strings.Builder
				want.WriteString("Usage:\n")
				for _, line := range route.Usage {
					want.WriteString("  " + line + "\n")
				}
				var got bytes.Buffer
				WriteRouteUsage(&got, strings.Join(path, " "))
				if got.String() != want.String() {
					t.Fatalf("WriteRouteUsage(%q) = %q, want %q", path, got.String(), want.String())
				}
				checked++
			}
			walk(path, route.Children)
		}
	}
	walk(nil, Routes())
	if checked == 0 {
		t.Fatal("no catalog route declares Usage")
	}
}
