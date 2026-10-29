package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	registry "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// TestPublicOperatorMessageRefusals pins the public refusals of an operator
// send: invalid input before anything is read, a non-Claude target before its
// route or any write, and a Claude target's executable and route admission
// before acceptance.
func TestPublicOperatorMessageRefusals(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "not-created")
	client, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "cfg"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	web := core.NewMessageOperator("web")
	for _, req := range []OperatorMessageRequest{
		{Target: "uid:agent-target", Payload: "hi"},
		{Operator: core.NewMessageOperator("Web"), Target: "uid:agent-target", Payload: "hi"},
		{Operator: core.NewMessageOperator("1web"), Target: "uid:agent-target", Payload: "hi"},
		{Operator: core.NewMessageOperator(strings.Repeat("a", 33)), Target: "uid:agent-target", Payload: "hi"},
		{Operator: web, Payload: "hi"},
		{Operator: web, Target: "uid:agent-target", Payload: "  "},
		{Operator: web, Target: "uid:agent-target", Payload: "hi", TTL: -1},
		{Operator: web, Target: "uid:agent-target", Payload: "hi", TTL: message.MaxTTL + time.Second},
	} {
		result, err := client.SendOperatorMessage(context.Background(), req)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeMessageInvalidInput || result != (OperatorMessageResult{}) {
			t.Fatalf("%q %+v: result=%+v err=%v", req.Operator.Client(), req, result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SendOperatorMessage(ctx, OperatorMessageRequest{Operator: web, Target: "uid:agent-target", Payload: "hi"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-admission state effects: %v", err)
	}

	t.Run("codex target", func(t *testing.T) {
		client, _, uid, _ := sdkStopFixture(t)
		storage := registry.NewStore(registry.PathFor(client.paths.StateDir))
		before, err := os.ReadFile(storage.Path())
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.SendOperatorMessage(context.Background(), OperatorMessageRequest{Operator: web, Target: "uid:" + uid, Payload: "hi", MessageRef: "projmux-web-codex"})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeOperatorOriginTargetNotClaude || !errors.Is(err, ErrOperatorOriginTargetNotClaude) || result != (OperatorMessageResult{}) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		after, err := os.ReadFile(storage.Path())
		if err != nil || string(before) != string(after) {
			t.Fatalf("Registry changed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(client.paths.StateDir, "agent-messages")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("message acceptance effect: %v", err)
		}
	})

	t.Run("claude target admission", func(t *testing.T) {
		client, _, uid, paneUID := sdkStopFixture(t)
		storage := registry.NewStore(registry.PathFor(client.paths.StateDir))
		if _, err := storage.Update(func(reg *metadata.Registry) error {
			agent, _ := reg.Agent(uid)
			agent.Spec.Provider = "claude"
			pane, _ := reg.Pane(paneUID)
			pane.Status.ProcessSession.Provider = "claude"
			pane.Status.ProcessSession.SessionID = pane.Status.ProcessSession.ThreadID
			pane.Status.ProcessSession.ThreadID = ""
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(storage.Path())
		if err != nil {
			t.Fatal(err)
		}
		request := OperatorMessageRequest{Operator: web, Target: "uid:" + uid, Payload: "hi"}
		client.projmuxExecutable = filepath.Join(t.TempDir(), "missing")
		var refusal *Refusal
		if result, err := client.SendOperatorMessage(context.Background(), request); !errors.As(err, &refusal) || refusal.Code != CodeMessageInvalidInput || result != (OperatorMessageResult{}) {
			t.Fatalf("missing executable: result=%+v err=%v", result, err)
		}
		// A valid executable proceeds to the route refusal: this retired fixture
		// has no live Claude authority, and must never reach acceptance.
		if client.projmuxExecutable, err = os.Executable(); err != nil {
			t.Fatal(err)
		}
		result, err := client.SendOperatorMessage(context.Background(), request)
		if !errors.As(err, &refusal) || refusal.Code != CodeMessageRouteUnavailable || result != (OperatorMessageResult{}) ||
			!strings.HasPrefix(err.Error(), "web message send: target Agent") {
			t.Fatalf("route admission: result=%+v err=%v", result, err)
		}
		after, err := os.ReadFile(storage.Path())
		if err != nil || string(before) != string(after) {
			t.Fatalf("Registry changed before admission: %v", err)
		}
		if _, err := os.Stat(filepath.Join(client.paths.StateDir, "agent-messages")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("message acceptance effect: %v", err)
		}
	})
}
