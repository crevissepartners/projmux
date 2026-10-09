package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func exitIfCodexProcessFixtureChild() {
	if os.Getenv("PMX_TEST_PROCESS_CODEX_CHILD") != "1" || len(os.Args) < 3 || os.Args[1] != "internal" || os.Args[2] != "codex-process-test-supervisor" {
		return
	}
	if err := processhost.ServeSupervisor(os.NewFile(3, "lifetime"), os.NewFile(4, "spec"), os.NewFile(5, "status")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

const processCodexProviderFixture = `
import os,sys,json
turn=0
current=''
responses=0
log=os.path.join(os.environ['HOME'],'wire.jsonl')
def emit(n):print(json.dumps(n),flush=True)
def notify(method,params):emit({'method':method,'params':params})
def complete():notify('turn/completed',{'threadId':'process-thread','turn':{'id':current,'status':'completed'}})
for line in sys.stdin:
 with open(log,'a') as f:f.write(line)
 n=json.loads(line)
 def reply(result):emit({'id':n['id'],'result':result})
 method=n.get('method','')
 if method=='initialize':reply({'userAgent':'projmux/0.160.0'})
 elif method=='initialized':pass
 elif method=='thread/start':
  p=n['params'];reply({'thread':{'id':'process-thread'},'model':p['model'],'reasoningEffort':p['config']['model_reasoning_effort'],'sandbox':{'type':'readOnly'},'approvalPolicy':'on-request'})
 elif method=='turn/start':
  turn+=1;current='turn-'+str(turn);responses=0
  notify('turn/started',{'threadId':'process-thread','turn':{'id':current}})
  reply({'turn':{'id':current}})
  prompt=n['params']['input'][0]['text']
  if prompt=='controls' or prompt.startswith('{'):
   a={'id':turn,'method':'item/commandExecution/requestApproval','params':{'threadId':'process-thread','turnId':current,'itemId':'command','startedAtMs':1,'command':'echo fixture','cwd':'/fixture','availableDecisions':['accept','decline','cancel']}}
   q={'id':str(turn),'method':'item/tool/requestUserInput','params':{'threadId':'process-thread','turnId':current,'itemId':'question','isBlocking':True,'questions':[{'id':'q','question':'Color?','options':[{'label':'blue'},{'label':'red'}]}]}}
   emit(a);emit(q);emit(a);emit(q)
  elif prompt!='hold':complete()
 elif method=='turn/interrupt':reply({});complete()
 elif 'result' in n:
  responses+=1
  if responses==2:complete()
`

type processCodexFixture struct {
	root, path, binary string
	endpoint           *codexProcessEndpoint
	control            *codexProcessControl
	store              *intmetadata.Store
}

func newProcessCodexFixture(t *testing.T, command func(string, string, []string) processhost.Command) *processCodexFixture {
	return newProcessCodexFixtureWithEvents(t, command, processhost.DefaultLimits().Events)
}

func newProcessCodexFixtureWithEvents(t *testing.T, command func(string, string, []string) processhost.Command, events int) *processCodexFixture {
	t.Helper()
	// Keep copied supervisor/provider paths short on both supported platforms.
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	t.Logf("isolated provider HOME=%s", root)
	binary := filepath.Join(root, "projmux.test")
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	h := newSessionRefHarness(t, "codex")
	encoded, _ := json.Marshal(h.registry)
	suffix := filepath.Base(root)
	agentUID, paneUID := h.agentUID+"-"+suffix, h.paneUID+"-"+suffix
	encoded = bytes.ReplaceAll(encoded, []byte(h.agentUID), []byte(agentUID))
	encoded = bytes.ReplaceAll(encoded, []byte(h.paneUID), []byte(paneUID))
	if err = json.Unmarshal(encoded, h.registry); err != nil {
		t.Fatal(err)
	}
	agent, _ := h.registry.Agent(agentUID)
	pane, _ := h.registry.Pane(paneUID)
	window, _ := h.registry.Window(agent.Metadata.OwnerUID())
	pane.Status.Activation.RuntimeID = ""
	b := processhost.Binding{Host: "host-" + suffix, Project: window.Metadata.OwnerUID(), Window: window.Metadata.UID, Agent: agentUID, Pane: paneUID, Generation: pane.Status.Activation.Generation, Operation: pane.Status.Activation.OperationID}

	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	agent.Status.Activation = coremetadata.AgentActivation{}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] = coremetadata.QuestionChannelOn
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: "codex", Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, ResumeState: coremetadata.ProcessResumeUnknown}
	path := intmetadata.PathFor(filepath.Join(root, "state", "projmux"))
	store := intmetadata.NewStore(path)
	if _, err = store.Update(func(r *coremetadata.Registry) error { *r = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	current := func(ctx context.Context, got processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if got != b {
			return processhost.ErrStale
		}
		r, err := store.LoadDegradedReadOnly()
		if err != nil {
			return err
		}
		p, ok := r.Pane(b.Pane)
		if !ok || p.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || p.Status.ProcessSession == nil || processSchemaBinding(p.Status.ProcessSession.Binding) != b {
			return processhost.ErrStale
		}
		return nil
	}
	tx := processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, session string) error { return current(ctx, b) }}
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key == "HOME" || key == "CODEX_HOME" || key == "XDG_STATE_HOME" || key == "OPENAI_API_KEY" || key == "CODEX_API_KEY" || key == "TMUX" || key == "TMUX_PANE" || key == "__PROJMUX_RUNTIME_ANCHOR_PANE" || strings.HasPrefix(key, "PMX_INTERNAL_") {
			continue
		}
		env = append(env, v)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	env = append(env, "HOME="+root, "CODEX_HOME="+filepath.Join(root, ".codex"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "PMX_TEST_PROCESS_CODEX_CHILD=1")
	limits := processhost.DefaultLimits()
	limits.Events = events
	limits.Grace = 200 * time.Millisecond
	limits.Startup = 8 * time.Second
	supervisor := processhost.Command{Path: binary, Args: []string{"internal", "codex-process-test-supervisor"}, Env: env}
	if product := os.Getenv("PMX_TEST_CLI"); product != "" {
		supervisor.Path = product
		supervisor.Args = []string{"internal", "process-host-supervisor"}
	}
	host, err := processhost.NewHost(b.Host, supervisor, tx, limits)
	if err != nil {
		t.Fatal(err)
	}
	cmd := processhost.Command{Path: "python3", Args: []string{"-u", "-c", processCodexProviderFixture}, Dir: root, Env: env}
	cfg := processhost.CodexConfig{Version: "0.1.0", Settings: codexappserver.ThreadSettings{Model: "stub-model", Effort: "low", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "on-request"}}}
	if command != nil {
		cmd = command(root, binary, env)
	}
	endpoint, err := startProcessCodex(context.Background(), host, processhost.Launch{Binding: b, Command: cmd}, cfg, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = endpoint.handle.Stop(b)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := endpoint.handle.Wait(ctx, b)
		if err != nil || s.Exit == nil {
			t.Errorf("actual Wait: %+v %v", s, err)
		}
		leaseDir := filepath.Dir(endpoint.socket)
		if _, err := os.Lstat(leaseDir); !os.IsNotExist(err) {
			t.Errorf("host lease remains after actual Wait: %s (%v)", leaseDir, err)
		}
	})
	f := &processCodexFixture{root: root, path: path, binary: binary, endpoint: endpoint, store: store}
	f.control = &codexProcessControl{endpoint: endpoint, questions: agentquestion.NewStore(filepath.Join(root, "answers")), approvals: agentapproval.NewStore(filepath.Join(root, "answers")), questionWindow: time.Minute, approvalWindow: time.Minute, now: time.Now}
	return f
}
func (f *processCodexFixture) wait(t *testing.T, predicate func(processhost.Snapshot) bool) processhost.Snapshot {
	t.Helper()
	return f.waitNamed(t, "predicate deadline", predicate)
}
func (f *processCodexFixture) waitNamed(t *testing.T, description string, predicate func(processhost.Snapshot) bool) processhost.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := f.endpoint.handle.Observe(f.endpoint.binding)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	s, _ := f.endpoint.handle.Observe(f.endpoint.binding)
	t.Fatalf("%s: last snapshot=%+v; complete wire rows=%d", description, s, len(f.pollWire(t)))
	return s
}
func (f *processCodexFixture) turn(t *testing.T, operation, prompt string) {
	t.Helper()
	if err := f.endpoint.handle.Turn(context.Background(), f.endpoint.authority(), operation, prompt); err != nil {
		t.Fatal(err)
	}
}
func (f *processCodexFixture) answerControls(t *testing.T) {
	t.Helper()
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, err := f.control.questions.List(f.endpoint.binding.Agent)
	if err != nil || len(q) != 1 {
		t.Fatalf("questions %v %v", q, err)
	}
	a, err := f.control.approvals.List(f.endpoint.binding.Agent)
	if err != nil || len(a) != 1 {
		t.Fatalf("approvals %v %v", a, err)
	}
	if _, err = f.control.questions.Answer(q[0].ID, f.endpoint.binding.Agent, map[string]string{"q": `["blue"]`}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.control.approvals.Answer(a[0].ID, f.endpoint.binding.Agent, false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
}
func (f *processCodexFixture) wire(t *testing.T) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]json.RawMessage
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		var n map[string]json.RawMessage
		if json.Unmarshal(line, &n) != nil {
			t.Fatal("bad wire")
		}
		result = append(result, n)
	}
	return result
}

