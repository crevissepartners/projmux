package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/testutil/liveguard"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "processhost-supervisor":
			if err := ServeSupervisor(os.NewFile(3, "lifetime"), os.NewFile(4, "spec"), os.NewFile(5, "status")); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "processhost-unprepared":
			for {
				time.Sleep(time.Hour)
			}
		case "processhost-prepared-stall":
			_ = json.NewEncoder(os.NewFile(5, "status")).Encode(processStatus{Prepared: true})
			_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
			for {
				time.Sleep(time.Hour)
			}
		case "processhost-race":
			var x int
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					for range 10000 {
						x++
					}
				})
			}
			wg.Wait()
			fmt.Fprintln(os.Stdout, x)
			os.Exit(0)
		case "processhost-provider":
			fixtureProvider()
			os.Exit(0)
		case "processhost-owner":
			fixtureOwner()
			os.Exit(0)
		case "processhost-escaped-leaf":
			deadline := time.Now().Add(time.Minute)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(os.Args[2]); err == nil {
					os.Exit(0)
				}
				time.Sleep(time.Millisecond)
			}
			os.Exit(1)
		case "processhost-leaf":
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	os.Exit(liveguard.RunTests(m, liveguard.ProviderOptIn("PROCESSHOST_TEST_CODEX", "PROCESSHOST_TEST_CLAUDE")))
}

// Only re-executed fixture children skip the race runtime's default one-second
// exit delay. The parent test binary retains its normal race settings, and all
// other inherited GORACE options (including fatal reports) remain unchanged.
func fixtureEnv() []string {
	env := os.Environ()
	for i, value := range env {
		if strings.HasPrefix(value, "GORACE=") {
			env[i] = value + " atexit_sleep_ms=0"
			return env
		}
	}
	return append(env, "GORACE=atexit_sleep_ms=0")
}

