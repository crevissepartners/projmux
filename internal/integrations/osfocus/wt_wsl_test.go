package osfocus

import (
	"errors"
	"sync"
	"testing"
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
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (r *recordedRun) run(name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
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

	rec := &recordedRun{}
	a := WindowsTerminalWSLAdapter{Run: rec.run}

	// Focus is synchronous — the runner must have been invoked by the time
	// Focus returns. (See wt_wsl.go: the previous goroutine wrap raced
	// short-lived callers like `projmux ai notify` and is removed.)
	if err := a.Focus(Target{Session: "ws", Window: "1", Pane: "0"}); err != nil {
		t.Fatalf("Focus returned error: %v", err)
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

// TestWindowsTerminalWSLAdapter_Focus_SynchronousReturnsRunnerError asserts
// the adapter surfaces the runner's error verbatim. The silent-fallback
// policy lives in Chain.Focus (see TestChain_AdapterErrorIsSwallowed), not
// at the adapter layer — the adapter just exposes the OS spawn result.
func TestWindowsTerminalWSLAdapter_Focus_SynchronousReturnsRunnerError(t *testing.T) {
	t.Parallel()

	boom := errors.New("wt.exe boom")
	rec := &recordedRun{err: boom}
	a := WindowsTerminalWSLAdapter{Run: rec.run}

	err := a.Focus(Target{})
	if !errors.Is(err, boom) {
		t.Fatalf("Focus returned %v, want runner error %v", err, boom)
	}

	// Even on error, the runner must have actually been called — i.e. the
	// adapter is synchronous so the spawn syscall has completed before
	// Focus returns. This is the property short-lived callers depend on.
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one synchronous run, got %d (%v)", len(calls), calls)
	}
	want := []string{"wt.exe", "-w", "0", "focus-tab", "-t", "0"}
	if !equalStrings(calls[0], want) {
		t.Fatalf("run args = %v, want %v", calls[0], want)
	}
}

func TestShellQuoteArgs_(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"wt.exe", "-w", "0", "ft", "-t", "0"}, `'wt.exe' '-w' '0' 'ft' '-t' '0'`},
		{[]string{"path with space"}, `'path with space'`},
		{[]string{"x'y"}, `'x'\''y'`},
	}
	for _, c := range cases {
		if got := shellQuoteArgs(c.in); got != c.want {
			t.Errorf("shellQuoteArgs(%v) = %q, want %q", c.in, got, c.want)
		}
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
