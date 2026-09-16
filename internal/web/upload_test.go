package web

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type uploadBackend struct {
	fakeBackend
	dir string
}

func (b *uploadBackend) UploadDir() (string, error) { return b.dir, nil }

func TestStoreUploadKeepsImagesByContentAndPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "web-uploads")
	now := time.Now()
	first, err := storeUpload(dir, pngBytes, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != "image/png" || filepath.Dir(first.Path) != dir || !strings.HasSuffix(first.Path, ".png") {
		t.Fatalf("stored = %+v", first)
	}
	info, err := os.Stat(first.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}
	if dirInfo, _ := os.Stat(dir); dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", dirInfo.Mode())
	}
	again, err := storeUpload(dir, pngBytes, now)
	if err != nil || again.Path != first.Path {
		t.Fatalf("same image stored twice: %+v %v", again, err)
	}
	for name, data := range map[string][]byte{
		"text":    []byte("hello"),
		"svg":     []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"partial": []byte("\x89PN"),
	} {
		if _, err := storeUpload(dir, data, now); AsError(err).Code != CodeInvalidRequest {
			t.Errorf("%s: err = %v, want invalid-request", name, err)
		}
	}
	for data, want := range map[string]string{
		"\xff\xd8\xff\xe0":         "image/jpeg",
		"GIF89a..":                 "image/gif",
		"RIFF\x00\x00\x00\x00WEBP": "image/webp",
	} {
		if got, ok := sniffImage([]byte(data)); !ok || got != want {
			t.Errorf("sniff %q = %q %v, want %s", data, got, ok, want)
		}
	}
}

func TestStoreUploadDropsOnlyItsOwnExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-uploadKeep - time.Hour)
	expired := filepath.Join(dir, strings.Repeat("a", 64)+".png")
	foreign := filepath.Join(dir, "notes.png")
	for _, path := range []string{expired, foreign} {
		if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storeUpload(dir, pngBytes, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Errorf("expired upload kept: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file the store did not write was removed: %v", err)
	}
}

// TestUploadRouteTakesImagesOnlyFromThisOrigin pins the one exception to
// the JSON rule: an image type is accepted on the upload route alone, and
// still only from this server's own origin.
func TestUploadRouteTakesImagesOnlyFromThisOrigin(t *testing.T) {
	backend := &uploadBackend{dir: filepath.Join(t.TempDir(), "up")}
	srv := httptest.NewUnstartedServer(nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	srv.Config.Handler = guardLoopback(port, New(backend, nil).Handler())
	srv.Start()
	defer srv.Close()

	send := func(path, contentType, origin string, body []byte) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	code, body := send(uploadPath, "image/png", "http://127.0.0.1:"+port, pngBytes)
	if code != http.StatusCreated || !strings.HasPrefix(body["path"].(string), backend.dir) {
		t.Fatalf("upload = %d %v", code, body)
	}
	res, err := http.Get(srv.URL + uploadPath + "/" + filepath.Base(body["path"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	served := new(bytes.Buffer)
	_, _ = served.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" || !bytes.Equal(served.Bytes(), pngBytes) {
		t.Fatalf("GET upload = %d %q %q", res.StatusCode, res.Header.Get("Content-Type"), served.Bytes())
	}
	for _, name := range []string{"..%2Fsecret.png", "notes.png", strings.Repeat("b", 64) + ".png"} {
		res, err := http.Get(srv.URL + uploadPath + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET upload %s = %d, want 404", name, res.StatusCode)
		}
	}

	refused := []struct {
		name, path, contentType, origin string
		body                            []byte
		want                            int
	}{
		{"another site", uploadPath, "image/png", "http://attacker.example", pngBytes, http.StatusForbidden},
		{"simple type", uploadPath, "text/plain", "", pngBytes, http.StatusForbidden},
		{"multipart", uploadPath, "multipart/form-data; boundary=x", "", pngBytes, http.StatusForbidden},
		{"image type elsewhere", "/api/v1/projects", "image/png", "", pngBytes, http.StatusForbidden},
		{"not an image", uploadPath, "image/png", "", []byte("hello"), http.StatusBadRequest},
		{"too large", uploadPath, "image/png", "", append(append([]byte{}, pngBytes...), make([]byte, uploadLimit)...), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range refused {
		if got, body := send(tc.path, tc.contentType, tc.origin, tc.body); got != tc.want {
			t.Errorf("%s: %d %v, want %d", tc.name, got, body, tc.want)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("a refused upload reached the core backend: %v", backend.calls)
	}
}
