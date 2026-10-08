package processhost

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Uses the installed provider with a copied supervisor, disposable HOME and
// localhost model SSE. An idle native interrupt gives only an acknowledgement.
func TestInstalledClaudeMessageReservationExpiry(t *testing.T) {
	if os.Getenv("PROCESSHOST_TEST_CLAUDE") != "1" {
		t.Skip("opt-in installed provider qualification")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Logf("isolated provider HOME=%s", home)
	server := installedClaudeStub(t, false)
	defer server.Close()
	cmd, err := ClaudeCommand(path, home, installedClaudeEnv(home, server.URL), []string{"--model", "haiku", "--permission-mode", "manual", "--tools", "", "--setting-sources", "", "--settings", `{"hooks":{}}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence"})
	if err != nil {
		t.Fatal(err)
	}
	h := testHost(t, func(_ *Transactions, l *Limits) {
		l.Startup = 10 * time.Second
		l.MessageReservation = 150 * time.Millisecond
	})
	h.supervisor.Path = installedClaudeSupervisor(t, home)
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	defer stopResume(t, p)
	turn(t, p, "initial", "offline initial input")
	s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
	a := Authority{Binding: binding(), Connection: s.Connection, Session: s.Session}
	if err := p.ReserveClaudeMessage(context.Background(), a, "native-uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishClaudeMessage(context.Background(), a, "native-uncertain", false, true); err != nil {
		t.Fatal(err)
	}
	if err := p.Interrupt(context.Background(), a, "native-uncertain"); err != nil {
		t.Fatal(err)
	}
	s = observeUntil(t, p, func(s Snapshot) bool { return s.MessageReservation == "expired" && hasEvent(p, "interrupt-ack") })
	if s.Exit != nil || s.Turn != "native-uncertain" || hasEvent(p, "process-exited") {
		t.Fatal("expiry invented result/exit", s)
	}
	if err := p.Turn(context.Background(), a, "blocked", "offline next input"); err != ErrClaudeJoinUnsupported {
		t.Fatal("expired generation reopened", err)
	}
	stopResume(t, p)
	s, _ = p.Observe(binding())
	if s.Exit == nil || s.MessageReservation != "" {
		t.Fatal("actual Wait did not settle lifetime", s)
	}
	t.Logf("native expiry retained generation fence; Stop actual Wait exit=%+v", s.Exit)
}
