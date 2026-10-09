package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentapproval"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// This dispatch is test-only. Public foreground and supervisor routes remain
// inactive. Every child runs a copy of this binary in the disposable root.
func exitIfClaudeProcessFixtureChild() {
	if os.Getenv("PMX_TEST_PROCESS_CLAUDE_CHILD") != "1" || len(os.Args) < 3 || os.Args[1] != "internal" {
		return
	}
	var err error
	switch os.Args[2] {
	case "process-host-owner-fixture":
		err = runProcessSupervisorOwnerFixture()
	case "processhost-test-supervisor":
		err = processhost.ServeSupervisor(os.NewFile(3, "lifetime"), os.NewFile(4, "spec"), os.NewFile(5, "status"))
	case "claude-endpoint-register":
		_, reason := claudeEndpointRegistrationHook(nil, os.Getenv, os.Stdin, os.Getppid(), startClaudeEndpointHelper)
		if reason != "" {
			err = fmt.Errorf("registration: %s", reason)
		}
	case claudeEndpointHelperRoute:
		err = runClaudeEndpointHelper(nil, (*diagnostics.ClaudeRegistrationRecorder)(nil))
	case claudeQuestionHookRoute:
		err = runClaudeQuestionHook(nil, os.Stdin, os.Stdout, os.Stderr)
	case claudePermissionHookRoute:
		err = runClaudePermissionHook(nil, os.Stdin, os.Stdout, os.Stderr)
	default:
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

const processClaudeProviderFixture = `
import os,sys,socket,json,threading,subprocess
path=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'provider.sock')
s=socket.socket(socket.AF_UNIX);s.bind(path);os.chmod(path,0o600);s.listen()
os.environ['CLAUDE_CODE_MESSAGING_SOCKET']=path
os.environ['CLAUDE_CODE_MESSAGING_TOKEN']='fixture-token'
write_lock=threading.Lock()
def emit(frame):
 with write_lock: print(json.dumps(frame),flush=True)
def hook(route,payload,env=None):
 p=subprocess.run([os.environ['PMX_TEST_PROCESS_BINARY'],'internal',route],input=json.dumps(payload).encode(),env=env or os.environ,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 if p.returncode or p.stdout or p.stderr: raise Exception((route,p.returncode,p.stdout,p.stderr))
hook('claude-endpoint-register',{'hook_event_name':'SessionStart','session_id':'process-session'})
def messages():
 while True:
  c,_=s.accept();f=c.makefile('rb');a=json.loads(f.readline());m=json.loads(f.readline());assert a['token']=='fixture-token';assert f.readline()==b''
  emit({'type':'system','subtype':'init','session_id':'process-session'})
  emit({'type':'command_lifecycle','session_id':'process-session','phase':'observed'})
  emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()
  inp={'questions':[{'question':'Color?','header':'Color','options':[{'label':'blue','description':'Blue'},{'label':'red','description':'Red'}],'multiSelect':False}]}
  request={'type':'control_request','request_id':'endpoint-'+json.loads(m['message']['content'])['messageRef'],'request':{'subtype':'can_use_tool','tool_name':'AskUserQuestion','input':inp}}
  hook('claude-question-hook',{'hook_event_name':'PreToolUse','session_id':'process-session','tool_name':'AskUserQuestion','tool_input':inp,'tool_use_id':'endpoint-tool'})
  emit(request);emit(request)
threading.Thread(target=messages,daemon=True).start()
request_count=0
for line in sys.stdin:
 frame=json.loads(line)
 if frame['type']=='user':
  prompt=frame['message']['content']
  # Input written into a running turn joins it: no second init, one result.
  if prompt.startswith('joined'):
   emit({'type':'assistant','session_id':'process-session','joined_echo':prompt});emit({'type':'result','subtype':'success','session_id':'process-session'});continue
  emit({'type':'system','subtype':'init','session_id':'process-session'})
  if prompt in ('question','permission'):
   inp={'questions':[{'question':'Color?','header':'Color','options':[{'label':'blue','description':'Blue'},{'label':'red','description':'Red'}],'multiSelect':False}]} if prompt=='question' else {'command':'printf fixture'}
   tool='AskUserQuestion' if prompt=='question' else 'Bash'
   # Both orderings occur: same hook and duplicated stream request still have
   # exactly one domain record and one stream writer.
   payload={'hook_event_name':'PreToolUse' if prompt=='question' else 'PermissionRequest','session_id':'process-session','tool_name':tool,'tool_input':inp,'tool_use_id':'fixture-tool'}
   hook('claude-question-hook' if prompt=='question' else 'claude-permission-hook',payload)
   request_count+=1
   request={'type':'control_request','request_id':prompt+'-'+str(request_count),'request':{'subtype':'can_use_tool','tool_name':tool,'input':inp}}
   emit(request);emit(request)
  elif prompt=='register-again':
   hook('claude-endpoint-register',{'hook_event_name':'SessionStart','session_id':'process-session'});emit({'type':'result','subtype':'success','session_id':'process-session'})
  elif prompt=='interrupt':emit({'type':'assistant','session_id':'process-session','content':'waiting'})
  elif prompt=='hold':emit({'type':'assistant','session_id':'process-session','content':'holding'})
  elif prompt=='background':
   # The turn ends, then Claude opens one itself (measured background task
   # completion) and asks for a permission inside it.
   emit({'type':'result','subtype':'success','session_id':'process-session'})
   emit({'type':'system','subtype':'task_notification','session_id':'process-session','task_id':'bg'})
   # Key order keeps this init out of the host-turn init rewrites other tests apply.
   emit({'session_id':'process-session','type':'system','subtype':'init'})
   inp={'command':'printf provider'}
   hook('claude-permission-hook',{'hook_event_name':'PermissionRequest','session_id':'process-session','tool_name':'Bash','tool_input':inp,'tool_use_id':'provider-tool'})
   emit({'type':'control_request','request_id':'provider-1','request':{'subtype':'can_use_tool','tool_name':'Bash','input':inp}})
  else:emit({'type':'result','subtype':'success','session_id':'process-session'})
 elif frame['type']=='control_response':
  emit({'type':'assistant','session_id':'process-session','wire_echo':frame})
  emit({'type':'result','subtype':'success','session_id':'process-session'})
 elif frame['type']=='control_request':
  emit({'type':'control_response','response':{'subtype':'success','request_id':frame['request_id']}})
  emit({'type':'result','subtype':'error_during_execution','session_id':'process-session'})
s.close()
`

type processClaudeFixture struct {
	root, binary, path string
	binding            processhost.Binding
	handle             *processhost.Handle
	host               *processhost.Host
	store              *intmetadata.Store
	control            *claudeProcessControl
	// registryChecks counts the host's Registry ownership transactions.
	registryChecks *atomic.Int64
}

func newProcessClaudeFixture(t *testing.T, command func(string, string) processhost.Command) *processClaudeFixture {
	return newProcessClaudeFixtureAt(t, command, "")
}

func newProcessClaudeFixtureAt(t *testing.T, command func(string, string) processhost.Command, sharedPath string, lifetime ...processOwnerLifetime) *processClaudeFixture {
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
	h := newSessionRefHarness(t, "claude")
	encoded, _ := json.Marshal(h.registry)
	suffix := filepath.Base(root)
	nextAgent, nextPane := h.agentUID+"-"+suffix, h.paneUID+"-"+suffix
	encoded = bytes.ReplaceAll(encoded, []byte(h.agentUID), []byte(nextAgent))
	encoded = bytes.ReplaceAll(encoded, []byte(h.paneUID), []byte(nextPane))
	if err := json.Unmarshal(encoded, h.registry); err != nil {
		t.Fatal(err)
	}
	h.agentUID, h.paneUID = nextAgent, nextPane
	renamedAgent, _ := h.registry.Agent(h.agentUID)
	renamedAgent.Metadata.Name += "-" + suffix
	renamedPane, _ := h.registry.Pane(h.paneUID)
	renamedPane.Metadata.Name += "-" + suffix
	for i := range h.registry.NameReservations {
		r := &h.registry.NameReservations[i]
		if r.UID == h.agentUID {
			r.Name = renamedAgent.Metadata.Name
		}
		if r.UID == h.paneUID {
			r.Name = renamedPane.Metadata.Name
		}
	}
	pane, _ := h.registry.Pane(h.paneUID)
	pane.Status.Activation.RuntimeID = ""
	agent, _ := h.registry.Agent(h.agentUID)
	window, _ := h.registry.Window(agent.Metadata.OwnerUID())
	b := processhost.Binding{Host: "fixture-host-" + suffix, Project: window.Metadata.OwnerUID(), Window: window.Metadata.UID, Agent: h.agentUID, Pane: h.paneUID, Generation: h.envGeneration, Operation: pane.Status.Activation.OperationID}
	if b.Operation == "" {
		b.Operation = "fixture-operation"
		pane.Status.Activation.OperationID = b.Operation
	}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: "claude", Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, ResumeState: coremetadata.ProcessResumeUnknown}
	path := intmetadata.PathFor(filepath.Join(root, "state"))
	if sharedPath != "" {
		path = sharedPath
	}
	store := intmetadata.NewStore(path)
	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		if sharedPath == "" {
			*reg = h.registry.Clone()
		} else {
			a, _ := h.registry.Agent(h.agentUID)
			p, _ := h.registry.Pane(h.paneUID)
			reg.Agents = append(reg.Agents, *a)
			reg.Panes = append(reg.Panes, *p)
			for _, reservation := range h.registry.NameReservations {
				if reservation.UID == h.agentUID || reservation.UID == h.paneUID {
					reg.NameReservations = append(reg.NameReservations, reservation)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registryChecks := new(atomic.Int64)
	current := func(ctx context.Context, binding processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		registryChecks.Add(1)
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			return err
		}
		p, ok := reg.Pane(binding.Pane)
		if !ok || binding != b || (p.Status.ProcessSession == nil || p.Status.ProcessSession.Binding.Generation != binding.Generation) || p.Metadata.OwnerUID() != binding.Agent {
			return processhost.ErrStale
		}
		return nil
	}
	tx := processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, session string) error { return current(ctx, b) }}
	env := append(os.Environ(), "PMX_TEST_PROCESS_CLAUDE_CHILD=1", "PMX_TEST_PROCESS_BINARY="+binary, "PMX_TEST_PROCESS_ROOT="+root, "HOME="+root)
	limits := processhost.DefaultLimits()
	limits.Startup = 8 * time.Second
	limits.Grace = 200 * time.Millisecond
	host, err := processhost.NewHost(b.Host, processhost.Command{Path: binary, Args: []string{"internal", "processhost-test-supervisor"}, Env: env}, tx, limits)
	if err != nil {
		t.Fatal(err)
	}
	cmd := processhost.Command{Path: "python3", Args: []string{"-u", "-c", processClaudeProviderFixture}, Env: env, Dir: root}
	if command != nil {
		cmd = command(root, binary)
		cmd.Env = append(cmd.Env, "PMX_TEST_PROCESS_BINARY="+binary, "PMX_TEST_PROCESS_ROOT="+root, "PMX_TEST_PROCESS_CLAUDE_CHILD=1")
	}
	ctx := context.Background()
	if len(lifetime) > 0 {
		ctx = lifetime[0].withContext(ctx)
	}
	handle, err := startProcessClaude(ctx, host, processhost.Launch{Binding: b, Command: cmd}, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(b)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := handle.Wait(ctx, b)
		if err != nil || s.Exit == nil {
			t.Errorf("Wait: %+v %v", s, err)
		}
		leaseDir := filepath.Dir(processClaudeHostSocket(path, b.Pane, b.Generation))
		assertClaudeProcessLeaseAfterWait(t, leaseDir, s)
	})
	questions := agentquestion.NewStore(filepath.Join(root, "answers"))
	approvals := agentapproval.NewStore(filepath.Join(root, "answers"))
	f := &processClaudeFixture{root: root, binary: binary, path: path, binding: b, handle: handle, host: host, store: store, registryChecks: registryChecks}
	f.control = &claudeProcessControl{handle: handle, binding: b, questions: questions, approvals: approvals, questionWindow: time.Minute, approvalWindow: time.Minute, now: time.Now}
	return f
}

