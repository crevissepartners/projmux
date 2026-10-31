package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type observedSocketRunner struct {
	out   string
	err   error
	calls [][]string
}

func (r *observedSocketRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.out), r.err
}

func TestObservedNativeObserverSocketHandsOverTheExactPhysicalPath(t *testing.T) {
	const path = "/tmp/pmx-observer/tmux-1000/projmux"
	for _, test := range []struct {
		name, out, expected, want string
		err                       error
		fail                      bool
	}{
		{name: "exact path", out: path + "\n", want: path},
		{name: "planned path agrees", out: path, expected: path, want: path},
		{name: "logical name is refused", out: "projmux", fail: true},
		{name: "empty answer is refused", fail: true},
		{name: "unclean path is refused", out: "/tmp/pmx/../x/s", fail: true},
		{name: "planned path differs", out: path, expected: "/tmp/other/s", fail: true},
		{name: "tmux failure", out: "secret-tmux-output " + path, err: errors.New("raw stderr " + path), fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &observedSocketRunner{out: test.out, err: test.err}
			got, err := observedNativeObserverSocket(runner, "%7", test.expected)
			if test.fail {
				if err == nil || got != "" {
					t.Fatalf("socket = %q err = %v, want a refusal", got, err)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/tmp/") || strings.Contains(err.Error(), "raw stderr") {
					t.Fatalf("refusal leaks tmux output or paths: %v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("socket = %q err = %v, want %q", got, err, test.want)
			}
			if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "tmux display-message -p -t %7 -F #{socket_path}" {
				t.Fatalf("tmux calls = %v", runner.calls)
			}
		})
	}
	if _, err := observedNativeObserverSocket(nil, "%7", ""); err == nil {
		t.Fatal("a missing runner must refuse")
	}
	if _, err := observedNativeObserverSocket(&observedSocketRunner{out: path}, "", ""); err == nil {
		t.Fatal("a missing pane id must refuse")
	}
}

// The startup receipt becomes one closed reason and one fixed notice, and the
// sink gets exactly that, once. A pending or committed result is worded as
// such, never as a dead observer, and nothing of a child's own wording or
// the creator's paths reaches either.
func TestNativeObserverDiagnosticNormalizesEveryReceipt(t *testing.T) {
	for _, test := range []struct {
		name   string
		in     NativeObserverStartup
		reason string
		ok     bool
	}{
		{name: "ready", in: NativeObserverStartup{Status: "ready", Epoch: "1", Committed: true}},
		{name: "stale", in: NativeObserverStartup{Status: "stale"}},
		{name: "start failed", in: NativeObserverStartup{Status: "fallback", Reason: "observer-start-failed"}, reason: "observer-start-failed", ok: true},
		{name: "exited", in: NativeObserverStartup{Status: "fallback", Reason: "observer-exited"}, reason: "observer-exited", ok: true},
		{name: "committed fallback keeps no child wording", in: NativeObserverStartup{Status: "fallback", Reason: "/tmp/leak secret", Committed: true}, reason: "observer-reported", ok: true},
		{name: "unknown uncommitted fallback", in: NativeObserverStartup{Status: "fallback", Reason: "free text /tmp/x"}, reason: "observer-reported", ok: true},
		{name: "timeout", in: NativeObserverStartup{Status: "retrying", Reason: "observer-timeout"}, reason: "observer-timeout", ok: true},
		{name: "committed retrying", in: NativeObserverStartup{Status: "retrying", Reason: "lifecycle-retry", Committed: true}, reason: "observer-reported", ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []NativeObserverReport
			restore := SetNativeObserverReportSink(func(r NativeObserverReport) { got = append(got, r) })
			defer restore()
			id := NativeObserverIdentity{AgentUID: "agent-abc234", PaneUID: "pane-xyz567", RuntimeID: "%7", Generation: "gen-1", ThreadID: "thread-secret"}
			notices := ReportNativeObserverStartup("/state/projmux", id, test.in)
			if !test.ok {
				if notices != nil || got != nil {
					t.Fatalf("notices = %v reports = %+v, want none", notices, got)
				}
				return
			}
			if len(notices) != 1 || len(got) != 1 {
				t.Fatalf("notices = %v reports = %+v, want exactly one each", notices, got)
			}
			want := NativeObserverReport{StateDir: "/state/projmux", AgentUID: "agent-abc234", PaneUID: "pane-xyz567", Reason: test.reason}
			if got[0] != want {
				t.Fatalf("report = %+v, want %+v", got[0], want)
			}
			for _, leak := range []string{"secret", "/tmp", "thread-", "gen-1", "%7", "free text"} {
				if strings.Contains(notices[0], leak) {
					t.Fatalf("notice leaks %q: %s", leak, notices[0])
				}
			}
			if !strings.Contains(notices[0], test.reason) || !strings.Contains(notices[0], "provider hook") {
				t.Fatalf("notice does not state the closed reason and the hook authority: %s", notices[0])
			}
		})
	}
	t.Run("no sink still returns the notice", func(t *testing.T) {
		if notices := ReportNativeObserverStartup("/state", NativeObserverIdentity{}, NativeObserverStartup{Status: "fallback", Reason: "observer-exited"}); len(notices) != 1 {
			t.Fatalf("notices = %v", notices)
		}
	})
}
