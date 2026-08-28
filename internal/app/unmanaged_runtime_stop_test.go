package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type unmanagedStopRunner struct {
	calls      [][]string
	listRows   []string
	appMarker  string
	logical    string
	socketPath string
}

func (r *unmanagedStopRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "display-message") && strings.HasSuffix(joined, "#{pid}"):
		return []byte("4242\n"), nil
	case strings.Contains(joined, "display-message"):
		return []byte(r.socketPath + "\n"), nil
	case strings.Contains(joined, "show-options -gqv @projmux_app"):
		return []byte(r.appMarker + "\n"), nil
	case strings.Contains(joined, "show-options -gqv @projmux_socket_name"):
		return []byte(r.logical + "\n"), nil
	case strings.Contains(joined, "list-sessions"):
		if len(r.listRows) == 0 {
			return nil, nil
		}
		row := r.listRows[0]
		r.listRows = r.listRows[1:]
		return []byte(row), nil
	case strings.Contains(joined, "kill-session"):
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected tmux args: %v", args)
	}
}

func (r *unmanagedStopRunner) topologyWrites() int {
	writes := 0
	for _, args := range r.calls {
		if strings.Contains(strings.Join(args, " "), "kill-session") {
			writes++
		}
	}
	return writes
}

func TestUnmanagedRuntimeStopRefusesManagedReplacementBeforeWrite(t *testing.T) {
	t.Parallel()
	sep := tmuxRowSep
	runner := &unmanagedStopRunner{
		appMarker: "1", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux",
		listRows: []string{
			strings.Join([]string{"$7", "scratch", "", "", ""}, sep) + "\n",
			strings.Join([]string{"$7", "scratch", "prj-managed", "", ""}, sep) + "\n",
		},
	}
	killed, err := executeUnmanagedRuntimeStop(context.Background(), runner, func(string) string { return "" }, "scratch", "")
	if err == nil || killed || !strings.Contains(err.Error(), "Registry-managed") {
		t.Fatalf("killed/error = %v / %v, want managed replacement refusal", killed, err)
	}
	if got := runner.topologyWrites(); got != 0 {
		t.Fatalf("managed replacement writes = %d, want zero: %#v", got, runner.calls)
	}
}

func TestUnmanagedRuntimeStopKillsExactStillUnownedHandle(t *testing.T) {
	t.Parallel()
	row := strings.Join([]string{"$7", "scratch", "", "", ""}, tmuxRowSep) + "\n"
	runner := &unmanagedStopRunner{
		appMarker: "1", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux",
		// Initial selection, executor pre-reobserve, and the immediately
		// pre-write guard must all see the same exact unowned tuple.
		listRows: []string{row, row, row},
	}
	killed, err := executeUnmanagedRuntimeStop(context.Background(), runner, func(string) string { return "" }, "scratch", "")
	if err != nil || !killed {
		t.Fatalf("killed/error = %v / %v", killed, err)
	}
	if got := runner.topologyWrites(); got != 1 {
		t.Fatalf("unowned writes = %d, want one: %#v", got, runner.calls)
	}
	foundExactKill := false
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "kill-session -t $7") {
			foundExactKill = true
		}
	}
	if !foundExactKill {
		t.Fatalf("kill did not use exact observed handle: %v", runner.calls)
	}
}

func TestUnmanagedRuntimeStopRefusesForgedAppRoute(t *testing.T) {
	t.Parallel()
	runner := &unmanagedStopRunner{appMarker: "0", logical: defaultAppSocket, socketPath: "/tmp/tmux/projmux"}
	if killed, err := executeUnmanagedRuntimeStop(context.Background(), runner, func(string) string { return "" }, "scratch", ""); err == nil || killed {
		t.Fatalf("killed/error = %v / %v, want forged route refusal", killed, err)
	}
	if got := runner.topologyWrites(); got != 0 {
		t.Fatalf("forged route writes = %d, want zero", got)
	}
}
