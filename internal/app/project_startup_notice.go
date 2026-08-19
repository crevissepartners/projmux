package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
)

// projectStartupNoticeMax bounds one `display-message` payload, in bytes.
//
// tmux renders the message on a single status line and silently truncates what
// does not fit, so a long disclosure would be reported as a *shorter, different*
// sentence. The buffered stderr mirror always carries the complete text; this
// cap only decides how much of it the transient popup line repeats, and the
// ellipsis makes the truncation visible instead of implicit.
//
// The budget is counted in bytes because that is what the transport spends, but
// the cut is taken on a rune boundary: the ko-KR catalog and an Agent name are
// both multi-byte, and a byte-exact cut would land mid-rune and put a U+FFFD
// replacement character in the operator's one visible disclosure.
const projectStartupNoticeMax = 220

// projectStartupNoticeSink is the operator-facing report surface for closed-Project
// startup.
//
// Phase 0 sent its "this Agent could not resume its recorded conversation"
// disclosure to os.Stderr. Inside a tmux popup that emit is guaranteed to happen
// and guaranteed *not* to be read: the popup closes with the process, and the
// operator is moved into the session before the bytes are ever on screen. The
// `new` row's result report has exactly the same problem, so both go through one
// object.
//
// It is a tee, not a replacement. Stderr keeps the complete, line-oriented,
// machine-readable record that the e2e smoke greps and that a `2>` redirect can
// capture; `tmux display-message` adds the half a human actually sees. Choosing
// only one of the two would either keep the invisible-disclosure bug or delete
// the only durable evidence of it.
//
// Writes are buffered and emitted once by Flush rather than per line, because
// consecutive display-message calls overwrite one another: three unresumed
// Agents emitted separately would show the operator exactly one of them.
type projectStartupNoticeSink struct {
	runner tmuxCommandRunner
	mirror io.Writer
	// lookupEnv answers whether this process has a tmux client to display on.
	// `tmux display-message` with no -L/-S and no inherited $TMUX addresses the
	// *default* server, which for a routine run outside tmux means flashing a
	// startup disclosure onto whatever unrelated session happens to be attached
	// there. No client means no display half; stderr still carries every line.
	lookupEnv func(string) string

	mu  sync.Mutex
	buf bytes.Buffer
}

// newProjectStartupNoticeSink builds the production surface: stderr plus the
// operator's current tmux client.
//
// The display half is deliberately run as a plain `tmux display-message` with no
// -L/-S routing, which is the same spelling displayNotifySidebarMessage uses. A
// message has to land on the client the operator is looking at, and that client
// is identified by the inherited $TMUX of this process, not by the app socket the
// materializer writes topology to.
func newProjectStartupNoticeSink(runner tmuxCommandRunner) *projectStartupNoticeSink {
	return &projectStartupNoticeSink{runner: runner, mirror: os.Stderr, lookupEnv: os.Getenv}
}

func (s *projectStartupNoticeSink) Write(p []byte) (int, error) {
	if s == nil {
		return len(p), nil
	}
	s.mu.Lock()
	s.buf.Write(p)
	s.mu.Unlock()
	if s.mirror != nil {
		return s.mirror.Write(p)
	}
	return len(p), nil
}

// Flush emits everything written since the last flush as one tmux message and
// resets the buffer. It is best effort in both directions: an unavailable tmux
// client must not turn a converged topology into a failed open, and the stderr
// mirror has already recorded the same text.
func (s *projectStartupNoticeSink) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	pending := s.buf.String()
	s.buf.Reset()
	s.mu.Unlock()
	s.display(pending)
}

// Report emits one message that was never written to the buffer, mirroring it to
// stderr first so the durable record and the transient line always agree.
func (s *projectStartupNoticeSink) Report(message string) {
	if s == nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if s.mirror != nil {
		_, _ = io.WriteString(s.mirror, message+"\n")
	}
	s.display(message)
}

func (s *projectStartupNoticeSink) display(text string) {
	message := projectStartupNoticeMessage(text)
	if message == "" || s.runner == nil {
		return
	}
	if s.lookupEnv == nil || strings.TrimSpace(s.lookupEnv("TMUX")) == "" {
		return
	}
	_, _ = s.runner.Run(context.Background(), "tmux", "display-message", message)
}

// projectStartupNoticeMessage folds a multi-line disclosure into the single line
// tmux can show, preserving every line as its own clause.
func projectStartupNoticeMessage(text string) string {
	lines := make([]string, 0, 4)
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	message := strings.Join(lines, "; ")
	if message == "" {
		return ""
	}
	if len(message) > projectStartupNoticeMax {
		message = strings.TrimSpace(truncateUTF8Prefix(message, projectStartupNoticeMax)) + "..."
	}
	return message
}

// truncateUTF8Prefix returns the longest prefix of s that fits in max bytes
// without splitting a rune.
//
// It is byte-budgeted rather than rune-budgeted -- unlike truncateRunes, which
// counts runes for a display-width purpose -- because this budget belongs to the
// transport: the cut must never make the payload larger, and a rune count would
// let one multi-byte line spend three times the bytes an ASCII one does. An
// input whose very first rune is already wider than the budget yields the empty
// string rather than a broken one.
func truncateUTF8Prefix(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := 0
	for index := range s {
		if index > max {
			break
		}
		end = index
	}
	return s[:end]
}

// projectStartupNoticeFlusher is the optional flush half of a notice writer. The
// materializer keeps its `notices io.Writer` field so a fixture can still pass a
// plain bytes.Buffer; a sink that batches opts in by implementing this.
type projectStartupNoticeFlusher interface {
	Flush()
}

func flushProjectStartupNotices(w io.Writer) {
	if flusher, ok := w.(projectStartupNoticeFlusher); ok {
		flusher.Flush()
	}
}
