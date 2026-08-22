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
	binding, err := client.StartThread(ctx, "/work/project", []string{"/work/extra"})
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
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })

	var request wireRequest
	var params threadResumeParams
	done := make(chan struct{})
	go func() {
		defer close(done)
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		_ = json.Unmarshal(line, &request)
		data, _ := json.Marshal(request.Params)
		_ = json.Unmarshal(data, &params)
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"thread":{"id":"thread-stored"}}}` + "\n"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	binding, err := client.ResumeThread(ctx, "thread-stored", "/work/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if request.Method != methodThreadResume || params.ThreadID != "thread-stored" || !params.ExcludeTurns || binding.ThreadID != "thread-stored" {
		t.Fatalf("request=%#v params=%#v binding=%#v", request, params, binding)
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
