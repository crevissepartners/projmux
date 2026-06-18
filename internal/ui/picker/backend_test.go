package picker

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

func TestResolveBackendDefaultsToNative(t *testing.T) {
	t.Parallel()

	if got := ResolveBackend(nil); got != BackendNative {
		t.Fatalf("ResolveBackend(nil) = %q, want %q", got, BackendNative)
	}
	if got := ResolveBackend(func(string) string { return "unknown" }); got != BackendNative {
		t.Fatalf("ResolveBackend(unknown) = %q, want %q", got, BackendNative)
	}
}

func TestResolveBackendIgnoresDeprecatedFZFOverride(t *testing.T) {
	t.Parallel()

	got := ResolveBackend(func(name string) string {
		if name != BackendEnv {
			t.Fatalf("env name = %q, want %q", name, BackendEnv)
		}
		return "fzf"
	})
	if got != BackendNative {
		t.Fatalf("ResolveBackend(fzf) = %q, want %q", got, BackendNative)
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

func TestFilterItemsKeepsInputOrderForSearchKeyRows(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "codex", Value: "codex", SearchText: "codex openai cli"},
		{Title: "claude", Value: "claude", SearchText: "claude anthropic cli"},
		{Title: "shell", Value: "shell", SearchText: "shell plain no agent"},
	}

	filtered := FilterItems(items, "cli")
	if got, want := valuesOf(filtered), []string{"codex", "claude"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(cli) values = %#v, want stable source order %#v", got, want)
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

func TestFilterItemsPrefersFZFBoundaryAndCamelCaseMatches(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "foob-r", Value: "late-boundary"},
		{Title: "fo-bar", Value: "boundary"},
		{Title: "FooBar", Value: "camel"},
		{Title: "foobazbar", Value: "plain"},
	}

	filtered := FilterItems(items, "fb")
	if got, want := valuesOf(filtered)[:3], []string{"boundary", "camel", "late-boundary"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(fb) leading values = %#v, want fzf-like boundary/camel ranking %#v", got, want)
	}
}

func TestFuzzyScoreMatchesFZFV2ReferenceScores(t *testing.T) {
	t.Parallel()

	// Reference cases mirror fzf src/algo/algo_test.go for projmux's fuzzy picker surface.
	tests := []struct {
		name          string
		source        string
		query         string
		caseSensitive bool
		want          int
	}{
		{
			name:   "camel with gaps",
			source: "fooBarbaz1",
			query:  "oBZ",
			want:   nativeScoreMatch*3 + nativeBonusCamel123 + nativeScoreGapStart + nativeScoreGapExtension*3,
		},
		{
			name:   "word boundaries",
			source: "foo bar baz",
			query:  "fbb",
			want: nativeScoreMatch*3 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryWhite*2 + nativeScoreGapStart*2 + nativeScoreGapExtension*4,
		},
		{
			name:   "delimiter boundaries",
			source: "/man1/zshcompctl.1",
			query:  "zshc",
			want: nativeScoreMatch*4 + nativeBonusBoundaryDelimiter*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryDelimiter*3,
		},
		{
			name:   "camel and consecutive acronym",
			source: "/AutomatorDocument.icns",
			query:  "rdoc",
			want:   nativeScoreMatch*4 + nativeBonusCamel123 + nativeBonusConsecutive*2,
		},
		{
			name:   "compact dot path",
			source: "/.oh-my-zsh/cache",
			query:  "zshc",
			want: nativeScoreMatch*4 + nativeBonusBoundary*nativeBonusFirstCharMultiplier +
				nativeBonusBoundary*2 + nativeScoreGapStart + nativeBonusBoundaryDelimiter,
		},
		{
			name:   "number run after digits",
			source: "ab0123 456",
			query:  "12356",
			want:   nativeScoreMatch*5 + nativeBonusConsecutive*3 + nativeScoreGapStart + nativeScoreGapExtension,
		},
		{
			name:   "number run after letters",
			source: "abc123 456",
			query:  "12356",
			want: nativeScoreMatch*5 + nativeBonusCamel123*nativeBonusFirstCharMultiplier +
				nativeBonusCamel123*2 + nativeBonusConsecutive + nativeScoreGapStart + nativeScoreGapExtension,
		},
		{
			name:   "slash path acronym",
			source: "foo/bar/baz",
			query:  "fbb",
			want: nativeScoreMatch*3 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryDelimiter*2 + nativeScoreGapStart*2 + nativeScoreGapExtension*4,
		},
		{
			name:   "camel acronym",
			source: "fooBarBaz",
			query:  "fbb",
			want: nativeScoreMatch*3 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusCamel123*2 + nativeScoreGapStart*2 + nativeScoreGapExtension*2,
		},
		{
			name:   "partial boundary acronym",
			source: "foo barbaz",
			query:  "fbb",
			want: nativeScoreMatch*3 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryWhite + nativeScoreGapStart*2 + nativeScoreGapExtension*3,
		},
		{
			name:   "compact prefix beats later boundary",
			source: "fooBar Baz",
			query:  "foob",
			want: nativeScoreMatch*4 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryWhite*3,
		},
		{
			name:          "case sensitive prefix plus camel",
			source:        "FooBar Baz",
			query:         "FooB",
			caseSensitive: true,
			want: nativeScoreMatch*4 + nativeBonusBoundaryWhite*nativeBonusFirstCharMultiplier +
				nativeBonusBoundaryWhite*2 + maxInt(nativeBonusCamel123, nativeBonusBoundaryWhite),
		},
		{
			name:          "consecutive bonus updated",
			source:        "foo-bar",
			query:         "o-ba",
			caseSensitive: true,
			want:          nativeScoreMatch*4 + nativeBonusBoundary*3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := fuzzyScore(tt.source, nativeSearchPattern(tt.query, tt.caseSensitive), tt.caseSensitive)
			if !ok {
				t.Fatalf("fuzzyScore(%q, %q) did not match", tt.source, tt.query)
			}
			if got != tt.want {
				t.Fatalf("fuzzyScore(%q, %q) = %d, want fzf V2 reference score %d", tt.source, tt.query, got, tt.want)
			}
		})
	}
}

func TestFuzzyScoreRejectsFZFV2ReferenceNonMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		source        string
		query         string
		caseSensitive bool
	}{
		{name: "missing case-sensitive uppercase", source: "fooBarbaz", query: "oBZ", caseSensitive: true},
		{name: "missing case-sensitive lowercase", source: "Foo Bar Baz", query: "fbb", caseSensitive: true},
		{name: "query longer than source", source: "fooBarbaz", query: "fooBarbazz", caseSensitive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := fuzzyScore(tt.source, nativeSearchPattern(tt.query, tt.caseSensitive), tt.caseSensitive)
			if ok || got != 0 {
				t.Fatalf("fuzzyScore(%q, %q) = (%d, %t), want no match", tt.source, tt.query, got, ok)
			}
		})
	}
}

func TestFilterItemsIgnoresANSIEscapeSequences(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "\x1b[36mCodex\x1b[0m split", Value: "codex"},
		{Title: "\x1b[32mShell\x1b[0m split", Value: "shell"},
	}

	filtered := FilterItems(items, "codex")
	if got, want := valuesOf(filtered), []string{"codex"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(codex) values = %#v, want %#v", got, want)
	}
}

func TestFilterItemsSearchesHiddenValueWhenNoSearchKey(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "Update Now", Value: "apply"},
		{Title: "Later", Value: "later"},
	}

	filtered := FilterItems(items, "apply")
	if got, want := valuesOf(filtered), []string{"apply"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(apply) values = %#v, want fzf hidden-value match %#v", got, want)
	}
}

