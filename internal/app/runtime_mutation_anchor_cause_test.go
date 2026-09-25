package app

import (
	"context"
	"strings"
	"testing"
)

// anchorRowFormat is the five-column containment read both anchor observers
// issue. It is spelled here so a fixture answers exactly the call the route
// makes.
func anchorRowFormat() string {
	return tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}")
}

func anchorRow(cols ...string) string {
	return strings.Join(cols, tmuxRowSepFormat) + "\n"
}

// TestRuntimeMutationAnchorRefusalNamesItsCause pins the split C-2 owes: a
// foreign socket, a replaced server generation, and an anchor Pane that no
// longer exists are three different causes and must not share one refusal.
//
// The absent-Pane row is the reason this test exists. tmux does not fail
// `display-message -t %N` for a Pane that is gone: it exits 0, fills the server
// columns, and leaves $/@/% blank. Every fixture in this package used to model
// absence as a transport error instead, which is why the shipped refusal could
// call absence "containment drifted" for four days without a red test.
func TestRuntimeMutationAnchorRefusalNamesItsCause(t *testing.T) {
	t.Parallel()
	const (
		path = "/tmp/projmux-route/app.sock"
		pid  = "4242"
		pane = "%8"
	)
	causes := []struct {
		name string
		row  string
		// want is the phrase the refusal must carry.
		want string
		// absent marks the row this Epic is about, so the assertion can also
		// state what the refusal must not say.
		absent bool
	}{
		{
			name: "anchor pane is absent",
			// The exact shape portfolio measured against real tmux on
			// 2026-09-22: rc=0, server columns present, $/@/% blank.
			row:    anchorRow(path, pid, "", "", ""),
			want:   "anchor pane " + pane + " no longer exists",
			absent: true,
		},
		{
			// Defensive only: see the comment on the socket branch in
			// observeRuntimeMutationAnchorRow.
			name: "pane answers on a foreign socket (defensive: unreachable from a shell, every caller routes to the proven socket)",
			row:  anchorRow("/tmp/projmux-route/foreign.sock", pid, "$1", "@2", pane),
			want: "answers on socket",
		},
		{
			name: "server generation was replaced",
			row:  anchorRow(path, "9999", "$1", "@2", pane),
			want: "server generation was replaced",
		},
		{
			name: "containment drifted onto another pane",
			row:  anchorRow(path, pid, "$1", "@2", "%9"),
			want: "containment drifted",
		},
	}
	scopes := []struct {
		name   string
		refuse func(*recordingTmuxRunner) error
	}{
		{
			name: "inherited",
			refuse: func(runner *recordingTmuxRunner) error {
				_, err := observeInheritedRuntimeMutationAuthority(context.Background(), runner,
					inheritedTmuxReceipt{SocketPath: path, ServerPID: pid, ClientID: "0"},
					pane, runtimeMutationRouteApp)
				return err
			},
		},
		{
			name: "explicit",
			refuse: func(runner *recordingTmuxRunner) error {
				_, err := observeExplicitAppAnchorAuthority(context.Background(), runner, path, pid, pane)
				return err
			},
		},
	}
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()
			seen := map[string]string{}
			for _, cause := range causes {
				runner := &recordingTmuxRunner{outputs: map[string]string{
					recordedTmuxCallKey("tmux", "display-message", "-p", "-t", pane, "-F", anchorRowFormat()): cause.row,
				}}
				err := scope.refuse(runner)
				if err == nil {
					t.Fatalf("%s: proven authority from a row that must refuse", cause.name)
				}
				if !strings.Contains(err.Error(), cause.want) {
					t.Fatalf("%s: refusal = %q, want it to carry %q", cause.name, err, cause.want)
				}
				if cause.absent && strings.Contains(err.Error(), "containment drifted") {
					t.Fatalf("%s: an absent anchor Pane is still reported as drift: %q", cause.name, err)
				}
				if prior, ok := seen[err.Error()]; ok {
					t.Fatalf("%s and %s share one refusal %q; C-2 requires the cause to be named",
						prior, cause.name, err)
				}
				seen[err.Error()] = cause.name
			}
		})
	}
}

// TestRuntimeMutationAnchorProvenRowStillGrantsAuthority is the positive half:
// splitting the refusals must not loosen what counts as proof. A row that
// matches the proven socket, the proven generation and the anchor's own exact
// $/@/% receipt still binds, and nothing else does.
func TestRuntimeMutationAnchorProvenRowStillGrantsAuthority(t *testing.T) {
	t.Parallel()
	const (
		path = "/tmp/projmux-route/app.sock"
		pid  = "4242"
		pane = "%8"
	)
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "display-message", "-p", "-t", pane, "-F", anchorRowFormat()): anchorRow(path, pid, "$1", "@2", pane),
	}}
	inherited, err := observeInheritedRuntimeMutationAuthority(context.Background(), runner,
		inheritedTmuxReceipt{SocketPath: path, ServerPID: pid, ClientID: "0"}, pane, runtimeMutationRouteApp)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := observeExplicitAppAnchorAuthority(context.Background(), runner, path, pid, pane)
	if err != nil {
		t.Fatal(err)
	}
	for _, authority := range []*runtimeMutationRouteAuthority{inherited, explicit} {
		if authority.ServerPID != pid || authority.SessionID != "$1" || authority.WindowID != "@2" || authority.PaneID != pane {
			t.Fatalf("proven anchor authority = %#v", authority)
		}
	}
}

// TestRuntimeMutationAnchorPartialReceiptIsStillRefused keeps the change-freeze
// boundary honest. Only a wholly blank $/@/% row is absence; a row missing one
// or two of the three is a broken receipt and is still refused, not promoted to
// "the Pane is gone" and not accepted.
func TestRuntimeMutationAnchorPartialReceiptIsStillRefused(t *testing.T) {
	t.Parallel()
	const (
		path = "/tmp/projmux-route/app.sock"
		pid  = "4242"
		pane = "%8"
	)
	for _, row := range []struct {
		name string
		cols []string
	}{
		{"no session id", []string{path, pid, "", "@2", pane}},
		{"no window id", []string{path, pid, "$1", "", pane}},
		{"no pane id", []string{path, pid, "$1", "@2", ""}},
		{"unprefixed session id", []string{path, pid, "1", "@2", pane}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingTmuxRunner{outputs: map[string]string{
				recordedTmuxCallKey("tmux", "display-message", "-p", "-t", pane, "-F", anchorRowFormat()): anchorRow(row.cols...),
			}}
			err := func() error {
				_, err := observeExplicitAppAnchorAuthority(context.Background(), runner, path, pid, pane)
				return err
			}()
			if err == nil {
				t.Fatalf("%s: partial receipt proved authority", row.name)
			}
			if !strings.Contains(err.Error(), "containment drifted") {
				t.Fatalf("%s: refusal = %q, want the containment refusal", row.name, err)
			}
		})
	}
}