func fixtureCommand(mode string) Command {
	return Command{Path: os.Args[0], Args: []string{"processhost-provider", mode}, Env: fixtureEnv()}
}
func testHost(t *testing.T, change func(*Transactions, *Limits)) *Host {
	t.Helper()
	tx := Transactions{Reserve: func(context.Context, Binding) error { return nil }, Commit: func(context.Context, Binding, string) error { return nil }, Current: func(context.Context, Binding) error { return nil }}
	limits := DefaultLimits()
	limits.Grace = 200 * time.Millisecond
	limits.Startup = 3 * time.Second
	limits.Write = 50 * time.Millisecond
	if change != nil {
		change(&tx, &limits)
	}
	host, err := NewHost("host", Command{Path: os.Args[0], Args: []string{"processhost-supervisor"}, Env: fixtureEnv()}, tx, limits)
	if err != nil {
		t.Fatal(err)
	}
	return host
}
func binding() Binding {
	return Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "gen", Operation: "operation"}
}
func start(t *testing.T, h *Host, mode string) *Handle {
	t.Helper()
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand(mode)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop(binding())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := p.Wait(ctx, binding()); err != nil {
			t.Error(err)
		}
	})
	return p
}
func authority(p *Handle) Authority {
	s, _ := p.Observe(binding())
	return Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
}
func observeUntil(t *testing.T, p *Handle, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := p.Observe(binding())
		if err != nil {
			t.Fatal(err)
		}
		if predicate(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	s, _ := p.Observe(binding())
	t.Fatalf("observation timeout: %+v", s)
	return s
}
func turn(t *testing.T, p *Handle, id, prompt string) {
	t.Helper()
	if err := p.Turn(context.Background(), authority(p), id, prompt); err != nil {
		t.Fatal(err)
	}
}
func events(p *Handle) []Event { e, _, _ := p.Events(binding(), 0); return e }
func hasEvent(p *Handle, kind string) bool {
	for _, e := range events(p) {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func fixtureProvider() {
	mode := "normal"
	if len(os.Args) > 2 {
		mode = os.Args[2]
	}
	if strings.HasPrefix(mode, "codex-") {
		codexFixture(mode)
		return
	}
	if mode == "resume-claude" || mode == "resume-claude-wrong-session" || mode == "resume-claude-refused" {
		resumeClaudeFixture(mode)
		return
	}
	switch mode {
	case "ignore-eof-term":
		ignoreTerm()
	case "exit0":
		return
	case "escaped-stdout", "escaped-stderr", "escaped-both":
		leaf := exec.Command(os.Args[0], "processhost-escaped-leaf", os.Getenv("PROCESSHOST_RELEASE_FILE"))
		leaf.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if mode != "escaped-stderr" {
			leaf.Stdout = os.Stdout
		}
		if mode != "escaped-stdout" {
			leaf.Stderr = os.Stderr
		}
		// Start's exec handshake completes setsid before this provider exits.
		if err := leaf.Start(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv("PROCESSHOST_LEAF_FILE"), []byte(strconv.Itoa(leaf.Process.Pid)), 0600); err != nil {
			panic(err)
		}
		return
	case "exit7":
		os.Exit(7)
	case "hup":
		_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		time.Sleep(time.Second)
		return
	case "malformed":
		fmt.Println("{no")
		time.Sleep(time.Hour)
	case "oversized":
		fmt.Println(strings.Repeat("x", 2<<20))
		time.Sleep(time.Hour)
	case "stdout-eof":
		_ = os.Stdout.Close()
		time.Sleep(time.Hour)
	case "blocked":
		time.Sleep(time.Hour)
	case "group", "exit-with-leaf":
		leaf := exec.Command(os.Args[0], "processhost-leaf")
		leaf.Stdout = os.Stdout
		leaf.Stderr = os.Stderr
		if err := leaf.Start(); err != nil {
			panic(err)
		}
		ignoreTerm()
		if err := os.WriteFile(os.Getenv("PROCESSHOST_LEAF_FILE"), []byte(strconv.Itoa(leaf.Process.Pid)), 0600); err != nil {
			panic(err)
		}
		if mode == "exit-with-leaf" {
			return
		}
		// Ignore TERM to exercise escalation rather than only graceful shutdown.
		ignoreTerm()
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	writer := json.NewEncoder(os.Stdout)
	result := func() {
		_ = writer.Encode(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}})
		_ = writer.Encode(map[string]any{"type": "result", "subtype": "success", "session_id": "session"})
	}
	for scanner.Scan() {
		var frame map[string]any
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return
		}
		switch frame["type"] {
		case "user":
			_ = writer.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "session"})
			message, _ := frame["message"].(map[string]any)
			prompt, _ := message["content"].(string)
			switch prompt {
			case "question", "allow", "deny":
				tool := "Bash"
				if prompt == "question" {
					tool = "AskUserQuestion"
				}
				req := map[string]any{"type": "control_request", "request_id": prompt, "request": map[string]any{"subtype": "can_use_tool", "tool_name": tool, "input": map[string]any{"questions": []any{map[string]any{"question": "Color?"}}}}}
				_ = writer.Encode(req)
				_ = writer.Encode(req)
			case "interrupt":
				_ = writer.Encode(map[string]any{"type": "stream_event", "session_id": "session", "event": map[string]any{"delta": "partial"}})
			case "unknown-noise":
				for i := range 2000 {
					_ = writer.Encode(map[string]any{"type": "command_lifecycle", "session_id": "session", "index": i})
				}
				result()
			case "noise":
				go func() { _, _ = io.CopyN(os.Stderr, strings.NewReader(strings.Repeat("e", 1<<20)), 1<<20) }()
				for i := range 2000 {
					_ = writer.Encode(map[string]any{"type": "stream_event", "session_id": "session", "event": i})
				}
				result()
			case "session-drift":
				_ = writer.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "foreign"})
			case "hold":
			default:
				result()
			}
		case "control_response":
			// Echo the exact response through a bounded protocol event to assert wire
			// contents without granting the host another response authority.
			_ = writer.Encode(map[string]any{"type": "assistant", "session_id": "session", "response_echo": frame})
			result()
		case "control_request":
			_ = writer.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame["request_id"]}})
		case "fixture-cancel":
			_ = writer.Encode(map[string]any{"type": "control_cancel_request", "request_id": frame["request_id"]})
			_ = writer.Encode(map[string]any{"type": "assistant", "session_id": "session", "cancel_echo": frame["request_id"]})
		case "fixture-release-interrupt":
			_ = writer.Encode(map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": "session"})
		}
	}
	if mode == "session-end" {
		time.Sleep(300 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("PROCESSHOST_SESSION_END_FILE"), []byte("complete"), 0600); err != nil {
			panic(err)
		}
	}
	if mode == "group" || mode == "ignore-eof" || mode == "ignore-eof-term" {
		for {
			time.Sleep(time.Hour)
		}
	}
}

