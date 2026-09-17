package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// One page follows every transcript it shows over a single stream. A browser
// allows six connections per host over HTTP/1.1, so a stream per agent slot
// leaves nothing for the requests the page makes once four slots are open.
//
// The client names each followed transcript with a key of its own and the
// offset to start at:
//
//	GET /api/v1/web/transcripts/events?stream=<key>:<agent>:<offset|start|end>&stream=...
//
// Frames:
//
//	ready                {"streams": {key: offset}}                  once, after every transcript was opened
//	turn                 {"stream", "agent", "turn"}                 one per appended entry
//	transcript-error     {"stream", "agent", "error"}                a transcript could not be opened or read
//	transcript-recovered {"stream", "agent"}                         a read succeeded after a failed one
//
// The `ready` frame and the last `turn` frame of each read carry an id listing
// every key's offset, `key:offset,key:offset`. An EventSource sends it back as
// Last-Event-ID when it reconnects, and the stream resumes each key from
// there. Only the last frame of a read carries the id, because every turn of
// one read ends at the same offset: a stream cut inside a read resumes at its
// start and sends that read again, rather than skipping its tail.

const (
	transcriptTurnEvent      = "turn"
	transcriptReadyEvent     = "ready"
	transcriptErrorEvent     = "transcript-error"
	transcriptRecoveredEvent = "transcript-recovered"
	maxTranscriptStreams     = 32
)

var transcriptKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

type transcriptStream struct {
	key      string
	agent    string
	offset   int64 // where to start, -1 for the end of the file
	follower Follower
	failing  bool
}

type transcriptFrame struct {
	Stream string `json:"stream"`
	Agent  string `json:"agent"`
	Turn   any    `json:"turn,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// parseTranscriptStreams reads the `stream` query values and applies the
// offsets a reconnecting client sent in Last-Event-ID.
func parseTranscriptStreams(values []string, lastID string) ([]*transcriptStream, error) {
	if len(values) == 0 {
		return nil, InvalidRequest("at least one stream=<key>:<agent>:<offset> is required")
	}
	if len(values) > maxTranscriptStreams {
		return nil, InvalidRequest(fmt.Sprintf("at most %d streams", maxTranscriptStreams))
	}
	streams := make([]*transcriptStream, 0, len(values))
	for _, value := range values {
		key, rest, ok := strings.Cut(value, ":")
		cut := strings.LastIndex(rest, ":")
		if !ok || cut <= 0 {
			return nil, InvalidRequest(fmt.Sprintf("stream %q is not <key>:<agent>:<offset>", value))
		}
		if !transcriptKey.MatchString(key) {
			return nil, InvalidRequest(fmt.Sprintf("stream key %q must be 1-16 letters, digits, - or _", key))
		}
		if slices.ContainsFunc(streams, func(s *transcriptStream) bool { return s.key == key }) {
			return nil, InvalidRequest(fmt.Sprintf("stream key %q is repeated", key))
		}
		offset, err := parseTranscriptOffset(rest[cut+1:])
		if err != nil {
			return nil, err
		}
		streams = append(streams, &transcriptStream{key: key, agent: rest[:cut], offset: offset})
	}
	if lastID == "" {
		return streams, nil
	}
	for pair := range strings.SplitSeq(lastID, ",") {
		key, raw, ok := strings.Cut(pair, ":")
		offset, err := strconv.ParseInt(raw, 10, 64)
		if !ok || err != nil || offset < 0 {
			return nil, InvalidRequest("Last-Event-ID must be key:offset pairs")
		}
		for _, s := range streams {
			if s.key == key {
				s.offset = offset
			}
		}
	}
	return streams, nil
}

func parseTranscriptOffset(raw string) (int64, error) {
	switch raw {
	case "", "end":
		return -1, nil
	case "start":
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, InvalidRequest("a stream offset must be start, end, or a byte offset")
	}
	return parsed, nil
}

// transcriptID lists every key's resume offset: where its follower got to,
// or where it was asked to start when it has none.
func transcriptID(streams []*transcriptStream) string {
	parts := make([]string, 0, len(streams))
	for _, s := range streams {
		offset := s.offset
		if s.follower != nil {
			offset = s.follower.Offset()
		}
		if offset < 0 {
			continue
		}
		parts = append(parts, s.key+":"+strconv.FormatInt(offset, 10))
	}
	return strings.Join(parts, ",")
}

func (s *Server) handleTranscriptEvents(w http.ResponseWriter, r *http.Request) {
	c, ok := s.client(w, r)
	if !ok {
		return
	}
	streams, err := parseTranscriptStreams(r.URL.Query()["stream"], r.Header.Get("Last-Event-ID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A transcript that cannot be opened is reported and left out of this
	// connection; the others are still followed.
	ready := map[string]int64{}
	var failed [][]byte
	for _, stream := range streams {
		follower, err := c.FollowTranscript(r.Context(), stream.agent, stream.offset)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			frame, _ := json.Marshal(transcriptFrame{Stream: stream.key, Agent: stream.agent, Error: asError(err)})
			failed = append(failed, frame)
			continue
		}
		stream.follower = follower
		ready[stream.key] = follower.Offset()
	}
	out, ok := startStream(w)
	if !ok {
		s.fail(w, r, NewError(http.StatusInternalServerError, CodeInternal, "streaming is not supported"))
		return
	}
	for _, frame := range failed {
		if out.event(transcriptErrorEvent, frame) != nil {
			return
		}
	}
	frame, _ := json.Marshal(map[string]any{"streams": ready})
	if out.eventID(transcriptReadyEvent, transcriptID(streams), frame) != nil {
		return
	}

	ticker := time.NewTicker(transcriptPoll)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			for _, stream := range streams {
				if stream.follower != nil && !followOnce(out, streams, stream) {
					return
				}
			}
			if out.keepalive() != nil {
				return
			}
		}
	}
}

// followOnce sends what one transcript appended since the last tick. A read
// error is reported once per failing run and the transcript stays followed:
// a provider rewriting its file mid-read is normal.
func followOnce(out *sseWriter, streams []*transcriptStream, stream *transcriptStream) bool {
	items, err := stream.follower.Next()
	if err != nil {
		if stream.failing {
			return true
		}
		stream.failing = true
		frame, _ := json.Marshal(transcriptFrame{Stream: stream.key, Agent: stream.agent, Error: asError(err)})
		return out.event(transcriptErrorEvent, frame) == nil
	}
	if stream.failing {
		stream.failing = false
		frame, _ := json.Marshal(transcriptFrame{Stream: stream.key, Agent: stream.agent})
		if out.event(transcriptRecoveredEvent, frame) != nil {
			return false
		}
	}
	frames := make([][]byte, 0, len(items))
	for _, item := range items {
		frame, err := json.Marshal(transcriptFrame{Stream: stream.key, Agent: stream.agent, Turn: item})
		if err != nil {
			continue
		}
		frames = append(frames, frame)
	}
	for i, frame := range frames {
		var err error
		if i == len(frames)-1 {
			err = out.eventID(transcriptTurnEvent, transcriptID(streams), frame)
		} else {
			err = out.event(transcriptTurnEvent, frame)
		}
		if err != nil {
			return false
		}
	}
	return true
}
