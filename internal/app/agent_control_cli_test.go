package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

type staticAgentControlBinding struct {
	live     agentControlLive
	observed bool
	err      error
}

func (b *staticAgentControlBinding) Live(context.Context, string) (agentControlLive, bool, error) {
	return b.live, b.observed, b.err
}

type scriptedAgentControlPicker struct {
	answers  []intpicker.Result
	rendered []intpicker.Options
}

func phase6CLIIdentity() codexLifecycleIdentity {
	return codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
}

func (p *scriptedAgentControlPicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.rendered = append(p.rendered, options)
	if len(p.answers) == 0 {
		return intpicker.Result{Closed: true}, nil
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer, nil
}

func exactControlCLICommand(t *testing.T) (*agentCommand, *fakeResourceStore, *staticAgentControlBinding) {
	t.Helper()
	store := newFakeResourceStore(t)
	pane, _ := store.registry.Pane("pan-alpha-codex")
	agent, _ := store.registry.Agent("agt-alpha-codex")
	pane.Status.Activation = coremetadata.PaneActivation{Generation: "generation-1", RuntimeID: "%7", AgentUID: agent.Metadata.UID}
	agent.Status.Activation = coremetadata.AgentActivation{State: coremetadata.ActivationPending}
	if changed, err := store.mutator().BindCodexActivation(&store.registry, coremetadata.CodexActivationObservation{
		AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: "generation-1", ThreadID: "thread-1", TurnID: "turn-1",
	}); err != nil || !changed {
		t.Fatalf("bind fixture: changed=%t err=%v", changed, err)
	}
	cmd, _, _ := newTestAgentCommand(t, store)
	binding := &staticAgentControlBinding{observed: true, live: agentControlLive{RuntimeID: "%7", PaneUID: pane.Metadata.UID, ThreadID: "thread-1", Authority: codexAuthorityControlPlane, Epoch: "epoch-1"}}
	cmd.controlBinding = binding
	cmd.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: "/tmp/projmux-phase6-cli"}, nil }
	return cmd, store, binding
}

func TestAgentControlCLIExactTurnDispatchAndTextFidelity(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		op   string
		text string
	}{
		{name: "start", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "  exact start  "}, op: agentControlOpStart, text: "  exact start  "},
		{name: "steer", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "\nexact steer\t"}, op: agentControlOpSteer, text: "\nexact steer\t"},
		{name: "interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}, op: agentControlOpInterrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			var calls []agentControlRequest
			cmd.controlCall = func(_ context.Context, stateDir string, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				if stateDir != "/tmp/projmux-phase6-cli" || identity != phase6CLIIdentity() {
					t.Fatalf("transport binding state=%q identity=%+v", stateDir, identity)
				}
				calls = append(calls, request)
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			if _, _, err := runRoute(t, cmd, test.args...); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0].Operation != test.op || calls[0].Text != test.text || calls[0].Identity != phase6CLIIdentity() || calls[0].Epoch != "epoch-1" {
				t.Fatalf("control calls = %+v", calls)
			}
		})
	}
}

func TestAgentApprovalCLIReviewNoopAndFocusOnlyFlows(t *testing.T) {
	pending := agentPendingApproval{RequestID: "7", Kind: codexappserver.ApprovalCommand, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Command: "make test", CWD: "/work", Decisions: []codexappserver.ApprovalDecision{codexappserver.DecisionAccept}}
	for _, test := range []struct {
		name      string
		answer    string
		wantOps   []string
		wantFocus bool
		wantErr   bool
	}{
		{name: "review once", answer: "decision:accept", wantOps: []string{agentControlOpApprovals, agentControlOpReview}},
		{name: "detail noop", answer: settingsNoopValue, wantOps: []string{agentControlOpApprovals}, wantErr: true},
		{name: "focus only", answer: "open", wantOps: []string{agentControlOpApprovals}, wantFocus: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, _, _ := exactControlCLICommand(t)
			picker := &scriptedAgentControlPicker{answers: []intpicker.Result{{Value: test.answer}}}
			cmd.controlPicker = picker
			focus := &recordingArgv{}
			cmd.focus = focus
			var calls []agentControlRequest
			cmd.controlCall = func(_ context.Context, _ string, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
				if identity != phase6CLIIdentity() || request.Identity != identity || request.Epoch != "epoch-1" {
					t.Fatalf("approval binding identity=%+v request=%+v", identity, request)
				}
				calls = append(calls, request)
				if request.Operation == agentControlOpApprovals {
					return agentControlResponse{OK: true, Approvals: []agentPendingApproval{pending}}, nil
				}
				return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
			}
			_, _, err := runRoute(t, cmd, "approval", "review", "uid:agt-alpha-codex", "--request", "7")
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%t", err, test.wantErr)
			}
			var ops []string
			for _, call := range calls {
				ops = append(ops, call.Operation)
			}
			if !reflect.DeepEqual(ops, test.wantOps) {
				t.Fatalf("operations=%v want=%v", ops, test.wantOps)
			}
			if test.name == "review once" && (calls[1].RequestKey != "7" || calls[1].Decision != "accept") {
				t.Fatalf("review request=%+v", calls[1])
			}
			if test.wantFocus {
				want := []string{"pane", "uid:pan-alpha-codex", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main"}
				if len(focus.calls) != 1 || !slices.Equal(focus.calls[0], want) {
					t.Fatalf("focus calls=%v want=%v", focus.calls, want)
				}
			} else if len(focus.calls) != 0 {
				t.Fatalf("unexpected focus=%v", focus.calls)
			}
			if len(picker.rendered) != 1 || len(picker.rendered[0].Items) < 2 {
				t.Fatalf("picker surface=%+v", picker.rendered)
			}
		})
	}
}

func TestAgentControlCLIStaleBindingCallsNoControl(t *testing.T) {
	cmd, _, binding := exactControlCLICommand(t)
	binding.live.Epoch = ""
	calls := 0
	cmd.controlCall = func(context.Context, string, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
		calls++
		return agentControlResponse{}, errors.New("must not be called")
	}
	cmd.focus = &recordingArgv{}
	_, _, err := runRoute(t, cmd, "turn", "interrupt", "uid:agt-alpha-codex")
	wantFocus := "`projmux focus pane uid:pan-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main`"
	if err == nil || calls != 0 || !strings.Contains(err.Error(), wantFocus) || strings.Contains(err.Error(), "send-keys") {
		t.Fatalf("stale binding err=%v calls=%d", err, calls)
	}
}
