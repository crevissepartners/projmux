package app

import (
	"bytes"
	"context"
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
 print('codex-cli 0.160.0');sys.exit(0)
if args==['app-server','daemon','version']:
 print(json.dumps({'status':'running','cliVersion':'0.160.0','appServerVersion':'0.160.0'}));sys.exit(0)
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
    if (method=='thread/resume' and os.path.exists(os.path.join(os.environ['HOME'],'fail-shared-init'))) or (method=='thread/read' and p.get('includeTurns') and os.path.exists(os.path.join(os.environ['HOME'],'fail-shared-observer'))):
     data=json.dumps({'id':frame['id'],'error':{'code':-32600,'message':'injected native consumer refusal'}}).encode();h=bytes([129,len(data)]) if len(data)<126 else bytes([129,126])+struct.pack('!H',len(data));c.sendall(h+data);continue
    if method=='initialize':r={'userAgent':'projmux/0.160.0'}
    elif method in ('thread/start','thread/resume'):
     id=p.get('threadId','process-thread' if started==0 else 'sibling-thread')
     if method=='thread/start':started+=1
     loaded.add(id)
     r={'thread':thread(id),'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval}
    elif method=='turn/start':r={'turn':{'id':'source-turn'}}
    elif method=='thread/read':r={'thread':thread(p['threadId'])}
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
		for _, letter := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" {
			candidate := filepath.Join(os.TempDir(), string(letter))
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
				_ = syscall.Kill(process.PID, syscall.SIGTERM)
			}
		})
	}
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
