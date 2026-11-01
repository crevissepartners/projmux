package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The fixture speaks the native Unix WebSocket protocol and the official
// proxy stdio upgrade. Dedicated children use the existing stdio fixture.
const codexHostMoveServerFixture = `
import os,sys,json,socket,threading,struct,hashlib,base64,select,time
path=sys.argv[2] if len(sys.argv)>2 else ''
args=sys.argv[1:]
if args==['--version']:
 print('codex-cli 0.160.1');sys.exit(0)
if args==['app-server','daemon','version']:
 print(json.dumps({'status':'running','cliVersion':'0.160.1','appServerVersion':'0.160.1'}));sys.exit(0)
if args==['app-server','proxy']:
 s=socket.socket(socket.AF_UNIX);s.connect(os.path.join(os.environ['CODEX_HOME'],'app-server-control','app-server-control.sock'))
 while True:
  ready,_,_=select.select([s,sys.stdin.buffer],[],[])
  for src in ready:
   data=os.read(src.fileno(),65536)
   if not data:sys.exit(0)
   if src is s:sys.stdout.buffer.write(data);sys.stdout.buffer.flush()
   else:s.sendall(data)
if args[0]!='serve':sys.exit(91)
os.makedirs(os.path.dirname(path),mode=0o700,exist_ok=True)
s=socket.socket(socket.AF_UNIX);s.bind(path);s.listen()
loaded={'process-thread','sibling-thread'};lock=threading.Lock()
model='stub-model';effort='low';sandbox={'type':'readOnly'};approval='on-request';started=0
def exact(c,n):
 data=b''
 while len(data)<n:
  more=c.recv(n-len(data))
  if not more:raise EOFError()
  data+=more
 return data
thread=lambda id:{'id':id,'cwd':os.environ['HOME'],'createdAt':1,'updatedAt':1,'status':{'type':'idle'},'turns':[],'path':os.path.join(os.environ['CODEX_HOME'],'rollout.jsonl')}
def session(c):
 global model,effort,sandbox,approval,started
 try:
  raw=b''
  while not raw.endswith(b'\r\n\r\n'):raw+=exact(c,1)
  key=[l.split(b':',1)[1].strip() for l in raw.split(b'\r\n') if l.lower().startswith(b'sec-websocket-key:')][0]
  accept=base64.b64encode(hashlib.sha1(key+b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
  c.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+b'\r\n\r\n')
  while True:
   h=exact(c,2);length=h[1]&127
   if h[0]&15==8:break
   if length==126:length=struct.unpack('!H',exact(c,2))[0]
   elif length==127:length=struct.unpack('!Q',exact(c,8))[0]
   mask=exact(c,4) if h[1]&128 else None;data=exact(c,length)
   if mask:data=bytes(v^mask[i%4] for i,v in enumerate(data))
   frame=json.loads(data);method=frame.get('method','');p=frame.get('params',{})
   with lock:
    with open(os.path.join(os.environ['HOME'],'shared-wire.jsonl'),'a') as f:f.write(json.dumps(frame)+'\n')
    if 'id' not in frame:continue
    if method=='thread/resume' and os.path.exists(os.path.join(os.environ['HOME'],'hold-shared-init')):
     open(os.path.join(os.environ['HOME'],'shared-init-entered'),'w').write('entered')
     while os.path.exists(os.path.join(os.environ['HOME'],'hold-shared-init')):time.sleep(.01)
    if (method=='thread/resume' and os.path.exists(os.path.join(os.environ['HOME'],'fail-shared-init'))) or (method=='thread/read' and os.path.exists(os.path.join(os.environ['HOME'],'fail-shared-observer'))):
     data=json.dumps({'id':frame['id'],'error':{'code':-32600,'message':'injected native consumer refusal'}}).encode();h=bytes([129,len(data)]) if len(data)<126 else bytes([129,126])+struct.pack('!H',len(data));c.sendall(h+data);continue
    if method=='initialize':r={'userAgent':'projmux/0.160.1'}
    elif method in ('thread/start','thread/resume'):
     id=p.get('threadId','process-thread' if started==0 else 'sibling-thread')
     if method=='thread/start':started+=1
     loaded.add(id)
     r={'thread':thread(id),'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval}
    elif method=='turn/start':r={'turn':{'id':'source-turn'}}
    elif method=='thread/read':
     r={'thread':thread(p['threadId'])}
     if not p.get('includeTurns'):r['thread']['turns']=[]
    elif method=='thread/turns/list':
     if p.get('limit')!=1 or p.get('sortDirection')!='desc' or p.get('itemsView')!='notLoaded':raise RuntimeError('unbounded lifecycle request')
     r={'data':[],'nextCursor':None}
    elif method=='thread/list':r={'data':[thread('process-thread'),thread('sibling-thread')],'nextCursor':None}
    elif method=='thread/loaded/list':r={'data':sorted(loaded),'nextCursor':None}
    elif method=='thread/unsubscribe':loaded.discard(p['threadId']);r={'status':'unsubscribed'}
    elif method=='thread/settings/update':
     model=p.get('model',model);effort=p.get('effort',effort);sandbox=p.get('sandboxPolicy',sandbox);approval=p.get('approvalPolicy',approval);r={}
    elif method=='remoteControl/status/read':r={'status':'disabled'}
    else:raise RuntimeError(method)
   data=json.dumps({'id':frame['id'],'result':r}).encode();h=bytes([129,len(data)]) if len(data)<126 else bytes([129,126])+struct.pack('!H',len(data));c.sendall(h+data)
 except (EOFError,BrokenPipeError,ConnectionResetError):pass
 finally:c.close()
while True:
 c,_=s.accept();threading.Thread(target=session,args=(c,),daemon=True).start()
`

