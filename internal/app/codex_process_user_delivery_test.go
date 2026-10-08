package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexProcessUserDeliveryOwnedStartSteerAndFences(t *testing.T) {
	f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		fixture := strings.Replace(processCodexProviderFixture, " elif method=='turn/interrupt':", " elif method=='turn/steer':reply({'turnId':current})\n elif method=='turn/interrupt':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", fixture}, Dir: root, Env: env}
	})
	e := f.endpoint
	identity, err := localipc.InspectOwnedSocket(e.socket)
	if err != nil {
		t.Fatal(err)
	}
	base := processForegroundRequest{Authority: e.authority(), Action: "user-deliver", Operation: "input-1", Prompt: "hold"}
	exchange := func(r processForegroundRequest) processForegroundResult {
		t.Helper()
		result, err := callProcessForeground(context.Background(), e.socket, identity, e.evidence.HostProcess, codexProcessExchange{Foreground: &r})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	wires := func() int { return len(f.pollWire(t)) }
	before := wires()
	for _, field := range []string{"host", "project", "window", "agent", "pane", "generation", "operation", "connection", "session"} {
		bad := base
		switch field {
		case "host":
			bad.Authority.Binding.Host = "old"
		case "project":
			bad.Authority.Binding.Project = "old"
		case "window":
			bad.Authority.Binding.Window = "old"
		case "agent":
			bad.Authority.Binding.Agent = "old"
		case "pane":
			bad.Authority.Binding.Pane = "old"
		case "generation":
			bad.Authority.Binding.Generation = "old"
		case "operation":
			bad.Authority.Binding.Operation = "old"
		case "connection":
			bad.Authority.Connection = "old"
		case "session":
			bad.Authority.Session = "old"
		}
		if r := exchange(bad); !r.Stale || r.Accepted || r.UserDelivery != nil {
			t.Fatalf("%s admitted: %+v", field, r)
		}
	}
	if wires() != before {
		t.Fatal("stale tuple wrote provider bytes")
	}
	badHost := e.evidence.HostProcess
	badHost.Start += "-old"
	for _, test := range []struct {
		host     coremetadata.ProcessIdentity
		identity localipc.SocketIdentity
	}{
		{badHost, identity}, {e.evidence.HostProcess, localipc.SocketIdentity{}},
	} {
		_, err := callProcessForeground(context.Background(), e.socket, test.identity, test.host, codexProcessExchange{Foreground: &base})
		if !errors.Is(err, processhost.ErrStale) {
			t.Fatalf("kernel/socket fence: %v", err)
		}
	}
	if wires() != before {
		t.Fatal("kernel/socket refusal wrote provider bytes")
	}
	first, err := processUserDeliveryAcceptance(exchange(base), base.Operation)
	if err != nil || first.Mode != processhost.UserTurnStart || first.TurnID == "" {
		t.Fatalf("start=%+v %v", first, err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == first.TurnID })
	explicit := base
	explicit.Action = "turn"
	explicit.Operation = "explicit"
	if r := exchange(explicit); !r.Busy || r.Accepted || r.UserDelivery != nil {
		t.Fatalf("explicit busy regression: %+v", r)
	}
	base.Operation = "input-2"
	base.Prompt = "more"
	second, err := processUserDeliveryAcceptance(exchange(base), base.Operation)
	if err != nil || second.Mode != processhost.UserTurnSteer || second.TurnID != first.TurnID {
		t.Fatalf("steer=%+v %v", second, err)
	}
	before = wires()
	if r := exchange(base); !r.Stale || r.Accepted || r.UserDelivery != nil {
		t.Fatalf("replay admitted: %+v", r)
	}
	if wires() != before {
		t.Fatal("replay wrote provider bytes")
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	starts, steers := 0, 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var n struct {
			Method string
			Params struct {
				ExpectedTurnID string `json:"expectedTurnId"`
			}
		}
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatal(err)
		}
		if n.Method == "turn/start" {
			starts++
		}
		if n.Method == "turn/steer" {
			steers++
			if n.Params.ExpectedTurnID != first.TurnID {
				t.Fatalf("wrong exact turn: %s", line)
			}
		}
	}
	if starts != 1 || steers != 1 {
		t.Fatalf("start=%d steer=%d", starts, steers)
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(e.binding.Agent)
	c := &agentCommand{controlPaths: func() (config.Paths, error) {
		return config.Paths{StateDir: filepath.Join(f.root, "state", "projmux")}, nil
	}}
	got, err := c.callProcessUserDelivery(reg, *agent, "caller")
	if err != nil || got.Mode != processhost.UserTurnSteer || got.TurnID != first.TurnID || got.Operation == "" {
		t.Fatalf("typed caller: %+v %v", got, err)
	}
	before = wires()
	_, err = f.store.Update(func(r *coremetadata.Registry) error {
		p, _ := r.Pane(e.binding.Pane)
		p.Status.Activation.Generation = "replaced"
		p.Status.Activation.Process.Binding.Generation = "replaced"
		p.Status.ProcessSession.Binding.Generation = "replaced"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	base.Operation = "stale-current"
	if r := exchange(base); !r.Stale || r.Accepted || r.UserDelivery != nil {
		t.Fatalf("old endpoint admitted: %+v", r)
	}
	if wires() != before {
		t.Fatal("old endpoint wrote provider bytes")
	}
}

func TestCodexProcessUserDeliveryLostSteerDoesNotReplay(t *testing.T) {
	f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		fixture := strings.Replace(processCodexProviderFixture, " elif method=='turn/interrupt':", " elif method=='turn/steer':sys.exit(0)\n elif method=='turn/interrupt':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", fixture}, Dir: root, Env: env}
	})
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(f.endpoint.binding.Agent)
	c := &agentCommand{controlPaths: func() (config.Paths, error) {
		return config.Paths{StateDir: filepath.Join(f.root, "state", "projmux")}, nil
	}}
	first, err := c.callProcessUserDelivery(reg, *agent, "hold")
	if err != nil || first.Mode != processhost.UserTurnStart {
		t.Fatalf("start: %+v %v", first, err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == first.TurnID })
	got, err := c.callProcessUserDelivery(reg, *agent, "lost-steer")
	if err == nil || got != (processhost.UserTurnDelivery{}) {
		t.Fatalf("lost acceptance fabricated: %+v %v", got, err)
	}
	starts, steers := 0, 0
	for _, n := range f.pollWire(t) {
		if string(n["method"]) == `"turn/start"` {
			starts++
		}
		if string(n["method"]) == `"turn/steer"` {
			steers++
		}
	}
	if starts != 1 || steers != 1 {
		t.Fatalf("lost steer retried/fell back: starts=%d steers=%d", starts, steers)
	}
}
