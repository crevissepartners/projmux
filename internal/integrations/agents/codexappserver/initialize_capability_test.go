package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

// handshakeEndpoint answers the initialize handshake and records every request
// frame it received, so a test can assert the exact initialize/request order
// and the exact params that reached the wire.
func handshakeEndpoint(t *testing.T, threadReply string) (*Client, func() []map[string]any) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	frames := make(chan map[string]any, 8)
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				close(frames)
				return
			}
			var frame map[string]any
			if json.Unmarshal(line, &frame) != nil {
				return
			}
			frames <- frame
			id, hasID := frame["id"]
			if !hasID {
				continue
			}
			var reply string
			switch frame["method"] {
			case methodInitialize:
				reply = `{"userAgent":"codex-cli/0.150.1","platformFamily":"unix","platformOs":"linux"}`
			case methodThreadStart:
				reply = threadReply
			case methodThreadResume:
				reply = threadReply
			default:
				continue
			}
			raw, _ := json.Marshal(id)
			_, _ = serverConn.Write([]byte(`{"id":` + string(raw) + `,"result":` + reply + `}` + "\n"))
		}
	}()

	return client, func() []map[string]any {
		_ = client.Close()
		var got []map[string]any
		for frame := range frames {
			got = append(got, frame)
		}
		return got
	}
}

// TestExperimentalInitializeGatesAdditionalRootsOnTheWire pins the
// initialize/request ordering ledger for the experimental-only field:
// additional writable roots reach the wire only on a connection whose
// initialize negotiated the experimental API capability, a plain initialize
// negotiates nothing, and a non-negotiated connection refuses roots with a
// typed unsupported error and zero requests.
func TestExperimentalInitializeGatesAdditionalRootsOnTheWire(t *testing.T) {
	t.Run("experimental initialize carries roots to the wire", func(t *testing.T) {
		client, collect := handshakeEndpoint(t, `{"thread":{"id":"thread-exact"}}`)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.InitializeExperimental(ctx, "0.13.0"); err != nil {
			t.Fatal(err)
		}
		if !client.ExperimentalAPI() {
			t.Fatal("experimental capability was not recorded on the connection")
		}
		if _, err := client.StartThread(ctx, "/work/project", []string{"/work/extra", " ", "/work/second"}); err != nil {
			t.Fatal(err)
		}
		frames := collect()
		if len(frames) < 3 {
			t.Fatalf("frames = %v", frames)
		}
		if frames[0]["method"] != methodInitialize || frames[1]["method"] != methodInitialized || frames[2]["method"] != methodThreadStart {
			t.Fatalf("frame order = %v, want initialize, initialized, thread/start", frames)
		}
		capabilities, _ := frames[0]["params"].(map[string]any)["capabilities"].(map[string]any)
		if capabilities["experimentalApi"] != true {
			t.Fatalf("initialize capabilities = %v", frames[0]["params"])
		}
		roots, _ := frames[2]["params"].(map[string]any)["runtimeWorkspaceRoots"].([]any)
		if !reflect.DeepEqual(roots, []any{"/work/extra", "/work/second"}) {
			t.Fatalf("thread/start roots = %v", roots)
		}
	})

	t.Run("plain initialize negotiates nothing and refuses roots", func(t *testing.T) {
		client, collect := handshakeEndpoint(t, `{"thread":{"id":"thread-exact"}}`)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.Initialize(ctx, "0.13.0"); err != nil {
			t.Fatal(err)
		}
		if client.ExperimentalAPI() {
			t.Fatal("plain initialize claimed the experimental capability")
		}
		for name, call := range map[string]func() error{
			methodThreadStart: func() error {
				_, err := client.StartThread(ctx, "/work/project", []string{"/work/extra"})
				return err
			},
			methodThreadResume: func() error {
				_, err := client.ResumeThread(ctx, "thread-exact", "/work/project", []string{"/work/extra"})
				return err
			},
		} {
			err := call()
			if !errors.Is(err, ErrExperimentalRequired) || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("%s with roots = %v, want a typed unsupported refusal", name, err)
			}
		}
		// An empty root list is always allowed and still reaches the wire.
		if _, err := client.StartThread(ctx, "/work/project", []string{" "}); err != nil {
			t.Fatal(err)
		}
		frames := collect()
		if len(frames) != 3 {
			t.Fatalf("frames = %v, want initialize, initialized, and one thread/start", frames)
		}
		if _, present := frames[0]["params"].(map[string]any)["capabilities"]; present {
			t.Fatalf("plain initialize sent capabilities: %v", frames[0]["params"])
		}
		if _, present := frames[2]["params"].(map[string]any)["runtimeWorkspaceRoots"]; present {
			t.Fatalf("empty roots reached the wire: %v", frames[2]["params"])
		}
	})
}