func codexHostMoveCLIFixture(t *testing.T) (processCreateCLI, coremetadata.Agent, string, string) {
	t.Helper()
	f := processCodexRelaunchFixture(t)
	// Keep physical native control paths within their public 100-byte bound.
	// A long platform TMPDIR may require private state in this checkout instead;
	// TMPDIR itself and product limits remain unchanged.
	physicalState, err := filepath.EvalSymlinks(filepath.Join(f.root, "state", "projmux"))
	if err != nil {
		t.Fatal(err)
	}
	if len(filepath.Join(physicalState, agentControlDirName, "control-"+strings.Repeat("a", 24)+".sock")) > localipc.MaxSocketPath {
		short := codexHostMoveFixtureShortState(t, os.TempDir(), repoRootForGate(t))
		if err := os.Rename(filepath.Join(f.root, "state", "projmux"), filepath.Join(short, "projmux")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_STATE_HOME", short)
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		f.store = intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
	}
	// Last cleanup before the fixture root goes (t.Cleanup is LIFO): the socket
	// cleanups below stop only what they can reach through a socket, and a
	// broker or observer that is already running when the fixture is created,
	// or a child that owns no socket, outlives them and keeps probing `codex` on
	// its PATH after the root is gone. This stops every process that carries the
	// fixture root or names its state directory, whatever sockets it owns.
	reapPaths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopFixtureProcesses(t, f.root, reapPaths.StateDir) })
	codexHome, err := os.MkdirTemp(os.TempDir(), "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(codexHome) })
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.Remove(filepath.Join(f.root, "tmux")); err != nil {
		t.Fatal(err)
	}
	tmuxDir := hostMoveFixturePhysicalDir(t, filepath.Join(f.root, "tmux-private"))
	t.Setenv("TMUX_TMPDIR", tmuxDir)
	serverScript := filepath.Join(f.root, "shared.py")
	if err := os.WriteFile(serverScript, []byte(codexHostMoveServerFixture), 0600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(os.Getenv("CODEX_HOME"), "app-server-control", "app-server-control.sock")
	server := exec.Command("python3", "-u", serverScript, "serve", socketPath)
	var serverErr bytes.Buffer
	server.Stderr = &serverErr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
		if t.Failed() {
			t.Logf("server stderr: %s", serverErr.String())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(socketPath); return err == nil })
	wrapper := "#!/bin/sh\ncase \"$1 $2\" in\n'--version '| 'app-server proxy' | 'app-server daemon') exec python3 -u " + fmt.Sprintf("%q", serverScript) + " \"$@\";;\n'app-server --listen') exec python3 -u " + fmt.Sprintf("%q", filepath.Join(f.root, "codex-provider.py")) + ";;\nesac\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(f.root, "codex"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(tmuxDir, fmt.Sprintf("tmux-%d", os.Getuid()), "projmux")
	hostCLIOutput(t, f, "config", "apply")
	hostCLIOutput(t, f, "start", "project", "uid:"+f.project)
	pidRaw, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	birth, _, err := localipc.Process(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, _, err := localipc.Process(pid)
		if err == nil && current == birth {
			out, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "#{socket_path}").Output()
			if err == nil && strings.TrimSpace(string(out)) == socket {
				_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
			}
		}
	})
	hostCLIOutput(t, f, "create", "agent", "--provider", "codex", "--profile", "none", "--model", "stub-model", "--effort", "low", "--project", "uid:"+f.project, "--window", "uid:"+f.window, "--name", "codex-host-source", "--", "source context")
	t.Cleanup(func() {
		if t.Failed() {
			reg, _ := f.store.LoadReadOnly()
			raw, _ := json.Marshal(reg)
			t.Logf("registry=%s server=%s", raw, serverErr.String())
			paths, _ := config.DefaultPathsFromEnv()
			ingest, _ := os.ReadFile(filepath.Join(paths.StateDir, "ai-ingest.log"))
			t.Logf("observer=%s", ingest)
			wire, _ := os.ReadFile(filepath.Join(f.root, "shared-wire.jsonl"))
			t.Logf("shared wire=%s", wire)
		}
	})
	var source coremetadata.Agent
	waitCodexCreate(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil || len(reg.Agents) != 1 {
			return false
		}
		source = reg.Agents[0].Clone()
		pane, ok := reg.Pane(source.Status.PaneRef)
		return ok && pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.Authority != nil && pane.Status.Activation.Codex.Authority.Valid()
	})
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	sockets, err := filepath.Glob(filepath.Join(paths.StateDir, "broker", "*.sock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range sockets {
		socketIdentity, err := localipc.InspectOwnedSocket(address)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		process, _, err := localipc.PeerProcess(conn)
		_ = conn.Close()
		if err != nil || process.OwnerUID != uint32(os.Getuid()) {
			t.Fatalf("fixture broker peer: %v", err)
		}
		t.Cleanup(func() {
			current, _, err := localipc.Process(process.PID)
			observed, socketErr := localipc.InspectOwnedSocket(address)
			if err == nil && current.OwnerUID == uint32(os.Getuid()) && current == process && socketErr == nil && observed == socketIdentity {
				if err := syscall.Kill(process.PID, syscall.SIGTERM); err == nil {
					waitCodexFixturePeerExit(t, process)
				}
			}
		})
	}
	// Native observers outlive the CLI that launched them. Stop only peers
	// authenticated through this fixture's private control sockets, before
	// removing its state root; otherwise their final writes recreate that root.
	t.Cleanup(func() {
		controls, _ := filepath.Glob(filepath.Join(paths.StateDir, "agent-control", "*.sock"))
		for _, address := range controls {
			identity, err := localipc.InspectOwnedSocket(address)
			if err != nil {
				continue
			}
			peer, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
			if err != nil {
				continue
			}
			process, _, err := localipc.PeerProcess(peer)
			_ = peer.Close()
			if err != nil || process.OwnerUID != uint32(os.Getuid()) {
				continue
			}
			current, _, err := localipc.Process(process.PID)
			observed, socketErr := localipc.InspectOwnedSocket(address)
			if err != nil || current != process || socketErr != nil || observed != identity {
				continue
			}
			if err = syscall.Kill(process.PID, syscall.SIGTERM); err != nil {
				continue
			}
			waitCodexFixturePeerExit(t, process)
		}
	})
	return f, source, source.Status.PaneRef, socket
}

