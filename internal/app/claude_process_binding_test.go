package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
  emit({'type':'system','subtype':'init','session_id':'process-session'})
  prompt=frame['message']['content']
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
  elif prompt=='interrupt':emit({'type':'assistant','session_id':'process-session','content':'waiting'})
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
}

func newProcessClaudeFixture(t *testing.T, command func(string, string) processhost.Command) *processClaudeFixture {
	return newProcessClaudeFixtureAt(t, command, "")
}

func newProcessClaudeFixtureAt(t *testing.T, command func(string, string) processhost.Command, sharedPath string) *processClaudeFixture {
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
	current := func(ctx context.Context, binding processhost.Binding) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			return err
		}
		p, ok := reg.Pane(binding.Pane)
		if !ok || binding != b || p.Status.Activation.Generation != binding.Generation || p.Metadata.OwnerUID() != binding.Agent {
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
	handle, err := startProcessClaude(context.Background(), host, processhost.Launch{Binding: b, Command: cmd}, path)
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
		if _, err := os.Lstat(leaseDir); !os.IsNotExist(err) {
			t.Errorf("host lease remains after actual Wait: %s (%v)", leaseDir, err)
		}
	})
	questions := agentquestion.NewStore(filepath.Join(root, "answers"))
	approvals := agentapproval.NewStore(filepath.Join(root, "answers"))
	f := &processClaudeFixture{root: root, binary: binary, path: path, binding: b, handle: handle, host: host, store: store}
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
		return p != nil && p.Status.Activation.Claude != nil && p.Status.Activation.Claude.Registration != nil
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
	return claudeProcessProof{Binding: f.binding, Socket: socket, Process: process, HostProcess: hostProcess, HostSocket: identity, Session: pane.Status.Activation.Claude.RegistrationSessionID}
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
	_, reason := claudeRegistrationBootstrap(reg, f.path, []byte(`{"hook_event_name":"SessionStart","session_id":"process-session","agentUID":"claimed-owner","generation":"claimed-gen"}`), env, proof.Process.PID)
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
	if err != nil || response.Delivery.State == agentdelivery.StateDelivered {
		t.Fatal("busy delivered", response, err)
	}
	after, _ := target.handle.Observe(target.binding)
	if after.Turn != pending.Turn || len(after.Pending) != 1 || after.Pending[0].ID != pending.Pending[0].ID {
		t.Fatal("busy changed existing turn", after)
	}
	answerProcessEndpointQuestion(t, target)
	target.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
	pane, _ := reg.Pane(target.binding.Pane)
	forged := &processClaudeProviderPoster{proof: targetProof, registrationGeneration: pane.Status.Activation.Claude.RegistrationGeneration}
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
	target.turn(t, "after-busy", "ordinary")
	target.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
}
