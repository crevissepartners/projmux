package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

type decodedResourceContext struct {
	Value    string                     `json:"value"`
	Source   registryview.ContextSource `json:"source"`
	Observed bool                       `json:"observed"`
}

type decodedContextResourceItem struct {
	Metadata struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	Context decodedResourceContext `json:"context"`
}

type decodedContextResourceList struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Items      []decodedContextResourceItem `json:"items"`
}

// TestGetListJSONCarriesContextFromTheResolutionSnapshot is the exact
// whole-document matrix for all four registry-backed plural get routes. It
// also includes an empty Pane context, whose three required keys must not be
// omitted.
func TestGetListJSONCarriesContextFromTheResolutionSnapshot(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		kind coremetadata.Kind
		uid  string
		args []string
		want registryview.Context
	}{
		{
			name: "project root basename",
			kind: coremetadata.KindProject,
			uid:  "prj-alpha",
			args: []string{"projects", "--project", "alpha", "-o", "json"},
			want: registryview.Context{Value: "alpha", Source: registryview.ContextSourceProjectRoot},
		},
		{
			name: "window fallback",
			kind: coremetadata.KindWindow,
			uid:  "win-alpha-main",
			args: []string{"windows", "--project", "alpha", "--window", "main", "-o", "json"},
			want: registryview.Context{Value: "window", Source: registryview.ContextSourceWindowFallback},
		},
		{
			name: "empty pane context keeps every key",
			kind: coremetadata.KindPane,
			uid:  "pan-alpha-zsh",
			args: []string{"panes", "--project", "alpha", "--window", "main", "--pane", "zsh", "-o", "json"},
			want: registryview.Context{},
		},
		{
			name: "agent provider",
			kind: coremetadata.KindAgent,
			uid:  "agt-alpha-codex",
			args: []string{"agents", "--project", "alpha", "--window", "main", "-o", "json"},
			want: registryview.Context{Value: "codex", Source: registryview.ContextSourceAgentProvider},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before, err := json.Marshal(store.registry)
			if err != nil {
				t.Fatalf("marshal Registry before get: %v", err)
			}

			stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store), test.args...)
			if err != nil {
				t.Fatalf("get %v error = %v", test.args, err)
			}
			if stderr != "" {
				t.Fatalf("get %v stderr = %q, want none", test.args, stderr)
			}
			resource, _, ok := resourceFor(store.registry, test.kind, test.uid)
			if !ok {
				t.Fatalf("fixture is missing %s %s", test.kind, test.uid)
			}
			want := exactContextListDocument(t, test.kind, resource, test.want)
			if stdout != want {
				t.Fatalf("get %v exact JSON =\n%s\nwant:\n%s", test.args, stdout, want)
			}
			assertContextKeys(t, stdout, test.want)

			after, err := json.Marshal(store.registry)
			if err != nil {
				t.Fatalf("marshal Registry after get: %v", err)
			}
			if !bytes.Equal(after, before) || store.transactions != 0 || store.writes != 0 {
				t.Fatalf("plural JSON changed Registry: bytesEqual=%t transactions=%d writes=%d",
					bytes.Equal(after, before), store.transactions, store.writes)
			}
		})
	}
}

func exactContextListDocument(t *testing.T, kind coremetadata.Kind, resource any, context registryview.Context) string {
	t.Helper()
	resourceBytes, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("marshal expected resource: %v", err)
	}
	contextBytes, err := json.Marshal(struct {
		Value    string                     `json:"value"`
		Source   registryview.ContextSource `json:"source"`
		Observed bool                       `json:"observed"`
	}{Value: context.Value, Source: context.Source, Observed: context.Observed})
	if err != nil {
		t.Fatalf("marshal expected context: %v", err)
	}
	item := append(append(append([]byte{}, resourceBytes[:len(resourceBytes)-1]...), []byte(`,"context":`)...), contextBytes...)
	item = append(item, '}')
	compact := fmt.Sprintf(`{"apiVersion":%q,"kind":%q,"items":[%s]}`,
		coremetadata.APIVersion, resourceListKind(kind, false), item)

	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(compact), "", "  "); err != nil {
		t.Fatalf("indent expected list document: %v", err)
	}
	indented.WriteByte('\n')
	return indented.String()
}

