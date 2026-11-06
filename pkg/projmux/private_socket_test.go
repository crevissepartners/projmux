package projmux_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/testutil/socketreuse"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

var _ net.Listener = (*projmux.PrivateSocketListener)(nil)

// socketRoot is a short directory of its own under /tmp, so the socket path
// stays inside localipc.MaxSocketPath. Only that exact directory is removed.
func socketRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pmx-psl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func skipWithoutUnixSockets(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets unavailable")
	}
}

type socketScenario struct {
	name string
	// prepare builds the state under root and returns the path to listen on.
	prepare func(t *testing.T, root string) string
}

func ownedStaleSocket(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		skipWithoutUnixSockets(t, err)
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = stale.Close()
}

func socketScenarios() []socketScenario {
	return []socketScenario{
		{"new path", func(t *testing.T, root string) string { return filepath.Join(root, "private", "new.sock") }},
		{"live socket", func(t *testing.T, root string) string {
			path := filepath.Join(root, "private", "live.sock")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			live, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				skipWithoutUnixSockets(t, err)
				t.Fatal(err)
			}
			live.SetUnlinkOnClose(false)
			t.Cleanup(func() { _ = live.Close() })
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"regular file", func(t *testing.T, root string) string {
			path := filepath.Join(root, "private", "file.sock")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"symlink", func(t *testing.T, root string) string {
			path := filepath.Join(root, "private", "link.sock")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "elsewhere"), path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"owned dead socket", func(t *testing.T, root string) string {
			path := filepath.Join(root, "private", "stale.sock")
			ownedStaleSocket(t, path)
			return path
		}},
		{"relative path", func(t *testing.T, root string) string { return "private/relative.sock" }},
		{"empty path", func(t *testing.T, root string) string { return "" }},
		{"not clean", func(t *testing.T, root string) string { return root + "/private/../unclean.sock" }},
		{"over the path bound", func(t *testing.T, root string) string {
			return filepath.Join(root, strings.Repeat("a", localipc.MaxSocketPath))
		}},
		{"at the path bound", func(t *testing.T, root string) string {
			name := strings.Repeat("b", localipc.MaxSocketPath-len(root)-1)
			return filepath.Join(root, name)
		}},
	}
}

// outcome is what a caller can observe of one listen attempt.
type socketOutcome struct {
	ok       bool
	message  string
	exists   bool
	mode     os.FileMode
	dirMode  os.FileMode
	dialable bool
}

func observeSocket(path, root string, err error) socketOutcome {
	out := socketOutcome{ok: err == nil}
	if err != nil {
		out.message = strings.ReplaceAll(err.Error(), root, "<root>")
	}
	if filepath.IsAbs(path) && filepath.Clean(path) == path {
		if info, statErr := os.Lstat(path); statErr == nil {
			out.exists = true
			out.mode = info.Mode()
		}
		if info, statErr := os.Stat(filepath.Dir(path)); statErr == nil {
			out.dirMode = info.Mode().Perm()
		}
		if conn, dialErr := net.DialTimeout("unix", path, time.Second); dialErr == nil {
			out.dialable = true
			_ = conn.Close()
		}
	}
	return out
}

// ListenPrivateSocket must say the same thing as the listen the projmux
// commands use, in every state a path can be in: accept or refuse, the exact
// error text, and what is left on disk afterwards.
func TestListenPrivateSocketEqualsInternal(t *testing.T) {
	for _, scenario := range socketScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			internalRoot, publicRoot := socketRoot(t), socketRoot(t)
			internalPath := scenario.prepare(t, internalRoot)
			publicPath := scenario.prepare(t, publicRoot)
			internal, internalErr := localipc.Listen(internalPath)
			if internal != nil {
				defer internal.Close()
			}
			internalOut := observeSocket(internalPath, internalRoot, internalErr)
			public, publicErr := projmux.ListenPrivateSocket(publicPath)
			if public != nil {
				defer public.Close()
			}
			publicOut := observeSocket(publicPath, publicRoot, publicErr)
			if internalOut != publicOut {
				t.Fatalf("public %+v, internal %+v", publicOut, internalOut)
			}
			if (public == nil) != (publicErr != nil) {
				t.Fatalf("listener %v with error %v", public, publicErr)
			}
		})
	}
}

func TestListenPrivateSocketModesAndTraffic(t *testing.T) {
	root := socketRoot(t)
	path := filepath.Join(root, "private", "traffic.sock")
	listener, err := projmux.ListenPrivateSocket(path)
	skipWithoutUnixSockets(t, err)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if listener.Path() != path || listener.Addr() == nil || listener.Addr().String() != path {
		t.Fatalf("Path %q Addr %v, want %q", listener.Path(), listener.Addr(), path)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory info=%v err=%v", info, err)
	}
	accepted := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- "accept: " + err.Error()
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		accepted <- string(data)
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case got := <-accepted:
		if got != "hello" {
			t.Fatalf("accepted %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted")
	}
}

func TestListenPrivateSocketServesHTTP(t *testing.T) {
	root := socketRoot(t)
	path := filepath.Join(root, "private", "http.sock")
	listener, err := projmux.ListenPrivateSocket(path)
	skipWithoutUnixSockets(t, err)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	}()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	response, err := client.Get("http://projmux/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("http.Serve did not return after Close")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed socket remains: %v", err)
	}
}

func TestListenPrivateSocketCloseKeepsReplacementAndIsRepeatable(t *testing.T) {
	root := socketRoot(t)
	path := filepath.Join(root, "private", "close.sock")
	listener, err := projmux.ListenPrivateSocket(path)
	skipWithoutUnixSockets(t, err)
	if err != nil {
		t.Fatal(err)
	}
	// Measured once sockets are known to be available, in the same /tmp.
	socketreuse.SkipIfReplacementCollapses(t, "/tmp", localipc.InspectOwnedSocket)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := localipc.InspectOwnedSocket(path); err != nil {
		t.Fatalf("replacement was removed: %v", err)
	}
	second := listener.Close() // a second Close must not panic or touch the replacement
	if _, err := localipc.InspectOwnedSocket(path); err != nil {
		t.Fatalf("second Close removed the replacement (err=%v): %v", second, err)
	}
	var none *projmux.PrivateSocketListener
	if none.Close() != nil || none.Path() != "" || none.Addr() != nil {
		t.Fatal("nil listener is not inert")
	}
	if _, err := none.Accept(); err == nil {
		t.Fatal("nil listener accepted")
	}
}

func TestListenPrivateSocketCloseRemovesItsOwnSocketTwice(t *testing.T) {
	root := socketRoot(t)
	path := filepath.Join(root, "private", "own.sock")
	listener, err := projmux.ListenPrivateSocket(path)
	skipWithoutUnixSockets(t, err)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed socket remains: %v", err)
	}
	_ = listener.Close()
}
