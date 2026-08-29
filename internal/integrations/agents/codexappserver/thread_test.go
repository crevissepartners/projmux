package codexappserver

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- test implementation of the RFC 6455 handshake.
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// startDefaultThreadFixture puts a fake Codex executable on PATH whose
// `app-server proxy` is this test binary's helper process, and returns the
// reader for the append-only event ledger that helper writes. Every event a
// subtest asserts on -- proxy opens, request methods, and the exact
// runtimeWorkspaceRoots one thread/start carried -- is one whitespace-free
// field in that file, so a ledger read is an exact wire observation rather
// than a summary of one.
func startDefaultThreadFixture(t *testing.T) func(path string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex executable is POSIX-only")
	}
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	fakeCodex := filepath.Join(pathDir, "codex")
	script := "#!/bin/sh\nif [ \"$1 $2 $3\" = \"app-server daemon version\" ]; then printf '%s\\n' '{\"status\":\"running\",\"backend\":\"pid\",\"managedCodexPath\":\"/discarded\",\"managedCodexVersion\":\"0.150.1\",\"socketPath\":\"/discarded\",\"cliVersion\":\"0.150.1\",\"appServerVersion\":\"0.150.1\"}'; exit 0; fi\nexec \"$PROJMUX_CODEX_THREAD_HELPER\" -test.run=TestStartDefaultThreadProxyHelperProcess\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PROJMUX_CODEX_THREAD_HELPER", helper)
	t.Setenv("GO_WANT_THREAD_HELPER", "1")
	t.Setenv("PROJMUX_CODEX_THREAD_UNSUPPORTED_ROOTS", "")

	return func(path string) []string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Fields(string(data))
	}
}

func TestStartDefaultThreadEmptyAndPromptedRequestCounts(t *testing.T) {
	readEvents := startDefaultThreadFixture(t)

	t.Run("empty falls back before proxy open", func(t *testing.T) {
		countPath := filepath.Join(t.TempDir(), "requests")
		t.Setenv("PROJMUX_CODEX_THREAD_COUNT", countPath)
		binding, err := StartDefaultThread(context.Background(), "0.13.0", "/work/project", []string{"/work/extra"}, "", "generation-empty")
		if err == nil || !CanFallback(err) || binding != (ThreadBinding{}) {
			t.Fatalf("empty binding/error = %#v / %v, safe fallback=%t", binding, err, CanFallback(err))
		}
		if events := readEvents(countPath); len(events) != 0 {
			t.Fatalf("empty prompt reached app-server: %v", events)
		}
	})

	t.Run("prompted creates one thread and turn", func(t *testing.T) {
		countPath := filepath.Join(t.TempDir(), "requests")
		t.Setenv("PROJMUX_CODEX_THREAD_COUNT", countPath)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		binding, err := StartDefaultThread(ctx, "0.13.0", "/work/project", nil, "exact prompt", "generation-prompted")
		if err != nil {
			t.Fatal(err)
		}
		if binding != (ThreadBinding{ThreadID: "thread-exact", TurnID: "turn-exact"}) {
			t.Fatalf("prompted binding = %#v", binding)
		}
		counts := map[string]int{}
		for _, event := range readEvents(countPath) {
			counts[event]++
		}
		for event, want := range map[string]int{
			"proxy/open":                  2,
			methodInitialize:              2,
			methodRemoteControlStatusRead: 1,
			methodThreadStart:             1,
			methodTurnStart:               1,
		} {
			if got := counts[event]; got != want {
				t.Fatalf("%s count = %d, want %d; all=%v", event, got, want, counts)
			}
		}
	})
}