func assertContextKeys(t *testing.T, stdout string, want registryview.Context) {
	t.Helper()
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("decode list document: %v", err)
	}
	if len(raw.Items) != 1 {
		t.Fatalf("list item count = %d, want 1", len(raw.Items))
	}
	contextRaw, ok := raw.Items[0]["context"]
	if !ok {
		t.Fatal("list item has no top-level context")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(contextRaw, &keys); err != nil {
		t.Fatalf("decode context object: %v", err)
	}
	if len(keys) != 3 || keys["value"] == nil || keys["source"] == nil || keys["observed"] == nil {
		t.Fatalf("context keys = %v, want exactly value/source/observed", reflect.ValueOf(keys).MapKeys())
	}
	var got decodedResourceContext
	if err := json.Unmarshal(contextRaw, &got); err != nil {
		t.Fatalf("decode typed context: %v", err)
	}
	if got.Value != want.Value || got.Source != want.Source || got.Observed != want.Observed {
		t.Fatalf("context = %+v, want %+v", got, want)
	}
}

func TestGetListJSONLiveContextRequiresExactUIDBinding(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	graph := resourcegraph.Resolve(store.registry, resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "isolated"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$1", Name: "alpha", ProjectUID: "prj-alpha", ProjectName: "alpha", Root: "/srv/alpha"},
			{ID: "$2", Name: "beta", ProjectUID: "prj-beta", ProjectName: "beta", Root: "/srv/beta"},
		},
		Windows: []resourcegraph.Window{
			{ID: "@1", SessionID: "$1", UID: "win-alpha-main", MirroredName: "main", DisplayName: "alpha exact window"},
			{ID: "@2", SessionID: "$2", UID: "win-runtime-only", MirroredName: "main", DisplayName: "unmatched beta window"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%1", WindowID: "@1", UID: "pan-alpha-zsh", MirroredName: "zsh", Title: "alpha exact pane"},
			{ID: "%2", WindowID: "@2", UID: "pan-runtime-only", MirroredName: "zsh", Title: "unmatched beta pane"},
		},
	})
	projector := registryview.NewObservedContextProjector(graph)
	readCalls := 0
	command := newTestListGetCommand(t, store)
	command.reads = func(coremetadata.Registry) resourceReadSnapshot {
		readCalls++
		return resourceReadSnapshot{
			runtime: coremetadata.RuntimeObservation{
				Windows: map[string]bool{"win-alpha-main": true},
				Panes:   map[string]bool{"pan-alpha-zsh": true},
			},
			contexts: projector,
		}
	}

	for _, test := range []struct {
		name string
		kind coremetadata.Kind
		args []string
		want map[string]registryview.Context
	}{
		{
			name: "projects never claim runtime context",
			kind: coremetadata.KindProject,
			args: []string{"projects", "-o", "json"},
			want: map[string]registryview.Context{
				"prj-alpha": projector.For(coremetadata.KindProject, "prj-alpha"),
				"prj-beta":  projector.For(coremetadata.KindProject, "prj-beta"),
				"prj-gone":  projector.For(coremetadata.KindProject, "prj-gone"),
			},
		},
		{
			name: "only exact window binding is observed",
			kind: coremetadata.KindWindow,
			args: []string{"windows", "--all-projects", "-o", "json"},
			want: map[string]registryview.Context{
				"win-alpha-main":   projector.For(coremetadata.KindWindow, "win-alpha-main"),
				"win-alpha-review": projector.For(coremetadata.KindWindow, "win-alpha-review"),
				"win-beta-main":    projector.For(coremetadata.KindWindow, "win-beta-main"),
				"win-gone-main":    projector.For(coremetadata.KindWindow, "win-gone-main"),
			},
		},
		{
			name: "only exact pane binding is observed",
			kind: coremetadata.KindPane,
			args: []string{"panes", "--all-projects", "-o", "json"},
			want: map[string]registryview.Context{
				"pan-alpha-zsh":    projector.For(coremetadata.KindPane, "pan-alpha-zsh"),
				"pan-alpha-log":    projector.For(coremetadata.KindPane, "pan-alpha-log"),
				"pan-alpha-codex":  projector.For(coremetadata.KindPane, "pan-alpha-codex"),
				"pan-alpha-review": projector.For(coremetadata.KindPane, "pan-alpha-review"),
				"pan-beta-zsh":     projector.For(coremetadata.KindPane, "pan-beta-zsh"),
				"pan-gone-zsh":     projector.For(coremetadata.KindPane, "pan-gone-zsh"),
			},
		},
		{
			name: "agents never claim runtime context",
			kind: coremetadata.KindAgent,
			args: []string{"agents", "--all-projects", "-o", "json"},
			want: map[string]registryview.Context{
				"agt-alpha-codex": projector.For(coremetadata.KindAgent, "agt-alpha-codex"),
				"agt-beta-codex":  projector.For(coremetadata.KindAgent, "agt-beta-codex"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeCalls := readCalls
			stdout, stderr, err := runRoute(t, command, test.args...)
			if err != nil {
				t.Fatalf("get %v error = %v", test.args, err)
			}
			if stderr != "" {
				t.Fatalf("get %v stderr = %q, want none", test.args, stderr)
			}
			if readCalls != beforeCalls+1 {
				t.Fatalf("get %v snapshot reads = %d, want exactly one", test.args, readCalls-beforeCalls)
			}
			got := contextByUID(t, stdout)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("get %v contexts = %#v, want %#v", test.args, got, test.want)
			}
		})
	}

	if got := projector.For(coremetadata.KindWindow, "win-alpha-main"); !got.Observed || got.Source != registryview.ContextSourceLiveWindowName {
		t.Fatalf("exact Window context = %+v, want observed live name", got)
	}
	if got := projector.For(coremetadata.KindPane, "pan-alpha-zsh"); !got.Observed || got.Source != registryview.ContextSourceLivePaneTitle {
		t.Fatalf("exact Pane context = %+v, want observed live title", got)
	}
	for _, target := range []struct {
		kind coremetadata.Kind
		uid  string
	}{
		{coremetadata.KindProject, "prj-alpha"},
		{coremetadata.KindAgent, "agt-alpha-codex"},
		{coremetadata.KindWindow, "win-beta-main"},
		{coremetadata.KindPane, "pan-beta-zsh"},
	} {
		if got := projector.For(target.kind, target.uid); got.Observed {
			t.Fatalf("%s %s context = %+v, want observed=false", target.kind, target.uid, got)
		}
	}
	if store.transactions != 0 || store.writes != 0 {
		t.Fatalf("live JSON reads opened Registry writes: transactions=%d writes=%d", store.transactions, store.writes)
	}
}

