package osfocus

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestWindowsTerminalWSLAdapter_Name(t *testing.T) {
	t.Parallel()

	if got := (WindowsTerminalWSLAdapter{}).Name(); got != "windows-terminal-wsl" {
		t.Fatalf("Name() = %q, want %q", got, "windows-terminal-wsl")
	}
}

func TestWindowsTerminalWSLAdapter_Detect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "both present",
			env:  map[string]string{"WT_SESSION": "abc", "WSL_INTEROP": "/run/WSL/1_interop"},
			want: true,
		},
		{
			name: "only WT_SESSION",
			env:  map[string]string{"WT_SESSION": "abc"},
			want: false,
		},
		{
			name: "only WSL_INTEROP",
			env:  map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"},
			want: false,
		},
		{
			name: "neither",
			env:  map[string]string{},
			want: false,
		},
		{
			name: "both empty strings",
			env:  map[string]string{"WT_SESSION": "", "WSL_INTEROP": ""},
			want: false,
		},
		{
			name: "WT_SESSION set, WSL_INTEROP empty string",
			env:  map[string]string{"WT_SESSION": "abc", "WSL_INTEROP": ""},
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := WindowsTerminalWSLAdapter{LookupEnv: envLookup(tc.env)}
			if got := a.Detect(); got != tc.want {
				t.Fatalf("Detect() = %v, want %v", got, tc.want)
			}
		})
	}
}

// recordedRun captures the args passed to the injected runner so the test can
// assert the adapter shells out with the exact spike-measured arguments.
type recordedRun struct {
	mu     sync.Mutex
	calls  [][]string
	err    error
	delay  time.Duration
	signal chan struct{}
}

func (r *recordedRun) run(name string, args ...string) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.signal != nil {
		close(r.signal)
		r.signal = nil
	}
	return r.err
}

func (r *recordedRun) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

func TestWindowsTerminalWSLAdapter_Focus_ShellsOutWithSpikeArgs(t *testing.T) {
	t.Parallel()

	rec := &recordedRun{signal: make(chan struct{})}
	signal := rec.signal
	a := WindowsTerminalWSLAdapter{Run: rec.run}

	if err := a.Focus(Target{Session: "ws", Window: "1", Pane: "0"}); err != nil {
		t.Fatalf("Focus returned error: %v", err)
	}

	// Focus dispatches to a goroutine; wait for the runner to be called.
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner was not invoked within timeout; calls=%v", rec.snapshot())
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one run, got %d (%v)", len(calls), calls)
	}
	want := []string{"wt.exe", "-w", "0", "focus-tab", "-t", "0"}
	if !equalStrings(calls[0], want) {
		t.Fatalf("run args = %v, want %v", calls[0], want)
	}
}

func TestWindowsTerminalWSLAdapter_Focus_SilentOnRunnerError(t *testing.T) {
	t.Parallel()

	rec := &recordedRun{
		err:    errors.New("wt.exe boom"),
		signal: make(chan struct{}),
	}
	signal := rec.signal
	a := WindowsTerminalWSLAdapter{Run: rec.run}

	if err := a.Focus(Target{}); err != nil {
		t.Fatalf("Focus returned error despite runner failure: %v", err)
	}
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner was not invoked within timeout")
	}
}

func TestWindowsTerminalWSLAdapter_Focus_NonBlocking(t *testing.T) {
	t.Parallel()

	// The runner sleeps for a noticeable interval. If Focus were synchronous
	// it would block for at least that long; the non-blocking contract says
	// Focus returns well before the runner completes.
	const runnerDelay = 250 * time.Millisecond
	rec := &recordedRun{
		delay:  runnerDelay,
		signal: make(chan struct{}),
	}
	signal := rec.signal
	a := WindowsTerminalWSLAdapter{Run: rec.run}

	start := time.Now()
	if err := a.Focus(Target{}); err != nil {
		t.Fatalf("Focus returned error: %v", err)
	}
	elapsed := time.Since(start)

	// Allow generous slack for scheduling jitter on CI. The point is that
	// Focus returns far faster than the runner's own delay.
	if elapsed > runnerDelay/2 {
		t.Fatalf("Focus blocked for %v, want < %v", elapsed, runnerDelay/2)
	}

	// The runner still completes asynchronously; verify it eventually fires
	// so we know dispatch actually happened (not silently dropped).
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner was never invoked after async dispatch")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
