package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRefusedFreshServerCreateEndsItsServerWithoutARollbackWarning drives the
// create that starts the app server and is then refused, on an isolated real
// server. `create window --project` on an offline Project starts the server
// with the session and its adopted first Window, and the requested Window is
// refused by tmux itself: the generated config's after-new-window hook fails
// the new-window after the Window exists.
//
// Rolling that back kills the session, which ends the server the create
// started, before the rollback reaches the rest of its ledger. Those targets
// ended with the server, so the rollback must finish without "rollback stopped"
// and without sending that socket another kill.
func TestRefusedFreshServerCreateEndsItsServerWithoutARollbackWarning(t *testing.T) {
	requireRealTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fx := newProjectIntentRealTmuxOn(t, ctx, strings.Join([]string{
		"set-option -g @projmux_app 1",
		// A Pane created with no command runs this instead of a shell.
		`set-option -g default-command "exec sleep 600"`,
		`set-hook -g after-new-window 'run-shell "exit 1"'`,
	}, "\n")+"\n")
	if out, err := fx.tmux("list-sessions"); err == nil {
		t.Fatalf("a server is running before the create: %q", out)
	}
	registryBefore := fx.store.snapshot()

	_, _, err := runRoute(t, fx.create, "window", "--project", "uid:"+projectIntentStoppedUID)
	if err == nil || !strings.Contains(err.Error(), "'exit 1' returned 1") {
		t.Fatalf("error = %v, want the requested Window's own tmux refusal", err)
	}
	if fx.store.writes != 0 || fx.store.snapshot() != registryBefore {
		t.Fatalf("refused create wrote the Registry: writes=%d", fx.store.writes)
	}
	if out, err := fx.tmux("list-sessions", "-F", "#{session_name}"); err == nil {
		t.Fatalf("the server the refused create started is still running: %q", out)
	}
	if warnings := fx.warnings.String(); strings.Contains(warnings, "rollback stopped") {
		t.Fatalf("rollback stopped on the server its own kill ended: %q", warnings)
	}
}
