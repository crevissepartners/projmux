package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log sink the server writes to from its own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type servedListeners struct {
	base   string // http://127.0.0.1:<port>
	port   string
	token  string
	unix   *http.Client
	noJump *http.Client // a TCP client that does not follow redirects
}

// serveWithToken runs Serve on a free loopback port and a short unix socket
// path (the 108-byte sun_path bound rules out t.TempDir), and stops it when
// the test ends.
func serveWithToken(t *testing.T, backend Backend, log *slog.Logger) servedListeners {
	t.Helper()
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "web", "api.sock")

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, backend, Options{
			Addr:       "127.0.0.1:0",
			SocketPath: socket,
			Token:      token,
			Log:        log,
			Ready:      func(addr string) { ready <- addr },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("Serve returned before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never became ready")
	}
	_, port, _ := net.SplitHostPort(addr)
	return servedListeners{
		base:  "http://" + addr,
		port:  port,
		token: token,
		unix: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}},
		noJump: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func (l servedListeners) get(t *testing.T, client *http.Client, url string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (l servedListeners) cookie(value string) map[string]string {
	return map[string]string{"Cookie": tokenCookieName(l.port) + "=" + value}
}

func TestTokenTCPRefusesARequestWithoutTheStartToken(t *testing.T) {
	l := serveWithToken(t, &fakeBackend{}, nil)
	for _, path := range []string{"/api/v1/version", "/api/v1/projects", "/", "/assets/app.js", "/api/v1/events", "/api/v1/web/transcripts/events"} {
		res := l.get(t, l.noJump, l.base+path, nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, res.StatusCode)
			continue
		}
		envelope, _ := decode(t, res)["error"].(map[string]any)
		if envelope == nil || envelope["code"] != CodeUnauthorized {
			t.Errorf("GET %s envelope = %v, want %s", path, envelope, CodeUnauthorized)
			continue
		}
		if strings.Contains(envelope["message"].(string), l.token) {
			t.Errorf("GET %s: the refusal echoes the token", path)
		}
	}
}

func TestTokenTCPAcceptsTheCookieAndTheBearerHeader(t *testing.T) {
	l := serveWithToken(t, &fakeBackend{}, nil)
	for name, header := range map[string]map[string]string{
		"cookie": l.cookie(l.token),
		"bearer": {"Authorization": "Bearer " + l.token},
	} {
		res := l.get(t, l.noJump, l.base+"/api/v1/version", header)
		if res.StatusCode != http.StatusOK || decode(t, res)["version"] != "test" {
			t.Errorf("%s: GET version = %d, want 200", name, res.StatusCode)
		}
	}
}

func TestTokenTCPRefusesAWrongToken(t *testing.T) {
	l := serveWithToken(t, &fakeBackend{}, nil)
	wrong, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string]map[string]string{
		"cookie":                 l.cookie(wrong),
		"bearer":                 {"Authorization": "Bearer " + wrong},
		"token prefix":           {"Authorization": "Bearer " + l.token[:len(l.token)-1]},
		"cookie of another port": {"Cookie": tokenCookieName("1") + "=" + l.token},
		"not a bearer scheme":    {"Authorization": "Basic " + l.token},
	} {
		if res := l.get(t, l.noJump, l.base+"/api/v1/version", header); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: GET version = %d, want 401", name, res.StatusCode)
		}
	}
	if res := l.get(t, l.noJump, l.base+"/?token="+wrong, nil); res.StatusCode != http.StatusUnauthorized || res.Header.Get("Set-Cookie") != "" {
		t.Errorf("a wrong ?token = %d with Set-Cookie %q, want 401 and no cookie", res.StatusCode, res.Header.Get("Set-Cookie"))
	}
}

