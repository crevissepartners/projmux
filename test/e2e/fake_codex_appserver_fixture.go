// Command fake_codex_appserver_fixture is an offline Codex app-server fixture
// used only by the lifecycle E2E smoke. It implements the read-only daemon
// version probe and minimum proxy surface needed for native lifecycle coverage.
package main

import (
	"bufio"
	"crypto/sha1" // #nosec G505 -- RFC 6455 requires SHA-1 for the handshake.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	// One event beyond both 64-entry delivery bounds deterministically revokes
	// the target binding. Keep the burst close to that edge: every duplicate
	// is still an intentional target projection write, and hundreds of them
	// can load the shared tmux server enough to perturb an otherwise healthy
	// sibling observer in this timing-sensitive real-process fixture.
	fixtureDisconnectBurst = 96
)

type fixtureCommand uint8

const (
	fixtureCommandUnknown fixtureCommand = iota
	fixtureCommandProxy
	fixtureCommandDaemonVersion
	fixtureCommandControlServer
	fixtureCommandDialogueBind
	fixtureCommandDialogueAgent
)

func main() {
	var err error
	switch classifyFixtureCommand(os.Args[1:]) {
	case fixtureCommandProxy:
		err = serveProxy()
	case fixtureCommandDaemonVersion:
		err = writeDaemonVersion(os.Stdout)
	case fixtureCommandControlServer:
		err = serveControlSocket()
	case fixtureCommandDialogueBind:
		err = bindDialogueAgentRoute(os.Args[2:])
	case fixtureCommandDialogueAgent:
		err = waitForDialogueAgentExit()
	default:
		fmt.Fprintln(os.Stderr, "unsupported fake Codex command")
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func classifyFixtureCommand(args []string) fixtureCommand {
	if len(args) == 2 && args[0] == "app-server" && args[1] == "proxy" {
		return fixtureCommandProxy
	}
	if len(args) == 3 && args[0] == "app-server" && args[1] == "daemon" && args[2] == "version" {
		return fixtureCommandDaemonVersion
	}
	if len(args) == 2 && args[0] == "app-server" && args[1] == "fixture-control" {
		return fixtureCommandControlServer
	}
	if dialogueFixtureProfile() {
		if len(args) == 5 && args[0] == "dialogue-bind" {
			return fixtureCommandDialogueBind
		}
		return fixtureCommandDialogueAgent
	}
	return fixtureCommandUnknown
}

func dialogueFixtureProfile() bool {
	dir, _, err := fixtureProfilePaths()
	return err == nil && dir == filepath.Join(filepath.Clean(os.Getenv("PROJMUX_SMOKE_WORKDIR")), "heterogeneous-dialogue", "codex-state")
}

// waitForDialogueAgentExit is the payload-free Codex Pane fixture. It starts
// no app-server thread or turn and writes no provider ledger entry.
func waitForDialogueAgentExit() error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	<-signals
	return nil
}

// bindDialogueAgentRoute creates the content-free, exact composite authority
// of a pre-existing synthetic Codex conversation. This test-only setup uses
// the same Registry mutators as native binding but performs no provider call,
// user turn, or model-history write.
func bindDialogueAgentRoute(args []string) error {
	if len(args) != 4 || !dialogueFixtureProfile() {
		return errors.New("dialogue binding requires one exact owned route")
	}
	registryPath, agentUID, paneUID, generation := args[0], args[1], args[2], args[3]
	wantRegistry := filepath.Join(filepath.Clean(os.Getenv("PROJMUX_SMOKE_WORKDIR")), "heterogeneous-dialogue", "state", "projmux", "metadata", "registry.json")
	if !filepath.IsAbs(registryPath) || filepath.Clean(registryPath) != wantRegistry ||
		strings.TrimSpace(agentUID) == "" || strings.TrimSpace(paneUID) == "" || strings.TrimSpace(generation) == "" {
		return errors.New("dialogue binding escaped its exact owned route")
	}
	store := intmetadata.NewStore(registryPath)
	_, _, err := store.UpdateConvergent(func(registry *coremetadata.Registry) error {
		endpoint := coremetadata.CodexEndpointRef{StateDomainID: "dialogue-state-domain", EndpointGenerationID: "dialogue-endpoint-generation"}
		mutator := intmetadata.DefaultMutator()
		if err := mutator.StageCodexEndpoint(registry, agentUID, endpoint); err != nil {
			return err
		}
		if _, err := mutator.BindCodexActivation(registry, coremetadata.CodexActivationObservation{
			AgentUID: agentUID, PaneUID: paneUID, Generation: generation,
			ThreadID: "dialogue-preexisting-thread", Endpoint: endpoint,
		}); err != nil {
			return err
		}
		pane, ok := registry.Pane(paneUID)
		if !ok || pane.Status.Activation.Codex == nil {
			return errors.New("dialogue Codex activation disappeared")
		}
		pane.Status.Activation.Codex.Authority = &coremetadata.CodexAuthorityRef{
			StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
			BrokerRuntimeID: "dialogue-broker-runtime", ConnectionEpoch: 1, BindingEpoch: 1,
		}
		return nil
	})
	return err
}

func writeDaemonVersion(writer io.Writer) error {
	return json.NewEncoder(writer).Encode(map[string]any{
		"status":              "running",
		"backend":             "pid",
		"managedCodexPath":    "/discarded/fake-managed-codex",
		"managedCodexVersion": "0.149.0",
		"socketPath":          "/discarded/fake-control.sock",
		"cliVersion":          "0.149.0",
		"appServerVersion":    "0.149.0",
	})
}

type fixturePeer struct {
	mu     sync.Mutex
	writer io.Writer
}

type fixtureControlHub struct {
	mu     sync.Mutex
	shared *fixturePeer
	conns  map[net.Conn]struct{}
}

func (h *fixtureControlHub) setShared(peer *fixturePeer) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.shared = peer
	h.mu.Unlock()
}

