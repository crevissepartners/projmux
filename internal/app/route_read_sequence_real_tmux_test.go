package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// TestRouteReadSequenceMatchesSeparateReadsThroughRealTmux runs the exact
// sequenced argv of one app-class proof against an isolated real tmux server
// and compares every section with the value the four separate reads give. The
// app marker is taken through each shape that matters: unset, set globally,
// set globally to the empty string, and set only on the session (a global
// read must not see it).
func TestRouteReadSequenceMatchesSeparateReadsThroughRealTmux(t *testing.T) {
	requireRealTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A unix socket path must stay short: use the /tmp root, never $TMPDIR.
	root, err := filepath.EvalSymlinks(shortTempDomain(t))
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "s")
	runner := realTmuxEnvRunner{environment: environment}
	tmux := func(args ...string) (string, error) {
		out, err := runner.Run(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	receipt, err := tmux("new-session", "-d", "-s", "route-sequence", "-P", "-F", "#{session_id}\t#{pid}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, receipt)
	}
	sessionID, serverPID, _ := strings.Cut(receipt, "\t")
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	if out, err := tmux("set-option", "-g", runtimeMutationSocketNameOption, "route-sequence"); err != nil {
		t.Fatalf("seed socket name marker: %v: %s", err, out)
	}

	routed := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}}
	reads := []routeRead{routeReadSocketPath, routeReadServerPID, routeReadAppMarker, routeReadSocketNameMarker}
	for _, test := range []struct {
		name    string
		seed    []string
		wantApp string
	}{
		{name: "unset", seed: []string{"set-option", "-gu", tmuxopts.AppGlobal}, wantApp: ""},
		{name: "global", seed: []string{"set-option", "-g", tmuxopts.AppGlobal, "1"}, wantApp: "1"},
		{name: "global empty", seed: []string{"set-option", "-g", tmuxopts.AppGlobal, ""}, wantApp: ""},
		{name: "session only", seed: []string{"set-option", "-t", sessionID, tmuxopts.AppGlobal, "1"}, wantApp: ""},
	} {
		// The cases share one server: clear both scopes before seeding one.
		_, _ = tmux("set-option", "-gu", tmuxopts.AppGlobal)
		_, _ = tmux("set-option", "-u", "-t", sessionID, tmuxopts.AppGlobal)
		if out, err := tmux(test.seed...); err != nil {
			t.Fatalf("%s: seed app marker: %v: %s", test.name, err, out)
		}
		sections, err := readTmuxSequence(ctx, routed, reads...)
		if err != nil {
			t.Fatalf("%s: read sequence: %v", test.name, err)
		}
		for i, read := range reads {
			out, err := routed.Run(ctx, "tmux", read...)
			if err != nil {
				t.Fatalf("%s: separate %q: %v", test.name, read, err)
			}
			if got, want := strings.TrimSpace(sections[i]), strings.TrimSpace(string(out)); got != want {
				t.Fatalf("%s: sequenced %q = %q, separate read = %q", test.name, read, got, want)
			}
		}
		if got := strings.TrimSpace(sections[2]); got != test.wantApp {
			t.Fatalf("%s: global app marker = %q, want %q", test.name, got, test.wantApp)
		}
		if got := strings.TrimSpace(sections[0]); got != socket {
			t.Fatalf("%s: socket path = %q, want %q", test.name, got, socket)
		}
		if got := strings.TrimSpace(sections[1]); got != serverPID {
			t.Fatalf("%s: server pid = %q, want %q", test.name, got, serverPID)
		}
		if got := strings.TrimSpace(sections[3]); got != "route-sequence" {
			t.Fatalf("%s: socket name marker = %q, want %q", test.name, got, "route-sequence")
		}
	}
}

// realTmuxEnvRunner execs tmux with an isolated environment. It returns stdout
// only, like the production runner, so a stderr warning never becomes a value.
type realTmuxEnvRunner struct {
	environment []string
}

func (r realTmuxEnvRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = r.environment
	out, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, err
	}
	return out, nil
}
