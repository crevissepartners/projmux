package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// TestAbsentAnchorPaneAnswersBlankReceiptThroughRealTmux pins the tmux behavior
// this Epic's fix rests on, against a real server rather than a fixture.
//
// Every in-package fake used to model a Pane that is gone as a failed
// `display-message`, which is why the shipped refusal could call absence
// "containment drift" with no test disagreeing. Real tmux does not fail that
// read: given `-t %N` for a Pane it does not have, it exits 0, answers the
// server-scoped columns, and leaves `#{session_id}`, `#{window_id}` and
// `#{pane_id}` empty. If tmux ever stops doing that, this test is where it
// should be noticed, because the classification above reads exactly that row.
func TestAbsentAnchorPaneAnswersBlankReceiptThroughRealTmux(t *testing.T) {
	requireRealTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "pma-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	// The isolated TMUX_TMPDIR and the stripped inherited anchor variables are
	// the smoke-isolation rule this package's real-tmux tests all follow: no
	// call may reach the live server, and no ambient anchor may be read.
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "s")
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	receipt, err := tmux("new-session", "-d", "-s", "anchor-absence", "-P", "-F",
		"#{session_id}\t#{pid}\t#{window_id}\t#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, receipt)
	}
	fields := strings.Split(receipt, "\t")
	if len(fields) != 4 {
		t.Fatalf("isolated tmux receipt = %q", receipt)
	}
	serverPID, livePane := fields[1], fields[3]
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
	for _, option := range [][2]string{{tmuxopts.AppGlobal, "1"}, {runtimeMutationSocketNameOption, "anchor-absence"}} {
		if out, err := tmux("set-option", "-g", option[0], option[1]); err != nil {
			t.Fatalf("mark isolated tmux app-owned: %v: %s", err, out)
		}
	}

	runner := realTmuxAnchorRunner{ctx: ctx, environment: environment}
	routed := explicitTmuxRunner{
		runner: runner,
		target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource},
	}

	// The shape itself: rc=0, server columns present, object columns blank.
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", "%99999", "-F", anchorRowFormat())
	if err != nil {
		t.Fatalf("real tmux failed a read for an absent Pane, which the classification does not expect: %v", err)
	}
	rows := splitTmuxRows(string(out), 5)
	if len(rows) != 1 {
		t.Fatalf("absent-Pane read returned %d rows: %q", len(rows), out)
	}
	if rows[0][0] != socket || rows[0][1] != serverPID {
		t.Fatalf("absent-Pane read lost its server columns: %#v", rows[0])
	}
	if rows[0][2] != "" || rows[0][3] != "" || rows[0][4] != "" {
		t.Fatalf("absent-Pane read answered object columns %#v; the absence classification reads a blank triple", rows[0])
	}

	// And what both observers make of it.
	for _, observer := range []struct {
		name   string
		refuse func(string) error
	}{
		{"explicit", func(pane string) error {
			_, err := observeExplicitAppAnchorAuthority(ctx, routed, socket, serverPID, pane)
			return err
		}},
		{"inherited", func(pane string) error {
			_, err := observeInheritedRuntimeMutationAuthority(ctx, routed,
				inheritedTmuxReceipt{SocketPath: socket, ServerPID: serverPID, ClientID: "0"}, pane, runtimeMutationRouteApp)
			return err
		}},
	} {
		t.Run(observer.name, func(t *testing.T) {
			err := observer.refuse("%99999")
			if err == nil {
				t.Fatal("an absent anchor Pane proved authority")
			}
			if !strings.Contains(err.Error(), "anchor pane %99999 no longer exists") {
				t.Fatalf("real-tmux absence refusal = %q", err)
			}
			if strings.Contains(err.Error(), "containment drifted") {
				t.Fatalf("real-tmux absence is still reported as drift: %q", err)
			}
			if err := observer.refuse(livePane); err != nil {
				t.Fatalf("the Window's live anchor Pane was refused: %v", err)
			}
		})
	}
}

// realTmuxAnchorRunner runs tmux with the isolated environment above. It is the
// tmuxCommandRunner seam, so the observers under test issue exactly the argv
// they issue in production.
type realTmuxAnchorRunner struct {
	ctx         context.Context
	environment []string
}

func (r realTmuxAnchorRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, append([]string{"-f", "/dev/null"}, args...)...)
	command.Env = r.environment
	return command.Output()
}
