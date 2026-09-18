package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
)

func TestReviewCapabilityUsesNegotiatedVersionOnly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version   string
		available bool
	}{
		{version: "codex-cli/0.148.0", available: false},
		{version: "codex-cli/0.149.0", available: true},
		{version: "0.150.0-beta.1", available: true},
		{version: "unknown", available: false},
	} {
		if got := reviewCapabilityForVersion(test.version); got.Available != test.available {
			t.Fatalf("reviewCapabilityForVersion(%q) = %#v, want available=%v", test.version, got, test.available)
		}
	}
}

func TestReviewStartWireLifecycle(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	defer client.Close()
	serverDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		var request wireRequest
		if err := json.NewDecoder(reader).Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != methodReviewStart {
			serverDone <- &unexpectedMethodError{got: request.Method, want: methodReviewStart}
			return
		}
		var params reviewStartParams
		data, _ := json.Marshal(request.Params)
		_ = json.Unmarshal(data, &params)
		if params.ThreadID != "thread-exact" {
			serverDone <- &unexpectedMethodError{got: params.ThreadID, want: "thread-exact"}
			return
		}
		_, _ = serverConn.Write([]byte(`{"id":1,"result":{"reviewThreadId":"thread-exact","turn":{"id":"turn-review","status":"inProgress","items":[]}}}` + "\n"))
		serverDone <- nil
	}()

	target, err := reviewTargetParams(corecap.ReviewTarget{Kind: corecap.ReviewUncommitted})
	if err != nil {
		t.Fatal(err)
	}
	var response reviewStartResult
	if err := client.Request(context.Background(), methodReviewStart, reviewStartParams{ThreadID: "thread-exact", Target: target}, &response); err != nil {
		t.Fatal(err)
	}
	got := corecap.ReviewResult{ThreadID: response.ReviewThreadID, TurnID: response.Turn.ID, Status: normalizeReviewStatus(response.Turn.Status)}
	want := corecap.ReviewResult{ThreadID: "thread-exact", TurnID: "turn-review", Status: corecap.ReviewInProgress}
	if got != want {
		t.Fatalf("review result = %#v, want %#v", got, want)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type unexpectedMethodError struct{ got, want string }

func (e *unexpectedMethodError) Error() string { return "got " + e.got + ", want " + e.want }
