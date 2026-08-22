package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestLifecycleDecoderProjectsOnlyClosedIdentityAndStatus(t *testing.T) {
	tests := []struct {
		name         string
		notification Notification
		want         LifecycleEvent
	}{
		{
			name: "waiting approval",
			notification: Notification{Method: "thread/status/changed", Params: json.RawMessage(
				`{"threadId":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]},"prompt":"must disappear"}`)},
			want: LifecycleEvent{Kind: LifecycleThreadStatus, ThreadID: "thread-1", ThreadState: ThreadStateWaitingOnApproval},
		},
		{
			name: "successful terminal",
			notification: Notification{Method: "turn/completed", Params: json.RawMessage(
				`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[{"type":"agentMessage","text":"private output"}]}}`)},
			want: LifecycleEvent{Kind: LifecycleTurnCompleted, ThreadID: "thread-1", TurnID: "turn-1", TurnState: TurnStateCompleted},
		},
		{
			name: "exact approval request",
			notification: Notification{Method: "item/permissions/requestApproval", RequestID: "91", Params: json.RawMessage(
				`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","cwd":"/private","reason":"private reason","permissions":{"network":{"enabled":true}}}`)},
			want: LifecycleEvent{Kind: LifecycleApprovalPending, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", RequestID: "91", ApprovalKind: ApprovalPermissions},
		},
		{
			name: "resolved numeric request",
			notification: Notification{Method: "serverRequest/resolved", Params: json.RawMessage(
				`{"threadId":"thread-1","requestId":91}`)},
			want: LifecycleEvent{Kind: LifecycleRequestResolved, ThreadID: "thread-1", RequestID: "91"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, recognized, err := DecodeLifecycleEvent(test.notification)
			if err != nil || !recognized {
				t.Fatalf("DecodeLifecycleEvent() = %#v, %t, %v", got, recognized, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("event = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestLifecycleDecoderRejectsIncompleteApprovalAndIgnoresFutureSurfaces(t *testing.T) {
	if _, recognized, err := DecodeLifecycleEvent(Notification{
		Method: "item/fileChange/requestApproval", RequestID: "req-1",
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1"}`),
	}); !recognized || err == nil {
		t.Fatalf("incomplete approval recognized=%t err=%v", recognized, err)
	}
	if got, recognized, err := DecodeLifecycleEvent(Notification{
		Method: "item/plan/delta", Params: json.RawMessage(`{"threadId":"thread-1","delta":"private"}`),
	}); recognized || err != nil || got != (LifecycleEvent{}) {
		t.Fatalf("progress event leaked into Phase 3: %#v, %t, %v", got, recognized, err)
	}
}

func TestLifecycleEventCapabilityUsesNegotiatedInitializeVersion(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "0.149.0", want: true},
		{version: "codex-cli/0.150.1", want: true},
		{version: "0.148.9"},
		{version: ""},
		{version: "private path without version"},
	} {
		client := &Client{version: test.version}
		if got := client.LifecycleEventsAvailable(); got != test.want {
			t.Fatalf("LifecycleEventsAvailable(%q) = %t, want %t", test.version, got, test.want)
		}
	}
}

func TestClientDeliversObservationOnlyServerRequest(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	go func() {
		_, _ = serverConn.Write([]byte(`{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"private"}}` + "\n"))
	}()
	select {
	case event := <-client.Notifications():
		if event.RequestID != "approval-1" || event.Method != "item/commandExecution/requestApproval" {
			t.Fatalf("server request = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("server request was not delivered")
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if line, err := bufio.NewReader(serverConn).ReadBytes('\n'); err == nil || len(line) != 0 {
		t.Fatalf("observation-only client wrote a server response: %q, %v", line, err)
	}
}

func TestReadLifecycleSnapshotKeepsOnlyLatestIdentityAndClosedState(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var request wireRequest
		_ = json.Unmarshal(line, &request)
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"active","activeFlags":["waitingOnUserInput"]},"turns":[{"id":"turn-old","status":"completed","items":[{"text":"secret"}]},{"id":"turn-current","status":"inProgress","items":[{"text":"secret current"}]}]}}}` + "\n"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := client.ReadLifecycleSnapshot(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	want := LifecycleSnapshot{ThreadID: "thread-1", ThreadState: ThreadStateWaitingOnUserInput, TurnID: "turn-current", TurnState: TurnStateInProgress}
	if got != want {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func TestReadLifecycleSnapshotRejectsIncompatibleClosedState(t *testing.T) {
	for _, test := range []struct {
		name    string
		thread  string
		wantErr bool
	}{
		{name: "empty turns remain valid", thread: `{"id":"thread-1","status":{"type":"idle"},"turns":[]}`},
		{name: "unknown thread state", thread: `{"id":"thread-1","status":{"type":"futureState"},"turns":[]}`, wantErr: true},
		{name: "missing latest turn ID", thread: `{"id":"thread-1","status":{"type":"active"},"turns":[{"status":"inProgress"}]}`, wantErr: true},
		{name: "unknown latest turn state", thread: `{"id":"thread-1","status":{"type":"active"},"turns":[{"id":"turn-1","status":"futureState"}]}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewClient(clientConn)
			t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
			go func() {
				_, _ = bufio.NewReader(serverConn).ReadBytes('\n')
				_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":` + test.thread + `}}` + "\n"))
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := client.ReadLifecycleSnapshot(ctx, "thread-1")
			if test.wantErr {
				if err == nil {
					t.Fatalf("snapshot = %#v, want protocol error", got)
				}
				return
			}
			if err != nil || got != (LifecycleSnapshot{ThreadID: "thread-1", ThreadState: ThreadStateIdle}) {
				t.Fatalf("snapshot = %#v, %v", got, err)
			}
		})
	}
}