func (f *processClaudeFixture) wait(t *testing.T, predicate func(processhost.Snapshot) bool) processhost.Snapshot {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		s, err := f.handle.Observe(f.binding)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(s) {
			return s
		}
		if s.Exit != nil {
			t.Fatalf("premature exit: %+v", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _ := f.handle.Observe(f.binding)
	t.Fatalf("wait timeout: %+v", s)
	return s
}
func (f *processClaudeFixture) turn(t *testing.T, id, prompt string) {
	t.Helper()
	s, _ := f.handle.Observe(f.binding)
	if err := f.handle.Turn(context.Background(), processhost.Authority{Binding: f.binding, Session: s.Session, Connection: s.Connection}, id, prompt); err != nil {
		t.Fatal(err)
	}
}
func (f *processClaudeFixture) proof(t *testing.T) claudeProcessProof {
	t.Helper()
	s := f.wait(t, func(s processhost.Snapshot) bool {
		reg, _ := f.store.LoadDegradedReadOnly()
		p, _ := reg.Pane(f.binding.Pane)
		if p == nil || p.Status.ProcessSession == nil {
			return false
		}
		_, ok := discoverProcessClaudeProof(f.path, reg, f.binding.Agent)
		return ok
	})
	process, _, err := localipc.Process(s.PID)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := f.store.LoadDegradedReadOnly()
	pane, _ := reg.Pane(f.binding.Pane)
	hostProcess, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	socket := processClaudeHostSocket(f.path, f.binding.Pane, f.binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		t.Fatal(err)
	}
	return claudeProcessProof{Binding: f.binding, Socket: socket, Process: process, HostProcess: hostProcess, HostSocket: identity, Session: pane.Status.ProcessSession.SessionID}
}

func TestClaudeProcessBindingRegistrationAndSingleControlWriter(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	reg, _ := f.store.LoadDegradedReadOnly()
	if _, reason := coremetadata.ResolveAgentRoute(reg, f.binding.Agent); reason == "" {
		t.Fatal("public process route activated")
	}
	route, reason := processClaudeRouteResolver(f.path, proof)(reg, f.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	if !probeClaudeRegistrationLease(f.path, route) {
		t.Fatal("process endpoint lease not ready")
	}
	f.turn(t, "question-turn", "question")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := f.control.questions.List(f.binding.Agent)
	if err != nil || len(records) != 1 {
		t.Fatalf("stream/hook count=%d %v", len(records), err)
	}
	if _, err := f.control.questions.Answer(records[0].ID, f.binding.Agent, map[string]string{"Color?": "blue"}); err != nil {
		t.Fatal(err)
	}
	if err := f.control.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	if err := f.handle.Respond(context.Background(), processhost.Authority{Binding: f.binding, Connection: snap.Connection, Session: snap.Session}, snap.Pending[0], processhost.Response{Answers: map[string]string{"Color?": "red"}}); err != processhost.ErrStale {
		t.Fatalf("duplicate response: %v", err)
	}
	for _, decision := range []bool{false, true} {
		f.turn(t, fmt.Sprintf("permission-%t", decision), "permission")
		f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
		if err := f.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		records, err := f.control.approvals.List(f.binding.Agent)
		if err != nil {
			t.Fatal(err)
		}
		waiting := []agentapproval.Record{}
		for _, record := range records {
			if record.State == agentapproval.StateWaiting {
				waiting = append(waiting, record)
			}
		}
		if len(waiting) != 1 {
			t.Fatalf("approval waiting count=%d", len(waiting))
		}
		if _, err := f.control.approvals.Answer(waiting[0].ID, f.binding.Agent, decision, agentapproval.ViaCLI); err != nil {
			t.Fatal(err)
		}
		if err := f.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	}
	f.turn(t, "interrupt", "interrupt")
	s := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "interrupt" })
	if err := f.handle.Interrupt(context.Background(), processhost.Authority{Binding: f.binding, Connection: s.Connection, Session: s.Session}, s.Turn); err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	reg, _ = f.store.LoadDegradedReadOnly()
	agent, _ := reg.Agent(f.binding.Agent)
	if agent.Status.LastTermination != nil {
		t.Fatal("interrupt wrote LastTermination")
	}
	events, _, _ := f.handle.Events(f.binding, 0)
	writes := 0
	for _, event := range events {
		if bytes.Contains(event.Raw, []byte(`"wire_echo"`)) {
			writes++
		}
	}
	if writes != 3 {
		t.Fatalf("response wire count=%d", writes)
	}
}

func TestClaudeProcessHooksRemainObservationOnlyAfterHostLoss(t *testing.T) {
	t.Setenv(internalClaudeProcessBindingEnv, `{"Host":"lost-host"}`)
	t.Setenv(internalClaudeProcessHostEnv, "/nonexistent/process.sock")
	var out bytes.Buffer
	question := claudeQuestionHook{loadRegistry: func() (coremetadata.Registry, error) {
		t.Fatal("hook opened Registry")
		return coremetadata.Registry{}, nil
	}}
	question.run(context.Background(), nil, strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`), &out, io.Discard)
	permission := claudePermissionHook{loadRegistry: func() (coremetadata.Registry, error) {
		t.Fatal("hook opened Registry")
		return coremetadata.Registry{}, nil
	}}
	permission.run(context.Background(), nil, strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`), &out, io.Discard)
	if out.Len() != 0 {
		t.Fatalf("hook control write: %s", out.String())
	}
}

