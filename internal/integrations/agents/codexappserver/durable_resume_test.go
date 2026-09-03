package codexappserver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type scriptedDurableResumeClient struct {
	threadID string
	status   string
	err      error
	closeErr error
	closed   *int
}

func (client scriptedDurableResumeClient) BootstrapThread(_ context.Context, threadID, _ string, _ []string) (ThreadSnapshot, error) {
	if client.err != nil {
		return ThreadSnapshot{}, client.err
	}
	return ThreadSnapshot{ThreadID: client.threadID, RuntimeStatus: client.status}, nil
}

func (client scriptedDurableResumeClient) Close() error {
	*client.closed++
	return client.closeErr
}

func TestDurableResumeBarrierUsesFreshIndependentExactResumeReadUntilReady(t *testing.T) {
	closed := 0
	results := []scriptedDurableResumeClient{
		{err: ErrThreadNotDurable, closed: &closed},
		{err: ErrThreadNotDurable, closed: &closed},
		{threadID: "thread-exact", status: "idle", closeErr: errors.New("owned stdio proxy reaped"), closed: &closed},
	}
	opens := 0
	var waits []time.Duration
	barrier := DurableResumeBarrier{
		Open: func(context.Context) (DurableResumeClient, error) {
			client := results[opens]
			opens++
			return client, nil
		},
		Wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
	}
	snapshot, err := barrier.Await(context.Background(), "thread-exact", "/work/project", []string{"/work/extra"})
	if err != nil || snapshot.ThreadID != "thread-exact" || snapshot.RuntimeStatus != "idle" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if opens != 3 || closed != 3 || !reflect.DeepEqual(waits, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}) {
		t.Fatalf("opens=%d closed=%d waits=%v", opens, closed, waits)
	}
}

func TestDurableResumeBarrierFailureOutcomeTableIsClosed(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome DurableResumeOutcome
	}{
		{name: "thread absence", err: ErrThreadAbsent, outcome: DurableResumeThreadAbsent},
		{name: "connection close", err: ErrDisconnected, outcome: DurableResumeConnectionClose},
		{name: "app-server restart", err: ErrEndpointChanged, outcome: DurableResumeEndpointChanged},
		{name: "protocol refusal", err: ErrProtocol, outcome: DurableResumeProtocolRefusal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closed := 0
			barrier := DurableResumeBarrier{Open: func(context.Context) (DurableResumeClient, error) {
				if test.err == ErrEndpointChanged {
					return nil, test.err
				}
				return scriptedDurableResumeClient{err: test.err, closed: &closed}, nil
			}}
			_, err := barrier.Await(context.Background(), "thread-outcome", "/work/project", nil)
			var outcome *DurableResumeError
			if !errors.As(err, &outcome) || outcome.Outcome != test.outcome || outcome.ThreadID != "thread-outcome" || outcome.Attempts != 1 {
				t.Fatalf("error=%v typed=%+v", err, outcome)
			}
			if test.err != ErrEndpointChanged && closed != 1 {
				t.Fatalf("client closes=%d, want 1", closed)
			}
		})
	}
}

func TestDurableResumeBarrierDeadlineIsBoundedAndRetriesOnlyTheExactThread(t *testing.T) {
	closed, opens := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	barrier := DurableResumeBarrier{
		Open: func(context.Context) (DurableResumeClient, error) {
			opens++
			return scriptedDurableResumeClient{err: ErrThreadNotDurable, closed: &closed}, nil
		},
		Wait: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	}
	_, err := barrier.Await(ctx, "thread-timeout", "/work/project", nil)
	var outcome *DurableResumeError
	if !errors.As(err, &outcome) || outcome.Outcome != DurableResumeTimeout || outcome.ThreadID != "thread-timeout" || outcome.Attempts != 1 || opens != 1 || closed != 1 {
		t.Fatalf("error=%v typed=%+v opens=%d closed=%d", err, outcome, opens, closed)
	}
}

func TestDurableResumeErrorRetainsNoProviderCause(t *testing.T) {
	providerCause := errors.New("secret-provider-response")
	err := NewDurableResumeError(DurableResumeProtocolRefusal, "thread-content-free", 1, providerCause)
	if errors.Is(err, providerCause) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("durable outcome retained provider cause: %v", err)
	}
}

func TestResponseErrorClassificationDropsProviderContent(t *testing.T) {
	for _, test := range []struct {
		message string
		want    error
	}{
		{message: "no rollout found for thread id secret-provider-title", want: ErrThreadNotDurable},
		{message: "thread not found: secret-provider-title", want: ErrThreadAbsent},
	} {
		err := classifyResponseError(&wireError{Code: -32600, Message: test.message})
		if !errors.Is(err, test.want) || !errors.Is(err, ErrProtocol) {
			t.Fatalf("classification=%v want=%v", err, test.want)
		}
		if got := err.Error(); got == test.message || containsProviderContent(got) {
			t.Fatalf("classified error leaked provider content: %q", got)
		}
	}
}

func containsProviderContent(value string) bool {
	for _, token := range []string{"secret-provider-title", "no rollout found", "thread not found"} {
		if len(value) >= len(token) {
			for offset := 0; offset+len(token) <= len(value); offset++ {
				if value[offset:offset+len(token)] == token {
					return true
				}
			}
		}
	}
	return false
}