func codexHostMoveFixtureShortState(t *testing.T, roots ...string) string {
	t.Helper()
	letters := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for _, root := range roots {
		physical, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < len(letters)*len(letters); index++ {
			candidate := filepath.Join(physical, string([]byte{letters[index/len(letters)], letters[index%len(letters)]}))
			control := filepath.Join(candidate, "projmux", agentControlDirName, "control-"+strings.Repeat("a", 24)+".sock")
			if len(control) > localipc.MaxSocketPath {
				break
			}
			if err := os.Mkdir(candidate, 0700); err == nil {
				t.Cleanup(func() { _ = os.RemoveAll(candidate) })
				return candidate
			} else if !os.IsExist(err) {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("no private short fixture state available")
	return ""
}

func TestCodexHostMoveActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f, source, oldPane, socket := codexHostMoveCLIFixture(t)
	hostCLIOutput(t, f, "create", "agent", "--provider", "codex", "--profile", "none", "--model", "stub-model", "--effort", "low", "--project", "uid:"+f.project, "--window", "uid:"+f.window, "--name", "codex-host-sibling", "--", "sibling context")
	var sibling coremetadata.Agent
	var siblingPane coremetadata.Pane
	siblingCtx, siblingCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer siblingCancel()
	waitCodexCreate(t, siblingCtx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		for _, agent := range reg.Agents {
			if agent.Metadata.UID == source.Metadata.UID {
				continue
			}
			pane, ok := reg.Pane(agent.Status.PaneRef)
			if ok && pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.ThreadID == "sibling-thread" && pane.Status.Activation.Codex.Authority != nil && pane.Status.Activation.Codex.Authority.Valid() {
				sibling = agent.Clone()
				siblingPane = pane.Clone()
				return true
			}
		}
		return false
	})
	ref := "uid:" + source.Metadata.UID
	before, _ := f.store.LoadReadOnly()
	oldNativePane, _ := before.Pane(oldPane)
	oldAuthority := *oldNativePane.Status.Activation.Codex.Authority
	wireBefore, _ := os.ReadFile(filepath.Join(f.root, "shared-wire.jsonl"))
	var preview agentRelaunchResult
	if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--dry-run", "-o", "json"), &preview); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.LoadReadOnly()
	wireAfter, _ := os.ReadFile(filepath.Join(f.root, "shared-wire.jsonl"))
	if !reflect.DeepEqual(before, after) || !bytes.Equal(wireBefore, wireAfter) || preview.CurrentHost != "tmux" || preview.TargetHost != "process" {
		t.Fatal("dry-run mutated source")
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.ConfigDir, "agent-guidance.md"), []byte("HOST-MOVE-BASE-B"), 0600); err != nil {
		t.Fatal(err)
	}
	guidance := (&mutationruntime.GuidanceService{Paths: func() (config.Paths, error) { return paths, nil }}).PlanProcessAgentGuidance()
	wantBase := guidance.CodexDeveloperInstructions(source.Metadata.UID, "")
	wantDigest := projectlinks.Digest([]byte(wantBase))
	if wantDigest == source.Metadata.Annotations[mutationruntime.CodexManagedBaseDigest] {
		t.Fatal("fixture did not change managed base A to B")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, moved := startProcessRelaunchCLI(t, ctx, f, ref, "--host", "process", "--socket-path", socket, "--yes")
	if moved.NewPaneUID == oldPane || moved.AgentUID != source.Metadata.UID {
		t.Fatal(moved)
	}
	record := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.ThreadID == "process-thread" && r.TurnID == ""
	})
	transferred, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	applied, present := transferred.Agent(source.Metadata.UID)
	if !present || applied.Metadata.Annotations[mutationruntime.CodexManagedBaseDigest] != wantDigest || applied.Metadata.Annotations[mutationruntime.CodexManagedBaseStatus] != "base-delivered" {
		t.Fatal("dedicated transfer did not record its accepted managed base B")
	}
	trace, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	resumes := 0
	for line := range bytes.SplitSeq(trace, []byte("\n")) {
		var frame struct {
			Method string
			Params struct {
				Base      string  `json:"baseInstructions"`
				Developer *string `json:"developerInstructions"`
			}
		}
		if json.Unmarshal(line, &frame) == nil && frame.Method == "thread/resume" {
			resumes++
			if frame.Params.Base != wantBase || frame.Params.Developer != nil {
				t.Fatal("dedicated transfer did not send base B without developer instructions")
			}
		}
	}
	if resumes != 1 {
		t.Fatalf("dedicated transfer resumed %d times, want exactly one", resumes)
	}
	var back agentRelaunchResult
	if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes", "-o", "json"), &back); err != nil {
		t.Fatal(err)
	}
	if back.AgentUID != source.Metadata.UID || back.NewPaneUID == record.Binding.PaneUID || back.CurrentHost != "process" || back.TargetHost != "tmux" {
		t.Fatal(back)
	}
	_ = owner.input.Close()
	if err := owner.cmd.Wait(); err != nil {
		t.Fatalf("old owner: %v %s", err, owner.stderr.String())
	}
	owner.done = true
	waitCodexCreate(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		agent, ok := reg.Agent(source.Metadata.UID)
		if !ok {
			return false
		}
		pane, present := reg.Pane(agent.Status.PaneRef)
		return present && pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.Authority != nil && pane.Status.Activation.Codex.Authority.Valid()
	})
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(source.Metadata.UID)
	newNativePane, _ := reg.Pane(agent.Status.PaneRef)
	newAuthority := newNativePane.Status.Activation.Codex.Authority
	if newAuthority.BrokerRuntimeID != oldAuthority.BrokerRuntimeID || newAuthority.StateDomainID != oldAuthority.StateDomainID || newAuthority.EndpointGenerationID != oldAuthority.EndpointGenerationID || newAuthority.ConnectionEpoch != oldAuthority.ConnectionEpoch {
		t.Fatal("shared broker/endpoint/connection changed across move")
	}
	if agent.Status.SessionRef.ConversationID() != "process-thread" {
		t.Fatal("thread changed")
	}
	currentSibling, ok := reg.Agent(sibling.Metadata.UID)
	currentSiblingPane, present := reg.Pane(siblingPane.Metadata.UID)
	if !ok || !present || currentSibling.Status.PaneRef != sibling.Status.PaneRef || !currentSibling.Status.SessionRef.SameConversation(sibling.Status.SessionRef) || !reflect.DeepEqual(currentSibling.Spec, sibling.Spec) || !reflect.DeepEqual(currentSiblingPane.Status.Activation, siblingPane.Status.Activation) {
		t.Fatal("sibling writer identity changed")
	}
	if out, err := exec.Command("tmux", "-S", socket, "display-message", "-t", siblingPane.Status.Activation.RuntimeID, "-p", "#{pane_id}").Output(); err != nil || strings.TrimSpace(string(out)) != siblingPane.Status.Activation.RuntimeID {
		t.Fatal("sibling runtime disappeared")
	}
	sharedWire, _ := os.ReadFile(filepath.Join(f.root, "shared-wire.jsonl"))
	for line := range bytes.SplitSeq(sharedWire, []byte("\n")) {
		var frame struct {
			Method string
			Params struct {
				Thread string `json:"threadId"`
			}
		}
		if json.Unmarshal(line, &frame) == nil && frame.Method == "thread/unsubscribe" && frame.Params.Thread == "sibling-thread" {
			t.Fatal("move retired sibling subscription")
		}
	}
	wire, _ := os.ReadFile(f.trace)
	if bytes.Contains(wire, []byte("thread/start")) {
		t.Fatal("dedicated writer created replacement thread")
	}
}

func TestCodexHostMoveActualCLIRecovery(t *testing.T) {
	for _, fault := range []string{"", "init", "observer", "split"} {
		t.Run(fault, func(t *testing.T) { codexHostMoveRecoveryCLI(t, fault) })
	}
}

// TestCodexHostMoveFixtureReapsBaselineProcess pins the fixture's final reap.
// On every platform it checks that the shared fixture's cleanup removes the
// fixture root. On Linux, which reads /proc, it also checks the selection
// contract: a process that carries the fixture root, is still running when
// the final cleanup begins, and owns no socket the fixture's socket cleanups
// can see is stopped before the fixture root is removed. The run 1698
// survivor, a broker alive at the last cleanup, is such a process. Other
// platforms say nothing about process reaping here. The decoy's own pid, uid
// and birth are kept and the verdict is its exit by SIGTERM, never a scan.
func TestCodexHostMoveFixtureReapsBaselineProcess(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	_, procErr := os.Stat("/proc/self/environ")
	var (
		root     string
		stateDir string
		decoy    coremetadata.ProcessIdentity
		exited   = make(chan error, 1)
		cmd      *exec.Cmd
	)
	// Registered before the fixture, so it runs after every fixture cleanup.
	t.Cleanup(func() {
		if root != "" {
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("fixture root %s was not removed by the fixture cleanup: %v", root, err)
			}
		}
		if cmd == nil {
			return
		}
		select {
		case err := <-exited:
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Errorf("baseline decoy pid=%d ended without a signal: %v", decoy.PID, err)
				return
			}
			if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
				t.Errorf("baseline decoy pid=%d was not stopped by the fixture reap's SIGTERM: %v", decoy.PID, err)
			}
			return
		case <-time.After(3 * time.Second):
		}
		t.Errorf("baseline fixture process survived the fixture cleanup: pid=%d birth=%v", decoy.PID, decoy)
		// The verdict is recorded; now collect everything the fixture owned,
		// including a real baseline process besides the decoy, by the same
		// exact-identity selection the fixture uses.
		stopFixtureProcesses(t, root, stateDir)
		if current, _, err := localipc.Process(decoy.PID); err == nil && current == decoy {
			if err := syscall.Kill(decoy.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("could not stop the surviving decoy pid=%d: %v", decoy.PID, err)
			}
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Errorf("surviving decoy pid=%d is still running after SIGKILL", decoy.PID)
		}
	})
	f, _, _, _ := codexHostMoveCLIFixture(t)
	root = f.root
	if procErr != nil {
		return
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	stateDir = paths.StateDir
	started := exec.Command("sleep", "300")
	started.Env = append(os.Environ(), "PMX_TEST_PROCESS_ROOT="+f.root)
	started.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := started.Start(); err != nil {
		t.Fatal(err)
	}
	identity, _, err := localipc.Process(started.Process.Pid)
	if err != nil || identity.OwnerUID != uint32(os.Getuid()) {
		_ = started.Process.Kill()
		_ = started.Wait()
		t.Fatalf("decoy identity: %v", err)
	}
	decoy, cmd = identity, started
	go func() { exited <- started.Wait() }()
}