func contextByUID(t *testing.T, stdout string) map[string]registryview.Context {
	t.Helper()
	var list decodedContextResourceList
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("decode context resource list: %v", err)
	}
	got := make(map[string]registryview.Context, len(list.Items))
	for _, item := range list.Items {
		got[item.Metadata.UID] = registryview.Context{
			Value: item.Context.Value, Source: item.Context.Source, Observed: item.Context.Observed,
		}
	}
	return got
}

func TestGetListJSONContextDoesNotChangeMetadataOrRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	command := newTestListGetCommand(t, store)
	registryBefore, err := json.Marshal(store.registry)
	if err != nil {
		t.Fatalf("marshal Registry before reads: %v", err)
	}

	window, ok := store.registry.Window("win-alpha-main")
	if !ok {
		t.Fatal("fixture Window is missing")
	}
	var wantMetadata bytes.Buffer
	if err := writeJSON(&wantMetadata, resourceList{
		APIVersion: coremetadata.APIVersion,
		Kind:       "WindowMetadataList",
		Items:      []any{window.Metadata},
	}); err != nil {
		t.Fatalf("render expected metadata: %v", err)
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "metadata list",
			args: []string{"windows", "--project", "alpha", "--window", "main", "-o", "metadata"},
			want: wantMetadata.String(),
		},
		{
			name: "uid scalar",
			args: []string{"windows", "--project", "alpha", "--window", "main", "-o", "uid"},
			want: "win-alpha-main\n",
		},
		{
			name: "default table",
			args: []string{"windows", "--project", "alpha", "--window", "main"},
			want: "NAME  STATUS  ACTIONS\n" +
				"main  live    -\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := runRoute(t, command, test.args...)
			if err != nil {
				t.Fatalf("get %v error = %v", test.args, err)
			}
			if stdout != test.want || stderr != "" {
				t.Fatalf("get %v = stdout %q stderr %q, want stdout %q and no stderr",
					test.args, stdout, stderr, test.want)
			}
			if strings.Contains(stdout, `"context"`) {
				t.Fatalf("get %v leaked JSON context: %s", test.args, stdout)
			}
		})
	}

	registryAfter, err := json.Marshal(store.registry)
	if err != nil {
		t.Fatalf("marshal Registry after reads: %v", err)
	}
	if !bytes.Equal(registryAfter, registryBefore) || store.transactions != 0 || store.writes != 0 {
		t.Fatalf("negative-mode reads changed Registry: bytesEqual=%t transactions=%d writes=%d",
			bytes.Equal(registryAfter, registryBefore), store.transactions, store.writes)
	}
}

