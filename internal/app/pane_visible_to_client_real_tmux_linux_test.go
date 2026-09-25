package app

import (
	"strings"
	"testing"
	"time"
)

// TestPaneVisibleToClientRealTmux asks a real tmux server, with exactly one
// attached terminal client, which Panes that client sees. Only the active Pane
// of the client's current Window is visible; a Pane in another Window of the
// same session and a Pane in another session are not. Both the attention and
// the ai auto-ack paths must agree with tmux.
func TestPaneVisibleToClientRealTmux(t *testing.T) {
	server := startRealTmuxQuestionServer(t)
	server.attach(t)

	otherWindowPane := server.paneID
	visiblePane, err := server.tmux("new-window", "-P", "-F", "#{pane_id}", "-t", "qa", "/bin/sh")
	if err != nil || !strings.HasPrefix(visiblePane, "%") {
		t.Fatalf("open a second Window: %v: %q", err, visiblePane)
	}
	otherSessionPane, err := server.tmux("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "other", "/bin/sh")
	if err != nil || !strings.HasPrefix(otherSessionPane, "%") {
		t.Fatalf("open a second session: %v: %q", err, otherSessionPane)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		viewed, err := server.tmux("list-clients", "-F", "#{client_session}\t#{pane_id}")
		if err == nil && viewed == "qa\t"+visiblePane {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("list-clients = %q (%v), want one client viewing %s in qa", viewed, err, visiblePane)
		}
	}

	attention := &attentionCommand{runner: server}
	ai := &aiCommand{readCommand: server.Run}
	cases := []struct {
		name   string
		paneID string
		want   bool
	}{
		{name: "visible pane", paneID: visiblePane, want: true},
		{name: "pane in another window", paneID: otherWindowPane, want: false},
		{name: "pane in another session", paneID: otherSessionPane, want: false},
	}
	for _, tc := range cases {
		if got := attention.paneVisibleToClient(tc.paneID); got != tc.want {
			t.Errorf("attention paneVisibleToClient(%s) [%s] = %v, want %v", tc.paneID, tc.name, got, tc.want)
		}
		if got := ai.paneVisibleToClient(tc.paneID); got != tc.want {
			t.Errorf("ai paneVisibleToClient(%s) [%s] = %v, want %v", tc.paneID, tc.name, got, tc.want)
		}
	}
}