func TestFilterItemsUsesFZFSmartCase(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "codex", Value: "lower"},
		{Title: "Codex", Value: "title"},
		{Title: "CODEX", Value: "upper"},
	}

	lower := FilterItems(items, "codex")
	if got, want := valuesOf(lower), []string{"lower", "title", "upper"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(codex) values = %#v, want fzf smart-case insensitive %#v", got, want)
	}

	title := FilterItems(items, "Codex")
	if got, want := valuesOf(title), []string{"title"}; !equalStringSlices(got, want) {
		t.Fatalf("FilterItems(Codex) values = %#v, want fzf smart-case sensitive %#v", got, want)
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

func TestNativeInteractiveClosesOnMatchingLaunchCloseKey(t *testing.T) {
	t.Setenv(NativeLaunchKeyEnv, "alt-1")

	result, err := runNativeInteractive(strings.NewReader("\x1b1"), io.Discard, Options{
		UI:      "switch",
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CloseActions("alt-1"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "alt-1" {
		t.Fatalf("result = %#v, want matching launch key to close immediately", result)
	}
}

func TestNativeInteractiveDoesNotIgnoreLaunchCloseKeyAfterFirstInput(t *testing.T) {
	t.Setenv(NativeLaunchKeyEnv, "alt-1")

	result, err := runNativeInteractive(strings.NewReader("\x1b[B\x1b1"), io.Discard, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
		Actions: CloseActions("alt-1"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "alt-1" {
		t.Fatalf("result = %#v, want later launch key to close instead of being ignored", result)
	}
}

func TestNativeInteractiveClosesOnMatchingCSIuLaunchCloseKey(t *testing.T) {
	t.Setenv(NativeLaunchKeyEnv, "alt-1")

	result, err := runNativeInteractive(strings.NewReader("\x1b[9005u"), io.Discard, Options{
		UI:      "switch",
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CloseActions("alt-1"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "alt-1" {
		t.Fatalf("result = %#v, want matching CSI-u launch key to close immediately", result)
	}
}

func TestNativeInteractiveClosesOnSplitLaunchEscape(t *testing.T) {
	t.Setenv(NativeLaunchKeyEnv, "alt-1")

	result, err := runNativeInteractive(&delayedByteReader{
		data:            []byte("\x1b1"),
		zerosBeforeByte: nativeMaybeReadAttempts + 1,
	}, io.Discard, Options{
		UI:      "switch",
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CloseActions("esc", "alt-1"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "esc" {
		t.Fatalf("result = %#v, want split escape launch key to close immediately", result)
	}
}

func TestNativeInteractiveDoesNotIgnoreDifferentLaunchCloseKey(t *testing.T) {
	t.Setenv(NativeLaunchKeyEnv, "alt-1")

	result, err := runNativeInteractive(strings.NewReader("\x1b2"), io.Discard, Options{
		UI:      "notify",
		Items:   []Item{{Title: "deploy", Value: "notification-id"}},
		Actions: CloseActions("alt-2"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed || result.Key != "alt-2" {
		t.Fatalf("result = %#v, want non-launch close key to close", result)
	}
}

func TestNativeInteractiveClearsScreenOnlyOnEnter(t *testing.T) {
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
	if result.Value != "/repo/web" {
		t.Fatalf("result = %#v, want selected web item", result)
	}
	if got := strings.Count(out.String(), "\x1b[2J"); got != 1 {
		t.Fatalf("native interactive full-screen clear count = %d, want one initial clear: %q", got, out.String())
	}
}

func TestNativeInteractiveWrapsRedrawsInSynchronizedUpdates(t *testing.T) {
	var out bytes.Buffer

	if _, err := runNativeInteractive(strings.NewReader("\x1b[B\r"), &out, Options{
		UI: "switch",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
			{Title: "web", Value: "/repo/web"},
		},
	}); err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	rendered := out.String()
	if got, want := strings.Count(rendered, nativeSyncUpdateEnter), 2; got != want {
		t.Fatalf("native synchronized update enter count = %d, want %d: %q", got, want, rendered)
	}
	if got, want := strings.Count(rendered, nativeSyncUpdateLeave), 2; got != want {
		t.Fatalf("native synchronized update leave count = %d, want %d: %q", got, want, rendered)
	}
	if strings.Index(rendered, nativeSyncUpdateEnter) > strings.Index(rendered, "╭") {
		t.Fatalf("native synchronized update starts after frame render: %q", rendered)
	}
	if got := strings.Count(rendered, "╭"); got != 1 {
		t.Fatalf("native redraw top-border count = %d, want one full frame then diff updates: %q", got, rendered)
	}
	if !strings.Contains(rendered, "\x1b[4;1H") {
		t.Fatalf("native redraw output = %q, want cursor-addressed row diff", rendered)
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
		if name == NativeTTYFallbackEnv {
			return "1"
		}
		return ""
	}) {
		t.Fatal("explicit native TTY fallback env should force controlling TTY")
	}
	if shouldOpenNativeTTYFallback(f, func(name string) string {
		switch name {
		case NativeTTYFallbackEnv:
			return "0"
		case "TMUX":
			return "/tmp/tmux-1000/default,1,0"
		default:
			return ""
		}
	}) {
		t.Fatal("explicit native TTY fallback disable should override tmux context")
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

func TestNativeInteractiveDeferredUpdatePreservesMutatedFilterAndSelection(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	var out lockedBuffer
	deferredStarted := make(chan struct{})
	releaseDeferred := make(chan struct{})
	resultCh := make(chan struct {
		result Result
		err    error
	}, 1)

	go func() {
		result, err := runNativeInteractive(reader, &out, Options{
			UI:        "switch",
			MultiLine: true,
			Items: []Item{
				{Title: "api", Value: "/repo/api", SearchText: "svc api"},
				{Title: "web", Value: "/repo/web", SearchText: "svc web"},
				{Title: "worker", Value: "/repo/worker", SearchText: "svc worker"},
			},
			DeferredUpdate: func() (DeferredUpdate, error) {
				close(deferredStarted)
				<-releaseDeferred
				return DeferredUpdate{Items: []Item{
					{Title: "api", Value: "/repo/api", SearchText: "svc api", MetaLines: []string{"branch main"}},
					{Title: "web", Value: "/repo/web", SearchText: "svc web", MetaLines: []string{"branch feature"}},
					{Title: "worker", Value: "/repo/worker", SearchText: "svc worker", MetaLines: []string{"branch jobs"}},
				}}, nil
			},
		})
		resultCh <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-deferredStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred update did not start after first render")
	}
	if _, err := writer.Write([]byte("svc\x1b[6~")); err != nil {
		t.Fatalf("write query/page-down input: %v", err)
	}
	close(releaseDeferred)
	waitForNativeOutput(t, &out, "branch main", "branch feature", "branch jobs")
	if _, err := writer.Write([]byte("\r")); err != nil {
		t.Fatalf("write enter input: %v", err)
	}

	var got struct {
		result Result
		err    error
	}
	select {
	case got = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runNativeInteractive did not return after enter")
	}
	if got.err != nil {
		t.Fatalf("runNativeInteractive() error = %v", got.err)
	}
	if got.result.Key != "enter" || got.result.Value != "/repo/worker" || got.result.Query != "svc" {
		t.Fatalf("result = %#v, want filtered selection to stay on worker", got.result)
	}
}

func TestNativeInteractiveDeferredUpdateTriggerRefreshesRepeatedly(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	var out lockedBuffer
	trigger := make(chan struct{}, 1)
	var started sync.Once
	deferredStarted := make(chan struct{})
	calls := 0
	resultCh := make(chan struct {
		result Result
		err    error
	}, 1)

	go func() {
		result, err := runNativeInteractive(reader, &out, Options{
			UI:            "notify-sidebar",
			DisableSearch: true,
			Items:         []Item{{Title: "alpha", Value: "alpha"}},
			DeferredUpdate: func() (DeferredUpdate, error) {
				started.Do(func() { close(deferredStarted) })
				calls++
				if calls == 1 {
					return DeferredUpdate{Items: []Item{{Title: "beta", Value: "beta"}}}, nil
				}
				return DeferredUpdate{Items: []Item{{Title: "gamma", Value: "gamma"}}}, nil
			},
			DeferredUpdateTrigger: trigger,
		})
		resultCh <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()

	waitForNativeOutput(t, &out, "alpha")
	select {
	case <-deferredStarted:
		t.Fatal("deferred update started before trigger")
	case <-time.After(20 * time.Millisecond):
	}
	trigger <- struct{}{}
	select {
	case <-deferredStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred update did not start after trigger")
	}
	waitForNativeOutput(t, &out, "beta")
	trigger <- struct{}{}
	waitForNativeOutput(t, &out, "gamma")
	if _, err := writer.Write([]byte("\r")); err != nil {
		t.Fatalf("write enter input: %v", err)
	}

	var got struct {
		result Result
		err    error
	}
	select {
	case got = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runNativeInteractive did not return after enter")
	}
	if got.err != nil {
		t.Fatalf("runNativeInteractive() error = %v", got.err)
	}
	if got.result.Value != "gamma" {
		t.Fatalf("result = %#v, want latest refreshed row", got.result)
	}
	if calls != 2 {
		t.Fatalf("deferred calls = %d, want two event refreshes", calls)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForNativeOutput(t *testing.T, out *lockedBuffer, wants ...string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		rendered := out.String()
		all := true
		for _, want := range wants {
			if !strings.Contains(rendered, want) {
				all = false
				break
			}
		}
		if all {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("native output = %q, want metadata %q", rendered, wants)
		case <-tick.C:
		}
	}
}

func TestNativeInteractiveUsesOptionsThemeForFrame(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{
		Background: "#010203",
		Foreground: "#aabbcc",
	})
	var out bytes.Buffer
	_, err := runNativeInteractive(strings.NewReader("\r"), &out, Options{
		UI:    "switch",
		Title: "Projects",
		Items: []Item{
			{Title: "api", Value: "/repo/api"},
		},
		Theme: &effective,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	rendered := out.String()
	for _, want := range []string{"\x1b[48;2;1;2;3m", "\x1b[38;2;170;187;204m"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native interactive output = %q, want themed frame SGR %q", rendered, want)
		}
	}
}

func TestNativeInteractiveSupportsFZFNavigationKeys(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "api", Value: "/repo/api"},
		{Title: "web", Value: "/repo/web"},
		{Title: "tools", Value: "/repo/tools"},
	}
	result, err := runNativeInteractive(strings.NewReader("\x1b[106;5u\x0e\x10\x0b\r"), io.Discard, Options{
		UI:    "switch",
		Items: items,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "enter" || result.Value != "/repo/api" {
		t.Fatalf("result = %#v, want Ctrl-J/Ctrl-N/Ctrl-P/Ctrl-K to navigate like fzf", result)
	}
}

func TestNativeInteractiveWrapsPreviousNavigationKeys(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "api", Value: "/repo/api"},
		{Title: "web", Value: "/repo/web"},
		{Title: "tools", Value: "/repo/tools"},
	}
	tests := []struct {
		name  string
		input string
	}{
		{name: "up", input: "\x1b[A\r"},
		{name: "ctrl-p", input: "\x10\r"},
		{name: "ctrl-k", input: "\x0b\r"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := runNativeInteractive(strings.NewReader(tt.input), io.Discard, Options{
				UI:    "switch",
				Items: items,
			})
			if err != nil {
				t.Fatalf("runNativeInteractive() error = %v", err)
			}
			if result.Key != "enter" || result.Value != "/repo/tools" {
				t.Fatalf("result = %#v, want previous navigation to wrap to last item", result)
			}
		})
	}
}

func TestNativeInteractiveWrapsNextNavigationKeys(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "api", Value: "/repo/api"},
		{Title: "web", Value: "/repo/web"},
		{Title: "tools", Value: "/repo/tools"},
	}
	tests := []struct {
		name  string
		input string
	}{
		{name: "down", input: "\x1b[B\r"},
		{name: "ctrl-n", input: "\x0e\r"},
		{name: "ctrl-j", input: "\x1b[106;5u\r"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := runNativeInteractive(strings.NewReader(tt.input), io.Discard, Options{
				UI:           "switch",
				InitialIndex: 2,
				Items:        items,
			})
			if err != nil {
				t.Fatalf("runNativeInteractive() error = %v", err)
			}
			if result.Key != "enter" || result.Value != "/repo/api" {
				t.Fatalf("result = %#v, want next navigation to wrap to first item", result)
			}
		})
	}
}

func TestNativeInteractiveNavigationKeysAreNoopForEmptyList(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[A\x10\x0b\x1b[B\x0e\x1b[106;5u"), io.Discard, Options{
		UI: "switch",
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed {
		t.Fatalf("result = %#v, want EOF close after empty-list navigation no-op", result)
	}
}

func TestNativeInteractiveJumpNavigationRemainsClamped(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Title: "api", Value: "/repo/api"},
		{Title: "web", Value: "/repo/web"},
		{Title: "tools", Value: "/repo/tools"},
	}
	tests := []struct {
		name         string
		input        string
		initialIndex int
		wantValue    string
	}{
		{name: "page-up at first", input: "\x1b[5~\r", wantValue: "/repo/api"},
		{name: "page-down at last", input: "\x1b[6~\r", initialIndex: 2, wantValue: "/repo/tools"},
		{name: "home jumps to first", input: "\x1b[H\r", initialIndex: 2, wantValue: "/repo/api"},
		{name: "end jumps to last", input: "\x1b[F\r", wantValue: "/repo/tools"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := runNativeInteractive(strings.NewReader(tt.input), io.Discard, Options{
				UI:           "switch",
				InitialIndex: tt.initialIndex,
				Items:        items,
			})
			if err != nil {
				t.Fatalf("runNativeInteractive() error = %v", err)
			}
			if result.Key != "enter" || result.Value != tt.wantValue {
				t.Fatalf("result = %#v, want %s", result, tt.wantValue)
			}
		})
	}
}

func TestNativeInteractiveTreatsCarriageReturnAsEnter(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"\r", "\n"} {
		key, err := readNativeKey(strings.NewReader(input))
		if err != nil {
			t.Fatalf("readNativeKey() error = %v", err)
		}
		if key.Name != "enter" || key.Text != "" {
			t.Fatalf("key = %#v for input %q, want Enter", key, input)
		}
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
	if !strings.Contains(rendered, "╯\r"+nativeSyncUpdateLeave+"\r\x1b[0m\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[H\x1b[J\x1b[?25h\x1b[?1049l") {
		t.Fatalf("native output = %q, want reset and alternate-screen clear before leave", rendered)
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
	if !strings.Contains(rendered, "╭") || !strings.Contains(rendered, "╰") || !strings.Contains(rendered, "│") {
		t.Fatalf("native output = %q, want fzf-like rounded border frame", rendered)
	}
}

func TestNativeInteractiveRendersOptionalTitlebar(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Title: "Projects",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 8, Cols: 40})

	lines := strings.Split(out.String(), "\r\n")
	if strings.Contains(lines[0], "Projects") {
		t.Fatalf("native top frame row = %q, want title outside border row", lines[0])
	}
	if !strings.Contains(lines[1], " Projects ") {
		t.Fatalf("native titlebar row = %q, want optional titlebar", lines[1])
	}
	if strings.Contains(lines[1], projmuxpicker.TitlebarStart) || strings.Contains(lines[1], projmuxpicker.TitlebarRule) {
		t.Fatalf("native titlebar row = %q, want frame styling without titlebar overlay ANSI", lines[1])
	}
	if strings.Contains(lines[1], projmuxpicker.TitlebarRule+"─") || strings.Contains(lines[1], strings.Repeat("─", 2)) {
		t.Fatalf("native titlebar row = %q, want no rule fill after title", lines[1])
	}
	if !strings.HasPrefix(lines[2], "├") || !strings.HasSuffix(lines[2], "┤") {
		t.Fatalf("native titlebar divider row = %q, want divider between title and search", lines[2])
	}
	if !strings.Contains(lines[3], "Search") {
		t.Fatalf("native search row = %q, want search below title divider", lines[3])
	}
}

func TestNativeInteractiveSeparatesSearchHeaderFromList(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 8, Cols: 40})

	lines := strings.Split(out.String(), "\r\n")
	if len(lines) < 5 {
		t.Fatalf("native output = %q, want prompt, separator, and list rows", out.String())
	}
	if !strings.Contains(lines[1], "switch") {
		t.Fatalf("prompt line = %q, want search prompt before separator", lines[1])
	}
	if !strings.Contains(lines[1], "Search") {
		t.Fatalf("prompt line = %q, want explicit search header label", lines[1])
	}
	if !strings.Contains(lines[2], strings.Repeat(nativeGapLine, 8)) {
		t.Fatalf("separator line = %q, want search/list divider", lines[2])
	}
	if !strings.Contains(lines[3], "api") {
		t.Fatalf("first list line = %q, want item after search divider", lines[3])
	}
}

func TestNativeInteractiveKoreanSearchEmptyAndFooterFitWidth(t *testing.T) {
	t.Setenv("LANG", "ko_KR.UTF-8")

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:     "settings",
		Title:  "설정",
		Footer: "Enter: 열기  |  Esc: 닫기",
	}, nil, "", 0, 0, nativeLayout{Rows: 10, Cols: 42})

	rendered := out.String()
	for _, want := range []string{"검색", "일치하는 항목 없음", "Enter: 열기", "Esc: 닫기"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native output = %q, want localized %q", rendered, want)
		}
	}
	for line := range strings.SplitSeq(rendered, "\r\n") {
		if got := projmuxpicker.VisibleLen(line); got > 44 {
			t.Fatalf("native localized line width = %d, want <= frame width 44: %q", got, line)
		}
	}
}

func TestNativeInteractiveExplicitLocaleOverridesEnvironmentChrome(t *testing.T) {
	t.Setenv("LANG", "ko_KR.UTF-8")

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:     "settings",
		Title:  "Settings",
		Footer: "Enter: open",
		Locale: i18n.FallbackLocale,
	}, nil, "", 0, 0, nativeLayout{Rows: 10, Cols: 42})

	rendered := out.String()
	for _, want := range []string{"Search", "No matches"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native output = %q, want explicit English chrome %q", rendered, want)
		}
	}
	for _, reject := range []string{"검색", "일치하는 항목 없음"} {
		if strings.Contains(rendered, reject) {
			t.Fatalf("native output = %q, rejected env-derived Korean chrome %q", rendered, reject)
		}
	}
}

func TestNativeInteractiveGeneralPickerEmptyLocaleUsesEnvironmentFallback(t *testing.T) {
	t.Setenv("LANG", "ko_KR.UTF-8")

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Title: "Projects",
	}, nil, "", 0, 0, nativeLayout{Rows: 10, Cols: 42})

	rendered := out.String()
	for _, want := range []string{"검색", "일치하는 항목 없음"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("native output = %q, want environment fallback chrome %q", rendered, want)
		}
	}
}