func TestResourceContextJSONIsConfinedToPluralGet(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	resource, _, ok := resourceFor(store.registry, coremetadata.KindPane, "pan-alpha-zsh")
	if !ok {
		t.Fatal("fixture Pane is missing")
	}
	var want bytes.Buffer
	if err := writeJSON(&want, resource); err != nil {
		t.Fatalf("render expected singular resource: %v", err)
	}

	getPane := &getCommand{
		loadRegistry: store.store().load,
		currentPath:  &stubCurrentPath{},
		runtime:      liveAlphaRuntime(),
	}
	stdout, stderr, err := runRoute(t, getPane,
		"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "-o", "json")
	if err != nil || stdout != want.String() || stderr != "" || strings.Contains(stdout, `"context"`) {
		t.Fatalf("singular get pane drifted: stdout=%q stderr=%q err=%v want=%q", stdout, stderr, err, want.String())
	}

	describe := newTestDescribeCommand(t, store)
	stdout, stderr, err = runRoute(t, describe,
		"pane", "zsh", "--project", "alpha", "--window", "main", "-o", "json")
	if err != nil || stdout != want.String() || stderr != "" || strings.Contains(stdout, `"context"`) {
		t.Fatalf("describe pane JSON drifted: stdout=%q stderr=%q err=%v want=%q", stdout, stderr, err, want.String())
	}

	match := selector.Match{
		Kind: coremetadata.KindPane, UID: "pan-alpha-zsh", Name: "zsh",
		Context: registryview.Context{Value: "must stay out", Source: registryview.ContextSourceLivePaneTitle, Observed: true},
	}
	var fanOut bytes.Buffer
	if err := writeResourceProjection(&fanOut, "create pane", "json", coremetadata.KindPane,
		[]selector.Match{match}, store.registry, true, resourceFixtureReadClock); err != nil {
		t.Fatalf("write create-style fan-out: %v", err)
	}
	if strings.Contains(fanOut.String(), `"context"`) {
		t.Fatalf("non-get fan-out leaked context: %s", fanOut.String())
	}
}

func TestGetListJSONEmptyAndReadFailureAreAtomic(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stdout, stderr, err := runRoute(t, newTestListGetCommand(t, store),
		"agents", "--project", "gone", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("empty list = stdout %q stderr %q err %v", stdout, stderr, err)
	}
	wantEmpty := "{\n  \"apiVersion\": \"projmux.io/v1alpha1\",\n  \"kind\": \"AgentList\",\n  \"items\": []\n}\n"
	if stdout != wantEmpty {
		t.Fatalf("empty list exact JSON = %q, want %q", stdout, wantEmpty)
	}

	readFailure := errors.New("injected Registry read failure")
	command := newTestListGetCommand(t, store)
	command.loadRegistry = func() (coremetadata.Registry, error) {
		return coremetadata.Registry{}, readFailure
	}
	stdout, _, err = runRoute(t, command, "projects", "-o", "json")
	if !errors.Is(err, readFailure) || stdout != "" {
		t.Fatalf("Registry read failure = stdout %q err %v, want zero stdout and %v", stdout, err, readFailure)
	}

	var partial bytes.Buffer
	err = writeContextResourceListProjection(&partial, "get projects", "json", coremetadata.KindProject,
		[]selector.Match{{Kind: coremetadata.KindProject, UID: "missing", Name: "missing"}},
		store.registry, resourceFixtureReadClock, nil)
	if err == nil || partial.Len() != 0 {
		t.Fatalf("stale resolved uid = stdout %q err %v, want zero stdout and error", partial.String(), err)
	}
}
