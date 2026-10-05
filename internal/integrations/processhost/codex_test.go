package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

func codexFixture(mode string) {
	if mode == "codex-group" {
		leaf := exec.Command(os.Args[0], "processhost-leaf")
		if err := leaf.Start(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv("PROCESSHOST_LEAF_FILE"), []byte(strconv.Itoa(leaf.Process.Pid)), 0600); err != nil {
			panic(err)
		}
	}
	logFile := os.Getenv("PROCESSHOST_CODEX_LOG")
	out := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	turn := 0
	responses := 0
	current := ""
	emit := func(method string, params any) { _ = out.Encode(map[string]any{"method": method, "params": params}) }
	complete := func() {
		emit("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": current, "status": "completed"}})
	}
	for scanner.Scan() {
		raw := append([]byte{}, scanner.Bytes()...)
		if logFile != "" {
			f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			_, _ = f.Write(append(raw, '\n'))
			_ = f.Close()
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &message) != nil {
			panic("bad request")
		}
		reply := func(result any) { _ = out.Encode(map[string]any{"id": message.ID, "result": result}) }
		switch message.Method {
		case "initialize":
			if mode == "codex-handshake-stall" {
				time.Sleep(time.Hour)
			}
			reply(map[string]any{"userAgent": "projmux/0.160.0"})
		case "initialized":
		case "thread/resume":
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(message.Params, &p)
			if mode == "codex-resume-refused" {
				_ = out.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32600, "message": "recorded thread unavailable"}})
				continue
			}
			id := p.ThreadID
			if mode == "codex-resume-wrong-thread" {
				id = "another-thread"
			}
			reply(map[string]any{"thread": map[string]string{"id": id}, "model": "fixture-model", "reasoningEffort": "high", "sandbox": map[string]string{"type": "readOnly"}, "approvalPolicy": "on-request"})
		case "thread/start":
			var p struct {
				Model    string            `json:"model"`
				Sandbox  string            `json:"sandbox"`
				Approval string            `json:"approvalPolicy"`
				Config   map[string]string `json:"config"`
			}
			_ = json.Unmarshal(message.Params, &p)
			model, effort, policy := p.Model, p.Config["model_reasoning_effort"], p.Approval
			sandbox := map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}[p.Sandbox]
			if mode == "codex-model-mismatch" {
				model = "wrong"
			}
			if mode == "codex-effort-mismatch" {
				effort = "wrong"
			}
			if mode == "codex-policy-mismatch" {
				policy = "never"
			}
			reply(map[string]any{"thread": map[string]string{"id": "thread"}, "model": model, "reasoningEffort": effort, "sandbox": map[string]string{"type": sandbox}, "approvalPolicy": policy})
			emit("thread/started", map[string]any{"thread": map[string]string{"id": "thread"}})
			if mode == "codex-backlog" {
				for range 2048 {
					emit("item/agentMessage/delta", map[string]string{"threadId": "thread", "delta": "flood"})
				}
				time.Sleep(time.Hour)
			}
			if mode == "codex-malformed" {
				fmt.Println("{bad")
				time.Sleep(time.Hour)
			}
			if mode == "codex-oversized" {
				fmt.Println(strings.Repeat("x", 2<<20))
				time.Sleep(time.Hour)
			}
			if mode == "codex-stdout-eof" {
				_ = os.Stdout.Close()
				time.Sleep(time.Hour)
			}
			if mode == "codex-blocked" {
				time.Sleep(time.Hour)
			}
		case "turn/start":
			turn++
			current = fmt.Sprintf("turn-%d", turn)
			var p struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(message.Params, &p)
			if mode == "codex-turn-stall" {
				time.Sleep(time.Hour)
			}
			if mode == "codex-turn-refusal" && turn == 1 {
				_ = out.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32000, "message": "turn refused"}})
				continue
			}
			if mode == "codex-turn-backlog" || mode == "codex-turn-overflow" {
				count := 96
				if mode == "codex-turn-overflow" {
					count = 2048
				}
				for range count {
					emit("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": current, "delta": "before reply"})
				}
			}
			// Intentionally send the first notification before the turn/start reply.
			emit("turn/started", map[string]any{"threadId": "thread", "turn": map[string]string{"id": current}})
			reply(map[string]any{"turn": map[string]string{"id": current}})
			prompt := p.Input[0].Text
			switch prompt {
			case "controls":
				responses = 0
				params := map[string]any{"threadId": "thread", "turnId": current, "itemId": "item", "startedAtMs": 1, "command": "echo fixture", "cwd": "/fixture", "availableDecisions": []string{"accept", "decline", "cancel"}}
				_ = out.Encode(map[string]any{"id": 1, "method": "item/commandExecution/requestApproval", "params": params})
				params = map[string]any{"threadId": "thread", "turnId": current, "itemId": "question", "isBlocking": true, "questions": []any{map[string]any{"id": "q", "question": "Pick", "options": []any{map[string]string{"label": "A"}, map[string]string{"label": "B"}}}}}
				_ = out.Encode(map[string]any{"id": "1", "method": "item/tool/requestUserInput", "params": params})
			case "hold":
			case "flood":
				for range 1024 {
					_, _ = os.Stderr.Write([]byte(strings.Repeat("e", 1024)))
					emit("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": current, "delta": "output"})
					time.Sleep(time.Millisecond)
				}
				complete()
			case "exit":
				complete()
				os.Exit(7)
			default:
				complete()
			}
		case "turn/steer":
			if mode == "codex-steer-overflow" {
				for range 2048 {
					emit("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": current, "delta": "flood"})
				}
				time.Sleep(time.Hour)
			}
			if mode == "codex-steer-requests" || mode == "codex-steer-frame" {
				command := "echo fixture"
				if mode == "codex-steer-frame" {
					command = strings.Repeat("x", 4096)
				}
				for _, id := range []int{2, 3} {
					params := map[string]any{"threadId": "thread", "turnId": current, "itemId": "item", "startedAtMs": 1, "command": command, "cwd": "/fixture", "availableDecisions": []string{"accept", "decline", "cancel"}}
					_ = out.Encode(map[string]any{"id": id, "method": "item/commandExecution/requestApproval", "params": params})
				}
			}
			if mode == "codex-steer-stall" {
				time.Sleep(time.Hour)
			}
			if mode == "codex-steer-queue" || mode == "codex-steer-refusal-queue" {
				for range 96 {
					emit("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": current, "delta": "before steer reply"})
				}
				complete()
			}
			if strings.Contains(mode, "steer-refusal") {
				_ = out.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32000, "message": "steer refused"}})
				continue
			}
			reply(map[string]any{})
		case "turn/interrupt":
			if mode == "codex-interrupt-before-ack" {
				complete()
				reply(map[string]any{})
				continue
			}
			if mode == "codex-interrupt-refusal" {
				_ = out.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32000, "message": "interrupt refused"}})
				continue
			}
			reply(map[string]any{})
			complete()
		default:
			if len(message.Result) > 0 {
				responses++
				if responses == 2 {
					complete()
				}
			} else {
				_ = out.Encode(map[string]any{"id": message.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
			}
		}
	}
}
func codexSettings() CodexConfig {
	return CodexConfig{Version: "0.1.0", DeveloperInstructions: "fixture persona", Settings: codexappserver.ThreadSettings{Model: "fixture-model", Effort: "high", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "on-request"}}}
}

