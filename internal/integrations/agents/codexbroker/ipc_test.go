package codexbroker

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TestDiscoveryCredentialAndPermissionFailuresBindNothingAndWriteNothing is the
// adversarial matrix.
//
// Every row is a way a caller can fail to prove it belongs in this state
// domain: the wrong credential, the wrong endpoint, an unshared protocol, a
// frame that is not a frame, and artifacts whose filesystem permissions or kind
// stop being proof of anything. The single assertion at the end is the one that
// matters: after all of them, the runtime holds zero bindings and the upstream
// endpoint has seen zero calls.
func TestDiscoveryCredentialAndPermissionFailuresBindNothingAndWriteNothing(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	host, endpoint := startTestHost(t, discovery, -1, ProtocolRange{Minimum: 1, Preferred: 1})
	record, err := readRecord(discovery)
	if err != nil {
		t.Fatalf("readRecord() = %v", err)
	}

	t.Run("handshake refusals", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			greeting hello
			want     Refusal
		}{
			{
				name:     "wrong credential",
				greeting: hello{Preferred: 1, Minimum: 1, Endpoint: discovery.Endpoint(), Credential: "not-the-credential"},
				want:     RefusalCredentialRejected,
			},
			{
				name:     "absent credential",
				greeting: hello{Preferred: 1, Minimum: 1, Endpoint: discovery.Endpoint()},
				want:     RefusalCredentialRejected,
			},
			{
				name:     "foreign endpoint",
				greeting: hello{Preferred: 1, Minimum: 1, Endpoint: "codex-app-server:other", Credential: record.Credential},
				want:     RefusalEndpointMismatch,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				reply := rawHandshake(t, discovery, test.greeting)
				if reply.Kind != replyRefused || reply.Refusal != test.want {
					t.Fatalf("handshake reply = %+v, want a %s refusal", reply, test.want)
				}
			})
		}
	})

	t.Run("unshared protocol drains its own runtime", func(t *testing.T) {
		// This row gets a runtime of its own: an unshared protocol window is
		// the one refusal that also starts a drain, and a drain with no binding
		// held shuts the runtime down immediately by design.
		lone := newRuntimeDiscovery(t)
		startTestHost(t, lone, -1, ProtocolRange{Minimum: 1, Preferred: 1})
		loneRecord, err := readRecord(lone)
		if err != nil {
			t.Fatalf("readRecord() = %v", err)
		}
		reply := rawHandshake(t, lone, hello{
			Preferred: 9, Minimum: 9, Endpoint: lone.Endpoint(), Credential: loneRecord.Credential,
		})
		if reply.Kind != replyRefused || reply.Refusal != RefusalDrainRequired {
			t.Fatalf("unshared protocol reply = %+v, want a drain-required refusal", reply)
		}
	})

	t.Run("malformed first frame", func(t *testing.T) {
		conn, err := net.DialTimeout("unix", discovery.SocketPath(), time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("this is not a frame\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		reply := readReply(t, conn)
		if reply.Kind != replyRefused || reply.Refusal != RefusalFrameInvalid {
			t.Fatalf("malformed handshake reply = %+v", reply)
		}
	})

	t.Run("untrusted artifacts", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			corrupt func(t *testing.T)
			restore func(t *testing.T)
			want    Refusal
		}{
			{
				name: "record readable beyond its owner",
				corrupt: func(t *testing.T) {
					// #nosec G302 -- the over-permissive mode is the fault under test.
					if err := os.Chmod(discovery.RecordPath(), 0o644); err != nil {
						t.Fatal(err)
					}
				},
				restore: func(t *testing.T) {
					if err := os.Chmod(discovery.RecordPath(), 0o600); err != nil {
						t.Fatal(err)
					}
				},
				want: RefusalDiscoveryUntrusted,
			},
			{
				name: "record replaced by a symlink",
				corrupt: func(t *testing.T) {
					moved := discovery.RecordPath() + ".moved"
					if err := os.Rename(discovery.RecordPath(), moved); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(moved, discovery.RecordPath()); err != nil {
						t.Fatal(err)
					}
				},
				restore: func(t *testing.T) {
					if err := os.Remove(discovery.RecordPath()); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(discovery.RecordPath()+".moved", discovery.RecordPath()); err != nil {
						t.Fatal(err)
					}
				},
				want: RefusalDiscoveryUntrusted,
			},
			{
				name: "record naming a foreign endpoint",
				corrupt: func(t *testing.T) {
					forged := record
					forged.Endpoint = "codex-app-server:other"
					if err := writeRecord(discovery, forged); err != nil {
						t.Fatal(err)
					}
				},
				restore: func(t *testing.T) {
					if err := writeRecord(discovery, record); err != nil {
						t.Fatal(err)
					}
				},
				want: RefusalDiscoveryUntrusted,
			},
			{
				name: "no record published",
				corrupt: func(t *testing.T) {
					if err := os.Rename(discovery.RecordPath(), discovery.RecordPath()+".moved"); err != nil {
						t.Fatal(err)
					}
				},
				restore: func(t *testing.T) {
					if err := os.Rename(discovery.RecordPath()+".moved", discovery.RecordPath()); err != nil {
						t.Fatal(err)
					}
				},
				want: RefusalHostUnavailable,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				test.corrupt(t)
				defer test.restore(t)
				if _, err := Dial(t.Context(), discovery, DialConfig{}); RefusalOf(err) != test.want {
					t.Fatalf("Dial(%s) = %v, want %s", test.name, err, test.want)
				}
			})
		}
	})

	t.Run("state domain must be absolute", func(t *testing.T) {
		for _, domain := range []string{"", "   ", "relative/state"} {
			if _, err := NewDiscovery(domain, DefaultEndpointKey); RefusalOf(err) != RefusalDomainRequired {
				t.Fatalf("NewDiscovery(%q) = %v, want domain-required", domain, err)
			}
		}
		generationKey, err := NewEndpointKey("private-domain", "private-generation")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewDiscovery("/tmp", "codex-app-server:other"); RefusalOf(err) != RefusalEndpointUnknown {
			t.Fatalf("NewDiscovery(unknown) = %v, want endpoint-unknown", err)
		}
		generationDiscovery, err := NewDiscovery("/tmp", generationKey)
		if err != nil || generationDiscovery.Endpoint() != generationKey {
			t.Fatalf("NewDiscovery(generation) = %+v/%v, want exact generation endpoint", generationDiscovery, err)
		}
	})

	// Nothing above proved it belonged here, so nothing above may have reached
	// the endpoint or taken a binding.
	if stats := host.Stats(); stats.Bindings != 0 {
		t.Fatalf("Stats().Bindings = %d after the refusal matrix", stats.Bindings)
	}
	if methods := endpoint.methods(); len(methods) != 0 {
		t.Fatalf("upstream requests = %v, want none", methods)
	}
	if snapshots := endpoint.bootstrapped(); len(snapshots) != 0 {
		t.Fatalf("upstream snapshots = %v, want none", snapshots)
	}
	if refusals := host.Stats().Refused; refusals == 0 {
		t.Fatal("the runtime counted no refusals")
	}
}