// Polling may observe an append before its final newline. Only that final
// fragment is deferred; a malformed complete line still fails immediately.
func (f *processCodexFixture) pollWire(t *testing.T) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	rows, err := processCodexCompleteWire(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func processCodexCompleteWire(raw []byte) ([]map[string]json.RawMessage, error) {
	var rows []map[string]json.RawMessage
	complete := raw[:bytes.LastIndexByte(raw, '\n')+1]
	if len(complete) == 0 {
		return nil, nil
	}
	for line := range bytes.SplitSeq(bytes.TrimSuffix(complete, []byte("\n")), []byte("\n")) {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("malformed complete wire line: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func TestProcessCodexCompleteWireDefersOnlyPartialLastLine(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		rows       int
		invalid    bool
	}{
		{"empty", "", 0, false},
		{"partial", `{"id":`, 0, false},
		{"complete and partial", "{\"id\":1}\n{\"id\":", 1, false},
		{"complete", "{\"id\":1}\n", 1, false},
		{"malformed complete", "{bad}\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := processCodexCompleteWire([]byte(tc.wire))
			if (err != nil) != tc.invalid || len(rows) != tc.rows {
				t.Fatalf("rows=%d error=%v", len(rows), err)
			}
		})
	}
}
func TestCodexProcessBindingSingleWriterAndExactTokens(t *testing.T) {
	t.Run("fixture-state-journal", func(t *testing.T) {
		parent := t.TempDir()
		sentinel := filepath.Join(parent, "sentry")
		if err := os.WriteFile(sentinel, []byte("parent state stays private"), 0600); err != nil {
			t.Fatal(err)
		}
		snapshotParent := func() map[string]string {
			t.Helper()
			state := map[string]string{}
			err := filepath.Walk(parent, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(parent, path)
				if err != nil {
					return err
				}
				value := fmt.Sprintf("mode=%v", info.Mode())
				if info.Mode().IsRegular() {
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					value += fmt.Sprintf(" bytes=%x", raw)
				} else if info.Mode()&os.ModeSymlink != 0 {
					target, err := os.Readlink(path)
					if err != nil {
						return err
					}
					value += " link=" + target
				}
				state[rel] = value
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := snapshotParent()
		t.Setenv("XDG_STATE_HOME", parent)
		var fixtureEnv []string
		fixture := newProcessCodexFixture(t, func(root, _ string, env []string) processhost.Command {
			fixtureEnv = append([]string{}, env...)
			stateHomes := 0
			for _, entry := range env {
				if value, ok := strings.CutPrefix(entry, "XDG_STATE_HOME="); ok {
					stateHomes++
					if value != filepath.Join(root, "state") {
						t.Fatalf("fixture inherited parent state home: %q", value)
					}
				}
			}
			if stateHomes != 1 {
				t.Fatalf("fixture state homes=%d", stateHomes)
			}
			return processhost.Command{Path: "python3", Args: []string{"-u", "-c", processCodexProviderFixture}, Dir: root, Env: env}
		})
		if product := os.Getenv("PMX_TEST_CLI"); product != "" {
			// Reject before inherited FD admission: this is a separate bounded
			// usage call, not the fixture's live owned supervisor.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output, err := os.CreateTemp(fixture.root, "journal-contract-output-")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			cmd := exec.CommandContext(ctx, product, "internal", "process-host-supervisor", "--fixture-refusal")
			cmd.Env = append([]string{}, fixtureEnv...)
			cmd.Stdout, cmd.Stderr = output, output
			err = cmd.Run()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("actual supervisor usage receipt: %v, context=%v", err, ctx.Err())
			}
			if info, err := output.Stat(); err != nil || info.Size() > 4096 {
				t.Fatalf("usage output unbounded: %v %v", info, err)
			}
			journal := filepath.Join(fixture.root, "state", "projmux", "logs", "operations.jsonl")
			info, err := os.Stat(journal)
			if err != nil || info.Size() > 65536 {
				t.Fatalf("fixture journal missing or unbounded: %v %v", info, err)
			}
			raw, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
				var event struct {
					Component, Event, Result, Kind, Message, Command, Subcommand string
				}
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if event.Component == "cli" && event.Event == "command.outcome" && event.Result == "error" && event.Kind == "usage" && event.Message == "invalid command usage" && event.Command == "" && event.Subcommand == "" {
					found = true
				}
			}
			if !found || bytes.Contains(raw, []byte("--fixture-refusal")) || bytes.Contains(raw, []byte(parent)) {
				t.Fatal("actual usage outcome absent or journal contains argv/parent state")
			}
			t.Log("actual copied CLI usage2 journal stayed in fixture state")
		} else {
			t.Log("unit fixture environment checked; actual copied CLI journal branch requires process-host CLI gate")
		}
		if err := fixture.endpoint.handle.Stop(fixture.endpoint.binding); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if result, err := fixture.endpoint.handle.Wait(ctx, fixture.endpoint.binding); err != nil || result.Exit == nil {
			t.Fatalf("fixture actual Wait: %+v %v", result, err)
		}
		after := snapshotParent()
		if len(after) != len(before) {
			t.Fatalf("parent state union changed: before=%v after=%v", before, after)
		}
		for path, value := range before {
			if after[path] != value {
				t.Fatalf("parent state metadata/content changed at %s", path)
			}
		}
	})
	f := newProcessCodexFixture(t, nil)
	e := f.endpoint
	if _, err := e.call(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	f.turn(t, "controls", "controls")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	f.answerControls(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := f.control.sync(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	count := 0
	for _, n := range f.wire(t) {
		if len(n["result"]) > 0 {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("response wires=%d", count)
	}
	for _, r := range snap.Pending {
		var err error
		if r.Kind == "question" {
			err = e.handle.RespondQuestion(context.Background(), e.authority(), r, map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}})
		} else {
			err = e.handle.RespondApproval(context.Background(), e.authority(), r, codexappserver.DecisionAccept)
		}
		if !errors.Is(err, processhost.ErrStale) {
			t.Fatalf("replayed: %v", err)
		}
	}
	before := e.evidence
	for _, change := range []func(*coremetadata.CodexProcessRouteEvidence){func(p *coremetadata.CodexProcessRouteEvidence) { p.HostInstance = "self-claimed" }, func(p *coremetadata.CodexProcessRouteEvidence) { p.Generation = "old" }, func(p *coremetadata.CodexProcessRouteEvidence) { p.Process.Start = "stale-birth" }, func(p *coremetadata.CodexProcessRouteEvidence) { p.Process.PID++ }, func(p *coremetadata.CodexProcessRouteEvidence) { p.ThreadID = "foreign" }, func(p *coremetadata.CodexProcessRouteEvidence) { p.Connection = "replaced" }, func(p *coremetadata.CodexProcessRouteEvidence) { p.HostProcess.Start = "foreign-host-birth" }} {
		bad := before
		change(&bad)
		reg, _ := f.store.LoadDegradedReadOnly()
		if _, reason := coremetadata.ResolveProcessCodexRoute(reg, e.binding.Agent, bad, func(proof coremetadata.CodexProcessRouteEvidence) bool { return e.current(context.Background(), proof) }); reason == "" {
			t.Fatal("self assertion registered")
		}
	}
}

func TestCodexProcessHooksAreObservationOnlyWithStaleClaims(t *testing.T) {
	h := newSessionRefHarness(t, "codex")
	base := h.cmd.lookupEnv
	h.cmd.lookupEnv = func(key string) string {
		if key == internalCodexProcessBindingEnv {
			return "malformed self-claim"
		}
		return base(key)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PermissionRequest", "Stop", "SessionEnd"} {
		payload, _ := json.Marshal(map[string]string{"hook_event_name": event, "thread_id": "foreign-thread"})
		if err := h.cmd.ingestCodexHook(payload, "%7"); err != nil {
			t.Fatal(err)
		}
	}
	if h.updates != 0 || h.loads != 0 || len(h.tmuxCalls) != 0 {
		t.Fatalf("hook wrote state: %d %d %v", h.updates, h.loads, h.tmuxCalls)
	}
	command := &agentCommand{lookupEnv: h.cmd.lookupEnv}
	if err := command.codexPermissionApproval(agentPermissionRequest{action: "answer", spelling: "fixture"}, *h.registry, coremetadata.Agent{}, nil, nil); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("hook control: %v", err)
	}
}
func TestCodexProcessTimeoutDisconnectAndGenerationNeverAutoAllow(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect", "generation"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcessCodexFixture(t, nil)
			e := f.endpoint
			now := time.Now()
			f.control.now = func() time.Time { return now }
			f.control.questions = f.control.questions.WithClock(func() time.Time { return now })
			f.control.approvals = f.control.approvals.WithClock(func() time.Time { return now })
			f.turn(t, "pending", "controls")
			f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
			if err := f.control.sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "timeout":
				now = now.Add(2 * time.Minute)
			case "disconnect":
				_ = e.handle.Stop(e.binding)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = e.handle.Wait(ctx, e.binding)
			case "generation":
				_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					p, _ := reg.Pane(e.binding.Pane)
					p.Status.Activation.Generation = "replaced-generation"
					p.Status.Activation.Process.Binding.Generation = "replaced-generation"
					p.Status.ProcessSession.Binding.Generation = "replaced-generation"
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := f.control.sync(context.Background())
			if mode != "timeout" && err == nil {
				t.Fatal("stale control accepted")
			}
			if mode == "timeout" && err != nil {
				t.Fatal(err)
			}
			for _, n := range f.wire(t) {
				if len(n["result"]) != 0 {
					t.Fatal("auto allow")
				}
			}
		})
	}
}
func TestCodexProcessInterruptDoesNotWriteTermination(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	e := f.endpoint
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.turn(t, "hold", "hold")
	s := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
	if err = e.handle.Interrupt(context.Background(), e.authority(), s.Turn); err != nil {
		t.Fatal(err)
	}
	s = f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if s.Exit != nil || s.State != "ready" {
		t.Fatalf("interrupt exit: %+v", s)
	}
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("interrupt changed Registry or LastTermination")
	}
}
func TestCodexProcessBidirectionalEndpointReceiptsAndReplayWireZero(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixture(t, nil)
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	left.endpoint.messages.Store(m)
	right.endpoint.messages.Store(m)
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	first, err := m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-process-first", "conversation-process", "coordination only", now, deadline)
	if err != nil || first.Delivery.State != "delivered" {
		t.Fatalf("first receipt: %+v %v", first, err)
	}
	right.answerControls(t)
	reply, err := m.reply(context.Background(), right.endpoint.binding.Agent, left.endpoint.binding.Agent, "message-process-first", "message-process-reply", "bounded reply", time.Now().UTC(), deadline)
	if err != nil || reply.Delivery.State != "delivered" {
		t.Fatalf("reply receipt: %+v %v", reply, err)
	}
	left.answerControls(t)
	for range 3 {
		receipt, err := m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-process-first", "conversation-process", "coordination only", now, deadline)
		if err != nil || receipt != first {
			t.Fatalf("retry: %+v %v", receipt, err)
		}
	}
	starts := func(f *processCodexFixture) int {
		count := 0
		for _, n := range f.wire(t) {
			if string(n["method"]) == `"turn/start"` {
				count++
			}
		}
		return count
	}
	if starts(left) != 1 || starts(right) != 1 {
		t.Fatal("duplicate message wrote a turn")
	}
	reg, _ := right.store.LoadDegradedReadOnly()
	p, _ := reg.Pane(right.endpoint.binding.Pane)
	p.Status.Activation.Generation = "old-generation"
	p.Status.Activation.Process.Binding.Generation = "old-generation"
	p.Status.ProcessSession.Binding.Generation = "old-generation"
	_, _, err = right.store.UpdateConvergent(func(r *coremetadata.Registry) error { *r = reg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-stale", "conversation-stale", "never written", now, deadline); !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("stale route: %v", err)
	}
	if starts(right) != 1 {
		t.Fatal("stale message wrote")
	}
}

func TestCodexProcessEndpointRejectsSameUserForgedPeer(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	e := f.endpoint
	request, _ := json.Marshal(codexProcessExchange{Binding: e.binding, Evidence: e.evidence})
	probe := exec.Command("python3", "-c", `import socket,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(3);s.connect(sys.argv[1]);s.sendall(sys.argv[2].encode());s.shutdown(socket.SHUT_WR);assert s.recv(4096)==b''
`, e.socket, string(request))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("forged peer: %s %v", out, err)
	}
	if len(f.wire(t)) != 3 {
		t.Fatal("forged peer wrote provider wire")
	}
	if _, err := e.call(context.Background(), ""); err != nil {
		t.Fatal("real host refused", err)
	}
}