func TestStartOperationAndBinding(t *testing.T) {
	var mu sync.Mutex
	reservations := 0
	h := testHost(t, func(tx *Transactions, _ *Limits) {
		tx.Reserve = func(context.Context, Binding) error { mu.Lock(); reservations++; mu.Unlock(); return nil }
	})
	var wg sync.WaitGroup
	results := make(chan *Handle, 16)
	for range 16 {
		wg.Go(func() {
			p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal")})
			if err != nil {
				t.Error(err)
			}
			results <- p
		})
	}
	wg.Wait()
	close(results)
	var p *Handle
	for got := range results {
		if p == nil {
			p = got
		} else if p != got {
			t.Fatal("duplicate child")
		}
	}
	defer p.Stop(binding())
	s, _ := p.Observe(binding())
	if s.State != "starting" || s.Session != "" || s.PID == 0 {
		t.Fatalf("spawn promoted readiness: %+v", s)
	}
	changed := binding()
	changed.Generation = "new"
	if _, err := h.Start(context.Background(), Launch{Binding: changed, Command: fixtureCommand("normal")}); err != ErrStale {
		t.Fatalf("changed operation: %v", err)
	}
	if err := p.Stop(changed); err != ErrStale {
		t.Fatal(err)
	}
	if _, err := p.Observe(changed); err != ErrStale {
		t.Fatal(err)
	}
	if reservations != 1 {
		t.Fatal(reservations)
	}
	turn(t, p, "first", "normal")
	observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	_ = p.Stop(binding())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := p.Wait(ctx, binding())
	if err != nil || s.Exit == nil {
		t.Fatalf("Wait: %+v %v", s, err)
	}
	again, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal")})
	if again != p || err != nil {
		t.Fatal("finished operation respawned")
	}
}

func TestClaudeTurnsQuestionsPermissionsInterrupt(t *testing.T) {
	p := start(t, testHost(t, nil), "normal")
	for i, prompt := range []string{"normal", "question", "allow", "deny"} {
		turn(t, p, strconv.Itoa(i), prompt)
		if prompt != "normal" {
			s := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
			token := s.Pending[0]
			response := Response{Allow: true}
			if prompt == "question" {
				response = Response{Answers: map[string]string{"Color?": "blue"}}
			}
			if prompt == "deny" {
				response = Response{Deny: "declined"}
			}
			a := authority(p)
			bad := a
			bad.Binding.Generation = "old"
			if err := p.Respond(context.Background(), bad, token, response); err != ErrStale {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			success := 0
			for range 8 {
				wg.Go(func() {
					err := p.Respond(context.Background(), a, token, response)
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						success++
					} else if err != ErrStale {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if success != 1 {
				t.Fatalf("response writers: %d", success)
			}
		}
		observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	}
	var raw strings.Builder
	for _, e := range events(p) {
		raw.WriteString(string(e.Raw))
	}
	for _, want := range []string{`"answers":{"Color?":"blue"}`, `"behavior":"deny"`, `"behavior":"allow"`} {
		if !strings.Contains(raw.String(), want) {
			t.Fatalf("missing wire %s in %s", want, raw.String())
		}
	}
	turn(t, p, "interrupt", "interrupt")
	observeUntil(t, p, func(s Snapshot) bool {
		for _, e := range events(p) {
			if e.Kind == "output" && e.Turn == "interrupt" {
				return s.Turn == "interrupt"
			}
		}
		return false
	})
	if err := p.Interrupt(context.Background(), authority(p), "interrupt"); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return hasEvent(p, "interrupt-ack") })
	s, _ := p.Observe(binding())
	if s.Turn != "interrupt" || s.Exit != nil {
		t.Fatalf("ack became completion: %+v", s)
	}
	if _, ok := s.Termination(time.Now()); ok {
		t.Fatal("turn interrupt became termination")
	}
	p.mu.Lock()
	err := p.writeLocked(context.Background(), map[string]any{"type": "fixture-release-interrupt"})
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	turn(t, p, "after", "normal")
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
}

func TestRollbackAndActualWait(t *testing.T) {
	t.Run("exec", func(t *testing.T) {
		h := testHost(t, nil)
		cmd := fixtureCommand("normal")
		cmd.Path = "/does-not-exist-processhost"
		p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
		if err == nil {
			t.Fatal("expected exec failure")
		}
		s, _ := p.Observe(binding())
		if s.Exit != nil {
			t.Fatal("invented Wait")
		}
	})
	t.Run("CAS", func(t *testing.T) {
		h := testHost(t, func(tx *Transactions, _ *Limits) {
			tx.Commit = func(context.Context, Binding, string) error { return ErrStale }
		})
		p := start(t, h, "normal")
		turn(t, p, "1", "normal")
		s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
		if s.Exit == nil || hasEvent(p, "ready") {
			t.Fatalf("CAS rollback: %+v", s)
		}
	})
	for _, mode := range []string{"exit0", "exit7", "hup", "stdout-eof"} {
		t.Run(mode, func(t *testing.T) {
			p := start(t, testHost(t, nil), mode)
			s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
			e, ok := s.Termination(time.Now())
			if !ok {
				t.Fatal(s)
			}
			if mode == "exit7" && s.Exit.Code != 7 {
				t.Fatal(s)
			}
			if mode == "hup" && string(e.Classification) != "killed" {
				t.Fatal(e)
			}
			if mode == "exit0" && (s.Failure != "" || hasEvent(p, "protocol-error")) {
				t.Fatal("ordinary exit0 misclassified as protocol failure", s)
			}
			if mode == "stdout-eof" && (s.Failure == "" || !hasEvent(p, "protocol-error")) {
				t.Fatal("EOF without Wait lost protocol failure", s)
			}
			if mode == "stdout-eof" && s.Exit.Signal == "" {
				t.Fatal("EOF fabricated exit0")
			}
		})
	}
}

func TestBoundedStreamsAndControl(t *testing.T) {
	for _, mode := range []string{"malformed", "oversized", "session-drift"} {
		t.Run(mode, func(t *testing.T) {
			p := start(t, testHost(t, nil), mode)
			if mode == "session-drift" {
				turn(t, p, "1", mode)
			}
			s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
			if s.Failure == "" || !hasEvent(p, "protocol-error") {
				t.Fatal(s)
			}
		})
	}
	t.Run("slow-reader", func(t *testing.T) {
		p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 16; l.DiagnosticBytes = 256 }), "normal")
		turn(t, p, "noise", "noise")
		s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
		if !hasEvent(p, "stream-gap") || len(s.Diagnostic) > 256 {
			t.Fatal(s)
		}
		turn(t, p, "next", "question")
		s = observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
		if err := p.Expire(authority(p), s.Pending[0]); err != nil {
			t.Fatal(err)
		}
		if err := p.Respond(context.Background(), authority(p), s.Pending[0], Response{Answers: map[string]string{"Color?": "blue"}}); err != ErrStale {
			t.Fatal(err)
		}
		if hasEvent(p, "control-answered") {
			t.Fatal("auto allow")
		}
	})
	t.Run("blocked-stdin", func(t *testing.T) {
		p := start(t, testHost(t, nil), "blocked")
		began := time.Now()
		err := p.Turn(context.Background(), authority(p), "1", strings.Repeat("x", 512<<10))
		if err == nil || time.Since(began) > time.Second {
			t.Fatalf("unbounded write: %v", err)
		}
		observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
	})
}