func TestNativeLineModeExplicitLocaleOverridesEnvironmentPrompt(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")

	var out bytes.Buffer
	renderNative(&out, Options{
		UI:     "settings",
		Prompt: "Settings > ",
		Locale: i18n.Locale("ko-KR"),
	}, nil, "")

	if got := out.String(); !strings.Contains(got, "번호, 검색어, 빈 입력으로 닫기: ") {
		t.Fatalf("native line mode output = %q, want Korean prompt from explicit locale", got)
	}
}

func TestNativeInteractiveSettingsAIBadgeStyleLongPreviewClampsFrameRows(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{})
	frameStyle := nativeFrameStyleForTest(t, effective)
	label := "◉  " + theme.ANSIAccentActionStart + "Preview emoji" + theme.ANSIReset +
		"  " + theme.ANSITextDimStart + "⏳ ✅ 🔄" + theme.ANSIReset
	items := []Item{{Label: label, Value: "ai-badge-style:emoji"}}
	frame := nativeInteractiveFrame(Options{
		UI:    "settings-ai-badge-style",
		Title: "모양 - AI 배지 스타일",
		TitleChips: []projmuxpicker.Chip{
			{Label: "전체", Active: true, ClickValue: "__settings_tab_global__"},
			{Label: "프로젝트", Disabled: true, ClickValue: "__settings_tab_project__"},
		},
		Prompt: "설정 > 모양 > AI 배지 스타일 > ",
		Footer: "Enter: apply  |  Back row: parent ",
		Items:  items,
		Theme:  &effective,
	}, items, "", 0, 0, 0, nativeLayout{Rows: 10, Cols: 44})

	lines := strings.Split(frame, "\r\n")
	if len(lines) != 10 {
		t.Fatalf("native frame rows = %d, want 10: %q", len(lines), frame)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, frameStyle) {
			t.Fatalf("frame row %d = %q, want app background style prefix %q", i, line, frameStyle)
		}
		assertNativeFrameResetsResumeStyleOrEnd(t, line, frameStyle)
		if got, want := projmuxpicker.VisibleLen(line), 44; got != want {
			t.Fatalf("frame row %d width = %d, want %d: %q", i, got, want, line)
		}
		plain := stripANSISequences(line)
		if i > 0 && i < len(lines)-1 && strings.HasPrefix(plain, "│") && !strings.HasSuffix(plain, "│") {
			t.Fatalf("frame row %d = %q, want stable vertical borders", i, line)
		}
	}
	if chipRow := lines[1]; !strings.Contains(chipRow, "전체") || !strings.Contains(chipRow, "프로젝트") {
		t.Fatalf("chip row = %q, want Korean Global/Project chips", chipRow)
	}
	for _, want := range []string{"설정 > 모양 > AI 배지 스타일 >", "⏳", "✅", "🔄"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("native frame = %q, want %q", frame, want)
		}
	}

	var previewRow string
	for _, line := range lines {
		if strings.Contains(line, "Preview emoji") {
			previewRow = line
			break
		}
	}
	if previewRow == "" {
		t.Fatalf("native frame = %q, want rendered AI badge preview row", frame)
	}
	if !strings.Contains(previewRow, nativeReset+frameStyle+" │") {
		t.Fatalf("preview row = %q, want reset to resume app background before marker lane and right border", previewRow)
	}
}

