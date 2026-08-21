package codexappserver

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- test implementation of the RFC 6455 handshake.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProxyProbeFaultMatrix(t *testing.T) {
	tests := []struct {
		name         string
		scenario     string
		timeout      time.Duration
		availability Availability
		reason       Reason
		connection   ConnectionState
	}{
		{name: "healthy", scenario: "healthy", timeout: time.Second, availability: AvailabilityAvailable, reason: ReasonNone, connection: ConnectionReady},
		{name: "unsupported", scenario: "unsupported", timeout: time.Second, availability: AvailabilityUnsupported, reason: ReasonUnsupported, connection: ConnectionDisconnected},
		{name: "malformed", scenario: "malformed", timeout: time.Second, availability: AvailabilityProtocolError, reason: ReasonProtocolError, connection: ConnectionProtocolErr},
		{name: "timeout", scenario: "timeout", timeout: 30 * time.Millisecond, availability: AvailabilityTimeout, reason: ReasonTimeout, connection: ConnectionTimedOut},
		{name: "missing socket", scenario: "missing-socket", timeout: time.Second, availability: AvailabilityUnavailable, reason: ReasonEndpointUnavailable, connection: ConnectionDisconnected},
		{name: "disconnect", scenario: "disconnect", timeout: time.Second, availability: AvailabilityUnavailable, reason: ReasonEndpointUnavailable, connection: ConnectionDisconnected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			health := probeProxy(context.Background(), tc.timeout, "0.13.0", true,
				func(string) (string, error) { return os.Args[0], nil },
				func(ctx context.Context) *exec.Cmd {
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestProxyProbeHelperProcess", "--", tc.scenario)
					cmd.Env = append(os.Environ(), "GO_WANT_PROXY_HELPER=1")
					return cmd
				})
			if health.Availability != tc.availability || health.Reason != tc.reason || health.Connection != tc.connection {
				t.Fatalf("probe health = %+v", health)
			}
			if health.Source != SourceAppServer && health.Source != SourceHookFallback {
				t.Fatalf("probe source = %q", health.Source)
			}
		})
	}

	health := probeProxy(context.Background(), time.Second, "0.13.0", false,
		func(string) (string, error) { return "", errors.New("missing") },
		func(context.Context) *exec.Cmd { return nil })
	if health.Source != SourceUnavailable || health.Reason != ReasonHookUnavailable {
		t.Fatalf("missing proxy without hook = %+v", health)
	}
}

func TestProxyProbeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PROXY_HELPER") != "1" {
		return
	}
	scenario := os.Args[len(os.Args)-1]
	if scenario == "missing-socket" {
		// A proxy whose local control socket is absent exits before completing
		// the HTTP Upgrade. The client must classify this without retry loops.
		os.Exit(0)
	}
	reader := bufio.NewReader(os.Stdin)
	request, err := http.ReadRequest(reader)
	if err != nil {
		os.Exit(2)
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	acceptSum := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 protocol checksum.
	accept := base64.StdEncoding.EncodeToString(acceptSum[:])
	fmt.Fprintf(os.Stdout, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	payload, err := readTestClientFrame(reader)
	if err != nil {
		os.Exit(3)
	}
	var initialize wireRequest
	if json.Unmarshal(payload, &initialize) != nil || initialize.Method != methodInitialize {
		os.Exit(3)
	}
	switch scenario {
	case "healthy":
		writeTestServerFrame(fmt.Sprintf("{\"id\":%d,\"result\":{\"userAgent\":\"codex-cli/0.149.0\",\"platformFamily\":\"unix\",\"platformOs\":\"linux\"}}", initialize.ID))
		_, _ = readTestClientFrame(reader)
	case "unsupported":
		writeTestServerFrame(fmt.Sprintf("{\"id\":%d,\"error\":{\"code\":-32601,\"message\":\"unsupported\"}}", initialize.ID))
	case "malformed":
		writeTestServerFrame("{malformed")
	case "timeout":
		time.Sleep(10 * time.Second)
	case "disconnect":
		os.Exit(0)
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func readTestClientFrame(reader io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	if header[1]&0x80 == 0 {
		return nil, errors.New("client frame is not masked")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%len(mask)]
	}
	return payload, nil
}

func writeTestServerFrame(payload string) {
	data := []byte(payload)
	header := []byte{0x81}
	if len(data) <= 125 {
		header = append(header, byte(len(data)))
	} else {
		header = append(header, 126, byte(len(data)>>8), byte(len(data)))
	}
	_, _ = os.Stdout.Write(append(header, data...))
}
