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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type fixtureCommand uint8

const (
	fixtureCommandUnknown fixtureCommand = iota
	fixtureCommandProxy
	fixtureCommandDaemonVersion
)

func main() {
	var err error
	switch classifyFixtureCommand(os.Args[1:]) {
	case fixtureCommandProxy:
		err = serveProxy()
	case fixtureCommandDaemonVersion:
		err = writeDaemonVersion(os.Stdout)
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
	return fixtureCommandUnknown
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

func serveProxy() error {
	observerProxy := false
	defer func() {
		if observerProxy {
			_ = markGate("proxy-exited")
		}
	}()
	reader := bufio.NewReader(os.Stdin)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	accept := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 handshake checksum.
	if _, err := fmt.Fprintf(os.Stdout, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:])); err != nil {
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
		}
		if json.Unmarshal(payload, &message) != nil {
			return errors.New("invalid JSON request")
		}
		switch message.Method {
		case "initialize":
			if err := writeResult(message.ID, map[string]any{"userAgent": "codex-cli/0.149.0", "platformFamily": "linux", "platformOs": "linux"}); err != nil {
				return err
			}
		case "initialized":
		case "remoteControl/status/read":
			if err := writeResult(message.ID, map[string]any{"status": "disabled"}); err != nil {
				return err
			}
		case "thread/start":
			if err := writeResult(message.ID, map[string]any{"thread": map[string]any{"id": "thread-phase3"}}); err != nil {
				return err
			}
		case "turn/start":
			if err := writeResult(message.ID, map[string]any{"turn": map[string]any{"id": "turn-phase3", "status": "inProgress"}}); err != nil {
				return err
			}
		case "thread/read":
			observerProxy = true
			epoch, err := nextObserverEpoch()
			if err != nil {
				return err
			}
			if epoch > 1 {
				if err := waitForGate("allow-reconnect"); err != nil {
					return err
				}
			}
			status := map[string]any{"type": "idle", "activeFlags": []string{}}
			turns := []map[string]any{}
			if epoch == 1 {
				status = map[string]any{"type": "active", "activeFlags": []string{}}
				turns = []map[string]any{{"id": "turn-phase3", "status": "inProgress"}}
			}
			if err := writeResult(message.ID, map[string]any{"thread": map[string]any{"id": "thread-phase3", "status": status, "turns": turns}}); err != nil {
				return err
			}
			if epoch == 1 {
				if err := waitForGate("emit-auto-approved"); err != nil {
					return err
				}
				if err := writeMessage(map[string]any{"id": "request-auto", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-phase3", "turnId": "turn-phase3", "itemId": "item-auto"}}); err != nil {
					return err
				}
				if err := writeMessage(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-phase3", "requestId": "request-auto"}}); err != nil {
					return err
				}
				if err := markGate("auto-approved-emitted"); err != nil {
					return err
				}
				if err := waitForGate("emit-actionable"); err != nil {
					return err
				}
				if err := writeMessage(map[string]any{"id": "request-actionable", "method": "item/permissions/requestApproval", "params": map[string]any{"threadId": "thread-phase3", "turnId": "turn-phase3", "itemId": "item-actionable"}}); err != nil {
					return err
				}
				if err := writeMessage(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": "thread-phase3", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}}); err != nil {
					return err
				}
				if err := waitForGate("resolve-actionable"); err != nil {
					return err
				}
				if err := writeMessage(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-phase3", "requestId": "request-actionable"}}); err != nil {
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
				if err := writeMessage(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-phase3", "turn": map[string]any{"id": "turn-phase3", "status": "completed"}}}); err != nil {
					return err
				}
				if err := markGate("first-completion-sent"); err != nil {
					return err
				}
				// Duplicate delivery must not enqueue or dispatch a second completion.
				if err := writeMessage(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-phase3", "turn": map[string]any{"id": "turn-phase3", "status": "completed"}}}); err != nil {
					return err
				}
				if err := markGate("duplicate-completion-sent"); err != nil {
					return err
				}
				if err := waitForGate("disconnect"); err != nil {
					return err
				}
				return nil
			}
		}
	}
}

func fixtureStateDir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("PROJMUX_FAKE_CODEX_STATE"))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("PROJMUX_FAKE_CODEX_STATE must be absolute")
	}
	smokeRoot := strings.TrimSpace(os.Getenv("PROJMUX_SMOKE_WORKDIR"))
	if smokeRoot == "" || !filepath.IsAbs(smokeRoot) {
		return "", errors.New("PROJMUX_SMOKE_WORKDIR must be absolute")
	}
	expected := filepath.Join(filepath.Clean(smokeRoot), "codex-lifecycle", "fake-codex-state")
	if filepath.Clean(dir) != expected {
		return "", errors.New("PROJMUX_FAKE_CODEX_STATE must be the fixed Codex lifecycle child of PROJMUX_SMOKE_WORKDIR")
	}
	dir = expected
	// #nosec G703 -- dir is accepted only when it equals the fixed lifecycle fixture child under the isolated smoke root above.
	return dir, os.MkdirAll(dir, 0o700)
}

func nextObserverEpoch() (int, error) {
	dir, err := fixtureStateDir()
	if err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "epoch")
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

func writeResult(id json.RawMessage, result any) error {
	return writeMessage(map[string]any{"id": json.RawMessage(id), "result": result})
}

func writeMessage(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	header, err := fixtureFrameHeader(len(payload))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(header, payload...))
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