func TestNativeInteractiveSettingsAppearanceParentLongPreviewRowsClampFrame(t *testing.T) {
	t.Setenv("LANG", "ko_KR.UTF-8")

	effective := theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{})
	items := []Item{
		{
			Label: nativeSettingsAppearanceRowForTest("AI badge style", "emoji - ⏳ prompt ✅ complete 🔄 working extra tail that clamps"),
			Value: "ai-badge-style:",
		},
		{
			Label: nativeSettingsAppearanceRowForTest("Path icon", "emoji - 📂 ~/source/repos/projmux extra tail that clamps"),
			Value: "statusbar:cwd",
		},
		{
			Label: nativeSettingsAppearanceRowForTest("Git icon", "emoji - 🐙 main * ↑1 extra tail that clamps"),
			Value: "statusbar:git",
		},
		{
			Label: nativeSettingsAppearanceRowForTest("Notify icon", "emoji - 🔔 Pending Notifications extra tail that clamps"),
			Value: "statusbar:notify",
		},
	}
	options := Options{
		UI:    "settings-statusbar",
		Title: "모양 - 테마 글꼴 및 아이콘 장식",
		TitleChips: []projmuxpicker.Chip{
			{Label: "전체", Active: true},
			{Label: "프로젝트", Disabled: true},
		},
		Prompt: "설정 > 모양 > ",
		Footer: "Enter: open  |  Back row: parent ",
		Items:  items,
		Theme:  &effective,
	}
	layout := nativeLayout{Rows: 12, Cols: 72}

	for _, selected := range []int{0, len(items) - 1} {
		frame := nativeInteractiveFrame(options, items, "", 0, selected, 0, layout)
		lines := strings.Split(frame, "\r\n")
		if len(lines) != layout.Rows {
			t.Fatalf("selected %d native frame rows = %d, want %d: %q", selected, len(lines), layout.Rows, frame)
		}
		for i, line := range lines {
			if got := projmuxpicker.VisibleLen(line); got != layout.Cols {
				t.Fatalf("selected %d frame row %d width = %d, want %d: %q", selected, i, got, layout.Cols, line)
			}
			plain := stripANSISequences(line)
			if i > 0 && i < len(lines)-1 && strings.HasPrefix(plain, "│") && !strings.HasSuffix(plain, "│") {
				t.Fatalf("selected %d frame row %d = %q, want stable right border", selected, i, line)
			}
			if settingsAppearanceParentRowForTest(plain) && !strings.HasSuffix(plain, " │") {
				t.Fatalf("selected %d appearance row %d = %q, want marker lane before right border", selected, i, line)
			}
		}
		for _, want := range []string{"검색", "설정 > 모양 >", "전체", "프로젝트", "⏳", "✅", "🔄", "📂", "🐙", "🔔"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("selected %d native frame = %q, want %q", selected, frame, want)
			}
		}
	}
}

func nativeSettingsAppearanceRowForTest(name, description string) string {
	const nameWidth = 24
	padding := max(nameWidth-len(name), 0)
	return "▸  " + theme.ANSIAccentActionStart + name + strings.Repeat(" ", padding) + theme.ANSIReset +
		"  " + theme.ANSITextDimStart + description + theme.ANSIReset
}

func settingsAppearanceParentRowForTest(plain string) bool {
	for _, marker := range []string{"AI badge style", "Path icon", "Git icon", "Notify icon"} {
		if strings.Contains(plain, marker) {
			return true
		}
	}
	return false
}