// Exercise the staged consumer through the copied CLI. The provider is held
// after admission, so a different Agent's mutation proves the Registry lock is
// free. Changing the reserved target then makes the successful init stale.
func TestCodexHostMoveActualCLIStagedCAS(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	f, source, _, socket := codexHostMoveCLIFixture(t)
	provider := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(provider)
	if err != nil {
		t.Fatal(err)
	}
	failed := bytes.Replace(raw, []byte("elif method=='thread/resume':"), []byte("elif method=='thread/resume':\n  sys.exit(91)"), 1)
	if bytes.Equal(raw, failed) {
		t.Fatal("fault injection missing")
	}
	if err = os.WriteFile(provider, failed, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref := "uid:" + source.Metadata.UID
	if out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--yes").CombinedOutput(); err == nil || !bytes.Contains(out, []byte("recover")) {
		t.Fatalf("failed dedicated target: %v %s", err, out)
	}
	if err = os.WriteFile(provider, raw, 0600); err != nil {
		t.Fatal(err)
	}
	hold := filepath.Join(f.root, "hold-shared-init")
	if err = os.WriteFile(hold, []byte("hold"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(hold) })
	cmd := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(filepath.Join(f.root, "shared-init-entered")); return err == nil })
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Agent(source.Metadata.UID)
	pane, ok := reg.Pane(target.Status.PaneRef)
	if target.Status.Phase != coremetadata.PhasePending || !ok || pane.Status.Activation.RuntimeID != "" || pane.Status.Activation.Generation == "" || pane.Status.Activation.OperationID == "" || pane.Status.Activation.Codex != nil {
		t.Fatal("provider wait lacks actual unstarted reservation")
	}
	mutated := make(chan error, 1)
	go func() {
		_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			mut := coremetadata.Mutator{}
			_, err := mut.CreateAgent(reg, f.window, coremetadata.CreateAgentOptions{Name: "independent-source", Provider: "codex", OperationID: "independent-op"})
			if err != nil {
				return err
			}
			agent, _ := reg.Agent(source.Metadata.UID)
			if agent.Metadata.Annotations == nil {
				agent.Metadata.Annotations = map[string]string{}
			}
			agent.Metadata.Annotations["test-stale-target"] = "changed"
			return nil
		})
		mutated <- err
	}()
	select {
	case err = <-mutated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = os.Remove(hold)
		<-done
		t.Fatal("provider init held Registry mutation lock")
	}
	if err = os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil || !bytes.Contains(output.Bytes(), []byte("reservation changed")) {
		t.Fatalf("stale final CAS accepted: %v %s", err, output.String())
	}
	reg, err = f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	target, _ = reg.Agent(source.Metadata.UID)
	if target.Metadata.Annotations["test-stale-target"] != "changed" || target.Status.Phase != coremetadata.PhasePending || target.Status.PaneRef != pane.Metadata.UID {
		t.Fatal("stale failure overwrote reserved target")
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	journals, _ := filepath.Glob(filepath.Join(paths.StateDir, "codex-host-transfers", "*.json"))
	if len(journals) != 1 {
		t.Fatalf("pending journal missing: %v", journals)
	}
	record, err := readCodexHostTransfer(journals[0])
	if err != nil || record.NativeBinding == nil || record.Phase != "native-initialized" {
		t.Fatalf("successful init effects lost: %+v %v", record, err)
	}
	// An explicit recovery must also preserve the changed target, rather than
	// restoring an earlier recipe over it after successful provider admission.
	if out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput(); err == nil {
		t.Fatalf("changed-target recovery accepted: %s", out)
	}
}
func codexHostMoveRecoveryCLI(t *testing.T, fault string) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	f, source, oldPane, socket := codexHostMoveCLIFixture(t)
	ref := "uid:" + source.Metadata.UID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	codexHostMovePendingJournal(t, ctx, f, ref, socket)
	codexHostMoveFailRecovery(t, ctx, f, ref, socket, fault)
	hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes")
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	current, ok := reg.Agent(source.Metadata.UID)
	if !ok || current.Status.PaneRef == oldPane || current.Status.Phase != coremetadata.PhaseRunning || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) {
		t.Fatal("recovery did not restore same-thread fresh native writer")
	}
	if current.Metadata.Annotations["projmux.io/codex-managed-base-status"] != "base-unconfirmed" {
		t.Fatal("recovery lost the confirmed shared-resume metadata effect")
	}
	pane, present := reg.Pane(current.Status.PaneRef)
	if !present || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() {
		t.Fatal("recovery lacks verified native authority")
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	journals, err := filepath.Glob(filepath.Join(paths.StateDir, "codex-host-transfers", "*.completed.json"))
	if err != nil || len(journals) != 1 {
		t.Fatalf("completed evidence: %v %v", journals, err)
	}
	record, err := readCodexHostTransfer(journals[0])
	if err != nil || record.TerminatedTarget == nil || record.TerminatedTarget.Status.LastTermination == nil {
		t.Fatalf("recovery lost target actual Wait: %v", err)
	}
}

// The public SDK relaunch of a Codex Agent meets the journal an unfinished host
// transfer retains exactly as `agent relaunch` does: on the Offline source a
// failed process move restores, on a reserved tmux target, and on the live
// process target an owner killed mid-launch leaves. On the same fixture a dry
// run and inexact requests refuse with the CLI's own text before any effect,
// as a usage error from the CLI and an invalid request from the SDK,
// and the exact recovery request (Host tmux, Confirm, nothing else) runs the
// CLI recovery: each variant ends once, through one surface, in the same
// Agent, Pane and journal state or the same refusal.
func TestCodexHostTransferSDKCLIParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	for _, start := range []string{"offline-source", "tmux-target", "live-process-source"} {
		var recovered []string
		for _, sdk := range []bool{false, true} {
			t.Run(start+"/"+map[bool]string{false: "cli-recovers", true: "sdk-recovers"}[sdk], func(t *testing.T) {
				recovered = append(recovered, codexHostTransferSDKCLIParity(t, start, sdk))
			})
		}
		if len(recovered) == 2 && recovered[0] != recovered[1] {
			t.Fatalf("%s: recovered state differs CLI %q SDK %q", start, recovered[0], recovered[1])
		}
		t.Logf("%s: recovered state %q", start, recovered)
	}
}