func TestCodexNegotiatedVersionUsesExactOwnedWire(t *testing.T) {
	host := testHost(t, nil)
	handle, launch, _ := codexStart(t, host, "codex-normal")
	got, err := handle.NegotiatedVersion(launch.Binding)
	if err != nil || got != "0.160.0" {
		t.Fatalf("negotiated version %q: %v", got, err)
	}
	foreign := launch.Binding
	foreign.Generation = "foreign"
	if _, err := handle.NegotiatedVersion(foreign); !errors.Is(err, ErrStale) {
		t.Fatalf("foreign generation: %v", err)
	}
}
func codexStart(t *testing.T, host *Host, mode string) (*CodexHandle, Launch, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "wire.jsonl")
	cmd := fixtureCommand(mode)
	cmd.Env = append(cmd.Env, "PROCESSHOST_CODEX_LOG="+log)
	cmd.Dir = t.TempDir()
	launch := Launch{Binding: binding(), Command: cmd}
	c, err := host.StartCodex(context.Background(), launch, codexSettings())
	if c != nil {
		t.Cleanup(func() {
			_ = c.Stop(binding())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.Wait(ctx, binding()); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return c, launch, log
}
func codexAuthority(c *CodexHandle) Authority { return authority(c.handle) }
func codexWire(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var result []map[string]json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		result = append(result, msg)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
func countMethod(wire []map[string]json.RawMessage, method string) int {
	n := 0
	for _, msg := range wire {
		if string(msg["method"]) == strconv.Quote(method) {
			n++
		}
	}
	return n
}

func TestCodexDedicatedThreadSettingsAndOperationRetry(t *testing.T) {
	h := testHost(t, nil)
	c, launch, log := codexStart(t, h, "codex-normal")
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			retry, err := h.StartCodex(context.Background(), launch, codexSettings())
			if retry == nil || retry.handle != c.handle {
				err = errors.New("operation created another handle")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	wire := codexWire(t, log)
	if countMethod(wire, "initialize") != 1 || countMethod(wire, "thread/start") != 1 || len(wire) != 3 {
		t.Fatalf("launch wire=%v", wire)
	}
	var p struct {
		Model, DeveloperInstructions, Sandbox, ApprovalPolicy, Cwd string
		Config                                                     map[string]string
	}
	if err := json.Unmarshal(wire[2]["params"], &p); err != nil {
		t.Fatal(err)
	}
	if p.Model != "fixture-model" || p.Config["model_reasoning_effort"] != "high" || p.Sandbox != "read-only" || p.ApprovalPolicy != "on-request" || p.DeveloperInstructions != "fixture persona" || p.Cwd != launch.Command.Dir {
		t.Fatalf("settings=%+v", p)
	}
	changed := codexSettings()
	changed.Settings.Effort = "low"
	if _, err := h.StartCodex(context.Background(), launch, changed); !errors.Is(err, ErrStale) {
		t.Fatalf("changed retry=%v", err)
	}
	s, _ := c.Observe(binding())
	if s.State != "ready" || s.Session != "thread" || s.Exit != nil {
		t.Fatalf("ready=%+v", s)
	}
}
func TestCodexTurnsQuestionsApprovalsInterrupt(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "op1", "controls"); err != nil {
		t.Fatal(err)
	}
	s := observeUntil(t, c.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	var approval, question Request
	for _, req := range s.Pending {
		if req.Kind == "question" {
			question = req
		} else {
			approval = req
		}
	}
	if approval.ID == question.ID {
		t.Fatal("raw scalar kinds collided")
	}
	wrong := a
	wrong.Connection = "replaced"
	if err := c.RespondApproval(context.Background(), wrong, approval, codexappserver.DecisionAccept); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong connection=%v", err)
	}
	stale := approval
	stale.Turn = "old"
	if err := c.RespondApproval(context.Background(), a, stale, codexappserver.DecisionAccept); !errors.Is(err, ErrStale) {
		t.Fatalf("stale turn=%v", err)
	}
	if err := c.RespondQuestion(context.Background(), a, question, map[int]agentquestion.Selection{0: {Labels: []string{"bad"}}}); err == nil {
		t.Fatal("bad answer allowed")
	}
	if err := c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionAccept); err != nil {
		t.Fatal(err)
	}
	if err := c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionAccept); !errors.Is(err, ErrStale) {
		t.Fatalf("duplicate=%v", err)
	}
	if err := c.RespondQuestion(context.Background(), a, question, map[int]agentquestion.Selection{0: {Labels: []string{"B"}}}); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if err := c.RespondQuestion(context.Background(), a, question, map[int]agentquestion.Selection{0: {Labels: []string{"B"}}}); !errors.Is(err, ErrStale) {
		t.Fatalf("terminal response=%v", err)
	}
	if err := c.Turn(context.Background(), a, "op1", "again"); !errors.Is(err, ErrStale) {
		t.Fatalf("duplicate turn=%v", err)
	}
	if err := c.Turn(context.Background(), a, "op2", "hold"); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn != "" })
	if err := c.Interrupt(context.Background(), a, "old"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := c.Interrupt(context.Background(), a, s.Turn); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if s.Exit != nil || s.State != "ready" || !hasEvent(c.handle, "interrupt-ack") || !hasEvent(c.handle, "turn-result") {
		t.Fatalf("interrupt=%+v", s)
	}
	if err := c.Turn(context.Background(), a, "op3", "exit"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := c.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.Exit.Code != 7 {
		t.Fatalf("actual Wait=%+v %v", s, err)
	}
	wire := codexWire(t, log)
	if countMethod(wire, "thread/start") != 1 || countMethod(wire, "turn/start") != 3 || countMethod(wire, "turn/interrupt") != 1 {
		t.Fatalf("methods=%v", wire)
	}
	var responses []map[string]json.RawMessage
	for _, msg := range wire {
		if len(msg["result"]) > 0 {
			responses = append(responses, msg)
		}
	}
	if len(responses) != 2 || string(responses[0]["id"]) != "1" || string(responses[1]["id"]) != `"1"` || string(responses[1]["result"]) != `{"answers":{"q":{"answers":["B"]}}}` {
		t.Fatalf("exact replies=%v", responses)
	}
}
func TestCodexInitializationFailureRollsBackOnlyOwnedChild(t *testing.T) {
	for _, mode := range []string{"codex-model-mismatch", "codex-effort-mismatch", "codex-policy-mismatch", "codex-handshake-stall", "CAS"} {
		t.Run(mode, func(t *testing.T) {
			commits := 0
			h := testHost(t, func(tx *Transactions, l *Limits) {
				l.Startup = 100 * time.Millisecond
				tx.Commit = func(context.Context, Binding, string) error { commits++; return errors.New("CAS lost") }
			})
			providerMode := mode
			if mode == "CAS" {
				providerMode = "codex-normal"
			}
			launch := Launch{Binding: binding(), Command: fixtureCommand(providerMode)}
			c, err := h.StartCodex(context.Background(), launch, codexSettings())
			if err == nil || c == nil {
				t.Fatalf("failed start=%v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, waitErr := c.Wait(ctx, binding())
			if waitErr != nil || s.State != "exited" || s.Exit == nil || s.Session != "" || hasEvent(c.handle, "ready") {
				t.Fatalf("rollback=%+v err=%v", s, waitErr)
			}
			if mode != "CAS" && commits != 0 {
				t.Fatal("committed before settings proof")
			}
			retry, again := h.StartCodex(context.Background(), launch, codexSettings())
			if retry == nil || retry.handle != c.handle || again == nil {
				t.Fatal("failure retry spawned")
			}
		})
	}
}
func TestCodexBoundedStreamsWaitAndControl(t *testing.T) {
	for _, mode := range []string{"codex-malformed", "codex-oversized", "codex-stdout-eof", "codex-turn-stall", "codex-blocked", "codex-backlog", "codex-normal"} {
		t.Run(mode, func(t *testing.T) {
			h := testHost(t, func(_ *Transactions, l *Limits) { l.Events = 8; l.Startup = 300 * time.Millisecond })
			launch := Launch{Binding: binding(), Command: fixtureCommand(mode)}
			c, err := h.StartCodex(context.Background(), launch, codexSettings())
			if c == nil {
				t.Fatal(err)
			}
			if err == nil {
				prompt := "flood"
				if mode == "codex-blocked" {
					prompt = strings.Repeat("x", 256<<10)
				}
				_ = c.Turn(context.Background(), codexAuthority(c), "bounded", prompt)
				if mode == "codex-normal" {
					s := observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
					e, _, _ := c.Events(binding(), 0)
					if s.Exit != nil || len(e) > h.limits.Events+10 || !hasEvent(c.handle, "stream-gap") || len(s.Diagnostic) > h.limits.DiagnosticBytes {
						t.Fatalf("bounded snapshot=%+v events=%d", s, len(e))
					}
					_ = c.Stop(binding())
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			s, waitErr := c.Wait(ctx, binding())
			if waitErr != nil || s.Exit == nil {
				t.Fatalf("Wait isolated=%+v %v", s, waitErr)
			}
			if mode != "codex-normal" && s.Failure == "" {
				t.Fatalf("missing explicit failure %+v", s)
			}
		})
	}
}

func TestCodexResponseRaceExpiryAndDeny(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "controls", "controls"); err != nil {
		t.Fatal(err)
	}
	s := observeUntil(t, c.handle, func(s Snapshot) bool { return len(s.Pending) == 2 })
	var approval, question Request
	for _, r := range s.Pending {
		if r.Kind == "question" {
			question = r
		} else {
			approval = r
		}
	}
	if err := c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionGrantTurn); err == nil {
		t.Fatal("unsafe decision accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.RespondApproval(ctx, a, approval, codexappserver.DecisionDecline); err == nil {
		t.Fatal("canceled write accepted")
	}
	if err := c.Expire(a, question); err != nil {
		t.Fatal(err)
	}
	if err := c.RespondQuestion(context.Background(), a, question, map[int]agentquestion.Selection{0: {Labels: []string{"A"}}}); !errors.Is(err, ErrStale) {
		t.Fatalf("expired response=%v", err)
	}
	const n = 8
	results := make(chan error, n)
	for range n {
		go func() {
			results <- c.RespondApproval(context.Background(), a, approval, codexappserver.DecisionDecline)
		}()
	}
	success := 0
	for range n {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successful responses=%d", success)
	}
	if err := c.Interrupt(context.Background(), a, s.Turn); err != nil {
		t.Fatal(err)
	}
	observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	wire := codexWire(t, log)
	responses := 0
	for _, msg := range wire {
		if len(msg["result"]) > 0 {
			responses++
			if string(msg["result"]) != `{"decision":"decline"}` {
				t.Fatalf("deny=%s", msg["result"])
			}
		}
	}
	if responses != 1 {
		t.Fatalf("wire responses=%d", responses)
	}
}

func TestCodexTurnRefusalPreservesChildAndSession(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-turn-refusal")
	a := codexAuthority(c)
	before, _ := c.Observe(binding())
	err := c.Turn(context.Background(), a, "refused", "first")
	if err == nil {
		t.Fatal("turn refusal lost")
	}
	s, _ := c.Observe(binding())
	if s.State != "ready" || s.Exit != nil || s.Turn != "" || s.Session != before.Session || s.Connection != before.Connection || syscall.Kill(s.PID, 0) != nil {
		t.Fatalf("refusal killed child or session: %+v", s)
	}
	var result struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	for _, e := range events(c.handle) {
		if e.Kind == "turn-result" {
			_ = json.Unmarshal(e.Raw, &result)
		}
	}
	if result.ThreadID != a.Session || result.Turn.Status != "failed" || result.Turn.ID != "refused" || result.Turn.Error.Message != "server-refused" {
		t.Fatalf("refusal result=%v", result)
	}
	if err := c.Turn(context.Background(), a, "refused", "retry"); !errors.Is(err, ErrStale) {
		t.Fatalf("operation replay=%v", err)
	}
	if err := c.Turn(context.Background(), a, "next", "complete"); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if s.State != "ready" || s.Exit != nil || syscall.Kill(s.PID, 0) != nil {
		t.Fatalf("next turn=%+v", s)
	}
	if countMethod(codexWire(t, log), "turn/start") != 2 {
		t.Fatal("turn replay reached wire")
	}
}

func TestCodexInterruptRefusalPreservesActiveTurn(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-interrupt-refusal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "hold", "hold"); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Observe(binding())
	if err := c.Interrupt(context.Background(), a, before.Turn); !codexappserver.IsResponseError(err) {
		t.Fatalf("refusal=%v", err)
	}
	s, _ := c.Observe(binding())
	if s.State != "ready" || s.Exit != nil || s.Turn != before.Turn || hasEvent(c.handle, "interrupt-ack") || syscall.Kill(s.PID, 0) != nil || !hasEvent(c.handle, "interrupt-refused") {
		t.Fatalf("refusal=%+v", s)
	}
	if countMethod(codexWire(t, log), "turn/interrupt") != 1 {
		t.Fatal("refusal automatically retried")
	}
	if err := c.Interrupt(context.Background(), a, s.Turn); !codexappserver.IsResponseError(err) {
		t.Fatalf("explicit retry=%v", err)
	}
	if countMethod(codexWire(t, log), "turn/interrupt") != 2 {
		t.Fatal("explicit retry missing")
	}
}

func TestCodexTurnBacklogDrainsBeforeReply(t *testing.T) {
	c, _, _ := codexStart(t, testHost(t, nil), "codex-turn-backlog")
	if err := c.Turn(context.Background(), codexAuthority(c), "backlog", "done"); err != nil {
		t.Fatal(err)
	}
	s := observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if s.State != "ready" || s.Exit != nil {
		t.Fatalf("backlog: %+v", s)
	}
}
func TestCodexExplicitInterruptRetryAfterRefusal(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-interrupt-refusal")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "hold", "hold"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Observe(binding())
	for range 2 {
		if err := c.Interrupt(context.Background(), a, s.Turn); !codexappserver.IsResponseError(err) {
			t.Fatalf("explicit refusal retry: %v", err)
		}
	}
	if countMethod(codexWire(t, log), "turn/interrupt") != 2 {
		t.Fatal("explicit retry missing")
	}
}

func TestCodexStartQueueOverflowIsBoundedAndExplicit(t *testing.T) {
	c, _, _ := codexStart(t, testHost(t, func(_ *Transactions, l *Limits) { l.Events = 128 }), "codex-turn-overflow")
	_ = c.Turn(context.Background(), codexAuthority(c), "overflow", "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := c.Wait(ctx, binding())
	if err != nil || s.Exit == nil || s.Failure == "" {
		t.Fatalf("queue overflow: %+v %v", s, err)
	}
}

func TestCodexInterruptResultBeforeAckKeepsExactTurn(t *testing.T) {
	c, _, _ := codexStart(t, testHost(t, nil), "codex-interrupt-before-ack")
	a := codexAuthority(c)
	if err := c.Turn(context.Background(), a, "hold", "hold"); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Observe(binding())
	turn := s.Turn
	if err := c.Interrupt(context.Background(), a, turn); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, c.handle, func(s Snapshot) bool { return s.Turn == "" })
	if s.Exit != nil || s.State != "ready" {
		t.Fatalf("interrupt: %+v", s)
	}
	for _, e := range events(c.handle) {
		if e.Kind == "interrupt-ack" && e.Turn != turn {
			t.Fatalf("ack wrong turn: %+v", e)
		}
	}
	if err := c.Turn(context.Background(), a, "next", "hold"); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLifecycleOwnershipBeforeInitKeepsSessionControlsFenced(t *testing.T) {
	handle := start(t, testHost(t, nil), "normal")
	a := authority(handle)
	a.Session = "hook-reservation"
	if err := handle.ValidateAuthority(context.Background(), a); !errors.Is(err, ErrStale) {
		t.Fatalf("uninitialized session authority: %v", err)
	}
	if err := handle.ValidateOwnership(context.Background(), a.Binding); err != nil {
		t.Fatal(err)
	}
	foreign := a.Binding
	foreign.Generation = "foreign"
	if err := handle.ValidateOwnership(context.Background(), foreign); !errors.Is(err, ErrStale) {
		t.Fatalf("foreign lifecycle: %v", err)
	}
	if err := handle.Stop(a.Binding); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := handle.Wait(ctx, a.Binding); err != nil {
		t.Fatal(err)
	}
}
