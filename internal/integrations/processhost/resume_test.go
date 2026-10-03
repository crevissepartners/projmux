package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func resumeClaudeFixture(mode string) {
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		raw := append([]byte{}, scanner.Bytes()...)
		if path := os.Getenv("PROCESSHOST_CODEX_LOG"); path != "" {
			f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			_, _ = f.Write(append(raw, '\n'))
			_ = f.Close()
		}
		var n struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(raw, &n)
		if n.Type != "user" {
			continue
		}
		if mode == "resume-claude-refused" {
			_ = out.Encode(map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": "session", "is_error": true})
			os.Exit(1)
		}
		session := "session"
		if mode == "resume-claude-wrong-session" {
			session = "foreign"
		}
		_ = out.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": session})
		if n.Message.Content == "controls" {
			for _, tool := range []string{"AskUserQuestion", "Bash"} {
				_ = out.Encode(map[string]any{"type": "control_request", "request_id": tool, "request": map[string]any{"subtype": "can_use_tool", "tool_name": tool, "input": map[string]any{"fixture": "not-durable-content"}}})
			}
		} else {
			_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "session_id": session})
		}
	}
}

func resumedBinding() Binding {
	b := binding()
	b.Generation = "new-generation"
	b.Operation = "resume-operation"
	return b
}

func waitResume(t *testing.T, p *Handle, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		s, err := p.Observe(p.launch.Binding)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	s, _ := p.Observe(p.launch.Binding)
	t.Fatalf("resume observation timeout: %+v", s)
	return s
}

func stopResume(t *testing.T, p *Handle) {
	t.Helper()
	_ = p.Stop(p.launch.Binding)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.Wait(ctx, p.launch.Binding); err != nil {
		t.Error(err)
	}
}

func savedRecord(t *testing.T, provider string, s Snapshot) SessionRecord {
	t.Helper()
	raw, err := json.Marshal(RecordSession(provider, s))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "not-durable-content") {
		t.Fatal("record contains question content")
	}
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record SessionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func assertResumeHistory(t *testing.T, p *Handle, r SessionRecord) {
	t.Helper()
	s, _ := p.Observe(p.launch.Binding)
	if s.Resume == nil || s.Resume.Binding != r.Binding || s.Resume.InterruptedTurn != r.Turn || !reflect.DeepEqual(s.Resume.Expired, r.Pending) || len(s.Pending) != 0 {
		t.Fatalf("retirement history lost or became current control: %+v", s)
	}
	// Snapshot mutation cannot rewrite retained durable identities.
	if len(s.Resume.Expired) > 0 {
		s.Resume.Expired[0].ID = "mutated"
	}
	s2, _ := p.Observe(p.launch.Binding)
	if !reflect.DeepEqual(s2.Resume.Expired, r.Pending) {
		t.Fatal("history aliased")
	}
}

