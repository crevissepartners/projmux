package runtime

import (
	"context"
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"reflect"
	"testing"
)

type scriptedTurnControl struct {
	calls     []string
	responses []agentcontrol.Response
	err       error
}

func (c *scriptedTurnControl) Bind(ctx context.Context, _ metadata.Registry, _ metadata.Agent) (agentcontrol.Binding, error) {
	return agentcontrol.Binding{Identity: agentcontrol.Identity{ThreadID: "thread", AgentUID: "agent", PaneUID: "pane", Generation: "generation"}}, ctx.Err()
}
func (c *scriptedTurnControl) Call(ctx context.Context, _ agentcontrol.Binding, r agentcontrol.Request) (agentcontrol.Response, error) {
	c.calls = append(c.calls, r.Operation)
	if err := ctx.Err(); err != nil {
		return agentcontrol.Response{}, err
	}
	if c.err != nil {
		return agentcontrol.Response{}, c.err
	}
	out := c.responses[0]
	c.responses = c.responses[1:]
	return out, nil
}
func TestTurnDeliveryFallbackOnlyExplicitInvalidOperation(t *testing.T) {
	ok := agentcontrol.Response{OK: true, ThreadID: "thread", TurnID: "turn"}
	steer := ok
	steer.Acceptance, steer.Delivery = "provider", "unconfirmed"
	for _, test := range []struct {
		name      string
		responses []agentcontrol.Response
		err       error
		calls     []string
		mode      string
		unknown   bool
	}{
		{"idle", []agentcontrol.Response{ok}, nil, []string{"turn-deliver"}, "start", false},
		{"busy", []agentcontrol.Response{steer}, nil, []string{"turn-deliver"}, "steer", false},
		{"legacy idle", []agentcontrol.Response{{Code: "invalid-operation"}, ok}, nil, []string{"turn-deliver", "turn-start"}, "start", false},
		{"legacy busy", []agentcontrol.Response{{Code: "invalid-operation"}, {Code: "turn-in-progress"}, steer}, nil, []string{"turn-deliver", "turn-start", "turn-steer"}, "steer", false},
		{"stale", []agentcontrol.Response{{Code: "stale-binding"}}, nil, []string{"turn-deliver"}, "start", false},
		{"provider timeout", []agentcontrol.Response{{Code: "timeout"}}, nil, []string{"turn-deliver"}, "start", true},
		{"transport loss", nil, errors.New("lost after write"), []string{"turn-deliver"}, "start", true},
		{"malformed steer", []agentcontrol.Response{{OK: true, ThreadID: "thread", TurnID: "turn", Acceptance: "provider", Delivery: "confirmed"}}, nil, []string{"turn-deliver"}, "steer", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := &scriptedTurnControl{responses: test.responses, err: test.err}
			a := metadata.Agent{}
			a.Spec.Provider = "codex"
			a.Metadata.UID = "agent"
			records := 0
			s := TurnService{Control: control, RecordStart: func(context.Context, agentcontrol.Binding, agentcontrol.Response) error { records++; return nil }}
			out, err := s.Execute(context.Background(), metadata.Registry{}, a, TurnRequest{Action: "deliver", Text: "-- byte faithful"})
			if !reflect.DeepEqual(control.calls, test.calls) || out.Mode != test.mode || out.OutcomeUnknown != test.unknown {
				t.Fatalf("out=%+v err=%v calls=%v", out, err, control.calls)
			}
			if out.Accepted && err == nil && out.Mode == "start" && records != 1 {
				t.Fatal("accepted start not recorded")
			}
			if (test.unknown || test.name == "stale") && err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
}
func TestTurnContextAndPostEffectReceipt(t *testing.T) {
	a := metadata.Agent{}
	a.Spec.Provider = "codex"
	control := &scriptedTurnControl{responses: []agentcontrol.Response{{OK: true, ThreadID: "thread", TurnID: "turn"}}}
	s := TurnService{Control: control, RecordStart: func(context.Context, agentcontrol.Binding, agentcontrol.Response) error {
		return errors.New("registry failed")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Execute(ctx, metadata.Registry{}, a, TurnRequest{Action: "start", Text: "text"}); !errors.Is(err, context.Canceled) || len(control.calls) != 0 {
		t.Fatalf("cancel %v %v", err, control.calls)
	}
	out, err := s.Execute(context.Background(), metadata.Registry{}, a, TurnRequest{Action: "start", Text: "text"})
	if err == nil || !out.Accepted || out.ThreadID != "thread" || out.TurnID != "turn" || len(control.calls) != 1 {
		t.Fatalf("receipt lost %+v %v", out, err)
	}
}
