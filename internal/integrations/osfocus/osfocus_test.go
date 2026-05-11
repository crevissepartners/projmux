package osfocus

import (
	"errors"
	"testing"
)

// fakeAdapter is a minimal Adapter used to exercise the Chain. It records
// whether Focus was called so chain ordering and short-circuit behavior can
// be asserted.
type fakeAdapter struct {
	name     string
	detect   bool
	focusErr error
	focused  int
	lastTgt  Target
}

func (a *fakeAdapter) Name() string { return a.name }
func (a *fakeAdapter) Detect() bool { return a.detect }
func (a *fakeAdapter) Focus(t Target) error {
	a.focused++
	a.lastTgt = t
	return a.focusErr
}

func TestChain_EmptyChainReturnsNil(t *testing.T) {
	t.Parallel()

	c := NewChain()
	if err := c.Focus(Target{Session: "ws"}); err != nil {
		t.Fatalf("Focus on empty chain returned %v, want nil", err)
	}
}

func TestChain_NilReceiverReturnsNil(t *testing.T) {
	t.Parallel()

	var c *Chain
	if err := c.Focus(Target{}); err != nil {
		t.Fatalf("Focus on nil chain returned %v, want nil", err)
	}
}

func TestChain_NoAdapterDetectsReturnsNil(t *testing.T) {
	t.Parallel()

	a := &fakeAdapter{name: "a", detect: false}
	c := NewChain(a)

	if err := c.Focus(Target{Session: "ws"}); err != nil {
		t.Fatalf("Focus returned %v, want nil", err)
	}
	if a.focused != 0 {
		t.Fatalf("Focus was called %d times on non-detecting adapter; want 0", a.focused)
	}
}

func TestChain_FirstDetectingAdapterWins(t *testing.T) {
	t.Parallel()

	first := &fakeAdapter{name: "first", detect: true}
	second := &fakeAdapter{name: "second", detect: true}
	c := NewChain(first, second)

	target := Target{Session: "ws", Window: "1", Pane: "0"}
	if err := c.Focus(target); err != nil {
		t.Fatalf("Focus returned %v, want nil", err)
	}
	if first.focused != 1 {
		t.Fatalf("first.focused = %d, want 1", first.focused)
	}
	if second.focused != 0 {
		t.Fatalf("second.focused = %d, want 0 (chain should short-circuit)", second.focused)
	}
	if first.lastTgt != target {
		t.Fatalf("first.lastTgt = %#v, want %#v", first.lastTgt, target)
	}
}

func TestChain_SkipsNonDetectingAdaptersUntilFirstMatch(t *testing.T) {
	t.Parallel()

	first := &fakeAdapter{name: "first", detect: false}
	second := &fakeAdapter{name: "second", detect: true}
	third := &fakeAdapter{name: "third", detect: true}
	c := NewChain(first, second, third)

	if err := c.Focus(Target{}); err != nil {
		t.Fatalf("Focus returned %v, want nil", err)
	}
	if first.focused != 0 || third.focused != 0 {
		t.Fatalf("only the first detecting adapter should run: first=%d third=%d", first.focused, third.focused)
	}
	if second.focused != 1 {
		t.Fatalf("second.focused = %d, want 1", second.focused)
	}
}

func TestChain_AdapterErrorIsSwallowed(t *testing.T) {
	t.Parallel()

	a := &fakeAdapter{name: "a", detect: true, focusErr: errors.New("boom")}
	c := NewChain(a)

	if err := c.Focus(Target{}); err != nil {
		t.Fatalf("Focus returned %v, want nil (chain should swallow adapter errors)", err)
	}
	if a.focused != 1 {
		t.Fatalf("a.focused = %d, want 1", a.focused)
	}
}

func TestChain_NilEntriesAreSkipped(t *testing.T) {
	t.Parallel()

	a := &fakeAdapter{name: "a", detect: true}
	// NewChain takes Adapter values; a typed-nil entry must be tolerated.
	c := NewChain(nil, a, nil)

	if err := c.Focus(Target{}); err != nil {
		t.Fatalf("Focus returned %v, want nil", err)
	}
	if a.focused != 1 {
		t.Fatalf("a.focused = %d, want 1", a.focused)
	}
}
