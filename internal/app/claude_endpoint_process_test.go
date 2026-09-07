package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The synthetic provider owns a disposable socket and token, emits only the
// public SessionStart input, and accepts exactly the documented auth line plus
// the one owner-frozen user frame. It never runs a model or uses a vendor
// reply/control frame. Its private capture socket is test memory, not a product
// receipt, log, or artifact.
const claudeEndpointSyntheticProvider = `
import glob, hashlib, json, os, secrets, socket, subprocess, sys
path = os.path.join(os.environ['PMX_TEST_ROOT'], 'provider-' + secrets.token_hex(8) + '.sock')
os.umask(0o077)
inbox = socket.socket(socket.AF_UNIX)
inbox.bind(path)
os.chmod(path, 0o600)
inbox.listen()
token = secrets.token_hex(24)
os.environ['CLAUDE_CODE_MESSAGING_SOCKET'] = path
os.environ['CLAUDE_CODE_MESSAGING_TOKEN'] = token
capture = socket.socket(socket.AF_UNIX)
capture.connect(os.environ['PMX_TEST_CAPTURE'])
receipt = capture.makefile('w', buffering=1)
receipt.write(json.dumps({'socket':path,'token':token,'pid':os.getpid(),'pane_env':os.environ.get('PMX_INTERNAL_ACTIVATION_PANE_UID'),'generation_env':os.environ.get('PMX_INTERNAL_ACTIVATION_GENERATION'),'registry_env':os.environ.get('PMX_INTERNAL_CLAUDE_REGISTRY_PATH')}) + '\n'); receipt.flush()
def hook(session):
    result = subprocess.run([os.environ['PMX_TEST_BIN'],'internal','claude-endpoint-register'], input=json.dumps({'hook_event_name':'SessionStart','session_id':session}).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert result.returncode == 0 and not result.stdout and not result.stderr
    receipt.write('hook-returned\n'); receipt.flush()
def receive(kind, session):
    connection, _ = inbox.accept()
    stream = connection.makefile('rb')
    auth = json.loads(stream.readline())
    frame = json.loads(stream.readline())
    assert auth == {'type':'auth','token':token}
    assert frame.get('type') == 'user'
    assert frame.get('message',{}).get('role') == 'user'
    assert stream.readline() == b''
    content = frame['message']['content']
    connection.close()
    receipt.write(json.dumps({'received':kind,'content':content}) + '\n'); receipt.flush()
    if kind == 'qualification':
        envelope = json.loads(content)
        registry_path = os.environ['PMX_INTERNAL_CLAUDE_REGISTRY_PATH']
        pane_uid = os.environ['PMX_INTERNAL_ACTIVATION_PANE_UID']
        generation = os.environ['PMX_INTERNAL_ACTIVATION_GENERATION']
        registry = json.load(open(registry_path))
        pane = next(x for x in registry['panes'] if x['metadata']['uid'] == pane_uid)
        authority = pane['status']['activation']['claude']['registration']['authority']
        target = {'agentUID': envelope['target']['agentUID'], 'paneUID': pane_uid, 'generation': generation, 'provider': 'claude', 'authority': authority}
        lease_dir = '/tmp/pmx-ce-' + hashlib.sha256((registry_path+'\x00'+pane_uid+'\x00'+generation).encode()).hexdigest()[:32]
        paths = glob.glob(lease_dir+'/coord-*.sock')
        assert len(paths) == 1
        original = json.load(open(os.path.join(os.environ['PMX_TEST_ROOT'], 'qualification-original.json')))
        reply = dict(original, messageRef='reply-'+original['messageRef'], replyTo=original['messageRef'], source=original['target'], target=original['source'], payload='HETEROGENEOUS_QUALIFIED:'+original['messageRef'])
        request = {'version':4, 'operation':'explicit-reply', 'target':target, 'sessionId':session, 'replyEnvelope':reply}
        child = "import json,socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.sendall(sys.stdin.buffer.read()+b'\\n'); s.shutdown(socket.SHUT_WR); r=json.loads(s.makefile('rb').readline()); assert r['kind']=='reply-accepted'; s.close()"
        child_env={k:v for k,v in os.environ.items() if k not in {'CLAUDE_CODE_MESSAGING_TOKEN','CLAUDE_CODE_MESSAGING_SOCKET'}}
        result = subprocess.run([sys.executable, '-c', child, paths[0]], input=json.dumps(request).encode(), env=child_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert result.returncode == 0 and not result.stdout and not result.stderr
        receipt.write('qualification-explicit-returned\n'); receipt.flush()
hook('synthetic-session-1')
for line in sys.stdin:
    command = line.strip()
    if command == 'qualify-1': receive('qualification', 'synthetic-session-1')
    elif command == 'message-1': receive('message', 'synthetic-session-1')
    elif command == 'repeat': hook('synthetic-session-2')
    elif command == 'qualify-2': receive('qualification', 'synthetic-session-2')
    elif command == 'message-2': receive('message', 'synthetic-session-2')
    elif command == 'exit': break
inbox.close()
os.unlink(path)
receipt.write('provider-connections=4\n'); receipt.flush()
`

