package app

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// TestClaudeEndpointInstalledSourceGate is opt-in account-backed evidence for
// Phase 1 registration only. The caller supplies an already prepared disposable
// Claude auth directory; the harness never reads or copies credentials. Raw
// provider stream and exported messaging credentials remain only in memory.
// This deliberately does NOT establish the later mandatory safe-mode delivery
// gate: no message is sent and no provider transport is opened by Projmux.
func TestClaudeEndpointInstalledSourceGate(t *testing.T) {
	binary, claude, claudeConfig := os.Getenv("PMX_TEST_CLAUDE_ENDPOINT_BIN"), os.Getenv("PMX_TEST_REAL_CLAUDE_BIN"), os.Getenv("PMX_TEST_REAL_CLAUDE_CONFIG_DIR")
	if binary == "" || claude == "" || claudeConfig == "" {
		t.Skip("requires built binary, installed Claude, and disposable authenticated Claude config directory")
	}
	if !filepath.IsAbs(binary) || !filepath.IsAbs(claude) || !filepath.IsAbs(claudeConfig) {
		t.Fatal("installed source gate paths must be absolute")
	}
	root, err := os.MkdirTemp("", "pce-real-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	// Provider source state belongs to a separate disposable directory. The
	// caller's authentication path is linked, never read, copied, or removed.
	providerConfig, err := os.MkdirTemp("", "pce-provider-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(providerConfig)
	if err := os.Symlink(filepath.Join(claudeConfig, ".credentials.json"), filepath.Join(providerConfig, ".credentials.json")); err != nil {
		t.Fatal("disposable provider authentication link unavailable")
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
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
	if err := os.Chmod(capturePath, 0o600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "session-start.py")
	// Capture the documented hook environment through an owned in-memory
	// channel, restore the public stdin payload, then exec the actual product
	// registration hook without adding an ancestor between it and Claude.
	script := `#!` + python + `
import json, os, socket, sys
payload = sys.stdin.buffer.read()
data = json.loads(payload)
capture = socket.socket(socket.AF_UNIX)
capture.connect(os.environ['PMX_TEST_CAPTURE'])
capture.sendall(json.dumps({'session_id':data.get('session_id'),'socket':os.environ.get('CLAUDE_CODE_MESSAGING_SOCKET'),'token':os.environ.get('CLAUDE_CODE_MESSAGING_TOKEN')}).encode() + b'\n')
capture.close()
read_fd, write_fd = os.pipe()
os.write(write_fd,payload)
os.close(write_fd)
os.dup2(read_fd,0)
os.close(read_fd)
binary = os.environ['PMX_TEST_BIN']
os.execv(binary,[binary,'internal','claude-endpoint-register'])
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"crossSessionInbound": "refuse", "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "exec " + shellQuote(wrapper), "timeout": 10}}}}}}
	settingsData, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settingsPath, settingsData, 0o600); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(root, "empty-mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"internal", "supervise", "--pane-uid", h.paneUID, "--agent-uid", h.agentUID, "--generation", h.envGeneration, "--operation-id", "op-3", "--registry-path", registryPath, "--", claude,
		"--print", "--verbose", "--output-format", "stream-json", "--strict-mcp-config", "--mcp-config", mcpPath, "--setting-sources", "", "--tools", "", "--no-session-persistence", "--settings", settingsPath, "--", "Reply exactly PHASE1_OK. Do not use tools or other agents."}
	cmd := exec.Command(binary, args...)
	cmd.Dir = work
	cmd.Env = append(claudeEndpointProcessEnv(root, binary), "CLAUDE_CONFIG_DIR="+providerConfig, "PMX_TEST_CAPTURE="+capturePath, "CLAUDE_CODE_DEBUG_LOGS_DIR="+filepath.Join(root, "diagnostics"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	spec := superviseSpec{RegistryPath: registryPath, PaneUID: h.paneUID, AgentUID: h.agentUID, Generation: h.envGeneration}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
		cleanupClaudeEndpointTestActivation(store, spec)
	}()
	_ = capture.SetDeadline(time.Now().Add(30 * time.Second))
	connection, err := capture.AcceptUnix()
	if err != nil {
		t.Fatal("installed Claude did not emit public SessionStart registration")
	}
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	var private struct {
		SessionID string `json:"session_id"`
		Socket    string `json:"socket"`
		Token     string `json:"token"`
	}
	err = json.NewDecoder(connection).Decode(&private)
	_ = connection.Close()
	if err != nil || private.SessionID == "" || private.Socket == "" || private.Token == "" {
		t.Fatal("installed SessionStart metadata incomplete")
	}
	deadline := time.Now().Add(15 * time.Second)
	var ready coremetadata.AgentRouteRef
	for {
		reg, err := store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		route, reason := coremetadata.ResolveAgentRoute(reg, h.agentUID)
		if reason == "" && probeClaudeRegistrationLease(registryPath, route) {
			ready = route
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("installed registration never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	authority := ready.Authority().(coremetadata.ClaudeAuthorityRef)
	if authority.SessionID != private.SessionID {
		t.Fatal("public SessionStart identity did not match exact activation registration")
	}
	assertNoClaudeSecretResidue(t, root, []string{private.Token, private.Socket})
	assertNoClaudeSecretResidue(t, claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration), []string{private.Token, private.Socket})
	upstreamLocatorObserved := assertClaudeConfigMessagingResidue(t, providerConfig, []string{private.Token, private.Socket})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("installed one-shot failed")
		}
	case <-time.After(90 * time.Second):
		t.Fatal("installed one-shot deadline")
	}
	initSeen, resultSeen, toolUses := false, false, 0
	for line := range bytes.SplitSeq(stdout.Bytes(), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("installed public stream was not JSON")
		}
		if event["type"] == "system" && event["subtype"] == "init" {
			initSeen = true
			for _, field := range []string{"tools", "mcp_servers", "plugins"} {
				items, ok := event[field].([]any)
				if !ok || len(items) != 0 {
					t.Fatal("installed init isolation gate failed")
				}
			}
		}
		if event["type"] == "result" {
			resultSeen = true
			if failed, _ := event["is_error"].(bool); failed {
				t.Fatal("installed result reported failure")
			}
			if result, _ := event["result"].(string); strings.TrimSpace(result) != "PHASE1_OK" {
				t.Fatal("installed one-shot marker mismatch")
			}
		}
		toolUses += countClaudeToolUse(event)
	}
	if !initSeen || !resultSeen || toolUses != 0 {
		t.Fatal("installed tools/MCP/plugins/tool-use gate incomplete")
	}
	// The upstream public init may contain its nonsecret socket locator. It is
	// inspected only in this in-memory buffer and never emitted as evidence.
	if bytes.Contains(stdout.Bytes(), []byte(private.Token)) || bytes.Contains(stderr.Bytes(), []byte(private.Token)) {
		t.Fatal("installed stream contained messaging credential")
	}
	deadline = time.Now().Add(5 * time.Second)
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
			t.Fatal("installed activation cleanup incomplete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if probeClaudeRegistrationLease(registryPath, ready) {
		t.Fatal("exited installed registration remained eligible")
	}
	if actual, _, err := claudeadapter.Process(authority.LeaseProcess.PID); err == nil && actual == authority.LeaseProcess {
		t.Fatal("installed helper survived provider")
	}
	if _, err := os.Lstat(private.Socket); !os.IsNotExist(err) {
		t.Fatal("installed provider inbox survived exit")
	}
	assertNoClaudeSecretResidue(t, root, []string{private.Token, private.Socket})
	upstreamLocatorObserved = assertClaudeConfigMessagingResidue(t, providerConfig, []string{private.Token, private.Socket}) || upstreamLocatorObserved
	if err := os.RemoveAll(providerConfig); err != nil {
		t.Fatal("disposable provider source state cleanup failed")
	}
	if _, err := os.Lstat(providerConfig); !os.IsNotExist(err) {
		t.Fatal("disposable provider source state survived cleanup")
	}
	t.Logf("public SessionStart→exact ready→exit→invalidated; tools=0 MCP=0 plugins=0 tool-use=0; messaging-token residue=0; helper/process/socket cleanup=0; upstream-public-locator-observed=%t; disposable-provider-state cleanup=0; Phase1 registration source gate only", upstreamLocatorObserved)
}

func assertClaudeConfigMessagingResidue(t *testing.T, root string, private []string) bool {
	t.Helper()
	tokenFound, socketFound := false, false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Account auth is caller-owned and never read or copied by this test.
		if entry.Name() == ".credentials.json" || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tokenFound = tokenFound || (len(private) > 0 && private[0] != "" && bytes.Contains(data, []byte(private[0])))
		socketFound = socketFound || (len(private) > 1 && private[1] != "" && bytes.Contains(data, []byte(private[1])))
		return nil
	})
	if err != nil {
		t.Fatal("disposable provider diagnostics scan failed")
	}
	if tokenFound {
		t.Fatal("messaging credential residue in disposable provider state")
	}
	// Native source state may store its own public locator, just as the public
	// init event does. It is never interpreted as authority or emitted, and the
	// entire task-owned directory is removed before reporting final evidence.
	return socketFound
}

func countClaudeToolUse(value any) int {
	count := 0
	switch value := value.(type) {
	case map[string]any:
		if kind, _ := value["type"].(string); kind == "tool_use" {
			count++
		}
		for _, child := range value {
			count += countClaudeToolUse(child)
		}
	case []any:
		for _, child := range value {
			count += countClaudeToolUse(child)
		}
	}
	return count
}

func TestClaudeEndpointLeasePathFitsSupportedUnixSockets(t *testing.T) {
	t.Setenv("TMPDIR", "/var/folders/"+strings.Repeat("long", 30)+"/T")
	path := claudeLeaseSocket("/very/long/creator/metadata/registry.json", "pane", "generation", "registration")
	if len(path) >= 104 {
		t.Fatal("helper locator exceeds supported Darwin sun_path")
	}
}
