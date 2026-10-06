package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	// Keep the native control socket within its public 100-byte bound while
	// all files remain under the caller-owned TMPDIR. No product limit changes.
	if len(filepath.Join(f.root, "state/projmux/agent-control/control-"+strings.Repeat("a", 24)+".sock")) > localipc.MaxSocketPath {
		short := ""
		letters := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
		for index := 0; index < len(letters)*len(letters); index++ {
			candidate := filepath.Join(os.TempDir(), string([]byte{letters[index/len(letters)], letters[index%len(letters)]}))
			if err := os.Mkdir(candidate, 0700); err == nil {
				short = candidate
				break
			} else if !os.IsExist(err) {
				t.Fatal(err)
			}
		}
		if short == "" {
			t.Fatal("no private short fixture state available")
		}
		if err := os.Rename(filepath.Join(f.root, "state", "projmux"), filepath.Join(short, "projmux")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		t.Setenv("XDG_STATE_HOME", short)
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		f.store = intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
	}
	codexHome, err := os.MkdirTemp(os.TempDir(), "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(codexHome) })
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.Remove(filepath.Join(f.root, "tmux")); err != nil {
		t.Fatal(err)
	}
	tmuxDir := filepath.Join(f.root, "tmux-private")
	if err := os.MkdirAll(tmuxDir, 0700); err != nil {
		t.Fatal(err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, moved := startProcessRelaunchCLI(t, ctx, f, ref, "--host", "process", "--socket-path", socket, "--yes")
	if moved.NewPaneUID == oldPane || moved.AgentUID != source.Metadata.UID {
		t.Fatal(moved)
	}
	record := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.ThreadID == "process-thread" && r.TurnID == ""
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ref := "uid:" + source.Metadata.UID
	out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--yes").CombinedOutput()
	t.Logf("injected failure=%v %s", err, out)
	if err == nil || !bytes.Contains(out, []byte("recover")) {
		t.Fatalf("failed target: %v %s", err, out)
	}
	if err = os.WriteFile(providerPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if fault != "" {
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
		if err = os.WriteFile(marker, []byte("fail"), 0600); err != nil {
			t.Fatal(err)
		}
		failed, failedErr := exec.CommandContext(ctx, f.binary, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes").CombinedOutput()
		if failedErr == nil {
			t.Fatalf("native %s failure was bypassed: %s", fault, failed)
		}
		if err = os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	}
	hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "tmux", "--socket-path", socket, "--yes")
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	current, ok := reg.Agent(source.Metadata.UID)
	if !ok || current.Status.PaneRef == oldPane || current.Status.Phase != coremetadata.PhaseRunning || !current.Status.SessionRef.SameConversation(source.Status.SessionRef) {
		t.Fatal("recovery did not restore same-thread fresh native writer")
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
	discovery, err := codexBrokerDiscoveryForEndpoint(domain, key)
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
