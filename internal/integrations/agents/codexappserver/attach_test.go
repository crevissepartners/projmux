package codexappserver

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- test implementation of the RFC 6455 handshake.
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestEndpointAttachAndDaemonLifecycleAuthorityMatrix pins that endpoint
// attach authority and daemon lifecycle authority are two independent axes: a
// ready exact-current endpoint is attachable whether or not the official
// daemon manager owns it, while lifecycle authority stays with the manager and
// with the cold-start contract alone.
func TestEndpointAttachAndDaemonLifecycleAuthorityMatrix(t *testing.T) {
	for _, test := range []struct {
		name      string
		readiness EndpointReadiness
		ownership ManagerOwnership
		relation  VersionRelation
		want      EndpointAuthority
	}{
		{
			name:      "managed current attaches and keeps managed lifecycle",
			readiness: EndpointReady, ownership: ManagerManaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachAllowed, Refusal: AttachRefusalNone, Lifecycle: DaemonLifecycleAuthorityManaged},
		},
		{
			name:      "unmanaged current attaches with no lifecycle authority",
			readiness: EndpointReady, ownership: ManagerUnmanaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachAllowed, Refusal: AttachRefusalNone, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "unmanaged skew refuses attach and lifecycle",
			readiness: EndpointReady, ownership: ManagerUnmanaged, relation: VersionSkew,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalVersionSkew, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "managed skew refuses attach but keeps managed lifecycle",
			readiness: EndpointReady, ownership: ManagerManaged, relation: VersionSkew,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalVersionSkew, Lifecycle: DaemonLifecycleAuthorityManaged},
		},
		{
			name:      "unknown running version refuses attach",
			readiness: EndpointReady, ownership: ManagerUnmanaged, relation: VersionUnknown,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalRuntimeVersionUnknown, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "unknown ownership refuses attach even at an exact current version",
			readiness: EndpointReady, ownership: ManagerUnknown, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalOwnershipUnknown, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "dead endpoint keeps the cold-start contract with nothing to attach to",
			readiness: EndpointDead, ownership: ManagerUnknown, relation: VersionUnknown,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalEndpointNotReady, Lifecycle: DaemonLifecycleAuthorityColdStart},
		},
		{
			name:      "protocol error is a mismatch, not a lifecycle problem",
			readiness: EndpointProtocolError, ownership: ManagerManaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalProtocolMismatch, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "unsupported protocol is a mismatch",
			readiness: EndpointUnsupported, ownership: ManagerUnmanaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalProtocolMismatch, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "timed out endpoint is not ready",
			readiness: EndpointTimedOut, ownership: ManagerManaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalEndpointNotReady, Lifecycle: DaemonLifecycleAuthorityNone},
		},
		{
			name:      "unavailable endpoint is not ready",
			readiness: EndpointUnavailable, ownership: ManagerManaged, relation: VersionCurrent,
			want: EndpointAuthority{Attach: EndpointAttachRefused, Refusal: AttachRefusalEndpointNotReady, Lifecycle: DaemonLifecycleAuthorityNone},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			health := Health{EndpointReadiness: test.readiness, ManagerOwnership: test.ownership, VersionRelation: test.relation}
			if got := AuthorityFor(health); got != test.want {
				t.Fatalf("AuthorityFor(%+v) = %+v, want %+v", health, got, test.want)
			}
		})
	}
}

// TestEndpointAttachAuthorityLeavesNativeActionReadinessUnchanged pins that
// Phase 0 only adds an axis. The existing native-action refusal that current
// product paths consume, and every field diagnostics render, are untouched, so
// an unmanaged endpoint still refuses the current product native action.
func TestEndpointAttachAuthorityLeavesNativeActionReadinessUnchanged(t *testing.T) {
	unmanagedCurrent := withManagerObservation(
		Decide(AvailabilityAvailable, ReasonNone, "0.150.1", EndpointStdioProxy, ConnectionReady, true),
		managerObservation{Ownership: ManagerUnmanaged, Executable: RunningExecutableUnknown, Relation: VersionCurrent, CLIVersion: "0.150.1", RunningVersion: "0.150.1"},
	)
	if unmanagedCurrent.NativeAction != NativeActionRefused || unmanagedCurrent.NativeRefusal != NativeActionRefusalUnmanaged {
		t.Fatalf("native action readiness changed: %+v", unmanagedCurrent)
	}
	if authority := AuthorityFor(unmanagedCurrent); authority.Attach != EndpointAttachAllowed || authority.Lifecycle != DaemonLifecycleAuthorityNone {
		t.Fatalf("unmanaged-current authority = %+v", authority)
	}
	// The two axes disagree on purpose, and only the new one is permissive.
	fields := reflect.TypeFor[Health]()
	for i := range fields.NumField() {
		if strings.Contains(strings.ToLower(fields.Field(i).Name), "attach") {
			t.Fatalf("attach authority leaked into the rendered Health projection: %s", fields.Field(i).Name)
		}
	}
}