func (h *fixtureControlHub) eventPeer(fallback *fixturePeer) *fixturePeer {
	if h == nil {
		return fallback
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shared
}

func (h *fixtureControlHub) track(conn net.Conn) {
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	h.mu.Unlock()
}

func (h *fixtureControlHub) untrack(conn net.Conn) {
	h.mu.Lock()
	delete(h.conns, conn)
	h.mu.Unlock()
}

func (h *fixtureControlHub) closeAll() {
	h.mu.Lock()
	conns := make([]net.Conn, 0, len(h.conns))
	for conn := range h.conns {
		conns = append(conns, conn)
	}
	h.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func fixtureControlSocketPath() (string, error) {
	_, expected, err := fixtureProfilePaths()
	if err != nil {
		return "", err
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if !filepath.IsAbs(codexHome) || filepath.Clean(codexHome) != expected {
		return "", errors.New("CODEX_HOME must match the selected fixed Codex fixture profile")
	}
	dir := filepath.Join(expected, "app-server-control")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "app-server-control.sock"), nil
}

func serveControlSocket() error {
	path, err := fixtureControlSocketPath()
	if err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(true)
	hub := &fixtureControlHub{conns: make(map[net.Conn]struct{})}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	go func() {
		<-signals
		_ = listener.Close()
		hub.closeAll()
	}()
	var handlers sync.WaitGroup
	for {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			_ = listener.Close()
			hub.closeAll()
			handlers.Wait()
			if errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return acceptErr
		}
		hub.track(conn)
		handlers.Go(func() {
			defer hub.untrack(conn)
			defer conn.Close()
			if serveErr := serveFixtureConnection(conn, conn, hub); serveErr != nil && !errors.Is(serveErr, io.EOF) && !errors.Is(serveErr, net.ErrClosed) {
				_ = recordFixtureFailure(serveErr)
			}
		})
	}
}

func serveProxy() error {
	return serveFixtureConnection(os.Stdin, os.Stdout, nil)
}

func serveFixtureConnection(input io.Reader, output io.Writer, hub *fixtureControlHub) error {
	observerProxy := false
	activeThread := ""
	defer func() {
		if observerProxy {
			_ = markGate("proxy-exited")
		}
	}()
	reader := bufio.NewReader(input)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	accept := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 handshake checksum.
	peer := &fixturePeer{writer: output}
	if _, err := fmt.Fprintf(output, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:])); err != nil {
		return err
	}
	for {
		payload, opcode, err := readClientFrame(reader)
		if err != nil {
			return err
		}
		if opcode == 0x8 {
			return nil
		}
		if opcode != 0x1 {
			continue
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(payload, &message) != nil {
			return errors.New("invalid JSON request")
		}
		var params struct {
			ThreadID     string `json:"threadId"`
			IncludeTurns bool   `json:"includeTurns"`
		}
		if len(message.Params) > 0 && json.Unmarshal(message.Params, &params) != nil {
			return errors.New("invalid JSON request params")
		}
		if message.Method == "turn/start" || message.Method == "turn/steer" || message.Method == "turn/interrupt" || (message.Method == "" && len(message.ID) > 0) {
			threadID := params.ThreadID
			if threadID == "" {
				threadID = activeThread
			}
			if err := recordProviderWrite(threadID, message.Method); err != nil {
				return err
			}
		}
		switch message.Method {
		case "initialize":
			if err := peer.writeResult(message.ID, map[string]any{"userAgent": "codex-cli/0.149.0", "platformFamily": "linux", "platformOs": "linux"}); err != nil {
				return err
			}
		case "initialized":
		case "remoteControl/status/read":
			if err := peer.writeResult(message.ID, map[string]any{"status": "disabled"}); err != nil {
				return err
			}
		case "thread/start":
			threadID, err := nextStartedThread()
			if err != nil {
				return err
			}
			activeThread = threadID
			if err := peer.writeResult(message.ID, map[string]any{"thread": map[string]any{"id": threadID}}); err != nil {
				return err
			}
		case "turn/start":
			threadID := params.ThreadID
			if threadID == "" {
				threadID = activeThread
			}
			if err := validateFixtureThread(threadID); err != nil {
				return err
			}
			if err := peer.writeResult(message.ID, map[string]any{"turn": map[string]any{"id": fixtureTurnID(threadID), "status": "inProgress"}}); err != nil {
				return err
			}
		case "turn/steer":
			if err := validateFixtureThread(params.ThreadID); err != nil {
				return err
			}
			if err := peer.writeResult(message.ID, map[string]any{"turn": map[string]any{"id": fixtureTurnID(params.ThreadID), "status": "inProgress"}}); err != nil {
				return err
			}
		case "thread/resume":
			// The endpoint broker subscribes the exact thread before it reads
			// it. The subscription creates nothing and starts no turn.
			if err := validateFixtureThread(params.ThreadID); err != nil {
				return err
			}
			activeThread = params.ThreadID
			hub.setShared(peer)
			if err := peer.writeResult(message.ID, map[string]any{"thread": map[string]any{"id": params.ThreadID}}); err != nil {
				return err
			}
		case "thread/read":
			if err := validateFixtureThread(params.ThreadID); err != nil {
				return err
			}
			if !params.IncludeTurns {
				hub.setShared(peer)
				// The broker's pre-turn bootstrap snapshot. It carries no turn,
				// so it is not the lifecycle epoch the scripted scenario steps.
				if err := peer.writeResult(message.ID, map[string]any{"thread": map[string]any{
					"id": params.ThreadID, "cwd": "/discarded", "createdAt": 1, "updatedAt": 2,
					"status": map[string]any{"type": "active", "activeFlags": []string{}},
				}}); err != nil {
					return err
				}
				continue
			}
			observerProxy = true
			epoch, err := nextObserverEpoch(params.ThreadID)
			if err != nil {
				return err
			}
			if params.ThreadID == "thread-phase3" && epoch > 1 {
				requestID := append(json.RawMessage(nil), message.ID...)
				go func() {
					if err := waitForGate("allow-reconnect"); err != nil {
						_ = recordFixtureFailure(err)
						return
					}
					if err := peer.writeResult(requestID, map[string]any{"thread": map[string]any{
						"id": "thread-phase3", "status": map[string]any{"type": "idle", "activeFlags": []string{}}, "turns": []map[string]any{},
					}}); err != nil {
						_ = recordFixtureFailure(err)
					}
				}()
				continue
			}
			status := map[string]any{"type": "idle", "activeFlags": []string{}}
			turns := []map[string]any{}
			if params.ThreadID == "thread-phase3" && epoch == 1 {
				status = map[string]any{"type": "active", "activeFlags": []string{}}
				turns = []map[string]any{{"id": "turn-phase3", "status": "inProgress"}}
			} else if params.ThreadID == "thread-sibling" {
				status = map[string]any{"type": "active", "activeFlags": []string{}}
				turns = []map[string]any{{"id": "turn-sibling", "status": "inProgress"}}
			}
			if err := peer.writeResult(message.ID, map[string]any{"thread": map[string]any{"id": params.ThreadID, "status": status, "turns": turns}}); err != nil {
				return err
			}
			if params.ThreadID == "thread-phase3" && epoch == 1 {
				eventPeer := hub.eventPeer(peer)
				if eventPeer == nil {
					return errors.New("owned lifecycle read has no shared event authority")
				}
				go func() {
					if err := func() error {
						if err := waitForGate("emit-auto-approved"); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"id": "request-auto", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-phase3", "turnId": "turn-phase3", "itemId": "item-auto"}}); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-phase3", "requestId": "request-auto"}}); err != nil {
							return err
						}
						if err := markGate("auto-approved-emitted"); err != nil {
							return err
						}
						if err := waitForGate("emit-actionable"); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"id": "request-actionable", "method": "item/permissions/requestApproval", "params": map[string]any{"threadId": "thread-phase3", "turnId": "turn-phase3", "itemId": "item-actionable"}}); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": "thread-phase3", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}}); err != nil {
							return err
						}
						if err := waitForGate("resolve-actionable"); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-phase3", "requestId": "request-actionable"}}); err != nil {
							return err
						}
						if err := markGate("resolved-sent"); err != nil {
							return err
						}
						if err := markGate("waiting-completion-gate"); err != nil {
							return err
						}
						if err := waitForGate("emit-complete"); err != nil {
							return err
						}
						if err := markGate("completion-gate-seen"); err != nil {
							return err
						}
						if err := eventPeer.writeMessage(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-phase3", "turn": map[string]any{"id": "turn-phase3", "status": "completed"}}}); err != nil {
							return err
						}
						if err := markGate("first-completion-sent"); err != nil {
							return err
						}
						// Duplicate delivery must not enqueue or dispatch a second completion.
						if err := eventPeer.writeMessage(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-phase3", "turn": map[string]any{"id": "turn-phase3", "status": "completed"}}}); err != nil {
							return err
						}
						if err := markGate("duplicate-completion-sent"); err != nil {
							return err
						}
						if err := waitForGate("disconnect"); err != nil {
							return err
						}
						for range fixtureDisconnectBurst {
							if err := eventPeer.writeMessage(map[string]any{"method": "thread/status/changed", "params": map[string]any{
								"threadId": "thread-phase3", "status": map[string]any{"type": "active", "activeFlags": []string{}},
							}}); err != nil {
								return err
							}
						}
						return nil
					}(); err != nil {
						_ = recordFixtureFailure(err)
					}
				}()
			}
		}
	}
}

