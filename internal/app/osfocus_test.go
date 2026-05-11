package app

import (
	"bytes"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/osfocus"
)

// fakeOSFocusChain stubs out the production chain so unit tests don't shell
// out to wt.exe and can assert when (and with what Target) the dispatcher
// was invoked.
type fakeOSFocusChain struct {
	mu      sync.Mutex
	calls   []osfocus.Target
	respond error
}

func (f *fakeOSFocusChain) Focus(t osfocus.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, t)
	return f.respond
}

func (f *fakeOSFocusChain) snapshot() []osfocus.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]osfocus.Target(nil), f.calls...)
}

func TestFocus_DispatchesOSFocusOnSuccess(t *testing.T) {
	t.Parallel()

	listSessions := []byte("100\tworkspace\t1\n")
	listClients := []byte("/dev/pts/0\tworkspace\n")

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return listSessions, nil
			case containsArg(args, "list-clients"):
				return listClients, nil
			}
			return nil, nil
		},
	}
	chain := &fakeOSFocusChain{}
	cmd := newFocusTestCommand(runner, nil, nil)
	cmd.osFocusChain = chain

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
	}

	calls := chain.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one os-focus dispatch, got %d (%v)", len(calls), calls)
	}
	got := calls[0]
	if got.Session != "workspace" {
		t.Errorf("dispatched Target.Session = %q, want %q", got.Session, "workspace")
	}
	if got.Window != "1" {
		t.Errorf("dispatched Target.Window = %q, want %q", got.Window, "1")
	}
	if got.Pane != "0" {
		t.Errorf("dispatched Target.Pane = %q, want %q", got.Pane, "0")
	}
}

func TestFocus_DoesNotDispatchOSFocusWhenNoClientAttached(t *testing.T) {
	t.Parallel()

	// notify-only fallback: there is no attached client, so no tmux pane was
	// actually focused. Raising the host window would land the user on the
	// wrong place — the dispatcher must stay quiet.
	listSessions := []byte("100\tworkspace\t0\n")

	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return listSessions, nil
			case containsArg(args, "list-clients"):
				return []byte(""), nil
			}
			return nil, nil
		},
	}
	notifier := &focusFakeNotifier{}
	chain := &fakeOSFocusChain{}
	cmd := newFocusTestCommand(runner, nil, notifier)
	cmd.osFocusChain = chain

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := cmd.Run([]string{"--target", "workspace:1.0"}, stdout, stderr); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if calls := chain.snapshot(); len(calls) != 0 {
		t.Fatalf("expected no os-focus dispatch on notify-only fallback, got %v", calls)
	}
}

func TestFocus_DoesNotDispatchOSFocusOnUnresolvedSession(t *testing.T) {
	t.Parallel()

	// Session never resolves → no switch-client → no os-focus dispatch.
	runner := &focusFakeRunner{
		respond: func(args []string) ([]byte, error) {
			switch {
			case containsArg(args, "list-sessions"):
				return []byte("100\tunrelated\t0\n"), nil
			case containsArg(args, "list-clients"):
				return []byte(""), nil
			}
			return nil, nil
		},
	}
	chain := &fakeOSFocusChain{}
	cmd := newFocusTestCommand(runner, nil, nil)
	cmd.osFocusChain = chain

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	_ = cmd.Run([]string{"--target", "needle:1"}, stdout, stderr)

	if calls := chain.snapshot(); len(calls) != 0 {
		t.Fatalf("expected no os-focus dispatch when session unresolved, got %v", calls)
	}
}