// This fixture uses the same real broker authority surface as L20. Registry
// provider labels alone are never enough to pass the final source fence.
func processFixtureCodexSource(t *testing.T, registry *coremetadata.Registry, claudeUID, stateDir string) (coremetadata.AgentRouteRef, func()) {
	t.Helper()
	claude, _ := registry.Agent(claudeUID)
	mutator := intmetadata.DefaultMutator()
	agent, err := mutator.CreateAgent(registry, claude.Metadata.OwnerUID(), coremetadata.CreateAgentOptions{Provider: "codex", OperationID: "op-source-create"})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{Command: "codex", CWD: stateDir}, "op-source-attach")
	if err != nil {
		t.Fatal(err)
	}
	generation := "gen-process-codex"
	if _, err := mutator.RecordPaneActivation(registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{Generation: generation, AgentUID: agent.Metadata.UID, OperationID: "op-source-attach"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ObservePaneActivationRuntime(registry, pane.Metadata.UID, generation, "%8"); err != nil {
		t.Fatal(err)
	}
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "process-dialogue-domain", EndpointGenerationID: "process-dialogue-endpoint"}
	key, err := codexbroker.NewEndpointKey(endpoint.StateDomainID, endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(stateDir, key)
	if err != nil {
		t.Fatal(err)
	}
	upstream := newBrokerTestEndpoint()
	broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) { return upstream, nil }})
	if err != nil {
		t.Fatal(err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
	if err != nil {
		_ = broker.Close()
		t.Fatal(err)
	}
	binding, err := broker.Bind("process-source-thread", "", nil)
	if err != nil {
		_ = host.Close()
		_ = broker.Close()
		t.Fatal(err)
	}
	cleanup := func() { _ = binding.Close(); _ = host.Close(); _ = broker.Close() }
	var observed codexbroker.Event
	select {
	case observed = <-binding.Events():
	case <-time.After(5 * time.Second):
		cleanup()
		t.Fatal("source broker snapshot timed out")
	}
	if observed.Origin != codexbroker.EventOriginSnapshot {
		cleanup()
		t.Fatal("source broker snapshot missing")
	}
	if err := mutator.StageCodexEndpoint(registry, agent.Metadata.UID, endpoint); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if _, err := mutator.BindCodexActivation(registry, coremetadata.CodexActivationObservation{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: generation, ThreadID: "process-source-thread", Endpoint: endpoint}); err != nil {
		cleanup()
		t.Fatal(err)
	}
	currentPane, _ := registry.Pane(pane.Metadata.UID)
	currentPane.Status.Activation.Codex.Authority = &coremetadata.CodexAuthorityRef{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID, BrokerRuntimeID: host.RuntimeID(), ConnectionEpoch: uint64(observed.Fence.Connection), BindingEpoch: uint64(observed.Fence.Binding)}
	route, reason := coremetadata.ResolveAgentRoute(*registry, agent.Metadata.UID)
	if reason != "" {
		cleanup()
		t.Fatal(reason)
	}
	return route, cleanup
}

func TestClaudeEndpointProcessIntegration(t *testing.T) {
	binary := os.Getenv("PMX_TEST_CLAUDE_ENDPOINT_BIN")
	if binary == "" {
		t.Skip("run test/integration/claude-endpoint-binding.sh for built-binary process integration")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("process test requires an absolute built binary")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "pce-process-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	h := newSessionRefHarness(t, aiModeClaude)
	registryPath := intmetadata.PathFor(filepath.Join(root, "state", "projmux"))
	sourceRoute, closeSource := processFixtureCodexSource(t, h.registry, h.agentUID, filepath.Dir(filepath.Dir(registryPath)))
	defer closeSource()
	metadataStore := intmetadata.NewStore(registryPath)
	if _, err := metadataStore.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "capture.sock")
	capture, err := net.ListenUnix("unix", &net.UnixAddr{Name: capturePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	readControl, writeControl, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writeControl.Close()
	args := []string{"internal", "supervise", "--pane-uid", h.paneUID, "--agent-uid", h.agentUID, "--generation", h.envGeneration, "--operation-id", "op-3", "--registry-path", registryPath, "--", python, "-c", claudeEndpointSyntheticProvider}
	cmd := exec.Command(binary, args...)
	cmd.Stdin = readControl
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(claudeEndpointProcessEnv(root, binary), "PMX_TEST_CAPTURE="+capturePath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readControl.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	defer func() {
		_ = writeControl.Close()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			t.Error("provider process did not stop")
		}
		cleanupClaudeEndpointTestActivation(metadataStore, superviseSpec{RegistryPath: registryPath, PaneUID: h.paneUID, AgentUID: h.agentUID, Generation: h.envGeneration})
	}()
	_ = capture.SetDeadline(time.Now().Add(8 * time.Second))
	readReceipt, err := capture.AcceptUnix()
	if err != nil {
		t.Fatal("provider capture startup failed")
	}
	defer readReceipt.Close()
	_ = readReceipt.SetReadDeadline(time.Now().Add(8 * time.Second))
	reader := bufio.NewReader(readReceipt)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal("provider did not produce private registration receipt")
	}
	var private struct {
		Socket, Token, PaneEnv, GenerationEnv, RegistryEnv string
		PID                                                int
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(line, &raw) != nil || json.Unmarshal(raw["socket"], &private.Socket) != nil ||
		json.Unmarshal(raw["token"], &private.Token) != nil || json.Unmarshal(raw["pid"], &private.PID) != nil ||
		json.Unmarshal(raw["pane_env"], &private.PaneEnv) != nil || json.Unmarshal(raw["generation_env"], &private.GenerationEnv) != nil ||
		json.Unmarshal(raw["registry_env"], &private.RegistryEnv) != nil || private.Socket == "" || private.Token == "" {
		t.Fatal("private registration receipt invalid")
	}
	if private.PaneEnv != h.paneUID || private.GenerationEnv != h.envGeneration || private.RegistryEnv != registryPath {
		t.Fatal("private activation context mismatch")
	}
	waitLine(t, reader, "hook-returned\n")

	getRoute := func() coremetadata.AgentRouteRef {
		t.Helper()
		reg, err := metadataStore.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		route, reason := coremetadata.ResolveAgentRoute(reg, h.agentUID)
		if reason != "" || !probeClaudeRegistrationLease(registryPath, route) {
			t.Fatalf("exact process registration unavailable: %s", reason)
		}
		return route
	}
	first := getRoute()
	assertNoClaudeSecretResidue(t, claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration), []string{private.Token, private.Socket})
	providerIdentity := first.Authority().(coremetadata.ClaudeAuthorityRef).Process
	if actual, parent, err := claudeadapter.Process(private.PID); err != nil || actual != providerIdentity || parent != cmd.Process.Pid {
		t.Fatal("provider did not match supervisor's exact child")
	}

	messageStore := messagestore.NewStore(filepath.Dir(filepath.Dir(registryPath)))
	qualify := func(route coremetadata.AgentRouteRef, command string) {
		t.Helper()
		target, ok := claudeTargetForRoute(route)
		if !ok {
			t.Fatal("exact target unavailable")
		}
		now := time.Now().UTC()
		ref := "qualification-" + command
		original := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: conversationRefFor(ref),
			Source: publicMessageRoute(sourceRoute), Target: publicMessageRoute(route), Authority: coremessage.PeerAuthority(),
			Payload: claudeQualificationMarkerPrefix + ref, AcceptedAt: now, Deadline: now.Add(time.Minute)}
		if _, _, err := messageStore.PutAccepted(original, "claude-coordination"); err != nil {
			t.Fatal(err)
		}
		originalBytes, _ := json.Marshal(original)
		if err := os.WriteFile(filepath.Join(root, "qualification-original.json"), originalBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		challenge := claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: ref, Target: target,
			Source: claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"}, Deadline: original.Deadline, BrokerEnvelope: &original}
		_, _ = writeControl.WriteString(command + "\n")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		response, callErr := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "qualify", Target: target, Envelope: &challenge, Qualification: ptrQualification(exactQualificationEvidence(route, time.Now().UTC())), ExplicitOptIn: true})
		cancel()
		if callErr != nil || response.Kind != "qualification-pending" {
			t.Fatalf("qualification start=%+v err=%v", response, callErr)
		}
		assertProviderReceipt(t, reader, "qualification", claudeQualificationMarkerPrefix)
		waitLine(t, reader, "qualification-explicit-returned\n")
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		status, callErr := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "qualification-status", Target: target, QualificationRef: response.QualificationRef})
		cancel()
		if callErr != nil || status.Kind != "qualification-qualified" {
			t.Fatalf("qualification status=%+v err=%v", status, callErr)
		}
	}
	qualify(first, "qualify-1")

	send := func(route coremetadata.AgentRouteRef, ref, command string) {
		t.Helper()
		target, ok := claudeTargetForRoute(route)
		if !ok {
			t.Fatal("exact target unavailable")
		}
		now := time.Now().UTC()
		public := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: "conversation-" + ref,
			// The source owns a real isolated broker binding for this entire test.
			Source: publicMessageRoute(sourceRoute),
			Target: publicMessageRoute(route), Authority: coremessage.PeerAuthority(), Payload: "HETEROGENEOUS_MARKER:" + ref,
			AcceptedAt: now, Deadline: now.Add(time.Minute)}
		if _, _, err := messageStore.PutAccepted(public, "claude-coordination"); err != nil {
			t.Fatal(err)
		}
		privateEnvelope := claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: ref, Target: target,
			Source: claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"}, Deadline: public.Deadline, BrokerEnvelope: &public}
		_, _ = writeControl.WriteString(command + "\n")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		response, callErr := callClaudeCoordination(ctx, registryPath, route, claudeCoordinationRequest{Version: claudeCoordinationVersion,
			Operation: "submit", Target: target, Envelope: &privateEnvelope})
		cancel()
		if callErr != nil || response.Delivery.State != agentdelivery.StateDelivered {
			t.Fatalf("push response=%+v err=%v", response, callErr)
		}
		assertProviderReceipt(t, reader, "message", `"messageRef":"`+ref+`"`)
		record, found, err := messageStore.Get(ref)
		if err != nil || !found || !record.HandoffObserved || record.Delivery.State != coremessage.StateDelivered {
			t.Fatalf("durable record=%+v found=%t err=%v", record, found, err)
		}
	}
	send(first, "process-message-1", "message-1")

	_, _ = writeControl.WriteString("repeat\n")
	waitLine(t, reader, "hook-returned\n")
	second := getRoute()
	if first.Same(second) || probeClaudeCoordinationEligibility(registryPath, second) || probeClaudeRegistrationLease(registryPath, first) {
		t.Fatal("replacement inherited old registration or qualification")
	}
	target, _ := claudeTargetForRoute(second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	unqualified, callErr := callClaudeCoordination(ctx, registryPath, second, claudeCoordinationRequest{Version: claudeCoordinationVersion,
		Operation: "submit", Target: target, Envelope: ptrCoordination(dialogueForRoute("pre-ready", second, time.Now().UTC()))})
	cancel()
	if callErr != nil || unqualified.Delivery.State != agentdelivery.StateRefused {
		t.Fatalf("replacement pre-qualification delivery=%+v err=%v", unqualified, callErr)
	}
	qualify(second, "qualify-2")
	send(second, "process-message-2", "message-2")

	_, _ = writeControl.WriteString("exit\n")
	waitLine(t, reader, "provider-connections=4\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("provider process failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("provider exit deadline")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("registration process wrote stdout or stderr")
	}
	assertNoClaudeSecretResidue(t, root, []string{private.Token, private.Socket}, stdout.Bytes(), stderr.Bytes())
}

