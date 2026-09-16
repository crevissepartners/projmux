// Package web serves the projmux HTTP API and the browser client that uses it.
//
// The package owns transport only: routing, the error envelope, the loopback
// guard, listeners, and the embedded client. Everything a route answers comes
// from a Backend, which internal/app implements over the same code the CLI
// runs. docs/web-api.md is the contract.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Backend is what the HTTP layer needs from projmux. Values it returns are
// JSON-encodable as they are; the HTTP layer adds no fields of its own.
//
// A method that refuses returns *Error. Any other error becomes `internal`.
type Backend interface {
	Version() string

	// Graph is one Registry read joined with one tmux observation.
	Graph(ctx context.Context) (any, error)

	Projects(ctx context.Context) (any, error)
	Project(ctx context.Context, project string) (any, error)
	Windows(ctx context.Context, project string) (any, error)
	Window(ctx context.Context, project, window string) (any, error)
	Panes(ctx context.Context, project, window string) (any, error)
	Pane(ctx context.Context, project, window, pane string) (any, error)
	WindowAgents(ctx context.Context, project, window string) (any, error)
	Agent(ctx context.Context, agent string) (any, error)
}

// Server routes requests to a Backend.
type Server struct {
	backend Backend
	log     *slog.Logger
}

// New builds a Server. A nil logger discards.
func New(backend Backend, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{backend: backend, log: log}
}

// Handler returns every route, without the loopback guard. Serve applies the
// guard to the TCP listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	read := func(pattern string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			body, err := fn(r)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, body)
		})
	}

	read("/api/v1/version", func(*http.Request) (any, error) {
		return map[string]string{"version": s.backend.Version()}, nil
	})
	read("/api/v1/graph", func(r *http.Request) (any, error) {
		return s.backend.Graph(r.Context())
	})
	read("/api/v1/projects", func(r *http.Request) (any, error) {
		return s.backend.Projects(r.Context())
	})
	read("/api/v1/projects/{project}", func(r *http.Request) (any, error) {
		return s.backend.Project(r.Context(), r.PathValue("project"))
	})
	read("/api/v1/projects/{project}/windows", func(r *http.Request) (any, error) {
		return s.backend.Windows(r.Context(), r.PathValue("project"))
	})
	read("/api/v1/projects/{project}/windows/{window}", func(r *http.Request) (any, error) {
		return s.backend.Window(r.Context(), r.PathValue("project"), r.PathValue("window"))
	})
	read("/api/v1/projects/{project}/windows/{window}/panes", func(r *http.Request) (any, error) {
		return s.backend.Panes(r.Context(), r.PathValue("project"), r.PathValue("window"))
	})
	read("/api/v1/projects/{project}/windows/{window}/panes/{pane}", func(r *http.Request) (any, error) {
		return s.backend.Pane(r.Context(), r.PathValue("project"), r.PathValue("window"), r.PathValue("pane"))
	})
	read("/api/v1/projects/{project}/windows/{window}/agents", func(r *http.Request) (any, error) {
		return s.backend.WindowAgents(r.Context(), r.PathValue("project"), r.PathValue("window"))
	})
	read("/api/v1/agents/{agent}", func(r *http.Request) (any, error) {
		return s.backend.Agent(r.Context(), r.PathValue("agent"))
	})

	// Anything else under the API prefix is an API miss, not the client page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, NotFound("no such route"))
	})
	// The page routes take no method in the pattern: "GET /" and "/api/" would
	// conflict, since neither is more specific than the other.
	mux.Handle("/assets/", getOnly(assetHandler()))
	mux.Handle("/", getOnly(http.HandlerFunc(serveIndex)))

	return s.logged(mux)
}

func getOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	e := asError(err)
	level := slog.LevelInfo
	if e.Status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	s.log.Log(r.Context(), level, "request refused", "method", r.Method, "path", r.URL.Path, "code", e.Code, "message", e.Message)
	writeError(w, e)
}

func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "ms", time.Since(start).Milliseconds())
	})
}
