package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// The routes under /api/v1/web exist for the browser client. They show the
// terminal and the provider transcripts rather than the Registry, so they are
// kept apart from the core surface, behind their own interface.

// ClientBackend is what the browser-only routes need. A server whose backend
// does not implement it answers those routes with `unsupported`.
type ClientBackend interface {
	// Messages returns the `web.*` catalog for the resolved locale.
	Messages(ctx context.Context) (any, error)
	// Transcript returns an agent's input surface, its repository for linking,
	// and a bounded tail of its conversation.
	Transcript(ctx context.Context, agent string, limit int) (any, error)
	// FollowTranscript starts reading the agent's transcript at a byte
	// offset: the `offset` a Transcript read returned, or the offset a
	// reconnecting stream's last frame id recorded. A negative offset means
	// the current end of the file.
	FollowTranscript(ctx context.Context, agent string, offset int64) (Follower, error)
	// Layout captures a window's panes where tmux put them.
	Layout(ctx context.Context, window string, contents bool) (any, error)
	// Screen captures one pane's visible grid.
	Screen(ctx context.Context, pane string) (any, error)
	// ResumeCandidates lists a window's Offline agents with a preview each.
	ResumeCandidates(ctx context.Context, window string) (any, error)
	// PreviewAgent renders the exact command a create-agent request would run.
	PreviewAgent(ctx context.Context, project, window string, req CreateAgentRequest) (any, error)
	// LaunchOptions lists what the launcher offers: enabled providers, whether
	// each is installed, the default split mode, and Claude's model choices.
	LaunchOptions(ctx context.Context) (any, error)
	// Settings reads the settings a web page consumes; UpdateSetting changes
	// one through the same function the TUI Settings uses.
	Settings(ctx context.Context) (any, error)
	UpdateSetting(ctx context.Context, req SettingRequest) (any, error)
	// Statusbar reports which status bar parts Settings turned on.
	Statusbar(ctx context.Context) (any, error)
	// PaneGit reads the git branch and state of a pane's directory.
	PaneGit(ctx context.Context, pane string) (any, error)
	// AnswerQuestion answers the agent's pending AskUserQuestion.
	AnswerQuestion(ctx context.Context, agent string, req QuestionAnswer) (any, error)
}

// QuestionAnswer is the body of POST /web/agents/{agent}/question. It carries
// only which options were picked; the question itself is read from the
// agent's transcript, and ToolID must name the question still pending.
type QuestionAnswer struct {
	ToolID  string `json:"toolId"`
	Answers []struct {
		Picks []int  `json:"picks"`
		Other string `json:"other"`
	} `json:"answers"`
}

// Follower yields what was appended since the previous call, and where it
// got to.
type Follower interface {
	Next() ([]any, error)
	Offset() int64
}

// Poll intervals. Variables only so a test can shorten them.
var (
	transcriptPoll = 400 * time.Millisecond
	layoutPoll     = 1500 * time.Millisecond
	screenPoll     = 700 * time.Millisecond
)

func (s *Server) client(w http.ResponseWriter, r *http.Request) (ClientBackend, bool) {
	backend, ok := s.backend.(ClientBackend)
	if !ok {
		s.fail(w, r, NewError(http.StatusNotImplemented, CodeUnsupported, "this server has no web client routes"))
	}
	return backend, ok
}