// TestStartDefaultThreadDeliversAdditionalRootsOrFailsClosed is the additional
// writable roots capability matrix for a native create.
//
// Additional writable roots are an experimental-only request field. A create
// that carries them must deliver the exact cleaned list on a connection that
// negotiated the capability; a create against an endpoint that cannot answer
// the negotiated form must fail closed, because the alternative -- creating the
// Agent anyway -- silently narrows the writable workspace the operator asked
// for; and a create with no roots must widen nothing, keeping the plain
// connection and putting no roots field on the wire at all.
func TestStartDefaultThreadDeliversAdditionalRootsOrFailsClosed(t *testing.T) {
	readEvents := startDefaultThreadFixture(t)

	t.Run("negotiated connection carries the exact cleaned list", func(t *testing.T) {
		countPath := filepath.Join(t.TempDir(), "requests")
		t.Setenv("PROJMUX_CODEX_THREAD_COUNT", countPath)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		binding, err := StartDefaultThread(ctx, "0.13.0", "/work/project",
			[]string{"/work/extra", "  ", "/work/second"}, "exact prompt", "generation-roots-ok")
		if err != nil {
			t.Fatal(err)
		}
		if binding != (ThreadBinding{ThreadID: "thread-exact", TurnID: "turn-exact"}) {
			t.Fatalf("rooted binding = %#v", binding)
		}
		events := readEvents(countPath)
		counts := map[string]int{}
		for _, event := range events {
			counts[event]++
		}
		for event, want := range map[string]int{
			methodThreadStart: 1,
			methodTurnStart:   1,
			"thread/start:roots=/work/extra|/work/second": 1,
		} {
			if got := counts[event]; got != want {
				t.Fatalf("%s count = %d, want %d; all=%v", event, got, want, events)
			}
		}
		if got := threadConnectionNegotiation(t, events); got != "initialize:experimentalApi=true" {
			t.Fatalf("thread connection handshake = %q, want the experimental negotiation; all=%v", got, events)
		}
	})

	t.Run("an endpoint that cannot carry roots fails closed", func(t *testing.T) {
		countPath := filepath.Join(t.TempDir(), "requests")
		t.Setenv("PROJMUX_CODEX_THREAD_COUNT", countPath)
		t.Setenv("PROJMUX_CODEX_THREAD_UNSUPPORTED_ROOTS", "1")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		binding, err := StartDefaultThread(ctx, "0.13.0", "/work/project",
			[]string{"/work/extra"}, "exact prompt", "generation-roots-unsupported")
		if binding != (ThreadBinding{}) {
			t.Fatalf("fail-closed binding = %#v, want the zero binding", binding)
		}
		var action *ThreadActionError
		if !errors.As(err, &action) || action.Reason != ReasonAdditionalRootsUnsupported {
			t.Fatalf("roots error = %v, want a typed %s refusal", err, ReasonAdditionalRootsUnsupported)
		}
		if CanFallback(err) {
			t.Fatalf("unsupported roots reported a safe fallback: %v", err)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported roots error does not wrap ErrUnsupported: %v", err)
		}
		events := readEvents(countPath)
		for _, event := range events {
			if event == methodTurnStart {
				t.Fatalf("fail-closed create still submitted a turn: %v", events)
			}
		}
	})

	t.Run("an empty root list puts nothing on the wire", func(t *testing.T) {
		countPath := filepath.Join(t.TempDir(), "requests")
		t.Setenv("PROJMUX_CODEX_THREAD_COUNT", countPath)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := StartDefaultThread(ctx, "0.13.0", "/work/project",
			[]string{"", "   "}, "exact prompt", "generation-roots-empty"); err != nil {
			t.Fatal(err)
		}
		events := readEvents(countPath)
		for _, event := range events {
			if strings.HasPrefix(event, "thread/start:roots=") {
				t.Fatalf("a whitespace-only root list reached the wire: %v", events)
			}
		}
		if got := threadConnectionNegotiation(t, events); got != "initialize:experimentalApi=false" {
			t.Fatalf("rootless create handshake = %q, want the plain connection; all=%v", got, events)
		}
	})
}

// threadConnectionNegotiation reports the handshake of the connection that
// actually carried thread/start. The readiness probe opens its own connection
// first, so the ledger holds more than one initialize and only the last one
// before thread/start describes the create's own wire surface.
func threadConnectionNegotiation(t *testing.T, events []string) string {
	t.Helper()
	negotiation := ""
	for _, event := range events {
		if strings.HasPrefix(event, "initialize:experimentalApi=") {
			negotiation = event
		}
		if event == methodThreadStart {
			return negotiation
		}
	}
	t.Fatalf("event ledger holds no thread/start: %v", events)
	return ""
}

func TestStartDefaultThreadProxyHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_THREAD_HELPER") != "1" {
		return
	}
	countPath := os.Getenv("PROJMUX_CODEX_THREAD_COUNT")
	appendEvent := func(event string) {
		file, err := os.OpenFile(countPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(20)
		}
		_, err = file.WriteString(event + "\n")
		_ = file.Close()
		if err != nil {
			os.Exit(21)
		}
	}
	appendEvent("proxy/open")
	reader := bufio.NewReader(os.Stdin)
	request, err := http.ReadRequest(reader)
	if err != nil {
		os.Exit(22)
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	acceptSum := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 protocol checksum.
	accept := base64.StdEncoding.EncodeToString(acceptSum[:])
	_, _ = fmt.Fprintf(os.Stdout, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)

	for {
		payload, err := readTestClientFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var message struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				Capabilities *struct {
					ExperimentalAPI bool `json:"experimentalApi"`
				} `json:"capabilities"`
				RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots"`
			} `json:"params"`
		}
		if json.Unmarshal(payload, &message) != nil || message.Method == "" {
			os.Exit(23)
		}
		appendEvent(message.Method)
		switch message.Method {
		case methodInitialize:
			negotiated := message.Params.Capabilities != nil && message.Params.Capabilities.ExperimentalAPI
			appendEvent(fmt.Sprintf("initialize:experimentalApi=%t", negotiated))
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex-cli/0.150.1","platformFamily":"unix","platformOs":"linux"}}`, message.ID))
		case methodInitialized:
		case methodRemoteControlStatusRead:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"status":"disabled","installationId":"discarded","serverName":"discarded"}}`, message.ID))
		case methodThreadStart:
			if roots := message.Params.RuntimeWorkspaceRoots; len(roots) > 0 {
				appendEvent("thread/start:roots=" + strings.Join(roots, "|"))
				if os.Getenv("PROJMUX_CODEX_THREAD_UNSUPPORTED_ROOTS") == "1" {
					// The oldest endpoints answer an experimental-only request
					// field with the unsupported-method code rather than
					// silently ignoring it.
					writeTestServerFrame(fmt.Sprintf(`{"id":%s,"error":{"code":-32601,"message":"unsupported"}}`, message.ID))
					continue
				}
			}
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"thread-exact"}}}`, message.ID))
		case methodTurnStart:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn-exact","status":"inProgress"}}}`, message.ID))
		default:
			os.Exit(24)
		}
	}
}

