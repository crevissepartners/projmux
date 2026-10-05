package processhost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrResumeRefused never authorizes creating a replacement conversation.
var ErrResumeRefused = errors.New("recorded provider session resume refused")

// RecordedControl retains identities only, without question text or decisions.
type RecordedControl struct {
	ID, Kind, Connection, Session, Turn string
}

// SessionRecord is an internal persistence value, not Registry schema. A caller
// records it before losing a host and retires that host before explicit resume.
// Having an identity is a resume candidate, not proof the provider can load it.
type SessionRecord struct {
	Provider                  string
	Binding                   Binding
	Session, Connection, Turn string
	Pending                   []RecordedControl
}

func RecordSession(provider string, snapshot Snapshot) SessionRecord {
	r := SessionRecord{Provider: provider, Binding: snapshot.Binding, Session: snapshot.Session, Connection: snapshot.Connection, Turn: snapshot.Turn}
	for _, q := range snapshot.Pending {
		r.Pending = append(r.Pending, RecordedControl{ID: q.ID, Kind: q.Kind, Connection: q.Connection, Session: q.Session, Turn: q.Turn})
	}
	return r
}

// Availability distinguishes a recorded candidate from an unknown identity.
// Actual provider acceptance is witnessed separately on the new connection.
func (r SessionRecord) Availability() string {
	if (r.Provider == "claude" || r.Provider == "codex") && r.Session != "" {
		return "resumable"
	}
	return "unknown"
}

// ResumeHistory describes retirement, never successful continuation of a turn.
// It keeps the old binding: expired requests cannot become new control tokens.
type ResumeHistory struct {
	Binding         Binding
	Session         string
	InterruptedTurn string
	Expired         []RecordedControl
}

func (r SessionRecord) history() *ResumeHistory {
	return &ResumeHistory{Binding: r.Binding, Session: r.Session, InterruptedTurn: r.Turn, Expired: slices.Clone(r.Pending)}
}

func (h *Host) prepareResume(launch Launch, record SessionRecord, provider string) (Launch, error) {
	b, old := launch.Binding, record.Binding
	if record.Provider != provider || record.Availability() != "resumable" || !old.valid(old.Host) || !b.valid(h.instance) ||
		b.Project != old.Project || b.Window != old.Window || b.Agent != old.Agent || b.Pane != old.Pane ||
		b.Generation == old.Generation || b.Operation == old.Operation || record.Connection == "" ||
		len(record.Session) > 256 || strings.TrimSpace(record.Session) != record.Session || len(record.Pending) > h.limits.Requests {
		return Launch{}, fmt.Errorf("%w: missing identity or unchanged ownership generation", ErrResumeRefused)
	}
	for _, q := range record.Pending {
		if q.ID == "" || (q.Kind != "question" && q.Kind != "permission") || q.Connection != record.Connection || q.Session != record.Session || q.Turn == "" || q.Turn != record.Turn {
			return Launch{}, fmt.Errorf("%w: inconsistent recorded control identity", ErrResumeRefused)
		}
	}
	record.Pending = slices.Clone(record.Pending)
	launch.resume = &record
	return launch, nil
}

// ResumeCodex owns a fresh app-server and loads only the recorded thread using
// the typed Client. No thread/start or shared endpoint fallback is permitted.
func (h *Host) ResumeCodex(ctx context.Context, launch Launch, config CodexConfig, record SessionRecord) (*CodexHandle, error) {
	launch, err := h.prepareResume(launch, record, "codex")
	if err != nil {
		return nil, err
	}
	p, err := h.StartCodex(ctx, launch, config)
	if err != nil {
		return p, fmt.Errorf("%w: %w", ErrResumeRefused, err)
	}
	return p, nil
}

// ResumeClaude supplies an explicit new turn: installed stream-json emits init
// only after input. The old turn is never resent. Success requires the recorded
// session in init before the caller can treat this generation as ready.
func (h *Host) ResumeClaude(ctx context.Context, launch Launch, record SessionRecord, turn, prompt string) (*Handle, error) {
	launch, err := h.prepareResume(launch, record, "claude")
	if err != nil {
		return nil, err
	}
	if turn == "" || turn == record.Turn || len(turn) > 256 || prompt == "" {
		return nil, fmt.Errorf("%w: an explicit new turn is required", ErrResumeRefused)
	}
	for _, arg := range launch.Command.Args {
		for _, reserved := range []string{"--resume", "-r", "--continue", "-c", "--fork-session", "--session-id", "--no-session-persistence"} {
			if arg == reserved || strings.HasPrefix(arg, reserved+"=") {
				return nil, fmt.Errorf("%w: conflicting session argument", ErrResumeRefused)
			}
		}
	}
	launch.Command.Args = append(slices.Clone(launch.Command.Args), "--resume", record.Session)
	launch.resumeTurn, launch.resumePrompt = turn, prompt
	p, err := h.Start(ctx, launch)
	if err != nil {
		return p, fmt.Errorf("%w: %w", ErrResumeRefused, err)
	}
	return p, nil
}

func (p *Handle) initializeResume(ctx context.Context) error {
	b := p.launch.Binding
	s, _ := p.Observe(b)
	a := Authority{Binding: b, Connection: s.Connection, Session: s.Session}
	if err := p.Turn(ctx, a, p.launch.resumeTurn, p.launch.resumePrompt); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, p.host.limits.Startup)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s, _ = p.Observe(b)
		if s.State == "ready" && s.Session == p.launch.expectedResumeSession() {
			return nil
		}
		if s.State != "starting" {
			return errors.New("provider did not accept recorded session")
		}
		select {
		case <-bounded.Done():
			return bounded.Err()
		case <-ticker.C:
		}
	}
}
