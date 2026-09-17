package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// settingsRootResultScope is the scope-level fixture the walk tests share.
type settingsRootResultScope struct {
	tab      settingsRootTab
	rootNode string
	axis     SettingsAxis
}

var settingsRootResultScopes = []settingsRootResultScope{
	{settingsRootTabGlobal, settingsNavScopeGlobal, settingsAxisGlobal},
	{settingsRootTabProject, settingsNavScopeProject, settingsAxisProject},
}

// settingsRootResultExpectedNodes walks the navigation catalog independently of
// the product walk and returns the node IDs the scope must produce a result for.
// It is deliberately a second walk rather than a frozen ID list: a node added to
// settingsNodeCatalog grows this expectation and therefore has to grow a result
// row too.
func settingsRootResultExpectedNodes(scope settingsRootResultScope) map[string]bool {
	want := map[string]bool{}
	var descend func(node settingsNavNode)
	descend = func(node settingsNavNode) {
		for _, child := range settingsNavChildren(node.ID) {
			if child.Hidden || child.Axis&scope.axis == 0 || settingsRootResultExcluded(child.ID) {
				continue
			}
			want[child.ID] = true
			descend(child)
		}
	}
	// Depth 1 is the visible root row itself and is not a result; its children
	// (depth 2) and everything below them are.
	for _, category := range settingsNavChildren(scope.rootNode) {
		if category.Hidden || category.Axis&scope.axis == 0 {
			continue
		}
		descend(category)
	}
	return want
}

// settingsRootResultMultiplied reports whether a node may legitimately produce
// more than one row: it is a code-enumerated template itself, or it is a control
// that lives inside one and is therefore rendered once per instance.
func settingsRootResultMultiplied(node settingsNavNode) bool {
	for {
		if node.Dynamic {
			if _, template := settingsRootResultInstances(node.ID, i18n.FallbackLocale, nil); template {
				return true
			}
		}
		parent, ok := settingsNavByID(node.Parent)
		if !ok {
			return false
		}
		node = parent
	}
}

func settingsRootResultNodeCounts(t *testing.T, entries []intpickercompat.Entry) map[string]int {
	t.Helper()

	counts := map[string]int{}
	for _, entry := range entries {
		nodeID, _, ok := parseSettingsRootResultValue(entry.Value)
		if !ok {
			t.Fatalf("result entry %#v does not parse as a root-result value", entry)
		}
		counts[nodeID]++
	}
	return counts
}

// TestSettingsRootResultsCoverEveryCatalogNodeOfTheScope is the walk contract:
// every visible, non-excluded catalogue node below depth 1 is reachable from the
// root by search, a static node exactly once, and nothing outside the catalog is
// ever emitted.
func TestSettingsRootResultsCoverEveryCatalogNodeOfTheScope(t *testing.T) {
	t.Parallel()

	for _, scope := range settingsRootResultScopes {
		t.Run(string(scope.tab), func(t *testing.T) {
			t.Parallel()

			entries := settingsRootResultEntries(scope.tab, i18n.FallbackLocale)
			counts := settingsRootResultNodeCounts(t, entries)
			want := settingsRootResultExpectedNodes(scope)

			for nodeID := range want {
				node, ok := settingsNavByID(nodeID)
				if !ok {
					t.Fatalf("expected node %q is not in the catalog", nodeID)
				}
				switch got := counts[nodeID]; {
				case got == 0:
					t.Errorf("catalog node %q produced no result row", nodeID)
				case got > 1 && !settingsRootResultMultiplied(node):
					t.Errorf("static catalog node %q produced %d result rows, want exactly 1", nodeID, got)
				}
			}
			for nodeID := range counts {
				if !want[nodeID] {
					t.Errorf("result row for node %q, which the scope walk must not emit", nodeID)
				}
			}
			// The scope root and its depth-1 children are the visible root rows.
			if counts[scope.rootNode] != 0 {
				t.Errorf("scope root %q produced %d result rows, want 0", scope.rootNode, counts[scope.rootNode])
			}
			for _, category := range settingsNavChildren(scope.rootNode) {
				if counts[category.ID] != 0 {
					t.Errorf("depth-1 root row %q produced %d result rows, want 0", category.ID, counts[category.ID])
				}
			}
			for _, entry := range entries {
				if !entry.SearchOnly {
					t.Fatalf("result entry %#v is not SearchOnly", entry)
				}
			}
			if err := validateSettingsEntryContracts(intpickercompat.Options{UI: "settings", Entries: entries}); err != nil {
				t.Fatalf("result entry contracts: %v", err)
			}
		})
	}
}