// processCodexSteerFixture answers turn/steer with the given Python statement.
func processCodexSteerFixture(steer string) func(string, string, []string) processhost.Command {
	return func(root, binary string, env []string) processhost.Command {
		fixture := strings.Replace(processCodexProviderFixture, " elif method=='turn/interrupt':", " elif method=='turn/steer':"+steer+"\n elif method=='turn/interrupt':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", fixture}, Dir: root, Env: env}
	}
}

// processCodexTurnWrites counts turn/start and turn/steer and returns every
// steer's expected turn and input text in wire order.
func processCodexTurnWrites(t *testing.T, f *processCodexFixture) (starts int, steers []map[string]string) {
	t.Helper()
	for _, n := range f.pollWire(t) {
		switch string(n["method"]) {
		case `"turn/start"`:
			starts++
		case `"turn/steer"`:
			var params struct {
				ExpectedTurnID string `json:"expectedTurnId"`
				Input          []struct{ Text string }
			}
			if err := json.Unmarshal(n["params"], &params); err != nil || len(params.Input) != 1 {
				t.Fatalf("steer params: %s %v", n["params"], err)
			}
			steers = append(steers, map[string]string{"turn": params.ExpectedTurnID, "text": params.Input[0].Text})
		}
	}
	return starts, steers
}