// TestRuntimeStateAndDiagnosticsRetainNoProviderContent is the state-directory
// privacy audit.
//
// A prompt-bearing mutation and a payload-bearing event are driven all the way
// across the socket, and then every byte the runtime persisted in its state
// domain is searched for them. The runtime forwards provider payloads and keeps
// none, so the only things on disk are a closed record and a credential.
func TestRuntimeStateAndDiagnosticsRetainNoProviderContent(t *testing.T) {
	const secret = "a private prompt body"
	discovery := newRuntimeDiscovery(t)
	host, endpoint := startTestHost(t, discovery, -1, ProtocolRange{})
	conn := dialTestClient(t, discovery, ProtocolRange{})
	binding, fence := boundRemote(t, conn, "thread-one")

	if outcome, err := binding.Submit(t.Context(), fence, Mutation{
		Method: "turn/start", Params: map[string]string{"text": secret},
	}); err != nil || outcome != MutationApplied {
		t.Fatalf("Submit() = %s, %v", outcome, err)
	}
	endpoint.push(codexappserver.Notification{
		Method: "item/updated",
		Params: json.RawMessage(`{"threadId":"thread-one","text":` + strconv.Quote(secret) + `}`),
	})
	if event := nextRemoteEvent(t, binding); !strings.Contains(string(event.Params), secret) {
		t.Fatalf("the payload did not reach the client verbatim: %+v", event)
	}

	for _, path := range domainFiles(t, discovery.Domain()) {
		payload, err := os.ReadFile(path) // #nosec G304 -- test-owned state domain.
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(payload), secret) {
			t.Fatalf("%s retained provider content", filepath.Base(path))
		}
	}

	// The runtime's own projection is a closed set of tokens and counters, so
	// it is safe to log and persist without redaction.
	statsType := reflect.TypeFor[HostStats]()
	var fields []string
	for index := range statsType.NumField() {
		fields = append(fields, statsType.Field(index).Name)
	}
	want := []string{"Endpoint", "Protocol", "Sessions", "LiveSessions", "Bindings", "Refused", "Draining"}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("HostStats fields = %v, want the closed set %v", fields, want)
	}
	rendered := strings.Join([]string{
		string(host.Stats().Endpoint),
		refuse(RefusalDrainRequired, nil).Error(),
		refuse(RefusalCredentialRejected, nil).Error(),
	}, " ")
	if strings.Contains(rendered, secret) || strings.Contains(rendered, discovery.SocketPath()) {
		t.Fatalf("a rendered runtime surface leaked a payload or a path: %s", rendered)
	}
}