func TestStartupHandshakeIsBounded(t *testing.T) {
	for _, mode := range []string{"processhost-unprepared", "processhost-prepared-stall"} {
		t.Run(mode, func(t *testing.T) {
			h := testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 50 * time.Millisecond; l.Grace = 30 * time.Millisecond })
			h.supervisor.Args = []string{mode}
			began := time.Now()
			p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal")})
			if err == nil || time.Since(began) > time.Second {
				t.Fatalf("unbounded handshake: %v", err)
			}
			s, _ := p.Observe(binding())
			if s.Exit != nil {
				t.Fatal("fabricated provider Wait")
			}
			if s.SupervisorPID > 0 && syscall.Kill(s.SupervisorPID, 0) != syscall.ESRCH {
				t.Fatal("supervisor remains")
			}
		})
	}
}

func TestOutputCannotEvictControlOrTermination(t *testing.T) {
	p := start(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 16 }), "normal")
	turn(t, p, "q", "question")
	s := observeUntil(t, p, func(s Snapshot) bool { return len(s.Pending) == 1 })
	if err := p.Respond(context.Background(), authority(p), s.Pending[0], Response{Answers: map[string]string{"Color?": "blue"}}); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	turn(t, p, "n", "noise")
	observeUntil(t, p, func(s Snapshot) bool { return s.Turn == "" })
	_ = p.Stop(binding())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.Wait(ctx, binding())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"control-pending", "control-answered", "turn-result", "process-exited", "stream-gap"} {
		if !hasEvent(p, kind) {
			t.Fatal("lost protected event", kind)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) > 16 || len(p.critical) > 16+p.host.limits.Requests+6 {
		t.Fatal("unbounded queue")
	}
}

func TestLiveMachineGuardHolds(t *testing.T) { liveguard.RequireActive(t) }
