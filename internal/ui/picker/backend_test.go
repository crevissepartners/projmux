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
		{Title: "bravo archived project index", Value: "1"},
		{Title: "api", Value: "2"},
		{Title: "api service", Value: "3"},
	}

	filtered := FilterItems(items, "api")
	if got, want := valuesOf(filtered), []string{"2", "3", "1"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(api) values = %#v, want %#v", got, want)
	}
}

func TestFilterItemsPreservesSearchKeyOrder(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "slow", Value: "1", SearchText: "bravo archived project index"},
		{Title: "exact", Value: "2", SearchText: "api"},
		{Title: "prefix", Value: "3", SearchText: "api service"},
	}

	filtered := FilterItems(items, "api")
	if got, want := valuesOf(filtered), []string{"1", "2", "3"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(api) values = %#v, want fzf reload-preserved order %#v", got, want)
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

func TestNativeTTYFallbackIsEnabledForAppTTYContexts(t *testing.T) {
	t.Parallel()

	tmp := filepath.Join(t.TempDir(), "stdin")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	defer f.Close()

	if !shouldOpenNativeTTYFallback(f, func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}) {
		t.Fatal("tmux context should force native picker through controlling TTY")
	}
	if !shouldOpenNativeTTYFallback(f, func(name string) string {
		if name == "PROJMUX_NATIVE_TTY_FALLBACK" {
			return "1"
		}
		return ""
	}) {
		t.Fatal("explicit native TTY fallback env should force controlling TTY")
	}
	if shouldOpenNativeTTYFallback(f, func(string) string { return "" }) {
		t.Fatal("non-stdin file without tmux/env should not force controlling TTY")
	}
}

func TestNativeRunnerFailsFastWhenFileInputIsNotTTY(t *testing.T) {
	t.Parallel()

	tmp := filepath.Join(t.TempDir(), "stdin")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	defer f.Close()

	_, err = (NativeRunner{In: f, Out: io.Discard}).Run(Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	})
	if err == nil || !strings.Contains(err.Error(), "native picker requires a TTY") {
		t.Fatalf("Run() error = %v, want non-TTY failure", err)
	}
}

