package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"reflect"
	"testing"
	"time"
)

// scriptedEndpoint serves one canned reply per request method over a pipe and
// records the exact request order and params. It answers nothing it was not
// scripted for, so an unexpected request fails the test instead of silently
// succeeding. The returned client has already negotiated the experimental API
// capability, and the handshake frames are excluded from the ledger.
func scriptedEndpoint(t *testing.T, replies map[string]string) (*Client, func() ([]string, []json.RawMessage)) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	replies = maps.Clone(replies)
	if replies == nil {
		replies = map[string]string{}
	}
	replies[methodInitialize] = `{"userAgent":"codex-cli/0.150.1","platformFamily":"unix","platformOs":"linux"}`

	methods := make(chan string, 8)
	params := make(chan json.RawMessage, 8)
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				close(methods)
				close(params)
				return
			}
			var request struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &request) != nil {
				return
			}
			methods <- request.Method
			params <- append(json.RawMessage(nil), request.Params...)
			if len(request.ID) == 0 {
				// A notification such as `initialized` is recorded and never
				// answered.
				continue
			}
			reply, ok := replies[request.Method]
			if !ok {
				_, _ = serverConn.Write([]byte(`{"id":` + string(request.ID) + `,"error":{"code":-32601,"message":"unscripted"}}` + "\n"))
				continue
			}
			_, _ = serverConn.Write([]byte(`{"id":` + string(request.ID) + `,"result":` + reply + `}` + "\n"))
		}
	}()

	handshakeCtx, cancelHandshake := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelHandshake()
	if _, err := client.InitializeExperimental(handshakeCtx, "0.13.0"); err != nil {
		t.Fatalf("scripted handshake: %v", err)
	}

	return client, func() ([]string, []json.RawMessage) {
		_ = client.Close()
		var gotMethods []string
		var gotParams []json.RawMessage
		for method := range methods {
			gotMethods = append(gotMethods, method)
		}
		for param := range params {
			gotParams = append(gotParams, param)
		}
		// Drop the two handshake frames so a ledger assertion describes only
		// the requests the test itself made.
		return gotMethods[2:], gotParams[2:]
	}
}

// TestPreTurnThreadBootstrapSubscribesThenReadsWithoutTurns pins the pre-turn
// bootstrap order and shape. Upstream refuses thread/read with turns for a
// thread whose first turn has not materialized, so bootstrap must send the
// explicit resume subscription first and then an includeTurns=false snapshot,
// and it must succeed against a fixture that has no turns at all.
func TestPreTurnThreadBootstrapSubscribesThenReadsWithoutTurns(t *testing.T) {
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: `{"thread":{"id":"thread-preturn"}}`,
		methodThreadRead:   `{"thread":{"id":"thread-preturn","cwd":"/work/project","name":"a private conversation title","createdAt":11,"updatedAt":12,"status":{"type":"idle"}}}`,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := client.BootstrapThread(ctx, "thread-preturn", "/work/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ThreadSnapshot{
		ThreadID:      "thread-preturn",
		CWD:           "/work/project",
		RuntimeStatus: "idle",
		CreatedAt:     time.Unix(11, 0).UTC(),
		UpdatedAt:     time.Unix(12, 0).UTC(),
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot = %+v, want %+v", snapshot, want)
	}

	methods, params := collect()
	if !reflect.DeepEqual(methods, []string{methodThreadResume, methodThreadRead}) {
		t.Fatalf("methods = %v, want subscription before snapshot", methods)
	}
	var resume threadResumeParams
	if err := json.Unmarshal(params[0], &resume); err != nil {
		t.Fatal(err)
	}
	if resume.ThreadID != "thread-preturn" || !resume.ExcludeTurns {
		t.Fatalf("thread/resume params = %+v, want the exact thread with turns excluded", resume)
	}
	var read threadReadParams
	if err := json.Unmarshal(params[1], &read); err != nil {
		t.Fatal(err)
	}
	if read.ThreadID != "thread-preturn" || read.IncludeTurns {
		t.Fatalf("thread/read params = %+v, want includeTurns=false", read)
	}
}

// TestPreTurnThreadBootstrapRefusesASubstitutedThread pins that neither leg of
// the bootstrap may quietly change identity, and that a failing subscription
// stops before the snapshot request is sent at all.
func TestPreTurnThreadBootstrapRefusesASubstitutedThread(t *testing.T) {
	t.Run("snapshot returns another thread", func(t *testing.T) {
		client, collect := scriptedEndpoint(t, map[string]string{
			methodThreadResume: `{"thread":{"id":"thread-preturn"}}`,
			methodThreadRead:   `{"thread":{"id":"thread-other","cwd":"/work/project","createdAt":11,"updatedAt":12,"status":{"type":"idle"}}}`,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.BootstrapThread(ctx, "thread-preturn", "/work/project", nil); !errors.Is(err, ErrProtocol) {
			t.Fatalf("substituted snapshot = %v, want a protocol refusal", err)
		}
		methods, _ := collect()
		if !reflect.DeepEqual(methods, []string{methodThreadResume, methodThreadRead}) {
			t.Fatalf("methods = %v", methods)
		}
	})

	t.Run("subscription failure stops before the snapshot", func(t *testing.T) {
		client, collect := scriptedEndpoint(t, map[string]string{})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.BootstrapThread(ctx, "thread-preturn", "/work/project", nil); err == nil {
			t.Fatal("bootstrap succeeded without a subscription")
		}
		methods, _ := collect()
		if !reflect.DeepEqual(methods, []string{methodThreadResume}) {
			t.Fatalf("methods = %v, want the snapshot never to be requested", methods)
		}
	})

	t.Run("empty thread id sends nothing", func(t *testing.T) {
		client, collect := scriptedEndpoint(t, map[string]string{})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.BootstrapThread(ctx, "  ", "/work/project", nil); !errors.Is(err, ErrProtocol) {
			t.Fatalf("empty thread id = %v", err)
		}
		if methods, _ := collect(); len(methods) != 0 {
			t.Fatalf("methods = %v, want zero requests", methods)
		}
	})
}