func ptrQualification(value claudeQualificationEvidence) *claudeQualificationEvidence { return &value }
func ptrCoordination(value claudeCoordinationEnvelope) *claudeCoordinationEnvelope    { return &value }

func dialogueForRoute(ref string, route coremetadata.AgentRouteRef, now time.Time) claudeCoordinationEnvelope {
	envelope := dialogueEnvelope(ref, now.Add(time.Minute))
	envelope.Target, _ = claudeTargetForRoute(route)
	envelope.BrokerEnvelope.Target = publicMessageRoute(route)
	return envelope
}

func waitLine(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil || line != want {
		t.Fatalf("provider barrier=%q err=%v, want %q", line, err, want)
	}
}

func assertProviderReceipt(t *testing.T, reader *bufio.Reader, kind, contains string) {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	var receipt struct {
		Received string `json:"received"`
		Content  string `json:"content"`
	}
	if err != nil || json.Unmarshal(line, &receipt) != nil || receipt.Received != kind || !strings.Contains(receipt.Content, contains) {
		t.Fatalf("provider receipt kind=%q content=%q err=%v", receipt.Received, receipt.Content, err)
	}
}

func cleanupClaudeEndpointTestActivation(store *intmetadata.Store, spec superviseSpec) {
	if reg, err := store.LoadDegradedReadOnly(); err == nil {
		if pane, ok := reg.Pane(spec.PaneUID); ok && pane.Status.Activation.Generation == spec.Generation && pane.Status.Activation.Claude != nil {
			binding := pane.Status.Activation.Claude
			processes := []coremetadata.ProcessIdentity{binding.Process}
			if binding.Registration != nil {
				processes = append(processes, binding.Registration.Authority.LeaseProcess)
			}
			for _, expected := range processes {
				if actual, _, err := claudeadapter.Process(expected.PID); err == nil && actual == expected {
					if process, err := os.FindProcess(expected.PID); err == nil {
						_ = process.Kill()
					}
				}
			}
		}
	}
	cleanupClaudeActivationLeases(spec)
}

func claudeEndpointProcessEnv(root, binary string) []string {
	drop := map[string]bool{"TMUX": true, "TMUX_PANE": true, "XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true, "TMPDIR": true, "PMX_TEST_ROOT": true, "PMX_TEST_BIN": true}
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !drop[key] {
			env = append(env, value)
		}
	}
	return append(env, "TMUX_PANE=%7", "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"), "TMPDIR="+root, "PMX_TEST_ROOT="+root, "PMX_TEST_BIN="+binary)
}