// TestAttachRefusesBeforeDialAndOpensExactlyOnceWhenAllowed pins the attach
// decision boundary: a refused endpoint is never dialed, and an allowed one is
// dialed exactly once with the requested capability.
func TestAttachRefusesBeforeDialAndOpensExactlyOnceWhenAllowed(t *testing.T) {
	for _, test := range []struct {
		name        string
		health      Health
		wantOpens   int
		wantRefusal AttachRefusal
	}{
		{
			name:      "unmanaged current dials once",
			health:    Health{EndpointReadiness: EndpointReady, ManagerOwnership: ManagerUnmanaged, VersionRelation: VersionCurrent},
			wantOpens: 1,
		},
		{
			name:        "skew never dials",
			health:      Health{EndpointReadiness: EndpointReady, ManagerOwnership: ManagerUnmanaged, VersionRelation: VersionSkew},
			wantRefusal: AttachRefusalVersionSkew,
		},
		{
			name:        "protocol mismatch never dials",
			health:      Health{EndpointReadiness: EndpointProtocolError, ManagerOwnership: ManagerManaged, VersionRelation: VersionCurrent},
			wantRefusal: AttachRefusalProtocolMismatch,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opens := 0
			experimental := false
			policy := attachPolicy{
				probe: func(context.Context) Health { return test.health },
				open: func(_ context.Context, wantExperimental bool) (*Client, error) {
					opens++
					experimental = wantExperimental
					return &Client{}, nil
				},
			}
			client, health, err := policy.attach(context.Background(), AttachOptions{ExperimentalAPI: true})
			if opens != test.wantOpens {
				t.Fatalf("dials = %d, want %d", opens, test.wantOpens)
			}
			if health != test.health {
				t.Fatalf("health = %+v", health)
			}
			if test.wantOpens == 0 {
				var attachErr *AttachError
				if !errors.As(err, &attachErr) || attachErr.Refusal != test.wantRefusal || client != nil {
					t.Fatalf("refusal = %v (client %v), want %s", err, client, test.wantRefusal)
				}
				if strings.Contains(attachErr.Error(), "/") {
					t.Fatalf("refusal text is not content-free: %q", attachErr.Error())
				}
				return
			}
			if err != nil || client == nil || !experimental {
				t.Fatalf("attach = %v (client %v, experimental %t)", err, client, experimental)
			}
		})
	}
}