// TestRuntimeProtocolNegotiationAndFrameBoundsAreClosed is the protocol unit.
//
// Negotiation is the newest version both windows contain, and an empty
// intersection is a refusal rather than a downgrade. The frame bound is checked
// on both directions, because a frame that is refused only on read would still
// have been written.
func TestRuntimeProtocolNegotiationAndFrameBoundsAreClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		client  ProtocolRange
		host    ProtocolRange
		version int
		ok      bool
	}{
		{name: "identical", client: ProtocolRange{1, 1}, host: ProtocolRange{1, 1}, version: 1, ok: true},
		{name: "new host, old client", client: ProtocolRange{1, 1}, host: ProtocolRange{2, 1}, version: 1, ok: true},
		{name: "old host, new client", client: ProtocolRange{2, 1}, host: ProtocolRange{1, 1}, version: 1, ok: true},
		{name: "both new", client: ProtocolRange{2, 2}, host: ProtocolRange{2, 1}, version: 2, ok: true},
		{name: "client dropped the old version", client: ProtocolRange{2, 2}, host: ProtocolRange{1, 1}, ok: false},
		{name: "host dropped the old version", client: ProtocolRange{1, 1}, host: ProtocolRange{2, 2}, ok: false},
		{name: "zero means this build", client: ProtocolRange{}, host: ProtocolRange{}, version: ProtocolVersion, ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			version, ok := negotiate(test.client, test.host)
			if ok != test.ok || (ok && version != test.version) {
				t.Fatalf("negotiate(%+v, %+v) = %d, %v; want %d, %v",
					test.client, test.host, version, ok, test.version, test.ok)
			}
		})
	}

	oversized := wireRequest{Kind: requestSubmit, Params: json.RawMessage(strconv.Quote(strings.Repeat("x", maxFrameBytes)))}
	var sink strings.Builder
	if err := writeFrame(&sink, oversized); RefusalOf(err) != RefusalFrameInvalid {
		t.Fatalf("writeFrame(oversized) = %v, want frame-invalid", err)
	}
	if sink.Len() != 0 {
		t.Fatal("an oversized frame was partially written")
	}
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("y", maxFrameBytes+8)+"\n"), frameBufferBytes)
	if _, err := readFrame(reader); RefusalOf(err) != RefusalFrameInvalid {
		t.Fatalf("readFrame(oversized) = %v, want frame-invalid", err)
	}
}

// rawHandshake sends one hand-built greeting and returns the runtime's answer.
func rawHandshake(t *testing.T, discovery Discovery, greeting hello) wireReply {
	t.Helper()
	conn, err := net.DialTimeout("unix", discovery.SocketPath(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := writeFrame(conn, greeting); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	return readReply(t, conn)
}

// readReply reads one frame from a raw connection.
func readReply(t *testing.T, conn net.Conn) wireReply {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := readFrame(bufio.NewReaderSize(conn, frameBufferBytes))
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var reply wireReply
	if err := json.Unmarshal(frame, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	return reply
}
