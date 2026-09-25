package codexbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// This fixture speaks the previous host's purpose gate. It is wire-level
// compatibility evidence; it does not execute a historical production binary.
func TestAuthorityProbeFallsBackOnlyBeforeLegacyHostDrain(t *testing.T) {
	for _, test := range []struct {
		name     string
		draining bool
		want     Refusal
		accepts  int
	}{
		{"legacy host before drain", false, RefusalNone, 2},
		{"legacy host during drain", true, RefusalDrainRequired, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			discovery := newRuntimeDiscovery(t)
			if err := prepareDiscoveryDir(discovery); err != nil {
				t.Fatal(err)
			}
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: discovery.SocketPath(), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := writeRecord(discovery, discoveryRecord{Protocol: ProtocolVersion, MinProtocol: MinProtocolVersion,
				Endpoint: discovery.Endpoint(), Runtime: "legacy-runtime", Credential: "legacy-credential"}); err != nil {
				t.Fatal(err)
			}
			seen := make(chan []string, 1)
			go func() {
				var purposes []string
				for range test.accepts {
					conn, err := listener.AcceptUnix()
					if err != nil {
						seen <- purposes
						return
					}
					_ = conn.SetDeadline(time.Now().Add(time.Second))
					reader := bufio.NewReader(conn)
					frame, err := readFrame(reader)
					if err != nil {
						_ = conn.Close()
						seen <- purposes
						return
					}
					var greeting hello
					if json.Unmarshal(frame, &greeting) != nil {
						_ = conn.Close()
						seen <- purposes
						return
					}
					purposes = append(purposes, greeting.Purpose)
					switch {
					case test.draining:
						_ = writeFrame(conn, wireReply{Kind: replyRefused, Refusal: RefusalDrainRequired})
					case greeting.Purpose != "":
						_ = writeFrame(conn, wireReply{Kind: replyRefused, Refusal: RefusalFrameInvalid})
					default:
						_ = writeFrame(conn, wireReply{Kind: replyWelcome, Runtime: "legacy-runtime", Protocol: ProtocolVersion})
						frame, err := readFrame(reader)
						if err == nil {
							var request wireRequest
							if json.Unmarshal(frame, &request) == nil && request.Kind == requestAuthority {
								_ = writeFrame(conn, wireReply{Kind: replyResult, ID: request.ID, Thread: request.Thread})
							}
						}
					}
					_ = conn.Close()
				}
				seen <- purposes
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = ProbeAuthority(ctx, discovery, DialConfig{Timeout: 500 * time.Millisecond}, "legacy-runtime", "thread-one", Fence{Connection: 1, Binding: 1})
			if RefusalOf(err) != test.want {
				t.Fatalf("legacy authority probe = %v, want %s", err, test.want)
			}
			select {
			case purposes := <-seen:
				if len(purposes) != test.accepts || purposes[0] != authoritySessionPurpose ||
					(test.accepts == 2 && purposes[1] != "") {
					t.Fatalf("legacy handshakes = %q", purposes)
				}
			case <-time.After(time.Second):
				t.Fatal("legacy handshake fixture did not finish")
			}
		})
	}
}