// TestAttachToUnmanagedCurrentEndpointMutatesNoDaemonLifecycle runs the real
// attach path against a fake Codex whose daemon reports a running, exact
// current, unmanaged endpoint. Initialize, the explicit pre-turn resume, and
// the includeTurns=false read all succeed while the recorded argv ledger holds
// zero daemon start, stop, restart, kill, login, or config invocations.
func TestAttachToUnmanagedCurrentEndpointMutatesNoDaemonLifecycle(t *testing.T) {
	ledger := installFakeCodex(t, `{"status":"running","managedCodexPath":"/discarded","managedCodexVersion":"0.150.1","socketPath":"/discarded","cliVersion":"0.150.1","appServerVersion":"0.150.1"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, health, err := AttachDefaultEndpoint(ctx, "0.13.0", AttachOptions{Timeout: 5 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer client.Close()
	if health.ManagerOwnership != ManagerUnmanaged || health.VersionRelation != VersionCurrent {
		t.Fatalf("health = %+v", health)
	}
	if health.NativeAction != NativeActionRefused {
		t.Fatalf("attach widened the untouched native-action axis: %+v", health)
	}
	if !client.ExperimentalAPI() {
		t.Fatal("attach did not negotiate the requested experimental capability")
	}

	snapshot, err := client.BootstrapThread(ctx, "thread-preturn", "/work/project", []string{"/work/extra"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if snapshot.ThreadID != "thread-preturn" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	argv, methods := ledger(t)
	for _, forbidden := range []string{
		"daemon start", "daemon stop", "daemon restart", "daemon kill",
		"enable-remote-control", "disable-remote-control", "daemon bootstrap",
		"login", "logout", "config set", "config write",
	} {
		for _, line := range argv {
			if strings.Contains(line, forbidden) {
				t.Fatalf("daemon lifecycle mutation %q reached the fake Codex: %v", forbidden, argv)
			}
		}
	}
	for method, want := range map[string]int{methodThreadResume: 1, methodThreadRead: 1, methodThreadStart: 0, methodTurnStart: 0} {
		if got := methods[method]; got != want {
			t.Fatalf("%s count = %d, want %d; all=%v", method, got, want, methods)
		}
	}
}

// TestAttachRefusesVersionSkewBeforeTheAttachDial runs the same real path
// against a skewed unmanaged endpoint. The read-only probe still opens its one
// proxy, the attach dial never happens, and no thread request is sent.
func TestAttachRefusesVersionSkewBeforeTheAttachDial(t *testing.T) {
	ledger := installFakeCodex(t, `{"status":"running","managedCodexVersion":"0.150.1","cliVersion":"0.150.1","appServerVersion":"0.149.0"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, health, err := AttachDefaultEndpoint(ctx, "0.13.0", AttachOptions{Timeout: 5 * time.Second})
	var attachErr *AttachError
	if !errors.As(err, &attachErr) || attachErr.Refusal != AttachRefusalVersionSkew || client != nil {
		t.Fatalf("attach = %v (client %v), want version-skew refusal", err, client)
	}
	if health.VersionRelation != VersionSkew {
		t.Fatalf("health = %+v", health)
	}

	argv, methods := ledger(t)
	proxyOpens := 0
	for _, line := range argv {
		if strings.Contains(line, "app-server proxy") {
			proxyOpens++
		}
	}
	if proxyOpens != 1 {
		t.Fatalf("proxy opens = %d, want 1 (the read-only probe only); argv=%v", proxyOpens, argv)
	}
	for _, forbidden := range []string{methodThreadStart, methodThreadResume, methodThreadRead, methodTurnStart} {
		if methods[forbidden] != 0 {
			t.Fatalf("%s reached a skewed endpoint; all=%v", forbidden, methods)
		}
	}
}

// installFakeCodex puts a POSIX shim named codex on PATH that records every
// argv it is given, answers the read-only daemon version query with the given
// payload, and serves `app-server proxy` from the in-test helper process. The
// returned reader splits the ledger into argv lines and app-server methods.
func installFakeCodex(t *testing.T, daemonVersion string) func(*testing.T) ([]string, map[string]int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Codex executable is POSIX-only")
	}
	helper, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	// PATH holds only this shim, so the script may use shell builtins alone.
	script := "#!/bin/sh\n" +
		"printf 'argv:%s\\n' \"$*\" >> \"$PROJMUX_CODEX_ATTACH_LEDGER\"\n" +
		"if [ \"$1 $2 $3\" = \"app-server daemon version\" ]; then printf '%s\\n' \"$PROJMUX_CODEX_DAEMON_VERSION\"; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"app-server proxy\" ]; then exec \"$PROJMUX_CODEX_ATTACH_HELPER\" -test.run=TestAttachProxyHelperProcess; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(pathDir, "codex"), []byte(script), 0o700); err != nil { // #nosec G306 -- the shim must be executable.
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PROJMUX_CODEX_ATTACH_HELPER", helper)
	t.Setenv("PROJMUX_CODEX_ATTACH_LEDGER", ledgerPath)
	t.Setenv("PROJMUX_CODEX_DAEMON_VERSION", daemonVersion)
	t.Setenv("GO_WANT_ATTACH_HELPER", "1")

	return func(t *testing.T) ([]string, map[string]int) {
		t.Helper()
		data, err := os.ReadFile(ledgerPath) // #nosec G304 -- test-owned temporary ledger.
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var argv []string
		methods := map[string]int{}
		for line := range strings.SplitSeq(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "argv:"):
				argv = append(argv, strings.TrimPrefix(line, "argv:"))
			case strings.HasPrefix(line, "method:"):
				methods[strings.TrimPrefix(line, "method:")]++
			}
		}
		return argv, methods
	}
}

func TestAttachProxyHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ATTACH_HELPER") != "1" {
		return
	}
	ledgerPath := os.Getenv("PROJMUX_CODEX_ATTACH_LEDGER")
	appendEvent := func(event string) {
		file, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(20)
		}
		_, err = file.WriteString(event + "\n")
		_ = file.Close()
		if err != nil {
			os.Exit(21)
		}
	}
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
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(payload, &message) != nil || message.Method == "" {
			os.Exit(23)
		}
		appendEvent("method:" + message.Method)
		switch message.Method {
		case methodInitialize:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex-cli/0.150.1","platformFamily":"unix","platformOs":"linux"}}`, message.ID))
		case methodInitialized:
		case methodRemoteControlStatusRead:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"status":"disabled"}}`, message.ID))
		case methodThreadResume:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"thread-preturn"}}}`, message.ID))
		case methodThreadRead:
			writeTestServerFrame(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"thread-preturn","cwd":"/work/project","createdAt":1,"updatedAt":2,"status":{"type":"idle"}}}}`, message.ID))
		default:
			os.Exit(24)
		}
	}
}