func TestCodexProcessMessageSteersRunningTurnAndStaleReceiptNeverWrites(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixture(t, processCodexSteerFixture("reply({})"))
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	left.endpoint.messages.Store(m)
	right.endpoint.messages.Store(m)
	ctx := context.Background()
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	right.turn(t, "hold", "hold")
	running := right.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" }).Turn
	const steered = 10
	for i := range steered {
		ref := fmt.Sprintf("message-steer-%d", i)
		receipt, err := m.send(ctx, left.endpoint.binding.Agent, right.endpoint.binding.Agent, ref, "conversation-steer", fmt.Sprintf("steer %d", i), now, deadline)
		if err != nil || receipt.Delivery.State != coremessage.StateDelivered || receipt.Delivery.Reason != "host-turn-steered" || receipt.Delivery.OutcomeUnknown {
			t.Fatalf("steer %d: %+v %v", i, receipt, err)
		}
	}
	starts, steers := processCodexTurnWrites(t, right)
	if starts != 1 || len(steers) != steered {
		t.Fatalf("running turn writes: starts=%d steers=%d", starts, len(steers))
	}
	for i, steer := range steers {
		var frame map[string]any
		if err := json.Unmarshal([]byte(steer["text"]), &frame); err != nil {
			t.Fatalf("steer %d is not the coordination envelope: %q %v", i, steer["text"], err)
		}
		// The envelope stays peer coordination; it never becomes operator input.
		if steer["turn"] != running || frame["kind"] != "projmux-coordination" || frame["authority"] != "untrusted-coordination-only" ||
			frame["messageRef"] != fmt.Sprintf("message-steer-%d", i) || frame["payload"] != fmt.Sprintf("steer %d", i) {
			t.Fatalf("steer %d: turn=%s frame=%v", i, steer["turn"], frame)
		}
	}
	if snap := right.wait(t, func(s processhost.Snapshot) bool { return true }); snap.Turn != running {
		t.Fatalf("steer replaced the running turn: %+v", snap)
	}
	var err error
	from, _ := processCodexMessageRoute(ctx, left.endpoint)
	to, _ := processCodexMessageRoute(ctx, right.endpoint)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-stale-receipt", ConversationRef: "conversation-stale", Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: "stale", AcceptedAt: now, Deadline: deadline}
	if _, _, err = m.store.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	_, _, err = left.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, _ := reg.Pane(left.endpoint.binding.Pane)
		p.Status.Activation.Generation = "new-generation"
		p.Status.Activation.Process.Binding.Generation = "new-generation"
		p.Status.ProcessSession.Binding.Generation = "new-generation"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := right.endpoint.call(ctx, envelope.MessageRef)
	if err != nil || result.Receipt == nil || result.Receipt.Delivery.State != coremessage.StateStale {
		t.Fatalf("stale receipt: %+v %v", result, err)
	}
	if starts, steers = processCodexTurnWrites(t, right); starts != 1 || len(steers) != steered {
		t.Fatalf("stale wire writes: starts=%d steers=%d", starts, len(steers))
	}
}