func TestClaudeProcessRegistrationRejectsSelfAssertionStaleHostAndBirth(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	for _, change := range []func(*claudeProcessProof){
		func(p *claudeProcessProof) { p.Binding.Generation = "old" },
		func(p *claudeProcessProof) { p.Binding.Host = "other-host" },
		func(p *claudeProcessProof) { p.HostProcess.PID++ },
		func(p *claudeProcessProof) { p.HostSocket.Inode++ },
		func(p *claudeProcessProof) { p.Binding.Agent = "other-owner" },
		func(p *claudeProcessProof) { p.Process.PID++ },
		func(p *claudeProcessProof) { p.Process.Start = "stale-birth" },
		func(p *claudeProcessProof) { p.Session = "other-session" },
	} {
		wrong := proof
		change(&wrong)
		if checkClaudeProcessHost(wrong, false) {
			t.Fatalf("self assertion accepted: %+v", wrong)
		}
		called := false
		reason := registerClaudeEndpoint(claudeEndpointBootstrap{ProcessProof: &wrong}, func(claudeEndpointBootstrap) error { called = true; return nil })
		if reason == "" || called {
			t.Fatal("stale registration reached endpoint writer")
		}
	}
	if checkClaudeProcessHost(proof, true) {
		t.Fatal("unrelated peer registered by claiming owned parent PID")
	}
	reg, _ := f.store.LoadDegradedReadOnly()
	binding, _ := json.Marshal(proof.Binding)
	env := func(key string) string {
		switch key {
		case internalClaudeProcessBindingEnv:
			return string(binding)
		case internalClaudeProcessHostEnv:
			return proof.Socket
		case internalActivationPaneUIDEnv:
			return proof.Binding.Pane
		case internalActivationGenerationEnv:
			return proof.Binding.Generation
		}
		return ""
	}
	_, reason := claudeRegistrationBootstrap(reg, f.path, []byte(`{"hook_event_name":"SessionStart","session_id":"process-session","agentUID":"claimed-owner","generation":"claimed-gen"}`), env, proof.Process.PID, nil)
	if reason == "" {
		t.Fatal("self-asserted SessionStart gained authority")
	}
}