func TestNativeCreateSendsOnePromptAndReturnsExactThreadTurn(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	var methods []string
	var turn turnStartParams
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(serverConn)
		for i := 1; i <= 2; i++ {
			line, _ := reader.ReadBytes('\n')
			var request wireRequest
			_ = json.Unmarshal(line, &request)
			methods = append(methods, request.Method)
			if request.Method == methodTurnStart {
				data, _ := json.Marshal(request.Params)
				_ = json.Unmarshal(data, &turn)
				_, _ = serverConn.Write([]byte(`{"id":2,"result":{"turn":{"id":"turn-exact","status":"inProgress"}}}` + "\n"))
			} else {
				_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":{"id":"thread-exact"}}}` + "\n"))
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	binding, err := client.StartThread(ctx, "/work/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := client.StartTurn(ctx, binding.ThreadID, "do exactly this", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if binding.ThreadID != "thread-exact" || turnID != "turn-exact" {
		t.Fatalf("binding = %#v turn=%q", binding, turnID)
	}
	if !reflect.DeepEqual(methods, []string{methodThreadStart, methodTurnStart}) {
		t.Fatalf("methods = %v", methods)
	}
	if turn.ThreadID != "thread-exact" || turn.ClientUserMessageID != "generation-1" ||
		len(turn.Input) != 1 || turn.Input[0] != (wireUserInput{Type: "text", Text: "do exactly this"}) {
		t.Fatalf("turn/start params = %#v", turn)
	}
}

func TestNativeResumeUsesStoredThreadAndCreatesZeroThreads(t *testing.T) {
	// thread/resume always excludes turns, and upstream requires the
	// experimental API capability for that field, so the request exists only on
	// a negotiated connection.
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: `{"thread":{"id":"thread-stored"}}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	binding, err := client.ResumeThread(ctx, "thread-stored", "/work/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	methods, rawParams := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume}) {
		t.Fatalf("methods = %v, want exactly one thread/resume and zero thread/start", methods)
	}
	var params threadResumeParams
	if err := json.Unmarshal(rawParams[0], &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "thread-stored" || !params.ExcludeTurns || binding.ThreadID != "thread-stored" {
		t.Fatalf("params=%#v binding=%#v", params, binding)
	}
}

// TestNativeResumeRefusesAnUnnegotiatedConnectionBeforeTheWire pins that the
// experimental-only excludeTurns field is never sent on a connection that did
// not negotiate the capability; the refusal is typed and sends nothing.
func TestNativeResumeRefusesAnUnnegotiatedConnectionBeforeTheWire(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	sent := make(chan struct{})
	go func() {
		if _, err := bufio.NewReader(serverConn).ReadBytes('\n'); err == nil {
			close(sent)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.ResumeThread(ctx, "thread-stored", "/work/project", nil)
	if !errors.Is(err, ErrExperimentalRequired) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unnegotiated resume = %v, want a typed unsupported refusal", err)
	}
	select {
	case <-sent:
		t.Fatal("unnegotiated resume reached the wire")
	default:
	}
}

func TestThreadActionFallbackClassificationNeverSynthesizesALane(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "unavailable before request", err: &ThreadActionError{Reason: "unavailable", SafeFallback: true}, want: true},
		{name: "unsupported thread start", err: &ThreadActionError{Reason: "unsupported", SafeFallback: true}, want: true},
		{name: "ambiguous thread start", err: &ThreadActionError{Reason: "ambiguous"}},
		{name: "turn start after thread commit", err: &ThreadActionError{Reason: "turn", err: context.DeadlineExceeded}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CanFallback(test.err); got != test.want {
				t.Fatalf("CanFallback() = %t, want %t", got, test.want)
			}
		})
	}
}