func TestCodexProcessMessageIdleStartsTurn(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixture(t, processCodexSteerFixture("reply({})"))
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	right.endpoint.messages.Store(m)
	now := time.Now().UTC()
	receipt, err := m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-idle", "conversation-idle", "idle", now, now.Add(time.Minute))
	if err != nil || receipt.Delivery.State != coremessage.StateDelivered || receipt.Delivery.Reason != "host-turn-accepted" {
		t.Fatalf("idle: %+v %v", receipt, err)
	}
	if starts, steers := processCodexTurnWrites(t, right); starts != 1 || len(steers) != 0 {
		t.Fatalf("idle writes: starts=%d steers=%d", starts, len(steers))
	}
}

// A steer the provider refuses or never answers settles with its reason and is
// never retried as a start or another steer.
func TestCodexProcessMessageFailedSteerSettlesWithoutStart(t *testing.T) {
	for _, test := range []struct {
		name, steer, reason string
		state               coremessage.State
		unknown             bool
	}{
		{"refused", "emit({'id':n['id'],'error':{'code':-32600,'message':'no active turn'}})", "provider-refused", coremessage.StateRefused, false},
		{"lost", "sys.exit(0)", "delivery-outcome-unknown", coremessage.StateFailed, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			left := newProcessCodexFixture(t, nil)
			right := newProcessCodexFixture(t, processCodexSteerFixture(test.steer))
			m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
			right.endpoint.messages.Store(m)
			right.turn(t, "hold", "hold")
			right.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
			now := time.Now().UTC()
			receipt, err := m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-failed-steer", "conversation-failed", "fails", now, now.Add(time.Minute))
			if err != nil || receipt.Delivery.State != test.state || receipt.Delivery.Reason != test.reason || receipt.Delivery.OutcomeUnknown != test.unknown {
				t.Fatalf("failed steer: %+v %v", receipt, err)
			}
			if starts, steers := processCodexTurnWrites(t, right); starts != 1 || len(steers) != 1 {
				t.Fatalf("failed steer retried: starts=%d steers=%d", starts, len(steers))
			}
		})
	}
}