func codexHostTransferSDKCLIParity(t *testing.T, start string, sdk bool) string {
	f, source, oldPane, socket := codexHostMoveCLIFixture(t)
	ref := "uid:" + source.Metadata.UID
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if start == "live-process-source" {
		// The owner dies after recording launching: the journal stays pending
		// on a live reciprocal process Pane binding.
		codexHostMoveOwnerDeath(t, ctx, f, ref, socket)
	} else {
		// The failed move restores the Offline tmux source, journal pending.
		codexHostMovePendingJournal(t, ctx, f, ref, socket)
	}
	if start == "tmux-target" {
		// A failed native restore then leaves its reserved tmux Pane with the
		// journal still pending: the route the SDK takes for a tmux Agent.
		codexHostMoveFailRecovery(t, ctx, f, ref, socket, "split")
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source.Metadata.UID))
	journal := filepath.Join(paths.StateDir, "codex-host-transfers", fmt.Sprintf("%x.json", sum[:]))
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	processPane, _ := processResumePane(reg, source.Metadata.UID)
	fixture, _ := reg.Agent(source.Metadata.UID)
	switch start {
	case "offline-source":
		if processPane != nil || fixture.Status.Phase != coremetadata.PhaseOffline || fixture.Status.PaneRef != "" {
			t.Fatalf("fixture must leave the Offline tmux source: pane=%+v status=%+v", processPane, fixture.Status)
		}
	case "tmux-target":
		if processPane != nil || fixture.Status.Reason != "native-transfer-planned" {
			t.Fatalf("fixture must leave the reserved native tmux target: pane=%+v status=%+v", processPane, fixture.Status)
		}
	case "live-process-source":
		if processPane == nil || processPane.Status.ProcessSession == nil || fixture.Status.PaneRef != processPane.Metadata.UID {
			t.Fatalf("fixture must leave a reciprocal process Pane binding: pane=%+v status=%+v", processPane, fixture.Status)
		}
	}
	t.Logf("%s fixture: phase=%s reason=%s pane=%s", start, fixture.Status.Phase, fixture.Status.Reason, fixture.Status.PaneRef)
	client, err := projmux.NewClient(projmux.Options{HomeDir: os.Getenv("HOME"), ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary, SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	evidence := func() string {
		t.Helper()
		registry, err := os.ReadFile(f.store.Path())
		if err != nil {
			t.Fatal(err)
		}
		pending, err := os.ReadFile(journal)
		if err != nil {
			t.Fatal(err)
		}
		panes, err := exec.Command("tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}").Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(registry) + "\x00" + string(pending) + "\x00" + string(panes)
	}
	for _, tc := range []struct {
		name string
		cli  []string
		sdk  core.RelaunchAgentRequest
		text string
	}{
		{"dry-run", []string{"--dry-run"}, core.RelaunchAgentRequest{DryRun: true}, "agent relaunch: pending Codex transfer recovery; nothing was changed; recover with: "},
		{"no-host", []string{"--yes"}, core.RelaunchAgentRequest{Confirm: true}, "agent relaunch: recover the previous exact recipe first; recover with: "},
		{"settings-change", []string{"--host", "tmux", "--yes", "--effort", "high"}, core.RelaunchAgentRequest{Host: "tmux", Confirm: true, Effort: "high"}, "agent relaunch: recover the previous exact recipe first; recover with: "},
	} {
		before := evidence()
		args := append([]string{"agent", "relaunch", ref, "--socket-path", socket}, tc.cli...)
		var stderr bytes.Buffer
		cli := exec.CommandContext(ctx, f.binary, args...)
		cli.Stderr = &stderr
		out, err := cli.Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("%s: CLI did not refuse with a usage error: err=%v out=%s", tc.name, err, out)
		}
		if before != evidence() {
			t.Fatalf("%s: CLI refusal changed state", tc.name)
		}
		q := tc.sdk
		q.Ref, q.SocketPath = ref, socket
		got, err := client.RelaunchAgent(ctx, q)
		var refused *projmux.Refusal
		if !errors.As(err, &refused) || refused.Code != projmux.CodeRelaunchRequestInvalid {
			t.Fatalf("%s: SDK did not refuse the request as invalid: result=%+v err=%v", tc.name, got, err)
		}
		want := tc.text + codexTransferRecoveryCommand(source.Metadata.UID, agentRelaunchRequest{socket: deleteSocketFlags{socketPath: socket}})
		if refused.Error() != want || strings.TrimSpace(stderr.String()) != want {
			t.Fatalf("%s: refusal text differs\nSDK=%q\nCLI=%q\nwant=%q", tc.name, refused.Error(), stderr.String(), want)
		}
		if before != evidence() {
			t.Fatalf("%s: SDK refusal changed state", tc.name)
		}
		t.Logf("%s: CLI and SDK refused with %q", tc.name, want)
	}
	var result core.RelaunchAgentResult
	var refusal string
	if sdk {
		result, err = client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, Host: "tmux", Confirm: true, SocketPath: socket})
		if err != nil {
			var refused *projmux.Refusal
			if start != "live-process-source" || !errors.As(err, &refused) {
				t.Fatal(err)
			}
			refusal = fmt.Sprintf("code=%s text=%s", refused.Code, refused.Error())
		}
	} else if start == "live-process-source" {
		var stderr bytes.Buffer
		cli := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes")
		cli.Stderr = &stderr
		if _, err := cli.Output(); err != nil {
			refusal = fmt.Sprintf("code=%s text=%s", projmux.CodeRelaunchExecutionFailed, strings.TrimSpace(stderr.String()))
		}
	} else {
		hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes")
	}
	if refusal != "" {
		// The exact recovery may refuse on a target whose owner died; both
		// surfaces must then refuse alike and leave the journal pending.
		t.Logf("exact recovery refused: %s", refusal)
		if _, err := os.Stat(journal); err != nil {
			t.Fatalf("refused recovery lost the pending journal: %v", err)
		}
		return "refused " + normalizeFixtureRefusal(refusal)
	}
	reg, err = f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	current, ok := reg.Agent(source.Metadata.UID)
	if !ok || current.Status.PaneRef == oldPane || current.Status.Phase != coremetadata.PhaseRunning || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) {
		t.Fatal("recovery did not restore same-thread fresh native writer")
	}
	pane, present := reg.Pane(current.Status.PaneRef)
	if !present || pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() {
		t.Fatal("recovery lacks a verified native tmux writer")
	}
	if sdk && (result.Outcome != personaOutcomeResumed || result.DryRun || result.CurrentHost != "tmux" || result.TargetHost != "tmux" || result.AgentUID != source.Metadata.UID || result.NewPaneUID != current.Status.PaneRef) {
		t.Fatalf("SDK recovery result %+v, Agent pane %s", result, current.Status.PaneRef)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("pending journal remains: %v", err)
	}
	archives, err := filepath.Glob(filepath.Join(paths.StateDir, "codex-host-transfers", "*.completed.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("completed evidence: %v %v", archives, err)
	}
	return fmt.Sprintf("phase=%s runtime=%s managed-base=%s archives=%d", current.Status.Phase, pane.Spec.Runtime.EffectiveKind(), current.Metadata.Annotations["projmux.io/codex-managed-base-status"], len(archives))
}

var fixtureCodexHostSocket = regexp.MustCompile(`pmx-ce-[0-9a-f]+/codex-host\.sock`)

// normalizeFixtureRefusal replaces only the per-fixture Codex host endpoint directory; every other byte stays compared.
func normalizeFixtureRefusal(text string) string {
	return fixtureCodexHostSocket.ReplaceAllString(text, "pmx-ce-<hash>/codex-host.sock")
}

