package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBackend answers every read from fixed values and records what was asked.
type fakeBackend struct {
	// Backend is nil: a route this fake does not implement panics, which a
	// test notices.
	Backend
	calls []string
}

func (f *fakeBackend) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeBackend) Version() string { return "test" }

func (f *fakeBackend) Graph(context.Context) (any, error) {
	f.record("graph")
	return map[string]any{"projects": []any{}}, nil
}

func (f *fakeBackend) Projects(context.Context) (any, error) {
	f.record("projects")
	return map[string]any{"kind": "ProjectList", "items": []any{}}, nil
}

func (f *fakeBackend) Project(_ context.Context, project string) (any, error) {
	f.record("project " + project)
	if project != "proj-p" {
		return nil, NotFound("no project " + project)
	}
	return map[string]any{"kind": "Project"}, nil
}

func (f *fakeBackend) Windows(_ context.Context, project string) (any, error) {
	f.record("windows " + project)
	return map[string]any{"kind": "WindowList"}, nil
}

func (f *fakeBackend) Window(_ context.Context, project, window string) (any, error) {
	f.record("window " + project + " " + window)
	return map[string]any{"kind": "Window"}, nil
}

func (f *fakeBackend) Panes(_ context.Context, project, window string) (any, error) {
	f.record("panes " + project + " " + window)
	return map[string]any{"kind": "PaneList"}, nil
}

func (f *fakeBackend) Pane(_ context.Context, project, window, pane string) (any, error) {
	f.record("pane " + project + " " + window + " " + pane)
	return map[string]any{"kind": "Pane"}, nil
}

func (f *fakeBackend) WindowAgents(_ context.Context, project, window string) (any, error) {
	f.record("agents " + project + " " + window)
	return map[string]any{"kind": "AgentList"}, nil
}

func (f *fakeBackend) Agent(_ context.Context, agent string) (any, error) {
	f.record("agent " + agent)
	return nil, errBoom
}

var errBoom = io.ErrUnexpectedEOF

func decode(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", res.Request.URL, err)
	}
	return body
}

func TestRoutesReachTheBackendWithPathValues(t *testing.T) {
	backend := &fakeBackend{}
	srv := httptest.NewServer(New(backend, nil).Handler())
	defer srv.Close()

	for _, path := range []string{
		"/api/v1/graph",
		"/api/v1/projects",
		"/api/v1/projects/proj-p",
		"/api/v1/projects/proj-p/windows",
		"/api/v1/projects/proj-p/windows/win-w",
		"/api/v1/projects/proj-p/windows/win-w/panes",
		"/api/v1/projects/proj-p/windows/win-w/panes/pane-x",
		"/api/v1/projects/proj-p/windows/win-w/agents",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, res.StatusCode)
		}
	}
	want := []string{
		"graph", "projects", "project proj-p", "windows proj-p", "window proj-p win-w",
		"panes proj-p win-w", "pane proj-p win-w pane-x", "agents proj-p win-w",
	}
	if strings.Join(backend.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("backend calls = %q\nwant %q", backend.calls, want)
	}
}

func TestErrorsUseOneEnvelope(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, nil).Handler())
	defer srv.Close()

	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/projects/proj-missing", 404, CodeNotFound}, // a backend refusal keeps its code
		{"/api/v1/agents/agent-x", 500, CodeInternal},        // an unclassified error is internal
		{"/api/v1/nope", 404, CodeNotFound},                  // an unknown API path is an API miss
		{"/api/v2/projects", 404, CodeNotFound},
	}
	for _, tc := range cases {
		res, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status {
			t.Errorf("GET %s = %d, want %d", tc.path, res.StatusCode, tc.status)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("GET %s content type %q", tc.path, ct)
		}
		body := decode(t, res)
		envelope, _ := body["error"].(map[string]any)
		if envelope == nil || envelope["code"] != tc.code || envelope["status"] != float64(tc.status) || envelope["message"] == "" {
			t.Errorf("GET %s body = %v, want code %s", tc.path, body, tc.code)
		}
	}
}

