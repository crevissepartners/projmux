package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist is the built client. It is generated from _ui and committed, so a Go
// build never needs Node; `make web-check` fails when the two disagree.
//
//go:embed all:dist
var dist embed.FS

func distFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return sub
}

func assetHandler() http.Handler {
	files := http.FileServer(http.FS(distFS()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Built asset names are content-addressed, so they never change in
		// place and can be cached for as long as the browser likes.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

// serveIndex renders the client for every path the client routes, and 404s
// the rest so a typo does not look like an empty view.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if !isClientPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	page, err := fs.ReadFile(distFS(), "index.html")
	if err != nil {
		http.Error(w, "the web client is not built into this binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// isClientPath accepts /, /project/x, /project/x/window/y, and
// /project/x/window/y/agent/z or .../pane/z: an agent's slot is addressed by
// its agent, which survives a resume, and a shell's by its pane. /a/z is the
// short address of an agent, which the client expands. The uids themselves
// are not checked here; the client asks the API, which reports a miss.
func isClientPath(path string) bool {
	if path == "/" {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch len(parts) {
	case 2:
		return parts[0] == "project" || parts[0] == "a"
	case 4:
		return parts[0] == "project" && parts[2] == "window"
	case 6:
		return parts[0] == "project" && parts[2] == "window" && (parts[4] == "pane" || parts[4] == "agent")
	}
	return false
}
