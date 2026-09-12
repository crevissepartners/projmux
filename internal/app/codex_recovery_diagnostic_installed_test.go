package app

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- RFC 6455 synthetic fixture handshake only.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

type installedDiagnosticInput struct {
	Root         string `json:"root"`
	Binary       string `json:"binary"`
	BinarySHA256 string `json:"binarySHA256"`
	SourceHead   string `json:"sourceHead"`
	SourceTree   string `json:"sourceTree"`
	Evidence     string `json:"evidence"`
}

type installedDiagnosticEvidence struct {
	Result           string                   `json:"result"`
	Provenance       string                   `json:"provenance"`
	Input            installedDiagnosticInput `json:"input"`
	TestBinarySHA256 string                   `json:"testBinarySHA256"`
	Journal          []aiIngestLogEntry       `json:"journal"`
	Doctor           []*codexappserver.Health `json:"doctor"`
	PublicJSON       []aiIngestLogEntry       `json:"publicJSON"`
	PublicText       []string                 `json:"publicText"`
	ProviderTurns    int                      `json:"providerTurns"`
	ManagerMutations int                      `json:"managerMutations"`
	SocketVerified   bool                     `json:"socketVerified"`
	Cleanup          bool                     `json:"cleanup"`
}

// TestInstalledCodexRecoveryDiagnosticFixture runs the actual candidate/installed
// executable's observer and doctor. Provider responses, bootstrap identity, and
// manager responses are explicitly SYNTHETIC. No real Codex CLI, auth, model,
// turn, live session, or daemon lifecycle action is used. The actual request,
// catalog wrapping, broker IPC, exact Registry CAS, journal, and doctor renderers
// are production paths. The same explicit input schema is used before/after merge.
func TestInstalledCodexRecoveryDiagnosticFixture(t *testing.T) {
	inputPath := os.Getenv("CODEX_DIAGNOSTIC_INPUT")
	if inputPath == "" {
		t.Skip("requires explicit isolated installed diagnostic input")
	}
	raw, err := codexinstalled.ReadExplicitInput(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var input installedDiagnosticInput
	if json.Unmarshal(raw, &input) != nil || !strings.HasPrefix(input.Root, "/tmp/") || filepath.Clean(input.Root) != input.Root || !filepath.IsAbs(input.Binary) || len(input.SourceHead) != 40 || len(input.SourceTree) != 40 || len(input.BinarySHA256) != 64 || !filepath.IsAbs(input.Evidence) || strings.HasPrefix(input.Evidence, input.Root+"/") {
		t.Fatal("invalid explicit diagnostic input")
	}
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(key); present {
			t.Fatal("unset inherited tmux routing before diagnostic fixture")
		}
	}
	evidence := installedDiagnosticEvidence{Result: "FAIL", Provenance: "synthetic manager/provider responses and legacy journal row; real installed observer, broker IPC, journal, doctor, public JSON/text diagnostics", Input: input}
	evidence.TestBinarySHA256 = verifyInstalledRecoveryBinary(t, input.Binary, input.SourceHead, input.BinarySHA256)
	defer func() {
		if t.Failed() {
			evidence.Result = "FAIL"
		}
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(input.Evidence, append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}()
	if err := os.Mkdir(input.Root, 0700); err != nil {
		t.Fatal("fixture root must not already exist")
	}
	defer func() {
		if err := os.RemoveAll(input.Root); err != nil {
			t.Error(err)
		}
	}()
	for key, leaf := range map[string]string{"HOME": "h", "CODEX_HOME": "c", "XDG_STATE_HOME": "s", "XDG_CONFIG_HOME": "g", "XDG_CACHE_HOME": "a", "XDG_DATA_HOME": "d", "XDG_RUNTIME_DIR": "r", "TMUX_TMPDIR": "t"} {
		path := filepath.Join(input.Root, leaf)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "DBUS_SESSION_BUS_PID", "DISPLAY", "WAYLAND_DISPLAY"} {
		t.Setenv(key, "")
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TERM", "xterm-256color")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	run := func(executable string, args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, executable, args...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("diagnostic command failed (%s): %v", filepath.Base(executable), err)
		}
		return strings.TrimSpace(string(out))
	}
	socketName := "diag-" + filepath.Base(input.Root)
	runtimeID := run("tmux", "-L", socketName, "-f", "/dev/null", "new-session", "-d", "-s", "diagnostic", "-P", "-F", "#{pane_id}")
	socket := run("tmux", "-L", socketName, "display-message", "-p", "#{socket_path}")
	if !strings.HasPrefix(socket, os.Getenv("TMUX_TMPDIR")+"/") {
		t.Fatal("socket escaped private root")
	}
	evidence.SocketVerified = true
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		output, err := exec.CommandContext(cleanupCtx, "tmux", "-S", socket, "display-message", "-p", "#{socket_path}").Output()
		observed := strings.TrimSpace(string(output))
		if err != nil || observed != socket || !strings.HasPrefix(observed, os.Getenv("TMUX_TMPDIR")+"/") {
			t.Error("cleanup socket proof failed")
			return
		}
		if err := exec.CommandContext(cleanupCtx, "tmux", "-S", socket, "kill-server").Run(); err != nil {
			t.Error("exact socket cleanup failed")
			return
		}
		evidence.Cleanup = true
	}()
	run("tmux", "-S", socket, "set-option", "-g", tmuxopts.AppGlobal, "1")
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	fake, identity := phase0RNativeCodexFixture(t)
	identity.RuntimeID = runtimeID
	pane, _ := fake.registry.Pane(identity.PaneUID)
	pane.Status.Activation.RuntimeID = runtimeID
	store := intmetadata.NewDefaultStore(paths)
	if _, err := store.Update(func(registry *coremetadata.Registry) error { *registry = fake.registry; return nil }); err != nil {
		t.Fatal(err)
	}
	run("tmux", "-S", socket, "set-option", "-p", "-t", runtimeID, tmuxopts.PaneUID, identity.PaneUID)
	// Select the exact synthetic Codex Agent rather than relying on registry order.
	agent, _ := fake.registry.Agent(identity.AgentUID)
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	key, err := codexbroker.NewEndpointKey(endpoint.StateDomainID, endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(paths.StateDir, key)
	if err != nil {
		t.Fatal(err)
	}
	target := codexLifecycleObserverTarget{Identity: identity, Route: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}, NativeRoute: codexNativeEndpointRoute{Endpoint: endpoint, State: coremetadata.CodexGenerationCurrent, TUIExecutable: input.Binary, SocketPath: filepath.Join(input.Root, "synthetic-provider.sock")}}
	for _, mode := range []string{"unsupported", "catalog", "protocol", "transport"} {
		shared := newBrokerTestEndpoint()
		broker, err := codexbroker.NewBroker(codexbroker.Config{Endpoint: key, Opener: func(context.Context) (codexbroker.Endpoint, error) { return shared, nil }, Lifecycle: func(_ context.Context, peer codexappserver.PeerIdentity) (codexappserver.LifecycleEndpoint, error) {
			return diagnosticOwnedEndpoint{peer: peer, mode: mode}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: -1})
		if err != nil {
			t.Fatal(err)
		}
		result := startCodexLifecycleObserverProcess(input.Binary, target, 10*time.Second)
		_ = host.Close()
		_ = broker.Close()
		expected := codexappserver.Diagnostic(syntheticDiagnosticFailure(ctx, mode))
		if result.Status != codexObserverStartupFallback || !result.committed {
			t.Fatalf("installed %s startup: %+v", mode, result)
		}
		data, err := os.ReadFile(filepath.Join(paths.StateDir, aiIngestLogName))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var entry aiIngestLogEntry
		if json.Unmarshal([]byte(lines[len(lines)-1]), &entry) != nil || entry.Failure == nil || entry.Failure.String() != expected.String() || string(entry.Reason) != string(codexNativeReason(syntheticDiagnosticFailure(ctx, mode))) {
			t.Fatalf("installed %s lost original failure: %+v", mode, entry)
		}
		evidence.Journal = append(evidence.Journal, entry)
		if shared.requestCount("turn/start") != 0 || len(shared.answerLedger()) != 0 {
			t.Fatal("diagnostic performed provider input")
		}
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(input.Root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	shim := `#!/bin/sh
printf '%s\n' "$*" >> "$CODEX_DIAGNOSTIC_ARGV"
exec "$CODEX_DIAGNOSTIC_HELPER" -test.run=^TestCodexDiagnosticProxyProcess$ -- "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_DIAGNOSTIC_HELPER", helper)
	t.Setenv("CODEX_DIAGNOSTIC_ARGV", filepath.Join(input.Root, "argv"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, row := range []struct{ manager, version, agreement, owner string }{
		{`{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`, "0.154.0", "consistent", "managed"},
		{`{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"0.151.0"}`, "0.151.0", "consistent", "managed"},
		{`{"status":"running","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`, "0.154.0", "consistent", "unmanaged"},
		{`{"status":"running","backend":null,"cliVersion":"0.154.0","appServerVersion":"0.154.0"}`, "0.154.0", "consistent", "unknown"},
		{`{"status":"running","backend":"pid","cliVersion":"0.154.0","appServerVersion":"0.154.0"}`, "0.151.0", "contradictory", "unknown"},
		{`{"status":"stopped"}`, "0.154.0", "contradictory", "unknown"},
		{`{"managedCodexPath":"/secret/managed/path"}`, "0.154.0", "insufficient", "unknown"},
		{`{broken`, "0.154.0", "insufficient", "unknown"},
	} {
		t.Setenv("CODEX_DIAGNOSTIC_MANAGER", row.manager)
		t.Setenv("CODEX_DIAGNOSTIC_VERSION", row.version)
		out := run(input.Binary, "doctor", "--section", "integrations", "--json")
		var report doctorReport
		if json.Unmarshal([]byte(out), &report) != nil || report.CodexAppServer == nil {
			t.Fatal("installed doctor JSON missing health")
		}
		health := report.CodexAppServer
		text := run(input.Binary, "doctor", "--section", "integrations")
		if health.ManagerEvidence == nil || health.ManagerEvidence.Agreement != row.agreement || string(health.ManagerOwnership) != row.owner || !strings.Contains(text, "agreement "+row.agreement) || !strings.Contains(text, "manager ownership: "+row.owner) {
			t.Fatalf("installed doctor inconsistent with synthetic evidence: %+v", health)
		}
		if guidance := health.OperatorRecovery.Guidance(); guidance != "" && !strings.Contains(text, guidance) {
			t.Fatal("doctor text/JSON recovery mismatch")
		}
		projection, _ := json.Marshal(health)
		if len(projection) > 4096 || strings.Contains(string(projection), "secret") {
			t.Fatal("doctor health bound/redaction failed")
		}
		evidence.Doctor = append(evidence.Doctor, health)
		if codexappserver.AuthorityFor(*health).Attach != codexappserver.EndpointAttachAllowed {
			defaultTarget := target
			defaultTarget.NativeRoute.Default = true
			defaultTarget.NativeRoute.SocketPath = ""
			result := startCodexLifecycleObserverProcess(input.Binary, defaultTarget, 10*time.Second)
			if result.Status != codexObserverStartupFallback || !result.committed {
				t.Fatalf("ownership fallback missing: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(paths.StateDir, aiIngestLogName))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var entry aiIngestLogEntry
			if json.Unmarshal([]byte(lines[len(lines)-1]), &entry) != nil || entry.Recovery == nil {
				t.Fatal("installed journal lost manager decision")
			}
			want := codexappserver.RecoveryDiagnosticOf(codexappserver.WithHealthDiagnostic(fmt.Errorf("fixture"), *health))
			observed, _ := json.Marshal(entry.Recovery)
			expected, _ := json.Marshal(want)
			if string(observed) != string(expected) {
				t.Fatalf("journal/doctor recovery mismatch: %s vs %s", observed, expected)
			}
			evidence.Journal = append(evidence.Journal, entry)
		}
	}
	for _, mode := range []string{"unsupported", "protocol", "transport"} {
		t.Setenv("CODEX_DIAGNOSTIC_FAILURE", mode)
		out := run(input.Binary, "doctor", "--section", "integrations", "--json")
		var report doctorReport
		if json.Unmarshal([]byte(out), &report) != nil || report.CodexAppServer == nil || report.CodexAppServer.Failure == nil {
			t.Fatal("doctor lost original initialize failure")
		}
		health := report.CodexAppServer
		defaultTarget := target
		defaultTarget.NativeRoute.Default = true
		defaultTarget.NativeRoute.SocketPath = ""
		result := startCodexLifecycleObserverProcess(input.Binary, defaultTarget, 10*time.Second)
		if result.Status != codexObserverStartupFallback || !result.committed {
			t.Fatal("initialize failure did not fall back")
		}
		data, err := os.ReadFile(filepath.Join(paths.StateDir, aiIngestLogName))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var entry aiIngestLogEntry
		if json.Unmarshal([]byte(lines[len(lines)-1]), &entry) != nil || entry.Failure == nil || entry.Failure.String() != health.Failure.String() {
			t.Fatal("doctor/journal original initialize failure mismatch")
		}
		evidence.Journal = append(evidence.Journal, entry)
		evidence.Doctor = append(evidence.Doctor, health)
	}
	// Exercise the public consumers, including a synthetic pre-diagnostic row.
	// All preceding rows were emitted by the actual observer executable.
	legacy := aiIngestLogEntry{At: "2026-09-13T00:00:00Z", Source: "codex-observer", Event: "observer.fallback", Result: "provider-hook", Reason: aiIngestRecordReason("unsupported")}
	legacyJSON, _ := json.Marshal(legacy)
	journalFile, err := os.OpenFile(filepath.Join(paths.StateDir, aiIngestLogName), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := journalFile.Write(append(legacyJSON, '\n'))
	closeErr := journalFile.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("could not append synthetic legacy journal row")
	}
	publicJSON := strings.Split(run(input.Binary, "diagnostics", "agent-hook", "--json"), "\n")
	evidence.PublicText = strings.Split(run(input.Binary, "diagnostics", "agent-hook"), "\n")
	wantRows := append(append([]aiIngestLogEntry{}, evidence.Journal...), legacy)
	if len(publicJSON) != len(wantRows) || len(evidence.PublicText) != len(wantRows) {
		t.Fatal("public journal consumers dropped records")
	}
	for i, want := range wantRows {
		var entry aiIngestLogEntry
		if err := json.Unmarshal([]byte(publicJSON[i]), &entry); err != nil {
			t.Fatal("public journal JSON is invalid")
		}
		gotJSON, _ := json.Marshal(entry)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatal("public JSON changed observer failure/recovery projection")
		}
		evidence.PublicJSON = append(evidence.PublicJSON, entry)
		text := evidence.PublicText[i]
		if !strings.Contains(text, "reason="+string(want.Reason)) {
			t.Fatal("public text lost legacy reason")
		}
		if want.Failure != nil && !strings.Contains(text, "failure="+want.Failure.String()) {
			t.Error("public text lost original observer failure")
		}
		if want.Recovery != nil {
			recovery, _ := json.Marshal(want.Recovery)
			if !strings.Contains(text, "recovery="+string(recovery)) {
				t.Error("public text lost doctor/journal recovery evidence")
			}
		}
		if len(publicJSON[i]) > maxCodexObserverFailureRecordBytes || len(text) > maxCodexObserverFailureRecordBytes || strings.Contains(text+publicJSON[i], "secret") || strings.ContainsAny(text, "\x00\x1b\xff") {
			t.Fatal("public diagnostics exceeded bounds or disclosed raw provider payload")
		}
	}
	if evidence.PublicText[len(wantRows)-1] != "2026-09-13T00:00:00Z codex-observer observer.fallback provider-hook reason=unsupported" {
		t.Fatal("public text changed synthetic legacy record")
	}
	argv, err := os.ReadFile(filepath.Join(input.Root, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(argv)), "\n") {
		if line != "app-server proxy" && line != "app-server daemon version" && line != "--version" {
			t.Fatalf("unexpected diagnostic command %q", line)
		}
	}
	evidence.Result = "PASS"
}

// This subprocess is a synthetic stand-in for ONLY the documented read-only
// daemon version/proxy commands. It rejects every lifecycle or model action.
func TestCodexDiagnosticProxyProcess(t *testing.T) {
	if os.Getenv("CODEX_DIAGNOSTIC_HELPER") == "" {
		return
	}
	args := os.Args
	split := 0
	for i, arg := range args {
		if arg == "--" {
			split = i + 1
			break
		}
	}
	if split == 0 {
		return
	}
	switch strings.Join(args[split:], " ") {
	case "app-server daemon version":
		fmt.Fprintln(os.Stdout, os.Getenv("CODEX_DIAGNOSTIC_MANAGER"))
		os.Exit(0)
	case "--version":
		fmt.Fprintln(os.Stdout, "codex-cli 0.154.0")
		os.Exit(0)
	case "app-server proxy":
	default:
		os.Exit(91)
	}
	reader := bufio.NewReader(os.Stdin)
	request, err := http.ReadRequest(reader)
	if err != nil {
		os.Exit(92)
	}
	sum := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) // #nosec G401 -- RFC 6455 fixture handshake.
	fmt.Fprintf(os.Stdout, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	for {
		var head [2]byte
		if _, err := io.ReadFull(reader, head[:]); err != nil {
			os.Exit(0)
		}
		n := uint64(head[1] & 127)
		if n == 126 {
			var ext [2]byte
			if _, err := io.ReadFull(reader, ext[:]); err != nil {
				os.Exit(93)
			}
			n = uint64(binary.BigEndian.Uint16(ext[:]))
		}
		if n > 4096 || n == 127 {
			os.Exit(94)
		}
		var mask [4]byte
		if head[1]&128 != 0 {
			if _, err := io.ReadFull(reader, mask[:]); err != nil {
				os.Exit(95)
			}
		}
		data := make([]byte, int(n))
		if _, err := io.ReadFull(reader, data); err != nil {
			os.Exit(96)
		}
		if head[1]&128 != 0 {
			for i := range data {
				data[i] ^= mask[i%4]
			}
		}
		if head[0]&15 == 8 {
			os.Exit(0)
		}
		var message struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(data, &message) != nil {
			os.Exit(97)
		}
		if message.Method == "initialize" && os.Getenv("CODEX_DIAGNOSTIC_FAILURE") != "" {
			mode := os.Getenv("CODEX_DIAGNOSTIC_FAILURE")
			if mode == "transport" {
				os.Exit(0)
			}
			payload := fmt.Appendf(nil, `{"id":%s,"error":{"code":-32601,"message":"secret"}}`, message.ID)
			if mode == "protocol" {
				payload = []byte(`{broken`)
			}
			_, _ = os.Stdout.Write(append([]byte{0x81, byte(len(payload))}, payload...))
			continue
		}
		var result any
		switch message.Method {
		case "initialize":
			result = map[string]string{"userAgent": "codex-cli/" + os.Getenv("CODEX_DIAGNOSTIC_VERSION")}
		case "initialized":
			continue
		case "remoteControl/status/read":
			result = map[string]string{"status": "disabled"}
		default:
			os.Exit(98)
		}
		payload, _ := json.Marshal(map[string]any{"id": message.ID, "result": result})
		frame := []byte{0x81}
		if len(payload) < 126 {
			frame = append(frame, byte(len(payload)))
		} else {
			frame = append(frame, 126, byte(len(payload)>>8), byte(len(payload)))
		}
		_, _ = os.Stdout.Write(append(frame, payload...))
	}
}