func TestTokenEventStreamNeedsTheToken(t *testing.T) {
	backend := &eventBackend{system: map[string]int{"cpu": 1}, signals: make(chan struct{}, 1)}
	l := serveWithToken(t, backend, nil)
	url := l.base + "/api/v1/events?topics=system"
	if res := l.get(t, l.noJump, url, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events without a token = %d, want 401", res.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Registered after serveWithToken's cleanup, so it runs first: the stream
	// is gone before the server shuts down.
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Cookie", tokenCookieName(l.port)+"="+l.token)
	res, err := l.noJump.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events with the token = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	frames := make(chan sseFrame, 4)
	go readFrames(t, bufio.NewScanner(res.Body), frames)
	if got := nextFrame(t, frames); got.event != TopicSystem {
		t.Fatalf("first frame = %+v", got)
	}
}

func TestTokenQuerySetsTheCookieAndRedirectsWithoutIt(t *testing.T) {
	log := &syncBuffer{}
	l := serveWithToken(t, &fakeBackend{}, slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})))

	res := l.get(t, l.noJump, l.base+"/?token="+l.token, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET ?token = %d, want 303", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v, want one", cookies)
	}
	c := cookies[0]
	if c.Name != tokenCookieName(l.port) || c.Value != l.token || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly SameSite=Strict Path=/ without Secure", c)
	}
	raw := res.Header.Get("Set-Cookie")
	if !strings.Contains(raw, "HttpOnly") || !strings.Contains(raw, "SameSite=Strict") {
		t.Errorf("Set-Cookie = %q", raw)
	}

	// The cookie the redirect set is what the page's requests carry.
	if res := l.get(t, l.noJump, l.base+"/", l.cookie(c.Value)); res.StatusCode != http.StatusOK {
		t.Errorf("the redirect target with the cookie = %d, want 200", res.StatusCode)
	}
	// Refused and served requests are both logged, by path only.
	l.get(t, l.noJump, l.base+"/api/v1/nope?token=x", nil)
	l.get(t, l.noJump, l.base+"/api/v1/version", map[string]string{"Authorization": "Bearer " + l.token})
	logged := log.String()
	if !strings.Contains(logged, "path=/api/v1/version") || !strings.Contains(logged, "code=unauthorized") {
		t.Fatalf("request log lacks the requests:\n%s", logged)
	}
	if strings.Contains(logged, l.token) || strings.Contains(logged, "token=") {
		t.Fatalf("request log carries a token:\n%s", logged)
	}
}

// The redirect target is always `/`, never built from the request, so a
// protocol-relative path cannot send the browser off-site.
func TestTokenQueryRedirectsOnlyToTheRoot(t *testing.T) {
	l := serveWithToken(t, &fakeBackend{}, nil)
	for _, path := range []string{
		"//evil.example/?token=" + l.token,
		"/some/path?token=" + l.token + "&x=1",
	} {
		res := l.get(t, l.noJump, l.base+path, nil)
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want 303", strings.Replace(path, l.token, "<token>", 1), res.StatusCode)
			continue
		}
		if got := res.Header.Get("Location"); got != "/" {
			t.Errorf("GET %s: Location = %q, want exactly /", strings.Replace(path, l.token, "<token>", 1), got)
		}
	}
}

// The socket's file mode is its access control; it asks for no token.
func TestTokenUnixSocketNeedsNoToken(t *testing.T) {
	l := serveWithToken(t, &fakeBackend{}, nil)
	res := l.get(t, l.unix, "http://projmux/api/v1/version", nil)
	if res.StatusCode != http.StatusOK || decode(t, res)["version"] != "test" {
		t.Fatalf("unix GET version without a token = %d, want 200", res.StatusCode)
	}
}

func TestTokenIsThirtyTwoRandomBytes(t *testing.T) {
	first, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) != 32 {
			t.Fatalf("token %d chars decodes to %d bytes (%v), want 32", len(token), len(raw), err)
		}
	}
	if first == second {
		t.Fatal("two tokens are equal")
	}
}

func TestServeRefusesTCPWithoutAToken(t *testing.T) {
	err := Serve(t.Context(), &fakeBackend{}, Options{Addr: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Serve without a token = %v, want a refusal", err)
	}
}
