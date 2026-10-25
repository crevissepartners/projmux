package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// stagedContext is a context the test ends by hand: Err is nil until the
// test calls end, and its first Done call is signaled on waiting. Done is
// read only by the slot select, after the entry Err check returned nil, so a
// test knows the call under test got past that check without sleeping.
type stagedContext struct {
	waiting  chan struct{}
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	err      error
	errAfter int
	calls    int
}

func newStagedContext(errAfter int) *stagedContext {
	return &stagedContext{waiting: make(chan struct{}), done: make(chan struct{}), errAfter: errAfter}
}

func (s *stagedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (s *stagedContext) Done() <-chan struct{} {
	s.once.Do(func() { close(s.waiting) })
	return s.done
}
func (s *stagedContext) Value(any) any { return nil }
func (s *stagedContext) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err == nil && s.errAfter > 0 && s.calls > s.errAfter {
		s.err = context.Canceled
	}
	return s.err
}
func (s *stagedContext) end(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	close(s.done)
}

func claudeHooksGateClient(t *testing.T) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Options{HomeDir: home, ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	return c, filepath.Join(home, ".claude", "settings.json")
}

// An apply waiting for another one's slot returns its context's error as soon
// as the context ends, before the slot is released, and writes nothing; once
// the slot is free an apply runs normally. A plan with nothing to change does
// not turn the cancellation into a success.
func TestApplyClaudeHooksWaitObservesContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove bool
		err    error
	}{
		{"install-canceled", false, context.Canceled},
		{"install-deadline", false, context.DeadlineExceeded},
		{"noop-remove-canceled", true, context.Canceled},
		{"noop-remove-deadline", true, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, path := claudeHooksGateClient(t)
			claudeHooksApply <- struct{}{}
			held := true
			defer func() {
				if held {
					<-claudeHooksApply
				}
			}()
			ctx := newStagedContext(0)
			result := make(chan error, 1)
			go func() {
				_, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: tc.remove})
				result <- err
			}()
			// Failure bounds, not sync points: on a timeout the slot is
			// freed so the stuck apply ends before the test fails.
			fail := func(msg string) {
				<-claudeHooksApply
				held = false
				<-result
				t.Fatal(msg)
			}
			select {
			case <-ctx.waiting:
			case <-time.After(10 * time.Second):
				fail("waiting apply never watched its context")
			}
			ctx.end(tc.err)
			var err error
			select {
			case err = <-result:
			case <-time.After(10 * time.Second):
				fail("waiting apply ignored its ended context")
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("waiting apply returned %v, want %v", err, tc.err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled apply wrote settings: %v", err)
			}
			<-claudeHooksApply
			held = false
			applied, err := c.ApplyClaudeHooks(context.Background(), core.ApplyClaudeHooksRequest{Remove: tc.remove})
			if err != nil || applied.Changed == tc.remove {
				t.Fatalf("apply after release %+v %v", applied, err)
			}
		})
	}
}

// A context that ends right after the slot is taken is seen before planning
// is acted on: a no-op removal returns the error, not a success.
func TestApplyClaudeHooksChecksContextAfterTakingTheSlot(t *testing.T) {
	c, path := claudeHooksGateClient(t)
	ctx := newStagedContext(1)
	_, err := c.ApplyClaudeHooks(ctx, core.ApplyClaudeHooksRequest{Remove: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("apply returned %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled apply wrote settings: %v", err)
	}
	select {
	case claudeHooksApply <- struct{}{}:
		<-claudeHooksApply
	default:
		t.Fatal("canceled apply kept the slot")
	}
}

// A preview whose context ends while the file is read returns the error and
// no plan.
func TestPreviewClaudeHooksChecksContextAfterReading(t *testing.T) {
	c, _ := claudeHooksGateClient(t)
	plan, err := c.PreviewClaudeHooks(newStagedContext(1), core.ClaudeHooksRequest{})
	if !errors.Is(err, context.Canceled) || plan != (core.ClaudeHooksPlan{}) {
		t.Fatalf("preview %+v %v", plan, err)
	}
}