func processCodexOutcomeCount(m *codexProcessMessages) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.outcomes)
}

func TestCodexProcessReceiptCommitRetryNeverReplaysTurn(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	store := messagestore.NewNonblockingStore(filepath.Join(left.root, "messages"))
	release := filepath.Join(left.root, "release-receipt-lock")
	released := filepath.Join(left.root, "receipt-lock-released")
	right := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		script := fmt.Sprintf("import fcntl,time\nreceipt_path=%q\nrelease_path=%q\nreleased_path=%q\n", store.Path()+".flock", release, released) + processCodexProviderFixture
		script = strings.Replace(script, "  reply({'turn':{'id':current}})", "  lock=open(receipt_path,'a')\n  fcntl.flock(lock,fcntl.LOCK_EX)\n  reply({'turn':{'id':current}})", 1)
		script = strings.Replace(script, " elif method=='turn/interrupt':", "  while not os.path.exists(release_path):time.sleep(0.002)\n  fcntl.flock(lock,fcntl.LOCK_UN);lock.close()\n  open(released_path,'w').close()\n elif method=='turn/interrupt':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Dir: root, Env: env}
	})
	m := &codexProcessMessages{store: store, endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	right.endpoint.messages.Store(m)
	ctx := context.Background()
	now := time.Now().UTC()
	from, _ := processCodexMessageRoute(ctx, left.endpoint)
	to, _ := processCodexMessageRoute(ctx, right.endpoint)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-receipt-commit", ConversationRef: "conversation-commit", Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: "hold", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := store.PutAccepted(envelope, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	// The provider locks the real store after Get, before turn/start acceptance.
	// ApplyMatching therefore actually fails; no successful receipt is rolled back.
	if _, err := m.receive(ctx, right.endpoint, envelope.MessageRef); !errors.Is(err, messagestore.ErrBusy) {
		t.Fatalf("receipt commit=%v", err)
	}
	if processCodexOutcomeCount(m) != 1 {
		t.Fatalf("uncommitted outcomes=%d", processCodexOutcomeCount(m))
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		if _, err := os.Stat(released); err == nil {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("receipt lock not released")
		case <-time.After(time.Millisecond):
		}
	}
	second, err := right.endpoint.call(ctx, envelope.MessageRef)
	if err != nil || second.Receipt == nil || second.Receipt.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("commit retry: %+v %v", second, err)
	}
	if processCodexOutcomeCount(m) != 0 {
		t.Fatalf("committed outcomes=%d", processCodexOutcomeCount(m))
	}
	count := 0
	for _, n := range right.wire(t) {
		if string(n["method"]) == `"turn/start"` {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("receipt retry wrote %d turns", count)
	}
}