// codexHostMoveFailRecovery makes one exact recovery attempt fail at the named
// native stage; the journal stays pending and the stage is unblocked again.
func codexHostMoveFailRecovery(t *testing.T, ctx context.Context, f processCreateCLI, ref, socket, fault string) {
	t.Helper()
	if fault == "" {
		return
	}
	if fault == "split" {
		realTmux, err := exec.LookPath("tmux")
		if err != nil {
			t.Fatal(err)
		}
		wrapper := "#!/bin/sh\ncase \" $* \" in *' split-window '*) if test -f " + fmt.Sprintf("%q", filepath.Join(f.root, "fail-shared-split")) + "; then exit 91; fi;; esac\nexec " + fmt.Sprintf("%q", realTmux) + " \"$@\"\n"
		if err = os.WriteFile(filepath.Join(f.root, "tmux"), []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(f.root, "fail-shared-"+fault)
	if err := os.WriteFile(marker, []byte("fail"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, failedErr := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput()
	if failedErr == nil {
		t.Fatalf("native %s failure was bypassed: %s", fault, failed)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
}

// codexHostMoveOwnerDeath kills the owner of a tmux-to-process move after it
// records launching, while the dedicated target holds thread/resume, then
// waits for the supervisor to retire the held provider. The Registry keeps the
// reciprocal process Pane binding and the journal stays pending.
func codexHostMoveOwnerDeath(t *testing.T, ctx context.Context, f processCreateCLI, ref, socket string) {
	t.Helper()
	providerPath := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.root, "resume-held")
	original := append([]byte(nil), raw...)
	raw = bytes.Replace(raw, []byte("elif method=='thread/resume':"), []byte("elif method=='thread/resume':\n  open("+fmt.Sprintf("%q", marker)+",'w').write(str(os.getpid()));__import__('time').sleep(30)"), 1)
	if bytes.Equal(original, raw) {
		t.Fatal("dedicated resume hold was not injected")
	}
	if err = os.WriteFile(providerPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	owner := exec.Command(f.binary, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--yes")
	owner.Stdout, owner.Stderr = &out, &out
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})
	var held []byte
	waitCodexCreate(t, ctx, func() bool { held, err = os.ReadFile(marker); return err == nil && len(held) > 0 })
	if err = owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	t.Logf("killed owner pid=%d: %v %s", owner.Process.Pid, owner.Wait(), out.String())
	// Only the dead owner writes the Registry; wait for the held provider to
	// exit so nothing outlives the fixture.
	waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(filepath.Join("/proc", string(held))); return os.IsNotExist(err) })
	if err = os.WriteFile(providerPath, original, 0600); err != nil {
		t.Fatal(err)
	}
}

// codexHostMovePendingJournal leaves the journal a failed tmux-to-process move
// retains: the dedicated target fails thread/resume, then the provider is restored.
func codexHostMovePendingJournal(t *testing.T, ctx context.Context, f processCreateCLI, ref, socket string) {
	t.Helper()
	providerPath := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), raw...)
	raw = bytes.Replace(raw, []byte("elif method=='thread/resume':"), []byte("elif method=='thread/resume':\n  sys.exit(91)"), 1)
	if bytes.Equal(original, raw) {
		t.Fatal("dedicated resume fault was not injected")
	}
	if err = os.WriteFile(providerPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--yes").CombinedOutput()
	t.Logf("injected failure=%v %s", err, out)
	if err == nil || !bytes.Contains(out, []byte("recover")) {
		t.Fatalf("failed target: %v %s", err, out)
	}
	if err = os.WriteFile(providerPath, original, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexHostMoveActualCLIPrepareAbortTerminal(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	f, source, _, socket := codexHostMoveCLIFixture(t)
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	current, _ := reg.Agent(source.Metadata.UID)
	pane, _ := reg.Pane(current.Status.PaneRef)
	authority := pane.Status.Activation.Codex.Authority
	domain, err := codexBrokerStateDomain(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := codexbroker.NewEndpointKey(authority.StateDomainID, authority.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	// The runtime that holds this binding was launched by the CLI binary under
	// test, so it is reached by the identity it granted, not by this test
	// binary's image.
	discovery, err := codexBrokerRuntimeDiscovery(domain, key, authority.BrokerRuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := codexbroker.DialTransfer(ctx, discovery, codexbroker.DialConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	from := codexbroker.TransferSource{Project: f.project, Window: f.window, Agent: current.Metadata.UID, Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation, Operation: pane.Status.Activation.OperationID, PaneRuntimeID: pane.Status.Activation.RuntimeID, RuntimeID: authority.BrokerRuntimeID, Thread: pane.Status.Activation.Codex.ThreadID, Endpoint: key, Fence: codexbroker.Fence{Connection: codexbroker.ConnectionEpoch(authority.ConnectionEpoch), Binding: codexbroker.BindingEpoch(authority.BindingEpoch)}}
	pending, err := codexbroker.NewTransferReceipt(from)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// Match the product's digest path, using actual records before admission.
	sum := sha256.Sum256([]byte(current.Metadata.UID))
	path := filepath.Join(paths.StateDir, "codex-host-transfers", fmt.Sprintf("%x.json", sum[:]))
	record := &codexHostTransferRecord{Version: 1, Source: current.Clone(), Pane: pane.Clone(), Receipt: pending, Phase: "preparing"}
	if err = writeCodexHostTransfer(path, record); err != nil {
		t.Fatal(err)
	}
	transfer, err := conn.PrepareTransfer(ctx, pending)
	if err != nil {
		t.Fatal(err)
	}
	if err = transfer.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.InspectPrepareNoEffect(ctx, pending); err != nil {
		t.Fatal(err)
	}
	for _, drift := range []string{"annotations", "full-session"} {
		_, _, err = f.store.UpdateConvergent(func(working *coremetadata.Registry) error {
			a, _ := working.Agent(source.Metadata.UID)
			if drift == "annotations" {
				if a.Metadata.Annotations == nil {
					a.Metadata.Annotations = map[string]string{}
				}
				a.Metadata.Annotations["fixture-drift"] = "changed"
			} else {
				a.Status.SessionRef.ObservedAt = a.Status.SessionRef.ObservedAt.Add(time.Second)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out, refused := exec.CommandContext(ctx, f.binary, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput()
		if refused == nil {
			t.Fatalf("changed %s cleared journal: %s", drift, out)
		}
		if _, err = os.Stat(path); err != nil {
			t.Fatal("changed source evidence removed", err)
		}
		_, _, err = f.store.UpdateConvergent(func(working *coremetadata.Registry) error {
			a, _ := working.Agent(source.Metadata.UID)
			*a = record.Source.Clone()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(intmetadata.PathFor(paths.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	hostCLIOutput(t, f, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "tmux", "--socket-path", socket, "--yes")
	after, err := os.ReadFile(intmetadata.PathFor(paths.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("no-effect cleanup mutated Registry")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preparing journal retained: %v", err)
	}
	final, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := final.Agent(source.Metadata.UID)
	if !reflect.DeepEqual(*got, record.Source) {
		t.Fatal("no-effect cleanup changed source")
	}
}

func TestCodexHostMoveActualCLIArchiveFailure(t *testing.T) {
	for _, persistFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(persistFailure), func(t *testing.T) { codexHostMoveArchiveFailureCLI(t, persistFailure, false) })
	}
	t.Run("regular-conflict", func(t *testing.T) { codexHostMoveArchiveFailureCLI(t, false, true) })
}

func codexHostMoveArchiveFailureCLI(t *testing.T, persistFailure, archiveConflict bool) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	f, source, _, socket := codexHostMoveCLIFixture(t)
	provider := filepath.Join(f.root, "codex-provider.py")
	raw, err := os.ReadFile(provider)
	if err != nil {
		t.Fatal(err)
	}
	entered := filepath.Join(f.root, "dedicated-held")
	release := filepath.Join(f.root, "dedicated-release")
	injection := "elif method=='thread/resume':\n  import time\n  open(" + fmt.Sprintf("%q", entered) + ",'w').write('held')\n  while not os.path.exists(" + fmt.Sprintf("%q", release) + "):time.sleep(0.01)"
	held := bytes.Replace(raw, []byte("elif method=='thread/resume':"), []byte(injection), 1)
	if bytes.Equal(raw, held) {
		t.Fatal("missing dedicated hold injection")
	}
	if err = os.WriteFile(provider, held, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "process", "--socket-path", socket, "--yes")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitCodexCreate(t, ctx, func() bool { _, err := os.Stat(entered); return err == nil })
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(paths.StateDir, "codex-host-transfers", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	record, err := readCodexHostTransfer(files[0])
	if err != nil {
		t.Fatal(err)
	}
	archive := files[0] + "." + record.Receipt.Token + ".completed.json"
	var conflictBytes []byte
	if archiveConflict {
		conflict := *record
		conflict.Source = record.Source.Clone()
		conflict.Source.Metadata.Name = "conflicting-archive-source"
		conflict.Phase = "completed"
		if err = writeCodexHostTransfer(archive, &conflict); err != nil {
			t.Fatal(err)
		}
		conflictBytes, err = os.ReadFile(archive)
	} else {
		err = os.Mkdir(archive, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	if persistFailure {
		terminationPath := filepath.Join(paths.StateDir, terminationJournalFile)
		if _, existingErr := os.Stat(terminationPath); existingErr == nil {
			if err = os.Rename(terminationPath, terminationPath+".fixture-before-fault"); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.Mkdir(terminationPath, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if persistFailure {
		if err == nil || !bytes.Contains(output.Bytes(), []byte("retirement unknown")) {
			t.Fatalf("undurable Wait fault=%v %s", err, output.String())
		}
		reg, readErr := f.store.LoadReadOnly()
		if readErr != nil {
			t.Fatal(readErr)
		}
		target, _ := reg.Agent(source.Metadata.UID)
		if target.Status.PaneRef != record.Target.Pane || coremetadata.MatchesProcessWait(metadataProcessBinding(record.Target), target.Status.LastTermination) {
			t.Fatal("undurable Wait restored source or fabricated proof")
		}
		if _, readErr = os.Stat(files[0]); readErr != nil {
			t.Fatal("undurable Wait lost transfer evidence", readErr)
		}
		return
	}
	if err == nil || !bytes.Contains(output.Bytes(), []byte("Wait persisted")) {
		t.Fatalf("archive fault=%v %s", err, output.String())
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	target, _ := reg.Agent(source.Metadata.UID)
	pane, ok := reg.Pane(record.Target.Pane)
	if !ok || target.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(metadataProcessBinding(record.Target), pane.Status.LastTermination) || !coremetadata.SameProcessWait(pane.Status.LastTermination, target.Status.LastTermination) {
		t.Fatal("archive failure lost durable target Wait")
	}
	journal, err := terminationJournalForRegistryPath(intmetadata.PathFor(paths.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := journal.read()
	if err != nil || len(receipts) == 0 {
		t.Fatal("actual termination journal missing", err)
	}
	if archiveConflict {
		after, readErr := os.ReadFile(archive)
		pending, pendingErr := readCodexHostTransfer(files[0])
		if readErr != nil || !bytes.Equal(conflictBytes, after) || pendingErr != nil || pending == nil || pending.Phase != "handoff-retired" || pending.TerminatedTarget == nil || !coremetadata.SameProcessWait(pending.TerminatedTarget.Status.LastTermination, pane.Status.LastTermination) {
			t.Fatal("conflicting archive or pending actual Wait evidence lost", readErr, pendingErr)
		}
		beforeRegistry, readErr := os.ReadFile(intmetadata.PathFor(paths.StateDir))
		if readErr != nil {
			t.Fatal(readErr)
		}
		out, refused := exec.CommandContext(ctx, f.binary, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput()
		afterRegistry, readErr := os.ReadFile(intmetadata.PathFor(paths.StateDir))
		after, archiveErr := os.ReadFile(archive)
		if refused == nil || !bytes.Contains(out, []byte("completed archive changed")) || readErr != nil || archiveErr != nil || !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(conflictBytes, after) {
			t.Fatalf("conflicting archive recovery admitted or mutated evidence: %v %s", refused, out)
		}
	}
	stable := target.Clone()
	for _, drift := range []string{"annotations", "full-session"} {
		_, _, err = f.store.UpdateConvergent(func(working *coremetadata.Registry) error {
			a, _ := working.Agent(source.Metadata.UID)
			if drift == "annotations" {
				if a.Metadata.Annotations == nil {
					a.Metadata.Annotations = map[string]string{}
				}
				a.Metadata.Annotations["fixture-drift"] = "changed"
			} else {
				a.Status.SessionRef.ObservedAt = a.Status.SessionRef.ObservedAt.Add(time.Second)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out, refused := exec.CommandContext(ctx, f.binary, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput()
		if refused == nil {
			t.Fatalf("changed target %s resumed: %s", drift, out)
		}
		if _, err = os.Stat(files[0]); err != nil {
			t.Fatal("changed target evidence removed", err)
		}
		if archiveConflict {
			after, readErr := os.ReadFile(archive)
			if readErr != nil || !bytes.Equal(conflictBytes, after) {
				t.Fatal("changed target overwrote conflicting archive", readErr)
			}
		}
		_, _, err = f.store.UpdateConvergent(func(working *coremetadata.Registry) error {
			a, _ := working.Agent(source.Metadata.UID)
			*a = stable.Clone()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(provider, raw, 0600); err != nil {
		t.Fatal(err)
	}
	hostCLIOutput(t, f, "agent", "relaunch", "uid:"+source.Metadata.UID, "--host", "tmux", "--socket-path", socket, "--yes")
	final, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ := final.Agent(source.Metadata.UID)
	if recovered.Status.Phase != coremetadata.PhaseRunning || !recovered.Status.SessionRef.SameConversation(source.Status.SessionRef) {
		t.Fatal("completed process recovery did not resume same thread")
	}
}

// Cleanup observes only this fixture's authenticated PID/birth, not provider
// retirement evidence. Zombies cannot perform late state writes; they are not
// converted into a supervisor Wait or usable launch authority.
func waitCodexFixturePeerExit(t *testing.T, process coremetadata.ProcessIdentity) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, _, err := localipc.Process(process.PID)
		if err != nil || current != process {
			return
		}
		state, _ := exec.Command("ps", "-p", strconv.Itoa(process.PID), "-o", "stat=").Output()
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("owned fixture peer cleanup still running: pid=%d birth=%v", process.PID, process)
}

// stopFixtureProcesses stops every process of this uid, other than the test
// itself, that was started for one fixture: its environment carries the
// fixture root as PMX_TEST_PROCESS_ROOT, or its argv names the fixture state
// directory (a detached observer such as `internal agent-hook ingest
// codex-broker-watch` gets that directory as an argument and owns no socket the
// broker cleanup could find). Each process is logged, its birth is read again
// right before SIGTERM, and it must exit. Without /proc it does nothing.
func stopFixtureProcesses(t *testing.T, root, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		// The birth is read before the environment and argv, so a pid that a
		// new process reuses after they were read fails the check below.
		process, _, err := localipc.Process(pid)
		if err != nil || process.OwnerUID != uint32(os.Getuid()) {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		argv := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if !slices.Contains(strings.Split(string(environ), "\x00"), "PMX_TEST_PROCESS_ROOT="+root) && !slices.Contains(argv, stateDir) {
			continue
		}
		t.Logf("fixture process left after the test: pid=%d argv=%q", pid, argv)
		if current, _, err := localipc.Process(pid); err != nil || current != process {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			// ESRCH: the process left on its own after the birth check.
			if !errors.Is(err, syscall.ESRCH) {
				t.Errorf("could not stop fixture process pid=%d birth=%v: %v", pid, process, err)
			}
			continue
		}
		waitCodexFixturePeerExit(t, process)
	}
}

// TestRelaunchPromptPartsSDKCLIParity is Epic 3j Task 2 (763): after the
// agent guidance and the Project's label link rules change, the public SDK
// relaunch dry run of a tmux Agent reports what `agent relaunch --dry-run -o
// json` reports, for Codex and for Claude. For Codex the actual relaunch then
// restarts the Agent on both surfaces and sends a managed base that carries
// the new guidance and link rules.
func TestRelaunchPromptPartsSDKCLIParity(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI")
	}
	for _, provider := range []string{aiModeCodex, aiModeClaude} {
		var applied []string
		for _, sdk := range []bool{false, true} {
			name := provider + "/" + map[bool]string{false: "cli-relaunches", true: "sdk-relaunches"}[sdk]
			logProviderGuardRecord(t, name+" before")
			t.Run(name, func(t *testing.T) {
				// Deferred, this runs before the subtest's cleanups.
				defer logProviderGuardRecord(t, name+" body end")
				applied = append(applied, relaunchPromptPartsSDKCLIParity(t, provider, sdk))
			})
			logProviderGuardRecord(t, name+" after cleanup")
		}
		if len(applied) == 2 && applied[0] != applied[1] {
			t.Fatalf("%s: relaunch differs CLI %q SDK %q", provider, applied[0], applied[1])
		}
		t.Logf("%s: relaunch %q", provider, applied)
	}
	time.Sleep(2 * time.Second)
	logProviderGuardRecord(t, "2s after the last subtest")
}

// logProviderGuardRecord logs how many provider stand-in runs the liveguard has
// recorded so far, with what `codex` resolves to and the head of PATH, so a CI
// log shows when a stray `codex` run happened. The path is the liveguard's
// private root (rootEnv) joined with providerDirName and providerRecordName in
// internal/testutil/liveguard, which does not export them.
func logProviderGuardRecord(t *testing.T, when string) {
	t.Helper()
	lines := 0
	if root := os.Getenv("PMX_TEST_LIVE_MACHINE_GUARD_ROOT"); root != "" {
		raw, _ := os.ReadFile(filepath.Join(root, "provider-bin", "invocations.log"))
		lines = strings.Count(string(raw), "\n")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		codex = "error: " + err.Error()
	}
	path := filepath.SplitList(os.Getenv("PATH"))
	t.Logf("provider guard record %s: lines=%d codex=%s PATH[:3]=%q", when, lines, codex, path[:min(3, len(path))])
}

func relaunchPromptPartsSDKCLIParity(t *testing.T, provider string, sdk bool) string {
	var f processCreateCLI
	var source coremetadata.Agent
	var socket string
	if provider == aiModeCodex {
		f, source, _, socket = codexHostMoveCLIFixture(t)
	} else {
		f, source, _, socket = hostMoveCLIFixture(t)
	}
	ref := "uid:" + source.Metadata.UID
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// The fixture stops only the brokers it saw when it was created. A broker
	// or control peer a relaunch starts later keeps probing `codex` on its
	// PATH; once the fixture root is gone that lookup reaches the real codex.
	// Registered after the fixture, this cleanup runs before its root is
	// removed (t.Cleanup is LIFO) and stops only the peers this test started,
	// then every other process started for this fixture.
	peerSockets := func() []string {
		var found []string
		for _, dir := range []string{"broker", agentControlDirName} {
			matches, _ := filepath.Glob(filepath.Join(paths.StateDir, dir, "*.sock"))
			found = append(found, matches...)
		}
		return found
	}
	fixturePeers := peerSockets()
	t.Cleanup(func() {
		for _, address := range peerSockets() {
			if slices.Contains(fixturePeers, address) {
				continue
			}
			identity, err := localipc.InspectOwnedSocket(address)
			if err != nil {
				continue
			}
			peer, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
			if err != nil {
				continue
			}
			process, _, err := localipc.PeerProcess(peer)
			_ = peer.Close()
			if err != nil || process.OwnerUID != uint32(os.Getuid()) {
				continue
			}
			current, _, err := localipc.Process(process.PID)
			observed, socketErr := localipc.InspectOwnedSocket(address)
			if err != nil || current != process || socketErr != nil || observed != identity {
				continue
			}
			if err = syscall.Kill(process.PID, syscall.SIGTERM); err != nil {
				continue
			}
			waitCodexFixturePeerExit(t, process)
		}
		stopFixtureProcesses(t, f.root, paths.StateDir)
	})
	guidance := []byte("Task 2 guidance: prefer projmux agent message send.")
	writeAgentGuidance(t, paths, guidance)
	writeLinkRules(t, paths, f.project, linkRulesAlpha)
	client, err := projmux.NewClient(projmux.Options{HomeDir: os.Getenv("HOME"), ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, ProjmuxExecutable: f.binary, SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cli := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--socket-path", socket, "--dry-run", "-o", "json")
	cli.Stderr = &stderr
	out, err := cli.Output()
	if err != nil {
		t.Fatalf("CLI dry run: %v %s", err, stderr.String())
	}
	var want agentRelaunchResult
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	got, err := client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, SocketPath: socket, DryRun: true})
	if err != nil {
		t.Fatalf("SDK dry run: %v", err)
	}
	var current, next core.RelaunchAgentSettings
	raw, _ := json.Marshal(want.CurrentSettings)
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(want.NewSettings)
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != want.Outcome || got.Restart != want.Restart || got.Unchanged != want.Unchanged || got.ConfirmationRequired != want.ConfirmationRequired ||
		!reflect.DeepEqual(got.RelaunchReasons, want.RelaunchReasons) || !reflect.DeepEqual(got.CurrentSettings, current) || !reflect.DeepEqual(got.NewSettings, next) {
		t.Fatalf("dry run differs\nSDK outcome=%s restart=%t unchanged=%t confirm=%t reasons=%q current=%+v new=%+v\nCLI %s", got.Outcome, got.Restart, got.Unchanged, got.ConfirmationRequired, got.RelaunchReasons, got.CurrentSettings, got.NewSettings, out)
	}
	for _, reason := range []string{"guidance-changed", "link-rules-changed"} {
		if !slices.Contains(want.RelaunchReasons, reason) {
			t.Fatalf("CLI dry run reasons %q lack %s: %s", want.RelaunchReasons, reason, out)
		}
	}
	after, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a dry run changed the Registry")
	}
	t.Logf("%s dry run: outcome=%s reasons=%q", provider, got.Outcome, got.RelaunchReasons)
	if provider != aiModeCodex {
		return fmt.Sprintf("outcome=%s reasons=%q", got.Outcome, got.RelaunchReasons)
	}
	wirePath := filepath.Join(f.root, "shared-wire.jsonl")
	sent, err := os.ReadFile(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	var outcome string
	if sdk {
		result, err := client.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: ref, SocketPath: socket, Confirm: true})
		if err != nil {
			t.Fatalf("SDK relaunch: %v", err)
		}
		outcome = result.Outcome
	} else {
		var result agentRelaunchResult
		if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--socket-path", socket, "--yes", "-o", "json"), &result); err != nil {
			t.Fatal(err)
		}
		outcome = result.Outcome
	}
	wire, err := os.ReadFile(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	// Only frames sent by this relaunch: the managed base it sends must carry
	// the new guidance and link rules.
	var carried []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(wire[len(sent):])), "\n") {
		var frame struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &frame) == nil && strings.Contains(string(frame.Params), string(guidance)) && strings.Contains(string(frame.Params), linkRulesAlpha.Jira[0]) {
			carried = append(carried, frame.Method)
		}
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	relaunched, ok := reg.Agent(source.Metadata.UID)
	if !ok {
		t.Fatal("Agent disappeared")
	}
	return fmt.Sprintf("outcome=%s phase=%s same-thread=%t base-with-guidance-and-link-rules=%q base-status=%s", outcome, relaunched.Status.Phase,
		relaunched.Status.SessionRef.SameConversation(source.Status.SessionRef), carried, relaunched.Metadata.Annotations[mutationruntime.CodexManagedBaseStatus])
}