func fixtureStateDir() (string, error) {
	dir, _, err := fixtureProfilePaths()
	if err != nil {
		return "", err
	}
	// #nosec G703 -- fixtureProfilePaths accepts only one of two fixed fixture
	// children under the isolated smoke root.
	return dir, os.MkdirAll(dir, 0o700)
}

func fixtureProfilePaths() (string, string, error) {
	dir := strings.TrimSpace(os.Getenv("PROJMUX_FAKE_CODEX_STATE"))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", "", errors.New("PROJMUX_FAKE_CODEX_STATE must be absolute")
	}
	smokeRoot := strings.TrimSpace(os.Getenv("PROJMUX_SMOKE_WORKDIR"))
	if smokeRoot == "" || !filepath.IsAbs(smokeRoot) {
		return "", "", errors.New("PROJMUX_SMOKE_WORKDIR must be absolute")
	}
	root := filepath.Clean(smokeRoot)
	profiles := [][2]string{
		{filepath.Join(root, "codex-lifecycle", "fake-codex-state"), filepath.Join(root, "codex-lifecycle", "codex-home")},
		{filepath.Join(root, "heterogeneous-dialogue", "codex-state"), filepath.Join(root, "heterogeneous-dialogue", "codex-home")},
	}
	for _, profile := range profiles {
		if filepath.Clean(dir) == profile[0] {
			return profile[0], profile[1], nil
		}
	}
	return "", "", errors.New("PROJMUX_FAKE_CODEX_STATE must select a fixed Codex fixture profile under PROJMUX_SMOKE_WORKDIR")
}

