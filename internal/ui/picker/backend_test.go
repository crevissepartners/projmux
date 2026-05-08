package picker

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResolveBackendDefaultsToFZF(t *testing.T) {
	t.Parallel()

	if got := ResolveBackend(nil); got != BackendFZF {
		t.Fatalf("ResolveBackend(nil) = %q, want %q", got, BackendFZF)
	}
	if got := ResolveBackend(func(string) string { return "unknown" }); got != BackendFZF {
		t.Fatalf("ResolveBackend(unknown) = %q, want %q", got, BackendFZF)
	}
}

func TestResolveBackendAllowsNativeOptIn(t *testing.T) {
	t.Parallel()

	got := ResolveBackend(func(name string) string {
		if name != BackendEnv {
			t.Fatalf("env name = %q, want %q", name, BackendEnv)
		}
		return "native"
	})
	if got != BackendNative {
		t.Fatalf("ResolveBackend(native) = %q, want %q", got, BackendNative)
	}
}

func TestFilterItemsUsesSearchTextNotMetadata(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "api", Value: "1", SearchText: "api service", MetaLines: []string{"postgres"}},
		{Title: "worker", Value: "2", SearchText: "worker", MetaLines: []string{"api"}},
	}

	filtered := FilterItems(items, "api")
	if len(filtered) != 1 || filtered[0].Value != "1" {
		t.Fatalf("FilterItems(api) = %#v, want only search-text match", filtered)
	}
}

func TestFilterItemsRanksBetterMatchesFirst(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "slow", Value: "1", SearchText: "bravo archived project index"},
		{Title: "exact", Value: "2", SearchText: "api"},
		{Title: "prefix", Value: "3", SearchText: "api service"},
	}

	filtered := FilterItems(items, "api")
	if got, want := valuesOf(filtered), []string{"2", "3", "1"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(api) values = %#v, want %#v", got, want)
	}
}

