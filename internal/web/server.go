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

	CreateWindow(ctx context.Context, project string, req CreateWindowRequest) (any, error)
	RenameWindow(ctx context.Context, project, window, name string) (any, error)
	DeleteWindow(ctx context.Context, project, window string, dryRun bool) (any, error)
	RenamePane(ctx context.Context, project, window, pane, name string) (any, error)
	DeletePane(ctx context.Context, project, window, pane string, dryRun bool) (any, error)
	FocusPane(ctx context.Context, project, window, pane string) (any, error)
	CreateAgent(ctx context.Context, project, window string, req CreateAgentRequest) (any, error)
	CreatePane(ctx context.Context, project, window string, req CreatePaneRequest) (any, error)
	RenameAgent(ctx context.Context, agent, name string) (any, error)
	ResumeAgent(ctx context.Context, agent string) (any, error)
	Capabilities(ctx context.Context, agent string) (any, error)
	StartTurn(ctx context.Context, agent, text string) (any, error)
	SteerTurn(ctx context.Context, agent, text string) (any, error)
	InterruptTurn(ctx context.Context, agent string) (any, error)
	SendMessage(ctx context.Context, agent string, req MessageRequest) (any, error)

	Notifications(ctx context.Context) (any, error)
	AckNotification(ctx context.Context, id string) (any, error)
	Usage(ctx context.Context) (any, error)
	System(ctx context.Context) (any, error)
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

	read("/api/v1/agents/{agent}/capabilities", func(r *http.Request) (any, error) {
		return s.backend.Capabilities(r.Context(), r.PathValue("agent"))
	})
	read("/api/v1/notifications", func(r *http.Request) (any, error) {
		return s.backend.Notifications(r.Context())
	})
	read("/api/v1/usage", func(r *http.Request) (any, error) {
		return s.backend.Usage(r.Context())
	})
	read("/api/v1/system", func(r *http.Request) (any, error) {
		return s.backend.System(r.Context())
	})

	write := func(pattern string, status int, fn func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			body, err := fn(w, r)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			writeJSON(w, status, body)
		})
	}
	project := func(r *http.Request) string { return r.PathValue("project") }
	window := func(r *http.Request) string { return r.PathValue("window") }
	pane := func(r *http.Request) string { return r.PathValue("pane") }
	agent := func(r *http.Request) string { return r.PathValue("agent") }

	write("POST /api/v1/projects/{project}/windows", http.StatusCreated, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req CreateWindowRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !req.Confirm {
			return nil, confirmRequired("creating a window")
		}
		return s.backend.CreateWindow(r.Context(), project(r), req)
	})
	write("PATCH /api/v1/projects/{project}/windows/{window}", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		name, err := decodeName(w, r)
		if err != nil {
			return nil, err
		}
		return s.backend.RenameWindow(r.Context(), project(r), window(r), name)
	})
	write("DELETE /api/v1/projects/{project}/windows/{window}", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		dryRun := r.URL.Query().Get("dryRun") == "true"
		var req confirmRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !dryRun && !req.Confirm {
			return nil, confirmRequired("deleting a window")
		}
		return s.backend.DeleteWindow(r.Context(), project(r), window(r), dryRun)
	})
	write("PATCH /api/v1/projects/{project}/windows/{window}/panes/{pane}", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		name, err := decodeName(w, r)
		if err != nil {
			return nil, err
		}
		return s.backend.RenamePane(r.Context(), project(r), window(r), pane(r), name)
	})
	write("DELETE /api/v1/projects/{project}/windows/{window}/panes/{pane}", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		dryRun := r.URL.Query().Get("dryRun") == "true"
		var req confirmRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !dryRun && !req.Confirm {
			return nil, confirmRequired("deleting a pane")
		}
		return s.backend.DeletePane(r.Context(), project(r), window(r), pane(r), dryRun)
	})
	write("POST /api/v1/projects/{project}/windows/{window}/panes/{pane}/focus", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := decodeBody(w, r, &struct{}{}); err != nil {
			return nil, err
		}
		return s.backend.FocusPane(r.Context(), project(r), window(r), pane(r))
	})
	write("POST /api/v1/projects/{project}/windows/{window}/agents", http.StatusCreated, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req CreateAgentRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !req.Confirm {
			return nil, confirmRequired("creating an agent")
		}
		return s.backend.CreateAgent(r.Context(), project(r), window(r), req)
	})
	write("POST /api/v1/projects/{project}/windows/{window}/panes", http.StatusCreated, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req CreatePaneRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !req.Confirm {
			return nil, confirmRequired("creating a pane")
		}
		return s.backend.CreatePane(r.Context(), project(r), window(r), req)
	})
	write("PATCH /api/v1/agents/{agent}", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		name, err := decodeName(w, r)
		if err != nil {
			return nil, err
		}
		return s.backend.RenameAgent(r.Context(), agent(r), name)
	})
	write("POST /api/v1/agents/{agent}/resume", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req confirmRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		if !req.Confirm {
			return nil, confirmRequired("resuming an agent")
		}
		return s.backend.ResumeAgent(r.Context(), agent(r))
	})
	write("POST /api/v1/agents/{agent}/turns", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		text, err := decodeText(w, r)
		if err != nil {
			return nil, err
		}
		return s.backend.StartTurn(r.Context(), agent(r), text)
	})
	write("POST /api/v1/agents/{agent}/turns/current/steer", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		text, err := decodeText(w, r)
		if err != nil {
			return nil, err
		}
		return s.backend.SteerTurn(r.Context(), agent(r), text)
	})
	write("DELETE /api/v1/agents/{agent}/turns/current", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := decodeBody(w, r, &struct{}{}); err != nil {
			return nil, err
		}
		return s.backend.InterruptTurn(r.Context(), agent(r))
	})
	write("POST /api/v1/agents/{agent}/messages", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req MessageRequest
		if err := decodeBody(w, r, &req); err != nil {
			return nil, err
		}
		return s.backend.SendMessage(r.Context(), agent(r), req)
	})
	write("POST /api/v1/notifications/{id}/ack", http.StatusOK, func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := decodeBody(w, r, &struct{}{}); err != nil {
			return nil, err
		}
		return s.backend.AckNotification(r.Context(), r.PathValue("id"))
	})

	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.registerClientRoutes(mux)

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