func TestNativeLineModeCanBeExplicitlyEnabledForFileInput(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(tmp, []byte("1\n"), 0o644); err != nil {
		t.Fatalf("write temp stdin: %v", err)
	}
	f, err := os.Open(tmp)
	if err != nil {
		t.Fatalf("open temp stdin: %v", err)
	}
	defer f.Close()

	t.Setenv("PROJMUX_NATIVE_LINE_MODE", "1")
	result, err := (NativeRunner{In: f, Out: io.Discard}).Run(Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Value != "/repo/api" {
		t.Fatalf("Run() = %#v, want selected api", result)
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

func TestNativeInteractiveUsesAlternateScreen(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	_, err := runNativeInteractive(strings.NewReader("\r"), &out, Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	rendered := out.String()
	if !strings.HasPrefix(rendered, nativeScreenEnter) {
		t.Fatalf("native output = %q, want alternate-screen enter prefix", rendered)
	}
	if !strings.HasSuffix(rendered, nativeScreenLeave) {
		t.Fatalf("native output = %q, want alternate-screen leave suffix", rendered)
	}
}

func TestNativeInteractiveRendersBorderFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 8, Cols: 40})

	rendered := out.String()
	if !strings.Contains(rendered, "┌") || !strings.Contains(rendered, "└") || !strings.Contains(rendered, "│") {
		t.Fatalf("native output = %q, want fzf-like border frame", rendered)
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

func TestNativeInteractiveConsumesShiftCSIKeys(t *testing.T) {
	t.Parallel()

	key, err := readNativeKey(strings.NewReader("\x1b[1;2B"))
	if err != nil {
		t.Fatalf("readNativeKey() error = %v", err)
	}
	if key.Name != "shift-down" || key.Text != "" {
		t.Fatalf("key = %#v, want shift-down", key)
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

func TestNativeInteractiveEditsTypedQueryAtCursor(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("abcd\x1b[D\x1b[DX\x1b[3~\r"), io.Discard, Options{
		UI:          "settings-workdir-typed",
		AcceptQuery: true,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Query != "abXd" {
		t.Fatalf("result = %#v, want cursor-edited query", result)
	}
}

func TestNativeInteractiveSupportsQueryLineEditingKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("bc\x01a\x05d\r"), io.Discard, Options{
		UI:          "settings-workdir-typed",
		AcceptQuery: true,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Query != "abcd" {
		t.Fatalf("result = %#v, want ctrl-a/ctrl-e edited query", result)
	}
}

func TestNativeInteractiveCtrlUDeletesBeforeCursor(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("ab cd\x1b[D\x1b[D\x15\r"), io.Discard, Options{
		UI:          "settings-workdir-typed",
		AcceptQuery: true,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Query != "cd" {
		t.Fatalf("result = %#v, want ctrl-u to preserve suffix after cursor", result)
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

func TestNativeInteractiveSupportsPrintableExpectKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("a"), io.Discard, Options{
		UI:      "notify",
		Items:   []Item{{Title: "deploy", Value: "notification-id"}},
		Actions: CustomActions("a"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "a" || result.Value != "notification-id" || result.Query != "" {
		t.Fatalf("result = %#v, want printable expect key action", result)
	}
}

func TestNativeInteractiveSupportsControlExpectKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x01"), io.Discard, Options{
		UI:      "notify",
		Items:   []Item{{Title: "deploy", Value: "notification-id"}},
		Actions: CustomActions("ctrl-a"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "ctrl-a" || result.Value != "notification-id" {
		t.Fatalf("result = %#v, want ctrl-a expect key action", result)
	}
}

func TestNativeInteractiveSupportsControlAltCloseKeys(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b\x13"), io.Discard, Options{
		UI:      "settings",
		Items:   []Item{{Title: "AI", Value: "ai"}},
		Actions: CloseActions("ctrl-alt-s"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "ctrl-alt-s" {
		t.Fatalf("result = %#v, want ctrl-alt-s close action", result)
	}
}

func TestNativeInteractiveSupportsCSIuAppKeyBindings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "alt-1 custom csi", in: "\x1b[9005u", want: "alt-1"},
		{name: "alt-2 custom csi", in: "\x1b[9003u", want: "alt-2"},
		{name: "alt-5 custom csi", in: "\x1b[9007u", want: "alt-5"},
		{name: "ctrl-n custom csi", in: "\x1b[9008u", want: "ctrl-n"},
		{name: "ctrl-alt-s generic csi", in: "\x1b[115;7u", want: "ctrl-alt-s"},
		{name: "alt-p generic csi", in: "\x1b[112;3u", want: "alt-p"},
		{name: "ctrl-a generic csi", in: "\x1b[97;5u", want: "ctrl-a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key, err := readNativeKey(strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("readNativeKey() error = %v", err)
			}
			if key.Name != tc.want || key.Text != "" {
				t.Fatalf("key = %#v, want %q", key, tc.want)
			}
		})
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
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 24, Cols: 120})

	rendered := out.String()
	if !strings.Contains(rendered, "│preview:/repo/api") {
		t.Fatalf("native output = %q, want side-by-side preview", rendered)
	}
	if strings.Contains(rendered, " │ preview\n") {
		t.Fatalf("native output = %q, want fzf-like preview without synthetic title row", rendered)
	}
	if strings.Contains(rendered, " │ ") {
		t.Fatalf("native output = %q, want fzf-like single-column preview border", rendered)
	}
}

func TestNativeInteractiveRendersPreviewOffset(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI: "switch",
		Preview: Preview{
			Command: "printf 'one\ntwo\nthree'",
			Window:  "down,25%,border-top",
		},
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 1, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if strings.Contains(rendered, "\none") || !strings.Contains(rendered, "two") || !strings.Contains(rendered, "three") || !strings.Contains(rendered, "─") {
		t.Fatalf("native output = %q, want preview scrolled by one line", rendered)
	}
}

func TestNativeInteractiveUsesScrollbarForLongLists(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 24)
	for i := 0; i < 24; i++ {
		items = append(items, Item{Title: "item " + strconv.Itoa(i), Value: strconv.Itoa(i)})
	}

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Items: items,
	}, items, "", 12, 0, nativeLayout{Rows: 10, Cols: 50})

	rendered := out.String()
	if !strings.Contains(rendered, nativeScrollbar) {
		t.Fatalf("native output = %q, want fzf-like scrollbar", rendered)
	}
	if strings.Contains(rendered, "more below") || strings.Contains(rendered, "more above") {
		t.Fatalf("native output = %q, want scrollbar instead of textual overflow rows", rendered)
	}
}

func TestNativeInteractiveUsesAvailableHeightForSimpleLists(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, Item{Title: "item " + strconv.Itoa(i), Value: strconv.Itoa(i)})
	}

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "settings",
		Items: items,
	}, items, "", 0, 0, nativeLayout{Rows: 24, Cols: 60})

	rendered := out.String()
	if !strings.Contains(rendered, "item 19") {
		t.Fatalf("native output = %q, want simple list to use full fzf-height surface", rendered)
	}
	if strings.Contains(rendered, nativeScrollbar) {
		t.Fatalf("native output = %q, want no scrollbar when all rows fit", rendered)
	}
}