func TestNativeInteractiveNoFooterBlankRowsUseThemeBackground(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{})
	frameStyle := nativeFrameStyleForTest(t, effective)
	items := []Item{{Title: "\x1b[31mapi\x1b[0m", Value: "/repo/api"}}
	frame := nativeInteractiveFrame(Options{
		UI:    "switch",
		Title: "Projects",
		Items: items,
		Theme: &effective,
	}, items, "", 0, 0, 0, nativeLayout{Rows: 9, Cols: 32})

	lines := strings.Split(frame, "\r\n")
	if got, want := len(lines), 9; got != want {
		t.Fatalf("native frame rows = %d, want %d: %q", got, want, frame)
	}
	blankRows := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, frameStyle) {
			t.Fatalf("frame row %d = %q, want app background style prefix %q", i, line, frameStyle)
		}
		assertNativeFrameResetsResumeStyleOrEnd(t, line, frameStyle)
		if got, want := projmuxpicker.VisibleLen(line), 32; got != want {
			t.Fatalf("frame row %d width = %d, want %d: %q", i, got, want, line)
		}
		if stripANSISequences(line) == "│"+strings.Repeat(" ", 30)+"│" {
			blankRows++
		}
	}
	if blankRows == 0 {
		t.Fatalf("native frame = %q, want no-footer blank rows inside styled frame", frame)
	}
}

func TestNativeInteractiveSplitPreviewGapsUseThemeBackground(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{})
	frameStyle := nativeFrameStyleForTest(t, effective)
	items := []Item{
		{Title: "api", Value: "/repo/api", PreviewTarget: "/repo/api"},
		{Title: "web", Value: "/repo/web", PreviewTarget: "/repo/web"},
	}
	frame := nativeInteractiveFrame(Options{
		UI:    "switch",
		Title: "Projects",
		Items: items,
		Preview: Preview{
			Command: "printf '\\033[32mpreview\\033[0m\\n'",
			Window:  "right,50%,border-left",
		},
		Theme: &effective,
	}, items, "", 0, 0, 0, nativeLayout{Rows: 9, Cols: 96})

	lines := strings.Split(frame, "\r\n")
	if got, want := len(lines), 9; got != want {
		t.Fatalf("native frame rows = %d, want %d: %q", got, want, frame)
	}
	if !strings.Contains(frame, "│") || !strings.Contains(frame, "preview") {
		t.Fatalf("native frame = %q, want split preview separator and preview content", frame)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, frameStyle) {
			t.Fatalf("frame row %d = %q, want app background style prefix %q", i, line, frameStyle)
		}
		assertNativeFrameResetsResumeStyleOrEnd(t, line, frameStyle)
		if got, want := projmuxpicker.VisibleLen(line), 96; got != want {
			t.Fatalf("frame row %d width = %d, want %d: %q", i, got, want, line)
		}
	}
}

func nativeFrameStyleForTest(t *testing.T, effective theme.EffectiveTheme) string {
	t.Helper()
	nativeTheme := projmuxpicker.ThemeFromEffective(effective)
	style := nativeTheme.Background + nativeTheme.Foreground
	if style == "" {
		t.Fatal("native frame style empty, want app background/foreground SGR")
	}
	return style
}

func assertNativeFrameResetsResumeStyleOrEnd(t *testing.T, line, style string) {
	t.Helper()
	for start := 0; ; {
		idx := strings.Index(line[start:], nativeReset)
		if idx < 0 {
			return
		}
		after := start + idx + len(nativeReset)
		if after == len(line) || strings.HasPrefix(line[after:], style) {
			start = after
			continue
		}
		t.Fatalf("line = %q, want reset followed by frame style %q or row end", line, style)
	}
}

