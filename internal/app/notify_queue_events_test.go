package app

import (
	"testing"
	"time"
)

func TestNotifyQueueRefreshTransportPublishesToSubscriber(t *testing.T) {
	t.Parallel()

	transport := newNotifyQueueRefreshTransport(t.TempDir())
	ctx := t.Context()

	events, err := transport.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notify queue refresh event")
	}
}

func TestNotifyQueueRefreshTransportPublishWithoutSubscribersIsNoop(t *testing.T) {
	t.Parallel()

	transport := newNotifyQueueRefreshTransport(t.TempDir())
	if err := transport.Publish(); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}