func TestNativeListLimitAccountsForHeaderFooterAndDownPreview(t *testing.T) {
	t.Parallel()

	options := Options{
		Header: "header",
		Footer: "line 1\nline 2\nline 3",
	}
	if got, want := nativeListLimit(options, nativeLayout{Rows: 20, Cols: 80}, "down", 5, true), 8; got != want {
		t.Fatalf("nativeListLimit() = %d, want %d", got, want)
	}
}

func TestNativeVisibleRangeCountsMultilineRenderedRows(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, Item{
			Label: "item " + strconv.Itoa(i) + "\n  detail",
			Value: strconv.Itoa(i),
		})
	}

	start, end := nativeVisibleRangeByRenderedRows(items, 5, 8)
	if start > 5 || end <= 5 {
		t.Fatalf("range = %d:%d, want selected item included", start, end)
	}
	if got := nativeRenderedListLineCount(items, start, end, true); got > 8 {
		t.Fatalf("rendered line count = %d for range %d:%d, want <= 8", got, start, end)
	}
	if got := end - start; got >= 8 {
		t.Fatalf("range item count = %d, want multiline rows to reduce visible items below row budget", got)
	}
}

func TestNativePreviewWidthUsesPreviewWindowPercent(t *testing.T) {
	t.Parallel()

	if got, want := nativePreviewWidth(120, "right,60%,border-left"), 72; got != want {
		t.Fatalf("nativePreviewWidth() = %d, want %d", got, want)
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
	}}, "", 0, 0, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if !strings.Contains(rendered, "▌") || !strings.Contains(rendered, "48;2;38;50;56") {
		t.Fatalf("native output = %q, want fzf-like pointer and current-row color", rendered)
	}
	if strings.Contains(rendered, "> api") {
		t.Fatalf("native output = %q, want multiline selection to avoid legacy > pointer", rendered)
	}
}

func TestNativeInteractiveRendersMultilineGapLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:        "switch",
		MultiLine: true,
		Items: []Item{
			{Label: "api\n  branch main", Value: "/repo/api"},
			{Label: "web\n  branch main", Value: "/repo/web"},
		},
	}, []Item{
		{Label: "api\n  branch main", Value: "/repo/api"},
		{Label: "web\n  branch main", Value: "/repo/web"},
	}, "", 0, 0, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if !strings.Contains(rendered, "  "+strings.Repeat(nativeGapLine, 8)) {
		t.Fatalf("native output = %q, want fzf-like multiline gap line", rendered)
	}
	if strings.Contains(rendered, nativeGapSentinel) {
		t.Fatalf("native output leaked gap sentinel: %q", rendered)
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
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 24, Cols: 80})

	rendered := out.String()
	if !strings.Contains(rendered, "preview:/repo/api") {
		t.Fatalf("native output = %q, want bottom preview", rendered)
	}
	if strings.Contains(rendered, "\npreview\n") {
		t.Fatalf("native output = %q, want fzf-like bottom preview without synthetic title row", rendered)
	}
	if strings.Contains(rendered, "\n\n─") {
		t.Fatalf("native output = %q, want fzf-like bottom preview without blank row before border", rendered)
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