func TestCodexProcessCommittedReceiptsDoNotExhaustOutcomeCache(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixtureWithEvents(t, func(root, binary string, env []string) processhost.Command {
		script := strings.Replace(processCodexProviderFixture, "if prompt=='controls' or prompt.startswith('{'):", "if prompt=='controls':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", script}, Dir: root, Env: env}
	}, 2048) // Test only: keep the existing host lifetime limit out of this cache test.
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}}
	right.endpoint.messages.Store(m)
	ctx := context.Background()
	retries := 0
	for i := range 258 {
		now := time.Now().UTC()
		messageRef, conversationRef := fmt.Sprintf("message-cache-%d", i), fmt.Sprintf("conversation-cache-%d", i)
		deadline := now.Add(time.Minute)
		if i == 0 {
			// Force a bounded IPC failure while the server waits for the receipt lock.
			// Keep the injected caller deadline shorter than the server exchange budget.
			// The lost reply is explicit; retrying the same envelope must not replay a
			// turn whose outcome was already committed by the first exchange.
			m.mu.Lock()
			first, cancel := context.WithTimeout(ctx, time.Second)
			_, err := m.send(first, left.endpoint.binding.Agent, right.endpoint.binding.Agent, messageRef, conversationRef, "complete", now, deadline)
			cancel()
			m.mu.Unlock()
			if err == nil || !strings.Contains(err.Error(), "bounded frame read failed") {
				t.Fatalf("forced IPC timeout must be explicit: %v", err)
			}
			retries++
		}
		retryCtx, cancel := context.WithDeadline(ctx, deadline)
		var receipt codexProcessReceipt
		var err error
		for range 32 {
			receipt, err = m.send(retryCtx, left.endpoint.binding.Agent, right.endpoint.binding.Agent, messageRef, conversationRef, "complete", now, deadline)
			if err == nil {
				break
			}
			if retryCtx.Err() != nil {
				break
			}
			if !errors.Is(err, messagestore.ErrBusy) && !errors.Is(err, processhost.ErrBusy) && !strings.Contains(err.Error(), "bounded frame read failed") {
				break
			}
			retries++
		}
		cancel()
		if err != nil || receipt.Delivery.State != coremessage.StateDelivered {
			t.Fatalf("message %d after bounded retry: %+v %v", i, receipt, err)
		}
		right.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
		if processCodexOutcomeCount(m) != 0 {
			t.Fatalf("message %d retained committed outcome", i)
		}
	}
	wire, err := os.ReadFile(filepath.Join(right.root, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(wire), `"method":"turn/start"`); count != 258 {
		t.Fatalf("258 delivered messages wrote %d turns", count)
	}
	t.Logf("delivered=258 explicit retries=%d committed outcomes=0 turn writes=258", retries)

	// A write can persist the terminal record but fail its final directory sync.
	// Confirming that durable receipt also releases the corresponding cache slot.
	record, found, err := m.store.Get("message-cache-257")
	if err != nil || !found {
		t.Fatalf("terminal record: %v %v", found, err)
	}
	m.mu.Lock()
	m.outcomes[record.Envelope.MessageRef] = coremessage.Event{Kind: coremessage.EventDeliver}
	m.mu.Unlock()
	if _, err = m.deliver(ctx, record); err != nil {
		t.Fatal(err)
	}
	if processCodexOutcomeCount(m) != 0 {
		t.Fatal("terminal receipt retained outcome")
	}
}

func TestCodexProcessReceiptDeadlineUsesInjectedClock(t *testing.T) {
	left := newProcessCodexFixture(t, nil)
	right := newProcessCodexFixture(t, nil)
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	m := &codexProcessMessages{store: messagestore.NewStore(filepath.Join(left.root, "messages")), endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint, right.endpoint.binding.Agent: right.endpoint}, now: func() time.Time { return deadline }}
	right.endpoint.messages.Store(m)
	receipt, err := m.send(context.Background(), left.endpoint.binding.Agent, right.endpoint.binding.Agent, "message-clock-expired", "conversation-clock", "complete", now, deadline)
	if err != nil || receipt.Delivery.State != coremessage.StateExpired {
		t.Fatalf("deadline equality: %+v %v", receipt, err)
	}
	for _, n := range right.wire(t) {
		if string(n["method"]) == `"turn/start"` {
			t.Fatal("expired message reached provider")
		}
	}
}