func nextObserverEpoch(threadID string) (int, error) {
	if err := validateFixtureThread(threadID); err != nil {
		return 0, err
	}
	dir, err := fixtureStateDir()
	if err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "epoch-"+threadID)
	// #nosec G304 -- fixtureStateDir validates the private isolated root and epoch is a fixed leaf under it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	data, _ := io.ReadAll(file)
	value := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &value)
	value++
	if err := file.Truncate(0); err != nil {
		return 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	_, err = fmt.Fprintf(file, "%d\n", value)
	return value, err
}

func nextStartedThread() (string, error) {
	dir, err := fixtureStateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "started-threads")
	// #nosec G304 -- fixtureStateDir validates the private isolated root and started-threads is a fixed leaf under it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	data, _ := io.ReadAll(file)
	value := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &value)
	value++
	if value > 2 {
		return "", errors.New("fake Codex fixture supports exactly two started threads")
	}
	if err := file.Truncate(0); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(file, "%d\n", value); err != nil {
		return "", err
	}
	if value == 1 {
		return "thread-phase3", nil
	}
	return "thread-sibling", nil
}

func validateFixtureThread(threadID string) error {
	if threadID != "thread-phase3" && threadID != "thread-sibling" {
		return fmt.Errorf("unsupported fake Codex thread %q", threadID)
	}
	return nil
}

