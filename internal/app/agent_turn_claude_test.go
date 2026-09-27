package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type claudeTurnRunner struct {
	commands         [][]string
	uid              string
	runtime          string
	dead             bool
	failSend         bool
	beforeFirstRead  func()
	beforeSecondRead func()
	reads            int
}

func (r *claudeTurnRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	if name != "tmux" || len(args) < 3 || args[0] != "-L" || args[1] != "claude-turn-test" {
		return nil, errors.New("wrong tmux socket")
	}
	switch args[2] {
	case "list-panes":
		return []byte(r.uid + tmuxRowSepFormat + r.runtime + "\n"), nil
	case "display-message":
		r.reads++
		if r.reads == 1 && r.beforeFirstRead != nil {
			r.beforeFirstRead()
		}
		if r.reads == 2 && r.beforeSecondRead != nil {
			r.beforeSecondRead()
		}
		dead := "0"
		if r.dead {
			dead = "1"
		}
		return []byte(strings.Join([]string{r.runtime, r.uid, dead}, tmuxRowSepFormat) + "\n"), nil
	case "send-keys":
		if r.failSend {
			return nil, errors.New("tmux rejected key")
		}
		return nil, nil
	default:
		return nil, errors.New("unexpected tmux command")
	}
}

func claudeTurnFixture(t *testing.T) (*agentCommand, *fakeResourceStore, *claudeTurnRunner, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	authority := coremetadata.ClaudeAuthorityRef{
		SessionID: "session-1", RegistrationGeneration: "registration-1",
		Process:      coremetadata.ProcessIdentity{PID: 101, Start: "boot:claude"},
		LeaseProcess: coremetadata.ProcessIdentity{PID: 102, Start: "boot:helper"},
	}
	for i := range store.registry.Agents {
		if store.registry.Agents[i].Metadata.UID == "agt-alpha-codex" {
			store.registry.Agents[i].Spec.Provider = "claude"
			store.registry.Agents[i].Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionInProgress, ObservedAt: resourceFixtureClock, Source: "provider-hook"}
		}
	}
	for i := range store.registry.Panes {
		if store.registry.Panes[i].Metadata.UID == "pan-alpha-codex" {
			store.registry.Panes[i].Status.Activation = coremetadata.PaneActivation{
				AgentUID: "agt-alpha-codex", RuntimeID: "%7", Generation: "generation-1",
				Claude: &coremetadata.ClaudeActivationBinding{
					Process: authority.Process, RegistrationGeneration: authority.RegistrationGeneration,
					RegistrationSessionID: authority.SessionID,
					Registration:          &coremetadata.ClaudeRegistration{Authority: authority, Ready: true},
				},
			}
		}
	}
	cmd, _, _ := newTestAgentCommand(t, store)
	cmd.now = func() time.Time { return resourceFixtureClock.Add(time.Minute) }
	stateDir := t.TempDir()
	cmd.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: stateDir}, nil }
	cmd.controlRoute = func(context.Context) (runtimeMutationRoute, error) {
		return runtimeMutationRoute{target: tmuxTransport{Kind: tmuxSocketName, Value: "claude-turn-test", Source: tmuxSocketNameSource}}, nil
	}
	runner := &claudeTurnRunner{uid: "pan-alpha-codex", runtime: "%7"}
	cmd.controlRunner = runner
	return cmd, store, runner, filepath.Join(stateDir, claudeTurnInterruptAuditName)
}