func TestNativeInteractiveFrameUsesCRLFRowsForRawTTY(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:    "switch",
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}, []Item{{Title: "api", Value: "/repo/api"}}, "", 0, 0, nativeLayout{Rows: 6, Cols: 24})

	rendered := out.String()
	if !strings.Contains(rendered, "╮\r\n│") || !strings.Contains(rendered, "│\r\n╰") {
		t.Fatalf("native output = %q, want frame rows to use CRLF so raw TTY returns to column 0", rendered)
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

func TestNativeInteractiveSelectsOnMouseRelease(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[<0;3;5M\x1b[<0;3;5m"), io.Discard, Options{
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
		t.Fatalf("result = %#v, want mouse release to select web", result)
	}
}

func TestNativeInteractiveMouseDownOnlyFocuses(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[<0;3;5M\r"), io.Discard, Options{
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
		t.Fatalf("result = %#v, want mouse down to focus web before enter", result)
	}
}

func TestNativeInteractiveMouseDragFollowsSelection(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[<0;3;4M\x1b[<32;3;5M\x1b[<0;99;5m\r"), io.Discard, Options{
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
		t.Fatalf("result = %#v, want primary drag to focus web before enter", result)
	}
}

func TestNativeInteractiveIgnoresMouseReleaseBeforeDown(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[<0;3;5m\x1b[<0;3;5M\x1b[<0;3;5m"), io.Discard, Options{
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
		t.Fatalf("result = %#v, want release before down ignored and later mouse up to select web", result)
	}
}

func TestNativeInteractiveSupportsMouseWheelSelection(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x1b[<65;3;4M\r"), io.Discard, Options{
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
		t.Fatalf("result = %#v, want mouse wheel down to focus web before enter", result)
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

func TestNativeInteractiveNormalizesAltShiftArrowCSIKeys(t *testing.T) {
	t.Parallel()

	// xterm encodes Alt-Shift-Left/Right as CSI 1;4D / 1;4C — modifier 4 is
	// Shift+Alt. Settings popup Phase 2.6 binds Alt-Shift-Left/Right for
	// tab navigation so the picker must surface them in their compound
	// normalized form rather than dropping the modifier.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "alt-shift-left", in: "\x1b[1;4D", want: "alt-shift-left"},
		{name: "alt-shift-right", in: "\x1b[1;4C", want: "alt-shift-right"},
		{name: "alt-shift-up", in: "\x1b[1;4A", want: "alt-shift-up"},
		{name: "alt-shift-down", in: "\x1b[1;4B", want: "alt-shift-down"},
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

func TestNativeInteractiveChipClickEmitsClickValueResult(t *testing.T) {
	t.Parallel()

	// Frame layout: outer 40x10, top border on row 1, chip strip on row 2.
	// Chip "Global" starts at column 3 ([ G l o b a l ] occupies 3..10),
	// gap at col 11, chip "Project" at cols 12..20. Click in the middle of
	// the Project chip (col 16) should emit a chip result carrying the
	// chip's ClickValue so the caller can resolve the tab transition.
	in := "\x1b[<0;16;2M\x1b[<0;16;2m"
	result, err := runNativeInteractive(strings.NewReader(in), io.Discard, Options{
		UI: "settings",
		TitleChips: []projmuxpicker.Chip{
			{Label: "Global", Active: true, ClickValue: "__settings_tab_global__"},
			{Label: "Project", ClickValue: "__settings_tab_project__"},
		},
		Items: []Item{
			{Title: "Section A", Value: "section:a"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "chip" || result.Value != "__settings_tab_project__" {
		t.Fatalf("result = %#v, want chip click resolved to Project ClickValue", result)
	}
}

func TestNativeInteractiveChipClickOnActiveChipResolves(t *testing.T) {
	t.Parallel()

	// Clicking the active chip still resolves through ClickValue — caller
	// can interpret "click active tab" as either a no-op or a soft refresh.
	in := "\x1b[<0;5;2M\x1b[<0;5;2m"
	result, err := runNativeInteractive(strings.NewReader(in), io.Discard, Options{
		UI: "settings",
		TitleChips: []projmuxpicker.Chip{
			{Label: "Global", Active: true, ClickValue: "__settings_tab_global__"},
			{Label: "Project", ClickValue: "__settings_tab_project__"},
		},
		Items: []Item{{Title: "Section A", Value: "section:a"}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "chip" || result.Value != "__settings_tab_global__" {
		t.Fatalf("result = %#v, want chip click on active chip resolved to Global ClickValue", result)
	}
}

func TestNativeInteractiveChipClickOnDisabledChipIsNoop(t *testing.T) {
	t.Parallel()

	// Disabled chip click is a no-op (matches chord behaviour). After the
	// click we send Enter to select the only item — verify the result is
	// the item, not a chip transition.
	in := "\x1b[<0;16;2M\x1b[<0;16;2m\r"
	result, err := runNativeInteractive(strings.NewReader(in), io.Discard, Options{
		UI: "settings",
		TitleChips: []projmuxpicker.Chip{
			{Label: "Global", Active: true, ClickValue: "__settings_tab_global__"},
			{Label: "Project", Disabled: true, ClickValue: "__settings_tab_project__"},
		},
		Items: []Item{{Title: "Section A", Value: "section:a"}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "enter" || result.Value != "section:a" {
		t.Fatalf("result = %#v, want disabled chip click ignored, enter selecting section:a", result)
	}
}

func TestNativeInteractiveChipClickOutsideStripDoesNotEmitChipResult(t *testing.T) {
	t.Parallel()

	// A click on a row that is not the chip strip must not be hijacked by
	// the chip-click handler. We check the result directly by calling the
	// chip resolver — the integration-style test above already exercises
	// row==2 clicks, so here we just need to assert "row==3 returns no
	// chip" so future refactors of the row-2 invariant surface as a
	// regression in this guard.
	if _, ok := nativeMouseChipResult(Options{
		TitleChips: []projmuxpicker.Chip{
			{Label: "Global", Active: true, ClickValue: "tab:global"},
			{Label: "Project", ClickValue: "tab:project"},
		},
	}, nativeLayout{Rows: 24, Cols: 40}, 16, 3, ""); ok {
		t.Fatalf("nativeMouseChipResult(row=3) ok=true, want false because chip strip lives on row 2")
	}
}

func TestNativeInteractiveWaitsForSplitEscapeSequences(t *testing.T) {
	t.Parallel()

	key, err := readNativeKey(&delayedByteReader{
		zerosBeforeByte: 25,
		data:            []byte("\x1b[B"),
	})
	if err != nil {
		t.Fatalf("readNativeKey() error = %v", err)
	}
	if key.Name != "down" || key.Text != "" {
		t.Fatalf("key = %#v, want down without query leakage", key)
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

func TestNativeInteractiveDisableSearchIgnoresPrintableInput(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	result, err := runNativeInteractive(strings.NewReader("web\r"), &out, Options{
		UI:            "notify-sidebar",
		Prompt:        "Notify > ",
		InitialQuery:  "web",
		DisableSearch: true,
		Items: []Item{
			{Title: "deploy", Value: "deploy-id", SearchText: "deploy"},
			{Title: "web", Value: "web-id", SearchText: "web"},
		},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "deploy-id" || result.Query != "" {
		t.Fatalf("result = %#v, want unfiltered first row with empty query", result)
	}
	rendered := out.String()
	if strings.Contains(rendered, "Notify >") {
		t.Fatalf("native output = %q, want disabled search to hide prompt/query input", rendered)
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

func TestNativeInteractiveSupportsRawCtrlXCustomAction(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("\x18"), io.Discard, Options{
		UI:      "switch",
		Items:   []Item{{Title: "api", Value: "/repo/api"}},
		Actions: CustomActions("ctrl-x"),
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "ctrl-x" || result.Value != "/repo/api" {
		t.Fatalf("result = %#v, want raw ctrl-x custom action on selected item", result)
	}
}

func TestNativeInteractiveSupportsCSIuCtrlXCustomAction(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"\x1b[120;5u", "\x1b[24;5u"} {
		result, err := runNativeInteractive(strings.NewReader(input), io.Discard, Options{
			UI:      "switch",
			Items:   []Item{{Title: "api", Value: "/repo/api"}},
			Actions: CustomActions("ctrl-x"),
		})
		if err != nil {
			t.Fatalf("runNativeInteractive(%q) error = %v", input, err)
		}
		if result.Key != "ctrl-x" || result.Value != "/repo/api" {
			t.Fatalf("result for %q = %#v, want CSI-u ctrl-x custom action on selected item", input, result)
		}
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
		{name: "ctrl-x generic csi", in: "\x1b[120;5u", want: "ctrl-x"},
		{name: "ctrl-x control-code csi", in: "\x1b[24;5u", want: "ctrl-x"},
		{name: "enter generic csi", in: "\x1b[13u", want: "enter"},
		{name: "esc generic csi", in: "\x1b[27u", want: "esc"},
		{name: "backspace generic csi", in: "\x1b[127u", want: "backspace"},
		{name: "tab generic csi", in: "\x1b[9u", want: "tab"},
		{name: "shift-tab generic csi", in: "\x1b[9;2u", want: "shift-tab"},
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
	for i := range 20 {
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
	plain := stripANSISequences(line)
	if !strings.Contains(plain, "› api") || !strings.HasSuffix(plain, "2/8") {
		t.Fatalf("nativePromptLine() = %q, want prompt and inline count", line)
	}
}

func TestNativePromptLineRendersQueryCursor(t *testing.T) {
	t.Parallel()

	line := nativePromptLineWithCursor("› ", "abcd", 2, 1, 1, 20)
	if !strings.Contains(line, "ab"+nativeCursorStart+"c"+nativeReset+"d") {
		t.Fatalf("nativePromptLineWithCursor() = %q, want styled cursor at query index", line)
	}
	if got, want := nativeVisibleLen(line), 20; got != want {
		t.Fatalf("nativeVisibleLen(line) = %d, want padded width %d", got, want)
	}
}

func TestNativePromptLineRendersEndCursor(t *testing.T) {
	t.Parallel()

	line := nativePromptLineWithCursor("› ", "api", 3, 1, 1, 20)
	if !strings.Contains(line, "api"+nativeCursorStart+" "+nativeReset) {
		t.Fatalf("nativePromptLineWithCursor() = %q, want visible end cursor", line)
	}
}

func TestNativePromptLineCursorDoesNotForceFilteredInfo(t *testing.T) {
	t.Parallel()

	line := nativePromptLineWithCursor("› ", "", 0, 5, 5, 20)
	plain := stripANSISequences(line)
	if strings.Contains(plain, "5/5") || !strings.HasSuffix(plain, "5") {
		t.Fatalf("nativePromptLineWithCursor() = %q, want unfiltered total count", line)
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
	if strings.Contains(rendered, "│ preview") {
		t.Fatalf("native output = %q, want fzf-like preview border without padded title column", rendered)
	}
}

func TestNativeInteractiveRendersSplitPreviewBorderThroughListArea(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	options := Options{
		UI: "switch",
		Preview: Preview{
			Command: "printf 'preview:%s' {2}",
			Window:  "right,60%,border-left",
		},
		Items: []Item{{Title: "api", Value: "/repo/api"}},
	}
	layout := nativeLayout{Rows: 14, Cols: 116}
	items := []Item{{Title: "api", Value: "/repo/api"}}
	renderNativeInteractiveContent(&out, options, items, "", 0, 0, 0, layout)

	listLimit := nativeListLimit(options, layout, "right", nativePreviewHeight(layout.Rows, options.Preview.Window), true)
	separatorRows := 0
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if strings.Contains(line, "│") {
			separatorRows++
		}
	}
	if separatorRows != listLimit {
		t.Fatalf("split preview separator rows = %d, want %d in output %q", separatorRows, listLimit, out.String())
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

func TestNativeSidebarReservesBlankDownPreviewFrame(t *testing.T) {
	t.Parallel()

	items := []Item{{Title: "api", Value: "/repo/api"}}
	options := Options{
		UI:        "sidebar",
		Preview:   Preview{Window: "down,25%,border-top"},
		Items:     items,
		MultiLine: true,
	}
	layout := nativeLayout{Rows: 16, Cols: 48}
	previewHeight := nativePreviewHeight(layout.Rows, options.Preview.Window)
	listLimit := nativeListLimit(options, layout, "down", previewHeight, true)

	var out bytes.Buffer
	renderNativeInteractiveContent(&out, options, items, "", 0, 0, 0, layout)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if got, want := len(lines), nativeChromeLineCount(options)+listLimit+1+previewHeight; got != want {
		t.Fatalf("rendered line count = %d, want reserved preview frame height %d in %q", got, want, out.String())
	}
	if got := strings.Count(out.String(), projmuxpicker.SeparatorLine(layout.Cols)); got < 2 {
		t.Fatalf("native output = %q, want search separator and reserved down-preview separator", out.String())
	}
}

func TestLimitedNativePreviewLinesKeepsLimitWithOverflowNotice(t *testing.T) {
	t.Parallel()

	lines := limitedNativePreviewLines("one\ntwo\nthree\nfour", 0, 3)

	if got, want := len(lines), 3; got != want {
		t.Fatalf("limitedNativePreviewLines() len = %d, want %d: %#v", got, want, lines)
	}
	if got, want := lines[2], "... 2 more lines"; got != want {
		t.Fatalf("overflow notice = %q, want %q", got, want)
	}
}

func TestNativeInteractiveUsesScrollbarForLongLists(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 24)
	for i := range 24 {
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
	for i := range 20 {
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
	if got, want := nativeListLimit(options, nativeLayout{Rows: 20, Cols: 80}, "down", 5, true), 7; got != want {
		t.Fatalf("nativeListLimit() = %d, want %d", got, want)
	}
}

func TestNativeInteractiveRendersFooterAtBottom(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:     "settings",
		Footer: "Enter: open",
		Items: []Item{
			{Title: "AI Settings", Value: "ai"},
			{Title: "Project Picker", Value: "project"},
		},
	}, []Item{
		{Title: "AI Settings", Value: "ai"},
		{Title: "Project Picker", Value: "project"},
	}, "", 0, 0, nativeLayout{Rows: 12, Cols: 40})

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("native output = %q, want framed footer", out.String())
	}
	footerLine := lines[len(lines)-2]
	separatorLine := lines[len(lines)-3]
	if !strings.Contains(footerLine, "Enter: open") {
		t.Fatalf("footer line = %q, want footer above bottom border", footerLine)
	}
	if !strings.Contains(separatorLine, nativeGapLine) {
		t.Fatalf("separator line = %q, want fzf-like footer border", separatorLine)
	}
	promptIndex := -1
	footerIndex := -1
	for i, line := range lines {
		if strings.Contains(line, "Settings") {
			promptIndex = i
		}
		if strings.Contains(line, "Enter: open") {
			footerIndex = i
		}
	}
	if promptIndex < 0 || footerIndex <= promptIndex+2 {
		t.Fatalf("native output = %q, want footer separated from prompt/list", out.String())
	}
}

func TestNativeVisibleRangeCountsMultilineRenderedRows(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 10)
	for i := range 10 {
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

func TestNativeInteractiveShowsPartialNextMultilineItem(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Label: "item 0\n  detail 0a\n  detail 0b", Value: "0"},
		{Label: "item 1\n  detail 1a\n  detail 1b", Value: "1"},
		{Label: "item 2\n  detail 2a\n  detail 2b", Value: "2"},
	}

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:        "sidebar",
		Items:     items,
		MultiLine: true,
	}, items, "", 0, 0, nativeLayout{Rows: 9, Cols: 48})

	rendered := out.String()
	if !strings.Contains(rendered, "item 1") {
		t.Fatalf("native output = %q, want next multiline item to start in remaining viewport", rendered)
	}
	if !strings.Contains(rendered, projmuxpicker.MutedStart+strings.Repeat(nativeGapLine, 8)) {
		t.Fatalf("native output = %q, want separator before partial next multiline item", rendered)
	}
	if strings.Contains(rendered, "detail 1a") || strings.Contains(rendered, "detail 1b") {
		t.Fatalf("native output = %q, want next item clipped to available viewport rows", rendered)
	}
	if !strings.Contains(rendered, nativeScrollbar) {
		t.Fatalf("native output = %q, want scrollbar for clipped multiline list", rendered)
	}
}

func TestNativePartialNextMultilineItemKeepsGapLine(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Label: "item 0\n  detail 0a\n  detail 0b", Value: "0"},
		{Label: "item 1\n  detail 1a", Value: "1"},
	}
	lines := nativeInteractiveListLines(items, 0, 1, 0, true)

	oneRemaining := nativeAppendPartialNextItemLines(items, lines, 1, 0, 4)
	if len(oneRemaining) != 4 {
		t.Fatalf("partial lines len = %d, want 4: %#v", len(oneRemaining), oneRemaining)
	}
	gapOnly := nativeRenderableListLine(oneRemaining[3], 48)
	if !strings.Contains(gapOnly, strings.Repeat(nativeGapLine, 8)) || strings.Contains(gapOnly, "item 1") {
		t.Fatalf("partial line = %q, want separator before next item when only one row remains", gapOnly)
	}

	twoRemaining := nativeAppendPartialNextItemLines(items, lines, 1, 0, 5)
	if len(twoRemaining) != 5 {
		t.Fatalf("partial lines len = %d, want 5: %#v", len(twoRemaining), twoRemaining)
	}
	gap := nativeRenderableListLine(twoRemaining[3], 48)
	next := nativeRenderableListLine(twoRemaining[4], 48)
	if !strings.Contains(gap, strings.Repeat(nativeGapLine, 8)) {
		t.Fatalf("gap line = %q, want separator before partial next item", gap)
	}
	if !strings.Contains(next, "item 1") {
		t.Fatalf("next line = %q, want first row of partial next item after separator", next)
	}
}

func TestNativeListScrollbarUnitsUseRenderedRowsForMultiline(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Label: "item 0\n  detail", Value: "0"},
		{Label: "item 1\n  detail", Value: "1"},
		{Label: "item 2\n  detail", Value: "2"},
	}

	total, start, end := nativeListScrollbarUnits(items, 1, 3, true)

	if got, want := total, 8; got != want {
		t.Fatalf("total scroll units = %d, want rendered row total %d", got, want)
	}
	if got, want := start, 3; got != want {
		t.Fatalf("start scroll units = %d, want rendered start offset %d", got, want)
	}
	if got, want := end, 8; got != want {
		t.Fatalf("end scroll units = %d, want rendered end offset %d", got, want)
	}
}

func TestNativeInteractiveKeepsMultilineScrollbarOnViewportTrack(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 8)
	for i := range 8 {
		items = append(items, Item{
			Label: "item " + strconv.Itoa(i) + "\n  detail",
			Value: strconv.Itoa(i),
		})
	}

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:        "sidebar",
		Items:     items,
		MultiLine: true,
	}, items, "", 4, 0, nativeLayout{Rows: 12, Cols: 40})

	lines := strings.Split(strings.TrimRight(out.String(), "\r\n"), "\r\n")
	scrollbarRows := 0
	for _, line := range lines {
		if strings.Contains(line, nativeScrollbar) {
			scrollbarRows++
		}
	}
	if scrollbarRows < 2 {
		t.Fatalf("native output = %q, want proportional multiline scrollbar on viewport track", out.String())
	}
}

func TestNativeInteractiveBackfillsBottomMultilineViewport(t *testing.T) {
	t.Parallel()

	items := make([]Item, 0, 8)
	for i := range 8 {
		items = append(items, Item{
			Label: "item " + strconv.Itoa(i) + "\n  detail " + strconv.Itoa(i),
			Value: strconv.Itoa(i),
		})
	}

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI:        "sidebar",
		Items:     items,
		MultiLine: true,
	}, items, "", len(items)-1, 0, nativeLayout{Rows: 10, Cols: 40})

	lines := strings.Split(strings.TrimRight(out.String(), "\r\n"), "\r\n")
	if len(lines) < 2 {
		t.Fatalf("native output = %q, want framed output", out.String())
	}
	lastContent := lines[len(lines)-2]
	if !strings.Contains(lastContent, "detail 7") {
		t.Fatalf("last content row = %q in %q, want final item tight to bottom border", lastContent, out.String())
	}
}

func TestNativePreviewWidthUsesPreviewWindowPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		contentCols int
		want        int
	}{
		{contentCols: 76, want: 42},
		{contentCols: 96, want: 54},
		{contentCols: 116, want: 66},
	}

	for _, tt := range tests {
		if got := nativePreviewWidth(tt.contentCols, "right,60%,border-left"); got != tt.want {
			t.Fatalf("nativePreviewWidth(%d) = %d, want fzf-measured content width %d", tt.contentCols, got, tt.want)
		}
	}
}

func TestNativePreviewHeightUsesPreviewWindowPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		contentRows int
		want        int
	}{
		{contentRows: 18, want: 3},
		{contentRows: 28, want: 6},
		{contentRows: 38, want: 8},
	}

	for _, tt := range tests {
		if got := nativePreviewHeight(tt.contentRows, "down,25%,border-top"); got != tt.want {
			t.Fatalf("nativePreviewHeight(%d) = %d, want fzf-measured content height %d", tt.contentRows, got, tt.want)
		}
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
	if !strings.Contains(rendered, "▌") || !strings.Contains(rendered, "48;2;44;56;61") {
		t.Fatalf("native output = %q, want fzf-like pointer and current-row color", rendered)
	}
	if strings.Contains(rendered, "> api") {
		t.Fatalf("native output = %q, want multiline selection to avoid legacy > pointer", rendered)
	}
}

func TestNativeInteractiveRendersSelectedMultilineContinuationMarker(t *testing.T) {
	t.Parallel()

	lines := nativeInteractiveItemLines(Item{
		Title:     "api",
		MetaLines: []string{"~rp/api", "master"},
		Value:     "/repo/api",
	}, true, true)

	if len(lines) != 3 {
		t.Fatalf("nativeInteractiveItemLines() len = %d, want 3: %#v", len(lines), lines)
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, nativeContinuation) || !strings.Contains(line, nativeCurrentStart) {
			t.Fatalf("selected multiline continuation line = %q, want marker and current-row style", line)
		}
		if strings.Contains(line, "┃┃┃") || strings.Contains(line, "|||") || !strings.Contains(line, "▌") {
			t.Fatalf("selected multiline continuation line = %q, want single pointer-width continuation bar", line)
		}
	}
}

func TestNativeInteractivePadsSelectedLineInsideStyle(t *testing.T) {
	t.Parallel()

	rendered := nativeRenderableListLine(nativeCurrentStart+"api"+nativeReset, 12)
	if !strings.Contains(rendered, nativeCurrentStart+"api         "+nativeReset) {
		t.Fatalf("nativeRenderableListLine() = %q, want padding before reset for full-row highlight", rendered)
	}
}

func TestNativeInteractiveUsesCurrentStyleForSimpleSelection(t *testing.T) {
	t.Parallel()

	line := nativeInteractiveItemLines(Item{
		Label: "\x1b[36mAI Settings\x1b[0m  \x1b[90mdefault split mode\x1b[0m",
		Value: "ai",
	}, true, false)[0]
	rendered := nativeRenderableListLine(line, 48)

	if !strings.Contains(rendered, nativePointer) || !strings.Contains(rendered, nativeCurrentStart) {
		t.Fatalf("rendered selected line = %q, want projmux pointer and current-row style", rendered)
	}
	if !strings.HasPrefix(rendered, nativePointer) {
		t.Fatalf("rendered selected line = %q, want pointer in current-row gutter", rendered)
	}
	if strings.HasPrefix(rendered, nativePointer+nativeCurrentStart) {
		t.Fatalf("rendered selected line = %q, want selected content to reuse pointer gutter style", rendered)
	}
	if strings.Contains(rendered, nativeInverseStart) {
		t.Fatalf("rendered selected line = %q, want no terminal inverse selection style", rendered)
	}
	if !strings.Contains(rendered, "default split mode"+nativeReset+nativeCurrentStart) {
		t.Fatalf("rendered selected line = %q, want current style restored after final label reset", rendered)
	}
	if !strings.HasSuffix(rendered, nativeReset) {
		t.Fatalf("rendered selected line = %q, want final reset", rendered)
	}
}

func TestNativeInteractiveHighlightsSimpleQueryMatches(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI: "ai-picker",
		Items: []Item{
			{Label: "\x1b[36mCodex\x1b[0m  \x1b[90mOpenAI CLI\x1b[0m", Value: "codex"},
			{Label: "\x1b[36mClaude\x1b[0m  \x1b[90mAnthropic CLI\x1b[0m", Value: "claude"},
		},
	}, []Item{
		{Label: "\x1b[36mCodex\x1b[0m  \x1b[90mOpenAI CLI\x1b[0m", Value: "codex"},
		{Label: "\x1b[36mClaude\x1b[0m  \x1b[90mAnthropic CLI\x1b[0m", Value: "claude"},
	}, "Co", 1, 0, nativeLayout{Rows: 8, Cols: 56})

	rendered := out.String()
	if !strings.Contains(rendered, nativeHighlightStart+"C"+nativeReset+"\x1b[36m") ||
		!strings.Contains(rendered, nativeHighlightStart+"o"+nativeReset+"\x1b[36m") {
		t.Fatalf("native output = %q, want fzf-like query highlight with original ANSI style restored", rendered)
	}
}

func TestNativeInteractiveDoesNotHighlightSearchKeyReloadLists(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderNativeInteractive(&out, Options{
		UI: "switch",
		Items: []Item{{
			Label:      "bravo-web",
			Value:      "/repo/bravo-web",
			SearchText: "bravo web project",
		}},
	}, []Item{{
		Label:      "bravo-web",
		Value:      "/repo/bravo-web",
		SearchText: "bravo web project",
	}}, "bravo", 0, 0, nativeLayout{Rows: 8, Cols: 48})

	if rendered := out.String(); strings.Contains(rendered, nativeHighlightStart) {
		t.Fatalf("native output = %q, want search-key reload lists to preserve fzf disabled-filter rendering without match highlights", rendered)
	}
}

func TestNativeTruncateANSIClosesStyleWhenCutBeforeReset(t *testing.T) {
	t.Parallel()

	got := nativeTruncateANSI("\x1b[90mProject Root is a long hint\x1b[0m", 12)
	if !strings.HasSuffix(got, nativeReset) {
		t.Fatalf("nativeTruncateANSI() = %q, want trailing reset after truncating styled text", got)
	}
	if gotLen := nativeVisibleLen(got); gotLen != 12 {
		t.Fatalf("nativeVisibleLen(truncated) = %d, want 12; value = %q", gotLen, got)
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
	if !strings.Contains(rendered, projmuxpicker.MutedStart+strings.Repeat(nativeGapLine, 8)) {
		t.Fatalf("native output = %q, want muted fzf-like full-width multiline gap line", rendered)
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

func TestNativeInteractiveCustomActionMutatesItemsAndRefreshes(t *testing.T) {
	t.Parallel()

	var calls int
	result, err := runNativeInteractive(strings.NewReader("a\r"), io.Discard, Options{
		UI:            "notify-sidebar",
		DisableSearch: true,
		Items: []Item{
			{Title: "api", Value: "api"},
			{Title: "web", Value: "web"},
		},
		Actions: []Action{{
			Key:    "a",
			Intent: ActionCustom,
			Mutate: func(ctx ActionContext) (DeferredUpdate, error) {
				calls++
				if ctx.Key != "a" || ctx.Value != "api" || ctx.SelectedIndex != 0 {
					t.Fatalf("action context = %#v, want selected api", ctx)
				}
				return DeferredUpdate{Items: []Item{{Title: "web", Value: "web"}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("mutate calls = %d, want 1", calls)
	}
	if result.Value != "web" {
		t.Fatalf("result = %#v, want web after in-session refresh", result)
	}
}

func TestNativeInteractiveCustomActionCanReturnResult(t *testing.T) {
	t.Parallel()

	var calls int
	result, err := runNativeInteractive(strings.NewReader("\r"), io.Discard, Options{
		UI:            "notify-sidebar",
		DisableSearch: true,
		Items: []Item{
			{Title: "child", Value: "child-id"},
		},
		Actions: []Action{{
			Key:    "enter",
			Intent: ActionCustom,
			Mutate: func(ctx ActionContext) (DeferredUpdate, error) {
				calls++
				if ctx.Value != "child-id" {
					t.Fatalf("action context = %#v, want child-id", ctx)
				}
				return DeferredUpdate{Result: &Result{Key: ctx.Key, Value: ctx.Value, Query: ctx.Query}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("mutate calls = %d, want 1", calls)
	}
	if result.Key != "enter" || result.Value != "child-id" {
		t.Fatalf("result = %#v, want custom result for child-id", result)
	}
}

func TestNativeInteractiveCustomActionRefreshPreservesSelectedValue(t *testing.T) {
	t.Parallel()

	result, err := runNativeInteractive(strings.NewReader("x\r"), io.Discard, Options{
		UI:              "notify-sidebar",
		DisableSearch:   true,
		InitialIndex:    1,
		InitialIndexSet: true,
		Items: []Item{
			{Title: "api", Value: "api"},
			{Title: "web", Value: "web"},
			{Title: "worker", Value: "worker"},
		},
		Actions: []Action{{
			Key:    "x",
			Intent: ActionCustom,
			Mutate: func(ctx ActionContext) (DeferredUpdate, error) {
				if ctx.Value != "web" || ctx.SelectedIndex != 1 {
					t.Fatalf("action context = %#v, want selected web", ctx)
				}
				return DeferredUpdate{Items: []Item{
					{Title: "web", Value: "web"},
					{Title: "worker", Value: "worker"},
				}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Value != "web" {
		t.Fatalf("result = %#v, want preserved web selection", result)
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

type delayedByteReader struct {
	data            []byte
	zerosBeforeByte int
	index           int
	zeros           int
}

func (r *delayedByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.index >= len(r.data) {
		return 0, io.EOF
	}
	if r.index > 0 && r.zeros < r.zerosBeforeByte {
		r.zeros++
		return 0, nil
	}
	p[0] = r.data[r.index]
	r.index++
	r.zeros = 0
	return 1, nil
}
