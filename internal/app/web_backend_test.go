package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/web"
)

// webFixtureBackend serves resourceFixtureRegistry with an empty observation:
// no tmux server is reachable, so nothing is live, which is also what a real
// server reports when the app socket is down.
func webFixtureBackend(t *testing.T) (*webBackend, *[]resourcegraph.Transport) {
	t.Helper()
	registry := resourceFixtureRegistry(t)
	var observed []resourcegraph.Transport
	backend := newWebBackend()
	// Settings are read from an empty home, never the developer's own.
	emptyHome := t.TempDir()
	backend.home = func() (string, error) { return emptyHome, nil }
	backend.env = func(string) string { return "" }
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	backend.observe = func(_ context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
		observed = append(observed, transport)
		return resourcegraph.Inventory{}
	}
	return backend, &observed
}

func webGet(t *testing.T, handler http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v\n%s", path, err, rec.Body.String())
	}
	return rec.Code, body
}

func itemUIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("no items in %v", body)
	}
	var uids []string
	for _, raw := range items {
		item := raw.(map[string]any)
		meta := item["metadata"].(map[string]any)
		uids = append(uids, meta["uid"].(string))
		if _, ok := item["context"].(map[string]any); !ok {
			t.Errorf("item %v has no context block", meta["uid"])
		}
	}
	return uids
}

func TestWebBackendListsFollowTheOwnerChain(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()

	cases := []struct {
		path string
		kind string
		want []string
	}{
		{"/api/v1/projects", "ProjectList", []string{"prj-alpha", "prj-beta", "prj-gone"}},
		{"/api/v1/projects/prj-alpha/windows", "WindowList", []string{"win-alpha-main", "win-alpha-review"}},
		// The Agent-owned codex pane is listed under the Agent's Window.
		{"/api/v1/projects/prj-alpha/windows/win-alpha-main/panes", "PaneList", []string{"pan-alpha-zsh", "pan-alpha-log", "pan-alpha-codex"}},
		{"/api/v1/projects/prj-alpha/windows/win-alpha-review/panes", "PaneList", []string{"pan-alpha-review"}},
		{"/api/v1/projects/prj-alpha/windows/win-alpha-main/agents", "AgentList", []string{"agt-alpha-codex"}},
		// Offline agents are listed: they are the Window's resume candidates.
		{"/api/v1/projects/prj-beta/windows/win-beta-main/agents", "AgentList", []string{"agt-beta-codex"}},
	}
	for _, tc := range cases {
		code, body := webGet(t, handler, tc.path)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %v", tc.path, code, body)
		}
		if body["kind"] != tc.kind || body["apiVersion"] != coremetadata.APIVersion {
			t.Errorf("GET %s envelope = %v/%v", tc.path, body["apiVersion"], body["kind"])
		}
		got := itemUIDs(t, body)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("GET %s = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The items are the CLI's own JSON projection, so a web client and a script
// reading `get -o json` see the same object.
func TestWebBackendItemsMatchTheCLIProjection(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()

	code, body := webGet(t, handler, "/api/v1/agents/agt-alpha-codex")
	if code != http.StatusOK {
		t.Fatalf("GET agent = %d %v", code, body)
	}
	registry := resourceFixtureRegistry(t)
	resource, _, _ := resourceFor(registry, coremetadata.KindAgent, "agt-alpha-codex")
	want, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	var wantMap map[string]any
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"apiVersion", "kind", "metadata", "spec"} {
		gotJSON, _ := json.Marshal(body[key])
		wantJSON, _ := json.Marshal(wantMap[key])
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Errorf("%s = %s, want %s", key, gotJSON, wantJSON)
		}
	}
	// The Workspace default comes from the Project root, as `get` fills it.
	spec := body["spec"].(map[string]any)
	if ws, _ := spec["workspace"].(map[string]any); ws == nil || ws["cwd"] != "/srv/alpha" {
		t.Errorf("spec.workspace = %v, want the project root", spec["workspace"])
	}
}

func TestWebBackendRefusesAChildOfAnotherParent(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()
	for _, path := range []string{
		"/api/v1/projects/prj-missing",
		"/api/v1/projects/prj-missing/windows",
		"/api/v1/projects/prj-beta/windows/win-alpha-main",
		"/api/v1/projects/prj-beta/windows/win-alpha-main/panes",
		"/api/v1/projects/prj-alpha/windows/win-alpha-review/panes/pan-alpha-zsh",
		"/api/v1/projects/prj-alpha/windows/win-alpha-main/panes/pan-missing",
		"/api/v1/projects/prj-beta/windows/win-alpha-main/agents",
		"/api/v1/agents/agt-missing",
	} {
		code, body := webGet(t, handler, path)
		envelope, _ := body["error"].(map[string]any)
		if code != http.StatusNotFound || envelope == nil || envelope["code"] != web.CodeNotFound {
			t.Errorf("GET %s = %d %v, want 404 not-found", path, code, body)
		}
	}
}