func TestNativeRunnerFiltersAndSelectsByNumber(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	runner := NativeRunner{
		In:  strings.NewReader("api\n1\n"),
		Out: &out,
	}

	result, err := runner.Run(Options{
		UI:     "switch",
		Prompt: "> ",
		Items: []Item{
			{Label: "API\n  branch main", Title: "api", Value: "/repo/api", SearchText: "api"},
			{Label: "Worker\n  mentions api in metadata", Title: "worker", Value: "/repo/worker", SearchText: "worker", MetaLines: []string{"api"}},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Value != "/repo/api" || result.Query != "api" || result.Key != "enter" {
		t.Fatalf("Run() = %#v, want selected API", result)
	}
	if !strings.Contains(out.String(), "> query: api") {
		t.Fatalf("native output = %q, want filtered query prompt", out.String())
	}
}

func valuesOf(items []Item) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Value)
	}
	return values
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNativeRunnerUsesSharedCloseActions(t *testing.T) {
	t.Parallel()

	result, err := (NativeRunner{In: strings.NewReader("alt-4\n")}).Run(Options{
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CloseActions("alt-4"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Closed || result.Key != "alt-4" {
		t.Fatalf("Run() = %#v, want close action", result)
	}
}

func TestNativeRunnerAcceptsTypedQuery(t *testing.T) {
	t.Parallel()

	result, err := (NativeRunner{In: strings.NewReader("/tmp/work\n")}).Run(Options{
		UI:          "settings-workdir-typed",
		AcceptQuery: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Key != "enter" || result.Query != "/tmp/work" || result.Value != "" {
		t.Fatalf("Run() = %#v, want typed query result", result)
	}
}

func TestNativeInteractiveSupportsArrowSelection(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	result, err := runNativeInteractive(strings.NewReader("\x1b[B\r"), &out, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "enter" || result.Value != "/repo/web" {
		t.Fatalf("result = %#v, want second item selected", result)
	}
	if strings.Contains(out.String(), "^[[") {
		t.Fatalf("native output leaked escape input: %q", out.String())
	}
}

func TestNativeInteractiveSupportsApplicationCursorKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1bOB\r"), io.Discard, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "enter" || result.Value != "/repo/web" || result.Query != "" {
		t.Fatalf("result = %#v, want application-cursor down to select web", result)
	}
}

func TestNativeInteractiveConsumesModifiedCSIKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[1;5B\r"), io.Discard, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
		Actions: []Action{{Key: "ctrl-down", Intent: ActionCustom}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "ctrl-down" || result.Value != "/repo/api" || result.Query != "" {
		t.Fatalf("result = %#v, want ctrl-down custom action without query leakage", result)
	}
}

func TestNativeInteractiveFiltersWithPrintableInput(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("we\r"), io.Discard, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api", SearchText: "api"},
			{Title: "web", Value: "/repo/web", SearchText: "web"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "/repo/web" || result.Query != "we" {
		t.Fatalf("result = %#v, want filtered web selection", result)
	}
}

func TestNativeInteractiveSupportsCustomExpectKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1bp"), io.Discard, Options{
		UI:      "switch",
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CustomActions("alt-p"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "alt-p" || result.Value != "/repo/api" {
		t.Fatalf("result = %#v, want alt-p custom action on selected item", result)
	}
}

func TestNativeInteractiveSupportsPageNavigationAndEditing(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, Item{Title: strconv.Itoa(i), Value: strconv.Itoa(i), SearchText: strconv.Itoa(i)})
	}

	result, err := runNativeInteractive(strings.NewReader("abc def\x17\x15\x1b[6~\r"), io.Discard, Options{
		UI:    "switch",
		Items: items,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "12" || result.Query != "" {
		t.Fatalf("result = %#v, want page-down selection after query editing", result)
	}
}

func TestNativeInteractiveRendersSelectedPreview(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	_, err := runNativeInteractive(strings.NewReader("\r"), &out, Options{
		UI: "switch",
		Preview: Preview{
			Command: "printf 'preview:%s' {2}",
		},
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !strings.Contains(out.String(), "preview:/repo/api") {
		t.Fatalf("native output = %q, want selected preview output", out.String())
	}
	if strings.Contains(out.String(), "Type to search") {
		t.Fatalf("native output = %q, want fzf-like prompt without generic help row", out.String())
	}
}

func TestNativePromptLineIncludesInlineMatchCount(t *testing.T) {
	t.Parallel()

	line := nativePromptLine("› ", "api", 2, 8, 20)
	if !strings.Contains(line, "› api") || !strings.HasSuffix(line, "2/8") {
		t.Fatalf("nativePromptLine() = %q, want prompt and inline count", line)
	}
}

func TestNativeInteractiveRendersWidePreviewBesideList(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI: "switch",
		Preview: Preview{
			Command: "printf 'preview:%s' {2}",
			Window:  "right,60%,border-left",
		},
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, nativeLayout{Rows: 24, Cols: 120})

	rendered := out.String()
	if !strings.Contains(rendered, " | preview") || !strings.Contains(rendered, "preview:/repo/api") {
		t.Fatalf("native output = %q, want side-by-side preview", rendered)
	}
}

func TestNativeInteractiveRendersFZFLikeMultilineSelection(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:        "switch",
		MultiLine: true,
		Items: []Item{{
			Label: "api\n  branch main",
			Value: "/repo/api",
		}},
	}, []Item{{
		Label: "api\n  branch main",
		Value: "/repo/api",
	}}, "", 0, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if !strings.Contains(rendered, "▌") || !strings.Contains(rendered, "48;2;38;50;56") {
		t.Fatalf("native output = %q, want fzf-like pointer and current-row color", rendered)
	}
	if strings.Contains(rendered, "> api") {
		t.Fatalf("native output = %q, want multiline selection to avoid legacy > pointer", rendered)
	}
}

func TestNativeSelectedContentKeepsCurrentStyleAfterReset(t *testing.T) {
	t.Parallel()

	rendered := nativeSelectedContent("\x1b[1mapi\x1b[0m branch")
	if !strings.Contains(rendered, nativeReset+nativeCurrentStart+" branch") {
		t.Fatalf("nativeSelectedContent() = %q, want current style restored after reset", rendered)
	}
}

func TestNativeInteractiveRendersDownPreviewBelowList(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI: "sidebar",
		Preview: Preview{
			Command: "printf 'preview:%s' {2}",
			Window:  "down,25%,border-top",
		},
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if !strings.Contains(rendered, "\npreview\npreview:/repo/api") {
		t.Fatalf("native output = %q, want bottom preview", rendered)
	}
}

func TestNativeInteractiveUsesInitialIndex(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\r"), io.Discard, Options{
		UI:           "switch",
		InitialIndex: 1,
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "/repo/web" {
		t.Fatalf("result = %#v, want initial index to select web", result)
	}
}

func TestNativeInteractiveRunsCustomActionCommandAndRefreshes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	result, err := runNativeInteractive(strings.NewReader("\x1b[C\r"), io.Discard, Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: marker}},
		Actions: []Action{{
			Key:     "right",
			Intent:  ActionCustom,
			Command: "printf cycled > {2}",
		}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != marker {
		t.Fatalf("result = %#v, want selected marker", result)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "cycled" {
		t.Fatalf("marker = %q, want cycled", data)
	}
}

func TestNativeInteractiveRunsFocusActionOnSelectionChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "focus.log")
	result, err := runNativeInteractive(strings.NewReader("\x1b[B\r"), io.Discard, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "api"},
			{Title: "web", Value: "web"},
		},
		Actions: []Action{{
			Key:     "focus",
			Intent:  ActionCustom,
			Command: "printf '%s\n' {2} >> " + shellQuoteNative(logPath),
		}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "web" {
		t.Fatalf("result = %#v, want web", result)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read focus log: %v", err)
	}
	if got, want := string(data), "api\nweb\n"; got != want {
		t.Fatalf("focus log = %q, want %q", got, want)
	}
}
