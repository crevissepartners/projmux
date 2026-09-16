package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Images pasted or dropped into the composer. A provider reads an image from
// a file, so the client uploads it here and appends the stored path to the
// message it sends. Only the four raster formats the providers read are
// kept, recognized by their bytes rather than by what the request claims.
const (
	uploadPath  = "/api/v1/web/uploads"
	uploadLimit = 10 << 20
	uploadKeep  = 7 * 24 * time.Hour
)

var uploadExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

var uploadName = regexp.MustCompile(`^[0-9a-f]{64}\.(png|jpg|webp|gif)$`)

// Uploader is a backend that keeps uploaded images.
type Uploader interface {
	// UploadDir is the directory uploads are stored in. It is created with
	// mode 0700 when missing.
	UploadDir() (string, error)
}

// StoredUpload is where an upload was kept.
type StoredUpload struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Bytes int    `json:"bytes"`
}

// uploadContentType reports whether a request is an upload with an image
// type. Like JSON, none of these types can be sent cross-site without a
// preflight, which this server never answers.
func uploadContentType(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != uploadPath {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	_, ok := uploadExtensions[mediaType]
	return err == nil && ok
}

// sniffImage names the format data is in, from its leading bytes.
func sniffImage(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	}
	return "", false
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	uploader, ok := s.backend.(Uploader)
	if !ok {
		s.fail(w, r, NewError(http.StatusNotImplemented, CodeUnsupported, "this server does not keep uploads"))
		return
	}
	if !uploadContentType(r) {
		s.fail(w, r, InvalidRequest("an upload must be image/png, image/jpeg, image/webp, or image/gif"))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, uploadLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(w, r, NewError(http.StatusRequestEntityTooLarge, CodeInvalidRequest, "an image may be at most 10 MiB"))
			return
		}
		s.fail(w, r, InvalidRequest("read upload: "+err.Error()))
		return
	}
	dir, err := uploader.UploadDir()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stored, err := storeUpload(dir, data, time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// serveUpload returns a stored image, so the conversation can show what was
// sent. Only names this store writes are served, from its own directory.
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	uploader, ok := s.backend.(Uploader)
	if !ok {
		s.fail(w, r, NewError(http.StatusNotImplemented, CodeUnsupported, "this server does not keep uploads"))
		return
	}
	name := r.PathValue("name")
	if !uploadName.MatchString(name) {
		s.fail(w, r, NotFound("no upload "+name))
		return
	}
	dir, err := uploader.UploadDir()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The name is checked above; the root keeps the read inside the store
	// regardless.
	root, err := os.OpenRoot(dir)
	if err != nil {
		s.fail(w, r, NotFound("no upload "+name))
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		s.fail(w, r, NotFound("no upload "+name))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		s.fail(w, r, NotFound("no upload "+name))
		return
	}
	for kind, ext := range uploadExtensions {
		if filepath.Ext(name) == ext {
			w.Header().Set("Content-Type", kind)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The name is the content hash, so the bytes behind it never change.
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

// storeUpload keeps data under its content hash, so the same image pasted
// twice is one file, and drops uploads older than the retention period.
func storeUpload(dir string, data []byte, now time.Time) (StoredUpload, error) {
	kind, ok := sniffImage(data)
	if !ok {
		return StoredUpload{}, InvalidRequest("the upload is not a PNG, JPEG, WebP, or GIF image")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return StoredUpload{}, err
	}
	pruneUploads(dir, now)
	sum := sha256.Sum256(data)
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+uploadExtensions[kind])
	stored := StoredUpload{Path: path, Type: kind, Bytes: len(data)}
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		// Pasting it again restarts its retention.
		return stored, os.Chtimes(path, now, now)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return StoredUpload{}, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return StoredUpload{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return StoredUpload{}, err
	}
	if err := tmp.Close(); err != nil {
		return StoredUpload{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return StoredUpload{}, err
	}
	return stored, nil
}

// pruneUploads removes uploads past retention. It touches only names this
// store writes, so nothing else placed in the directory is lost.
func pruneUploads(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !uploadName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > uploadKeep {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
