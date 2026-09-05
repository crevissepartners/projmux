package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The disposable synthetic provider owns its own actual socket and credential,
// then emits the public SessionStart input. A private capture socket carries
// receipts to test memory, never stdout/stderr or a test artifact. This is required
// by make test-integration; it does not require a model, account, or network.
const claudeEndpointSyntheticProvider = `
import json, os, secrets, socket, subprocess, sys
path = os.path.join(os.environ['PMX_TEST_ROOT'], 'provider-' + secrets.token_hex(8) + '.sock')
os.umask(0o077)
inbox = socket.socket(socket.AF_UNIX)
inbox.bind(path)
os.chmod(path,0o600)
inbox.listen()
inbox.settimeout(0.01)
token = secrets.token_hex(24)
os.environ['CLAUDE_CODE_MESSAGING_SOCKET'] = path
os.environ['CLAUDE_CODE_MESSAGING_TOKEN'] = token
capture = socket.socket(socket.AF_UNIX)
capture.connect(os.environ['PMX_TEST_CAPTURE'])
receipt = capture.makefile('w', buffering=1)
receipt.write(json.dumps({'socket': path, 'token': token, 'pid':os.getpid(), 'pane_env':os.environ.get('PMX_INTERNAL_ACTIVATION_PANE_UID'), 'generation_env':os.environ.get('PMX_INTERNAL_ACTIVATION_GENERATION'), 'registry_env':os.environ.get('PMX_INTERNAL_CLAUDE_REGISTRY_PATH')}) + '\n'); receipt.flush()
def hook(session):
    result = subprocess.run([os.environ['PMX_TEST_BIN'], 'internal', 'claude-endpoint-register'], input=json.dumps({'hook_event_name':'SessionStart','session_id':session}).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert result.returncode == 0 and not result.stdout and not result.stderr
hook('synthetic-session-1')
receipt.write('hook-returned\n'); receipt.flush()
for line in sys.stdin:
    if line.strip() == 'wake':
        child = subprocess.Popen([os.environ['PMX_TEST_BIN'], 'internal', 'claude-message-wait'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        receipt.write(json.dumps({'wait_started': child.pid}) + '\n'); receipt.flush()
        out, err = child.communicate(input=json.dumps({'hook_event_name':'Stop','session_id':'synthetic-session-1'}).encode(), timeout=8)
        receipt.write(json.dumps({'wait_rc': child.returncode, 'stdout_len': len(out), 'stderr': err.decode()}) + '\n'); receipt.flush()
    elif line.strip() == 'repeat':
        hook('synthetic-session-2')
        receipt.write('hook-returned\n'); receipt.flush()
    elif line.strip() == 'nested':
        child = "import json,os,subprocess; subprocess.run([os.environ['PMX_TEST_BIN'],'internal','claude-endpoint-register'],input=json.dumps({'hook_event_name':'SessionStart','session_id':'nested-foreign-session'}).encode(),check=True)"
        result = subprocess.run([sys.executable,'-c',child],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        assert result.returncode == 0 and not result.stdout and not result.stderr
        receipt.write('nested-returned\n'); receipt.flush()
    elif line.strip() == 'exit':
        break
try:
    connection, _ = inbox.accept()
    connection.close()
    raise AssertionError('provider transport was opened')
except socket.timeout:
    pass
inbox.close()
os.unlink(path)
receipt.write('provider-connections=0\n'); receipt.flush()
`

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
	h := newSessionRefHarness(t, "claude")
	registryPath := intmetadata.PathFor(filepath.Join(root, "state", "projmux"))
	store := intmetadata.NewStore(registryPath)
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
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
	cmd.Env = claudeEndpointProcessEnv(root, binary)
	cmd.Env = append(cmd.Env, "PMX_TEST_CAPTURE="+capturePath)
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
		cleanupClaudeEndpointTestActivation(store, superviseSpec{RegistryPath: registryPath, PaneUID: h.paneUID, AgentUID: h.agentUID, Generation: h.envGeneration})
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
		Socket        string
		Token         string
		PID           int
		PaneEnv       string `json:"pane_env"`
		GenerationEnv string `json:"generation_env"`
		RegistryEnv   string `json:"registry_env"`
	}
	if json.Unmarshal(line, &private) != nil || private.Token == "" || private.Socket == "" {
		t.Fatal("private registration receipt invalid")
	}
	if private.PaneEnv != h.paneUID || private.GenerationEnv != h.envGeneration || private.RegistryEnv != registryPath {
		t.Fatalf("private activation context mismatch: pane=%t generation=%t registry=%t", private.PaneEnv == h.paneUID, private.GenerationEnv == h.envGeneration, private.RegistryEnv == registryPath)
	}
	waitHook := func() {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil || line != "hook-returned\n" {
			t.Fatal("SessionStart helper bootstrap failed")
		}
	}
	waitHook()
	getRoute := func() coremetadata.AgentRouteRef {
		t.Helper()
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		route, reason := coremetadata.ResolveAgentRoute(reg, h.agentUID)
		if reason != "" || !probeClaudeRegistrationLease(registryPath, route) {
			pane, _ := reg.Pane(h.paneUID)
			if pane != nil && pane.Status.Activation.Claude != nil {
				binding := pane.Status.Activation.Claude
				t.Logf("process_bound=%t claim_present=%t registration_present=%t registry_reason=%s", binding.Process.Valid(), binding.RegistrationGeneration != "", binding.Registration != nil, reason)
				actual, _, processErr := claudeadapter.Process(private.PID)
				_, socketErr := inspectClaudeSocket(private.Socket)
				t.Logf("exact_provider_birth=%t socket_admitted=%t", processErr == nil && actual == binding.Process, socketErr == nil)
			}
			t.Fatal("exact process registration was not ready")
		}
		return route
	}
	first := getRoute()
	assertNoClaudeSecretResidue(t, claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration), []string{private.Token, private.Socket})
	// The disposable provider starts its direct Stop hook child, reports a
	// barrier without sleeping, and then blocks on the provider stderr pipe.
	// The test submits only a Projmux-owned coordination frame and waits for the
	// child's exact helper receipt; no provider/model/vendor transport is used.
	_, _ = io.WriteString(writeControl, "wake\n")
	line, err = reader.ReadBytes('\n')
	var waitStarted struct {
		PID int `json:"wait_started"`
	}
	if err != nil || json.Unmarshal(line, &waitStarted) != nil || waitStarted.PID <= 0 {
		t.Fatal("coordination waiter start barrier failed")
	}
	target, ok := claudeTargetForRoute(first)
	if !ok {
		t.Fatal("exact coordination target unavailable")
	}
	waiterDeadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		barrier, barrierErr := callClaudeCoordination(ctx, registryPath, first, claudeCoordinationRequest{
			Version: claudeCoordinationVersion, Operation: "waiter-ready", Target: target,
		})
		cancel()
		if barrierErr == nil && barrier.Kind == "waiter-ready" {
			break
		}
		if time.Now().After(waiterDeadline) {
			t.Fatalf("coordination waiter-ready barrier = %+v err=%v", barrier, barrierErr)
		}
	}
	envelope := coordinationEnvelope("process-message-1", time.Now().Add(time.Minute))
	envelope.Target = target
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	response, callErr := callClaudeCoordination(ctx, registryPath, first, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "submit", Target: target, Envelope: &envelope,
	})
	cancel()
	if callErr != nil || response.Delivery.State != agentdelivery.StateHandoff {
		t.Fatalf("coordination submit = %+v err=%v", response, callErr)
	}
	line, err = reader.ReadBytes('\n')
	var wake struct {
		RC        int    `json:"wait_rc"`
		StdoutLen int    `json:"stdout_len"`
		Stderr    string `json:"stderr"`
	}
	if err != nil || json.Unmarshal(line, &wake) != nil || wake.RC != 2 || wake.StdoutLen != 0 {
		t.Fatalf("coordination hook result rc=%d stdout=%d err=%v", wake.RC, wake.StdoutLen, err)
	}
	var providerFrame claudeCoordinationEnvelope
	if json.Unmarshal([]byte(wake.Stderr), &providerFrame) != nil || providerFrame.MessageRef != envelope.MessageRef || providerFrame.Payload != envelope.Payload {
		t.Fatal("provider stderr did not receive the exact bounded coordination frame")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	status, callErr := callClaudeCoordination(ctx, registryPath, first, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "status", Target: target, MessageRef: envelope.MessageRef,
	})
	cancel()
	if callErr != nil || status.Delivery.State != agentdelivery.StateDelivered || status.Delivery.WaiterRef == "" {
		t.Fatalf("coordination delivery receipt = %+v err=%v", status.Delivery, callErr)
	}
	foreignHook := exec.Command(binary, "internal", "claude-message-wait")
	foreignHook.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"synthetic-session-1"}`)
	foreignHook.Env = append(claudeEndpointProcessEnv(root, binary), internalClaudeRegistryPathEnv+"="+registryPath,
		internalActivationPaneUIDEnv+"="+h.paneUID, internalActivationGenerationEnv+"="+h.envGeneration)
	if output, runErr := foreignHook.CombinedOutput(); runErr != nil || len(output) != 0 {
		t.Fatalf("foreign hook process was not quietly refused: err=%v output=%q", runErr, output)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	waiterStatus, callErr := callClaudeCoordination(ctx, registryPath, first, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "waiter-ready", Target: target,
	})
	cancel()
	if callErr != nil || waiterStatus.Kind != "no-waiter" {
		t.Fatalf("foreign hook armed a waiter: %+v err=%v", waiterStatus, callErr)
	}
	// Exercise actual entrypoints with malformed, oversized, and foreign
	// bootstrap input. The output/diagnostic scanner must never echo stdin.
	beforeNegative, _ := os.ReadFile(registryPath)
	hookIdentity, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := json.Marshal(claudeEndpointBootstrap{RegistryPath: registryPath, PaneUID: h.paneUID, AgentUID: h.agentUID, Generation: h.envGeneration,
		Registration: coremetadata.ClaudeRegistration{Authority: first.Authority().(coremetadata.ClaudeAuthorityRef)}, HookProcess: hookIdentity, Socket: private.Socket, Token: private.Token})
	for _, input := range [][]byte{[]byte("{" + private.Token), []byte(strings.Repeat(private.Token, 2000)), forged} {
		for _, route := range []string{"claude-endpoint-register", "claude-endpoint-helper"} {
			readAck, writeAck, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			negative := exec.Command(binary, "internal", route)
			negative.Stdin = bytes.NewReader(input)
			negative.ExtraFiles = []*os.File{writeAck}
			negative.Env = append(claudeEndpointProcessEnv(root, binary), internalClaudeRegistryPathEnv+"="+registryPath, internalActivationPaneUIDEnv+"="+h.paneUID, internalActivationGenerationEnv+"="+h.envGeneration)
			output, runErr := negative.CombinedOutput()
			_ = readAck.Close()
			_ = writeAck.Close()
			if runErr != nil || len(output) != 0 {
				t.Fatal("private invalid entrypoint emitted output or failed noisily")
			}
			assertNoClaudeSecretResidue(t, root, []string{private.Token, private.Socket}, output)
		}
	}
	afterNegative, _ := os.ReadFile(registryPath)
	if !bytes.Equal(beforeNegative, afterNegative) || !first.Same(getRoute()) {
		t.Fatal("invalid private entrypoint changed current registration")
	}
	providerIdentity := first.Authority().(coremetadata.ClaudeAuthorityRef).Process
	if _, parent, err := claudeadapter.Process(providerIdentity.PID); err != nil || parent != cmd.Process.Pid {
		t.Fatal("provider did not match supervisor's exact child")
	}
	_, _ = io.WriteString(writeControl, "nested\n")
	line, err = reader.ReadBytes('\n')
	if err != nil || string(line) != "nested-returned\n" {
		t.Fatal("nested isolation fixture failed")
	}
	if !first.Same(getRoute()) {
		t.Fatal("nested unmanaged process replaced managed activation")
	}
	_, _ = io.WriteString(writeControl, "repeat\n")
	waitHook()
	second := getRoute()
	if first.Same(second) || probeClaudeRegistrationLease(registryPath, first) {
		t.Fatal("repeated SessionStart reused old lease")
	}
	// Kill the actual helper while its provider remains alive. The production
	// supervisor watcher must revoke and clean it without a capability write.
	helperPID := second.Authority().(coremetadata.ClaudeAuthorityRef).LeaseProcess.PID
	if current, _, err := claudeadapter.Process(helperPID); err != nil || current != second.Authority().(coremetadata.ClaudeAuthorityRef).LeaseProcess {
		t.Fatal("helper birth changed before controlled kill")
	}
	helperProcess, err := os.FindProcess(helperPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := helperProcess.Kill(); err != nil {
		t.Fatal(err)
	}
	crashDeadline := time.Now().Add(5 * time.Second)
	for {
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		_, reason := coremetadata.ResolveAgentRoute(reg, h.agentUID)
		_, statErr := os.Lstat(claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration))
		if reason != "" && os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(crashDeadline) {
			t.Fatal("supervisor did not invalidate and clean killed helper")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if actual, _, err := claudeadapter.Process(providerIdentity.PID); err != nil || actual != providerIdentity {
		t.Fatal("provider stopped when helper was killed")
	}
	_, _ = io.WriteString(writeControl, "repeat\n")
	waitHook()
	third := getRoute()
	_, _ = io.WriteString(writeControl, "exit\n")
	line, err = reader.ReadBytes('\n')
	if err != nil || string(line) != "provider-connections=0\n" {
		t.Fatal("provider transport isolation failed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("provider process failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("provider exit deadline")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		_, reason := coremetadata.ResolveAgentRoute(reg, h.agentUID)
		_, statErr := os.Lstat(claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration))
		if reason != "" && os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("registration or helper files survived provider exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, route := range []coremetadata.AgentRouteRef{first, second, third} {
		helper := route.Authority().(coremetadata.ClaudeAuthorityRef).LeaseProcess
		if observed, _, err := claudeadapter.Process(helper.PID); err == nil && observed == helper {
			t.Fatal("helper process survived activation")
		}
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("registration process wrote stdout or stderr")
	}
	assertNoClaudeSecretResidue(t, root, []string{private.Token, private.Socket}, stdout.Bytes(), stderr.Bytes())
	t.Log("SessionStart→ready→replacement→exit→invalidated; nested registration refused; provider connections=0; secret residue=0; helper/process/socket cleanup=0")
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
