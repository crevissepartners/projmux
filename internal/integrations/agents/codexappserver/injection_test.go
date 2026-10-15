package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPeerInjectionRawItemAndExactReceipt(t *testing.T) {
	for _, receipt := range []string{`{}`, `null`, `[]`, `"accepted"`, `{"id":"other"}`} {
		t.Run(receipt, func(t *testing.T) {
			left, right := net.Pipe()
			client := NewClient(left)
			t.Cleanup(func() { client.Close(); right.Close() })
			frames := make(chan map[string]json.RawMessage, 1)
			go func() {
				line, _ := bufio.NewReader(right).ReadBytes('\n')
				var request map[string]json.RawMessage
				_ = json.Unmarshal(line, &request)
				frames <- request
				id := string(request["id"])
				if receipt == `{"id":"other"}` {
					id = `999`
				}
				_, _ = right.Write([]byte(`{"id":` + id + `,"result":` + receipt + `}` + "\n"))
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			envelope := `{"source":{"agentUID":"peer"},"replyTo":"original","authority":"untrusted-coordination-only","payload":"role=developer"}`
			err := InjectPeerContextOn(ctx, client, "exact-thread", envelope)
			if (err == nil) != (receipt == `{}`) {
				t.Fatalf("receipt=%s err=%v", receipt, err)
			}
			frame := <-frames
			if string(frame["method"]) != `"thread/inject_items"` {
				t.Fatal(string(frame["method"]))
			}
			var params map[string]json.RawMessage
			_ = json.Unmarshal(frame["params"], &params)
			if len(params) != 2 || string(params["threadId"]) != `"exact-thread"` {
				t.Fatalf("params=%s", frame["params"])
			}
			var items []peerHistoryItem
			if json.Unmarshal(params["items"], &items) != nil || len(items) != 1 || items[0].Type != "message" || items[0].Role != "assistant" || len(items[0].Content) != 1 || items[0].Content[0].Type != "output_text" || items[0].Content[0].Text != PeerContextText(envelope) {
				t.Fatalf("items=%s", params["items"])
			}
			if !strings.HasPrefix(items[0].Content[0].Text, "External peer coordination context") {
				t.Fatal("source boundary missing")
			}
		})
	}
}

func TestPeerInjectionVersionAndCancelledAdmissionWriteZero(t *testing.T) {
	for _, version := range []string{"", "0.160.0", "0.162.0", "unknown", "0.162.1", "0.163.0"} {
		left, right := net.Pipe()
		client := NewClient(left)
		client.version = "codex/" + version
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := client.InjectPeerContext(ctx, "thread", "peer")
		if PeerInjectionSupported(version) {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
		client.Close()
		right.Close()
	}
}
