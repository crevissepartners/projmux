package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// TestServerRequestResponseIsOncePerConnectionAcrossRawScalarKinds pins the
// broker-facing half of the approval contract: an inbound server request keeps
// its byte-faithful raw identity, a string id and a numeric id that render the
// same text remain two distinct requests, and the response authority for one
// request is consumed exactly once per connection.
func TestServerRequestResponseIsOncePerConnectionAcrossRawScalarKinds(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	var mu sync.Mutex
	var written [][]byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			mu.Lock()
			written = append(written, append([]byte(nil), line...))
			mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The server sends both a numeric and a string request whose ids render as
	// the same text. Each must be answerable exactly once.
	for _, raw := range []string{`7`, `"7"`} {
		if err := client.RespondServerRequest(ctx, json.RawMessage(raw), struct {
			Decision string `json:"decision"`
		}{"accept"}); err != nil {
			t.Fatalf("first response for %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`7`, `"7"`} {
		err := client.RespondServerRequest(ctx, json.RawMessage(raw), struct {
			Decision string `json:"decision"`
		}{"decline"})
		if !errors.Is(err, ErrResponseAlreadySent) {
			t.Fatalf("duplicate response for %s = %v, want ErrResponseAlreadySent", raw, err)
		}
	}

	// A distinct id is unaffected by another request's consumed authority.
	if err := client.RespondServerRequest(ctx, json.RawMessage(`8`), struct{}{}); err != nil {
		t.Fatalf("independent response: %v", err)
	}

	_ = client.Close()
	<-done

	mu.Lock()
	frames := append([][]byte(nil), written...)
	mu.Unlock()
	if len(frames) != 3 {
		t.Fatalf("wire frames = %d, want 3 (one per answered request); frames=%s", len(frames), frames)
	}
	wantIDs := []string{`7`, `"7"`, `8`}
	for i, frame := range frames {
		var response struct {
			ID     json.RawMessage   `json:"id"`
			Result map[string]string `json:"result"`
		}
		if err := json.Unmarshal(frame, &response); err != nil {
			t.Fatal(err)
		}
		if string(response.ID) != wantIDs[i] {
			t.Fatalf("frame %d id = %s, want %s", i, response.ID, wantIDs[i])
		}
		if i < 2 && response.Result["decision"] != "accept" {
			t.Fatalf("frame %d kept the refused duplicate decision: %v", i, response.Result)
		}
	}
}

// TestServerRequestResponseAuthorityDiesWithTheConnection pins that a
// disconnected or replaced connection owns no pending response authority: the
// answer is refused before the wire instead of being written to a connection
// the server request never arrived on.
func TestServerRequestResponseAuthorityDiesWithTheConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	_ = serverConn.Close()
	_ = client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := client.RespondServerRequest(ctx, json.RawMessage(`9`), struct{}{})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("response on a closed connection = %v, want ErrDisconnected", err)
	}

	// A fresh connection is a fresh response ledger: the same id is answerable
	// again because it is a different connection's request.
	replacementClient, replacementServer := net.Pipe()
	replacement := NewClient(replacementClient)
	t.Cleanup(func() { _ = replacement.Close(); _ = replacementServer.Close() })
	line := make(chan []byte, 1)
	go func() { got, _ := bufio.NewReader(replacementServer).ReadBytes('\n'); line <- got }()
	if err := replacement.RespondServerRequest(ctx, json.RawMessage(`9`), struct{}{}); err != nil {
		t.Fatalf("response on the replacement connection: %v", err)
	}
	var response struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(<-line, &response); err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `9` {
		t.Fatalf("replacement response id = %s", response.ID)
	}
}

// TestServerRequestRawIdentityIsPreservedFromFrameToResponse pins the whole
// path: the raw id bytes the server sent survive routing into the notification
// and come back on the response byte for byte.
func TestServerRequestRawIdentityIsPreservedFromFrameToResponse(t *testing.T) {
	for _, raw := range []string{`31`, `"opaque-31"`} {
		t.Run(raw, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewClient(clientConn)
			t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
			line := make(chan []byte, 1)
			go func() {
				reader := bufio.NewReader(serverConn)
				_, _ = serverConn.Write([]byte(`{"method":"item/commandExecution/requestApproval","id":` + raw + `,"params":{}}` + "\n"))
				got, _ := reader.ReadBytes('\n')
				line <- got
			}()
			event := <-client.Notifications()
			if string(event.RawRequestID) != raw {
				t.Fatalf("raw request id = %s, want %s", event.RawRequestID, raw)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.RespondServerRequest(ctx, event.RawRequestID, struct{}{}); err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(<-line, &response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != raw {
				t.Fatalf("response id = %s, want %s", response.ID, raw)
			}
			if err := client.RespondServerRequest(ctx, event.RawRequestID, struct{}{}); !errors.Is(err, ErrResponseAlreadySent) {
				t.Fatalf("second response = %v, want ErrResponseAlreadySent", err)
			}
		})
	}
}
