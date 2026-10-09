package processhost

import (
	"context"
	"errors"
	"testing"
	"time"
)

// An owner asks whether the Registry still holds its generation through the
// host's own Transactions.Current, bounded and without the Handle lock, and in
// any provider state: only that answer decides.
func TestHandleCheckGenerationAsksTransactionsCurrent(t *testing.T) {
	b := Binding{Host: "h", Agent: "a", Pane: "p", Generation: "g", Operation: "o"}
	answer := error(nil)
	var asked []Binding
	var bounded bool
	p := &Handle{launch: Launch{Binding: b}, state: "stopping"}
	p.host = &Host{limits: Limits{Startup: time.Minute}, tx: Transactions{Current: func(ctx context.Context, got Binding) error {
		asked = append(asked, got)
		_, bounded = ctx.Deadline()
		if !p.mu.TryLock() {
			t.Error("Transactions.Current ran under the Handle lock")
		} else {
			p.mu.Unlock()
		}
		return answer
	}}}
	if err := p.CheckGeneration(context.Background(), b); err != nil || len(asked) != 1 || asked[0] != b || !bounded {
		t.Fatalf("current generation: err=%v asked=%v bounded=%v", err, asked, bounded)
	}
	for _, want := range []error{ErrStale, errors.New("metadata: read registry")} {
		answer = want
		if err := p.CheckGeneration(context.Background(), b); !errors.Is(err, want) {
			t.Fatalf("Current answered %v, CheckGeneration returned %v", want, err)
		}
	}
	if err := p.CheckGeneration(context.Background(), Binding{Pane: "other"}); !errors.Is(err, ErrStale) || len(asked) != 3 {
		t.Fatalf("another binding: err=%v asked=%d", err, len(asked))
	}
	codex := &CodexHandle{handle: p}
	answer = ErrStale
	if err := codex.CheckGeneration(context.Background(), b); !errors.Is(err, ErrStale) || len(asked) != 4 {
		t.Fatalf("Codex Handle: err=%v asked=%d", err, len(asked))
	}
}