func (s *Server) registerClientRoutes(mux *http.ServeMux) {
	clientRead := func(pattern string, fn func(c ClientBackend, r *http.Request) (any, error)) {
		mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			c, ok := s.client(w, r)
			if !ok {
				return
			}
			body, err := fn(c, r)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, body)
		})
	}

	clientRead("/api/v1/web/i18n", func(c ClientBackend, r *http.Request) (any, error) {
		return c.Messages(r.Context())
	})
	clientRead("/api/v1/web/agents/{agent}/transcript", func(c ClientBackend, r *http.Request) (any, error) {
		limit := 120
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 || parsed > 1000 {
				return nil, InvalidRequest("limit must be between 1 and 1000")
			}
			limit = parsed
		}
		return c.Transcript(r.Context(), r.PathValue("agent"), limit)
	})
	clientRead("/api/v1/web/windows/{window}/layout", func(c ClientBackend, r *http.Request) (any, error) {
		return c.Layout(r.Context(), r.PathValue("window"), r.URL.Query().Get("contents") == "1")
	})
	clientRead("/api/v1/web/panes/{pane}/screen", func(c ClientBackend, r *http.Request) (any, error) {
		return c.Screen(r.Context(), r.PathValue("pane"))
	})
	clientRead("/api/v1/web/launch", func(c ClientBackend, r *http.Request) (any, error) {
		return c.LaunchOptions(r.Context())
	})
	clientRead("/api/v1/web/statusbar", func(c ClientBackend, r *http.Request) (any, error) {
		return c.Statusbar(r.Context())
	})
	clientRead("/api/v1/web/panes/{pane}/git", func(c ClientBackend, r *http.Request) (any, error) {
		return c.PaneGit(r.Context(), r.PathValue("pane"))
	})
	clientRead("/api/v1/web/windows/{window}/resume-candidates", func(c ClientBackend, r *http.Request) (any, error) {
		return c.ResumeCandidates(r.Context(), r.PathValue("window"))
	})
	mux.HandleFunc("POST /api/v1/web/projects/{project}/windows/{window}/agents/preview", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.client(w, r)
		if !ok {
			return
		}
		var req CreateAgentRequest
		if err := decodeBody(w, r, &req); err != nil {
			s.fail(w, r, err)
			return
		}
		body, err := c.PreviewAgent(r.Context(), r.PathValue("project"), r.PathValue("window"), req)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})

	clientRead("/api/v1/web/settings", func(c ClientBackend, r *http.Request) (any, error) {
		return c.Settings(r.Context())
	})
	mux.HandleFunc("PATCH /api/v1/web/settings", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.client(w, r)
		if !ok {
			return
		}
		var req SettingRequest
		if err := decodeBody(w, r, &req); err != nil {
			s.fail(w, r, err)
			return
		}
		if req.Key == "" {
			s.fail(w, r, InvalidRequest("key is required"))
			return
		}
		body, err := c.UpdateSetting(r.Context(), req)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("POST "+uploadPath, s.handleUpload)
	mux.HandleFunc("GET "+uploadPath+"/{name}", s.serveUpload)
	mux.HandleFunc("POST /api/v1/web/agents/{agent}/question", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.client(w, r)
		if !ok {
			return
		}
		var req QuestionAnswer
		if err := decodeBody(w, r, &req); err != nil {
			s.fail(w, r, err)
			return
		}
		if req.ToolID == "" {
			s.fail(w, r, InvalidRequest("toolId is required"))
			return
		}
		body, err := c.AnswerQuestion(r.Context(), r.PathValue("agent"), req)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /api/v1/web/transcripts/events", s.handleTranscriptEvents)
	mux.HandleFunc("GET /api/v1/web/windows/{window}/layout/events", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.client(w, r)
		if !ok {
			return
		}
		contents := r.URL.Query().Get("contents") == "1"
		window := r.PathValue("window")
		s.streamCapture(w, r, "layout", layoutPoll, func(ctx context.Context) (any, error) {
			return c.Layout(ctx, window, contents)
		})
	})
	mux.HandleFunc("GET /api/v1/web/panes/{pane}/screen/events", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.client(w, r)
		if !ok {
			return
		}
		pane := r.PathValue("pane")
		s.streamCapture(w, r, "screen", screenPoll, func(ctx context.Context) (any, error) {
			return c.Screen(ctx, pane)
		})
	})
}

func startStream(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher, last: time.Now()}, true
}

// streamCapture re-captures on a timer and sends only when the capture
// changed. A failed capture ends the stream with `gone`: the pane or window
// is not there to follow any more.
func (s *Server) streamCapture(w http.ResponseWriter, r *http.Request, event string, every time.Duration, capture func(context.Context) (any, error)) {
	first, err := capture(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, ok := startStream(w)
	if !ok {
		s.fail(w, r, NewError(http.StatusInternalServerError, CodeInternal, "streaming is not supported"))
		return
	}
	var digest [32]byte
	send := func(body any) bool {
		frame, err := json.Marshal(body)
		if err != nil {
			return true
		}
		// The capture carries its own timestamp, which would make every poll
		// look like a change; it is left out of the comparison.
		next := sha256.Sum256(withoutAt(frame))
		if next == digest {
			return out.keepalive() == nil
		}
		digest = next
		return out.event(event, frame) == nil
	}
	if !send(first) {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			body, err := capture(r.Context())
			if err != nil {
				frame, _ := json.Marshal(map[string]*Error{"error": asError(err)})
				_ = out.event("gone", frame)
				return
			}
			if !send(body) {
				return
			}
		}
	}
}

func withoutAt(frame []byte) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(frame, &fields) != nil {
		return frame
	}
	delete(fields, "at")
	stripped, err := json.Marshal(fields)
	if err != nil {
		return frame
	}
	return stripped
}
