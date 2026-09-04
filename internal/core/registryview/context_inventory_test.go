package registryview

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestStoredPresentationProductConsumerInventoryIsZero mechanically keeps the
// Phase-1 consumer cutover closed. Phase 2 consumed the exact 11-site
// compatibility handoff, so the production typed-accessor inventory below is
// empty. Only decode-only v3 migration wire fields remain, outside these
// searched accessors; adding a product read is a test failure.
func TestStoredPresentationProductConsumerInventoryIsZero(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	consumers := []string{
		"internal/core/registryview/context.go",
		"internal/core/registryview/view.go",
		"internal/core/selector/selector.go",
		"internal/core/selector/cardinality.go",
		"internal/app/resource_routes.go",
		"internal/app/get.go",
		"internal/app/describe.go",
		"internal/app/registry_navigation_view.go",
		"internal/app/switch_registry_rows.go",
		"internal/app/recent_window_registry.go",
		"internal/app/settings_projects.go",
		"internal/app/ai.go",
	}
	for _, path := range consumers {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read product consumer %s: %v", path, err)
		}
		text := string(data)
		for _, forbidden := range []string{
			"Metadata.DisplayName",
			"meta.DisplayName",
			"Status.DisplayTitle",
			"window.DisplayName()",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s still consumes stored presentation through %q", path, forbidden)
			}
		}
	}
}

func TestStoredPresentationCompatibilityInventoryIsZero(t *testing.T) {
	t.Parallel()

	type site struct {
		path   string
		needle string
	}
	want := map[site]int{}
	got := map[site]int{}
	root := repositoryRoot(t)
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, needle := range []string{"Metadata.DisplayName", "Status.DisplayTitle", "window.DisplayName()"} {
			if count := strings.Count(text, needle); count > 0 {
				got[site{filepath.ToSlash(rel), needle}] = count
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan stored presentation compatibility sites: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("compatibility inventory has %d sites, want %d:\ngot  %#v\nwant %#v", len(got), len(want), got, want)
	}
	for key, count := range want {
		if got[key] != count {
			t.Errorf("compatibility site %s %q count = %d, want %d", key.path, key.needle, got[key], count)
		}
	}
	for key, count := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unclassified stored presentation site %s %q count=%d", key.path, key.needle, count)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve inventory test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