func TestClaudeProcessTimeoutAndDisconnectNeverAutoAllow(t *testing.T) {
	for _, kind := range []string{"question", "permission"} {
		for _, disconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disconnect=%t", kind, disconnect), func(t *testing.T) {
				f := newProcessClaudeFixture(t, nil)
				_ = f.proof(t)
				now := time.Now()
				f.control.now = func() time.Time { return now }
				f.control.questions = f.control.questions.WithClock(func() time.Time { return now })
				f.control.approvals = f.control.approvals.WithClock(func() time.Time { return now })
				f.turn(t, "pending", kind)
				s := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
				if err := f.control.sync(context.Background()); err != nil {
					t.Fatal(err)
				}
				token := s.Pending[0]
				if kind == "question" {
					records, err := f.control.questions.List(f.binding.Agent)
					if err != nil || len(records) != 1 {
						t.Fatal("missing question", err)
					}
				} else {
					records, err := f.control.approvals.List(f.binding.Agent)
					if err != nil || len(records) != 1 {
						t.Fatal("missing approval", err)
					}
				}
				if disconnect {
					_ = f.handle.Stop(f.binding)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, err := f.handle.Wait(ctx, f.binding)
					if err != nil {
						t.Fatal(err)
					}
					if err := f.control.sync(context.Background()); err == nil {
						t.Fatal("disconnected control remained available")
					}
				} else {
					now = now.Add(2 * time.Minute)
					if err := f.control.sync(context.Background()); err != nil {
						t.Fatal(err)
					}
					s, _ = f.handle.Observe(f.binding)
					if len(s.Pending) != 0 {
						t.Fatal("timeout retained pending token")
					}
					if err := f.handle.Respond(context.Background(), processhost.Authority{Binding: f.binding, Connection: s.Connection, Session: s.Session}, token, processhost.Response{Allow: true}); err != processhost.ErrStale {
						t.Fatal(err)
					}
				}
				events, _, _ := f.handle.Events(f.binding, 0)
				for _, event := range events {
					if event.Kind == "control-answered" || bytes.Contains(event.Raw, []byte(`"wire_echo"`)) {
						t.Fatal("timeout/disconnect wrote response")
					}
				}
			})
		}
	}
}

