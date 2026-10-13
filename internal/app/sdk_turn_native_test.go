package app

import (
	"context"
	"encoding/json"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSDKNativeTurnDefaultTransportExactSocketAndNoReplay(t *testing.T) {
	for range 2 {
		root := shortTempDomain(t)
		socket := filepath.Join(root, "tmux.sock")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		run := func(args ...string) string {
			t.Helper()
			cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("tmux %v: %s %v", args, out, err)
			}
			return strings.TrimSpace(string(out))
		}
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Fatal("tmux required for native SDK default transport")
		}
		runtimeID := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "sdk-turn", "sleep 30")
		t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })
		if got := run("display-message", "-p", "-t", runtimeID, "#{socket_path}"); got != socket {
			t.Fatalf("socket %s", got)
		}
		_, fixture, _ := exactControlCLICommand(t)
		reg := fixture.registry.Clone()
		raw, _ := json.Marshal(reg)
		raw = []byte(strings.ReplaceAll(string(raw), "%7", runtimeID))
		if err := json.Unmarshal(raw, &reg); err != nil {
			t.Fatal(err)
		}
		for key, value := range map[string]string{"@projmux_pane_uid": "pan-alpha-codex", "@projmux_ai_thread_id": "thread-1", "@projmux_codex_authority": "provider-control-plane", "@projmux_codex_authority_epoch": "epoch-1", "@projmux_codex_authority_reason": "ready"} {
			run("set-option", "-p", "-t", runtimeID, key, value)
		}
		state := filepath.Join(root, "state")
		storage := store.NewStore(store.PathFor(state))
		if _, err := storage.Update(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
			t.Fatal(err)
		}
		id := phase6CLIIdentity()
		id.RuntimeID = runtimeID
		wire := &fakeExactControlWire{}
		epoch := newCodexControlEpoch(wire, id, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, func(codexLifecycleIdentity) bool { return true })
		server, err := startCodexControlServer(state, phase6CLIEndpoint(), epoch)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		sdk, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: state, HomeDir: root, SocketPath: socket})
		if err != nil {
			t.Fatal(err)
		}
		req := core.TurnRequest{Ref: "uid:agt-alpha-codex", Text: "-- exact input", Generation: "generation-1"}
		first, err := sdk.DeliverTurn(ctx, req)
		if err != nil || !first.Accepted || first.Mode != "start" || first.TurnID != "turn-new" {
			t.Fatalf("start %+v %v", first, err)
		}
		wire.mu.Lock()
		wire.turnID = "turn-new"
		wire.snapshot = codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-new", TurnState: codexappserver.TurnStateInProgress}
		wire.mu.Unlock()
		second, err := sdk.DeliverTurn(ctx, req)
		if err != nil || !second.Accepted || second.Mode != "steer" || second.Acceptance != "provider" || second.Delivery != "unconfirmed" {
			t.Fatalf("steer %+v %v", second, err)
		}
		_, err = sdk.StartTurn(ctx, req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnBusy {
			t.Fatalf("busy %v", err)
		}
		before := wire.writes()
		req.Generation = "stale"
		if _, err = sdk.SteerTurn(ctx, req); !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnStale {
			t.Fatal(err)
		}
		if wire.writes() != before {
			t.Fatal("stale input wrote provider bytes")
		}
		req.Generation = "generation-1"
		interrupted, err := sdk.InterruptTurn(ctx, req)
		if err != nil || !interrupted.Accepted {
			t.Fatalf("interrupt %+v %v", interrupted, err)
		}
		latest, err := storage.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		a, _ := latest.Agent("agt-alpha-codex")
		if a.Status.PaneRef != "pan-alpha-codex" || !a.Status.SessionRef.Codex.HasStartedTurn {
			t.Fatal("current identity or first turn evidence lost")
		}
		if _, err := os.Stat(filepath.Dir(server.path)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSDKClaudeInterruptExplicitCallerAndAudit(t *testing.T) {
	root := shortTempDomain(t)
	socket := filepath.Join(root, "tmux.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	runtimeID := run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "sdk-claude", "sleep 30")
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })
	_, fixture, _, _ := claudeTurnFixture(t)
	reg := fixture.registry.Clone()
	raw, _ := json.Marshal(reg)
	raw = []byte(strings.ReplaceAll(string(raw), "%7", runtimeID))
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent("agt-alpha-codex")
	a.Status.Interaction.ObservedAt = time.Now()
	run("set-option", "-p", "-t", runtimeID, "@projmux_pane_uid", "pan-alpha-codex")
	state := filepath.Join(root, "state")
	storage := store.NewStore(store.PathFor(state))
	if _, err := storage.Update(func(r *coremetadata.Registry) error { *r = reg; return nil }); err != nil {
		t.Fatal(err)
	}
	sdk, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: state, HomeDir: root, SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	req := core.TurnRequest{Ref: "uid:agt-alpha-codex"}
	_, err = sdk.InterruptTurn(ctx, req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeOperatorClientInvalid {
		t.Fatalf("implicit caller %v", err)
	}
	audit := filepath.Join(state, claudeTurnInterruptAuditName)
	if _, err = os.Stat(audit); !os.IsNotExist(err) {
		t.Fatal("missing Via wrote audit", err)
	}
	req.Via = "sdk-t5-fixture"
	out, err := sdk.InterruptTurn(ctx, req)
	if err != nil || !out.Accepted || out.Delivery != "sent" || out.Generation != "generation-1" {
		t.Fatalf("interrupt %+v %v", out, err)
	}
	entries := claudeTurnAudit(t, audit)
	if len(entries) != 2 || entries[0].Result != "requested" || entries[1].Result != "delivered" || entries[1].Via != req.Via {
		t.Fatalf("audit %+v", entries)
	}
}