// TestSettingsRootResultsAreScopedToTheirTab keeps the two tabs disjoint and
// keeps the Project tab empty without a project context.
func TestSettingsRootResultsAreScopedToTheirTab(t *testing.T) {
	t.Parallel()

	global := settingsRootResultEntries(settingsRootTabGlobal, i18n.FallbackLocale)
	project := settingsRootResultEntries(settingsRootTabProject, i18n.FallbackLocale)
	if len(global) == 0 || len(project) == 0 {
		t.Fatalf("result counts = global %d, project %d; want both non-empty", len(global), len(project))
	}
	for name, pair := range map[string]struct {
		entries []intpickercompat.Entry
		axis    SettingsAxis
		other   SettingsAxis
	}{
		"global":  {global, settingsAxisGlobal, settingsAxisProject},
		"project": {project, settingsAxisProject, settingsAxisGlobal},
	} {
		for _, entry := range pair.entries {
			nodeID, _, _ := parseSettingsRootResultValue(entry.Value)
			node, ok := settingsNavByID(nodeID)
			if !ok {
				t.Fatalf("%s result %q names no catalog node", name, entry.Value)
			}
			if node.Axis&pair.axis == 0 {
				t.Errorf("%s tab result %q is not in the %s scope", name, nodeID, name)
			}
			if node.Axis == pair.other {
				t.Errorf("%s tab result %q belongs to the other scope", name, nodeID)
			}
		}
	}

	// No project context: the Project tab renders its passive guidance row and
	// nothing else, so it must not offer results for Views it cannot open.
	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	options := cmd.rootOptions(settingsRootTabProject)
	for _, entry := range options.Entries {
		if entry.SearchOnly {
			t.Fatalf("project tab without a project context produced result row %#v", entry)
		}
	}
}

// TestSettingsRootResultsExcludeUserDataCollectionItems pins the exact
// exclusion: the three item templates and their subtrees are user data, while
// their parent Views and collection-level controls stay searchable.
func TestSettingsRootResultsExcludeUserDataCollectionItems(t *testing.T) {
	t.Parallel()

	counts := map[string]int{}
	for _, scope := range settingsRootResultScopes {
		for node, count := range settingsRootResultNodeCounts(t, settingsRootResultEntries(scope.tab, i18n.FallbackLocale)) {
			counts[node] += count
		}
	}

	for _, excluded := range settingsRootResultExcludedSubtrees {
		for _, node := range settingsNodeCatalog {
			if node.ID != excluded && !strings.HasPrefix(node.ID, excluded+".") {
				continue
			}
			if counts[node.ID] != 0 {
				t.Errorf("excluded item subtree node %q produced %d result rows, want 0", node.ID, counts[node.ID])
			}
		}
	}
	// The three parents and every collection-level control stay.
	for _, kept := range []string{
		settingsNavProjectsExtraRoots,
		settingsNavProjectsExtraRoots + ".add-current",
		settingsNavProjectsExtraRoots + ".add-path",
		settingsNavProjectsPins,
		settingsNavProjectsPins + ".pin-current",
		settingsNavProjectsPins + ".select",
		settingsNavProjectsCandidates,
	} {
		if counts[kept] != 1 {
			t.Errorf("collection row %q produced %d result rows, want 1", kept, counts[kept])
		}
	}
}

// settingsRootResultSeamProbe counts the process-external seams a Settings root
// render reaches. The seams the root rows are known to use (a config stat) are
// counted so the test can compare "root rows" against "root rows + results";
// the seams neither path may ever touch fail immediately.
type settingsRootResultSeamProbe struct {
	t     *testing.T
	stats int
}

func (p *settingsRootResultSeamProbe) command(home, project string) *settingsCommand {
	refuse := func(what string) {
		p.t.Helper()
		p.t.Errorf("Settings root render reached the %s seam", what)
	}
	return &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_CWD" {
				return project
			}
			return ""
		},
		osStat: func(path string) (os.FileInfo, error) {
			p.stats++
			return os.Stat(path)
		},
		runCommand: func(name string, _ ...string) error {
			refuse("exec " + name)
			return errors.New("refused")
		},
		runOutput: func(name string, _ ...string) ([]byte, error) {
			refuse("exec " + name)
			return nil, errors.New("refused")
		},
		tmuxRunner: settingsRootResultRefusingTmux{t: p.t},
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration {
			refuse("AI notify diagnostics")
			return nil
		},
		resourceRegistry: func() (coremetadata.Registry, error) {
			refuse("Registry")
			return coremetadata.Registry{}, errors.New("refused")
		},
	}
}

type settingsRootResultRefusingTmux struct{ t *testing.T }

func (r settingsRootResultRefusingTmux) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.t.Errorf("Settings root render ran %s %v", name, args)
	return nil, errors.New("refused")
}

