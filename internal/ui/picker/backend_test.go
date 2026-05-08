package picker

import (
	"bytes"
	"io"
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