// The observation always targets the app socket, never an inherited $TMUX.
func TestWebBackendObservesTheAppSocket(t *testing.T) {
	t.Setenv("TMUX", "/tmp/some-other-server,1,0")
	backend, observed := webFixtureBackend(t)
	if _, err := backend.Graph(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*observed) != 1 {
		t.Fatalf("observations = %d, want 1", len(*observed))
	}
	got := (*observed)[0]
	if got.Kind != resourcegraph.TransportSocketName || got.Value != defaultAppSocket {
		t.Fatalf("observed transport = %+v, want -L %s", got, defaultAppSocket)
	}
}

func TestWebBackendRegistryFailureIsInternal(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	backend.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, errors.New("disk on fire") }
	code, body := webGet(t, web.New(backend, nil).Handler(), "/api/v1/projects")
	envelope, _ := body["error"].(map[string]any)
	if code != http.StatusInternalServerError || envelope["code"] != web.CodeInternal {
		t.Fatalf("GET projects with a broken registry = %d %v", code, body)
	}
}

// The TCP URL printed at start carries the start token, which is fresh on
// every start and is the only place the token is written.
func TestWebCommandPrintsTheStartTokenURL(t *testing.T) {
	start := func(args ...string) (web.Options, string) {
		t.Helper()
		var got web.Options
		cmd := newWebCommand()
		cmd.unsetenv = func(string) error { return nil }
		cmd.backend = func() web.Backend { backend, _ := webFixtureBackend(t); return backend }
		cmd.serve = func(_ context.Context, _ web.Backend, opts web.Options) error {
			got = opts
			bound := ""
			if opts.Addr != "" {
				bound = "127.0.0.1:8787"
			}
			opts.Ready(bound)
			return nil
		}
		var stdout, stderr bytes.Buffer
		if err := cmd.Run(args, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr.String(), got.Token) && got.Token != "" {
			t.Errorf("stderr carries the token: %q", stderr.String())
		}
		return got, stdout.String()
	}

	first, out := start("--socket", "-", "-v")
	if want := "projmux web: http://127.0.0.1:8787/?token=" + first.Token + "\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token decodes to %d bytes (%v), want 32", len(raw), err)
	}
	second, _ := start("--socket", "-")
	if second.Token == "" || second.Token == first.Token {
		t.Fatalf("second start token = %q, want a fresh one", second.Token)
	}
	// Without TCP there is nothing to protect with a token.
	socketOnly, out := start("--addr", "", "--socket", "/tmp/pw/api.sock")
	if socketOnly.Token != "" || strings.Contains(out, "token") {
		t.Fatalf("socket-only start: token %q, stdout %q", socketOnly.Token, out)
	}
}

func TestWebCommandRefusesNonLoopbackAndClearsTmuxEnv(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "192.168.0.2:80", ":8787", "example.com:80"} {
		cmd := newWebCommand()
		cmd.serve = func(context.Context, web.Backend, web.Options) error {
			t.Fatalf("served on %s", addr)
			return nil
		}
		err := cmd.Run([]string{"--addr", addr}, &bytes.Buffer{}, &bytes.Buffer{})
		if !IsUsageError(err) {
			t.Errorf("--addr %s: err = %v, want a usage error", addr, err)
		}
	}

	var cleared []string
	var got web.Options
	cmd := newWebCommand()
	cmd.unsetenv = func(name string) error { cleared = append(cleared, name); return nil }
	cmd.backend = func() web.Backend { backend, _ := webFixtureBackend(t); return backend }
	cmd.serve = func(_ context.Context, _ web.Backend, opts web.Options) error {
		got = opts
		return nil
	}
	if err := cmd.Run([]string{"--addr", "[::1]:0", "--socket", "-"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cleared, ",") != "TMUX,TMUX_PANE" {
		t.Errorf("cleared env = %v", cleared)
	}
	if got.Addr != "[::1]:0" || got.SocketPath != "" {
		t.Errorf("options = %+v", got)
	}
	if err := cmd.Run([]string{"--addr", "", "--socket", "-"}, &bytes.Buffer{}, &bytes.Buffer{}); !IsUsageError(err) {
		t.Errorf("both listeners disabled: err = %v, want a usage error", err)
	}
}