func TestClientPathsServeTheClientAndNothingElse(t *testing.T) {
	srv := httptest.NewServer(New(&fakeBackend{}, nil).Handler())
	defer srv.Close()
	for path, want := range map[string]int{
		"/":                            200,
		"/project/a":                   200,
		"/project/a/window/b":          200,
		"/project/a/window/b/pane/c":   200,
		"/project/a/window/b/agent/c":  200,
		"/project/a/window/b/other/c":  404,
		"/a/agent-x":                   200,
		"/a":                           404,
		"/b/agent-x":                   404,
		"/project":                     404,
		"/project/a/window":            404,
		"/project/a/pane/c":            404,
		"/nope":                        404,
		"/project/a/window/b/pane/c/x": 404,
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, res.StatusCode, want)
		}
		if want == 200 && !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET %s content type %q", path, res.Header.Get("Content-Type"))
		}
	}
}

// TestLoopbackGuardKeepsOtherSitesOut pins the two attacks that were real
// against the prototype: a cross-site "simple" POST that runs without a
// preflight, and a DNS-rebound page whose requests only the Host header gives
// away.
func TestLoopbackGuardKeepsOtherSitesOut(t *testing.T) {
	backend := &fakeBackend{}
	srv := httptest.NewUnstartedServer(nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	srv.Config.Handler = guardLoopback(port, New(backend, nil).Handler())
	srv.Start()
	defer srv.Close()
	self := "127.0.0.1:" + port

	send := func(method, path string, headers map[string]string, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			if k == "Host" {
				req.Host = v
				continue
			}
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	refused := []struct {
		name    string
		method  string
		headers map[string]string
	}{
		{"rebound host", "GET", map[string]string{"Host": "attacker.example:" + port}},
		{"loopback host on another port", "GET", map[string]string{"Host": "127.0.0.1:1"}},
		{"cross-site origin", "GET", map[string]string{"Origin": "http://attacker.example"}},
		{"another local origin", "GET", map[string]string{"Origin": "http://127.0.0.1:1"}},
		{"https origin", "GET", map[string]string{"Origin": "https://" + self}},
		{"simple post", "POST", map[string]string{"Content-Type": "text/plain"}},
		{"form post", "POST", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}},
		{"post without a type", "POST", nil},
		{"delete with a text body", "DELETE", map[string]string{"Content-Type": "text/plain"}},
		{"json post from another site", "POST", map[string]string{"Content-Type": "application/json", "Origin": "http://attacker.example"}},
	}
	for _, tc := range refused {
		if got := send(tc.method, "/api/v1/projects", tc.headers, `{}`); got != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tc.name, got)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("a refused request reached the backend: %v", backend.calls)
	}

	allowed := []map[string]string{
		nil,
		{"Origin": "http://" + self},
		{"Host": "localhost:" + port},
	}
	for _, headers := range allowed {
		if got := send("GET", "/api/v1/projects", headers, ""); got != http.StatusOK {
			t.Errorf("GET with %v: status %d, want 200", headers, got)
		}
	}
	// A bodiless DELETE needs no type: a cross-site page cannot send one
	// without a preflight.
	if got := sendEmpty(t, srv.URL, "DELETE", "/api/v1/projects"); got == http.StatusForbidden {
		t.Errorf("bodiless DELETE was refused by the guard")
	}
	if got := sendEmpty(t, srv.URL, "POST", "/api/v1/projects"); got != http.StatusForbidden {
		t.Errorf("bodiless untyped POST = %d, want 403", got)
	}
	// A same-origin JSON write passes the guard; there is no POST route here
	// yet, so the mux answers, not the guard.
	if got := send("POST", "/api/v1/projects", map[string]string{"Content-Type": "application/json; charset=utf-8", "Origin": "http://" + self}, `{}`); got == http.StatusForbidden {
		t.Errorf("same-origin JSON POST was refused by the guard")
	}
}

func TestServeListensOnTCPAndSocket(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "web", "api.sock")
	if len(socket) > 100 {
		t.Skipf("temp socket path too long for the private endpoint bound: %s", socket)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, &fakeBackend{}, Options{
			Addr:       "127.0.0.1:0",
			SocketPath: socket,
			Token:      "start-token",
			Ready:      func(addr string) { ready <- addr },
		})
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Serve returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never became ready")
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/v1/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer start-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if decode(t, res)["version"] != "test" {
		t.Fatal("TCP listener did not answer the version route")
	}

	unixClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	// The socket skips the Host check: a local program names any host.
	res, err = unixClient.Get("http://projmux/api/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	if decode(t, res)["version"] != "test" {
		t.Fatal("unix listener did not answer the version route")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func sendEmpty(t *testing.T, base, method, path string) int {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