func TestClaudeProcessBidirectionalEndpointReceiptsAndStaleWireZero(t *testing.T) {
	left := newProcessClaudeFixture(t, nil)
	leftProof := left.proof(t)
	right := newProcessClaudeFixtureAt(t, nil, left.path)
	rightProof := right.proof(t)
	left.turn(t, "first", "ordinary")
	left.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	right.turn(t, "first", "ordinary")
	right.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	registry, err := left.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	leftRoute, reason := processClaudeRouteResolver(left.path, leftProof)(registry, left.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	rightRoute, reason := processClaudeRouteResolver(right.path, rightProof)(registry, right.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(left.path)))
	now := time.Now().UTC()
	for i, pair := range []struct {
		source, target coremetadata.AgentRouteRef
		receiver       *processClaudeFixture
	}{{leftRoute, rightRoute, right}, {rightRoute, leftRoute, left}} {
		ref := fmt.Sprintf("message-process-direction-%d", i)
		envelope := dialogueForRoute(ref, pair.target, now)
		envelope.BrokerEnvelope.Source = publicMessageRoute(pair.source)
		if _, _, err := store.PutAccepted(*envelope.BrokerEnvelope, "claude-coordination"); err != nil {
			t.Fatal(err)
		}
		target, _ := claudeTargetForRoute(pair.target)
		request := claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "submit", Target: target, Envelope: &envelope}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		response, err := callClaudeCoordination(ctx, left.path, pair.target, request)
		cancel()
		if err != nil || response.Delivery.State != agentdelivery.StateDelivered {
			t.Fatalf("direction%d receipt: %+v %v", i, response, err)
		}
		pair.receiver.wait(t, func(processhost.Snapshot) bool {
			events, _, _ := pair.receiver.handle.Events(pair.receiver.binding, 0)
			for _, e := range events {
				if bytes.Contains(e.Raw, []byte(ref)) {
					return true
				}
			}
			return false
		})
		pending := pair.receiver.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
		if pending.Pending[0].Kind != "question" {
			t.Fatal("endpoint control not bound", pending)
		}
		if err := pair.receiver.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		questions, _ := pair.receiver.control.questions.List(pair.receiver.binding.Agent)
		if len(questions) != 1 {
			t.Fatalf("endpoint hook/stream questions=%d", len(questions))
		}
		if _, err := pair.receiver.control.questions.Answer(questions[0].ID, pair.receiver.binding.Agent, map[string]string{"Color?": "blue"}); err != nil {
			t.Fatal(err)
		}
		if err := pair.receiver.control.sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		completed := pair.receiver.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
		if completed.State != "ready" || completed.Exit != nil || completed.MessageReservation != "" {
			t.Fatal("endpoint did not complete", completed)
		}
		pair.receiver.turn(t, "after-endpoint", "ordinary")
		pair.receiver.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
		record, found, err := store.Get(ref)
		if err != nil || !found || !record.HandoffObserved || record.Delivery.State != coremessage.StateDelivered {
			t.Fatalf("durable direction%d receipt: %+v %v", i, record, err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		duplicate, err := callClaudeCoordination(ctx, left.path, pair.target, request)
		cancel()
		if err != nil || duplicate.Delivery.State != agentdelivery.StateDelivered {
			t.Fatalf("duplicate receipt: %+v %v", duplicate, err)
		}
		stale := request
		stale.Target.Generation = "old-generation"
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		_, err = callClaudeCoordination(ctx, left.path, pair.target, stale)
		cancel()
		if err == nil {
			t.Fatal("stale target admitted")
		}
		events, _, _ := pair.receiver.handle.Events(pair.receiver.binding, 0)
		writes := 0
		for _, e := range events {
			if bytes.Contains(e.Raw, []byte(`"message_echo"`)) && bytes.Contains(e.Raw, []byte(ref)) {
				writes++
			}
		}
		if writes != 1 {
			t.Fatalf("direction%d provider writes=%d", i, writes)
		}
	}
}

// heldListerAlias answers HeldFor for the process fixture's Agent from the
// hold fixture's Agent: the two fixtures name one target under two UIDs. Each
// answer for that Agent reports how many records it listed.
type heldListerAlias struct {
	store    *messagestore.Store
	from, to string
	listed   chan int
}

func (a *heldListerAlias) HeldFor(agentUID string) ([]messagestore.Record, error) {
	if agentUID != a.from {
		return nil, nil
	}
	held, err := a.store.HeldFor(a.to)
	a.listed <- len(held)
	return held, err
}

// A message held while its target's turn ran outlives a release window that
// ended with the target still busy. The result of the target's next turn must
// wake the release through the real process start wiring, and that release
// delivers the held record with no resend. A result with nothing held launches
// nothing.
func TestClaudeProcessTurnResultRestartsAnEndedHeldRelease(t *testing.T) {
	// The process fixture comes first: it shortens TMPDIR for its sockets, and
	// the test's temporary root is fixed by the first TempDir call.
	process := newProcessClaudeFixture(t, nil)
	process.proof(t)
	hold := newHoldFixture(t)
	hold.setInteraction(t, coremetadata.InteractionIdle)
	hold.installFakeSleep()
	alias := &heldListerAlias{store: hold.store, from: process.binding.Agent, to: hold.claudeUID, listed: make(chan int, 16)}
	launched := make(chan string, 16)
	previous := processClaudeHeldRelease
	t.Cleanup(func() { processClaudeHeldRelease = previous })
	processClaudeHeldRelease = func() heldMessageRelease {
		return heldMessageRelease{store: func() (agentMessageHeldLister, error) { return alias, nil },
			launch: func(agentUID string) error { launched <- agentUID; return nil }}
	}
	awaitListed := func(want int) {
		t.Helper()
		select {
		case got := <-alias.listed:
			if got != want {
				t.Fatalf("turn result listed %d held records, want %d", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("turn result did not wake the held-message release")
		}
	}

	process.turn(t, "result-nothing-held", "ordinary")
	awaitListed(0)

	// Held by a busy turn, with a deadline past the release window.
	ref := "message-result-wake"
	hold.putHeld(t, ref, hold.now, hold.now.Add(time.Hour), claudeHoldReasonTurnActive)
	hold.adapter.busy = 1000
	if err := hold.cmd.releaseHeldMessages(hold.claudeUID); err != nil {
		t.Fatal(err)
	}
	var waited time.Duration
	for _, wait := range hold.waits {
		waited += wait
	}
	if got := hold.delivery(t, ref); got.State != coremessage.StateHeld || waited < agentMessageReleaseRetryWindow {
		t.Fatalf("release window did not end with the record held: %+v after %s", got, waited)
	}
	select {
	case agentUID := <-launched:
		t.Fatalf("a result with nothing held launched a release for %s", agentUID)
	default:
	}
	hold.adapter.busy = 0
	submitted := len(hold.adapter.submits)

	process.turn(t, "result-wakes-release", "ordinary")
	awaitListed(1)
	select {
	case agentUID := <-launched:
		if agentUID != process.binding.Agent {
			t.Fatalf("release launched for %s, want the turn's Agent %s", agentUID, process.binding.Agent)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("held record did not launch a release")
	}
	if err := hold.cmd.releaseHeldMessages(hold.claudeUID); err != nil {
		t.Fatal(err)
	}
	if got := hold.delivery(t, ref); got.State != coremessage.StateDelivered {
		t.Fatalf("woken release did not deliver the held record: %+v", got)
	}
	if got := hold.adapter.submits[submitted:]; len(got) != 1 || got[0] != ref {
		t.Fatalf("woken release submits %v, want exactly %s", got, ref)
	}
}

func TestClaudeProcessEndpointBusyAndForgedHelperWriteZero(t *testing.T) {
	source := newProcessClaudeFixture(t, nil)
	sourceProof := source.proof(t)
	target := newProcessClaudeFixtureAt(t, nil, source.path)
	targetProof := target.proof(t)
	source.turn(t, "first", "ordinary")
	source.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	target.turn(t, "pending", "question")
	pending := target.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	reg, err := source.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	sourceRoute, reason := processClaudeRouteResolver(source.path, sourceProof)(reg, source.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	targetRoute, reason := processClaudeRouteResolver(target.path, targetProof)(reg, target.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	envelope := dialogueForRoute("message-process-busy", targetRoute, time.Now().UTC())
	envelope.BrokerEnvelope.Source = publicMessageRoute(sourceRoute)
	store := messagestore.NewStore(filepath.Dir(filepath.Dir(source.path)))
	if _, _, err := store.PutAccepted(*envelope.BrokerEnvelope, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	coordTarget, _ := claudeTargetForRoute(targetRoute)
	request := claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "submit", Target: coordTarget, Envelope: &envelope}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	response, err := callClaudeCoordination(ctx, source.path, targetRoute, request)
	cancel()
	if err != nil || response.Delivery.State != agentdelivery.StateHeld || response.Delivery.Reason != claudeHoldReasonTurnActive {
		t.Fatal("busy not held", response, err)
	}
	record, _, recordErr := store.Get(envelope.MessageRef)
	if recordErr != nil || record.HandoffObserved {
		t.Fatalf("busy marked handoff: %+v %v", record, recordErr)
	}
	// Repeated busy checks must remain retryable and preserve the same active turn.
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	response, err = callClaudeCoordination(ctx, source.path, targetRoute, request)
	cancel()
	if err != nil || response.Delivery.State != agentdelivery.StateHeld {
		t.Fatalf("second busy: %+v %v", response, err)
	}
	after, _ := target.handle.Observe(target.binding)
	if after.Turn != pending.Turn || len(after.Pending) != 1 || after.Pending[0].ID != pending.Pending[0].ID {
		t.Fatal("busy changed existing turn", after)
	}
	answerProcessEndpointQuestion(t, target)
	target.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	authority := targetRoute.Authority().(coremetadata.ClaudeAuthorityRef)
	forged := &processClaudeProviderPoster{proof: targetProof, registrationGeneration: authority.RegistrationGeneration}
	// A correct payload from this test process is still not the registered
	// helper's kernel PID/start identity. It must not reserve or write.
	content, _ := json.Marshal(claudeProviderCoordinationContent{Kind: "projmux-coordination", MessageRef: envelope.MessageRef})
	admitted, err := forged.exchange(string(content), "reserve", claudeProviderPostOutcome{})
	if err != nil || admitted {
		t.Fatal("forged helper admitted", admitted, err)
	}
	after, _ = target.handle.Observe(target.binding)
	if after.Turn != "" || after.MessageReservation != "" {
		t.Fatal("forged helper reserved", after)
	}
	events, _, _ := target.handle.Events(target.binding, 0)
	for _, e := range events {
		if e.Kind == "message-reserved" || bytes.Contains(e.Raw, []byte("message-process-busy")) {
			t.Fatal("busy/forged helper wrote", e)
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	response, err = callClaudeCoordination(ctx, source.path, targetRoute, request)
	cancel()
	if err != nil || response.Delivery.State != agentdelivery.StateDelivered {
		t.Fatalf("busy not retryable after result: %+v %v", response, err)
	}
	target.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 })
	answerProcessEndpointQuestion(t, target)
	target.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	target.turn(t, "after-busy", "ordinary")
	target.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
}

// The helper's idle identity gate and one bounded host read can outlast the
// host's one-second completion cleanup. Wait must report that timeout without
// pretending that helper-owned entries were already removed.
func assertClaudeProcessLeaseAfterWait(t *testing.T, dir string, snapshot processhost.Snapshot) {
	t.Helper()
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return
	}
	if !strings.Contains(snapshot.Diagnostic, "owned completion cleanup:") {
		t.Errorf("lease survived Wait without cleanup diagnostic: %q", snapshot.Diagnostic)
	}
	deadline := time.Now().Add(localipc.Deadline + claudeEndpointIdleRegistryFloor + claudeEndpointPollInterval)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("host lease remains beyond helper shutdown bound: %s", dir)
}

func TestClaudeProcessWaitBoundsDelayedHelperCleanup(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	_ = f.proof(t)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	route, reason := processClaudeRouteResolver(f.path, f.proof(t))(reg, f.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	identity := route.Authority().(coremetadata.ClaudeAuthorityRef).LeaseProcess
	helper, err := os.FindProcess(identity.PID)
	if err != nil {
		t.Fatal(err)
	}
	signal := func(sig syscall.Signal) error {
		current, _, err := localipc.Process(identity.PID)
		if err != nil || current != identity {
			return fmt.Errorf("owned helper birth changed: %v", err)
		}
		return helper.Signal(sig)
	}
	if err := signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			_ = signal(syscall.SIGCONT)
		}
	})
	if err := f.handle.Stop(f.binding); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Fixture cleanup calls Wait again: a completed Wait reads the same actual exit idempotently.
	snapshot, err := f.handle.Wait(ctx, f.binding)
	if err != nil || snapshot.Exit == nil {
		t.Fatalf("actual Wait: %+v %v", snapshot, err)
	}
	if !strings.Contains(snapshot.Diagnostic, "owned completion cleanup:") || !strings.Contains(snapshot.Diagnostic, context.DeadlineExceeded.Error()) {
		t.Fatalf("one-second cleanup timeout missing: %q", snapshot.Diagnostic)
	}
	dir := filepath.Dir(processClaudeHostSocket(f.path, f.binding.Pane, f.binding.Generation))
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("paused helper lease vanished before helper exit: %v", err)
	}
	if err := signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	resumed = true
	assertClaudeProcessLeaseAfterWait(t, dir, snapshot)
	deadline := time.Now().Add(localipc.Deadline)
	for {
		current, _, err := localipc.Process(identity.PID)
		if err != nil || current != identity {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned helper remains after lease cleanup: %+v", identity)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("actual Wait exit=%+v, delayed helper cleanup diagnostic=%q, lease removed", snapshot.Exit, snapshot.Diagnostic)
}

func TestClaudeProcessLiveRegistrationReplacementPreservesV5Registry(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	oldRoute, reason := processClaudeRouteResolver(f.path, proof)(reg, f.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	oldAuthority := oldRoute.Authority().(coremetadata.ClaudeAuthorityRef)
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.turn(t, "registration-replacement", "register-again")
	f.wait(t, func(snapshot processhost.Snapshot) bool {
		current, err := f.store.LoadDegradedReadOnly()
		if err != nil {
			return false
		}
		route, reason := processClaudeRouteResolver(f.path, proof)(current, f.binding.Agent)
		return reason == "" && !route.Same(oldRoute) && snapshot.Turn == ""
	})
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("live registration wrote durable fields or updatedAt")
	}
	if probeClaudeRegistrationLease(f.path, oldRoute) {
		t.Fatal("retired helper lease remained authoritative")
	}
	// The old helper's clear cannot remove the replacement's registration.
	deadline := time.Now().Add(localipc.Deadline)
	for time.Now().Before(deadline) {
		identity, _, err := localipc.Process(oldAuthority.LeaseProcess.PID)
		if err != nil || identity != oldAuthority.LeaseProcess {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	route, reason := processClaudeRouteResolver(f.path, proof)(reg, f.binding.Agent)
	if reason != "" || route.Same(oldRoute) || !probeClaudeRegistrationLease(f.path, route) {
		t.Fatalf("replacement cleared by retired helper: %+v %s", route, reason)
	}
	if identity, _, err := localipc.Process(oldAuthority.LeaseProcess.PID); err == nil && identity == oldAuthority.LeaseProcess {
		t.Fatal("retired exact helper remained")
	}
}

func TestClaudeProcessRollbackPreservesBoundedCleanupErrors(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	binding := f.binding
	binding.Generation = "stale-generation"
	closeErr := errors.New("fixture cleanup failed")
	service := &claudeProcessService{handle: f.handle, binding: binding, closeLease: func(context.Context) error { return closeErr }}
	if err := service.rollback(context.Background()); !errors.Is(err, closeErr) || !errors.Is(err, processhost.ErrStale) {
		t.Fatalf("cleanup/Stop/Wait failure lost: %v", err)
	}
}
