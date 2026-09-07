package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLifecycleDiscardedASCIIKeepsStringBoundariesAndValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, tail string
		valid      bool
	}{
		{"closing quote", `"`, true},
		{"escaped quote and slash", `\"\\\/"`, true},
		{"unicode escape", `\u0061\uD83D\uDE00"`, true},
		{"UTF8", "한😀\"", true},
		{"control", "\x01\"", false},
		{"bad escape", `\q"`, false},
		{"short unicode escape", `\u01"`, false},
		{"invalid UTF8", "\xff\"", false},
		{"incomplete UTF8", "\xe2\x82\"", false},
		{"overlong UTF8", "\xc0\xaf\"", false},
		{"missing quote", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, width := range []int{1, 7, lifecycleChunkBytes, 2 * lifecycleChunkBytes} {
				body := strings.Repeat("discarded-body-", lifecycleChunkBytes/7) + tc.tail
				raw := []byte(`{"id":1,"result":{"thread":{"id":"thread-1","status":{"type":"idle"},"turns":[]},"body":"` + body + `}}`)
				snapshot, err := projectLifecycleJSON(t.Context(), raw, 1, "thread-1", width)
				if !tc.valid {
					if !errors.Is(err, ErrProtocol) {
						t.Fatalf("width=%d: malformed string accepted: %v", width, err)
					}
					continue
				}
				if err != nil || snapshot.ThreadID != "thread-1" || snapshot.TurnCount != 0 {
					t.Fatalf("width=%d: snapshot=%+v err=%v", width, snapshot, err)
				}
				encoded, err := json.Marshal(snapshot)
				if err != nil || strings.Contains(string(encoded), "discarded-body") {
					t.Fatal("discarded body entered projected metadata")
				}
			}
		})
	}
}

type lifecycleASCIICancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *lifecycleASCIICancelContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestLifecycleDiscardedASCIIChecksCancellationWithinBoundedBufferedRun(t *testing.T) {
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &lifecycleASCIICancelContext{Context: base, cancel: cancel}
	// An input is permitted to return a larger chunk. Cancellation must still
	// be observed within one 4 KiB run, without waiting for the next read.
	buffer := []byte(`"` + strings.Repeat("a", lifecycleChunkBytes*8) + `"`)
	projector := lifecycleProjector{ctx: ctx, buffer: buffer}
	if _, err := projector.string(false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if projector.pos > lifecycleChunkBytes+1 {
		t.Fatalf("cancellation consumed %d buffered bytes", projector.pos)
	}
	if projector.retained != 0 {
		t.Fatal("discarded ASCII retained body state")
	}

	projector = lifecycleProjector{ctx: t.Context(), buffer: buffer, retained: lifecycleRetainedBytes - lifecycleRetainedReserve + 1}
	if _, err := projector.string(false); !errors.Is(err, ErrProtocol) {
		t.Fatalf("retained budget=%v", err)
	}
	if projector.pos > 1 {
		t.Fatal("ASCII scan advanced after retained budget refusal")
	}
}