func fixtureTurnID(threadID string) string {
	if threadID == "thread-sibling" {
		return "turn-sibling"
	}
	return "turn-phase3"
}

func waitForGate(name string) error {
	dir, err := fixtureStateDir()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", name)
}

func markGate(name string) error {
	dir, err := fixtureStateDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("ready\n"), 0o600)
}

func recordProviderWrite(threadID, method string) error {
	dir, err := fixtureStateDir()
	if err != nil {
		return err
	}
	if method == "" {
		method = "server-response"
	}
	if threadID == "" {
		threadID = "request"
	}
	path := filepath.Join(dir, "provider-writes")
	// #nosec G304 -- fixtureStateDir validates the isolated smoke child and the
	// provider-writes leaf is fixed under it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "%s|%s\n", threadID, method)
	return err
}

func recordFixtureFailure(failure error) error {
	dir, err := fixtureStateDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "fixture-error"), []byte(failure.Error()+"\n"), 0o600)
}

func (p *fixturePeer) writeResult(id json.RawMessage, result any) error {
	return p.writeMessage(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (p *fixturePeer) writeMessage(message any) error {
	if p == nil || p.writer == nil {
		return errors.New("fixture peer is unavailable")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	header, err := fixtureFrameHeader(len(payload))
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = p.writer.Write(append(header, payload...))
	return err
}

func fixtureFrameHeader(payloadLen int) ([]byte, error) {
	if payloadLen < 0 {
		return nil, errors.New("fixture frame length is negative")
	}
	header := []byte{0x81}
	switch {
	case payloadLen <= 125:
		// #nosec G115 -- the branch proves payloadLen is in the uint8-safe 0..125 range.
		return append(header, byte(payloadLen)), nil
	case payloadLen <= 1<<16-1:
		var extended [2]byte
		// #nosec G115 -- the branch proves payloadLen is in the uint16-safe 126..65535 range.
		binary.BigEndian.PutUint16(extended[:], uint16(payloadLen))
		header = append(header, 126)
		return append(header, extended[:]...), nil
	default:
		return nil, errors.New("fixture frame is too large")
	}
}

func readClientFrame(reader *bufio.Reader) ([]byte, byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, 0, err
	}
	opcode := header[0] & 0x0f
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if header[1]&0x80 == 0 || length > 1<<20 {
		return nil, 0, errors.New("invalid client websocket frame")
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, 0, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, 0, err
	}
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	return payload, opcode, nil
}