// TestSettingsRootResultsReadNoProcessState is the purity guard. The walk is a
// package function with no settingsCommand, so no seam is even in scope for it;
// this pins that property from the outside. Adding the results to the root costs
// zero extra filesystem calls, never reaches tmux, the Registry or an
// executable, and the rows do not move when the on-disk overrides that do change
// the matching rendered Views are present.
func TestSettingsRootResultsReadNoProcessState(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	mkdirAll(t, filepath.Join(project, ".projmux"))

	rowsOnly := &settingsRootResultSeamProbe{t: t}
	rowsOnly.command(home, project).rootEntriesForTabLocale(settingsRootTabGlobal, i18n.FallbackLocale)

	withResults := &settingsRootResultSeamProbe{t: t}
	options := withResults.command(home, project).rootOptions(settingsRootTabGlobal)
	if withResults.stats != rowsOnly.stats {
		t.Errorf("root with results made %d filesystem stats, want the %d the root rows already make", withResults.stats, rowsOnly.stats)
	}
	results := 0
	for _, entry := range options.Entries {
		if entry.SearchOnly {
			results++
		}
	}
	if results == 0 {
		t.Fatalf("root options = %d entries, want search results among them", len(options.Entries))
	}

	// The same rows must come out of a HOME stuffed with the overrides that do
	// change the matching rendered Views, and with no tmux on PATH.
	stuffed := t.TempDir()
	config := filepath.Join(stuffed, ".config", "projmux")
	mkdirAll(t, config)
	writeFile(t, filepath.Join(config, "keymap.toml"), "schema_version = 2\n\n[bindings.ProjectSidebarToggle]\nkeys = []\n")
	writeFile(t, filepath.Join(config, "workdirs"), filepath.Join(stuffed, "extra-root")+"\n")
	writeFile(t, filepath.Join(config, "ai-hook-catalog-claude.json"), `{"provider":"claude","events":[{"name":"OverrideOnly","install":true,"action":"notify"}]}`)
	t.Setenv("PATH", filepath.Join(stuffed, "no-binaries"))
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("HOME", stuffed)

	for _, scope := range settingsRootResultScopes {
		for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
			before := settingsRootResultEntries(scope.tab, locale)
			after := settingsRootResultEntries(scope.tab, locale)
			if len(before) != len(after) {
				t.Fatalf("%s/%s result count is not stable: %d then %d", scope.tab, locale, len(before), len(after))
			}
			for i := range before {
				if before[i] != after[i] {
					t.Fatalf("%s/%s result %d changed with the environment: %#v then %#v", scope.tab, locale, i, before[i], after[i])
				}
			}
			for _, entry := range after {
				if strings.Contains(entry.Label, "OverrideOnly") {
					t.Fatalf("%s/%s result row read the on-disk AI hook catalog override: %#v", scope.tab, locale, entry)
				}
			}
		}
	}
}

// TestSettingsRootResultsFindDeepRowsInBothLocales walks the real root option
// builder and searches it the way the picker does.
func TestSettingsRootResultsFindDeepRowsInBothLocales(t *testing.T) {
	for _, tt := range []struct {
		locale i18n.Locale
		env    string
		leaf   string
	}{
		{i18n.FallbackLocale, "en-US", "Icon"},
		{"ko-KR", "ko-KR", ""},
	} {
		t.Run(tt.env, func(t *testing.T) {
			home := t.TempDir()
			cmd := &settingsCommand{
				homeDir: func() (string, error) { return home, nil },
				lookupEnv: func(name string) string {
					if name == i18n.LocaleEnvName {
						return tt.env
					}
					return ""
				},
			}
			options := intpickercompat.PickerOptions(withSettingsRenderedLabelSearchText(cmd.rootOptions(settingsRootTabGlobal)))

			// Appearance > Status Bar > Git > Icon, four levels down.
			want := settingsActionPrefixRootResult + settingsNavStatusBar + ".git.icon"
			var target intpicker.Item
			for _, item := range options.Items {
				if item.Value == want {
					target = item
				}
			}
			if target.Value == "" {
				t.Fatalf("root items carry no %q row", want)
			}
			path := strings.TrimSpace(stripSettingsLabelANSI(target.Label))
			segments := strings.Split(path, settingsRootResultSeparator)
			if len(segments) != 4 {
				t.Fatalf("result label %q has %d segments, want 4", path, len(segments))
			}
			leaf := tt.leaf
			if leaf == "" {
				leaf = settingsCatalogTextLocale(tt.locale, "Icon")
				if leaf == "Icon" {
					t.Fatalf("ko-KR leaf label is still %q; the localized query would not prove anything", leaf)
				}
			}
			if got := segments[len(segments)-1]; got != leaf {
				t.Fatalf("result leaf label = %q, want %q", got, leaf)
			}
			// The rendered path itself and the localized leaf alone both find
			// the row through the picker's own filter.
			for _, query := range []string{path, leaf, strings.Join(segments[1:], " ")} {
				found := false
				for _, item := range intpicker.FilterItems(options.Items, query) {
					if item.Value == want {
						found = true
					}
				}
				if !found {
					t.Errorf("query %q does not find %q", query, want)
				}
			}

			// At an empty query the result rows are invisible and the visible
			// list is exactly the root rows.
			visible := intpicker.FilterItems(options.Items, "")
			for _, item := range visible {
				if item.SearchOnly {
					t.Fatalf("empty query kept SearchOnly row %#v", item)
				}
				if _, _, ok := parseSettingsRootResultValue(item.Value); ok {
					t.Fatalf("empty query kept result row %q", item.Value)
				}
			}
			if len(visible) >= len(options.Items) {
				t.Fatalf("empty query kept %d of %d rows, want the results dropped", len(visible), len(options.Items))
			}
		})
	}
}