func claudeTurnAudit(t *testing.T, path string) []claudeTurnInterruptAudit {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []claudeTurnInterruptAudit
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var entry claudeTurnInterruptAudit
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func claudeSendCount(r *claudeTurnRunner) int {
	n := 0
	for _, args := range r.commands {
		if len(args) > 2 && args[2] == "send-keys" {
			n++
		}
	}
	return n
}

func TestClaudeTurnInterruptSendsExactlyOneEscAfterAudit(t *testing.T) {
	cmd, _, runner, auditPath := claudeTurnFixture(t)
	var prewriteSeen bool
	runner.beforeSecondRead = func() {
		entries := claudeTurnAudit(t, auditPath)
		prewriteSeen = len(entries) == 1 && entries[0].Result == "requested"
	}
	out, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex", "--via", "web")
	if err != nil || !prewriteSeen || claudeSendCount(runner) != 1 || !strings.Contains(out, "agent=uid:agt-alpha-codex pane=uid:pan-alpha-codex delivery=sent") {
		t.Fatalf("out=%q err=%v prewrite=%t sends=%d", out, err, prewriteSeen, claudeSendCount(runner))
	}
	last := runner.commands[len(runner.commands)-1]
	if strings.Join(last, " ") != "-L claude-turn-test send-keys -t %7 Escape" {
		t.Fatalf("last tmux command=%v", last)
	}
	entries := claudeTurnAudit(t, auditPath)
	if len(entries) != 2 || entries[1].Result != "delivered" || entries[1].Via != "web" || entries[1].AgentUID != "agt-alpha-codex" || entries[1].PaneUID != "pan-alpha-codex" || entries[1].At.IsZero() {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestClaudeTurnInterruptRefusesUnsafeTargetsWithoutSending(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fakeResourceStore, *claudeTurnRunner)
	}{
		{"idle", func(s *fakeResourceStore, _ *claudeTurnRunner) {
			s.registry.Agents[0].Status.Interaction.Kind = coremetadata.InteractionIdle
		}},
		{"unknown", func(s *fakeResourceStore, _ *claudeTurnRunner) {
			s.registry.Agents[0].Status.Interaction.Kind = coremetadata.InteractionUnknown
		}},
		{"stale", func(s *fakeResourceStore, _ *claudeTurnRunner) {
			s.registry.Agents[0].Status.Interaction.ObservedAt = resourceFixtureClock.Add(-time.Hour)
		}},
		{"offline", func(s *fakeResourceStore, _ *claudeTurnRunner) {
			s.registry.Agents[0].Status.Phase = coremetadata.PhaseOffline
		}},
		{"owner mismatch", func(s *fakeResourceStore, _ *claudeTurnRunner) {
			s.registry.Panes[2].Status.Activation.AgentUID = "other-agent"
		}},
		{"runtime replaced", func(_ *fakeResourceStore, r *claudeTurnRunner) { r.runtime = "%8" }},
		{"dead pane", func(_ *fakeResourceStore, r *claudeTurnRunner) { r.dead = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, store, runner, _ := claudeTurnFixture(t)
			tt.change(store, runner)
			_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex", "--via", "web")
			if err == nil || claudeSendCount(runner) != 0 {
				t.Fatalf("err=%v sends=%d", err, claudeSendCount(runner))
			}
		})
	}
}

func TestClaudeTurnInterruptRequiresViaAndAuditPrewrite(t *testing.T) {
	for _, via := range [][]string{nil, {"--via", "cli"}, {"--via", "popup"}} {
		cmd, _, runner, _ := claudeTurnFixture(t)
		args := append([]string{"turn", "interrupt", "uid:agt-alpha-codex"}, via...)
		if _, _, err := runRoute(t, cmd, args...); err == nil || claudeSendCount(runner) != 0 {
			t.Fatalf("via=%v err=%v", via, err)
		}
	}
	cmd, _, runner, auditPath := claudeTurnFixture(t)
	if err := os.Mkdir(auditPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex", "--via", "web")
	if err == nil || !strings.Contains(err.Error(), "audit log") || claudeSendCount(runner) != 0 {
		t.Fatalf("err=%v sends=%d", err, claudeSendCount(runner))
	}
}

func TestClaudeTurnInterruptDeliveryAndRaceFailuresAreAudited(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*claudeTurnRunner)
	}{
		{"send failure", func(r *claudeTurnRunner) { r.failSend = true }},
		{"pane replacement", func(r *claudeTurnRunner) { r.beforeSecondRead = func() { r.runtime = "%8" } }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, _, runner, auditPath := claudeTurnFixture(t)
			tt.change(runner)
			_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex", "--via", "web")
			if err == nil {
				t.Fatal("expected explicit failure")
			}
			entries := claudeTurnAudit(t, auditPath)
			if len(entries) != 2 || entries[0].Result != "requested" || entries[1].Result != "failed" || entries[1].Reason == "" {
				t.Fatalf("audit=%+v", entries)
			}
			wantSends := 0
			if tt.name == "send failure" {
				wantSends = 1
			}
			if claudeSendCount(runner) != wantSends {
				t.Fatalf("sends=%d want=%d", claudeSendCount(runner), wantSends)
			}
		})
	}
}

func TestClaudeTurnInterruptRejectsTurnReplacementBeforeEsc(t *testing.T) {
	cmd, store, runner, auditPath := claudeTurnFixture(t)
	runner.beforeFirstRead = func() {
		for i := range store.registry.Agents {
			if store.registry.Agents[i].Metadata.UID == "agt-alpha-codex" {
				store.registry.Agents[i].Status.Interaction.ObservedAt = resourceFixtureClock.Add(30 * time.Second)
			}
		}
	}
	_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex", "--via", "web")
	if err == nil || !strings.Contains(err.Error(), "turn or Pane activation changed") || claudeSendCount(runner) != 0 {
		t.Fatalf("err=%v sends=%d", err, claudeSendCount(runner))
	}
	entries := claudeTurnAudit(t, auditPath)
	if len(entries) != 2 || entries[1].Result != "failed" {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestClaudePaneFrameRejectsAmbiguousOutput(t *testing.T) {
	for _, raw := range []string{
		"%7\\037pane\\0370\\037extra\n",
		"%7\\037pane\\0370\nother\n",
		"%7" + tmuxRowSep + "pane" + tmuxRowSepFormat + "0\n",
		"%7 pane 0\n",
	} {
		if _, err := parseClaudePaneFrame([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed frame %q", raw)
		}
	}
}