func TestResumeClaudeRetiresOldTurnAndControls(t *testing.T) {
	h := testHost(t, nil)
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	cmd := fixtureCommand("resume-claude")
	cmd.Env = append(cmd.Env, "PROCESSHOST_CODEX_LOG="+log)
	old, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopResume(t, old) })
	turn(t, old, "old-turn", "controls")
	prior := observeUntil(t, old, func(s Snapshot) bool { return len(s.Pending) == 2 })
	record := savedRecord(t, "claude", prior)
	oldAuthority := authority(old)
	stopResume(t, old)
	h = testHost(t, nil)
	h.instance = "restarted-host"
	b := resumedBinding()
	b.Host = h.instance
	launch := Launch{Binding: b, Command: cmd}
	var mu sync.Mutex
	var handles []*Handle
	var failures []error
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p, e := h.ResumeClaude(context.Background(), launch, record, "new-turn", "ordinary")
			mu.Lock()
			handles = append(handles, p)
			failures = append(failures, e)
			mu.Unlock()
		})
	}
	wg.Wait()
	p := handles[0]
	if p == nil {
		t.Fatalf("resume failures=%v", failures)
	}
	t.Cleanup(func() { stopResume(t, p) })
	for i, e := range failures {
		if e != nil || handles[i] != p {
			t.Fatalf("retry not idempotent: %v", failures)
		}
	}
	waitResume(t, p, func(s Snapshot) bool { return s.Turn == "" })
	assertResumeHistory(t, p, record)
	before := len(codexWire(t, log))
	for _, q := range prior.Pending {
		if err := p.Respond(context.Background(), oldAuthority, q, Response{Allow: true, Answers: map[string]string{"fixture": "yes"}}); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.Expire(oldAuthority, q); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := old.Respond(context.Background(), oldAuthority, q, Response{Allow: true}); err == nil {
			t.Fatal("retired host accepted")
		}
	}
	if err := p.Interrupt(context.Background(), oldAuthority, prior.Turn); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := p.consume([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"interrupt-old-turn"}}`)); err == nil {
		t.Fatal("old ack admitted")
	}
	if hasEvent(p, "interrupt-ack") {
		t.Fatal("old ack attributed to new generation")
	}
	if len(codexWire(t, log)) != before {
		t.Fatal("stale control wrote provider wire")
	}
	if before != 2 {
		t.Fatalf("launch retry resent new prompt: wire=%d", before)
	}
	if p.pid == old.pid || p.connection == old.connection || p.launch.Binding.Generation == old.launch.Binding.Generation {
		t.Fatal("runtime identity reused")
	}
	a := Authority{Binding: launch.Binding, Session: record.Session, Connection: p.connection}
	if err := p.Turn(context.Background(), a, "follow-up", "controls"); err != nil {
		t.Fatal(err)
	}
	current := waitResume(t, p, func(s Snapshot) bool { return len(s.Pending) == 2 })
	before = len(codexWire(t, log))
	for _, q := range prior.Pending {
		if err := p.Respond(context.Background(), a, q, Response{Allow: true}); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.Expire(a, q); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	if len(codexWire(t, log)) != before {
		t.Fatal("old tokens rebound with current authority")
	}
	for _, q := range current.Pending {
		response := Response{Allow: true}
		if q.Kind == "question" {
			response = Response{Answers: map[string]string{"fixture": "yes"}}
		}
		if err := p.Respond(context.Background(), a, q, response); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResumeCodexRetiresOldTurnAndControls(t *testing.T) {
	h := testHost(t, nil)
	old, launch, log := codexStart(t, h, "codex-normal")
	if err := old.Turn(context.Background(), codexAuthority(old), "old-operation", "controls"); err != nil {
		t.Fatal(err)
	}
	prior := observeUntil(t, old.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	record := savedRecord(t, "codex", prior)
	oldAuthority := codexAuthority(old)
	stopResume(t, old.handle)
	h = testHost(t, nil)
	h.instance = "restarted-host"
	launch.Binding = resumedBinding()
	launch.Binding.Host = h.instance
	p, err := h.ResumeCodex(context.Background(), launch, codexSettings(), record)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopResume(t, p.handle) })
	assertResumeHistory(t, p.handle, record)
	before := len(codexWire(t, log))
	for _, q := range prior.Pending {
		if err := p.RespondApproval(context.Background(), oldAuthority, q, codexappserver.DecisionAccept); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.RespondQuestion(context.Background(), oldAuthority, q, nil); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.Expire(oldAuthority, q); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	if err := p.Interrupt(context.Background(), oldAuthority, prior.Turn); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := p.handle.adapter.(*codexAdapter).consume(codexappserver.Notification{Method: "turn/completed", Params: []byte(`{"threadId":"thread","turn":{"id":"turn-1"}}`)}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if hasEvent(p.handle, "turn-result") || hasEvent(p.handle, "interrupt-ack") {
		t.Fatal("old result attributed to new generation")
	}
	if len(codexWire(t, log)) != before {
		t.Fatal("stale control wrote provider wire")
	}
	a := Authority{Binding: launch.Binding, Session: record.Session, Connection: p.handle.connection}
	if err := p.Turn(context.Background(), a, "new-operation", "controls"); err != nil {
		t.Fatal(err)
	}
	current := waitResume(t, p.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	before = len(codexWire(t, log))
	for _, q := range prior.Pending {
		if err := p.RespondApproval(context.Background(), a, q, codexappserver.DecisionAccept); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.RespondQuestion(context.Background(), a, q, nil); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
		if err := p.Expire(a, q); !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	if len(codexWire(t, log)) != before {
		t.Fatal("old tokens rebound with current authority")
	}
	for _, q := range current.Pending {
		if q.Kind == "permission" {
			if err := p.RespondApproval(context.Background(), a, q, codexappserver.DecisionDecline); err != nil {
				t.Fatal(err)
			}
		} else if err := p.Expire(a, q); err != nil {
			t.Fatal(err)
		}
	}
	starts, resumes := 0, 0
	for _, n := range codexWire(t, log) {
		var method string
		_ = json.Unmarshal(n["method"], &method)
		if method == "thread/start" {
			starts++
		}
		if method == "thread/resume" {
			resumes++
		}
	}
	if starts != 1 || resumes != 1 {
		t.Fatalf("start=%d resume=%d", starts, resumes)
	}
}

func TestResumeRefusalsNeverCreateReplacementConversation(t *testing.T) {
	for _, mode := range []string{"resume-claude-refused", "resume-claude-wrong-session", "codex-resume-refused", "codex-resume-wrong-thread"} {
		t.Run(mode, func(t *testing.T) {
			h := testHost(t, func(_ *Transactions, l *Limits) { l.Grace = time.Second })
			provider, session := "claude", "session"
			if strings.HasPrefix(mode, "codex") {
				provider, session = "codex", "thread"
			}
			record := SessionRecord{Provider: provider, Binding: binding(), Connection: "old", Session: session}
			log := filepath.Join(t.TempDir(), "wire.jsonl")
			cmd := fixtureCommand(mode)
			cmd.Env = append(cmd.Env, "PROCESSHOST_CODEX_LOG="+log)
			launch := Launch{Binding: resumedBinding(), Command: cmd}
			var p *Handle
			var err error
			if provider == "claude" {
				p, err = h.ResumeClaude(context.Background(), launch, record, "new-turn", "ordinary")
			} else {
				var c *CodexHandle
				c, err = h.ResumeCodex(context.Background(), launch, codexSettings(), record)
				if c != nil {
					p = c.handle
				}
			}
			if !errors.Is(err, ErrResumeRefused) || p == nil {
				t.Fatalf("refusal=%v handle=%v", err, p)
			}
			if s, _ := p.Observe(launch.Binding); s.Exit == nil || s.Session != "" {
				t.Fatalf("failed resume survived: %+v", s)
			}
			for _, n := range codexWire(t, log) {
				if string(n["method"]) == `"thread/start"` {
					t.Fatal("fallback thread created")
				}
			}
		})
	}
}

func TestResumeRecordUnknownAndGenerationRefusalBeforeSpawn(t *testing.T) {
	h := testHost(t, nil)
	base := SessionRecord{Provider: "codex", Binding: binding(), Connection: "old", Session: "thread"}
	if base.Availability() != "resumable" || (SessionRecord{}).Availability() != "unknown" {
		t.Fatal("candidate conflated with unknown")
	}
	for _, change := range []func(*SessionRecord, *Launch){func(r *SessionRecord, l *Launch) { r.Session = "" }, func(r *SessionRecord, l *Launch) { l.Binding.Generation = r.Binding.Generation }, func(r *SessionRecord, l *Launch) { l.Binding.Operation = r.Binding.Operation }, func(r *SessionRecord, l *Launch) { l.Binding.Agent = "other" }, func(r *SessionRecord, l *Launch) { r.Provider = "claude" }} {
		r := base
		l := Launch{Binding: resumedBinding(), Command: fixtureCommand("codex-normal")}
		change(&r, &l)
		if p, err := h.ResumeCodex(context.Background(), l, codexSettings(), r); p != nil || !errors.Is(err, ErrResumeRefused) {
			t.Fatalf("prewrite refusal=%v handle=%v", err, p)
		}
	}
	if len(h.operations) != 0 {
		t.Fatal("invalid resume reserved operation")
	}
}
